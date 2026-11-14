import Foundation
import SwiftUI
import PersonaWire

nonisolated struct LarkMessageDisplay: Codable, Equatable, Sendable {
    var accountID: String
    var antiRecall: Bool
    var showEditHistory: Bool
    /// A messaging source offers its sealed mode (`GET status` modes, #2138); nil on the old API, which checks its
    /// encryption service when Encrypted message is chosen.
    var sealed: Bool? = nil

    enum CodingKeys: String, CodingKey {
        case accountID = "account_id", antiRecall = "anti_recall", showEditHistory = "show_edit_history"
        case sealed
    }
    static let hidden = Self(accountID: "", antiRecall: false, showEditHistory: false)

    /// A messaging source's sealed messages can be sent.
    var seals: Bool { sealed == true }
    /// Preserve the text and parts supplied by the message source.
    func shown(_ parts: [LarkPart]) -> [LarkPart] { parts }
    func shown(_ text: String) -> String { text }
    func summary(_ text: String) -> String { text }

    /// The switch behind a key of POST /api/v1/lark/message-display.
    func value(of key: String) -> Bool? {
        switch key {
        case "anti_recall": antiRecall
        case "show_edit_history": showEditHistory
        default: nil
        }
    }

    func showsBody(of message: LarkMessage) -> Bool {
        !message.recalled || (antiRecall && message.retained == true && !message.isSystem && !(message.parts ?? []).isEmpty)
    }
    func showsHistory(of message: LarkMessage) -> Bool {
        showEditHistory && showsBody(of: message) && !(message.editHistory ?? []).isEmpty
    }

    func forwardedPreview(_ message: JSONValue) -> String {
        if message["isRecalled"]?.bool == true && !(antiRecall && message["hasRetainedRecallContent"]?.bool == true) {
            return String(localized: "Message recalled")
        }
        let text: String
        if let raw = message["olk"], raw != .null {
            let encryption = (try? JSON.encoder.encode(raw)).flatMap { try? JSON.decoder.decode(LarkEncryption.self, from: $0) }
            text = encryption?.summary ?? String(localized: "Encrypted message")
        } else { text = summary(message["textPreview"]?.string ?? message["text"]?.string ?? "") }
        return message["isRecalled"]?.bool == true ? String(localized: "\(text) · Recalled") : text
    }
}

nonisolated struct LarkEditVersion: Codable, Hashable, Sendable {
    var editedAt: String
    var text: String
    var parts: [LarkPart]?
    var olk: LarkEncryption?

    enum CodingKeys: String, CodingKey { case editedAt = "edited_at", text, parts, olk }
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(editedAt, forKey: .editedAt)
        try c.encode(olk?.cached.summary ?? text, forKey: .text)
        try c.encodeIfPresent(olk?.publicParts ?? parts, forKey: .parts)
        try c.encodeIfPresent(olk?.cached, forKey: .olk)
    }
}

extension EnvironmentValues {
    @Entry var larkDisplay: LarkMessageDisplay = .hidden
}

extension APIClient {
    /// On a messaging source the settings are the source's to apply (#2138): retained recalls and edit history
    /// are included; encrypted messages can be chosen while the source offers the sealed mode.
    func messagingDisplay(_ source: String) async throws -> LarkMessageDisplay {
        struct Status: Decodable { var modes: [String]? }
        let user = try await messagingCall { try await messagingUser(source) }
        let status = try JSON.decoder.decode(Status.self, from: await request("GET", messagingPath(source, "status"), query: messagingQuery()))
        return LarkMessageDisplay(accountID: user, antiRecall: true, showEditHistory: true,
                                  sealed: status.modes?.contains("sealed") == true)
    }
    func larkMessageDisplay() async throws -> LarkMessageDisplay { try await get("/api/v1/lark/message-display") }
    func larkMessageDisplayChange(_ snapshot: LarkMessageDisplay, key: String, value: Bool, expected: Bool) async throws -> LarkMessageDisplay {
        struct Body: Encodable {
            var account_id: String
            var key: String
            var value: Bool
            var expected: Bool
        }
        return try await post("/api/v1/lark/message-display", body: Body(account_id: snapshot.accountID, key: key, value: value, expected: expected))
    }
}

/// Owned by one HubLink. Unknown or unverifiable account preferences always hide retained content.
@Observable final class LarkMessageDisplayModel {
    private(set) var snapshot: LarkMessageDisplay?
    private(set) var loading = false
    private(set) var saving = false
    private(set) var problem: String?
    @ObservationIgnored private var pendingRefresh: APIClient?
    @ObservationIgnored private var refreshTask: Task<Void, Never>?
    var policy: LarkMessageDisplay { snapshot ?? .hidden }

    func refresh(using api: APIClient) async {
        pendingRefresh = api
        guard !saving else { return }
        if let refreshTask { await refreshTask.value; return }
        loading = true
        // The model owns this read: leaving one view must not cancel a queued live update.
        let task = Task { await readPending() }
        refreshTask = task
        await task.value
    }

