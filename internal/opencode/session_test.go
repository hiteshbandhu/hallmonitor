package opencode

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/model"
)

// fixture builds a database with opencode's tables (the columns we read).
func fixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	for _, q := range []string{
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT, slug TEXT, directory TEXT,
			title TEXT, version TEXT, agent TEXT, model TEXT, time_created INTEGER, time_updated INTEGER, time_archived INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`CREATE TABLE session_input (id TEXT PRIMARY KEY, session_id TEXT, prompt TEXT, delivery TEXT, admitted_seq INTEGER, promoted_seq INTEGER, time_created INTEGER)`,
	} {
		if _, err := w.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	return w, path
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(q, err)
	}
}

func TestReadV1(t *testing.T) {
	w, path := fixture(t)
	now := time.Now()
	ms := func(ago time.Duration) int64 { return now.Add(-ago).UnixMilli() }
	exec(t, w, `INSERT INTO session VALUES ('ses_a','p',NULL,'s','/code/app','Fix the flaky test','1.18.34','build','{"id":"claude-sonnet-5"}',?,?,NULL)`, ms(time.Hour), ms(time.Second))
	exec(t, w, `INSERT INTO session VALUES ('ses_old','p',NULL,'s','/code/app','old','1.18.34','build',NULL,?,?,NULL)`, ms(72*time.Hour), ms(48*time.Hour))
	exec(t, w, `INSERT INTO message VALUES ('msg_1','ses_a',?,?,'{"role":"user","model":{"providerID":"anthropic","modelID":"claude-sonnet-5"}}')`, ms(time.Minute), ms(time.Minute))
	exec(t, w, `INSERT INTO part VALUES ('prt_1','msg_1','ses_a',?,?,'{"type":"text","text":"why does checkout fail?"}')`, ms(time.Minute), ms(time.Minute))
	exec(t, w, `INSERT INTO part VALUES ('prt_2','msg_1','ses_a',?,?,'{"type":"text","text":"<system>","synthetic":true}')`, ms(time.Minute), ms(time.Minute))
	exec(t, w, `INSERT INTO message VALUES ('msg_2','ses_a',?,?,'{"role":"assistant","modelID":"claude-sonnet-5","time":{"created":1},"tokens":{"input":100,"output":5,"cache":{"read":40000,"write":2000}}}')`, ms(50*time.Second), ms(time.Second))
	exec(t, w, `INSERT INTO part VALUES ('prt_3','msg_2','ses_a',?,?,'{"type":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"/code/app/checkout.ts"},"title":"checkout.ts"}}')`, ms(40*time.Second), ms(39*time.Second))
	exec(t, w, `INSERT INTO part VALUES ('prt_4','msg_2','ses_a',?,?,'{"type":"tool","tool":"bash","state":{"status":"running","input":{"command":"npm test","description":"Run the tests"}}}')`, ms(10*time.Second), ms(10*time.Second))
	exec(t, w, `INSERT INTO part VALUES ('prt_5','msg_2','ses_a',?,?,'{"type":"tool","tool":"task","state":{"status":"running","input":{"description":"Map the payment flow","prompt":"…"}}}')`, ms(9*time.Second), ms(9*time.Second))

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rows, err := Sessions(ctx, db, now.Add(-time.Hour))
	if err != nil || len(rows) != 1 || rows[0].ID != "ses_a" || rows[0].Model != "claude-sonnet-5" || rows[0].Directory != "/code/app" {
		t.Fatalf("sessions = %+v, %v", rows, err)
	}
	st, err := Read(ctx, db, "ses_a", now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != model.StatusBusy || st.Prompt != "why does checkout fail?" || st.Context != 42100 {
		t.Fatalf("state = %+v", st)
	}
	if st.Last != "task · Map the payment flow" || st.Sub == nil || st.Sub.Running != 1 || len(st.Activity) == 0 {
		t.Fatalf("last = %q sub = %+v", st.Last, st.Sub)
	}

	// The question tool means it's waiting on you.
	exec(t, w, `INSERT INTO part VALUES ('prt_6','msg_2','ses_a',?,?,'{"type":"tool","tool":"question","state":{"status":"running","input":{"questions":[{"question":"Use Postgres or SQLite?"}]}}}')`, ms(time.Second), ms(time.Second))
	st, _ = Read(ctx, db, "ses_a", now)
	if st.Status != model.StatusWaiting || st.Last != "asks · Use Postgres or SQLite?" {
		t.Fatalf("question: %+v", st)
	}

	// Completed: idle. Failed: error with the message.
	exec(t, w, `UPDATE message SET data = json_set(data, '$.time.completed', ?) WHERE id = 'msg_2'`, ms(0))
	if st, _ = Read(ctx, db, "ses_a", now); st.Status != model.StatusIdle {
		t.Fatalf("completed: %+v", st)
	}
	exec(t, w, `UPDATE message SET data = json_set(data, '$.error', json('{"name":"APIError","data":{"message":"overloaded"}}')) WHERE id = 'msg_2'`)
	if st, _ = Read(ctx, db, "ses_a", now); st.Status != model.StatusError || st.Last != "error: overloaded" {
		t.Fatalf("error: %+v", st)
	}
	exec(t, w, `UPDATE message SET data = json_set(data, '$.error', json('{"name":"MessageAbortedError","data":{}}')) WHERE id = 'msg_2'`)
	if st, _ = Read(ctx, db, "ses_a", now); st.Status != model.StatusIdle || st.Last != "turn aborted" {
		t.Fatalf("aborted: %+v", st)
	}
}

func TestReadV2(t *testing.T) {
	w, path := fixture(t)
	now := time.Now()
	ms := func(ago time.Duration) int64 { return now.Add(-ago).UnixMilli() }
	exec(t, w, `INSERT INTO session VALUES ('ses_b','p',NULL,'s','/code/api','New session - 2026-10-08T10:00:00.000Z','2.0.0','build',NULL,?,?,NULL)`, ms(time.Hour), ms(time.Second))
	exec(t, w, `INSERT INTO session_message VALUES ('msg_1','ses_b','user',1,?,?,'{"text":"add rate limiting","files":[],"agents":[],"time":{"created":1}}')`, ms(time.Minute), ms(time.Minute))
	exec(t, w, `INSERT INTO session_message VALUES ('msg_2','ses_b','assistant',2,?,?,?)`, ms(50*time.Second), ms(time.Second),
		`{"agent":"build","model":{"id":"gpt-6","providerID":"openai"},"content":[{"type":"text","id":"t","text":"Looking."},{"type":"tool","id":"c1","name":"edit","state":{"status":"running","input":{"filePath":"/code/api/limit.go"}},"time":{"created":`+strconv.FormatInt(ms(5*time.Second), 10)+`}}],"tokens":{"input":10,"output":1,"reasoning":0,"cache":{"read":990,"write":0}},"time":{"created":1}}`)

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := Read(ctx, db, "ses_b", now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != model.StatusBusy || st.Prompt != "add rate limiting" || st.Model != "gpt-6" || st.Context != 1000 || st.Last != "edit · limit.go" {
		t.Fatalf("state = %+v", st)
	}
	exec(t, w, `UPDATE session_message SET data = json_set(data, '$.time.completed', ?) WHERE id = 'msg_2'`, ms(0))
	if st, _ = Read(ctx, db, "ses_b", now); st.Status != model.StatusIdle {
		t.Fatalf("completed: %+v", st)
	}
	// A queued prompt not yet turned into a message.
	exec(t, w, `INSERT INTO session_input VALUES ('in_1','ses_b','{}','queue',3,NULL,?)`, ms(0))
	if st, _ = Read(ctx, db, "ses_b", now); st.Status != model.StatusBusy {
		t.Fatalf("queued: %+v", st)
	}
	if !DefaultTitle("New session - 2026-10-08T10:00:00.000Z") || DefaultTitle("Fix it") {
		t.Fatal("DefaultTitle")
	}
}
