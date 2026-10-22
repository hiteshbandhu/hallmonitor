package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFromAgentboard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	old := filepath.Join(home, ".local", "share", "agentboard", "usage")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "claude-limits.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	settings := filepath.Join(home, ".claude", "settings.json")
	os.WriteFile(settings, []byte(`{
  "model": "opus",
  "statusLine": {
    "type": "command",
    "command": "'/Applications/AgentBoard.app/Contents/Helpers/agentboard' statusline --then 'my-line'"
  }
}`), 0o644)
	self := filepath.Join(home, "bin", "hallmonitor")
	installed = nil // whatever this machine has installed

	FromAgentboard(self)

	if _, err := os.Stat(filepath.Join(home, ".local", "share", "hallmonitor", "usage", "claude-limits.json")); err != nil {
		t.Error("data folder not moved:", err)
	}
	b, _ := os.ReadFile(settings)
	want := `"command": "'` + self + `' statusline --then 'my-line'"`
	if !strings.Contains(string(b), want) || !strings.Contains(string(b), `"model": "opus"`) {
		t.Errorf("settings now:\n%s", b)
	}
	if baks, _ := filepath.Glob(settings + ".bak-hallmonitor-*"); len(baks) != 1 {
		t.Error("no backup")
	}

	FromAgentboard(self) // second run: nothing left to do
	if baks, _ := filepath.Glob(settings + ".bak-hallmonitor-*"); len(baks) != 1 {
		t.Error("ran again")
	}
}
