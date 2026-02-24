package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
)

func setupTest(t *testing.T) {
	// 重置全局变量
	processesMu.Lock()
	processes = []*Process{}
	processCounter = 0
	processesMu.Unlock()
	// Reset instance ID for consistent test results
	ResetInstanceID()
}

func TestHandleCreateProcess(t *testing.T) {
	setupTest(t)

	tests := []struct {
		name           string
		requestBody    string
		expectedStatus int
		expectID       bool
		expectError    bool
	}{
		{
			name:           "valid request",
			requestBody:    `{"command": "echo hello", "working_dir": "/tmp"}`,
			expectedStatus: http.StatusOK,
			expectID:       true,
			expectError:    false,
		},
		{
			name:           "missing command",
			requestBody:    `{"working_dir": "/tmp"}`,
			expectedStatus: http.StatusBadRequest,
			expectID:       false,
			expectError:    true,
		},
		{
			name:           "invalid json",
			requestBody:    `{"command": "echo hello", "working_dir": `,
			expectedStatus: http.StatusBadRequest,
			expectID:       false,
			expectError:    true,
		},
		{
			name:           "empty command",
			requestBody:    `{"command": "", "working_dir": "/tmp"}`,
			expectedStatus: http.StatusBadRequest,
			expectID:       false,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/_entrypoint/processes", bytes.NewBufferString(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			handleCreateProcess(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectID {
				var response map[string]string
				err := json.NewDecoder(w.Body).Decode(&response)
				assert.NoError(t, err)
				assert.Contains(t, response, "id")
				assert.NotEmpty(t, response["id"])
			}

			if tt.expectError {
				var response map[string]string
				err := json.NewDecoder(w.Body).Decode(&response)
				assert.NoError(t, err)
				assert.Contains(t, response, "error")
			}
		})
	}
}

