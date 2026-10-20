//! Hall Monitor, the window, in GPUI: an experiment next to the Swift app.
//! It reads the same `hallmonitor` feed; the notch and menu bar stay native.
//!
//! main.rs is the shell (state, sidebar, keys); agents.rs and usage.rs are
//! the two views; theme.rs and ui.rs are the design system.

mod agents;
mod feed;
mod fmt;
mod theme;
mod ui;
mod usage;

use std::borrow::Cow;
use std::collections::HashMap;
use std::time::Duration;

use chrono::Utc;
use futures::StreamExt;
use gpui::{
    App, Application, Bounds, ClickEvent, Context, FocusHandle, FontWeight, KeyBinding, Menu, MenuItem,
    TitlebarOptions, Window, WindowBounds, WindowOptions, actions, div, point, prelude::*, px, rgb, size,
};

use feed::{Msg, RateLimit, Session, Usage};
use theme::*;

actions!(
    hallmonitor,
    [SelectNext, SelectPrev, OpenSelected, CycleFilter, ShowAgents, ShowUsage, CycleRange, Quit]
);

#[derive(Clone, Copy, PartialEq)]
pub enum Pane {
    Agents,
    Usage,
}

#[derive(Clone, Copy, PartialEq)]
pub enum Filter {
    All,
    Claude,
    Codex,
    Opencode,
}

impl Filter {
    pub const ALL: [Filter; 4] = [Filter::All, Filter::Claude, Filter::Codex, Filter::Opencode];
    pub fn label(self) -> &'static str {
        match self {
            Filter::All => "All",
            Filter::Claude => "Claude",
            Filter::Codex => "Codex",
            Filter::Opencode => "opencode",
        }
    }
    pub fn admits(self, provider: &str) -> bool {
        match self {
            Filter::All => true,
            Filter::Claude => provider == "claude",
            Filter::Codex => provider == "codex",
            Filter::Opencode => provider == "opencode",
        }
    }
}

pub struct Board {
    pub sessions: Vec<Session>,
    pub usage: HashMap<u32, Usage>,
    pub problem: Option<String>,
    pub connected: bool,
    pub selected: Option<String>,
    pub filter: Filter,
    pub pane: Pane,
    /// Days shown in the usage view: one of feed::RANGES.
    pub range: u32,
    /// The chart column under the pointer.
    pub hover_bar: Option<usize>,
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
        // Durations and activity strips move with the clock: one redraw a
        // second, nothing in between.
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
            usage: HashMap::new(),
            problem: None,
            connected: false,
            selected: None,
            filter: Filter::All,
            // `--usage` opens on the usage view.
            pane: if std::env::args().any(|a| a == "--usage") { Pane::Usage } else { Pane::Agents },
            // `--range 30` opens the usage view on 30 days.
            range: std::env::args()
                .skip_while(|a| a != "--range")
                .nth(1)
                .and_then(|n| n.parse().ok())
                .filter(|n| feed::RANGES.contains(n))
                .unwrap_or(7),
            hover_bar: None,
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
            Msg::Usage(days, u) => {
                self.usage.insert(days, u);
            }
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

    pub fn visible(&self) -> Vec<&Session> {
        let now = Utc::now();
        self.sessions.iter().filter(|s| self.filter.admits(&s.provider) && !s.stale(now)).collect()
    }

    pub fn selected_session(&self) -> Option<&Session> {
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

    pub fn set_filter(&mut self, f: Filter, cx: &mut Context<Self>) {
        self.filter = f;
        self.keep_selection();
        cx.notify();
    }

    pub fn set_pane(&mut self, p: Pane, cx: &mut Context<Self>) {
        self.pane = p;
        self.hover_bar = None;
        cx.notify();
    }

    pub fn set_range(&mut self, days: u32, cx: &mut Context<Self>) {
        self.range = days;
        self.hover_bar = None;
        cx.notify();
    }

    /// Plan limits, Claude's first.
    pub fn limits(&self) -> Vec<RateLimit> {
        let Some(u) = self.usage.get(&1).or_else(|| self.usage.values().next()) else { return vec![] };
        let mut l: Vec<RateLimit> = u.rate_limits.values().cloned().collect();
        l.sort_by_key(|r| (r.provider != "claude", r.window_minutes));
        l
    }

    // ---- actions ----

    fn select_next(&mut self, _: &SelectNext, _: &mut Window, cx: &mut Context<Self>) {
        if self.pane == Pane::Agents {
            self.step(1, cx);
        }
    }
    fn select_prev(&mut self, _: &SelectPrev, _: &mut Window, cx: &mut Context<Self>) {
        if self.pane == Pane::Agents {
            self.step(-1, cx);
        }
    }
    fn open_selected(&mut self, _: &OpenSelected, _: &mut Window, _: &mut Context<Self>) {
        if self.pane == Pane::Agents {
            if let Some(s) = self.selected_session() {
                feed::focus(s);
            }
        }
    }
    fn cycle_filter(&mut self, _: &CycleFilter, _: &mut Window, cx: &mut Context<Self>) {
        let i = Filter::ALL.iter().position(|f| *f == self.filter).unwrap_or(0);
        self.set_filter(Filter::ALL[(i + 1) % Filter::ALL.len()], cx);
    }
    fn cycle_range(&mut self, _: &CycleRange, _: &mut Window, cx: &mut Context<Self>) {
        let i = feed::RANGES.iter().position(|r| *r == self.range).unwrap_or(0);
        self.set_range(feed::RANGES[(i + 1) % feed::RANGES.len()], cx);
    }
    fn show_agents(&mut self, _: &ShowAgents, _: &mut Window, cx: &mut Context<Self>) {
        self.set_pane(Pane::Agents, cx);
    }
    fn show_usage(&mut self, _: &ShowUsage, _: &mut Window, cx: &mut Context<Self>) {
        self.set_pane(Pane::Usage, cx);
    }
}

impl Render for Board {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let content = match self.pane {
            Pane::Agents => self.agents_view(cx).into_any_element(),
            Pane::Usage => self.usage_view(cx).into_any_element(),
        };
        div()
            .size_full()
            .flex()
            .bg(rgb(INK))
            .text_color(rgb(TEXT))
            .font_family(SANS)
            .text_size(px(size::BODY))
            .track_focus(&self.focus)
            .on_action(cx.listener(Self::select_next))
            .on_action(cx.listener(Self::select_prev))
            .on_action(cx.listener(Self::open_selected))
            .on_action(cx.listener(Self::cycle_filter))
            .on_action(cx.listener(Self::cycle_range))
            .on_action(cx.listener(Self::show_agents))
            .on_action(cx.listener(Self::show_usage))
            .child(self.sidebar(cx))
            .child(div().flex_1().min_w_0().h_full().flex().child(content))
    }
}

