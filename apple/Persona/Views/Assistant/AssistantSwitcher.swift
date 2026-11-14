import SwiftUI

// The assistant switcher (docs/assistant/managers.md "Clients"): whenever the hub supplies a roster with an installed
// current assistant, including a single assistant, the sidebar's header names it (the interface's word for a manager)
// with whether it is online and where it runs, and opens the hub's assistants as a list: one row each with its state, the
// current one checked, the default marked only when there are several installed assistants, one still installing with its
// code's time left, and beside every other one than the current how much waits for the user there (`AssistantBadges`: its
// agents waiting and its inbox's Needs You), then New Assistant and Manage Assistants… (Settings › Assistants). A
// single-assistant roster therefore has one row without Default, followed by those two actions. The header's face carries
// a mark while another assistant has something waiting. Picking one makes it the current assistant on this device
// (`AppModel.selectAssistantManager`, kept for each hub across launches); picking one still installing shows how to
// install it. Without a roster, or without an installed current assistant, the sidebar keeps the old Persona header.

/// What the switcher says and how it marks an assistant, kept apart from the views to test.
enum AssistantSwitcherText {
    /// Under a name: online and where it runs, offline and when it last synced, or installing and its code's time left.
    static func line(_ m: HubManager, now: Date) -> String {
        if m.installing == nil, m.online, let host = m.host, !host.isEmpty { return String(localized: "Online · \(host)") }
        return ManagersText.status(m, now: now).text
    }

    /// The rows in the list's order: the installed ones, the default first, then those still installing.
    static func rows(_ roster: ManagerRoster) -> [HubManager] {
        roster.installed + roster.managers.filter { $0.installing != nil }
    }

    /// Which of an avatar's colors a manager gets, the same on every client: the default's is the assistant's own
    /// (nil), any other's the sum of its id's UTF-8 bytes modulo the five hues (blue, green, orange, pink, indigo).
    static func hue(_ m: HubManager, defaultID: String) -> Int? {
        m.id == defaultID ? nil : m.id.utf8.reduce(0) { $0 + Int($1) } % 5
    }

    static func color(_ m: HubManager, defaultID: String) -> Color {
        guard let hue = hue(m, defaultID: defaultID) else { return Theme.ai }
        return [Theme.Hue.blue, Theme.Hue.green, Theme.Hue.orange, Theme.Hue.pink, Theme.Hue.indigo][hue]
    }

    /// What VoiceOver says for a row: the name, its line, what marks it, and how much waits there (`waiting`; the
    /// current one's row shows none).
    static func spoken(_ m: HubManager, now: Date, current: Bool, isDefault: Bool, waiting: Int = 0) -> String {
        var parts = [m.name, line(m, now: now)]
        if isDefault { parts.append(String(localized: "Default [Prompt Composer 844b8c]")) }
        if current { parts.append(String(localized: "Current")) }
        if !current, waiting > 0 { parts.append(AssistantBadges.spoken(waiting)) }
        return parts.joined(separator: ", ")
    }

    /// What VoiceOver says for the header: the current assistant and its line, and that another one has something
    /// waiting while the mark shows.
    static func spokenHeader(_ m: HubManager, now: Date, othersWaiting: Bool) -> String {
        var parts = [m.name, line(m, now: now)]
        if othersWaiting { parts.append(String(localized: "Another assistant has something waiting for you")) }
        return parts.joined(separator: ", ")
    }

    /// Settings' full scope title and the current assistant's line; its header draws no other-assistant waiting dot.
    static func spokenSettingsHeader(title: String, manager m: HubManager, now: Date) -> String {
        [title, line(m, now: now)].joined(separator: ", ")
    }
}

/// An assistant's avatar in the switcher's list: its name's first letter on a solid disc in its color, with its state's
/// dot at the disc's foot, as on every client. The letter and the dot's ring are the list's own surface color (`surface`):
/// white on the disc in light mode, and dark on the lighter dark-mode colors, where white would not read. `fade`: an
/// offline row's (`Theme.Fade.offline`).
struct AssistantMonogram: View {
    var manager: HubManager
    var defaultID: String
    var now: Date
    var surface: Color = Theme.card
    var fade: Double = 1
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let color = AssistantSwitcherText.color(manager, defaultID: defaultID)
        // The disc grows with the text as the rows do (`scaledFrame`); its letter stops growing first, so it stays inside.
        Monogram(name: manager.name, size: Theme.Size.wellL * Theme.sizeScale(typeSize), fill: color.opacity(fade), ink: surface,
                 font: Theme.Font.bodyStrong)
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .overlay(alignment: .bottomTrailing) {
                AssistantStateDot(tone: ManagersText.status(manager, now: now).tone, ring: surface, fade: fade)
            }
            .accessibilityHidden(true)
    }
}

