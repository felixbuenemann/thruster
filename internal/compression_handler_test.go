package internal

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/brotli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompressionHandler(t *testing.T) {
	largeBody := strings.Repeat("A", 2000)

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, err := w.Write([]byte(largeBody))
		require.NoError(t, err)
	})

	defaultOpts := CompressionOptions{
		GzipEnabled:   true,
		BrotliEnabled: true,
	}

	t.Run("compresses responses with gzip", func(t *testing.T) {
		opts := CompressionOptions{GzipEnabled: true, BrotliEnabled: false}
		handler := NewCompressionHandler(opts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "gzip", rr.Header().Get("Content-Encoding"))

		reader, err := gzip.NewReader(rr.Body)
		require.NoError(t, err)
		defer reader.Close()
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.Equal(t, largeBody, string(body))
	})

	t.Run("compresses responses with brotli", func(t *testing.T) {
		opts := CompressionOptions{GzipEnabled: false, BrotliEnabled: true}
		handler := NewCompressionHandler(opts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "br")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "br", rr.Header().Get("Content-Encoding"))

		reader := brotli.NewReader(rr.Body)
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.Equal(t, largeBody, string(body))
	})

	t.Run("prefers brotli over gzip when both are accepted and enabled", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "br", rr.Header().Get("Content-Encoding"))

		reader := brotli.NewReader(rr.Body)
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.Equal(t, largeBody, string(body))
	})

	t.Run("uses gzip when brotli is disabled", func(t *testing.T) {
		opts := CompressionOptions{GzipEnabled: true, BrotliEnabled: false}
		handler := NewCompressionHandler(opts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "gzip", rr.Header().Get("Content-Encoding"))
	})

	t.Run("uses brotli when gzip is disabled", func(t *testing.T) {
		opts := CompressionOptions{GzipEnabled: false, BrotliEnabled: true}
		handler := NewCompressionHandler(opts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "br", rr.Header().Get("Content-Encoding"))
	})

	t.Run("prefers brotli over gzip regardless of order", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "br, gzip")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "br", rr.Header().Get("Content-Encoding"))
	})

	t.Run("respects quality values - prefers gzip when it has higher quality", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "br;q=0.5, gzip;q=1.0")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "gzip", rr.Header().Get("Content-Encoding"))
	})

	t.Run("respects quality values - prefers brotli when it has higher quality", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip;q=0.8, br;q=1.0")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "br", rr.Header().Get("Content-Encoding"))
	})

	t.Run("handles q=0 (rejected encodings)", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "br;q=0, gzip")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "gzip", rr.Header().Get("Content-Encoding"))
	})

	t.Run("handles wildcard encoding", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "*")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		// Should use brotli as it's preferred
		assert.Equal(t, "br", rr.Header().Get("Content-Encoding"))
	})

	t.Run("does not compress when no encoding is accepted", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		// No Accept-Encoding header
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Empty(t, rr.Header().Get("Content-Encoding"))
		assert.Equal(t, largeBody, rr.Body.String())
	})

	t.Run("does not compress small responses", func(t *testing.T) {
		smallBody := "small"
		smallUpstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(smallBody))
		})

		handler := NewCompressionHandler(defaultOpts, smallUpstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Empty(t, rr.Header().Get("Content-Encoding"))
		assert.Equal(t, smallBody, rr.Body.String())
	})

	t.Run("does not compress already encoded responses", func(t *testing.T) {
		encodedUpstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "identity")
			w.Write([]byte(largeBody))
		})

		handler := NewCompressionHandler(defaultOpts, encodedUpstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Equal(t, "identity", rr.Header().Get("Content-Encoding"))
		assert.Equal(t, largeBody, rr.Body.String())
	})

	t.Run("does not compress non-compressible content types", func(t *testing.T) {
		imageUpstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte(largeBody))
		})

		handler := NewCompressionHandler(defaultOpts, imageUpstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Empty(t, rr.Header().Get("Content-Encoding"))
	})

	t.Run("applies jitter when configured with gzip", func(t *testing.T) {
		opts := CompressionOptions{GzipEnabled: true, GzipJitter: 32, BrotliEnabled: false}
		handler := NewCompressionHandler(opts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		require.Equal(t, "gzip", rr.Header().Get("Content-Encoding"))

		// Verify the response is valid gzip
		reader, err := gzip.NewReader(rr.Body)
		require.NoError(t, err)
		defer reader.Close()
		body, err := io.ReadAll(reader)
		require.NoError(t, err)

		// The decompressed body should contain our original content
		// (jitter adds extra bytes but doesn't corrupt the original data)
		assert.True(t, strings.HasPrefix(string(body), largeBody))
	})

	t.Run("wraps with guard when gzip disableOnAuth is true", func(t *testing.T) {
		opts := CompressionOptions{GzipEnabled: true, GzipDisableOnAuth: true, BrotliEnabled: true, BrotliDisableOnAuth: true}
		handler := NewCompressionHandler(opts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		req.Header.Set("Cookie", "session=secret")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		// Should NOT be compressed due to Cookie header
		assert.Empty(t, rr.Header().Get("Content-Encoding"))
		assert.Equal(t, largeBody, rr.Body.String())
	})

	t.Run("compresses authenticated requests when disableOnAuth is false", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "gzip, br")
		req.Header.Set("Cookie", "session=secret")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		// Should use brotli (preferred)
		assert.Equal(t, "br", rr.Header().Get("Content-Encoding"))
	})

	t.Run("sets Vary header", func(t *testing.T) {
		handler := NewCompressionHandler(defaultOpts, upstream)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", "br")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		assert.Contains(t, rr.Header().Get("Vary"), "Accept-Encoding")
	})
}

