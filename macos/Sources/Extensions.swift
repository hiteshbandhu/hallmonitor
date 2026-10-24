import AppKit
import Combine
import CryptoKit
import Security
import SwiftUI

// MARK: - Extensions
//
// An extension is a separate program Hall Monitor starts in a sandbox. It
// may reach the internet over HTTPS, listen on the local ports its manifest
// names, and read and write its own folder; nothing else: not your files,
// not Claude's, not other programs. It never sees prompts or answers, only
// each agent's status (and only if it asks to). The app draws everything:
// the extension sends what to show, a card of a kind the app knows and a
// settings form, and gets back what you did. docs/extensions.md has the
// protocol; extension/extension.go is the Go side.

struct ExtensionManifest: Codable, Equatable, Identifiable {
    var proto: Int
    var id: String
    var name: String
    var version: String
    var summary: String
    var executable: String
    var docs: String?
    var symbol: String?
    var cards: [String]?
    var permissions: ExtensionPermissions

    enum CodingKeys: String, CodingKey {
        case proto = "protocol", id, name, version, summary, executable, docs, symbol, cards, permissions
    }

    /// What Hall Monitor accepts: the protocol it speaks, a plain id, an
    /// executable inside the extension's folder, ports above 1023.
    var problem: String? {
        if proto != ExtensionHost.protocolVersion { return "It needs a different version of Hall Monitor (protocol \(proto))." }
        if id.range(of: "^[a-z0-9][a-z0-9-]{0,31}$", options: .regularExpression) == nil { return "Its id isn't valid." }
        if name.isEmpty || name.count > 40 || summary.count > 200 || version.count > 20 { return "Its name, summary or version isn't valid." }
        if executable.range(of: "^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$", options: .regularExpression) == nil
            || executable.split(separator: "/").contains(where: { $0 == ".." || $0 == "." }) { return "Its program isn't inside its folder." }
        if let p = permissions.listen, p.count > 4 || p.contains(where: { !(1024...65535).contains($0) }) { return "It asks for ports it can't have." }
        return nil
    }
}

struct ExtensionPermissions: Codable, Equatable {
    /// The hosts it talks to, shown before you install. The sandbox lets it
    /// reach any host over HTTPS; this is what it says it does.
    var network: [String]?
    /// Local ports it may listen on, on 127.0.0.1 only (a sign-in redirect).
    var listen: [Int]?
    /// Whether it gets each agent's status.
    var agents: Bool?
    /// Whether it may open web pages, right after you press something of its.
    var open: Bool?

    /// In words, for the install row.
    var summary: String {
        var parts: [String] = []
        if let n = network, !n.isEmpty { parts.append("Connects to " + n.joined(separator: ", ")) }
        if let l = listen, !l.isEmpty { parts.append("listens on " + l.map { "127.0.0.1:\($0)" }.joined(separator: ", ")) }
        if agents == true { parts.append("sees whether agents are working or waiting") }
        if open == true { parts.append("opens web pages when you ask") }
        let s = parts.joined(separator: "; ")
        return s.isEmpty ? "Runs on its own, offline." : s.prefix(1).uppercased() + s.dropFirst() + "."
    }
}

/// One listing in the marketplace index.
struct MarketEntry: Codable, Identifiable, Equatable {
    var id: String
    var name: String
    var version: String
    var summary: String
    var url: String
    var sha256: String
    var docs: String?
    var symbol: String?
    var min_app: String?
    var permissions: ExtensionPermissions
}

/// A row of an extension's settings form.
struct ExtBlock: Decodable, Equatable, Identifiable {
    var kind: String
    var key: String?
    var label: String?
    var text: String?
    var tone: String?
    var value: Value?
    var placeholder: String?
    var action: String?
    var secret: Bool?
    var primary: Bool?
    var mono: Bool?
    var disabled: Bool?

    var id: String { [kind, key, action, label, text].compactMap { $0 }.joined(separator: "|") }

    enum Value: Decodable, Equatable {
        case string(String), bool(Bool)
        init(from d: Decoder) throws {
            let c = try d.singleValueContainer()
            if let b = try? c.decode(Bool.self) { self = .bool(b) } else { self = .string((try? c.decode(String.self)) ?? "") }
        }
        var string: String { if case .string(let s) = self { return s }; return "" }
        var bool: Bool { if case .bool(let b) = self { return b }; return false }
    }
}

