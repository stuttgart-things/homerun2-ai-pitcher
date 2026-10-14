package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flaky struct {
	fails int
	calls int
	err   error
}

func (f *flaky) Complete(context.Context, string, string) (*Result, error) {
	f.calls++
	if f.calls <= f.fails {
		if f.err != nil {
			return nil, f.err
		}
		return nil, errors.New("openrouter: HTTP 429")
	}
	return &Result{Text: "ok", Model: "m"}, nil
}

func TestRetryingSucceedsAfterFailures(t *testing.T) {
	f := &flaky{fails: 2}
	r := &Retrying{Next: f, Attempts: 3, Delay: time.Millisecond}
	res, err := r.Complete(context.Background(), "s", "u")
	if err != nil || res.Text != "ok" || f.calls != 3 {
		t.Errorf("res=%v err=%v calls=%d", res, err, f.calls)
	}
}

func TestRetryingGivesUp(t *testing.T) {
	f := &flaky{fails: 5}
	r := &Retrying{Next: f, Attempts: 3, Delay: time.Millisecond}
	if _, err := r.Complete(context.Background(), "s", "u"); err == nil || f.calls != 3 {
		t.Errorf("err=%v calls=%d, want an error after 3 calls", err, f.calls)
	}
}

func TestRetryingSkipsMissingKey(t *testing.T) {
	f := &flaky{fails: 5, err: ErrNoAPIKey}
	r := &Retrying{Next: f, Attempts: 3, Delay: time.Hour}
	if _, err := r.Complete(context.Background(), "s", "u"); !errors.Is(err, ErrNoAPIKey) || f.calls != 1 {
		t.Errorf("err=%v calls=%d, want one call", err, f.calls)
	}
}

func TestRetryingZeroAttemptsMeansOne(t *testing.T) {
	f := &flaky{}
	if _, err := (&Retrying{Next: f}).Complete(context.Background(), "s", "u"); err != nil || f.calls != 1 {
		t.Errorf("err=%v calls=%d", err, f.calls)
	}
}

func TestRetryingSkipsPermanentHTTPErrors(t *testing.T) {
	for status, wantCalls := range map[int]int{400: 1, 401: 1, 402: 1, 403: 1, 429: 3, 500: 3, 503: 3} {
		f := &flaky{fails: 5, err: &HTTPError{Status: status}}
		r := &Retrying{Next: f, Attempts: 3, Delay: time.Millisecond}
		_, _ = r.Complete(context.Background(), "s", "u")
		if f.calls != wantCalls {
			t.Errorf("HTTP %d: %d calls, want %d", status, f.calls, wantCalls)
		}
	}
}
