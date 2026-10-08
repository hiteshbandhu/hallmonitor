import Charts
import SwiftUI

// MARK: - Data: `hallmonitor usage --days N --json`

struct Named: Decodable, Hashable {
    var name: String
    var provider: String?
    var counters: Counters
}

struct DayTotal: Decodable {
    var date: String
    var by_provider: [String: Counters]?
}

struct FullSummary: Decodable {
    var total: Counters
    var by_provider: [String: Counters]?
    var sessions: Int?
    var days: [DayTotal]?
    var heat: [[Double]]?
    var projects: [Named]?
    var models: [Named]?
    var tools: [Named]?
    var rate_limits: [String: RateLimit]?
}

extension Counters: Hashable {
    static func == (a: Counters, b: Counters) -> Bool {
        a.active_s == b.active_s && a.prompts == b.prompts && a.tools == b.tools && a.tokens?.total == b.tokens?.total
    }
    func hash(into h: inout Hasher) { h.combine(active_s); h.combine(prompts); h.combine(tokens?.total) }
}

@MainActor
final class UsageStore: ObservableObject {
    @Published var days = 7 { didSet { if days != oldValue { load() } } }
    @Published private(set) var summary: [Int: FullSummary] = [:]
    @Published private(set) var loading = false
    private let board: Board
    private var timer: Timer?

    init(board: Board) {
        self.board = board
    }

    var current: FullSummary? { summary[days] }

    /// Loads the selected range (and keeps it fresh while the window is up).
    func load() {
        fetch(days)
        if timer == nil {
            timer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
                MainActor.assumeIsolated { if let self { self.fetch(self.days) } }
            }
        }
    }

    private func fetch(_ n: Int) {
        loading = summary[n] == nil
        let p = board.shellCommand("usage --days \(n) --json")
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        p.terminationHandler = { [weak self] _ in
            let data = out.fileHandleForReading.readDataToEndOfFile()
            let s = try? Board.decoder.decode(FullSummary.self, from: data)
            guard let self else { return }
            Task { @MainActor in
                if let s { withAnimation(.smooth) { self.summary[n] = s } }
                self.loading = false
            }
        }
        try? p.run()
    }
}

// MARK: - The view

struct UsageView: View {
    @ObservedObject var store: UsageStore

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                PaneHeader(title: "Usage", subtitle: store.loading ? "Updating…" : "From your agents’ own logs") {
                    Chips(options: [(1, "Today"), (7, "7 days"), (30, "30 days")], selection: $store.days)
                }
                if let s = store.current {
                    KPIs(s: s)
                    HStack(alignment: .top, spacing: 14) {
                        DailyChart(s: s, days: store.days)
                        Limits(limits: (s.rate_limits ?? [:]).sorted { $0.key < $1.key }.map(\.value))
                            .frame(width: 300)
                    }
                    Heatmap(heat: s.heat ?? [])
                    HStack(alignment: .top, spacing: 14) {
                        TopList(title: "Projects", items: s.projects ?? [], value: { $0.active_s ?? 0 }, format: hoursText)
                        TopList(title: "Models", items: s.models ?? [], value: { Double($0.tokens?.total ?? 0) },
                                format: { humanTokens(Int64($0)) })
                        TopList(title: "Tools", items: s.tools ?? [], value: { Double($0.tools ?? 0) },
                                format: { "\(Int($0))" })
                    }
                } else {
                    ProgressView("Reading your agents’ logs…")
                        .frame(maxWidth: .infinity, minHeight: 400)
                }
            }
            .padding(24)
        }
        .background(Backdrop())
    }
}

private struct KPIs: View {
    let s: FullSummary

