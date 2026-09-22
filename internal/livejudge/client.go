package livejudge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
	"unicode/utf8"
)

type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type Client struct {
	Endpoint string
	APIKey   string
	Prompt   string
	HTTP     *http.Client
}

func NewClient(key, prompt string) *Client {
	return &Client{Endpoint: "https://api.typesafe.ai/v1/systemone", APIKey: key, Prompt: prompt,
		HTTP: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}
}

func (c *Client) Evaluate(ctx context.Context, input Input) (result Result, err error) {
	start := time.Now()
	result.StartedAt = start.UTC()
	defer func() { result.Elapsed = time.Since(start) }()
	if c.APIKey == "" {
		return result, errors.New("缺少 JEV_API_KEY")
	}
	questions, err := Questions(c.Prompt)
	if err != nil {
		return result, err
	}
	state, _ := json.Marshal(input)
	// 保留完整当前文本。超过预算明确报错，不截掉可能在末尾的结果。
	if utf8.RuneCount(state) > 24000 {
		return result, errors.New("judge_context_too_large")
	}
	body, err := json.Marshal(struct {
		Model     string              `json:"model"`
		State     Input               `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{Model, input, questions})
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(body)
	result.RequestHash = hex.EncodeToString(sum[:])
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return result, errors.New("jev_invalid_endpoint")
	}
	request.Header.Set("Authorization", "Bearer "+c.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		// 不记录包含请求、URL 或上游回显的错误，防止凭据出现在报告中。
		if errors.Is(err, context.DeadlineExceeded) {
			return result, errors.New("jev_timeout")
		}
		return result, errors.New("jev_transport_error")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("jev_http_%d", response.StatusCode)
	}
	var raw struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"answers"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return result, errors.New("jev_timeout")
		}
		return result, errors.New("jev_response_read_error")
	}
	if len(responseBody) > 65536 {
		return result, errors.New("jev_response_too_large")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if decoder.Decode(&raw) != nil {
		return result, errors.New("jev_invalid_json")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return result, errors.New("jev_trailing_json")
	}
	if raw.Model != Model {
		return result, errors.New("jev_unexpected_model")
	}
	values := make([]float64, 0, 4)
	for _, name := range []string{"related", "notify", "completed", "needs_user_input"} {
		a, ok := raw.Answers[name]
		if !ok || a.Type != "noul" || a.Noul == nil || math.IsNaN(*a.Noul) || math.IsInf(*a.Noul, 0) || *a.Noul < 0 || *a.Noul > 1 {
			return result, errors.New("jev_invalid_answer_" + name)
		}
		values = append(values, *a.Noul)
	}
	result.Probabilities = Probabilities{values[0], values[1], values[2], values[3]}
	result.Model, result.InputTokens, result.OutputTokens = raw.Model, raw.Usage.Input, raw.Usage.Output
	return result, nil
}