/// An assistant's state at the foot of its avatar (the Persona face in the header, a row's disc): green online, gray
/// offline, orange while it is still being installed, ringed in the color of what it sits on (`ring`) so it stands apart
/// from the avatar. Settings › Assistants keeps `managerDot` beside its words.
struct AssistantStateDot: View {
    var tone: ManagersText.Tone
    var ring: Color = Theme.pageBackground
    var fade: Double = 1

    var body: some View {
        Circle()
            .fill(Self.color(tone).opacity(fade))
            .overlay(Circle().strokeBorder(ring, lineWidth: Theme.Size.lineMedium))
            .scaledFrame(width: Theme.Size.dotXL, height: Theme.Size.dotXL)
            .accessibilityHidden(true)
    }

    static func color(_ tone: ManagersText.Tone) -> Color {
        switch tone {
        case .online: Theme.positive
        case .installing: Theme.Hue.orange
        case .offline: Theme.Hue.gray
        }
    }
}

/// On the sidebar's header while another assistant than the current one has something waiting (its count is in the
/// list): at the head of the face, or beside the name where the face isn't drawn.
struct AssistantWaitingDot: View {
    var ring: Color = Theme.pageBackground

    var body: some View {
        Circle()
            .fill(Theme.needsYou)
            .overlay(Circle().strokeBorder(ring, lineWidth: Theme.Size.lineMedium))
            .scaledFrame(width: Theme.Size.dotXL, height: Theme.Size.dotXL)
            .accessibilityHidden(true)
    }
}

/// The sidebar's header by default: the Persona face (the one picked in Appearance), the current assistant's name with
/// an arrow, and its state. Settings uses `identityStyle` for its named scope heading with the same popover. A tap
/// opens the list. `compact`: one line without the face, for a title bar's row (the Mac).
struct AssistantSwitcherHeader: View {
    enum IdentityStyle {
        case sidebar
        case settings(title: String, defaultID: String)
    }

    @Environment(AppModel.self) private var model
    var manager: HubManager
    var nameFont: Font
    var compact = false
    var showsSwitchLabel = false
    var identityStyle: IdentityStyle = .sidebar
    /// Opens Settings on the page `AppModel.settingsPath` names.
    var openSettings: () -> Void

    @State private var listShown = false
    /// What the list asked for that presents something else, run once the list is gone (`afterList`).
    @State private var next: Next?
    @State private var page = ManagersModel()
    @State private var naming = false
    @State private var newName = ""

    /// What a row or an action of the list presents once the list has gone away.
    private enum Next: Equatable {
        case create, manage
        case code(HubManager)
    }

