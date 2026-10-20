//! Hall Monitor, the window, in GPUI: an experiment next to the Swift app.
//! It reads the same `hallmonitor` feed; the notch and menu bar stay native.

mod feed;
mod theme;

use std::borrow::Cow;
use std::time::Duration;

use chrono::{DateTime, Utc};
use futures::StreamExt;
use gpui::{
    App, Application, Bounds, ClickEvent, Context, Div, FocusHandle, FontWeight, KeyBinding, Menu, MenuItem,
    SharedString, Stateful, TitlebarOptions, Window, WindowBounds, WindowOptions, actions, div, point, prelude::*,
    px, rgb, rgba, size,
};

use feed::{Msg, RateLimit, Session, Usage};
use theme::*;

actions!(hallmonitor, [SelectNext, SelectPrev, OpenSelected, CycleFilter, Quit]);

#[derive(Clone, Copy, PartialEq)]
enum Filter {
    All,
    Claude,
    Codex,
    Opencode,
}

impl Filter {
    const ALL: [Filter; 4] = [Filter::All, Filter::Claude, Filter::Codex, Filter::Opencode];
    fn label(self) -> &'static str {
        match self {
            Filter::All => "All",
            Filter::Claude => "Claude",
            Filter::Codex => "Codex",
            Filter::Opencode => "opencode",
        }
    }
    fn admits(self, provider: &str) -> bool {
        match self {
            Filter::All => true,
            Filter::Claude => provider == "claude",
            Filter::Codex => provider == "codex",
            Filter::Opencode => provider == "opencode",
        }
    }
}

struct Board {
    sessions: Vec<Session>,
    usage: Option<Usage>,
    problem: Option<String>,
    connected: bool,
    selected: Option<String>,
    filter: Filter,
    focus: FocusHandle,
}

impl Board {
    fn new(cx: &mut Context<Self>) -> Self {
        let (tx, mut rx) = futures::channel::mpsc::unbounded();
        feed::start(tx);
        cx.spawn(async move |this, cx| {
            while let Some(msg) = rx.next().await {
                if this.update(cx, |b, cx| b.apply(msg, cx)).is_err() {
                    break;
                }
            }
        })
        .detach();
        // Durations and activity strips move with the clock.
        cx.spawn(async move |this, cx| {
            loop {
                cx.background_executor().timer(Duration::from_secs(1)).await;
                if this.update(cx, |_, cx| cx.notify()).is_err() {
                    break;
                }
            }
        })
        .detach();
        Board {
            sessions: vec![],
            usage: None,
            problem: None,
            connected: false,
            selected: None,
            filter: Filter::All,
            focus: cx.focus_handle(),
        }
    }

    fn apply(&mut self, msg: Msg, cx: &mut Context<Self>) {
        match msg {
            Msg::Sessions(mut list) => {
                let rank = |s: &Session| if s.needs_you() { 0 } else if s.working() { 1 } else { 2 };
                list.sort_by(|a, b| {
                    rank(a).cmp(&rank(b)).then_with(|| b.updated_at.or(b.started_at).cmp(&a.updated_at.or(a.started_at)))
                });
                self.sessions = list;
                self.connected = true;
                self.problem = None;
                self.keep_selection();
            }
            Msg::Usage(u) => self.usage = Some(u),
            Msg::Problem(p) => self.problem = Some(p),
        }
        cx.notify();
    }

    fn keep_selection(&mut self) {
        let keys: Vec<String> = self.visible().iter().map(|s| s.key()).collect();
        if !self.selected.as_ref().is_some_and(|k| keys.contains(k)) {
            self.selected = keys.into_iter().next();
        }
    }

    fn visible(&self) -> Vec<&Session> {
        let now = Utc::now();
        self.sessions.iter().filter(|s| self.filter.admits(&s.provider) && !s.stale(now)).collect()
    }

    fn selected_session(&self) -> Option<&Session> {
        let key = self.selected.as_ref()?;
        self.sessions.iter().find(|s| &s.key() == key)
    }

