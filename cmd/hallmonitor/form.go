package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hiteshbandhu/hallmonitor/internal/ask"
)

// maxForm is how much of a tool's input the notch lays out as a form. The
// card scrolls, so it's more than maxDetail; past it, the form isn't
// offered for allowing, just as with the plain detail.
const maxForm = 8000

// maxAlways is the longest "don't ask again" choice the notch offers.
const maxAlways = 160

// permissionForm lays a tool's input out for the notch: a shell command as
// code, an edit as a diff, a URL as a headline. Each tool takes the fields
// it knows, by name and type; every field it doesn't (an unknown one, or a
// known one of an unexpected type) still gets its own row, so the form
// always shows all of what Allow approves. complete is false when it
// doesn't fit or the input can't be read one way only.
func permissionForm(tool string, input json.RawMessage) (form []ask.Block, complete bool) {
	m, ok := decodeInput(input)
	if !ok {
		return nil, false
	}
	f := &formBuilder{m: m, order: keyOrder(input)}
	switch tool {
	case "Bash", "PowerShell":
		f.code("command", "")
		f.note("description")
		f.chips(f.duration("timeout", "Timeout"), f.flag("run_in_background", "In background", false),
			f.flag("dangerouslyDisableSandbox", "Outside the sandbox", true))
	case "Edit":
		f.headline("file_path", "pencil")
		if old, okOld := f.m["old_string"].(string); okOld {
			if repl, okNew := f.m["new_string"].(string); okNew {
				delete(f.m, "old_string")
				delete(f.m, "new_string")
				f.diff(old, repl)
			}
		}
		f.chips(f.flag("replace_all", "Every occurrence", false))
	case "MultiEdit":
		f.headline("file_path", "pencil")
		f.edits()
	case "Write":
		f.headline("file_path", "doc.badge.plus")
		f.code("content", "New contents")
	case "Read":
		f.headline("file_path", "doc.text")
		f.chips(f.count("offset", "From line %s"), f.count("limit", "%s lines"), f.text("pages", "Pages %s"))
	case "Glob":
		f.code("pattern", "Files matching")
		f.field("path", "In", true)
	case "Grep":
		f.code("pattern", "Search for")
		f.field("path", "In", true)
		f.field("glob", "Files", true)
		f.field("type", "File type", true)
		f.chips(f.choice("output_mode", map[string]string{"content": "Matching lines", "files_with_matches": "File names", "count": "Counts"}),
			f.flag("-i", "Ignore case", false), f.flag("-n", "Line numbers", false), f.flag("multiline", "Across lines", false),
			f.flag("-o", "Only the match", false),
			f.count("-A", "%s after"), f.count("-B", "%s before"), f.count("-C", "%s around"),
			f.count("head_limit", "First %s"), f.count("offset", "Skip %s"))
	case "WebFetch":
		f.headline("url", "globe")
		f.prose("prompt", "Then")
		f.chips(f.count("offset", "From character %s"))
	case "WebSearch":
		f.headline("query", "magnifyingglass")
		f.chips(f.list("allowed_domains", "Only %s"), f.list("blocked_domains", "Not %s"))
	case "Agent", "Task":
		f.headline("description", "person.2")
		f.prose("prompt", "Task")
		f.chips(f.text("subagent_type", "%s agent"), f.text("model", "Model %s"), f.text("isolation", "Isolation %s"),
			f.flag("run_in_background", "In background", false))
	case "Skill":
		f.headline("skill", "sparkles")
		f.field("args", "With", true)
	case "NotebookEdit":
		f.headline("notebook_path", "book")
		f.chips(f.text("cell_id", "Cell %s"), f.text("cell_type", "%s cell"), f.text("edit_mode", "%s"))
		f.code("new_source", "New source")
	}
	f.rest()
	return f.blocks, f.size <= maxForm
}

type formBuilder struct {
	m      map[string]any // the fields no block has taken yet
	order  []string       // the input's keys, in the order they came
	blocks []ask.Block
	size   int
}

// keyOrder is the order of an object's keys as written (decodeInput has
// already refused anything odd).
func keyOrder(input json.RawMessage) []string {
	d := json.NewDecoder(strings.NewReader(string(input)))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for d.More() {
		t, err := d.Token()
		k, ok := t.(string)
		var skip json.RawMessage
		if err != nil || !ok || d.Decode(&skip) != nil {
			return keys
		}
		keys = append(keys, k)
	}
	return keys
}

