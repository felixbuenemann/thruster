package internal

import (
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type StaticFileHandler struct {
	path string
	next http.Handler
}

func NewStaticFileHandler(path string, next http.Handler) *StaticFileHandler {
	return &StaticFileHandler{
		path: path,
		next: next,
	}
}

func (h *StaticFileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only handle GET and HEAD requests
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.next.ServeHTTP(w, r)
		return
	}

	// Clean the URL path to prevent directory traversal
	urlPath := path.Clean(r.URL.Path)
	if urlPath == "/" {
		h.next.ServeHTTP(w, r)
		return
	}

	// Build the file path
	filePath := filepath.Join(h.path, urlPath)

	// Ensure the resolved path is within the static directory (prevent directory traversal)
	absStaticPath, err := filepath.Abs(h.path)
	if err != nil {
		h.next.ServeHTTP(w, r)
		return
	}
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		h.next.ServeHTTP(w, r)
		return
	}
	if !strings.HasPrefix(absFilePath, absStaticPath) {
		h.next.ServeHTTP(w, r)
		return
	}

	// Check if the original file exists
	originalInfo, err := os.Stat(filePath)
	if err != nil || originalInfo.IsDir() {
		h.next.ServeHTTP(w, r)
		return
	}

	// Try to serve a precompressed version
	if h.servePrecompressed(w, r, filePath) {
		return
	}

	// Serve the original file
	slog.Debug("Static file serving", "path", filePath)
	http.ServeFile(w, r, filePath)
}

// servePrecompressed attempts to serve a precompressed version of the file.
// Returns true if a precompressed file was served, false otherwise.
func (h *StaticFileHandler) servePrecompressed(w http.ResponseWriter, r *http.Request, originalPath string) bool {
	acceptEncoding := r.Header.Get("Accept-Encoding")
	if acceptEncoding == "" {
		return false
	}

	encodings := parseAcceptEncoding(acceptEncoding)

	// Try encodings in order of preference (brotli > gzip)
	type encodingInfo struct {
		name      string
		extension string
	}

	candidates := []encodingInfo{
		{"br", ".br"},
		{"gzip", ".gz"},
	}

	for _, candidate := range candidates {
		if !encodings[candidate.name] {
			continue
		}

		compressedPath := originalPath + candidate.extension
		info, err := os.Stat(compressedPath)
		if err != nil || info.IsDir() {
			continue
		}

		slog.Debug("Static file serving precompressed", "path", compressedPath, "encoding", candidate.name)

		// Set headers before serving
		w.Header().Set("Content-Encoding", candidate.name)
		w.Header().Set("Content-Type", getContentType(originalPath))
		w.Header().Add("Vary", "Accept-Encoding")

		http.ServeFile(w, r, compressedPath)
		return true
	}

	return false
}

// parseAcceptEncoding parses the Accept-Encoding header and returns a map of accepted encodings.
func parseAcceptEncoding(header string) map[string]bool {
	encodings := make(map[string]bool)

	for _, part := range strings.Split(header, ",") {
		// Handle weighted encodings like "gzip;q=0.8"
		encoding := strings.TrimSpace(strings.Split(part, ";")[0])
		if encoding != "" {
			encodings[encoding] = true
		}
	}

	return encodings
}

// getContentType determines the Content-Type based on the original file extension.
func getContentType(filePath string) string {
	ext := filepath.Ext(filePath)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType
}