    fn step(&mut self, by: isize, cx: &mut Context<Self>) {
        let keys: Vec<String> = self.visible().iter().map(|s| s.key()).collect();
        if keys.is_empty() {
            return;
        }
        let at = self.selected.as_ref().and_then(|k| keys.iter().position(|x| x == k));
        let next = match at {
            Some(i) => (i as isize + by).clamp(0, keys.len() as isize - 1) as usize,
            None => 0,
        };
        self.selected = Some(keys[next].clone());
        cx.notify();
    }

    fn select_next(&mut self, _: &SelectNext, _: &mut Window, cx: &mut Context<Self>) {
        self.step(1, cx);
    }
    fn select_prev(&mut self, _: &SelectPrev, _: &mut Window, cx: &mut Context<Self>) {
        self.step(-1, cx);
    }
    fn open_selected(&mut self, _: &OpenSelected, _: &mut Window, _: &mut Context<Self>) {
        if let Some(s) = self.selected_session() {
            feed::focus(s);
        }
    }
    fn cycle_filter(&mut self, _: &CycleFilter, _: &mut Window, cx: &mut Context<Self>) {
        let i = Filter::ALL.iter().position(|f| *f == self.filter).unwrap_or(0);
        self.set_filter(Filter::ALL[(i + 1) % Filter::ALL.len()], cx);
    }
    fn set_filter(&mut self, f: Filter, cx: &mut Context<Self>) {
        self.filter = f;
        self.keep_selection();
        cx.notify();
    }
}

// ---- view ----

impl Render for Board {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        div()
            .size_full()
            .flex()
            .flex_col()
            .bg(rgb(BASE))
            .text_color(rgb(FG))
            .font_family(SANS)
            .text_size(px(13.))
            .track_focus(&self.focus)
            .on_action(cx.listener(Self::select_next))
            .on_action(cx.listener(Self::select_prev))
            .on_action(cx.listener(Self::open_selected))
            .on_action(cx.listener(Self::cycle_filter))
            .child(self.titlebar(cx))
            .child(div().flex_1().min_h_0().flex().child(self.list(cx)).child(self.detail()))
            .child(self.footer())
    }
}