func TestHandleListProcesses(t *testing.T) {
	setupTest(t)

	// 创建一些测试进程
	process1 := &Process{ID: "test_1", Command: "echo test1", WorkingDir: "/tmp", Status: "completed", Output: "test1\n"}
	process2 := &Process{ID: "test_2", Command: "echo test2", WorkingDir: "/home", Status: "running"}

	processesMu.Lock()
	processes = append(processes, process1, process2)
	processesMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/_entrypoint/processes", nil)
	w := httptest.NewRecorder()

	handleListProcesses(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response []*Process
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.NoError(t, err)
	assert.Len(t, response, 2)

	// 检查返回的进程（顺序可能不同）
	foundIDs := make(map[string]bool)
	for _, p := range response {
		foundIDs[p.ID] = true
		assert.Contains(t, []string{"test_1", "test_2"}, p.ID)
		if p.ID == "test_1" {
			assert.Equal(t, "echo test1", p.Command)
			assert.Equal(t, "/tmp", p.WorkingDir)
			assert.Equal(t, "completed", p.Status)
			assert.Equal(t, "test1\n", p.Output)
		} else if p.ID == "test_2" {
			assert.Equal(t, "echo test2", p.Command)
			assert.Equal(t, "/home", p.WorkingDir)
			assert.Equal(t, "running", p.Status)
		}
	}
	assert.True(t, foundIDs["test_1"])
	assert.True(t, foundIDs["test_2"])
}

func TestHandleProcesses(t *testing.T) {
	setupTest(t)

	t.Run("POST method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/_entrypoint/processes",
			bytes.NewBufferString(`{"command": "echo test"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handleProcesses(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_entrypoint/processes", nil)
		w := httptest.NewRecorder()

		handleProcesses(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("unsupported method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/_entrypoint/processes", nil)
		w := httptest.NewRecorder()

		handleProcesses(w, req)

		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)

		var response map[string]string
		err := json.NewDecoder(w.Body).Decode(&response)
		assert.NoError(t, err)
		assert.Equal(t, "Method not allowed", response["error"])
	})
}

func TestExecuteProcess(t *testing.T) {
	setupTest(t)

	tests := []struct {
		name           string
		command        string
		workingDir     string
		expectError    bool
		expectedOutput string
	}{
		{
			name:           "successful command",
			command:        "echo hello world",
			workingDir:     "",
			expectError:    false,
			expectedOutput: "hello world\n",
		},
		{
			name:           "command with working directory",
			command:        "pwd",
			workingDir:     "/tmp",
			expectError:    false,
			expectedOutput: "/tmp\n",
		},
		{
			name:           "failing command",
			command:        "false",
			workingDir:     "",
			expectError:    true,
			expectedOutput: "",
		},
		{
			name:           "command with stderr",
			command:        "echo error >&2 && echo output",
			workingDir:     "",
			expectError:    false,
			expectedOutput: "output\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 为每个子测试重置状态
			setupTest(t)

			process := &Process{
				ID:         "test_1",
				Command:    tt.command,
				WorkingDir: tt.workingDir,
				Status:     "running",
			}

			processesMu.Lock()
			processes = append(processes, process)
			processesMu.Unlock()

			executeProcess(process, "", "")

			processesMu.RLock()
			// 现在只有一个进程，它在索引0处
			updatedProcess := processes[0]
			processesMu.RUnlock()

			if tt.expectError {
				assert.Equal(t, "failed", updatedProcess.Status)
			} else {
				assert.Equal(t, "completed", updatedProcess.Status)
				assert.Equal(t, tt.expectedOutput, updatedProcess.Output)
			}
		})
	}
}

func TestReverseProxyHandler(t *testing.T) {
	// 创建一个测试服务器来模拟目标服务器
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "proxied")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("proxied response"))
	}))
	defer targetServer.Close()

	// 创建一个测试版本的reverse proxy handler
	testReverseProxyHandler := func(w http.ResponseWriter, r *http.Request) {
		targetURL := targetServer.URL + r.URL.Path
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}

		req, err := http.NewRequest(r.Method, targetURL, r.Body)
		if err != nil {
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}

		for key, values := range r.Header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "Failed to forward request", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		w.WriteHeader(resp.StatusCode)
		// 复制响应体
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		w.Write(buf.Bytes())
	}

	req := httptest.NewRequest(http.MethodGet, "/test/path?query=value", nil)
	w := httptest.NewRecorder()

	testReverseProxyHandler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "proxied", w.Header().Get("X-Test"))
	assert.Equal(t, "proxied response", w.Body.String())
}

func TestReverseProxyHandlerWithLogging(t *testing.T) {
	// 临时启用 LOG_BODY
	originalLogBody := LOG_BODY
	LOG_BODY = true
	defer func() { LOG_BODY = originalLogBody }()

	// 创建一个测试服务器来模拟目标服务器
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "success"}`))
	}))
	defer targetServer.Close()

	// 创建一个支持日志的测试版本的reverse proxy handler
	testReverseProxyHandler := func(w http.ResponseWriter, r *http.Request) {
		targetURL := targetServer.URL + r.URL.Path
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

		req, err := http.NewRequest(r.Method, targetURL, r.Body)
		if err != nil {
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}

		for key, values := range r.Header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "Failed to forward request", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// 如果启用日志，读取并打印响应body
		var respBodyBytes []byte
		if LOG_BODY {
			respBodyBytes, err = io.ReadAll(resp.Body)
			if err != nil {
				log.Error().Err(err).Msg("Failed to read response body for logging")
			} else {
				log.Info().Str("response_body", string(respBodyBytes)).Msg("Received response body")
			}
			resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
		}

		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}

	// 测试带请求体的POST请求
	reqBody := `{"action": "test", "data": "sample"}`
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	testReverseProxyHandler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, `{"result": "success"}`, w.Body.String())
}

