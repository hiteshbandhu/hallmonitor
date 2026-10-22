package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/hiteshbandhu/hallmonitor/internal/hub"
	"github.com/hiteshbandhu/hallmonitor/internal/model"
)

type fake struct{ n int }

func (fake) Name() string { return "fake" }
func (f fake) Collect(context.Context) ([]model.Session, error) {
	statuses := []model.Status{model.StatusBusy, model.StatusWaiting, model.StatusIdle, model.StatusError, model.StatusBlocked}
	var out []model.Session
	for i := 0; i < f.n; i++ {
		out = append(out, model.Session{
			Provider:  []string{"claude", "codex"}[i%2],
			ID:        fmt.Sprint("s", i),
			Title:     strings.Repeat("a very long session title ", 3),
			CWD:       "/Users/x/Developer/some-really-long-project-name",
			Status:    statuses[i%len(statuses)],
			Model:     "claude-opus-5-5",
			Last:      "Bash · " + strings.Repeat("run the whole test suite ", 4),
			Prompt:    "please fix everything",
			Context:   123456,
			Host:      []string{"", "gpu-box-with-long-name"}[i%2],
			Since:     time.Now().Add(-time.Duration(i) * time.Minute),
			UpdatedAt: time.Now(),
		})
	}
	return out, nil
}

// Every frame line must fit the terminal, or the terminal wraps and the
// whole board shears.
func TestRenderFits(t *testing.T) {
	for _, n := range []int{0, 1, 7, 30} {
		h := hub.New(time.Second, fake{n})
		h.Once(context.Background())
		h.Once(context.Background())
		for _, size := range [][2]int{{40, 12}, {80, 24}, {120, 40}, {131, 40}, {200, 60}, {320, 90}} {
			out := Render(h, Options{}, size[0], size[1])
			lines := strings.Split(out, "\n")
			if len(lines) > size[1] {
				t.Errorf("n=%d %dx%d: %d lines", n, size[0], size[1], len(lines))
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > size[0] {
					t.Errorf("n=%d %dx%d: line %d is %d wide", n, size[0], size[1], i, w)
					break
				}
			}
		}
	}
}

type poisoned struct{}

func (poisoned) Name() string { return "poisoned" }
func (poisoned) Collect(context.Context) ([]model.Session, error) {
	evil := "\x1b]2;EVIL-WINDOW-TITLE\x07\x1b]52;c;aGVsbG8tZnJvbS1ldmls\x07\u009d8;;https://evil.example\u009c"
	return []model.Session{{
		Provider: "claude", ID: "s", Status: model.StatusBusy, UpdatedAt: time.Now(),
		Title: "t" + evil, CWD: "/p" + evil, Last: "Bash · " + evil, Prompt: evil, Host: "h" + evil,
	}}, fmt.Errorf("remote said %s", evil)
}

// Agent text never reaches the terminal as an escape sequence.
func TestRenderDropsAgentEscapes(t *testing.T) {
	h := hub.New(time.Second, poisoned{})
	h.Once(context.Background())
	out := Render(h, Options{}, 160, 40)
	for _, seq := range []string{"\x1b]", "\u009d", "\u009c", "\x07"} {
		if strings.Contains(out, seq) {
			t.Fatalf("frame carries %q", seq)
		}
	}
	if !strings.Contains(out, "EVIL-WINDOW-TITLE") {
		t.Fatal("the text itself should still show")
	}
}
