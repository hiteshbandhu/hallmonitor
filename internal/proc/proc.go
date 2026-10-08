// Package proc has small helpers for running CLIs and inspecting processes.
package proc

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Run executes a command with the context deadline and returns stdout.
func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out.Bytes(), nil
}

// Alive reports whether pid exists (EPERM still means it exists).
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

type Proc struct {
	PID     int
	Comm    string // executable basename
	Args    string
	Started time.Time
}

// List returns processes whose executable basename is one of names.
func List(ctx context.Context, names ...string) ([]Proc, error) {
	// lstart is always five fields: "Tue Sep 29 16:33:10 2026". ucomm is the
	// executable's name; macOS cuts comm (its full path) to 16 characters
	// when other columns follow, so /opt/homebrew/bin/codex never matched.
	out, err := Run(ctx, "ps", "-axo", "pid=,lstart=,ucomm=,args=")
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var ps []Proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		started, _ := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.Join(f[1:6], " "), time.Local)
		comm := f[6]
		if i := strings.LastIndexByte(comm, '/'); i >= 0 {
			comm = comm[i+1:]
		}
		if !want[comm] {
			continue
		}
		ps = append(ps, Proc{PID: pid, Comm: comm, Args: strings.Join(f[7:], " "), Started: started})
	}
	return ps, nil
}

// OpenFiles returns the cwd and the paths of regular files pid has open.
func OpenFiles(ctx context.Context, pid int) (cwd string, files []string, err error) {
	out, err := Run(ctx, "lsof", "-n", "-P", "-p", strconv.Itoa(pid), "-Ffn")
	if err != nil && len(out) == 0 {
		return "", nil, err
	}
	var fd string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'f':
			fd = line[1:]
		case 'n':
			if fd == "cwd" {
				cwd = line[1:]
			} else {
				files = append(files, line[1:])
			}
		}
	}
	return cwd, files, nil
}
