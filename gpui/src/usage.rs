//! The usage view: where agent time went, from the agents' own logs.

use chrono::{Datelike, Local, Timelike};
use gpui::{ClickEvent, Context, Div, FontWeight, SharedString, div, prelude::*, px, relative, rgb, rgba};

use crate::Board;
use crate::feed::{self, Counters, Named, Usage};
use crate::fmt;
use crate::theme::*;
use crate::ui;

const CHART_H: f32 = 180.;
const PROVIDERS: [&str; 3] = ["claude", "codex", "opencode"];

impl Board {
    pub fn usage_view(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let u = self.usage.get(&self.range);
        let mut col = div()
            .id("usage")
            .size_full()
            .overflow_y_scroll()
            .px(px(space::S6))
            .pb(px(space::S6))
            .flex()
            .flex_col()
            .gap(px(space::S5))
            .child(div().h(px(layout::CHROME - space::S5)).flex_none())
            .child(self.usage_headline(u))
            .child(self.ranges(cx));
        if let Some(u) = u {
            col = col
                .child(self.chart(u, cx))
                .child(heatmap(u, self.range))
                .child(
                    div()
                        .flex()
                        .gap(px(space::S4))
                        .child(top_list("Projects", &u.projects, |c| c.active_s, fmt::hours))
                        .child(top_list("Models", &u.models, |c| c.tokens.total() as f64, |v| fmt::human(v as i64)))
                        .child(top_list("Tools", &u.tools, |c| c.tools as f64, |v| fmt::count(v as i64))),
                );
        }
        col
    }

    fn usage_headline(&self, u: Option<&Usage>) -> Div {
        let Some(u) = u else {
            return ui::headline("Reading your agent logs…", "The first read takes a few seconds; after that it's instant.");
        };
        let when = match self.range {
            1 => "today",
            7 => "in the last 7 days",
            _ => "in the last 30 days",
        };
        let t = &u.total;
        let mut sub = format!(
            "{} prompts, {} tool calls and {} tokens across {}.",
            fmt::count(t.prompts),
            fmt::count(t.tools),
            fmt::human(t.tokens.total()),
            fmt::plural(u.sessions as usize, "session", "sessions"),
        );
        if let Some(c) = fmt::cache_share(&t.tokens) {
            sub += &format!(" {c:.0}% of input came from cache.");
        }
        ui::headline(format!("{} of agent time {when}.", fmt::hours_words(t.active_s)), sub)
    }

    fn ranges(&self, cx: &mut Context<Self>) -> Div {
        div()
            .flex()
            .items_center()
            .justify_between()
            .child(ui::segmented().children(feed::RANGES.iter().map(|&days| {
                let text = match days {
                    1 => "Today",
                    7 => "7 days",
                    _ => "30 days",
                };
                ui::segment(SharedString::from(format!("range-{days}")), text, days == self.range)
                    .on_click(cx.listener(move |b, _: &ClickEvent, _, cx| b.set_range(days, cx)))
            })))
            .child(ui::label("r to switch", size::SMALL, TEXT_3))
    }

