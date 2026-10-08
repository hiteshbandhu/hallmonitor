// Package usage keeps a local ledger of how much you use coding agents.
//
// It reads the logs the agents already write (Claude Code transcripts, Codex
// rollouts, opencode's database) incrementally, remembering how far into
// each one it got, and folds them into per-day files of hourly buckets under
// $XDG_DATA_HOME/hallmonitor/usage (default ~/.local/share). The ledger
// outlives the source logs, which Claude Code prunes after 30 days by default.
//
// Only counts are stored: tokens, replies, prompts, tool names, active time.
// No prompt text or tool input ever lands in the ledger.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const stateVersion = 1

// Tokens are normalized across providers: Input excludes cached reads.
type Tokens struct {
	Input      int64 `json:"in,omitempty"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
	Output     int64 `json:"out,omitempty"`
}

func (t Tokens) Total() int64 { return t.Input + t.CacheRead + t.CacheWrite + t.Output }

func (t *Tokens) Add(o Tokens) {
	t.Input += o.Input
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.Output += o.Output
}

// Counters is one bucket of activity.
type Counters struct {
	Tokens  Tokens  `json:"tokens"`
	Replies int64   `json:"replies,omitempty"` // model responses
	Prompts int64   `json:"prompts,omitempty"` // human turns
	Tools   int64   `json:"tools,omitempty"`   // tool calls
	Active  float64 `json:"active_s,omitempty"`
}

func (c *Counters) Add(o Counters) {
	c.Tokens.Add(o.Tokens)
	c.Replies += o.Replies
	c.Prompts += o.Prompts
	c.Tools += o.Tools
	c.Active += o.Active
}

// Day is one ledger file. Bucket keys are "HH|provider|project|model".
type Day struct {
	Date     string               `json:"date"`
	Buckets  map[string]*Counters `json:"buckets"`
	Tools    map[string]int64     `json:"tools"`    // "provider|tool" -> calls
	Sessions map[string]string    `json:"sessions"` // session id -> "provider|project"
}

// RateLimit is the latest plan-usage reading a provider reported.
type RateLimit struct {
	Provider    string    `json:"provider"`
	Window      string    `json:"window,omitempty"` // "5h", "7d", "spend"; "" = the provider's primary window
	UsedPercent float64   `json:"used_percent"`
	WindowMin   int       `json:"window_minutes"`
	ResetsAt    time.Time `json:"resets_at"`
	ObservedAt  time.Time `json:"observed_at"`
}

type fileState struct {
	Offset int64     `json:"offset"`
	Size   int64     `json:"size"`
	Mod    time.Time `json:"mod"`
	Seen   []string  `json:"seen,omitempty"` // recent message ids, for dedup across reads
	// Per-file context carried between reads (Codex: cwd, model, session).
	Ctx map[string]string `json:"ctx,omitempty"`
}

type state struct {
	Version    int                   `json:"version"`
	Files      map[string]*fileState `json:"files"`
	Last       map[string]time.Time  `json:"last"` // session -> last event, for active time
	RateLimits map[string]RateLimit  `json:"rate_limits"`
}

// Ledger is the on-disk usage store.
type Ledger struct {
	Dir  string
	Home string

	st    *state
	days  map[string]*Day
	dirty map[string]bool
}

func DefaultDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "hallmonitor", "usage")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "hallmonitor", "usage")
}

func Open(dir string) *Ledger {
	home, _ := os.UserHomeDir()
	return &Ledger{Dir: dir, Home: home}
}

// Progress reports scan progress in bytes.
type Progress func(done, total int64)

// ErrBusy means another hallmonitor is updating the ledger right now.
var ErrBusy = errors.New("usage ledger is being updated by another hallmonitor")

// Update reads new data from every agent log into the ledger.
func (l *Ledger) Update(ctx context.Context, progress Progress) error {
	if err := os.MkdirAll(filepath.Join(l.Dir, "days"), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(l.Dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return ErrBusy
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	l.loadState()
	l.days, l.dirty = map[string]*Day{}, map[string]bool{}

	type job struct {
		path string
		scan func(path string, fs *fileState, data []byte) error
		size int64
	}
	var jobs []job
	var total int64
	add := func(paths []string, scan func(string, *fileState, []byte) error) {
		for _, p := range paths {
			st, err := os.Stat(p)
			if err != nil {
				continue
			}
			fs := l.st.Files[p]
			if fs != nil && fs.Size == st.Size() && fs.Mod.Equal(st.ModTime()) {
				continue // untouched
			}
			n := st.Size()
			if fs != nil && fs.Offset <= n {
				n -= fs.Offset
			}
			jobs = append(jobs, job{p, scan, n})
			total += n
		}
	}
	add(l.claudeFiles(), l.scanClaude)
	add(l.codexFiles(), l.scanCodex)
	// Oldest first so active-time gaps and rate limits resolve in order.
	sort.SliceStable(jobs, func(i, j int) bool { return modTime(jobs[i].path).Before(modTime(jobs[j].path)) })

	var done int64
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		if err := l.readNew(j.path, j.scan); err != nil {
			continue // one bad file never stops the ledger
		}
		done += j.size
		if progress != nil {
			progress(done, total)
		}
	}
	if ctx.Err() == nil {
		l.scanOpencode(ctx)
	}
	return l.save()
}

// readNew hands the complete lines appended since the last read to scan.
func (l *Ledger) readNew(path string, scan func(string, *fileState, []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	fs := l.st.Files[path]
	if fs == nil || fs.Offset > st.Size() { // new, or rewritten
		fs = &fileState{}
		l.st.Files[path] = fs
	}
	const maxChunk = 64 << 20
	for fs.Offset < st.Size() {
		n := min(st.Size()-fs.Offset, maxChunk)
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, fs.Offset); err != nil {
			return err
		}
		end := lastNewline(buf)
		if end < 0 {
			if n == maxChunk {
				fs.Offset += n // a single absurd line; skip it
				continue
			}
			break // partial line still being written
		}
		if err := scan(path, fs, buf[:end+1]); err != nil {
			return err
		}
		fs.Offset += int64(end + 1)
	}
	fs.Size, fs.Mod = st.Size(), st.ModTime()
	return nil
}

func lastNewline(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

// record folds one event into the ledger.
type event struct {
	at       time.Time
	provider string
	session  string
	project  string
	model    string
	c        Counters
	tools    []string
	active   bool // counts toward active time
}

const maxGap = 5 * time.Minute

func (l *Ledger) record(e event) {
	if e.at.IsZero() {
		return
	}
	local := e.at.Local()
	d := l.day(local.Format("2006-01-02"))
	if e.active && e.session != "" {
		if last, ok := l.st.Last[e.session]; ok {
			if gap := e.at.Sub(last); gap > 0 && gap <= maxGap {
				e.c.Active += gap.Seconds()
			}
		}
		if e.at.After(l.st.Last[e.session]) {
			l.st.Last[e.session] = e.at
		}
	}
	key := fmt.Sprintf("%s|%s|%s|%s", local.Format("15"), e.provider, e.project, e.model)
	b := d.Buckets[key]
	if b == nil {
		b = &Counters{}
		d.Buckets[key] = b
	}
	b.Add(e.c)
	for _, t := range e.tools {
		d.Tools[e.provider+"|"+t]++
	}
	if e.session != "" {
		d.Sessions[e.session] = e.provider + "|" + e.project
	}
	l.dirty[d.Date] = true
}

func (l *Ledger) day(date string) *Day {
	if d, ok := l.days[date]; ok {
		return d
	}
	d := readDay(filepath.Join(l.Dir, "days", date+".json"))
	if d == nil {
		d = &Day{Date: date}
	}
	if d.Buckets == nil {
		d.Buckets = map[string]*Counters{}
	}
	if d.Tools == nil {
		d.Tools = map[string]int64{}
	}
	if d.Sessions == nil {
		d.Sessions = map[string]string{}
	}
	l.days[date] = d
	return d
}

func readDay(path string) *Day {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var d Day
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	return &d
}

func (l *Ledger) loadState() {
	l.st = &state{}
	if b, err := os.ReadFile(filepath.Join(l.Dir, "state.json")); err == nil {
		_ = json.Unmarshal(b, l.st)
	}
	if l.st.Version != stateVersion {
		// Unknown layout: start over rather than double count.
		l.st = &state{Version: stateVersion}
		_ = os.RemoveAll(filepath.Join(l.Dir, "days"))
		_ = os.MkdirAll(filepath.Join(l.Dir, "days"), 0o755)
	}
	if l.st.Files == nil {
		l.st.Files = map[string]*fileState{}
	}
	if l.st.Last == nil {
		l.st.Last = map[string]time.Time{}
	}
	if l.st.RateLimits == nil {
		l.st.RateLimits = map[string]RateLimit{}
	}
	// Forget active-time anchors older than the gap window.
	for k, t := range l.st.Last {
		if time.Since(t) > 24*time.Hour {
			delete(l.st.Last, k)
		}
	}
}

func (l *Ledger) save() error {
	for date := range l.dirty {
		if err := writeJSON(filepath.Join(l.Dir, "days", date+".json"), l.days[date]); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(l.Dir, "state.json"), l.st)
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func modTime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// remember keeps a bounded list of recent ids for cross-read dedup.
func remember(fs *fileState, seen map[string]bool, id string) {
	seen[id] = true
	fs.Seen = append(fs.Seen, id)
	if len(fs.Seen) > 64 {
		fs.Seen = fs.Seen[len(fs.Seen)-64:]
	}
}

func seenSet(fs *fileState) map[string]bool {
	m := make(map[string]bool, len(fs.Seen))
	for _, id := range fs.Seen {
		m[id] = true
	}
	return m
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
