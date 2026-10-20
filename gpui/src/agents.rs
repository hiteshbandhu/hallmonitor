//! The agents view: who's working, who needs you, and one agent in detail.

use chrono::{DateTime, Utc};
use gpui::{AnyElement, ClickEvent, Context, Div, FontWeight, SharedString, Stateful, div, prelude::*, px, rgb, rgba};

use crate::feed::{self, Session};
use crate::fmt;
use crate::theme::*;
use crate::ui;
use crate::{Board, Filter};

impl Board {
    pub fn agents_view(&self, cx: &mut Context<Self>) -> impl IntoElement {
        div()
            .size_full()
            .flex()
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .flex()
                    .flex_col()
                    .child(div().h(px(layout::CHROME)).flex_none())
                    .child(
                        div()
                            .px(px(space::S6))
                            .pb(px(space::S5))
                            .flex()
                            .flex_col()
                            .gap(px(space::S5))
                            .child(self.agents_headline())
                            .child(self.filters(cx)),
                    )
                    .child(self.list(cx)),
            )
            .child(self.inspector())
    }

    /// The sentence that answers "does anything need me?".
    fn agents_headline(&self) -> Div {
        let now = Utc::now();
        let needs: Vec<&Session> = self.sessions.iter().filter(|s| s.needs_you()).collect();
        let working = self.sessions.iter().filter(|s| s.working()).count();
        let idle = self.sessions.iter().filter(|s| !s.needs_you() && !s.working() && !s.stale(now)).count();
        if !self.connected {
            let sub = self.problem.clone().unwrap_or_else(|| "Starting the hallmonitor feed.".into());
            return ui::headline("Connecting…", sub);
        }
        match (needs.as_slice(), working) {
            ([one], w) => ui::headline(
                format!("{} is waiting on you.", one.name()),
                if w > 0 { format!("{} working besides.", fmt::plural(w, "agent is", "agents are")) } else { "Nothing else is running.".into() },
            ),
            ([], 0) => ui::headline(
                "All quiet.",
                if idle > 0 {
                    format!("{} idle. Start one and it shows up here.", fmt::plural(idle, "agent", "agents"))
                } else {
                    "Start Claude Code, Codex or opencode and it shows up here.".into()
                },
            ),
            ([], w) => ui::headline(format!("{} working.", fmt::plural(w, "agent", "agents")), "Nothing needs you right now."),
            (many, w) => ui::headline(
                format!("{} agents need you.", many.len()),
                if w > 0 { format!("{w} more working.") } else { "Nothing else is running.".into() },
            ),
        }
    }

    fn filters(&self, cx: &mut Context<Self>) -> Div {
        let counts = |f: Filter| self.sessions.iter().filter(|s| f.admits(&s.provider) && !s.stale(Utc::now())).count();
        div().flex().items_center().justify_between().child(ui::segmented().children(Filter::ALL.iter().filter(|f| **f == Filter::All || counts(**f) > 0).map(|&f| {
            ui::segment(SharedString::from(format!("filter-{}", f.label())), f.label(), f == self.filter)
                .on_click(cx.listener(move |b, _: &ClickEvent, _, cx| b.set_filter(f, cx)))
        })))
        .child(ui::label("f to switch", size::SMALL, TEXT_3))
    }

    fn list(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let now = Utc::now();
        let visible = self.visible();
        let mut body = div().id("agents").flex_1().min_h_0().overflow_y_scroll().px(px(space::S5)).pb(px(space::S5));
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
                    .items_baseline()
                    .gap(px(space::S2))
                    .px(px(space::S3))
                    .pt(px(space::S4))
                    .pb(px(space::S2))
                    .child(ui::label(title, size::SMALL, TEXT_2).font_weight(FontWeight::SEMIBOLD))
                    .child(ui::label(group.len().to_string(), size::SMALL, TEXT_3)),
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
        let state = state_color(s);
        let doing = Some(s.last.replace("↳ ", "")).filter(|d| (s.working() || s.needs_you()) && !d.is_empty());
        let subagents = s.running_subagents();
        let elapsed = s.status_since.map(|t| fmt::ago(now - t)).unwrap_or_default();
        let row_key = key.clone();
        let open = s.clone();
        div()
            .id(SharedString::from(key))
            .flex()
            .items_center()
            .gap(px(space::S3))
            .px(px(space::S3))
            .py(px(10.))
            .mb(px(2.))
            .rounded(px(radius::ROW))
            .cursor_pointer()
            .border_1()
            .when(selected, |d| d.bg(rgb(RAISE)).border_color(rgba(alpha(ACCENT, 0x55))))
            .when(!selected, |d| d.border_color(rgba(0)).hover(|d| d.bg(rgb(DEEP))))
            .on_click(cx.listener(move |b, ev: &ClickEvent, _, cx| {
                b.selected = Some(row_key.clone());
                if ev.click_count() >= 2 {
                    feed::focus(&open);
                }
                cx.notify();
            }))
            .child(ui::mark(&s.provider, 34.))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .flex()
                    .flex_col()
                    .gap(px(3.))
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap(px(space::S2))
                            .child(
                                div()
                                    .min_w_0()
                                    .truncate()
                                    .text_size(px(size::LEAD - 1.))
                                    .font_weight(FontWeight::MEDIUM)
                                    .child(s.name().to_string()),
                            )
                            .when(subagents > 0, |d| {
                                d.child(
                                    div()
                                        .flex_none()
                                        .px(px(6.))
                                        .rounded(px(radius::CHIP))
                                        .bg(rgba(alpha(SUB, 0x1f)))
                                        .text_size(px(size::CAPTION))
                                        .text_color(rgb(SUB))
                                        .child(format!("+{} {}", subagents, if subagents == 1 { "subagent" } else { "subagents" })),
                                )
                            }),
                    )
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap(px(6.))
                            .min_w_0()
                            .child(ui::label(s.project().to_string(), size::SMALL, TEXT_2).flex_none())
                            .when(!s.model.is_empty(), |d| d.child(ui::tag(fmt::model_name(&s.model))))
                            .when(!s.host.is_empty(), |d| d.child(ui::tag(s.host.clone())))
                            .when_some(doing, |d, doing| {
                                d.child(
                                    div()
                                        .min_w_0()
                                        .truncate()
                                        .pl(px(space::S1))
                                        .text_size(px(size::SMALL))
                                        .text_color(rgb(if s.needs_you() { NEEDS } else { TEXT_3 }))
                                        .child(doing),
                                )
                            }),
                    ),
            )
            .child(ui::pulse(&s.activity, now, if s.needs_you() || s.working() { state } else { TEXT_3 }))
            .child(
                div()
                    .w(px(72.))
                    .flex_none()
                    .flex()
                    .items_center()
                    .justify_end()
                    .gap(px(space::S2))
                    .child(
                        ui::label(if s.needs_you() { fmt::status_word(s).to_string() } else { elapsed }, size::SMALL, if s.needs_you() || s.working() { TEXT } else { TEXT_3 })
                            .font_weight(FontWeight::MEDIUM),
                    )
                    .child(ui::lamp(state, s.needs_you() || s.working())),
            )
    }

    fn inspector(&self) -> impl IntoElement {
        let panel = div()
            .id("inspector")
            .w(px(layout::INSPECTOR))
            .flex_none()
            .h_full()
            .bg(rgb(DEEP))
            .border_l_1()
            .border_color(rgb(RULE))
            .overflow_y_scroll()
            .px(px(space::S5))
            .pt(px(layout::CHROME))
            .pb(px(space::S5))
            .flex()
            .flex_col()
            .gap(px(space::S5));
        let Some(s) = self.selected_session() else {
            return panel.child(ui::label("Pick an agent to see what it's doing.", size::BODY, TEXT_3));
        };
        let now = Utc::now();
        let state = state_color(s);
        let since = s.status_since.map(|t| fmt::ago(now - t));
        let state_line = match (s.status.as_str(), since) {
            ("busy", Some(d)) => format!("Working for {d}"),
            ("idle", Some(d)) => format!("Idle for {d}"),
            (_, Some(d)) => format!("{} for {d}", fmt::status_word(s)),
            _ => fmt::status_word(s).to_string(),
        };
        let open = s.clone();
        let mut p = panel
            .child(
                div()
                    .flex()
                    .flex_col()
                    .gap(px(space::S3))
                    .child(ui::mark(&s.provider, 44.))
                    .child(
                        div()
                            .text_size(px(size::TITLE))
                            .line_height(px(size::TITLE * 1.25))
                            .font_weight(FontWeight::SEMIBOLD)
                            .line_clamp(3)
                            .child(s.name().to_string()),
                    )
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap(px(space::S2))
                            .child(ui::lamp(state, s.needs_you() || s.working()))
                            .child(ui::label(state_line, size::BODY, if s.needs_you() || s.working() { state } else { TEXT_2 })),
                    ),
            )
            .when(s.host.is_empty(), |d| {
                d.child(
                    div()
                        .id("open")
                        .flex()
                        .items_center()
                        .justify_center()
                        .gap(px(space::S2))
                        .py(px(9.))
                        .rounded(px(radius::ROW))
                        .bg(rgb(ACCENT))
                        .text_color(rgb(INK))
                        .font_weight(FontWeight::SEMIBOLD)
                        .cursor_pointer()
                        .hover(|d| d.opacity(0.92))
                        .on_click(move |_, _, _| feed::focus(&open))
                        .child(format!("Open in {}", fmt::open_target(s)))
                        .child(div().text_size(px(size::SMALL)).opacity(0.6).child("↵")),
                )
            });
        if (s.working() || s.needs_you()) && !s.last.is_empty() {
            p = p.child(ui::field("Now", div().font_family(MONO).text_size(px(size::SMALL)).text_color(rgb(TEXT)).child(s.last.replace("↳ ", ""))));
        }
        if !s.prompt.is_empty() {
            p = p.child(ui::field("You asked", div().text_color(rgb(TEXT_2)).child(format!("“{}”", s.prompt))));
        }
        if let Some(sa) = s.subagents.as_ref().filter(|sa| sa.total > 0) {
            let running = s.running_subagents();
            let mut d = div().flex().flex_col().gap(px(space::S1)).child(
                div().text_color(rgb(if running > 0 { SUB } else { TEXT_2 })).child(if running > 0 {
                    format!("{running} of {} running", sa.total)
                } else {
                    format!("{} started, none running", sa.total)
                }),
            );
            if running > 0 {
                for a in &sa.active {
                    d = d.child(div().truncate().text_size(px(size::SMALL)).text_color(rgb(TEXT_3)).child(a.clone()));
                }
            }
            p = p.child(ui::field("Subagents", d));
        }
        p.child(div().h(px(1.)).bg(rgb(RULE))).child(facts(s, now))
    }
}

