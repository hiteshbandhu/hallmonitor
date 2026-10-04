// Package model holds the vendor-neutral session shape every adapter emits.
package model

import (
	"context"

	"github.com/hiteshbandhu/hallmonitor/internal/ask"
	"strconv"
	"time"
)

type Status string

const (
	StatusBusy    Status = "busy"    // agent is working on a turn
	StatusWaiting Status = "waiting" // agent needs the user (approval, input)
	StatusBlocked Status = "blocked" // background agent parked / blocked
	StatusIdle    Status = "idle"    // alive, nothing running
	StatusError   Status = "error"
	StatusUnknown Status = "unknown"
)

// Rank orders statuses for sorting: things that need eyes first.
func (s Status) Rank() int {
	switch s {
	case StatusWaiting:
		return 0
	case StatusBusy:
		return 1
	case StatusError:
		return 2
	case StatusBlocked:
		return 3
	case StatusIdle:
		return 4
	}
	return 5
}

type Session struct {
	Host      string    `json:"host,omitempty"` // "" = this machine
	Provider  string    `json:"provider"`
	ID        string    `json:"id"`
	PID       int       `json:"pid,omitempty"`
	Title     string    `json:"title"`
	CWD       string    `json:"cwd"`
	Status    Status    `json:"status"`
	Kind      string    `json:"kind"`
	Model     string    `json:"model,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	Source    string    `json:"source"`
	Last      string    `json:"last,omitempty"`   // latest activity, e.g. "Bash · run tests", capped
	Prompt    string    `json:"prompt,omitempty"` // latest user prompt, capped
	Since     time.Time `json:"status_since,omitzero"`
	Context   int       `json:"context_tokens,omitempty"`
	// Activity holds unix seconds of recent agent events (tool calls,
	// replies), at most one per second, for backfilling history on start.
	Activity []int64           `json:"activity,omitempty"`
	Extra    map[string]string `json:"extra,omitempty"`
	// Subagents the session started; nil when it never started any.
	Subagents *Subagents `json:"subagents,omitempty"`
	// A question or permission prompt waiting for an answer from the notch.
	Ask *ask.Ask `json:"ask,omitempty"`

	// History is the status sampled once per refresh, oldest first. Filled
	// by the hub; not part of the JSON snapshot.
	History []Status `json:"-"`
}

// Subagents counts the helpers a session started (Claude Code's Agent tool).
type Subagents struct {
	Running int      `json:"running"`
	Total   int      `json:"total"`
	Active  []string `json:"active,omitempty"` // what the running ones are doing, newest first, capped
}

type AdapterError struct {
	Provider string `json:"provider"`
	Error    string `json:"error"`
}

type Snapshot struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Sessions    []Session      `json:"sessions"`
	Errors      []AdapterError `json:"adapter_errors"`
}

// Adapter reads one vendor's local state. Adapters must be read-only and must
// not panic on unexpected formats; return an error instead.
type Adapter interface {
	Name() string
	Collect(ctx context.Context) ([]Session, error)
}

// Snip trims s to n runes on one line.
func Snip(s string, n int) string {
	out := make([]rune, 0, n)
	space := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			if !space && len(out) > 0 {
				out = append(out, ' ')
			}
			space = true
			continue
		}
		space = false
		out = append(out, r)
		if len(out) >= n {
			return string(out[:n-1]) + "…"
		}
	}
	return string(out)
}

// Key identifies a session across refreshes.
func (s Session) Key() string {
	id := s.ID
	if id == "" {
		id = "pid:" + strconv.Itoa(s.PID)
	}
	return s.Host + "|" + s.Provider + "|" + id
}

// LastSeen is the best "last activity" time we have.
func (s Session) LastSeen() time.Time {
	if !s.UpdatedAt.IsZero() {
		return s.UpdatedAt
	}
	return s.StartedAt
}

// Stale sessions are parked and haven't moved in a day.
func (s Session) Stale(now time.Time) bool {
	switch s.Status {
	case StatusBusy, StatusWaiting:
		return false
	}
	return now.Sub(s.LastSeen()) > 24*time.Hour
}

// ActivityWindow is how far back Activity reaches.
const ActivityWindow = 20 * time.Minute

// AddActivity records t in a sorted, de-duplicated, windowed list.
func AddActivity(list []int64, t time.Time, now time.Time) []int64 {
	if t.IsZero() || now.Sub(t) > ActivityWindow {
		return list
	}
	u := t.Unix()
	if n := len(list); n > 0 && list[n-1] >= u {
		return list
	}
	return append(list, u)
}