func (f *formBuilder) add(b ask.Block) {
	f.size += len([]rune(b.Text)) + len([]rune(b.Label))
	for _, l := range b.Diff {
		f.size += len([]rune(l.Text)) + 1
	}
	for _, c := range b.Items {
		f.size += len([]rune(c.Text))
	}
	f.blocks = append(f.blocks, b)
}

// take removes key when it holds a string and returns it.
func (f *formBuilder) take(key string) (string, bool) {
	s, ok := f.m[key].(string)
	if ok {
		delete(f.m, key)
	}
	return s, ok
}

func (f *formBuilder) headline(key, icon string) {
	if s, ok := f.take(key); ok {
		f.add(ask.Block{Kind: "headline", Text: visible(s), Icon: icon})
	}
}

func (f *formBuilder) code(key, label string) {
	if s, ok := f.take(key); ok {
		f.add(ask.Block{Kind: "code", Label: label, Text: visible(s)})
	}
}

func (f *formBuilder) prose(key, label string) {
	if s, ok := f.take(key); ok {
		f.add(ask.Block{Kind: "text", Label: label, Text: visible(s)})
	}
}

// note is Claude's own words about the call: shown, set apart from what
// actually runs.
func (f *formBuilder) note(key string) {
	if s, ok := f.take(key); ok {
		f.add(ask.Block{Kind: "note", Text: visible(s)})
	}
}

func (f *formBuilder) field(key, label string, mono bool) {
	if s, ok := f.take(key); ok {
		f.add(ask.Block{Kind: "field", Label: label, Text: visible(s), Mono: mono})
	}
}

// chips adds one row of the chips that apply; a nil one doesn't.
func (f *formBuilder) chips(cs ...*ask.Chip) {
	var row []ask.Chip
	for _, c := range cs {
		if c != nil {
			row = append(row, *c)
		}
	}
	if len(row) > 0 {
		f.add(ask.Block{Kind: "chips", Items: row})
	}
}

// flag takes a boolean: a chip when true, nothing when false (the default
// behaviour, the same as leaving it out).
func (f *formBuilder) flag(key, label string, warn bool) *ask.Chip {
	b, ok := f.m[key].(bool)
	if !ok {
		return nil
	}
	delete(f.m, key)
	if !b {
		return nil
	}
	return &ask.Chip{Text: label, Warn: warn}
}

// count takes a whole number into format's %s.
func (f *formBuilder) count(key, format string) *ask.Chip {
	n, ok := f.m[key].(json.Number)
	if !ok {
		return nil
	}
	if _, err := strconv.ParseInt(string(n), 10, 64); err != nil {
		return nil // not whole: its own row, as written
	}
	delete(f.m, key)
	return &ask.Chip{Text: fmt.Sprintf(format, string(n))}
}

// duration takes milliseconds, said in the largest unit that's exact.
func (f *formBuilder) duration(key, label string) *ask.Chip {
	n, ok := f.m[key].(json.Number)
	if !ok {
		return nil
	}
	ms, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || ms < 0 {
		return nil
	}
	delete(f.m, key)
	switch {
	case ms > 0 && ms%60000 == 0:
		return &ask.Chip{Text: fmt.Sprintf("%s %d min", label, ms/60000)}
	case ms > 0 && ms%1000 == 0:
		return &ask.Chip{Text: fmt.Sprintf("%s %d s", label, ms/1000)}
	}
	return &ask.Chip{Text: fmt.Sprintf("%s %d ms", label, ms)}
}

func (f *formBuilder) text(key, format string) *ask.Chip {
	s, ok := f.m[key].(string)
	if !ok || s == "" || strings.ContainsAny(s, "\n\r") || len([]rune(s)) > 60 {
		return nil // long or empty: its own row
	}
	delete(f.m, key)
	return &ask.Chip{Text: fmt.Sprintf(format, visible(s))}
}

