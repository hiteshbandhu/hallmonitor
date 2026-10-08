// Package tui is the fullscreen board: a status wall meant to live on a
// second display and be read from across the room.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/hiteshbandhu/hallmonitor/internal/focus"
	"github.com/hiteshbandhu/hallmonitor/internal/hub"
	"github.com/hiteshbandhu/hallmonitor/internal/icons"
	"github.com/hiteshbandhu/hallmonitor/internal/model"
	"github.com/hiteshbandhu/hallmonitor/internal/termimg"
	"github.com/hiteshbandhu/hallmonitor/internal/usage"
)

type Options struct {
	Providers []string     // filter cycle; empty = claude, codex, opencode
	CWD       string       // prefix filter
	Images    termimg.Mode // how to draw provider logos
	FetchIcon bool         // allow fetching logos from the CDN
	Usage     bool         // keep the usage ledger updated and show it
	StartView string       // "agents" (default) or "usage"
}

func Run(ctx context.Context, h *hub.Hub, opt Options) error {
	h.Run(ctx)
	m := newModel(h, opt)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	if opt.Images != termimg.Off {
		go func() { p.Send(loadLogos(ctx, opt)) }()
	}
	if opt.Usage {
		go usageLoop(ctx, p)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.Updates():
				p.Send(refreshMsg{})
			}
		}
	}()
	_, err := p.Run()
	return err
}

// Render draws one frame, for screenshots and tests.
func Render(h *hub.Hub, opt Options, w, ht int) string {
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	m := newModel(h, opt)
	m.w, m.h = w, ht
	m.frame = 7
	if v, err := strconv.Atoi(os.Getenv("HALLMONITOR_FRAME")); err == nil {
		m.frame = v // debug: pick the animation frame for screenshots
	}
	if opt.Images != termimg.Off {
		lm := loadLogos(context.Background(), opt)
		m.logos = lm.logos
	}
	if opt.Usage {
		l := usage.Open(usage.DefaultDir())
		err := l.Update(context.Background(), nil)
		if err == usage.ErrBusy {
			err = nil
		}
		um := summarize(l, err)
		m.usage, m.usageErr = &um, um.err
	}
	return m.View()
}

// ---- logos ----

type logo struct {
	small, large []string // 4×2 and 8×4 cells
	tiny         string   // 2×1 cells, for one-line spots like the top bar
}

type logosMsg struct {
	logos    map[string]logo
	transmit string // kitty uploads, written once
}

const (
	smallW, smallH = 4, 2
	largeW, largeH = 8, 4
)

func loadLogos(ctx context.Context, opt Options) logosMsg {
	out := logosMsg{logos: map[string]logo{}}
	for i, p := range []string{"claude", "codex", "opencode"} {
		ic, err := icons.Load(ctx, p, opt.FetchIcon)
		if err != nil {
			continue
		}
		img := termimg.Downscale(ic.Image, 128)
		switch opt.Images {
		case termimg.Kitty:
			idS, idL, idT := uint32(0xA1B000+i*3+1), uint32(0xA1B000+i*3+2), uint32(0xA1B000+i*3+3)
			out.transmit += termimg.Transmit(idS, img, smallW, smallH) + termimg.Transmit(idL, img, largeW, largeH) +
				termimg.Transmit(idT, img, 2, 1)
			out.logos[p] = logo{small: termimg.Cells(idS, smallW, smallH), large: termimg.Cells(idL, largeW, largeH),
				tiny: termimg.Cells(idT, 2, 1)[0]}
		case termimg.Blocks:
			out.logos[p] = logo{small: termimg.HalfBlocks(img, smallW, smallH), large: termimg.HalfBlocks(img, largeW, largeH)}
		}
	}
	return out
}

type refreshMsg struct{}
type tickMsg time.Time

type uiModel struct {
	hub       *hub.Hub
	snap      model.Snapshot
	events    []hub.Event
	hosts     map[string]error
	all       []model.Session // after filters, stale included
	vis       []model.Session // cards on the wall, in wall order
	stale     int
	sel       string // selected session key
	filters   []string
	filterIx  int
	showStale bool
	cwd       string
	w, h      int
	frame     int
	home      string
	hostname  string
	interval  time.Duration
	logos     map[string]logo
	transmit  string // pending kitty uploads
	txFrames  int    // frames left to keep sending them

	showUsage     bool
	usage         *usageMsg
	usageErr      error
	usageProgress float64
	usageDays     int
}

func newModel(h *hub.Hub, opt Options) *uiModel {
	home, _ := os.UserHomeDir()
	hn := machineName()
	if v := os.Getenv("HALLMONITOR_HOSTNAME"); v != "" {
		hn = v // debug: screenshots without the real machine name
	}
	f := []string{""}
	if len(opt.Providers) > 0 {
		f = append(f, opt.Providers...)
	} else {
		f = append(f, "claude", "codex", "opencode")
	}
	m := &uiModel{hub: h, filters: f, cwd: opt.CWD, home: home, hostname: hn, interval: 2 * time.Second,
		usageDays: 7, showUsage: opt.StartView == "usage"}
	m.refresh()
	return m
}

