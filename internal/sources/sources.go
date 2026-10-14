// Package sources reads the inputs of a summary: scout's digest (the numbers)
// and schedule-pitcher's open findings. Both are plain authenticated GETs.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DigestMessage is the message scout would pitch for the window.
type DigestMessage struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// SystemCount is one system's share of a window.
type SystemCount struct {
	System string `json:"system"`
	Count  int64  `json:"count"`
	Alerts int64  `json:"alerts"`
}

// Window is one period of the digest.
type Window struct {
	Start      time.Time        `json:"start"`
	End        time.Time        `json:"end"`
	Total      int64            `json:"total"`
	Severities map[string]int64 `json:"severities"`
	TopSystems []SystemCount    `json:"topSystems"`
}

// Digest is the response of scout's GET /analytics/digest.
type Digest struct {
	Schedule string        `json:"schedule"`
	Message  DigestMessage `json:"message"`
	Current  Window        `json:"current"`
	Previous *Window       `json:"previous,omitempty"`
}

// Finding is the part of a schedule-pitcher finding the summary uses.
type Finding struct {
	Source    string    `json:"source"`
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	Message   string    `json:"message,omitempty"`
	Severity  string    `json:"severity"`
	Status    string    `json:"status"`
	URL       string    `json:"url,omitempty"`
	FirstSeen time.Time `json:"firstSeen"`
	AckBy     string    `json:"ackBy,omitempty"`
	AckNote   string    `json:"ackNote,omitempty"`
}

// Client fetches from scout and schedule-pitcher.
type Client struct {
	HTTP *http.Client
}

// NewClient returns a client with a sane timeout.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// FetchDigest calls GET <scoutURL>/analytics/digest?schedule=<schedule>.
func (c *Client) FetchDigest(ctx context.Context, scoutURL, token, schedule string) (*Digest, error) {
	u := scoutURL + "/analytics/digest?schedule=" + url.QueryEscape(schedule)
	var d Digest
	if err := c.getJSON(ctx, u, token, &d); err != nil {
		return nil, fmt.Errorf("scout digest: %w", err)
	}
	return &d, nil
}

// FetchOpenFindings calls GET <baseURL>/api/findings?status=open: open and
// acknowledged findings, worst first.
func (c *Client) FetchOpenFindings(ctx context.Context, baseURL, token string) ([]Finding, error) {
	var f []Finding
	if err := c.getJSON(ctx, baseURL+"/api/findings?status=open", token, &f); err != nil {
		return nil, fmt.Errorf("schedule-pitcher findings: %w", err)
	}
	return f, nil
}

func (c *Client) getJSON(ctx context.Context, u, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: HTTP %d: %s", u, resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
