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
		Answers: map[string][]string{"Colour?": {"Blue"}, "Sizes?": {"S", "M"}}})
	b, _ := json.Marshal(out)
	for _, want := range []string{`"permissionDecision":"allow"`, `"Colour?":"Blue"`, `"Sizes?":["S","M"]`, `"questions":[`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	// One question left unanswered: say nothing, Claude asks as usual.
	if hookOutput("PreToolUse", a, questions, ask.Answer{Action: "answer", Answers: map[string][]string{"Colour?": {"Red"}}}) != nil {
		t.Error("answered an incomplete set")
	}
	if hookOutput("PreToolUse", a, questions, ask.Answer{Action: "pass"}) != nil {
		t.Error("pass should fall back")
	}

	p := ask.Ask{Kind: ask.KindPermission, Tool: "Bash"}
	b, _ = json.Marshal(hookOutput("PermissionRequest", p, nil, ask.Answer{Action: "deny"}))
	if !strings.Contains(string(b), `"behavior":"deny"`) || !strings.Contains(string(b), `"hookEventName":"PermissionRequest"`) {
		t.Errorf("deny: %s", b)
	}
	b, _ = json.Marshal(hookOutput("PermissionRequest", p, nil, ask.Answer{Action: "allow"}))
	if !strings.Contains(string(b), `"behavior":"allow"`) {
		t.Errorf("allow: %s", b)
	}
}

func TestPermissionDetail(t *testing.T) {
	if d := permissionDetail("Bash", json.RawMessage(`{"command":"rm -rf build","description":"clean"}`)); d != "rm -rf build" {
		t.Errorf("bash: %q", d)
	}
	if d := permissionDetail("Edit", json.RawMessage(`{"file_path":"/a/b/main.go"}`)); d != "main.go" {
		t.Errorf("edit: %q", d)
	}
}