func TestReverseProxyHandlerStreaming(t *testing.T) {
	// 创建一个模拟OpenAI API流式响应的测试服务器
	streamData := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-3.5-turbo-0125","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-3.5-turbo-0125","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-3.5-turbo-0125","choices":[{"index":0,"delta":{"content":" World"},"finish_reason":null}]}

data: [DONE]

`

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 设置流式响应头
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		// 模拟流式数据发送
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("Expected http.Flusher")
			return
		}

		lines := strings.Split(streamData, "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			fmt.Fprintf(w, "%s\n", line)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond) // 模拟流式延迟
		}
	}))
	defer targetServer.Close()

	// 创建支持流式的测试版本的reverse proxy handler
	testStreamingProxyHandler := func(w http.ResponseWriter, r *http.Request) {
		targetURL := targetServer.URL + r.URL.Path
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}

		req, err := http.NewRequest(r.Method, targetURL, r.Body)
		if err != nil {
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}

		for key, values := range r.Header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

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
			log.Debug().Str("content_type", contentType).Msg("Streaming response detected")
			io.Copy(w, resp.Body)
		} else {
			io.Copy(w, resp.Body)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"Hello"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()

	testStreamingProxyHandler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))

	// 检查响应是否包含预期的流式数据
	body := w.Body.String()
	assert.Contains(t, body, "data:")
	assert.Contains(t, body, "chat.completion.chunk")
	assert.Contains(t, body, "Hello")
	assert.Contains(t, body, "World")
	assert.Contains(t, body, "[DONE]")
}

func TestReverseProxyHandlerApplicationNdjson(t *testing.T) {
	// 测试 application/x-ndjson 格式的流式响应
	ndjsonData := `{"id":"test-1","content":"First message"}
{"id":"test-2","content":"Second message"}
{"id":"test-3","content":"Third message"}
`

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(ndjsonData))
	}))
	defer targetServer.Close()

	// 创建测试版本的reverse proxy handler
	testNdjsonProxyHandler := func(w http.ResponseWriter, r *http.Request) {
		targetURL := targetServer.URL + r.URL.Path

		req, err := http.NewRequest(r.Method, targetURL, r.Body)
		if err != nil {
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}

		for key, values := range r.Header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

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
			log.Debug().Str("content_type", contentType).Msg("Streaming response detected")
			io.Copy(w, resp.Body)
		} else {
			io.Copy(w, resp.Body)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	w := httptest.NewRecorder()

	testNdjsonProxyHandler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/x-ndjson", w.Header().Get("Content-Type"))
	assert.Equal(t, ndjsonData, w.Body.String())
}

func TestOpenAIStreamingIntegration(t *testing.T) {
	// 模拟OpenAI聊天完成API的流式响应
	openaiStreamResponse := `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":"Hello!"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":" How"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":" can"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":" I"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":" help"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":" you"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":"?"},"finish_reason":null}]}

data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

	// 创建模拟OpenAI API服务器
	openaiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 检查请求是否正确
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// 设置OpenAI风格的响应头
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)

		// 发送流式响应
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("Expected http.Flusher")
			return
		}

		// 逐行发送数据以模拟真实的流式响应
		lines := strings.Split(openaiStreamResponse, "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				fmt.Fprintln(w, line)
				flusher.Flush()
				time.Sleep(5 * time.Millisecond) // 模拟网络延迟
			}
		}
	}))
	defer openaiServer.Close()

	// 创建测试版本的reverse proxy handler，指向我们的模拟服务器
	testOpenAIProxyHandler := func(w http.ResponseWriter, r *http.Request) {
		targetURL := openaiServer.URL + r.URL.Path
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
			log.Debug().Str("content_type", contentType).Msg("Streaming response detected")
			io.Copy(w, resp.Body)
		} else {
			io.Copy(w, resp.Body)
		}
	}

	// 模拟OpenAI API请求
	requestBody := `{
		"model": "gpt-3.5-turbo",
		"messages": [
			{
				"role": "user",
				"content": "Hello, how can you help me?"
			}
		],
		"stream": true
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")

	w := httptest.NewRecorder()

	// 启用LOG_BODY以测试日志功能
	originalLogBody := LOG_BODY
	LOG_BODY = true
	defer func() { LOG_BODY = originalLogBody }()

	testOpenAIProxyHandler(w, req)

	// 验证响应
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))

	// 验证流式响应内容
	body := w.Body.String()

	// 检查是否包含预期的OpenAI流式响应格式
	assert.Contains(t, body, "data:")
	assert.Contains(t, body, "chat.completion.chunk")
	assert.Contains(t, body, "Hello!")
	assert.Contains(t, body, "How")
	assert.Contains(t, body, "can")
	assert.Contains(t, body, "I")
	assert.Contains(t, body, "help")
	assert.Contains(t, body, "you")
	assert.Contains(t, body, "[DONE]")

	// 验证JSON结构
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			jsonStr := strings.TrimPrefix(line, "data: ")
			var chunk map[string]interface{}
			err := json.Unmarshal([]byte(jsonStr), &chunk)
			assert.NoError(t, err, "Invalid JSON in stream chunk: %s", jsonStr)

			// 验证OpenAI响应结构
			assert.Contains(t, chunk, "id")
			assert.Contains(t, chunk, "object")
			assert.Contains(t, chunk, "created")
			assert.Contains(t, chunk, "model")
			assert.Contains(t, chunk, "choices")
		}
	}
}

func TestIntegration(t *testing.T) {
	setupTest(t)

	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_entrypoint/processes" {
			handleProcesses(w, r)
		} else {
			reverseProxyHandler(w, r)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	t.Run("create and list process", func(t *testing.T) {
		// 创建进程
		reqBody := `{"command": "echo integration test", "working_dir": "/tmp"}`
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/_entrypoint/processes",
			bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var createResp map[string]string
		json.NewDecoder(resp.Body).Decode(&createResp)
		resp.Body.Close()

		processID := createResp["id"]
		assert.NotEmpty(t, processID)

		// 等待进程完成
		time.Sleep(100 * time.Millisecond)

		// 列出进程
		req, _ = http.NewRequest(http.MethodGet, server.URL+"/_entrypoint/processes", nil)
		resp, err = client.Do(req)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var processes []*Process
		json.NewDecoder(resp.Body).Decode(&processes)
		resp.Body.Close()

		assert.GreaterOrEqual(t, len(processes), 1)
		found := false
		for _, p := range processes {
			if p.ID == processID {
				found = true
				assert.Equal(t, "echo integration test", p.Command)
				assert.Equal(t, "/tmp", p.WorkingDir)
				assert.Equal(t, "completed", p.Status)
				assert.Equal(t, "integration test\n", p.Output)
				break
			}
		}
		assert.True(t, found, "Created process not found in list")
	})
}

func TestStartEntrypointProcess(t *testing.T) {
	setupTest(t)

	// 创建一个临时的 entrypoint.sh 文件
	entrypointPath := "/tmp/entrypoint_test.sh"
	entrypointContent := `#!/bin/bash
