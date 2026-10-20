//! The data: `hallmonitor --stream` for agents, `hallmonitor usage --json`
//! for today's totals and plan limits. The same feed the Swift app reads;
//! nothing here knows about the UI.

use std::collections::BTreeMap;
use std::io::{BufRead, BufReader};
use std::path::Path;
use std::process::{Command, Stdio};
use std::thread;
use std::time::Duration;

use chrono::{DateTime, Utc};
use futures::channel::mpsc::UnboundedSender;
use serde::Deserialize;

#[derive(Clone, Debug, Deserialize)]
pub struct Session {
    #[serde(default)]
    pub host: String,
    pub provider: String,
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub pid: i64,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub cwd: String,
    pub status: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub last: String,
    #[serde(default)]
    pub prompt: String,
    pub status_since: Option<DateTime<Utc>>,
    pub updated_at: Option<DateTime<Utc>>,
    pub started_at: Option<DateTime<Utc>>,
    #[serde(default)]
    pub context_tokens: i64,
    pub subagents: Option<Subagents>,
    /// Unix seconds of recent tool calls and replies.
    #[serde(default)]
    pub activity: Vec<i64>,
    #[serde(default)]
    pub extra: BTreeMap<String, String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct Subagents {
    pub running: i64,
    pub total: i64,
    #[serde(default)]
    pub active: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct Snapshot {
    sessions: Vec<Session>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Tokens {
    #[serde(default, rename = "in")]
    pub input: i64,
    #[serde(default)]
    pub cache_read: i64,
    #[serde(default)]
    pub cache_write: i64,
    #[serde(default)]
    pub out: i64,
}

impl Tokens {
    pub fn total(&self) -> i64 {
        self.input + self.cache_read + self.cache_write + self.out
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Counters {
    #[serde(default)]
    pub tokens: Tokens,
    #[serde(default)]
    pub prompts: i64,
    #[serde(default)]
    pub tools: i64,
    #[serde(default)]
    pub active_s: f64,
}

#[derive(Clone, Debug, Deserialize)]
pub struct RateLimit {
    pub provider: String,
    #[serde(default)]
    pub window: String,
    pub used_percent: f64,
    #[serde(default)]
    pub window_minutes: i64,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Usage {
    #[serde(default)]
    pub total: Counters,
    #[serde(default)]
    pub rate_limits: BTreeMap<String, RateLimit>,
}

pub enum Msg {
    Sessions(Vec<Session>),
    Usage(Usage),
    Problem(String),
}

impl Session {
    pub fn key(&self) -> String {
        let id = if self.id.is_empty() { format!("pid:{}", self.pid) } else { self.id.clone() };
        format!("{}|{}|{}", self.host, self.provider, id)
    }
    pub fn project(&self) -> &str {
        Path::new(&self.cwd).file_name().and_then(|s| s.to_str()).unwrap_or(&self.cwd)
    }
    pub fn name(&self) -> &str {
        if self.title.is_empty() { self.project() } else { &self.title }
    }
    pub fn needs_you(&self) -> bool {
        self.status == "waiting" || self.status == "error"
    }
    pub fn working(&self) -> bool {
        self.status == "busy"
    }
    /// Idle for a day: kept out of the list.
    pub fn stale(&self, now: DateTime<Utc>) -> bool {
        let seen = self.updated_at.or(self.started_at);
        !self.needs_you() && !self.working() && seen.is_some_and(|t| (now - t).num_hours() >= 24)
    }
    pub fn running_subagents(&self) -> i64 {
        if self.working() || self.needs_you() { self.subagents.as_ref().map_or(0, |s| s.running) } else { 0 }
    }
}

/// The hallmonitor CLI: $HALLMONITOR_BIN, the installed app's, or PATH's.
pub fn binary() -> String {
    if let Ok(b) = std::env::var("HALLMONITOR_BIN") {
        return b;
    }
    let app = "/Applications/HallMonitor.app/Contents/Helpers/hallmonitor";
    if Path::new(app).exists() { app.into() } else { "hallmonitor".into() }
}

/// Runs the stream and the usage poll on their own threads, sending what
/// they read; restarts the stream if it dies.
pub fn start(tx: UnboundedSender<Msg>) {
    let stream_tx = tx.clone();
    thread::spawn(move || loop {
        let child = Command::new(binary())
            .args(["--stream", "--with-hosts", "--watch", "2s"])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn();
        match child {
            Ok(mut child) => {
                let out = child.stdout.take().expect("piped");
                for line in BufReader::new(out).lines() {
                    let Ok(line) = line else { break };
                    if let Ok(snap) = serde_json::from_str::<Snapshot>(&line) {
                        if stream_tx.unbounded_send(Msg::Sessions(snap.sessions)).is_err() {
                            let _ = child.kill();
                            return;
                        }
                    }
                }
                let _ = child.wait();
                let _ = stream_tx.unbounded_send(Msg::Problem("The hallmonitor feed stopped; restarting it…".into()));
            }
            Err(e) => {
                let _ = stream_tx.unbounded_send(Msg::Problem(format!("Can't run {}: {e}", binary())));
            }
        }
        thread::sleep(Duration::from_secs(3));
    });

    thread::spawn(move || loop {
        // output() reads while the command runs, so big output can't block it.
        if let Ok(out) = Command::new(binary()).args(["usage", "--days", "1", "--json"]).stderr(Stdio::null()).output() {
            if let Ok(u) = serde_json::from_slice::<Usage>(&out.stdout) {
                if tx.unbounded_send(Msg::Usage(u)).is_err() {
                    return;
                }
            }
        }
        thread::sleep(Duration::from_secs(60));
    });
}

/// Brings the agent's app or terminal to the front.
pub fn focus(s: &Session) {
    let mut args = vec!["focus".to_string(), "--provider".into(), s.provider.clone(), "--pid".into(), s.pid.to_string()];
    if !s.id.is_empty() {
        args.extend(["--id".into(), s.id.clone()]);
    }
    if let Some(e) = s.extra.get("entrypoint").filter(|e| !e.is_empty()) {
        args.extend(["--entrypoint".into(), e.clone()]);
    }
    if !s.host.is_empty() {
        args.extend(["--host".into(), s.host.clone()]);
    }
    let _ = Command::new(binary()).args(args).stdout(Stdio::null()).stderr(Stdio::null()).spawn();
}
