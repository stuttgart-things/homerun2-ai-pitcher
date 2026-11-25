package fleetissues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Issue is an open or resolved tracking issue.
type Issue struct {
	Number    int
	Title     string
	Body      string
	URL       string
	Labels    []string
	UpdatedAt time.Time
}

// HasLabel reports whether the issue carries label l.
func (i Issue) HasLabel(l string) bool {
	for _, x := range i.Labels {
		if x == l {
			return true
		}
	}
	return false
}

// Client is the part of the GitHub issues API the sync needs.
type Client interface {
	ListOpen(ctx context.Context, label string) ([]Issue, error)
	Create(ctx context.Context, title, body string, labels []string) (Issue, error)
	Comment(ctx context.Context, number int, body string) error
	SetBody(ctx context.Context, number int, body string) error
	AddLabel(ctx context.Context, number int, label string) error
	RemoveLabel(ctx context.Context, number int, label string) error
	Close(ctx context.Context, number int) error
}

// GitHub is the REST client for one repository.
type GitHub struct {
	Repo  string // owner/name
	Token string
	API   string // default https://api.github.com
	HTTP  *http.Client
}

type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (g *GitHub) do(ctx context.Context, method, path string, in, out any) error {
	api := g.API
	if api == "" {
		api = "https://api.github.com"
	}
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, api+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := g.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func toIssue(i ghIssue) Issue {
	out := Issue{Number: i.Number, Title: i.Title, Body: i.Body, URL: i.HTMLURL, UpdatedAt: i.UpdatedAt}
	for _, l := range i.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

// ListOpen returns the open issues (no pull requests) with the label.
func (g *GitHub) ListOpen(ctx context.Context, label string) ([]Issue, error) {
	var out []Issue
	for page := 1; page <= 10; page++ {
		var batch []ghIssue
		q := url.Values{"state": {"open"}, "labels": {label}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
		if err := g.do(ctx, http.MethodGet, "/repos/"+g.Repo+"/issues?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		for _, i := range batch {
			if i.PullRequest == nil {
				out = append(out, toIssue(i))
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// Create opens an issue.
func (g *GitHub) Create(ctx context.Context, title, body string, labels []string) (Issue, error) {
	var i ghIssue
	err := g.do(ctx, http.MethodPost, "/repos/"+g.Repo+"/issues", map[string]any{"title": title, "body": body, "labels": labels}, &i)
	return toIssue(i), err
}

// Comment adds a comment.
func (g *GitHub) Comment(ctx context.Context, number int, body string) error {
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", g.Repo, number), map[string]any{"body": body}, nil)
}

// SetBody replaces the issue body.
func (g *GitHub) SetBody(ctx context.Context, number int, body string) error {
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", g.Repo, number), map[string]any{"body": body}, nil)
}

// AddLabel adds a label (created by GitHub if missing).
func (g *GitHub) AddLabel(ctx context.Context, number int, label string) error {
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/labels", g.Repo, number), map[string]any{"labels": []string{label}}, nil)
}

// RemoveLabel removes a label.
func (g *GitHub) RemoveLabel(ctx context.Context, number int, label string) error {
	return g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/issues/%d/labels/%s", g.Repo, number, url.PathEscape(label)), nil, nil)
}

// Close closes the issue as completed.
func (g *GitHub) Close(ctx context.Context, number int) error {
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", g.Repo, number), map[string]any{"state": "closed", "state_reason": "completed"}, nil)
}
