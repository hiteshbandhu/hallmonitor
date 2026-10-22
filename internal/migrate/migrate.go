// Package migrate carries a setup over from when hallmonitor was called
// agentboard: its data and config folders, and Claude Code's status line.
package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// FromAgentboard moves agentboard's folders to hallmonitor's names and points
// Claude Code's status line at hallmonitor. It does nothing once done, and
// never overwrites anything hallmonitor already has.
func FromAgentboard(self string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	base := func(env, def string) string {
		if d := os.Getenv(env); d != "" {
			return d
		}
		return filepath.Join(home, def)
	}
	for _, root := range []string{base("XDG_DATA_HOME", ".local/share"), base("XDG_CONFIG_HOME", ".config"), base("XDG_CACHE_HOME", ".cache")} {
		moveDir(filepath.Join(root, "agentboard"), filepath.Join(root, "hallmonitor"))
	}
	statusLine(filepath.Join(home, ".claude", "settings.json"), self)
}

// The binary may be quoted: '/path/agentboard' statusline.
var oldCmd = regexp.MustCompile(`agentboard'?\s+statusline`)

func moveDir(from, to string) {
	if _, err := os.Stat(from); err != nil {
		return
	}
	if _, err := os.Stat(to); err == nil {
		return
	}
	_ = os.Rename(from, to)
}

// statusLine rewrites an "<path>/agentboard statusline" command, as
// `agentboard statusline --install` set it up, to run hallmonitor. The old
// app or binary is about to be gone, and the status line would break.
func statusLine(path, self string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s := string(b)
	m := oldCmd.FindStringIndex(s)
	if m == nil {
		return
	}
	// The command is a JSON string: find where it starts.
	start := strings.LastIndexByte(s[:m[0]], '"')
	if start < 0 {
		return
	}
	old := s[start+1 : m[1]]
	bin := Binary(self)
	if bin == "" {
		return
	}
	_ = os.WriteFile(path+".bak-hallmonitor-"+time.Now().Format("20060102-150405"), b, 0o600)
	st, _ := os.Stat(path)
	mode := os.FileMode(0o644)
	if st != nil {
		mode = st.Mode().Perm()
	}
	// The command sits in a JSON string and runs through sh: quote it for both.
	q, _ := json.Marshal("'" + strings.ReplaceAll(bin, "'", `'\''`) + "' statusline")
	_ = os.WriteFile(path, []byte(strings.Replace(s, old, string(q[1:len(q)-1]), 1)), mode)
}

var installed = []string{"/opt/homebrew/bin/hallmonitor", "/usr/local/bin/hallmonitor",
	"/Applications/HallMonitor.app/Contents/Helpers/hallmonitor"}

// Binary is the hallmonitor command a status line should run: a Homebrew
// install first (it survives app updates), then the app's own copy, then
// this one.
func Binary(self string) string {
	for _, p := range installed {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if self != "" && filepath.Base(self) == "hallmonitor" {
		return self
	}
	return ""
}
