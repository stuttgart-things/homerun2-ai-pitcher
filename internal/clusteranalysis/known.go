package clusteranalysis

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// KnownFormat is the format of the known-findings file
// (stuttgart-things/stuttgart-things#4831).
const KnownFormat = "known-findings/v1"

// KnownFindings are the findings the team has accepted: kept in git with a
// reason, an owner and an expiry date. The analysis marks the findings they
// match as known instead of reporting them as open or new; the archive keeps
// them all. An expired entry matches nothing, so its findings are open again.
type KnownFindings struct {
	Format  string       `yaml:"format,omitempty" json:"format,omitempty"`
	Entries []KnownEntry `yaml:"entries" json:"entries"`
}

// KnownEntry accepts one finding (fingerprint) or every finding a pattern
// matches.
type KnownEntry struct {
	ID          string      `yaml:"id" json:"id"`
	Fingerprint string      `yaml:"fingerprint,omitempty" json:"fingerprint,omitempty"`
	Match       *KnownMatch `yaml:"match,omitempty" json:"match,omitempty"`
	Reason      string      `yaml:"reason" json:"reason"`
	Owner       string      `yaml:"owner" json:"owner"`
	// Expires is a day (YYYY-MM-DD); the entry applies up to and including it.
	Expires string `yaml:"expires" json:"expires"`
	Issue   int    `yaml:"issue,omitempty" json:"issue,omitempty"`

	expires time.Time // the end of the Expires day, UTC
	match   []matcher
}

// KnownMatch is a pattern: every field that is set must match. Values are
// globs where * matches any run of characters (also /) and ? one character.
type KnownMatch struct {
	Source    string `yaml:"source,omitempty" json:"source,omitempty"`
	Target    string `yaml:"target,omitempty" json:"target,omitempty"`
	Kind      string `yaml:"kind,omitempty" json:"kind,omitempty"`
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	Name      string `yaml:"name,omitempty" json:"name,omitempty"`
	// Text matches if one of the finding's error texts matches.
	Text string `yaml:"text,omitempty" json:"text,omitempty"`
}

type matcher struct {
	field string
	re    *regexp.Regexp
}

