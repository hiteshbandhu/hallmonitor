import AppKit
import SwiftUI

// MARK: - The main window
//
// A regular Mac window over the same live feed the notch and menu use. The
// app stays a menu bar extra; while this window is open it also gets a Dock
// icon and a place in ⌘-Tab, like any app with a window.

enum Pane: String, CaseIterable, Identifiable {
    case agents, usage, machines
    var id: String { rawValue }
    var title: String {
        switch self {
        case .agents: return "Agents"
        case .usage: return "Usage"
        case .machines: return "Machines"
        }
    }
    var symbol: String {
        switch self {
        case .agents: return "rectangle.grid.2x2"
        case .usage: return "chart.bar.xaxis"
        case .machines: return "desktopcomputer"
        }
    }
}

@MainActor
final class Nav: ObservableObject {
    @Published var pane: Pane = .agents
    @Published var selection: String?
    @Published var inspector = true
    @Published var filter = "all"   // all / claude / codex / opencode
    @Published var search = ""
}

@MainActor
final class MainWindow: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    let nav = Nav()
    let usage: UsageStore
    private let board: Board

    init(board: Board) {
        self.board = board
        self.usage = UsageStore(board: board)
    }

    func show(_ pane: Pane? = nil) {
        if let pane { nav.pane = pane }
        if window == nil {
            let root = MainView(board: board, usage: usage, nav: nav)
            let host = NSHostingController(rootView: root)
            let w = NSWindow(contentViewController: host)
            w.title = "Hall Monitor"
            w.appearance = NSAppearance(named: .darkAqua)
            // No toolbar: content runs to the top and the traffic lights sit
            // over the sidebar. Each pane draws its own header and controls.
            w.styleMask = [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView]
            w.titlebarAppearsTransparent = true
            w.titleVisibility = .hidden
            w.backgroundColor = NSColor(Theme.canvas)
            w.setContentSize(NSSize(width: 1180, height: 760))
            w.minSize = NSSize(width: 860, height: 560)
            w.isReleasedWhenClosed = false
            // A normal document-style window: lives on one Space, can go full
            // screen, shows in Mission Control and ⌘-Tab / ⌘` cycling. Without
            // this a menu bar app's window floats over full-screen apps.
            w.collectionBehavior = [.managed, .participatesInCycle, .fullScreenPrimary]
            w.level = .normal
            w.tabbingMode = .disallowed
            w.delegate = self
            w.center()
            w.setFrameAutosaveName("HallMonitorMain")
            window = w
        }
        // Become a regular app first, so the window is ordered as a normal
        // app window (own Space, Dock icon, menu bar) rather than a UI element.
        if NSApp.activationPolicy() != .regular {
            NSApp.setActivationPolicy(.regular)
        }
        NSApp.activate()
        window?.makeKeyAndOrderFront(nil)
        usage.load()
    }

    func windowWillClose(_ notification: Notification) {
        // Back to a menu bar extra: no Dock icon without a window.
        DispatchQueue.main.async { NSApp.setActivationPolicy(.accessory) }
    }
}

struct MainView: View {
    @ObservedObject var board: Board
    @ObservedObject var usage: UsageStore
    @ObservedObject var nav: Nav

    var body: some View {
        NavigationSplitView {
            List(selection: Binding(get: { nav.pane }, set: { if let p = $0 { nav.pane = p } })) {
                SidebarHeader(board: board)
                    .listRowInsets(EdgeInsets(top: 30, leading: 6, bottom: 14, trailing: 6))
                    .selectionDisabled()
                Section {
                    ForEach(Pane.allCases) { p in
                        Label {
                            Text(p.title)
                        } icon: {
                            Image(systemName: p.symbol)
                                .foregroundStyle(nav.pane == p ? Theme.teal : Color.white.opacity(0.75))
                        }
                        .badge(badge(p))
                        .tag(p)
                    }
                }
                if !board.limits.isEmpty {
                    Section("Plan limits") {
                        ForEach(board.limits, id: \.key) { _, l in
                            SidebarLimit(limit: l)
                        }
                    }
                }
            }
            .listStyle(.sidebar)
            .navigationSplitViewColumnWidth(min: 190, ideal: 210, max: 260)
        } detail: {
            Group {
                switch nav.pane {
                case .agents: AgentsView(board: board, nav: nav)
                case .usage: UsageView(store: usage)
                case .machines: MachinesView(board: board)
                }
            }
            .transition(.opacity)
            .animation(.smooth(duration: 0.22), value: nav.pane)
        }
        .frame(minWidth: 860, minHeight: 560)
        .toolbar(removing: .sidebarToggle)
        .toolbar(.hidden, for: .windowToolbar)
        .preferredColorScheme(.dark)
        .tint(Theme.teal)
        .font(Theme.font(13))
    }