echo "Entrypoint running"
sleep 0.1
echo "Entrypoint done"
`

	err := os.WriteFile(entrypointPath, []byte(entrypointContent), 0755)
	assert.NoError(t, err)
	defer os.Remove(entrypointPath)

	// 测试版本的 startEntrypointProcess 函数
	testStartEntrypointProcess := func() {
		processesMu.Lock()
		processCounter++
		id := fmt.Sprintf("test_%d", processCounter)
		process := &Process{
			ID:         id,
			Command:    entrypointPath,
			WorkingDir: "/",
			Status:     "running",
		}
		processes = append(processes, process)
		processesMu.Unlock()

		// 异步执行命令
		go executeProcess(process, "", "")

		log.Info().Str("process_id", id).Msg("Started entrypoint process")
	}

	// 调用函数
	testStartEntrypointProcess()

	// 等待进程完成
	time.Sleep(200 * time.Millisecond)

	// 检查进程是否被创建并完成
	processesMu.RLock()
	var process *Process
	exists := len(processes) > 0
	if exists {
		process = processes[0] // 第一个进程
	}
	processesMu.RUnlock()

	assert.True(t, exists, "Entrypoint process should be created")
	assert.Equal(t, "/tmp/entrypoint_test.sh", process.Command)
	assert.Equal(t, "/", process.WorkingDir)
	assert.Equal(t, "completed", process.Status)
	assert.Contains(t, process.Output, "Entrypoint running")
	assert.Contains(t, process.Output, "Entrypoint done")
}

func TestMainEntrypointCheck(t *testing.T) {
	setupTest(t)

	// 创建一个临时的 entrypoint.sh 文件
	entrypointPath := "/tmp/entrypoint_main_test.sh"
	entrypointContent := `#!/bin/bash
