// Package hub runs adapters on their own loops and merges their latest
// results into one snapshot. A slow or broken adapter never blocks the others.
package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/model"
)

type result struct {
	sessions []model.Session
	err      error
	at       time.Time
}

type Hub struct {
	adapters []model.Adapter
	interval time.Duration

	mu      sync.Mutex
	latest  map[string]result
	history map[string]*track
	events  []Event
	updates chan struct{}
}

// Event is a status transition, for the activity feed.
type Event struct {
	At      time.Time
	Session model.Session
	From    model.Status
	To      model.Status
}

const maxEvents = 200

// track is one session's recent status samples.
type track struct {
	samples []model.Status
	changed time.Time
	seen    time.Time
}

const historyLen = 600 // 20 minutes at 2s

func New(interval time.Duration, adapters ...model.Adapter) *Hub {
	return &Hub{
		adapters: adapters,
		interval: interval,
		latest:   map[string]result{},
		history:  map[string]*track{},
		updates:  make(chan struct{}, 1),
	}
}

// Once collects from every adapter concurrently and returns a snapshot.
func (h *Hub) Once(ctx context.Context) model.Snapshot {
	var wg sync.WaitGroup
	for _, a := range h.adapters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.collect(ctx, a)
		}()
	}
	wg.Wait()
	return h.Snapshot()
}

// Run polls each adapter on its own loop until ctx ends. Updates() fires after
// any adapter finishes a pass.
func (h *Hub) Run(ctx context.Context) {
	for _, a := range h.adapters {
		go func() {
			t := time.NewTicker(h.interval)
			defer t.Stop()
			for {
				h.collect(ctx, a)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
}

func (h *Hub) Updates() <-chan struct{} { return h.updates }

func (h *Hub) collect(ctx context.Context, a model.Adapter) {
	var r result
	func() {
		defer func() {
			if p := recover(); p != nil {
				r.err = fmt.Errorf("adapter panic: %v", p)
			}
		}()
		r.sessions, r.err = a.Collect(ctx)
	}()
	r.at = time.Now()
	// Everything past here reaches a terminal or the app: agent text and
	// other machines' snapshots don't get to carry escape sequences there.
	for i := range r.sessions {
		r.sessions[i].Sanitize()
	}
	if r.err != nil {
		r.err = errors.New(model.Clean(r.err.Error()))
	}

	h.mu.Lock()
	// Keep the last good rows if this pass failed outright, so a hiccup
	// doesn't blank the board.
	if prev, ok := h.latest[a.Name()]; ok && r.err != nil && len(r.sessions) == 0 {
		r.sessions = prev.sessions
	}
	h.latest[a.Name()] = r
	if r.err == nil {
		h.record(r.sessions, r.at)
	}
	h.mu.Unlock()

	select {
	case h.updates <- struct{}{}:
	default:
	}
}

func (h *Hub) record(sessions []model.Session, now time.Time) {
	for _, s := range sessions {
		k := s.Key()
		t := h.history[k]
		if t == nil {
			t = &track{changed: now, samples: backfill(s.Activity, now, h.interval)}
			h.history[k] = t
			// Seed the feed with recent transitions we can date.
			if !s.Since.IsZero() && now.Sub(s.Since) < time.Hour {
				h.addEvent(Event{At: s.Since, Session: s, To: s.Status})
			}
		}
		if n := len(t.samples); n > 0 && t.samples[n-1] != s.Status {
			t.changed = now
			h.addEvent(Event{At: now, Session: s, From: t.samples[n-1], To: s.Status})
		}
		t.samples = append(t.samples, s.Status)
		if len(t.samples) > historyLen {
			t.samples = t.samples[len(t.samples)-historyLen:]
		}
		t.seen = now
	}
	// Forget sessions gone for ten minutes.
	for k, t := range h.history {
		if now.Sub(t.seen) > 10*time.Minute {
			delete(h.history, k)
		}
	}
}

// backfill rebuilds recent history from event timestamps: a sample counts as
// working if the agent did something in the 30s before it. Gaps longer than
// that (a long build, a pause) read as idle, which is close enough.
func backfill(activity []int64, now time.Time, every time.Duration) []model.Status {
	if len(activity) == 0 || every <= 0 {
		return nil
	}
	const hold = 30 * time.Second
	first := time.Unix(activity[0], 0)
	n := min(historyLen-1, int(now.Sub(first)/every))
	out := make([]model.Status, 0, n)
	j := 0
	for i := n; i > 0; i-- {
		t := now.Add(-time.Duration(i) * every)
		for j < len(activity) && time.Unix(activity[j], 0).Before(t.Add(-hold)) {
			j++
		}
		st := model.StatusIdle
		if j < len(activity) && !time.Unix(activity[j], 0).After(t) {
			st = model.StatusBusy
		}
		out = append(out, st)
	}
	return out
}

func (h *Hub) addEvent(e Event) {
	h.events = append(h.events, e)
	sort.SliceStable(h.events, func(i, j int) bool { return h.events[i].At.Before(h.events[j].At) })
	if len(h.events) > maxEvents {
		h.events = h.events[len(h.events)-maxEvents:]
	}
}

// Events returns the feed, newest first.
func (h *Hub) Events() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Event, len(h.events))
	for i, e := range h.events {
		out[len(out)-1-i] = e
	}
	return out
}

// Hosts reports each adapter's name and whether its last pass failed.
func (h *Hub) Hosts() map[string]error {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]error{}
	for _, a := range h.adapters {
		if r, ok := h.latest[a.Name()]; ok {
			out[a.Name()] = r.err
		} else {
			out[a.Name()] = errPending
		}
	}
	return out
}

var errPending = fmt.Errorf("connecting…")

func (h *Hub) Snapshot() model.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	snap := model.Snapshot{GeneratedAt: time.Now(), Sessions: []model.Session{}, Errors: []model.AdapterError{}}
	for _, a := range h.adapters {
		r, ok := h.latest[a.Name()]
		if !ok {
			continue
		}
		snap.Sessions = append(snap.Sessions, r.sessions...)
		if r.err != nil {
			snap.Errors = append(snap.Errors, model.AdapterError{Provider: a.Name(), Error: r.err.Error()})
		}
	}
	snap.Sessions = dedupe(snap.Sessions)
	for i := range snap.Sessions {
		s := &snap.Sessions[i]
		if t := h.history[s.Key()]; t != nil {
			s.History = append([]model.Status(nil), t.samples...)
			if s.Since.IsZero() && len(t.samples) > 1 {
				s.Since = t.changed
			}
		}
	}
	sort.SliceStable(snap.Sessions, func(i, j int) bool {
		a, b := snap.Sessions[i], snap.Sessions[j]
		if a.Status.Rank() != b.Status.Rank() {
			return a.Status.Rank() < b.Status.Rank()
		}
		return a.LastSeen().After(b.LastSeen())
	})
	return snap
}

// dedupe drops repeats by provider+session id, then provider+pid.
func dedupe(in []model.Session) []model.Session {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		keys := []string{}
		if s.ID != "" {
			keys = append(keys, s.Host+"|"+s.Provider+"|id|"+s.ID)
		}
		if s.PID > 0 {
			keys = append(keys, fmt.Sprintf("%s|%s|pid|%d", s.Host, s.Provider, s.PID))
		}
		dup := false
		for _, k := range keys {
			if seen[k] {
				dup = true
			}
		}
		if dup {
			continue
		}
		for _, k := range keys {
			seen[k] = true
		}
		out = append(out, s)
	}
	return out
}
