package opencode

import (
	"testing"

	"github.com/hiteshbandhu/hallmonitor/internal/proc"
)

func TestParseArgs(t *testing.T) {
	for _, c := range []struct {
		args, sub, session string
		cont               bool
		kind               string
	}{
		{"/Users/x/.opencode/bin/opencode", "", "", false, "interactive"},
		{"opencode ~/code/app --continue", "", "", true, "interactive"},
		{"opencode -s ses_abc", "", "ses_abc", false, "interactive"},
		{"opencode run --session=ses_q fix it", "run", "ses_q", false, "background"},
		{"opencode serve --port 4096", "serve", "", false, "server"},
		{"opencode db path", "db", "", false, "interactive"},
	} {
		in := parseArgs(proc.Proc{Args: c.args})
		if in.sub != c.sub || in.session != c.session || in.cont != c.cont || kind(in) != c.kind {
			t.Errorf("%q: sub=%q session=%q cont=%v kind=%s", c.args, in.sub, in.session, in.cont, kind(in))
		}
	}
	if !tools["db"] || tools["run"] || tools[""] {
		t.Error("tools")
	}
}
