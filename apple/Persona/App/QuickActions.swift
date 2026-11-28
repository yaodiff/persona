#if os(iOS)
import UIKit

/// The home screen icon's long-press menu (#3199): the last conversation opened here (#2191, `RecentChatsStore`), then
/// Record, New Chat and Inbox; the system shows four at most, so Agents is not among them (the tab bar has it).
/// They only open a page; with no hub paired the app stays on its welcome page. Record starts recording a meeting too
/// (docs/apple/recording.md「Meeting recordings」), with or without a hub: the text syncs once there is one.
enum QuickAction: String, CaseIterable {
    /// The type is the item's identity: kept from when it was called Record a Meeting.
    case meetingRecording = "persona.meeting-recording"
    case newConversation = "persona.new-conversation"
    case inbox = "persona.inbox"
    /// A recently opened conversation: its `SessionKey.path` in the item's user info.
    static let recentType = "persona.recent"

    var item: UIApplicationShortcutItem {
        let (title, symbol): (String, String) = switch self {
        case .meetingRecording: (MeetingWords.start, Theme.Icon.recording)
        case .newConversation: (String(localized: "New Chat [Quick Actions ea4b29]"), Theme.Icon.compose)
        case .inbox: (String(localized: "Inbox"), Theme.Icon.inbox)
        }
        return UIApplicationShortcutItem(type: rawValue, localizedTitle: title, localizedSubtitle: nil,
                                         icon: UIApplicationShortcutIcon(systemImageName: symbol))
    }

    /// The menu now: at launch, when a conversation is opened and when the app goes to the background (the titles as
    /// they are then). A conversation the app no longer has is left out.
    @MainActor static func publish() {
        let model = AppModel.shared
        let recent = RecentChatsStore.shared.latest(RecentChats.homeScreen) { model.session($0) != nil }.compactMap { key -> UIApplicationShortcutItem? in
            guard let s = model.session(key) else { return nil }
            return UIApplicationShortcutItem(type: recentType, localizedTitle: s.displayTitle, localizedSubtitle: recentSubtitle(s, key: key),
                                             icon: UIApplicationShortcutIcon(systemImageName: Theme.Icon.recent), userInfo: ["key": key.path as NSString])
        }
        let actions = offered(recording: RecordingModule.allowance, hidden: model.hiddenNavigationPages)
        UIApplication.shared.shortcutItems = recent + actions.map(\.item)
    }

    /// The menu's actions on this hub: Start Meeting is left out while the hub's recording module is off (#15214). The
    /// menu is published at launch and when the app leaves the front, so one tapped before it caught up is refused in
    /// words (`MeetingRecorder.startRefusal`).
    nonisolated static func offered(recording: RecordingAllowance, hidden: Set<HubNavigationPage> = []) -> [QuickAction] {
        allCases.filter {
            switch $0 {
            case .meetingRecording: recording.offersWrites
            case .inbox: !hidden.contains(.inbox)
            case .newConversation: true
            }
        }
    }

    /// Where a recent conversation is: "Persona" for one of Persona's, else its computer and folder.
    @MainActor private static func recentSubtitle(_ s: Session, key: SessionKey) -> String {
        if s.assistant { return "Persona" }
        let machine = AppModel.shared.machine(key.machineRef)?.name ?? key.machineID
        return [machine, s.folderName].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    /// The item the menu was tapped with: true when it was one of these.
    @MainActor @discardableResult
    static func perform(_ item: UIApplicationShortcutItem, in model: AppModel = .shared) -> Bool {
        if item.type == recentType, let path = item.userInfo?["key"] as? String, let key = SessionKey(path: path) {
            model.open(key) // an Agent's chat or a Persona conversation, as a notification opens it
            return true
        }
        guard let action = QuickAction(rawValue: item.type) else { return false }
        model.shellSettings = false
        switch action {
        case .meetingRecording:
            Task { await MeetingRecorder.shared.startMeeting(inForeground: true) }
        case .newConversation:
            model.openNewAssistant()
        case .inbox:
            model.openNavigationEntry(.inbox)
        }
        return true
    }
}

@MainActor extension AppModel {
    /// The home-screen New Chat action's navigation, shared with the dictate control.
    func openNewAssistant() {
        noteNewAssistantChat()
        selectedTab = .assistant
        // As the drawer's New Chat: either router takes Assistant back to its root (a pushed inbox included); without
        // one yet, the cold launch only needs the conversation gone.
        if let router = tabRouter {
            router.show(.newConversation)
            (router as? ShellState)?.drawer.close()
        } else {
            assistantCurrent = nil
        }
    }

    /// Message Persona (#17789): the main conversation with its composer's keyboard up (`composerFocus`), from the
    /// Action button, the control or Shortcuts. A cold launch waits (at most 8 s) for the hub's main conversation, as
    /// Call Persona does; with none (a fresh install), the new conversation page, whose first message starts it.
    func openMainAssistantForTyping(waits: Int = 32) async {
        shellSettings = false
        selectedTab = .assistant
        for _ in 0..<waits where assistantMain == nil {
            try? await Task.sleep(for: .milliseconds(250))
        }
        guard let main = assistantMain else {
            openNewAssistant()
            return
        }
        openAssistantConversation(main)
        composerFocus = main
    }

    /// Clear the old conversation before the first view can run, so a cold launch keeps the control's new conversation.
    func openNewAssistantForDictation() {
        assistantCurrent = nil
        dictationRequest = DictationRequest()
        #if DEBUG
        ShellTrace.shared.note("dictation request installed")
        #endif
        openNewAssistant()
    }
}

/// The scene's delegate for the menu and for a redial from the Phone app's Recents (#9290): a tap while the app runs or
/// is in the background comes here; one that launched it comes with the scene's connection options. SwiftUI still owns
/// the window.
final class QuickActionSceneDelegate: NSObject, UIWindowSceneDelegate {
    func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options connectionOptions: UIScene.ConnectionOptions) {
        if let item = connectionOptions.shortcutItem { Task { @MainActor in QuickAction.perform(item) } }
        // Recents launched the app to call back: the call waits for the hubs (`PersonaCalls.callPersona`).
        for activity in connectionOptions.userActivities where PersonaCalls.isCall(activity) {
            Task { @MainActor in PersonaCalls.shared.continueActivity(activity) }
        }
    }

    func scene(_ scene: UIScene, continue userActivity: NSUserActivity) {
        guard PersonaCalls.isCall(userActivity) else { return }
        Task { @MainActor in PersonaCalls.shared.continueActivity(userActivity) }
    }

    func windowScene(_ windowScene: UIWindowScene, performActionFor shortcutItem: UIApplicationShortcutItem,
                     completionHandler: @escaping (Bool) -> Void) {
        Task { @MainActor in completionHandler(QuickAction.perform(shortcutItem)) }
    }
}
#endif
