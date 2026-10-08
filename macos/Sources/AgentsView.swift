import AppKit
import SwiftUI

// MARK: - Agents: the live board, as a Mac window

struct AgentsView: View {
    @ObservedObject var board: Board
    @ObservedObject var nav: Nav
    @FocusState private var focused: Bool

    private var visible: [Session] {
        board.sessions.filter { s in
            !s.stale
                && (nav.filter == "all" || s.provider == nav.filter)
                && (nav.search.isEmpty || s.name.localizedCaseInsensitiveContains(nav.search)
                    || s.project.localizedCaseInsensitiveContains(nav.search)
                    || (s.last ?? "").localizedCaseInsensitiveContains(nav.search))
        }
    }

    private var selected: Session? {
        board.sessions.first { $0.key == nav.selection }
    }

    var body: some View {
        let list = visible
        let groups: [(String, [Session])] = [
            ("Needs you", list.filter(\.needsYou)),
            ("Working", list.filter(\.working)),
            ("Idle", list.filter { !$0.needsYou && !$0.working }),
        ].filter { !$0.1.isEmpty }

        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                PaneHeader(title: "Agents", subtitle: subtitle) {
                    Chips(options: [("all", "All"), ("claude", "Claude"), ("codex", "Codex"), ("opencode", "opencode")], selection: $nav.filter)
                    SearchField(text: $nav.search, prompt: "Search agents")
                    IconButton(symbol: "terminal", help: "Open the board in a terminal") { Launcher.openBoard() }
                    IconButton(symbol: "sidebar.right", on: nav.inspector, help: "Show or hide details") {
                        withAnimation(.snappy) { nav.inspector.toggle() }
                    }
                }
                Header(board: board)
                if list.isEmpty {
                    ContentUnavailableView {
                        Label(board.connected ? "No agents running" : "Connecting…",
                              systemImage: board.connected ? "moon.zzz" : "antenna.radiowaves.left.and.right")
                    } description: {
                        Text(nav.search.isEmpty ? "Start Claude Code, Codex or opencode and they show up here, live."
                                                : "Nothing matches “\(nav.search)”.")
                    }
                    .frame(maxWidth: .infinity, minHeight: 260)
                }
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 300, maximum: 460), spacing: 14, alignment: .top)],
                          alignment: .leading, spacing: 14, pinnedViews: []) {
                    ForEach(groups, id: \.0) { title, items in
                        Section {
                            ForEach(items, id: \.key) { s in
                                AgentCard(session: s, history: board.history[s.key] ?? [],
                                          selected: nav.selection == s.key)
                                    .onTapGesture(count: 2) { board.focus(s) }
                                    .onTapGesture { nav.selection = s.key }
                                    .contextMenu { menu(for: s) }
                                    .transition(.scale(scale: 0.96).combined(with: .opacity))
                            }
                        } header: {
                            SectionTitle(title: title, count: items.count)
                        }
                    }
                }
                .animation(.spring(response: 0.42, dampingFraction: 0.86), value: list.map(\.key))
                .animation(.spring(response: 0.42, dampingFraction: 0.86), value: list.map(\.status))
            }
            .padding(24)
        }
        .scrollContentBackground(.hidden)
        .background(Backdrop())
        .focusable()
        .focusEffectDisabled()
        .focused($focused)
        .onKeyPress(.return) {
            if let s = selected { board.focus(s); return .handled }
            return .ignored
        }
        .onKeyPress(.escape) {
            nav.selection = nil
            return .handled
        }
        .onAppear { focused = true }
        .inspector(isPresented: $nav.inspector) {
            Group {
                if let s = selected {
                    AgentInspector(session: s, board: board)
                } else {
                    ContentUnavailableView("No agent selected", systemImage: "cursorarrow.click",
                                           description: Text("Click a card to see everything about it. Double-click, or press Return, to jump to it."))
                }
            }
            .inspectorColumnWidth(min: 260, ideal: 300, max: 380)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .background(Theme.panel.ignoresSafeArea())
        }
    }

    private var subtitle: String {
        let w = board.working.count, n = board.needsYou.count
        if n > 0 { return "\(n) need\(n == 1 ? "s" : "") you · \(w) working" }
        return w > 0 ? "\(w) working" : "All quiet"
    }

    @ViewBuilder
    private func menu(for s: Session) -> some View {
        if (s.host ?? "").isEmpty {
            Button("Open") { board.focus(s) }
            Button("Show in Finder") { NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: s.cwd) }
        }
        Button("Copy Session ID") {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(s.id, forType: .string)
        }
        .disabled(s.id.isEmpty)
    }
}

// MARK: Header: the numbers, and the fleet over the last few minutes

private struct Header: View {
    @ObservedObject var board: Board

