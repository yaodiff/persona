import SwiftUI
import PersonaWire

// UI version 6's tiles (docs/extensions/extensions.md "UI version 6", #827): a `grid` (columns that fit the width, two
// on a phone, at most `max_columns`) and a `carousel` (one line that scrolls sideways, snapping to a tile, the next one's
// edge showing). A tile has its picture across the top or its icon in its color, the title, a state line, a value and a
// spark, a badge and a switch in its corner; `active` lays its color under it and around it (the text stays in the
// ink). Tapping it does its action; its extra actions are in its menu (`extensionRowActions`).

/// An extension's tile.
struct ExtensionTileView: View {
    var tile: ExtensionTile
    var model: (any ExtensionViewControlling)?
    var minHeight: CGFloat = Theme.Size.tileHeight
    @Binding var link: LinkTarget?
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(AppModel.self) private var app
    @Environment(\.colorScheme) private var scheme
    /// Its switch asking first (`confirm`): the value it asks about, kept apart from the alert's flag.
    @State private var asking: String?
    @State private var confirming = false

    private var tint: Color { ExtensionUIStyle.color(tile.color) ?? Theme.accent }
    private var control: ExtensionRowControl? {
        tile.control.flatMap { $0.kind == ExtensionRowControlKind.switch && $0.drawable ? $0 : nil }
    }
    private var switchedOn: Bool { control.map { (model?.controlValues[$0.name] ?? $0.wire) == "true" } ?? false }
    /// On: `active`, or its switch on.
    private var on: Bool { tile.active == true || switchedOn }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
        content
            .frame(maxWidth: .infinity, alignment: .topLeading)
            .scaledFrame(minHeight: minHeight, alignment: .topLeading)
            .background {
                shape.fill(on ? AnyShapeStyle(tint.opacity(scheme == .dark ? 0.22 : 0.14)) : AnyShapeStyle(Theme.Fill.chip))
            }
            .overlay { shape.strokeBorder(on ? tint : Color.clear, lineWidth: Theme.Size.lineMedium) }
            .clipShape(shape)
            .contentShape(shape)
            .onTapGesture { tap() }
            .extensionRowActions(tile.actions, model: model, link: $link)
            .accessibilityElement(children: .combine)
            .accessibilityLabel(spoken)
            .accessibilityAddTraits(tile.action != nil ? .isButton : [])
            .accessibilityAction { tap() }
            .accessibilityIdentifier("plugin-ui-tile-\(tile.key ?? tile.title)")
            .alert(control?.action.confirm ?? "", isPresented: $confirming) {
                Button { if let v = asking, let c = control { send(c, v) } } label: {
                    Text(asking == "true" ? String(localized: "Turn On") : String(localized: "Turn Off")).lineLimit(1)
                }
                .accessibilityIdentifier("plugin-ui-confirm")
                Button(role: .cancel) { if let c = control { model?.preview(c.name, nil) } } label: { Text("Cancel").lineLimit(1) }
                    .accessibilityIdentifier("plugin-ui-confirm-cancel")
            }
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: Theme.Space.flush) {
            if let img = tile.image {
                // As tall as the tile's width makes it: offered only part of the tile's height, a 16:9 shape fits
                // narrower than the tile.
                picture(img)
                    .aspectRatio(16 / 9, contentMode: .fit)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity)
                    .clipped()
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(alignment: .top, spacing: Theme.Space.s) {
                    if tile.image == nil {
                        ThemedIcon(ExtensionUIStyle.icon(tile.icon))
                            .symbolVariant(on ? .fill : .none)
                            .font(Theme.Font.heading)
                            .foregroundStyle(tile.color == nil && !on ? Color.secondary : tint)
                            .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                            .accessibilityHidden(true)
                    }
                    Spacer(minLength: 0)
                    if let b = tile.badge, !b.text.isEmpty { Chip(text: b.text, look: ExtensionUIStyle.badge(b)) }
                    if let c = control { toggle(c) }
                }
                .padding(.bottom, Theme.Space.xs)
                Spacer(minLength: 0)
                Text(tile.title).font(Theme.Font.labelStrong).foregroundStyle(Color.primary).lineLimit(1)
                if let s = tile.subtitle, !s.isEmpty {
                    Text(s).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
                if tile.value?.isEmpty == false || tile.spark != nil {
                    HStack(alignment: .bottom, spacing: Theme.Space.s) {
                        if let v = tile.value, !v.isEmpty {
                            ExtensionChangingText(v).font(Theme.Font.bodyStrong.monospacedDigit()).foregroundStyle(Color.primary).lineLimit(1)
                        }
                        Spacer(minLength: 0)
                        if let spark = tile.spark { ExtensionSparkLine(spark: spark) }
                    }
                    .padding(.top, Theme.Space.xxs)
                }
            }
            .padding(Theme.Space.m)
        }
    }

    /// Its picture: an https one, or one the hub serves for the page (`ExtensionPicture`).
    private func picture(_ img: ExtensionImageSource) -> some View {
        ExtensionPicture(src: img, model: model, points: Theme.Size.tileRegular, alt: tile.title)
    }

    private func toggle(_ c: ExtensionRowControl) -> some View {
        Toggle(isOn: Binding(get: { switchedOn }, set: { change(c, $0 ? "true" : "false") })) { Text(tile.title) }
            .labelsHidden()
            .tint(tint)
            .controlSize(.small)
            .disabled(model?.sendsControls != true || c.disabled == true)
            .fixedSize()
            .accessibilityIdentifier("plugin-ui-switch-\(c.name)")
    }

    /// Its switch flipped: shown at once, asked first when the action says so, sent (`ExtensionViewModel.change`).
    private func change(_ c: ExtensionRowControl, _ value: String) {
        guard model?.sendsControls == true, c.disabled != true else { return }
        if let confirm = c.action.confirm, !confirm.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            model?.preview(c.name, value)
            asking = value
            confirming = true
            return
        }
        send(c, value)
    }

    private func send(_ c: ExtensionRowControl, _ value: String) {
        guard let model else { return }
        Task { ExtensionOutcome.show(await model.change(c, to: value), app: app, link: $link) }
    }

    private var spoken: String {
        var parts = [tile.title]
        if let s = tile.subtitle, !s.isEmpty { parts.append(s) }
        if let v = tile.value, !v.isEmpty { parts.append(v) }
        if let b = tile.badge, !b.text.isEmpty { parts.append(b.text) }
        if on { parts.append(String(localized: "On")) }
        return parts.joined(separator: String(localized: ", "))
    }

    private func tap() {
        guard let a = tile.action, let model, model.isEnabled(a) else { return }
        let execute = model.prepared(a, with: [:], displayed: displayed)
        Task { ExtensionOutcome.show(await execute(), app: app, link: $link) }
    }
}