// choice takes a string that's one of names' keys, said as its value.
func (f *formBuilder) choice(key string, names map[string]string) *ask.Chip {
	s, ok := f.m[key].(string)
	if !ok || names[s] == "" {
		return nil
	}
	delete(f.m, key)
	return &ask.Chip{Text: names[s]}
}

// list takes an array of short strings into format's %s.
func (f *formBuilder) list(key, format string) *ask.Chip {
	arr, ok := f.m[key].([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	var parts []string
	for _, v := range arr {
		s, isStr := v.(string)
		if !isStr || s == "" || strings.ContainsAny(s, ", \n\r") {
			return nil
		}
		parts = append(parts, visible(s))
	}
	delete(f.m, key)
	return &ask.Chip{Text: fmt.Sprintf(format, strings.Join(parts, ", "))}
}

func (f *formBuilder) diff(old, repl string) {
	f.add(ask.Block{Kind: "diff", Diff: lineDiff(visible(old), visible(repl))})
}

// edits takes MultiEdit's edits when each is a plain old/new pair.
func (f *formBuilder) edits() {
	arr, ok := f.m["edits"].([]any)
	if !ok {
		return
	}
	type edit struct {
		old, repl string
		all       bool
	}
	var es []edit
	for _, v := range arr {
		e, isObj := v.(map[string]any)
		old, ok1 := e["old_string"].(string)
		repl, ok2 := e["new_string"].(string)
		all, hasAll := e["replace_all"]
		allB, ok3 := all.(bool)
		if !isObj || !ok1 || !ok2 || (hasAll && !ok3) || len(e) != 2+map[bool]int{true: 1}[hasAll] {
			return // anything else: the whole list as its own row
		}
		es = append(es, edit{old, repl, allB})
	}
	delete(f.m, "edits")
	for _, e := range es {
		f.diff(e.old, e.repl)
		if e.all {
			f.chips(&ask.Chip{Text: "Every occurrence"})
		}
	}
}

// rest gives each field no block took a row of its own, in the order the
// input gave them: short text and plain values as a field, a list of short
// words as chips, longer text as prose, anything structured as code.
func (f *formBuilder) rest() {
	seen := map[string]bool{}
	var keys []string
	for _, k := range f.order {
		if _, ok := f.m[k]; ok && !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var others []string
	for k := range f.m {
		if !seen[k] {
			others = append(others, k)
		}
	}
	sort.Strings(others)
	keys = append(keys, others...)
	labels := restLabels(keys)
	for _, k := range keys {
		label := labels[k]
		switch v := f.m[k].(type) {
		case string:
			if strings.ContainsAny(v, "\n\r") || len([]rune(v)) > 80 {
				f.add(ask.Block{Kind: "text", Label: label, Text: visible(v)})
			} else {
				f.add(ask.Block{Kind: "field", Label: label, Text: visible(v)})
			}
		case bool:
			f.add(ask.Block{Kind: "field", Label: label, Text: map[bool]string{true: "Yes", false: "No"}[v]})
		case []any:
			if words, ok := shortWords(v); ok {
				var items []ask.Chip
				for _, w := range words {
					items = append(items, ask.Chip{Text: w})
				}
				f.add(ask.Block{Kind: "chips", Label: label, Items: items})
				break
			}
			b, _ := json.MarshalIndent(v, "", "  ")
			f.add(ask.Block{Kind: "code", Label: label, Text: visible(string(b))})
		case map[string]any:
			b, _ := json.MarshalIndent(v, "", "  ")
			f.add(ask.Block{Kind: "code", Label: label, Text: visible(string(b))})
		default: // numbers (exact digits) and null
			b, _ := json.Marshal(v)
			f.add(ask.Block{Kind: "field", Label: label, Text: visible(string(b)), Mono: true})
		}
	}
	f.m = map[string]any{}
}

// restLabels names each key for people ("file_path" reads "File path").
// If two would then read the same, every key keeps its own spelling, so
// no two rows can be mistaken for each other.
func restLabels(keys []string) map[string]string {
	human := map[string]string{}
	seen := map[string]bool{}
	clash := false
	for _, k := range keys {
		l := k
		if plainKey.MatchString(k) {
			if words := strings.Join(strings.FieldsFunc(k, func(r rune) bool { return r == '_' || r == '-' }), " "); words != "" {
				l = strings.ToUpper(words[:1]) + words[1:]
			}
		}
		clash = clash || seen[l]
		seen[l] = true
		human[k] = l
	}
	out := map[string]string{}
	for _, k := range keys {
		switch {
		case !plainKey.MatchString(k):
			kb, _ := json.Marshal(k)
			out[k] = visible(string(kb))
		case clash:
			out[k] = k
		default:
			out[k] = human[k]
		}
	}
	return out
}

// shortWords is a list of short one-line strings, each shown as a chip.
func shortWords(v []any) ([]string, bool) {
	if len(v) == 0 || len(v) > 12 {
		return nil, false
	}
	var out []string
	for _, x := range v {
		s, ok := x.(string)
		if !ok || s == "" || len([]rune(s)) > 40 || strings.ContainsAny(s, "\n\r") {
			return nil, false
		}
		out = append(out, visible(s))
	}
	return out, true
}

// lineDiff is old turned into repl line by line: kept lines once, removed
// and added ones marked. Every line of both is in it. Too big to compare
// cheaply, it's all of old removed, then all of repl added.
func lineDiff(old, repl string) []ask.DiffLine {
	a, b := strings.Split(old, "\n"), strings.Split(repl, "\n")
	if old == "" {
		a = nil
	}
	if repl == "" {
		b = nil
	}
	var out []ask.DiffLine
	if len(a)*len(b) > 250000 {
		for _, l := range a {
			out = append(out, ask.DiffLine{Op: "-", Text: l})
		}
		for _, l := range b {
			out = append(out, ask.DiffLine{Op: "+", Text: l})
		}
		return out
	}
	// Longest common subsequence, from the end.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out = append(out, ask.DiffLine{Op: " ", Text: a[i]})
			i, j = i+1, j+1
		// Removed before added, as diffs read.
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, ask.DiffLine{Op: "-", Text: a[i]})
			i++
		default:
			out = append(out, ask.DiffLine{Op: "+", Text: b[j]})
			j++
		}
	}
	return out
}