    private func readPending() async {
        defer { loading = false; refreshTask = nil }
        while let api = pendingRefresh {
            pendingRefresh = nil
            do {
                let next: LarkMessageDisplay
                if let source = api.messagingSource { next = try await api.messagingDisplay(source) }
                else { next = try await api.larkMessageDisplay() }
                // A change arrived after this read began. Only publish the subsequent read.
                guard pendingRefresh == nil else { continue }
                guard !next.accountID.isEmpty else { throw LarkOutboxIdentity.Failure.unavailable }
                snapshot = next; problem = nil
            } catch {
                guard pendingRefresh == nil else { continue }
                snapshot = nil
                if LarkLoadProblem.isNotLoggedIn(error) {
                    problem = LarkLoadProblem.of(error).message
                } else {
                    problem = (error as? APIError)?.status == 404 ? String(localized: "Update the hub and the Lark gateway first to use the message display settings.") : String(localized: "Couldn't read the settings. Connect and try again.")
                }
            }
        }
    }

    func set(_ key: String, value: Bool, using api: APIClient) async {
        guard let current = snapshot, !saving, !loading, let expected = current.value(of: key) else { return }
        saving = true
        do {
            let next = try await api.larkMessageDisplayChange(current, key: key, value: value, expected: expected)
            guard !next.accountID.isEmpty else { throw LarkOutboxIdentity.Failure.unavailable }
            if pendingRefresh == nil { snapshot = next; problem = nil }
        } catch {
            snapshot = nil
            if LarkLoadProblem.isNotLoggedIn(error) {
                problem = LarkLoadProblem.of(error).message
            } else {
                switch (error as? APIError)?.status {
                case 409: problem = String(localized: "The settings or account changed. Reload them before making changes.")
                case 403: problem = String(localized: "This connection doesn't have permission to make changes.")
                case 404: problem = String(localized: "Update the Lark gateway first to change these settings.")
                default: problem = String(localized: "Couldn't confirm the changes were saved. Reload the settings.")
                }
            }
        }
        saving = false
        if let api = pendingRefresh { await refresh(using: api) }
    }
}

struct LarkMessageDisplaySettingsView: View {
    @Environment(AppModel.self) private var app
    var body: some View {
        Group {
            if let link = app.hub { LarkMessageDisplaySettingsContent(link: link).id(ObjectIdentifier(link)) }
            else { ContentUnavailableView("Connect to a Hub First", systemImage: Theme.Icon.connectors) }
        }
        .navigationTitle("Message Tweaks").navigationBarTitleDisplayMode(.inline)
    }
}

private struct LarkMessageDisplaySettingsContent: View {
    let link: HubLink
    private var state: LarkMessageDisplayModel { link.larkDisplay }
    var body: some View {
        List {
            Section {
                setting("Keep Recalled Messages", key: "anti_recall", value: state.policy.antiRecall)
            } footer: { Text("Shows recalled messages the Lark gateway kept, with the Recalled mark. Content it didn't keep can't be recovered.") }
            Section {
                setting("Edit History", key: "show_edit_history", value: state.policy.showEditHistory)
            } footer: { Text("Tap Edited under a message to see the earlier versions that were kept.") }
            Section {
                if state.loading || state.saving { ProgressView(state.saving ? String(localized: "Saving…") : String(localized: "Loading… [File Viewer 86b6d0]")) }
                if let problem = state.problem {
                    Text(problem).font(Theme.Font.label).foregroundStyle(.secondary)
                    Button("Reload [Lark Message Display df7392]") { Task { await state.refresh(using: link.api) } }.lineLimit(1)
                }
            } footer: { Text("Shares the current Lark account's settings with the Lark gateway's web page.") }
        }
        .themedPage()
        .task { await state.refresh(using: link.api) }
    }
    private func setting(_ title: LocalizedStringKey, key: String, value: Bool) -> some View {
        Toggle(title, isOn: Binding(get: { value }, set: { next in Task { await state.set(key, value: next, using: link.api) } }))
            .disabled(state.snapshot == nil || state.loading || state.saving)
            .accessibilityIdentifier("lark.display." + key)
    }
}

struct LarkEditHistoryView: View {
    var message: LarkMessage
    @Environment(\.dismiss) private var dismiss
    @Environment(\.larkDisplay) private var display
    var body: some View {
        NavigationStack {
            List {
                if display.showsHistory(of: message) {
                    ForEach(Array((message.editHistory ?? []).enumerated()), id: \.offset) { _, version in
                        VStack(alignment: .leading, spacing: Theme.Space.s) {
                            if let at = JSON.parseDate(version.editedAt) {
                                Text(at.formatted(date: .abbreviated, time: .shortened)).font(Theme.Font.caption).foregroundStyle(.secondary)
                            }
                            if let encryption = version.olk { LarkEncryptedContent(encryption: encryption) }
                            else if let parts = version.parts, !parts.isEmpty { LarkPartsView(parts: display.shown(parts)) }
                            else { Text(display.shown(version.text)).font(Theme.Font.body) }
                        }
                    }
                } else { Text("Edit history is hidden").foregroundStyle(.secondary) }
            }
            .themedPage().navigationTitle("Edit History").navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() }.lineLimit(1) } }
        }
    }
}
