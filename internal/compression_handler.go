package internal

import (
	"bufio"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/brotli"
	"github.com/klauspost/compress/gzip"
)

const (
	encodingBrotli = "br"
	encodingGzip   = "gzip"

	// Minimum response size to compress
	minCompressSize = 1024

	// Compression levels
	brotliCompressionLevel = brotli.DefaultCompression
	gzipCompressionLevel   = gzip.DefaultCompression
)

// HeaderNoCompression is the header that can be set to disable compression
// for a specific response. This is used by the compression guard.
const HeaderNoCompression = "X-No-Compression"

// Pool for gzip writers
var gzipWriterPool = sync.Pool{
	New: func() interface{} {
		w, _ := gzip.NewWriterLevel(io.Discard, gzipCompressionLevel)
		return w
	},
}

// Pool for brotli writers
var brotliWriterPool = sync.Pool{
	New: func() interface{} {
		return brotli.NewWriterLevel(io.Discard, brotliCompressionLevel)
	},
}

// CompressionOptions configures the compression handler
type CompressionOptions struct {
	GzipEnabled         bool
	GzipDisableOnAuth   bool
	GzipJitter          int
	BrotliEnabled       bool
	BrotliDisableOnAuth bool
}

func NewCompressionHandler(opts CompressionOptions, next http.Handler) http.Handler {
	handler := &compressionHandler{
		gzipEnabled:   opts.GzipEnabled,
		gzipJitter:    opts.GzipJitter,
		brotliEnabled: opts.BrotliEnabled,
		next:          next,
	}

	// Apply compression guard if either encoding requires it
	if (opts.GzipEnabled && opts.GzipDisableOnAuth) || (opts.BrotliEnabled && opts.BrotliDisableOnAuth) {
		return NewCompressionGuardHandler(handler)
	}

	return handler
}

type compressionHandler struct {
	gzipEnabled   bool
	gzipJitter    int
	brotliEnabled bool
	next          http.Handler
}

func (h *compressionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check what encodings the client accepts
	encoding := selectEncoding(r.Header.Get("Accept-Encoding"), h.brotliEnabled, h.gzipEnabled)
	if encoding == "" {
		h.next.ServeHTTP(w, r)
		return
	}

	cw := &compressResponseWriter{
		ResponseWriter: w,
		encoding:       encoding,
		gzipJitter:     h.gzipJitter,
		minSize:        minCompressSize,
	}
	defer cw.Close()

	h.next.ServeHTTP(cw, r)
}

// selectEncoding parses the Accept-Encoding header and returns the best
// supported encoding. Brotli is preferred over gzip when both are enabled.
func selectEncoding(acceptEncoding string, brotliEnabled, gzipEnabled bool) string {
	if acceptEncoding == "" {
		return ""
	}

	var clientSupportsBrotli, clientSupportsGzip bool
	var brotliQ, gzipQ float64 = 1.0, 1.0

	for _, part := range strings.Split(acceptEncoding, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Parse encoding and optional quality value
		encoding, q := parseEncodingWithQuality(part)

		switch encoding {
		case encodingBrotli:
			clientSupportsBrotli = true
			brotliQ = q
		case encodingGzip:
			clientSupportsGzip = true
			gzipQ = q
		case "*":
			// Wildcard matches any encoding
			if !clientSupportsBrotli {
				clientSupportsBrotli = true
				brotliQ = q
			}
			if !clientSupportsGzip {
				clientSupportsGzip = true
				gzipQ = q
			}
		}
	}

	// Skip encodings with q=0 (explicitly rejected by client)
	if clientSupportsBrotli && brotliQ == 0 {
		clientSupportsBrotli = false
	}
	if clientSupportsGzip && gzipQ == 0 {
		clientSupportsGzip = false
	}

	// Check what's actually available (client supports AND server enabled)
	hasBrotli := clientSupportsBrotli && brotliEnabled
	hasGzip := clientSupportsGzip && gzipEnabled

	// Prefer brotli over gzip when both are available with equal quality
	if hasBrotli && hasGzip {
		if brotliQ >= gzipQ {
			return encodingBrotli
		}
		return encodingGzip
	}

	if hasBrotli {
		return encodingBrotli
	}
	if hasGzip {
		return encodingGzip
	}

	return ""
}

// parseEncodingWithQuality parses an encoding specification like "gzip;q=0.8"
func parseEncodingWithQuality(s string) (encoding string, quality float64) {
	quality = 1.0

	parts := strings.SplitN(s, ";", 2)
	encoding = strings.TrimSpace(parts[0])

	if len(parts) == 2 {
		qPart := strings.TrimSpace(parts[1])
		if strings.HasPrefix(qPart, "q=") {
			if q, err := strconv.ParseFloat(qPart[2:], 64); err == nil {
				quality = q
			}
		}
	}

	return encoding, quality
}

type compressResponseWriter struct {
	http.ResponseWriter
	encoding    string
	gzipJitter  int
	minSize     int
	writer      io.WriteCloser
	wroteHeader bool
	buf         []byte
	statusCode  int
}

func (w *compressResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}

	w.statusCode = statusCode
	// Don't write header yet - wait until we know if we're compressing
}