    /// Agent time per day by provider; for today, per hour.
    fn chart(&self, u: &Usage, cx: &mut Context<Self>) -> Div {
        // Columns: a label, a reading, and each provider's seconds.
        let cols: Vec<(String, String, Vec<(&str, f64)>)> = if self.range == 1 {
            let wd = Local::now().weekday().num_days_from_monday() as usize;
            let row = u.heat.get(wd).cloned().unwrap_or_default();
            (0..24)
                .map(|h| {
                    let label = if h % 6 == 0 { format!("{h}:00") } else { String::new() };
                    (label, format!("{h}:00 – {}:00", h + 1), vec![("all", row.get(h).copied().unwrap_or(0.))])
                })
                .collect()
        } else {
            let n = u.days.len();
            u.days
                .iter()
                .enumerate()
                .map(|(i, d)| {
                    let every = if n > 14 { 5 } else { 1 };
                    let label = if (n - 1 - i) % every == 0 { fmt::day_label(&d.date, self.range) } else { String::new() };
                    let parts = PROVIDERS.iter().map(|p| (*p, d.by_provider.get(*p).map_or(0., |c| c.active_s))).collect();
                    (label, fmt::day_long(&d.date), parts)
                })
                .collect()
        };
        let peak = cols.iter().map(|c| c.2.iter().map(|p| p.1).sum::<f64>()).fold(0., f64::max).max(60.);
        // The reading follows the pointer; otherwise it shows now: this hour, or today.
        let now_col = if self.range == 1 { Local::now().hour() as usize } else { cols.len().saturating_sub(1) };
        let focus = self.hover_bar.filter(|i| *i < cols.len()).unwrap_or(now_col);
        let reading = cols.get(focus).map(|(_, title, parts)| {
            let total: f64 = parts.iter().map(|p| p.1).sum();
            let mut r = div()
                .flex()
                .items_baseline()
                .gap(px(space::S3))
                .child(ui::label(title.clone(), size::SMALL, TEXT_2))
                .child(div().text_size(px(size::LEAD)).font_weight(FontWeight::SEMIBOLD).child(fmt::hours(total)));
            if parts.len() > 1 {
                for (p, s) in parts.iter().filter(|p| p.1 > 0.) {
                    r = r.child(
                        div()
                            .flex()
                            .items_center()
                            .gap(px(5.))
                            .child(div().size(px(7.)).rounded(px(2.)).bg(rgb(provider_color(p))))
                            .child(ui::label(format!("{} {}", provider_name(p), fmt::hours(*s)), size::SMALL, TEXT_2)),
                    );
                }
            }
            r
        });
        let this = cx.entity().downgrade();
        let gap = if cols.len() > 14 { 3. } else { 10. };
        let bars = div().flex_1().h(px(CHART_H)).flex().items_end().gap(px(gap)).children(cols.iter().enumerate().map(|(i, (_, _, parts))| {
            let total: f64 = parts.iter().map(|p| p.1).sum();
            let dim = self.hover_bar.is_some_and(|h| h != i);
            let this = this.clone();
            let mut stack = div().w_full().max_w(px(40.)).flex().flex_col_reverse().rounded_t(px(4.)).overflow_hidden();
            for (p, s) in parts.iter().filter(|p| p.1 > 0.) {
                let color = if *p == "all" { ACCENT } else { provider_color(p) };
                stack = stack.child(div().w_full().h(px((s / peak) as f32 * CHART_H)).bg(rgb(color)));
            }
            div()
                .id(("bar", i))
                .flex_1()
                .h_full()
                .flex()
                .flex_col()
                .justify_end()
                .items_center()
                .when(dim, |d| d.opacity(0.4))
                .on_hover(move |hovered, _, app| {
                    let _ = this.update(app, |b, cx| {
                        if *hovered {
                            b.hover_bar = Some(i);
                        } else if b.hover_bar == Some(i) {
                            b.hover_bar = None;
                        }
                        cx.notify();
                    });
                })
                .child(if total > 0. { stack } else { div().w_full().max_w(px(40.)).h(px(2.)).rounded(px(1.)).bg(rgb(RAISE)) })
        }));
        // Gridlines at the peak and half of it, labelled in a gutter.
        let grid = div()
            .absolute()
            .top_0()
            .left_0()
            .right_0()
            .h(px(CHART_H))
            .children([0., 0.5].map(|f| {
                // Centered on its line: the label is ~14px tall.
                div()
                    .absolute()
                    .left_0()
                    .right_0()
                    .top(relative(f))
                    .mt(px(-7.))
                    .flex()
                    .items_center()
                    .gap(px(space::S2))
                    .child(ui::label(fmt::hours(peak * (1. - f as f64)), size::CAPTION, TEXT_3).w(px(40.)))
                    .child(div().flex_1().h(px(1.)).bg(rgb(RULE)))
            }));
        let labels = div().flex().gap(px(gap)).pl(px(48.)).children(cols.iter().map(|(label, _, _)| {
            // Labels are sparse, so a wide one may spill past its column.
            div().flex_1().min_w_0().flex().justify_center().child(ui::label(label.clone(), size::CAPTION, TEXT_3).whitespace_nowrap().flex_none())
        }));
        let title = if self.range == 1 { "Agent time by hour" } else { "Agent time per day" };
        ui::panel()
            .child(ui::panel_title(title, reading))
            .child(
                div()
                    .flex()
                    .flex_col()
                    .gap(px(space::S2))
                    .child(div().relative().h(px(CHART_H)).flex().child(grid).child(div().w(px(48.)).flex_none()).child(bars))
                    .child(labels),
            )
    }
}

