package ask

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
)

// Watch notices that a question was answered in Claude Code itself.
//
// Claude Code shows its own prompt while the hook waits, and doesn't stop
// the hook when you answer there: the tool call just carries on. So the hook
// watches the session transcript for that tool call's result, or for a new
// prompt from you, and withdraws the question from the notch.
type Watch struct {
	path  string
	off   int64
	id    string // the tool call, once known
	tool  string
	input any
	open  map[string]bool // calls to tool still without a result
}

// NewWatch watches transcript for the end of a call to tool with input, or
// the call toolUseID when Claude Code says which (PreToolUse does,
// PermissionRequest doesn't).
func NewWatch(transcript, toolUseID, tool string, input json.RawMessage) *Watch {
	w := &Watch{path: transcript, id: toolUseID, tool: tool, open: map[string]bool{}}
	_ = json.Unmarshal(input, &w.input)
	fi, err := os.Stat(transcript)
	if err != nil {
		return w
	}
	w.off = fi.Size()
	if w.id == "" {
		// The call is already in the transcript: Claude Code writes the
		// reply before it asks. Find the latest one that matches.
		start := max(0, w.off-1<<20)
		for _, line := range w.lines(start, w.off, start > 0) {
			w.see(line)
		}
	}
	return w
}

// Done reports whether the call ended or you moved on.
func (w *Watch) Done() bool {
	if w.path == "" {
		return false
	}
	fi, err := os.Stat(w.path)
	if err != nil || fi.Size() <= w.off {
		return false
	}
	lines := w.lines(w.off, fi.Size(), false)
	for _, line := range lines {
		w.off += int64(len(line)) + 1
		if w.see(line) {
			return true
		}
	}
	return false
}

type tEntry struct {
	Type    string `json:"type"`
	IsMeta  bool   `json:"isMeta"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type tBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

// see reads one transcript line: it learns the call's id from a matching
// tool_use, and reports true on its tool_result or a new prompt.
func (w *Watch) see(line []byte) bool {
	var e tEntry
	if json.Unmarshal(line, &e) != nil || len(e.Message.Content) == 0 {
		return false
	}
	var blocks []tBlock
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		// A plain string: a prompt you typed.
		var s string
		return e.Type == "user" && !e.IsMeta && json.Unmarshal(e.Message.Content, &s) == nil && s != ""
	}
	for _, b := range blocks {
		switch {
		case e.Type == "assistant" && b.Type == "tool_use" && b.Name == w.tool && b.ID != "":
			w.open[b.ID] = true
			var in any
			if json.Unmarshal(b.Input, &in) == nil && reflect.DeepEqual(in, w.input) {
				w.id = b.ID
			}
		case e.Type == "user" && b.Type == "tool_result":
			// Claude Code may have reworded the input; then the call is
			// the only one to that tool still running.
			only := w.id == "" && w.open[b.ToolUseID] && len(w.open) == 1
			delete(w.open, b.ToolUseID)
			if b.ToolUseID != "" && (b.ToolUseID == w.id || only) {
				return true
			}
		case e.Type == "user" && b.Type == "text" && !e.IsMeta:
			return true
		}
	}
	return false
}

// lines are the complete lines in [from, to); with partial, the first is
// cut and dropped.
func (w *Watch) lines(from, to int64, partial bool) [][]byte {
	f, err := os.Open(w.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, min(to-from, 32<<20))
	n, _ := io.ReadFull(io.NewSectionReader(f, from, int64(len(buf))), buf)
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return nil
	}
	out := bytes.Split(buf[:end], []byte{'\n'})
	if partial && len(out) > 0 {
		out = out[1:]
	}
	return out
}