func TestSelectEncoding(t *testing.T) {
	tests := []struct {
		name           string
		acceptEncoding string
		brotliEnabled  bool
		gzipEnabled    bool
		expected       string
	}{
		{"empty", "", true, true, ""},
		{"gzip only - both enabled", "gzip", true, true, "gzip"},
		{"br only - both enabled", "br", true, true, "br"},
		{"both - gzip first", "gzip, br", true, true, "br"},
		{"both - br first", "br, gzip", true, true, "br"},
		{"gzip with spaces", "  gzip  ", true, true, "gzip"},
		{"br with quality", "br;q=0.8", true, true, "br"},
		{"gzip higher quality", "br;q=0.5, gzip;q=1.0", true, true, "gzip"},
		{"br higher quality", "gzip;q=0.5, br;q=0.8", true, true, "br"},
		{"equal quality prefers br", "gzip;q=1.0, br;q=1.0", true, true, "br"},
		{"gzip rejected", "gzip;q=0, br", true, true, "br"},
		{"br rejected", "br;q=0, gzip", true, true, "gzip"},
		{"both rejected", "gzip;q=0, br;q=0", true, true, ""},
		{"wildcard", "*", true, true, "br"},
		{"wildcard with quality", "*;q=0.5", true, true, "br"},
		{"deflate only (unsupported)", "deflate", true, true, ""},
		{"identity (unsupported)", "identity", true, true, ""},
		{"complex browser header", "gzip, deflate, br", true, true, "br"},
		{"complex with qualities", "gzip;q=1.0, deflate;q=0.6, br;q=0.8", true, true, "gzip"},
		// Test with encodings disabled
		{"brotli disabled - use gzip", "gzip, br", false, true, "gzip"},
		{"gzip disabled - use brotli", "gzip, br", true, false, "br"},
		{"both disabled", "gzip, br", false, false, ""},
		{"brotli disabled - client only wants br", "br", false, true, ""},
		{"gzip disabled - client only wants gzip", "gzip", true, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := selectEncoding(tt.acceptEncoding, tt.brotliEnabled, tt.gzipEnabled)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsCompressibleContentType(t *testing.T) {
	tests := []struct {
		contentType  string
		compressible bool
	}{
		{"text/plain", true},
		{"text/html", true},
		{"text/css", true},
		{"text/javascript", true},
		{"text/html; charset=utf-8", true},
		{"application/json", true},
		{"application/javascript", true},
		{"application/xml", true},
		{"application/xhtml+xml", true},
		{"image/svg+xml", true},
		{"image/png", false},
		{"image/jpeg", false},
		{"image/gif", false},
		{"image/webp", false},
		{"video/mp4", false},
		{"audio/mpeg", false},
		{"application/octet-stream", false},
		{"application/zip", false},
		{"application/gzip", false},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			result := isCompressibleContentType(tt.contentType)
			assert.Equal(t, tt.compressible, result)
		})
	}
}
