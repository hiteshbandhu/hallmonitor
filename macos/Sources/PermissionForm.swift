import SwiftUI

// MARK: - A permission prompt, laid out per tool
//
// The hook turns a tool's input into blocks (ask.Block): a command as code,
// an edit as a diff, a URL as a headline. Every field of the input is in
// one of them, so this shows all of what Allow approves; it only decides
// how each piece looks.

struct PermissionForm: View {
    let blocks: [FormBlock]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, b in
                block(b)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private func block(_ b: FormBlock) -> some View {
        switch b.kind {
        case "headline": Headline(text: b.text ?? "", icon: b.icon)
        case "code": Code(label: b.label, text: b.text ?? "")
        case "text": Prose(label: b.label, text: b.text ?? "")
        case "note": Note(text: b.text ?? "")
        case "diff": Diff(lines: b.diff ?? [])
        case "chips": ChipRow(label: b.label, items: b.items ?? [])
        default: Field(label: b.label ?? "", text: b.text ?? "", mono: b.mono == true)
        }
    }
}

private let box = RoundedRectangle(cornerRadius: 9, style: .continuous)

/// Small caps over a block, like a form's field label.
private struct Caption: View {
    let text: String
    var body: some View {
        Text(text.uppercased())
            .font(.system(size: 9.5, weight: .bold))
            .tracking(0.5)
            .foregroundStyle(.white.opacity(0.42))
    }
}

/// What the call is about: a file, a URL, a search. A path reads as its
/// folder, quieter, then the file; a URL as its site, then the rest.
private struct Headline: View {
    let text: String
    let icon: String?

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 7) {
            if let icon {
                Image(systemName: icon)
                    .font(.system(size: 11.5, weight: .semibold))
                    .foregroundStyle(.white.opacity(0.5))
            }
            styled
                .font(.system(size: 12.5, weight: .medium, design: looksLikePath ? .monospaced : .default))
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
        }
    }

    private var looksLikePath: Bool { text.hasPrefix("/") || text.hasPrefix("~") || text.contains("://") }

    private var styled: Text {
        let dim = Color.white.opacity(0.5)
        if let r = text.range(of: "://") {
            let afterScheme = text[r.upperBound...]
            let host = afterScheme.prefix { $0 != "/" }
            let rest = afterScheme.dropFirst(host.count)
            return Text(text[..<r.upperBound]).foregroundColor(dim)
                + Text(host).foregroundColor(.white).bold()
                + Text(rest).foregroundColor(dim)
        }
        if looksLikePath, let slash = text.lastIndex(of: "/"), slash != text.index(before: text.endIndex) {
            let dir = text[...slash], name = text[text.index(after: slash)...]
            return Text(dir).foregroundColor(dim) + Text(name).foregroundColor(.white).bold()
        }
        return Text(text).foregroundColor(.white)
    }
}

/// What runs, or data, exactly as written.
private struct Code: View {
    let label: String?
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let label, !label.isEmpty { Caption(text: label) }
            Text(text)
                .font(.system(size: 12, design: .monospaced))
                .foregroundStyle(.white.opacity(0.92))
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(10)
                .background(box.fill(.white.opacity(0.07)))
                .textSelection(.enabled)
        }
    }
}

/// Words for a model or a subagent: a prompt.
private struct Prose: View {
    let label: String?
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let label, !label.isEmpty { Caption(text: label) }
            Text(text)
                .font(.system(size: 12))
                .foregroundStyle(.white.opacity(0.85))
                .lineSpacing(2)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(10)
                .background(box.strokeBorder(.white.opacity(0.1)))
                .textSelection(.enabled)
        }
    }
}

/// Claude's own description of the call: set apart, so it can't pass for
/// what actually runs.
private struct Note: View {
    let text: String

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            Image(systemName: "text.bubble")
                .font(.system(size: 10.5))
            Text(text)
                .italic()
                .fixedSize(horizontal: false, vertical: true)
        }
        .font(.system(size: 11.5))
        .foregroundStyle(.white.opacity(0.55))
        .help("Claude’s description of the command")
    }
}

/// An edit: removed lines red, added green, kept lines plain.
private struct Diff: View {
    let lines: [FormBlock.DiffLine]

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(lines.enumerated()), id: \.offset) { _, l in
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(l.op == " " ? " " : l.op)
                        .foregroundStyle(color(l.op).opacity(0.8))
                        .frame(width: 8)
                    Text(l.text.isEmpty ? " " : l.text)
                        .foregroundStyle(l.op == " " ? .white.opacity(0.55) : color(l.op))
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .font(.system(size: 11.5, design: .monospaced))
                .padding(.horizontal, 8)
                .padding(.vertical, 1.5)
                .background(background(l.op))
            }
        }
        .padding(.vertical, 6)
        .background(box.fill(.white.opacity(0.05)))
        .clipShape(box)
        .textSelection(.enabled)
    }

    private func color(_ op: String) -> Color {
        switch op {
        case "-": return Color(red: 1, green: 0.58, blue: 0.56)
        case "+": return Color(red: 0.55, green: 0.93, blue: 0.62)
        default: return .white
        }
    }

    private func background(_ op: String) -> Color {
        switch op {
        case "-": return Color(red: 1, green: 0.3, blue: 0.3).opacity(0.14)
        case "+": return Color(red: 0.3, green: 0.9, blue: 0.45).opacity(0.12)
        default: return .clear
        }
    }
}

/// Short facts about the call; risky ones in red.
private struct ChipRow: View {
    let label: String?
    let items: [FormBlock.Chip]

    var body: some View {
        // A named list (an MCP tool's labels, say) sits in a row like a field.
        if let label, !label.isEmpty {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text(label)
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(.white.opacity(0.45))
                    .frame(minWidth: 64, alignment: .leading)
                chips.frame(maxWidth: .infinity, alignment: .leading)
            }
        } else {
            chips
        }
    }

    private var chips: some View {
        Flow(spacing: 5) {
            ForEach(Array(items.enumerated()), id: \.offset) { _, c in
                HStack(spacing: 4) {
                    if c.warn == true { Image(systemName: "exclamationmark.triangle.fill").font(.system(size: 9)) }
                    Text(c.text)
                }
                .font(.system(size: 10.5, weight: .medium))
                .foregroundStyle(c.warn == true ? Color(red: 1, green: 0.62, blue: 0.58) : .white.opacity(0.7))
                .padding(.horizontal, 8)
                .frame(height: 20)
                .background(Capsule().fill(c.warn == true ? Color.red.opacity(0.22) : .white.opacity(0.08)))
            }
        }
    }
}

/// A name and a short value.
private struct Field: View {
    let label: String
    let text: String
    let mono: Bool

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text(label)
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(.white.opacity(0.45))
                .frame(minWidth: 64, alignment: .leading)
            Text(text)
                .font(.system(size: 12, design: mono ? .monospaced : .default))
                .foregroundStyle(.white.opacity(0.92))
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
        }
    }
}

/// Lays chips out in rows, wrapping when a row is full.
private struct Flow: Layout {
    var spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, row: CGFloat = 0, widest: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(.unspecified)
            if x > 0, x + size.width > width {
                y += row + spacing
                x = 0
                row = 0
            }
            x += size.width + spacing
            row = max(row, size.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: min(widest, width), height: y + row)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, row: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(.unspecified)
            if x > bounds.minX, x + size.width > bounds.maxX {
                y += row + spacing
                x = bounds.minX
                row = 0
            }
            s.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            row = max(row, size.height)
        }
    }
}
