#if os(iOS)
import Foundation
import Observation

nonisolated enum HubNavigationPage: String, Sendable {
    case inbox, todos
}

/// The hub's sidebar arrangement. The phone only reads the visibility of its Inbox and To-dos pages.
@MainActor @Observable final class HubPageVisibility {
    nonisolated private struct Arrangement: Decodable {
        struct Pages: Decodable { var hidden: Set<String>? }
        var pages: Pages?
    }

    /// Nil before the first completed read; an empty set is a result, including the old-hub fallback.
    private var hidden: Set<HubNavigationPage>?
    private var group: String?
    private var link: ObjectIdentifier?
    @ObservationIgnored private var reading: Task<Void, Never>?
    @ObservationIgnored private var readID = 0
    @ObservationIgnored private(set) var selection = 0

    var hasRead: Bool { hidden != nil }

    func hiddenPages(in group: String?, link: ObjectIdentifier? = nil) -> Set<HubNavigationPage> {
        guard group == self.group, link == self.link else { return [] }
        return hidden ?? []
    }

    func follow(_ group: String?, link: ObjectIdentifier? = nil) {
        guard group != self.group || link != self.link else { return }
        selection &+= 1
        readID &+= 1
        reading?.cancel()
        reading = nil
        self.group = group
        self.link = link
        hidden = nil
    }

    /// A foreground transition, a hello and a link open can all ask at once. Coalesce reads for the current hub.
    @discardableResult func refresh(_ api: APIClient, link: ObjectIdentifier? = nil) -> Task<Void, Never> {
        follow(api.group, link: link)
        if let reading, !reading.isCancelled { return reading }
        readID &+= 1
        let id = readID
        let reading = Task {
            defer { if self.readID == id { self.reading = nil } }
            let pages: Set<HubNavigationPage>
            do {
                let data = try await api.request("GET", "/api/v1/navigation", timeout: 5)
                let saved = try JSONDecoder().decode(Arrangement.self, from: data)
                pages = Set((saved.pages?.hidden ?? []).compactMap(HubNavigationPage.init(rawValue:)))
            } catch {
                pages = [] // An older hub or failed read leaves the existing navigation available.
            }
            guard !Task.isCancelled, self.readID == id else { return }
            if hidden != pages { hidden = pages }
        }
        self.reading = reading
        return reading
    }
}

extension AppModel {
    var hiddenNavigationPages: Set<HubNavigationPage> {
        guard let hub else { return [] }
        return pageVisibility.hiddenPages(in: hub.group, link: ObjectIdentifier(hub))
    }

    @discardableResult func refreshPageVisibility() -> Task<Void, Never>? {
        guard let hub, !hub.isDemo else {
            pageVisibility.follow(nil)
            return nil
        }
        return pageVisibility.refresh(hub.api.showing(nil), link: ObjectIdentifier(hub))
    }

    /// A widget or shortcut can arrive before protected pairing data is available. Keep only the latest tap for the
    /// selected group until the registry becomes readable. A known unpaired device stays on the pairing screen.
    @discardableResult func openNavigationEntry(_ page: HubPage) -> Task<Void, Never>? {
        guard let hub else {
            if registryUnreadable { deferredNavigationEntry = (page, hubGroup) }
            return nil
        }
        return openNavigationLink(page, group: hub.group)
    }

    @discardableResult func resumeNavigationEntry() -> Task<Void, Never>? {
        guard let entry = deferredNavigationEntry else { return nil }
        deferredNavigationEntry = nil
        return openNavigationLink(entry.page, group: entry.group)
    }

    /// Once a read has landed for this hub, route immediately. The refresh can reconcile the destination afterward.
    @discardableResult func openNavigationLink(_ page: HubPage, group: String) -> Task<Void, Never>? {
        guard let hub, hub.group == group else { return nil }
        let reading = refreshPageVisibility()
        if reading == nil || pageVisibility.hasRead {
            routeNavigationLink(page, group: group)
            return reading
        }
        let selection = pageVisibility.selection
        return Task {
            await reading?.value
            guard !Task.isCancelled, self.hub === hub, pageVisibility.selection == selection, pageVisibility.hasRead else { return }
            routeNavigationLink(page, group: group)
        }
    }

    private func routeNavigationLink(_ page: HubPage, group: String) {
        if case .sheet(let page) = openHubPage(page, group: group) { linkSheet = page }
    }

    @discardableResult func returnToStartIfHidden(_ page: HubNavigationPage?) -> Bool {
        guard let page, hiddenNavigationPages.contains(page) else { return false }
        inboxToOpen = nil
        linkSheet = nil
        showShellPage(.conversation)
        return true
    }
}

extension HubPage {
    var navigationPage: HubNavigationPage? {
        switch self {
        case .inbox, .inboxItem: .inbox
        case .todos, .todo: .todos
        default: nil
        }
    }
}

extension ShellPage {
    var navigationPage: HubNavigationPage? {
        switch self {
        case .inbox: .inbox
        case .todos: .todos
        default: nil
        }
    }
}
#endif
