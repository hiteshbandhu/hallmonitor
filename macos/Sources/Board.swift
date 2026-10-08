import AppKit
import Foundation

// MARK: - Snapshot types (mirror internal/model and internal/usage)

struct Session: Decodable, Hashable {
    var host: String?
    var provider: String
    var id: String
    var pid: Int?
    var title: String
    var cwd: String
    var status: String
    var kind: String?
    var model: String?
    var last: String?
    var prompt: String?
    var status_since: Date?
    var updated_at: Date?
    var started_at: Date?
    var context_tokens: Int?
    var subagents: Subagents?
    var extra: [String: String]?
    var ask: PendingAsk?

    /// Subagents running right now; only counts while the session works.
    var runningSubagents: Int { working || needsYou ? subagents?.running ?? 0 : 0 }

    var key: String { "\(host ?? "")|\(provider)|\(id.isEmpty ? "pid:\(pid ?? 0)" : id)" }
    var project: String { (cwd as NSString).lastPathComponent }
    var name: String { title.isEmpty ? project : title }
    var lastSeen: Date? { updated_at ?? started_at }

    var needsYou: Bool { status == "waiting" || status == "error" }
    var working: Bool { status == "busy" }
    /// Parked and quiet for a day: hidden, like the terminal board does.
    var stale: Bool {
        guard !needsYou, !working, let seen = lastSeen else { return false }
        return Date().timeIntervalSince(seen) > 24 * 3600
    }
}

struct Subagents: Decodable, Hashable {
    var running: Int
    var total: Int
    var active: [String]?

    var label: String {
        let word = total == 1 ? "subagent" : "subagents"
        return running > 0 ? "\(running) of \(total) \(word) running" : "\(total) \(word)"
    }
}

struct FleetSample: Hashable {
    var at: Date
    var working: Int
    var needsYou: Int
}

struct Machine: Identifiable, Hashable {
    var name: String
    var host: String?          // ssh destination; nil for this Mac
    var error: String?
    var agents: [Session]
    var id: String { host ?? "local" }
    var live: Bool { error == nil }

    /// "dev@gpu-box" -> "gpu-box", as the board labels hosts.
    static func label(_ dest: String) -> String {
        if let at = dest.lastIndex(of: "@") { return String(dest[dest.index(after: at)...]) }
        return dest
    }
}

/// ~/.config/hallmonitor/hosts: one ssh destination per line.
enum Hosts {
    static var path: String {
        let env = ProcessInfo.processInfo.environment["XDG_CONFIG_HOME"] ?? ""
        let base = env.isEmpty ? NSHomeDirectory() + "/.config" : env
        return base + "/hallmonitor/hosts"
    }

    static func read() -> [String] {
        guard let s = try? String(contentsOfFile: path, encoding: .utf8) else { return [] }
        return s.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty && !$0.hasPrefix("#") }
    }

    static func write(_ hosts: [String]) {
        try? FileManager.default.createDirectory(atPath: (path as NSString).deletingLastPathComponent, withIntermediateDirectories: true)
        try? (hosts.joined(separator: "\n") + "\n").write(toFile: path, atomically: true, encoding: .utf8)
    }
}

struct AdapterError: Decodable, Hashable {
    var provider: String
    var error: String
}

struct Snapshot: Decodable {
    var generated_at: Date
    var sessions: [Session]
    var adapter_errors: [AdapterError]?
}

struct Tokens: Decodable {
    var `in`: Int64?
    var cache_read: Int64?
    var cache_write: Int64?
    var out: Int64?
    var total: Int64 { (`in` ?? 0) + (cache_read ?? 0) + (cache_write ?? 0) + (out ?? 0) }
}

struct Counters: Decodable {
    var tokens: Tokens?
    var prompts: Int64?
    var tools: Int64?
    var active_s: Double?
}

struct RateLimit: Decodable {
    var provider: String
    var used_percent: Double
    var window_minutes: Int?
    var resets_at: Date?
    var observed_at: Date?

    /// Nothing has reported this limit for a while, so it's only approximate.
    var isStale: Bool {
        guard let at = observed_at else { return false }
        return Date().timeIntervalSince(at) > 15 * 60
    }

    var windowLabel: String {
        guard let m = window_minutes, m > 0 else { return "" }
        if m % (60 * 24) == 0 { return "\(m / (60 * 24))d" }
        if m % 60 == 0 { return "\(m / 60)h" }
        return "\(m)m"
    }
}