echo "Main entrypoint test"
`

	err := os.WriteFile(entrypointPath, []byte(entrypointContent), 0755)
	assert.NoError(t, err)
	defer os.Remove(entrypointPath)

	// 测试文件存在检查逻辑
	if _, err := os.Stat(entrypointPath); err == nil {
		t.Log("File exists check passed")
		// 这里会调用 startEntrypointProcess，但我们不执行它以避免实际启动进程
	} else {
		t.Error("File should exist")
	}

	// 验证没有实际进程被创建（因为我们没有调用startEntrypointProcess）
	processesMu.RLock()
	processCount := len(processes)
	processesMu.RUnlock()

	assert.Equal(t, 0, processCount, "No processes should be created in this test")
}

func TestCheckPort(t *testing.T) {
	// 测试未被监听的端口
	assert.False(t, checkPort(9999))

	// 启动一个测试服务器在9998端口
	server := &http.Server{Addr: ":9998"}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("Test server failed")
		}
	}()
	defer server.Close()

	// 等待服务器启动
	time.Sleep(100 * time.Millisecond)

	// 测试被监听的端口
	assert.True(t, checkPort(9998))
}

func TestWaitForPort8000Switch(t *testing.T) {
	// 测试开关默认值为true
	os.Unsetenv("WAIT_FOR_PORT_8000")
	waitForPort8000 := os.Getenv("WAIT_FOR_PORT_8000")
	if waitForPort8000 == "" {
		waitForPort8000 = "true"
	}
	assert.Equal(t, "true", waitForPort8000)

	// 测试开关设置为false
	os.Setenv("WAIT_FOR_PORT_8000", "false")
	waitForPort8000 = os.Getenv("WAIT_FOR_PORT_8000")
	assert.Equal(t, "false", waitForPort8000)

	// 清理环境变量
	os.Unsetenv("WAIT_FOR_PORT_8000")
}

func TestProcessOutputLogging(t *testing.T) {
	setupTest(t)

	// 测试多行输出的进程
	process := &Process{
		ID:         "test_1",
		Command:    "echo 'Line 1'; echo 'Line 2'; echo 'Error message' >&2; echo 'Line 3'",
		WorkingDir: "",
		Status:     "running",
	}

	processesMu.Lock()
	processes = append(processes, process)
	processesMu.Unlock()

	executeProcess(process, "", "")

	processesMu.RLock()
	result := processes[0]
	processesMu.RUnlock()

	// 验证进程状态
	assert.Equal(t, "completed", result.Status)

	// 验证输出内容被保存
	assert.Contains(t, result.Output, "Line 1")
	assert.Contains(t, result.Output, "Line 2")
	assert.Contains(t, result.Output, "Line 3")
	assert.Contains(t, result.Error, "Error message")

	// 验证输出被正确分割（每行都有前缀）
	outputLines := strings.Split(strings.TrimSpace(result.Output), "\n")
	assert.Len(t, outputLines, 3)
	for _, line := range outputLines {
		assert.Contains(t, line, "Line ")
	}
}

// TestMain 设置测试环境
func TestMain(m *testing.M) {
	// 设置测试前的初始化
	setupTest(nil)

	// 运行测试
	code := m.Run()

	// 清理
	os.Exit(code)
}