impl Board {
    fn titlebar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let needs = self.sessions.iter().filter(|s| s.needs_you()).count();
        let working = self.sessions.iter().filter(|s| s.working()).count();
        let mut summary = vec![];
        if needs > 0 {
            summary.push(format!("{needs} need{} you", if needs == 1 { "s" } else { "" }));
        }
        if working > 0 || summary.is_empty() {
            summary.push(format!("{working} working"));
        }
        let chips: Vec<_> = Filter::ALL
            .iter()
            .map(|&f| {
                let on = f == self.filter;
                div()
                    .id(SharedString::from(f.label()))
                    .px_2p5()
                    .py_0p5()
                    .rounded_md()
                    .text_size(px(12.))
                    .cursor_pointer()
                    .text_color(rgb(if on { FG } else { MUTED }))
                    .when(on, |d| d.bg(rgb(RAISED)))
                    .hover(|d| d.text_color(rgb(FG)))
                    .on_click(cx.listener(move |b, _: &ClickEvent, _, cx| b.set_filter(f, cx)))
                    .child(f.label())
            })
            .collect();
        div()
            .h(px(46.))
            .flex_none()
            .flex()
            .items_center()
            .gap_4()
            .pl(px(84.)) // past the traffic lights
            .pr_4()
            .border_b_1()
            .border_color(rgb(LINE))
            .child(
                div()
                    .flex()
                    .items_baseline()
                    .gap_2()
                    .child(div().font_weight(FontWeight::SEMIBOLD).child("Hall Monitor"))
                    .child(
                        div()
                            .text_size(px(12.))
                            .text_color(rgb(if needs > 0 { AMBER } else { MUTED }))
                            .child(summary.join(" · ")),
                    ),
            )
            .child(div().flex_1())
            .child(div().flex().gap_0p5().p_0p5().rounded_lg().bg(rgb(PANEL)).children(chips))
            .children(self.limits().into_iter().map(limit_meter))
    }

    /// Claude's limits first, then the rest, at most three.
    fn limits(&self) -> Vec<RateLimit> {
        let Some(u) = &self.usage else { return vec![] };
        let mut l: Vec<RateLimit> = u.rate_limits.values().cloned().collect();
        l.sort_by_key(|r| (r.provider != "claude", r.window_minutes));
        l.truncate(3);
        l
    }

    fn list(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let now = Utc::now();
        let visible = self.visible();
        let mut body = div().id("list").flex_1().min_w_0().overflow_y_scroll().px_3().pb_4();
        if visible.is_empty() {
            let msg = if let Some(p) = &self.problem {
                p.clone()
            } else if !self.connected {
                "Connecting to hallmonitor…".into()
            } else {
                "No agents. Start Claude Code, Codex or opencode and they show up here.".into()
            };
            return body.child(div().pt(px(120.)).flex().justify_center().text_color(rgb(MUTED)).child(msg));
        }
        let groups: [(&str, Vec<&Session>); 3] = [
            ("Needs you", visible.iter().copied().filter(|s| s.needs_you()).collect()),
            ("Working", visible.iter().copied().filter(|s| s.working()).collect()),
            ("Idle", visible.iter().copied().filter(|s| !s.needs_you() && !s.working()).collect()),
        ];
        for (title, group) in groups {
            if group.is_empty() {
                continue;
            }
            body = body.child(
                div()
                    .flex()
                    .gap_2()
                    .px_2()
                    .pt_4()
                    .pb_1p5()
                    .text_size(px(11.))
                    .font_weight(FontWeight::SEMIBOLD)
                    .text_color(rgb(MUTED))
                    .child(title.to_uppercase())
                    .child(div().text_color(rgb(FAINT)).child(group.len().to_string())),
            );
            for s in group {
                body = body.child(self.row(s, now, cx));
            }
        }
        body
    }

    fn row(&self, s: &Session, now: DateTime<Utc>, cx: &mut Context<Self>) -> Stateful<Div> {
        let key = s.key();
        let selected = self.selected.as_deref() == Some(key.as_str());
        let mut sub = vec![s.project().to_string()];
        if !s.host.is_empty() {
            sub.push(s.host.clone());
        }
        if !s.model.is_empty() {
            sub.push(model_name(&s.model));
        }
        if s.context_tokens > 0 {
            sub.push(format!("{}k", s.context_tokens / 1000));
        }
        let doing = Some(s.last.replace("↳ ", "")).filter(|d| (s.working() || s.needs_you()) && !d.is_empty());
        let subagents = s.running_subagents();
        let row_key = key.clone();
        let open = s.clone();
        div()
            .id(SharedString::from(key))
            .flex()
            .items_center()
            .gap_3()
            .px_2p5()
            .py_2()
            .rounded_lg()
            .cursor_pointer()
            .border_1()
            .border_color(rgba(0))
            .when(selected, |d| d.bg(rgb(RAISED)).border_color(rgb(LINE)))
            .when(!selected, |d| d.hover(|d| d.bg(rgb(PANEL))))
            .on_click(cx.listener(move |b, ev: &ClickEvent, _, cx| {
                b.selected = Some(row_key.clone());
                if ev.click_count() >= 2 {
                    feed::focus(&open);
                }
                cx.notify();
            }))
            .child(badge(&s.provider, 30.))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .flex()
                    .flex_col()
                    .gap(px(2.))
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap_2()
                            .child(div().min_w_0().truncate().font_weight(FontWeight::MEDIUM).child(s.name().to_string()))
                            .when(subagents > 0, |d| {
                                d.child(div().flex_none().text_size(px(11.)).text_color(rgb(CYAN)).child(format!("⑂ {subagents}")))
                            }),
                    )
                    .child(div().truncate().text_size(px(12.)).text_color(rgb(MUTED)).child(sub.join(" · ")))
                    .when_some(doing, |d, doing| {
                        d.child(
                            div()
                                .truncate()
                                .font_family(MONO)
                                .text_size(px(11.5))
                                .text_color(rgb(if s.needs_you() { AMBER } else { SOFT }))
                                .child(doing),
                        )
                    }),
            )
            .child(activity_strip(s, now, tone(s)))
            .child(status_chip(s, now))
    }

    fn detail(&self) -> impl IntoElement {
        let panel = div()
            .id("detail")
            .w(px(340.))
            .flex_none()
            .border_l_1()
            .border_color(rgb(LINE))
            .bg(rgb(SIDE))
            .overflow_y_scroll()
            .p_5()
            .flex()
            .flex_col()
            .gap_5();
        let Some(s) = self.selected_session() else {
            return panel.child(div().text_color(rgb(MUTED)).child("Select an agent"));
        };
        let now = Utc::now();
        let open = s.clone();
        let target = open_target(s);
        let mut p = panel
            .child(
                div()
                    .flex()
                    .gap_3()
                    .items_center()
                    .child(badge(&s.provider, 40.))
                    .child(
                        div()
                            .min_w_0()
                            .flex()
                            .flex_col()
                            .gap_0p5()
                            .child(div().text_size(px(16.)).font_weight(FontWeight::SEMIBOLD).line_clamp(2).child(s.name().to_string()))
                            .child(div().text_size(px(12.)).text_color(rgb(tone(s))).child(status_text(s, now))),
                    ),
            )
            .when(s.host.is_empty(), |d| {
                d.child(
                    div()
                        .id("open")
                        .flex()
                        .justify_center()
                        .py_2()
                        .rounded_lg()
                        .bg(rgb(GREEN))
                        .text_color(rgb(BASE))
                        .font_weight(FontWeight::SEMIBOLD)
                        .cursor_pointer()
                        .hover(|d| d.opacity(0.9))
                        .on_click(move |_, _, _| feed::focus(&open))
                        .child(format!("Open in {target}  ↵")),
                )
            });
        if (s.working() || s.needs_you()) && !s.last.is_empty() {
            p = p.child(section("Now", div().font_family(MONO).text_size(px(12.)).child(s.last.replace("↳ ", ""))));
        }
        if !s.prompt.is_empty() {
            p = p.child(section("Asked", div().text_color(rgb(SOFT)).child(format!("“{}”", s.prompt))));
        }
        if let Some(sa) = s.subagents.as_ref().filter(|sa| sa.total > 0) {
            let running = s.running_subagents();
            let mut d = div().flex().flex_col().gap_1().child(div().text_color(rgb(if running > 0 { CYAN } else { SOFT })).child(
                if running > 0 { format!("{running} of {} running", sa.total) } else { format!("{} started, none running", sa.total) },
            ));
            if running > 0 {
                for a in &sa.active {
                    d = d.child(div().truncate().text_size(px(12.)).text_color(rgb(MUTED)).child(format!("↳ {a}")));
                }
            }
            p = p.child(section("Subagents", d));
        }
        let mut facts: Vec<(&str, String)> = vec![("Folder", tilde(&s.cwd))];
        if !s.model.is_empty() {
            facts.push(("Model", model_name(&s.model)));
        }
        if s.context_tokens > 0 {
            facts.push(("Context", format!("{:.1}k tokens", s.context_tokens as f64 / 1000.)));
        }
        facts.push(("Machine", if s.host.is_empty() { "This Mac".into() } else { s.host.clone() }));
        if let Some(t) = s.started_at {
            facts.push(("Started", format!("{} ago", ago(now - t))));
        }
        if s.pid > 0 {
            facts.push(("PID", s.pid.to_string()));
        }
        if !s.id.is_empty() {
            facts.push(("Session", s.id.chars().take(18).collect()));
        }
        let grid = div().flex().flex_col().gap_1p5().children(facts.into_iter().map(|(k, v)| {
            div()
                .flex()
                .gap_3()
                .text_size(px(12.5))
                .child(div().w(px(64.)).flex_none().text_color(rgb(MUTED)).child(k))
                .child(div().min_w_0().truncate().child(v))
        }));
        p.child(section("Details", grid))
    }

    fn footer(&self) -> impl IntoElement {
        let today = self.usage.as_ref().map(|u| {
            let t = &u.total;
            format!(
                "Today  {} agent time · {} tokens · {} prompts · {} tool calls",
                hours(t.active_s),
                human(t.tokens.total()),
                t.prompts,
                t.tools
            )
        });
        div()
            .h(px(30.))
            .flex_none()
            .flex()
            .items_center()
            .px_4()
            .gap_4()
            .border_t_1()
            .border_color(rgb(LINE))
            .text_size(px(11.5))
            .text_color(rgb(MUTED))
            .child(div().flex_1().truncate().child(today.unwrap_or_default()))
            .child(div().text_color(rgb(FAINT)).child("↑↓ select   ↵ open   f filter   ⌘Q quit"))
    }
}

