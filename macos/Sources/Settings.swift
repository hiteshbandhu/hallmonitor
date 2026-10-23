import AppKit
import ServiceManagement
import SwiftUI

// MARK: - Settings window
//
// A standard macOS settings window: toolbar tabs across the top, one pane
// each. Every pane has the same size, so switching tabs never resizes the
// window; a pane that runs longer scrolls.

enum SettingsTab: String, CaseIterable {
    case general, notch, claude, spotify, about

    var title: String {
        switch self {
        case .general: return "General"
        case .notch: return "Notch"
        case .claude: return "Claude Code"
        case .spotify: return "Spotify"
        case .about: return "About"
        }
    }

    var symbol: String {
        switch self {
        case .general: return "gearshape"
        case .notch: return "rectangle.topthird.inset.filled"
        case .claude: return "asterisk"
        case .spotify: return "music.note"
        case .about: return "info.circle"
        }
    }

    @MainActor @ViewBuilder
    var view: some View {
        switch self {
        case .general: GeneralSettings()
        case .notch: NotchSettings()
        case .claude: ClaudeSettings()
        case .spotify: SpotifySettings()
        case .about: AboutSettings()
        }
    }
}

@MainActor
enum SettingsWindow {
    static let size = NSSize(width: 540, height: 500)

    static func make() -> NSWindow {
        let tabs = NSTabViewController()
        tabs.tabStyle = .toolbar
        tabs.transitionOptions = [.allowUserInteraction] // a straight switch, no resize or fade
        for tab in SettingsTab.allCases {
            let host = NSHostingController(rootView: tab.view)
            host.sizingOptions = []
            host.preferredContentSize = SettingsWindow.size
            let item = NSTabViewItem(viewController: host)
            item.label = tab.title
            item.identifier = tab.rawValue
            item.image = NSImage(systemSymbolName: tab.symbol, accessibilityDescription: tab.title)
            tabs.addTabViewItem(item)
        }
        let w = NSWindow(contentViewController: tabs)
        w.styleMask = [.titled, .closable]
        w.setContentSize(SettingsWindow.size)
        w.toolbarStyle = .preference
        w.isReleasedWhenClosed = false
        return w
    }
}

/// Each pane: a grouped form at the same width, sized to its content.
private struct SettingsPane<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        // A grouped Form scrolls by itself once it outgrows the window.
        Form { content }
            .formStyle(.grouped)
            .frame(width: SettingsWindow.size.width, height: SettingsWindow.size.height)
    }
}

private struct Footer: View {
    let text: String
    init(_ text: String) { self.text = text }
    // Left-aligned like the rows above it; a grouped Form would otherwise
    // push footers to the trailing edge.
    var body: some View {
        Text(text)
            .font(.footnote)
            .foregroundStyle(.secondary)
            .multilineTextAlignment(.leading)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

// MARK: General

struct GeneralSettings: View {
    @AppStorage("notify") private var notify = true
    @AppStorage("showMenuBar") private var showMenuBar = true
    @AppStorage("showCount") private var showCount = false
    @AppStorage("showLimit") private var showLimit = true
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        SettingsPane {
            Section {
                Toggle("Launch at login", isOn: $launchAtLogin)
                    .onChange(of: launchAtLogin) { _, on in
                        do {
                            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
                        } catch {
                            launchAtLogin = SMAppService.mainApp.status == .enabled
                        }
                    }
                Toggle("Notify when an agent needs you or finishes", isOn: $notify)
            }
            Section {
                Toggle("Show Hall Monitor in the menu bar", isOn: $showMenuBar)
                Toggle("Show agent count in the menu bar", isOn: $showCount)
                    .disabled(!showMenuBar)
                Toggle("Show plan usage in the menu bar", isOn: $showLimit)
                    .disabled(!showMenuBar)
            } header: {
                Text("Menu Bar")
            } footer: {
                if !showMenuBar {
                    Footer("To get back here, open Hall Monitor again from Spotlight or Finder and choose Settings… (⌘,).")
                }
            }
        }
    }
}

// MARK: Notch

struct NotchSettings: View {
    @AppStorage("notch") private var notch = true
    @AppStorage("answerFromNotch") private var answerFromNotch = false

