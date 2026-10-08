package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/model"
)

// Row is a session row.
type Row struct {
	ID, Title, Directory, ParentID, Model, Agent, Version string
	Created, Updated                                      time.Time
}

const sessionCols = `id, title, directory, COALESCE(parent_id,''), COALESCE(json_extract(model,'$.id'),''),
	COALESCE(agent,''), version, time_created, time_updated`

func scanRow(sc interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var c, u int64
	err := sc.Scan(&r.ID, &r.Title, &r.Directory, &r.ParentID, &r.Model, &r.Agent, &r.Version, &c, &u)
	r.Created, r.Updated = time.UnixMilli(c), time.UnixMilli(u)
	return r, err
}

// Sessions are the root, unarchived sessions updated since since, newest first.
func Sessions(ctx context.Context, db *sql.DB, since time.Time) ([]Row, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+` FROM session
		WHERE parent_id IS NULL AND time_archived IS NULL AND time_updated >= ?
		ORDER BY time_updated DESC LIMIT 200`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Session reads one session row.
func Session(ctx context.Context, db *sql.DB, id string) (Row, error) {
	return scanRow(db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM session WHERE id = ?`, id))
}

// Latest is the newest root session in dir, for `opencode --continue`.
func Latest(ctx context.Context, db *sql.DB, dir string) (Row, error) {
	return scanRow(db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM session
		WHERE parent_id IS NULL AND time_archived IS NULL AND directory = ?
		ORDER BY time_updated DESC LIMIT 1`, dir))
}

// State is what the board shows for a session, read from its newest
// messages. opencode keeps run status, permission prompts and questions in
// memory only, so this is inferred: an assistant reply that hasn't completed
// means a turn is running, and a running question tool means it's asking.
type State struct {
	Status   model.Status
	Since    time.Time
	Last     string
	Prompt   string
	Model    string
	Context  int
	Activity []int64
	Sub      *model.Subagents
}

// Read describes session id. It reads v2 messages (session_message) when the
// session has any, else the v1 message and part tables.
func Read(ctx context.Context, db *sql.DB, id string, now time.Time) (State, error) {
	if HasTable(db, "session_message") {
		var n int
		if db.QueryRowContext(ctx, `SELECT count(*) FROM session_message WHERE session_id = ? LIMIT 1`, id).Scan(&n) == nil && n > 0 {
			return readV2(ctx, db, id, now)
		}
	}
	return readV1(ctx, db, id, now)
}

// ---- v1: message + part ----