func (m *uiModel) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case refreshMsg:
		m.refresh()
	case usageMsg:
		m.usage, m.usageErr = &msg, msg.err
	case limitsMsg:
		if m.usage != nil {
			m.usage.today.RateLimits, m.usage.week.RateLimits, m.usage.month.RateLimits = msg, msg, msg
		}
	case usageProgressMsg:
		m.usageProgress = float64(msg)
	case logosMsg:
		m.logos = msg.logos
		// Keep the upload in the frame for a couple of seconds: the renderer
		// only writes lines that changed, so it goes out once, whenever the
		// next flush happens.
		m.transmit, m.txFrames = msg.transmit, 25
	case tickMsg:
		m.frame++
		if m.txFrames > 0 {
			m.txFrames--
		}
		return m, tick()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.showUsage {
				m.showUsage = false
				return m, nil
			}
			return m, tea.Quit
		case "u":
			m.showUsage = !m.showUsage
		case "t":
			if m.usageDays == 7 {
				m.usageDays = 30
			} else {
				m.usageDays = 7
			}
		case "right", "l", "tab":
			m.moveFlat(1)
		case "left", "h", "shift+tab":
			m.moveFlat(-1)
		case "down", "j":
			m.moveRow(1)
		case "up", "k":
			m.moveRow(-1)
		case "f":
			m.filterIx = (m.filterIx + 1) % len(m.filters)
			m.refresh()
		case "s":
			m.showStale = !m.showStale
			m.refresh()
		case "r":
			go m.hub.Once(context.Background())
		case "enter", "o":
			if i := m.index(); i >= 0 && !m.showUsage {
				s := m.vis[i]
				go func() {
					_, _ = focus.Focus(context.Background(), focus.Target{Host: s.Host, Provider: s.Provider,
						ID: s.ID, PID: s.PID, Entrypoint: s.Extra["entrypoint"]})
				}()
			}
		}
	}
	return m, nil
}

func (m *uiModel) refresh() {
	m.snap = m.hub.Snapshot()
	m.events = m.hub.Events()
	m.hosts = m.hub.Hosts()
	want := m.filters[m.filterIx]
	now := time.Now()
	m.all, m.vis, m.stale = nil, nil, 0
	for _, s := range m.snap.Sessions {
		if want != "" && s.Provider != want {
			continue
		}
		if m.cwd != "" && !strings.HasPrefix(s.CWD, m.cwd) {
			continue
		}
		m.all = append(m.all, s)
		if s.Stale(now) {
			m.stale++
		}
	}
	for _, sec := range m.sections() {
		m.vis = append(m.vis, sec.items...)
	}
	if m.index() < 0 && len(m.vis) > 0 {
		m.sel = m.vis[0].Key()
	}
}

func (m *uiModel) index() int {
	for i, s := range m.vis {
		if s.Key() == m.sel {
			return i
		}
	}
	return -1
}

func (m *uiModel) moveFlat(d int) {
	if len(m.vis) == 0 {
		return
	}
	i := max(0, min(len(m.vis)-1, m.index()+d))
	m.sel = m.vis[i].Key()
}

// moveRow moves to the card in the previous/next grid row, keeping the column.
func (m *uiModel) moveRow(d int) {
	rows := m.gridRows(m.cols(m.mainWidth()))
	for r, row := range rows {
		for c, i := range row {
			if m.vis[i].Key() != m.sel {
				continue
			}
			nr := r + d
			if nr < 0 || nr >= len(rows) {
				return
			}
			m.sel = m.vis[rows[nr][min(c, len(rows[nr])-1)]].Key()
			return
		}
	}
}

// ---- sections & grid ----

type section struct {
	title string
	color lipgloss.TerminalColor
	items []model.Session
}