struct UsageSummary: Decodable {
    var total: Counters
    var rate_limits: [String: RateLimit]?
}

// MARK: - Board: runs `hallmonitor --stream` and publishes what it says

@MainActor
final class Board: ObservableObject {
    @Published private(set) var sessions: [Session] = []
    @Published private(set) var errors: [AdapterError] = []
    @Published private(set) var connected = false
    @Published private(set) var problem: String?
    @Published private(set) var today: Counters?
    @Published private(set) var limits: [(key: String, value: RateLimit)] = []
    /// Each session's status, sampled per snapshot (every 2 s), oldest
    /// first, for the timelines in the window.
    @Published private(set) var history: [String: [UInt8]] = [:]
    /// How many agents were working / needed you, per snapshot.
    @Published private(set) var fleet: [FleetSample] = []
    static let historyLength = 150 // 5 minutes

    /// Called for each status change worth telling the user about.
    var onEvent: ((BoardEvent) -> Void)?

    private var process: Process?
    private var buffer = Data()
    private var previous: [String: Session] = [:]
    private var seededOnce = false
    private var usageTimer: Timer?

    var needsYou: [Session] { sessions.filter(\.needsYou) }
    var working: [Session] { sessions.filter(\.working) }
    var idle: [Session] { sessions.filter { !$0.needsYou && !$0.working && !$0.stale } }

