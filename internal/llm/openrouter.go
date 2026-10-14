// Package llm talks to OpenRouter's OpenAI-compatible chat completions API.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	// Deny: substrings of model ids whose answer counts as a failure.
	// openrouter/free also routes to classifiers (e.g.
	// nvidia/nemotron-3.5-content-safety answered "User Safety: safe").
	Deny []string
	// MinChars: a shorter answer counts as a failure (0 = no minimum).
	MinChars int
	HTTP     *http.Client
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
	Model string `json:"model"`
	// Models is OpenRouter's fallback list: tried in order when the one
	// before fails or is unavailable.
	Models      []string      `json:"models,omitempty"`
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

// ErrNoAPIKey is returned without a request; retrying it is pointless.
var ErrNoAPIKey = errors.New("no OPENROUTER_API_KEY")

// Complete sends one system and one user message and returns the answer.
func (c *Client) Complete(ctx context.Context, system, user string) (*Result, error) {
	if c.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	models := splitModels(c.Model)
	req0 := chatRequest{
		Model: models[0],
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.2,
		MaxTokens:   c.maxTokens(),
		Reasoning:   reasoning{Effort: "low", Exclude: true},
	}
	if len(models) > 1 {
		req0.Models = models
	}
	body, err := json.Marshal(req0)
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
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: empty answer (model %s, no choices)", cr.Model)
	}
	if strings.TrimSpace(cr.Choices[0].Message.Content) == "" {
		// Seen with free reasoning models: the thinking used the budget or
		// the model returned nothing (finish_reason length / stop / error).
		return nil, fmt.Errorf("openrouter: empty answer (model %s, finish_reason %q)", cr.Model, cr.Choices[0].FinishReason)
	}
	model := cr.Model
	if model == "" {
		model = c.Model
	}
	if len(models) > 1 && model != models[0] {
		// OpenRouter does not say why; a single-model run shows the error.
		slog.Warn("first model not used, OpenRouter fell back", "wanted", models[0], "got", model)
	}
	for _, d := range c.Deny {
		if d != "" && strings.Contains(strings.ToLower(model), strings.ToLower(d)) {
			return nil, fmt.Errorf("openrouter: answer from denied model %s (matches %q)", model, d)
		}
	}
	res := &Result{Text: strings.TrimSpace(cr.Choices[0].Message.Content), Model: model}
	if c.MinChars > 0 && len([]rune(res.Text)) < c.MinChars {
		return nil, fmt.Errorf("openrouter: answer from %s too short (%d < %d chars): %q", model, len([]rune(res.Text)), c.MinChars, truncate(res.Text, 60))
	}
	if cr.Choices[0].FinishReason == "length" {
		res.Truncated = true
		res.Text = lastSentence(res.Text)
		if res.Text == "" {
			return nil, errors.New("openrouter: answer cut off by the token limit before a complete sentence")
		}
	}
	return res, nil
}

// splitModels turns "a, b,c" into [a b c]; one model stays one.
func splitModels(m string) []string {
	var out []string
	for _, p := range strings.Split(m, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{m}
	}
	return out
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
