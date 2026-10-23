package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hiteshbandhu/hallmonitor/internal/ask"
)

// formText is everything a form puts on screen.
func formText(bs []ask.Block) string {
	var parts []string
	for _, b := range bs {
		parts = append(parts, b.Label, b.Text)
		for _, l := range b.Diff {
			parts = append(parts, l.Text)
		}
		for _, c := range b.Items {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Every value of every input is on screen, whichever tool it is and
// whatever fields it has: the form can't hide what Allow approves.
func TestFormShowsEverything(t *testing.T) {
	inputs := map[string]string{
		"Bash":         `{"command":"rm -rf build","description":"clean up","timeout":120000,"run_in_background":true,"dangerouslyDisableSandbox":true,"extra":"surprise"}`,
		"Edit":         `{"file_path":"/a/b.go","old_string":"x := 1\ny := 2","new_string":"x := 1\ny := 3","replace_all":true}`,
		"MultiEdit":    `{"file_path":"/a/b.go","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d","replace_all":true}]}`,
		"Write":        `{"file_path":"/a/new.txt","content":"hello\nworld"}`,
		"Read":         `{"file_path":"/a/b.go","offset":10,"limit":50,"pages":"1-3"}`,
		"Glob":         `{"pattern":"**/*.ts","path":"/src"}`,
		"Grep":         `{"pattern":"TODO.*fix","path":"/src","glob":"*.go","output_mode":"content","-i":true,"-C":3,"head_limit":20}`,
		"WebFetch":     `{"url":"https://example.com/api","prompt":"list the endpoints"}`,
		"WebSearch":    `{"query":"swift concurrency","allowed_domains":["apple.com","swift.org"]}`,
		"Agent":        `{"description":"Find endpoints","prompt":"Search the repo for HTTP handlers","subagent_type":"Explore","model":"sonnet"}`,
		"Skill":        `{"skill":"pdf","args":"merge a.pdf b.pdf"}`,
		"NotebookEdit": `{"notebook_path":"/n.ipynb","cell_id":"c1","new_source":"print(1)","edit_mode":"replace"}`,
		"mcp__x__run":  `{"title":"Review","initiation":"offer","count":9007199254740993,"opts":{"a":[1,2]},"flag":false,"none":null}`,
	}
	for tool, in := range inputs {
		form, complete := permissionForm(tool, json.RawMessage(in))
		if !complete || len(form) == 0 {
			t.Errorf("%s: complete=%v blocks=%d", tool, complete, len(form))
			continue
		}
		shown := formText(form)
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(in))
		d.UseNumber()
		_ = d.Decode(&m)
		var walk func(v any)
		walk = func(v any) {
			switch v := v.(type) {
			case string:
				// Fixed words for a fixed set of values.
				if said := map[string]string{"content": "Matching lines", "files_with_matches": "File names", "count": "Counts"}[v]; said != "" && strings.Contains(shown, said) {
					return
				}
				if !strings.Contains(shown, v) && !strings.Contains(shown, strings.ReplaceAll(v, "\n", "\n")) {
					for _, line := range strings.Split(v, "\n") {
						if !strings.Contains(shown, line) {
							t.Errorf("%s: %q not shown in\n%s", tool, line, shown)
						}
					}
				}
			case json.Number:
				if !strings.Contains(shown, string(v)) && !strings.Contains(shown, "2 min") {
					t.Errorf("%s: %s not shown in\n%s", tool, v, shown)
				}
			case map[string]any:
				for _, x := range v {
					walk(x)
				}
			case []any:
				for _, x := range v {
					walk(x)
				}
			}
		}
		walk(m)
	}
}

func TestFormBash(t *testing.T) {
	form, _ := permissionForm("Bash", json.RawMessage(`{"command":"ls","description":"list","timeout":90000,"dangerouslyDisableSandbox":true,"run_in_background":false}`))
	if form[0].Kind != "code" || form[0].Text != "ls" || form[1].Kind != "note" {
		t.Fatalf("%+v", form)
	}
	chips := form[2].Items
	if len(chips) != 2 || chips[0].Text != "Timeout 90 s" || !chips[1].Warn {
		t.Fatalf("chips %+v", chips)
	}
	// A known field of the wrong type is still shown, as its own row.
	form, _ = permissionForm("Bash", json.RawMessage(`{"command":"ls","timeout":"forever"}`))
	if last := form[len(form)-1]; last.Kind != "field" || last.Label != "Timeout" || last.Text != "forever" {
		t.Fatalf("%+v", form)
	}
}

func TestLineDiff(t *testing.T) {
	got := lineDiff("a\nb\nc", "a\nB\nc\nd")
	want := []ask.DiffLine{{Op: " ", Text: "a"}, {Op: "-", Text: "b"}, {Op: "+", Text: "B"}, {Op: " ", Text: "c"}, {Op: "+", Text: "d"}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if d := lineDiff("", "new"); len(d) != 1 || d[0].Op != "+" {
		t.Fatalf("%+v", d)
	}
}

func TestFormLimitsAndEscapes(t *testing.T) {
	big, _ := json.Marshal(map[string]string{"file_path": "/a", "content": strings.Repeat("x", maxForm+1)})
	if _, complete := permissionForm("Write", big); complete {
		t.Error("an oversized form offered for allowing")
	}
	if form, complete := permissionForm("Bash", json.RawMessage(`{"command":"a","command":"b"}`)); form != nil || complete {
		t.Error("a key given twice was laid out")
	}
	form, _ := permissionForm("Bash", json.RawMessage(`{"command":"echo \u001b]2;x\u0007 ‮"}`))
	if strings.ContainsAny(form[0].Text, "\x1b\x07‮") {
		t.Errorf("controls got through: %q", form[0].Text)
	}
}

func TestAlwaysChoices(t *testing.T) {
	sugg := []json.RawMessage{
		json.RawMessage(`{"type":"setMode","mode":"bypassPermissions","destination":"session"}`),
		json.RawMessage(`{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm test:*"}],"behavior":"allow","destination":"localSettings"}`),
		json.RawMessage(`{"type":"addRules","rules":[{"toolName":"Bash"}],"behavior":"deny","destination":"localSettings"}`),
		json.RawMessage(`{"type":"setMode","mode":"acceptEdits","destination":"session"}`),
		json.RawMessage(`{"type":"addDirectories","directories":["/tmp/x"],"destination":"session"}`),
	}
	labels, raw := alwaysChoices(sugg)
	if len(labels) != 2 || labels[0] != "Always allow Bash(npm test:*) in this project, for you" || labels[1] != "Allow all edits for this session" {
		t.Fatalf("%q", labels)
	}
	// Answering "always" hands back that suggestion as it came.
	a := ask.Ask{Kind: ask.KindPermission, Tool: "Bash", FormComplete: true}
	b, _ := json.Marshal(hookOutput("PermissionRequest", a, nil, ask.Answer{Action: "always", Always: 0}, raw))
	if !strings.Contains(string(b), `"updatedPermissions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm test:*"}],"behavior":"allow","destination":"localSettings"}]`) {
		t.Errorf("%s", b)
	}
	if hookOutput("PermissionRequest", a, nil, ask.Answer{Action: "always", Always: 5}, raw) != nil {
		t.Error("an index past the choices was allowed")
	}
	a.FormComplete = false
	if hookOutput("PermissionRequest", a, nil, ask.Answer{Action: "always", Always: 0}, raw) != nil {
		t.Error("allowed always without the whole input shown")
	}
}

// An MCP tool's fields come in the order it sent them, named for people,
// a list of words as chips and yes/no for a boolean.
func TestFormGeneric(t *testing.T) {
	form, _ := permissionForm("mcp__github__create_pull_request",
		json.RawMessage(`{"owner":"h","title":"T","body":"one\ntwo","draft":true,"labels":["a","b"]}`))
	var got []string
	for _, b := range form {
		got = append(got, b.Kind+":"+b.Label)
	}
	if strings.Join(got, " ") != "field:Owner field:Title text:Body field:Draft chips:Labels" {
		t.Fatalf("%v", got)
	}
	if form[3].Text != "Yes" || len(form[4].Items) != 2 {
		t.Fatalf("%+v", form)
	}
	// Two keys that would read the same keep their own spelling.
	form, _ = permissionForm("mcp__x__y", json.RawMessage(`{"file_path":"a","file-path":"b"}`))
	if form[0].Label != "file_path" || form[1].Label != "file-path" {
		t.Fatalf("%+v", form)
	}
}

// A standing grant is only offered when it can be read whole.
func TestAlwaysChoicesReadable(t *testing.T) {
	many := `{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"a"},{"toolName":"Bash","ruleContent":"b"},{"toolName":"Bash","ruleContent":"c"},{"toolName":"Bash","ruleContent":"curl:*"}],"behavior":"allow","destination":"localSettings"}`
	long := `{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"` + strings.Repeat("x", 200) + `"}],"behavior":"allow","destination":"localSettings"}`
	nl := `{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm test\ncurl evil:*"}],"behavior":"allow","destination":"localSettings"}`
	ctl := `{"type":"addDirectories","directories":["/tmp/\u202eevil"],"destination":"session"}`
	if labels, _ := alwaysChoices([]json.RawMessage{json.RawMessage(many), json.RawMessage(long), json.RawMessage(nl), json.RawMessage(ctl)}); len(labels) != 0 {
		t.Fatalf("offered %q", labels)
	}
}
