package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

var LOG_BODY = true

// logWriter 是一个自定义的writer，用于实时输出进程日志
type logWriter struct {
	prefix string
	level  string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	// 移除末尾的换行符，避免日志重复换行
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

func reverseProxyHandler(w http.ResponseWriter, r *http.Request) {
	// 构建目标URL
	targetURL := "http://localhost:8000" + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// 如果启用日志，读取并打印请求body
	var reqBodyBytes []byte
	var err error
	if LOG_BODY && r.Body != nil {
		reqBodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			log.Error().Err(err).Msg("Failed to read request body for logging")
		} else {
			log.Info().Str("request_body", string(reqBodyBytes)).Msg("Forwarding request body")
		}
		r.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))
	}

	// 创建新的请求
	req, err := http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// 复制请求头
	for key, values := range r.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to forward request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 检查是否为流式响应
	contentType := resp.Header.Get("Content-Type")
	isStreaming := contentType == "text/event-stream" ||
		contentType == "application/x-ndjson" ||
		resp.Header.Get("Transfer-Encoding") == "chunked" ||
		strings.Contains(resp.Header.Get("Cache-Control"), "no-cache")

	// 如果启用日志且不是流式响应，读取并打印响应body
	var respBodyBytes []byte
	if LOG_BODY && !isStreaming {
		respBodyBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Error().Err(err).Msg("Failed to read response body for logging")
		} else {
			log.Info().Str("response_body", string(respBodyBytes)).Msg("Received response body")
		}
		resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
	}

	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// 设置状态码
	w.WriteHeader(resp.StatusCode)

	// 复制响应体
	if isStreaming {
		// 对于流式响应，使用流式复制以支持实时数据传输
		log.Debug().Str("content_type", contentType).Msg("Streaming response detected")
		io.Copy(w, resp.Body)
	} else {
		// 对于普通响应，直接复制
		io.Copy(w, resp.Body)
	}
}
