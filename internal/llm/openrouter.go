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
	// MaxTokens caps the answer INCLUDING a reasoning model's thinking:
	// openrouter/free often routes to one, and 600 tokens left it ~200
	// characters of answer, cut mid-sentence.
	MaxTokens int
	HTTP      *http.Client
}

// Result is the answer plus the model that actually produced it. With the
// router model openrouter/free that differs from the requested one, and the
// summary names it.
type Result struct {
	Text  string
	Model string
	// Truncated: the model hit the token limit (finish_reason "length").
	// Text is then cut back to its last complete sentence.
	Truncated bool
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Exclude bool   `json:"exclude"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	// Reasoning models think little and keep the thinking out of the
	// answer; models without reasoning ignore it.
	Reasoning reasoning `json:"reasoning"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
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
		MaxTokens:   c.maxTokens(),
		Reasoning:   reasoning{Effort: "low", Exclude: true},
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
	res := &Result{Text: strings.TrimSpace(cr.Choices[0].Message.Content), Model: model}
	if cr.Choices[0].FinishReason == "length" {
		res.Truncated = true
		res.Text = lastSentence(res.Text)
		if res.Text == "" {
			return nil, errors.New("openrouter: answer cut off by the token limit before a complete sentence")
		}
	}
	return res, nil
}

func (c *Client) maxTokens() int {
	if c.MaxTokens > 0 {
		return c.MaxTokens
	}
	return 4000
}

// lastSentence cuts s after its last sentence end (. ! ? followed by space,
// newline or the end), or returns "" if there is none.
func lastSentence(s string) string {
	end := -1
	for i, r := range s {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		next := i + 1
		if next == len(s) || s[next] == ' ' || s[next] == '\n' {
			end = next
		}
	}
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(s[:end])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
