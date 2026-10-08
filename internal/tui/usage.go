package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hiteshbandhu/hallmonitor/internal/usage"
)

// ---- data ----

type usageMsg struct {
	today, week, month usage.Summary
	err                error
}

type usageProgressMsg float64

// usageLoop keeps the ledger fresh while the board runs: a full pass on
// start (the first ever can take a few seconds), then every minute.
func usageLoop(ctx context.Context, p *tea.Program) {
	l := usage.Open(usage.DefaultDir())
	for {
		last := time.Time{}
		err := l.Update(ctx, func(done, total int64) {
			if total > 32<<20 && time.Since(last) > 250*time.Millisecond {
				last = time.Now()
				p.Send(usageProgressMsg(float64(done) / float64(max(total, 1))))
			}
		})
		if err == usage.ErrBusy {
			err = nil // someone else is updating; their result is on disk
		}
		p.Send(summarize(l, err))
		go l.MaybeProbeClaude(ctx) // writes the limits file; picked up below
		// Between passes, pick up new plan limits as soon as they're written.
		seen, next := l.LimitsChanged(), time.After(time.Minute)
	wait:
		for {
			select {
			case <-ctx.Done():
				return
			case <-next:
				break wait
			case <-time.After(3 * time.Second):
				if at := l.LimitsChanged(); !at.Equal(seen) {
					seen = at
					p.Send(limitsMsg(l.RateLimits()))
				}
			}
		}
	}
}

type limitsMsg map[string]usage.RateLimit

func summarize(l *usage.Ledger, err error) usageMsg {
	return usageMsg{today: l.Summarize(1), week: l.Summarize(7), month: l.Summarize(30), err: err}
}

// ---- view ----

func (m *uiModel) usageView() string {
	var b []string
	b = append(b, m.topBar(), "")
	if m.usage == nil {
		msg := "Reading your agent logs…"
		if m.usageProgress > 0 {
			msg = fmt.Sprintf("Indexing your agent logs… %d%%", int(m.usageProgress*100))
		}
		body := make([]string, max(1, m.h-4))
		body[len(body)/2-1] = center(lipgloss.NewStyle().Bold(true).Foreground(cFg).Render(msg), m.w)
		body[len(body)/2] = center(fg(cMuted).Render("First run reads everything once; after that it takes milliseconds."), m.w)
		b = append(b, body...)
		b = append(b, m.usageFooter())
		return strings.Join(b, "\n")
	}
	u := m.usage
	rng := u.week
	if m.usageDays == 30 {
		rng = u.month
	}

	b = append(b, m.usageTiles(u.today, rng)...)
	b = append(b, "")

	rightW := 0
	if m.w >= 130 {
		rightW = min(56, m.w*34/100)
	}
	leftW := m.w - 2*margin - rightW
	if rightW > 0 {
		leftW -= 4
	}
	bodyH := m.h - len(b) - 2
	chartH := max(6, min(22, bodyH-13))
	left := m.dailyChart(rng, leftW, chartH)
	if bodyH-len(left) >= 11 {
		left = append(left, "")
		left = append(left, heatmap(rng, leftW)...)
	}
	var right []string
	if rightW > 0 {
		right = m.usageLists(rng, rightW, bodyH)
	}
	for i := 0; i < bodyH; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		line := strings.Repeat(" ", margin) + pad(fitStyled(l, leftW), leftW)
		if rightW > 0 {
			line += "    " + fitStyled(r, rightW)
		}
		b = append(b, line)
	}
	b = append(b, "", m.usageFooter())
	if len(b) > m.h {
		b = append(b[:m.h-1], b[len(b)-1])
	}
	for i := range b {
		b[i] = fitStyled(b[i], m.w)
	}
	return strings.Join(b, "\n")
}

func (m *uiModel) usageTiles(today, rng usage.Summary) []string {
	n := fmt.Sprint(m.usageDays)
	tiles := [][]string{
		textTile("AGENT-HOURS TODAY", hours(today.Total.Active), func(r int) lipgloss.TerminalColor { return gradient(1 - float64(r)/3) }),
		textTile("PROMPTS TODAY", fmt.Sprint(today.Total.Prompts), func(int) lipgloss.TerminalColor { return cFg }),
		textTile("TOKENS TODAY", humanN(today.Total.Tokens.Total()), func(int) lipgloss.TerminalColor { return cClaude }),
		textTile("AGENT-HOURS "+n+"D", hours(rng.Total.Active), func(int) lipgloss.TerminalColor { return cMuted }),
		textTile("CACHE HIT "+n+"D", cachePct(rng.Total.Tokens), func(int) lipgloss.TerminalColor { return cMuted }),
	}
	out := make([]string, 5)
	for _, t := range tiles {
		if lipgloss.Width(out[0])+lipgloss.Width(t[0]) > m.w-2*margin {
			break
		}
		for i := range out {
			out[i] += t[i]
		}
	}
	for i := range out {
		out[i] = strings.Repeat(" ", margin) + out[i]
	}
	return out
}

