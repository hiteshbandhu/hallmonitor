package usage

import (
	"bytes"
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
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Claude Code shows plan limits in two places: the status line (terminal
// sessions only, after a reply) and its /usage screen. For everyone else,
// sessions from the desktop app included, hallmonitor opens Claude Code in a
// hidden terminal now and then, types /usage and reads the screen. Claude
// Code uses its own login for that; hallmonitor never sees a token, and
// /usage doesn't send anything to the model, so it costs nothing.

// ProbeEvery is how often plan limits are read from /usage, at most.
const ProbeEvery = 15 * time.Minute

// ProbeDir is the folder the hidden Claude Code runs in. Sessions there are
// hallmonitor's own and are left off the board and out of the ledger.
func ProbeDir() string { return filepath.Join(filepath.Dir(DefaultDir()), "claude-probe") }

// ErrNoPlanLimits means /usage showed no plan limits: signed in with an API
// key, or not signed in.
var ErrNoPlanLimits = errors.New("claude /usage shows no plan limits")

type probeState struct {
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

func probeStatePath(dir string) string { return filepath.Join(dir, "claude-probe.json") }

// MaybeProbeClaude reads plan limits from /usage when nothing has reported
// them for ProbeEvery and no probe ran in that time. Any number of
// hallmonitors can call it; one probes. HALLMONITOR_NO_PROBE=1 turns it off.
func (l *Ledger) MaybeProbeClaude(ctx context.Context) {
	if os.Getenv("HALLMONITOR_NO_PROBE") != "" {
		return
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return
	}
	now := time.Now()
	for _, rl := range l.claudeLimits() {
		if now.Sub(rl.ObservedAt) < ProbeEvery {
			return // the status line reported recently
		}
	}
	var st probeState
	if b, err := os.ReadFile(probeStatePath(l.Dir)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if now.Sub(st.At) < ProbeEvery {
		return
	}
	_ = l.ProbeClaude(ctx)
}

// ProbeClaude reads plan limits from /usage now and saves them.
func (l *Ledger) ProbeClaude(ctx context.Context) error {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(l.Dir, ".probe-lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return ErrBusy
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	screen, err := readUsageScreen(ctx)
	now := time.Now()
	var limits map[string]RateLimit
	if err == nil {
		limits = parseUsageScreen(screen, now)
		if len(limits) == 0 {
			err = ErrNoPlanLimits
		}
	}
	st := probeState{At: now}
	if err != nil {
		st.Error = err.Error()
	}
	_ = writeJSON(probeStatePath(l.Dir), st)
	if err != nil {
		return err
	}
	saved := l.claudeLimits()
	for k, v := range limits {
		saved[k] = v
	}
	return writeJSON(claudeLimitsPath(l.Dir), saved)
}

// readUsageScreen runs Claude Code in a pseudo-terminal, opens /usage and
// returns what it drew, as plain text.
func readUsageScreen(ctx context.Context) (string, error) {
	dir := ProbeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	defer cleanProbeTranscripts(dir)

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Safe mode: none of the user's hooks, MCP servers, plugins or CLAUDE.md
	// run for this. No tools either; nothing is ever sent to the model.
	cmd := exec.CommandContext(ctx, "claude", "--safe-mode", "--strict-mcp-config", "--tools", "")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "CLAUDE_CODE_CHILD_SESSION=1")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		return "", err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		f.Close()
	}()

	var (
		mu  sync.Mutex
		buf bytes.Buffer
		at  = time.Now() // last output
	)
	go func() {
		b := make([]byte, 32<<10)
		for {
			n, err := f.Read(b)
			mu.Lock()
			buf.Write(b[:n])
			at = time.Now()
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	text := func() string {
		mu.Lock()
		defer mu.Unlock()
		return plainText(buf.String())
	}
	// quiet waits until the screen has stopped changing for a moment.
	quiet := func(d, limit time.Duration) {
		end := time.Now().Add(limit)
		for time.Now().Before(end) && ctx.Err() == nil {
			mu.Lock()
			idle := time.Since(at)
			mu.Unlock()
			if idle > d {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	send := func(s string) { _, _ = f.Write([]byte(s)) }

	quiet(1500*time.Millisecond, 15*time.Second)
	if strings.Contains(text(), "trust this folder") {
		// First run in the probe folder: it's ours, and empty.
		send("\x1b[B")
		time.Sleep(300 * time.Millisecond)
		send("\r")
		quiet(1500*time.Millisecond, 15*time.Second)
	}
	mu.Lock()
	start := buf.Len()
	mu.Unlock()
	send("/usage")
	time.Sleep(800 * time.Millisecond)
	send("\r")

	// Wait for the limits, then a little longer: /usage may draw cached
	// numbers first and refresh them.
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		mu.Lock()
		s := plainText(buf.String()[start:])
		mu.Unlock()
		if weekRe.MatchString(s) || strings.Contains(s, "/login") {
			quiet(1500*time.Millisecond, 5*time.Second)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	mu.Lock()
	out := plainText(buf.String()[start:])
	mu.Unlock()
	send("\x1b")
	time.Sleep(200 * time.Millisecond)
	send("/exit\r")
	time.Sleep(500 * time.Millisecond)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, nil
}

// cleanProbeTranscripts removes any transcript Claude Code kept for the probe
// session, so it doesn't pile up in ~/.claude/projects.
func cleanProbeTranscripts(dir string) {
	home, _ := os.UserHomeDir()
	project := filepath.Join(home, ".claude", "projects", EscapeProject(dir))
	files, _ := filepath.Glob(filepath.Join(project, "*.jsonl"))
	for _, f := range files {
		_ = os.Remove(f)
	}
}

// EscapeProject is how Claude Code names a folder under ~/.claude/projects.
func EscapeProject(dir string) string {
	return nonAlnum.ReplaceAllString(dir, "-")
}

var (
	nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)
	ansiRe   = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)
	spaceRe  = regexp.MustCompile(`[ \t\r\n]+`)
	// "Current session ▌ 1% used Resets 6:20am (Asia/Calcutta)"
	sessionRe = regexp.MustCompile(`Current session[^%]*?(\d{1,3})% used\s*Resets\s+([^()]+?)\s*\(([^)]+)\)`)
	weekRe    = regexp.MustCompile(`Current week \(all models\)[^%]*?(\d{1,3})% used\s*Resets\s+([^()]+?)\s*\(([^)]+)\)`)
	resetRe   = regexp.MustCompile(`(?i)^(?:([A-Z][a-z]{2})\w*\.? (\d{1,2})(?:, (\d{4}))?(?: at)?\s*)?(?:(\d{1,2})(?::(\d{2}))?\s*(am|pm))?$`)
)

// plainText drops escape sequences; cursor moves become spaces.
func plainText(s string) string {
	return spaceRe.ReplaceAllString(ansiRe.ReplaceAllString(s, " "), " ")
}

// parseUsageScreen pulls the 5-hour and weekly limits out of /usage. The
// screen redraws as it loads, so the last reading of each wins.
func parseUsageScreen(s string, now time.Time) map[string]RateLimit {
	out := map[string]RateLimit{}
	grab := func(re *regexp.Regexp, key string, window int) {
		m := re.FindAllStringSubmatch(s, -1)
		if len(m) == 0 {
			return
		}
		last := m[len(m)-1]
		pct, err := strconv.ParseFloat(last[1], 64)
		if err != nil {
			return
		}
		out[key] = RateLimit{
			Provider:    "claude",
			Window:      key[len("claude:"):],
			UsedPercent: pct,
			WindowMin:   window,
			ResetsAt:    parseReset(last[2], last[3], now),
			ObservedAt:  now,
		}
	}
	grab(sessionRe, "claude:5h", 5*60)
	grab(weekRe, "claude:7d", 7*24*60)
	return out
}

// parseReset reads "6:20am", "Oct 1 at 1:30am", "Oct 1" or "Oct 1, 2027 at
// 9pm" in the named time zone, as the next such moment after now. Zero when
// it can't tell.
func parseReset(text, zone string, now time.Time) time.Time {
	loc, err := time.LoadLocation(strings.TrimSpace(zone))
	if err != nil {
		loc = time.Local
	}
	m := resetRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil || (m[1] == "" && m[4] == "") {
		return time.Time{}
	}
	n := now.In(loc)
	hour, min := 0, 0
	if m[4] != "" {
		hour, _ = strconv.Atoi(m[4])
		min, _ = strconv.Atoi(m[5])
		hour %= 12
		if strings.EqualFold(m[6], "pm") {
			hour += 12
		}
	}
	if m[1] == "" { // a time today, or tomorrow if that's passed
		t := time.Date(n.Year(), n.Month(), n.Day(), hour, min, 0, 0, loc)
		if t.Before(n) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
	mon, err := time.Parse("Jan", m[1])
	if err != nil {
		return time.Time{}
	}
	day, _ := strconv.Atoi(m[2])
	year := n.Year()
	if m[3] != "" {
		year, _ = strconv.Atoi(m[3])
	}
	t := time.Date(year, mon.Month(), day, hour, min, 0, 0, loc)
	if m[3] == "" && t.Before(n.AddDate(0, 0, -1)) {
		t = t.AddDate(1, 0, 0) // "Jan 2" seen on Dec 30
	}
	return t
}
