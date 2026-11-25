package incident

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/pitch"
)

// Options of the correlation. Zero values get the defaults of Defaults.
type Options struct {
	// MinOccurrences: the first message about a key is the original card and
	// says it all; from this occurrence on a key is worth an AI message.
	MinOccurrences int
	// Cooldown: at most one "still firing" message per key per Cooldown.
	Cooldown time.Duration
	// StaleAfter: a key not seen for this long starts a new episode, as if it
	// had resolved (plain messages never resolve; a silenced alert may not).
	StaleAfter time.Duration
	// Retention: resolved or stale states are dropped after this long.
	Retention time.Duration

	System   string // ai-incident
	Author   string
	Location *time.Location
}

// Defaults: the second occurrence counts, one message per 6h (Alertmanager
// repeats every 4h on the platform), a new episode after 26h of silence.
func (o Options) Defaults() Options {
	if o.MinOccurrences <= 0 {
		o.MinOccurrences = 2
	}
	if o.Cooldown <= 0 {
		o.Cooldown = 6 * time.Hour
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 26 * time.Hour
	}
	if o.Retention <= 0 {
		o.Retention = 7 * 24 * time.Hour
	}
	if o.System == "" {
		o.System = "ai-incident"
	}
	if o.Author == "" {
		o.Author = "homerun2-ai-pitcher"
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	return o
}

// Correlator decides per event what to keep and what to say.
type Correlator struct {
	Store Store
	Opt   Options
}

// Decision is the outcome of one event: the state to write and the messages
// to pitch. Commit writes the state; Rollback undoes the "emitted" marks when
// the pitch failed, so the next occurrence tries again.
type Decision struct {
	State    *State
	Messages []pitch.Message
	// New: the event opened an episode (it goes into the window).
	New bool

	prevEmitted     int
	prevLastEmitted time.Time
}

// Rollback forgets that the messages were emitted.
func (d *Decision) Rollback() {
	if d.State != nil {
		d.State.Emitted, d.State.LastEmitted = d.prevEmitted, d.prevLastEmitted
	}
}

// Observe updates the key's state with e. now is the processing time (for
// the cooldown); e.At is the event's own time.
func (c *Correlator) Observe(ctx context.Context, e Event, now time.Time) (*Decision, error) {
	o := c.Opt.Defaults()
	key := e.Key()
	st, err := c.Store.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("state %s: %w", key, err)
	}

	if e.Status == StatusResolved {
		if st == nil || st.Resolved {
			return &Decision{}, nil // nothing open: the resolved card says it all
		}
		st.Resolved, st.ResolvedAt, st.LastSeen = true, e.At, e.At
		st.Cards++
		st.add(e)
		d := &Decision{State: st, prevEmitted: st.Emitted, prevLastEmitted: st.LastEmitted}
		// Only close what this mode talked about; otherwise the original
		// resolved card is enough.
		if st.Emitted > 0 {
			st.Emitted++
			st.LastEmitted = now
			d.Messages = append(d.Messages, c.resolvedMessage(st))
		}
		slog.Info("incident resolved", "key", key, "title", st.Title, "cards", st.Cards, "aiMessages", st.Emitted,
			"occurrences", st.Occurrences, "duration", st.ResolvedAt.Sub(st.FirstSeen).Round(time.Minute))
		return d, nil
	}

	fresh := st == nil || st.Resolved || e.At.Sub(st.LastSeen) > o.StaleAfter
	if fresh {
		resolutions := 0
		if st != nil {
			resolutions = st.Resolutions
			if st.Resolved {
				resolutions++
			}
		}
		st = &State{
			Key: key, Title: e.Msg.Title, System: e.Msg.System, Severity: e.Msg.Severity,
			Fingerprint: e.Fingerprint, URL: e.Msg.URL, Alert: e.IsAlert(),
			FirstSeen: e.At, Resolutions: resolutions,
		}
	}
	st.Occurrences++
	st.Cards++
	if e.At.After(st.LastSeen) {
		st.LastSeen = e.At
	}
	if e.Msg.Severity != "" {
		st.Severity = e.Msg.Severity
	}
	st.add(e)
	d := &Decision{State: st, New: fresh, prevEmitted: st.Emitted, prevLastEmitted: st.LastEmitted}

	if st.Occurrences >= o.MinOccurrences && (st.LastEmitted.IsZero() || now.Sub(st.LastEmitted) >= o.Cooldown) {
		all, err := c.Store.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("states: %w", err)
		}
		d.Messages = append(d.Messages, c.ongoingMessage(st, related(st, all)))
		st.Emitted++
		st.LastEmitted = now
	}
	return d, nil
}