/// A "player" card as an extension sends it.
private struct PlayerJSON: Decodable {
    var title: String
    var artist: String
    var art: String?
    var playing: Bool
    var progress: Double
    var duration: Double
    var at: Double
    var shuffle: Bool?
    var `repeat`: String?
    var volume: Int?
    var device: String?
    var devices: [PlayerDevice]?
    var disallows: [String]?
}

/// What a player card shows: the track, its cover, and the controls' state.
struct NowPlaying: Equatable {
    var trackID: String
    var title: String
    var artist: String
    var artURL: URL?
    var art: NSImage?
    var isPlaying: Bool
    var deviceID: String?
    var deviceName: String = ""
    var progress: TimeInterval = 0
    var duration: TimeInterval = 0
    /// When progress was read: the bar moves on from there.
    var at = Date()
    var shuffle = false
    var repeatMode = "off" // "off", "context" (all) or "track" (one)
    var volume: Int?        // nil when the device can't change it
    /// What the player won't do right now (an ad, a device that can't seek…).
    var disallows: Set<String> = []
    /// The cover's color, bright enough to read on black (the notch's
    /// equalizer); nil until the cover loads.
    var tint: NSColor?
    /// The extension it came from, which gets the controls.
    var source = ""
    var devices: [PlayerDevice] = []

    /// Where the track is now.
    func position(at now: Date) -> TimeInterval {
        min(duration, progress + (isPlaying ? now.timeIntervalSince(at) : 0))
    }
}

struct PlayerDevice: Identifiable, Hashable, Codable {
    var id: String
    var name: String
    var type: String
    var active: Bool

    var symbol: String {
        switch type.lowercased() {
        case "computer": return "laptopcomputer"
        case "smartphone": return "iphone"
        case "speaker": return "hifispeaker"
        case "tv": return "tv"
        case "tablet": return "ipad"
        case "automobile": return "car"
        default: return "speaker.wave.2"
        }
    }
}

@MainActor
final class ExtensionHost: ObservableObject {
    static let shared = ExtensionHost()
    nonisolated static let protocolVersion = 1
    /// The marketplace: a list of extensions, each with its download and
    /// checksum, kept in Hall Monitor's repository.
    static let indexURL = URL(string: "https://raw.githubusercontent.com/hiteshbandhu/hallmonitor/main/extensions/index.json")!

    enum RunState: Equatable { case off, running, failed(String) }

    @Published private(set) var installed: [ExtensionManifest] = []
    @Published private(set) var forms: [String: [ExtBlock]] = [:]
    @Published private(set) var states: [String: RunState] = [:]
    /// The card beside the agents: the first extension, in install order,
    /// that has one to show.
    @Published private(set) var player: NowPlaying?
    @Published private(set) var market: [MarketEntry] = []
    @Published private(set) var marketProblem: String?
    @Published private(set) var installing: Set<String> = []
    @Published private(set) var problems: [String: String] = [:]

    private var runners: [String: ExtensionRunner] = [:]
    private var players: [String: NowPlaying] = [:]
    private var assets: [String: [(key: String, image: NSImage, tint: NSColor?)]] = [:]
    private var crashes: [String: [Date]] = [:]
    private var touched: [String: Date] = [:] // when you last pressed something of its
    private var lastAgents: [[String: String]]?
    private var bag: Set<AnyCancellable> = []
    private var started = false
    private var previewing = false

    static var base: URL {
        if let d = ProcessInfo.processInfo.environment["HALLMONITOR_EXTENSIONS_DIR"], !d.isEmpty { return URL(fileURLWithPath: d) }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("HallMonitor/Extensions", isDirectory: true)
    }

    func start(board: Board) {
        guard !started else { return }
        started = true
        reload()
        for m in installed where enabled(m.id) { launch(m) }
        board.objectWillChange
            .receive(on: RunLoop.main)
            .sink { [weak self, weak board] _ in
                DispatchQueue.main.async {
                    guard let self, let board else { return }
                    self.agentsChanged(board.sessions)
                }
            }
            .store(in: &bag)
        NotificationCenter.default.addObserver(forName: NSApplication.willTerminateNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { ExtensionHost.shared.stopAll() }
        }
    }

    func stopAll() {
        for r in runners.values { r.stop() }
        runners = [:]
    }