/// An extension's `grid`: tiles in as many columns as fit (two on a phone), at most `max_columns` (4 when it does not
/// say).
struct ExtensionGridItem: View {
    var grid: ExtensionGridComponent
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    @Environment(\.pluginUICompact) private var compact

    var body: some View {
        let most = min(max(grid.maxColumns ?? 4, 1), 6)
        ViewThatFits(in: .horizontal) {
            ForEach((1...most).reversed(), id: \.self) { n in
                columns(n).frame(minWidth: CGFloat(n) * Theme.Size.tileMin + CGFloat(n - 1) * Theme.Space.s)
            }
        }
        .accessibilityElement(children: .contain)
    }

    private func columns(_ n: Int) -> some View {
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: Theme.Space.s), count: n), spacing: Theme.Space.s) {
            ForEach(Array(grid.tiles.prefix(compact ? 4 : 60).enumerated()), id: \.offset) { _, t in
                ExtensionTileView(tile: t, model: model, link: $link)
            }
        }
    }
}

/// An extension's `carousel`: tiles in a line that scrolls sideways, snapping to a tile, the next one's edge showing;
/// narrow ones for scenes, regular ones for rooms, wide ones (most of the width) for cameras.
struct ExtensionCarouselItem: View {
    var carousel: ExtensionCarouselComponent
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    @Environment(\.pluginUICompact) private var compact

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let t = carousel.title, !t.isEmpty {
                Text(t).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                    .padding(.horizontal, Theme.Space.page)
                    .accessibilityAddTraits(.isHeader)
            }
            ScrollView(.horizontal) {
                LazyHStack(alignment: .top, spacing: Theme.Space.s) {
                    ForEach(Array(carousel.tiles.prefix(compact ? 6 : 20).enumerated()), id: \.offset) { _, t in
                        ExtensionTileView(tile: t, model: model,
                                          minHeight: carousel.width == ExtensionTileWidth.narrow ? Theme.Size.box80 : Theme.Size.tileHeight,
                                          link: $link)
                            .containerRelativeFrame(.horizontal) { visible, _ in tileWidth(visible) }
                    }
                }
                .scrollTargetLayout()
                .padding(.horizontal, Theme.Space.page)
            }
            .scrollTargetBehavior(.viewAligned)
            .scrollIndicators(.hidden)
            .scrollClipDisabled()
        }
        .accessibilityElement(children: .contain)
    }

    /// From the scroll view's width, known at the first layout: a width learnt after it leaves a list row as tall as
    /// that first layout made it (a wide tile's picture then fitted small inside it).
    private func tileWidth(_ visible: CGFloat) -> CGFloat {
        switch carousel.width {
        case ExtensionTileWidth.narrow: Theme.Size.tileNarrow
        case ExtensionTileWidth.wide: max(Theme.Size.tileRegular, (visible - Theme.Space.page * 2) * Theme.Size.tileWideShare)
        default: Theme.Size.tileRegular
        }
    }
}