func (w *compressResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if w.statusCode == 0 {
			w.statusCode = http.StatusOK
		}
	}

	// Check if compression is disabled for this response
	if w.Header().Get(HeaderNoCompression) != "" {
		w.Header().Del(HeaderNoCompression)
		w.flushHeader()
		return w.ResponseWriter.Write(b)
	}

	// If we already have a writer, use it
	if w.writer != nil {
		return w.writer.Write(b)
	}

	// Buffer data until we have enough to decide whether to compress
	w.buf = append(w.buf, b...)

	if len(w.buf) < w.minSize {
		// Not enough data yet to decide
		return len(b), nil
	}

	// We have enough data - decide whether to compress
	if w.shouldCompress() {
		w.startCompression()
	} else {
		w.flushHeader()
	}

	return w.flushBuffer()
}

func (w *compressResponseWriter) shouldCompress() bool {
	// Don't compress if Content-Encoding is already set
	if w.Header().Get("Content-Encoding") != "" {
		return false
	}

	// Don't compress if Content-Type indicates non-compressible content
	contentType := w.Header().Get("Content-Type")
	if contentType != "" && !isCompressibleContentType(contentType) {
		return false
	}

	return true
}

func (w *compressResponseWriter) startCompression() {
	w.Header().Set("Content-Encoding", w.encoding)
	w.Header().Add("Vary", "Accept-Encoding")
	w.Header().Del("Content-Length") // Length will change after compression

	switch w.encoding {
	case encodingBrotli:
		bw := brotliWriterPool.Get().(*brotli.Writer)
		bw.Reset(w.ResponseWriter)
		w.writer = bw
	case encodingGzip:
		gw := gzipWriterPool.Get().(*gzip.Writer)
		gw.Reset(w.ResponseWriter)
		w.writer = &gzipWriterWithJitter{Writer: gw, jitter: w.gzipJitter}
	}

	w.flushHeader()
}

func (w *compressResponseWriter) flushHeader() {
	w.ResponseWriter.WriteHeader(w.statusCode)
}

func (w *compressResponseWriter) flushBuffer() (int, error) {
	if len(w.buf) == 0 {
		return 0, nil
	}

	var n int
	var err error

	if w.writer != nil {
		n, err = w.writer.Write(w.buf)
	} else {
		n, err = w.ResponseWriter.Write(w.buf)
	}

	w.buf = nil
	return n, err
}

func (w *compressResponseWriter) Close() error {
	// If we have buffered data that hasn't been written yet
	if len(w.buf) > 0 {
		if w.shouldCompress() && len(w.buf) >= w.minSize {
			w.startCompression()
		} else {
			w.flushHeader()
		}
		w.flushBuffer()
	} else if !w.wroteHeader {
		// No data written at all - flush empty response
		w.flushHeader()
	}

	if w.writer != nil {
		err := w.writer.Close()

		// Return writers to pools
		switch w.encoding {
		case encodingBrotli:
			if bw, ok := w.writer.(*brotli.Writer); ok {
				brotliWriterPool.Put(bw)
			}
		case encodingGzip:
			if gwj, ok := w.writer.(*gzipWriterWithJitter); ok {
				gzipWriterPool.Put(gwj.Writer)
			}
		}

		return err
	}

	return nil
}

// Flush implements http.Flusher
func (w *compressResponseWriter) Flush() {
	// Flush any buffered data first
	if len(w.buf) > 0 {
		if w.shouldCompress() {
			w.startCompression()
		} else {
			w.flushHeader()
		}
		w.flushBuffer()
	}

	if flusher, ok := w.writer.(interface{ Flush() error }); ok {
		flusher.Flush()
	}

	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker
func (w *compressResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// gzipWriterWithJitter wraps a gzip.Writer to add random jitter for BREACH mitigation
type gzipWriterWithJitter struct {
	*gzip.Writer
	jitter      int
	jitterAdded bool
}

func (w *gzipWriterWithJitter) Close() error {
	// Add jitter before closing
	if w.jitter > 0 && !w.jitterAdded {
		w.jitterAdded = true
		jitterSize := rand.Intn(w.jitter + 1)
		if jitterSize > 0 {
			jitterBytes := make([]byte, jitterSize)
			w.Writer.Write(jitterBytes)
		}
	}
	return w.Writer.Close()
}

// isCompressibleContentType returns true if the content type should be compressed
func isCompressibleContentType(contentType string) bool {
	// Extract the media type without parameters
	mediaType := strings.SplitN(contentType, ";", 2)[0]
	mediaType = strings.TrimSpace(strings.ToLower(mediaType))

	// Compress text-based formats
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}

	// Compress common web content types
	compressibleTypes := map[string]bool{
		"application/json":                  true,
		"application/javascript":            true,
		"application/x-javascript":          true,
		"application/xml":                   true,
		"application/xhtml+xml":             true,
		"application/rss+xml":               true,
		"application/atom+xml":              true,
		"application/x-font-ttf":            true,
		"application/vnd.ms-fontobject":     true,
		"application/x-web-app-manifest+json": true,
		"font/opentype":                     true,
		"font/ttf":                          true,
		"font/eot":                          true,
		"image/svg+xml":                     true,
		"image/x-icon":                      true,
		"image/vnd.microsoft.icon":          true,
	}

	return compressibleTypes[mediaType]
}
