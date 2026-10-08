package usage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLedgerOpencode(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("OPENCODE_DB", "")
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "opencode")
	os.MkdirAll(dir, 0o755)
	db, err := sql.Open("sqlite", filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := func(sec int) int64 { return base.Add(time.Duration(sec) * time.Second).UnixMilli() }
	for _, q := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, directory TEXT)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, data TEXT)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, data TEXT)`,
		`INSERT INTO session VALUES ('ses_1', NULL, '/z/app'), ('ses_kid', 'ses_1', '/z/app'), ('ses_2', NULL, '/z/api')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	ins := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(q, err)
		}
	}
	// v1: a prompt, a completed reply with two tool calls, a subagent's
	// synthetic prompt and reply, then a reply still running.
	ins(`INSERT INTO message VALUES ('msg_1','ses_1',?,'{"role":"user"}')`, at(0))
	ins(`INSERT INTO message VALUES ('msg_2','ses_1',?,?)`, at(5),
		`{"role":"assistant","modelID":"claude-sonnet-5","time":{"completed":`+itoa64(at(20))+`},"tokens":{"input":10,"output":20,"reasoning":5,"cache":{"read":1000,"write":50}}}`)
	ins(`INSERT INTO part VALUES ('prt_1','msg_2','ses_1','{"type":"tool","tool":"bash"}'), ('prt_2','msg_2','ses_1','{"type":"tool","tool":"read"}'), ('prt_3','msg_2','ses_1','{"type":"text"}')`)
	ins(`INSERT INTO message VALUES ('msg_3','ses_kid',?,'{"role":"user"}')`, at(25))
	ins(`INSERT INTO message VALUES ('msg_4','ses_kid',?,?)`, at(26),
		`{"role":"assistant","modelID":"claude-haiku-4-5","time":{"completed":`+itoa64(at(40))+`},"tokens":{"input":1,"output":2,"reasoning":0,"cache":{"read":0,"write":0}}}`)
	ins(`INSERT INTO message VALUES ('msg_5','ses_1',?,'{"role":"assistant","modelID":"claude-sonnet-5","time":{},"tokens":{"input":999}}')`, at(50))
	// v2: a prompt and a completed reply with one tool.
	ins(`INSERT INTO session_message VALUES ('msg_a','ses_2','user',1,?,'{"text":"hi"}')`, at(100))
	ins(`INSERT INTO session_message VALUES ('msg_b','ses_2','assistant',2,?,?)`, at(101),
		`{"model":{"id":"gpt-6"},"content":[{"type":"tool","name":"edit"},{"type":"text"}],"tokens":{"input":7,"output":3,"reasoning":1,"cache":{"read":90,"write":0}},"time":{"completed":`+itoa64(at(110))+`}}`)

	l := &Ledger{Dir: filepath.Join(home, "ledger"), Home: home}
	ctx := context.Background()
	if err := l.Update(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// The running reply finishes; a second read counts it once, and only it.
	ins(`UPDATE message SET data = json_set(data, '$.time.completed', ?, '$.tokens.output', 1) WHERE id = 'msg_5'`, at(60))
	if err := l.Update(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Update(ctx, nil); err != nil {
		t.Fatal(err)
	}

	d := l.day(base.Local().Format("2006-01-02"))
	var oc Counters
	for k, c := range d.Buckets {
		if contains(k, "|opencode|") {
			oc.Add(*c)
		}
	}
	if want := (Tokens{Input: 10 + 1 + 999 + 7, CacheRead: 1090, CacheWrite: 50, Output: 25 + 2 + 1 + 4}); oc.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", oc.Tokens, want)
	}
	if oc.Replies != 4 || oc.Prompts != 2 || oc.Tools != 3 {
		t.Errorf("counts = %+v", oc)
	}
	if d.Tools["opencode|bash"] != 1 || d.Tools["opencode|edit"] != 1 {
		t.Errorf("tools = %v", d.Tools)
	}
	if _, ok := d.Sessions["ses_kid"]; ok {
		t.Error("subagent session should roll up to its parent")
	}
	if oc.Active != 60+10 { // ses_1: 0s → 60s; ses_2: 100s → 110s
		t.Errorf("active = %v", oc.Active)
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
