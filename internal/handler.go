package internal

import (
	"log/slog"
	"net/http"
	"net/url"
)

type HandlerOptions struct {
	badGatewayPage                 string
	cache                          Cache
	maxCacheableResponseBody       int
	maxRequestBody                 int
	targetUrl                      *url.URL
	xSendfileEnabled               bool
	gzipCompressionEnabled         bool
	gzipCompressionDisableOnAuth   bool
	gzipCompressionJitter          int
	brotliCompressionEnabled       bool
	brotliCompressionDisableOnAuth bool
	forwardHeaders                 bool
	logRequests                    bool
}

func NewHandler(options HandlerOptions) http.Handler {
	handler := NewProxyHandler(options.targetUrl, options.badGatewayPage, options.forwardHeaders)
	handler = NewCacheHandler(options.cache, options.maxCacheableResponseBody, handler)
	handler = NewSendfileHandler(options.xSendfileEnabled, handler)
	handler = NewRequestStartHandler(handler)

	if options.gzipCompressionEnabled || options.brotliCompressionEnabled {
		handler = NewCompressionHandler(CompressionOptions{
			GzipEnabled:         options.gzipCompressionEnabled,
			GzipDisableOnAuth:   options.gzipCompressionDisableOnAuth,
			GzipJitter:          options.gzipCompressionJitter,
			BrotliEnabled:       options.brotliCompressionEnabled,
			BrotliDisableOnAuth: options.brotliCompressionDisableOnAuth,
		}, handler)
	}

	if options.maxRequestBody > 0 {
		handler = http.MaxBytesHandler(handler, int64(options.maxRequestBody))
	}

	if options.logRequests {
		handler = NewLoggingHandler(slog.Default(), handler)
	}

	return handler
}
