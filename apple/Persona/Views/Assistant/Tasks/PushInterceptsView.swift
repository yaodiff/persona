import SwiftUI

@MainActor @Observable
final class PushInterceptsModel {
    @ObservationIgnored private let readMode: @Sendable () async throws -> LnModeResponse
    @ObservationIgnored private let readIntercepts: @Sendable () async throws -> PushInterceptsResponse
    @ObservationIgnored private var revision = 0

    private(set) var enabled = false
    private(set) var groups = PushInterceptGroups()
    private(set) var loading = false
    private(set) var failed = false

    init(api: APIClient) {
        readMode = { try await api.pushLnMode() }
        readIntercepts = { try await api.pushIntercepts() }
    }

    init(mode: @escaping @Sendable () async throws -> LnModeResponse,
         intercepts: @escaping @Sendable () async throws -> PushInterceptsResponse) {
        readMode = mode
        readIntercepts = intercepts
    }

    func run() async {
        while !Task.isCancelled {
            await refresh()
            try? await Task.sleep(for: .seconds(30))
        }
    }

    func refresh() async {
        revision += 1
        let ticket = revision
        loading = true
        do {
            do {
                let mode = try await readMode()
                try Task.checkCancellation()
                guard ticket == revision else { return }
                enabled = mode.enabled
                guard mode.enabled else {
                    groups = PushInterceptGroups()
                    failed = false
                    loading = false
                    return
                }
            } catch is CancellationError {
                return
            } catch {
                guard ticket == revision, !Task.isCancelled else { return }
                failed = true
                if (error as? APIError)?.status == 404 {
                    enabled = false
                    groups = PushInterceptGroups()
                }
                loading = false
                return
            }
            do {
                let response = try await readIntercepts()
                try Task.checkCancellation()
                guard ticket == revision else { return }
                groups = PushInterceptsLogic.groups(response, now: .now, calendar: .current)
                failed = false
            } catch is CancellationError {
                return
            } catch {
                guard ticket == revision, !Task.isCancelled else { return }
                failed = true
            }
        } catch {
            guard ticket == revision, !Task.isCancelled else { return }
            failed = true
        }
        if ticket == revision { loading = false }
    }
}

struct PushInterceptsEntry: View {
    @State private var model: PushInterceptsModel
    var otherTrayOpen: Bool
    var onOpen: () -> Void
    @State private var open = false
    @State private var p2Open = false

    init(api: APIClient, otherTrayOpen: Bool, onOpen: @escaping () -> Void) {
        _model = State(initialValue: PushInterceptsModel(api: api))
        self.otherTrayOpen = otherTrayOpen
        self.onOpen = onOpen
    }

    init(model: PushInterceptsModel, otherTrayOpen: Bool = false, onOpen: @escaping () -> Void = {}) {
        _model = State(initialValue: model)
        self.otherTrayOpen = otherTrayOpen
        self.onOpen = onOpen
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if model.enabled {
            if open {
                PushInterceptsTray(model: model, p2Open: $p2Open) {
                    withAnimation(Theme.Motion.state) { open = false }
                }
                .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            Button {
                Haptics.tap()
                onOpen()
                withAnimation(Theme.Motion.state) { open.toggle() }
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.notification).accessibilityHidden(true)
                    Text("Held Pushes [Push Intercepts]")
                    if let count = CountBadge.text(model.groups.total) { Text(count).monospacedDigit() }
                }
                .dockCapsule(open ? .selected : .plain)
                .background(Theme.pageBackground, in: .capsule)
                .fixedSize()
            }
            .buttonStyle(.plain)
            .accessibilityLabel(String(localized: "Held pushes, \(model.groups.total) today [Push Intercepts]"))
            .accessibilityAddTraits(open ? .isSelected : [])
            .accessibilityIdentifier("action-bar.push-intercepts")
            }
        }
        .onChange(of: otherTrayOpen) { _, value in if value { open = false } }
        .onChange(of: model.enabled) { _, value in if !value { open = false } }
        .onChange(of: open) { _, value in if !value { p2Open = false } }
        .tabActiveTask { await model.run() }
        #if DEBUG
        .onAppear {
            if UserDefaults.standard.bool(forKey: "openPushIntercepts") {
                open = true
                UserDefaults.standard.removeObject(forKey: "openPushIntercepts")
            }
        }
        #endif
    }
}