    func start() {
        launch()
        refreshUsage()
        usageTimer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            guard let self else { return }
            Task { @MainActor in self.refreshUsage() }
        }
        // Plan limits show up the moment Claude Code reports them, not a
        // minute later: watch the file `hallmonitor statusline` writes.
        limitsSeen = Board.limitsChanged()
        limitsTimer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] _ in
            guard let self else { return }
            Task { @MainActor in
                let at = Board.limitsChanged()
                if at != self.limitsSeen {
                    self.limitsSeen = at
                    self.refreshUsage()
                }
            }
        }
    }

    private var limitsTimer: Timer?
    private var limitsSeen: Date?

    nonisolated private static func limitsChanged() -> Date? {
        let env = ProcessInfo.processInfo.environment["XDG_DATA_HOME"] ?? ""
        let data = env.isEmpty ? NSHomeDirectory() + "/.local/share" : env
        let attrs = try? FileManager.default.attributesOfItem(atPath: data + "/hallmonitor/usage/claude-limits.json")
        return attrs?[.modificationDate] as? Date
    }

    /// Brings the app the agent runs in to the front: the Claude app on that
    /// session, or its terminal or editor.
    func focus(_ s: Session) {
        // Every field is quoted: sessions can come from other machines.
        var args = "focus --provider \(Board.quote(s.provider)) --pid \(s.pid ?? 0)"
        if !s.id.isEmpty { args += " --id \(Board.quote(s.id))" }
        if let e = s.extra?["entrypoint"], !e.isEmpty { args += " --entrypoint \(Board.quote(e))" }
        if let h = s.host, !h.isEmpty { args += " --host \(Board.quote(h))" }
        let p = shellCommand(args)
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        try? p.run()
    }

    nonisolated static func quote(_ s: String) -> String { "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'" }

    /// Adds or removes the Claude Code hooks that send questions to the notch.
    func setAnswerHooks(_ on: Bool) {
        let p = shellCommand(on ? "hook --install" : "hook --uninstall")
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        try? p.run()
    }

    /// Starts the helper over, e.g. after a setting it reads changed.
    func restart() { process?.terminate() }

    func stop() {
        process?.terminate()
        usageTimer?.invalidate()
        limitsTimer?.invalidate()
    }

    // Run through the user's login shell so PATH has claude, codex and
    // hallmonitor even though Finder launched us with a bare environment.
    func shellCommand(_ args: String, env: String = "") -> Process {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: ProcessInfo.processInfo.environment["SHELL"] ?? "/bin/zsh")
        p.arguments = ["-lc", "\(env)exec \(Board.binary) \(args)"]
        return p
    }

    /// The hallmonitor CLI: bundled next to us, or on PATH.
    nonisolated static var binary: String {
        if let env = ProcessInfo.processInfo.environment["HALLMONITOR_BIN"] { return env }
        let helper = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/hallmonitor").path
        if FileManager.default.isExecutableFile(atPath: helper) { return helper }
        return "hallmonitor"
    }

    private func launch() {
        // The helper reads Claude's /usage every 15 minutes unless turned off.
        let probe = UserDefaults.standard.bool(forKey: "probeLimits") ? "" : "HALLMONITOR_NO_PROBE=1 "
        let p = shellCommand("--stream --with-hosts --watch 2s", env: probe)
        let out = Pipe()
        let err = Pipe()
        p.standardOutput = out
        p.standardError = err
        out.fileHandleForReading.readabilityHandler = { [weak self] h in
            let data = h.availableData
            guard let self else { return }
            Task { @MainActor in self.consume(data) }
        }
        p.terminationHandler = { [weak self] proc in
            let msg = String(data: err.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines)
            let status = proc.terminationStatus
            guard let self else { return }
            Task { @MainActor in
                self.connected = false
                self.problem = (msg?.isEmpty == false) ? msg : "hallmonitor exited (\(status))"
                if status == 127 {
                    self.problem = "Can't find the hallmonitor command. Install it, or set HALLMONITOR_BIN."
                }
                DispatchQueue.main.asyncAfter(deadline: .now() + 3) { self.launch() }
            }
        }
        do {
            try p.run()
            process = p
        } catch {
            problem = error.localizedDescription
        }
    }

    private func consume(_ data: Data) {
        buffer.append(data)
        while let nl = buffer.firstIndex(of: 0x0A) {
            let line = buffer.subdata(in: buffer.startIndex..<nl)
            buffer.removeSubrange(buffer.startIndex...nl)
            guard !line.isEmpty, let snap = try? Board.decoder.decode(Snapshot.self, from: line) else { continue }
            apply(snap)
        }
    }

    private func apply(_ snap: Snapshot) {
        connected = true
        problem = nil
        errors = snap.adapter_errors ?? []
        let order: (Session) -> Int = { $0.needsYou ? 0 : $0.working ? 1 : 2 }
        sessions = snap.sessions.sorted {
            if order($0) != order($1) { return order($0) < order($1) }
            return ($0.lastSeen ?? .distantPast) > ($1.lastSeen ?? .distantPast)
        }

        // Transitions, skipping the very first snapshot so launching the app
        // doesn't fire a notification for everything already running.
        var next: [String: Session] = [:]
        for s in snap.sessions {
            next[s.key] = s
            guard seededOnce, let old = previous[s.key], old.status != s.status else { continue }
            if s.needsYou && !old.needsYou {
                onEvent?(.needsYou(s))
            } else if old.working && s.status == "idle" {
                let busyFor = old.status_since.map { Date().timeIntervalSince($0) } ?? 0
                onEvent?(.finished(s, after: busyFor))
            } else if s.working && !old.working {
                onEvent?(.started(s))
            }
        }
        previous = next
        seededOnce = true
        record(snap.sessions)
        let publisher = objectWillChange
        ProjectIcon.prefetch(snap.sessions) {
            DispatchQueue.main.async { publisher.send() }
        }
    }

    private func record(_ list: [Session]) {
        var h = history
        var seen = Set<String>()
        for s in list {
            seen.insert(s.key)
            var row = h[s.key] ?? []
            row.append(s.needsYou ? 2 : s.working ? 1 : 0)
            if row.count > Board.historyLength { row.removeFirst(row.count - Board.historyLength) }
            h[s.key] = row
        }
        for k in h.keys where !seen.contains(k) { h[k] = nil }
        history = h
        var f = fleet
        f.append(FleetSample(at: Date(), working: list.filter(\.working).count, needsYou: list.filter(\.needsYou).count))
        if f.count > Board.historyLength { f.removeFirst(f.count - Board.historyLength) }
        fleet = f
    }

    /// Machines: this Mac, plus every host in the hosts file or the feed.
    var machines: [Machine] {
        var out = [Machine(name: "This Mac", host: nil, error: nil,
                           agents: sessions.filter { ($0.host ?? "").isEmpty && !$0.stale })]
        var names = Hosts.read()
        for s in sessions { if let h = s.host, !h.isEmpty, !names.contains(where: { Machine.label($0) == h }) { names.append(h) } }
        for n in names {
            let label = Machine.label(n)
            let err = errors.first { $0.provider == "host:" + label }?.error
            out.append(Machine(name: label, host: n, error: err, agents: sessions.filter { $0.host == label && !$0.stale }))
        }
        return out
    }

    func refreshUsage() {
        let p = shellCommand("usage --days 1 --json")
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        p.terminationHandler = { [weak self] _ in
            let data = out.fileHandleForReading.readDataToEndOfFile()
            let summary = try? Board.decoder.decode(UsageSummary.self, from: data)
            guard let self else { return }
            Task { @MainActor in
                self.today = summary?.total
                self.limits = (summary?.rate_limits ?? [:]).sorted { $0.key < $1.key }
            }
        }
        try? p.run()
    }

    nonisolated(unsafe) static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        let frac = ISO8601DateFormatter()
        frac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            // Go writes nanoseconds; ISO8601DateFormatter wants at most millis.
            let trimmed = s.replacingOccurrences(of: #"(\.\d{3})\d+"#, with: "$1", options: .regularExpression)
            if let d = frac.date(from: trimmed) ?? plain.date(from: trimmed) { return d }
            throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: "bad date \(s)"))
        }
        return d
    }()
}

