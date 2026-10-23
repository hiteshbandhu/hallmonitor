import AppKit
import SwiftUI

// MARK: - Answering from the notch
//
// `hallmonitor hook` (a Claude Code hook) parks a question in
// ~/.local/share/hallmonitor/asks/<id>.json and waits; the live feed attaches
// it to its session. The notch shows it; clicking an answer writes
// <id>.answer.json, which the hook hands back to Claude Code.

struct AskOption: Decodable, Hashable {
    var label: String
    var description: String?
}

struct AskQuestion: Decodable, Hashable {
    var question: String
    var header: String?
    var options: [AskOption]
    var multiSelect: Bool?
}

struct PendingAsk: Decodable, Hashable {
    var id: String
    var session_id: String
    var kind: String           // "question" | "permission"
    var questions: [AskQuestion]?
    var tool: String?
    var detail: String?
    var complete: Bool?
    var form: [FormBlock]?
    var form_complete: Bool?
    var always: [String]?
    var deadline: Date?

    /// Allow approves all of it: only offered when all of it is on screen,
    /// as the form (when there is one) or the plain detail.
    var canAllow: Bool { form?.isEmpty == false ? form_complete == true : complete == true }
}

/// One piece of a permission form, laid out by the hook (ask.Block).
struct FormBlock: Decodable, Hashable {
    var kind: String
    var label: String?
    var text: String?
    var icon: String?
    var mono: Bool?
    var diff: [DiffLine]?
    var items: [Chip]?

    struct DiffLine: Decodable, Hashable { var op: String; var text: String }
    struct Chip: Decodable, Hashable { var text: String; var warn: Bool? }
}

enum Asks {
    static var dir: String {
        let env = ProcessInfo.processInfo.environment["XDG_DATA_HOME"] ?? ""
        let base = env.isEmpty ? NSHomeDirectory() + "/.local/share" : env
        return base + "/hallmonitor/asks"
    }

    /// Tells the hook the app is up and wants questions (it only waits
    /// while this is fresh).
    static func heartbeat() {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true,
                                                 attributes: [.posixPermissions: 0o700])
        // Yours alone, as the hook expects; it ignores the notch otherwise.
        try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: dir)
        let p = dir + "/.app-alive"
        if !FileManager.default.fileExists(atPath: p) {
            FileManager.default.createFile(atPath: p, contents: nil, attributes: [.posixPermissions: 0o600])
        }
        try? FileManager.default.setAttributes([.modificationDate: Date()], ofItemAtPath: p)
    }

    /// The shape the hook gives a question's id (ask.NewID). The id names
    /// the answer file, so anything else is never written.
    static func validID(_ id: String) -> Bool {
        id.range(of: "^[0-9]{14}-[0-9a-f]{16}$", options: .regularExpression) != nil
    }

    /// A tool as people read it (ask.ToolLabel in the CLI): an MCP tool's
    /// mcp__server__tool_name as ("Tool name", "server").
    static func toolLabel(_ tool: String) -> (name: String, server: String) {
        guard tool.hasPrefix("mcp__") else { return (tool, "") }
        let rest = tool.dropFirst(5)
        guard let sep = rest.range(of: "__") else { return (tool, "") }
        let server = String(rest[..<sep.lowerBound]), raw = String(rest[sep.upperBound...])
        let words = raw.split(whereSeparator: { $0 == "_" || $0 == "-" }).joined(separator: " ")
        guard !server.isEmpty, !words.isEmpty else { return (tool, "") }
        return (words.prefix(1).uppercased() + words.dropFirst(), server)
    }

    /// action: "answer" (with answers), "allow", "deny" or "pass".
    static func reply(_ id: String, action: String, answers: [String: [String]] = [:], always: Int? = nil) {
        guard validID(id) else { return }
        var obj: [String: Any] = ["action": action]
        if !answers.isEmpty { obj["answers"] = answers }
        if let always { obj["always"] = always }
        guard let data = try? JSONSerialization.data(withJSONObject: obj) else { return }
        let tmp = dir + "/\(id).answer.json.tmp"
        FileManager.default.createFile(atPath: tmp, contents: data, attributes: [.posixPermissions: 0o600])
        try? FileManager.default.moveItem(atPath: tmp, toPath: dir + "/\(id).answer.json")
    }
}

