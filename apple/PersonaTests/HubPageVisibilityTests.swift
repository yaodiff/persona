#if os(iOS)
import Foundation
import Testing
@testable import Persona

@MainActor @Suite(.serialized) struct HubPageVisibilityTests {
    private static let path = "/api/v1/navigation"
    private static let hidden = #"{"pages":{"hidden":["todos","inbox","web-only"]}}"#

    @Test func refreshHidesPagesAndShowsThemAgain() async {
        let hub = FakeHub(), visibility = HubPageVisibility()
        hub.on("GET", Self.path, json: Self.hidden)
        let first = visibility.refresh(hub.client)
        let same = visibility.refresh(hub.client)
        await first.value
        await same.value
        #expect(hub.sent("GET", Self.path).count == 1)
        #expect(visibility.hiddenPages(in: "") == [.inbox, .todos])

        hub.on("GET", Self.path, json: #"{"pages":{"hidden":[]}}"#)
        await visibility.refresh(hub.client).value
        #expect(visibility.hiddenPages(in: "").isEmpty)
    }

    @Test(arguments: [404, 503]) func failedRefreshShowsEveryPage(status: Int) async {
        let hub = FakeHub(), visibility = HubPageVisibility()
        hub.on("GET", Self.path, json: Self.hidden)
        await visibility.refresh(hub.client).value
        #expect(visibility.hiddenPages(in: "") == [.inbox, .todos])

        hub.on("GET", Self.path) { _ in throw APIError(status: status, message: "Unavailable") }
        await visibility.refresh(hub.client).value
        #expect(visibility.hiddenPages(in: "").isEmpty)
    }

    @Test func aLateResponseCannotCrossHubSelections() async {
        let first = FakeHub(), second = FakeHub(), visibility = HubPageVisibility()
        let gate = ReadGate()
        first.onAsync("GET", Self.path) { _ in await gate.hold() }
        second.on("GET", Self.path, json: #"{"pages":{"hidden":["inbox"]}}"#)
        var secondClient = second.client
        secondClient.group = "other"
        let old = visibility.refresh(first.client)
        await gate.arrival()

        await visibility.refresh(secondClient).value
        #expect(visibility.hiddenPages(in: "").isEmpty)
        #expect(visibility.hiddenPages(in: "other") == [.inbox])
        gate.release(Data(Self.hidden.utf8))
        await old.value
        #expect(visibility.hiddenPages(in: "other") == [.inbox])
    }

    @Test func quickActionsKeepTheirOrderAndOmitHiddenInbox() {
        #expect(QuickAction.offered(recording: .on, hidden: [.inbox, .todos]) == [.meetingRecording, .newConversation])
        #expect(QuickAction.offered(recording: .off, hidden: [.todos]) == [.newConversation, .inbox])
    }

    @Test func launchRoutesWaitForVisibilityAndReturnToStart() async throws {
        let hub = FakeHub(), gate = ReadGate()
        hub.onAsync("GET", Self.path) { _ in await gate.hold() }
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let app = AppModel(stateFolder: folder)
        app.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        app.connectOnly(ServerConfig(url: hub.url, token: "sample"))
        let router = PadRouter(unified: true)
        router.attach(to: app)
        app.selectedTab = .chats
        let inbox = try #require(app.openNavigationLink(.inboxItem(id: "example", manager: nil), group: ""))
        await gate.arrival()
        #expect(app.selectedTab == .chats)
        gate.release(Data(Self.hidden.utf8))
        await inbox.value
        #expect(app.inboxToOpen == nil)
        #expect(app.selectedTab == .assistant)
        #expect(router.request?.page == .conversation)

        for path in ["/inbox", "/todos", "/todos/example", "/todos?id=example"] {
            let url = try #require(URL(string: "persona://open\(path)"))
            #expect(app.openOwnLink(url) == .opened)
            #expect(router.request?.page == .conversation)
        }
        app.pageVisibility.follow(nil)
        #expect(app.openHubPage(.todo(id: "example"), group: "") == .sheet(.todo("example")))
        #expect(app.openHubPage(.inbox, group: "") == .opened)
        #expect(router.request?.page == .inbox)
    }

    @Test func warmLinkRoutesBeforeItsRefreshReturns() async throws {
        let hub = FakeHub(), gate = ReadGate()
        hub.on("GET", Self.path, json: #"{"pages":{"hidden":[]}}"#)
        let (app, router) = try makeApp(hub)
        app.connectOnly(ServerConfig(url: hub.url, token: "sample"))
        await app.refreshPageVisibility()?.value
        hub.onAsync("GET", Self.path) { _ in await gate.hold() }
        app.selectedTab = .chats

        let refresh = try #require(app.openNavigationLink(.inbox, group: ""))
        #expect(router.request?.page == .inbox)
        await gate.arrival()
        gate.release(Data(Self.hidden.utf8))
        await refresh.value
        #expect(app.hiddenNavigationPages == [.inbox, .todos])
    }

    @Test func cancellingAnOlderReadLeavesItsReplacementInFlight() async {
        let hub = FakeHub(), visibility = HubPageVisibility()
        let first = ReadGate(), second = ReadGate()
        hub.onAsync("GET", Self.path) { _ in await first.hold() }
        let oldRead = visibility.refresh(hub.client)
        await first.arrival()
        oldRead.cancel()
        #expect(!visibility.hasRead)

        hub.onAsync("GET", Self.path) { _ in await second.hold() }
        let replacement = visibility.refresh(hub.client)
        // An uncooperative transport can finish the cancelled read after its replacement has started.
        first.release(Data(Self.hidden.utf8))
        await oldRead.value
        if replacement == oldRead {
            Issue.record("Refresh reused a cancelled read")
            return
        }
        await second.arrival()
        #expect(!visibility.hasRead)
        #expect(visibility.refresh(hub.client) == replacement)
        second.release(Data(#"{"pages":{"hidden":["todos"]}}"#.utf8))
        await replacement.value
        #expect(visibility.hasRead)
        #expect(visibility.hiddenPages(in: "") == [.todos])
    }

    @Test func aNewHubLinkInTheSameGroupStartsWithoutAnAnswer() async {
        let hub = FakeHub(), visibility = HubPageVisibility()
        let oldLink = ReadGate(), newLink = ReadGate()
        hub.on("GET", Self.path, json: Self.hidden)
        await visibility.refresh(hub.client, link: ObjectIdentifier(oldLink)).value
        #expect(visibility.hasRead)
        #expect(visibility.hiddenPages(in: "", link: ObjectIdentifier(newLink)).isEmpty)
        visibility.follow("", link: ObjectIdentifier(newLink))
        #expect(!visibility.hasRead)
    }

    @Test(arguments: [true, false]) func coldEntryWaitsForTheSavedPairing(shortcut: Bool) async throws {
        let hub = FakeHub(), gate = ReadGate()
        hub.onAsync("GET", Self.path) { _ in await gate.hold() }
        var loaded = HubStore.Loaded.unreadable
        let suite = "HubPageVisibilityTests." + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let (app, router) = try makeApp(hub, read: { loaded }, defaults: defaults)
        app.hubGroup = "saved"
        app.selectedTab = .chats
        if shortcut {
            #expect(QuickAction.perform(QuickAction.inbox.item, in: app))
        } else {
            app.openWidget(.todos)
        }
        #expect(app.deferredNavigationEntry?.group == "saved")
        #expect(app.selectedTab == .chats)
        app.retryRegistry()
        #expect(app.deferredNavigationEntry != nil)
        loaded = .registry(HubRegistry(hubs: [paired(hub, group: "saved")]))
        app.retryRegistry()
        #expect(app.deferredNavigationEntry == nil)
        let visibility = try #require(app.refreshPageVisibility())
        await gate.arrival()
        #expect(app.selectedTab == .chats)
        gate.release(Data(#"{"pages":{"hidden":[]}}"#.utf8))
        await visibility.value
        let expected = shortcut ? ShellPage.inbox : .todos
        for _ in 0..<100 where router.request?.page != expected { await Task.yield() }
        #expect(router.request?.page == expected)
    }

    @Test(arguments: [true, false]) func coldEntryFollowsThePreviouslySelectedFallback(shortcut: Bool) async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: #"{"pages":{"hidden":[]}}"#)
        let suite = "HubPageVisibilityTests." + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        defaults.set("", forKey: AppModel.hubGroupKey)
        let primary = paired(hub, group: ""), remaining = paired(hub, group: "spare")
        let both = HubRegistry(hubs: [primary, remaining])
        let (previous, _) = try makeApp(hub, read: { .registry(both) }, defaults: defaults)
        #expect(previous.hub?.group == "")
        // Rebuild from the registry after disconnect, as removeGroup does, without writing the real keychain.
        let afterDisconnect = HubRegistry(hubs: [remaining])
        previous.registry = afterDisconnect
        previous.buildLinks()
        #expect(previous.hub?.group == "spare")
        #expect(defaults.string(forKey: AppModel.hubGroupKey) == "spare")

        var loaded = HubStore.Loaded.unreadable
        let (relaunched, router) = try makeApp(hub, read: { loaded }, defaults: defaults)
        relaunched.selectedTab = .chats
        if shortcut {
            #expect(QuickAction.perform(QuickAction.inbox.item, in: relaunched))
        } else {
            relaunched.openWidget(.todos)
        }
        #expect(relaunched.deferredNavigationEntry?.group == "spare")
        loaded = .registry(afterDisconnect)
        relaunched.retryRegistry()
        let expected: ShellPage = shortcut ? .inbox : .todos
        let read = try #require(relaunched.refreshPageVisibility())
        await read.value
        for _ in 0..<100 where router.request?.page != expected { await Task.yield() }
        #expect(relaunched.deferredNavigationEntry == nil)
        #expect(router.request?.page == expected)
    }

    @Test func aDeferredEntryDoesNotFollowADifferentHubOrLaterPairing() throws {
        let hub = FakeHub()
        var loaded = HubStore.Loaded.unreadable
        let suite = "HubPageVisibilityTests." + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let (app, router) = try makeApp(hub, read: { loaded }, defaults: defaults)
        app.hubGroup = "missing"
        app.selectedTab = .chats
        app.openWidget(.inbox)
        loaded = .registry(HubRegistry(hubs: [paired(hub, group: "other")]))
        app.retryRegistry()
        #expect(app.hub?.group == "other")
        #expect(app.deferredNavigationEntry == nil)
        #expect(router.request?.page != .inbox)

        let (unpaired, _) = try makeApp(hub, read: { .registry(HubRegistry()) })
        unpaired.openWidget(.inbox)
        #expect(unpaired.deferredNavigationEntry == nil)
    }

    private func makeApp(_ hub: FakeHub, read: @escaping () -> HubStore.Loaded = { .registry(HubRegistry()) },
                         defaults: UserDefaults = .standard) throws -> (AppModel, PadRouter) {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let app = AppModel(stateFolder: folder, readHubRegistry: read, hubSelectionDefaults: defaults)
        app.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        let router = PadRouter(unified: true)
        router.attach(to: app)
        return (app, router)
    }

    private func paired(_ hub: FakeHub, group: String) -> PairedHub {
        PairedHub(config: ServerConfig(url: hub.url, token: "sample"), group: group, deviceID: "sample")
    }

    private final class ReadGate {
        private var response: CheckedContinuation<Data, Never>?
        private var awaitingArrival: CheckedContinuation<Void, Never>?

        func hold() async -> Data {
            await withCheckedContinuation { continuation in
                response = continuation
                awaitingArrival?.resume()
                awaitingArrival = nil
            }
        }

        func arrival() async {
            if response == nil { await withCheckedContinuation { awaitingArrival = $0 } }
        }

        func release(_ data: Data) {
            response?.resume(returning: data)
            response = nil
        }
    }
}
#endif
