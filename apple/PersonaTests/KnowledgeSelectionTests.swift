import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Settings › Knowledge Selection (`KBSelection`, `KnowledgeSelectionModel`) without the page.
@MainActor struct KnowledgeSelectionTests {
    private struct Boom: Error {}

    /// The document a hub answers with before anything was saved (the hub's `kb.Defaults`), nulls as it writes them.
    private static let defaults = #"""
    {"version":1,
     "messages":{"order":[["mentions","mine"],["dm"],["group"],["bot"],["official"],["muted"],["collapsed"],["done"]],
      "rules":{"mentions":{"on":true,"days":null,"attachment_days":null,"include_all":true},
               "mine":{"on":true,"days":null,"attachment_days":null,"context":50},
               "dm":{"on":true,"days":null,"attachment_days":null},
               "group":{"on":false,"days":null,"attachment_days":null},
               "bot":{"on":false,"days":null,"attachment_days":null},
               "official":{"on":false,"days":null,"attachment_days":null},
               "muted":{"on":false,"days":null,"attachment_days":null},
               "collapsed":{"on":false,"days":null,"attachment_days":null},
               "done":{"on":false,"days":null,"attachment_days":null}}},
     "docs":{"order":[["mentions","comment_mentions"],["authored"],["viewed"]],
      "rules":{"mentions":{"on":true,"days":null},"comment_mentions":{"on":true,"days":null},
               "authored":{"on":true,"days":null},"viewed":{"on":true,"days":null}}},
     "items":[],"history":{}}
    """#

    private func selection() throws -> KBSelection { try KBSelection.read(Data(Self.defaults.utf8)) }

    private func json(_ selection: KBSelection) throws -> [String: Any] {
        try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(selection)) as? [String: Any])
    }

    private func model(_ start: KBSelection, saved: @escaping (KBSelection) -> Void = { _ in }) -> KnowledgeSelectionModel {
        var hub = start
        return KnowledgeSelectionModel(deps: .init(load: { hub }, save: { hub = $0; saved($0); return $0 },
                                                   entries: { _ in [] }, meta: { _, _ in nil }))
    }

    // MARK: The document

    @Test func readsTheHubsDefaults() throws {
        let s = try selection()
        #expect(s.isValid && s.version == 1)
        #expect(s.messages.order.first == ["mentions", "mine"] && s.messages.rules["mentions"]?.includeAll == true)
        #expect(s.messages.rules["mine"]?.context == 50 && s.messages.rules["group"]?.on == false)
        #expect(s.docs.rules.count == 4 && s.items == [] && s.history == [:])
    }

    /// The hub refuses a field it does not know (`DisallowUnknownFields`), so a document rule never carries the
    /// options only a message rule has, and nothing unset is sent as a value.
    @Test func sendsOnlyTheFieldsEachRuleHas() throws {
        var s = try selection()
        s.docs.rules["viewed"]?.days = 30
        s.messages.rules["group"] = KBSelectionRule(on: true, days: 90, attachmentDays: 7)
        let out = try json(s)
        let docs = try #require((out["docs"] as? [String: Any])?["rules"] as? [String: [String: Any]])
        #expect(Set(docs["viewed"]!.keys) == ["on", "days"] && docs["viewed"]?["days"] as? Int == 30)
        #expect(Set(docs["authored"]!.keys) == ["on"])
        let messages = try #require((out["messages"] as? [String: Any])?["rules"] as? [String: [String: Any]])
        #expect(Set(messages["group"]!.keys) == ["on", "days", "attachment_days"])
        #expect(Set(messages["mentions"]!.keys) == ["on", "include_all"] && Set(messages["mine"]!.keys) == ["on", "context"])
        let back = try KBSelection.read(JSON.encoder.encode(s))
        #expect(back == s)
    }

    @Test func aPerItemSettingKeepsItsWireNames() throws {
        var s = try selection()
        s.setItem(KBSelectionItem(source: "lark", kind: .chat, id: "oc_1", name: "Team", read: true, store: true, days: 14))
        s.history = ["lark": KBSelectionHistory(backDays: 60)]
        let out = try json(s)
        let item = try #require((out["items"] as? [[String: Any]])?.first)
        #expect(item["source"] as? String == "lark" && item["kind"] as? String == "chat" && item["id"] as? String == "oc_1")
        #expect(item["read"] as? Bool == true && item["store"] as? Bool == true && item["days"] as? Int == 14)
        #expect(((out["history"] as? [String: Any])?["lark"] as? [String: Any])?["back_days"] as? Int == 60)
    }

    /// A PUT replaces the whole document: one this version cannot send back whole is not opened for editing.
    @Test func refusesADocumentItWouldNotSendBackWhole() throws {
        let newer = Self.defaults.replacingOccurrences(of: #""version":1"#, with: #""version":2"#)
        #expect(throws: KBSelectionUnsupported.self) { try KBSelection.read(Data(newer.utf8)) }
        let extraField = Self.defaults.replacingOccurrences(of: #""items":[]"#, with: #""items":[],"sources":{"lark":{"on":true}}"#)
        #expect(throws: KBSelectionUnsupported.self) { try KBSelection.read(Data(extraField.utf8)) }
        let extraOption = Self.defaults.replacingOccurrences(of: #""context":50"#, with: #""context":50,"threads":true"#)
        #expect(throws: KBSelectionUnsupported.self) { try KBSelection.read(Data(extraOption.utf8)) }
        #expect(throws: KBSelectionUnsupported.self) { try KBSelection.read(Data(#"{"items":[],"tags":[]}"#.utf8)) }
        let nullField = Self.defaults.replacingOccurrences(of: #""items":[]"#, with: #""items":[],"sources":null"#)
        #expect(throws: KBSelectionUnsupported.self) { try KBSelection.read(Data(nullField.utf8)) }
        let nullOption = Self.defaults.replacingOccurrences(of: #""context":50"#, with: #""context":50,"threads":null"#)
        #expect(throws: KBSelectionUnsupported.self) { try KBSelection.read(Data(nullOption.utf8)) }
        let nullItemOption = Self.defaults.replacingOccurrences(
            of: #""items":[]"#,
            with: #""items":[{"source":"lark","kind":"chat","id":"oc_1","read":true,"store":false,"later":null}]"#)
        #expect(throws: KBSelectionUnsupported.self) { try KBSelection.read(Data(nullItemOption.utf8)) }
        let nullHistoryOption = Self.defaults.replacingOccurrences(of: #""history":{}"#, with: #""history":{"lark":{"back_days":null,"days":null}}"#)
        #expect(throws: KBSelectionUnsupported.self) { try KBSelection.read(Data(nullHistoryOption.utf8)) }
    }

    @Test func validatesWhatTheHubWould() throws {
        let base = try selection()
        func invalid(_ edit: (inout KBSelection) -> Void) -> Bool {
            var s = base
            edit(&s)
            return !s.isValid
        }
        #expect(invalid { $0.messages.rules["dm"]?.days = 0 })
        #expect(invalid { $0.messages.rules["dm"]?.attachmentDays = 36501 })
        #expect(invalid { $0.docs.rules["viewed"]?.attachmentDays = 3 })
        #expect(invalid { $0.messages.rules["dm"]?.includeAll = true })
        #expect(invalid { $0.messages.rules["dm"]?.context = 5 })
        #expect(invalid { $0.messages.rules["mine"]?.context = 201 })
        #expect(invalid { $0.messages.rules["dm"] = nil })
        #expect(invalid { $0.messages.order.removeLast() })
        #expect(invalid { $0.messages.order[0].append("dm") })
        #expect(invalid { $0.items = [KBSelectionItem(source: "lark", kind: .chat, id: "a", read: false, store: true)] })
        #expect(invalid { $0.items = [KBSelectionItem(source: "", kind: .chat, id: "a", read: true, store: false)] })
        #expect(invalid {
            let item = KBSelectionItem(source: "lark", kind: .doc, id: "a", read: true, store: false)
            $0.items = [item, item]
        })
        #expect(invalid { $0.history = ["lark": KBSelectionHistory(backDays: 0)] })
        #expect(!invalid { $0.messages.rules["mine"]?.context = 0 })
        #expect(!invalid { $0.messages.rules["dm"]?.days = 36500 })
    }

    // MARK: Priority levels

    @Test func movesALevelByItsRule() throws {
        var s = try selection()
        s.moveLevel(of: "group", in: .messages, by: -1)
        #expect(Array(s.messages.order.prefix(3)) == [["mentions", "mine"], ["group"], ["dm"]])
        s.moveLevel(of: "mine", in: .messages, by: 1)
        #expect(Array(s.messages.order.prefix(3)) == [["group"], ["mentions", "mine"], ["dm"]])
        s.moveLevel(of: "group", in: .messages, by: -1) // already first
        s.moveLevel(of: "done", in: .messages, by: 1) // already last
        s.moveLevel(of: "nothing", in: .messages, by: 1)
        #expect(s.messages.order.first == ["group"] && s.messages.order.last == ["done"] && s.isValid)
        let untouched = try selection().docs
        #expect(s.docs == untouched)
    }

    @Test func regroupsARule() throws {
        var s = try selection()
        s.regroup("dm", in: .messages, joinPrevious: true)
        #expect(s.messages.order.first == ["mentions", "mine", "dm"] && s.messages.order.count == 7)
        s.regroup("mine", in: .messages, joinPrevious: false)
        #expect(Array(s.messages.order.prefix(2)) == [["mentions", "dm"], ["mine"]] && s.messages.order.count == 8)
        let before = s
        s.regroup("mentions", in: .messages, joinPrevious: true) // nothing above the first level
        s.regroup("mine", in: .messages, joinPrevious: false) // already alone
        #expect(s == before && s.isValid)
        s.regroup("comment_mentions", in: .docs, joinPrevious: false)
        #expect(s.docs.order == [["mentions"], ["comment_mentions"], ["authored"], ["viewed"]] && s.isValid)
    }

    // MARK: Per-item settings

    @Test func aSettingIsOnePerSourceKindAndID() throws {
        var s = try selection()
        s.setItem(KBSelectionItem(source: "lark", kind: .chat, id: "a", read: true, store: false))
        s.setItem(KBSelectionItem(source: "lark", kind: .doc, id: "a", read: true, store: false))
        s.setItem(KBSelectionItem(source: "telegram", kind: .chat, id: "a", read: true, store: false))
        s.setItem(KBSelectionItem(source: "lark", kind: .chat, id: "a", read: false, store: false))
        #expect(s.items?.count == 3 && s.items?.first?.read == false && s.isValid)
        s.removeItem(key: KBSelectionItem(source: "lark", kind: .doc, id: "a", read: true, store: false).key)
        #expect(s.items?.map(\.source) == ["lark", "telegram"])
    }

    @Test func aModeIsReadAndStore() {
        var item = KBSelectionItem(source: "lark", kind: .chat, id: "a", read: true, store: true, days: 30)
        #expect(item.mode == .all)
        item.mode = .rules
        #expect(item.read && !item.store && item.days == nil)
        item.mode = .skip
        #expect(!item.read && !item.store && item.mode == .skip)
        item.mode = .all
        #expect(item.read && item.store)
    }

    @Test func picksAChatByItsSourceAndADocumentByItsID() throws {
        let chat = KBEntry(kind: "chats", id: "lark-oc-1", name: "Team", relevance: 0)
        let meta: JSONValue = ["source": "lark", "source_id": "oc_1", "kind": "group", "id": "lark-oc-1"]
        let item = try KnowledgeSelectionText.item(for: chat, kind: .chat, meta: meta)
        #expect(item == KBSelectionItem(source: "lark", kind: .chat, id: "oc_1", name: "Team", read: true, store: false))
        #expect(throws: KnowledgeSelectionPickError.notSelectable) {
            try KnowledgeSelectionText.item(for: chat, kind: .chat, meta: ["source": "mail", "source_id": "t1", "kind": "mail"])
        }
        #expect(throws: KnowledgeSelectionPickError.noIdentity) {
            try KnowledgeSelectionText.item(for: chat, kind: .chat, meta: ["source": "lark", "kind": "group"])
        }
        let doc = KBEntry(kind: "docs", id: "doc1", name: "", relevance: 0)
        let picked = try KnowledgeSelectionText.item(for: doc, kind: .doc, meta: ["source": "lark", "id": "tok"])
        #expect(picked == KBSelectionItem(source: "lark", kind: .doc, id: "tok", read: true, store: false))
        #expect(throws: KnowledgeSelectionPickError.noIdentity) { try KnowledgeSelectionText.item(for: doc, kind: .doc, meta: ["id": "tok"]) }
        #expect(throws: KnowledgeSelectionPickError.noIdentity) { try KnowledgeSelectionText.item(for: doc, kind: .doc, meta: nil) }
    }

    @Test func aNumberFieldIsBlankAWholeNumberInRangeOrInvalid() {
        #expect(KnowledgeSelectionText.number("", in: KBSelection.dayRange) == .blank)
        #expect(KnowledgeSelectionText.number("  ", in: KBSelection.dayRange) == .blank)
        #expect(KnowledgeSelectionText.number(" 30 ", in: KBSelection.dayRange) == .value(30))
        #expect(KnowledgeSelectionText.number("0", in: KBSelection.contextRange) == .value(0))
        for bad in ["0", "36501", "-1", "1.5", "ten", "３０", "99999999999999999999"] {
            #expect(KnowledgeSelectionText.number(bad, in: KBSelection.dayRange) == .invalid, "\(bad)")
        }
    }

    // MARK: The model

    @Test func aChangeIsShownAndSavedAtOnce() async throws {
        var saved: [KBSelection] = []
        let m = model(try selection()) { saved.append($0) }
        #expect(m.selection == nil)
        await m.load()
        await m.setRule("group", in: .messages, on: true)
        #expect(saved.count == 1 && saved.last?.messages.rules["group"]?.on == true)
        #expect(m.selection == saved.last && m.error == nil && !m.saving)
        await m.setRule("group", in: .messages, on: true) // already on: nothing to save
        #expect(saved.count == 1)
    }

    /// Another device saved since this page read the document: the change goes onto what the hub has now.
    @Test func aChangeGoesOntoWhatTheHubHasNow() async throws {
        var hub = try selection()
        var saved: [KBSelection] = []
        let m = KnowledgeSelectionModel(deps: .init(load: { hub }, save: { hub = $0; saved.append($0); return $0 },
                                                    entries: { _ in [] }, meta: { _, _ in nil }))
        await m.load()
        hub.messages.rules["bot"]?.on = true
        hub.setItem(KBSelectionItem(source: "lark", kind: .chat, id: "a", read: false, store: false))
        await m.moveLevel(of: "group", in: .messages, by: -1)
        let now = try #require(saved.last)
        #expect(now.messages.rules["bot"]?.on == true && now.items?.count == 1)
        #expect(Array(now.messages.order.prefix(3)) == [["mentions", "mine"], ["group"], ["dm"]] && m.selection == now)
    }

    @Test func aLateRefreshCannotReplaceANewerSave() async throws {
        let start = try selection()
        var loads = 0
        var refreshStarted: CheckedContinuation<Void, Never>?
        var finishRefresh: CheckedContinuation<KBSelection, Never>?
        let m = KnowledgeSelectionModel(deps: .init(load: {
            loads += 1
            if loads == 2 {
                refreshStarted?.resume()
                return await withCheckedContinuation { finishRefresh = $0 }
            }
            return start
        }, save: { $0 }, entries: { _ in [] }, meta: { _, _ in nil }))
        await m.load()
        let refreshing = Task { await m.load() }
        await withCheckedContinuation { (started: CheckedContinuation<Void, Never>) in refreshStarted = started }
        await m.setRule("group", in: .messages, on: true)
        #expect(m.selection?.messages.rules["group"]?.on == true)
        finishRefresh?.resume(returning: start)
        await refreshing.value
        #expect(m.selection?.messages.rules["group"]?.on == true)
    }

    @Test func anOldHubDuringAnEditRetiresTheEditor() async throws {
        let start = try selection()
        var loads = 0
        let duringRead = KnowledgeSelectionModel(deps: .init(load: {
            loads += 1
            if loads == 1 { return start }
            throw APIError(status: 404, message: "unknown kind")
        }, save: { $0 }, entries: { _ in [] }, meta: { _, _ in nil }))
        await duringRead.load()
        await duringRead.setRule("group", in: .messages, on: true)
        #expect(duringRead.selection == nil && duringRead.unsupported == .hub && duringRead.error == nil)

        let duringSave = KnowledgeSelectionModel(deps: .init(load: { start }, save: { _ in
            throw APIError(status: 404, message: "unknown kind")
        }, entries: { _ in [] }, meta: { _, _ in nil }))
        await duringSave.load()
        await duringSave.setRule("group", in: .messages, on: true)
        #expect(duringSave.selection == nil && duringSave.unsupported == .hub && duringSave.error == nil)
    }

    @Test func aFailedSavePutsTheOldValueBack() async throws {
        let start = try selection()
        let m = KnowledgeSelectionModel(deps: .init(load: { start }, save: { _ in throw APIError(status: 403, message: "read-only device") },
                                                    entries: { _ in [] }, meta: { _, _ in nil }))
        await m.load()
        let ok = await m.setItem(KBSelectionItem(source: "lark", kind: .chat, id: "a", read: false, store: false))
        #expect(!ok && m.selection == start && m.error?.contains("read-only device") == true && !m.saving)
    }

    @Test func anInvalidChangeIsNotSent() async throws {
        var saves = 0
        let m = model(try selection()) { _ in saves += 1 }
        await m.load()
        let start = try selection()
        let ok = await m.setRule("dm", in: .messages, to: KBSelectionRule(on: true, days: 0))
        #expect(!ok && saves == 0 && m.error != nil && m.selection == start)
    }

    @Test func aHubWithoutTheSelectionSaysSo() async {
        let old = KnowledgeSelectionModel(deps: .init(load: { throw APIError(status: 404, message: "unknown kind") }, save: { $0 },
                                                      entries: { _ in [] }, meta: { _, _ in nil }))
        await old.load()
        #expect(old.unsupported == .hub && old.selection == nil && old.error == nil)
        let newer = KnowledgeSelectionModel(deps: .init(load: { throw KBSelectionUnsupported() }, save: { $0 },
                                                        entries: { _ in [] }, meta: { _, _ in nil }))
        await newer.load()
        let ok = await newer.setItem(KBSelectionItem(source: "lark", kind: .chat, id: "a", read: false, store: false))
        #expect(newer.unsupported == .document && newer.selection == nil && !ok)
    }

    @Test func aReadThatFailsSaysWhyAndNothingCanBeSaved() async {
        var saves = 0
        let m = KnowledgeSelectionModel(deps: .init(load: { throw Boom() }, save: { saves += 1; return $0 },
                                                    entries: { _ in [] }, meta: { _, _ in nil }))
        await m.load()
        #expect(m.selection == nil && m.error != nil && m.unsupported == nil)
        await m.setRule("group", in: .messages, on: true)
        #expect(saves == 0)
    }

    @Test func anEntryThatHasASettingOpensIt() async throws {
        var start = try selection()
        let kept = KBSelectionItem(source: "lark", kind: .chat, id: "oc_1", name: "Team", read: true, store: true, days: 30)
        start.setItem(kept)
        let m = KnowledgeSelectionModel(deps: .init(
            load: { start }, save: { $0 }, entries: { _ in [] },
            meta: { _, id in ["source": "lark", "source_id": .string(id == "known" ? "oc_1" : "oc_2"), "kind": "group"] }))
        await m.load()
        let known = try await m.item(for: KBEntry(kind: "chats", id: "known", name: "Team", relevance: 0), kind: .chat)
        #expect(known == kept)
        let fresh = try await m.item(for: KBEntry(kind: "chats", id: "other", name: "Other", relevance: 0), kind: .chat)
        #expect(fresh.id == "oc_2" && fresh.mode == .rules && fresh.days == nil)
    }
}