enum BoardEvent {
    case needsYou(Session)
    case finished(Session, after: TimeInterval)
    case started(Session)

    var session: Session {
        switch self {
        case .needsYou(let s), .finished(let s, _), .started(let s): return s
        }
    }
}

// MARK: - Helpers shared by the views

enum Provider {
    static func name(_ p: String) -> String {
        switch p {
        case "claude": return "Claude"
        case "codex": return "Codex"
        case "opencode": return "opencode"
        default: return p.capitalized
        }
    }

    static func color(_ p: String) -> NSColor {
        switch p {
        case "claude": return NSColor(red: 0.85, green: 0.47, blue: 0.34, alpha: 1)
        case "codex": return NSColor(red: 0.49, green: 0.83, blue: 0.99, alpha: 1)
        // opencode's mark is monochrome: warm stone, dark on light and light on dark.
        case "opencode": return opencodeColor
        default: return .secondaryLabelColor
        }
    }

    /// The vendor app's own icon if it's installed; for opencode, its block
    /// mark drawn on a tile; nil otherwise.
    static func icon(_ p: String) -> NSImage? {
        if let cached = iconCache[p] { return cached }
        let candidates: [String]
        switch p {
        case "claude": candidates = ["Claude.app"]
        case "codex": candidates = ["Codex.app", "ChatGPT.app"]
        case "opencode": candidates = ["OpenCode.app"]
        default: candidates = []
        }
        let roots = ["/Applications", NSHomeDirectory() + "/Applications"]
        for app in candidates {
            for root in roots {
                let path = root + "/" + app
                if FileManager.default.fileExists(atPath: path) {
                    let img = NSWorkspace.shared.icon(forFile: path)
                    iconCache[p] = img
                    return img
                }
            }
        }
        if p == "opencode" {
            let img = opencodeTile()
            iconCache[p] = img
            return img
        }
        return nil
    }

    /// opencode's hollow block on a dark stone tile, like the terminal board.
    private static func opencodeTile() -> NSImage {
        NSImage(size: NSSize(width: 64, height: 64), flipped: false) { r in
            NSColor(red: 0.16, green: 0.15, blue: 0.14, alpha: 1).setFill()
            NSBezierPath(roundedRect: r, xRadius: r.width * 0.225, yRadius: r.height * 0.225).fill()
            let u = r.width / 24 // the mark is drawn on a 24-unit grid
            let ring = NSBezierPath(rect: NSRect(x: 7 * u, y: 5 * u, width: 10 * u, height: 14 * u))
            ring.append(NSBezierPath(rect: NSRect(x: 10 * u, y: 9 * u, width: 4 * u, height: 6 * u)).reversed)
            NSColor(red: 0.91, green: 0.90, blue: 0.89, alpha: 1).setFill()
            ring.fill()
            return true
        }
    }

    private static var iconCache: [String: NSImage] = [:]

    private static let opencodeColor = NSColor(name: nil) { a in
        a.bestMatch(from: [.darkAqua, .vibrantDark]) != nil
            ? NSColor(red: 0.84, green: 0.83, blue: 0.82, alpha: 1)
            : NSColor(red: 0.34, green: 0.33, blue: 0.31, alpha: 1)
    }
}

func shortDuration(_ t: TimeInterval) -> String {
    let s = max(0, Int(t))
    if s < 60 { return "\(s)s" }
    if s < 3600 { return "\(s / 60)m" }
    if s < 48 * 3600 {
        let h = s / 3600, m = (s % 3600) / 60
        return h < 10 && m > 0 ? "\(h)h \(m)m" : "\(h)h"
    }
    return "\(s / 86400)d"
}