/// A first guess at the card's height, until it has measured itself.
func askHeight(_ a: PendingAsk, index: Int) -> CGFloat {
    let header: CGFloat = 46, footer: CGFloat = 30, pad: CGFloat = 18
    if a.kind == "permission" {
        if let form = a.form, !form.isEmpty {
            // A first guess only: the card measures itself right after.
            let text = form.reduce(0) { $0 + ($1.text?.count ?? 0) / 56 + ($1.diff?.count ?? 0) + 1 }
            return header + min(420, CGFloat(form.count) * 22 + CGFloat(text) * 15) + 38 + CGFloat(a.always?.count ?? 0) * 26 + footer + pad
        }
        let lines = min(3, max(1, ((a.detail ?? "").count + 55) / 56))
        return header + CGFloat(lines) * 16 + 18 + 38 + footer + pad
    }
    guard let qs = a.questions, !qs.isEmpty else { return 120 }
    let q = qs[min(index, qs.count - 1)]
    let qLines = min(3, max(1, (q.question.count + 51) / 52))
    let rowH: CGFloat = q.options.contains { !($0.description ?? "").isEmpty } ? 46 : 36
    var h = header + CGFloat(qLines) * 18 + 12 + CGFloat(q.options.count) * (rowH + 6) + 38 + footer + pad
    if q.header != nil { h += 20 }
    if q.multiSelect == true { h += 42 }
    return h
}

struct AskCard: View {
    let session: Session
    let ask: PendingAsk
    @ObservedObject var model: NotchModel
    @State private var index = 0
    @State private var picked: [String: Set<String>] = [:]
    @State private var other = ""
    @State private var typing = false
    @FocusState private var otherFocused: Bool

    var body: some View {
        // The island sizes to what the card measures, up to most of the
        // screen; past that the card scrolls.
        ScrollView(.vertical, showsIndicators: false) {
            VStack(alignment: .leading, spacing: 10) {
                header
                if ask.kind == "permission" { permission } else { question }
                footer
            }
            .padding(.horizontal, 16)
            .padding(.top, 6)
            .padding(.bottom, 12)
            .fixedSize(horizontal: false, vertical: true)
            .background(GeometryReader { g in
                Color.clear.preference(key: AskHeightKey.self, value: g.size.height)
            })
        }
        .onPreferenceChange(AskHeightKey.self) { h in
            MainActor.assumeIsolated { model.measured(ask.id, index: index, height: h) }
        }
    }

    // MARK: pieces

    private var header: some View {
        HStack(spacing: 10) {
            Image(nsImage: ProjectIcon.badged(cwd: session.cwd, host: session.host, provider: session.provider, size: 26))
                .interpolation(.high)
                .frame(width: 26, height: 26)
            VStack(alignment: .leading, spacing: 1) {
                Text(session.name)
                    .font(.system(size: 12.5, weight: .semibold))
                    .foregroundStyle(.white)
                    .lineLimit(1)
                Group {
                    if ask.kind == "permission" {
                        let (name, server) = Asks.toolLabel(ask.tool ?? "a tool")
                        // An MCP tool by its name in words, then the server
                        // it comes from, quieter.
                        Text("wants to use \(name)")
                            + Text(server.isEmpty ? "" : " · \(server)").foregroundColor(.white.opacity(0.45))
                    } else {
                        Text("has a question")
                    }
                }
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(Color(red: 1, green: 0.66, blue: 0.2))
                .lineLimit(1)
                .help(ask.tool ?? "")
            }
            Spacer(minLength: 8)
            if let d = ask.deadline {
                ClockText { now in
                    Text(remaining(d.timeIntervalSince(now)))
                        .font(.system(size: 11, weight: .medium).monospacedDigit())
                        .foregroundStyle(.white.opacity(0.4))
                }
                .help("Then Claude asks you the usual way")
            }
        }
        .frame(height: 40)
    }

