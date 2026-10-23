package ask

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPostRefusesBadID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, id := range []string{"", "a", "../x", "20261022160418-0123456789abcdef/../../x"} {
		if Post(Ask{ID: id, Deadline: time.Now().Add(time.Minute)}) == nil {
			t.Errorf("posted %q", id)
		}
	}
}

func TestDirIsPrivate(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(Dir(), 0o777)
	if Pending(time.Now()) != nil {
		t.Fatal("read questions from a directory others can write")
	}
	if err := Post(Ask{ID: NewID(), Deadline: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(Dir()); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode().Perm())
	}
}

func TestWaitIgnoresLinkedAnswer(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := NewID()
	if err := Post(Ask{ID: id, Deadline: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "allow.json")
	if err := os.WriteFile(elsewhere, []byte(`{"action":"allow"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, answerPath(id)); err != nil {
		t.Fatal(err)
	}
	if _, ok := Wait(id, time.Now().Add(300*time.Millisecond), nil, nil); ok {
		t.Fatal("took an answer through a link")
	}
	_ = os.Remove(answerPath(id))
	if err := os.WriteFile(answerPath(id), []byte(`{"action":"deny"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ans, ok := Wait(id, time.Now().Add(time.Second), nil, nil); !ok || ans.Action != "deny" {
		t.Fatalf("answer = %+v, %v", ans, ok)
	}
}

func TestPendingSkipsRenamedQuestion(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	now := time.Now()
	a := Ask{ID: NewID(), SessionID: "s", Kind: KindQuestion, At: now, Deadline: now.Add(time.Minute)}
	if err := Post(a); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(askPath(a.ID), filepath.Join(Dir(), "other.json")); err != nil {
		t.Fatal(err)
	}
	if got := Pending(now); len(got) != 0 {
		t.Fatalf("pending = %+v", got)
	}
}

func TestToolLabel(t *testing.T) {
	for tool, want := range map[string][2]string{
		"Bash":                            {"Bash", ""},
		"mcp__ccd_session__start_session": {"Start session", "ccd_session"},
		"mcp__plugin_playwright_playwright__browser_click": {"Browser click", "plugin_playwright_playwright"},
		"mcp__github__create-pull-request":                 {"Create pull request", "github"},
		"mcp__linear__getIssue":                            {"GetIssue", "linear"},
		"mcp__broken":                                      {"mcp__broken", ""},
		"mcp____x":                                         {"mcp____x", ""},
	} {
		name, server := ToolLabel(tool)
		if name != want[0] || server != want[1] {
			t.Errorf("ToolLabel(%q) = %q, %q; want %q, %q", tool, name, server, want[0], want[1])
		}
	}
}