type v1Message struct {
	ID      string
	Created time.Time
	Role    string `json:"role"`
	ModelID string `json:"modelID"`
	Model   struct {
		ModelID string `json:"modelID"`
	} `json:"model"`
	Time struct {
		Completed int64 `json:"completed"`
	} `json:"time"`
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
	Tokens struct {
		Input int64 `json:"input"`
		Cache struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

func readV1(ctx context.Context, db *sql.DB, id string, now time.Time) (State, error) {
	st := State{Status: model.StatusIdle}
	// Message data is small; the bulky content lives in parts.
	rows, err := db.QueryContext(ctx, `SELECT id, time_created, data FROM message
		WHERE session_id = ? ORDER BY time_created DESC, id DESC LIMIT 40`, id)
	if err != nil {
		return st, err
	}
	var msgs []v1Message // newest first
	for rows.Next() {
		var m v1Message
		var c int64
		var data string
		if rows.Scan(&m.ID, &c, &data) != nil {
			continue
		}
		_ = json.Unmarshal([]byte(data), &m)
		m.Created = time.UnixMilli(c)
		msgs = append(msgs, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	var lastUser, lastAsst *v1Message
	for i := range msgs {
		m := &msgs[i]
		if m.Role == "user" && lastUser == nil {
			lastUser = m
		}
		if m.Role == "assistant" && lastAsst == nil {
			lastAsst = m
		}
	}
	if lastUser == nil {
		// A long agentic turn can push the prompt past the window above.
		var m v1Message
		var c int64
		var data string
		if db.QueryRowContext(ctx, `SELECT id, time_created, data FROM message WHERE session_id = ?
			AND json_extract(data,'$.role') = 'user' ORDER BY time_created DESC, id DESC LIMIT 1`, id).Scan(&m.ID, &c, &data) == nil {
			_ = json.Unmarshal([]byte(data), &m)
			m.Created = time.UnixMilli(c)
			lastUser = &m
		}
	}
	if lastUser != nil {
		st.Prompt = model.Snip(userText(ctx, db, lastUser.ID), 120)
		st.Model = lastUser.Model.ModelID
		st.Since = lastUser.Created
	}
	if lastAsst != nil {
		if lastAsst.ModelID != "" {
			st.Model = lastAsst.ModelID
		}
		t := lastAsst.Tokens
		st.Context = int(t.Input + t.Cache.Read + t.Cache.Write)
	}

	// The turn is running when the newest message is the user's, or an
	// assistant reply that hasn't completed or failed.
	if len(msgs) > 0 {
		m := msgs[0]
		switch {
		case m.Role == "user":
			st.Status, st.Since = model.StatusBusy, m.Created
		case m.Error != nil:
			st.Status = model.StatusIdle
			if m.Error.Name == "MessageAbortedError" {
				st.Last = "turn aborted"
			} else {
				st.Status = model.StatusError
				msg := m.Error.Data.Message
				if msg == "" {
					msg = m.Error.Name
				}
				st.Last = "error: " + model.Snip(msg, 110)
			}
		case m.Time.Completed == 0:
			st.Status = model.StatusBusy
		default:
			st.Since = time.UnixMilli(m.Time.Completed)
		}
	}

	// Parts of the recent turn(s): tool calls for Last, timestamps for the
	// activity strip, task calls for subagents. Only short fields are pulled.
	from := now.Add(-model.ActivityWindow)
	if lastUser != nil && lastUser.Created.Before(from) {
		from = lastUser.Created
	}
	prows, err := db.QueryContext(ctx, `SELECT time_created, time_updated,
			COALESCE(json_extract(data,'$.type'),''), COALESCE(json_extract(data,'$.tool'),''),
			COALESCE(json_extract(data,'$.state.status'),''), COALESCE(json_extract(data,'$.state.title'),''),
			COALESCE(json_extract(data,'$.state.input.description'), json_extract(data,'$.state.input.filePath'),
				json_extract(data,'$.state.input.pattern'), json_extract(data,'$.state.input.url'),
				json_extract(data,'$.state.input.query'), json_extract(data,'$.state.input.command'),
				json_extract(data,'$.state.input.questions[0].question'), '')
		FROM part WHERE session_id = ? AND time_created >= ? ORDER BY id`, id, from.UnixMilli())
	if err != nil {
		return st, err
	}
	defer prows.Close()
	var parts []part
	for prows.Next() {
		var p part
		var c, u int64
		if prows.Scan(&c, &u, &p.typ, &p.tool, &p.status, &p.title, &p.hint) != nil {
			continue
		}
		p.created, p.updated = time.UnixMilli(c), time.UnixMilli(u)
		parts = append(parts, p)
	}
	last := st.Last
	applyParts(&st, parts, now)
	if last != "" { // the error or abort says more than the last tool
		st.Last = last
	}
	return st, prows.Err()
}

// userText is a v1 user message's prompt: its non-synthetic text parts.
func userText(ctx context.Context, db *sql.DB, msgID string) string {
	rows, err := db.QueryContext(ctx, `SELECT substr(COALESCE(json_extract(data,'$.text'),''), 1, 400) FROM part
		WHERE message_id = ? AND json_extract(data,'$.type') = 'text'
		AND COALESCE(json_extract(data,'$.synthetic'), 0) = 0 ORDER BY id`, msgID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var b []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil && strings.TrimSpace(s) != "" {
			b = append(b, s)
		}
	}
	return strings.Join(b, " ")
}

// part is the slice of a tool or text part the board needs, from either
// storage version.
type part struct {
	created, updated               time.Time
	typ, tool, status, title, hint string
}

func applyParts(st *State, parts []part, now time.Time) {
	var sub model.Subagents
	for _, p := range parts {
		st.Activity = model.AddActivity(st.Activity, p.created, now)
		if p.typ != "tool" {
			continue
		}
		line := toolLine(p.tool, p.title, p.hint)
		st.Last = line
		if p.tool == "task" {
			sub.Total++
			if p.status == "running" || p.status == "pending" {
				sub.Running++
				d := p.hint
				if d == "" {
					d = p.title
				}
				sub.Active = append([]string{model.Snip(d, 60)}, sub.Active...)
			}
		}
		// The question tool blocks until the user answers.
		if p.tool == "question" && (p.status == "running" || p.status == "pending") && st.Status == model.StatusBusy {
			st.Status = model.StatusWaiting
			st.Since = p.created
			st.Last = "asks · " + model.Snip(p.hint, 100)
		}
	}
	if sub.Total > 0 {
		if len(sub.Active) > 3 {
			sub.Active = sub.Active[:3]
		}
		st.Sub = &sub
	}
}

// toolLine is "tool · hint", like the other providers' cards.
func toolLine(tool, title, hint string) string {
	h := title
	if h == "" {
		h = hint
	}
	if strings.HasPrefix(h, "/") {
		h = filepath.Base(h)
	}
	if h == "" {
		return tool
	}
	return tool + " · " + model.Snip(h, 70)
}

// ---- v2: session_message ----

type v2Message struct {
	Text  string `json:"text"`
	Model struct {
		ID string `json:"id"`
	} `json:"model"`
	Content []struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		State struct {
			Status string          `json:"status"`
			Input  json.RawMessage `json:"input"`
		} `json:"state"`
		Time struct {
			Created int64 `json:"created"`
		} `json:"time"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Tokens struct {
		Input int64 `json:"input"`
		Cache struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

func readV2(ctx context.Context, db *sql.DB, id string, now time.Time) (State, error) {
	st := State{Status: model.StatusIdle}
	rows, err := db.QueryContext(ctx, `SELECT type, time_created, data FROM session_message
		WHERE session_id = ? AND type IN ('user','assistant','shell') ORDER BY seq DESC LIMIT 12`, id)
	if err != nil {
		return st, err
	}
	type row struct {
		typ     string
		created time.Time
		m       v2Message
	}
	var msgs []row // newest first
	for rows.Next() {
		var r row
		var c int64
		var data string
		if rows.Scan(&r.typ, &c, &data) != nil {
			continue
		}
		r.created = time.UnixMilli(c)
		_ = json.Unmarshal([]byte(data), &r.m)
		msgs = append(msgs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	var parts []part
	gotUser, gotAsst := false, false
	for _, r := range msgs {
		switch r.typ {
		case "user":
			if !gotUser {
				st.Prompt = model.Snip(r.m.Text, 120)
				gotUser = true
			}
		case "assistant":
			if !gotAsst {
				st.Model = r.m.Model.ID
				t := r.m.Tokens
				st.Context = int(t.Input + t.Cache.Read + t.Cache.Write)
				gotAsst = true
			}
			if r.created.After(now.Add(-model.ActivityWindow)) {
				// Content is oldest first; collect in reverse to stay
				// chronological after the final reverse below.
				for i := len(r.m.Content) - 1; i >= 0; i-- {
					c := r.m.Content[i]
					at := time.UnixMilli(c.Time.Created)
					if c.Time.Created == 0 {
						at = r.created
					}
					p := part{created: at, updated: at, typ: c.Type, tool: c.Name, status: c.State.Status}
					if c.Type == "tool" {
						p.hint = inputHint(c.State.Input)
					}
					parts = append(parts, p)
				}
			}
		}
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}

	if len(msgs) > 0 {
		r := msgs[0]
		switch {
		case r.typ == "user":
			st.Status, st.Since = model.StatusBusy, r.created
		case r.typ == "assistant" && r.m.Error != nil:
			st.Status = model.StatusError
			st.Last = "error: " + model.Snip(r.m.Error.Message, 110)
		case r.typ == "assistant" && r.m.Time.Completed == 0:
			st.Status, st.Since = model.StatusBusy, r.created
		default:
			st.Since = r.created
			if r.m.Time.Completed > 0 {
				st.Since = time.UnixMilli(r.m.Time.Completed)
			}
		}
	}
	// Input accepted but not yet turned into a message: queued or starting.
	if st.Status != model.StatusBusy && HasTable(db, "session_input") {
		var n int
		if db.QueryRowContext(ctx, `SELECT count(*) FROM session_input WHERE session_id = ? AND promoted_seq IS NULL`, id).Scan(&n) == nil && n > 0 {
			st.Status = model.StatusBusy
		}
	}
	last := st.Last
	applyParts(&st, parts, now)
	if last != "" {
		st.Last = last
	}
	return st, nil
}

func inputHint(raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, k := range []string{"description", "filePath", "pattern", "url", "query", "command"} {
		if v, ok := in[k].(string); ok && v != "" {
			return v
		}
	}
	if qs, ok := in["questions"].([]any); ok && len(qs) > 0 {
		if q, ok := qs[0].(map[string]any); ok {
			v, _ := q["question"].(string)
			return v
		}
	}
	return ""
}
