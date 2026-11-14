import SwiftUI

// Surfaces and buttons that follow the chosen style (AppStyle, through StyleSpec.current): Liquid Glass in Liquid Glass /
// BeautifulUI, the style's own solid look in Clay / Island / Lark. Use these instead of `glassEffect` / `.themedButton()` directly.

extension View {
    /// A floating panel (composer, interaction panel, status strips, toasts).
    func surface<S: Shape>(tint: Color? = nil, interactive: Bool = false, in shape: S) -> some View {
        modifier(Surface(shape: shape, tint: tint, interactive: interactive))
    }

    /// A list row on the style's canvas (plain lists paint rows white otherwise).
    @ViewBuilder func themedRow() -> some View {
        if StyleSpec.current.usesGlass { self } else { listRowBackground(Color.clear) }
    }

    /// A scroll page that paints its own background (not a List): the system's grouped gray in the glass styles, the
    /// style's canvas otherwise (it used to stay system gray in Clay / Island / Lark, and jumped against the next page).
    func themedGroupedPage() -> some View { background(Theme.groupedPage) }

    /// The page behind a list / scroll view: the style's canvas; the glass styles keep the system's.
    @ViewBuilder func themedPage() -> some View {
        if StyleSpec.current.usesGlass {
            self
        } else {
            scrollContentBackground(.hidden).background(Theme.pageBackground)
        }
    }
}

private struct Surface<S: Shape>: ViewModifier {
    var shape: S
    var tint: Color?
    var interactive: Bool

    func body(content: Content) -> some View {
        switch StyleSpec.current.surface {
        case .glass:
            #if os(visionOS)
            // visionOS has no Liquid Glass: its own glass material, the tint washed over it, and the system's
            // look-at highlight on the panels that take a pinch.
            content
                .background { if let tint { shape.fill(tint) } }
                .background(.regularMaterial, in: shape)
                .hoverEffect(isEnabled: interactive)
            #else
            content.glassEffect(glass, in: shape)
            #endif
        case .puffy:
            // Clay: a puffy pastel slab — light from the top left, a soft shadow below in the color's wash.
            content.background {
                ZStack {
                    shape.fill(Theme.card)
                    if let tint { shape.fill(tint) }
                }
                .shadow(color: StyleSpec.current.puffyHighlight.opacity(0.8), radius: 6, x: -4, y: -4)
                .shadow(color: StyleSpec.current.puffyShadow.opacity(0.22), radius: 12, x: 5, y: 8)
            }
            .overlay(shape.stroke(Color.white.opacity(0.45), lineWidth: 1))
        case .floating:
            content.background {
                ZStack {
                    shape.fill(Theme.card)
                    if let tint { shape.fill(tint) }
                }
                .shadow(color: .black.opacity(0.13), radius: 20, y: 10)
            }
        case .hairline:
            content.background {
                ZStack {
                    shape.fill(Theme.card)
                    if let tint { shape.fill(tint) }
                }
            }
            .overlay(shape.stroke(Theme.cardStroke, lineWidth: 0.5))
        }
    }

    #if !os(visionOS)
    private var glass: Glass {
        var g = Glass.regular
        if let tint { g = g.tint(tint) }
        if interactive { g = g.interactive() }
        return g
    }
    #endif
}

extension View {
    /// A `Menu` drawn as its label and nothing more, as on iOS (a ⋯, a chip that draws its own ⌃⌄). The Mac's default
    /// is a bordered pop-up button with its own ⌄ beside the label: a second arrow and a frame around the chip, a wide
    /// pill under the round ⋯ (#502). visionOS draws a glass platter under the label, a blank white pill once the
    /// tertiary ⋯ fades into it. iOS unchanged.
    @ViewBuilder func labelOnlyMenu() -> some View {
        #if os(macOS)
        menuStyle(.button).buttonStyle(.plain).menuIndicator(.hidden).fixedSize()
        #elseif os(visionOS)
        menuStyle(.button).buttonStyle(.plain)
        #else
        self
        #endif
    }

    /// A `Menu` that keeps the button the system draws for it, without the Mac's ⌄ beside the label: a ⋯ among a
    /// toolbar's bordered buttons (the file viewer's, next to Done). iOS unchanged.
    @ViewBuilder func arrowlessMenu() -> some View {
        #if os(macOS)
        menuIndicator(.hidden).fixedSize()
        #else
        self
        #endif
    }

