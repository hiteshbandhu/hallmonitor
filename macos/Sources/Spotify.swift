import AppKit
import Combine
import CryptoKit
import Network
import Security

// MARK: - Spotify
//
// Optional, off until you connect: shows what's playing in the notch and
// pauses it while an agent waits for you. It talks to Spotify's Web API
// (playback control needs Premium) with your own developer app's client ID,
// signed in with PKCE, so there is no client secret. The refresh token lives
// in the Keychain; the access token only in memory.

struct NowPlaying: Equatable {
    var trackID: String
    var title: String
    var artist: String
    var artURL: URL?
    var art: NSImage?
    var isPlaying: Bool
    var deviceID: String?
}

@MainActor
final class Spotify: ObservableObject {
    static let shared = Spotify()

    @Published private(set) var connected = Keychain.read() != nil
    @Published private(set) var connecting = false
    @Published private(set) var nowPlaying: NowPlaying?
    @Published private(set) var problem: String?

    /// Register exactly this as the app's redirect URI.
    static let port: UInt16 = 43821
    static let redirectURI = "http://127.0.0.1:\(port)/callback"
    static let scopes = "user-read-playback-state user-modify-playback-state"

    static var clientID: String {
        (UserDefaults.standard.string(forKey: "spotifyClientID") ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Spotify client IDs are 32 hex digits; anything else never reaches a URL.
    static func validClientID(_ id: String) -> Bool {
        id.range(of: "^[0-9a-fA-F]{32}$", options: .regularExpression) != nil
    }

    private var accessToken: String?
    private var expires = Date.distantPast
    private var refreshToken: String?
    private var poller: Timer?
    private var pollPaused = Date.distantPast // after a 429
    private var callback: Callback?
    private var bag: Set<AnyCancellable> = []

    // MARK: sign in

    func connect() {
        let id = Spotify.clientID
        guard Spotify.validClientID(id) else {
            problem = "That doesn't look like a Spotify client ID (32 letters and digits)."
            return
        }
        callback?.stop()
        let verifier = Spotify.randomURLSafe(64)
        let state = Spotify.randomURLSafe(16)
        var c = URLComponents(string: "https://accounts.spotify.com/authorize")!
        c.queryItems = [
            .init(name: "client_id", value: id),
            .init(name: "response_type", value: "code"),
            .init(name: "redirect_uri", value: Spotify.redirectURI),
            .init(name: "scope", value: Spotify.scopes),
            .init(name: "state", value: state),
            .init(name: "code_challenge_method", value: "S256"),
            .init(name: "code_challenge", value: Spotify.challenge(verifier)),
        ]
        problem = nil
        connecting = true
        let cb = Callback(port: Spotify.port, state: state) { [weak self] result in
            Task { @MainActor in await self?.finishConnect(result, verifier: verifier, clientID: id) }
        }
        do {
            try cb.start()
        } catch {
            connecting = false
            problem = "Couldn't listen on 127.0.0.1:\(Spotify.port) for Spotify's reply: \(error.localizedDescription)"
            return
        }
        callback = cb
        NSWorkspace.shared.open(c.url!)
    }

    private func finishConnect(_ result: Result<String, Error>, verifier: String, clientID: String) async {
        callback = nil
        defer { connecting = false }
        switch result {
        case .failure(let e):
            problem = e.localizedDescription
        case .success(let code):
            do {
                try await token([
                    "grant_type": "authorization_code", "code": code, "redirect_uri": Spotify.redirectURI,
                    "client_id": clientID, "code_verifier": verifier,
                ])
                connected = true
                problem = nil
                startPolling()
            } catch {
                problem = error.localizedDescription
            }
        }
    }

    func disconnect() {
        callback?.stop()
        callback = nil
        connecting = false
        Keychain.delete()
        accessToken = nil
        refreshToken = nil
        expires = .distantPast
        connected = false
        nowPlaying = nil
        pausedForAgents = nil
        poller?.invalidate()
        poller = nil
    }

    /// Posts to the token endpoint and keeps what comes back.
    private func token(_ form: [String: String]) async throws {
        var req = URLRequest(url: URL(string: "https://accounts.spotify.com/api/token")!)
        req.httpMethod = "POST"
        req.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        var c = URLComponents()
        c.queryItems = form.map { URLQueryItem(name: $0.key, value: $0.value) }
        // URLComponents leaves "+" alone; a form body must not.
        req.httpBody = c.percentEncodedQuery?.replacingOccurrences(of: "+", with: "%2B").data(using: .utf8)
        let (data, resp) = try await URLSession.shared.data(for: req)
        struct Token: Decodable {
            var access_token: String
            var expires_in: Double
            var refresh_token: String?
        }
        guard (resp as? HTTPURLResponse)?.statusCode == 200, let t = try? JSONDecoder().decode(Token.self, from: data) else {
            struct Err: Decodable { var error: String; var error_description: String? }
            let e = try? JSONDecoder().decode(Err.self, from: data)
            if e?.error == "invalid_grant", form["grant_type"] == "refresh_token" {
                disconnect()
                throw SpotifyError("Spotify signed Hall Monitor out. Connect again in Settings.")
            }
            throw SpotifyError("Spotify refused the sign-in: \(e?.error_description ?? e?.error ?? "HTTP \((resp as? HTTPURLResponse)?.statusCode ?? 0)")")
        }
        accessToken = t.access_token
        expires = Date().addingTimeInterval(t.expires_in - 60)
        // A refresh may or may not hand out a new refresh token.
        if let r = t.refresh_token, r != refreshToken {
            refreshToken = r
            Keychain.save(r)
        }
    }

    private func validToken() async throws -> String {
        if let a = accessToken, Date() < expires { return a }
        if refreshToken == nil { refreshToken = Keychain.read() }
        guard let r = refreshToken else { throw SpotifyError("Not connected to Spotify.") }
        try await token(["grant_type": "refresh_token", "refresh_token": r, "client_id": Spotify.clientID])
        return accessToken ?? ""
    }

    // MARK: player

    private func api(_ method: String, _ path: String, device: String? = nil, retry: Bool = true) async throws -> (Data, Int) {
        var c = URLComponents(string: "https://api.spotify.com/v1/me/player" + path)!
        if let device { c.queryItems = [URLQueryItem(name: "device_id", value: device)] }
        var req = URLRequest(url: c.url!)
        req.httpMethod = method
        req.timeoutInterval = 8
        req.setValue("Bearer \(try await validToken())", forHTTPHeaderField: "Authorization")
        if method != "GET" { req.setValue("0", forHTTPHeaderField: "Content-Length") }
        let (data, resp) = try await URLSession.shared.data(for: req)
        let http = resp as? HTTPURLResponse
        let status = http?.statusCode ?? 0
        switch status {
        case 401 where retry:
            expires = .distantPast
            return try await api(method, path, device: device, retry: false)
        case 429:
            let wait = Double(http?.value(forHTTPHeaderField: "Retry-After") ?? "") ?? 30
            pollPaused = Date().addingTimeInterval(wait)
            throw SpotifyError("Spotify asked Hall Monitor to slow down.")
        case 403 where method != "GET":
            throw SpotifyError("Spotify only lets Premium accounts control playback.")
        default:
            return (data, status)
        }
    }

    /// What's playing now; nil when nothing is (Spotify answers 204).
    private func playback() async throws -> NowPlaying? {
        let (data, status) = try await api("GET", "")
        guard status == 200 else { return nil }
        struct Image: Decodable { var url: String; var width: Int? }
        struct Item: Decodable {
            var id: String?
            var name: String
            var artists: [Artist]?
            var album: Album?
            struct Artist: Decodable { var name: String }
            struct Album: Decodable { var images: [Image] }
        }
        struct State: Decodable {
            var is_playing: Bool
            var device: Device?
            var item: Item?
            struct Device: Decodable { var id: String? }
        }
        guard let s = try? JSONDecoder().decode(State.self, from: data), let item = s.item else { return nil }
        // The smallest cover that's still sharp at 60 pt on a Retina screen.
        let images = (item.album?.images ?? []).sorted { ($0.width ?? 0) < ($1.width ?? 0) }
        let cover = images.first { ($0.width ?? 0) >= 120 } ?? images.last
        return NowPlaying(
            trackID: item.id ?? item.name, title: item.name,
            artist: (item.artists ?? []).map(\.name).joined(separator: ", "),
            artURL: cover.flatMap { URL(string: $0.url) }, art: nil,
            isPlaying: s.is_playing, deviceID: s.device?.id)
    }

    func refresh() async {
        guard connected, Date() >= pollPaused else { return }
        do {
            var np = try await playback()
            if var n = np {
                n.art = n.artURL == nowPlaying?.artURL ? nowPlaying?.art : nil
                np = n
            }
            nowPlaying = np
            if let url = np?.artURL, np?.art == nil { await loadArt(url) }
        } catch {
            // Keep the last state; a hiccup shouldn't blank the notch.
        }
    }

    /// Album art only from Spotify's own image hosts, over HTTPS, and small.
    private func loadArt(_ url: URL) async {
        guard url.scheme == "https", let host = url.host,
              host.hasSuffix(".scdn.co") || host.hasSuffix(".spotifycdn.com") else { return }
        guard let (data, resp) = try? await URLSession.shared.data(from: url),
              (resp as? HTTPURLResponse)?.statusCode == 200, data.count < 2 << 20,
              let img = NSImage(data: data), img.isValid else { return }
        if nowPlaying?.artURL == url { nowPlaying?.art = img }
    }

    func togglePlay() {
        guard let np = nowPlaying else { return }
        nowPlaying?.isPlaying.toggle() // feels instant; the next poll corrects it
        control(np.isPlaying ? "pause" : "play", device: np.deviceID)
    }

    func next() { control("next", device: nowPlaying?.deviceID, method: "POST") }

    private func control(_ path: String, device: String?, method: String = "PUT") {
        Task {
            do {
                _ = try await api(method, "/" + path, device: device)
                problem = nil
            } catch {
                problem = error.localizedDescription
            }
            try? await Task.sleep(for: .milliseconds(600))
            await refresh()
        }
    }

    // MARK: polling

    func start(board: Board) {
        board.objectWillChange
            .receive(on: RunLoop.main)
            .sink { [weak self, weak board] _ in
                DispatchQueue.main.async {
                    guard let self, let board else { return }
                    self.agentsChanged(board.sessions)
                }
            }
            .store(in: &bag)
        if connected { startPolling() }
    }

    private func startPolling() {
        poller?.invalidate()
        poller = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                guard let self, self.wanted else { return }
                await self.refresh()
            }
        }
        poller?.tolerance = 1
        Task { await refresh() }
    }

