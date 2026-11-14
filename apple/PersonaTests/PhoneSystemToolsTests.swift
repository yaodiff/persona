import Foundation
import MapKit
import Testing
@testable import Persona
import PersonaWire

/// The iPhone system integrations (#2276–#2286): what each tool reads from its arguments, what its approval card says,
/// and the rules that keep it from acting on a guess (an ambiguous occurrence, an overwritten field, an unallowed shortcut).
@MainActor
struct PhoneSystemToolsTests {
    private func json(_ text: String) throws -> JSONValue {
        try JSONDecoder().decode(JSONValue.self, from: Data(text.utf8))
    }

    private func text(of content: [ToolContent]) -> String {
        content.compactMap(\.text).joined(separator: "\n")
    }

    // MARK: - Sources

    /// They act outside Persona: nothing is offered until the user turns a source on.
    @Test func theIPhoneIntegrationsStartOff() {
        for source in PhoneDataSource.iPhoneOnly {
            let key = "phoneSource.\(source.rawValue)"
            let saved = UserDefaults.standard.object(forKey: key)
            defer { UserDefaults.standard.set(saved, forKey: key) }
            UserDefaults.standard.removeObject(forKey: key)
            #expect(!source.isEnabled, "\(source.rawValue) must be opt-in")
        }
    }

    @Test func homeIsOfferedOnlyInABuildWithHomeKit() {
        #expect(PhoneDataSource.onThisDevice.contains(.home) == HomeAccess.isBuilt)
        #expect(PhoneDataSource.onThisDevice.contains(.alarms))
    }

    // MARK: - Calendar (#2276)

    @Test func aNewStartKeepsTheEventsLength() throws {
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        let change = try CalendarTool.EventChange.parse(json(#"{"id":"e","start":"2027-01-15T15:00:00+08:00"}"#),
                                                        currentStart: start, currentEnd: start.addingTimeInterval(1800))
        let newStart = try #require(change.start)
        #expect(change.end == newStart.addingTimeInterval(1800))
    }

    @Test func aChangeWithNothingOrAnEndBeforeTheStartIsRefused() {
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(throws: ToolFailure.self) {
            try CalendarTool.EventChange.parse(json(#"{"id":"e"}"#), currentStart: start, currentEnd: start)
        }
        #expect(throws: ToolFailure.self) {
            try CalendarTool.EventChange.parse(json(#"{"id":"e","start":"2027-01-15T15:00:00+08:00","end":"2027-01-15T14:00:00+08:00"}"#),
                                               currentStart: start, currentEnd: start)
        }
    }

    @Test func theSpanIsThisUnlessFutureIsAsked() throws {
        #expect(try CalendarTool.parseSpan(json(#"{}"#)) == .thisEvent)
        #expect(try CalendarTool.parseSpan(json(#"{"span":"future"}"#)) == .futureEvents)
        #expect(throws: ToolFailure.self) { try CalendarTool.parseSpan(json(#"{"span":"all"}"#)) }
    }

    @Test func theChangeCardShowsBeforeAndAfterAndTheSpan() throws {
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        let target = CalendarTool.EventTarget(title: "Dentist", start: start, end: start.addingTimeInterval(3600), allDay: false,
                                              calendar: "Home", location: nil, recurring: true)
        let change = try CalendarTool.EventChange.parse(json(#"{"id":"e","start":"2027-01-15T15:00:00+08:00","title":"Dentist (moved)"}"#),
                                                        currentStart: target.start, currentEnd: target.end)
        let card = CalendarTool.changeSummary(target, change: change, future: false)
        #expect(card.contains("Dentist") && card.contains("Dentist (moved)") && card.contains("→") && card.contains("Home"))
        #expect(card.contains(String(localized: "Only this occurrence")))
        let future = CalendarTool.changeSummary(target, change: change, future: true)
        #expect(future.contains(String(localized: "This and all future occurrences")))
        let delete = CalendarTool.deleteSummary(target, future: false)
        #expect(delete.contains("Dentist") && delete.contains(String(localized: "Only this occurrence")))
    }

    /// A made-up id matches nothing: the card says so and the tool then fails without deleting.
    @Test func theDeleteCardForAnUnknownEventSaysSo() {
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_calendar_delete_event" }
        let card = tool?.confirmation?(.object(["id": .string("no-such-event")]))
        #expect(card?.contains("no-such-event") == true)
    }

    @Test func theNewEventCardShowsEverythingItWillWrite() throws {
        guard let start = try ToolDate.parse("2027-01-15T15:00:00+08:00"),
              let end = try ToolDate.parse("2027-01-15T16:00:00+08:00") else {
            Issue.record("valid test dates required")
            return
        }
        let notes = "bring gift " + String(repeating: "n", count: 240)
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_calendar_create_event" }
        let card = try #require(tool?.confirmation?(.object([
            "title": .string("Dinner"), "start": .string("2027-01-15T15:00:00+08:00"),
            "end": .string("2027-01-15T16:00:00+08:00"), "calendar": .string("Family"), "notes": .string(notes),
        ])))
        #expect(card.contains("Family"))
        #expect(card.contains(String(notes.prefix(200))))
        #expect(!card.contains(String(notes.prefix(201))))
        let timeZone = try #require(TimeZone(secondsFromGMT: 8 * 3600))
        #expect(card.contains(CardTime.text(start, timeZone: timeZone)))
        #expect(card.contains(CardTime.text(end, timeZone: timeZone)))
        #expect(!card.contains("2027-01-15T15:00:00+08:00"))
    }

