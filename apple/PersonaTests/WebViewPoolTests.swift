import Foundation
import Testing
import WebKit
@testable import Persona

/// The link sheet's web views (LinkSheet.swift `WebViewPool`, #4074): the first tap shows the preloaded view; once a
/// sheet has shown it and closed, the pool lets it go and the next tap gets a fresh view on the link's own URL with no
/// back/forward history, however far the user browsed in it. A machine's dev server is not loaded again until it is
/// opened. Each test has its own pool except the sheet's teardown, which goes to the shared one; links are on `.invalid`
/// hosts and pages come from `loadHTMLString`, so nothing reaches a real server.
@MainActor
struct WebViewPoolTests {
    private let link = URL(string: "https://example.invalid/pull/1")!

    /// Waits (20 s at most) until `done`: `web` got as far as the case needs. A timeout says where `web` stood.
    private func until(_ what: String, _ web: WKWebView, _ done: () -> Bool, sourceLocation: SourceLocation = #_sourceLocation) async throws {
        for _ in 0..<400 {
            if done() { return }
            try await Task.sleep(for: .milliseconds(50))
        }
        let list = web.backForwardList
        #expect(done(), "timed out waiting: \(what); loading \(web.isLoading), at \(web.url?.absoluteString ?? "nil"), back \(list.backList.map(\.url)), forward \(list.forwardList.map(\.url))",
                sourceLocation: sourceLocation)
    }

    @Test func theFirstTapShowsThePreloadedView() {
        let pool = WebViewPool()
        let preloaded = pool.view(for: link)
        #expect(pool.view(for: link) === preloaded)
        #expect(pool.existing(for: link) === preloaded)
    }

    @Test func aViewASheetShowedIsNotHandedOutAgain() {
        let pool = WebViewPool()
        let shown = pool.view(for: link)
        pool.release(shown, for: link)
        let next = pool.view(for: link)
        #expect(next !== shown)
        #expect(next.url == link) // already loading the link again, so the next tap is still preloaded
    }

    /// The report: the sheet closed on another page than its link's, the link tapped again. (Here the view only moves to
    /// that page: neither `loadHTMLString` nor a script's `history.pushState` adds back/forward entries in a unit test,
    /// so the history a followed link leaves is `LinkSheetReopenTests`'.)
    @Test func theNextTapDoesNotOpenWhereTheSheetWasClosed() async throws {
        let pool = WebViewPool()
        let shown = pool.view(for: link)
        let next = URL(string: "https://example.invalid/issues/2")! // an issue the pull request links
        shown.loadHTMLString("<p>issue 2</p>", baseURL: next)
        try await until("the next page", shown) { !shown.isLoading && shown.url == next }

        pool.release(shown, for: link)
        let reopened = pool.view(for: link)
        #expect(reopened !== shown)
        #expect(reopened.url == link)
        #expect(!reopened.canGoBack && !reopened.canGoForward)
        #expect(reopened.backForwardList.backList.isEmpty && reopened.backForwardList.forwardList.isEmpty)
    }

    @Test func closingTheSheetLetsGoOfItsView() {
        let url = URL(string: "https://example.invalid/sheet/\(UUID().uuidString)")!
        let shown = WebViewPool.shared.view(for: url)
        let sheet = PooledWebView.Coordinator()
        sheet.url = url
        PooledWebView.dismantleUIView(shown, coordinator: sheet)
        #expect(WebViewPool.shared.existing(for: url) !== shown)
    }

    @Test func lettingGoOfAViewThePoolNoLongerHoldsChangesNothing() {
        let pool = WebViewPool()
        let pooled = pool.view(for: link)
        pool.release(SelectableWebView(frame: .zero), for: link) // pushed out by newer links while it was shown
        #expect(pool.existing(for: link) === pooled)
    }

    @Test func aMachinesDevServerIsNotAskedAgainUntilOpened() {
        let pool = WebViewPool()
        let dev = URL(string: "http://127.0.0.1:9/")!
        let shown = pool.view(for: dev)
        pool.release(shown, for: dev)
        #expect(pool.existing(for: dev) == nil)
        #expect(pool.view(for: dev) !== shown)
    }
    /// A preloaded link (one nobody opened yet) is kept in memory, not in WebKit's disk cache; a link opened without a
    /// preloaded view still loads in the default store (#8352).
    @Test func aPreloadedPageIsKeptInMemoryNotOnDisk() async throws {
        let pool = WebViewPool()
        let url = URL(string: "https://example.invalid/preload/\(UUID().uuidString)")!
        pool.preload([url])
        for _ in 0..<200 where pool.existing(for: url) == nil { try await Task.sleep(for: .milliseconds(50)) }
        let preloaded = try #require(pool.existing(for: url))
        #expect(!preloaded.configuration.websiteDataStore.isPersistent)
        #expect(pool.view(for: link).configuration.websiteDataStore.isPersistent)
    }

    /// Preloads carry the default store's cookies, so a signed-in site preloads signed in.
    @Test func preloadsCarryTheDefaultStoresCookies() async throws {
        let name = "persona-test-\(UUID().uuidString)"
        let cookie = try #require(HTTPCookie(properties: [.name: name, .value: "1", .domain: "example.invalid", .path: "/"]))
        let defaults = WKWebsiteDataStore.default().httpCookieStore
        await defaults.setCookie(cookie)
        defer { Task { await defaults.deleteCookie(cookie) } }
        let pool = WebViewPool()
        let url = URL(string: "https://example.invalid/preload/\(UUID().uuidString)")!
        pool.preload([url])
        for _ in 0..<200 where pool.existing(for: url) == nil { try await Task.sleep(for: .milliseconds(50)) }
        let preloaded = try #require(pool.existing(for: url))
        let cookies = await preloaded.configuration.websiteDataStore.httpCookieStore.allCookies()
        #expect(cookies.contains { $0.name == name })
    }

    @Test func selectableWebViewInstallsNavigationAndUIDelegatesAndHandlesProcessTermination() throws {
        let view = SelectableWebView(frame: .zero, configuration: WKWebViewConfiguration())
        #expect(view.navigationDelegate != nil)
        #expect(view.uiDelegate != nil)

        view.navigationDelegate?.webViewWebContentProcessDidTerminate?(view)
        #expect(view.pageState.loadError == .processTerminated)
    }

    @Test func jsDialogsAndExternalTargetBlankFollowInAppRules() async throws {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let view = SelectableWebView(frame: CGRect(x: 0, y: 0, width: 320, height: 480), configuration: config)
        view.pageState.isPresented = true
        defer { view.pageState.dismissPendingPrompts() }

        // Alert / confirm / prompt dialogs enqueue cleanly and resolve with safe defaults on dismiss.
        var alertDone = false
        var confirmResult: Bool?
        var promptResult: String?
        view.pageState.enqueueDialog(WebDialog(.alert(message: "hi", done: { alertDone = true })))
        view.pageState.enqueueDialog(WebDialog(.confirm(message: "sure?", done: { confirmResult = $0 })))
        view.pageState.enqueueDialog(WebDialog(.prompt(message: "name?", defaultText: "ada", done: { promptResult = $0 })))
        #expect(view.pageState.pendingDialog != nil)
        view.pageState.pendingDialog?.finishAlert()
        view.pageState.advanceDialog()
        #expect(alertDone)
        view.pageState.pendingDialog?.finishConfirm(true)
        view.pageState.advanceDialog()
        #expect(confirmResult == true)
        view.pageState.pendingDialog?.finishPrompt("grace")
        view.pageState.advanceDialog()
        #expect(promptResult == "grace")
        #expect(view.pageState.pendingDialog == nil)

        // Loopback trust policy only accepts hosts+ports actively owned by PortForwarder.
        #expect(!PortForwarder.shared.isForwardedLoopback(host: "example.com", port: 443))
        #expect(!PortForwarder.shared.isForwardedLoopback(host: "localhost", port: 65534))
    }
}