    /// Reads what's installed from disk.
    func reload() {
        let fm = FileManager.default
        let dirs = (try? fm.contentsOfDirectory(at: Self.base, includingPropertiesForKeys: nil)) ?? []
        var out: [ExtensionManifest] = []
        for d in dirs where !d.lastPathComponent.hasPrefix(".") {
            guard let data = try? Data(contentsOf: d.appendingPathComponent("manifest.json")),
                  let m = try? JSONDecoder().decode(ExtensionManifest.self, from: data),
                  m.problem == nil, m.id == d.lastPathComponent else { continue }
            out.append(m)
        }
        let order = UserDefaults.standard.stringArray(forKey: "ext.order") ?? []
        installed = out.sorted { (order.firstIndex(of: $0.id) ?? .max, $0.name) < (order.firstIndex(of: $1.id) ?? .max, $1.name) }
    }

    func dir(_ id: String) -> URL { Self.base.appendingPathComponent(id, isDirectory: true) }

    // MARK: on and off

    func enabled(_ id: String) -> Bool {
        UserDefaults.standard.object(forKey: "ext.\(id).enabled") as? Bool ?? true
    }

    func setEnabled(_ id: String, _ on: Bool) {
        UserDefaults.standard.set(on, forKey: "ext.\(id).enabled")
        if on, let m = installed.first(where: { $0.id == id }) {
            crashes[id] = []
            launch(m)
        } else {
            runners.removeValue(forKey: id)?.stop()
            clear(id)
            states[id] = .off
        }
    }

    private func clear(_ id: String) {
        forms[id] = nil
        players[id] = nil
        assets[id] = nil
        pickPlayer()
    }

    private func launch(_ m: ExtensionManifest) {
        runners.removeValue(forKey: m.id)?.stop()
        let r = ExtensionRunner(manifest: m, dir: dir(m.id))
        r.onMessage = { [weak self] msg in Task { @MainActor in self?.handle(m.id, msg) } }
        r.onExit = { [weak self, weak r] status in Task { @MainActor in self?.exited(m, runner: r, status: status) } }
        do {
            try r.start()
        } catch {
            states[m.id] = .failed("Couldn't start: \(error.localizedDescription)")
            return
        }
        runners[m.id] = r
        states[m.id] = .running
        problems[m.id] = nil
        r.send(["type": "hello", "protocol": Self.protocolVersion, "app": appVersion,
                "settings": settings(m.id), "secrets": secrets(m.id)])
        if m.permissions.agents == true, let a = lastAgents { r.send(["type": "agents", "sessions": a]) }
    }