// glob compiles a glob into an anchored regexp.
func glob(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range p {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile("(?s)" + b.String())
}

// LoadKnownFile reads and validates a known-findings file.
func LoadKnownFile(path string) (*KnownFindings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := ParseKnown(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// ParseKnown parses a known-findings file strictly: unknown fields, an entry
// without id, reason, owner or expires, with both or neither of fingerprint
// and match, or a duplicate id are errors -- a typo must not silently hide
// or unhide findings.
func ParseKnown(data []byte) (*KnownFindings, error) {
	k := &KnownFindings{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(k); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if k.Format != "" && k.Format != KnownFormat {
		return nil, fmt.Errorf("format %q, want %q", k.Format, KnownFormat)
	}
	seen := map[string]bool{}
	var errs []string
	for i := range k.Entries {
		e := &k.Entries[i]
		name := e.ID
		if name == "" {
			name = fmt.Sprintf("entry %d", i+1)
			errs = append(errs, name+": id missing")
		} else if seen[e.ID] {
			errs = append(errs, fmt.Sprintf("id %q twice", e.ID))
		}
		seen[e.ID] = true
		for field, v := range map[string]string{"reason": e.Reason, "owner": e.Owner, "expires": e.Expires} {
			if strings.TrimSpace(v) == "" {
				errs = append(errs, fmt.Sprintf("%s: %s missing", name, field))
			}
		}
		if e.Expires != "" {
			d, err := time.Parse("2006-01-02", e.Expires)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: expires %q is not YYYY-MM-DD", name, e.Expires))
			}
			e.expires = d.AddDate(0, 0, 1)
		}
		switch {
		case e.Fingerprint != "" && e.Match != nil:
			errs = append(errs, name+": fingerprint and match are exclusive")
		case e.Fingerprint == "" && e.Match == nil:
			errs = append(errs, name+": fingerprint or match required")
		case e.Match != nil:
			m := e.Match
			for _, f := range []struct{ field, v string }{
				{"source", m.Source}, {"target", m.Target}, {"kind", m.Kind},
				{"namespace", m.Namespace}, {"name", m.Name}, {"text", m.Text},
			} {
				if f.v != "" {
					e.match = append(e.match, matcher{f.field, glob(f.v)})
				}
			}
			if len(e.match) == 0 {
				errs = append(errs, name+": match without any field")
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return k, nil
}

// Active reports whether the entry still applies at now.
func (e *KnownEntry) Active(now time.Time) bool { return now.Before(e.expires) }

func (e *KnownEntry) matches(f Finding, texts []string) bool {
	if e.Fingerprint != "" {
		return e.Fingerprint == f.Fingerprint
	}
	for _, m := range e.match {
		var ok bool
		switch m.field {
		case "source":
			ok = m.re.MatchString(f.Source)
		case "target":
			ok = m.re.MatchString(f.Cluster)
		case "kind":
			ok = m.re.MatchString(f.Kind)
		case "namespace":
			ok = m.re.MatchString(f.Namespace)
		case "name":
			ok = m.re.MatchString(f.Name)
		case "text":
			for _, t := range texts {
				if m.re.MatchString(t) {
					ok = true
					break
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Lookup returns the first active entry matching the finding (with its full,
// unclipped error texts), nil if none.
func (k *KnownFindings) Lookup(f Finding, texts []string, now time.Time) *KnownEntry {
	if k == nil {
		return nil
	}
	for i := range k.Entries {
		e := &k.Entries[i]
		if e.Active(now) && e.matches(f, texts) {
			return e
		}
	}
	return nil
}

// KnownRef is an entry of the file as the report shows it.
type KnownRef struct {
	ID      string   `json:"id"`
	Reason  string   `json:"reason"`
	Owner   string   `json:"owner"`
	Expires string   `json:"expires"`
	Issue   int      `json:"issue,omitempty"`
	Count   int      `json:"count,omitempty"`   // findings it matched in this run
	Systems []string `json:"systems,omitempty"` // source/target of those findings
}

// KnownState is what the analysis did with the file: the entries that
// matched, those that expired (their findings count as open again) and the
// active ones that matched nothing (to clean up).
type KnownState struct {
	Matched []KnownRef `json:"matched,omitempty"`
	Expired []KnownRef `json:"expired,omitempty"`
	Unused  []KnownRef `json:"unused,omitempty"`
}

func (e *KnownEntry) ref() KnownRef {
	return KnownRef{ID: e.ID, Reason: e.Reason, Owner: e.Owner, Expires: e.Expires, Issue: e.Issue}
}

// knownTracker counts the matches of one run.
type knownTracker struct {
	k       *KnownFindings
	count   map[string]int
	systems map[string]map[string]bool
}

func newKnownTracker(k *KnownFindings) *knownTracker {
	return &knownTracker{k: k, count: map[string]int{}, systems: map[string]map[string]bool{}}
}

func (t *knownTracker) hit(e *KnownEntry, system string) {
	t.count[e.ID]++
	if t.systems[e.ID] == nil {
		t.systems[e.ID] = map[string]bool{}
	}
	t.systems[e.ID][system] = true
}

func (t *knownTracker) state(now time.Time) *KnownState {
	if t.k == nil {
		return nil
	}
	s := &KnownState{}
	for i := range t.k.Entries {
		e := &t.k.Entries[i]
		r := e.ref()
		switch {
		case !e.Active(now):
			s.Expired = append(s.Expired, r)
		case t.count[e.ID] == 0:
			s.Unused = append(s.Unused, r)
		default:
			r.Count = t.count[e.ID]
			for sys := range t.systems[e.ID] {
				r.Systems = append(r.Systems, sys)
			}
			sort.Strings(r.Systems)
			s.Matched = append(s.Matched, r)
		}
	}
	return s
}