func (m *uiModel) sections() []section {
	now := time.Now()
	secs := []section{
		{title: "NEEDS YOU", color: cAmber},
		{title: "WORKING", color: cGreen},
		{title: "IDLE", color: cMuted},
		{title: "STALE", color: cFaint},
	}
	for _, s := range m.all {
		switch {
		case s.Status == model.StatusWaiting || s.Status == model.StatusError:
			secs[0].items = append(secs[0].items, s)
		case s.Status == model.StatusBusy:
			secs[1].items = append(secs[1].items, s)
		case s.Stale(now):
			if m.showStale {
				secs[3].items = append(secs[3].items, s)
			}
		default:
			secs[2].items = append(secs[2].items, s)
		}
	}
	var out []section
	for _, s := range secs {
		if len(s.items) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// gridRows returns card indices (into m.vis) laid out row by row, with each
// section starting a new row.
func (m *uiModel) gridRows(cols int) [][]int {
	var rows [][]int
	i := 0
	for _, sec := range m.sections() {
		for j := 0; j < len(sec.items); j += cols {
			var row []int
			for k := j; k < min(j+cols, len(sec.items)); k++ {
				row = append(row, i+k)
			}
			rows = append(rows, row)
		}
		i += len(sec.items)
	}
	return rows
}

const (
	minCardW = 48
	cardH    = 7
	gap      = 2
	railW    = 46
	margin   = 2
)

func (m *uiModel) hasRail() bool { return m.w >= 130 }

func (m *uiModel) mainWidth() int {
	w := m.w - 2*margin
	if m.hasRail() {
		w -= railW + 3
	}
	return max(20, w)
}

func (m *uiModel) cols(mainW int) int {
	return max(1, (mainW+gap)/(minCardW+gap))
}

// ---- palette ----

type ac = lipgloss.AdaptiveColor

var (
	cFg     = ac{Light: "#18181b", Dark: "#ececec"}
	cMuted  = ac{Light: "#6b6b73", Dark: "#8d8d96"}
	cFaint  = ac{Light: "#c4c4cc", Dark: "#3a3a42"}
	cGreen  = ac{Light: "#15803d", Dark: "#4ade80"}
	cGreenD = ac{Light: "#86c79c", Dark: "#1f5f38"}
	cGlow   = ac{Light: "#0f5132", Dark: "#d9ffe6"}
	cAmber  = ac{Light: "#b45309", Dark: "#fbbf24"}
	cAmberD = ac{Light: "#e0b27a", Dark: "#7a5a12"}
	cRed    = ac{Light: "#b91c1c", Dark: "#f87171"}
	cViolet = ac{Light: "#6d28d9", Dark: "#a78bfa"}

	// Provider colors: Claude's clay orange, Codex's cool blue, opencode's
	// monochrome as a warm stone.
	cClaude   = ac{Light: "#b3532f", Dark: "#d97757"}
	cCodex    = ac{Light: "#0369a1", Dark: "#7dd3fc"}
	cOpencode = ac{Light: "#57534e", Dark: "#d6d3d1"}
)

func fg(c lipgloss.TerminalColor) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

func statusColor(s model.Status) lipgloss.TerminalColor {
	switch s {
	case model.StatusBusy:
		return cGreen
	case model.StatusWaiting:
		return cAmber
	case model.StatusError:
		return cRed
	case model.StatusBlocked:
		return cViolet
	}
	return cMuted
}

func providerColor(p string) lipgloss.TerminalColor {
	switch p {
	case "claude":
		return cClaude
	case "codex":
		return cCodex
	case "opencode":
		return cOpencode
	}
	return cMuted
}

// providerMark is each tool's own mark: the ✻ Claude Code shows, the >_
// prompt Codex uses, and a block for opencode's pixel logo.
func providerMark(p string) string {
	switch p {
	case "claude":
		return "✻ Claude"
	case "codex":
		return ">_ Codex"
	case "opencode":
		return "▣ opencode"
	}
	return p
}

// providerChip is the provider's mark set in its brand colors, crisp in any
// terminal since it's just glyphs.
func providerChip(p string) string {
	switch p {
	case "claude":
		return lipgloss.NewStyle().Bold(true).
			Foreground(ac{Light: "#fffaf5", Dark: "#fff8f2"}).
			Background(ac{Light: "#c96442", Dark: "#d97757"}).
			Render(" ✻ Claude ")
	case "codex":
		return lipgloss.NewStyle().Bold(true).
			Foreground(ac{Light: "#e0f2fe", Dark: "#7dd3fc"}).
			Background(ac{Light: "#0c4a6e", Dark: "#1f2937"}).
			Render(" >_ Codex ")
	case "opencode":
		return lipgloss.NewStyle().Bold(true).
			Foreground(ac{Light: "#fafaf9", Dark: "#e7e5e4"}).
			Background(ac{Light: "#292524", Dark: "#292524"}).
			Render(" ▣ opencode ")
	}
	return fg(cMuted).Render(" " + p + " ")
}

// gradient maps 0..1 through teal → green → yellow.
func gradient(t float64) lipgloss.TerminalColor {
	stops := [][3]float64{{45, 212, 191}, {74, 222, 128}, {250, 204, 21}}
	t = max(0, min(1, t))
	seg := t * float64(len(stops)-1)
	i := min(int(seg), len(stops)-2)
	f := seg - float64(i)
	a, b := stops[i], stops[i+1]
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x",
		int(a[0]+(b[0]-a[0])*f), int(a[1]+(b[1]-a[1])*f), int(a[2]+(b[2]-a[2])*f)))
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// pulse alternates between two shades, for things that need attention.
func (m *uiModel) pulse(a, b lipgloss.TerminalColor) lipgloss.TerminalColor {
	if (m.frame/6)%2 == 1 {
		return b
	}
	return a
}

// ---- view ----

func (m *uiModel) View() string {
	if m.w == 0 {
		return ""
	}
	if m.showUsage {
		out := m.usageView()
		if m.txFrames > 0 && m.transmit != "" {
			out = m.transmit + out
		}
		return out
	}
	compact := m.h < 28 || m.w < 80
	var b []string
	b = append(b, m.topBar())
	if compact {
		b = append(b, m.summary())
	} else {
		b = append(b, "")
		b = append(b, m.hero()...)
	}
	b = append(b, "")
	bodyH := max(1, m.h-len(b)-2)
	body := m.wall(m.mainWidth(), bodyH)
	if m.hasRail() {
		rail := m.rail(railW, bodyH)
		for i := range body {
			body[i] = pad(body[i], m.mainWidth()) + "   " + rail[i]
		}
	}
	for _, l := range body {
		b = append(b, strings.Repeat(" ", margin)+l)
	}
	b = append(b, "", m.footer())
	// Never let a line wrap or the frame scroll: that shears the whole board.
	if len(b) > m.h {
		b = append(b[:m.h-1], b[len(b)-1])
	}
	for i := range b {
		b[i] = fitStyled(b[i], m.w)
	}
	if m.txFrames > 0 && m.transmit != "" {
		b[0] = m.transmit + b[0] // zero-width APC sequences
	}
	return strings.Join(b, "\n")
}

// machines maps each remote host to its connection error (nil = healthy).
func (m *uiModel) machines() map[string]error {
	out := map[string]error{}
	for name, err := range m.hosts {
		if h, ok := strings.CutPrefix(name, "host:"); ok {
			out[h] = err
		}
	}
	for _, s := range m.snap.Sessions {
		if _, ok := out[s.Host]; s.Host != "" && !ok {
			out[s.Host] = nil
		}
	}
	return out
}

func (m *uiModel) topBar() string {
	brand := lipgloss.NewStyle().Bold(true).Foreground(cFg).Render("hallmonitor")
	left := strings.Repeat(" ", margin-1) + fg(gradient(0.3)).Render("▍") + brand

	var chips []string
	for name, err := range m.machines() {
		dot := fg(cGreen).Render("●")
		if err != nil {
			dot = fg(cRed).Render("●")
			if strings.Contains(err.Error(), "connecting") {
				dot = fg(cAmber).Render("●")
			}
		}
		chips = append(chips, dot+" "+fg(cMuted).Render(name))
	}
	sort.Strings(chips)
	chips = append([]string{fg(cGreen).Render("●") + " " + fg(cMuted).Render(m.hostname)}, chips...)
	mid := strings.Join(chips, "   ")

	now := time.Now()
	limits := m.limitChips()
	right := limits + fg(cMuted).Render(now.Format("Mon 2 Jan")+"  ") +
		lipgloss.NewStyle().Bold(true).Foreground(cFg).Render(now.Format("15:04:05")) + strings.Repeat(" ", margin)
	if m.w < 100 {
		right = limits + lipgloss.NewStyle().Bold(true).Foreground(cFg).Render(now.Format("15:04")) + strings.Repeat(" ", margin)
	}
	if lipgloss.Width(left)+lipgloss.Width(mid)+lipgloss.Width(right)+6 > m.w {
		mid = ""
	}
	space := m.w - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right)
	l := max(3, space/2)
	return left + strings.Repeat(" ", l) + mid + strings.Repeat(" ", max(1, space-l)) + right
}

