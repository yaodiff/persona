import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Source subscriptions (#2041): the pages under their reserved id, the entry rows' counts and the event that redraws them.
@MainActor
struct SubscriptionsUITests {
    @Test func thePagesFirstStateNamesTheScope() throws {
        #expect(SubscriptionsUI.state() == nil)
        #expect(SubscriptionsUI.state(extension: "apps") == #"{"ext":"apps"}"#)
        #expect(SubscriptionsUI.state(connector: "github") == #"{"c":"github"}"#)
        #expect(SubscriptionsUI.id.hasPrefix("-"), "no extension can have the id")
    }

    @Test func countsShowOnlyWhenSomethingCanBeSubscribed() throws {
        let lines = try JSONDecoder().decode([SubscriptionSummaryLine].self, from: Data(#"""
            [{"connector":"apps","extension":"apps","subscribable":true,"on":2,"failing":1},
             {"connector":"github","extension":"","on":0,"failing":0},
             {"connector":"notes","subscribable":false,"on":0,"failing":0},
             {"connector":"old","subscribable":false,"on":1,"failing":0}]
            """#.utf8))
        #expect(SubscriptionCounts.of(lines) { $0.extensionID == "apps" } == SubscriptionCounts(on: 2, failing: 1))
        #expect(SubscriptionCounts.of(lines) { $0.connector == "github" } == nil, "not read yet: hidden")
        #expect(SubscriptionCounts.of(lines) { $0.connector == "notes" } == nil, "nothing to subscribe: hidden")
        #expect(SubscriptionCounts.of(lines) { $0.connector == "old" } == SubscriptionCounts(on: 1, failing: 0), "tools gone, subscriptions kept")
        #expect(SubscriptionCounts(on: 0, failing: 0).text != SubscriptionCounts(on: 2, failing: 1).text)
    }

    @Test func aSubscriptionEventRedrawsThePages() {
        let event = ServerEvent.decode(Data(#"{"type":"subscription","id":"sub_1","connector":"apps","revision":3,"state":"ok","removed":false}"#.utf8))
        guard case .extensionInvalidate(let ext, let id)? = event else { Issue.record("not decoded: \(String(describing: event))"); return }
        #expect(ext == SubscriptionsUI.id)
        #expect(id == nil)
        // Other events still decode as before.
        guard case .extensionInvalidate("demo", "home")? = ServerEvent.decode(Data(#"{"type":"extension_ui","extension":"demo","id":"home","invalidate":true}"#.utf8)) else {
            Issue.record("extension_ui no longer decodes"); return
        }
    }

    /// Set Up with … (#5278): the pages' persona://ask is taken only from them, its text whole; other links are not it.
    @Test func onlyThePagesAskTheAssistant() throws {
        let text = "<persona-context kind=\"context\" title=\"Source\" url=\"\">\nconnector: apps\n</persona-context>\n\nHelp me"
        var c = URLComponents(string: "persona://ask")!
        c.queryItems = [URLQueryItem(name: "text", value: text)]
        let url = try #require(c.url)
        #expect(SubscriptionsUI.askText(url) == text)
        #expect(ExtensionViewModel.opens(url, item: nil, owner: SubscriptionsUI.id))
        #expect(!ExtensionViewModel.opens(url, item: nil, owner: "example-gateway"), "an extension can't send as the user")
        #expect(!ExtensionViewModel.opens(url, item: nil))
        for bad in ["persona://ask?text=", "persona://ask?text=%20", "persona://ask/x?text=a", "persona://asks?text=a", "https://example.com/ask?text=a"] {
            #expect(SubscriptionsUI.askText(URL(string: bad)!) == nil, "\(bad)")
        }
        #expect(SubscriptionsUI.askText(URL(string: "persona://ask?text=" + String(repeating: "a", count: SubscriptionsUI.askMost + 1))!) == nil)
    }

    /// Only the pages are told the assistant's name, beside the context the app always sends.
    @Test func thePagesAreToldTheAssistantsName() throws {
        let context = ExtensionUIContext.here()
        let body = ExtensionUIRequest(context: context, state: #"{"p":"source"}"#, action: "ask", values: ["a": "b"])
        let sent = try JSONSerialization.jsonObject(with: JSONEncoder().encode(SubscriptionsUI.Request(request: body, assistant: "Claude"))) as? [String: Any]
        let ctx = try #require(sent?["context"] as? [String: Any])
        #expect(ctx["assistant"] as? String == "Claude")
        #expect(ctx["platform"] as? String == context.platform && ctx["ui_version"] as? Int == context.uiVersion)
        #expect(sent?["state"] as? String == #"{"p":"source"}"# && sent?["action"] as? String == "ask")
        // sent even without a name: the key says this app opens Set Up with …, which the pages draw only then (#5661)
        for name in ["", nil] as [String?] {
            let none = try JSONSerialization.jsonObject(with: JSONEncoder().encode(SubscriptionsUI.Request(request: body, assistant: name))) as? [String: Any]
            #expect((none?["context"] as? [String: Any])?["assistant"] as? String == "")
        }
    }
}
