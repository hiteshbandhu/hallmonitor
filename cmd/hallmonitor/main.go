// hallmonitor: a read-only board of every coding agent running on this machine
// and, over SSH, on your other machines.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/adapters/claude"
	"github.com/hiteshbandhu/hallmonitor/internal/adapters/codex"
	"github.com/hiteshbandhu/hallmonitor/internal/adapters/demo"
	"github.com/hiteshbandhu/hallmonitor/internal/adapters/opencode"
	"github.com/hiteshbandhu/hallmonitor/internal/adapters/remote"
	"github.com/hiteshbandhu/hallmonitor/internal/hub"
	"github.com/hiteshbandhu/hallmonitor/internal/migrate"
	"github.com/hiteshbandhu/hallmonitor/internal/model"
	"github.com/hiteshbandhu/hallmonitor/internal/termimg"
	"github.com/hiteshbandhu/hallmonitor/internal/tui"
	"github.com/hiteshbandhu/hallmonitor/internal/usage"
)

var version = "dev"

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if self, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(self); err == nil {
			self = r
		}
		migrate.FromAgentboard(self)
	}
	if len(os.Args) > 1 && os.Args[1] == "statusline" {
		runStatusline(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		runHook(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "focus" {
		runFocus(context.Background(), os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "usage" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		runUsage(ctx, os.Args[2:])
		return
	}
	var (
		asJSON    = flag.Bool("json", false, "print a JSON snapshot and exit")
		once      = flag.Bool("once", false, "print a table and exit")
		stream    = flag.Bool("stream", false, "print a JSON snapshot per line every --watch (used over ssh)")
		watch     = flag.Duration("watch", 2*time.Second, "refresh interval")
		provider  = flag.String("provider", "", "comma-separated providers (claude,codex,opencode)")
		cwd       = flag.String("cwd", "", "only sessions under this directory")
		remoteCmd = flag.String("remote-cmd", "", "hallmonitor command on remote hosts (default: hallmonitor, or agentboard from before the rename)")
		noLocal   = flag.Bool("no-local", false, "only show remote hosts")
		render    = flag.String("render", "", "debug: render one TUI frame at WxH and exit")
		demoMode  = flag.Bool("demo", false, "show a synthetic fleet (for screenshots and trying it out)")
		images    = flag.String("images", "auto", "provider logos: auto, kitty, blocks, off")
		noFetch   = flag.Bool("no-fetch", false, "never download logos from the CDN")
		view      = flag.String("view", "agents", "screen to open on: agents or usage")
		withHosts = flag.Bool("with-hosts", false, "with --stream: include remote hosts (for the menu bar app)")
		noUsage   = flag.Bool("no-usage", false, "don't read agent logs for usage stats")
		showVer   = flag.Bool("version", false, "print version")
		hosts     multiFlag
	)
	flag.Var(&hosts, "host", "ssh destination to also watch (repeatable); also read from ~/.config/hallmonitor/hosts")
	flag.Parse()
	if *showVer {
		fmt.Println("hallmonitor", version)
		return
	}

	all := map[string]model.Adapter{
		"claude":   claude.Adapter{},
		"codex":    codex.Adapter{},
		"opencode": opencode.Adapter{},
	}
	order := []string{"claude", "codex", "opencode"}
	providers := order
	if *provider != "" {
		providers = nil
		for _, p := range strings.Split(*provider, ",") {
			p = strings.TrimSpace(p)
			if _, ok := all[p]; !ok {
				fmt.Fprintf(os.Stderr, "hallmonitor: unknown provider %q (have: %s)\n", p, strings.Join(order, ", "))
				os.Exit(2)
			}
			providers = append(providers, p)
		}
	}
	var adapters []model.Adapter
	if !*noLocal {
		for _, p := range providers {
			adapters = append(adapters, all[p])
		}
	}
	// Remote hosts don't apply to a plain --stream (that's what a remote
	// runs, and it should report only itself); the menu bar asks for them.
	if !*stream || *withHosts {
		live := !*asJSON && !*once || *stream
		for _, h := range append(readHostsFile(), hosts...) {
			adapters = append(adapters, remote.New(h, *remoteCmd, *watch, live))
		}
	}

	if *demoMode {
		adapters = []model.Adapter{&demo.Adapter{}}
	}

	dir := *cwd
	if strings.HasPrefix(dir, "~") {
		home, _ := os.UserHomeDir()
		dir = home + dir[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	h := hub.New(*watch, adapters...)

	switch {
	case *stream:
		// The menu bar app: keep Claude plan limits fresh even when every
		// session is in the desktop app, which has no status line.
		if *withHosts && !*noLocal && !*demoMode && slices.Contains(providers, "claude") {
			go probeLoop(ctx)
		}
		runStream(ctx, h, *watch)
	case *asJSON || *once:
		snap := h.Once(ctx)
		snap.Sessions = filterCWD(snap.Sessions, dir)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(snap)
		} else {
			printTable(snap)
		}
	case *render != "":
		var w, hh int
		if _, err := fmt.Sscanf(*render, "%dx%d", &w, &hh); err != nil {
			fmt.Fprintln(os.Stderr, "hallmonitor: --render wants WxH")
			os.Exit(2)
		}
		if *demoMode {
			ticks := 400
			if v, err := strconv.Atoi(os.Getenv("HALLMONITOR_DEMO_TICKS")); err == nil && v > 0 {
				ticks = v
			}
			for range ticks { // build up history instantly
				h.Once(ctx)
			}
		} else {
			h.Once(ctx)
			time.Sleep(*watch) // a second sample, so timelines and feed have data
			h.Once(ctx)
		}
		mode := termimg.Blocks // screenshots can't show kitty placements
		if *images != "auto" {
			mode = termimg.Detect(*images)
		}
		fmt.Print(tui.Render(h, tui.Options{CWD: dir, Images: mode, FetchIcon: !*noFetch, Usage: !*noUsage, StartView: *view}, w, hh))
	default:
		var cycle []string
		if *provider != "" {
			cycle = providers
		}
		opt := tui.Options{Providers: cycle, CWD: dir, Images: termimg.Detect(*images), FetchIcon: !*noFetch,
			Usage: !*noUsage, StartView: *view}
		if err := tui.Run(ctx, h, opt); err != nil {
			fmt.Fprintln(os.Stderr, "hallmonitor:", err)
			os.Exit(1)
		}
	}
}

func probeLoop(ctx context.Context) {
	l := usage.Open(usage.DefaultDir())
	for {
		l.MaybeProbeClaude(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func runStream(ctx context.Context, h *hub.Hub, every time.Duration) {
	enc := json.NewEncoder(os.Stdout)
	emit := func() bool { return enc.Encode(h.Snapshot()) == nil }
	h.Once(ctx)
	if !emit() {
		return
	}
	h.Run(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !emit() {
				return // ssh went away
			}
		}
	}
}

// readHostsFile reads ~/.config/hallmonitor/hosts: one ssh destination per
// line, # comments allowed.
func readHostsFile() []string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(dir, "hallmonitor", "hosts"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func filterCWD(in []model.Session, dir string) []model.Session {
	if dir == "" {
		return in
	}
	var out []model.Session
	for _, s := range in {
		if strings.HasPrefix(s.CWD, dir) {
			out = append(out, s)
		}
	}
	return out
}

func printTable(snap model.Snapshot) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tPROVIDER\tSTATUS\tTITLE\tCWD\tPID\tUPDATED")
	for _, s := range snap.Sessions {
		pid := "—"
		if s.PID > 0 {
			pid = fmt.Sprint(s.PID)
		}
		upd := "—"
		if t := s.LastSeen(); !t.IsZero() {
			upd = time.Since(t).Round(time.Second).String()
		}
		host := s.Host
		if host == "" {
			host = "local"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", host, s.Provider, s.Status, model.Snip(s.Title, 40), s.CWD, pid, upd)
	}
	w.Flush()
	for _, e := range snap.Errors {
		fmt.Fprintf(os.Stderr, "! %s: %s\n", e.Provider, e.Error)
	}
}
