//! The design system's components. Views compose these; anything drawn
//! twice lives here.

use chrono::{DateTime, Utc};
use gpui::{BoxShadow, Div, FontWeight, SharedString, Stateful, div, point, prelude::*, px, rgb, rgba};

use crate::theme::*;

/// Text in one of the scale's sizes and a text level.
pub fn label(s: impl Into<SharedString>, size: f32, color: u32) -> Div {
    div().text_size(px(size)).text_color(rgb(color)).child(s.into())
}

/// A view's opening line: a sentence set large, with a quieter second line.
pub fn headline(main: impl Into<SharedString>, sub: impl Into<SharedString>) -> Div {
    div()
        .flex()
        .flex_col()
        .gap(px(space::S1))
        .child(
            div()
                .text_size(px(size::HEAD))
                .line_height(px(size::HEAD * 1.2))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(rgb(TEXT))
                .child(main.into()),
        )
        .child(label(sub, size::LEAD, TEXT_2))
}

/// A panel: one raised surface for one subject.
pub fn panel() -> Div {
    div()
        .flex()
        .flex_col()
        .gap(px(space::S4))
        .p(px(space::S5))
        .rounded(px(radius::PANEL))
        .bg(rgb(DEEP))
        .border_1()
        .border_color(rgb(RULE))
}

/// A panel's title, with an optional reading on the right.
pub fn panel_title(title: &str, reading: Option<Div>) -> Div {
    div()
        .flex()
        .items_baseline()
        .justify_between()
        .child(div().text_size(px(size::LEAD)).font_weight(FontWeight::SEMIBOLD).child(title.to_string()))
        .children(reading)
}

/// The provider's mark on a tile tinted with its color.
pub fn mark(provider: &str, size: f32) -> Div {
    let (glyph, scale) = match provider {
        "claude" => ("✻", 0.62),
        "codex" => (">_", 0.38),
        "opencode" => ("◰", 0.56),
        _ => ("•", 0.5),
    };
    let c = provider_color(provider);
    div()
        .size(px(size))
        .flex_none()
        .flex()
        .items_center()
        .justify_center()
        .rounded(px(size * 0.3))
        .bg(rgba(alpha(c, 0x1f)))
        .border_1()
        .border_color(rgba(alpha(c, 0x33)))
        .text_color(rgb(c))
        .font_family(if provider == "codex" { MONO } else { SANS })
        .font_weight(FontWeight::MEDIUM)
        .text_size(px(size * scale))
        .child(glyph)
}

/// A signal lamp: a dot that glows while it means something.
pub fn lamp(color: u32, lit: bool) -> Div {
    let d = div().size(px(8.)).flex_none().rounded_full().bg(rgb(color));
    if !lit {
        return d;
    }
    d.shadow(vec![BoxShadow {
        color: rgba(alpha(color, 0x8c)).into(),
        offset: point(px(0.), px(0.)),
        blur_radius: px(8.),
        spread_radius: px(1.),
    }])
}

/// A small quiet tag: a model, a machine.
pub fn tag(s: impl Into<SharedString>) -> Div {
    div()
        .flex_none()
        .px(px(6.))
        .py(px(1.))
        .rounded(px(radius::CHIP))
        // Translucent, so it reads on any surface, the selected row's too.
        .bg(rgba(alpha(TEXT, 0x14)))
        .text_size(px(size::CAPTION))
        .text_color(rgb(TEXT_2))
        .child(s.into())
}

/// A key cap for a shortcut hint.
pub fn key(s: &str) -> Div {
    div()
        .px(px(5.))
        .rounded(px(4.))
        .border_1()
        .border_color(rgb(RULE))
        .text_size(px(size::CAPTION))
        .text_color(rgb(TEXT_3))
        .child(s.to_string())
}

/// The container of a segmented control.
pub fn segmented() -> Div {
    div().flex().p(px(2.)).gap(px(2.)).rounded(px(radius::CHIP + 2.)).bg(rgb(RAISE))
}

/// One option of a segmented control.
pub fn segment(id: impl Into<SharedString>, text: &str, on: bool) -> Stateful<Div> {
    div()
        .id(id.into())
        .px(px(space::S3))
        .py(px(3.))
        .rounded(px(radius::CHIP))
        .cursor_pointer()
        .text_size(px(size::SMALL))
        .font_weight(FontWeight::MEDIUM)
        .text_color(rgb(if on { TEXT } else { TEXT_2 }))
        .when(on, |d| d.bg(rgb(LIFT)))
        .when(!on, |d| d.hover(|d| d.text_color(rgb(TEXT))))
        .child(text.to_string())
}

/// A horizontal meter, full width of its parent.
pub fn meter(fraction: f64, color: u32) -> Div {
    let f = fraction.clamp(0., 1.) as f32;
    div()
        .h(px(4.))
        .w_full()
        .rounded(px(2.))
        .bg(rgb(RAISE))
        .child(div().h_full().w(gpui::relative(f)).rounded(px(2.)).bg(rgb(color)))
}

/// Color for how much of a limit is used.
pub fn usage_color(pct: f64) -> u32 {
    if pct >= 85. {
        ERROR
    } else if pct >= 60. {
        NEEDS
    } else {
        WORKING
    }
}

/// The last ten minutes of an agent's tool calls and replies, in 20-second
/// columns, in the color of its state.
pub fn pulse(activity: &[i64], now: DateTime<Utc>, color: u32) -> Div {
    const COLS: usize = 30;
    const SPAN: i64 = 20;
    let mut counts = [0u32; COLS];
    let end = now.timestamp();
    for &t in activity {
        let age = end - t;
        if (0..SPAN * COLS as i64).contains(&age) {
            counts[COLS - 1 - (age / SPAN) as usize] += 1;
        }
    }
    let peak = counts.iter().copied().max().unwrap_or(0).max(3) as f32;
    div().flex_none().flex().items_end().gap(px(2.)).h(px(20.)).children(counts.into_iter().map(move |n| {
        let h = if n == 0 { 2. } else { 5. + 15. * (n as f32 / peak).min(1.) };
        div().w(px(2.)).h(px(h)).rounded(px(1.)).bg(if n == 0 { rgb(RAISE) } else { rgb(color) })
    }))
}

/// One fact in the inspector: a quiet label over its value.
pub fn field(name: &str, value: impl IntoElement) -> Div {
    div()
        .flex()
        .flex_col()
        .gap(px(3.))
        .child(label(name.to_string(), size::SMALL, TEXT_3))
        .child(div().text_size(px(size::BODY)).text_color(rgb(TEXT)).child(value))
}

/// A sidebar navigation item.
pub fn nav_item(id: &'static str, text: &str, on: bool, count: Option<(usize, u32)>, shortcut: &str) -> Stateful<Div> {
    div()
        .id(id)
        .flex()
        .items_center()
        .gap(px(space::S2))
        .px(px(space::S3))
        .py(px(7.))
        .rounded(px(radius::ROW - 2.))
        .cursor_pointer()
        .text_size(px(size::BODY))
        .font_weight(FontWeight::MEDIUM)
        .text_color(rgb(if on { TEXT } else { TEXT_2 }))
        .when(on, |d| d.bg(rgb(RAISE)))
        .when(!on, |d| d.hover(|d| d.bg(rgb(RAISE)).text_color(rgb(TEXT))))
        .child(div().flex_1().child(text.to_string()))
        .children(count.map(|(n, color)| label(n.to_string(), size::SMALL, color).font_weight(FontWeight::SEMIBOLD)))
        .child(key(shortcut))
}
