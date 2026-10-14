package llm

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Completer is anything that answers one system + user prompt.
type Completer interface {
	Complete(ctx context.Context, system, user string) (*Result, error)
}

// Retrying retries a Completer: openrouter/free answers from whatever free
// model is available, and those fail (429, 5xx, empty or cut-off answers)
// far more often than paid ones. A missing key is not retried.
type Retrying struct {
	Next     Completer
	Attempts int           // total tries, at least 1
	Delay    time.Duration // before the 2nd try, doubled for each further one
}

// Complete tries up to Attempts times and returns the last error.
func (r *Retrying) Complete(ctx context.Context, system, user string) (*Result, error) {
	attempts := max(r.Attempts, 1)
	delay := r.Delay
	var err error
	for i := 1; i <= attempts; i++ {
		var res *Result
		res, err = r.Next.Complete(ctx, system, user)
		if err == nil {
			return res, nil
		}
		if errors.Is(err, ErrNoAPIKey) || i == attempts {
			break
		}
		slog.Warn("AI call failed, retrying", "attempt", i, "of", attempts, "in", delay, "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return nil, err
}