    var body: some View {
        let t = s.total
        let tok = t.tokens
        let read: Int64 = tok?.cache_read ?? 0
        let fresh: Int64 = (tok?.in ?? 0) + (tok?.cache_write ?? 0)
        let cache: Double = Double(read) / Double(max(1, read + fresh))
        HStack(spacing: 12) {
            KPI(title: "Agent time", value: t.active_s ?? 0, text: hoursText(t.active_s ?? 0), symbol: "clock.fill", tint: .green)
            KPI(title: "Tokens", value: Double(tok?.total ?? 0), text: humanTokens(tok?.total ?? 0), symbol: "circle.hexagongrid.fill", tint: .purple)
            KPI(title: "Prompts", value: Double(t.prompts ?? 0), text: "\(t.prompts ?? 0)", symbol: "text.bubble.fill", tint: .blue)
            KPI(title: "Tool calls", value: Double(t.tools ?? 0), text: abbreviated(t.tools ?? 0), symbol: "hammer.fill", tint: .orange)
            KPI(title: "Cache hits", value: cache, text: "\(Int((cache * 100).rounded()))%", symbol: "bolt.horizontal.fill", tint: .teal)
        }
    }

    private func abbreviated(_ n: Int64) -> String {
        n >= 10_000 ? String(format: "%.1fk", Double(n) / 1000) : "\(n)"
    }
}

private struct KPI: View {
    let title: String
    let value: Double
    let text: String
    let symbol: String
    let tint: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label { Eyebrow(text: title) } icon: {
                Image(systemName: symbol).font(Theme.font(10)).foregroundStyle(.white.opacity(0.42))
            }
            Text(text)
                .font(Theme.number(26))
                .monospacedDigit()
                .contentTransition(.numericText(value: value))
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Card())
        .animation(.spring(response: 0.45, dampingFraction: 0.85), value: value)
    }
}

private struct DayBar: Identifiable {
    var day: Date
    var provider: String
    var hours: Double
    var id: String { "\(day.timeIntervalSince1970)|\(provider)" }
}

private struct DailyChart: View {
    let s: FullSummary
    let days: Int

    private var bars: [DayBar] {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd"
        return (s.days ?? []).flatMap { d -> [DayBar] in
            guard let day = f.date(from: d.date) else { return [] }
            return ["claude", "codex", "opencode"].map { p in
                DayBar(day: day, provider: p, hours: (d.by_provider?[p]?.active_s ?? 0) / 3600)
            }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(days == 1 ? "Agent time today" : "Agent time per day").font(Theme.display(14))
            Chart(bars) { b in
                BarMark(x: .value("Day", b.day, unit: .day), y: .value("Hours", b.hours))
                    .foregroundStyle(by: .value("Provider", Provider.name(b.provider)))
                    .cornerRadius(4)
            }
            .chartForegroundStyleScale([Provider.name("claude"): Palette.provider("claude"),
                                        Provider.name("codex"): Palette.provider("codex"),
                                        Provider.name("opencode"): Palette.provider("opencode")])
            .chartYAxis {
                AxisMarks { v in
                    AxisGridLine().foregroundStyle(.quaternary)
                    AxisValueLabel { if let h = v.as(Double.self) { Text("\(Int(h))h") } }
                }
            }
            .chartXAxis {
                AxisMarks(values: .stride(by: .day, count: days > 10 ? 5 : 1)) { _ in
                    AxisValueLabel(format: .dateTime.weekday(.abbreviated).day(), centered: true)
                }
            }
            .chartLegend(position: .top, alignment: .trailing)
            .frame(height: 220)
            .animation(.spring(response: 0.5, dampingFraction: 0.85), value: bars.map(\.hours))
        }
        .padding(16)
        .frame(maxWidth: .infinity)
        .background(Card())
    }
}

