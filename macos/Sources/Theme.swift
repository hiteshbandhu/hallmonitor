import SwiftUI

// MARK: - The look
//
// Dark graphite, like the notch and the icon. Colour means something: the
// icon's teal-to-lime for work in progress, amber for "needs you", each
// provider's own colour for its mark. Everything else stays neutral.

enum Theme {
    static let canvas = Color(red: 0.055, green: 0.059, blue: 0.071)      // #0E0F12
    static let panel = Color(red: 0.086, green: 0.094, blue: 0.114)       // #16181D
    static let panelHi = Color(red: 0.11, green: 0.118, blue: 0.141)      // hover
    static let stroke = Color.white.opacity(0.07)
    static let hairline = Color.white.opacity(0.045)

    static let teal = Color(red: 0.18, green: 0.83, blue: 0.75)           // #2DD4BF
    static let lime = Color(red: 0.64, green: 0.90, blue: 0.21)           // #A3E635
    static let amber = Color(red: 0.96, green: 0.62, blue: 0.04)          // #F59E0B
    static let red = Color(red: 0.97, green: 0.33, blue: 0.33)

    /// The icon's bars: work in progress.
    static let work = LinearGradient(colors: [teal, Color(red: 0.29, green: 0.87, blue: 0.5), lime],
                                     startPoint: .bottomLeading, endPoint: .topTrailing)

    // IBM Plex, bundled (OFL). Sans for text, Mono for IDs and code.
    static func font(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font {
        let name: String
        switch weight {
        case .bold, .heavy, .black: name = "IBMPlexSans-Bold"
        case .semibold: name = "IBMPlexSans-SmBld"
        case .medium: name = "IBMPlexSans-Medm"
        default: name = "IBMPlexSans"
        }
        return .custom(name, fixedSize: size)
    }

    static func mono(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font {
        .custom(weight == .regular ? "IBMPlexMono" : "IBMPlexMono-Medm", fixedSize: size)
    }

    static func display(_ size: CGFloat, _ weight: Font.Weight = .semibold) -> Font { font(size, weight) }

    static func number(_ size: CGFloat) -> Font { font(size, .medium) }
}

/// Small uppercase section label.
struct Eyebrow: View {
    let text: String
    var body: some View {
        Text(text.uppercased())
            .font(Theme.font(10.5, .semibold))
            .tracking(0.9)
            .foregroundStyle(.white.opacity(0.42))
    }
}

/// The surface tiles and cards sit on: a panel with a hairline edge and a
/// faint top highlight, so it reads as a raised sheet in the dark.
struct Panel: View {
    var glow: Color? = nil
    var selected = false
    var hover = false

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: 12, style: .continuous)
        shape
            .fill(hover ? Theme.panelHi : Theme.panel)
            .overlay(shape.strokeBorder(
                LinearGradient(colors: [.white.opacity(0.11), .white.opacity(0.04)], startPoint: .top, endPoint: .bottom),
                lineWidth: 1))
            .overlay {
                if selected { shape.strokeBorder(Theme.teal.opacity(0.9), lineWidth: 1.5) }
                else if let glow { shape.strokeBorder(glow.opacity(0.55), lineWidth: 1) }
            }
    }
}

/// The window's backdrop: graphite with a soft light from above.
struct Backdrop: View {
    var body: some View {
        ZStack {
            Theme.canvas
            RadialGradient(colors: [Color.white.opacity(0.045), .clear], center: .top, startRadius: 0, endRadius: 700)
        }
        .ignoresSafeArea()
    }
}

/// A thin bar of colour down a card's left edge: the status at a glance.
struct StatusRail: View {
    let color: Color

    var body: some View {
        Capsule().fill(color).frame(width: 3)
    }
}

// MARK: - In-window controls (instead of the system toolbar)

/// The top of each pane: a title, a live subtitle, and the pane's controls.
struct PaneHeader<Controls: View>: View {
    let title: String
    let subtitle: String
    @ViewBuilder var controls: Controls

    var body: some View {
        HStack(alignment: .center, spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(Theme.font(22, .semibold)).foregroundStyle(.white)
                Text(subtitle)
                    .font(Theme.font(12))
                    .foregroundStyle(.white.opacity(0.5))
                    .contentTransition(.numericText())
                    .animation(.smooth, value: subtitle)
            }
            Spacer(minLength: 12)
            controls
        }
    }
}

/// A row of options in one rounded track; the chosen one is lit.
struct Chips<T: Hashable>: View {
    let options: [(T, String)]
    @Binding var selection: T
    @Namespace private var ns

    var body: some View {
        HStack(spacing: 2) {
            ForEach(options, id: \.0) { value, label in
                let on = value == selection
                Text(label)
                    .font(Theme.font(12, .medium))
                    .lineLimit(1)
                    .fixedSize()
                    .foregroundStyle(on ? Color.white : Color.white.opacity(0.55))
                    .padding(.horizontal, 12)
                    .frame(height: 26)
                    .background {
                        if on {
                            RoundedRectangle(cornerRadius: 7, style: .continuous)
                                .fill(Color.white.opacity(0.11))
                                .matchedGeometryEffect(id: "chip", in: ns)
                        }
                    }
                    .contentShape(Rectangle())
                    .onTapGesture { withAnimation(.snappy(duration: 0.22)) { selection = value } }
            }
        }
        .padding(3)
        .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(Theme.panel))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Theme.stroke))
    }
}

/// A search field in the theme. ⌘F focuses it (Edit ▸ Find).
struct SearchField: View {
    @Binding var text: String
    var prompt = "Search"
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass").font(.system(size: 11, weight: .semibold))
                .foregroundStyle(.white.opacity(0.45))
            TextField(prompt, text: $text)
                .textFieldStyle(.plain)
                .font(Theme.font(12.5))
                .focused($focused)
                .onExitCommand { text = ""; focused = false }
            if !text.isEmpty {
                Button { text = "" } label: {
                    Image(systemName: "xmark.circle.fill").foregroundStyle(.white.opacity(0.4))
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 10)
        .frame(width: 210, height: 32)
        .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(Theme.panel))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous)
            .strokeBorder(focused ? Theme.teal.opacity(0.7) : Theme.stroke))
        .onReceive(NotificationCenter.default.publisher(for: .focusSearch)) { _ in focused = true }
    }
}

/// A square icon button in the theme.
struct IconButton: View {
    let symbol: String
    var on = false
    let help: String
    let action: () -> Void
    @State private var hover = false

    var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(on ? Theme.teal : Color.white.opacity(hover ? 0.9 : 0.6))
                .frame(width: 32, height: 32)
                .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(hover ? Theme.panelHi : Theme.panel))
                .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Theme.stroke))
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
        .help(help)
    }
}

extension Notification.Name {
    static let focusSearch = Notification.Name("HallMonitorFocusSearch")
}
