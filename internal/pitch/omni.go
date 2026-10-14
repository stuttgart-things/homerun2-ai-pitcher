// Package pitch delivers the summary through omni-pitcher's POST /pitch.
package pitch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Message is the omni-pitcher /pitch payload.
type Message struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Author   string `json:"author"`
	System   string `json:"system"`
	Tags     string `json:"tags"`
	URL      string `json:"url,omitempty"`
}

// Client posts to <URL>/pitch.
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// Pitch sends one message and returns omni-pitcher's objectId.
func (c *Client) Pitch(ctx context.Context, m Message) (string, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/pitch", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("pitch: HTTP %d: %s", resp.StatusCode, raw)
	}
	var r struct {
		ObjectID string `json:"objectId"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.ObjectID, nil
}