    /// It stopped: start it again, after a pause that grows, unless it keeps
    /// crashing.
    private func exited(_ m: ExtensionManifest, runner: ExtensionRunner?, status: Int32) {
        guard let runner, runners[m.id] === runner else { return } // stopped on purpose
        runners[m.id] = nil
        clear(m.id)
        let now = Date()
        var recent = (crashes[m.id] ?? []).filter { now.timeIntervalSince($0) < 300 }
        recent.append(now)
        crashes[m.id] = recent
        if recent.count >= 5 {
            states[m.id] = .failed("Stopped after crashing \(recent.count) times in five minutes. Its log is in \(dir(m.id).appendingPathComponent("log.txt").path).")
            return
        }
        states[m.id] = .failed("Stopped (exit \(status)); starting it again…")
        let delay = min(60, pow(2, Double(recent.count)))
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak self] in
            guard let self, self.enabled(m.id), self.runners[m.id] == nil,
                  self.installed.contains(where: { $0.id == m.id }) else { return }
            self.launch(m)
        }
    }

    // MARK: what you do

    func action(_ id: String, _ name: String, _ args: [String: Any] = [:]) {
        touched[id] = Date()
        runners[id]?.send(["type": "action", "name": name, "args": args])
    }

    func input(_ id: String, _ key: String, _ value: Any) {
        touched[id] = Date()
        runners[id]?.send(["type": "input", "key": key, "value": value])
    }

    /// Each agent's status, under a key that says nothing about it.
    private func agentsChanged(_ sessions: [Session]) {
        let list = sessions.filter { !$0.stale }.map { s -> [String: String] in
            let h = SHA256.hash(data: Data(s.key.utf8)).prefix(8).map { String(format: "%02x", $0) }.joined()
            return ["key": h, "status": s.status]
        }
        guard list != lastAgents else { return }
        lastAgents = list
        for m in installed where m.permissions.agents == true {
            runners[m.id]?.send(["type": "agents", "sessions": list])
        }
    }

    // MARK: what it says

    private func handle(_ id: String, _ msg: [String: Any]) {
        guard runners[id] != nil, let m = installed.first(where: { $0.id == id }) else { return }
        switch msg["type"] as? String {
        case "card":
            guard let c = msg["card"] as? [String: Any] else {
                players[id] = nil
                pickPlayer()
                return
            }
            guard c["kind"] as? String == "player", m.cards?.contains("player") == true,
                  let pj = c["player"], let data = try? JSONSerialization.data(withJSONObject: pj),
                  let p = try? JSONDecoder().decode(PlayerJSON.self, from: data) else { return }
            players[id] = nowPlaying(p, from: id)
            pickPlayer()
        case "asset":
            guard let key = msg["key"] as? String, key.count <= 128, let b64 = msg["data"] as? String, b64.count < 3 << 20,
                  let data = Data(base64Encoded: b64), let img = NSImage(data: data), img.isValid else { return }
            var list = (assets[id] ?? []).filter { $0.key != key }
            list.append((key, img, ExtensionHost.tint(of: img)))
            assets[id] = Array(list.suffix(8))
            if var p = players[id], p.artURL?.absoluteString == "asset:" + key {
                p.art = img
                p.tint = list.last?.tint
                players[id] = p
                pickPlayer()
            }
        case "settings":
            guard let f = msg["form"], let data = try? JSONSerialization.data(withJSONObject: f),
                  let blocks = try? JSONDecoder().decode([ExtBlock].self, from: data) else { return }
            forms[id] = Array(blocks.prefix(40))
        case "save":
            guard let values = msg["settings"] as? [String: Any] else { return }
            var s = settings(id)
            for (k, v) in values where k.count <= 64 {
                if v is NSNull { s[k] = nil } else if JSONSerialization.isValidJSONObject([v]) { s[k] = v }
            }
            if let data = try? JSONSerialization.data(withJSONObject: s), data.count <= 64 << 10 {
                UserDefaults.standard.set(s, forKey: "ext.\(id).settings")
            }
        case "secret":
            guard let key = msg["key"] as? String, key.range(of: "^[a-z0-9_.-]{1,64}$", options: .regularExpression) != nil else { return }
            if let v = msg["value"] as? String, v.utf8.count <= 16 << 10 {
                ExtensionSecrets.save(id, key, v)
            } else if msg["value"] is NSNull || msg["value"] == nil {
                ExtensionSecrets.delete(id, key)
            }
        case "open":
            // Only a web page, only if it may, and only right after you
            // pressed something of its (a sign-in button, say).
            guard m.permissions.open == true, let s = msg["url"] as? String, let url = URL(string: s), url.scheme == "https",
                  let t = touched[id], Date().timeIntervalSince(t) < 15 else { return }
            touched[id] = nil
            NSWorkspace.shared.open(url)
        default:
            break
        }
    }

    private func nowPlaying(_ p: PlayerJSON, from id: String) -> NowPlaying {
        let asset = p.art.flatMap { key in assets[id]?.last { $0.key == key } }
        return NowPlaying(
            trackID: p.title + "\u{1}" + p.artist, title: String(p.title.prefix(200)), artist: String(p.artist.prefix(200)),
            artURL: p.art.flatMap { URL(string: "asset:" + $0) }, art: asset?.image, isPlaying: p.playing, deviceID: nil,
            deviceName: String((p.device ?? "").prefix(60)), progress: max(0, p.progress), duration: max(0, p.duration),
            at: Date(timeIntervalSince1970: p.at / 1000), shuffle: p.shuffle ?? false, repeatMode: p.repeat ?? "off",
            volume: p.volume.map { min(100, max(0, $0)) }, disallows: Set(p.disallows ?? []), tint: asset?.tint,
            source: id, devices: Array((p.devices ?? []).prefix(20)))
    }

    private func pickPlayer() {
        let next = installed.lazy.compactMap { [players] m in players[m.id] }.first
        if next != player { player = next }
    }

    // MARK: settings and secrets it keeps

    func settings(_ id: String) -> [String: Any] {
        UserDefaults.standard.dictionary(forKey: "ext.\(id).settings") ?? [:]
    }

    private func secrets(_ id: String) -> [String: String] {
        var out: [String: String] = [:]
        for k in ExtensionSecrets.keys(id) { out[k] = ExtensionSecrets.read(id, k) }
        return out
    }

    private var appVersion: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0"
    }

    // MARK: marketplace

    func loadMarket() {
        guard !previewing else { return }
        Task {
            do {
                var req = URLRequest(url: Self.indexURL, cachePolicy: .reloadIgnoringLocalCacheData)
                req.timeoutInterval = 10
                let (data, resp) = try await URLSession.shared.data(for: req)
                guard (resp as? HTTPURLResponse)?.statusCode == 200 else { throw ExtError("The marketplace didn't answer (HTTP \((resp as? HTTPURLResponse)?.statusCode ?? 0)).") }
                struct Index: Decodable { var extensions: [MarketEntry] }
                market = try JSONDecoder().decode(Index.self, from: data).extensions
                    .filter { $0.id.range(of: "^[a-z0-9][a-z0-9-]{0,31}$", options: .regularExpression) != nil }
                marketProblem = nil
            } catch {
                marketProblem = "Couldn't load the marketplace: \(error.localizedDescription)"
            }
        }
    }

    /// Too old a Hall Monitor for it; a development build runs anything.
    func needsNewerApp(_ e: MarketEntry) -> Bool {
        guard let min = e.min_app, appVersion != "0.1.0" else { return false }
        return ExtensionHost.compare(appVersion, min) < 0
    }

    func update(for m: ExtensionManifest) -> MarketEntry? {
        market.first { $0.id == m.id && ExtensionHost.compare($0.version, m.version) > 0 }
    }

    func install(_ e: MarketEntry) {
        guard !installing.contains(e.id) else { return }
        installing.insert(e.id)
        problems[e.id] = nil
        Task {
            defer { installing.remove(e.id) }
            do {
                guard let url = URL(string: e.url), url.scheme == "https" else { throw ExtError("Its download isn't an https link.") }
                let (file, resp) = try await URLSession.shared.download(from: url)
                defer { try? FileManager.default.removeItem(at: file) }
                guard (resp as? HTTPURLResponse)?.statusCode == 200 else { throw ExtError("The download failed (HTTP \((resp as? HTTPURLResponse)?.statusCode ?? 0)).") }
                let data = try Data(contentsOf: file, options: .mappedIfSafe)
                guard data.count < 100 << 20 else { throw ExtError("The download is too big.") }
                let sum = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
                guard sum == e.sha256.lowercased() else { throw ExtError("The download doesn't match the marketplace's checksum, so it wasn't installed.") }
                try await installArchive(file, expect: e)
            } catch {
                problems[e.id] = error.localizedDescription
            }
        }
    }

    /// From a .tar.gz you have, for trying an extension out.
    func installFile(_ file: URL) {
        installing.insert("file")
        Task {
            defer { installing.remove("file") }
            do {
                try await installArchive(file, expect: nil)
                marketProblem = nil
            } catch {
                marketProblem = error.localizedDescription
            }
        }
    }

    /// `HallMonitor --install-extension file.tar.gz`: installs without
    /// starting it (the running app picks it up when it starts again).
    func installFromCommandLine(_ file: URL) async throws -> ExtensionManifest {
        reload()
        return try await installArchive(file, expect: nil, run: false)
    }

    @discardableResult
    private func installArchive(_ file: URL, expect: MarketEntry?, run: Bool = true) async throws -> ExtensionManifest {
        let fm = FileManager.default
        try fm.createDirectory(at: Self.base, withIntermediateDirectories: true)
        let staging = Self.base.appendingPathComponent(".staging-\(UUID().uuidString)", isDirectory: true)
        try fm.createDirectory(at: staging, withIntermediateDirectories: true)
        defer { try? fm.removeItem(at: staging) }
        try await Task.detached { try ExtensionHost.untar(file, into: staging) }.value
        let root = try ExtensionHost.checkTree(staging)
        guard let data = try? Data(contentsOf: root.appendingPathComponent("manifest.json")),
              let m = try? JSONDecoder().decode(ExtensionManifest.self, from: data) else { throw ExtError("It has no readable manifest.json.") }
        if let p = m.problem { throw ExtError(p) }
        if let e = expect {
            guard m.id == e.id, m.version == e.version else { throw ExtError("It isn't the version the marketplace lists.") }
            guard m.permissions == e.permissions else { throw ExtError("It asks for more than the marketplace says.") }
        }
        let exe = root.appendingPathComponent(m.executable)
        guard (try? exe.resourceValues(forKeys: [.isRegularFileKey]))?.isRegularFile == true else { throw ExtError("Its program is missing.") }
        try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: exe.path)

        let fresh = !installed.contains { $0.id == m.id }
        runners.removeValue(forKey: m.id)?.stop()
        clear(m.id)
        let target = dir(m.id)
        // An update keeps what it stored.
        let keep = target.appendingPathComponent("data")
        if fm.fileExists(atPath: keep.path) { try fm.moveItem(at: keep, to: root.appendingPathComponent("data")) }
        if fm.fileExists(atPath: target.path) { try fm.removeItem(at: target) }
        try fm.moveItem(at: root, to: target)
        if fresh {
            var order = UserDefaults.standard.stringArray(forKey: "ext.order") ?? []
            order.removeAll { $0 == m.id }
            order.append(m.id)
            UserDefaults.standard.set(order, forKey: "ext.order")
            UserDefaults.standard.set(true, forKey: "ext.\(m.id).enabled")
            if m.id == "spotify" { ExtensionHost.adoptBuiltInSpotify() }
        }
        reload()
        crashes[m.id] = []
        if run, enabled(m.id), let m = installed.first(where: { $0.id == m.id }) { launch(m) }
        return m
    }

    func uninstall(_ id: String) {
        runners.removeValue(forKey: id)?.stop()
        clear(id)
        try? FileManager.default.removeItem(at: dir(id))
        for k in ExtensionSecrets.keys(id) { ExtensionSecrets.delete(id, k) }
        let d = UserDefaults.standard
        d.removeObject(forKey: "ext.\(id).settings")
        d.removeObject(forKey: "ext.\(id).enabled")
        d.set((d.stringArray(forKey: "ext.order") ?? []).filter { $0 != id }, forKey: "ext.order")
        states[id] = nil
        problems[id] = nil
        reload()
    }

    nonisolated static func untar(_ file: URL, into dir: URL) throws {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/tar")
        // bsdtar refuses absolute paths and ".." unless asked (-P).
        p.arguments = ["-xzf", file.path, "-C", dir.path, "--no-same-owner"]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        try p.run()
        p.waitUntilExit()
        guard p.terminationStatus == 0 else { throw ExtError("It isn't a .tar.gz Hall Monitor can open.") }
    }

    /// Plain files and folders only (no links out of the folder), not too
    /// many or too big; the manifest at the top or one folder down.
    nonisolated static func checkTree(_ dir: URL) throws -> URL {
        let keys: [URLResourceKey] = [.isSymbolicLinkKey, .isRegularFileKey, .isDirectoryKey, .fileSizeKey]
        guard let walk = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: keys) else { throw ExtError("It couldn't be read.") }
        var total = 0, count = 0
        for case let u as URL in walk {
            let v = try u.resourceValues(forKeys: Set(keys))
            if v.isSymbolicLink == true || (v.isRegularFile != true && v.isDirectory != true) { throw ExtError("It contains links or special files.") }
            total += v.fileSize ?? 0
            count += 1
            if total > 200 << 20 || count > 2000 { throw ExtError("It's too big.") }
        }
        if FileManager.default.fileExists(atPath: dir.appendingPathComponent("manifest.json").path) { return dir }
        let top = (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? []
        if top.count == 1, FileManager.default.fileExists(atPath: top[0].appendingPathComponent("manifest.json").path) { return top[0] }
        throw ExtError("It has no manifest.json.")
    }

    /// Spotify used to be built in: its sign-in and switches move into the
    /// extension, so it starts already connected.
    private static func adoptBuiltInSpotify() {
        let d = UserDefaults.standard
        var s: [String: Any] = [:]
        if let id = d.string(forKey: "spotifyClientID") { s["client_id"] = id }
        if let v = d.object(forKey: "spotifyNowPlaying") as? Bool { s["show"] = v }
        if let v = d.object(forKey: "spotifyPause") as? Bool { s["pause"] = v }
        if !s.isEmpty { d.set(s, forKey: "ext.spotify.settings") }
        let old = ["svc": "dev.hallmonitor.app.spotify", "acct": "refresh-token"]
        if let token = ExtensionSecrets.readRaw(service: old["svc"]!, account: old["acct"]!) {
            ExtensionSecrets.save("spotify", "refresh_token", token)
            // The old one goes only once the new one reads back.
            if ExtensionSecrets.read("spotify", "refresh_token") == token {
                ExtensionSecrets.deleteRaw(service: old["svc"]!, account: old["acct"]!)
            }
        }
        for k in ["spotifyClientID", "spotifyNowPlaying", "spotifyPause"] { d.removeObject(forKey: k) }
    }

    // MARK: helpers

    /// Compares dotted versions: 1.10 is after 1.9.
    nonisolated static func compare(_ a: String, _ b: String) -> Int {
        let x = a.split(separator: ".").map { Int($0) ?? 0 }, y = b.split(separator: ".").map { Int($0) ?? 0 }
        for i in 0..<max(x.count, y.count) {
            let l = i < x.count ? x[i] : 0, r = i < y.count ? y[i] : 0
            if l != r { return l < r ? -1 : 1 }
        }
        return 0
    }

    /// The cover's average color, lifted so it reads on black: bright, and
    /// saturated unless the cover is grey to begin with.
    nonisolated static func tint(of image: NSImage) -> NSColor? {
        guard let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil) else { return nil }
        var px = [UInt8](repeating: 0, count: 4)
        guard let ctx = CGContext(data: &px, width: 1, height: 1, bitsPerComponent: 8, bytesPerRow: 4,
                                  space: CGColorSpaceCreateDeviceRGB(),
                                  bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        ctx.interpolationQuality = .medium
        ctx.draw(cg, in: CGRect(x: 0, y: 0, width: 1, height: 1))
        let avg = NSColor(srgbRed: CGFloat(px[0]) / 255, green: CGFloat(px[1]) / 255, blue: CGFloat(px[2]) / 255, alpha: 1)
        var h: CGFloat = 0, sat: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        avg.getHue(&h, saturation: &sat, brightness: &b, alpha: &a)
        let grey = sat < 0.12
        return NSColor(hue: h, saturation: grey ? 0 : min(max(sat, 0.5), 0.85), brightness: max(b, grey ? 0.85 : 0.9), alpha: 1)
    }

    /// For snapshots: an installed extension with its form, and a listing.
    func preview(_ m: ExtensionManifest, form: [ExtBlock], market: [MarketEntry]) {
        previewing = true
        installed = [m]
        forms[m.id] = form
        states[m.id] = .running
        self.market = market
    }
}

