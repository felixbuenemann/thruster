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

	// Find the file to serve (checks path, path.html, path/index.html)
	filePath, found := h.findFile(urlPath)
	if !found {
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

// findFile looks for a file matching the URL path.
// It checks in order: path, path.html, path/index.html
// Returns the file path and true if found, empty string and false otherwise.
func (h *StaticFileHandler) findFile(urlPath string) (string, bool) {
	// Ensure the path is within the static directory
	absStaticPath, err := filepath.Abs(h.path)
	if err != nil {
		return "", false
	}

	// Candidates to check, in order of priority (matching Rails behavior)
	candidates := []string{
		urlPath,
		urlPath + ".html",
		path.Join(urlPath, "index.html"),
	}

	for _, candidate := range candidates {
		filePath := filepath.Join(h.path, candidate)

		// Resolve to absolute path and verify it's within static directory
		absFilePath, err := filepath.Abs(filePath)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(absFilePath+string(filepath.Separator), absStaticPath+string(filepath.Separator)) &&
			absFilePath != absStaticPath {
			continue
		}

		// Check if file exists and is not a directory
		info, err := os.Stat(filePath)
		if err != nil || info.IsDir() {
			continue
		}

		return filePath, true
	}

	return "", false
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
