// Package demo is a synthetic fleet for screenshots and trying the board
// without any agents running. Statuses drift over time so the wall moves.
package demo

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/model"
)

type seed struct {
	host, provider, title, cwd, model string
	ctx                               int
	period                            int // ticks per status cycle
	acts                              []string
	prompt                            string
}

var fleet = []seed{
	{"", "claude", "Migrate billing to Stripe v3", "/home/dev/pay-api", "claude-opus-5-5", 182_000, 23,
		[]string{"Edit · webhooks.ts", "Bash · run integration tests", "Grep · PaymentIntent", "Read · stripe.d.ts"}, "move us off the legacy charges API, keep webhooks idempotent"},
	{"", "codex", "Flaky e2e on checkout", "/home/dev/storefront", "gpt-6-astra", 64_000, 17,
		[]string{"exec · pnpm playwright test", "apply_patch", "exec · git bisect run"}, "checkout.spec fails 1 in 5 on CI, find out why"},
	{"", "claude", "Docs site search", "/home/dev/docs", "claude-sonnet-5", 41_000, 31,
		[]string{"Write · search-index.ts", "WebFetch · algolia docs", "Bash · npm run build"}, "add instant search to the docs"},
	{"gpu-box", "claude", "Fine-tune eval harness", "/srv/ml/evals", "claude-opus-5-5", 256_000, 19,
		[]string{"Bash · python eval.py --suite long", "Read · results.jsonl", "Edit · scorer.py"}, "the scorer double-counts partial matches"},
	{"gpu-box", "codex", "CUDA OOM in trainer", "/srv/ml/trainer", "gpt-6-astra", 97_000, 13,
		[]string{"exec · nvidia-smi", "exec · python train.py --bs 8", "apply_patch"}, "trainer OOMs at step 1200, bisect the batch size"},
	{"build-01", "claude", "Release 4.2 changelog", "/opt/ci/monorepo", "claude-haiku-4-5", 22_000, 41,
		[]string{"Bash · git log v4.1..HEAD", "Write · CHANGELOG.md"}, "draft the 4.2 changelog from merged PRs"},
	{"build-01", "codex", "Bump Go to 1.26", "/opt/ci/services", "gpt-6-astra", 58_000, 29,
		[]string{"exec · go test ./...", "apply_patch", "exec · go mod tidy"}, "upgrade every service to go 1.26"},
	{"", "opencode", "Rate-limit the public API", "/home/dev/gateway", "claude-sonnet-5", 88_000, 21,
		[]string{"edit · ratelimit.go", "bash · go test ./...", "grep · X-RateLimit"}, "add per-key rate limits to the public endpoints"},
	{"gpu-box", "opencode", "Profile the data loader", "/srv/ml/loader", "kimi-k2.5", 47_000, 27,
		[]string{"bash · py-spy record -o loader.svg", "read · dataset.py", "edit · dataset.py"}, "the loader stalls between epochs, find the hot spot"},
	{"", "claude", "Refactor auth middleware", "/home/dev/pay-api", "claude-opus-5-5", 133_000, 37,
		[]string{"Edit · middleware/auth.go", "Bash · go test ./auth/..."}, "split session and token auth"},
}

type Adapter struct {
	mu   sync.Mutex
	tick int
	t0   time.Time
}

func (*Adapter) Name() string { return "demo" }

func (a *Adapter) Collect(context.Context) ([]model.Session, error) {
	a.mu.Lock()
	a.tick++
	if a.t0.IsZero() {
		a.t0 = time.Now()
	}
	tick := a.tick
	a.mu.Unlock()

	now := time.Now()
	out := make([]model.Session, 0, len(fleet))
	for i, f := range fleet {
		h := fnv.New32a()
		h.Write([]byte(f.title))
		phase := int(h.Sum32()%97) + tick
		p := phase % f.period
		var st model.Status
		switch {
		case i == 5 && tick < 40: // one parked session
			st = model.StatusIdle
		case p < f.period*6/10:
			st = model.StatusBusy
		case p < f.period*7/10 && i%3 == 1:
			st = model.StatusWaiting
		case p == f.period-1 && i == 4:
			st = model.StatusError
		default:
			st = model.StatusIdle
		}
		last := f.acts[(phase/3)%len(f.acts)]
		if st == model.StatusWaiting {
			last = "approve: " + f.acts[0]
		}
		if st == model.StatusError {
			last = "error: CUDA out of memory (tried to allocate 2.1 GiB)"
		}
		out = append(out, model.Session{
			Host:      f.host,
			Provider:  f.provider,
			ID:        f.title,
			PID:       40000 + i*137,
			Title:     f.title,
			CWD:       f.cwd,
			Status:    st,
			Kind:      "interactive",
			Model:     f.model,
			StartedAt: now.Add(-time.Duration(20+i*17) * time.Minute),
			UpdatedAt: now.Add(-time.Duration(i) * time.Second),
			Since:     now.Add(-time.Duration((p%7)*9+5) * time.Second),
			Source:    "demo",
			Last:      last,
			Prompt:    f.prompt,
			Context:   f.ctx + tick*350,
			Subagents: demoSubagents(f.provider, i, st),
		})
	}
	return out, nil
}

// demoSubagents gives a couple of the Claude sessions helpers, some running.
func demoSubagents(provider string, i int, st model.Status) *model.Subagents {
	if provider != "claude" || i%2 == 1 {
		return nil
	}
	sa := &model.Subagents{Total: 3 + i}
	if st == model.StatusBusy {
		sa.Running = 1 + i%3
		sa.Active = []string{"Map the webhook handlers", "Find every charges API call", "Check the refund paths"}[:sa.Running]
	}
	return sa
}