    /// Polling only while something uses it.
    private var wanted: Bool {
        let d = UserDefaults.standard
        return d.bool(forKey: "spotifyNowPlaying") || d.bool(forKey: "spotifyPause")
    }

    // MARK: pause while an agent needs you

    private var waiting: Set<String> = []
    /// The device Hall Monitor paused, until it resumes it.
    private var pausedForAgents: (device: String?, track: String)?
    private var resumeTask: Task<Void, Never>?

    private func agentsChanged(_ sessions: [Session]) {
        // Waiting for your answer, here or on another machine; an agent that
        // errored out doesn't hold your music hostage.
        let now = Set(sessions.filter { $0.status == "waiting" }.map(\.key))
        let arrived = !now.subtracting(waiting).isEmpty
        waiting = now
        guard connected, UserDefaults.standard.bool(forKey: "spotifyPause") else { return }
        if arrived {
            resumeTask?.cancel()
            resumeTask = nil
            if pausedForAgents == nil { pauseForAgents() }
        } else if now.isEmpty, pausedForAgents != nil, resumeTask == nil {
            // A beat before resuming: agents often ask again right away.
            resumeTask = Task { @MainActor in
                try? await Task.sleep(for: .seconds(2))
                guard !Task.isCancelled, self.waiting.isEmpty else { return }
                await self.resumeAfterAgents()
                self.resumeTask = nil
            }
        }
    }

