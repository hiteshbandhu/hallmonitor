//! The design system's tokens. Everything on screen takes its color, size
//! and spacing from here; views never use a raw value.
//!
//! Cool ink surfaces instead of black, three text levels, and color kept for
//! meaning: each state is a signal lamp (working, needs you, error), the
//! single accent marks selection and focus, and providers get their own
//! quiet hues for charts.

pub const SANS: &str = "IBM Plex Sans";
pub const MONO: &str = "IBM Plex Mono";

// Surfaces, back to front.
pub const INK: u32 = 0x0c111b; // the window
pub const DEEP: u32 = 0x121826; // sidebar, inspector, panels
pub const RAISE: u32 = 0x1a2233; // hover, selection, tracks
pub const LIFT: u32 = 0x222b3f; // controls on a panel
pub const RULE: u32 = 0x263047; // hairlines

// Text.
pub const TEXT: u32 = 0xe8ecf4;
pub const TEXT_2: u32 = 0xa3adc2;
pub const TEXT_3: u32 = 0x6b768c;

// Signal lamps.
pub const WORKING: u32 = 0x5ee6b5;
pub const NEEDS: u32 = 0xffd15c;
pub const ERROR: u32 = 0xff7a7a;
pub const SUB: u32 = 0x7cc4ff;

/// The one accent: selection, focus, the primary action.
pub const ACCENT: u32 = 0x9aa8ff;

// Providers, for marks and chart series.
pub const CLAUDE: u32 = 0xf0a27a;
pub const CODEX: u32 = 0xc9d1e3;
pub const OPENCODE: u32 = 0xb7a6ff;

/// Type scale (px): caption, small, body, lead, title, head, display.
pub mod size {
    pub const CAPTION: f32 = 11.;
    pub const SMALL: f32 = 12.;
    pub const BODY: f32 = 13.;
    pub const LEAD: f32 = 15.;
    pub const TITLE: f32 = 20.;
    pub const HEAD: f32 = 28.;
}

/// Spacing on a 4px grid.
pub mod space {
    pub const S1: f32 = 4.;
    pub const S2: f32 = 8.;
    pub const S3: f32 = 12.;
    pub const S4: f32 = 16.;
    pub const S5: f32 = 24.;
    pub const S6: f32 = 32.;
}

/// Corner radii by role: chips, rows, panels.
pub mod radius {
    pub const CHIP: f32 = 6.;
    pub const ROW: f32 = 10.;
    pub const PANEL: f32 = 14.;
}

/// Fixed regions of the window.
pub mod layout {
    pub const SIDEBAR: f32 = 216.;
    pub const INSPECTOR: f32 = 320.;
    /// The strip the traffic lights sit in; it stays free of controls so the
    /// window drags from it.
    pub const CHROME: f32 = 52.;
}

pub fn provider_color(p: &str) -> u32 {
    match p {
        "claude" => CLAUDE,
        "codex" => CODEX,
        "opencode" => OPENCODE,
        _ => TEXT_3,
    }
}

pub fn provider_name(p: &str) -> &str {
    match p {
        "claude" => "Claude",
        "codex" => "Codex",
        "opencode" => "opencode",
        other => other,
    }
}

/// c at alpha a (0-255), for glows and heat.
pub fn alpha(c: u32, a: u8) -> u32 {
    (c << 8) | a as u32
}