/// The agent's particulars, two to a row.
fn facts(s: &Session, now: DateTime<Utc>) -> AnyElement {
    let mut items: Vec<(&str, String)> = vec![("Folder", fmt::tilde(&s.cwd))];
    if !s.model.is_empty() {
        items.push(("Model", fmt::model_name(&s.model)));
    }
    if s.context_tokens > 0 {
        items.push(("Context", format!("{} tokens", fmt::human(s.context_tokens))));
    }
    items.push(("Machine", if s.host.is_empty() { "This Mac".into() } else { s.host.clone() }));
    if let Some(t) = s.started_at {
        items.push(("Started", format!("{} ago", fmt::ago(now - t))));
    }
    if s.pid > 0 {
        items.push(("Process", s.pid.to_string()));
    }
    if !s.id.is_empty() {
        items.push(("Session", s.id.chars().take(8).collect()));
    }
    // The folder gets its own line; the rest pair up.
    let mut col = div().flex().flex_col().gap(px(space::S4));
    let mut it = items.into_iter();
    if let Some((k, v)) = it.next() {
        col = col.child(ui::field(k, div().truncate().child(v)));
    }
    let rest: Vec<_> = it.collect();
    for pair in rest.chunks(2) {
        col = col.child(div().flex().gap(px(space::S4)).children(pair.iter().map(|(k, v)| {
            div().flex_1().min_w_0().child(ui::field(k, div().truncate().child(v.clone())))
        })));
    }
    col.into_any_element()
}

pub fn state_color(s: &Session) -> u32 {
    match s.status.as_str() {
        "waiting" => NEEDS,
        "error" => ERROR,
        "busy" => WORKING,
        _ => TEXT_3,
    }
}
