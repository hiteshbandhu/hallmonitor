import AppKit
import SwiftUI
import UserNotifications

// Plain AppKit entry: a SwiftUI App with only a Settings scene opens that
// window at launch, which a menu bar app must not do.
@main
enum Main {
    /// Settings from when the app was called AgentBoard (another bundle id).
    static func migrateSettings() {
        let d = UserDefaults.standard
        guard !d.bool(forKey: "migratedFromAgentBoard") else { return }
        d.set(true, forKey: "migratedFromAgentBoard")
        guard let old = UserDefaults(suiteName: "dev.agentboard.bar") else { return }
        for key in ["notch", "notify", "showCount", "showLimit", "probeLimits"] where d.object(forKey: key) == nil {
            if let v = old.object(forKey: key) { d.set(v, forKey: key) }
        }
    }

    static func main() {
        let app = NSApplication.shared
        let delegate = MainActor.assumeIsolated { AppDelegate() }
        app.delegate = delegate
        app.run()
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    let board = Board()
    private let notch = NotchController()
    private var status: StatusMenu?
    private var defaultsObserver: Any?
    private var settingsWindow: NSWindow?
    private lazy var main = MainWindow(board: board)
    private var askHeartbeat: Timer?

    func applicationDidFinishLaunching(_ note: Notification) {
        Main.migrateSettings()
        UserDefaults.standard.register(defaults: ["notch": true, "notify": true, "showCount": false, "showLimit": true, "probeLimits": true])
        // Menu bar only, whatever Info.plist says (handy for debug builds).
        NSApp.setActivationPolicy(.accessory)

        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.requestAuthorization(options: [.alert, .sound]) { _, _ in }

        board.onEvent = { [weak self] e in self?.handle(e) }
        board.start()
        if let i = CommandLine.arguments.firstIndex(of: "--snapshot"), i + 1 < CommandLine.arguments.count {
            SnapshotMode.run(board: board, dir: CommandLine.arguments[i + 1])
            return
        }
        status = StatusMenu(board: board)
        status?.onSettings = { [weak self] in self?.showSettings() }
        status?.onOpen = { [weak self] pane in self?.main.show(pane) }
        // While answering from the notch is on, tell the hook we're listening.
        askHeartbeat = Timer.scheduledTimer(withTimeInterval: 4, repeats: true) { _ in
            if UserDefaults.standard.bool(forKey: "answerFromNotch") { Asks.heartbeat() }
        }
        askHeartbeat?.tolerance = 1
        if UserDefaults.standard.bool(forKey: "answerFromNotch") { Asks.heartbeat() }
        MainMenu.install(openPane: { [weak self] p in self?.main.show(p) }, settings: { [weak self] in self?.showSettings() })
        if CommandLine.arguments.contains("--window") { main.show() }
        notch.start(board: board)
        defaultsObserver = NotificationCenter.default.addObserver(
            forName: UserDefaults.didChangeNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated {
                self?.notch.setEnabled(UserDefaults.standard.bool(forKey: "notch"))
                self?.status?.refresh()
            }
        }
    }

    func showSettings() {
        if settingsWindow == nil {
            let w = NSWindow(contentViewController: NSHostingController(rootView: SettingsView()))
            w.title = "Hall Monitor Settings"
            w.styleMask = [.titled, .closable]
            w.isReleasedWhenClosed = false
            w.center()
            settingsWindow = w
        }
        NSApp.activate(ignoringOtherApps: true)
        settingsWindow?.makeKeyAndOrderFront(nil)
    }

    /// Clicking the Dock icon (or opening the app again) brings the window back.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        main.show()
        return true
    }

    func applicationWillTerminate(_ note: Notification) {
        board.stop()
    }

    private func handle(_ e: BoardEvent) {
        switch e {
        case .needsYou, .finished:
            notch.announce(e)
            notify(e)
        case .started:
            break // too chatty to announce
        }
    }

    private func notify(_ e: BoardEvent) {
        guard UserDefaults.standard.bool(forKey: "notify") else { return }
        let s = e.session
        let content = UNMutableNotificationContent()
        content.title = s.name
        switch e {
        case .needsYou:
            content.subtitle = "\(Provider.name(s.provider)) needs you"
            content.sound = .default
        case .finished(_, let after):
            // Only long turns are worth a notification.
            guard after >= 60 else { return }
            content.subtitle = "\(Provider.name(s.provider)) finished after \(shortDuration(after))"
        case .started:
            return
        }
        content.body = [s.project, s.host ?? ""].filter { !$0.isEmpty }.joined(separator: " · ")
        let req = UNNotificationRequest(identifier: s.key + "\(Date().timeIntervalSince1970)", content: content, trigger: nil)
        UNUserNotificationCenter.current().add(req)
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                            withCompletionHandler handler: @escaping (UNNotificationPresentationOptions) -> Void) {
        handler([.banner, .sound])
    }
}
