package incident

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisSourceAndStore(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()

	src := &RedisSource{Client: rdb, Streams: []string{"alerts", "messages"}, Group: "g", Consumer: "c1",
		// miniredis has no RedisJSON; the documents are plain strings here.
		Doc: func(ctx context.Context, key string) (string, error) { return rdb.Get(ctx, key).Result() }}
	if err := src.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := src.Setup(ctx); err != nil {
		t.Fatalf("second setup (BUSYGROUP): %v", err)
	}

	doc, _ := json.Marshal(Message{Title: "TargetDown", System: "sthings-app", Severity: "error", Tags: "status=firing,fingerprint=ff"})
	if err := mr.Set("obj-1", string(doc)); err != nil {
		t.Fatal(err)
	}
	if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: "alerts", Values: map[string]any{"messageID": "obj-1"}}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: "messages", Values: map[string]any{"messageID": "expired"}}).Err(); err != nil {
		t.Fatal(err)
	}

	var got []Event
	for range 3 { // the first read is the (empty) backlog
		evs, err := src.Read(ctx, 10, 10*time.Millisecond, t0)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, evs...)
	}
	if len(got) != 2 {
		t.Fatalf("events = %+v", got)
	}
	var alertEv, gone Event
	for _, e := range got {
		if e.Stream == "alerts" {
			alertEv = e
		} else {
			gone = e
		}
	}
	if alertEv.Fingerprint != "ff" || alertEv.Msg.Title != "TargetDown" || alertEv.ObjectID != "obj-1" {
		t.Errorf("alert = %+v", alertEv)
	}
	if gone.Msg.Title != "" || (Filter{}).Keep(gone) {
		t.Errorf("expired document = %+v", gone)
	}
	for _, e := range got {
		if err := src.Ack(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	pend, err := rdb.XPending(ctx, "alerts", "g").Result()
	if err != nil || pend.Count != 0 {
		t.Errorf("pending = %+v, %v", pend, err)
	}

	store := &RedisStore{Client: rdb, Prefix: "p"}
	if st, err := store.Get(ctx, "fp:ff"); st != nil || err != nil {
		t.Errorf("unknown = %v %v", st, err)
	}
	if err := store.Put(ctx, &State{Key: "fp:ff", Title: "TargetDown", Clusters: []string{"a"}, FirstSeen: t0}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Get(ctx, "fp:ff")
	if err != nil || st.Title != "TargetDown" || !st.FirstSeen.Equal(t0) {
		t.Errorf("state = %+v, %v", st, err)
	}
	all, _ := store.All(ctx)
	if len(all) != 1 {
		t.Errorf("all = %d", len(all))
	}
	w := (*Window)(nil).Add(alertEv, t0)
	if err := store.PutWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	if w2, err := store.Window(ctx); err != nil || len(w2.Events) != 1 {
		t.Errorf("window = %+v, %v", w2, err)
	}
	if err := store.PutWindow(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if w2, _ := store.Window(ctx); w2 != nil {
		t.Error("window not deleted")
	}
	if err := store.Delete(ctx, "fp:ff"); err != nil {
		t.Fatal(err)
	}
}

func TestRedisSourceReadsOwnBacklogFirst(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	doc := func(ctx context.Context, key string) (string, error) { return `{"title":"T","system":"s"}`, nil }

	first := &RedisSource{Client: rdb, Streams: []string{"messages"}, Group: "g", Consumer: "c1", Doc: doc}
	if err := first.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	_ = rdb.XAdd(ctx, &redis.XAddArgs{Stream: "messages", Values: map[string]any{"messageID": "o"}}).Err()
	evs, _ := first.Read(ctx, 10, 10*time.Millisecond, t0) // backlog: empty
	evs2, _ := first.Read(ctx, 10, 10*time.Millisecond, t0)
	if len(evs)+len(evs2) != 1 {
		t.Fatalf("read %d+%d", len(evs), len(evs2))
	}
	// Not acked; the same consumer restarts and reads it again.
	again := &RedisSource{Client: rdb, Streams: []string{"messages"}, Group: "g", Consumer: "c1", Doc: doc}
	evs, err := again.Read(ctx, 10, 10*time.Millisecond, t0)
	if err != nil || len(evs) != 1 {
		t.Errorf("backlog after restart = %d, %v", len(evs), err)
	}
}