private struct PushInterceptsTray: View {
    let model: PushInterceptsModel
    @Binding var p2Open: Bool
    let close: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                Text("Held Pushes Today [Push Intercepts]").font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                Button {
                    Task { await model.refresh() }
                } label: {
                    Text("Retry").dockCapsule().fixedSize().tapTargetHeight().contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(model.loading)
                .accessibilityIdentifier("action-tray.push-intercepts.retry")
                Button(action: close) {
                    ThemedIcon(Theme.Icon.close).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                        .tapTargetFrame().contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Close")
                .accessibilityIdentifier("action-tray.push-intercepts.close")
            }
            .padding(.horizontal, Theme.Space.m)
            if model.failed {
                Text("Couldn’t load held pushes. Check the connection and retry. [Push Intercepts]")
                    .font(Theme.Font.caption).foregroundStyle(Theme.danger).fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, Theme.Space.m)
                    .accessibilityIdentifier("action-tray.push-intercepts.error")
            }
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if model.loading && model.groups.total == 0 {
                        Text("Loading held pushes… [Push Intercepts]")
                            .font(Theme.Font.caption).foregroundStyle(.secondary)
                            .accessibilityIdentifier("action-tray.push-intercepts.loading")
                    } else if model.groups.total == 0 && !model.loading {
                        Text("No pushes were held today. [Push Intercepts]")
                            .font(Theme.Font.caption).foregroundStyle(.secondary)
                            .accessibilityIdentifier("action-tray.push-intercepts.empty")
                    } else {
                        if !model.groups.p1.isEmpty {
                            section(String(localized: "P1 · Needs Your Review [Push Intercepts]"), rows: model.groups.p1)
                        }
                        if !model.groups.p2.isEmpty {
                            FoldButton(title: String(localized: "P2 · Quiet [Push Intercepts]"), folded: !p2Open,
                                       detail: "\(model.groups.p2.count)", action: { p2Open.toggle() }) {
                                HStack(spacing: Theme.Space.sm) {
                                    Text("P2 · Quiet [Push Intercepts]").font(Theme.Font.sectionHeader).foregroundStyle(.secondary)
                                    CountBadge(count: model.groups.p2.count, look: .neutral)
                                }
                            }
                            if p2Open { section(String(localized: "P2 · Quiet [Push Intercepts]"), rows: model.groups.p2, showTitle: false) }
                        }
                    }
                }
                .padding(.horizontal, Theme.Space.m)
                .padding(.bottom, Theme.Space.xs)
            }
            .frame(maxHeight: Theme.Size.box380)
        }
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("action-tray.push-intercepts")
    }

    @ViewBuilder private func section(_ title: String, rows: [PushInterceptItem], showTitle: Bool = true) -> some View {
        if showTitle {
            HStack(spacing: Theme.Space.sm) {
                Text(title).font(Theme.Font.sectionHeader).foregroundStyle(.secondary)
                CountBadge(count: rows.count, look: .neutral)
            }
            .accessibilityAddTraits(.isHeader)
        }
        ForEach(rows) { item in
            PushInterceptRow(item: item, onOpen: close)
        }
    }
}

private struct PushInterceptRow: View {
    let item: PushInterceptItem
    var onOpen: () -> Void
    @Environment(\.openLink) private var openLink
    private var destination: PushInterceptDestination? {
        PushInterceptsLogic.destination(item.record, knownTodoIDs: Set(TasksStore.shared.todos.keys))
    }

    var body: some View {
        if let destination {
            Button(action: {
                onOpen()
                open(destination)
            }) { content }
                .buttonStyle(.plain)
                .accessibilityIdentifier("push-intercepts.row.\(item.id)")
        } else {
            content.accessibilityIdentifier("push-intercepts.row.\(item.id)")
        }
    }

    private var content: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(item.summaries.first.flatMap { $0.isEmpty ? nil : $0 } ?? String(localized: "No summary [Push Intercepts]"))
                    .font(Theme.Font.secondaryMedium).foregroundStyle(.primary).lineLimit(3)
                ForEach(Array(item.summaries.dropFirst().enumerated()), id: \.offset) { _, summary in
                    Text(summary).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                }
                Text([reason, item.record.manager?.trimmingCharacters(in: .whitespacesAndNewlines)]
                    .compactMap { $0?.isEmpty == false ? $0 : nil }
                    .joined(separator: " · "))
                    .font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            Spacer(minLength: Theme.Space.s)
            Text(AssistantScheduleText.hm(item.date)).font(Theme.Font.caption).foregroundStyle(.secondary).monospacedDigit()
            if destination != nil { ThemedIcon(Theme.Icon.open).font(Theme.Font.label).foregroundStyle(.tertiary) }
        }
        .padding(Theme.Space.m)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .contentShape(.rect)
    }

    private var reason: String {
        switch item.record.reason {
        case "ln_dedup_12h": String(localized: "A related push already alerted in the last 12 hours [Push Intercepts]")
        case "ln_p2_silent": String(localized: "Kept silent [Push Intercepts]")
        default: String(localized: "Held for the briefing [Push Intercepts]")
        }
    }

    private func open(_ destination: PushInterceptDestination) {
        switch destination {
        case .todo(let id):
            TasksStore.shared.detailTask = id
        case .session(let group, let machine, let session):
            AppModel.shared.open(SessionKey(group: group, machineID: machine, sessionID: session))
        case .url(let url):
            openLink?(url)
        }
    }
}