// Commit writes the decision's state.
func (c *Correlator) Commit(ctx context.Context, d *Decision) error {
	if d.State == nil {
		return nil
	}
	return c.Store.Put(ctx, d.State)
}

// related: the other open alert states with the same alertname, e.g. the
// same alert from another cluster's Alertmanager.
func related(st *State, all []*State) []*State {
	if !st.Alert {
		return nil
	}
	var out []*State
	for _, s := range all {
		if s.Key != st.Key && s.Alert && !s.Resolved && s.Title == st.Title {
			out = append(out, s)
		}
	}
	return out
}

func (c *Correlator) ongoingMessage(st *State, rel []*State) pitch.Message {
	o := c.Opt.Defaults()
	clusters := slices.Clone(st.Clusters)
	first := st.FirstSeen
	occ := st.Occurrences
	for _, r := range rel {
		for _, cl := range r.Clusters {
			if !slices.Contains(clusters, cl) {
				clusters = append(clusters, cl)
			}
		}
		if r.FirstSeen.Before(first) {
			first = r.FirstSeen
		}
		occ += r.Occurrences
	}
	slices.Sort(clusters)

	verb := "still firing"
	if !st.Alert {
		verb = fmt.Sprintf("seen %d times", st.Occurrences)
	}
	title := fmt.Sprintf("[AI] %s: %s since %s", st.Title, verb, c.fmtTime(first))
	if len(clusters) > 1 {
		title += fmt.Sprintf(", on %d clusters", len(clusters))
	}
	if st.Alert {
		if st.Resolutions == 0 {
			title += ", never resolved"
		} else {
			title += fmt.Sprintf(", resolved %d times before (flapping)", st.Resolutions)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s) on %s\n", st.Title, orDash(st.Severity), strings.Join(clusters, ", "))
	fmt.Fprintf(&b, "First seen %s, last seen %s, %d messages", c.fmtTime(first), c.fmtTime(st.LastSeen), occ)
	if len(rel) > 0 {
		fmt.Fprintf(&b, " from %d alert sources", len(rel)+1)
	}
	b.WriteString(".\n")
	if len(st.Namespaces) > 0 {
		fmt.Fprintf(&b, "Namespaces: %s\n", strings.Join(st.Namespaces, ", "))
	}
	b.WriteString("\nCorrelated by " + keyKind(st) + "; the original messages are delivered unchanged. ")
	fmt.Fprintf(&b, "Next update at the earliest in %s.", o.Cooldown)

	return pitch.Message{
		Title: title, Message: b.String(), Severity: "info", Author: o.Author, System: o.System,
		Tags: c.tags(st, "ongoing"), URL: st.URL,
	}
}

func (c *Correlator) resolvedMessage(st *State) pitch.Message {
	o := c.Opt.Defaults()
	d := st.ResolvedAt.Sub(st.FirstSeen).Round(time.Minute)
	title := fmt.Sprintf("[AI] %s: resolved after %s", st.Title, fmtDuration(d))
	msg := fmt.Sprintf("%s on %s: first seen %s, resolved %s. %d alert messages, %d AI updates including this one.",
		st.Title, strings.Join(st.Clusters, ", "), c.fmtTime(st.FirstSeen), c.fmtTime(st.ResolvedAt), st.Cards, st.Emitted)
	return pitch.Message{
		Title: title, Message: msg, Severity: "info", Author: o.Author, System: o.System,
		Tags: c.tags(st, "resolved"), URL: st.URL,
	}
}

// tags never carry status= or fingerprint=: the notification-catcher would
// take the message for an alert.
func (c *Correlator) tags(st *State, phase string) string {
	tags := []string{"ai", "incident", "phase=" + phase, "key=" + strings.TrimPrefix(strings.TrimPrefix(st.Key, "fp:"), "ts:")}
	return strings.Join(tags, ",")
}

func (c *Correlator) fmtTime(t time.Time) string {
	return t.In(c.Opt.Defaults().Location).Format("2006-01-02 15:04")
}

func keyKind(st *State) string {
	if st.Fingerprint != "" {
		return "Alertmanager fingerprint"
	}
	return "system and title"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// fmtDuration: 3h, 3h20m, 45m, 2d4h.
func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	h := int(d / time.Hour)
	m := int((d - time.Duration(h)*time.Hour) / time.Minute)
	var b strings.Builder
	if days > 0 {
		fmt.Fprintf(&b, "%dd", days)
	}
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 && days == 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	return b.String()
}