// alwaysChoices are Claude Code's own "don't ask again" suggestions that the
// notch can offer: allow rules, extra directories, and accepting edits.
// Each comes back to Claude Code exactly as it was sent; the app only sees
// its description, and answers with an index. A standing grant is offered
// only when its description is short and plain enough to read whole: at
// most three rules or directories, one line, maxAlways characters. Anything
// bigger is left to Claude Code's own prompt.
func alwaysChoices(suggestions []json.RawMessage) (labels []string, raw []json.RawMessage) {
	where := map[string]string{
		"session": "for this session", "localSettings": "in this project, for you",
		"projectSettings": "in this project, for everyone", "userSettings": "everywhere",
	}
	for _, s := range suggestions {
		var e struct {
			Type     string `json:"type"`
			Behavior string `json:"behavior"`
			Mode     string `json:"mode"`
			Dest     string `json:"destination"`
			Rules    []struct {
				Tool    string  `json:"toolName"`
				Content *string `json:"ruleContent"`
			} `json:"rules"`
			Dirs []string `json:"directories"`
		}
		if json.Unmarshal(s, &e) != nil || where[e.Dest] == "" {
			continue
		}
		var what string
		switch {
		case e.Type == "addRules" && e.Behavior == "allow" && len(e.Rules) > 0 && len(e.Rules) <= 3:
			var rs []string
			for _, r := range e.Rules {
				name, _ := ask.ToolLabel(r.Tool)
				if r.Content != nil {
					name += "(" + *r.Content + ")"
				}
				rs = append(rs, name)
			}
			what = "Always allow " + strings.Join(rs, ", ")
		case e.Type == "addDirectories" && len(e.Dirs) > 0 && len(e.Dirs) <= 3:
			what = "Always allow access to " + strings.Join(e.Dirs, ", ")
		case e.Type == "setMode" && e.Mode == "acceptEdits":
			what = "Allow all edits"
		default:
			continue
		}
		label := what + " " + where[e.Dest]
		if strings.ContainsAny(label, "\n\r") || len([]rune(label)) > maxAlways || visible(label) != label {
			continue
		}
		labels = append(labels, label)
		raw = append(raw, s)
		if len(raw) == 2 {
			break
		}
	}
	return labels, raw
}