    var body: some View {
        // Read here, not in the label alone: the spoken name says it too.
        let othersWaiting = model.othersWaiting
        let showsFace = !compact
        Button { listShown.toggle() } label: {
            HStack(spacing: Theme.Space.m) {
                switch identityStyle {
                case .sidebar:
                    if showsFace {
                        PersonaAvatar(size: Theme.Size.avatar)
                            .overlay(alignment: .bottomTrailing) { AssistantStateDot(tone: ManagersText.status(manager, now: .now).tone) }
                            .overlay(alignment: .topTrailing) { if othersWaiting { AssistantWaitingDot() } }
                    }
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        HStack(spacing: Theme.Space.xs) {
                            Text(manager.name).font(nameFont).foregroundStyle(ink).lineLimit(1).minimumScaleFactor(0.7)
                            ThemedIcon(Theme.Icon.expand).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                            if othersWaiting && !showsFace { AssistantWaitingDot() }
                        }
                        if !compact {
                            Text(AssistantSwitcherText.line(manager, now: .now)).font(Theme.Font.label).foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                    }
                case let .settings(title, defaultID):
                    AssistantMonogram(manager: manager, defaultID: defaultID, now: .now, surface: Theme.pageBackground)
                    Text(title).font(nameFont).foregroundStyle(ink).lineLimit(1).minimumScaleFactor(0.7)
                }
                if showsSwitchLabel {
                    Spacer(minLength: Theme.Space.s)
                    Text("Switch").font(Theme.Font.labelStrong).lineLimit(1)
                }
            }
            // A finger's target at least (the Mac's one line keeps its title row's height).
            .scaledFrame(minHeight: compact ? nil : Theme.Size.tapTarget)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(spokenHeader(othersWaiting: othersWaiting))
        .accessibilityHint("Switches assistants")
        .accessibilityAddTraits(.isHeader)
        .accessibilityIdentifier(switchIdentifier)
        .popover(isPresented: $listShown, arrowEdge: .top) {
            AssistantSwitcherList(pick: pick, create: { afterList(.create) }, manage: { afterList(.manage) })
                .presentationCompactAdaptation(.popover)
                // A solid panel, as on the other clients: through glass the sidebar's badges showed beside the rows.
                .presentationBackground(Theme.card)
                // Gone, not going: UIKit refuses another presentation while the popover is still on its way out.
                .onDisappear(perform: listGone)
        }
        .alert("New Assistant", isPresented: $naming) {
            TextField("For example: Work", text: $newName)
            Button("Create") { Task { await page.create(name: newName) } }
                .disabled(!ManagersText.validName(newName))
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("You get an install code. Install it on the computer that will run it within 10 minutes.")
        }
        .managerCodeSheet(page)
        .managerFailures(page)
        #if DEBUG
        .task { // `-showAssistantSwitcher YES`: the list open (screenshots, UI tests)
            guard UserDefaults.standard.bool(forKey: "showAssistantSwitcher") else { return }
            try? await Task.sleep(for: .seconds(1))
            listShown = true
        }
        #endif
    }

    private func spokenHeader(othersWaiting: Bool) -> String {
        switch identityStyle {
        case .sidebar:
            return AssistantSwitcherText.spokenHeader(manager, now: .now, othersWaiting: othersWaiting)
        case let .settings(title, _):
            return AssistantSwitcherText.spokenSettingsHeader(title: title, manager: manager, now: .now)
        }
    }

    private var switchIdentifier: String {
        switch identityStyle {
        case .sidebar: return "assistant-switcher"
        case .settings: return "settings-assistant-switch"
        }
    }

    /// The name's color: the Mac's title rows have their own label color.
    private var ink: Color {
        #if os(macOS)
        Theme.macLabel
        #else
        Color.primary
        #endif
    }

    /// Closes the list and keeps what presents something else (an alert, a sheet, Settings) for when it is gone.
    private func afterList(_ what: Next) {
        next = what
        listShown = false
    }

    /// The list went away: what it asked for, if anything (a tap outside asks for nothing).
    private func listGone() {
        guard let what = next else { return }
        next = nil
        switch what {
        case .create:
            newName = ""
            naming = true
        case .manage:
            model.settingsPath = NavigationPath([SettingsPage.managers])
            openSettings()
        case .code(let m):
            Task { await page.showCode(m) }
        }
    }

    /// An installed one becomes the current assistant; one still installing shows how to install it.
    private func pick(_ m: HubManager) {
        if m.installing != nil {
            afterList(.code(m))
            return
        }
        listShown = false
        if m.id != model.currentAssistant?.id {
            model.selectAssistantManager(m.id)
            model.toast = Toast(String(localized: "Switched to \(m.name)"), style: .success)
        }
    }
}

/// The hub's assistants as the switcher lists them, then New Assistant and Manage Assistants….
private struct AssistantSwitcherList: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var typeSize
    var pick: (HubManager) -> Void
    var create: () -> Void
    var manage: () -> Void

