package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hiteshbandhu/hallmonitor/internal/ask"
	"github.com/hiteshbandhu/hallmonitor/internal/migrate"
)

// How long a question waits in the notch before Claude Code asks the usual way.
const askWait = 120 * time.Second

// runHook is `hallmonitor hook`: Claude Code runs it before AskUserQuestion
// and before permission prompts (once you turn on answering from the notch).
// With --install / --uninstall it adds or removes itself in
// ~/.claude/settings.json.
func runHook(args []string) {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	install := fs.Bool("install", false, "answer Claude Code's questions from the notch: add the hooks to ~/.claude/settings.json")
	uninstall := fs.Bool("uninstall", false, "remove the hooks from ~/.claude/settings.json")
	status := fs.Bool("status", false, "print whether the hooks are installed")
	_ = fs.Parse(args)
	switch {
	case *install, *uninstall:
		if err := editHooks(*install); err != nil {
			fmt.Fprintln(os.Stderr, "hallmonitor:", err)
			os.Exit(1)
		}
		return
	case *status:
		fmt.Println(hooksInstalled())
		return
	}
	hook()
}

type hookInput struct {
	Event      string          `json:"hook_event_name"`
	SessionID  string          `json:"session_id"`
	Transcript string          `json:"transcript_path"`
	CWD        string          `json:"cwd"`
	Tool       string          `json:"tool_name"`
	ToolUseID  string          `json:"tool_use_id"`
	ToolInput  json.RawMessage `json:"tool_input"`
}

// interactive are tools whose permission prompt is the tool itself: a
// question is asked through PreToolUse instead, and Claude Code ignores an
// allow from a hook for them.
var interactive = map[string]bool{"AskUserQuestion": true, "ExitPlanMode": true}

// hook never fails loudly: on anything unexpected it says nothing, and
// Claude Code carries on as if it weren't there.
func hook() {
	in, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	var h hookInput
	if json.Unmarshal(in, &h) != nil || !ask.AppListening(time.Now()) {
		return
	}
	a := ask.Ask{ID: newID(), SessionID: h.SessionID, CWD: h.CWD, PID: os.Getpid(), At: time.Now()}
	a.Deadline = a.At.Add(askWait)
	var questions json.RawMessage
	switch {
	case h.Event == "PreToolUse" && h.Tool == "AskUserQuestion":
		var ti struct {
			Questions json.RawMessage `json:"questions"`
		}
		if json.Unmarshal(h.ToolInput, &ti) != nil || json.Unmarshal(ti.Questions, &a.Questions) != nil || len(a.Questions) == 0 {
			return
		}
		questions = ti.Questions
		a.Kind = ask.KindQuestion
	case h.Event == "PermissionRequest" && !interactive[h.Tool]:
		a.Kind = ask.KindPermission
		a.Tool = h.Tool
		a.Detail, a.Complete = permissionDetail(h.Tool, h.ToolInput)
	default:
		return
	}
	// Watch from before the notch shows it: Claude Code shows its own
	// prompt alongside, and you may answer there first.
	watch := ask.NewWatch(h.Transcript, h.ToolUseID, h.Tool, h.ToolInput)
	ppid := os.Getppid()
	if ask.Post(a) != nil {
		return
	}
	defer ask.Done(a.ID)

	// Claude Code may kill the hook if you answer in the terminal instead.
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() { <-sig; close(stop) }()

	ans, ok := ask.Wait(a.ID, a.Deadline, stop, func() bool { return os.Getppid() != ppid || watch.Done() })
	if !ok {
		return
	}
	out := hookOutput(h.Event, a, questions, ans)
	if out != nil {
		_ = json.NewEncoder(os.Stdout).Encode(out)
	}
}

func hookOutput(event string, a ask.Ask, questions json.RawMessage, ans ask.Answer) any {
	switch {
	case event == "PreToolUse" && ans.Action == "answer":
		answers := map[string]any{}
		for _, q := range a.Questions {
			got := ans.Answers[q.Question]
			if len(got) == 0 {
				return nil // incomplete: let Claude Code ask
			}
			if q.MultiSelect {
				answers[q.Question] = got
			} else {
				answers[q.Question] = got[0]
			}
		}
		return map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": "Answered in Hall Monitor",
			"updatedInput":             map[string]any{"questions": questions, "answers": answers},
		}}
	case event == "PermissionRequest" && ans.Action == "allow" && !a.Complete:
		return nil // you couldn't see all of it: Claude asks as usual
	case event == "PermissionRequest" && (ans.Action == "allow" || ans.Action == "deny"):
		d := map[string]any{"behavior": ans.Action}
		if ans.Action == "deny" {
			d["message"] = "Denied in Hall Monitor."
		}
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": d}}
	}
	return nil // "pass": the usual prompt
}

// maxDetail is how much of a tool's input the notch shows. Allow approves
// all of it, so anything longer isn't offered for allowing there.
const maxDetail = 2000