func textTile(label, value string, color func(row int) lipgloss.TerminalColor) []string {
	g := bigText(value)
	w := max(lipgloss.Width(label), lipgloss.Width(g[0])) + 6
	out := []string{pad(fg(cMuted).Render(label), w)}
	for r, l := range g {
		out = append(out, pad(fg(color(r)).Bold(true).Render(l), w))
	}
	return append(out, strings.Repeat(" ", w))
}

// chartProviders stack bottom to top in the daily chart.
var chartProviders = []struct {
	id, name string
	color    ac
}{{"claude", "Claude", cClaude}, {"codex", "Codex", cCodex}, {"opencode", "opencode", cOpencode}}

// dailyChart is a stacked bar per day: Claude on the bottom, then Codex,
// then opencode.
func (m *uiModel) dailyChart(s usage.Summary, w, h int) []string {
	days := s.Days
	peak := 0.0
	shown := chartProviders[:2] // opencode joins the legend once it has hours
	for _, d := range days {
		sum := 0.0
		for _, p := range chartProviders {
			sum += d.ByProvider[p.id].Active
		}
		peak = max(peak, sum)
		if d.ByProvider["opencode"].Active > 0 {
			shown = chartProviders
		}
	}
	title := fg(cMuted).Bold(true).Render("AGENT-HOURS PER DAY") + fg(cFaint).Render(fmt.Sprintf("  last %d days · peak %s", len(days), hours(peak)))
	var lg []string
	for _, p := range shown {
		lg = append(lg, fg(p.color).Render("■")+fg(cMuted).Render(" "+p.name))
	}
	legend := strings.Join(lg, "  ")
	out := []string{title + strings.Repeat(" ", max(2, w-lipgloss.Width(title)-lipgloss.Width(legend))) + legend}
	if peak == 0 {
		peak = 1
	}
	colW := max(1, (w-5)/max(1, len(days)))
	barW := max(1, min(colW-1, colW*3/5))
	if colW == 1 {
		barW = 1
	}
	levels := []rune(" ▁▂▃▄▅▆▇█")
	eighths := h * 8
	for r := 0; r < h; r++ {
		base := (h - 1 - r) * 8
		axis := "    "
		if r == 0 {
			axis = fitR(hours(peak), 4)
		}
		var sb strings.Builder
		sb.WriteString(fg(cFaint).Render(axis) + " ")
		for _, d := range days {
			// Cumulative height of each provider's segment, in eighths.
			cum := make([]int, len(chartProviders))
			sum := 0.0
			for i, p := range chartProviders {
				sum += d.ByProvider[p.id].Active
				cum[i] = int(sum / peak * float64(eighths))
			}
			tot := cum[len(cum)-1]
			if tot == 0 && sum > 0 {
				tot = 1
				cum[len(cum)-1] = 1
			}
			t := max(0, min(8, tot-base))
			var cell string
			if t == 0 {
				if r == h-1 {
					cell = fg(cFaint).Render(strings.Repeat("▁", barW))
				} else {
					cell = strings.Repeat(" ", barW)
				}
			} else {
				// The segment at the bottom of this cell, and the one at its top.
				lo, hi := 0, 0
				for lo < len(cum)-1 && cum[lo] <= base {
					lo++
				}
				for hi < len(cum)-1 && cum[hi] <= base+t-1 {
					hi++
				}
				b := min(8, cum[lo]-base)
				switch {
				case lo == hi || b >= t:
					cell = fg(chartProviders[lo].color).Render(strings.Repeat(string(levels[t]), barW))
				case t == 8:
					cell = lipgloss.NewStyle().Foreground(chartProviders[lo].color).Background(chartProviders[hi].color).
						Render(strings.Repeat(string(levels[b]), barW))
				default:
					cell = fg(chartProviders[lo].color).Render(strings.Repeat(string(levels[b]), barW))
				}
			}
			sb.WriteString(cell + strings.Repeat(" ", colW-barW))
		}
		out = append(out, sb.String())
	}
	// Day labels: weekday names for a week, day-of-month every few for longer.
	var lb strings.Builder
	lb.WriteString("     ")
	every := max(1, (6+colW-1)/colW)
	for i, d := range days {
		t, _ := time.Parse("2006-01-02", d.Date)
		label := ""
		switch {
		case i == len(days)-1:
			label = "today"
		case len(days) <= 14 && colW >= 4:
			label = t.Format("Mon")
		case (len(days)-1-i)%every == 0 && len(days)-1-i >= every:
			label = t.Format("Jan 2")
			if colW*every < 7 {
				label = t.Format("2")
			}
		}
		col := 5 + i*colW
		if label != "" && lipgloss.Width(lb.String()) <= col {
			lb.WriteString(strings.Repeat(" ", col-lipgloss.Width(lb.String())))
			lb.WriteString(label)
		}
	}
	out = append(out, fg(cFaint).Render(lb.String()))
	return out
}