    private func pauseForAgents() {
        Task {
            guard let np = try? await playback(), np.isPlaying else { return }
            pausedForAgents = (np.deviceID, np.trackID)
            do {
                _ = try await api("PUT", "/pause", device: np.deviceID)
                nowPlaying?.isPlaying = false
            } catch {
                pausedForAgents = nil
                problem = error.localizedDescription
            }
        }
    }

    private func resumeAfterAgents() async {
        guard let p = pausedForAgents else { return }
        pausedForAgents = nil
        // Only if it's still the way we left it: you may have moved on.
        guard let np = try? await playback(), !np.isPlaying, np.trackID == p.track else { return }
        _ = try? await api("PUT", "/play", device: p.device)
        await refresh()
    }

    // MARK: helpers

    static func randomURLSafe(_ bytes: Int) -> String {
        var b = [UInt8](repeating: 0, count: bytes)
        _ = SecRandomCopyBytes(kSecRandomDefault, bytes, &b)
        return base64URL(Data(b))
    }

    static func challenge(_ verifier: String) -> String {
        base64URL(Data(SHA256.hash(data: Data(verifier.utf8))))
    }

    static func base64URL(_ d: Data) -> String {
        d.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

struct SpotifyError: LocalizedError {
    let message: String
    init(_ m: String) { message = m }
    var errorDescription: String? { message }
}

// MARK: - The sign-in redirect

/// Waits on 127.0.0.1 (never another interface) for the one redirect back
/// from Spotify, checks its state, and stops. Gives up after five minutes.
private final class Callback: @unchecked Sendable {
    private let port: UInt16
    private let state: String
    private let done: (Result<String, Error>) -> Void
    private var listener: NWListener?
    private var finished = false
    private let queue = DispatchQueue(label: "hallmonitor.spotify-callback")

    init(port: UInt16, state: String, done: @escaping (Result<String, Error>) -> Void) {
        self.port = port
        self.state = state
        self.done = done
    }

    func start() throws {
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: NWEndpoint.Port(rawValue: port)!)
        params.allowLocalEndpointReuse = true
        let l = try NWListener(using: params)
        l.newConnectionHandler = { [weak self] conn in self?.handle(conn) }
        l.stateUpdateHandler = { [weak self] s in
            if case .failed(let e) = s { self?.finish(.failure(e)) }
        }
        l.start(queue: queue)
        listener = l
        queue.asyncAfter(deadline: .now() + 300) { [weak self] in
            self?.finish(.failure(SpotifyError("Spotify sign-in timed out. Try Connect again.")))
        }
    }

    func stop() { queue.async { [weak self] in self?.finished = true; self?.listener?.cancel() } }

    private func handle(_ conn: NWConnection) {
        conn.start(queue: queue)
        conn.receive(minimumIncompleteLength: 1, maximumLength: 16 << 10) { [weak self] data, _, _, _ in
            guard let self else { return }
            let line = data.flatMap { String(data: $0, encoding: .utf8) }?.split(separator: "\r\n").first ?? ""
            let parts = line.split(separator: " ")
            let items = parts.count >= 2 ? URLComponents(string: String(parts[1]))?.queryItems ?? [] : []
            let value = { (name: String) in items.first { $0.name == name }?.value }
            guard parts.first == "GET", parts.count >= 2, parts[1].hasPrefix("/callback?") else {
                self.reply(conn, 404, "Not found")
                return
            }
            guard value("state") == self.state else {
                self.reply(conn, 400, "This sign-in link doesn't match the one Hall Monitor started. Try Connect again.")
                return
            }
            if let code = value("code") {
                self.reply(conn, 200, "Hall Monitor is connected to Spotify. You can close this tab.")
                self.finish(.success(code))
            } else {
                let err = value("error") ?? "unknown error"
                self.reply(conn, 200, "Spotify didn't connect (\(err)). You can close this tab.")
                self.finish(.failure(SpotifyError(err == "access_denied" ? "You declined the Spotify sign-in." : "Spotify sign-in failed: \(err)")))
            }
        }
    }

    private func reply(_ conn: NWConnection, _ status: Int, _ text: String) {
        let esc = text.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;")
        let body = "<!doctype html><meta charset=utf-8><title>Hall Monitor</title><body style=\"font:15px -apple-system;margin:4em;text-align:center\">\(esc)"
        let head = "HTTP/1.1 \(status) \(status == 200 ? "OK" : "Error")\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: \(body.utf8.count)\r\nConnection: close\r\n\r\n"
        conn.send(content: Data((head + body).utf8), completion: .contentProcessed { _ in conn.cancel() })
    }

    private func finish(_ r: Result<String, Error>) {
        guard !finished else { return }
        finished = true
        listener?.cancel()
        done(r)
    }
}

// MARK: - Keychain

/// The refresh token, as a generic password only this app reads without
/// asking.
private enum Keychain {
    static let service = "dev.hallmonitor.app.spotify"
    static let account = "refresh-token"

    static var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
    }

    static func read() -> String? {
        var q = query
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let d = out as? Data else { return nil }
        return String(data: d, encoding: .utf8)
    }

    static func save(_ token: String) {
        let data = Data(token.utf8)
        if SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary) == errSecItemNotFound {
            var q = query
            q[kSecValueData as String] = data
            q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            q[kSecAttrLabel as String] = "Hall Monitor: Spotify"
            SecItemAdd(q as CFDictionary, nil)
        }
    }

    static func delete() { SecItemDelete(query as CFDictionary) }
}
