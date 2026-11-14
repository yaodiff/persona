import Foundation
import HealthKit
import Testing
import UIKit
@testable import Persona

/// The registry is main-actor isolated (the app target defaults to MainActor),
/// so every test that touches a tool runs on the main actor.
@MainActor
struct PhoneToolsTests {
    // MARK: - Catalogue

    @Test func toolNamesAreUniqueAndPrefixed() {
        var seen = Set<String>()
        for tool in PhoneToolRegistry.all {
            #expect(tool.spec.name.hasPrefix("phone_"), "tool \(tool.spec.name) must start with phone_")
            #expect(seen.insert(tool.spec.name).inserted, "duplicate tool name \(tool.spec.name)")
            #expect(!tool.spec.description.isEmpty, "tool \(tool.spec.name) has no description")
            #expect(tool.spec.description.count > 40, "tool \(tool.spec.name) needs a description Claude can act on")
        }
    }

    @Test func everySchemaIsAnObject() {
        for tool in PhoneToolRegistry.all {
            guard case .object(let schema) = tool.spec.inputSchema else {
                Issue.record("\(tool.spec.name): input_schema is not a JSON object")
                continue
            }
            #expect(schema["type"] == .string("object"), "\(tool.spec.name): schema type must be object")
            if let properties = schema["properties"], case .object(let props) = properties {
                for (key, value) in props {
                    guard case .object = value else {
                        Issue.record("\(tool.spec.name): property \(key) is not an object schema")
                        continue
                    }
                }
            } else if schema["properties"] != nil {
                Issue.record("\(tool.spec.name): properties is not an object")
            }
            if let required = schema["required"], case .array = required {} else if schema["required"] != nil {
                Issue.record("\(tool.spec.name): required is not an array")
            }
        }
    }

    @Test func everyRequiredPropertyIsDeclared() {
        for tool in PhoneToolRegistry.all {
            guard case .object(let schema) = tool.spec.inputSchema,
                  case .object(let props)? = schema["properties"],
                  case .array(let required)? = schema["required"] else { continue }
            for case .string(let name) in required {
                #expect(props[name] != nil, "\(tool.spec.name): required \(name) has no schema")
            }
        }
    }

    @Test func coversEveryDataSource() {
        let covered = Set(PhoneToolRegistry.all.map(\.source))
        for source in PhoneDataSource.allCases {
            #expect(covered.contains(source), "no tool for data source \(source.rawValue)")
        }
    }

    @Test func expectedToolNamesExist() {
        let names = Set(PhoneToolRegistry.all.map(\.spec.name))
        let expected = [
            "phone_contacts_search",
            "phone_calendar_events",
            "phone_calendar_create_event",
            "phone_reminders_list",
            "phone_reminders_create",
            "phone_reminders_complete",
            "phone_reminders_update",
            "phone_reminders_delete",
            "phone_location_current",
            "phone_location_search",
            "phone_health_summary",
            "phone_health_write_sample",
            "phone_workouts_recent",
            "phone_workout_detail",
            "phone_photos_recent",
            "phone_photo_get",
            "phone_device_info",
            "phone_clipboard_get",
            "phone_clipboard_set",
            "phone_notify",
            // iPhone system integrations (#2276–#2286)
            "phone_calendar_update_event",
            "phone_calendar_delete_event",
            "phone_contacts_create",
            "phone_contacts_update",
            "phone_maps_search_nearby",
            "phone_maps_navigate",
            "phone_maps_route",
            "phone_alarms_list",
            "phone_alarm_create",
            "phone_timer_start",
            "phone_alarm_cancel",
            "phone_alarm_pause",
            "phone_alarm_resume",
            "phone_messages_compose",
            "phone_call_start",
            "phone_shortcuts_list",
            "phone_shortcuts_register",
            "phone_shortcuts_forget",
            "phone_shortcuts_run",
            "phone_media_play",
            "phone_media_control",
            "phone_music_create_playlist",
            "phone_music_snapshot",
            "phone_media_open",
            "phone_home_list",
            "phone_home_control",
            "phone_home_run_scene",
        ]
        for name in expected {
            #expect(names.contains(name), "missing tool \(name)")
        }
    }

    // MARK: - Enabled specs