struct ExtError: LocalizedError {
    let message: String
    init(_ m: String) { message = m }
    var errorDescription: String? { message }
}

// MARK: - Running one

/// One extension's process: started under sandbox-exec with a profile made
/// from its manifest, talked to over its stdin and stdout, its stderr to a
/// log in its folder.
final class ExtensionRunner: @unchecked Sendable {
    let manifest: ExtensionManifest
    let dir: URL
    var onMessage: (([String: Any]) -> Void)?
    var onExit: ((Int32) -> Void)?
    private var process: Process?
    private var input: FileHandle?
    private var buffer = Data()
    private let queue = DispatchQueue(label: "hallmonitor.extension")

    init(manifest: ExtensionManifest, dir: URL) {
        self.manifest = manifest
        self.dir = dir
    }

    func start() throws {
        let fm = FileManager.default
        let data = dir.appendingPathComponent("data", isDirectory: true)
        let tmp = data.appendingPathComponent("tmp", isDirectory: true)
        try fm.createDirectory(at: tmp, withIntermediateDirectories: true)
        let exe = dir.appendingPathComponent(manifest.executable).resolvingSymlinksInPath()
        let log = dir.appendingPathComponent("log.txt")
        // A log that's grown big starts over.
        if let size = (try? fm.attributesOfItem(atPath: log.path))?[.size] as? Int, size > 1 << 20 { try? fm.removeItem(at: log) }
        if !fm.fileExists(atPath: log.path) { fm.createFile(atPath: log.path, contents: nil) }
        let logHandle = try FileHandle(forWritingTo: log)
        logHandle.seekToEndOfFile()

        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/sandbox-exec")
        p.arguments = ["-p", ExtensionRunner.profile(dir: dir.resolvingSymlinksInPath(), data: data.resolvingSymlinksInPath(), exe: exe, listen: manifest.permissions.listen ?? []), exe.path]
        p.currentDirectoryURL = data
        p.environment = ["HOME": data.path, "TMPDIR": tmp.path + "/", "PATH": "/usr/bin:/bin", "LANG": "en_US.UTF-8",
                         "HALLMONITOR_EXTENSION": manifest.id]
        let stdin = Pipe(), stdout = Pipe()
        p.standardInput = stdin
        p.standardOutput = stdout
        p.standardError = logHandle
        stdout.fileHandleForReading.readabilityHandler = { [weak self] h in
            let chunk = h.availableData
            guard let self else { return }
            if chunk.isEmpty {
                h.readabilityHandler = nil
                return
            }
            self.queue.async { self.received(chunk) }
        }
        p.terminationHandler = { [weak self] proc in
            try? logHandle.close()
            self?.onExit?(proc.terminationStatus)
        }
        try p.run()
        process = p
        input = stdin.fileHandleForWriting
    }

