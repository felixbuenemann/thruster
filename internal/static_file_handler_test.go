package internal

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticFileHandler_serves_static_file(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/hello.txt", nil)
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, string(fixtureContent("hello.txt")), w.Body.String())
}

func TestStaticFileHandler_serves_gzip_precompressed(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/hello.txt", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Header().Get("Vary"), "Accept-Encoding")

	// Decompress and verify content
	reader, err := gzip.NewReader(w.Body)
	require.NoError(t, err)
	defer reader.Close()
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, string(fixtureContent("hello.txt")), string(body))
}

func TestStaticFileHandler_serves_brotli_precompressed(t *testing.T) {
	// Create a temporary directory with a .br file
	tempDir := t.TempDir()
	content := "Hello, World! This is a test file.\n"

	// Create original file
	origPath := filepath.Join(tempDir, "test.txt")
	require.NoError(t, os.WriteFile(origPath, []byte(content), 0644))

	// Create a .br file (we don't need valid brotli content to test the handler logic)
	brContent := []byte("fake-brotli-content")
	brPath := filepath.Join(tempDir, "test.txt.br")
	require.NoError(t, os.WriteFile(brPath, brContent, 0644))

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	h := NewStaticFileHandler(tempDir, upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test.txt", nil)
	r.Header.Set("Accept-Encoding", "br, gzip")
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "br", w.Header().Get("Content-Encoding"))
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Header().Get("Vary"), "Accept-Encoding")
	assert.Equal(t, brContent, w.Body.Bytes())
}

func TestStaticFileHandler_prefers_brotli_over_gzip(t *testing.T) {
	// Create a temporary directory with both .gz and .br files
	tempDir := t.TempDir()
	content := "Test content for compression preference\n"

	// Create original file
	origPath := filepath.Join(tempDir, "test.txt")
	require.NoError(t, os.WriteFile(origPath, []byte(content), 0644))

	// Create gzip file
	gzPath := filepath.Join(tempDir, "test.txt.gz")
	var gzBuf bytes.Buffer
	gzWriter := gzip.NewWriter(&gzBuf)
	_, err := gzWriter.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, gzWriter.Close())
	require.NoError(t, os.WriteFile(gzPath, gzBuf.Bytes(), 0644))

	// Create brotli file (mock content is fine for testing preference)
	brContent := []byte("brotli-content")
	brPath := filepath.Join(tempDir, "test.txt.br")
	require.NoError(t, os.WriteFile(brPath, brContent, 0644))

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	h := NewStaticFileHandler(tempDir, upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test.txt", nil)
	r.Header.Set("Accept-Encoding", "gzip, br")
	h.ServeHTTP(w, r)

	// Should prefer brotli over gzip
	assert.Equal(t, "br", w.Header().Get("Content-Encoding"))
	assert.Equal(t, brContent, w.Body.Bytes())
}

func TestStaticFileHandler_falls_back_to_original_when_no_precompressed(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	// Use a file that has no precompressed versions
	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/image.jpg", nil)
	r.Header.Set("Accept-Encoding", "gzip, br")
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", w.Header().Get("Content-Encoding"))
	assert.Equal(t, "image/jpeg", w.Header().Get("Content-Type"))
	assert.Equal(t, fixtureContent("image.jpg"), w.Body.Bytes())
}

func TestStaticFileHandler_falls_back_to_original_when_encoding_not_accepted(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/hello.txt", nil)
	// No Accept-Encoding header
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", w.Header().Get("Content-Encoding"))
	assert.Equal(t, string(fixtureContent("hello.txt")), w.Body.String())
}

func TestStaticFileHandler_passes_through_when_file_not_found(t *testing.T) {
	upstreamCalled := false
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/nonexistent.txt", nil)
	h.ServeHTTP(w, r)

	assert.True(t, upstreamCalled)
	assert.Equal(t, "upstream response", w.Body.String())
}

func TestStaticFileHandler_passes_through_for_root_path(t *testing.T) {
	upstreamCalled := false
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream response"))
	})

	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(w, r)

	assert.True(t, upstreamCalled)
}

func TestStaticFileHandler_passes_through_for_non_GET_methods(t *testing.T) {
	upstreamCalled := false
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := NewStaticFileHandler("fixtures", upstream)

	methods := []string{"POST", "PUT", "DELETE", "PATCH"}
	for _, method := range methods {
		upstreamCalled = false
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/hello.txt", nil)
		h.ServeHTTP(w, r)
		assert.True(t, upstreamCalled, "Expected upstream to be called for method %s", method)
	}
}

func TestStaticFileHandler_prevents_directory_traversal(t *testing.T) {
	upstreamCalled := false
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/../config.go", nil)
	h.ServeHTTP(w, r)

	// Should pass through to upstream (file not found within fixtures dir)
	assert.True(t, upstreamCalled)
}

func TestStaticFileHandler_handles_HEAD_requests(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("HEAD", "/hello.txt", nil)
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	// HEAD should not include body
	assert.Empty(t, w.Body.String())
}

func TestStaticFileHandler_serves_gzip_only_when_accepted(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	h := NewStaticFileHandler("fixtures", upstream)

	// Request with only br accepted, but only .gz exists
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/hello.txt", nil)
	r.Header.Set("Accept-Encoding", "br") // only brotli, no gzip
	h.ServeHTTP(w, r)

	// Should fall back to original since we don't have .br and client doesn't accept gzip
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", w.Header().Get("Content-Encoding"))
	assert.Equal(t, string(fixtureContent("hello.txt")), w.Body.String())
}

func TestStaticFileHandler_handles_weighted_accept_encoding(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach upstream for static file")
	})

	h := NewStaticFileHandler("fixtures", upstream)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/hello.txt", nil)
	r.Header.Set("Accept-Encoding", "gzip;q=0.8, deflate;q=0.6")
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
}
