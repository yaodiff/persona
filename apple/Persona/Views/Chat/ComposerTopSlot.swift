import PersonaKit
import SwiftUI

/// Composer-bound accessories fused into the top edge of `ShellComposerBar` for Assistant conversations:
/// queued messages (aligned with Web #5327 pending queue) and the task-routing banner, each separated from the input
/// field below by a hairline divider. Kept in its own child views so `Composer` avoids extra observation reads (P5).
struct ComposerTopSlot: View {
    let key: SessionKey

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ComposerQueuedTop(key: key)
            ComposerRouteTop(conversation: key.sessionID)
        }
    }
}

/// Queued messages docked inside the top edge of the Assistant's `ShellComposerBar` card, separated from the input
/// field by a hairline divider. Collapsed to one concise row (count + first-message preview + one-tap Send Now);
/// expanding discloses the full interactive queue list (edit, reorder, copy, delete, folded relayed group, sending rows)
/// without leaving the composer card.
struct ComposerQueuedTop: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    @State private var busy = false
    @State private var expanded = false
    @State private var relayedExpanded = false
    @State private var rowHeight: CGFloat = 0
    @Environment(\.dynamicTypeSize) private var typeSize
    private static let visibleRows: CGFloat = 3

    var body: some View {
        let session = model.session(key)
        let chat = model.chat(key)
        let queued = session?.queued ?? []
        let own = session?.ownQueuedCount ?? queued.count
        let sending = QueuePlacement.split(chat.arriving + chat.pending, working: session?.status.isActive == true,
                                           queued: queued, stripShown: true).strip
        if !queued.isEmpty || !sending.isEmpty {
            let relays = queued.dropFirst(own).map { QueuedRelay($0) }
            let steers = session?.steersQueue == true || (session?.assistant == true && session?.canSend == true)
            let photoMarks = chat.queuePhotoMarks
            let persona = model.assistantShownName
            let total = queued.count + sending.count
            let relayedCount = max(queued.count - own, 0)
            let drawnCount = own + sending.count + (relayedCount > 0 ? (relayedExpanded ? relayedCount : 1) : 0)
            let preview = Self.collapsedPreview(queued: queued, own: own, sending: sending, photoMarks: photoMarks)

            VStack(alignment: .leading, spacing: 0) {
                header(total: total, preview: preview, own: own, steers: steers, persona: persona)
                if expanded {
                    ScrollView {
                        rows(queued: queued, own: own, relays: relays, sending: sending, photoMarks: photoMarks,
                             steers: steers, relayedCount: relayedCount, drawnCount: drawnCount)
                    }
                    .scrollBounceBehavior(.basedOnSize)
                    .fixedSize(horizontal: false, vertical: rowHeight == 0)
                    .frame(height: rowHeight > 0 ? rowHeight * min(CGFloat(drawnCount), Self.visibleRows) : nil)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.bottom, Theme.Space.s)
                    .transition(.opacity)
                }
                Rectangle()
                    .fill(Theme.Shell.composerStroke)
                    .frame(height: Theme.Size.hairline)
            }
            .animation(Theme.Motion.state, value: expanded)
            .transition(.opacity)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("queue-strip")
        }
    }

    @ViewBuilder
    private func header(total: Int, preview: String?, own: Int, steers: Bool, persona: String) -> some View {
        Group {
            if typeSize >= Theme.interfaceLargestTypeSize {
                ViewThatFits(in: .horizontal) {
                    oneLineHeader(total: total, preview: preview, own: own, steers: steers, persona: persona)
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        toggleButton(total: total, preview: nil, multiline: true)
                        sendNowButton(own: own, steers: steers, persona: persona)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
            } else {
                oneLineHeader(total: total, preview: preview, own: own, steers: steers, persona: persona)
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.sm)
    }

    private func oneLineHeader(total: Int, preview: String?, own: Int, steers: Bool, persona: String) -> some View {
        HStack(spacing: Theme.Space.s) {
            toggleButton(total: total, preview: preview, multiline: false)
            sendNowButton(own: own, steers: steers, persona: persona)
        }
    }

    private func toggleButton(total: Int, preview: String?, multiline: Bool) -> some View {
        Button {
            Haptics.tap()
            withAnimation(Theme.Motion.state) { expanded.toggle() }
        } label: {
            HStack(spacing: Theme.Space.xs) {
                ThemedIcon(Theme.Icon.time)
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(.secondary)
                    .accessibilityHidden(true)
                Text("\(total) queued [Queue Strip]")
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
                    .lineLimit(multiline ? nil : 1)
                    .fixedSize(horizontal: !multiline, vertical: multiline)
                if !expanded, let preview, !preview.isEmpty {
                    Text(verbatim: "·")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.tertiary)
                        .accessibilityHidden(true)
                    Text(preview)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                }
                ThemedIcon(Theme.Icon.expand)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(expanded ? 180 : 0))
                    .accessibilityHidden(true)
                Spacer(minLength: 0)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text("\(total) queued [Queue Strip]"))
        .accessibilityValue(expanded ? String(localized: "Expanded") : String(localized: "Collapsed"))
        .accessibilityIdentifier("queue-strip.toggle")
    }

    @ViewBuilder
    private func sendNowButton(own: Int, steers: Bool, persona: String) -> some View {
        if busy {
            ProgressView().controlSize(.mini)
        } else if steers, own > 0 {
            Button(action: sendNow) {
                Label("Send Now [Queue Strip]", systemImage: Theme.Icon.sendNow)
                    .font(Theme.Font.captionStrong)
                    .lineLimit(1)
                    .fixedSize()
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.accent)
            .accessibilityHint(Text("\(persona) gets them right away instead of at its next step. What it is doing now stops, or goes on in the background when it can, and it starts again with your messages. [Queue Strip]"))
            .accessibilityIdentifier("queue-send-now")
        }
    }

    private func rows(queued: [String], own: Int, relays: [QueuedRelay?], sending: [PendingMessage],
                      photoMarks: [Int], steers: Bool, relayedCount: Int, drawnCount: Int) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            ForEach(0..<own, id: \.self) { i in
                ownRow(i, queued: queued, own: own, photoMarks: photoMarks, steers: steers)
            }
            if relayedCount > 0 {
                Button {
                    withAnimation(Theme.Motion.state) { relayedExpanded.toggle() }
                } label: {
                    HStack(spacing: Theme.Space.s) {
                        ThemedIcon(Theme.Icon.sendNow).font(Theme.Font.label).foregroundStyle(.secondary)
                        Text("\(relayedCount) messages from other conversations are waiting [Queue Strip]")
                            .font(Theme.Font.secondary)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                        Spacer(minLength: 0)
                        ThemedIcon(Theme.Icon.expand)
                            .font(Theme.Font.captionStrong)
                            .foregroundStyle(.secondary)
                            .rotationEffect(.degrees(relayedExpanded ? 180 : 0))
                    }
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.s)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("queue-relayed-summary")
                .accessibilityLabel(Text("\(relayedCount) messages from other conversations are waiting [Queue Strip]"))
                if relayedExpanded {
                    ForEach(own..<queued.count, id: \.self) { i in
                        relayedRow(i, queued: queued, own: own, relays: relays)
                    }
                }
            }
            ForEach(Array(sending.enumerated()), id: \.element.id) { j, msg in
                sendingRow(msg, number: queued.count + j + 1)
            }
        }
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
            if drawnCount > 0 { rowHeight = h / CGFloat(drawnCount) }
        }
    }

    private func ownRow(_ i: Int, queued: [String], own: Int, photoMarks: [Int], steers: Bool) -> some View {
        let q = queued[i]
        let photos = photoMarks.indices.contains(i) ? photoMarks[i] : 0
        let draft = QueuedDraft(q, photoCount: photos)
        return Menu {
            Button { act(i, delete: false) } label: { Label("Take Back to Edit", systemImage: Theme.Icon.edit) }
            if steers, own > 1 {
                Button { move(i, by: -1) } label: { Label("Move Up [Queue Strip]", systemImage: Theme.Icon.queueUp) }
                    .disabled(i == 0)
                    .accessibilityIdentifier("queue-move-up")
                Button { move(i, by: 1) } label: { Label("Move Down [Queue Strip]", systemImage: Theme.Icon.queueDown) }
                    .disabled(i == own - 1)
                    .accessibilityIdentifier("queue-move-down")
            }
            Button { UIPasteboard.general.string = draft.text.isEmpty ? q : draft.text } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
            Button(role: .destructive) { act(i, delete: true) } label: { Label("Delete", systemImage: Theme.Icon.delete) }
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Text("\(i + 1)")
                    .font(Theme.Font.labelHeavy.monospacedDigit())
                    .foregroundStyle(Theme.accent)
                    .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                    .background(Theme.accent.opacity(0.14), in: .circle)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    QueuedWords(draft: draft)
                    if photos > 0 {
                        HStack(spacing: Theme.Space.xs) {
                            ThemedIcon(Theme.Icon.photo)
                            Text("\(photos) images")
                        }
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 0)
                ThemedIcon(Theme.Icon.more).font(Theme.Font.label).foregroundStyle(.secondary).padding(.top, Theme.Space.xs)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        }
        .disabled(busy)
        .accessibilityLabel(Text("Item \(i + 1): \(QueuedWords.spoken(draft))"))
        .accessibilityIdentifier("queue-item")
        .accessibilityHint(Text("Edit, copy or delete"))
        .accessibilityValue(photos > 0 ? Text("\(photos) images") : Text(""))
    }

    private func relayedRow(_ i: Int, queued: [String], own: Int, relays: [QueuedRelay?]) -> some View {
        let q = queued[i]
        let relay = relays[i - own]
        let draft = QueuedDraft(relay?.text ?? q)
        let source = relay.map { String(localized: "From \($0.source)") }
        let body = QueuedWords.spoken(draft)
        let words = [source, body].compactMap { $0 }.joined(separator: " · ")
        let noWake = relay?.noWake == true
        return Menu {
            Button {
                let copy = QueuedDraft(q).text
                UIPasteboard.general.string = copy.isEmpty ? q : copy
            } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Text("\(i + 1)")
                    .font(Theme.Font.labelHeavy.monospacedDigit())
                    .foregroundStyle(Theme.accent)
                    .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                    .background(Theme.accent.opacity(0.14), in: .circle)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    if let source { Text(source).font(Theme.Font.labelStrong).lineLimit(1) }
                    QueuedWords(draft: draft)
                    (noWake ? Text("Arrives with the next message [Queue Strip]") : Text("Arrives when this turn ends [Queue Strip]"))
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
                ThemedIcon(Theme.Icon.more).font(Theme.Font.label).foregroundStyle(.secondary).padding(.top, Theme.Space.xs)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        }
        .disabled(busy)
        .accessibilityLabel(noWake
            ? Text("Item \(i + 1): \(words), arrives with the next message [Queue Strip]")
            : Text("Item \(i + 1): \(words), arrives when this turn ends [Queue Strip]"))
        .accessibilityIdentifier("queue-item")
        .accessibilityHint(Text("Copy [Readiness Views d37380]"))
    }

    private func sendingRow(_ msg: PendingMessage, number: Int) -> some View {
        let draft = QueuedDraft(msg.text, photoCount: msg.images.count)
        return Menu {
            switch msg.state {
            case .failed:
                Button { model.retry(key, msg) } label: { Label("Resend", systemImage: Theme.Icon.retry) }
            case .waiting:
                Button { model.retry(key, msg) } label: { Label("Retry Now [Message Views f40f4f]", systemImage: Theme.Icon.retry) }
            case .sending, .sent:
                EmptyView()
            }
            Button { UIPasteboard.general.string = draft.text.isEmpty ? msg.text : draft.text } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
            if msg.onlyHere {
                if msg.state == .waiting {
                    Button(role: .destructive) { model.discard(key, msg) } label: { Label("Don't Send", systemImage: Theme.Icon.delete) }
                } else {
                    Button(role: .destructive) { model.discard(key, msg) } label: { Label("Delete", systemImage: Theme.Icon.delete) }
                }
            }
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Text("\(number)")
                    .font(Theme.Font.labelHeavy.monospacedDigit())
                    .foregroundStyle(Theme.accent)
                    .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                    .background(Theme.accent.opacity(0.14), in: .circle)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    QueuedWords(draft: draft)
                    if !msg.images.isEmpty {
                        HStack(spacing: Theme.Space.xs) {
                            ThemedIcon(Theme.Icon.photo)
                            Text("\(msg.images.count) images")
                        }
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                    switch msg.state {
                    case .sending:
                        Text("Sending… [Queue Strip]").font(Theme.Font.caption).foregroundStyle(.secondary)
                    case .waiting:
                        Text("Sends when connected").font(Theme.Font.caption).foregroundStyle(.secondary)
                    case .failed:
                        Text("Not sent. Tap to resend [Queue Strip]").font(Theme.Font.caption).foregroundStyle(Theme.danger)
                    case .sent:
                        EmptyView()
                    }
                }
                Spacer(minLength: 0)
                ThemedIcon(msg.state == .failed ? Theme.Icon.errorFilled : Theme.Icon.more).font(Theme.Font.label)
                    .foregroundStyle(msg.state == .failed ? Theme.danger : .secondary).padding(.top, Theme.Space.xs)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .opacity(msg.state == .failed || msg.state == .waiting ? 0.8 : 1)
        }
        .accessibilityLabel(Text("Item \(number): \(QueuedWords.spoken(draft))"))
        .accessibilityIdentifier("queue-sending-item")
    }

    private func act(_ i: Int, delete: Bool) {
        busy = true
        Task {
            await model.editQueued(key, index: i, delete: delete)
            busy = false
        }
    }

    private func move(_ i: Int, by step: Int) {
        busy = true
        Task {
            await model.moveQueued(key, index: i, by: step)
            busy = false
        }
    }

    private func sendNow() {
        busy = true
        Task {
            await model.sendQueuedNow(key)
            busy = false
        }
    }

    /// One-line summary of the first own or sending queued message for the collapsed top edge.
    private static func collapsedPreview(queued: [String], own: Int, sending: [PendingMessage], photoMarks: [Int]) -> String? {
        if own > 0, let first = queued.first {
            let photos = photoMarks.first ?? 0
            let spoken = QueuedWords.spoken(QueuedDraft(first, photoCount: photos))
            return spoken.isEmpty ? nil : spoken
        }
        if let first = sending.first {
            let spoken = QueuedWords.spoken(QueuedDraft(first.text, photoCount: first.images.count))
            return spoken.isEmpty ? nil : spoken
        }
        return nil
    }
}

/// Task-routing banner fused into the top edge of `ShellComposerBar` while the next message is routed to a specific task.
struct ComposerRouteTop: View {
    var conversation: String
    private var store: TasksStore { .shared }

    var body: some View {
        if let id = store.routes[conversation], let todo = store.todos[id] {
            VStack(spacing: 0) {
                HStack(spacing: Theme.Space.s) {
                    Label("Message “\(todo.title)”", systemImage: Theme.Icon.routeToTask)
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(Theme.accent)
                        .lineLimit(1)
                        .truncationMode(.tail)
                    Spacer(minLength: Theme.Space.xs)
                    Button {
                        withAnimation(Theme.Motion.state) { store.routes[conversation] = nil }
                    } label: {
                        ThemedIcon(Theme.Icon.close)
                            .font(Theme.Font.labelStrong)
                            .foregroundStyle(.secondary)
                            .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                            .contentShape(.circle)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Hand to Persona")
                }
                .padding(.horizontal, Theme.Space.l)
                .padding(.vertical, Theme.Space.sm)
                Rectangle()
                    .fill(Theme.Shell.composerStroke)
                    .frame(height: Theme.Size.hairline)
            }
            .transition(.opacity)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("composer-route-top")
        }
    }
}
