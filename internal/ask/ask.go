// Package ask carries a question from a Claude Code session to Hall Monitor
// and the answer back.
//
// Claude Code runs `hallmonitor hook` before it asks you something (the
// AskUserQuestion tool) and before it shows a permission prompt. The hook
// writes the question to a file, the app shows it in the notch, and when you
// click an answer the app writes it next to the question; the hook hands it
// to Claude Code. If the app isn't running, or you don't answer in time, or
// you choose to answer in Claude, the hook says nothing and Claude Code asks
// you the usual way. Nothing here runs unless you turn it on.
//
// Who can answer: the files live in a directory only you can open, and the
// hook takes an answer only from a plain file you own. That keeps other
// users out. It can't tell the app from another program running as you:
// such a program could write an answer, but it could just as well add an
// allow-everything hook to ~/.claude/settings.json, so it gains nothing
// here. Sandboxed agents (Claude Code's or Codex's sandbox) can't write
// outside their project, so they can't answer.
package ask

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Kinds of question.
const (
	KindQuestion   = "question"   // AskUserQuestion
	KindPermission = "permission" // a permission prompt
)

// Option is one choice of a question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is one question of an AskUserQuestion call.
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
}

// Ask is a pending question from a session.
type Ask struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	CWD       string     `json:"cwd,omitempty"`
	Kind      string     `json:"kind"`
	Questions []Question `json:"questions,omitempty"` // KindQuestion
	Tool      string     `json:"tool,omitempty"`      // KindPermission: the tool, e.g. "Bash"
	Detail    string     `json:"detail,omitempty"`    // KindPermission: what it wants to do
	Complete  bool       `json:"complete,omitempty"`  // KindPermission: Detail is all of it, so it may be allowed
	PID       int        `json:"pid,omitempty"`       // the waiting hook; when it's gone, so is the question
	At        time.Time  `json:"at"`
	Deadline  time.Time  `json:"deadline"`
}

// Answer is what the app writes back.
type Answer struct {
	// "answer": Answers holds the choices; "allow" / "deny" for permissions;
	// "pass": leave it to Claude Code's own prompt.
	Action  string              `json:"action"`
	Answers map[string][]string `json:"answers,omitempty"` // question text -> labels (or typed text)
}

// ToolLabel names a tool for people: an MCP tool's mcp__server__tool_name
// reads as its name in words and the server it comes from ("Start session",
// "ccd_session"); any other tool is its own name, with no server.
func ToolLabel(tool string) (name, server string) {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return tool, ""
	}
	server, name, ok = strings.Cut(rest, "__")
	if !ok || server == "" || name == "" {
		return tool, ""
	}
	words := strings.Join(strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' }), " ")
	if words == "" {
		return tool, ""
	}
	return strings.ToUpper(words[:1]) + words[1:], server
}

// Dir is where pending questions live.
func Dir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "hallmonitor", "asks")
}

// AlivePath is touched by the app every few seconds while answering from the
// notch is on. The hook only waits when it's fresh.
func AlivePath() string { return filepath.Join(Dir(), ".app-alive") }

// AppListening reports whether the app is up and wants questions.
func AppListening(now time.Time) bool {
	if secureDir() != nil {
		return false
	}
	fi, err := ownFile(AlivePath())
	return err == nil && now.Sub(fi.ModTime()) < 12*time.Second
}

// NewID names a new question.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102150405") + "-" + hex.EncodeToString(b)
}

// validID is the shape NewID makes. An id names files, so
// anything else (a "../" in a planted question, say) is refused.
var validID = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{16}$`)

// ValidID reports whether id can name a question.
func ValidID(id string) bool { return validID.MatchString(id) }

func askPath(id string) string    { return filepath.Join(Dir(), id+".json") }
func answerPath(id string) string { return filepath.Join(Dir(), id+".answer.json") }

// secureDir makes the directory, or checks the one there: a real directory
// (not a link), yours, open to no one else.
func secureDir() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	return checkDir(true)
}

// checkDir reports whether the directory is yours alone; with fix, it
// takes away other people's access first.
func checkDir(fix bool) error {
	dir := Dir()
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s isn't a directory of yours", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if !fix {
			return fmt.Errorf("%s is open to others", dir)
		}
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// ownFile stats path if it's a plain file you own with one name: not a
// link, not someone else's.
func ownFile(path string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		return nil, fmt.Errorf("%s isn't a plain file of yours", path)
	}
	return fi, nil
}

// readOwn reads a file ownFile accepts, without following a link swapped in
// after the check.
func readOwn(path string, limit int64) ([]byte, error) {
	if _, err := ownFile(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// Post writes a question for the app to show.
func Post(a Ask) error {
	if !ValidID(a.ID) {
		return errors.New("bad question id")
	}
	if err := secureDir(); err != nil {
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tmp := askPath(a.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, askPath(a.ID))
}

// Wait blocks until the question is answered, the deadline passes, stop
// closes or gone (when set) reports it was answered elsewhere.
func Wait(id string, deadline time.Time, stop <-chan struct{}, gone func() bool) (Answer, bool) {
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		if b, err := readOwn(answerPath(id), 1<<20); err == nil {
			var ans Answer
			if json.Unmarshal(b, &ans) == nil {
				return ans, true
			}
		}
		if time.Now().After(deadline) || (gone != nil && gone()) {
			return Answer{}, false
		}
		select {
		case <-stop:
			return Answer{}, false
		case <-t.C:
		}
	}
}

// Done removes a question and its answer.
func Done(id string) {
	if !ValidID(id) {
		return
	}
	_ = os.Remove(askPath(id))
	_ = os.Remove(answerPath(id))
}

// Pending lists unanswered questions, oldest first, dropping any whose
// deadline has passed (their hook has given up).
func Pending(now time.Time) []Ask {
	if checkDir(false) != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(Dir(), "*.json"))
	var out []Ask
	for _, f := range files {
		if strings.HasSuffix(f, ".answer.json") {
			continue
		}
		b, err := readOwn(f, 1<<20)
		if err != nil {
			continue
		}
		var a Ask
		if json.Unmarshal(b, &a) != nil || !ValidID(a.ID) || filepath.Base(f) != a.ID+".json" {
			continue
		}
		if now.After(a.Deadline.Add(5*time.Second)) || (a.PID > 0 && !alive(a.PID)) {
			_ = os.Remove(f)
			continue
		}
		if _, err := os.Stat(answerPath(a.ID)); err == nil {
			continue // answered, the hook is picking it up
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// alive reports whether process pid still runs.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