// ---- pieces ----

fn section(label: &str, body: impl IntoElement) -> impl IntoElement {
    div()
        .flex()
        .flex_col()
        .gap_1p5()
        .child(div().text_size(px(10.5)).font_weight(FontWeight::SEMIBOLD).text_color(rgb(FAINT)).child(label.to_uppercase()))
        .child(body)
}

/// The provider's mark on a tinted tile.
fn badge(provider: &str, size: f32) -> impl IntoElement {
    // Each mark at the size that fills its tile alike.
    let (glyph, scale, fg, bg) = match provider {
        "claude" => ("✻", 0.66, CLAUDE, 0x2a1d17),
        "codex" => (">_", 0.4, 0xe8e8ea, 0x232428),
        "opencode" => ("◰", 0.6, OPENCODE, 0x1e1f2c),
        _ => ("•", 0.5, MUTED, RAISED),
    };
    div()
        .size(px(size))
        .flex_none()
        .flex()
        .items_center()
        .justify_center()
        .rounded(px(size * 0.28))
        .bg(rgb(bg))
        .text_color(rgb(fg))
        .font_family(if provider == "codex" { MONO } else { SANS })
        .font_weight(FontWeight::MEDIUM)
        .text_size(px(size * scale))
        .child(glyph)
}

/// The last ten minutes of tool calls and replies, in 20-second columns.
fn activity_strip(s: &Session, now: DateTime<Utc>, tone: u32) -> impl IntoElement {
    const COLS: usize = 30;
    const SPAN: i64 = 20;
    let mut counts = [0u32; COLS];
    let end = now.timestamp();
    for &t in &s.activity {
        let age = end - t;
        if (0..SPAN * COLS as i64).contains(&age) {
            counts[COLS - 1 - (age / SPAN) as usize] += 1;
        }
    }
    let peak = counts.iter().copied().max().unwrap_or(0).max(3) as f32;
    div().flex_none().flex().items_end().gap(px(1.)).h(px(18.)).children(counts.into_iter().map(move |n| {
        let h = if n == 0 { 2. } else { 4. + 14. * (n as f32 / peak).min(1.) };
        div().w(px(2.)).h(px(h)).rounded(px(1.)).bg(rgb(if n == 0 { LINE } else { tone }))
    }))
}