impl Board {
    fn sidebar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let needs = self.sessions.iter().filter(|s| s.needs_you()).count();
        let working = self.sessions.iter().filter(|s| s.working()).count();
        let agents_count = if needs > 0 { Some((needs, NEEDS)) } else if working > 0 { Some((working, WORKING)) } else { None };
        let limits = self.limits();
        div()
            .w(px(layout::SIDEBAR))
            .flex_none()
            .h_full()
            .flex()
            .flex_col()
            .bg(rgb(DEEP))
            .border_r_1()
            .border_color(rgb(RULE))
            .child(div().h(px(layout::CHROME)).flex_none())
            .child(
                div()
                    .px(px(space::S3))
                    .flex()
                    .flex_col()
                    .gap(px(2.))
                    .child(
                        ui::nav_item("nav-agents", "Agents", self.pane == Pane::Agents, agents_count, "⌘1")
                            .on_click(cx.listener(|b, _: &ClickEvent, _, cx| b.set_pane(Pane::Agents, cx))),
                    )
                    .child(
                        ui::nav_item("nav-usage", "Usage", self.pane == Pane::Usage, None, "⌘2")
                            .on_click(cx.listener(|b, _: &ClickEvent, _, cx| b.set_pane(Pane::Usage, cx))),
                    ),
            )
            .child(div().flex_1())
            .when(!limits.is_empty(), |d| {
                d.child(
                    div()
                        .mx(px(space::S3))
                        .mb(px(space::S3))
                        .p(px(space::S3))
                        .flex()
                        .flex_col()
                        .gap(px(space::S3))
                        .rounded(px(radius::ROW))
                        .bg(rgb(INK))
                        .border_1()
                        .border_color(rgb(RULE))
                        .child(ui::label("Plan limits", size::SMALL, TEXT_3))
                        .children(limits.into_iter().map(limit_row)),
                )
            })
            .child(
                div()
                    .px(px(space::S4))
                    .pb(px(space::S4))
                    .flex()
                    .gap(px(space::S2))
                    .items_center()
                    .text_size(px(size::CAPTION))
                    .text_color(rgb(TEXT_3))
                    .child(ui::key("↑↓"))
                    .child("select")
                    .child(ui::key("↵"))
                    .child("open"),
            )
    }
}

fn limit_row(l: RateLimit) -> impl IntoElement {
    let pct = l.used_percent.clamp(0., 100.);
    let name = fmt::limit_name(&l);
    let resets = l.resets_at.map(|t| format!("Resets {}", fmt::when(t))).unwrap_or_default();
    div()
        .flex()
        .flex_col()
        .gap(px(5.))
        .child(
            div()
                .flex()
                .justify_between()
                .text_size(px(size::SMALL))
                .child(div().text_color(rgb(TEXT_2)).child(name))
                .child(div().font_weight(FontWeight::SEMIBOLD).child(format!("{pct:.0}%"))),
        )
        .child(ui::meter(pct / 100., ui::usage_color(pct)))
        .when(!resets.is_empty(), |d| d.child(ui::label(resets, size::CAPTION, TEXT_3)))
}

fn main() {
    Application::new().run(|cx: &mut App| {
        cx.text_system()
            .add_fonts(vec![
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexSans-Regular.otf").as_slice()),
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexSans-Medium.otf").as_slice()),
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexSans-SemiBold.otf").as_slice()),
                Cow::Borrowed(include_bytes!("../../macos/Fonts/IBMPlexSans-Bold.otf").as_slice()),
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
            KeyBinding::new("r", CycleRange, None),
            KeyBinding::new("cmd-1", ShowAgents, None),
            KeyBinding::new("cmd-2", ShowUsage, None),
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

        let bounds = Bounds::centered(None, size(px(1240.), px(800.)), cx);
        cx.open_window(
            WindowOptions {
                window_bounds: Some(WindowBounds::Windowed(bounds)),
                window_min_size: Some(size(px(900.), px(560.))),
                titlebar: Some(TitlebarOptions {
                    title: Some("Hall Monitor".into()),
                    appears_transparent: true,
                    traffic_light_position: Some(point(px(18.), px(20.))),
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
