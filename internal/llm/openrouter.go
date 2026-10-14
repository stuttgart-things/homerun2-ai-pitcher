// Package llm talks to OpenRouter's OpenAI-compatible chat completions API.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client calls POST <BaseURL>/chat/completions.
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// Result is the answer plus the model that actually produced it. With the
// router model openrouter/free that differs from the requested one, and the
// summary names it.
type Result struct {
	Text  string
	Model string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

// Complete sends one system and one user message and returns the answer.
func (c *Client) Complete(ctx context.Context, system, user string) (*Result, error) {
	if c.APIKey == "" {
		return nil, errors.New("no OPENROUTER_API_KEY")
	}
	body, err := json.Marshal(chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.2,
		MaxTokens:   600,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	// OpenRouter attribution headers, optional.
	req.Header.Set("HTTP-Referer", "https://github.com/stuttgart-things/homerun2-ai-pitcher")
	req.Header.Set("X-Title", "homerun2-ai-pitcher")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("openrouter: decode: %w", err)
	}
	if cr.Error != nil {
		return nil, fmt.Errorf("openrouter: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 || strings.TrimSpace(cr.Choices[0].Message.Content) == "" {
		return nil, errors.New("openrouter: empty answer")
	}
	model := cr.Model
	if model == "" {
		model = c.Model
	}
	return &Result{Text: strings.TrimSpace(cr.Choices[0].Message.Content), Model: model}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