fn status_chip(s: &Session, now: DateTime<Utc>) -> impl IntoElement {
    let since = s.status_since.map(|t| ago(now - t)).unwrap_or_default();
    let (text, fg, bg) = if s.needs_you() {
        ((if s.status == "error" { "error" } else { "needs you" }).to_string(), AMBER, 0x2b2112)
    } else if s.working() {
        (since, GREEN, 0x13261a)
    } else {
        (since, MUTED, PANEL)
    };
    div()
        .flex_none()
        .min_w(px(52.))
        .flex()
        .justify_center()
        .px_2()
        .py_0p5()
        .rounded_md()
        .bg(rgb(bg))
        .text_color(rgb(fg))
        .text_size(px(11.5))
        .font_weight(FontWeight::MEDIUM)
        .child(text)
}

fn limit_meter(l: RateLimit) -> impl IntoElement {
    let pct = l.used_percent.clamp(0., 100.);
    let color = if pct >= 85. { RED } else if pct >= 60. { AMBER } else { GREEN };
    let name = match (l.provider.as_str(), l.window.as_str()) {
        (p, "") => capitalize(p),
        (p, w) => format!("{} {w}", capitalize(p)),
    };
    div()
        .flex()
        .items_center()
        .gap_1p5()
        .text_size(px(11.5))
        .child(div().text_color(rgb(MUTED)).child(name))
        .child(
            div()
                .w(px(44.))
                .h(px(4.))
                .rounded(px(2.))
                .bg(rgb(LINE))
                .child(div().h_full().w(px(44. * pct as f32 / 100.)).rounded(px(2.)).bg(rgb(color))),
        )
        .child(div().w(px(30.)).child(format!("{pct:.0}%")))
}