/// Active time by weekday and hour over the range.
fn heatmap(u: &Usage, range: u32) -> Div {
    let peak = u.heat.iter().flatten().copied().fold(0., f64::max).max(1.);
    let days = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
    let levels = [0x2e, 0x5c, 0x8f, 0xc4, 0xff];
    let mut grid = div().flex().flex_col().gap(px(3.));
    for (d, name) in days.iter().enumerate() {
        let row = u.heat.get(d).cloned().unwrap_or_default();
        grid = grid.child(
            div().flex().items_center().gap(px(3.)).child(ui::label(*name, size::CAPTION, TEXT_3).w(px(36.)).flex_none()).children((0..24).map(|h| {
                let v = row.get(h).copied().unwrap_or(0.);
                let cell = div().flex_1().h(px(16.)).rounded(px(3.));
                if v <= 0. {
                    cell.bg(rgb(RAISE))
                } else {
                    let level = (((v / peak) * levels.len() as f64).ceil() as usize).clamp(1, levels.len()) - 1;
                    cell.bg(rgba(alpha(WORKING, levels[level])))
                }
            })),
        );
    }
    let hours = div().flex().gap(px(3.)).pl(px(39.)).children((0..24).map(|h| {
        div().flex_1().child(ui::label(if h % 6 == 0 { format!("{h}:00") } else { String::new() }, size::CAPTION, TEXT_3))
    }));
    let span = if range == 1 { "today" } else { "over these days" };
    ui::panel()
        .child(ui::panel_title("When your agents work", Some(ui::label(format!("Active time by hour, {span}"), size::SMALL, TEXT_3))))
        .child(div().flex().flex_col().gap(px(space::S2)).child(grid).child(hours))
}

/// The top six of a list, each with a bar against the first.
fn top_list(title: &str, items: &[Named], value: impl Fn(&Counters) -> f64, show: impl Fn(f64) -> String) -> Div {
    let top: Vec<(&Named, f64)> = items.iter().map(|n| (n, value(&n.counters))).filter(|(_, v)| *v > 0.).take(6).collect();
    let peak = top.first().map_or(1., |t| t.1).max(1.);
    let mut list = div().flex().flex_col().gap(px(space::S3));
    if top.is_empty() {
        list = list.child(ui::label("Nothing yet", size::SMALL, TEXT_3));
    }
    for (n, v) in top {
        let name = if title == "Models" { fmt::model_name(&n.name) } else { n.name.clone() };
        let color = if n.provider.is_empty() { ACCENT } else { provider_color(&n.provider) };
        list = list.child(
            div()
                .flex()
                .flex_col()
                .gap(px(5.))
                .child(
                    div()
                        .flex()
                        .justify_between()
                        .gap(px(space::S2))
                        .child(div().min_w_0().truncate().text_size(px(size::BODY)).child(name))
                        .child(ui::label(show(v), size::SMALL, TEXT_2).flex_none()),
                )
                .child(ui::meter(v / peak, color)),
        );
    }
    ui::panel().flex_1().min_w_0().child(ui::panel_title(title, None)).child(list)
}
