package livejudge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const validResponse = `{"model":"jev-1.13.0","answers":{"related":{"type":"noul","noul":0.9},"notify":{"type":"noul","noul":0.8},"completed":{"type":"noul","noul":0.7},"needs_user_input":{"type":"noul","noul":0.1}},"usage":{"input_tokens":100,"output_tokens":40}}`

func TestClientProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"ok", validResponse, "", 200},
		{"forbidden", "secret-key", "jev_http_403", 403}, {"limit", "secret-key", "jev_http_429", 429}, {"server", "secret-key", "jev_http_503", 503},
		{"missing", `{"model":"jev-1.13.0","answers":{}}`, "jev_invalid_answer_related", 200},
		{"null", strings.Replace(validResponse, `"noul":0.9`, `"noul":null`, 1), "jev_invalid_answer_related", 200},
		{"high", strings.Replace(validResponse, `"noul":0.9`, `"noul":1.1`, 1), "jev_invalid_answer_related", 200},
		{"negative", strings.Replace(validResponse, `"noul":0.9`, `"noul":-0.1`, 1), "jev_invalid_answer_related", 200},
		{"nan", strings.Replace(validResponse, `"noul":0.9`, `"noul":NaN`, 1), "jev_invalid_json", 200},
		{"wrongtype", strings.Replace(validResponse, `"type":"noul"`, `"type":"score"`, 1), "jev_invalid_answer_related", 200},
		{"wrongmodel", strings.Replace(validResponse, Model, "jev-latest", 1), "jev_unexpected_model", 200},
		{"trailing", validResponse + "{}", "jev_trailing_json", 200},
		{"malformed", "secret-key", "jev_invalid_json", 200},
		{"oversized", validResponse + strings.Repeat(" ", 65537), "jev_response_too_large", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret-key" || r.Method != "POST" {
					t.Error("错误请求协议")
				}
				var payload struct {
					Model     string
					State     Input
					Questions map[string]Question
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload.Model != Model || len(payload.Questions) != 4 || len([]rune(payload.State.Current)) < 600 {
					t.Error("模型、并行问题或完整文本错误")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c := NewClient("secret-key", "zh-request-v2")
			c.Endpoint = server.URL
			r, err := c.Evaluate(context.Background(), Input{Current: strings.Repeat("长", 600) + "结果通过"})
			if tc.want == "" {
				if err != nil || r.Probabilities.Related != .9 || r.InputTokens != 100 || r.RequestHash == "" {
					t.Fatalf("%+v %v", r, err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("%v want %s", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "secret-key") {
				t.Fatal("密钥泄漏")
			}
		})
	}
}

func TestTimeoutAndRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		fmt.Fprint(w, validResponse)
	}))
	defer server.Close()
	c := NewClient("key", "zh-request-v2")
	c.Endpoint = server.URL
	c.HTTP.Timeout = 5 * time.Millisecond
	if _, err := c.Evaluate(context.Background(), Input{}); err == nil || err.Error() != "jev_timeout" {
		t.Fatalf("%v", err)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, server.URL, http.StatusFound) }))
	defer redirect.Close()
	c.Endpoint = redirect.URL
	if _, err := c.Evaluate(context.Background(), Input{}); err == nil || err.Error() != "jev_http_302" {
		t.Fatalf("%v", err)
	}
}

func TestTimeoutWhileReadingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(40 * time.Millisecond)
		fmt.Fprint(w, validResponse)
	}))
	defer server.Close()
	c := NewClient("key", "zh-request-v2")
	c.Endpoint = server.URL
	c.HTTP.Timeout = 5 * time.Millisecond
	if _, err := c.Evaluate(context.Background(), Input{}); err == nil || err.Error() != "jev_timeout" {
		t.Fatalf("%v", err)
	}
}
