#if os(iOS)
import SwiftUI

/// The question the live screen asks once on mobile data (#6396, `ScreenMobileData`): a small card under the bars that
/// covers none of the picture. Its title says whether that's cellular or a personal hotspot (both are mobile data). The picture already runs on Data Saver; Clearer or Data Saver takes effect at once and is
/// kept for every computer, and the card goes. Closed unanswered, the picture stays on Data Saver and the question comes
/// back once next time.
struct ScreenMobileDataCard: View {
    var hotspot = false
    let choose: (ScreenMobileData) -> Void
    let close: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
                ThemedIcon(Theme.Icon.mobileData).font(Theme.Font.caption).foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(hotspot ? String(localized: "On a Personal Hotspot") : String(localized: "On Mobile Data"))
                        .font(Theme.Font.captionStrong)
                    Text("The screen is saving data. Make it clearer?").font(Theme.Font.caption).foregroundStyle(.secondary)
                }
                .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: Theme.Space.s)
                Button(action: close) { ThemedIcon(Theme.Icon.close).font(Theme.Font.caption) }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .accessibilityLabel(Text("Close"))
                    .accessibilityIdentifier("screen-mobile-data-close")
            }
            // Side by side where they fit, one over the other where they don't (large text): never wrapped.
            ViewThatFits(in: .horizontal) {
                HStack(spacing: Theme.Space.s) { buttons }
                VStack(spacing: Theme.Space.s) { buttons }
            }
        }
        .padding(Theme.Space.m)
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
        // A wide screen (iPad, a phone on its side) keeps it a card, not a band across the picture.
        .frame(maxWidth: Theme.Size.readable)
        .padding(.horizontal, Theme.Space.page)
        .padding(.bottom, Theme.Space.xs)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("screen-mobile-data")
    }

    @ViewBuilder private var buttons: some View {
        option(.clearer, prominent: true)
        option(.saver, prominent: false)
    }

    private func option(_ m: ScreenMobileData, prominent: Bool) -> some View {
        Button { choose(m) } label: {
            VStack(spacing: Theme.Space.xxs) {
                Text(m.title).font(Theme.Font.captionStrong)
                Text(m.usage).font(Theme.Font.label)
            }
            .lineLimit(1)
            .minimumScaleFactor(0.8)
            .frame(maxWidth: .infinity)
        }
        .themedButton(prominent: prominent)
        .accessibilityIdentifier("screen-mobile-data-\(m.rawValue)")
    }
}
#endif
