package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOpenRouter answers per model: "hang" sends 200 and then nothing (as
// OpenRouter does while a model thinks), "abort" drops the connection,
// anything else answers.
func fakeOpenRouter(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		seen = append(seen, req.Model)
		mu.Unlock()
		switch req.Model {
		case "hang":
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
		case "abort":
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
		default:
			_, _ = w.Write([]byte(`{"model":"` + req.Model + `","choices":[{"finish_reason":"stop","message":{"content":"Ok."}}]}`))
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestCompleteHangingModelCostsItsTimeoutNotTheBudget(t *testing.T) {
	srv, seen := fakeOpenRouter(t)
	c := &Client{BaseURL: srv.URL, APIKey: "key", Model: "hang,abort,good", HTTP: srv.Client(),
		ModelsPerRequest: 1, RequestTimeout: 200 * time.Millisecond}

	start := time.Now()
	res, err := c.Complete(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "good" || res.FinishReason != "stop" {
		t.Errorf("res = %+v", res)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("took %v, want about one RequestTimeout", el)
	}
	if got := strings.Join(seen(), ","); got != "hang,abort,good" {
		t.Errorf("requests = %s", got)
	}
}

func TestCompleteErrorsNameModelAndCause(t *testing.T) {
	srv, _ := fakeOpenRouter(t)
	c := &Client{BaseURL: srv.URL, APIKey: "key", Model: "hang", HTTP: srv.Client(), RequestTimeout: 100 * time.Millisecond}
	_, err := c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "openrouter [hang]: no answer within 100ms") ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("hang: err = %v", err)
	}

	c.Model = "abort"
	_, err = c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "openrouter [abort]:") || strings.Contains(err.Error(), "no answer within") {
		t.Errorf("abort: err = %v", err)
	}
}

func TestCompleteSharesTheDeadlineSoTheFallbackGetsItsTurn(t *testing.T) {
	// RequestTimeout alone (10s) would spend the whole 3s deadline on the
	// first model; the share gives each of the three about 1s.
	srv, seen := fakeOpenRouter(t)
	c := &Client{BaseURL: srv.URL, APIKey: "key", Model: "hang,hang,good", HTTP: srv.Client(),
		ModelsPerRequest: 1, RequestTimeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := c.Complete(ctx, "s", "u")
	if err != nil || res.Model != "good" {
		t.Fatalf("res=%+v err=%v requests=%v", res, err, seen())
	}
}

func TestCompleteStopsAtTheRunDeadline(t *testing.T) {
	srv, seen := fakeOpenRouter(t)
	c := &Client{BaseURL: srv.URL, APIKey: "key", Model: "hang,hang,hang,good", HTTP: srv.Client(),
		ModelsPerRequest: 1, RequestTimeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	// The share has a floor of 1s, so the first request runs into the run
	// deadline; the rest are not sent.
	_, err := c.Complete(ctx, "s", "u")
	if err == nil || !strings.Contains(err.Error(), "run deadline reached") {
		t.Errorf("err = %v", err)
	}
	if n := len(seen()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestDialerReportsEveryAddress(t *testing.T) {
	// The first run in LabDA: IPv6 "network is unreachable" was all the log
	// showed. Now every address is listed, IPv4 first.
	var tried []string
	d := &Dialer{
		lookup: func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("2606:4700::6812:273"), net.ParseIP("104.18.2.115")}, nil
		},
		dial: func(_ context.Context, network, addr string) (net.Conn, error) {
			tried = append(tried, addr)
			if strings.HasPrefix(addr, "[") {
				return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connect: network is unreachable")}
			}
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("i/o timeout")}
		},
	}
	_, err := d.DialContext(context.Background(), "tcp", "openrouter.ai:443")
	want := "dial openrouter.ai:443: all 2 addresses failed: 104.18.2.115: i/o timeout; 2606:4700::6812:273: connect: network is unreachable"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant  %s", err, want)
	}
	if strings.Join(tried, " ") != "104.18.2.115:443 [2606:4700::6812:273]:443" {
		t.Errorf("tried %v, want IPv4 first", tried)
	}

	tried = nil
	d.Network = "tcp4"
	_, _ = d.DialContext(context.Background(), "tcp", "openrouter.ai:443")
	if len(tried) != 1 || strings.HasPrefix(tried[0], "[") {
		t.Errorf("tcp4 tried %v", tried)
	}
}

func TestDialerConnects(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	d := &Dialer{lookup: func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("::1"), net.ParseIP("127.0.0.1")}, nil
	}}
	conn, err := d.DialContext(context.Background(), "tcp", "localhost:"+port)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestNewHTTPClientReachesAServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	c := NewHTTPClient("tcp4")
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}
