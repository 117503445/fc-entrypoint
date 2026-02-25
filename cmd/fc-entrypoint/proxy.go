package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

var LOG_BODY = true

// logWriter is a custom writer for real-time process logging
type logWriter struct {
	prefix string
	level  string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	// Remove trailing newline to avoid duplicate line breaks
	line := strings.TrimRight(string(p), "\n")
	if line == "" {
		return len(p), nil
	}

	switch w.level {
	case "info":
		log.Info().Str("output", w.prefix+line).Msg("Process output")
	case "error":
		log.Error().Str("output", w.prefix+line).Msg("Process error")
	default:
		log.Info().Str("output", w.prefix+line).Msg("Process output")
	}

	return len(p), nil
}

func reverseProxyHandler(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// Build target URL
	targetURL := "http://localhost:8000" + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// If logging is enabled, read and print request body
	var reqBodyBytes []byte
	var err error
	if LOG_BODY && r.Body != nil {
		reqBodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("Failed to read request body for logging")
		} else {
			log.Ctx(ctx).Info().Str("request_body", string(reqBodyBytes)).Msg("Forwarding request body")
		}
		r.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))
	}

	// Create new request
	req, err := http.NewRequestWithContext(ctx, r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// Copy request headers
	for key, values := range r.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to forward request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Check if it's a streaming response
	contentType := resp.Header.Get("Content-Type")
	isStreaming := contentType == "text/event-stream" ||
		contentType == "application/x-ndjson" ||
		resp.Header.Get("Transfer-Encoding") == "chunked" ||
		strings.Contains(resp.Header.Get("Cache-Control"), "no-cache")

	// If logging is enabled and not streaming, read and print response body
	var respBodyBytes []byte
	if LOG_BODY && !isStreaming {
		respBodyBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("Failed to read response body for logging")
		} else {
			log.Ctx(ctx).Info().Str("response_body", string(respBodyBytes)).Msg("Received response body")
		}
		resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
	}

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Set status code
	w.WriteHeader(resp.StatusCode)

	// Copy response body
	if isStreaming {
		// For streaming responses, use streaming copy to support real-time data transfer
		log.Ctx(ctx).Debug().Str("content_type", contentType).Msg("Streaming response detected")
		io.Copy(w, resp.Body)
	} else {
		// For normal responses, copy directly
		io.Copy(w, resp.Body)
	}
}