// limitChips shows plan limits at a glance: "✻ 5h 23%  ✻ 7d 71%  >_ 30d 0%".
func (m *uiModel) limitChips() string {
	if m.usage == nil || len(m.usage.today.RateLimits) == 0 {
		return ""
	}
	rl := m.usage.today.RateLimits
	keys := make([]string, 0, len(rl))
	for k := range rl {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		l := rl[k]
		var c lipgloss.TerminalColor = cGreen
		switch {
		case l.UsedPercent >= 80:
			c = cRed
		case l.UsedPercent >= 50:
			c = cAmber
		}
		// The real app icon where the terminal can draw images, else its mark.
		mark := fg(providerColor(l.Provider)).Render(strings.Fields(providerMark(l.Provider))[0])
		if lg, ok := m.logos[l.Provider]; ok && lg.tiny != "" {
			mark = lg.tiny
		}
		win := ""
		if l.WindowMin > 0 {
			win = " " + shortWindow(l.WindowMin)
		}
		pct := lipgloss.NewStyle().Bold(true).Foreground(c).Render(fmt.Sprintf("%.0f%%", l.UsedPercent))
		if l.Stale(time.Now()) {
			// An old reading: say so rather than pass it off as live.
			pct = fg(cMuted).Render(fmt.Sprintf("~%.0f%%", l.UsedPercent))
		}
		parts = append(parts, mark+fg(cMuted).Render(win+" ")+pct)
	}
	return strings.Join(parts, "   ") + "     "
}

func (m *uiModel) counts() (waiting, busy, idle int) {
	now := time.Now()
	for _, s := range m.all {
		switch s.Status {
		case model.StatusWaiting, model.StatusError:
			waiting++
		case model.StatusBusy:
			busy++
		default:
			if !s.Stale(now) {
				idle++
			}
		}
	}
	return
}

// summary is the one-line hero for small terminals.
func (m *uiModel) summary() string {
	waiting, busy, idle := m.counts()
	n := func(v int, label string, c lipgloss.TerminalColor) string {
		if v == 0 {
			c = cMuted
		}
		return fg(c).Bold(true).Render(fmt.Sprint(v)) + " " + fg(cMuted).Render(label)
	}
	return strings.Repeat(" ", margin) + n(waiting, "need you", cAmber) + "   " + n(busy, "working", cGreen) + "   " + n(idle, "idle", cFg)
}

// hero is the big-number band plus the fleet waveform. Digits are five rows
// tall on big screens, three on smaller ones.
func (m *uiModel) hero() []string {
	waiting, busy, idle := m.counts()
	rows := 3
	if m.h >= 40 {
		rows = 5
	}
	var wc lipgloss.TerminalColor = cMuted
	if waiting > 0 {
		wc = m.pulse(cAmber, cAmberD)
	}
	tiles := [][]string{
		tile("NEEDS YOU", waiting, rows, func(int) lipgloss.TerminalColor { return wc }),
		tile("WORKING", busy, rows, func(r int) lipgloss.TerminalColor {
			if busy == 0 {
				return cMuted
			}
			return gradient(1 - float64(r)/float64(rows)) // yellow-green top, teal bottom
		}),
		tile("IDLE", idle, rows, func(int) lipgloss.TerminalColor { return cMuted }),
	}
	if running, any := m.subagents(); any {
		tiles = append(tiles, tile("SUBAGENTS", running, rows, func(r int) lipgloss.TerminalColor {
			if running == 0 {
				return cMuted
			}
			return gradient(1 - float64(r)/float64(rows))
		}))
	}
	tiles = append(tiles, [][]string{
		tile("MACHINES", 1+len(m.machines()), rows, func(int) lipgloss.TerminalColor { return cFg }),
	}...)
	out := make([]string, rows+2)
	for _, t := range tiles {
		for i := range out {
			out[i] += t[i]
		}
	}
	used := lipgloss.Width(out[0])
	waveW := m.w - used - 2*margin - 2
	if waveW >= 20 {
		wave := m.waveform(waveW, rows)
		for i := range out {
			out[i] += "  " + wave[i]
		}
	}
	for i := range out {
		out[i] = strings.Repeat(" ", margin) + out[i]
	}
	return out
}

