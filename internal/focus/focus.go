// Package focus brings the app an agent runs in to the front: the Claude app
// (opened on that session), or the terminal or editor its process lives in.
// It only activates apps; it never types into them or needs any permission.
package focus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Target is what we know about the agent to focus.
type Target struct {
	Host       string // "" = this machine
	Provider   string
	ID         string // session id
	PID        int
	Entrypoint string // Claude Code's entrypoint, e.g. "claude-desktop", "cli"
}

// Focus brings the agent's app forward and says which app that was.
func Focus(ctx context.Context, t Target) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch {
	case t.Host != "":
		return "", fmt.Errorf("runs on %s", t.Host)
	case runtime.GOOS != "darwin":
		return "", errors.New("only on macOS")
	case t.Provider == "claude" && t.Entrypoint == "claude-desktop":
		return claudeApp(ctx, t.ID)
	case t.PID <= 0:
		return "", errors.New("no process to find")
	}
	return appOfProcess(ctx, t.PID)
}

// claudeApp opens the session in the Claude app, which names sessions
// local_<uuid> and records which Claude Code session each one runs.
func claudeApp(ctx context.Context, cliID string) (string, error) {
	if local := claudeAppSession(cliID); local != "" {
		if run(ctx, "open", "claude://code/continue?session="+local) == nil {
			return "Claude", nil
		}
	}
	return "Claude", run(ctx, "open", "-a", "Claude")
}

func claudeAppSession(cliID string) string {
	if cliID == "" {
		return ""
	}
	home, _ := os.UserHomeDir()
	files, _ := filepath.Glob(filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "*", "*", "local_*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var meta struct {
			CLISessionID string `json:"cliSessionId"`
		}
		if json.Unmarshal(b, &meta) == nil && meta.CLISessionID == cliID {
			return strings.TrimSuffix(filepath.Base(f), ".json")
		}
	}
	return ""
}

// appOfProcess walks up from the agent's process to the app it runs under
// (Terminal, iTerm, Ghostty, cmux, VS Code, Cursor, …) and activates it.
func appOfProcess(ctx context.Context, pid int) (string, error) {
	chain := ancestors(ctx, pid)
	if len(chain) == 0 {
		return "", fmt.Errorf("process %d is gone", pid)
	}
	// Inside tmux the chain ends at the tmux server; the app is wherever a
	// tmux client is attached.
	for _, p := range chain[1:] {
		if b := filepath.Base(p.comm); b == "tmux" || strings.HasPrefix(b, "tmux:") {
			if c := tmuxClient(ctx); c > 0 {
				chain = append(chain[:1], ancestors(ctx, c)...)
			}
			break
		}
	}
	for _, p := range chain[1:] { // [0] is the agent itself
		if app := appOf(p.comm); app != "" {
			return strings.TrimSuffix(filepath.Base(app), ".app"), run(ctx, "open", "-a", app)
		}
	}
	return "", errors.New("not running in an app on this Mac")
}

type proc struct {
	ppid int
	comm string
}

// ancestors are pid and its parents, nearest first, up to launchd.
func ancestors(ctx context.Context, pid int) []proc {
	var out []proc
	for i := 0; i < 40 && pid > 1; i++ {
		b, err := exec.CommandContext(ctx, "ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			break
		}
		f := strings.TrimSpace(string(b))
		sp := strings.IndexByte(f, ' ')
		if sp < 0 {
			break
		}
		ppid, _ := strconv.Atoi(f[:sp])
		out = append(out, proc{ppid, strings.TrimSpace(f[sp:])})
		pid = ppid
	}
	return out
}

// appOf is the outermost .app bundle a process runs from ("" if none):
// VS Code's terminal runs under ".../Visual Studio Code.app/.../Code Helper.app".
func appOf(comm string) string {
	if i := strings.Index(comm, ".app/"); i >= 0 {
		return comm[:i+4]
	}
	return ""
}

// tmuxClient is the pid of the most recently active tmux client.
func tmuxClient(ctx context.Context) int {
	out, err := exec.CommandContext(ctx, "tmux", "list-clients", "-F", "#{client_activity} #{client_pid}").Output()
	if err != nil {
		return 0
	}
	best, pid := 0, 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var at, p int
		if _, err := fmt.Sscan(l, &at, &p); err == nil && at >= best {
			best, pid = at, p
		}
	}
	return pid
}

func run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %s", name, msg)
		}
	}
	return err
}
