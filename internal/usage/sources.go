package usage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hiteshbandhu/hallmonitor/internal/opencode"
)

// ---- Claude Code ----

func (l *Ledger) claudeFiles() []string {
	root := filepath.Join(l.Home, ".claude", "projects")
	a, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	b, _ := filepath.Glob(filepath.Join(root, "*", "*", "subagents", "*.jsonl"))
	probe := filepath.Join(root, EscapeProject(ProbeDir())) + string(filepath.Separator)
	var out []string
	for _, f := range append(a, b...) {
		if !strings.HasPrefix(f, probe) { // hallmonitor's own /usage probe
			out = append(out, f)
		}
	}
	return out
}

type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	IsMeta    bool   `json:"isMeta"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input       int64 `json:"input_tokens"`
			CacheRead   int64 `json:"cache_read_input_tokens"`
			CacheCreate int64 `json:"cache_creation_input_tokens"`
			Output      int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// scanClaude reads transcript lines. A reply is split over several lines
// (one per content block) that repeat the same usage, so each
// (message id, request id) is counted once. Subagent transcripts carry the
// parent's sessionId, so their tokens land on the parent session.
func (l *Ledger) scanClaude(path string, fs *fileState, data []byte) error {
	seen := seenSet(fs)
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		// Cheap prefilter: skip summaries, snapshots and the like.
		if !bytes.Contains(line, []byte(`"type":"assistant"`)) && !bytes.Contains(line, []byte(`"type":"user"`)) {
			continue
		}
		var cl claudeLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, cl.Timestamp)
		e := event{at: at, provider: "claude", session: cl.SessionID, project: project(cl.CWD), active: true}
		switch cl.Type {
		case "user":
			var s string
			if !cl.IsMeta && json.Unmarshal(cl.Message.Content, &s) == nil && humanPrompt(s) {
				e.c.Prompts = 1
			}
		case "assistant":
			model := cl.Message.Model
			if model == "" || strings.HasPrefix(model, "<") {
				continue // synthetic messages
			}
			e.model = model
			id := cl.Message.ID + "|" + cl.RequestID
			first := !seen[id]
			if first {
				remember(fs, seen, id)
				if u := cl.Message.Usage; u != nil {
					e.c.Tokens = Tokens{Input: u.Input, CacheRead: u.CacheRead, CacheWrite: u.CacheCreate, Output: u.Output}
				}
				e.c.Replies = 1
			}
			var blocks []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			_ = json.Unmarshal(cl.Message.Content, &blocks)
			for _, b := range blocks {
				if b.Type == "tool_use" && b.Name != "" {
					e.tools = append(e.tools, toolName(b.Name))
					e.c.Tools++
				}
			}
		default:
			continue
		}
		l.record(e)
	}
	return nil
}

func humanPrompt(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && !strings.HasPrefix(s, "<") && !strings.HasPrefix(s, "Caveat:")
}

// toolName shortens MCP tool names to server·tool.
func toolName(n string) string {
	if strings.HasPrefix(n, "mcp__") {
		parts := strings.Split(n, "__")
		if len(parts) >= 3 {
			return parts[len(parts)-1]
		}
	}
	return n
}

func project(cwd string) string {
	if cwd == "" {
		return "?"
	}
	// Worktrees roll up to their repo: …/repo/.claude/worktrees/x, …/repo/.kandy/worktrees/x
	for _, marker := range []string{"/.claude/worktrees/", "/.kandy/worktrees/", "/.worktrees/"} {
		if i := strings.Index(cwd, marker); i > 0 {
			cwd = cwd[:i]
		}
	}
	return filepath.Base(cwd)
}

// ---- Codex ----

func (l *Ledger) codexFiles() []string {
	root := filepath.Join(l.Home, ".codex")
	a, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	b, _ := filepath.Glob(filepath.Join(root, "archived_sessions", "rollout-*.jsonl"))
	return append(a, b...)
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexUsage struct {
	Input     int64 `json:"input_tokens"`
	Cached    int64 `json:"cached_input_tokens"`
	CacheW    int64 `json:"cache_write_input_tokens"`
	Output    int64 `json:"output_tokens"`
	Reasoning int64 `json:"reasoning_output_tokens"`
}

// scanCodex reads rollout lines. Token usage comes from token_usage_record
// (one per model response, deduped by response id). Codex counts cached
// input inside input_tokens, so it's split out to match Claude's shape.
func (l *Ledger) scanCodex(path string, fs *fileState, data []byte) error {
	if fs.Ctx == nil {
		fs.Ctx = map[string]string{}
	}
	seen := seenSet(fs)
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var cl codexLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, cl.Timestamp)
		var p struct {
			Type       string      `json:"type"`
			ID         string      `json:"id"`
			SessionID  string      `json:"session_id"`
			CWD        string      `json:"cwd"`
			Model      string      `json:"model"`
			Name       string      `json:"name"`
			ResponseID string      `json:"response_id"`
			Usage      *codexUsage `json:"usage"`
			Item       *struct {
				Type string `json:"type"`
			} `json:"item"`
			RateLimits *struct {
				Primary *struct {
					UsedPercent float64 `json:"used_percent"`
					WindowMin   int     `json:"window_minutes"`
					ResetsAt    int64   `json:"resets_at"`
				} `json:"primary"`
			} `json:"rate_limits"`
		}
		_ = json.Unmarshal(cl.Payload, &p)

		e := event{at: at, provider: "codex", session: fs.Ctx["session"], project: fs.Ctx["project"], model: fs.Ctx["model"]}
		switch cl.Type {
		case "session_meta":
			fs.Ctx["session"] = firstNonEmpty(p.SessionID, p.ID)
			fs.Ctx["project"] = project(p.CWD)
			continue
		case "turn_context":
			if p.Model != "" {
				fs.Ctx["model"] = p.Model
			}
			continue
		case "token_usage_record":
			if p.Usage == nil || p.ResponseID == "" || seen[p.ResponseID] {
				continue
			}
			remember(fs, seen, p.ResponseID)
			u := p.Usage
			e.c.Tokens = Tokens{Input: max(0, u.Input-u.Cached), CacheRead: u.Cached, CacheWrite: u.CacheW, Output: u.Output}
			e.c.Replies = 1
			e.active = true
		case "response_item":
			if p.Type == "function_call" || p.Type == "custom_tool_call" {
				if p.Name != "" {
					e.tools = []string{p.Name}
					e.c.Tools = 1
				}
			}
			e.active = true
		case "event_msg":
			switch p.Type {
			case "item_completed":
				if p.Item != nil && p.Item.Type == "UserMessage" {
					e.c.Prompts = 1
				}
			case "token_count":
				if rl := p.RateLimits; rl != nil && rl.Primary != nil {
					prev := l.st.RateLimits["codex"]
					if at.After(prev.ObservedAt) {
						l.st.RateLimits["codex"] = RateLimit{
							Provider:    "codex",
							UsedPercent: rl.Primary.UsedPercent,
							WindowMin:   rl.Primary.WindowMin,
							ResetsAt:    time.Unix(rl.Primary.ResetsAt, 0),
							ObservedAt:  at,
						}
					}
				}
			}
			e.active = true
		default:
			continue
		}
		l.record(e)
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---- opencode ----

// scanOpencode reads messages from opencode's database newer than the last
// read: tokens and replies from assistant messages, prompts from root
// sessions' user messages, tool calls from their parts. A reply is counted
// once it has completed (or failed), so the cursor stops at the oldest one
// still running. Subagent sessions roll up to their parent. Both storage
// versions are read, each with its own cursor.
func (l *Ledger) scanOpencode(ctx context.Context) {
	for _, path := range opencode.DBPaths(l.Home) {
		if ctx.Err() != nil {
			return
		}
		db, err := opencode.Open(path)
		if err != nil {
			continue
		}
		fs := l.st.Files[path]
		if fs == nil {
			fs = &fileState{}
			l.st.Files[path] = fs
		}
		if fs.Ctx == nil {
			fs.Ctx = map[string]string{}
		}
		for _, v := range []ocStore{ocV1, ocV2} {
			if opencode.HasTable(db, v.table) {
				_ = l.scanOpencodeStore(ctx, db, fs, v)
			}
		}
	}
}

// ocStore is one opencode storage version: how to read its messages and
// their tool calls in a common shape.
type ocStore struct {
	table, cursor string
	messages      string // args: after, after, afterID
	tools         string // %s: the id placeholders
}

var ocV1 = ocStore{
	table: "message", cursor: "",
	messages: `SELECT m.id, m.time_created, m.session_id, COALESCE(s.parent_id, ''), s.directory,
			COALESCE(json_extract(m.data,'$.role'),''), COALESCE(json_extract(m.data,'$.modelID'),''),
			COALESCE(json_extract(m.data,'$.time.completed'),0), json_extract(m.data,'$.error') IS NOT NULL,
			COALESCE(json_extract(m.data,'$.tokens.input'),0), COALESCE(json_extract(m.data,'$.tokens.output'),0),
			COALESCE(json_extract(m.data,'$.tokens.reasoning'),0),
			COALESCE(json_extract(m.data,'$.tokens.cache.read'),0), COALESCE(json_extract(m.data,'$.tokens.cache.write'),0)
		FROM message m JOIN session s ON s.id = m.session_id
		WHERE m.time_created > ? OR (m.time_created = ? AND m.id > ?)
		ORDER BY m.time_created, m.id LIMIT 20000`,
	tools: `SELECT message_id, json_extract(data,'$.tool') FROM part
		WHERE message_id IN (%s) AND json_extract(data,'$.type') = 'tool'`,
}

var ocV2 = ocStore{
	table: "session_message", cursor: "v2",
	messages: `SELECT m.id, m.time_created, m.session_id, COALESCE(s.parent_id, ''), s.directory,
			m.type, COALESCE(json_extract(m.data,'$.model.id'),''),
			COALESCE(json_extract(m.data,'$.time.completed'),0), json_extract(m.data,'$.error') IS NOT NULL,
			COALESCE(json_extract(m.data,'$.tokens.input'),0), COALESCE(json_extract(m.data,'$.tokens.output'),0),
			COALESCE(json_extract(m.data,'$.tokens.reasoning'),0),
			COALESCE(json_extract(m.data,'$.tokens.cache.read'),0), COALESCE(json_extract(m.data,'$.tokens.cache.write'),0)
		FROM session_message m JOIN session s ON s.id = m.session_id
		WHERE m.type IN ('user','assistant') AND (m.time_created > ? OR (m.time_created = ? AND m.id > ?))
		ORDER BY m.time_created, m.id LIMIT 20000`,
	tools: `SELECT m.id, json_extract(j.value,'$.name') FROM session_message m, json_each(m.data,'$.content') j
		WHERE m.id IN (%s) AND json_extract(j.value,'$.type') = 'tool'`,
}

type ocMsg struct {
	id, session, parent, dir, role, model string
	created, completed                    int64
	failed                                bool
	tok                                   Tokens
	reasoning                             int64
}

func (l *Ledger) scanOpencodeStore(ctx context.Context, db *sql.DB, fs *fileState, v ocStore) error {
	after, _ := strconv.ParseInt(fs.Ctx[v.cursor+"at"], 10, 64)
	afterID := fs.Ctx[v.cursor+"id"]
	rows, err := db.QueryContext(ctx, v.messages, after, after, afterID)
	if err != nil {
		return err
	}
	var msgs []ocMsg
	for rows.Next() {
		var m ocMsg
		if rows.Scan(&m.id, &m.created, &m.session, &m.parent, &m.dir, &m.role, &m.model, &m.completed, &m.failed,
			&m.tok.Input, &m.tok.Output, &m.reasoning, &m.tok.CacheRead, &m.tok.CacheWrite) == nil {
			msgs = append(msgs, m)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	stale := time.Now().Add(-6 * time.Hour).UnixMilli() // a reply this old never finishes
	var done []ocMsg
	for _, m := range msgs {
		if m.role == "assistant" && m.completed == 0 && !m.failed && m.created > stale {
			break
		}
		done = append(done, m)
	}
	tools := opencodeTools(ctx, db, v, done)
	for _, m := range done {
		session := m.session
		if m.parent != "" {
			session = m.parent
		}
		e := event{at: time.UnixMilli(m.created), provider: "opencode", session: session, project: project(m.dir), active: true}
		switch m.role {
		case "user":
			if m.parent == "" {
				e.c.Prompts = 1
			}
		case "assistant":
			if m.completed > 0 {
				e.at = time.UnixMilli(m.completed)
			}
			e.model = m.model
			e.c.Tokens = m.tok
			e.c.Tokens.Output += m.reasoning // opencode counts reasoning apart from output
			e.c.Replies = 1
			e.tools = tools[m.id]
			e.c.Tools = int64(len(e.tools))
		default:
			continue
		}
		l.record(e)
	}
	if n := len(done); n > 0 {
		fs.Ctx[v.cursor+"at"] = strconv.FormatInt(done[n-1].created, 10)
		fs.Ctx[v.cursor+"id"] = done[n-1].id
	}
	return nil
}

// opencodeTools lists the tool calls of each assistant message.
func opencodeTools(ctx context.Context, db *sql.DB, v ocStore, msgs []ocMsg) map[string][]string {
	out := map[string][]string{}
	var ids []any
	for _, m := range msgs {
		if m.role == "assistant" {
			ids = append(ids, m.id)
		}
	}
	for len(ids) > 0 {
		batch := ids[:min(len(ids), 500)]
		ids = ids[len(batch):]
		q := fmt.Sprintf(v.tools, "?"+strings.Repeat(",?", len(batch)-1))
		rows, err := db.QueryContext(ctx, q, batch...)
		if err != nil {
			return out
		}
		for rows.Next() {
			var id string
			var tool sql.NullString
			if rows.Scan(&id, &tool) == nil && tool.String != "" {
				out[id] = append(out[id], tool.String)
			}
		}
		rows.Close()
	}
	return out
}