func humanTokens(_ n: Int64) -> String {
    switch n {
    case 1_000_000_000...: return String(format: "%.1fB", Double(n) / 1e9)
    case 1_000_000...: return String(format: "%.1fM", Double(n) / 1e6)
    case 1_000...: return String(format: "%.1fk", Double(n) / 1e3)
    default: return "\(n)"
    }
}

func hoursText(_ s: Double) -> String {
    let h = s / 3600
    return h >= 100 ? String(format: "%.0fh", h) : String(format: "%.1fh", h)
}

// MARK: - Project icons

enum ProjectIcon {
    private static var cache: [String: NSImage] = [:]

    /// Common places a project keeps its own logo, best first.
    private static let candidates = [
        "icon.png", "logo.png", "favicon.png", "favicon.ico", "icon.icns", "logo.svg", "icon.svg", "favicon.svg",
        "public/favicon.png", "public/favicon.ico", "public/icon.png", "public/logo.png", "public/favicon.svg", "public/logo.svg",
        "assets/icon.png", "assets/logo.png", "static/favicon.png", "static/favicon.ico", "app/icon.png", "app/favicon.ico",
        "src/app/icon.png", "src/app/favicon.ico", ".github/logo.png", "docs/logo.png",
    ]

    /// The project's own logo, or a custom Finder icon set on its folder;
    /// nil when there's nothing more specific than a plain folder.
    static func image(cwd: String, host: String?) -> NSImage? {
        guard (host ?? "").isEmpty else { return nil }
        let key = "|" + cwd
        if let hit = cache[key] { return hit }
        guard searchable(cwd) else { return nil }
        var img = logo(in: cwd)
        if img == nil, hasCustomFolderIcon(cwd) {
            img = NSWorkspace.shared.icon(forFile: cwd)
        }
        if let img { cache[key] = img }
        return img
    }

    /// Finder stores a custom folder icon in a hidden "Icon\r" file.
    private static func hasCustomFolderIcon(_ dir: String) -> Bool {
        FileManager.default.fileExists(atPath: (dir as NSString).appendingPathComponent("Icon\r"))
    }

    private static var searched: Set<String> = []
    private static let searchQueue = DispatchQueue(label: "hallmonitor.project-icons", qos: .utility)

    /// Looks deeper (monorepos keep logos in apps/web/public) off the main
    /// thread, once per project; calls back when something better turned up.
    static func prefetch(_ sessions: [Session], found: @escaping @Sendable () -> Void) {
        for s in sessions where (s.host ?? "").isEmpty {
            let root = repoRoot(s.cwd)
            guard !searched.contains(root) else { continue }
            searched.insert(root)
            guard searchable(root), isProject(root) else { continue }
            let key = "|" + s.cwd
            searchQueue.async {
                guard let path = deepLogo(in: root), let img = NSImage(contentsOfFile: path), img.isValid else { return }
                DispatchQueue.main.async {
                    cache[key] = img
                    found()
                }
            }
        }
    }

    private static func repoRoot(_ dir: String) -> String {
        var root = dir
        for marker in ["/.claude/worktrees/", "/.kandy/worktrees/", "/.worktrees/"] {
            if let r = root.range(of: marker) { root = String(root[..<r.lowerBound]) }
        }
        return root
    }

    /// Folders in the home directory that macOS guards or that hold other
    /// apps' data. Reading them is what makes macOS ask for permission, and an
    /// icon is never worth a prompt.
    private static let guarded: Set<String> = ["Library", "Desktop", "Documents", "Downloads", "Music", "Pictures", "Movies", "Public", "Applications", ".Trash"]

    /// True when `dir` is somewhere we may look for an icon: inside the home
    /// folder, but not the home folder itself (an agent started in ~ is not a
    /// project) and not inside a guarded folder. Other volumes are out too.
    static func searchable(_ dir: String) -> Bool {
        let home = URL(fileURLWithPath: NSHomeDirectory()).resolvingSymlinksInPath().pathComponents
        let path = URL(fileURLWithPath: dir).resolvingSymlinksInPath().pathComponents
        guard path.count > home.count, Array(path.prefix(home.count)) == home else { return false }
        return !guarded.contains(path[home.count])
    }

    private static let markers = [".git", "package.json", "go.mod", "Cargo.toml", "pyproject.toml", "Package.swift", "Gemfile", "pom.xml", "build.gradle", "build.gradle.kts", "composer.json", "pubspec.yaml", "mix.exs", "deno.json", "Makefile", "CMakeLists.txt"]

