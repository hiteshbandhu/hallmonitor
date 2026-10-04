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
package ask

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	fi, err := os.Stat(AlivePath())
	return err == nil && now.Sub(fi.ModTime()) < 12*time.Second
}

func askPath(id string) string    { return filepath.Join(Dir(), id+".json") }
func answerPath(id string) string { return filepath.Join(Dir(), id+".answer.json") }

// Post writes a question for the app to show.
func Post(a Ask) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
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

// Wait blocks until the question is answered or the deadline passes.
func Wait(id string, deadline time.Time, stop <-chan struct{}) (Answer, bool) {
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		if b, err := os.ReadFile(answerPath(id)); err == nil {
			var ans Answer
			if json.Unmarshal(b, &ans) == nil {
				return ans, true
			}
		}
		if time.Now().After(deadline) {
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
	_ = os.Remove(askPath(id))
	_ = os.Remove(answerPath(id))
}

// Pending lists unanswered questions, oldest first, dropping any whose
// deadline has passed (their hook has given up).
func Pending(now time.Time) []Ask {
	files, _ := filepath.Glob(filepath.Join(Dir(), "*.json"))
	var out []Ask
	for _, f := range files {
		if strings.HasSuffix(f, ".answer.json") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var a Ask
		if json.Unmarshal(b, &a) != nil {
			continue
		}
		if now.After(a.Deadline.Add(5 * time.Second)) {
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
