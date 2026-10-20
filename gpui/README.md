# Hall Monitor in GPUI (experiment)

The main window rebuilt in [GPUI](https://gpui.rs), Zed's GPU-drawn UI
framework, to see whether it looks and runs better than the SwiftUI one.
It reads the same feed (`hallmonitor --stream`, `hallmonitor usage --json`);
the notch and the menu bar stay in the Swift app.

```bash
cd gpui && cargo run --release
```

Builds with the Command Line Tools alone: the `runtime_shaders` feature has
macOS compile GPUI's Metal shaders at launch, so no Xcode or `metal`
compiler is needed. It uses the installed app's `hallmonitor`
(`/Applications/HallMonitor.app/Contents/Helpers/hallmonitor`), or
`$HALLMONITOR_BIN`, or the one on your PATH.

Two views, ⌘1 and ⌘2:

- **Agents**: a sentence that says whether anything needs you, agents
  grouped by state with a ten-minute activity strip each, and an inspector
  with what the selected one is doing and a button to open it.
- **Usage**: agent time today, over 7 or 30 days: a stacked chart by
  provider (hover a column for its breakdown), when your agents work by
  weekday and hour, and the top projects, models and tools.

Plan limits sit in the sidebar.

Keys: ↑↓ or j/k select, ↵ (or a double-click) opens the agent, f cycles the
provider filter, r cycles the usage range, ⌘Q quits. `--usage` and
`--range 1|7|30` open on the usage view.

## Design system

`src/theme.rs` holds the tokens: cool ink surfaces, three text levels,
signal-lamp colors for states (working, needs you, error, subagents), one
accent for selection and the primary action, provider hues for charts, and
the type, spacing and radius scales. `src/ui.rs` holds the components built
from them (headline, panel, mark, lamp, tag, segmented control, meter,
activity pulse, inspector field, nav item). Views use only these.

Nothing animates on its own: the window redraws once a second for the
clock, and otherwise only when data or you change something, which keeps it
near 0% CPU.