    @Test func enabledSpecsFollowTheToggles() {
        let source = PhoneDataSource.clipboard
        let previous = source.isEnabled
        defer { source.isEnabled = previous }

        source.isEnabled = true
        #expect(PhoneToolRegistry.enabledSpecs().contains { $0.name == "phone_clipboard_get" })

        source.isEnabled = false
        #expect(!PhoneToolRegistry.enabledSpecs().contains { $0.name == "phone_clipboard_get" })
        // Disabling one source leaves the rest alone.
        #expect(PhoneToolRegistry.enabledSpecs().contains { $0.name == "phone_device_info" })
    }

    /// The registration asks EventKit off the main thread and passes its answers in (#1337): they decide calendar's and
    /// reminders' tools, the switches still count.
    @Test func enabledSpecsTakeEventKitsAnswers() {
        let calendar = PhoneDataSource.calendar, reminders = PhoneDataSource.reminders
        let before = (calendar.isEnabled, reminders.isEnabled)
        defer { calendar.isEnabled = before.0; reminders.isEnabled = before.1 }
        calendar.isEnabled = true
        reminders.isEnabled = true
        let names = PhoneToolRegistry.enabledSpecs(eventKit: (events: .denied, reminders: .granted)).map(\.name)
        #expect(!names.contains("phone_calendar_events") && !names.contains("phone_calendar_create_event"))
        #expect(names.contains("phone_reminders_list") && names.contains("phone_reminders_complete"))
        reminders.isEnabled = false
        #expect(!PhoneToolRegistry.enabledSpecs(eventKit: (events: .granted, reminders: .granted)).contains { $0.name.hasPrefix("phone_reminders") })
        #expect(PhoneToolRegistry.enabledSpecs(eventKit: (events: .granted, reminders: .granted)).contains { $0.name == "phone_calendar_events" })
    }

    // MARK: - Not asked yet (#7659)