fn tone(s: &Session) -> u32 {
    if s.needs_you() {
        AMBER
    } else if s.working() {
        GREEN
    } else {
        FAINT
    }
}

fn status_text(s: &Session, now: DateTime<Utc>) -> String {
    let word = match s.status.as_str() {
        "waiting" => "Needs you",
        "error" => "Error",
        "busy" => "Working",
        _ => "Idle",
    };
    match s.status_since {
        Some(t) => format!("{word} · {}", ago(now - t)),
        None => word.into(),
    }
}

fn open_target(s: &Session) -> &'static str {
    match s.extra.get("entrypoint").map(String::as_str) {
        Some("claude-desktop") => "Claude",
        _ => "its terminal",
    }
}

fn ago(d: chrono::TimeDelta) -> String {
    let s = d.num_seconds().max(0);
    match s {
        0..60 => format!("{s}s"),
        60..3600 => format!("{}m", s / 60),
        3600..86400 => format!("{}h {}m", s / 3600, s % 3600 / 60),
        _ => format!("{}d", s / 86400),
    }
}

fn hours(sec: f64) -> String {
    if sec < 3600. { format!("{:.0}m", sec / 60.) } else { format!("{:.1}h", sec / 3600.) }
}

fn human(n: i64) -> String {
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

/// "claude-opus-5-5" -> "Opus 5.5"; anything else as given.
fn model_name(m: &str) -> String {
    let Some(rest) = m.strip_prefix("claude-") else { return m.to_string() };
    let mut parts = rest.split('-');
    let family = capitalize(parts.next().unwrap_or(""));
    let version: Vec<&str> = parts.take_while(|p| p.len() <= 2 && p.chars().all(|c| c.is_ascii_digit())).collect();
    if version.is_empty() { family } else { format!("{family} {}", version.join(".")) }
}

fn capitalize(s: &str) -> String {
    let mut c = s.chars();
    c.next().map(|f| f.to_uppercase().collect::<String>() + c.as_str()).unwrap_or_default()
}

fn tilde(p: &str) -> String {
    match std::env::var("HOME") {
        Ok(h) if !h.is_empty() && p.starts_with(&h) => format!("~{}", &p[h.len()..]),
        _ => p.to_string(),
    }
}

fn main() {
    Application::new().run(|cx: &mut App| {
        cx.text_system()
            .add_fonts(vec![
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexSans-Regular.otf").as_slice()),
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexSans-Medium.otf").as_slice()),
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexSans-SemiBold.otf").as_slice()),
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexMono-Regular.otf").as_slice()),
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexMono-Medium.otf").as_slice()),
            ])
            .expect("bundled fonts");
        cx.bind_keys([
            KeyBinding::new("down", SelectNext, None),
            KeyBinding::new("j", SelectNext, None),
            KeyBinding::new("up", SelectPrev, None),
            KeyBinding::new("k", SelectPrev, None),
            KeyBinding::new("enter", OpenSelected, None),
            KeyBinding::new("f", CycleFilter, None),
            KeyBinding::new("cmd-q", Quit, None),
            KeyBinding::new("cmd-w", Quit, None),
        ]);
        cx.on_action(|_: &Quit, cx| cx.quit());
        cx.set_menus(vec![Menu { name: "Hall Monitor".into(), items: vec![MenuItem::action("Quit Hall Monitor", Quit)] }]);
        cx.on_window_closed(|cx| {
            if cx.windows().is_empty() {
                cx.quit();
            }
        })
        .detach();

        let bounds = Bounds::centered(None, size(px(1180.), px(760.)), cx);
        cx.open_window(
            WindowOptions {
                window_bounds: Some(WindowBounds::Windowed(bounds)),
                window_min_size: Some(size(px(760.), px(460.))),
                titlebar: Some(TitlebarOptions {
                    title: Some("Hall Monitor".into()),
                    appears_transparent: true,
                    traffic_light_position: Some(point(px(16.), px(16.))),
                }),
                ..Default::default()
            },
            |window, cx| {
                let board = cx.new(Board::new);
                let focus = board.read(cx).focus.clone();
                window.focus(&focus);
                board
            },
        )
        .expect("open window");
        cx.activate(true);
    });
}
