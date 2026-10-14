package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func server(t *testing.T, status int, body string, check func(*http.Request, chatRequest)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if check != nil {
			check(r, req)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCompleteReturnsAnswerAndActualModel(t *testing.T) {
	srv := server(t, 200, `{"model":"google/gemma-free:free","choices":[{"message":{"content":"  Alles ruhig.  "}}]}`,
		func(r *http.Request, req chatRequest) {
			if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
				t.Errorf("path/auth = %s / %s", r.URL.Path, r.Header.Get("Authorization"))
			}
			if req.Model != "openrouter/free" || len(req.Messages) != 2 || req.Messages[0].Role != "system" {
				t.Errorf("request = %+v", req)
			}
		})
	c := &Client{BaseURL: srv.URL, APIKey: "key", Model: "openrouter/free", HTTP: srv.Client()}

	res, err := c.Complete(context.Background(), "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Alles ruhig." || res.Model != "google/gemma-free:free" {
		t.Errorf("result = %+v", res)
	}
}

func TestCompleteErrors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"http error":    {429, `{"error":{"message":"rate limited"}}`, "HTTP 429"},
		"error in body": {200, `{"error":{"message":"no free model available"}}`, "no free model available"},
		"empty answer":  {200, `{"choices":[{"message":{"content":"   "}}]}`, "empty answer"},
		"no choices":    {200, `{"choices":[]}`, "empty answer"},
		"not json":      {200, `<html>`, "decode"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := server(t, tc.status, tc.body, nil)
			c := &Client{BaseURL: srv.URL, APIKey: "key", Model: "m", HTTP: srv.Client()}
			_, err := c.Complete(context.Background(), "s", "u")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCompleteWithoutKey(t *testing.T) {
	c := &Client{BaseURL: "http://unused", Model: "m", HTTP: http.DefaultClient}
	if _, err := c.Complete(context.Background(), "s", "u"); err == nil {
		t.Fatal("want an error without API key")
	}
}
