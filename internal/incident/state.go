package incident

import (
	"context"
	"slices"
	"sync"
	"time"
)

// State is what is known about one key (fingerprint or title+system).
type State struct {
	Key         string    `json:"key"`
	Title       string    `json:"title"`
	System      string    `json:"system"`
	Severity    string    `json:"severity"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	URL         string    `json:"url,omitempty"`
	Alert       bool      `json:"alert"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	// Occurrences: firing (or plain) messages since FirstSeen; repeats are
	// Occurrences-1.
	Occurrences int       `json:"occurrences"`
	Clusters    []string  `json:"clusters,omitempty"`
	Namespaces  []string  `json:"namespaces,omitempty"`
	Resolved    bool      `json:"resolved"`
	ResolvedAt  time.Time `json:"resolvedAt,omitzero"`
	// Resolutions before this episode: a key that resolves and fires again
	// is flapping.
	Resolutions int `json:"resolutions,omitempty"`
	// What this mode pitched about the key in this episode.
	Emitted     int       `json:"emitted,omitempty"`
	LastEmitted time.Time `json:"lastEmitted,omitzero"`
	// Cards: every message the bus carried for this episode, firing and
	// resolved: the cards the outputs would show without correlation (#135's
	// measure, "Teams cards per real incident").
	Cards int `json:"cards"`
}

// Repeats is Occurrences-1, never below 0.
func (s *State) Repeats() int { return max(s.Occurrences-1, 0) }

func (s *State) add(e Event) {
	if c := e.Cluster(); c != "" && !slices.Contains(s.Clusters, c) {
		s.Clusters = append(s.Clusters, c)
		slices.Sort(s.Clusters)
	}
	if n := e.Namespace(); n != "" && !slices.Contains(s.Namespaces, n) {
		s.Namespaces = append(s.Namespaces, n)
		slices.Sort(s.Namespaces)
	}
}

// Store keeps the states and the open window. Redis in the cluster (so a
// restarted pod or the next ScaledJob run picks them up), memory in tests.
type Store interface {
	Get(ctx context.Context, key string) (*State, error) // nil, nil if unknown
	Put(ctx context.Context, s *State) error
	Delete(ctx context.Context, key string) error
	All(ctx context.Context) ([]*State, error)
	Window(ctx context.Context) (*Window, error)    // nil, nil if none open
	PutWindow(ctx context.Context, w *Window) error // nil deletes
}

// MemStore is a Store in memory.
type MemStore struct {
	mu     sync.Mutex
	states map[string]State
	window *Window
}

func (m *MemStore) Get(_ context.Context, key string) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.states[key]
	if !ok {
		return nil, nil
	}
	c := clone(s)
	return &c, nil
}

func (m *MemStore) Put(_ context.Context, s *State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states == nil {
		m.states = map[string]State{}
	}
	m.states[s.Key] = clone(*s)
	return nil
}

func (m *MemStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, key)
	return nil
}

func (m *MemStore) All(_ context.Context) ([]*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*State
	for _, s := range m.states {
		c := clone(s)
		out = append(out, &c)
	}
	slices.SortFunc(out, func(a, b *State) int { return a.FirstSeen.Compare(b.FirstSeen) })
	return out, nil
}

func (m *MemStore) Window(context.Context) (*Window, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.window == nil {
		return nil, nil
	}
	w := *m.window
	w.Events = slices.Clone(w.Events)
	return &w, nil
}

func (m *MemStore) PutWindow(_ context.Context, w *Window) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w == nil {
		m.window = nil
		return nil
	}
	c := *w
	c.Events = slices.Clone(w.Events)
	m.window = &c
	return nil
}

func clone(s State) State {
	s.Clusters = slices.Clone(s.Clusters)
	s.Namespaces = slices.Clone(s.Namespaces)
	return s
}