    var body: some View {
        let subs = board.sessions.reduce(0) { $0 + $1.runningSubagents }
        HStack(alignment: .top, spacing: 12) {
            Stat(title: "Needs you", value: board.needsYou.count, color: board.needsYou.isEmpty ? .secondary : Palette.needsYou,
                 symbol: "exclamationmark.circle.fill")
            Stat(title: "Working", value: board.working.count, color: board.working.isEmpty ? .secondary : Palette.working,
                 symbol: "bolt.fill")
            Stat(title: "Idle", value: board.idle.count, color: .secondary, symbol: "moon.fill")
            Stat(title: "Subagents", value: subs, color: subs == 0 ? .secondary : .teal, symbol: "arrow.triangle.branch")
            FleetChart(samples: board.fleet)
                .frame(maxWidth: .infinity)
        }
        .frame(height: 104)
    }
}

private struct Stat: View {
    let title: String
    let value: Int
    let color: Color
    let symbol: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Eyebrow(text: title)
            Text("\(value)")
                .font(Theme.number(40))
                .monospacedDigit()
                .foregroundStyle(title == "Working" && value > 0 ? AnyShapeStyle(Theme.work) : AnyShapeStyle(color))
                .contentTransition(.numericText(value: Double(value)))
                .animation(.spring(response: 0.4, dampingFraction: 0.8), value: value)
        }
        .padding(14)
        .frame(width: 128, height: 104, alignment: .topLeading)
        .background(Card())
    }
}

/// How many agents were busy over the last five minutes, newest on the
/// right. Drawn with a Canvas: it redraws only when a snapshot arrives.
private struct FleetChart: View {
    let samples: [FleetSample]

    var body: some View {
        let values = samples.map { Double($0.working + $0.needsYou) }
        let top = max(3, (values.max() ?? 0) + 1)
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Eyebrow(text: "Fleet activity")
                Spacer()
                Text("5 min")
                    .font(Theme.font(10.5)).foregroundStyle(.white.opacity(0.3))
            }
            Canvas { ctx, size in
                // Baseline grid.
                for k in 1...2 {
                    let y = size.height * CGFloat(k) / 3
                    ctx.fill(Path(CGRect(x: 0, y: y, width: size.width, height: 0.5)), with: .color(.white.opacity(0.06)))
                }
                guard values.count > 1 else { return }
                let n = Board.historyLength
                let step = size.width / CGFloat(n - 1)
                let x0 = CGFloat(n - values.count) * step
                func pt(_ i: Int) -> CGPoint {
                    CGPoint(x: x0 + CGFloat(i) * step, y: size.height * (1 - CGFloat(values[i] / top)))
                }
                var line = Path()
                line.move(to: pt(0))
                for i in 1..<values.count { line.addLine(to: pt(i)) }
                var area = line
                area.addLine(to: CGPoint(x: pt(values.count - 1).x, y: size.height))
                area.addLine(to: CGPoint(x: x0, y: size.height))
                area.closeSubpath()
                ctx.fill(area, with: .linearGradient(Gradient(colors: [Theme.lime.opacity(0.4), Theme.teal.opacity(0.03)]),
                                                     startPoint: .zero, endPoint: CGPoint(x: 0, y: size.height)))
                ctx.stroke(line, with: .linearGradient(Gradient(colors: [Theme.teal, Theme.lime]),
                                                       startPoint: CGPoint(x: x0, y: 0), endPoint: CGPoint(x: size.width, y: 0)),
                           lineWidth: 1.5)
            }
        }
        .padding(14)
        .frame(height: 104)
        .background(Card())
    }
}

private struct SectionTitle: View {
    let title: String
    let count: Int

    var body: some View {
        HStack(spacing: 6) {
            Text(title).font(Theme.display(15))
            Text("\(count)")
                .font(Theme.font(12).monospacedDigit())
                .foregroundStyle(.secondary)
                .contentTransition(.numericText(value: Double(count)))
            Spacer()
        }
        .padding(.top, 6)
    }
}

/// The surface every tile and card sits on.
struct Card: View {
    var highlight: Color? = nil
    var selected = false
    var hover = false

    var body: some View { Panel(glow: highlight, selected: selected, hover: hover) }
}

// MARK: One agent

struct AgentCard: View, Equatable {
    let session: Session
    let history: [UInt8]
    let selected: Bool
    @State private var hover = false

    // Redraw only when the agent changes; the elapsed time ticks inside the
    // pill on its own.
    static func == (a: AgentCard, b: AgentCard) -> Bool {
        a.session == b.session && a.history == b.history && a.selected == b.selected
    }

