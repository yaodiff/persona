#if DEBUG && os(iOS)
import BeautifulUI
import SwiftUI

/// `-showButtonContrast YES` (#6157): a secondary `.themedButton()` on every kind of fill one sits on in the app, for
/// screenshots that measure each label against the button's own fill (`ButtonContrastUITests`). The places:
/// - The first screen (and a row of every kind of control, `ButtonKindsRow`, #7214):
///   - `page`: the page itself.
///   - `grouped`: the grouped page; bottom bars use it too.
///   - `card`: `.card()`, the most common card.
///   - `themeCard`: a `Theme.card` fill.
///   - `surface`: a floating panel (`.surface`: interaction panels, the tray).
/// - The second screen, after `contrast.next`:
///   - `list`: a settings list's row (`SettingsForm`).
///   - `well`: `Theme.Fill.well`.
///   - `wash`: the accent at 12 %, as receipts and suggestions use.
///   - `bar`: the `.bar` material.
///   - `black`: black and dark whatever the look, as the screen viewer and the image editor are.
/// - In a sheet, after `contrast.openSheet`: the sheet itself, a panel, `.card()`, `Theme.card` and a list row in it
///   (Settings is a sheet), under a title as real sheets have: at half height the sheet's rim shows what is behind it.
///   It starts at half height and goes to full height with `contrast.expand`.
///
/// Each place has three buttons, the three colors call sites give a secondary button: the accent, `.tint(Theme.danger)`
/// and the destructive role. They are drawn over the app in the saved style, color and look (`-ui.style`, `-ui.color`,
/// `-ui.scheme`), with RootView's tint and type (`rootLook`), and each one's identifier is
/// `contrast.<place>.<accent|danger|destructive>`.
struct ButtonContrastOverlay: View {
    @State private var shown = UserDefaults.standard.bool(forKey: "showButtonContrast")
    @State private var second = false
    @State private var sheet = false

    var body: some View {
        if shown {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                if second {
                    ButtonContrastList(place: "list")
                    ButtonContrastRow(place: "well").background(Theme.Fill.well, in: Self.box)
                    ButtonContrastRow(place: "wash").background(Theme.accent.opacity(0.12), in: Self.box)
                    ButtonContrastRow(place: "bar").background(.bar, in: Self.box)
                    ButtonContrastRow(place: "black").background(Color.black, in: Self.box).environment(\.colorScheme, .dark)
                } else {
                    ButtonContrastRow(place: "page")
                    ButtonContrastRow(place: "grouped").background(Theme.groupedPage, in: Self.box)
                    ButtonContrastRow(place: "card").card()
                    ButtonContrastRow(place: "themeCard").background(Theme.card, in: Self.box)
                    ButtonContrastRow(place: "surface").surface(in: Self.box)
                    ButtonKindsRow()
                }
                HStack(spacing: Theme.Space.l) {
                    Button { second.toggle() } label: { Text(verbatim: "next") }
                        .accessibilityIdentifier("contrast.next")
                    Button { sheet = true } label: { Text(verbatim: "sheet") }
                        .accessibilityIdentifier("contrast.openSheet")
                }
                Spacer(minLength: .zero)
            }
            .padding(.horizontal, Theme.Space.page)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .background(StyleSpec.current.usesGlass ? Color(.systemBackground) : Theme.pageBackground)
            .sheet(isPresented: $sheet) { ButtonContrastSheet().rootLook() }
            .rootLook()
        }
    }

    static let box = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
}

/// The sheet's places, at half height first: the ones nearest the top are on the screen at both heights.
private struct ButtonContrastSheet: View {
    @State private var detent: PresentationDetent = .medium

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text(verbatim: "sheet").font(Theme.Font.heading).scaledFrame(minHeight: Theme.Size.tapTarget)
            ButtonContrastRow(place: "sheet")
            ButtonContrastRow(place: "sheetSurface").surface(in: ButtonContrastOverlay.box)
            ButtonContrastRow(place: "sheetCard").card()
            ButtonContrastRow(place: "sheetThemeCard").background(Theme.card, in: ButtonContrastOverlay.box)
            ButtonContrastList(place: "sheetList")
            Button { detent = detent == .large ? .medium : .large } label: { Text(verbatim: "expand") }
                .accessibilityIdentifier("contrast.expand")
        }
        .padding(Theme.Space.page)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .presentationDetents([.medium, .large], selection: $detent)
        .interactiveDismissDisabled()
    }
}

private extension View {
    /// What RootView gives every screen and this overlay, drawn over it, would miss: the accent as the tint (a secondary
    /// button's label takes it), the style's font design and BeautifulUI's theme.
    func rootLook() -> some View {
        tint(Theme.accent).fontDesign(AppStyle.current.fontDesign).buiTheme(.persona())
    }
}

/// A settings list (`SettingsForm`) with one row.
private struct ButtonContrastList: View {
    var place: String

    var body: some View {
        SettingsForm {
            Section { ButtonContrastRow(place: place).themedRow() }
        }
        .themedPage()
        .scrollDisabled(true)
        .scaledFrame(height: Theme.Size.tapTarget * 3)
    }
}

/// One place: a secondary button in the accent, one tinted `Theme.danger` and one with the destructive role (which
/// `.themedButton()` draws in `Theme.danger`, #7042), with words of the length real ones have.
/// Every kind of control a call site styles with `.themedButton()`, on the page (#7214): a Button, a Menu, a Link and a
/// ShareLink, each to draw the style's background (a Menu drew none in the glass styles and Lark after #7129).
/// Identifiers `contrast.kinds.<button|menu|link|share>`, and `contrast.kinds` for the row (the page beside them).
private struct ButtonKindsRow: View {
    var body: some View {
        HStack(spacing: Theme.Space.s) {
            Button {} label: { Text(verbatim: "Button").lineLimit(1) }
                .themedButton()
                .accessibilityIdentifier("contrast.kinds.button")
            Menu {
                Button {} label: { Text(verbatim: "One") }
            } label: {
                Text(verbatim: "Menu").lineLimit(1)
            }
            .themedButton()
            .accessibilityIdentifier("contrast.kinds.menu")
            Link(destination: URL(string: "https://example.com")!) { Text(verbatim: "Link").lineLimit(1) }
                .themedButton()
                .accessibilityIdentifier("contrast.kinds.link")
            ShareLink(item: "Persona") { Text(verbatim: "Share").lineLimit(1) }
                .themedButton()
                .accessibilityIdentifier("contrast.kinds.share")
        }
        .padding(Theme.Space.s)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("contrast.kinds")
    }
}

private struct ButtonContrastRow: View {
    var place: String

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            Button {} label: { Text("Open Settings").lineLimit(1) }
                .themedButton()
                .accessibilityIdentifier("contrast.\(place).accent")
            Button {} label: { Text("Delete").lineLimit(1) }
                .themedButton()
                .tint(Theme.danger)
                .accessibilityIdentifier("contrast.\(place).danger")
            Button(role: .destructive) {} label: { Text("Delete").lineLimit(1) }
                .themedButton()
                .accessibilityIdentifier("contrast.\(place).destructive")
        }
        .padding(Theme.Space.s)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
#endif