    private func received(_ chunk: Data) {
        buffer.append(chunk)
        while let nl = buffer.firstIndex(of: 0x0A) {
            let line = buffer[buffer.startIndex..<nl]
            buffer.removeSubrange(buffer.startIndex...nl)
            if let obj = try? JSONSerialization.jsonObject(with: line) as? [String: Any] { onMessage?(obj) }
        }
        // A line this long isn't the protocol: stop it.
        if buffer.count > 8 << 20 {
            buffer.removeAll()
            process?.terminate()
        }
    }

    func send(_ obj: [String: Any]) {
        guard let input, var data = try? JSONSerialization.data(withJSONObject: obj) else { return }
        data.append(0x0A)
        queue.async { try? input.write(contentsOf: data) }
    }

    /// Closing its stdin tells it to go; it gets a moment, then it's ended.
    func stop() {
        onExit = nil
        let p = process
        queue.async { [input] in
            try? input?.close()
            DispatchQueue.global().asyncAfter(deadline: .now() + 2) { if p?.isRunning == true { p?.terminate() } }
        }
        process = nil
        input = nil
    }

    /// What it may do: read the system and its own folder, write only its
    /// data, reach the internet over HTTPS (and DNS), listen on its own
    /// ports on 127.0.0.1, and run nothing but itself.
    static func profile(dir: URL, data: URL, exe: URL, listen: [Int]) -> String {
        func q(_ s: String) -> String { "\"" + s.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"") + "\"" }
        var lines = [
            "(version 1)",
            "(deny default)",
            "(import \"system.sb\")",
            "(allow process-fork)",
            "(allow process-exec (literal \(q(exe.path))))",
            "(allow file-read* (subpath \"/System\") (subpath \"/usr/lib\") (subpath \"/usr/share\") (subpath \"/Library\") (subpath \"/private/var/db\") (subpath \"/private/etc\") (subpath \"/dev\") (literal \"/private/var/run/resolv.conf\"))",
            "(allow file-read* (subpath \(q(dir.path))))",
            "(allow file-read* file-write* (subpath \(q(data.path))))",
            "(allow file-read-metadata)",
            "(allow network-outbound (remote tcp \"*:443\"))",
            "(allow network-outbound (literal \"/private/var/run/mDNSResponder\"))",
            "(allow mach-lookup (global-name \"com.apple.SecurityServer\") (global-name \"com.apple.trustd.agent\") (global-name \"com.apple.trustd\") (global-name \"com.apple.ocspd\") (global-name \"com.apple.dnssd.service\") (global-name \"com.apple.SystemConfiguration.configd\"))",
            "(allow sysctl-read)",
        ]
        for port in listen where (1024...65535).contains(port) {
            lines.append("(allow network-bind network-inbound (local tcp \"localhost:\(port)\"))")
        }
        return lines.joined(separator: "\n")
    }
}