    var body: some View {
        // The rows' "code expires in n min" and "last synced …" move on without a push.
        TimelineView(.periodic(from: .now, by: 15)) { clock in
            VStack(spacing: 0) {
                if let roster = model.managerRoster {
                    let current = model.currentAssistant?.id
                    let badges = model.assistantBadges
                    ScrollView {
                        VStack(spacing: 0) {
                            ForEach(AssistantSwitcherText.rows(roster)) { m in
                                row(m, roster: roster, current: m.id == current, waiting: badges[m.id] ?? 0, now: clock.date)
                            }
                        }
                    }
                    .scrollBounceBehavior(.basedOnSize)
                    // On the rows' list only: a container's identifier covers those of the buttons in it.
                    .accessibilityIdentifier("assistant-switcher-list")
                    Divider().padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xs)
                }
                action(String(localized: "New Assistant"), Theme.Icon.add, tint: Theme.accent, id: "assistant-switcher-new", run: create)
                action(String(localized: "Manage Assistants…"), Theme.Icon.settings, tint: nil, id: "assistant-switcher-manage", run: manage)
            }
            .padding(Theme.Space.s)
            // As wide as the text needs (`scaledFrame`), but able to give: a popover wider than the screen is cut at both
            // edges, so the screen's width wins at the largest text sizes.
            .frame(idealWidth: width, maxWidth: width)
        }
    }

    private var width: CGFloat { Theme.Size.box300 * Theme.sizeScale(typeSize) }

    /// `waiting`: what waits for the user at it (`AppModel.assistantBadges`), shown beside any but the current one.
    private func row(_ m: HubManager, roster: ManagerRoster, current: Bool, waiting: Int, now: Date) -> some View {
        // Default only beside another one: with one assistant there is nothing to tell apart.
        let isDefault = m.id == roster.defaultID && m.installing == nil && roster.isMulti
        // An offline one's whole row is greyed, not off: its pages show what was last read of it. In the colors, not the
        // row's opacity, which the popover's vibrant text doesn't take.
        let fade = m.installing == nil && !m.online ? Theme.Fade.offline : 1
        return Button { pick(m) } label: {
            HStack(spacing: Theme.Space.m) {
                AssistantMonogram(manager: m, defaultID: roster.defaultID, now: now, fade: fade)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    NameWithChips {
                        Text(m.name).font(Theme.Font.bodyStrong).foregroundStyle(Color.primary.opacity(fade)).lineLimit(1)
                    } chips: {
                        if isDefault { Chip(text: String(localized: "Default [Prompt Composer 844b8c]"), look: .tint(Theme.accent)).opacity(fade) }
                    }
                    Text(AssistantSwitcherText.line(m, now: now)).font(Theme.Font.label).foregroundStyle(Color.secondary.opacity(fade))
                        .lineLimit(1)
                }
                Spacer(minLength: Theme.Space.s)
                if current {
                    ThemedIcon(Theme.Icon.done).font(Theme.Font.bodyStrong).foregroundStyle(Theme.accent.opacity(fade))
                } else if waiting > 0 {
                    CountBadge(count: waiting, look: .needs)
                }
            }
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, Theme.Space.xs)
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(AssistantSwitcherText.spoken(m, now: now, current: current, isDefault: isDefault, waiting: waiting))
        .accessibilityAddTraits(current ? .isSelected : [])
        .accessibilityIdentifier("assistant-switcher-row-\(m.id)")
    }

    private func action(_ title: String, _ symbol: String, tint: Color?, id: String, run: @escaping () -> Void) -> some View {
        Button(action: run) {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(symbol)
                    .font(Theme.Font.bodyStrong)
                    .foregroundStyle(tint ?? Color.primary)
                    .scaledFrame(width: Theme.Size.wellL, height: Theme.Size.wellL)
                    .background((tint ?? Color.primary).opacity(0.15), in: .circle)
                Text(title).font(tint == nil ? Theme.Font.body : Theme.Font.bodyStrong).foregroundStyle(tint ?? Color.primary).lineLimit(1)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, Theme.Space.xs)
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(id)
    }
}

/// In place of the list on a page of an offline assistant's things the hub merges (the inbox, the to-dos): a merged
/// list leaves an offline manager's items out, so the page says the assistant is offline and since when, not that there
/// is nothing (docs/assistant/managers.md "Offline is not empty"). Under its avatar with the offline mark, as the
/// switcher's list draws it and as the other clients' pages do.
struct AssistantOfflineNotice: View {
    @Environment(AppModel.self) private var model
    var manager: HubManager
    /// The surface it sits on: the avatar's letter and its mark's ring take its color.
    var surface: Color = Theme.pageBackground

    static func title(_ m: HubManager) -> String { String(localized: "\(m.name) Is Offline") }

    static func message(_ m: HubManager) -> String {
        guard let seen = m.lastSeen else { return String(localized: "This updates when it's back online.") }
        let when = seen.formatted(.relative(presentation: .named, unitsStyle: .wide))
        return String(localized: "Last synced \(when). This updates when it's back online.")
    }

    var body: some View {
        // Laid out as `EmptyStateView`, with the assistant's avatar where that has its symbol.
        VStack(spacing: Theme.Space.l) {
            AssistantMonogram(manager: manager, defaultID: model.managerRoster?.defaultID ?? ManagerRoster.assistant, now: .now,
                              surface: surface, fade: Theme.Fade.offline)
            Text(Self.title(manager)).font(Theme.Font.title).multilineTextAlignment(.center)
            Text(Self.message(manager))
                .font(Theme.Font.secondary)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(Theme.Space.xxl)
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("assistant-offline-notice")
    }
}

/// Over the composer of an offline manager's conversation, or of its page's new conversation: nothing goes to it until it
/// is back, and since when it last synced.
struct ManagerOfflineNote: View {
    var manager: HubManager

    /// "<name> · last synced <time>", and that messages can go once it is back.
    static func text(_ m: HubManager) -> String {
        guard let seen = m.lastSeen else { return String(localized: "\(m.name) is offline. Messages can be sent once it's back.") }
        let when = seen.formatted(.relative(presentation: .named, unitsStyle: .wide))
        return String(localized: "\(m.name) · last synced \(when). Messages can be sent once it's back.")
    }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.offline).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
            Text(Self.text(manager)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(3)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("manager-offline-note")
    }
}
