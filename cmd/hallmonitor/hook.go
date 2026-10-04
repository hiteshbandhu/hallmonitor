package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/ask"
	"github.com/hiteshbandhu/hallmonitor/internal/migrate"
	"github.com/hiteshbandhu/hallmonitor/internal/model"
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
	Event     string          `json:"hook_event_name"`
	SessionID string          `json:"session_id"`
	CWD       string          `json:"cwd"`
	Tool      string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// hook never fails loudly: on anything unexpected it says nothing, and
// Claude Code carries on as if it weren't there.
func hook() {
	in, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	var h hookInput
	if json.Unmarshal(in, &h) != nil || !ask.AppListening(time.Now()) {
		return
	}
	a := ask.Ask{ID: newID(), SessionID: h.SessionID, CWD: h.CWD, At: time.Now()}
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
	case h.Event == "PermissionRequest":
		a.Kind = ask.KindPermission
		a.Tool = h.Tool
		a.Detail = permissionDetail(h.Tool, h.ToolInput)
	default:
		return
	}
	if ask.Post(a) != nil {
		return
	}
	defer ask.Done(a.ID)

	// Claude Code kills the hook if you answer in the terminal instead.
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() { <-sig; close(stop) }()

	ans, ok := ask.Wait(a.ID, a.Deadline, stop)
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
	case event == "PermissionRequest" && (ans.Action == "allow" || ans.Action == "deny"):
		d := map[string]any{"behavior": ans.Action}
		if ans.Action == "deny" {
			d["message"] = "Denied in Hall Monitor."
		}
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": d}}
	}
	return nil // "pass": the usual prompt
}

// permissionDetail is the one line that says what the tool wants to do.
func permissionDetail(tool string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	for _, k := range []string{"command", "file_path", "url", "pattern", "path", "query", "prompt", "description"} {
		if v, ok := m[k].(string); ok && v != "" {
			if k == "file_path" || k == "path" {
				return filepath.Base(v)
			}
			return model.Snip(strings.TrimSpace(v), 200)
		}
	}
	return model.Snip(string(input), 200)
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
