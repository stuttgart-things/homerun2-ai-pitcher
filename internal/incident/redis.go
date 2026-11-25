package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore keeps the states in one hash (<prefix>:state, field = key) and
// the open window in <prefix>:window.
type RedisStore struct {
	Client *redis.Client
	Prefix string // e.g. homerun2-ai-pitcher:incident
}

func (s *RedisStore) stateKey() string  { return s.Prefix + ":state" }
func (s *RedisStore) windowKey() string { return s.Prefix + ":window" }

func (s *RedisStore) Get(ctx context.Context, key string) (*State, error) {
	raw, err := s.Client.HGet(ctx, s.stateKey(), key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, fmt.Errorf("decode state %s: %w", key, err)
	}
	return &st, nil
}

func (s *RedisStore) Put(ctx context.Context, st *State) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.Client.HSet(ctx, s.stateKey(), st.Key, raw).Err()
}

func (s *RedisStore) Delete(ctx context.Context, key string) error {
	return s.Client.HDel(ctx, s.stateKey(), key).Err()
}

func (s *RedisStore) All(ctx context.Context) ([]*State, error) {
	m, err := s.Client.HGetAll(ctx, s.stateKey()).Result()
	if err != nil {
		return nil, err
	}
	var out []*State
	for k, raw := range m {
		var st State
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			slog.Warn("undecodable incident state dropped", "key", k, "error", err)
			_ = s.Delete(ctx, k)
			continue
		}
		out = append(out, &st)
	}
	slices.SortFunc(out, func(a, b *State) int { return a.FirstSeen.Compare(b.FirstSeen) })
	return out, nil
}

func (s *RedisStore) Window(ctx context.Context) (*Window, error) {
	raw, err := s.Client.Get(ctx, s.windowKey()).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var w Window
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return nil, fmt.Errorf("decode window: %w", err)
	}
	return &w, nil
}

func (s *RedisStore) PutWindow(ctx context.Context, w *Window) error {
	if w == nil {
		return s.Client.Del(ctx, s.windowKey()).Err()
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	// A window nobody closes (no consumer running) must not live forever.
	return s.Client.Set(ctx, s.windowKey(), raw, 24*time.Hour).Err()
}

// RedisSource reads the streams with a consumer group of its own. Stream
// entries hold messageID, the key of the message's JSON document, as
// homerun-library's Pitcher.Enqueue writes them.
type RedisSource struct {
	Client   *redis.Client
	Streams  []string
	Group    string
	Consumer string
	// ClaimIdle: entries another consumer of the group read but did not ack
	// for this long are taken over (a ScaledJob run that died).
	ClaimIdle time.Duration
	// Doc reads a message document; default JSON.GET. Replaced in tests.
	Doc func(ctx context.Context, key string) (string, error)

	ready   bool
	backlog bool // own pending entries are read first, after a restart
}

// Setup creates the group on every stream (MKSTREAM, from new entries on)
// and takes over entries idle in dead consumers.
func (s *RedisSource) Setup(ctx context.Context) error {
	for _, st := range s.Streams {
		err := s.Client.XGroupCreateMkStream(ctx, st, s.Group, "$").Err()
		if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
			return fmt.Errorf("create group %s on %s: %w", s.Group, st, err)
		}
		if s.ClaimIdle > 0 {
			start := "0-0"
			for {
				ids, next, err := s.Client.XAutoClaimJustID(ctx, &redis.XAutoClaimArgs{
					Stream: st, Group: s.Group, Consumer: s.Consumer, MinIdle: s.ClaimIdle, Start: start, Count: 100,
				}).Result()
				if err != nil {
					slog.Warn("claiming idle entries failed", "stream", st, "error", err)
					break
				}
				if len(ids) > 0 {
					slog.Info("claimed idle entries", "stream", st, "count", len(ids))
				}
				if next == "0-0" || next == "" {
					break
				}
				start = next
			}
		}
	}
	s.ready, s.backlog = true, true
	return nil
}

// Read returns up to count events, waiting up to block for new ones. Own
// pending entries (a restart) come first. An entry whose document is gone
// comes back as an event with an empty message; the caller acks it.
func (s *RedisSource) Read(ctx context.Context, count int, block time.Duration, now time.Time) ([]Event, error) {
	if !s.ready {
		if err := s.Setup(ctx); err != nil {
			return nil, err
		}
	}
	id := ">"
	if s.backlog {
		id = "0"
	}
	args := &redis.XReadGroupArgs{Group: s.Group, Consumer: s.Consumer, Count: int64(count)}
	args.Streams = append(args.Streams, s.Streams...)
	for range s.Streams {
		args.Streams = append(args.Streams, id)
	}
	// go-redis: Block 0 is BLOCK 0, which waits forever; < 0 sends none.
	args.Block = -1
	if !s.backlog && block > 0 {
		args.Block = block
	}
	res, err := s.Client.XReadGroup(ctx, args).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, st := range res {
		for _, m := range st.Messages {
			out = append(out, s.event(ctx, st.Stream, m, now))
		}
	}
	if s.backlog && len(out) == 0 {
		s.backlog = false
	}
	return out, nil
}

func (s *RedisSource) event(ctx context.Context, stream string, m redis.XMessage, now time.Time) Event {
	objectID, _ := m.Values["messageID"].(string)
	var msg Message
	if objectID != "" {
		raw, err := s.doc(ctx, objectID)
		if err == nil {
			err = json.Unmarshal([]byte(raw), &msg)
		}
		if err != nil {
			slog.Warn("message document not readable", "stream", stream, "entry", m.ID, "messageID", objectID, "error", err)
		}
	}
	return NewEvent(stream, m.ID, objectID, msg, now)
}

func (s *RedisSource) doc(ctx context.Context, key string) (string, error) {
	if s.Doc != nil {
		return s.Doc(ctx, key)
	}
	return s.Client.Do(ctx, "JSON.GET", key).Text()
}

// Ack acknowledges e in its stream.
func (s *RedisSource) Ack(ctx context.Context, e Event) error {
	return s.Client.XAck(ctx, e.Stream, s.Group, e.EntryID).Err()
}