// heatmap is weekday × hour, like a contribution graph for your agents.
func heatmap(s usage.Summary, w int) []string {
	cellW := max(1, min(4, (w-4)/24))
	peak := 0.0
	for _, row := range s.Heat {
		for _, v := range row {
			peak = max(peak, v)
		}
	}
	out := []string{fg(cMuted).Bold(true).Render("WHEN YOUR AGENTS WORK") + fg(cFaint).Render("  active time by weekday and hour")}
	var hdr strings.Builder
	hdr.WriteString("    ")
	for h := 0; h < 24; h++ {
		lbl := ""
		if h%6 == 0 {
			lbl = fmt.Sprint(h)
		}
		hdr.WriteString(fit(lbl, cellW))
	}
	out = append(out, fg(cFaint).Render(hdr.String()))
	days := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	for d, row := range s.Heat {
		var sb strings.Builder
		sb.WriteString(fg(cMuted).Render(days[d]) + " ")
		for _, v := range row {
			block := strings.Repeat("█", cellW-1) + " "
			if cellW == 1 {
				block = "■"
			}
			if v == 0 || peak == 0 {
				sb.WriteString(fg(cFaint).Render(block))
				continue
			}
			t := v / peak
			sb.WriteString(fg(gradient(0.15 + 0.85*t)).Render(block))
		}
		out = append(out, sb.String())
	}
	legend := fg(cFaint).Render("    less ") + fg(cFaint).Render("■ ") +
		fg(gradient(0.2)).Render("■ ") + fg(gradient(0.5)).Render("■ ") + fg(gradient(0.8)).Render("■ ") + fg(gradient(1)).Render("■") +
		fg(cFaint).Render(" more")
	return append(out, legend)
}

func (m *uiModel) usageLists(s usage.Summary, w, h int) []string {
	var out []string
	head := func(t string) {
		hd := fg(cMuted).Bold(true).Render(t) + " "
		out = append(out, hd+fg(cFaint).Render(strings.Repeat("─", max(0, w-lipgloss.Width(hd)))))
	}
	bars := func(items []usage.Named, n int, val func(usage.Counters) float64, label func(usage.Counters) string) {
		if len(items) == 0 {
			out = append(out, fg(cFaint).Render("nothing yet"))
			return
		}
		items = items[:min(n, len(items))]
		peak := max(val(items[0].Counters), 1e-9)
		nameW, valW := 18, 7
		barW := max(4, w-nameW-valW-2)
		for _, it := range items {
			v := val(it.Counters)
			fill := v / peak * float64(barW)
			full := int(fill)
			var bar strings.Builder
			col := gradient(0.2 + 0.7*v/peak)
			if it.Provider != "" {
				col = providerColor(it.Provider)
			}
			bar.WriteString(fg(col).Render(strings.Repeat("█", full)))
			if frac := fill - float64(full); frac > 0.125 && full < barW {
				bar.WriteString(fg(col).Render(string([]rune("▏▎▍▌▋▊▉")[min(6, int(frac*8)-1)])))
				full++
			}
			bar.WriteString(fg(cFaint).Render(strings.Repeat("·", max(0, barW-full))))
			out = append(out, fg(cFg).Render(fit(it.Name, nameW))+bar.String()+" "+fg(cMuted).Render(fitR(label(it.Counters), valW)))
		}
	}
	head(fmt.Sprintf("TOP PROJECTS · %dD", m.usageDays))
	bars(s.Projects, 5, func(c usage.Counters) float64 { return c.Active }, func(c usage.Counters) string { return hours(c.Active) })
	out = append(out, "")
	head("MODELS · TOKENS")
	bars(s.Models, 4, func(c usage.Counters) float64 { return float64(c.Tokens.Total()) }, func(c usage.Counters) string { return humanN(c.Tokens.Total()) })
	out = append(out, "")
	head("TOOLS · CALLS")
	bars(s.Tools, 5, func(c usage.Counters) float64 { return float64(c.Tools) }, func(c usage.Counters) string { return humanN(c.Tools) })

	if len(s.RateLimits) > 0 && len(out)+4 < h {
		out = append(out, "")
		head("PLAN LIMITS")
		keys := make([]string, 0, len(s.RateLimits))
		for k := range s.RateLimits {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(out)+2 > h {
				break
			}
			rl := s.RateLimits[k]
			label := providerMark(rl.Provider)
			if rl.WindowMin > 0 {
				label += " " + shortWindow(rl.WindowMin)
			}
			label = fit(label, 14)
			gw := max(8, w-14-6)
			used := min(gw, int(rl.UsedPercent/100*float64(gw)))
			var col lipgloss.TerminalColor = cGreen
			if rl.UsedPercent >= 80 {
				col = cRed
			} else if rl.UsedPercent >= 50 {
				col = cAmber
			}
			out = append(out, fg(providerColor(rl.Provider)).Bold(true).Render(label)+
				fg(col).Render(strings.Repeat("█", used))+fg(cFaint).Render(strings.Repeat("░", gw-used))+
				fg(cFg).Render(fmt.Sprintf(" %3.0f%%", rl.UsedPercent)))
			note := "resets " + rl.ResetsAt.Local().Format("Mon 15:04")
			if age := time.Since(rl.ObservedAt); rl.Stale(time.Now()) {
				note += " · as of " + dur(age) + " ago"
			}
			out = append(out, strings.Repeat(" ", 14)+fg(cMuted).Render(fit(note, w-14)))
		}
	}
	sess := fmt.Sprintf("%d sessions · %s prompts · %s tool calls · %s replies", s.Sessions,
		humanN(s.Total.Prompts), humanN(s.Total.Tools), humanN(s.Total.Replies))
	if len(out)+2 < h {
		out = append(out, "", fg(cFaint).Render(fit(sess, w)))
	}
	return out
}

