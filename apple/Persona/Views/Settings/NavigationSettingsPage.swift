import SwiftUI

#if os(iOS)
/// Settings › Appearance › Navigation (#6332): Separate (Assistant | Agents, the default) or Sidebar (everything in one
/// sidebar, as the web app). Each choice shows an iPhone and an iPad sketched that way. Picking one applies at once:
/// `RootView` reads the same value, and this page stays open (the settings path is the app model's).
struct NavigationSettingsPage: View {
    @StoredValue(NavigationLayout.key) private var layout: NavigationLayout = .separate

    var body: some View {
        SettingsForm {
            Section {
                ForEach(NavigationLayout.allCases) { choice in
                    Button { layout = choice } label: {
                        NavigationChoice(layout: choice, selected: layout == choice)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("navigation-layout-" + choice.rawValue)
                    .accessibilityAddTraits(layout == choice ? .isSelected : [])
                }
            } footer: {
                Text("Only this device. It applies at once; open chats and drafts stay as they are.")
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Navigation"))
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// One way to lay the app out: its name, an iPhone and an iPad sketched that way, and what it does.
private struct NavigationChoice: View {
    var layout: NavigationLayout
    var selected: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                Text(layout.title).font(Theme.Font.bodyStrong).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                if selected {
                    ThemedIcon(Theme.Icon.done).font(Theme.Font.bodyStrong).foregroundStyle(Theme.accent)
                }
            }
            HStack(alignment: .bottom, spacing: Theme.Space.l) {
                NavigationSketch(layout: layout, pad: false)
                NavigationSketch(layout: layout, pad: true)
            }
            .padding(Theme.Space.m)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.groupedPage, in: .rect(cornerRadius: Theme.Radius.card))
            .allowsHitTesting(false)
            .accessibilityHidden(true)
            Text(layout.summary).font(Theme.Font.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.vertical, Theme.Space.xs)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }
}

/// A device drawn in a few strokes: the switch or tabs at the top for Separate; a sidebar with its sections (an
/// accent heading, Needs You's tone) for Sidebar. Not to scale, and the same in every text size.
private struct NavigationSketch: View {
    var layout: NavigationLayout
    var pad: Bool

    var body: some View {
        ZStack(alignment: .topLeading) {
            RoundedRectangle(cornerRadius: pad ? Theme.Radius.control : Theme.Radius.card, style: .continuous)
                .fill(Theme.card)
            HStack(alignment: .top, spacing: Theme.Space.xs) {
                if layout == .sidebar || pad { sidebarColumn }
                page
            }
            .padding(pad && layout == .separate ? .bottom : .all, Theme.Space.xs)
            .padding(.top, pad && layout == .separate ? Theme.Space.l : 0)
            if layout == .separate { switchPill }
        }
        .frame(width: pad ? Theme.Size.box150 : Theme.Size.box76, height: pad ? Theme.Size.box110 : Theme.Size.box140)
        .clipShape(.rect(cornerRadius: pad ? Theme.Radius.control : Theme.Radius.card, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: pad ? Theme.Radius.control : Theme.Radius.card, style: .continuous)
                .strokeBorder(Theme.cardStroke, lineWidth: Theme.Size.line)
        }
    }

    /// Separate's Assistant | Agents: one pill across the top.
    private var switchPill: some View {
        Capsule().fill(Theme.accent)
            .frame(width: Theme.Size.avatar, height: Theme.Size.dot)
            .frame(maxWidth: .infinity)
            .padding(.top, Theme.Space.s)
    }

    /// The sidebar: lines for the rows, an accent one where a section starts, a Needs You one and two cards for Agents.
    private var sidebarColumn: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            line(Theme.Size.wellXL)
            line(Theme.Size.avatarM)
            line(Theme.Size.wellL)
            if layout == .sidebar {
                line(Theme.Size.iconL, tint: Theme.accent)
                line(Theme.Size.avatar)
                line(Theme.Size.iconXS, tint: Theme.needsYou)
                RoundedRectangle(cornerRadius: Theme.Radius.thumbnail).fill(Theme.Fill.chip)
                    .frame(width: Theme.Size.avatarL, height: Theme.Size.markS)
                RoundedRectangle(cornerRadius: Theme.Radius.thumbnail).fill(Theme.Fill.chip)
                    .frame(width: Theme.Size.avatar, height: Theme.Size.markS)
            }
            Spacer(minLength: 0)
        }
        .padding(Theme.Space.xs)
        .frame(width: pad ? Theme.Size.box50 : Theme.Size.box56)
        .frame(maxHeight: .infinity, alignment: .top)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.thumbnail))
    }

    /// The page beside it: a few lines of text, and the composer at its foot on a phone.
    private var page: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if !(layout == .sidebar && !pad) {
                line(Theme.Size.box50)
                line(Theme.Size.box64)
                line(Theme.Size.avatar)
                Spacer(minLength: 0)
                if !pad {
                    Capsule().fill(Theme.Fill.chip).frame(height: Theme.Size.markM)
                }
            }
        }
        .padding(.top, layout == .separate && !pad ? Theme.Space.xl : Theme.Space.xs)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func line(_ width: CGFloat, tint: Color = Theme.Fill.chip) -> some View {
        Capsule().fill(tint).frame(width: width, height: Theme.Size.dotXS)
    }
}
#endif
