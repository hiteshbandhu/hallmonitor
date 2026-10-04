// Package claude reads Claude Code sessions from `claude agents --json` and
// ~/.claude/sessions/<pid>.json. It never touches the messaging sockets or the
// <pid>.<hash>.key files that sit next to the session JSON.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/ask"
	"github.com/hiteshbandhu/hallmonitor/internal/model"
	"github.com/hiteshbandhu/hallmonitor/internal/proc"
	"github.com/hiteshbandhu/hallmonitor/internal/usage"
)

type Adapter struct {
	Home string // defaults to $HOME
}

func (Adapter) Name() string { return "claude" }

// cliEntry is one element of `claude agents --json`.
type cliEntry struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	StartedAt int64  `json:"startedAt"`
	Status    string `json:"status"` // interactive: busy | idle
	State     string `json:"state"`  // background: blocked | ...
}

// sessionFile is ~/.claude/sessions/<pid>.json.
type sessionFile struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	CWD             string `json:"cwd"`
	StartedAt       int64  `json:"startedAt"`
	Version         string `json:"version"`
	Kind            string `json:"kind"`
	Entrypoint      string `json:"entrypoint"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"` // with status "waiting": "input needed", a permission prompt, …
	UpdatedAt       int64  `json:"updatedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

var sessionFileRe = regexp.MustCompile(`^\d+\.json$`)

func (a Adapter) Collect(ctx context.Context) ([]model.Session, error) {
	home := a.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	files := readSessionFiles(filepath.Join(home, ".claude", "sessions"))

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, cliErr := proc.Run(cctx, "claude", "agents", "--json")
	var entries []cliEntry
	if cliErr == nil {
		if err := json.Unmarshal(out, &entries); err != nil {
			cliErr = err
		}
	}

	var sessions []model.Session
	seen := map[int]bool{}
	for _, e := range entries {
		s := model.Session{
			Provider:  "claude",
			ID:        firstNonEmpty(e.SessionID, e.ID),
			PID:       e.PID,
			Title:     e.Name,
			CWD:       e.CWD,
			Kind:      e.Kind,
			StartedAt: ms(e.StartedAt),
			Source:    "cli",
			Status:    mapStatus(e.Status, e.State),
			Extra:     map[string]string{},
		}
		if e.State != "" {
			s.Extra["state"] = e.State
		}
		if f, ok := files[e.PID]; ok && e.PID > 0 {
			applyFile(&s, f)
			seen[e.PID] = true
		}
		sessions = append(sessions, s)
	}

	// Session files the CLI didn't list (or the CLI failed): keep them only if
	// the pid is still alive, so crashed sessions don't haunt the board.
	for pid, f := range files {
		if seen[pid] || !proc.Alive(pid) {
			continue
		}
		s := model.Session{
			Provider: "claude",
			ID:       f.SessionID,
			PID:      pid,
			CWD:      f.CWD,
			Kind:     f.Kind,
			Source:   "session-file",
			Status:   mapStatus(f.Status, ""),
			Extra:    map[string]string{},
		}
		applyFile(&s, f)
		sessions = append(sessions, s)
	}

	// Claude Code not installed at all: nothing to show, and not an error.
	if errors.Is(cliErr, exec.ErrNotFound) && len(files) == 0 {
		return nil, nil
	}
	if cliErr != nil && len(sessions) == 0 {
		return nil, cliErr
	}
	// hallmonitor's own hidden Claude Code, reading /usage, isn't an agent.
	probe := usage.ProbeDir()
	kept := sessions[:0]
	for _, s := range sessions {
		if s.CWD != probe {
			kept = append(kept, s)
		}
	}
	sessions = kept
	pending := map[string]ask.Ask{}
	for _, a := range ask.Pending(time.Now()) {
		pending[a.SessionID] = a
	}
	for i := range sessions {
		enrich(home, &sessions[i])
		if a, ok := pending[sessions[i].ID]; ok {
			// Waiting on you, whatever the session file says.
			a := a
			sessions[i].Ask = &a
			sessions[i].Status = model.StatusWaiting
			if a.Kind == ask.KindPermission {
				sessions[i].Last = "wants to use " + a.Tool + " · " + a.Detail
			} else if len(a.Questions) > 0 {
				sessions[i].Last = "asks · " + a.Questions[0].Question
			}
		}
	}
	return sessions, nil
}

func applyFile(s *model.Session, f sessionFile) {
	if f.Name != "" {
		s.Title = f.Name
	}
	if s.CWD == "" {
		s.CWD = f.CWD
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = ms(f.StartedAt)
	}
	if f.Status != "" {
		s.Status = mapStatus(f.Status, "")
	}
	if f.WaitingFor != "" {
		s.Extra["waiting_for"] = f.WaitingFor
	}
	s.UpdatedAt = ms(max(f.UpdatedAt, f.StatusUpdatedAt))
	s.Since = ms(f.StatusUpdatedAt)
	if f.Entrypoint != "" {
		s.Extra["entrypoint"] = f.Entrypoint
	}
	if f.Version != "" {
		s.Extra["version"] = f.Version
	}
}

func readSessionFiles(dir string) map[int]sessionFile {
	out := map[int]sessionFile{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !sessionFileRe.MatchString(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var f sessionFile
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		if f.PID == 0 {
			f.PID, _ = strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		}
		out[f.PID] = f
	}
	return out
}

func mapStatus(status, state string) model.Status {
	v := strings.ToLower(firstNonEmpty(status, state))
	switch v {
	case "busy", "running", "working", "active":
		return model.StatusBusy
	case "idle", "done", "completed":
		return model.StatusIdle
	case "blocked":
		return model.StatusBlocked
	case "waiting", "needs_input", "awaiting_input", "permission":
		return model.StatusWaiting
	case "error", "failed":
		return model.StatusError
	}
	return model.StatusUnknown
}

func ms(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// transcript reads are cached by path+size+mtime so a quiet session costs a
// stat per refresh.
var (
	cacheMu sync.Mutex
	cache   = map[string]cachedActivity{}
)

type cachedActivity struct {
	size int64
	mod  time.Time
	a    activity
}

func enrich(home string, s *model.Session) {
	p := transcriptPath(home, s.CWD, s.ID)
	if p == "" {
		return
	}
	st, err := os.Stat(p)
	if err != nil {
		return
	}
	cacheMu.Lock()
	c, ok := cache[p]
	cacheMu.Unlock()
	if !ok || c.size != st.Size() || !c.mod.Equal(st.ModTime()) {
		a, err := readActivity(p)
		if err != nil {
			return
		}
		c = cachedActivity{st.Size(), st.ModTime(), a}
		cacheMu.Lock()
		cache[p] = c
		cacheMu.Unlock()
	}
	s.Last, s.Prompt, s.Context = c.a.Last, c.a.Prompt, c.a.Context
	// While it waits on you, say what for: that's the useful line.
	if w := s.Extra["waiting_for"]; w != "" && s.Status == model.StatusWaiting {
		s.Last = "needs " + w
		if c.a.Last != "" && !strings.HasPrefix(c.a.Last, "↳ ") {
			s.Last += " · " + c.a.Last
		}
	}
	s.Activity = c.a.Activity
	s.Subagents = readSubagents(p, time.Now())
	if c.a.Model != "" {
		s.Model = c.a.Model
	}
	if c.a.LastAt.After(s.UpdatedAt) {
		s.UpdatedAt = c.a.LastAt
	}
}