    private func badge(_ p: Pane) -> Int {
        switch p {
        case .agents: return board.needsYou.count
        case .machines: return board.machines.filter { !$0.live }.count
        case .usage: return 0
        }
    }
}

/// The app's mark and a one-line read of the fleet.
private struct SidebarHeader: View {
    @ObservedObject var board: Board

    var body: some View {
        HStack(spacing: 10) {
            Image(nsImage: NSApp.applicationIconImage)
                .resizable()
                .interpolation(.high)
                .frame(width: 34, height: 34)
            VStack(alignment: .leading, spacing: 1) {
                Text("Hall Monitor").font(Theme.display(13.5))
                Text(line)
                    .font(Theme.font(11))
                    .foregroundStyle(board.needsYou.isEmpty ? Color.white.opacity(0.5) : Theme.amber)
                    .contentTransition(.numericText())
                    .animation(.smooth, value: line)
            }
        }
    }

    private var line: String {
        let n = board.needsYou.count, w = board.working.count
        if n > 0 { return "\(n) need\(n == 1 ? "s" : "") you" }
        return w > 0 ? "\(w) working" : "All quiet"
    }
}

private struct SidebarLimit: View {
    let limit: RateLimit

    var body: some View {
        HStack(spacing: 8) {
            Ring(value: limit.used_percent / 100, color: Palette.limit(limit.used_percent), width: 3)
                .frame(width: 14, height: 14)
            Text("\(Provider.name(limit.provider)) \(limit.windowLabel)")
                .lineLimit(1)
            Spacer(minLength: 4)
            Text("\(limit.isStale ? "~" : "")\(Int(limit.used_percent.rounded()))%")
                .monospacedDigit()
                .foregroundStyle(.secondary)
                .contentTransition(.numericText(value: limit.used_percent))
        }
        .animation(.smooth, value: limit.used_percent)
    }
}

// MARK: - Shared design pieces

enum Palette {
    static let working = Color(red: 0.29, green: 0.87, blue: 0.5)
    static let needsYou = Theme.amber
    static let error = Theme.red
    static let idle = Color.white.opacity(0.45)

    static func status(_ s: Session) -> Color {
        if s.status == "error" { return error }
        if s.needsYou { return needsYou }
        if s.working { return working }
        return idle
    }

    static func provider(_ p: String) -> Color { Color(nsColor: Provider.color(p)) }

    static func limit(_ pct: Double) -> Color {
        pct >= 80 ? error : pct >= 50 ? needsYou : working
    }
}

/// A progress ring that animates to its value.
struct Ring: View {
    var value: Double
    var color: Color
    var width: CGFloat = 8

    var body: some View {
        ZStack {
            Circle().stroke(color.opacity(0.18), lineWidth: width)
            Circle()
                .trim(from: 0, to: max(0.001, min(1, value)))
                .stroke(color, style: StrokeStyle(lineWidth: width, lineCap: .round))
                .rotationEffect(.degrees(-90))
        }
        .animation(.spring(response: 0.6, dampingFraction: 0.85), value: value)
    }
}

/// Text that needs the time: reads the shared clock, so a hundred of them
/// still cost one timer.
struct ClockText<Content: View>: View {
    @ObservedObject private var clock = Clock.shared
    let content: (Date) -> Content

    init(@ViewBuilder _ content: @escaping (Date) -> Content) { self.content = content }

    var body: some View { content(clock.now) }
}

/// One shared one-second clock, so durations tick without a timer per view.
@MainActor
final class Clock: ObservableObject {
    static let shared = Clock()
    @Published private(set) var now = Date()
    private var timer: Timer?

    private init() {
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.now = Date() }
        }
        timer?.tolerance = 0.2
    }
}