    // MARK: - Reminders (#2277)

    @Test func theNewReminderCardShowsEverythingItWillWrite() throws {
        guard let due = try ToolDate.parse("2027-01-15T18:00:00+08:00") else {
            Issue.record("valid test due date required")
            return
        }
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_reminders_create" }
        let card = try #require(tool?.confirmation?(.object([
            "title": .string("Expenses"), "due": .string("2027-01-15T18:00:00+08:00"),
            "notes": .string("call first"), "list": .string("Personal"), "priority": .number(1),
        ])))
        #expect(card.contains("Personal") && card.contains("call first"))
        #expect(card.contains(RemindersTool.priorityName(1)))
        #expect(card.contains(CardTime.text(due)))
        #expect(!card.contains("2027-01-15T18:00:00+08:00"))
    }

    @Test func theReminderCardNamesNotesPriorityAndList() throws {
        let target = RemindersTool.CompletionTarget(title: "Expenses", list: "Work", due: nil)
        let card = RemindersTool.changeSummary(id: "r1", target: target,
                                               args: try json(#"{"id":"r1","notes":"call first","priority":1,"list":"Personal"}"#))
        #expect(card.contains("call first") && card.contains("Personal"))
        #expect(card.contains(RemindersTool.priorityName(1)))
        let cleared = RemindersTool.changeSummary(id: "r1", target: target, args: try json(#"{"id":"r1","notes":null}"#))
        #expect(cleared.contains(String(localized: "Notes removed")))
    }

    // MARK: - Contacts (#2286)

    @Test func aNewContactNeedsAName() throws {
        #expect(throws: ToolFailure.self) { try ContactsTool.ContactEdit.plan(current: nil, args: json(#"{"job_title":"Chef"}"#)) }
        let edit = try ContactsTool.ContactEdit.plan(current: nil, args: json(#"{"given_name":"Sam","phones":[{"value":"555-0100","label":"mobile"}]}"#))
        #expect(edit.fields.map(\.key) == ["given_name"])
        #expect(edit.phones == [.init(value: "555-0100", label: "mobile")])
        #expect(ContactsTool.createSummary(edit).contains("Sam"))
    }

    @Test func anExistingFieldIsNotOverwrittenUnlessAsked() throws {
        let current = ContactsTool.ContactFields(givenName: "Sam", company: "Acme", phones: ["555 0100"])
        #expect(throws: ToolFailure.self) {
            try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","company":"Initech"}"#))
        }
        let replaced = try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","company":"Initech","replace":true}"#))
        #expect(replaced.fields == [.init(key: "company", before: "Acme", after: "Initech")])
        // An empty field is filled without replace.
        let filled = try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","job_title":"Chef"}"#))
        #expect(filled.fields == [.init(key: "job_title", before: "", after: "Chef")])
    }

    @Test func phonesAndEmailsAreAddedOnceAndReplacedOnlyWhenAsked() throws {
        let current = ContactsTool.ContactFields(givenName: "Sam", phones: ["555 0100"], emails: ["sam@example.com"])
        let added = try ContactsTool.ContactEdit.plan(current: current,
            args: json(#"{"id":"c","phones":["555-0100","555-0199"],"emails":["SAM@example.com","s@example.org"]}"#))
        #expect(added.phones.map(\.value) == ["555-0199"])
        #expect(added.emails.map(\.value) == ["s@example.org"])
        #expect(!added.replacePhones && !added.replaceEmails)
        #expect(throws: ToolFailure.self) {
            try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","phones":["555-0100"]}"#))
        }
        let replaced = try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","phones":["555-0199"],"replace":true}"#))
        #expect(replaced.replacePhones && replaced.phones.map(\.value) == ["555-0199"])
        #expect(ContactsTool.updateSummary(current, replaced).contains("555-0199"))
        #expect(throws: ToolFailure.self) {
            try ContactsTool.ContactEdit.plan(current: current, args: json(#"{"id":"c","emails":["not an email"]}"#))
        }
    }

    // MARK: - Calls (#2280)

    @Test func aCallTakesOneNumberAndNothingElse() throws {
        let call = try CallTool.CallRequest.parse(json(#"{"number":"+1 (555) 010-0199","kind":"phone","name":"Sam"}"#))
        #expect(call.address == "+15550100199")
        #expect(call.url?.absoluteString == "tel:+15550100199")
        #expect(CallTool.summary(call).contains("Sam") && CallTool.summary(call).contains("+15550100199"))
        #expect(throws: ToolFailure.self) { try CallTool.CallRequest.parse(json(#"{"number":"555-0100 or 555-0199"}"#)) }
        #expect(throws: ToolFailure.self) { try CallTool.CallRequest.parse(json(#"{"number":"12"}"#)) }
        let facetime = try CallTool.CallRequest.parse(json(#"{"number":"me@example.com","kind":"facetime_audio"}"#))
        #expect(facetime.url?.absoluteString == "facetime-audio:me@example.com")
    }

    // MARK: - Messages (#2279)

    @Test func aDraftNeedsRecipientsAndText() throws {
        let draft = try MessagesTool.Draft.parse(json(#"{"recipients":[" 555-0100 ","me@example.com"],"body":"Home at 7"}"#))
        #expect(draft == .init(recipients: ["555-0100", "me@example.com"], body: "Home at 7"))
        #expect(throws: ToolFailure.self) { try MessagesTool.Draft.parse(json(#"{"recipients":[],"body":"x"}"#)) }
        #expect(throws: ToolFailure.self) { try MessagesTool.Draft.parse(json(#"{"recipients":["Mom"],"body":"x"}"#)) }
        #expect(throws: ToolFailure.self) { try MessagesTool.Draft.parse(json(#"{"recipients":["555-0100"],"body":"  "}"#)) }
    }

    /// The compose sheet is the user's own Send: no extra card, and the hub waits for it like a confirmed write.
    @Test func composingAsksNoCard() {
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_messages_compose" }
        #expect(tool != nil && tool?.confirmation == nil)
    }

    // MARK: - Maps (#2281)

    @Test func aDestinationNeedsCoordinatesAndAKnownMode() throws {
        let walk = try MapsTool.Destination.parse(json(#"{"latitude":31.2,"longitude":121.4,"name":"Cafe","mode":"walking"}"#))
        #expect(walk.mode == .walking && walk.name == "Cafe")
        #expect(try MapsTool.Destination.parse(json(#"{"latitude":1,"longitude":2}"#)).mode == .driving)
        #expect(throws: ToolFailure.self) { try MapsTool.Destination.parse(json(#"{"latitude":91,"longitude":2}"#)) }
        #expect(throws: ToolFailure.self) { try MapsTool.Destination.parse(json(#"{"latitude":1,"longitude":2,"mode":"flying"}"#)) }
    }

    /// A route starts at the phone unless both start coordinates are given (#5544); each mode asks Maps for its own kind
    /// of route.
    @Test func aRouteStartsAtThePhoneOrAtBothCoordinatesGiven() throws {
        #expect(try MapsTool.routeStart(json(#"{"latitude":1,"longitude":2}"#)) == nil)
        let given = try #require(try MapsTool.routeStart(json(#"{"from_latitude":31.2,"from_longitude":121.4}"#)))
        #expect(given.latitude == 31.2 && given.longitude == 121.4)
        #expect(throws: ToolFailure.self) { try MapsTool.routeStart(json(#"{"from_latitude":31.2}"#)) }
        #expect(throws: ToolFailure.self) { try MapsTool.routeStart(json(#"{"from_latitude":31.2,"from_longitude":200}"#)) }
        let modes = try ["driving", "walking", "transit"].map {
            try MapsTool.Destination.parse(json(#"{"latitude":1,"longitude":2,"mode":"\#($0)"}"#)).transportType
        }
        #expect(modes == [.automobile, .walking, .transit])
        let tool = PhoneToolRegistry.all.first { $0.spec.name == "phone_maps_route" }
        #expect(tool != nil && tool?.confirmation == nil) // it opens nothing, so it asks nobody
    }

    /// A route's line is short enough for the model and the card: its ends stay, a straight stretch is two points, and a
    /// winding one keeps at most `RouteLine.most`.
    @Test func aRouteLineKeepsItsEndsAndStaysShort() {
        func point(_ latitude: Double, _ longitude: Double) -> CLLocationCoordinate2D {
            CLLocationCoordinate2D(latitude: latitude, longitude: longitude)
        }
        let straight = (0..<500).map { point(31.2 + Double($0) * 0.00001, 121.4 + Double($0) * 0.00002) }
        let line = RouteLine.simplified(straight)
        #expect(line.count == 2 && line.first?.latitude == straight.first?.latitude && line.last?.latitude == straight.last?.latitude)
        let winding = (0..<3000).map { i -> CLLocationCoordinate2D in
            let t = Double(i) / 3000
            return point(31.2 + t * 0.05, 121.4 + 0.01 * sin(t * 40))
        }
        let kept = RouteLine.simplified(winding)
        #expect(kept.count > 2 && kept.count <= RouteLine.most)
        #expect(kept.first?.longitude == winding.first?.longitude && kept.last?.latitude == winding.last?.latitude)
        #expect(RouteLine.simplified(Array(winding.prefix(10))).count == 10)
        #expect(RouteLine.rounded(31.2304449) == 31.23044)
    }

    // MARK: - Shortcuts (#2283)

    @Test func theRunLinkComesBackWithItsToken() throws {
        let url = try #require(ShortcutsTool.Link.run(name: "Coming Home & Lights", input: "a=b?", token: "T1"))
        let parts = try #require(URLComponents(url: url, resolvingAgainstBaseURL: false))
        #expect(url.scheme == "shortcuts" && url.host == "x-callback-url" && url.path == "/run-shortcut")
        let items = Dictionary(uniqueKeysWithValues: (parts.queryItems ?? []).map { ($0.name, $0.value ?? "") })
        #expect(items["name"] == "Coming Home & Lights")
        #expect(items["text"] == "a=b?")
        #expect(items["x-success"] == "persona://shortcut-callback/success?token=T1")

        let back = try #require(URL(string: "persona://shortcut-callback/success?token=T1&result=done"))
        let parsed = try #require(ShortcutsTool.Link.parse(back))
        #expect(parsed.token == "T1" && parsed.outcome == "success" && parsed.text == "done")
        #expect(ShortcutsTool.Link.parse(try #require(URL(string: "persona://pair?x=1"))) == nil)
    }

    @Test func onlyAnAllowedShortcutRuns() async {
        let source = PhoneDataSource.shortcuts
        let enabled = source.isEnabled
        let saved = ShortcutsTool.Allowlist.all
        defer { source.isEnabled = enabled; ShortcutsTool.Allowlist.all = saved }
        source.isEnabled = true
        ShortcutsTool.Allowlist.all = [.init(name: "Coming Home", purpose: nil)]
        #expect(ShortcutsTool.Allowlist.find("coming home", in: ShortcutsTool.Allowlist.all)?.name == "Coming Home")

        let result = await PhoneToolRegistry.call(name: "phone_shortcuts_run", args: .object(["name": .string("Delete Everything")])) { _ in true }
        #expect(result.isError)
        #expect(text(of: result.content).contains("not a shortcut the user allowed"))
    }

    // MARK: - Alarms (#2278)

    @Test func anAlarmTimeIsATimeOfDayOrAFutureMoment() throws {
        let morning = try AlarmsTool.AlarmRequest.parse(json(#"{"time":"06:40","repeat":["Mon","fri","mon"],"label":"Wake up"}"#))
        #expect(morning.when == .time(hour: 6, minute: 40, weekdays: ["mon", "fri"]))
        #expect(AlarmsTool.alarmSummary(morning).contains("06:40") && AlarmsTool.alarmSummary(morning).contains("Wake up"))
        #expect(throws: ToolFailure.self) { try AlarmsTool.AlarmRequest.parse(json(#"{"time":"25:00"}"#)) }
        #expect(throws: ToolFailure.self) { try AlarmsTool.AlarmRequest.parse(json(#"{"time":"06:40","repeat":["someday"]}"#)) }
        #expect(throws: ToolFailure.self) { try AlarmsTool.AlarmRequest.parse(json(#"{"time":"2020-01-01T06:40:00+08:00"}"#)) }
        let at = try AlarmsTool.AlarmRequest.parse(json(#"{"time":"2099-01-01T06:40:00+08:00"}"#))
        if case .at = at.when {} else { Issue.record("an ISO time is one moment") }
    }

    @Test func aTimerLastsASecondToADay() throws {
        #expect(try AlarmsTool.timerSeconds(json(#"{"seconds":1500}"#)) == 1500)
        #expect(throws: ToolFailure.self) { try AlarmsTool.timerSeconds(json(#"{"seconds":0}"#)) }
        #expect(throws: ToolFailure.self) { try AlarmsTool.timerSeconds(json(#"{"seconds":86401}"#)) }
        #expect(AlarmsTool.timerSummary(seconds: 1500, label: "Focus").contains("Focus"))
    }

    // MARK: - Home (#2285)

    @Test func homeCommandsBecomeHomeKitValues() throws {
        #expect(try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"lock","value":true}"#)).number == 1)
        #expect(try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"door","value":"true"}"#)).number == 0)
        #expect(try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"power","value":"off"}"#)).number == 0)
        #expect(try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"brightness","value":42.4}"#)).number == 42)
        #expect(throws: ToolFailure.self) { try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"brightness","value":140}"#)) }
        #expect(throws: ToolFailure.self) { try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"colour","value":1}"#)) }
    }

    @Test func aSecurityAccessorySaysSoOnTheCard() throws {
        let unlock = try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"a","control":"lock","value":false}"#))
        let card = HomeTool.summary(accessory: "Front Door", room: "Hall", sensitive: true, command: unlock)
        #expect(card.contains("Front Door") && card.contains("Hall"))
        #expect(card.contains(String(localized: "Security accessory: check before you allow it.")))
        let lamp = HomeTool.summary(accessory: "Lamp", room: nil, sensitive: false,
                                    command: try HomeTool.HomeCommand.parse(json(#"{"accessory_id":"b","control":"power","value":true}"#)))
        #expect(!lamp.contains(String(localized: "Security accessory: check before you allow it.")))
    }

    // MARK: - Media (#2284)

    @Test func otherServicesOpenASearchOnly() throws {
        #expect(MediaTool.searchURL(service: "spotify", query: "lofi beats")?.absoluteString == "https://open.spotify.com/search/lofi%20beats")
        #expect(MediaTool.searchURL(service: "youtube", query: "a&b")?.absoluteString == "https://www.youtube.com/results?search_query=a%26b")
        #expect(MediaTool.searchURL(service: "other", query: "x") == nil)
    }

    @Test func aPlaylistDraftIsCheckedBeforeTheCard() throws {
        let args = JSONValue.object([
            "name": .string(" Road Trip "),
            "songs": .array([.string("Midnight City M83"), .string("Outro M83")]),
            "play": .bool(true),
        ])
        let draft = try MediaTool.PlaylistDraft.parse(args)
        #expect(draft.name == "Road Trip" && draft.songs.count == 2 && draft.play && draft.note == nil)
        let summary = MediaTool.playlistConfirmation(args)
        #expect(summary.contains("Road Trip") && summary.contains("2"))
        #expect(try MediaTool.PlaylistDraft.parse(.object([
            "name": .string("Quiet"),
            "songs": .array([.string("Song")]),
            "play": .bool(false),
        ])).play == false)
        #expect((try? MediaTool.PlaylistDraft.parse(.object([
            "name": .string(""),
            "songs": .array([.string("Song")]),
        ]))) == nil)
        #expect((try? MediaTool.PlaylistDraft.parse(.object([
            "name": .string("Empty"),
            "songs": .array([]),
        ]))) == nil)
    }
}
