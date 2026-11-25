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
	"time"
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
	// RequestTimeout caps one request (one model, or one group of
	// ModelsPerRequest models). It is cut to a fair share of the context's
	// remaining time, so the later models of the list still get their turn
	// before the run's deadline (#11). 0 = 30s.
	RequestTimeout time.Duration
	// ModelsPerRequest: how many models of the list go into one request as
	// OpenRouter's server-side fallback (1-3, 0 = 3). With 1, every model
	// has its own RequestTimeout: a free model that hangs costs that much,
	// not the whole budget of its group.
	ModelsPerRequest int
	HTTP             *http.Client
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
	// FinishReason as OpenRouter reported it (stop, length, ...).
	FinishReason string
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
	// OpenRouter takes at most 3 models per request ("'models' array must
	// have 3 items or fewer"); a longer list is tried in groups.
	gs := groups(splitModels(c.Model), c.modelsPerRequest())
	var err error
	for i, group := range gs {
		if ctx.Err() != nil {
			if err == nil {
				err = ctx.Err()
			}
			return nil, fmt.Errorf("openrouter: run deadline reached before models %s: %w", strings.Join(group, ","), err)
		}
		timeout := c.share(ctx, len(gs)-i)
		start := time.Now()
		var res *Result
		res, err = c.completeGroup(ctx, timeout, group, system, user)
		if err == nil {
			return res, nil
		}
		var he *HTTPError
		if errors.As(err, &he) && !he.Retryable() {
			return nil, err // a bad request or key is bad for every group
		}
		slog.Warn("model group failed", "group", i+1, "of", len(gs), "models", strings.Join(group, ","),
			"timeout", timeout, "elapsed", time.Since(start).Round(time.Millisecond), "error", err)
	}
	return nil, err
}

// share is the timeout of the next request: RequestTimeout, or less when
// the context's remaining time split over the requests still to come is
// shorter. Without that, one hanging free model used the whole budget and
// the fallback list never got its turn (#11).
func (c *Client) share(ctx context.Context, left int) time.Duration {
	t := c.RequestTimeout
	if t <= 0 {
		t = 30 * time.Second
	}
	if dl, ok := ctx.Deadline(); ok && left > 0 {
		if s := time.Until(dl) / time.Duration(left); s < t {
			t = max(s, time.Second)
		}
	}
	return t
}

func (c *Client) modelsPerRequest() int {
	if c.ModelsPerRequest >= 1 && c.ModelsPerRequest <= maxModelsPerRequest {
		return c.ModelsPerRequest
	}
	return maxModelsPerRequest
}

const maxModelsPerRequest = 3

func groups(models []string, n int) [][]string {
	var out [][]string
	for len(models) > n {
		out = append(out, models[:n])
		models = models[n:]
	}
	return append(out, models)
}

// HTTPError is a non-200 answer of OpenRouter.
type HTTPError struct {
	Status int
	Text   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("openrouter: HTTP %d: %s", e.Status, e.Text)
}

// Retryable: rate limits, timeouts and server errors may pass on the next
// try; a malformed request (400), a bad key (401/403) or no credit (402)
// will not.
func (e *HTTPError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status == http.StatusRequestTimeout || e.Status >= 500
}

// completeGroup sends one request. Its errors name the models, and wrap
// the cause (%w) so the network error, timeout or HTTP status stays
// visible and testable.
func (c *Client) completeGroup(ctx context.Context, timeout time.Duration, models []string, system, user string) (*Result, error) {
	res, err := c.request(ctx, timeout, models, system, user)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) {
			return nil, err // already "openrouter: HTTP n: ..."
		}
		return nil, fmt.Errorf("openrouter [%s]: %w", strings.Join(models, ","), err)
	}
	return res, nil
}

func (c *Client) request(parent context.Context, timeout time.Duration, models []string, system, user string) (*Result, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// timedOut: this request's own timeout, not the caller's deadline.
	timedOut := func(err error) error {
		if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("no answer within %s: %w", timeout, err)
		}
		return err
	}
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
		return nil, timedOut(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		// OpenRouter answers 200 at once and keeps the body open while the
		// model thinks: a slow model fails here, not before the headers.
		return nil, timedOut(fmt.Errorf("reading the answer (HTTP %d): %w", resp.StatusCode, err))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, Text: errorText(raw)}
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if cr.Error != nil {
		return nil, errors.New(cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("empty answer (model %s, no choices)", cr.Model)
	}
	if strings.TrimSpace(cr.Choices[0].Message.Content) == "" {
		// Seen with free reasoning models: the thinking used the budget or
		// the model returned nothing (finish_reason length / stop / error).
		return nil, fmt.Errorf("empty answer (model %s, finish_reason %q)", cr.Model, cr.Choices[0].FinishReason)
	}
	model := cr.Model
	if model == "" {
		model = c.Model
	}
	if len(models) > 1 && model != models[0] {
		// OpenRouter does not say why; a single-model run shows the error.
		slog.Warn("first model not used, OpenRouter fell back", "group", strings.Join(models, ","), "wanted", models[0], "got", model)
	}
	for _, d := range c.Deny {
		if d != "" && strings.Contains(strings.ToLower(model), strings.ToLower(d)) {
			return nil, fmt.Errorf("answer from denied model %s (matches %q)", model, d)
		}
	}
	res := &Result{Text: strings.TrimSpace(cr.Choices[0].Message.Content), Model: model, FinishReason: cr.Choices[0].FinishReason}
	if c.MinChars > 0 && len([]rune(res.Text)) < c.MinChars {
		return nil, fmt.Errorf("answer from %s too short (%d < %d chars): %q", model, len([]rune(res.Text)), c.MinChars, truncate(res.Text, 60))
	}
	if cr.Choices[0].FinishReason == "length" {
		res.Truncated = true
		res.Text = lastSentence(res.Text)
		if res.Text == "" {
			return nil, errors.New("answer cut off by the token limit before a complete sentence")
		}
	}
	return res, nil
}

// errorText makes OpenRouter's error JSON readable: the upstream message
// (error.metadata.raw) if there is one, else error.message, else the raw
// body. It ends up in the pitched fallback, so no JSON.
func errorText(raw []byte) string {
	var e struct {
		Error struct {
			Message  string `json:"message"`
			Metadata struct {
				Raw          string `json:"raw"`
				ProviderName string `json:"provider_name"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		msg := e.Error.Metadata.Raw
		if msg == "" {
			msg = e.Error.Message
		}
		if msg != "" {
			if p := e.Error.Metadata.ProviderName; p != "" {
				msg += " (" + p + ")"
			}
			return truncate(msg, 200)
		}
	}
	return truncate(string(raw), 200)
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