    @ViewBuilder
    private var question: some View {
        if let qs = ask.questions, !qs.isEmpty {
            let q = qs[min(index, qs.count - 1)]
            let multi = q.multiSelect == true
            VStack(alignment: .leading, spacing: 6) {
                if let h = q.header, !h.isEmpty {
                    Text(h.uppercased())
                        .font(.system(size: 9.5, weight: .bold))
                        .tracking(0.6)
                        .foregroundStyle(.white.opacity(0.45))
                }
                Text(q.question)
                    .font(.system(size: 13.5, weight: .semibold))
                    .foregroundStyle(.white)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .padding(.bottom, 6)
                ForEach(q.options, id: \.self) { o in
                    OptionRow(option: o, multi: multi, on: picked[q.question, default: []].contains(o.label)) {
                        choose(q, o.label)
                    }
                }
                if typing {
                    HStack(spacing: 6) {
                        TextField("Type an answer", text: $other)
                            .textFieldStyle(.plain)
                            .font(.system(size: 12.5))
                            .foregroundStyle(.white)
                            .focused($otherFocused)
                            .onSubmit { if !other.isEmpty { choose(q, other, typed: true) } }
                        Button("Send") { if !other.isEmpty { choose(q, other, typed: true) } }
                            .buttonStyle(NotchButton(kind: .primary))
                            .disabled(other.isEmpty)
                    }
                    .padding(.horizontal, 10)
                    .frame(height: 36)
                    .background(RoundedRectangle(cornerRadius: 9, style: .continuous).fill(.white.opacity(0.08)))
                } else {
                    Button {
                        typing = true
                        model.wantsKeyboard = true
                        DispatchQueue.main.async { otherFocused = true }
                    } label: {
                        Label("Something else…", systemImage: "pencil")
                            .font(.system(size: 12, weight: .medium))
                            .foregroundStyle(.white.opacity(0.6))
                            .frame(maxWidth: .infinity, minHeight: 32, alignment: .leading)
                            .padding(.horizontal, 10)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
                if multi {
                    Button("Send") { next(q) }
                        .buttonStyle(NotchButton(kind: .primary))
                        .disabled(picked[q.question, default: []].isEmpty)
                        .frame(maxWidth: .infinity, alignment: .trailing)
                }
            }
        }
    }

    private var permission: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let form = ask.form, !form.isEmpty {
                PermissionForm(blocks: form)
            } else {
                Text(ask.detail ?? "")
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(.white.opacity(0.9))
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(10)
                    .background(RoundedRectangle(cornerRadius: 9, style: .continuous).fill(.white.opacity(0.07)))
                    .textSelection(.enabled)
            }
            HStack(spacing: 8) {
                if !ask.canAllow {
                    Text("Too long to show in full: allow it in Claude")
                        .font(.system(size: 11, weight: .medium))
                        .foregroundStyle(.white.opacity(0.5))
                }
                Spacer()
                Button("Deny") { finish("deny") }.buttonStyle(NotchButton(kind: .secondary))
                if ask.canAllow {
                    Button("Allow") { finish("allow") }.buttonStyle(NotchButton(kind: .primary))
                }
            }
            // Claude Code's own "don't ask again" choices, as it words them.
            if ask.canAllow, let always = ask.always, !always.isEmpty {
                VStack(alignment: .trailing, spacing: 2) {
                    ForEach(Array(always.enumerated()), id: \.offset) { i, label in
                        Button {
                            Asks.reply(ask.id, action: "always", always: i)
                            model.answered(ask.id)
                        } label: {
                            // Never cut short: this is a standing grant, read
                            // all of it (the hook keeps it short anyway).
                            Label(label, systemImage: "checkmark.seal")
                                .font(.system(size: 11, weight: .medium))
                                .foregroundStyle(.white.opacity(0.6))
                                .fixedSize(horizontal: false, vertical: true)
                                .multilineTextAlignment(.trailing)
                        }
                        .buttonStyle(.plain)
                        .help("Allow, and don’t ask again")
                    }
                }
                .frame(maxWidth: .infinity, alignment: .trailing)
            }
        }
    }

