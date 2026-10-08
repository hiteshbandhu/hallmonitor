// Package opencode reads opencode sessions: running `opencode` processes,
// matched to their sessions in opencode's SQLite database, whose newest
// messages tell us busy, asking, idle or failed. Both storage versions are
// read: the message/part tables the TUI writes, and the newer
// session_message table.
//
// It opens the database read-only and never talks to a running opencode.
package opencode

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/model"
	oc "github.com/hiteshbandhu/hallmonitor/internal/opencode"
	"github.com/hiteshbandhu/hallmonitor/internal/proc"
)

type Adapter struct {
	Home string // defaults to $HOME
}

func (Adapter) Name() string { return "opencode" }

// Subcommands that aren't an agent at work.
var tools = map[string]bool{
	"attach": true, // a client of a server we already see
	"db":     true, "session": true, "export": true, "import": true, "models": true, "providers": true,
	"auth": true, "account": true, "mcp": true, "agent": true, "upgrade": true, "uninstall": true,
	"stats": true, "debug": true, "generate": true, "github": true, "pr": true, "plug": true,
	"completion": true, "help": true,
}

type instance struct {
	proc.Proc
	cwd     string
	sub     string // subcommand, "" for the TUI
	session string // --session
	cont    bool   // --continue
}

func (i instance) server() bool { return i.sub == "serve" || i.sub == "web" || i.sub == "acp" }

func (a Adapter) Collect(ctx context.Context) ([]model.Session, error) {
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	procs, err := proc.List(pctx, "opencode", ".opencode", "opencode-cli")
	if err != nil {
		return nil, err
	}
	var insts []instance
	for _, p := range procs {
		in := parseArgs(p)
		if tools[in.sub] || strings.Contains(p.Args, "--version") {
			continue
		}
		in.cwd, _, _ = proc.OpenFiles(pctx, p.PID)
		insts = append(insts, in)
	}
	if len(insts) == 0 {
		return nil, nil
	}
	// Newest first: a new TUI in a folder takes that folder's newest session.
	sort.Slice(insts, func(i, j int) bool { return insts[i].Started.After(insts[j].Started) })

	now := time.Now()
	out, err := a.match(ctx, insts, now)
	if err != nil && len(out) == 0 {
		// No database yet (opencode never ran a prompt) still shows the
		// processes as idle.
		for _, in := range insts {
			if !in.server() {
				out = append(out, placeholder(in))
			}
		}
		if errors.Is(err, errNoDB) {
			err = nil
		}
	}
	return out, err
}

var errNoDB = errors.New("no opencode database")

func (a Adapter) match(ctx context.Context, insts []instance, now time.Time) ([]model.Session, error) {
	paths := oc.DBPaths(a.Home)
	if len(paths) == 0 {
		return nil, errNoDB
	}
	db, err := oc.Open(paths[0])
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	oldest := now
	for _, in := range insts {
		if in.Started.Before(oldest) {
			oldest = in.Started
		}
	}
	cands, err := oc.Sessions(qctx, db, oldest.Add(-time.Minute))
	if err != nil {
		return nil, err
	}
	claimed := map[string]bool{}
	var out []model.Session
	add := func(in instance, r oc.Row) {
		claimed[r.ID] = true
		out = append(out, describe(qctx, db, in, r, now))
	}
	// Sessions named on the command line first, then the rest by folder.
	for _, in := range insts {
		switch {
		case in.session != "":
			if r, err := oc.Session(qctx, db, in.session); err == nil {
				add(in, r)
			}
		case in.cont && in.cwd != "":
			if r, err := oc.Latest(qctx, db, in.cwd); err == nil && !claimed[r.ID] {
				add(in, r)
			}
		}
	}
	for _, in := range insts {
		if in.server() || in.session != "" || in.cont {
			continue
		}
		found := false
		for _, r := range cands {
			if !claimed[r.ID] && r.Directory == in.cwd && !r.Updated.Before(in.Started.Add(-2*time.Second)) {
				add(in, r)
				found = true
				break
			}
		}
		if !found {
			out = append(out, placeholder(in))
		}
	}
	// A server (opencode serve/web, the desktop app's sidecar) runs sessions
	// in any folder: show what it touched since it started.
	for _, in := range insts {
		if !in.server() {
			continue
		}
		for _, r := range cands {
			if !claimed[r.ID] && !r.Updated.Before(in.Started) {
				add(in, r)
			}
		}
	}
	return out, nil
}

func describe(ctx context.Context, db *sql.DB, in instance, r oc.Row, now time.Time) model.Session {
	s := model.Session{
		Provider:  "opencode",
		ID:        r.ID,
		PID:       in.PID,
		Title:     r.Title,
		CWD:       r.Directory,
		Status:    model.StatusIdle,
		Kind:      kind(in),
		Model:     r.Model,
		StartedAt: r.Created,
		UpdatedAt: r.Updated,
		Source:    "db",
		Extra:     map[string]string{"version": r.Version},
	}
	if oc.DefaultTitle(s.Title) {
		s.Title = ""
	}
	if s.CWD == "" {
		s.CWD = in.cwd
	}
	if r.Agent != "" {
		s.Extra["agent"] = r.Agent
	}
	st, err := oc.Read(ctx, db, r.ID, now)
	if err != nil {
		return s
	}
	s.Status, s.Since, s.Last, s.Prompt, s.Context, s.Activity, s.Subagents =
		st.Status, st.Since, st.Last, st.Prompt, st.Context, st.Activity, st.Sub
	if st.Model != "" {
		s.Model = st.Model
	}
	return s
}

// placeholder is an opencode with no session yet.
func placeholder(in instance) model.Session {
	return model.Session{
		Provider:  "opencode",
		PID:       in.PID,
		CWD:       in.cwd,
		Status:    model.StatusIdle,
		Kind:      kind(in),
		StartedAt: in.Started,
		Source:    "process",
	}
}

func kind(in instance) string {
	switch {
	case in.sub == "run":
		return "background"
	case in.server():
		return "server"
	}
	return "interactive"
}

// parseArgs reads the subcommand and session flags from a command line like
// "opencode run -s ses_x …" or "/path/opencode ~/code/app --continue".
func parseArgs(p proc.Proc) instance {
	in := instance{Proc: p}
	f := strings.Fields(p.Args)
	for i := 1; i < len(f); i++ {
		a := f[i]
		switch {
		case a == "-s" || a == "--session":
			if i+1 < len(f) {
				in.session = f[i+1]
				i++
			}
		case strings.HasPrefix(a, "--session="):
			in.session = strings.TrimPrefix(a, "--session=")
		case a == "-c" || a == "--continue":
			in.cont = true
		case strings.HasPrefix(a, "-"):
		case i == 1 && !strings.ContainsAny(a, "/.~"):
			in.sub = a
		}
	}
	return in
}
