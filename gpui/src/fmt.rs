//! How numbers, times and names read on screen.

use chrono::{DateTime, Datelike, Local, NaiveDate, TimeDelta, Utc};

use crate::feed::{RateLimit, Session, Tokens};
use crate::theme::provider_name;

/// "16s", "4m", "1h 7m", "2d".
pub fn ago(d: TimeDelta) -> String {
    let s = d.num_seconds().max(0);
    match s {
        0..60 => format!("{s}s"),
        60..3600 => format!("{}m", s / 60),
        3600..86400 => format!("{}h {}m", s / 3600, s % 3600 / 60),
        _ => format!("{}d", s / 86400),
    }
}

/// Active seconds as "42m", "6.4h".
pub fn hours(sec: f64) -> String {
    if sec < 3600. { format!("{:.0}m", sec / 60.) } else { format!("{:.1}h", sec / 3600.) }
}

/// Active seconds in words, for sentences: "42 minutes", "6.4 hours".
pub fn hours_words(sec: f64) -> String {
    let h = sec / 3600.;
    if sec < 3600. {
        let m = (sec / 60.).round() as i64;
        format!("{m} minute{}", if m == 1 { "" } else { "s" })
    } else if (h - 1.).abs() < 0.05 {
        "1 hour".into()
    } else {
        format!("{h:.1} hours")
    }
}

pub fn human(n: i64) -> String {
    let n = n as f64;
    if n >= 1e9 {
        format!("{:.1}B", n / 1e9)
    } else if n >= 1e6 {
        format!("{:.1}M", n / 1e6)
    } else if n >= 1e3 {
        format!("{:.1}k", n / 1e3)
    } else {
        format!("{n}")
    }
}

/// Thousands with separators: 3,148.
pub fn count(n: i64) -> String {
    let s = n.abs().to_string();
    let mut out = String::new();
    for (i, c) in s.chars().enumerate() {
        if i > 0 && (s.len() - i) % 3 == 0 {
            out.push(',');
        }
        out.push(c);
    }
    if n < 0 { format!("-{out}") } else { out }
}

pub fn cache_share(t: &Tokens) -> Option<f64> {
    let inputs = t.input + t.cache_read + t.cache_write;
    (inputs > 0).then(|| t.cache_read as f64 / inputs as f64 * 100.)
}

/// "claude-opus-5-5" -> "Opus 5.5"; anything else as given.
pub fn model_name(m: &str) -> String {
    let Some(rest) = m.strip_prefix("claude-") else { return m.to_string() };
    let mut parts = rest.split('-');
    let family = capitalize(parts.next().unwrap_or(""));
    let version: Vec<&str> = parts.take_while(|p| p.len() <= 2 && p.chars().all(|c| c.is_ascii_digit())).collect();
    if version.is_empty() { family } else { format!("{family} {}", version.join(".")) }
}

pub fn capitalize(s: &str) -> String {
    let mut c = s.chars();
    c.next().map(|f| f.to_uppercase().collect::<String>() + c.as_str()).unwrap_or_default()
}

pub fn tilde(p: &str) -> String {
    match std::env::var("HOME") {
        Ok(h) if !h.is_empty() && p.starts_with(&h) => format!("~{}", &p[h.len()..]),
        _ => p.to_string(),
    }
}

/// "Claude, 5 hours", "Claude, weekly", "Codex, monthly".
pub fn limit_name(l: &RateLimit) -> String {
    let window = match (l.window.as_str(), l.window_minutes) {
        ("5h", _) | (_, 300) => "5 hours".to_string(),
        ("7d", _) | (_, 10080) => "weekly".to_string(),
        (_, 43200) => "monthly".to_string(),
        ("", 0) => String::new(),
        (w, _) if !w.is_empty() => w.to_string(),
        (_, m) => format!("{} days", m / 1440),
    };
    let p = provider_name(&l.provider).to_string();
    if window.is_empty() { p } else { format!("{p}, {window}") }
}

/// A moment ahead, the way you'd say it: "4:30 PM", "Thu 1:30 AM", "Oct 29".
pub fn when(t: DateTime<Utc>) -> String {
    let local = t.with_timezone(&Local);
    let now = Local::now();
    let days = (local.date_naive() - now.date_naive()).num_days();
    let clock = local.format("%-I:%M %p").to_string();
    match days {
        0 => clock,
        1..=6 => format!("{} {clock}", local.format("%a")),
        _ => local.format("%b %-d").to_string(),
    }
}

/// A chart column's label for a date: weekday for a week, day of month
/// for longer ranges.
pub fn day_label(date: &str, range: u32) -> String {
    let Ok(d) = NaiveDate::parse_from_str(date, "%Y-%m-%d") else { return date.to_string() };
    if d == Local::now().date_naive() {
        return "Today".into();
    }
    if range <= 7 { d.format("%a").to_string() } else if d.day() == 1 { d.format("%b %-d").to_string() } else { d.format("%-d").to_string() }
}

/// "Tuesday, Oct 20" for a chart reading.
pub fn day_long(date: &str) -> String {
    NaiveDate::parse_from_str(date, "%Y-%m-%d").map(|d| d.format("%A, %b %-d").to_string()).unwrap_or_else(|_| date.into())
}

pub fn status_word(s: &Session) -> &'static str {
    match s.status.as_str() {
        "waiting" => "Needs you",
        "error" => "Error",
        "busy" => "Working",
        _ => "Idle",
    }
}

pub fn open_target(s: &Session) -> &'static str {
    match s.extra.get("entrypoint").map(String::as_str) {
        Some("claude-desktop") => "Claude",
        _ => "terminal",
    }
}

pub fn plural(n: usize, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}
