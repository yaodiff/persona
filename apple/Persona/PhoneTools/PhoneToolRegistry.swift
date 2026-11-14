import AVFoundation
import Contacts
import CoreLocation
import EventKit
import Foundation
import HealthKit
import Photos
import UserNotifications

// MARK: - Authorization

/// What the user has allowed for one group of phone data.
enum PhoneAuthStatus: String, Sendable {
    case notDetermined, granted, denied, limited, notNeeded

    var isUsable: Bool { self == .granted || self == .limited || self == .notNeeded }

    var title: String {
        switch self {
        case .notDetermined: String(localized: "Not Allowed Yet")
        case .granted: String(localized: "Allowed [Phone Tool Registry 521e65]")
        case .denied: String(localized: "Denied")
        case .limited: String(localized: "Limited [Phone Tool Registry 027f40]")
        case .notNeeded: String(localized: "Not Needed")
        }
    }
}

/// Everything the registry knows about permissions, so the settings screen and
/// the tools agree on one answer.
enum PhoneAuthorization {
    static func status(_ source: PhoneDataSource) -> PhoneAuthStatus {
        switch source {
        case .contacts: ContactsAccess.status()
        case .calendar: CalendarAccess.status()
        case .reminders: CalendarAccess.reminderStatus()
        case .location: LocationAccess.status()
        case .health: HealthAccess.cachedStatus()
        case .photos: PhotosAccess.status()
        case .notifications: NotificationsAccess.cachedStatus()
        case .device, .clipboard: .notNeeded
        case .alarms, .messages, .calls, .shortcuts, .media, .home: SystemAccess.status(source)
        }
    }

    static func request(_ source: PhoneDataSource) async -> PhoneAuthStatus {
        switch source {
        case .contacts: await ContactsAccess.request()
        case .calendar: await CalendarAccess.requestEvents()
        case .reminders: await CalendarAccess.requestReminders()
        case .location: await LocationAccess.request()
        case .health: await HealthAccess.request()
        case .photos: await PhotosAccess.request()
        case .notifications: await NotificationsAccess.request()
        case .device, .clipboard: .notNeeded
        case .alarms, .messages, .calls, .shortcuts, .media, .home: await SystemAccess.request(source)
        }
    }
}

// MARK: - Registry

/// The catalogue of `phone_*` tools Claude can call, and the single entry point
/// the WebSocket layer uses to run one.
enum PhoneToolRegistry {
    /// Every tool, whatever the user's toggles say. Tests walk this list.
    static var all: [PhoneTool] {
        ContactsTool.tools
            + CalendarTool.tools
            + RemindersTool.tools
            + LocationTool.tools
            + healthTools
            + PhotosTool.tools
            + DeviceTool.tools
            + ClipboardTool.tools
            + NotificationsTool.tools
            + MapsTool.tools
            + AlarmsTool.tools
            + MessagesTool.tools
            + CallTool.tools
            + ShortcutsTool.tools
            + MediaTool.tools
            + HomeTool.tools
    }

    /// A Mac has HealthKit's API but no health data: no health tools there.
    private static var healthTools: [PhoneTool] {
        #if os(iOS) || os(visionOS)
        HealthTool.tools + WorkoutTool.tools
        #else
        []
        #endif
    }

    /// The tool definitions announced to the hub in `register_phone`; `eventKit`: calendar's and reminders'
    /// authorizations asked already (off the main thread, `CalendarAccess.statusesOffMain`). Each source is asked once.
    /// A source iOS hasn't asked about yet is offered when its first use asks (`PhoneDataSource.offered`, #7659).
    static func enabledSpecs(eventKit: (events: PhoneAuthStatus, reminders: PhoneAuthStatus)? = nil) -> [ToolSpec] {
        var active: [PhoneDataSource: Bool] = [:]
        func usable(_ source: PhoneDataSource) -> Bool {
            if let known = active[source] { return known }
            let status = switch (source, eventKit) {
            case (.calendar, let ek?): ek.events
            case (.reminders, let ek?): ek.reminders
            default: PhoneAuthorization.status(source)
            }
            let now = source.isEnabled && source.offered(status)
            active[source] = now
            return now
        }
        return all.filter { usable($0.source) }.map(\.spec)
    }

    /// Current permission state of one data source, for the settings screen.
    static func authorizationStatus(_ source: PhoneDataSource) -> PhoneAuthStatus {
        PhoneAuthorization.status(source)
    }

