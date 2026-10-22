package ask

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestWatchPermission(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	appendLine(t, p, `{"type":"user","message":{"content":"run the tests"}}`)
	appendLine(t, p, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"make test"}}]}}`)
	w := NewWatch(p, "", "Bash", json.RawMessage(`{"command":"make test"}`))
	if w.Done() {
		t.Fatal("done before anything happened")
	}
	// The other call's result isn't ours.
	appendLine(t, p, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`)
	if w.Done() {
		t.Fatal("done on another call's result")
	}
	appendLine(t, p, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"PASS"}]}}`)
	if !w.Done() {
		t.Fatal("not done after our result")
	}
}

func TestWatchQuestion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	appendLine(t, p, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{}}]}}`)
	w := NewWatch(p, "q1", "AskUserQuestion", nil)
	appendLine(t, p, `{"type":"user","isMeta":true,"message":{"content":"caveat"}}`)
	if w.Done() {
		t.Fatal("done on a meta line")
	}
	// You pressed Esc and typed something else.
	appendLine(t, p, `{"type":"user","message":{"content":"never mind, do X"}}`)
	if !w.Done() {
		t.Fatal("not done after a new prompt")
	}
	if NewWatch("", "", "Bash", nil).Done() {
		t.Fatal("no transcript should never be done")
	}
}

func TestPendingDropsDeadHook(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	now := time.Now()
	live := Ask{ID: NewID(), SessionID: "s", Kind: KindQuestion, PID: os.Getpid(), At: now, Deadline: now.Add(time.Minute)}
	dead := Ask{ID: NewID(), SessionID: "s", Kind: KindQuestion, PID: 1 << 30, At: now, Deadline: now.Add(time.Minute)}
	if Post(live) != nil || Post(dead) != nil {
		t.Fatal("post")
	}
	got := Pending(now)
	if len(got) != 1 || got[0].ID != live.ID {
		t.Fatalf("pending = %+v", got)
	}
}

func TestWatchRewordedInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	appendLine(t, p, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"e0","name":"Edit","input":{"file_path":"a.go"}}]}}`)
	appendLine(t, p, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"e0"}]}}`)
	w := NewWatch(p, "", "Edit", json.RawMessage(`{"file_path":"/abs/b.go"}`))
	appendLine(t, p, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"b.go"}}]}}`)
	appendLine(t, p, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x9"}]}}`)
	if w.Done() {
		t.Fatal("done on an unrelated result")
	}
	appendLine(t, p, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"e1"}]}}`)
	if !w.Done() {
		t.Fatal("not done after the only open Edit finished")
	}
}
