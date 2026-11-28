import Foundation
import SwiftUI

/// A message to land on once its chat is open (a hub link's `?at=`, #2696): taken by that chat's view.
struct MessageJump: Equatable {
    var key: SessionKey
    var item: String
    var id = UUID() // the same message linked twice still moves there again
    /// A task's card in the assistant's pager whose View in Chat opened this chat (#12625): `item` is found from it once the
    /// chat's items are in (`CardSource.source`), the message that asked or, without a tool use id, the newest question.
    var card: Interaction? = nil
    #if os(iOS)
    var starred: StarredSource? = nil
    #endif
}

#if os(iOS)
extension AppModel {
    func openStarredMessage(_ star: StarredSnapshot, key: SessionKey) {
        guard star.source.identifies(key) else { return }
        messageJump = MessageJump(key: key, item: star.item_id, starred: star.source)
        if star.conversation == .assistant {
            openAssistantConversation(key, manager: star.source_manager)
        } else {
            open(key)
        }
    }
}
#endif

/// Where a hub link leads in the app (#2696, HubPageLink): which paired group it is a page of, and the key it opens.
extension AppModel {
    /// Every address `group`'s hubs are known by: what was paired, where the socket is, the mesh's endpoints and the
    /// hub's `public_url` (the one its links are written with, often not the address this device uses).
    func hubAddresses(_ group: String) -> [URL] {
        var out: [URL] = []
        let members = registry.members(group)
        out += members.flatMap(\.configs).map(\.url) // the paired address and those the user added
        out += members.flatMap { ($0.mesh ?? []).flatMap { ($0.endpoints ?? []).compactMap(URL.init(string:)) } }
        if let l = link(group) {
            out.append(l.config.url)
            if let u = l.hello.publicURL { out.append(u) }
            out += (l.hello.mesh ?? []).flatMap { ($0.endpoints ?? []).compactMap(URL.init(string:)) }
        }
        return out
    }

    /// The group and page a link is, `preferring` a group when two answer to the same address; nil: not a page of a
    /// paired hub the app has natively (it opens as a web page, as before). An app link (`persona://open<path>`, an older
    /// `persona://` form) is on the hub its `hub=` names, else on `group`, else on the hub-level one.
    func hubPage(_ url: URL, preferring group: String? = nil) -> (group: String, page: HubPage)? {
        if let (page, hubID) = HubPageLink.appPage(url) {
            let named = hubID.flatMap { registry.group(hubID: $0) }
            if hubID != nil && named == nil { return nil } // a hub this device is not paired with
            guard let g = named ?? group ?? hub?.group, link(g)?.isDemo == false else { return nil }
            return (g, page)
        }
        let groups = links.filter { !$0.isDemo }.map(\.group)
        let order = (group.map { [$0] } ?? []) + groups.filter { $0 != group }
        for g in order where link(g) != nil {
            if let p = HubPageLink.page(url, hubs: hubAddresses(g)) { return (g, p) }
        }
        return nil
    }

    /// Whether a port link (a link to the agent's machine, PortLinks) is really one of a paired hub's pages: the hub runs
    /// on that machine, or is the phone's own localhost (a mock hub).
    func isHubPage(_ l: PortLink, hostnames: [String]) -> Bool {
        let scheme = l.secure ? "https" : "http"
        return (["127.0.0.1", "localhost"] + hostnames).contains { host in
            URL(string: "\(scheme)://\(host):\(l.port)\(l.path)").flatMap { hubPage($0) } != nil
        }
    }

    /// The key of a session a link names in `group`: its full id from the session list when the link has a prefix of
    /// one (the web accepts short ids, #2464), else as written.
    func linkedSession(_ group: String, machine: String, session: String) -> SessionKey {
        let sessions = sessionsByMachine[MachineRef(group: group, id: machine)] ?? []
        if !sessions.contains(where: { $0.sessionID == session }) {
            let matches = sessions.filter { $0.sessionID.hasPrefix(session) }
            if session.count >= 6, matches.count == 1 { return SessionKey(group: group, machineID: machine, sessionID: matches[0].sessionID) }
        }
        return SessionKey(group: group, machineID: machine, sessionID: session)
    }

    /// The key of a Persona conversation a link names (`/persona/<id>`): the session of that id in the group's lists, else
    /// on the assistant's machine when the group is the hub-level one; nil when neither says where it is.
    func linkedConversation(_ group: String, id: String) -> SessionKey? {
        for (ref, sessions) in sessionsByMachine where ref.group == group {
            if let s = sessions.first(where: { $0.sessionID == id }) { return SessionKey(group: group, machineID: ref.id, sessionID: s.sessionID) }
        }
        guard group == hub?.group, let config = assistantConfig, config.main == id else { return nil }
        return config.mainKey(group: group)
    }

    /// Opens a chat a link leads to, at `item` when it names one: a Persona conversation in the Persona tab, an agent's
    /// in Agents (as a notification does).
    func openLinked(_ key: SessionKey, item: String?) {
        messageJump = item.map { MessageJump(key: key, item: $0) }
        if session(key)?.assistant == true || key == assistantMain {
            openAssistantConversation(key)
        } else {
            open(key)
        }
    }
}