    /// A button in a settings form (`SettingsForm`): the phone's themed button; on the Mac the system's standard push
    /// button (prominent: the default-looking one), as System Settings has them, not a tinted capsule (#1221).
    @ViewBuilder func formButton(prominent: Bool = false) -> some View {
        #if os(macOS)
        if prominent { buttonStyle(.borderedProminent) } else { buttonStyle(.bordered) }
        #else
        themedButton(prominent: prominent)
        #endif
    }

    /// Glass buttons in the glass styles, bordered ones in Lark, soft raised pills in Clay / Island. Picks the system
    /// style directly: wrapping `Button(configuration)` in a PrimitiveButtonStyle that re-styles it recursed until the
    /// stack overflowed (crash on opening a chat in the glass styles).
    /// Primary and secondary decisions on a pending draft use large targets, not compact utility buttons.
    @ViewBuilder func draftDecisionButton(prominent: Bool = false) -> some View {
        #if os(macOS)
        if StyleSpec.current.buttons == .glass {
            buttonStyle(MacCapsuleButton(prominent: prominent, minimumHeight: Theme.Size.tapTarget)).controlSize(.large)
        } else {
            themedButton(prominent: prominent).controlSize(.large)
        }
        #else
        themedButton(prominent: prominent).controlSize(.large)
        #endif
    }

    /// The style's button; capped at the interface's largest size like everything but reading content, with the Large
    /// Content Viewer for a long press at the accessibility sizes (`docs/design/DESIGN_SYSTEM.md` section 6.2).
    func themedButton(prominent: Bool = false) -> some View {
        styledButton(prominent: prominent).accessibilityShowsLargeContentViewer()
    }

    @ViewBuilder private func styledButton(prominent: Bool) -> some View {
        switch StyleSpec.current.buttons {
        case .glass:
            // .glassProminent keeps its full color when disabled (iOS 26.0): an empty composer's send looked tappable.
            #if os(macOS)
            // The Mac's glass styles took their colors from the window, not from us: the prominent one drew its label
            // cyan on the blue (about 3.2:1), the plain one turned solid blue in the active window (2026-11-02). Both
            // drawn here (`MacCapsuleButton`).
            buttonStyle(MacCapsuleButton(prominent: prominent))
            #elseif os(visionOS)
            // visionOS has no .glassProminent: its windows are glass already, and the system's prominent style is the filled one.
            if prominent { buttonStyle(.borderedProminent).modifier(DimWhenDisabled()) } else { buttonStyle(.bordered) }
            #else
            if prominent {
                buttonStyle(.glassProminent).modifier(DimWhenDisabled())
            } else {
                buttonStyle(RedWhenDestructive()).buttonStyle(.glass).menuStyle(SystemStyleMenu()).modifier(LabelOffItsFill())
            }
            #endif
        case .bordered:
            #if os(iOS)
            if prominent {
                buttonStyle(.borderedProminent)
            } else {
                buttonStyle(RedWhenDestructive()).buttonStyle(.bordered).menuStyle(SystemStyleMenu(bordered: true)).modifier(LabelOffItsFill())
            }
            #else
            if prominent { buttonStyle(.borderedProminent) } else { buttonStyle(.bordered).modifier(LabelOffItsFill()) }
            #endif
        case .soft:
            buttonStyle(SoftButtonStyle(prominent: prominent))
        }
    }
}

/// A secondary glass or bordered button's label, pushed away from the fill under it (#6157): blended with it, lighter in
/// dark (screen), darker in light (multiply), and the more so the lighter that fill is. The glass's fill is the system's
/// and turns lighter in a sheet in dark; Lark's is a wash of the label's own color. In the plain tint the label read at
/// 2.0:1 on a panel in a dark sheet and 2.7:1 for coral in light Lark, under the 3:1 `StyleSpecTests` holds the accent
/// to. `ButtonContrastUITests` measures it on every fill the app puts one on. Disabled buttons keep the system's look; the
/// Mac (frozen) and visionOS draw theirs unchanged.
private struct LabelOffItsFill: ViewModifier {
    @Environment(\.colorScheme) private var scheme
    @Environment(\.isEnabled) private var enabled