    /// Only folders that look like a project get the deep search, so a folder
    /// of projects (or of anything else) is never walked.
    private static func isProject(_ dir: String) -> Bool {
        markers.contains { FileManager.default.fileExists(atPath: (dir as NSString).appendingPathComponent($0)) }
    }

    private static let skip: Set<String> = ["node_modules", ".git", ".next", "dist", "build", "out", "target", "vendor", ".venv", "venv", "Pods", "DerivedData", ".claude", ".kandy", "coverage"]

    private static func deepLogo(in root: String) -> String? {
        let fm = FileManager.default
        var best: (rank: Int, depth: Int, path: String)?
        func rank(_ name: String) -> Int? {
            let n = name.lowercased()
            guard n.hasPrefix("favicon") || n.hasPrefix("icon") || n.hasPrefix("logo") || n.hasPrefix("apple-touch-icon") else { return nil }
            if n.hasSuffix(".icns") { return 0 }
            if n.hasSuffix(".png") { return n.hasPrefix("apple-touch-icon") ? 1 : 2 }
            if n.hasSuffix(".svg") { return 3 }
            if n.hasSuffix(".ico") { return 4 }
            return nil
        }
        var budget = 2000  // directories; a project's logo is never that deep in
        func walk(_ dir: String, _ depth: Int) {
            guard depth <= 4, budget > 0, let items = try? fm.contentsOfDirectory(atPath: dir) else { return }
            budget -= 1
            for name in items {
                if name.hasPrefix(".") && name != ".github" { continue }
                let path = (dir as NSString).appendingPathComponent(name)
                // Never follow links: they can lead out of the project.
                guard let type = (try? fm.attributesOfItem(atPath: path))?[.type] as? FileAttributeType else { continue }
                if type == .typeDirectory {
                    if !skip.contains(name) { walk(path, depth + 1) }
                } else if type == .typeRegular, let r = rank(name) {
                    if best == nil || (r, depth) < (best!.rank, best!.depth) { best = (r, depth, path) }
                }
            }
        }
        walk(root, 0)
        return best?.path
    }

    private static func logo(in dir: String) -> NSImage? {
        // Worktrees share their repo's logo.
        var root = dir
        for marker in ["/.claude/worktrees/", "/.kandy/worktrees/", "/.worktrees/"] {
            if let r = root.range(of: marker) { root = String(root[..<r.lowerBound]) }
        }
        for rel in candidates {
            let path = (root as NSString).appendingPathComponent(rel)
            guard let attrs = try? FileManager.default.attributesOfItem(atPath: path),
                  let size = attrs[.size] as? Int, size > 0, size < 2_000_000,
                  let img = NSImage(contentsOfFile: path), img.isValid, img.size.width >= 16 else { continue }
            return img
        }
        return nil
    }

    /// The project icon with the provider's app icon badged in the corner,
    /// like a document icon badged with its app.
    /// Without a project icon, it's just the provider's app icon.
    static func badged(cwd: String, host: String?, provider: String, size: CGFloat) -> NSImage {
        let badge = Provider.icon(provider)
        guard let base = image(cwd: cwd, host: host) else {
            return NSImage(size: NSSize(width: size, height: size), flipped: false) { r in
                if let badge {
                    badge.draw(in: r)
                } else {
                    let cfg = NSImage.SymbolConfiguration(pointSize: size * 0.7, weight: .semibold)
                        .applying(.init(paletteColors: [Provider.color(provider)]))
                    NSImage(systemSymbolName: provider == "claude" ? "asterisk.circle.fill" : "terminal.fill",
                            accessibilityDescription: nil)?.withSymbolConfiguration(cfg)?.draw(in: r.insetBy(dx: size * 0.1, dy: size * 0.1))
                }
                return true
            }
        }
        return NSImage(size: NSSize(width: size, height: size), flipped: false) { r in
            base.draw(in: r.insetBy(dx: size * 0.04, dy: size * 0.04))
            if let badge {
                let b = size * 0.52
                let rect = NSRect(x: r.maxX - b + size * 0.06, y: r.minY - size * 0.06, width: b, height: b)
                // A thin ring so the badge separates from the icon under it.
                NSGraphicsContext.current?.compositingOperation = .clear
                NSBezierPath(roundedRect: rect.insetBy(dx: -1, dy: -1), xRadius: b * 0.28, yRadius: b * 0.28).fill()
                NSGraphicsContext.current?.compositingOperation = .sourceOver
                badge.draw(in: rect)
            }
            return true
        }
    }
}
