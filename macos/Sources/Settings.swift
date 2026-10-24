import AppKit
import ServiceManagement
import SwiftUI

// MARK: - Settings window
//
// A standard macOS settings window: toolbar tabs across the top, one pane
// each. Every pane has the same size, so switching tabs never resizes the
// window; a pane that runs longer scrolls.

enum SettingsTab: String, CaseIterable {
    case general, notch, claude, extensions, about

    var title: String {
        switch self {
        case .general: return "General"
        case .notch: return "Notch"
        case .claude: return "Claude Code"
        case .extensions: return "Extensions"
        case .about: return "About"
        }
    }

    var symbol: String {
        switch self {
        case .general: return "gearshape"
        case .notch: return "rectangle.topthird.inset.filled"
        case .claude: return "asterisk"
        case .extensions: return "puzzlepiece.extension"
        case .about: return "info.circle"
        }
    }

    @MainActor @ViewBuilder
    var view: some View {
        switch self {
        case .general: GeneralSettings()
        case .notch: NotchSettings()
        case .claude: ClaudeSettings()
        case .extensions: ExtensionsSettings()
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
                Toggle("Open at login", isOn: $launchAtLogin)
                    .onChange(of: launchAtLogin) { _, on in
                        do {
                            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
                        } catch {
                            launchAtLogin = SMAppService.mainApp.status == .enabled
                        }
                    }
                Toggle("Notifications", isOn: $notify)
            } footer: {
                Footer("A notification when an agent needs you, or finishes a turn that took over a minute.")
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
                Toggle("Answer Claude in the notch", isOn: $answerFromNotch)
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

// MARK: Extensions

/// What's installed, each with its switch and its own form, then what the
/// marketplace has.
struct ExtensionsSettings: View {
    @ObservedObject private var host = ExtensionHost.shared

    var body: some View {
        SettingsPane {
            if host.installed.isEmpty {
                Section {
                    Text("No extensions yet. Add one below.").foregroundStyle(.secondary)
                }
            }
            ForEach(host.installed) { m in
                Section {
                    HStack(spacing: 10) {
                        ExtensionIcon(symbol: m.symbol)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(m.name).fontWeight(.semibold)
                            Text(m.summary).font(.footnote).foregroundStyle(.secondary)
                        }
                        Spacer()
                        Toggle("On", isOn: Binding(get: { host.enabled(m.id) }, set: { host.setEnabled(m.id, $0); host.objectWillChange.send() }))
                            .labelsHidden()
                            .toggleStyle(.switch)
                    }
                    if host.enabled(m.id) {
                        ForEach(host.forms[m.id] ?? []) { b in ExtBlockRow(id: m.id, block: b) }
                        if case .failed(let why) = host.states[m.id] {
                            Text(why).font(.footnote).foregroundStyle(.red).textSelection(.enabled)
                        }
                    }
                    if let p = host.problems[m.id] { Text(p).font(.footnote).foregroundStyle(.red) }
                } footer: {
                    HStack(spacing: 12) {
                        Text("Version \(m.version)")
                        if let d = m.docs, let url = URL(string: d), url.scheme == "https" { Link("Setup guide", destination: url) }
                        Spacer()
                        if let u = host.update(for: m) {
                            Button(host.installing.contains(m.id) ? "Updating…" : "Update to \(u.version)") { host.install(u) }
                                .disabled(host.installing.contains(m.id) || host.needsNewerApp(u))
                        }
                        Button("Uninstall", role: .destructive) { host.uninstall(m.id) }
                    }
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                }
            }
            Section {
                let available = host.market.filter { e in !host.installed.contains { $0.id == e.id } }
                if available.isEmpty, host.marketProblem == nil {
                    Text(host.market.isEmpty ? "Loading…" : "You have everything.").foregroundStyle(.secondary)
                }
                ForEach(available) { e in
                    HStack(alignment: .top, spacing: 10) {
                        ExtensionIcon(symbol: e.symbol)
                        VStack(alignment: .leading, spacing: 3) {
                            Text(e.name).fontWeight(.semibold)
                            Text(e.summary).font(.footnote).foregroundStyle(.secondary)
                            Text(e.permissions.summary).font(.footnote).foregroundStyle(.tertiary)
                            if let p = host.problems[e.id] { Text(p).font(.footnote).foregroundStyle(.red) }
                        }
                        Spacer()
                        Button(host.installing.contains(e.id) ? "Installing…" : "Install") { host.install(e) }
                            .disabled(host.installing.contains(e.id) || host.needsNewerApp(e))
                            .help(host.needsNewerApp(e) ? "Needs Hall Monitor \(e.min_app ?? "") or later" : "")
                    }
                }
                if let p = host.marketProblem { Text(p).font(.footnote).foregroundStyle(.red) }
            } header: {
                Text("Available")
            } footer: {
                VStack(alignment: .leading, spacing: 6) {
                    Footer("Extensions run in a sandbox: they can reach the internet over HTTPS and keep their own files, but can't read your projects or Claude's, run other programs, or answer for you. Their secrets are kept in the Keychain.")
                    Button("Install from File…") { pickFile() }
                        .disabled(host.installing.contains("file"))
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .onAppear { host.loadMarket() }
    }

    private func pickFile() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.init(filenameExtension: "gz")!]
        panel.message = "Choose an extension (.tar.gz). Only install one you trust: it runs in a sandbox, but it's still a program."
        panel.prompt = "Install"
        if panel.runModal() == .OK, let url = panel.url { host.installFile(url) }
    }
}

private struct ExtensionIcon: View {
    let symbol: String?

    var body: some View {
        Image(systemName: symbol.flatMap { NSImage(systemSymbolName: $0, accessibilityDescription: nil) == nil ? nil : $0 } ?? "puzzlepiece.extension")
            .font(.system(size: 14, weight: .medium))
            .foregroundStyle(.white)
            .frame(width: 28, height: 28)
            .background(RoundedRectangle(cornerRadius: 7, style: .continuous).fill(.tint))
    }
}

/// One row of an extension's form, drawn as Settings draws its own.
@MainActor
private struct ExtBlockRow: View {
    let id: String
    let block: ExtBlock
    private var host: ExtensionHost { .shared }

    var body: some View {
        let b = block
        switch b.kind {
        case "status":
            if let l = b.label, !l.isEmpty {
                LabeledContent(l) { Text(b.text ?? "").foregroundStyle(tone(b.tone) ?? .secondary) }
            } else {
                Text(b.text ?? "").font(.footnote).foregroundStyle(tone(b.tone) ?? .secondary).textSelection(.enabled)
            }
        case "input":
            ExtInput(id: id, block: b)
        case "toggle":
            Toggle(b.label ?? "", isOn: Binding(get: { b.value?.bool ?? false }, set: { host.input(id, b.key ?? "", $0) }))
                .disabled(b.disabled == true)
        case "button":
            HStack {
                Spacer()
                if b.primary == true {
                    Button(b.label ?? "") { host.action(id, b.action ?? "") }.buttonStyle(.borderedProminent).disabled(b.disabled == true)
                } else {
                    Button(b.label ?? "") { host.action(id, b.action ?? "") }.disabled(b.disabled == true)
                }
            }
        case "note":
            if b.mono == true {
                Text(b.text ?? "").font(.footnote.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
            } else {
                Text(b.text ?? "").font(.footnote).foregroundStyle(.secondary)
            }
        default:
            EmptyView()
        }
    }

    private func tone(_ t: String?) -> Color? {
        switch t {
        case "ok": return .green
        case "warn": return .orange
        case "error": return .red
        default: return nil
        }
    }
}

/// A text field that sends what you type a beat after you stop, and takes
/// the extension's value only when it isn't what you just sent.
@MainActor
private struct ExtInput: View {
    let id: String
    let block: ExtBlock
    @State private var text = ""
    @State private var sent: String?
    @State private var pending: DispatchWorkItem?

    var body: some View {
        Group {
            if block.secret == true {
                SecureField(block.label ?? "", text: $text, prompt: block.placeholder.map { Text($0) })
            } else {
                TextField(block.label ?? "", text: $text, prompt: block.placeholder.map { Text($0) })
            }
        }
        .disabled(block.disabled == true)
        .onAppear { text = block.value?.string ?? "" }
        .onChange(of: block.value?.string ?? "") { _, v in if v != sent { text = v } }
        .onChange(of: text) { _, v in
            pending?.cancel()
            let w = DispatchWorkItem { MainActor.assumeIsolated { send(v) } }
            pending = w
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3, execute: w)
        }
        .onSubmit { send(text) }
    }

    private func send(_ v: String) {
        guard v != sent, v != (block.value?.string ?? "") || sent != nil else { return }
        sent = v
        ExtensionHost.shared.input(id, block.key ?? "", v)
    }
}

// MARK: About

struct AboutSettings: View {
    private func link(_ title: String, _ url: String) -> some View {
        Link(destination: URL(string: url)!) {
            HStack {
                Text(title).foregroundStyle(.primary)
                Spacer()
                Image(systemName: "arrow.up.right").font(.system(size: 11, weight: .semibold)).foregroundStyle(.secondary)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

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
                LabeledContent("Command line tool") {
                    Button("Show in Finder") {
                        NSWorkspace.shared.selectFile(Board.binary, inFileViewerRootedAtPath: "")
                    }
                    .help(Board.binary)
                }
            } footer: {
                Footer("Run hallmonitor in a terminal to see the board there.")
            }
            Section {
                link("GitHub", "https://github.com/hiteshbandhu/hallmonitor")
                link("Report an issue", "https://github.com/hiteshbandhu/hallmonitor/issues")
            }
        }
    }
}