    /// A source iOS hasn't asked about is offered while it is on, when its first use asks; Health and the iPhone
    /// integrations only once allowed; a refused one never; one turned off in Settings never.
    @Test func aSourceNotAskedYetIsOfferedWhenItsFirstUseAsks() {
        for source in [PhoneDataSource.contacts, .calendar, .reminders, .location, .photos, .notifications] {
            #expect(source.asksWhenUsed, "\(source) asks the first time a tool uses it")
            #expect(source.offered(.notDetermined) && source.offered(.granted))
            #expect(!source.offered(.denied))
        }
        for source in [PhoneDataSource.health, .alarms, .media, .home] {
            #expect(!source.asksWhenUsed, "\(source) asks only from Settings")
            #expect(!source.offered(.notDetermined) && source.offered(.granted))
        }
        let calendar = PhoneDataSource.calendar, reminders = PhoneDataSource.reminders
        let before = (calendar.isEnabled, reminders.isEnabled)
        defer { calendar.isEnabled = before.0; reminders.isEnabled = before.1 }
        calendar.isEnabled = true
        reminders.isEnabled = true
        let notAsked = PhoneToolRegistry.enabledSpecs(eventKit: (events: .notDetermined, reminders: .notDetermined)).map(\.name)
        #expect(notAsked.contains("phone_reminders_list") && notAsked.contains("phone_calendar_events"))
        #expect(!PhoneToolRegistry.enabledSpecs(eventKit: (events: .denied, reminders: .denied)).contains { $0.name.hasPrefix("phone_reminders") })
        reminders.isEnabled = false
        #expect(!PhoneToolRegistry.enabledSpecs(eventKit: (events: .notDetermined, reminders: .notDetermined))
            .contains { $0.name.hasPrefix("phone_reminders") }, "turned off in Settings, it is neither offered nor asked")
    }

    /// Calls to a source iOS hasn't asked about share one dialog; refused, each says where to allow it.
    @Test func theFirstUseAsksOnceAndARefusalSaysWhereToAllowIt() async {
        let asked = AskCount()
        let restore = firstUse(status: .notDetermined, inFront: true, answer: .denied, asked: asked)
        defer { restore() }
        let first = Task { await PhoneToolRegistry.call(name: "phone_reminders_list", args: .object([:])) { _ in true } }
        let second = Task { await PhoneToolRegistry.call(name: "phone_reminders_list", args: .object([:])) { _ in true } }
        for result in [await first.value, await second.value] {
            #expect(result.isError)
            #expect(text(of: result.content).contains("iOS 设置 › Persona"))
        }
        #expect(asked.count == 1, "calls that arrive together share one dialog")
    }

    /// iOS can show its dialog only while Persona is in front: otherwise the call says to open Persona and asks nothing.
    @Test func theFirstUseOutsidePersonaSaysToOpenIt() async {
        let asked = AskCount()
        let restore = firstUse(status: .notDetermined, inFront: false, answer: .granted, asked: asked)
        defer { restore() }
        let result = await PhoneToolRegistry.call(name: "phone_reminders_list", args: .object([:])) { _ in true }
        #expect(result.isError)
        #expect(text(of: result.content).contains("not open on the iPhone"))
        #expect(asked.count == 0)
    }

    /// The dialog comes after a write's card: a write the user turned down never asks iOS.
    @Test func aDeclinedWriteNeverAsks() async {
        let asked = AskCount()
        let restore = firstUse(status: .notDetermined, inFront: true, answer: .granted, asked: asked)
        defer { restore() }
        let result = await PhoneToolRegistry.call(
            name: "phone_calendar_create_event",
            args: .object([
                "title": .string("Team sync"),
                "start": .string("2026-10-24T14:00:00+08:00"),
                "end": .string("2026-10-24T15:00:00+08:00"),
            ])
        ) { _ in false }
        #expect(result.isError)
        #expect(asked.count == 0)
    }

    /// Turns calendar and reminders on and replaces what a first use reads and asks; the result puts everything back.
    private func firstUse(status: PhoneAuthStatus, inFront: Bool, answer: PhoneAuthStatus, asked: AskCount) -> () -> Void {
        let calendar = PhoneDataSource.calendar, reminders = PhoneDataSource.reminders
        let before = (calendar.isEnabled, reminders.isEnabled)
        let saved = (PhoneToolRegistry.accessStatus, PhoneToolRegistry.inFront, PhoneToolRegistry.askAccess)
        calendar.isEnabled = true
        reminders.isEnabled = true
        PhoneToolRegistry.accessStatus = { _ in status }
        PhoneToolRegistry.inFront = { inFront }
        PhoneToolRegistry.askAccess = { _ in
            asked.count += 1
            try? await Task.sleep(for: .milliseconds(50)) // the dialog is on screen while a second call arrives
            return answer
        }
        return {
            calendar.isEnabled = before.0
            reminders.isEnabled = before.1
            (PhoneToolRegistry.accessStatus, PhoneToolRegistry.inFront, PhoneToolRegistry.askAccess) = saved
        }
    }

    @Test func disabledSourceMeansError() async {
        let source = PhoneDataSource.clipboard
        let previous = source.isEnabled
        source.isEnabled = false
        defer { source.isEnabled = previous }

        let result = await PhoneToolRegistry.call(name: "phone_clipboard_get", args: .object([:])) { _ in true }
        #expect(result.isError)
        #expect(text(of: result.content).contains("turned off"))
    }

    @Test func deniedSaysWhereToAllowIt() {
        let message = ToolErrors.describe(ToolFailure(.denied, "日历 / Calendar"))
        #expect(message.contains("设置 › 连接器 › 这台 iPhone"))
        #expect(message.contains("iOS 设置 › Persona"), "a source refused before can only be allowed in iOS Settings")
        #expect(!message.contains("Phone data"), "the page was renamed")
    }

    // MARK: - Dispatch

    @Test func unknownToolIsAnError() async {
        var asked = false
        let result = await PhoneToolRegistry.call(name: "phone_does_not_exist", args: .object([:])) { _ in
            asked = true
            return true
        }
        #expect(result.isError)
        #expect(text(of: result.content).contains("Unknown tool"))
        #expect(!asked, "an unknown tool must not prompt the user")
    }

    @Test func decliningConfirmationStopsTheTool() async {
        let source = PhoneDataSource.calendar
        let previous = source.isEnabled
        source.isEnabled = true
        defer { source.isEnabled = previous }

        nonisolated(unsafe) var summary: String?
        let result = await PhoneToolRegistry.call(
            name: "phone_calendar_create_event",
            args: .object([
                "title": .string("Team sync"),
                "start": .string("2026-10-24T14:00:00+08:00"),
                "end": .string("2026-10-24T15:00:00+08:00"),
            ])
        ) { prompt in
            summary = prompt
            return false
        }
        #expect(result.isError)
        #expect(text(of: result.content) == "The user declined on the phone.")
        #expect(summary?.contains("Team sync") == true)
        #expect(summary?.contains("14:00") == true)
    }

    @Test func calendarTruncatedCountsOnlyMatchingEvents() {
        let isEven: (Int) -> Bool = { $0 % 2 == 0 }
        // Exactly `limit` matches; the rest were rejected by the filter: nothing was cut off.
        let exact = CalendarTool.firstMatches([1, 2, 3, 4, 5], limit: 2, where: isEven)
        #expect(exact.kept == [2, 4])
        #expect(exact.truncated == false)
        // A third match exists past the limit.
        let over = CalendarTool.firstMatches([1, 2, 3, 4, 5, 6], limit: 2, where: isEven)
        #expect(over.kept == [2, 4])
        #expect(over.truncated == true)
        // No filter: more items than the limit.
        let all = CalendarTool.firstMatches([1, 2, 3], limit: 2) { _ in true }
        #expect(all.kept == [1, 2])
        #expect(all.truncated == true)
        // Fewer items than the limit.
        let few = CalendarTool.firstMatches([1, 2], limit: 5) { _ in true }
        #expect(few.kept == [1, 2])
        #expect(few.truncated == false)
    }

    @Test func completingAReminderNamesItOnTheCard() {
        let summary = RemindersTool.completeSummary(
            id: "ABC-123",
            target: .init(title: "Buy milk", list: "Groceries", due: "2026-10-24T18:00:00+08:00")
        )
        #expect(summary == "把「Buy milk」（Groceries）标记为已完成\n到期：2026-10-24T18:00:00+08:00")
        #expect(!summary.contains("ABC-123"))

        let bare = RemindersTool.completeSummary(id: "ABC-123", target: .init(title: "", list: nil, due: nil))
        #expect(bare == "把「(无标题)」标记为已完成")
    }

    @Test func completingAnUnknownReminderSaysSoAndKeepsTheId() {
        #expect(RemindersTool.completeSummary(id: "ABC-123", target: nil) == "找不到这条提醒（或没有提醒事项权限）：ABC-123")
        #expect(RemindersTool.completeSummary(id: nil, target: nil) == "找不到这条提醒（或没有提醒事项权限）：?")
    }

    /// The approval card the registry shows looks the id up (#2381): a made-up one matches nothing, and the card says
    /// so instead of asking to complete a bare id.
    @Test func theCompleteCardLooksTheReminderUp() {
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_reminders_complete" }
        let summary = tool?.confirmation?(.object(["id": .string("no-such-reminder")]))
        #expect(summary == "找不到这条提醒（或没有提醒事项权限）：no-such-reminder")
    }

    @Test func readOnlyToolsDoNotAskForConfirmation() async {
        var asked = false
        // Developer tools need no permission at all, so this runs end to end.
        let result = await PhoneToolRegistry.call(name: "phone_device_info", args: .object([:])) { _ in
            asked = true
            return true
        }
        #expect(!asked)
        #expect(!result.isError)
        #expect(text(of: result.content).contains("ios_version"))
    }

    @Test func toolsWithConfirmationAreAllWrites() {
        let writes = Set([
            "phone_calendar_create_event",
            "phone_reminders_create",
            "phone_reminders_complete",
            "phone_reminders_update",
            "phone_reminders_delete",
            "phone_health_write_sample",
            "phone_calendar_update_event",
            "phone_calendar_delete_event",
            "phone_contacts_create",
            "phone_contacts_update",
            "phone_alarm_create",
            "phone_timer_start",
            "phone_alarm_cancel",
            "phone_call_start",
            "phone_shortcuts_register",
            "phone_music_create_playlist",
            "phone_home_control",
            "phone_home_run_scene",
        ])
        for tool in PhoneToolRegistry.all where tool.confirmation != nil {
            #expect(writes.contains(tool.spec.name), "unexpected confirmation on \(tool.spec.name)")
        }
        for name in writes {
            let tool = PhoneToolRegistry.all.first { $0.spec.name == name }
            #expect(tool?.confirmation != nil, "\(name) changes data and must ask first")
        }
    }

    @Test func everyHealthSummaryQueryAsksWhatItsTypeCanAnswer() {
        // Summing a discrete type (weight, resting heart rate) raises inside HealthKit and took the app down (#914).
        for (metric, identifier) in HealthTool.quantityIdentifiers {
            let type = HKQuantityType(identifier)
            let options = HealthTool.statisticsOptions(for: type, metric: metric)
            if type.aggregationStyle == .cumulative {
                #expect(options == .cumulativeSum, "\(metric)")
            } else {
                #expect(!options.contains(.cumulativeSum), "\(metric) is discrete: summing it raises in HealthKit")
            }
        }
    }

    @Test func nutritionAndRestingEnergyUseDailyTotalsAndTheirPublicUnits() throws {
        let energy = HKQuantity(unit: .joule(), doubleValue: 418_400)
        let nutrients = HKQuantity(unit: .gramUnit(with: .milli), doubleValue: 12_500)
        let metrics: [(String, HKQuantityTypeIdentifier, HKQuantity, Double)] = [
            ("dietary_energy", .dietaryEnergyConsumed, energy, 100),
            ("protein", .dietaryProtein, nutrients, 12.5),
            ("carbs", .dietaryCarbohydrates, nutrients, 12.5),
            ("fat", .dietaryFatTotal, nutrients, 12.5),
            ("resting_energy", .basalEnergyBurned, energy, 100),
        ]
        let summary = try #require(HealthTool.tools.first { $0.spec.name == "phone_health_summary" })
        let advertised = try #require(summary.spec.inputSchema["properties"]?["metrics"]?["items"]?["enum"]?.array)
        for (metric, identifier, sample, expected) in metrics {
            let actualIdentifier = try #require(HealthTool.quantityIdentifiers[metric])
            #expect(actualIdentifier == identifier)
            let type = HKQuantityType(actualIdentifier)
            #expect(advertised.contains(.string(metric)), "\(metric) must be callable through the hub")
            #expect(HealthTool.readTypes.contains(type), "\(metric) needs read permission")
            #expect(HealthTool.statisticsOptions(for: type, metric: metric) == .cumulativeSum)
            #expect(abs(sample.doubleValue(for: HealthTool.quantityUnit(for: metric)) - expected) < 0.001)
        }
    }

    // MARK: - Helpers

    @Test func argumentClampingStaysInRange() {
        let args = JSONValue.object(["limit": .number(9999)])
        #expect(ToolArgs.int(args, "limit", fallback: 20, min: 1, max: 200) == 200)
        #expect(ToolArgs.int(.object(["limit": .number(-5)]), "limit", fallback: 20, min: 1, max: 200) == 1)
        #expect(ToolArgs.int(.object([:]), "limit", fallback: 20, min: 1, max: 200) == 20)
        #expect(ToolArgs.string(.object(["q": .string("   ")]), "q") == nil)
        #expect(ToolArgs.string(.object(["q": .string(" hi ")]), "q") == "hi")
    }

    @Test func datesRoundTripAndRejectGarbage() throws {
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        let text = ToolDate.string(now)
        let parsed = try ToolDate.parse(text)
        #expect(parsed != nil)
        #expect(abs((parsed ?? .distantPast).timeIntervalSince(now)) < 1)

        // A bare local date is accepted so "2026-10-24" works in filters.
        #expect(try ToolDate.parse("2026-10-24") != nil)
        // Local times with seconds and no offset, as models often write them, read as the phone's own time.
        let local = DateComponents(calendar: .current, timeZone: .current, year: 2026, month: 10, day: 24, hour: 14, minute: 0, second: 30).date
        #expect(try ToolDate.parse("2026-10-24T14:00:30") == local)
        #expect(try ToolDate.parse("2026-10-24 14:00:30") == local)
        #expect(try ToolDate.parse("") == nil)
        #expect(try ToolDate.parse(nil) == nil)
        #expect(throws: (any Error).self) { try ToolDate.parse("next tuesday") }
    }

    @Test func schemaBuilderProducesObjects() {
        let schema = Schema.object(["a": Schema.string("x"), "b": Schema.integer("y", min: 1, max: 3)], required: ["a"])
        guard case .object(let dict) = schema else {
            Issue.record("schema is not an object")
            return
        }
        #expect(dict["type"] == .string("object"))
        #expect(dict["additionalProperties"] == .bool(false))
        #expect(dict["required"] == .array([.string("a")]))
    }

    // MARK: - Clipboard (#4192)

    /// Persona in front: the write lands and answers ok with its length, and the read gives the text back.
    @Test func clipboardInFrontCopiesAndReads() throws {
        let board = FakeClipboard()
        let set = try ClipboardTool.set(.object(["text": .string("héllo")]), board: board, inFront: true)
        #expect(text(of: set) == JSONValue.object(["ok": .bool(true), "characters": .number(5)]).pretty)
        #expect(board.changeCount == 1)
        let get = try ClipboardTool.get(board: board, inFront: true)
        #expect(text(of: get) == JSONValue.object(["text": .string("héllo"), "characters": .number(5)]).pretty)
        // Clearing it is still a write that answers ok, though no string is left.
        let clear = try ClipboardTool.set(.object(["text": .string("")]), board: board, inFront: true)
        #expect(text(of: clear) == JSONValue.object(["ok": .bool(true), "characters": .number(0)]).pretty)
    }

    /// Persona in the background (a silent push woke it, or a connection outlived the app on screen): iOS would drop
    /// the write and read nothing, so neither tool touches the pasteboard and both ask for Persona to be opened,
    /// rather than answering ok or "empty".
    @Test func clipboardInBackgroundRefuses() {
        let board = FakeClipboard(string: "earlier")
        let set = failure { try ClipboardTool.set(.object(["text": .string("new")]), board: board, inFront: false) }
        let get = failure { try ClipboardTool.get(board: board, inFront: false) }
        for message in [set, get] {
            #expect(message.contains("Persona is not open on the iPhone"))
            #expect(message.contains("open Persona and try again"))
        }
        #expect(board.changeCount == 0 && board.reads == 0)
        // Bad arguments still say so first: opening Persona would not fix them.
        let missing = failure { try ClipboardTool.set(.object([:]), board: board, inFront: false) }
        #expect(missing.contains("text is required"))
    }

    /// In front, but the write did not land (the change count did not move, or no string is there): a failure, never ok.
    @Test func clipboardWriteThatDidNotLandFails() {
        let secret = "tok-8f3a1c"
        let dropped = FakeClipboard(string: "earlier")
        dropped.takesWrites = false
        let message = failure { try ClipboardTool.set(.object(["text": .string(secret)]), board: dropped, inFront: true) }
        #expect(message.contains("did not reach the iPhone's clipboard"))
        #expect(!message.contains(secret), "the refusal never quotes the text (it may be a secret read from a file)")

        let emptied = FakeClipboard()
        emptied.keepsStrings = false // the count moved, yet no string arrived
        let noString = failure { try ClipboardTool.set(.object(["text": .string(secret)]), board: emptied, inFront: true) }
        #expect(noString.contains("did not reach the iPhone's clipboard"))
    }

    /// The real pasteboard does what the check expects: a write by the app in front (the test host) moves `changeCount`
    /// at once and leaves a string, so a write that landed is never reported as lost.
    @Test func clipboardCheckPassesOnTheRealPasteboard() throws {
        let set = try ClipboardTool.set(.object(["text": .string("Persona clipboard check")]), board: UIPasteboard.general, inFront: true)
        #expect(text(of: set) == JSONValue.object(["ok": .bool(true), "characters": .number(23)]).pretty)
    }

    /// The model's text of the error a tool call throws, or "" when it did not throw.
    private func failure(of call: () throws -> [ToolContent]) -> String {
        do {
            _ = try call()
            Issue.record("the call did not fail")
            return ""
        } catch {
            return ToolErrors.describe(error)
        }
    }

    private func text(of content: [ToolContent]) -> String {
        content.compactMap(\.text).joined(separator: "\n")
    }
}

/// How many times a test's first use asked iOS (#7659).
@MainActor private final class AskCount {
    var count = 0
}

/// A pasteboard that takes writes like iOS does for the app in front, or drops them (`takesWrites`) or keeps no string
/// (`keepsStrings`), and counts reads.
@MainActor private final class FakeClipboard: ClipboardBoard {
    var takesWrites = true
    var keepsStrings = true
    private(set) var changeCount = 0
    private(set) var reads = 0
    private var stored: String?

    init(string: String? = nil) {
        stored = string
    }

    var string: String? {
        get {
            reads += 1
            return stored
        }
        set {
            guard takesWrites else { return }
            changeCount += 1
            stored = keepsStrings ? newValue : nil
        }
    }

    var hasStrings: Bool { !(stored ?? "").isEmpty }
}