    var body: some View {
        let s = session
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 10) {
                Image(nsImage: ProjectIcon.badged(cwd: s.cwd, host: s.host, provider: s.provider, size: 34))
                    .interpolation(.high)
                    .frame(width: 34, height: 34)
                VStack(alignment: .leading, spacing: 2) {
                    Text(s.name)
                        .font(Theme.font(13.5, .semibold))
                        .foregroundStyle(.white.opacity(0.95))
                        .lineLimit(1)
                    Text(meta)
                        .font(Theme.font(11))
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer(minLength: 6)
                StatusPill(session: s, compact: true)
            }
            .frame(height: 36, alignment: .top)
            // Every row is always there and one line tall, so all cards are
            // the same height and line up across the grid.
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Image(systemName: activitySymbol)
                    .font(Theme.font(11))
                    .foregroundStyle(s.working || s.needsYou ? Palette.status(s) : Color.white.opacity(0.5))
                    .frame(width: 14)
                Text(activity)
                    .font(Theme.font(12))
                    .foregroundStyle(s.working || s.needsYou ? .primary : .secondary)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
            .frame(height: 16, alignment: .leading)
            Group {
                if let p = s.prompt, !p.isEmpty {
                    Text("“\(p)”").foregroundStyle(.white.opacity(0.5))
                } else {
                    Text("No prompt yet").foregroundStyle(.white.opacity(0.25))
                }
            }
            .font(Theme.font(11.5).italic())
            .lineLimit(1)
            .frame(height: 16, alignment: .leading)
            HStack(spacing: 8) {
                Timeline(history: history)
                    .frame(height: 6)
                if let sa = s.subagents, sa.total > 0 {
                    SubagentChip(running: s.runningSubagents, total: sa.total)
                }
            }
            .frame(height: 20)
        }
        .frame(maxWidth: .infinity, alignment: .topLeading)
        .padding(.vertical, 14)
        .padding(.leading, 18)
        .padding(.trailing, 14)
        .background(Card(highlight: s.needsYou ? Palette.needsYou : nil, selected: selected, hover: hover))
        .overlay(alignment: .leading) {
            StatusRail(color: Palette.status(s))
                .padding(.vertical, 12)
                .padding(.leading, 6)
                .opacity(s.working || s.needsYou ? 1 : 0.35)
        }
        .animation(.easeOut(duration: 0.15), value: hover)
        .onHover { hover = $0 }
        .contentShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        .help((s.host ?? "").isEmpty ? "Double-click to open" : "Runs on \(s.host!)")
    }

    private var meta: String {
        var parts = [session.project]
        if let m = session.model, !m.isEmpty { parts.append(Model.display(m)) }
        if let c = session.context_tokens, c > 0 { parts.append("\(c / 1000)k context") }
        if let h = session.host, !h.isEmpty { parts.append(h) }
        return parts.joined(separator: " · ")
    }

    private var activity: String {
        if let last = session.last, !last.isEmpty {
            return last.hasPrefix("↳ ") ? String(last.dropFirst(2)) : last
        }
        return session.working ? "Thinking…" : "No activity yet"
    }

    private var activitySymbol: String {
        guard let last = session.last, !last.isEmpty else { return "ellipsis" }
        if session.needsYou { return "hand.raised.fill" }
        return last.hasPrefix("↳ ") ? "text.bubble" : "hammer.fill"
    }
}

struct StatusPill: View {
    let session: Session
    @ObservedObject private var clock = Clock.shared
    private var now: Date { clock.now }
    /// Cards sit under a section title that already says the status, so
    /// they only need the glyph and how long.
    var compact = false

    var body: some View {
        let c = Palette.status(session)
        HStack(spacing: 5) {
            if session.working {
                Thinking(provider: session.provider)
                    .scaleEffect(0.8)
                    .frame(width: 11, height: 11)
            } else {
                Image(systemName: symbol)
                    .font(Theme.font(9, .bold))
            }
            Text(label)
                .monospacedDigit()
        }
        .font(Theme.font(11, .semibold))
        .foregroundStyle(c)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(Capsule().fill(c.opacity(0.13)))
    }

    private var symbol: String {
        if session.status == "error" { return "xmark.octagon.fill" }
        if session.needsYou { return "exclamationmark" }
        if session.working { return "circle.fill" }
        return "moon.fill"
    }

    private var label: String {
        let word = statusWord(session.status)
        guard let since = session.status_since else { return word }
        let t = shortDuration(now.timeIntervalSince(since))
        if compact { return session.needsYou ? "\(word) · \(t)" : t }
        return "\(word) · \(t)"
    }
}

private struct SubagentChip: View {
    let running: Int
    let total: Int

    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: "arrow.triangle.branch")
            Text(running > 0 ? "\(running)/\(total)" : "\(total)").monospacedDigit()
        }
        .font(Theme.font(10.5, .semibold))
        .foregroundStyle(running > 0 ? Color.teal : Color.secondary)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(Capsule().fill((running > 0 ? Color.teal : Color.secondary).opacity(0.12)))
        .help(running > 0 ? "\(running) of \(total) subagents running" : "\(total) subagents, none running")
    }
}