    private var footer: some View {
        HStack {
            if let qs = ask.questions, qs.count > 1 {
                Text("\(min(index, qs.count - 1) + 1) of \(qs.count)")
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(.white.opacity(0.4))
            }
            Spacer()
            Button { finish("pass") } label: {
                Text("Answer in \(session.extra?["entrypoint"] == "claude-desktop" ? "Claude" : "the terminal")")
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(.white.opacity(0.5))
            }
            .buttonStyle(.plain)
            .help("Close this; Claude asks you there instead")
        }
        .frame(height: 18)
    }

    // MARK: actions

    private func choose(_ q: AskQuestion, _ label: String, typed: Bool = false) {
        if q.multiSelect == true && !typed {
            var s = picked[q.question, default: []]
            if s.contains(label) { s.remove(label) } else { s.insert(label) }
            picked[q.question] = s
            return
        }
        picked[q.question] = [label]
        next(q)
    }

    private func next(_ q: AskQuestion) {
        let qs = ask.questions ?? []
        typing = false
        other = ""
        if index + 1 < qs.count {
            withAnimation(.snappy) { index += 1 }
            model.layoutAsk(index: index)
            return
        }
        var answers: [String: [String]] = [:]
        for q in qs { answers[q.question] = Array(picked[q.question, default: []]).sorted() }
        Asks.reply(ask.id, action: "answer", answers: answers)
        model.answered(ask.id)
    }

    private func finish(_ action: String) {
        Asks.reply(ask.id, action: action)
        model.answered(ask.id)
    }

    private func remaining(_ t: TimeInterval) -> String {
        let s = max(0, Int(t))
        return String(format: "%d:%02d", s / 60, s % 60)
    }
}

private struct OptionRow: View {
    let option: AskOption
    let multi: Bool
    let on: Bool
    let action: () -> Void
    @State private var hover = false

    var body: some View {
        Button(action: action) {
            HStack(spacing: 10) {
                if multi {
                    Image(systemName: on ? "checkmark.circle.fill" : "circle")
                        .font(.system(size: 14))
                        .foregroundStyle(on ? Color.green : .white.opacity(0.4))
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(option.label)
                        .font(.system(size: 12.5, weight: .semibold))
                        .foregroundStyle(.white)
                        .lineLimit(3)
                        .fixedSize(horizontal: false, vertical: true)
                    if let d = option.description, !d.isEmpty {
                        Text(d)
                            .font(.system(size: 11))
                            .foregroundStyle(.white.opacity(0.55))
                            .lineLimit(5)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: 0)
                if !multi {
                    Image(systemName: "arrow.right")
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundStyle(.white.opacity(hover ? 0.8 : 0.25))
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, minHeight: (option.description ?? "").isEmpty ? 36 : 46, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 9, style: .continuous)
                .fill(.white.opacity(on ? 0.16 : hover ? 0.12 : 0.07)))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
    }
}

struct NotchButton: ButtonStyle {
    enum Kind { case primary, secondary }
    let kind: Kind
    @Environment(\.isEnabled) private var enabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(kind == .primary ? Color.black : .white)
            .padding(.horizontal, 14)
            .frame(height: 30)
            .background(Capsule().fill(kind == .primary ? Color(red: 0.33, green: 0.86, blue: 0.5) : .white.opacity(0.12)))
            .opacity(enabled ? (configuration.isPressed ? 0.75 : 1) : 0.4)
    }
}

private struct AskHeightKey: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}