    func body(content: Content) -> some View {
        #if os(iOS)
        if enabled {
            content.foregroundStyle(.tint.blendMode(scheme == .dark ? .screen : .multiply))
        } else {
            content
        }
        #else
        content
        #endif
    }
}

/// A secondary button with the destructive role draws in `Theme.danger` in every style (#7042). The system's glass and
/// bordered styles take the app's tint over the role (the accent: a Delete looked like any other button), so this style,
/// placed under theirs, gives such a button the danger tint and leaves every other button as it is. `Button(configuration)`
/// draws the style above this one, as Apple's documented way to adjust the current style; re-styling it inside a
/// PrimitiveButtonStyle is what recursed before (see `draftDecisionButton`). Clay / Island read the role in `SoftButtonStyle`.
private struct RedWhenDestructive: PrimitiveButtonStyle {
    @ViewBuilder func makeBody(configuration: Configuration) -> some View {
        if configuration.role == .destructive {
            Button(configuration).tint(Theme.danger)
        } else {
            Button(configuration)
        }
    }
}

#if os(iOS)
/// A `Menu` styled as a secondary button, drawn with the system's glass or bordered style itself (#7214). Drawn through
/// `RedWhenDestructive` like a button, its label lost the style's background: the ⋯ and clipboard menus of the live
/// screen floated on the page beside capsule buttons. A menu has no destructive role to color, so it skips that style.
/// `Menu(configuration)` draws the menu style below this one, as Apple documents for styles that adjust the current one.
private struct SystemStyleMenu: MenuStyle {
    var bordered = false

    @ViewBuilder func makeBody(configuration: Configuration) -> some View {
        if bordered {
            Menu(configuration).buttonStyle(.bordered)
        } else {
            Menu(configuration).buttonStyle(.glass)
        }
    }
}
#endif

/// Fades a control whose style does not show "disabled" by itself.
private struct DimWhenDisabled: ViewModifier {
    @Environment(\.isEnabled) private var enabled

    func body(content: Content) -> some View {
        content.opacity(enabled ? 1 : 0.4)
    }
}

/// A raised pill for the clay / island styles, at least 44 pt tall (it was 34). Colored by the current tint, as the
/// system styles are: the accent, or what the call site sets — Interrupt and Close Agent are red here too (they were the
/// accent: purple in Clay, teal in Island). A secondary one with the destructive role is `Theme.danger`, as in the other
/// styles (#7042).
private struct SoftButtonStyle: ButtonStyle {
    var prominent: Bool
    @Environment(\.isEnabled) private var enabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .foregroundStyle(labelStyle(configuration))
            .background {
                ZStack {
                    if prominent {
                        Capsule().fill(.tint)
                        // Depth that only darkens: a lighter top (Color.gradient) took the white words under 3:1.
                        Capsule().fill(LinearGradient(colors: [.clear, .black.opacity(0.12)], startPoint: .top, endPoint: .bottom))
                    } else {
                        Capsule().fill(Theme.card)
                    }
                }
                .compositingGroup()
                .shadow(color: StyleSpec.current.softShadow,
                        radius: configuration.isPressed ? 2 : 6, y: configuration.isPressed ? 1 : 3)
            }
            .opacity(enabled ? 1 : 0.45)
            .scaleEffect(configuration.isPressed ? 0.96 : 1)
            .animation(.snappy(duration: 0.15), value: configuration.isPressed)
    }

    private func labelStyle(_ configuration: Configuration) -> AnyShapeStyle {
        if prominent { return AnyShapeStyle(Color.white) }
        #if os(iOS)
        if configuration.role == .destructive { return AnyShapeStyle(Theme.danger) }
        #endif
        return AnyShapeStyle(.tint)
    }
}

extension View {
    /// Mirrors vertically: applied to the chat's scroll view (newest first) and again to each row to stand it up.
    func flippedBack() -> some View { scaleEffect(x: 1, y: -1) }
}

extension Theme {
    /// The line a file was opened at (`path:12`), marked across the code view.
    static var lineFocus: Color { accent.opacity(Quote.flashOpacity) }

    /// The color `themedGroupedPage()` paints: what floats over such a page (a composer) is backed with it, so text
    /// that scrolls under it at large sizes does not show through.
    static var groupedPage: Color { StyleSpec.current.usesGlass ? Color(.systemGroupedBackground) : pageBackground }
}