/// The last five minutes, a sliver per snapshot: green working, amber
/// waiting on you, faint when idle. Drawn, not laid out, so it's cheap.
struct Timeline: View {
    let history: [UInt8]

    var body: some View {
        Canvas { ctx, size in
            let n = Board.historyLength
            let w = size.width / CGFloat(n)
            let start = n - history.count
            ctx.fill(Path(roundedRect: CGRect(origin: .zero, size: size), cornerRadius: 3),
                     with: .color(.white.opacity(0.07)))
            for (i, v) in history.enumerated() where v > 0 {
                let x = CGFloat(start + i) * w
                ctx.fill(Path(CGRect(x: x, y: 0, width: w + 0.6, height: size.height)),
                         with: .color(v == 2 ? Palette.needsYou : Palette.working))
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: 3))
    }
}

// MARK: Inspector

struct AgentInspector: View {
    let session: Session
    @ObservedObject var board: Board

    var body: some View {
        let s = session
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                HStack(spacing: 12) {
                    Image(nsImage: ProjectIcon.badged(cwd: s.cwd, host: s.host, provider: s.provider, size: 48))
                        .interpolation(.high)
                        .frame(width: 48, height: 48)
                    VStack(alignment: .leading, spacing: 4) {
                        Text(s.name).font(Theme.display(15)).lineLimit(2)
                        StatusPill(session: s).fixedSize()
                    }
                }
                if (s.host ?? "").isEmpty {
                    Button { board.focus(s) } label: {
                        Label("Open \(opener)", systemImage: "arrow.up.forward.app")
                            .font(Theme.font(13, .semibold))
                            .foregroundStyle(.black.opacity(0.85))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 8)
                            .background(Capsule().fill(Theme.work))
                    }
                    .buttonStyle(.plain)
                    .keyboardShortcut(.return, modifiers: .command)
                }
                if let last = s.last, !last.isEmpty {
                    Block(title: "Doing") { Text(last.replacingOccurrences(of: "↳ ", with: "")).textSelection(.enabled) }
                }
                if let p = s.prompt, !p.isEmpty {
                    Block(title: "Asked") { Text("“\(p)”").italic().textSelection(.enabled) }
                }
                if let sa = s.subagents, sa.total > 0 {
                    Block(title: "Subagents") {
                        VStack(alignment: .leading, spacing: 6) {
                            Text(s.runningSubagents > 0 ? sa.label : "\(sa.total) started, none running")
                                .foregroundStyle(s.runningSubagents > 0 ? Color.teal : .secondary)
                            if s.runningSubagents > 0 {
                                ForEach(sa.active ?? [], id: \.self) { a in
                                    Label(a, systemImage: "arrow.turn.down.right")
                                        .font(Theme.font(12.5))
                                        .foregroundStyle(.secondary)
                                }
                            }
                        }
                    }
                }
                Block(title: "Activity") {
                    Timeline(history: board.history[s.key] ?? []).frame(height: 10)
                }
                Block(title: "Details") {
                    Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 10, verticalSpacing: 7) {
                        row("Folder", (s.cwd as NSString).abbreviatingWithTildeInPath, reveal: (s.host ?? "").isEmpty ? s.cwd : nil)
                        if let m = s.model, !m.isEmpty { row("Model", Model.display(m)) }
                        if let c = s.context_tokens, c > 0 { row("Context", "\(humanTokens(Int64(c))) tokens") }
                        row("Machine", s.host ?? "This Mac")
                        if let st = s.started_at { row("Started", st.formatted(.relative(presentation: .named))) }
                        if let pid = s.pid, pid > 0 { row("PID", "\(pid)") }
                        if !s.id.isEmpty { row("Session", String(s.id.prefix(8)), copy: s.id) }
                    }
                    .font(Theme.font(12.5))
                }
            }
            .padding(18)
        }
    }

    private var opener: String {
        session.extra?["entrypoint"] == "claude-desktop" ? "in Claude" : "its terminal"
    }

    @ViewBuilder
    private func row(_ k: String, _ v: String, reveal: String? = nil, copy: String? = nil) -> some View {
        GridRow {
            Text(k).foregroundStyle(.secondary)
            HStack(spacing: 4) {
                Text(v).lineLimit(2).textSelection(.enabled)
                if let reveal {
                    Button { NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: reveal) } label: {
                        Image(systemName: "arrow.right.circle.fill")
                    }
                    .buttonStyle(.plain).foregroundStyle(.secondary).help("Show in Finder")
                }
                if let copy {
                    Button {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(copy, forType: .string)
                    } label: { Image(systemName: "doc.on.doc") }
                        .buttonStyle(.plain).foregroundStyle(.secondary).help("Copy")
                }
            }
        }
    }
}

struct Block<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Eyebrow(text: title)
            content
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