func (m *uiModel) usageFooter() string {
	k := func(key, what string) string { return fg(cFg).Render(key) + " " + fg(cMuted).Render(what) }
	other := 30
	if m.usageDays == 30 {
		other = 7
	}
	left := strings.Repeat(" ", margin) + strings.Join([]string{
		k("u", "back to agents"),
		k("t", fmt.Sprintf("%d days", other)),
		k("q", "quit"),
	}, fg(cFaint).Render("   ·   "))
	right := fg(cFaint).Render("local ledger · counts only, no prompts stored") + strings.Repeat(" ", margin)
	if m.usageErr != nil {
		right = fg(cRed).Render(fit("! "+m.usageErr.Error(), m.w/2)) + strings.Repeat(" ", margin)
	}
	return left + strings.Repeat(" ", max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))) + right
}

// ---- formatting ----

func hours(sec float64) string {
	h := sec / 3600
	switch {
	case h >= 100:
		return fmt.Sprintf("%.0fh", h)
	case h >= 0.05:
		return fmt.Sprintf("%.1fh", h)
	}
	return "0h"
}

func humanN(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e4:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1e3:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func cachePct(t usage.Tokens) string {
	in := t.Input + t.CacheRead + t.CacheWrite
	if in == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", float64(t.CacheRead)*100/float64(in))
}

// shortWindow is 300 → "5h", 10080 → "7d".
func shortWindow(min int) string {
	if min >= 60*24 && min%(60*24) == 0 {
		return fmt.Sprintf("%dd", min/(60*24))
	}
	if min >= 60 && min%60 == 0 {
		return fmt.Sprintf("%dh", min/60)
	}
	return fmt.Sprintf("%dm", min)
}

func windowName(min int) string {
	switch {
	case min >= 60*24 && min%(60*24) == 0:
		return fmt.Sprintf("%d-day", min/(60*24))
	case min >= 60 && min%60 == 0:
		return fmt.Sprintf("%d-hour", min/60)
	}
	return fmt.Sprintf("%d-min", min)
}

func center(s string, w int) string {
	return strings.Repeat(" ", max(0, (w-lipgloss.Width(s))/2)) + s
}

// bigText renders digits and a few units in the 3-row block font.
func bigText(s string) []string {
	out := make([]string, 3)
	for i, r := range s {
		g, ok := digits3[r]
		if !ok {
			g, ok = glyphs3[r]
		}
		if !ok {
			continue
		}
		for row := range out {
			if i > 0 {
				out[row] += " "
			}
			out[row] += g[row]
		}
	}
	return out
}

var glyphs3 = map[rune][]string{
	'.': {" ", " ", "▀"},
	'h': {"█  ", "█▀▄", "▀ ▀"},
	'k': {"█  ", "█▄▀", "▀ ▀"},
	'M': {"█▄ ▄█", "█ ▀ █", "▀   ▀"},
	'B': {"█▀▄", "█▀▄", "▀▀ "},
	'%': {"▀ ▄▀", " ▄▀ ", "▀  ▀"},
}

func fitR(s string, n int) string {
	if lipgloss.Width(s) >= n {
		return fit(s, n)
	}
	return strings.Repeat(" ", n-lipgloss.Width(s)) + s
}
