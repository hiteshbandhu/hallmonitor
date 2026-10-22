// Package remote shows sessions from other machines over SSH. The remote side
// runs `hallmonitor --stream`, which prints one JSON snapshot per line; we keep
// one ssh connection per host open and reconnect with backoff. No daemon, no
// open ports: it rides on the user's ~/.ssh/config and agent.
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hiteshbandhu/hallmonitor/internal/model"
)

type Adapter struct {
	Target   string        // ssh destination, e.g. "dev@gpu-box" or a Host alias
	Command  string        // hallmonitor binary on the remote, default "hallmonitor"
	Interval time.Duration // remote refresh interval
	Stream   bool          // keep a live connection (TUI); false = one-shot

	once    sync.Once
	mu      sync.Mutex
	latest  model.Snapshot
	gotAt   time.Time
	lastErr error
}

func New(target, command string, interval time.Duration, stream bool) *Adapter {
	if command == "" {
		// Machines set up before the rename still have it as agentboard.
		command = `"$(command -v hallmonitor || command -v agentboard || echo hallmonitor)"`
	}
	return &Adapter{Target: target, Command: command, Interval: interval, Stream: stream}
}

// HostName is the label shown on the board: the destination without user@.
func HostName(target string) string {
	if i := strings.LastIndexByte(target, '@'); i >= 0 {
		target = target[i+1:]
	}
	return model.Clean(target)
}

func (a *Adapter) Name() string { return "host:" + HostName(a.Target) }

func (a *Adapter) sshArgs(remote string) []string {
	return []string{
		"-o", "BatchMode=yes", // never prompt for passwords from a TUI
		"-o", "ConnectTimeout=8",
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=3",
		"--", a.Target, remote,
	}
}

// ValidTarget refuses a destination ssh would read as an option (say
// "-oProxyCommand=…", which runs a local command) or that holds spaces or
// control characters.
func ValidTarget(target string) error {
	if target == "" || strings.HasPrefix(target, "-") || strings.ContainsFunc(target, func(r rune) bool {
		return r <= ' ' || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return fmt.Errorf("not an ssh destination: %q", target)
	}
	return nil
}

func (a *Adapter) Collect(ctx context.Context) ([]model.Session, error) {
	if err := ValidTarget(a.Target); err != nil {
		return nil, err
	}
	if !a.Stream {
		return a.oneShot(ctx)
	}
	a.once.Do(func() { go a.loop(ctx) })

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gotAt.IsZero() {
		if a.lastErr != nil {
			return nil, a.lastErr
		}
		return nil, errors.New("connecting…")
	}
	out := a.tag(a.latest)
	if age := time.Since(a.gotAt); age > 3*a.Interval+5*time.Second {
		msg := fmt.Sprintf("stale, last update %s ago", age.Round(time.Second))
		if a.lastErr != nil {
			msg += ": " + a.lastErr.Error()
		}
		return out, errors.New(msg)
	}
	return out, nil
}

func (a *Adapter) oneShot(ctx context.Context) ([]model.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", a.sshArgs(a.Command+" --json")...).Output()
	if err != nil {
		return nil, sshError(err)
	}
	var snap model.Snapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		return nil, fmt.Errorf("bad snapshot from %s: %w", a.Target, err)
	}
	return a.tag(snap), remoteErrors(snap)
}

func (a *Adapter) loop(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := a.stream(ctx)
		a.mu.Lock()
		a.lastErr = err
		a.mu.Unlock()
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second // it was healthy for a while
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (a *Adapter) stream(ctx context.Context) error {
	remote := fmt.Sprintf("%s --stream --watch %s", a.Command, a.Interval)
	cmd := exec.CommandContext(ctx, "ssh", a.sshArgs(remote)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var snap model.Snapshot
		if json.Unmarshal(sc.Bytes(), &snap) != nil {
			continue
		}
		a.mu.Lock()
		a.latest, a.gotAt, a.lastErr = snap, time.Now(), remoteErrors(snap)
		a.mu.Unlock()
	}
	err = cmd.Wait()
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return errors.New(lastLine(msg))
	}
	if err != nil {
		return sshError(err)
	}
	return errors.New("connection closed")
}

func (a *Adapter) tag(snap model.Snapshot) []model.Session {
	host := HostName(a.Target)
	out := make([]model.Session, len(snap.Sessions))
	for i, s := range snap.Sessions {
		s.Host = host
		// A question there is answered there; one shown here could only
		// be a fake, and its id would name a file here.
		s.Ask = nil
		out[i] = s
	}
	return out
}

func remoteErrors(snap model.Snapshot) error {
	if len(snap.Errors) == 0 {
		return nil
	}
	var parts []string
	for _, e := range snap.Errors {
		parts = append(parts, e.Provider+": "+e.Error)
	}
	return errors.New(strings.Join(parts, "; "))
}

func sshError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return errors.New(lastLine(strings.TrimSpace(string(ee.Stderr))))
	}
	return err
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