// MARK: - Secrets

/// Each extension's secrets, as generic passwords under its own service.
/// The Keychain can't list them with their values, so the keys are kept too.
enum ExtensionSecrets {
    static func service(_ id: String) -> String { "dev.hallmonitor.ext.\(id)" }

    static func keys(_ id: String) -> [String] { UserDefaults.standard.stringArray(forKey: "ext.\(id).secrets") ?? [] }

    static func read(_ id: String, _ key: String) -> String? { readRaw(service: service(id), account: key) }

    static func save(_ id: String, _ key: String, _ value: String) {
        let q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service(id), kSecAttrAccount as String: key]
        let data = Data(value.utf8)
        if SecItemUpdate(q as CFDictionary, [kSecValueData as String: data] as CFDictionary) == errSecItemNotFound {
            var add = q
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            add[kSecAttrLabel as String] = "Hall Monitor extension: \(id)"
            SecItemAdd(add as CFDictionary, nil)
        }
        var k = keys(id)
        if !k.contains(key), k.count < 16 {
            k.append(key)
            UserDefaults.standard.set(k, forKey: "ext.\(id).secrets")
        }
    }

    static func delete(_ id: String, _ key: String) {
        deleteRaw(service: service(id), account: key)
        UserDefaults.standard.set(keys(id).filter { $0 != key }, forKey: "ext.\(id).secrets")
    }

    static func readRaw(service: String, account: String) -> String? {
        let q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
                                kSecAttrAccount as String: account, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let d = out as? Data else { return nil }
        return String(data: d, encoding: .utf8)
    }

    static func deleteRaw(service: String, account: String) {
        let q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
        SecItemDelete(q as CFDictionary)
    }
}