// subagents is how many subagents are running across the fleet, and whether
// any session ever started one (so the tile only shows for people who use them).
func (m *uiModel) subagents() (running int, any bool) {
	for _, s := range m.all {
		if s.Subagents != nil && s.Subagents.Total > 0 {
			any = true
			if s.Status == model.StatusBusy || s.Status == model.StatusWaiting {
				running += s.Subagents.Running
			}
		}
	}
	return running, any
}

func tile(label string, n, rows int, color func(row int) lipgloss.TerminalColor) []string {
	digits := bigNumber(n, rows)
	w := max(lipgloss.Width(label), lipgloss.Width(digits[0])) + 6
	out := []string{pad(fg(cMuted).Render(label), w)}
	for r, d := range digits {
		out = append(out, pad(fg(color(r)).Bold(true).Render(d), w))
	}
	return append(out, strings.Repeat(" ", w))
}

// waveform is a bar chart of how many agents were working per sample,
// colored by height.
func (m *uiModel) waveform(w, rows int) []string {
	counts := make([]int, w)
	for _, s := range m.all {
		h := s.History
		for i := 0; i < len(h) && i < w; i++ {
			if st := h[len(h)-1-i]; st == model.StatusBusy || st == model.StatusWaiting {
				counts[w-1-i]++
			}
		}
	}
	// Scale to at least 3 so a lone agent is a third of the height, not a
	// wall of full bars.
	peak := 3
	for _, c := range counts {
		peak = max(peak, c)
	}
	longest := 0
	for _, s := range m.all {
		longest = max(longest, len(s.History))
	}
	span := time.Duration(min(w, longest)) * m.interval
	label := fg(cMuted).Render("FLEET ACTIVITY") + fg(cFaint).Render("  last "+dur(span))
	levels := []rune(" ▁▂▃▄▅▆▇█")
	out := []string{label}
	steps := rows * 8
	for r := 0; r < rows; r++ {
		color := gradient(float64(rows-1-r) / float64(max(1, rows-1)))
		var sb strings.Builder
		for _, c := range counts {
			v := c * steps / peak
			if c > 0 && v == 0 {
				v = 1
			}
			lv := max(0, min(8, v-(rows-1-r)*8))
			if lv == 0 {
				if r == rows-1 {
					sb.WriteString(fg(cFaint).Render("▁"))
				} else {
					sb.WriteString(" ")
				}
				continue
			}
			sb.WriteString(fg(color).Render(string(levels[lv])))
		}
		out = append(out, sb.String())
	}
	peakN := 0
	for _, c := range counts {
		peakN = max(peakN, c)
	}
	return append(out, fg(cFaint).Render(pad(fmt.Sprintf("peak %d working", peakN), w)))
}

