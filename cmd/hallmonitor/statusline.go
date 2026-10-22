package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/usage"
)

// runStatusline is `hallmonitor statusline`, meant to be Claude Code's
// statusLine command. It saves the plan limits Claude Code passes in (the
// only place they're exposed), then prints a status line: its own, or the
// output of --then, which gets the same input, so an existing status line
// keeps working.
func runStatusline(args []string) {
	fs := flag.NewFlagSet("statusline", flag.ExitOnError)
	then := fs.String("then", "", "run this status line command too, and print its output instead")
	install := fs.Bool("install", false, "set hallmonitor as Claude Code's status line in ~/.claude/settings.json")
	_ = fs.Parse(args)
	if *install {
		if err := installStatusline(); err != nil {
			fmt.Fprintln(os.Stderr, "hallmonitor:", err)
			os.Exit(1)
		}
		return
	}

	in, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	var st usage.ClaudeStatus
	if json.Unmarshal(in, &st) == nil {
		_ = usage.Open(usage.DefaultDir()).SaveClaudeLimits(st, time.Now())
	}
	// Keep the last raw rate_limits block (and nothing else from the input)
	// so it's easy to see what Claude Code actually reported.
	var raw struct {
		RateLimits json.RawMessage `json:"rate_limits"`
	}
	if json.Unmarshal(in, &raw) == nil {
		_ = os.WriteFile(filepath.Join(usage.DefaultDir(), "claude-rate-limits-raw.json"),
			[]byte(fmt.Sprintf("{\"at\":%q,\"rate_limits\":%s}\n", time.Now().Format(time.RFC3339), orNull(raw.RateLimits))), 0o600)
	}

	if *then != "" {
		cmd := exec.Command("/bin/sh", "-c", *then)
		cmd.Stdin = bytes.NewReader(in)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
		return
	}
	fmt.Println(ownStatusline(in))
}

// installStatusline points Claude Code's statusLine at hallmonitor, keeping
// any existing status line by chaining it with --then. The old settings
// file is backed up next to itself first.
func installStatusline() error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".claude", "settings.json")
	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("%s isn't valid JSON, not touching it: %w", path, err)
		}
		backup := path + ".bak-hallmonitor-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return err
		}
		fmt.Println("backed up", backup)
	} else if !os.IsNotExist(err) {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	// Prefer the stable Homebrew link over a versioned Cellar path.
	for _, p := range []string{"/opt/homebrew/bin/hallmonitor", "/usr/local/bin/hallmonitor"} {
		if r, err := filepath.EvalSymlinks(p); err == nil && r == self {
			self = p
		}
	}
	cmd := shellQuote(self) + " statusline"

	if sl, ok := settings["statusLine"].(map[string]any); ok {
		existing, _ := sl["command"].(string)
		switch {
		case (strings.Contains(existing, "hallmonitor") || strings.Contains(existing, "agentboard")) && strings.Contains(existing, "statusline"):
			fmt.Println("already installed:", existing)
			return nil
		case existing != "":
			cmd += " --then " + shellQuote(existing)
			fmt.Println("keeping your status line; hallmonitor runs it via --then")
		}
	}
	settings["statusLine"] = map[string]any{"type": "command", "command": cmd}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Println("statusLine →", cmd)
	fmt.Println("Claude plan limits show up after your next Claude Code reply (Pro/Max plans).")
	return nil
}

func orNull(b json.RawMessage) string {
	if len(b) == 0 {
		return "null"
	}
	return string(b)
}

// shellQuote quotes s for sh unless every character is plainly safe, so a
// path with a newline, "=" or "{" can't change the command either.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:,@%", r))
	}) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func ownStatusline(in []byte) string {
	var d struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
		ContextWindow struct {
			UsedPercentage *float64 `json:"used_percentage"`
		} `json:"context_window"`
		RateLimits map[string]struct {
			UsedPercentage float64 `json:"used_percentage"`
		} `json:"rate_limits"`
	}
	_ = json.Unmarshal(in, &d)
	parts := []string{"\x1b[38;2;217;119;87m✻\x1b[0m " + d.Model.DisplayName}
	if p := d.ContextWindow.UsedPercentage; p != nil {
		parts = append(parts, fmt.Sprintf("ctx %s", pct(*p)))
	}
	for _, w := range []struct{ key, label string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		if rl, ok := d.RateLimits[w.key]; ok {
			parts = append(parts, fmt.Sprintf("%s %s", w.label, pct(rl.UsedPercentage)))
		}
	}
	return strings.Join(parts, " \x1b[2m·\x1b[0m ")
}

// pct colors a percentage green, amber past 50, red past 80.
func pct(p float64) string {
	c := "74;222;128"
	switch {
	case p >= 80:
		c = "248;113;113"
	case p >= 50:
		c = "251;191;36"
	}
	return fmt.Sprintf("\x1b[38;2;%sm%.0f%%\x1b[0m", c, p)
}