    /// Asks the system for access; the settings screen calls this from its
    /// toggle or button.
    static func requestAccess(_ source: PhoneDataSource) async -> PhoneAuthStatus {
        await PhoneAuthorization.request(source)
    }

    /// Runs one tool call. Never throws: failures come back as an error result
    /// with a message meant for Claude and for the transcript on the phone.
    /// In order: the source's switch, the write's confirmation card, iOS's permission dialog for a source it hasn't
    /// asked about yet (`askOnFirstUse`), then the tool.
    static func call(
        name: String,
        args: JSONValue,
        confirm: (String) async -> Bool
    ) async -> (content: [ToolContent], isError: Bool) {
        guard let tool = all.first(where: { $0.spec.name == name }) else {
            return ([.text("未知工具 / Unknown tool: \(name). 可用工具 / Available tools: \(enabledSpecs().map(\.name).joined(separator: ", ")).")], true) // i18n-ignore
        }
        guard tool.source.isEnabled else {
            return ([.text("“\(tool.source.title)” 已在手机上关闭 / The \(tool.source.rawValue) data source is turned off on the phone. " // i18n-ignore
                + "The user can enable it in Persona › 设置 › 连接器 › 这台 iPhone.")], true) // i18n-ignore
        }
        if let summary = tool.confirmation?(args) {
            let allowed = await confirm(summary)
            guard allowed else {
                return ([.text("The user declined on the phone.")], true)
            }
        }
        #if os(iOS)
        // After the card: a write the user turned down never asks iOS.
        if let refusal = await askOnFirstUse(tool.source) {
            return ([.text(refusal)], true)
        }
        #endif
        do {
            let content = try await tool.run(args)
            return (content.isEmpty ? [.text("完成 / Done.")] : content, false) // i18n-ignore
        } catch {
            return ([.text(ToolErrors.describe(error))], true)
        }
    }

    #if os(iOS)
    /// What a first use reads, whether Persona is in front, and the question (#7659): tests replace them, so no system
    /// dialog shows in a test run.
    static var accessStatus: (PhoneDataSource) async -> PhoneAuthStatus = { await currentStatus($0) }
    static var inFront: () -> Bool = { Foreground.isActive }
    static var askAccess: (PhoneDataSource) async -> PhoneAuthStatus = { await PhoneAuthorization.request($0) }

    /// The question on screen for each source: calls that arrive together wait for the same answer.
    private static var asking: [PhoneDataSource: Task<PhoneAuthStatus, Never>] = [:]

    /// The first use of a source iOS hasn't asked about (`PhoneDataSource.asksWhenUsed`, #7659): iOS shows its own
    /// permission dialog now, which it can only do while Persona is in front. nil: the call goes on; else what it answers.
    /// Either answer re-registers (`Notification.Name.phoneAccessAnswered`): a refused source leaves the hub's list.
    private static func askOnFirstUse(_ source: PhoneDataSource) async -> String? {
        guard source.asksWhenUsed else { return nil }
        if asking[source] == nil {
            guard await accessStatus(source) == .notDetermined else { return nil }
            guard inFront() else {
                return "Persona is not open on the iPhone, so iOS cannot ask the user for \(source.title) access yet. "
                    + "Ask the user to open Persona on the iPhone and try again: iOS asks once, then this works."
            }
        }
        let ask = askAccess
        let question = asking[source] ?? Task { await ask(source) } // another call may have asked while this one read
        asking[source] = question
        let answer = await question.value
        asking[source] = nil
        NotificationCenter.default.post(name: .phoneAccessAnswered, object: nil)
        return answer.isUsable ? nil : ToolErrors.describe(ToolFailure(.denied, source.title))
    }

    /// Calendar's and reminders' answers are read off the main thread, as the registration reads them (#1337).
    private static func currentStatus(_ source: PhoneDataSource) async -> PhoneAuthStatus {
        switch source {
        case .calendar: await CalendarAccess.statusesOffMain().events
        case .reminders: await CalendarAccess.statusesOffMain().reminders
        default: PhoneAuthorization.status(source)
        }
    }
    #endif
}

extension Notification.Name {
    /// A tool call asked iOS for a source's permission (#7659): the phone's registration follows the answer.
    static let phoneAccessAnswered: Notification.Name = Notification.Name("Persona.phoneAccessAnswered")
}