/// What opening a link to one of Persona's pages did (docs/protocol/deep-links.md, #9926): the app went to the page, or
/// the page rises over the screen the link was tapped on, as a link does.
enum HubLinkOpen: Equatable {
    case opened
    case sheet(HubLinkSheet)
}

/// A page a link opens over the screen it was tapped on: a to-do, a knowledge entry, a computer.
enum HubLinkSheet: Identifiable, Hashable {
    case todo(String)
    case knowledge(KBEntry)
    case machine(MachineRef)

    var id: String {
        switch self {
        case .todo(let id): "todo/" + id
        case .knowledge(let e): "kb/" + e.kind + "/" + e.id
        case .machine(let ref): "machine/" + ref.path
        }
    }
}

extension AppModel {
    /// Opens a link to one of Persona's pages: a web link to a paired hub or an app link (`HubPageLink`). Nil: not one,
    /// or one the app cannot place (another group's to-do, a conversation it does not know); the caller opens it as
    /// before (the web card, the system).
    func openOwnLink(_ url: URL, preferring group: String? = nil) -> HubLinkOpen? {
        guard let (g, page) = hubPage(url, preferring: group) else { return nil }
        return openHubPage(page, group: g)
    }

    /// Goes to `page` of `group`. The hub-level pages (Persona, the Inbox, to-dos, knowledge, Settings) open only for the
    /// hub-level group: a link never switches the hub the app shows.
    func openHubPage(_ page: HubPage, group: String) -> HubLinkOpen? {
        let hubLevel = group == hub?.group
        #if os(iOS)
        if hubLevel, returnToStartIfHidden(page.navigationPage) { return .opened }
        #endif
        switch page {
        case .agent(let machine, let session, let item):
            openLinked(linkedSession(group, machine: machine, session: session), item: item)
        case .conversation(let id, let item):
            guard let key = linkedConversation(group, id: id) else { return nil }
            openLinked(key, item: item)
        case .todo(let id):
            guard TodosLogic.isOnTodosHub(sessionGroup: group, hubGroup: hub?.group) else { return nil }
            return .sheet(.todo(id))
        case .knowledgeEntry(let kind, let id):
            guard hubLevel else { return nil }
            return .sheet(.knowledge(KBEntry(kind: kind, id: id, name: id, relevance: 0)))
        case .machine(let id):
            return .sheet(.machine(MachineRef(group: group, id: id)))
        case .extensionPage(let ext, let ui, let state):
            return openExtension(ExtensionSystemLink(owner: ext, ui: ui, group: group, state: state)) ? .opened : nil
        case .agents:
            selectedTab = .chats
        #if os(iOS)
        case .machines:
            showShellPage(.machines)
        case .persona, .newConversation, .inbox, .inboxItem, .todos, .knowledge, .settings:
            guard hubLevel else { return nil }
            openHubLevel(page)
        #else
        // visionOS has no shell pages (docs/apple/vision.md, "Shell"): these links open as web pages, as before #9993.
        case .machines, .newConversation, .todos, .knowledge:
            return nil
        case .persona, .inbox, .inboxItem, .settings:
            guard hubLevel else { return nil }
            openHubLevel(page)
        #endif
        }
        return .opened
    }

    private func openHubLevel(_ page: HubPage) {
        switch page {
        case .persona:
            if let main = assistantMain { openAssistantConversation(main) } else { selectedTab = .assistant }
        #if os(iOS)
        case .newConversation:
            openNewAssistant()
        #endif
        case .inbox:
            selectedTab = .mail
        case .inboxItem(let id, let manager):
            selectedTab = .mail
            inboxToOpen = InboxOpenRequest(id: id, manager: manager)
        #if os(iOS)
        case .todos:
            showShellPage(.todos)
        case .knowledge:
            showShellPage(.knowledge)
        #endif
        case .settings(let tab, let item):
            openSettings(tab: tab, item: item)
        default:
            break
        }
    }

    /// Settings at a web tab (web/src/views/settings/nav.ts) and its item: the page the app has for it, else Settings.
    private func openSettings(tab: String?, item: String?) {
        if tab == "extensions", let id = item {
            openExtensionSettings(id)
            return
        }
        if tab == "connectors", item == HubPageLink.sentSettings {
            openSentRecords()
            return
        }
        let pages: [SettingsPage] = switch tab ?? "" {
        case "persona": [.assistant]
        case "avatar": [.persona]
        case "models": [.models]
        case "agents": [.agents]
        case "channels": [.channels] + (item.map { [.channel($0)] } ?? [])
        case "connectors": item.map { [.connector($0)] } ?? []
        case "sources": [.inboxSources]
        case "extensions": [.plugins]
        case "notifications": [.notifications]
        case "devices": [.devices]
        case "hub": [.hub]
        case "knowledge": [.knowledgeSelection]
        default: []
        }
        settingsPath = NavigationPath(pages)
        showSettings()
    }
}

/// An Inbox item a link asked to open (`/inbox/<id>`): the Inbox pushes it once it is listed.
struct InboxOpenRequest: Equatable {
    var id: String
    var manager: String?
    var token = UUID() // the same item linked twice opens again
}
