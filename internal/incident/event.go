// Package incident is MODE=incident (#5, stuttgart-things/homerun-library#135):
// it reads the raw bus (alerts, messages) with its own consumer group and
// correlates what it reads, without a model first:
//
//   - per fingerprint (Alertmanager alerts through omni-pitcher's
//     /pitch/alertmanager carry status=firing|resolved,fingerprint=<fp>), or
//     title+system for everything else, it keeps first seen, last seen,
//     repeats, clusters, namespaces and whether it resolved, in Redis;
//   - a repeat becomes one "[AI] ... still firing since ..." message per
//     cooldown instead of yet another card; a resolution closes the state;
//   - alerts that start within one window and share an alertname, cluster or
//     namespace become one incident summary. One model call per window adds
//     the probable common cause; without a model the deterministic summary
//     goes out alone (fail-open). Numbers never come from the model.
//
// It only reads, correlates and pitches info messages marked AI (level 0 of
// the 2026-10-22 comment in #135). It never acts on a cluster. It never
// reads its own output: every ai-* system is excluded.
package incident

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

// Message is the homerun message as omni-pitcher stores it (the Redis JSON
// document a stream entry points to). Only the fields used here.
type Message struct {
	Title     string `json:"title"`
	Message   string `json:"message"`
	Severity  string `json:"severity"`
	Author    string `json:"author"`
	Timestamp string `json:"timestamp"`
	System    string `json:"system"`
	Tags      string `json:"tags"`
	URL       string `json:"url"`
}

// Event is one stream entry and its message.
type Event struct {
	Stream   string // stream name
	EntryID  string // stream entry id, for XACK
	ObjectID string // the message document's key
	Msg      Message
	// At is the message's timestamp, or the time it was read.
	At time.Time
	// Labels are the key=value tags; Status and Fingerprint come from them.
	Labels      map[string]string
	Status      string // firing, resolved, or "" for a non-alert message
	Fingerprint string
}

// Alert states as omni-pitcher's status tag carries them.
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
)

// NewEvent parses the tags of m. now is used when m has no usable timestamp.
func NewEvent(stream, entryID, objectID string, m Message, now time.Time) Event {
	e := Event{Stream: stream, EntryID: entryID, ObjectID: objectID, Msg: m, At: now, Labels: map[string]string{}}
	if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil && !t.IsZero() && t.Before(now.Add(time.Minute)) {
		e.At = t
	}
	for _, tag := range strings.Split(m.Tags, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(tag), "=")
		if ok && k != "" {
			e.Labels[k] = v
		}
	}
	e.Status = e.Labels["status"]
	if e.Status != StatusFiring && e.Status != StatusResolved {
		e.Status = ""
	}
	e.Fingerprint = e.Labels["fingerprint"]
	return e
}

// IsAlert: the message carries an Alertmanager state.
func (e Event) IsAlert() bool { return e.Status != "" }

// Key is what the state is kept under: the fingerprint, else a hash of the
// system and the normalised title.
func (e Event) Key() string {
	if e.Fingerprint != "" {
		return "fp:" + e.Fingerprint
	}
	h := sha256.Sum256([]byte(e.Msg.System + "\x00" + NormTitle(e.Msg.Title)))
	return "ts:" + hex.EncodeToString(h[:8])
}

// Cluster: the cluster label, else the system (omni-pitcher sets the
// Alertmanager receiver as the system, one per cluster).
func (e Event) Cluster() string {
	if c := e.Labels["cluster"]; c != "" {
		return c
	}
	return e.Msg.System
}

// Namespace: the namespace label, if any.
func (e Event) Namespace() string { return e.Labels["namespace"] }

// withDigit is a word part that contains a digit: a number, a pod or
// replica-set suffix, a hash.
var withDigit = regexp.MustCompile(`[a-z0-9]*[0-9][a-z0-9]*`)

// NormTitle folds case, ids and numbers, so "Pod x-7d9f8c restarted 3 times"
// and "Pod x-5b2a1e restarted 4 times" share a key.
func NormTitle(t string) string {
	t = withDigit.ReplaceAllString(strings.ToLower(t), "#")
	return strings.Join(strings.Fields(t), " ")
}

// Excluded: system matches one of the patterns ("ai-*" is a prefix match,
// anything else exact).
func Excluded(system string, patterns []string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(system, prefix) {
				return true
			}
		} else if system == p {
			return true
		}
	}
	return false
}