private struct Limits: View {
    let limits: [RateLimit]

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Plan limits").font(Theme.display(14))
            if limits.isEmpty {
                Text("Claude and Codex share these with Hall Monitor as you use them.")
                    .font(Theme.font(12.5))
                    .foregroundStyle(.secondary)
                Spacer(minLength: 0)
            }
            ForEach(Array(limits.enumerated()), id: \.offset) { _, l in
                HStack(spacing: 14) {
                    ZStack {
                        Ring(value: l.used_percent / 100, color: Palette.limit(l.used_percent), width: 7)
                        Text("\(Int(l.used_percent.rounded()))%")
                            .font(Theme.number(12))
                            .monospacedDigit()
                            .contentTransition(.numericText(value: l.used_percent))
                    }
                    .frame(width: 52, height: 52)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("\(Provider.name(l.provider)) · \(window(l))").font(Theme.font(12.5, .medium))
                        if let r = l.resets_at {
                            Text("Resets \(r.formatted(.relative(presentation: .named)))")
                                .font(Theme.font(11)).foregroundStyle(.secondary)
                        }
                        if l.isStale, let at = l.observed_at {
                            Text("as of \(at.formatted(.relative(presentation: .named)))")
                                .font(Theme.font(10)).foregroundStyle(.tertiary)
                        }
                    }
                }
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, minHeight: 268, alignment: .topLeading)
        .background(Card())
    }

    private func window(_ l: RateLimit) -> String {
        switch l.windowLabel {
        case "5h": return "5-hour"
        case "7d": return "weekly"
        case "30d": return "monthly"
        default: return l.windowLabel
        }
    }
}

private struct HeatCell: Identifiable {
    var day: Int
    var hour: Int
    var value: Double
    var id: Int { day * 24 + hour }
}

private struct Heatmap: View {
    let heat: [[Double]]
    private let days = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]

    var body: some View {
        let cells = heat.enumerated().flatMap { d, row in row.enumerated().map { HeatCell(day: d, hour: $0, value: $1) } }
        let peak = max(1, cells.map(\.value).max() ?? 1)
        VStack(alignment: .leading, spacing: 10) {
            Text("When your agents work").font(Theme.display(14))
            Chart(cells) { c in
                RectangleMark(xStart: .value("Hour", Double(c.hour) + 0.06), xEnd: .value("Hour", Double(c.hour) + 0.94),
                              y: .value("Day", days[c.day]), height: .ratio(0.8))
                    .foregroundStyle(c.value == 0 ? AnyShapeStyle(Color.white.opacity(0.05))
                                                  : AnyShapeStyle(Theme.lime.opacity(0.18 + 0.82 * (c.value / peak))))
                    .cornerRadius(3)
            }
            .chartYScale(domain: days)
            .chartXScale(domain: 0...24)
            .chartXAxis {
                AxisMarks(values: [0, 6, 12, 18]) { v in
                    AxisValueLabel { if let h = v.as(Int.self) { Text(h == 0 ? "12am" : h == 12 ? "12pm" : h < 12 ? "\(h)am" : "\(h - 12)pm") } }
                }
            }
            .frame(height: 190)
        }
        .padding(16)
        .background(Card())
    }
}

private struct TopList: View {
    let title: String
    let items: [Named]
    let value: (Counters) -> Double
    let format: (Double) -> String

    var body: some View {
        let top = Array(items.prefix(6))
        let peak = max(1, top.map { value($0.counters) }.max() ?? 1)
        VStack(alignment: .leading, spacing: 10) {
            Text(title).font(Theme.display(14))
            if top.isEmpty {
                Text("Nothing yet").font(Theme.font(12.5)).foregroundStyle(.secondary)
            }
            ForEach(top, id: \.self) { n in
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text(display(n)).font(Theme.font(12.5)).lineLimit(1)
                        Spacer()
                        Text(format(value(n.counters))).font(Theme.font(12.5).monospacedDigit()).foregroundStyle(.secondary)
                    }
                    GeometryReader { g in
                        Capsule().fill(n.provider.map { AnyShapeStyle(Palette.provider($0)) } ?? AnyShapeStyle(Theme.work))
                            .frame(width: max(4, g.size.width * value(n.counters) / peak))
                    }
                    .frame(height: 5)
                }
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, minHeight: 230, alignment: .topLeading)
        .background(Card())
        .animation(.spring(response: 0.5, dampingFraction: 0.85), value: top)
    }

    private func display(_ n: Named) -> String {
        let name = n.name.hasPrefix("claude-") ? Model.display(n.name) : n.name
        return (name as NSString).lastPathComponent
    }
}
