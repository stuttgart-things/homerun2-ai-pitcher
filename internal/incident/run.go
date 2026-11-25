package incident

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/pitch"
)

// Source is where events come from (RedisSource).
type Source interface {
	Read(ctx context.Context, count int, block time.Duration, now time.Time) ([]Event, error)
	Ack(ctx context.Context, e Event) error
}

// Pitcher delivers a message (pitch.Client, or a printer for dry runs).
type Pitcher interface {
	Pitch(ctx context.Context, m pitch.Message) (string, error)
}

// Filter decides which events are correlated at all.
type Filter struct {
	// ExcludeSystems: never read: every ai-* system (this mode's own output
	// and the digests'), scout's digest. Patterns ending in * are prefixes.
	ExcludeSystems []string
	// Severities of plain (non-alert) messages that are tracked. Alerts are
	// always tracked, whatever their severity: a warning that fires for days
	// is exactly the case.
	Severities []string
}

// Keep: e is correlated.
func (f Filter) Keep(e Event) bool {
	if e.Msg.System == "" && e.Msg.Title == "" {
		return false // document gone
	}
	if Excluded(e.Msg.System, f.ExcludeSystems) {
		return false
	}
	return e.IsAlert() || slices.Contains(f.Severities, e.Msg.Severity)
}

// Runner is the consumer loop.
type Runner struct {
	Source     Source
	Correlator *Correlator
	Summarizer *Summarizer
	Pitcher    Pitcher
	Filter     Filter
	Batch      int
	Block      time.Duration
	Now        func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Step reads one batch, correlates and acknowledges it, and closes the
// window when it is due. It returns how many entries it read.
func (r *Runner) Step(ctx context.Context) (int, error) {
	batch := r.Batch
	if batch <= 0 {
		batch = 50
	}
	block := r.Block
	if block <= 0 {
		block = 5 * time.Second
	}
	events, err := r.Source.Read(ctx, batch, block, r.now())
	if err != nil {
		return 0, err
	}
	store := r.Correlator.Store
	for _, e := range events {
		if err := r.handle(ctx, e); err != nil {
			// The store is unreachable: leave the entry pending, it is read
			// again after a restart.
			return len(events), err
		}
		// Fail-open: acknowledged whether or not anything was pitched; a
		// lost AI message is better than a backlog.
		if err := r.Source.Ack(ctx, e); err != nil {
			slog.Warn("ack failed", "stream", e.Stream, "entry", e.EntryID, "error", err)
		}
	}

	w, err := store.Window(ctx)
	if err != nil {
		return len(events), err
	}
	if w.Due(r.now(), r.Summarizer.Opt) {
		slog.Info("incident window closed", "alerts", len(w.Events), "opened", w.Opened)
		for _, m := range r.Summarizer.Summarize(ctx, w) {
			r.pitch(ctx, m)
		}
		if err := store.PutWindow(ctx, nil); err != nil {
			return len(events), err
		}
	}
	return len(events), nil
}

func (r *Runner) handle(ctx context.Context, e Event) error {
	if !r.Filter.Keep(e) {
		return nil
	}
	d, err := r.Correlator.Observe(ctx, e, r.now())
	if err != nil {
		return err
	}
	sent := true
	for _, m := range d.Messages {
		if !r.pitch(ctx, m) {
			sent = false
		}
	}
	if !sent {
		d.Rollback()
	}
	if err := r.Correlator.Commit(ctx, d); err != nil {
		return err
	}
	if d.New && e.IsAlert() {
		store := r.Correlator.Store
		w, err := store.Window(ctx)
		if err != nil {
			return err
		}
		if err := store.PutWindow(ctx, w.Add(e, r.now())); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) pitch(ctx context.Context, m pitch.Message) bool {
	id, err := r.Pitcher.Pitch(ctx, m)
	if err != nil {
		slog.Error("incident pitch failed", "title", m.Title, "error", err)
		return false
	}
	slog.Info("incident pitched", "objectId", id, "title", m.Title)
	return true
}

// Prune drops states that resolved, or were last seen, longer than
// Retention ago.
func (r *Runner) Prune(ctx context.Context) error {
	o := r.Correlator.Opt.Defaults()
	all, err := r.Correlator.Store.All(ctx)
	if err != nil {
		return err
	}
	now := r.now()
	for _, s := range all {
		last := s.LastSeen
		if s.Resolved && s.ResolvedAt.After(last) {
			last = s.ResolvedAt
		}
		if now.Sub(last) > o.Retention {
			if err := r.Correlator.Store.Delete(ctx, s.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// Run loops until ctx ends. With once (a Job, e.g. KEDA's ScaledJob) it
// returns when the streams are drained and no window is open any more.
func (r *Runner) Run(ctx context.Context, once bool) error {
	lastPrune := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		n, err := r.Step(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if once {
				return err
			}
			slog.Error("incident step failed, retrying", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if r.now().Sub(lastPrune) > time.Hour {
			if err := r.Prune(ctx); err != nil {
				slog.Warn("prune failed", "error", err)
			}
			lastPrune = r.now()
		}
		if once && n == 0 {
			w, err := r.Correlator.Store.Window(ctx)
			if err != nil {
				return err
			}
			if w == nil {
				return nil
			}
		}
	}
}
