import Foundation
#if os(iOS)
import SwiftUI
#endif

// The all-day recording consent sheet (docs/assistant/day-recap.md「Retention and privacy」): before the first all-day
// recording the iPhone says what is recorded, where the audio and the text go, who can read the text and how long each
// is kept, and nothing records or uploads until the user agrees. `RecordingDayConsent` keeps the agreement with the
// sheet's version, so a new version asks again; the watch learns it as `recordingDay` 1.

/// What the sheet says, one point each, in the contract's order. Pure, so tests check every point is there without
/// reading the view.
nonisolated enum RecordingDayConsentPoint: CaseIterable, Identifiable, Sendable {
    /// All sound around the user is recorded, other people included: ask them first.
    case everyoneIsRecorded
    /// The audio stays on the watch and the iPhone, deleted within 7 days.
    case audioStaysOnDevices
    /// The text goes to the hub, which sends it to the AI model of the knowledge base and the main chat.
    case textGoesToTheModel
    /// Agents and extensions allowed to read the knowledge base can read it.
    case readersOfKnowledge
    /// The text is kept `recording_days`, the recap as long as meeting minutes.
    case howLongItIsKept

    var id: Self { self }

    var symbol: String {
        switch self {
        case .everyoneIsRecorded: Theme.Icon.people
        case .audioStaysOnDevices: Theme.Icon.watch
        case .textGoesToTheModel: Theme.Icon.ai
        case .readersOfKnowledge: Theme.Icon.knowledge
        case .howLongItIsKept: Theme.Icon.time
        }
    }

    /// - Parameter transcriptDays: how long the hub keeps a recording's text (`recording_days`, as reconciliation last
    ///   said it).
    func text(transcriptDays: Int) -> String {
        switch self {
        case .everyoneIsRecorded:
            String(localized: "It records all the sound around you, other people included. Ask them first: some places require everyone's consent.")
        case .audioStaysOnDevices:
            String(localized: "The audio stays on your Apple Watch and iPhone and is deleted within 7 days.")
        case .textGoesToTheModel:
            String(localized: "The text goes to your Persona hub, which sends it to the AI model set up for the knowledge base and the main chat.")
        case .readersOfKnowledge:
            String(localized: "Agents and extensions allowed to read the knowledge base can read it too.")
        case .howLongItIsKept:
            String(localized: "The text is kept for \(transcriptDays) days, and the day's recap as long as meeting minutes, which is forever by default.")
        }
    }
}

#if os(iOS)
/// The consent sheet, from the Today section's switch or its Agree row: the points, then Agree (prominent) and Not Now
/// pinned under them. Agree is the one way to agree; Not Now and a swipe down leave everything as it was. Readable at
/// every text size: the points scroll, the buttons stay.
struct RecordingDayConsentView: View {
    /// How long the hub keeps a recording's text (`recording_days`).
    var transcriptDays: Int
    /// The user's answer: true for Agree. The presenter stores it and closes the sheet.
    var answer: (Bool) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    heading
                    VStack(alignment: .leading, spacing: Theme.Space.l) {
                        ForEach(RecordingDayConsentPoint.allCases) { point in
                            row(point)
                        }
                    }
                    Text("You can pause at any time, erase the last 10 minutes, or delete a whole day.")
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(.horizontal, Theme.Space.page)
                .padding(.vertical, Theme.Space.l)
                .frame(maxWidth: Theme.Size.readable, alignment: .leading)
                .frame(maxWidth: .infinity)
            }
            .themedGroupedPage()
            .safeAreaInset(edge: .bottom, spacing: 0) { footer }
            .navigationTitle("All-Day Recording")
            .navigationBarTitleDisplayMode(.inline)
        }
        .presentationDetents([.large])
        .presentationDragIndicator(.visible)
    }

    private var heading: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.allDay).font(Theme.Font.display).foregroundStyle(Theme.accent)
                .accessibilityHidden(true)
            Text("Before Your Apple Watch Records Your Day").font(Theme.Font.title)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("allday-consent-title")
            Text("It records the whole day, and your hub writes a recap of it every evening.")
                .font(Theme.Font.secondary).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// One point: its mark beside its words (one spoken element: the mark says nothing the words don't).
    private func row(_ point: RecordingDayConsentPoint) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
            ThemedIcon(point.symbol).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.icon)
                .accessibilityHidden(true)
            Text(point.text(transcriptDays: transcriptDays)).font(Theme.Font.body)
                .fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("allday-consent-point")
    }

    private var footer: some View {
        VStack(spacing: Theme.Space.s) {
            Button { reply(true) } label: {
                Text("Agree").font(Theme.Font.heading).lineLimit(1)
                    .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.tapTarget)
            }
            .themedButton(prominent: true)
            .accessibilityIdentifier("allday-consent-agree")
            Button { reply(false) } label: {
                Text("Not Now [Day Consent]").font(Theme.Font.body).lineLimit(1)
                    .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.tapTarget)
            }
            .buttonStyle(.borderless)
            .accessibilityIdentifier("allday-consent-not-now")
        }
        .padding(.horizontal, Theme.Space.xl)
        .padding(.vertical, Theme.Space.m)
        .frame(maxWidth: Theme.Size.readable)
        .frame(maxWidth: .infinity)
        .background(Theme.groupedPage.ignoresSafeArea(edges: .bottom))
    }

    private func reply(_ agreed: Bool) {
        answer(agreed)
        dismiss()
    }
}
#endif
