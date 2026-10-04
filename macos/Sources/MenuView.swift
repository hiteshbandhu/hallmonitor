import AppKit
import Combine
import ServiceManagement
import SwiftUI

/// The menu bar item: a real NSStatusItem with a real NSMenu, so it opens,
/// highlights, and handles left/right clicks like every other menu extra.
@MainActor
final class StatusMenu: NSObject, NSMenuDelegate {
    private let board: Board
    private let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let menu = NSMenu()
    private var bag: Set<AnyCancellable> = []
    private var isOpen = false
    var onSettings: (() -> Void)?
    var onOpen: ((Pane) -> Void)?

    init(board: Board) {
        self.board = board
        super.init()
        menu.delegate = self
        menu.autoenablesItems = false
        item.menu = menu
        item.button?.imagePosition = .imageLeading
        updateButton()
        board.objectWillChange
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                // objectWillChange fires before the change lands.
                DispatchQueue.main.async {
                    guard let self else { return }
                    // Rebuilding an open menu dismisses it: update the rows
                    // in place and leave structure and button for later.
                    if self.isOpen { self.refreshOpenMenu() } else { self.updateButton() }
                }
            }
            .store(in: &bag)
    }

    // MARK: Button

    func refresh() { updateButton() }

    private func updateButton() {
        guard let button = item.button else { return }
        let waiting = board.needsYou.count
        let busy = board.working.count
        button.image = StatusMenu.glyph(badge: waiting > 0)
        // Like native extras: dimmed when there's nothing going on.
        button.appearsDisabled = waiting + busy == 0
        let count = waiting > 0 ? waiting : busy
        var parts: [String] = []
        if UserDefaults.standard.bool(forKey: "showCount") && count > 0 { parts.append("\(count)") }
        // The limit you're closest to, Claude first: what people actually watch.
        if UserDefaults.standard.bool(forKey: "showLimit"), let l = tightestLimit {
            // "~" marks a reading nothing has refreshed lately.
            parts.append("\(l.isStale ? "~" : "")\(Int(l.used_percent.rounded()))%")
        }
        button.title = parts.isEmpty ? "" : " " + parts.joined(separator: " · ")
        button.toolTip = summary
        button.setAccessibilityLabel("Hall Monitor: \(summary)")
    }

    /// Three bold rounded bars, drawn as a template image so the menu bar
    /// tints it like every other extra; a dot marks "needs you".
    static func glyph(badge: Bool) -> NSImage {
        let img = NSImage(size: NSSize(width: 18, height: 16), flipped: false) { _ in
            NSColor.black.setFill()
            let barW: CGFloat = 3.4, gap: CGFloat = 1.9, base: CGFloat = 2
            let heights: [CGFloat] = [7, 12, 9]
            for (i, h) in heights.enumerated() {
                let x = 1.5 + CGFloat(i) * (barW + gap)
                NSBezierPath(roundedRect: NSRect(x: x, y: base, width: barW, height: h), xRadius: barW / 2, yRadius: barW / 2).fill()
            }
            if badge {
                let dot = NSRect(x: 12.2, y: 9.2, width: 5.6, height: 5.6)
                // Knock out a ring so the dot reads as separate from the bars.
                NSGraphicsContext.current?.compositingOperation = .clear
                NSBezierPath(ovalIn: dot.insetBy(dx: -1.4, dy: -1.4)).fill()
                NSGraphicsContext.current?.compositingOperation = .sourceOver
                NSBezierPath(ovalIn: dot).fill()
            }
            return true
        }
        img.isTemplate = true
        return img
    }

    private var tightestLimit: RateLimit? {
        let all = board.limits.map(\.value)
        let claude = all.filter { $0.provider == "claude" }
        return (claude.isEmpty ? all : claude).max { $0.used_percent < $1.used_percent }
    }

    private var summary: String {
        var parts: [String] = []
        if !board.needsYou.isEmpty { parts.append("\(board.needsYou.count) need\(board.needsYou.count == 1 ? "s" : "") you") }
        if !board.working.isEmpty { parts.append("\(board.working.count) working") }
        if !board.idle.isEmpty { parts.append("\(board.idle.count) idle") }
        return parts.isEmpty ? "No agents running" : parts.joined(separator: ", ")
    }

    // MARK: Menu

    func menuWillOpen(_ menu: NSMenu) {
        isOpen = true
        rebuild()
    }

    func menuDidClose(_ menu: NSMenu) {
        isOpen = false
        updateButton()
    }

    /// Updates durations and details of the agent rows already on screen.
    private func refreshOpenMenu() {
        let byKey = Dictionary(board.sessions.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        for item in menu.items {
            guard let key = item.identifier?.rawValue, let s = byKey[key] else { continue }
            item.title = s.name
            item.setSubtitle(detail(s))
            if let since = s.status_since {
                item.badge = NSMenuItemBadge(string: s.needsYou ? "needs you" : shortDuration(Date().timeIntervalSince(since)))
            }
        }
    }

    private func rebuild() {
        menu.removeAllItems()

        if let problem = board.problem, !board.connected {
            let i = NSMenuItem(title: "Not connected", action: nil, keyEquivalent: "")
            i.isEnabled = false
            i.setSubtitle(problem)
            menu.addItem(i)
        } else if board.sessions.allSatisfy(\.stale) {
            let i = NSMenuItem(title: "No agents running", action: nil, keyEquivalent: "")
            i.isEnabled = false
            i.setSubtitle("Start Claude Code or Codex and it shows up here")
            menu.addItem(i)
        } else {
            section("Needs You", board.needsYou)
            section("Working", board.working)
            section("Idle", Array(board.idle.prefix(8)))
        }

        usage()

        menu.addItem(.separator())
        menu.addItem(action("Open Hall Monitor", "rectangle.grid.2x2", key: "o", #selector(openBoard)))
        menu.addItem(action("Usage", "chart.bar.xaxis", key: "u", #selector(openUsage)))
        menu.addItem(action("Board in Terminal", "terminal", key: "t", #selector(openTerminal)))
        menu.addItem(.separator())
        menu.addItem(action("Settings…", "gearshape", key: ",", #selector(openSettings)))
        menu.addItem(action("Quit Hall Monitor", nil, key: "q", #selector(quit)))
    }

    private func section(_ title: String, _ items: [Session]) {
        guard !items.isEmpty else { return }
        if menu.numberOfItems > 0 { menu.addItem(.separator()) }
        menu.addItem(.sectionHeader(title: title))
        for s in items { menu.addItem(agentItem(s)) }
    }

    private func agentItem(_ s: Session) -> NSMenuItem {
        let i = NSMenuItem(title: s.name, action: #selector(openBoard), keyEquivalent: "")
        i.target = self
        i.identifier = NSUserInterfaceItemIdentifier(s.key)
        i.image = ProjectIcon.badged(cwd: s.cwd, host: s.host, provider: s.provider, size: 22)
        i.setSubtitle(detail(s))
        if let since = s.status_since {
            let text = s.needsYou ? "needs you" : shortDuration(Date().timeIntervalSince(since))
            i.badge = NSMenuItemBadge(string: text)
        }
        i.submenu = details(s)
        return i
    }

    private func detail(_ s: Session) -> String {
        if s.working || s.needsYou, let last = s.last, !last.isEmpty {
            let doing = last.replacingOccurrences(of: "↳ ", with: "")
            let n = s.runningSubagents
            return n > 0 ? "\(n) subagent\(n == 1 ? "" : "s") · " + doing : doing
        }
        var parts = [s.project]
        if let h = s.host, !h.isEmpty { parts.append(h) }
        if let m = s.model, !m.isEmpty { parts.append(Model.display(m)) }
        return parts.joined(separator: " · ")
    }

    /// Everything we know about one agent, plus a couple of useful actions.
    private func details(_ s: Session) -> NSMenu {
        let m = NSMenu()
        m.autoenablesItems = false
        func info(_ label: String, _ value: String?) {
            guard let value, !value.isEmpty else { return }
            let i = NSMenuItem(title: value, action: nil, keyEquivalent: "")
            i.isEnabled = false
            i.setSubtitle(label)
            m.addItem(i)
        }
        if (s.host ?? "").isEmpty {
            let open = NSMenuItem(title: "Open", action: #selector(focusAgent(_:)), keyEquivalent: "")
            open.target = self
            open.representedObject = s
            open.image = NSImage(systemSymbolName: "arrow.up.forward.app", accessibilityDescription: nil)
            m.addItem(open)
            m.addItem(.separator())
        }
        m.addItem(.sectionHeader(title: "\(Provider.name(s.provider)) · \(statusWord(s.status))"))
        info("Doing", s.last?.replacingOccurrences(of: "↳ ", with: ""))
        info("Asked", s.prompt.map { "“\($0)”" })
        info("Folder", (s.cwd as NSString).abbreviatingWithTildeInPath)
        var model = s.model.map(Model.display) ?? ""
        if let c = s.context_tokens, c > 0 { model += model.isEmpty ? "\(c / 1000)k context" : " · \(c / 1000)k context" }
        info("Model", model)
        if let sa = s.subagents, sa.total > 0 {
            info("Subagents", s.runningSubagents > 0 ? sa.label : "\(sa.total) started, none running")
            if s.runningSubagents > 0 {
                for a in sa.active ?? [] { info("Running", a) }
            }
        }
        info("Machine", s.host)
        if let started = s.started_at {
            info("Started", started.formatted(.relative(presentation: .named)))
        }
        m.addItem(.separator())
        let copy = NSMenuItem(title: "Copy Session ID", action: #selector(copyID(_:)), keyEquivalent: "")
        copy.target = self
        copy.representedObject = s.id
        copy.isEnabled = !s.id.isEmpty
        m.addItem(copy)
        if s.host?.isEmpty ?? true {
            let reveal = NSMenuItem(title: "Show Folder in Finder", action: #selector(reveal(_:)), keyEquivalent: "")
            reveal.target = self
            reveal.representedObject = s.cwd
            m.addItem(reveal)
        }
        return m
    }

    private func usage() {
        guard board.today != nil || !board.limits.isEmpty else { return }
        menu.addItem(.separator())
        menu.addItem(.sectionHeader(title: "Today"))
        if let t = board.today {
            let i = NSMenuItem(title: "\(hoursText(t.active_s ?? 0)) of agent time", action: #selector(openUsage), keyEquivalent: "")
            i.target = self
            i.image = NSImage(systemSymbolName: "clock", accessibilityDescription: nil)
                        i.setSubtitle("\(humanTokens(t.tokens?.total ?? 0)) tokens · \(t.prompts ?? 0) prompts · \(t.tools ?? 0) tool calls")
            menu.addItem(i)
        }
        for (_, l) in board.limits {
            let i = NSMenuItem(title: "\(Provider.name(l.provider)) \(windowName(l)) limit", action: nil, keyEquivalent: "")
            i.isEnabled = true
            i.image = gaugeImage(percent: l.used_percent, provider: l.provider)
            i.badge = NSMenuItemBadge(string: "\(Int(l.used_percent.rounded()))%")
            var notes: [String] = []
            if let r = l.resets_at {
                notes.append("Resets " + r.formatted(.dateTime.weekday(.abbreviated).hour().minute()))
            }
            if l.isStale, let at = l.observed_at {
                notes.append("as of \(shortDuration(Date().timeIntervalSince(at))) ago")
            }
            if !notes.isEmpty { i.setSubtitle(notes.joined(separator: " · ")) }
            menu.addItem(i)
        }
    }

    private func windowName(_ l: RateLimit) -> String {
        switch l.windowLabel {
        case "5h": return "5-hour"
        case "7d": return "weekly"
        case "30d": return "monthly"
        default: return l.windowLabel
        }
    }

    private func action(_ title: String, _ symbol: String?, key: String, _ sel: Selector) -> NSMenuItem {
        let i = NSMenuItem(title: title, action: sel, keyEquivalent: key)
        i.target = self
        if let symbol { i.image = NSImage(systemSymbolName: symbol, accessibilityDescription: nil) }
        return i
    }

    // MARK: Actions

    @objc private func openBoard() { onOpen?(.agents) }
    @objc private func openUsage() { onOpen?(.usage) }
    @objc private func openTerminal() { Launcher.openBoard() }

    @objc private func openSettings() { onSettings?() }

    @objc private func quit() { NSApp.terminate(nil) }

    @objc private func focusAgent(_ sender: NSMenuItem) {
        if let s = sender.representedObject as? Session { board.focus(s) }
    }

    @objc private func copyID(_ sender: NSMenuItem) {
        guard let id = sender.representedObject as? String else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(id, forType: .string)
    }

    @objc private func reveal(_ sender: NSMenuItem) {
        guard let path = sender.representedObject as? String else { return }
        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
    }

    // MARK: Images

    private func icon(for provider: String, size: CGFloat) -> NSImage? {
        if let app = Provider.icon(provider) {
            let img = NSImage(size: NSSize(width: size, height: size), flipped: false) { r in
                app.draw(in: r)
                return true
            }
            return img
        }
        let symbol = provider == "claude" ? "asterisk.circle.fill" : "terminal.fill"
        let cfg = NSImage.SymbolConfiguration(paletteColors: [Provider.color(provider)])
        return NSImage(systemSymbolName: symbol, accessibilityDescription: nil)?.withSymbolConfiguration(cfg)
    }

    /// A small ring gauge, drawn the way macOS draws capacity indicators.
    private func gaugeImage(percent: Double, provider: String) -> NSImage {
        let size = NSSize(width: 22, height: 22)
        let icon = Provider.icon(provider)
        return NSImage(size: size, flipped: false) { r in
            // The provider's real app icon inside the usage ring.
            icon?.draw(in: r.insetBy(dx: 5, dy: 5))
            let rect = r.insetBy(dx: 1.5, dy: 1.5)
            let track = NSBezierPath(ovalIn: rect)
            track.lineWidth = 2.5
            NSColor.tertiaryLabelColor.setStroke()
            track.stroke()
            let p = min(max(percent, 0), 100) / 100
            guard p > 0 else { return true }
            let arc = NSBezierPath()
            let c = NSPoint(x: r.midX, y: r.midY)
            arc.appendArc(withCenter: c, radius: rect.width / 2, startAngle: 90, endAngle: 90 - 360 * p, clockwise: true)
            arc.lineWidth = 2.5
            arc.lineCapStyle = .round
            (percent >= 80 ? NSColor.systemRed : percent >= 50 ? NSColor.systemOrange : NSColor.systemGreen).setStroke()
            arc.stroke()
            return true
        }
    }
}

func statusWord(_ s: String) -> String {
    switch s {
    case "busy": return "Working"
    case "waiting": return "Needs you"
    case "error": return "Error"
    case "blocked": return "Blocked"
    case "idle": return "Idle"
    default: return s.capitalized
    }
}

// MARK: - Settings window

struct SettingsView: View {
    @AppStorage("notch") private var notch = true
    @AppStorage("notify") private var notify = true
    @AppStorage("showCount") private var showCount = false
    @AppStorage("showLimit") private var showLimit = true
    @AppStorage("probeLimits") private var probeLimits = true
    @AppStorage("answerFromNotch") private var answerFromNotch = false
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        Form {
            Section {
                Toggle("Show agents around the notch", isOn: $notch)
                Toggle("Notify when an agent needs you or finishes", isOn: $notify)
                Toggle("Show agent count in the menu bar", isOn: $showCount)
                Toggle("Show plan usage in the menu bar", isOn: $showLimit)
            }
            Section {
                Toggle("Answer Claude’s questions from the notch", isOn: $answerFromNotch)
                    .onChange(of: answerFromNotch) { _, on in
                        (NSApp.delegate as? AppDelegate)?.board.setAnswerHooks(on)
                    }
            } footer: {
                Text("When Claude Code asks you something or wants permission, answer it in the notch. Adds a hook to ~/.claude/settings.json (backed up first); if you don’t answer within two minutes, Claude asks you the usual way.")
                    .font(.footnote).foregroundStyle(.secondary)
            }
            Section {
                Toggle("Check Claude plan limits every 15 minutes", isOn: $probeLimits)
                    .onChange(of: probeLimits) { _, _ in (NSApp.delegate as? AppDelegate)?.board.restart() }
            } footer: {
                Text("Opens Claude Code's /usage in the background, so limits stay current even for sessions in the Claude app. Nothing is sent to the model, and Hall Monitor never sees your login.")
                    .font(.footnote).foregroundStyle(.secondary)
            }
            Section {
                Toggle("Launch at login", isOn: $launchAtLogin)
                    .onChange(of: launchAtLogin) { _, on in
                        do {
                            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
                        } catch {
                            launchAtLogin = SMAppService.mainApp.status == .enabled
                        }
                    }
            }
            Section {
                LabeledContent("hallmonitor CLI") {
                    Text(Board.binary).textSelection(.enabled).foregroundStyle(.secondary)
                }
            }
        }
        .formStyle(.grouped)
        .frame(width: 460)
        .fixedSize()
    }
}

// MARK: - Shared bits

struct AppIcon: View {
    let provider: String
    let size: CGFloat

    var body: some View {
        if let img = Provider.icon(provider) {
            Image(nsImage: img)
                .resizable()
                .interpolation(.high)
                .frame(width: size, height: size)
        } else {
            Image(systemName: provider == "claude" ? "asterisk.circle.fill" : "terminal.fill")
                .resizable()
                .scaledToFit()
                .foregroundStyle(Color(nsColor: Provider.color(provider)))
                .frame(width: size, height: size)
        }
    }
}

enum Model {
    /// "claude-opus-5-5" → "Opus 5.5"
    static func display(_ m: String) -> String {
        guard m.hasPrefix("claude-") else { return m }
        let parts = m.dropFirst("claude-".count).split(separator: "-")
        guard let family = parts.first else { return m }
        let version = parts.dropFirst().joined(separator: ".")
        return family.capitalized + (version.isEmpty ? "" : " " + version)
    }
}

@MainActor
enum Launcher {
    /// Opens the terminal board in iTerm2 if it's installed, else Terminal.
    static func openBoard(view: String = "agents") {
        let cmd = "\(Board.binary) --view \(view)"
        let script: String
        if FileManager.default.fileExists(atPath: "/Applications/iTerm.app") {
            script = """
            tell application "iTerm"
                activate
                create window with default profile command "\(cmd)"
            end tell
            """
        } else {
            script = """
            tell application "Terminal"
                activate
                do script "\(cmd)"
            end tell
            """
        }
        var err: NSDictionary?
        NSAppleScript(source: script)?.executeAndReturnError(&err)
    }
}

extension NSMenuItem {
    /// A gray second line under the title, the way menus showed detail
    /// before NSMenuItem.subtitle.
    func setSubtitle(_ text: String) {
        let t = NSMutableAttributedString(string: title, attributes: [
            .font: NSFont.menuFont(ofSize: 0),
        ])
        t.append(NSAttributedString(string: "\n" + text, attributes: [
            .font: NSFont.menuFont(ofSize: NSFont.smallSystemFontSize),
            .foregroundColor: NSColor.secondaryLabelColor,
        ]))
        attributedTitle = t
    }
}