func (m *uiModel) wall(w, h int) []string {
	secs := m.sections()
	if len(secs) == 0 {
		return m.empty(w, h)
	}
	cols := m.cols(w)
	cw := (w - gap*(cols-1)) / cols
	var lines []string
	selTop, selBot := 0, 0
	for _, sec := range secs {
		head := fg(sec.color).Bold(true).Render(sec.title) + " " + fg(cMuted).Render(fmt.Sprint(len(sec.items))) + " "
		lines = append(lines, head+fg(cFaint).Render(strings.Repeat("─", max(0, w-lipgloss.Width(head)))))
		for j := 0; j < len(sec.items); j += cols {
			row := make([]string, cardH)
			for k := j; k < min(j+cols, len(sec.items)); k++ {
				s := sec.items[k]
				selected := s.Key() == m.sel
				if selected {
					selTop, selBot = len(lines), len(lines)+cardH
				}
				c := m.card(s, cw, selected, k)
				for i := range row {
					if k > j {
						row[i] += strings.Repeat(" ", gap)
					}
					row[i] += c[i]
				}
			}
			lines = append(lines, row...)
		}
		lines = append(lines, "")
	}
	// Scroll so the selected card is on screen.
	off := 0
	if selBot > h {
		off = selBot - h
	}
	if selTop < off {
		off = selTop
	}
	lines = lines[min(off, len(lines)):]
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

func (m *uiModel) empty(w, h int) []string {
	out := make([]string, h)
	msg := []string{
		lipgloss.NewStyle().Bold(true).Foreground(cFg).Render("All quiet."),
		fg(cMuted).Render("No Claude Code, Codex or opencode sessions running right now."),
		fg(cFaint).Render("Start one in any terminal and it shows up here within seconds."),
	}
	top := max(0, h/2-2)
	for i, l := range msg {
		if top+i < h {
			out[top+i] = strings.Repeat(" ", max(0, (w-lipgloss.Width(l))/2)) + l
		}
	}
	return out
}

func (m *uiModel) card(s model.Session, w int, selected bool, n int) []string {
	sc := statusColor(s.Status)
	var border lipgloss.TerminalColor = cFaint
	switch s.Status {
	case model.StatusBusy:
		border = cGreenD
	case model.StatusWaiting:
		border = m.pulse(cAmber, cAmberD)
	case model.StatusError:
		border = cRed
	}
	tl, tr, bl, br, hz, vt := "╭", "╮", "╰", "╯", "─", "│"
	if selected {
		tl, tr, bl, br, hz, vt = "┏", "┓", "┗", "┛", "━", "┃"
		border = cFg
		if s.Status == model.StatusBusy || s.Status == model.StatusWaiting || s.Status == model.StatusError {
			border = sc
		}
	}
	bs := fg(border)

	// Top border: ─ status ───── provider ─
	glyph := statusGlyph(s.Status)
	switch s.Status {
	case model.StatusBusy:
		glyph = spinner[(m.frame+n*3)%len(spinner)]
	case model.StatusWaiting:
		if (m.frame/6)%2 == 1 {
			glyph = "◇"
		}
	}
	label := glyph + " " + statusWord(s.Status)
	if !s.Since.IsZero() {
		label += " · " + dur(time.Since(s.Since))
	}
	left := fg(sc).Bold(true).Render(label)
	right := providerChip(s.Provider)
	if s.Host != "" {
		right = fg(cMuted).Render(s.Host+" · ") + right
	}
	fill := w - 2 - 4 - lipgloss.Width(left) - lipgloss.Width(right) - 2
	top := bs.Render(tl+hz+" ") + left + " " + bs.Render(strings.Repeat(hz, max(1, fill))) + " " + right + bs.Render(" "+hz+tr)
	if lipgloss.Width(top) > w { // very narrow: drop the provider tag
		fill = w - 2 - 4 - lipgloss.Width(left)
		top = bs.Render(tl+hz+" ") + left + " " + bs.Render(strings.Repeat(hz, max(1, fill))+tr)
	}

	iw := w - 4
	line := func(content string) string {
		return bs.Render(vt) + " " + pad(content, iw) + " " + bs.Render(vt)
	}

	title := s.Title
	if title == "" {
		title = filepath.Base(s.CWD)
	}
	titleL := lipgloss.NewStyle().Bold(true).Foreground(cFg).Render(fit(title, iw))

	var meta []string
	if p := filepath.Base(s.CWD); p != "" && p != "." && p != "/" {
		meta = append(meta, p)
	}
	if s.Model != "" {
		meta = append(meta, shortModel(s.Model))
	}
	if s.Context > 0 {
		meta = append(meta, fmt.Sprintf("%dk ctx", (s.Context+500)/1000))
	}
	if s.Kind == "background" {
		meta = append(meta, "bg")
	}
	metaText := func(w int) string {
		tag, tagW := subagentTag(s.Subagents)
		if tag == "" || w-tagW-2 < 12 {
			return fg(cMuted).Render(fit(strings.Join(meta, " · "), w))
		}
		// fit pads, so the tag sits at the right edge.
		return fg(cMuted).Render(fit(strings.Join(meta, " · "), w-tagW-2)) + "  " + tag
	}
	metaL := metaText(iw)

	actL := ""
	if s.Last != "" {
		var ac lipgloss.TerminalColor = cMuted
		if s.Status == model.StatusBusy {
			ac = cFg
		}
		mark, text := "▸ ", s.Last
		if strings.HasPrefix(text, "↳ ") { // a reply, not a tool call
			mark, text = "↳ ", strings.TrimPrefix(text, "↳ ")
		}
		actL = fg(sc).Render(mark) + fg(ac).Render(fit(text, iw-2))
	}
	promptL := ""
	if s.Prompt != "" {
		promptL = fg(cMuted).Italic(true).Render(fit("“"+s.Prompt+"”", iw))
	}

	if lg, ok := m.logos[s.Provider]; ok && iw > 30 {
		tw := iw - smallW - 2
		titleL = lg.small[0] + "  " + lipgloss.NewStyle().Bold(true).Foreground(cFg).Render(fit(title, tw))
		metaL = lg.small[1] + "  " + metaText(tw)
	}

	return []string{
		top,
		line(titleL),
		line(metaL),
		line(actL),
		line(promptL),
		line(timeline(s.History, iw)),
		m.bottomBorder(s, w, bl, hz, br, bs, n),
	}
}

// subagentTag is "3/12 subagents" while some run (bright), "12 subagents"
// once they're all done (muted), and its width.
func subagentTag(sa *model.Subagents) (string, int) {
	if sa == nil || sa.Total == 0 {
		return "", 0
	}
	word := "subagents"
	if sa.Total == 1 {
		word = "subagent"
	}
	if sa.Running == 0 {
		t := fmt.Sprintf("%d %s", sa.Total, word)
		return fg(cMuted).Render(t), len(t)
	}
	t := fmt.Sprintf("%d/%d %s", sa.Running, sa.Total, word)
	return fg(cGreen).Bold(true).Render(t), len(t)
}

// bottomBorder is plain, except on working cards where a glint sweeps along
// it, so busy agents visibly move.
func (m *uiModel) bottomBorder(s model.Session, w int, bl, hz, br string, bs lipgloss.Style, n int) string {
	if s.Status != model.StatusBusy {
		return bs.Render(bl + strings.Repeat(hz, w-2) + br)
	}
	const glint = 10
	span := w - 2 + glint
	pos := (m.frame*2 + n*17) % span
	var sb strings.Builder
	sb.WriteString(bs.Render(bl))
	for i := 0; i < w-2; i++ {
		d := i - (pos - glint)
		if d >= 0 && d < glint {
			// Brightest at the head of the glint, fading behind it.
			c := gradient(0.2 + 0.6*float64(d)/float64(glint))
			if d >= glint-2 {
				c = cGlow
			}
			sb.WriteString(fg(c).Render(hz))
			continue
		}
		sb.WriteString(bs.Render(hz))
	}
	sb.WriteString(bs.Render(br))
	return sb.String()
}

func timeline(h []model.Status, w int) string {
	var sb strings.Builder
	start := len(h) - w
	for i := 0; i < w; i++ {
		j := start + i
		if j < 0 {
			sb.WriteString(fg(cFaint).Render("·"))
			continue
		}
		switch h[j] {
		case model.StatusBusy:
			sb.WriteString(fg(cGreen).Render("▆"))
		case model.StatusWaiting:
			sb.WriteString(fg(cAmber).Render("▆"))
		case model.StatusError:
			sb.WriteString(fg(cRed).Render("▆"))
		case model.StatusBlocked:
			sb.WriteString(fg(cViolet).Render("▁"))
		default:
			sb.WriteString(fg(cFaint).Render("▁"))
		}
	}
	return sb.String()
}

func (m *uiModel) rail(w, h int) []string {
	var out []string
	head := func(t string) string {
		hd := fg(cMuted).Bold(true).Render(t) + " "
		return hd + fg(cFaint).Render(strings.Repeat("─", max(0, w-lipgloss.Width(hd))))
	}
	if i := m.index(); i >= 0 {
		s := m.vis[i]
		out = append(out, head("SELECTED"))
		start := len(out)
		out = append(out, providerChip(s.Provider)+
			fg(cMuted).Render("  "+s.Kind))
		kv := func(k, v string) {
			out = append(out, fg(cMuted).Render(pad(k, 10))+fg(cFg).Render(fit(v, w-10)))
		}
		lg, hasLogo := m.logos[s.Provider]
		if hasLogo {
			kv = func(k, v string) {
				out = append(out, fg(cMuted).Render(pad(k, 10))+fg(cFg).Render(fit(v, w-10-largeW-2)))
			}
		}
		kv("where", orDash(m.short(s.CWD)))
		pid := "—"
		if s.PID > 0 {
			pid = fmt.Sprint(s.PID)
		}
		host := s.Host
		if host == "" {
			host = m.hostname
		}
		kv("machine", host+" · pid "+pid)
		started := "—"
		if !s.StartedAt.IsZero() {
			started = s.StartedAt.Local().Format("Jan 2 15:04") + " · " + dur(time.Since(s.StartedAt)) + " ago"
		}
		kv("started", started)
		kv("session", orDash(shortID(s.ID)))
		if sa := s.Subagents; sa != nil && sa.Total > 0 {
			v := fmt.Sprintf("%d started", sa.Total)
			if sa.Running > 0 {
				v = fmt.Sprintf("%d running · %d started", sa.Running, sa.Total)
			}
			kv("subagents", v)
			for _, a := range sa.Active {
				out = append(out, pad("", 10)+fg(cGreen).Render("▸ ")+fg(cMuted).Render(fit(a, w-12)))
			}
		}
		if hasLogo {
			for r := 0; r < largeH && start+r < len(out); r++ {
				out[start+r] = lg.large[r] + "  " + out[start+r]
			}
		}
		out = append(out, "")
	}
	out = append(out, head("ACTIVITY"))
	if len(m.events) == 0 {
		out = append(out, fg(cFaint).Render("watching for changes…"))
	}
	for _, e := range m.events {
		if len(out)+2 > h {
			break
		}
		name := e.Session.Title
		if name == "" {
			name = filepath.Base(e.Session.CWD)
		}
		detail := eventVerb(e)
		if e.Session.Host != "" {
			detail += " · " + e.Session.Host
		}
		mark := fg(providerColor(e.Session.Provider)).Render(strings.Fields(providerMark(e.Session.Provider))[0])
		out = append(out,
			fg(cFaint).Render(e.At.Local().Format("15:04")+" ")+fg(statusColor(e.To)).Render(statusGlyph(e.To)+" ")+fg(cFg).Render(fit(name, w-8)),
			"        "+mark+" "+fg(cMuted).Render(fit(detail, w-12)))
	}
	for len(out) < h {
		out = append(out, "")
	}
	for i := range out {
		out[i] = pad(fitStyled(out[i], w), w)
	}
	return out[:h]
}

func (m *uiModel) footer() string {
	k := func(key, what string) string {
		return fg(cFg).Render(key) + " " + fg(cMuted).Render(what)
	}
	filter := "all"
	if f := m.filters[m.filterIx]; f != "" {
		filter = f
	}
	parts := []string{
		k("←↑↓→", "move"),
		k("enter", "open"),
		k("f", "provider: "+filter),
	}
	if m.stale > 0 {
		verb := "show"
		if m.showStale {
			verb = "hide"
		}
		parts = append(parts, k("s", fmt.Sprintf("%s %d stale", verb, m.stale)))
	}
	parts = append(parts, k("q", "quit"))
	left := strings.Repeat(" ", margin) + strings.Join(parts, fg(cFaint).Render("   ·   "))

	var errs []string
	for _, e := range m.snap.Errors {
		if strings.Contains(e.Error, "connecting") {
			continue
		}
		errs = append(errs, e.Provider+": "+e.Error)
	}
	right := fg(cFaint).Render("read-only") + strings.Repeat(" ", margin)
	if m.usage != nil {
		t := m.usage.today.Total
		right = fg(cMuted).Render("today ") + fg(cFg).Render(hours(t.Active)) + fg(cMuted).Render(" agent time · ") +
			fg(cFg).Render(humanN(t.Tokens.Total())) + fg(cMuted).Render(" tokens   ") + fg(cFg).Render("u") + fg(cMuted).Render(" usage") +
			strings.Repeat(" ", margin)
	}
	if len(errs) > 0 {
		right = fg(cRed).Render(fit("! "+strings.Join(errs, " · "), max(10, m.w/2))) + strings.Repeat(" ", margin)
	}
	space := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	return left + strings.Repeat(" ", max(1, space)) + right
}

// ---- words ----

func statusWord(s model.Status) string {
	switch s {
	case model.StatusBusy:
		return "WORKING"
	case model.StatusWaiting:
		return "NEEDS YOU"
	case model.StatusError:
		return "ERROR"
	case model.StatusBlocked:
		return "BLOCKED"
	case model.StatusIdle:
		return "IDLE"
	}
	return "UNKNOWN"
}

func statusGlyph(s model.Status) string {
	switch s {
	case model.StatusBusy:
		return "●"
	case model.StatusWaiting:
		return "◆"
	case model.StatusError:
		return "✕"
	case model.StatusBlocked:
		return "■"
	case model.StatusIdle:
		return "○"
	}
	return "·"
}

func eventVerb(e hub.Event) string {
	switch e.To {
	case model.StatusBusy:
		return "started working"
	case model.StatusWaiting:
		return "needs you"
	case model.StatusError:
		return "hit an error"
	case model.StatusBlocked:
		return "blocked"
	case model.StatusIdle:
		if e.From == model.StatusBusy || e.From == "" {
			return "finished"
		}
		return "went idle"
	}
	return "appeared"
}

func shortModel(s string) string {
	s = strings.TrimPrefix(s, "claude-")
	if i := strings.Index(s, "["); i > 0 {
		s = s[:i]
	}
	return s
}

func shortID(id string) string {
	if len(id) > 8 && strings.Count(id, "-") >= 3 {
		return id[:8]
	}
	return id
}

func (m *uiModel) short(p string) string {
	if m.home != "" && strings.HasPrefix(p, m.home) {
		return "~" + p[len(m.home):]
	}
	return p
}

func dur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		h := int(d.Hours())
		if mm := int(d.Minutes()) % 60; h < 10 && mm > 0 {
			return fmt.Sprintf("%dh %dm", h, mm)
		}
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---- big digits ----

var digits3 = map[rune][]string{
	'0': {"█▀█", "█ █", "▀▀▀"},
	'1': {"▀█ ", " █ ", "▀▀▀"},
	'2': {"▀▀█", "█▀▀", "▀▀▀"},
	'3': {"▀▀█", " ▀█", "▀▀▀"},
	'4': {"█ █", "▀▀█", "  ▀"},
	'5': {"█▀▀", "▀▀█", "▀▀▀"},
	'6': {"█▀▀", "█▀█", "▀▀▀"},
	'7': {"▀▀█", "  █", "  ▀"},
	'8': {"█▀█", "█▀█", "▀▀▀"},
	'9': {"█▀█", "▀▀█", "▀▀▀"},
}

var digits5 = map[rune][]string{
	'0': {"████", "█  █", "█  █", "█  █", "████"},
	'1': {" ██ ", "  █ ", "  █ ", "  █ ", " ███"},
	'2': {"████", "   █", "████", "█   ", "████"},
	'3': {"████", "   █", " ███", "   █", "████"},
	'4': {"█  █", "█  █", "████", "   █", "   █"},
	'5': {"████", "█   ", "████", "   █", "████"},
	'6': {"████", "█   ", "████", "█  █", "████"},
	'7': {"████", "   █", "  █ ", " █  ", " █  "},
	'8': {"████", "█  █", "████", "█  █", "████"},
	'9': {"████", "█  █", "████", "   █", "████"},
}

func bigNumber(n, rows int) []string {
	font := digits3
	if rows == 5 {
		font = digits5
	}
	out := make([]string, rows)
	for i, r := range fmt.Sprint(n) {
		g := font[r]
		for row := range out {
			if i > 0 {
				out[row] += " "
			}
			out[row] += g[row]
		}
	}
	return out
}

// ---- layout helpers ----

// fit pads or truncates plain text to exactly n cells.
func fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w <= n {
		return s + strings.Repeat(" ", n-w)
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > n {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// fitStyled truncates a string that may contain ANSI styling.
func fitStyled(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(n).Render(s)
}

func pad(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// machineName is this machine's short name. macOS hostnames can be whatever
// the network handed out ("unknown_46:08:…"), so prefer the name the user
// gave the Mac.
func machineName() string {
	hn, _ := os.Hostname()
	hn = strings.Split(hn, ".")[0]
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "LocalHostName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				hn = n
			}
		}
	}
	return strings.ToLower(hn)
}