    var body: some View {
        SettingsPane {
            Section {
                Toggle("Show agents around the notch", isOn: $notch)
            } footer: {
                Footer("While agents work, their icons sit beside the notch; hover it for the list. Only on Macs with a notch.")
            }
            Section {
                Toggle("Answer Claude’s questions from the notch", isOn: $answerFromNotch)
                    .onChange(of: answerFromNotch) { _, on in
                        (NSApp.delegate as? AppDelegate)?.board.setAnswerHooks(on)
                    }
            } footer: {
                Footer("When Claude Code asks you something or wants permission, answer it in the notch. Adds a hook to ~/.claude/settings.json (backed up first); if you don’t answer within two minutes, Claude asks you the usual way.")
            }
        }
    }
}

// MARK: Claude Code

struct ClaudeSettings: View {
    @AppStorage("probeLimits") private var probeLimits = true

    var body: some View {
        SettingsPane {
            Section {
                Toggle("Check Claude plan limits every 15 minutes", isOn: $probeLimits)
                    .onChange(of: probeLimits) { _, _ in (NSApp.delegate as? AppDelegate)?.board.restart() }
            } footer: {
                Footer("Opens Claude Code's /usage in the background, so limits stay current even for sessions in the Claude app. Nothing is sent to the model, and Hall Monitor never sees your login.")
            }
        }
    }
}

// MARK: Spotify

struct SpotifySettings: View {
    @AppStorage("spotifyClientID") private var clientID = ""
    @AppStorage("spotifyNowPlaying") private var nowPlaying = true
    @AppStorage("spotifyPause") private var pause = true
    @ObservedObject private var spotify = Spotify.shared

    var body: some View {
        SettingsPane {
            Section {
                if spotify.connected {
                    LabeledContent("Account") {
                        HStack {
                            Text("Connected").foregroundStyle(.secondary)
                            Button("Disconnect") { spotify.disconnect() }
                        }
                    }
                } else {
                    TextField("Client ID", text: $clientID, prompt: Text("32 letters and digits"))
                    LabeledContent("Account") {
                        Button(spotify.connecting ? "Waiting for your browser…" : "Connect") { spotify.connect() }
                            .disabled(spotify.connecting || !Spotify.validClientID(clientID.trimmingCharacters(in: .whitespaces)))
                    }
                }
                if let p = spotify.problem {
                    Text(p).font(.footnote).foregroundStyle(.red)
                }
            } footer: {
                if !spotify.connected {
                    VStack(alignment: .leading, spacing: 4) {
                        Footer("Needs Spotify Premium. Create an app at developer.spotify.com/dashboard with the Web API, add this redirect URI, and paste its client ID above:")
                        Text(Spotify.redirectURI).font(.footnote.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            Section {
                Toggle("Show what’s playing in the notch", isOn: $nowPlaying)
                Toggle("Pause music while an agent waits for you", isOn: $pause)
            } footer: {
                Footer("Music resumes once no agent is waiting, unless you changed it meanwhile. Your sign-in is kept in the Keychain.")
            }
            .disabled(!spotify.connected)
        }
    }
}

// MARK: About

struct AboutSettings: View {
    private var version: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
    }

    var body: some View {
        SettingsPane {
            Section {
                HStack(spacing: 14) {
                    Image(nsImage: NSApp.applicationIconImage)
                        .resizable()
                        .frame(width: 56, height: 56)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Hall Monitor").font(.title3.weight(.semibold))
                        Text("Version \(version)").foregroundStyle(.secondary)
                    }
                }
                .padding(.vertical, 4)
            }
            Section {
                LabeledContent("hallmonitor CLI") {
                    Text(Board.binary).textSelection(.enabled).foregroundStyle(.secondary)
                }
                Link("github.com/hiteshbandhu/hallmonitor", destination: URL(string: "https://github.com/hiteshbandhu/hallmonitor")!)
                Link("Report an issue", destination: URL(string: "https://github.com/hiteshbandhu/hallmonitor/issues")!)
            }
        }
    }
}
