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

Keys: ↑↓ or j/k select, ↵ (or a double-click) opens the agent, f cycles the
provider filter, ⌘Q quits.