// permissionDetail is everything the tool wants to do: a shell command as
// written, then every other input as a `key: value` line with the value in
// JSON, so no value can pass for another line. complete is false when it
// doesn't fit or can't be shown faithfully (not an object, a key given
// twice); then only Deny or Claude's own prompt are offered.
func permissionDetail(tool string, input json.RawMessage) (detail string, complete bool) {
	m, ok := decodeInput(input)
	if !ok {
		d, _ := fit(visible(string(input)))
		return d, false
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	if cmd, isStr := m["command"].(string); isStr && tool == "Bash" {
		lines = append(lines, visible(cmd))
		delete(m, "command")
		delete(m, "description") // Claude's own words about the command, not what it runs
	}
	for _, k := range keys {
		v, present := m[k]
		if !present {
			continue
		}
		b, _ := json.Marshal(v)
		name := k
		if !plainKey.MatchString(k) {
			kb, _ := json.Marshal(k)
			name = string(kb)
		}
		lines = append(lines, name+": "+visible(string(b)))
	}
	return fit(strings.Join(lines, "\n"))
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// decodeInput reads a tool input the way Claude Code does, refusing what
// could read two ways: a key given twice at any depth, an escape that
// isn't a whole character (JavaScript keeps a lone surrogate, Go doesn't),
// or anything but one object. Numbers keep their exact digits.
func decodeInput(input json.RawMessage) (map[string]any, bool) {
	if loneSurrogate(input) {
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	v, ok := strictValue(d)
	if !ok {
		return nil, false
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, false
	}
	m, isObj := v.(map[string]any)
	return m, isObj
}

// loneSurrogate reports a \uD800-\uDFFF escape that isn't half of a
// high-low pair.
func loneSurrogate(b []byte) bool {
	hex4 := func(i int) (rune, bool) {
		if i+6 > len(b) || b[i] != '\\' || b[i+1] != 'u' {
			return 0, false
		}
		var r rune
		for _, c := range b[i+2 : i+6] {
			switch {
			case c >= '0' && c <= '9':
				r = r<<4 | rune(c-'0')
			case c >= 'a' && c <= 'f':
				r = r<<4 | rune(c-'a'+10)
			case c >= 'A' && c <= 'F':
				r = r<<4 | rune(c-'A'+10)
			default:
				return 0, false
			}
		}
		return r, true
	}
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		r, ok := hex4(i)
		if !ok {
			i++ // \\, \" and the like: skip the escaped character
			continue
		}
		switch {
		case r >= 0xD800 && r <= 0xDBFF:
			lo, ok := hex4(i + 6)
			if !ok || lo < 0xDC00 || lo > 0xDFFF {
				return true
			}
			i += 11
		case r >= 0xDC00 && r <= 0xDFFF:
			return true
		default:
			i += 5
		}
	}
	return false
}

func strictValue(d *json.Decoder) (any, bool) {
	t, err := d.Token()
	if err != nil {
		return nil, false
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			kt, err := d.Token()
			k, isKey := kt.(string)
			if err != nil || !isKey {
				return nil, false
			}
			if _, dup := m[k]; dup {
				return nil, false
			}
			v, ok := strictValue(d)
			if !ok {
				return nil, false
			}
			m[k] = v
		}
		_, err := d.Token()
		return m, err == nil
	case json.Delim('['):
		var a []any
		for d.More() {
			v, ok := strictValue(d)
			if !ok {
				return nil, false
			}
			a = append(a, v)
		}
		_, err := d.Token()
		return a, err == nil
	}
	return t, true
}

// visible spells out characters that would hide or reorder text on screen:
// controls (but newlines and tabs), zero-width and bidirectional marks.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == utf8.RuneError, unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			fmt.Fprintf(&b, "\\u{%04X}", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fit(s string) (string, bool) {
	r := []rune(s)
	if len(r) <= maxDetail {
		return s, true
	}
	return string(r[:maxDetail-1]) + "…", false
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102150405") + "-" + hex.EncodeToString(b)
}

// ---- install ----

const hookMark = "hallmonitor hook"

func claudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func hooksInstalled() bool {
	b, err := os.ReadFile(claudeSettingsPath())
	return err == nil && strings.Contains(string(b), hookMark)
}

// editHooks adds (or removes) our two hooks, leaving every other hook and
// setting as it was. A backup of settings.json is kept.
func editHooks(add bool) error {
	path := claudeSettingsPath()
	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("%s isn't valid JSON, not touching it: %w", path, err)
		}
		if err := os.WriteFile(path+".bak-hallmonitor-"+time.Now().Format("20060102-150405"), raw, 0o600); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	// Drop ours wherever they are, then add them back if installing.
	for event, v := range hooks {
		groups, _ := v.([]any)
		var kept []any
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			hs, _ := gm["hooks"].([]any)
			var keep []any
			for _, h := range hs {
				hm, _ := h.(map[string]any)
				if cmd, _ := hm["command"].(string); strings.Contains(cmd, hookMark) {
					continue
				}
				keep = append(keep, h)
			}
			if len(keep) > 0 {
				gm["hooks"] = keep
				kept = append(kept, gm)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if add {
		self, _ := os.Executable()
		if r, err := filepath.EvalSymlinks(self); err == nil {
			self = r
		}
		bin := migrate.Binary(self)
		if bin == "" {
			bin = self
		}
		cmd := map[string]any{"type": "command", "command": shellQuote(bin) + " hook", "timeout": int(askWait.Seconds()) + 30}
		for event, matcher := range map[string]string{"PreToolUse": "AskUserQuestion", "PermissionRequest": ""} {
			groups, _ := hooks[event].([]any)
			hooks[event] = append(groups, map[string]any{"matcher": matcher, "hooks": []any{cmd}})
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
