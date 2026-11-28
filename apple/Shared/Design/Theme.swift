import SwiftUI
#if os(iOS)
import UIKit
#endif
import PersonaWire

/// How the whole app is drawn, picked in Settings › Interface Style: panels, cards and buttons, the page they sit on, the type,
/// the components. The colors are Theme Color (`ThemeColor`), a choice of its own; `StyleSpec` puts the two together.
/// Changing either rebuilds the view tree (RootView is keyed by `AppModel.look`), so plain static tokens are enough.
nonisolated enum AppStyle: String, CaseIterable, Identifiable, Sendable {
    /// iOS 26 system look: Liquid Glass on the system's pages.
    case glass
    /// Claymorphism: puffy rounded surfaces with layered shadows on a pastel wash of the color, rounded type.
    case clay
    /// Floating white "islands" with deep soft shadows on a misty wash of the color, generous corners.
    case island
    /// Feishu: flat white cards on light gray, hairline borders — like the Feishu app.
    case lark
    /// Liquid Glass's surfaces with BeautifulUI's components: the thinking trace, tool chips in work cards, and its prompt
    /// bar as the composer. Only this style uses them (CLAUDE.md: a new look is a new style, never a change to one).
    case beautiful
    // "claude" was a style until 2026-10-31: Liquid Glass in coral, now Liquid Glass + Theme Color Coral (`AppearanceChoice`).

    nonisolated(unsafe) static var current: AppStyle = AppearanceChoice.saved.style {
        didSet { StyleSpec.recompose() }
    }

    var id: String { rawValue }
    var title: String { component.title }
    var subtitle: String { component.subtitle }
    /// Liquid Glass surfaces and buttons; the other styles draw solid ones of their own.
    var usesGlass: Bool { component.surface == .glass }
    /// BeautifulUI's components (`.beautiful` only); every other style keeps Persona' own.
    var usesBeautifulUI: Bool { component.components == .beautiful }
    var fontDesign: Font.Design { component.fontDesign }
    /// Corner radius of the cards in the style picker's preview.
    var corner: CGFloat { component.previewCorner }
}

nonisolated enum Theme {
    #if os(iOS)
    /// A split's list column slid by a finger (#4206, `SplitColumnControl`).
    enum Split {
        /// How much of the blur over the pictures a drag reaches (a fraction of `.systemUltraThinMaterial`'s).
        static let dragBlur: CGFloat = 0.8
        /// How far the finger goes, in points, before the blur is at its most: none as it lands.
        static let blurReach: CGFloat = 80
        /// Faster than this, in points a second, the fling decides where the column settles, not how far it went.
        static let fling: CGFloat = 500
        /// How long the line takes to settle out or away after the finger lets go.
        static let settle: TimeInterval = 0.28
        /// How long the pictures take to fade once the line has settled, the split under them as it ends.
        static let fade: TimeInterval = 0.2
        /// The guide line on the line between the column and the page while a finger moves it, in the theme color (as
        /// Feishu's; measured there at about 3 pt).
        static let guide: CGFloat = 3
        /// How dark the system lays a page under the column over it (measured: the page at 0.8 of its light); the drag's
        /// page picture is taken without it and darkens as the column comes out.
        static let overDim: CGFloat = 0.2
    }

    /// By Repository's tree on iOS (#2531): a card per repository, its worktrees and their agents a level in each, every
    /// line's mark in one column per level; the agents in the display's rows (#3753).
    enum Tree {
        /// A level in.
        static let indent: CGFloat = 22
        /// The column a line's mark sits in: the folder, a worktree's mark, an agent's state.
        static let column: CGFloat = 18
        /// Above and below a line's words: the line is its whole row, at least a finger's height (`RepoTreeLine`).
        static let rowPadding = Space.sm
        /// The agent or worktree open beside the list (regular width).
        static let selected = Palette.systemBlue.opacity(0.1)
    }
    #endif

    /// Orca-compatible workspace cards. Scoped to repository browsing, including nested selection surfaces.
    enum WorkspaceSidebar {
        static let background = Color(light: 0xF5F5F5, dark: 0x2A2A2A)
        static let foreground = Color(light: 0x0A0A0A, dark: 0xF0F0F0)
        static let muted = Color(light: 0x737373, dark: 0xA3A3A3)
        static let border = Color(light: 0xE5E5E5, dark: 0x404040)
        static let chip = Color(light: 0xFFFFFF, dark: 0x353535)
        static let success = Color(light: 0x00BC7D, dark: 0x00BC7D)
        static let unread = Color(light: 0xFF9800, dark: 0xFFB020)
        static let selected = foreground.opacity(0.08)
        static let hovered = foreground.opacity(0.04)
        static let cardLeading: CGFloat = 10
        static let childInset: CGFloat = 10
        static let statusLane: CGFloat = 16
        static let titleHeight: CGFloat = 20
        static let agentHeight: CGFloat = 24
        static let badgeHeight: CGFloat = 16
        static let projectBadgeWidth: CGFloat = 96
        static let cardRadius: CGFloat = 8
        static let chipRadius: CGFloat = 4
        static let rule: CGFloat = 1
        static let hostRotation = Angle.degrees(90)
        static var title: SwiftUI.Font { Font.caption }
        static var titleUnread: SwiftUI.Font { Font.captionStrong }
        static var meta: SwiftUI.Font { Font.label }
        static var agent: SwiftUI.Font { Font.label }
    }

    private static var spec: StyleSpec { StyleSpec.current }
    #if os(watchOS)
    static let systemPage = Color.black
    static let systemCard = Color(white: 0.12)
    static let groupedCard = Color(white: 0.12)
    #else
    static let systemPage = Color(uiColor: .systemBackground)
    static let systemCard = Color(uiColor: .secondarySystemBackground)
    /// A card on a grouped page: white in light, where `systemCard` is the grouped page's own gray (the same in dark).
    static let groupedCard = Color(uiColor: .secondarySystemGroupedBackground)
    #endif

    static var accent: Color { spec.accent }
    /// The persona's own color (primary color, docs/apple/persona.md): its face's glow and backdrop, never something to tap (that is
    /// `accent`). The accent when the persona brings none (the built-in Persona is drawn in the accent).
    static var persona: Color { spec.personaPrimary ?? spec.accent }
    /// The app icon's coral. Only for Persona' own mark (the glyph in Settings / Chat, the Live Activity's star), never for
    /// something to tap: that is `accent`, whatever the style.
    static let brand = Palette.coral
    static var danger: Color { spec.extensionColors?.danger ?? Role.dangerColor }

    // Meanings that are not agent states. Never use a state color (StatusPalette) for these: changing a state color
    // must not recolor diffs, warnings or categories.
    /// Success and "connected / online / usable" outside the agent states (toasts, the hub dot, a finished to-do).
    static var positive: Color { spec.extensionColors?.positive ?? Role.positiveColor }
    /// Warnings that are not an agent asking for you (connecting, stale, truncated, a version mismatch): a symbol
    /// beside the words, never a dot. `cautionText` when the words themselves are colored.
    static var caution: Color { spec.extensionColors?.caution ?? Role.cautionColor }
    static var cautionText: Color { spec.extensionColors?.cautionText ?? Role.cautionTextColor }
    /// What the AI wrote or suggests (✦ summaries, AI actions): not the accent, which means "tap me".
    static var ai: Color { spec.extensionColors?.ai ?? Role.aiColor }
    /// A soft ground in the AI color, behind what is the AI's and lit (a workflow's current phase, its mark, the
    /// watch's workflow chip and Pick tag), as a state's `StatusTone.tint`. Stronger on the watch's black.
    static var aiTint: Color { ai.opacity(aiTintStrength) }
    #if os(watchOS)
    private static let aiTintStrength = 0.2
    #else
    private static let aiTintStrength = 0.14
    #endif
    /// A line picked out for a moment in the accent's soft ground (the transcript line a minutes item's time jumped to).
    static var accentTint: Color { accent.opacity(aiTintStrength) }
    /// Any color's soft ground at the AI tint's strength: behind a mark drawn in that color (a minutes topic's icon tile,
    /// #6563), so the mark reads at 3:1 or better on it (`StyleSpecTests`).
    static func softTint(_ color: Color) -> Color { color.opacity(aiTintStrength) }
    /// The AI color at half strength: a mark of the AI's that is at work but not the lit one (a workflow phase with
    /// agents running that the run has moved past), apart from a ring (not started) and the full dot (lit).
    static var aiMuted: Color { ai.opacity(0.5) }
    /// Someday on a to-do's trailing swipe (#10465), beside Done's `positive`: the owner's "not now", the system's indigo,
    /// no state's color. Close to `ai` and to Clay's accent, which never sit beside it in a row's swipe. White words on it
    /// read at 4.5:1 or better in both appearances (`StyleSpecTests`).
    static let somedayTint = Hue.indigo

    enum Role {
        static let danger = PersonaPalette.Pair(light: 0xD93A3A, dark: 0xFF5F57)
        static let positive = PersonaPalette.Pair(light: 0x248A3D, dark: 0x30D158)
        static let caution = PersonaPalette.Pair(light: 0xF08A00, dark: 0xFF9F0A)
        static let cautionText = PersonaPalette.Pair(light: 0xB35400, dark: 0xFF9F0A)
        static let ai = PersonaPalette.Pair(light: 0x5B4FD6, dark: 0x9C94FF)
        static let dangerColor = danger.color
        static let positiveColor = positive.color
        static let cautionColor = caution.color
        static let cautionTextColor = cautionText.color
        static let aiColor = ai.color
    }
    /// Where an agent was opened (#2526): a Feishu group's topic (botmux) and a terminal pane (herdr), each its own color.
    static let sourceFeishu = Color(light: 0x1D5FD6, dark: 0x82AAFF)
    static let sourceTerminal = Color(light: 0x6A46C2, dark: 0xBEA3FF)
    /// /loop and other scheduled wake-ups (Waiting).
    static let loop = Color(light: 0x2E6FD0, dark: 0x5B9BFF)
    /// Count badges on rows (unread messages): white text on it is ≥ 4.5:1 in both appearances.
    static let countBadge = Color(light: 0x636366, dark: 0x636366)
    /// The count on a tab bar's item, as the system's tab bar draws `.badge` (white on its red), the iPad's included: iPhone
    /// Duo's tab bar (#4231).
    static let tabBarBadge = Color.red
    static let onSolid = Color.white

    /// A code to scan with another app (a QR code): dark modules on a light ground in both appearances, the way
    /// cameras read them.
    enum Scan {
        static let module = Color.black
        static let ground = Color.white
    }
    /// The disc behind a monogram that has no color of its own (a mail sender).
    #if os(watchOS)
    static let monogramFill = Color.white.opacity(0.16)
    #else
    static let monogramFill = Color(uiColor: .secondarySystemFill)
    #endif

    /// A name's disc without a picture (`Monogram`) and its letter's color: the neutral disc in the label's color, in every
    /// style.
    static func monogram(_ name: String) -> (fill: Color, ink: Color?) {
        return (monogramFill, nil)
    }

    /// Diffs and Git: their own colors, text strength (≥ 4.5:1 on the code background) for signs and counts.
    enum Diff {
        static let add = Color(light: 0x1D7A4C, dark: 0x4CC38A)
        static let del = Color(light: 0xC62B33, dark: 0xFF6369)
        static let addFill = Color(light: 0x30A46C, dark: 0x4CC38A).opacity(0.14)
        static let delFill = Color(light: 0xE5484D, dark: 0xFF6369).opacity(0.12)
        /// The words that changed inside a changed line (the chat's `DiffView`), over that line's fill: the code on
        /// them stays ≥ 4.5:1 in both appearances (about 7:1 dark, 11:1 light).
        static let addEmphasis = Color(light: 0x30A46C, dark: 0x4CC38A).opacity(0.32)
        static let delEmphasis = Color(light: 0xE5484D, dark: 0xFF6369).opacity(0.30)
        /// A modified file ("M"), the way editors mark it, kept apart from the working amber.
        static let changed = Color(light: 0x8A6A00, dark: 0xE3B341)
    }

    /// Corner radii: five, plus capsules and circles. Static: the styles differ in color and surface, not in corners
    /// (`AppStyle.corner` only draws the style previews in Settings).
    enum Radius {
        /// A file's thumbnail in a list row, about an icon's size (the Files tab): a page's corner, not a tile's.
        static let thumbnail: CGFloat = 3
        /// Badges, small marks.
        static let badge: CGFloat = 6
        /// Code blocks, thumbnails, segmented controls, small tiles.
        static let control: CGFloat = 10
        /// Cards, groups, an open work line, option rows.
        static let card: CGFloat = 14
        /// Message bubbles.
        static let bubble: CGFloat = 20
        /// Floating panels over the chat (interactions, strips, the key bar), the terminal card, the composer's field.
        static let panel: CGFloat = 22
        /// A whole page pushed aside as a card (the Persona tab's page beside its sidebar): about the screen's own
        /// corners on a current iPhone, so the card reads as the screen slid over, not a smaller box.
        static let page: CGFloat = 55
        /// An app icon's corner for an icon `side` points wide: the home screen's squircle (about 22.4% of the side).
        static func appIcon(_ side: CGFloat) -> CGFloat { side * 0.2237 }
    }

    /// Spacing on a 4 pt grid, BeautifulUI's (`BUISpacing`): 2 4 6 8 12 16 24 32, nothing in between. Pages and the
    /// terminal card keep 16 from the screen's edges.
    enum Space {
        /// No gap: parts that touch (a diff line's gutter, mark and text; the two sides of a split diff).
        static let flush: CGFloat = 0
        static let xxs: CGFloat = 2
        static let xs: CGFloat = 4
        static let sm: CGFloat = 6
        static let s: CGFloat = 8
        static let m: CGFloat = 12
        static let l: CGFloat = 16
        static let xl: CGFloat = 24
        static let xxl: CGFloat = 32
        /// Between the lines of one row: title, preview, meta.
        static let lines: CGFloat = xs
        /// From a title to the content under it.
        static let titleToContent: CGFloat = s
        /// Between blocks on a page or in a card.
        static let block: CGFloat = l
        /// Between sections.
        static let section: CGFloat = xl
        /// A page's left and right edge: its search field, filter chips, section titles and cards line up on it.
        static let page: CGFloat = l
    }

    /// Three grays for backgrounds instead of `Color.primary.opacity(…)`: they are the system fills, so they read in
    /// dark mode too (5 % white on black was all but invisible). `well` behind icons and quiet rows, `chip` behind
    /// labels and small controls, `pressed` for a selected or pressed state.
    enum Fill {
        #if os(watchOS)
        static let well = Color.white.opacity(0.08)
        static let chip = Color.white.opacity(0.12)
        static let pressed = Color.white.opacity(0.18)
        #else
        static let well = Color(uiColor: .quaternarySystemFill)
        static let chip = Color(uiColor: .tertiarySystemFill)
        static let pressed = Color(uiColor: .secondarySystemFill)
        /// Laid over a page pushed aside by a sidebar, so the sidebar in front reads as what is active (#334). Black in
        /// both modes; darker in dark mode, where the page is already near black and only its text can dim.
        static let scrim = Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? UIColor.black.withAlphaComponent(0.45) : UIColor.black.withAlphaComponent(0.2) })
        #endif
    }

    #if !os(watchOS)
    /// A long-pressed message lifted over its chat (`MessageLift`, #603).
    enum Lift {
        /// How far the chat behind is blurred: its words still show, faintly.
        static let blur: CGFloat = 3
        /// Over the blurred chat: lighter than `Fill.scrim`, for the same reason.
        static let scrim = Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? UIColor.black.withAlphaComponent(0.3) : UIColor.black.withAlphaComponent(0.08) })
        /// The bar of actions next to it: light in light mode, the dark bar it always was in dark mode.
        static let bar = Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? UIColor(white: 0.17, alpha: 1) : UIColor.white })
        /// A selection handle dragged this close to a long lifted message's top or bottom edge, or past it, scrolls the
        /// message on that way, faster the closer it gets: up to `edgeScrollSpeed` points a second (#3188).
        static let edgeScroll: CGFloat = 44
        static let edgeScrollSpeed: CGFloat = 900
        /// How far a selection handle's knob reaches past the selected lines (over the first, under the last): the text
        /// view over a lifted message's words reaches that far past them, so a knob there is in it, to be dragged (#5291),
        /// and the edit menu stays that far off them (#6005).
        static let handleReach: CGFloat = 16
    }
    #endif

    #if !os(watchOS)
    /// The crop of a new avatar (`AvatarCropView`): drawn over the photo itself, so the same in light and dark.
    enum Crop {
        /// Outside the circle the avatar keeps: dimmed, so the circle reads as what is kept.
        static let outside = Color.black.opacity(0.5)
        /// The circle's edge.
        static let edge = Color.white.opacity(0.9)
    }

    /// A card that adds one more to a grid of cards (Settings › Appearance › Avatar › New from Photo): an empty well
    /// with a dashed accent edge, so it reads as a place for something new rather than one of the choices.
    enum AddCard {
        static var edge: Color { Theme.accent.opacity(0.45) }
        static let dash: [CGFloat] = [Theme.Space.xs, Theme.Space.xs]
    }

    /// The avatar sheet's Theme Color, Pictures for Each State and image drop highlight (#15449, #15612, `AvatarStatesPages`).
    enum AvatarSheet {
        /// A theme color's swatch, round, beside its name.
        static let swatch = Size.iconS
        /// The edge around a swatch, so a white or a black one still reads on the row.
        static let swatchEdge = Color.primary.opacity(0.15)
        /// A state's picture in its row, as it will move: the size of the top bar's dot.
        static let preview = Size.avatarL
        /// The accent border and tint drawn over a picture slot or avatar card while an image is dragged over it (#15612).
        static var dropStroke: Color { Theme.accent }
        static let dropStrokeWidth = Size.rule
        static var dropTint: Color { Theme.accentTint }
    }
    #endif

    /// The image viewer's backdrop (`ImageViewer`), as Photos has it: the page's own color while its bars show (white
    /// in light mode, black in dark), black once a tap hides them.
    enum Viewer {
        static let canvas = systemPage
        static let immersive = Color.black
    }

    /// The type scale: every text in the app is one of these levels, all Dynamic Type text styles, so they follow the
    /// system's text size and the app's own Small / Standard / Large (`TextSize`, which moves the Dynamic Type size a step).
    /// As in BeautifulUI, levels differ more by weight and color than by size: a row's title is the body's size in
    /// semibold, a section header the caption's size in semibold secondary. Sizes at the default setting in brackets.
    /// The terminal grid (its own size setting), the Live Activity (system limits), the share card (a rendered
    /// image) and glyphs drawn to a frame are the exceptions.
    enum Font {
        /// The home page's greeting, the welcome page (28 bold).
        static let display = SwiftUI.Font.title.bold()
        /// A detail page's or a sheet's title: a mail's subject, a document, a profile, the settings header (22).
        static let title = SwiftUI.Font.title2.weight(.semibold)
        /// Titles of rows, cards, panels and Markdown headings below the first (17 semibold).
        static let heading = SwiftUI.Font.headline
        /// Messages, replies, mail and documents (17).
        static let body = SwiftUI.Font.body
        /// Previews, subtitles, descriptions in cards (15).
        static let secondary = SwiftUI.Font.subheadline
        /// Times, places, states, meta lines (13).
        static let caption = SwiftUI.Font.footnote
        /// A list's section header: the caption's size, semibold, in the secondary color (13).
        static let sectionHeader = SwiftUI.Font.footnote.weight(.semibold)
        /// Chips, badges, counts, small labels on controls (12).
        static let label = SwiftUI.Font.caption
        /// Code, diffs, paths, terminal snippets (13).
        static let code = SwiftUI.Font.footnote.monospaced()

        /// Text styles follow Dynamic Type on the iPhone: nothing to multiply.
        static let scale: CGFloat = 1

        // Emphasis inside a level: views use these, never `.weight(` on a level (TypeScaleTests). Strong is semibold,
        // the usual emphasis; medium and heavy (bold) are the weights in use today, kept so nothing moves.
        static let bodyStrong = body.weight(.semibold)
        static let bodyHeavy = body.weight(.bold)
        static let secondaryStrong = secondary.weight(.semibold)
        static let secondaryMedium = secondary.weight(.medium)
        static let captionStrong = caption.weight(.semibold)
        static let captionMedium = caption.weight(.medium)
        static let captionHeavy = caption.weight(.bold)
        static let labelStrong = label.weight(.semibold)
        static let labelMedium = label.weight(.medium)
        static let labelHeavy = label.weight(.bold)

        #if os(iOS) || os(visionOS)
        /// `body` for a UIKit control inside a SwiftUI page (the hidden `SecretField`): the same text style, so it
        /// follows Dynamic Type with the rest.
        static var uiBody: UIFont { .preferredFont(forTextStyle: .body) }
        #endif
    }

    /// The levels' sizes at the default text size and their line heights (a multiple of the size, as in CSS), for
    /// text that runs over several lines: `.lineHeight(_:)` turns them into line spacing at the current size.
    enum Level {
        case display, title, heading, body, secondary, caption, label, code

        var size: CGFloat {
            switch self {
            case .display: 28
            case .title: 22
            case .heading, .body: 17
            case .secondary: 15
            case .caption, .code: 13
            case .label: 12
            }
        }

        var lineHeight: CGFloat {
            switch self {
            case .display: 1.2
            case .title: 1.25
            case .heading: 1.3
            case .body: 1.45
            case .secondary: 1.4
            case .caption, .code: 1.35
            case .label: 1.3
            }
        }

        /// SF's own line height is about 1.19 × the size; line spacing adds the rest.
        var extraLeading: CGFloat { max(0, lineHeight - 1.19) * size }
    }

    /// How faint something is drawn that can't be acted on for now: rows that can't be picked while selecting (the
    /// inbox's Select, #2059; the web uses the same 45%).
    enum Fade {
        static let unavailable: Double = 0.45
        /// The page-colored chip on a transcript divider, as the web's bg-page/50.
        static let dividerBackground: Double = 0.5
        /// An offline assistant's row in the switcher's list: the whole row, still pickable (the web and Android use 55%).
        static let offline: Double = 0.55
    }

    /// How much of a rich card must intersect the scroll viewport before its isolated content runs.
    enum Visibility {
        static let richCardThreshold: Double = 0.01
    }

    /// The watch's list rows: rounded cards with room around and inside them, like the system's lists (rows at least
    /// 44 pt). The phone does not use these.
    enum Watch {
        /// Between two cards, as seen (it was 5).
        static let cardGap: CGFloat = 10
        /// What watchOS's List already leaves between two rows (measured on a 46 mm simulator): the cards add the rest.
        static let listRowSpacing: CGFloat = 5
        /// Inside a card, around its lines.
        static let cardPadding = EdgeInsets(top: 10, leading: 12, bottom: 10, trailing: 12)
        /// Between a row's lines (kind, title, place).
        static let lineSpacing: CGFloat = 4
        static let minRowHeight: CGFloat = 44
        static let cardRadius: CGFloat = 16
        /// The system list rows' gray.
        static let cardFill = Color(red: 0x22 / 255, green: 0x22 / 255, blue: 0x23 / 255)
        /// Blocks on a detail page (the question, its options, the buttons).
        static let blockSpacing: CGFloat = 10
        /// Inside a message bubble.
        static let bubblePadding: CGFloat = 8
        /// The Milk Frog on its page: its clips' pixels at 2x.
        static let petSize: CGFloat = 144
        /// The Milk Frog's round face on the list's row.
        static let petFaceSize: CGFloat = 40
    }

    /// Sizes of what is drawn next to text, at the default text size. Use them with `scaledFrame` so they follow
    /// Small / Standard / Large and the system's text size (up to 1.6 ×, `Theme.sizeScale`); the lines stay as they are, in a
    /// plain `.frame`. Nothing on a page writes a size as a number (`TokenRatchetTests`).
    enum Size {
        static let nonvisualProbe: CGFloat = 0
        // Lines, rules and bars: do not scale.
        static let hairline: CGFloat = 0.5
        /// The width of the small window's source layer (VNC's picture in picture, #1839): a normal size, behind the
        /// app's content; one too small never started.
        static let pictureInPictureSource: CGFloat = 192
        /// The width the small window's card is drawn at (VNCPiPCard: Connecting…, why not), in points, about the small
        /// window's own on an iPhone so its text reads at its size there.
        static let pictureInPictureCard: CGFloat = 240
        /// A task sheet closes on a rightward swipe that starts within `edgeBackZone` of the leading edge and travels at
        /// least `edgeBackDistance` (a sheet has no system edge-back; #5603).
        static let edgeBackZone: CGFloat = 24
        static let edgeBackDistance: CGFloat = 72
        static let line: CGFloat = 1
        static let lineMedium: CGFloat = 1.5
        static let rule: CGFloat = 2
        static let ruleWide: CGFloat = 2.5
        static let bar: CGFloat = 3
        /// How much of a long quote in a reply shows while it is folded (about four lines of body text, #2984).
        static let quoteFold: CGFloat = 104
        static let barWide: CGFloat = 4
        /// The usage cards' reset timeline (#13173): a row of the chart, the account-name column, the 5h and 7d bars, the
        /// hollow dot at a bar's end, and the room a window's bar leaves above itself for its protection flag.
        static let timelineRow: CGFloat = 30
        static let timelineName: CGFloat = 64
        static let timelineCap: CGFloat = 6
        static let timelineCapWide: CGFloat = 9
        static let timelineEnd: CGFloat = 11
        static let protectFlag: CGFloat = 14
        /// A share bar's thickness (the assistant's stacked-bar card).
        static let meter: CGFloat = 12
        /// An Agent chat's header folded into its slim bar (Android `Tokens.Size.slimBar`).
        static let slimBar: CGFloat = 40
        /// How much of the conversation a lone question card keeps in view over it when it grows past half the page
        /// (`PendingCards`, #6277): about two lines of a message, under the chat's header as it stands open (more once an
        /// Agents header has folded to its slim bar). The card the user has to answer gets the rest. A call keeps less for
        /// its captions (`voiceThreadRoom`).
        static let cardThreadRoom: CGFloat = 72
        /// The shortest a question card over the composer can be made with its grabber (#12306): its title row and the
        /// first lines of what it asks. The tallest leaves the conversation `Theme.Space.xl` (`FooterRoom.tallestRoom`).
        static let cardLeast: CGFloat = 150
        /// How far a sideways strip fades out at an end with more to scroll to (`.edgeFade()`): a cut-off chip reads as
        /// "more", not as a broken row.
        static let fadeEdge: CGFloat = 24
        // Dots and small marks.
        static let grabber: CGFloat = 5
        static let dotXS: CGFloat = 6
        static let dotS: CGFloat = 7
        static let dot: CGFloat = 8
        static let dotL: CGFloat = 9
        static let dotXL: CGFloat = 10
        /// How much of its circle the working arc (◐) draws, and the line it is drawn with (#10465).
        static let workingArcSweep: CGFloat = 0.72
        static let workingArcLine: CGFloat = 2
        static let markXS: CGFloat = 12
        static let markS: CGFloat = 14
        static let mark: CGFloat = 15
        static let markM: CGFloat = 16
        static let markL: CGFloat = 17
        static let badge: CGFloat = 18
        // Icons and the wells behind them.
        static let iconXS: CGFloat = 20
        static let iconS: CGFloat = 22
        static let icon: CGFloat = 24
        static let iconL: CGFloat = 26
        /// The least height the Mac's pointer is drawn at over its picture in the app's VNC (VNCPointerArt, #3075):
        /// about the size macOS draws it on its own screen, so it reads however small the picture is.
        static let pointer: CGFloat = 22
        static let wellXS: CGFloat = 28
        static let wellS: CGFloat = 30
        static let well: CGFloat = 32
        static let wellM: CGFloat = 34
        static let wellL: CGFloat = 36
        static let wellXL: CGFloat = 38
        // Faces and touch.
        static let avatar: CGFloat = 40
        static let avatarM: CGFloat = 42
        /// The most of the page the chat header's workflow section takes before it scrolls inside (#4532).
        static let workflowShare: CGFloat = 0.38
        /// The smallest thing a finger should hit.
        static let tapTarget: CGFloat = 44
        /// The widest a To-dos row's "OK: ‹option›" is drawn (#10465; DESIGN_SYSTEM 2.5): past it, at the text size in use,
        /// it says "OK" alone, since line 2 already names the recommendation and a button's words are never cut. Not
        /// scaled: the label's length and the text size decide (`TodoRowAnswers.okTitle`).
        static let todoAnswerMax: CGFloat = 260
        /// A search field's band under a navigation bar, as the system's drawer draws it (`AgentsHeader`).
        static let searchBand: CGFloat = 54
        /// The field in it, at its top.
        static let searchField: CGFloat = 44
        /// Between a search field and its ✕ while searching, as the system's search lays them out.
        static let searchCloseGap: CGFloat = 11
        static let avatarL: CGFloat = 46
        static let avatarXL: CGFloat = 48
        // Boxes: label columns, thumbnails, charts, previews and panels, by their size at the default text size.
        static let box50: CGFloat = 50
        static let box56: CGFloat = 56
        static let box58: CGFloat = 58
        static let box60: CGFloat = 60
        static let box64: CGFloat = 64
        static let box72: CGFloat = 72
        static let box76: CGFloat = 76
        static let box80: CGFloat = 80
        /// The height of the map in a saved place's editor: the reminder's circle with the streets around it, and room to
        /// pan the map and tap where the place is (#4248).
        static let placeMap: CGFloat = 220
        /// The map a place answer draws in the chat (`PlaceMapCard`, #5543): small enough to scroll past.
        static let chatMap: CGFloat = 160
        static let box110: CGFloat = 110
        static let box120: CGFloat = 120
        /// The longest a tab's name gets in the chat header's strip of the space's tabs: one long name cannot push the
        /// current tab out of sight.
        static let tabName: CGFloat = 120
        static let box140: CGFloat = 140
        static let box150: CGFloat = 150
        /// A file's card above the composer (#8361): a name of about 14 characters whole, and the next card showing.
        static let box160: CGFloat = 160
        static let box180: CGFloat = 180
        static let box200: CGFloat = 200
        static let box240: CGFloat = 240
        static let box260: CGFloat = 260
        static let box280: CGFloat = 280
        static let box300: CGFloat = 300
        /// A Feishu topic folds behind Expand past this (the Lark gateway's topic cards).
        static let box320: CGFloat = 320
        static let box380: CGFloat = 380
        // Wide screens (iPad).
        /// A page meant to be read, not a list (the welcome page): no wider than this, centered.
        static let readable: CGFloat = 560
        /// The square a new avatar is cropped in (Settings › Appearance › Avatar › New from Photo): no wider than this,
        /// narrower when the sheet is.
        static let avatarCrop: CGFloat = 320
        /// The widest the Agents chat header's title gets on a wide page (iPad, Mac): on an iPhone it takes all the room
        /// between the round buttons; past this a line is too long to read at a glance.
        static let headerTitle: CGFloat = 480
        /// The list column of a split on iPad: wide enough for the agents' cards and the inbox rows.
        static let sidebarMin: CGFloat = 340
        /// The open iPhone Duo's list column, its one width (#4206), upright over the page and on its side beside it: as
        /// wide as ChatGPT's sidebar on the open Duo (320 of its 669 pt upright, leaving the page most of its 951 pt on its
        /// side). The system's own was about 199 pt upright and 239 on its side, too narrow for the sidebar's rows.
        static let sidebarPhone: CGFloat = 320
        /// The narrowest a split's page gets beside its list column (iOS, `SplitPageMin`), no narrower than a large phone's
        /// screen: where the column would leave the page less, it lies over the page instead (the open iPhone Duo and an
        /// iPad mini, upright). At 349 pt beside the Duo's 320 pt column the split could not settle whether the page fit,
        /// put the column away and back on every frame, and the scene ran out of its 10 seconds (#4206).
        static let splitPageMin: CGFloat = 400
        /// The knowledge map's outline and detail column side by side from this width (#7251): any iPad in landscape,
        /// a 13-inch one upright beside its sidebar, a large iPhone turned; narrower, the outline alone.
        static let knowledgeMapTwoColumns: CGFloat = 540
        /// The iPad's list column, its one width (#4206).
        static let sidebar: CGFloat = 400
        static let sidebarMax: CGFloat = 480
        /// The narrowest column of a card grid (Machines' cards): two side by side in an iPad's detail column.
        static let cardColumn: CGFloat = 330
        /// The narrowest column of a chat's grid of subagent cards: two side by side from about 540 pt (iPad, Mac), as
        /// the web's from 560 px; one on a phone.
        static let subagentColumn: CGFloat = 260
        // CarPlay.
        /// The image of a CarPlay voice control state: Apple's guide asks for 150 pt (CarPlay sets its own size).
        static let carPlayVoiceImage: CGFloat = 150
        /// The file tree's column of an extension's code page beside its diffs or code (#2961).
        static let codeSideColumn: CGFloat = 300
        /// The width from which an extension's code page puts its side column beside the main one (#2961).
        static let codeSplitMin: CGFloat = 700
        // An extension's tiles (UI version 6's grid and carousel, #827).
        /// A grid's narrowest tile: two to a phone, more on wider screens.
        static let tileMin: CGFloat = 150
        /// A tile's least height.
        static let tileHeight: CGFloat = 96
        /// A carousel's narrow tile (scenes) and its usual one (rooms); a wide one (cameras) takes most of the width.
        static let tileNarrow: CGFloat = 140
        static let tileRegular: CGFloat = 200
        /// A wide carousel tile's share of the width it scrolls in (the next one's edge shows).
        static let tileWideShare: CGFloat = 0.8
        // An extension's gauge tile (UI version 9, #9337).
        static let gaugeRing: CGFloat = 96
        static let gaugeRingCompact: CGFloat = 80
        static let gaugeStroke: CGFloat = 9
        // An extension's graph canvas (UI version 10, #9727): one fixed node box and its gaps.
        static let graphNodeWidth: CGFloat = 224
        static let graphNodeHeight: CGFloat = 64
        static let graphNodeGap: CGFloat = 32
        static let graphPageShare: CGFloat = 0.7
        static let graphPageMinHeight: CGFloat = 320
        static let graphPageMaxHeight: CGFloat = 900
        static let graphPreviewHeight: CGFloat = 200
        static let graphFoldChip: CGFloat = 24
        static let graphPreviewMinScale: CGFloat = 0.01
        static let graphZoomStep: CGFloat = 1.25
        static let graphWheelZoomSensitivity: Double = 0.0025
        // An extension's 3D scene (UI version 11, #12544): page share, compact preview and pin collision box.
        static let scenePageShare: CGFloat = 0.62
        static let scenePageMinHeight: CGFloat = 280
        static let scenePageMaxHeight: CGFloat = 640
        static let scenePreviewHeight: CGFloat = 200
        /// What a page's scene adds to the model's own height: room for pins above it and the control bar below.
        static let scenePageChrome: CGFloat = 120
        static let sceneZoomStep: Double = 1.25
        static let sceneWheelZoomSensitivity: Double = 0.0025
        static let sceneOrbitSensitivity: Double = 0.45
        static let scenePinCompactWidth: CGFloat = 44
        static let scenePinCharWidth: CGFloat = 9
        static let scenePinBaseWidth: CGFloat = 40
        static let scenePinHeight: CGFloat = 32
        // A pin pill's estimated width, for keeping pins apart: the gap before its state, the widest its
        // text may grow, and the width of one wide (CJK) character.
        static let scenePinStateGap: CGFloat = 6
        static let scenePinTextMaxWidth: CGFloat = 180
        static let scenePinWideCharWidth: CGFloat = 13.5
        static let sceneLabelHeight: CGFloat = 22
        // An extension's interactive map (UI version 12): page share, compact preview, inline chat tool-view height,
        // companion list cap, route strokes and pin sizes.
        static let mapPageShare: CGFloat = 0.56
        static let mapPageMinHeight: CGFloat = 260
        static let mapPageMaxHeight: CGFloat = 520
        static let mapPreviewHeight: CGFloat = 200
        static let mapToolHeight: CGFloat = 220
        static let mapListMaxHeight: CGFloat = 220
        static let mapRouteStroke: CGFloat = 5
        static let mapRouteDimStroke: CGFloat = 3.5
        static let mapOriginDot: CGFloat = 12
        static let mapPinSize: CGFloat = 28
    }

    /// UI version 11's 3D `scene` component (#12544): calm architectural material defaults when a shape omits `color`,
    /// warm and cool lamp tints, and light/dark illumination levels (dark mode dims the room lighting, never the
    /// extension's `#RRGGBB` shape colors).
    enum Scene {
        static let defaultYaw: Double = 225
        static let defaultPitch: Double = 35
        static let defaultZoom: Double = 1
        static let minPitch: Double = 10
        static let maxPitch: Double = 90
        static let minZoom: Double = 0.5
        static let maxZoom: Double = 4.0
        /// A full-screen view taller than it is wide (an upright iPhone) opens at least this steep (#14335): a wide home
        /// fills the width either way, and looking down more lets the floor plan take more of the height.
        static let tallViewPitch: Double = 60
        /// A pin whose spot is on screen keeps its pill at least this far inside the viewport's edges (#14335).
        static let pinEdgeInset: CGFloat = 4
        /// The cutaway (#13036): the outer walls between the camera and the rooms are drawn this high (metres above
        /// their bottom); a wall counts as facing the camera from this cosine on, and as an outer wall when the ground
        /// this far past its camera-side face is not a floor (a prism no higher than the floor top); a shape within
        /// this of a lowered wall's faces stands in it and is cut too; one rising this much over the cut within reach
        /// of one, on the room's side, is drawn see-through at this opacity.
        static let cutawayHeight: Double = 0.6
        static let cutawayFacing: Double = 0.35
        static let cutawayProbe: Double = 0.3
        static let cutawayFloorTop: Double = 0.1
        static let cutawayBand: Double = 0.16
        static let cutawaySeeThroughAbove: Double = 0.6
        static let cutawayReach: Double = 0.9
        static let cutawaySeeThroughOpacity: CGFloat = 0.3
        /// Pins draw above the model, nearer ones over farther ones: a pin's layer is this less its depth.
        static let pinLayerBase: Double = 10_000
        static let fitMargin: Double = 1.1
        static let accessibilityYawStep: Double = 30
        static let accessibilityPitchStep: Double = 15
        static let matteHex: UInt32 = 0xECE7DE
        static let woodHex: UInt32 = 0xC9A97E
        static let tileHex: UInt32 = 0xE5E0D6
        static let metalHex: UInt32 = 0xB8BCC2
        static let screenHex: UInt32 = 0x1E2024
        static let glassHex: UInt32 = 0xB8D4E3
        static let warmLightHex: UInt32 = 0xFFDCAA
        static let coolLightHex: UInt32 = 0xE6F2FF
        static let sunLightHex: UInt32 = 0xFFF9EE
        static let skyLightHex: UInt32 = 0xDDE8F8
        static let lightAmbientIntensity: CGFloat = 380
        static let darkAmbientIntensity: CGFloat = 230
        static let lightKeyIntensity: CGFloat = 640
        static let darkKeyIntensity: CGFloat = 380
        static let lightFillIntensity: CGFloat = 90
        static let darkFillIntensity: CGFloat = 110
        // A lit lamp warms its corner of the room without washing light walls out to white.
        static let lampIntensity: CGFloat = 90
        static let lampAttenuationStart: CGFloat = 0.1
        static let lampAttenuationEnd: CGFloat = 2.4
        static let maxOmniLights: Int = 8
        static let glassOpacity: CGFloat = 0.42
        static var ground: Color { StyleSpec.current.usesGlass ? Theme.groupedCard : Theme.card }
    }

    /// UI version 12's declarative `map` component: region fitting padding, minimum span so single-point views keep
    /// street context, unselected route dimming, and adaptive card/sheet ground.
    enum Map {
        static let fitPadding: Double = 1.35
        static let minSpanDegrees: Double = 0.008
        static let minSpanMeters: Double = 700
        static let unselectedOpacity: Double = 0.42
        static let gridStepFraction: CGFloat = 0.25
        static let gridLineOpacity: Double = 0.08
        static let vectorRouteOpacity: Double = 0.92
        static var ground: Color { StyleSpec.current.usesGlass ? Theme.groupedCard : Theme.card }
    }

    /// The system's sidebar column on iPad (iOS 26, the glass styles), measured from its pixels: the split fills the
    /// status bar above the column with it (`SidebarTopFill`), which the column itself cannot draw into.
    static let systemSidebar = Color(light: 0xE6E9EE, dark: 0x191C20)

    /// The to-dos' calendar (#2029, docs/assistant/todos.md "Calendar"): its time axis, its columns and how a to-do's
    /// block is drawn on it.
    enum CalendarGrid {
        /// An hour on the time axis: a to-do's block (`Size.tapTarget` high) takes most of one, so two an hour apart
        /// never overlap.
        static let hour: CGFloat = 52
        /// The column of hours at the left of a day: "09:00", "11:00 AM" and the now line's "2:20 PM" fit.
        static let gutter: CGFloat = 60
        /// The same on a phone's week, whose seven columns need the room: the hours alone, and the moment's "14:20".
        static let gutterNarrow: CGFloat = 40
        /// A day of the month grid, at the least: its date and two lines.
        static let monthCell: CGFloat = 92
        /// The width from which the week shows each to-do's own block with its time and state (iPad, Mac); narrower,
        /// a block is a few characters and the to-dos due at one time share one.
        static let wide: CGFloat = 600
        /// A day's dots in the week strip: at most this many, the rest are counted in the day.
        static let dots = 3
        /// The ground of a to-do's block and chip: its state's color, this strong.
        static let tint: Double = 0.14
        /// A done to-do, faded.
        static let doneFade: Double = 0.5
        /// A day of the month grid that belongs to the month before or after.
        static let outsideFade: Double = 0.45
        /// A to-do being dragged, where it was (#2045): where it would land is drawn in full.
        static let liftedFade: Double = 0.3
        /// How far the axis scrolls each frame while a dragged to-do is held near its top or bottom edge.
        static let autoscrollStep: CGFloat = 4
        /// Lines a day of the month grid shows: its entries, or one fewer and "N more".
        static let monthLines = 3
        /// Where a new to-do's time snaps to when the empty axis is tapped, in minutes.
        static let snapMinutes = 30
        /// The calendar's ground under the page's controls: white on the grouped gray in the glass styles, as the list's
        /// rows are; the style's card otherwise.
        static var ground: Color { StyleSpec.current.usesGlass ? Theme.groupedCard : Theme.card }
        /// The hour lines and the columns' edges; the line under the all-day row is `lineStrong`.
        static let line = Color.primary.opacity(0.1)
        static let lineStrong = Color.primary.opacity(0.18)
    }

    /// A heat grid (`HeatGrid`, docs/design/DESIGN_SYSTEM.md 3.15): the token calendar's squares, a day each (a column a
    /// week) or an hour each (a row a day), in the theme color's steps from faint to full and no usage in the neutral
    /// fill. The squares keep their size at every text size; a wider screen shows more weeks.
    enum Heat {
        /// A day's square and the room between two: 19 weeks across an iPhone's card.
        static let cell: CGFloat = 13
        static let gap: CGFloat = 3
        static let radius: CGFloat = 3
        /// An hour's square on the last week's grid: as wide as 24 fit, this high, this far apart.
        static let hourHeight: CGFloat = 15
        static let hourGap: CGFloat = 2
        /// The fewest and the most weeks a calendar shows.
        static let minWeeks = 8
        static let maxWeeks = 53
        /// The theme color's strength at each step above none (`HeatLevels`, four steps).
        static let steps: [Double] = [0.28, 0.5, 0.75, 1]
        /// Today's outline and the picked square's.
        static let todayLine: CGFloat = 1.5
        static let pickedLine: CGFloat = 2
        /// A chart's columns other than the one its line reads (Token usage by project's days, #16530; the web's 55%).
        static let unpicked: Double = 0.55

        /// A square at step `level`: 0 is no usage, 1 to 4 the theme color's steps.
        static func fill(_ level: Int) -> Color {
            level <= 0 ? Theme.Fill.chip : Theme.accent.opacity(steps[min(level, steps.count) - 1])
        }
    }

    /// Motion: a state change snaps, a layout change glides. Nothing repeats except the Needs You dot, the work spinner,
    /// the persona's dot in the Persona tab's top bar (docs/apple/persona.md), the dot of a held mic (`DictationHold`) and,
    /// when it is turned on, the composer's glow while a reply is generating (`ComposerGlow`); those stop with Reduce Motion
    /// (the spinner is the system's).
    /// The Agents' owner-requested single capsule: filter choices and a fixed grouping menu.
    enum FilterCapsule {
        static let separatorHeight: CGFloat = 22
        static var selection: Color { Theme.accent.opacity(0.12) }
    }

    enum Motion {
        static let state = Animation.snappy(duration: 0.25)
        static let layout = Animation.smooth(duration: 0.3)
        /// Opening and closing things (a work card, a menu, a section): a state change, 0.25 s (it was the system's
        /// default snappy, 0.5 s, which trailed behind the finger).
        static let standard = state
        /// Task-draft dismissal uses the same six-second Undo window as the web.
        static let draftUndo: Duration = .seconds(6)
        /// A to-do ticked, put in Someday or deleted from the list (#10465): its "Done · Undo" toast stays this long, and
        /// the write waits as long on the device before it goes, so Undo sends nothing (`TodoOutbox`).
        static let todoUndo: Duration = .seconds(4)
        /// `todoUndo` while VoiceOver or Switch Control runs: the toast's Undo is out of their reach, so the rows offer it
        /// and the write waits this long (HIG-005).
        static let todoUndoAssistive: Duration = .seconds(20)
        /// How long an alert that follows another waits before asking: UIKit drops a presentation made while another is
        /// still going away, and SwiftUI took up to about 0.8 s to close one answered elsewhere (queued "already exists"
        /// questions, #6113).
        static let alertHandoff: Duration = .seconds(1)
        /// A block of content unfolding in place, its height growing (the Feishu pinned shelf's rows): slower than a
        /// state change so the eye can follow the rows coming in. `standard` read faster than Feishu's own (2026-11-04).
        static let expand = Animation.smooth(duration: 0.4)
        /// One way of a blink (the built-in persona's eyes): a blink is shut and open again, well under a quarter second.
        static let blink = Animation.easeInOut(duration: 0.07)
        /// Mochi's nominal drawing cadence; a periodic schedule does not request display-rate updates.
        static let personaFrameInterval: TimeInterval = 1 / 15
        static let needsYouBreathDuration: TimeInterval = 1
        static let needsYouBreathScale: CGFloat = 1.2
        static let needsYouRingOpacity = 0.6
        /// A compositor request, not a guaranteed upper bound on rendering frequency.
        static let breathMinimumFPS: Float = 10
        static let breathPreferredFPS: Float = 30
        /// One turn of the working arc (◐, a to-do being worked on, #10465), the To-dos list's only other loop: paused
        /// off screen, still with Reduce Motion.
        static let workingArcTurn: TimeInterval = 1
        /// What opens in the chat fades in over the space it takes; its height does not animate (`unfolding`). What
        /// closes there goes at once (#3005); `conceal` is for what fades away elsewhere (a menu, an inbox event).
        static let revealTime = 0.28
        static let concealTime = 0.2
        static let reveal = Animation.easeOut(duration: revealTime)
        static let conceal = Animation.easeIn(duration: concealTime)
        /// The system's Reduce Motion, outside a view (a view reads `\.accessibilityReduceMotion`): what would move or fade
        /// on its own is there, or gone, at once.
        @MainActor static var reduced: Bool {
            #if os(iOS) || os(visionOS)
            UIAccessibility.isReduceMotionEnabled
            #else
            false
            #endif
        }
        /// A drawer let go by the finger (the Persona tab's sidebar): no bounce, carrying on at the finger's speed.
        /// `velocity` is the share of the remaining way covered per second when it lets go (0 from a button).
        static func drawer(velocity: Double = 0) -> Animation {
            .interpolatingSpring(duration: 0.32, bounce: 0, initialVelocity: velocity)
        }
        /// Something the finger dragged aside going back to its place (a message swiped to quote it): a little bounce.
        static let springBack = Animation.spring(duration: 0.3, bounce: 0.25)
        /// iPhone Duo's tab bar (#9837, after the system's tab bar there): a glass lens leaves the chosen item for the new
        /// one, swelling as it goes (`lensSwellTime`, to `lensSwellScale`, `lensStretch` longer along the bar than across),
        /// glides there with a bounce (`lensGlide`, `lensGlideTime`) and shrinks and fades once it lands (`lensFadeTime`);
        /// with Reduce Motion the items only cross-fade (`tabFade`).
        static let lensGlide = Spring(duration: 0.45, bounce: 0.3)
        static let lensGlideTime: TimeInterval = 0.45
        static let lensSwellTime: TimeInterval = 0.15
        static let lensFadeTime: TimeInterval = 0.2
        static let lensSwellScale: CGFloat = 1.2
        static let lensStretch: CGFloat = 0.12
        static let tabFade = Animation.easeInOut(duration: 0.2)
        /// The stationary sidebar rises only this far as the page slides off it; no horizontal parallax or zoom.
        static let drawerLift = Theme.Space.s

        // The composer, after Telegram's input panel (docs/apple/ios.md, New shell, "Input motion"): only its timings and curves.
        /// The draft gaining or losing a line: short, so the field never trails behind the typing.
        static let composerGrow = Animation.easeInOut(duration: 0.1)
        /// The keyboard's own spring on iOS 26 (critically damped): the box opening as the keyboard rises moves with it.
        static let keyboard = Animation.interpolatingSpring(mass: 1, stiffness: 555.027, damping: 47.118)
        /// Send taking the round button's place and leaving it: it grows out of a point near the right edge.
        static let composerSwap = Animation.timingCurve(0.38, 0.7, 0.125, 1, duration: 0.4)
        /// What fades with the swap fades faster than it grows, and clears a slight blur.
        static let composerSwapFade = Animation.easeInOut(duration: 0.2)
        static let composerSwapBlur: CGFloat = 4
        /// Where send grows from: this small, not zero, so the scale animation has a size to start at.
        static let composerSwapFrom: CGFloat = 0.001
        /// An icon swapped inside the round button shrinks to this and fades as the next grows from it.
        static let composerSwapScale: CGFloat = 0.2
        /// Send pressed down shrinks, slowly, and comes back quicker when let go.
        static let composerPressScale: CGFloat = 0.75
        static let composerPress = Animation.easeOut(duration: 0.4)
        static let composerRelease = Animation.timingCurve(0.38, 0.7, 0.125, 1, duration: 0.25)
        /// A quote bar over the field slides in by its own height while the box grows with it; it fades in quicker than
        /// it fades out.
        static let composerAccessory = Animation.timingCurve(0.38, 0.7, 0.125, 1, duration: 0.4)
        static let composerAccessoryIn = Animation.easeInOut(duration: 0.15)
        static let composerAccessoryOut = Animation.easeInOut(duration: 0.2)
        /// Holding the mic: the circle under the finger springs up from a small one; the strip and the lock come with it.
        static let holdAppear = Animation.spring(duration: 0.5, bounce: 0.3)
        static let holdAppearScale: CGFloat = 0.2
        static let holdLeave = Animation.easeOut(duration: 0.18)
        /// The 「+」 panel (`AttachSheet`, a system sheet): switching tabs, the old page fades out over the new.
        static let attachTabFade = Animation.easeInOut(duration: 0.23)
        /// The tab bar's highlight sliding to the tab picked.
        static let attachLens = Animation.timingCurve(0.38, 0.7, 0.125, 1, duration: 0.4)
        /// The tab bar giving way to the caption and send (a cross-fade), and coming back.
        static let attachBarSwap = Animation.interpolatingSpring(mass: 1, stiffness: 555.027, damping: 47.118)
        /// A photo's mark: picked, it fills and bounces (0.9, 1.1, 1); let go, it empties.
        static let attachPick = Animation.easeOut(duration: 0.21)
        static let attachUnpick = Animation.easeIn(duration: 0.15)
        static let attachPickSqueeze: CGFloat = 0.9
        static let attachPickBounce: CGFloat = 1.1
        static let attachPickSqueezeTime: TimeInterval = 0.08
        static let attachPickBounceTime: TimeInterval = 0.13
        static let attachPickSettleTime: TimeInterval = 0.1

        /// The recording dot's blink, one way (it blinks while the finger holds, and stops with Reduce Motion).
        static let holdBlink = Animation.easeInOut(duration: 0.5)
        /// The composer turning into the dictation bar and back (`DictationBar`): one spring for the surface's shape, a
        /// slight bounce coming in and none going out; the buttons come in a beat later and leave quicker.
        static let dictationIn = Animation.spring(duration: 0.42, bounce: 0.16)
        static let dictationOut = Animation.spring(duration: 0.32, bounce: 0)
        static let dictationButtonsIn = Animation.spring(duration: 0.36, bounce: 0.2).delay(0.06)
        static let dictationButtonsOut = Animation.easeOut(duration: 0.14)
        /// How small the bar's buttons are as they come in and go.
        static let dictationButtonFrom: CGFloat = 0.6
    }

    /// The 「+」 panel (`AttachSheet`): a system sheet at half height (detents medium and large), a grid of recent photos
    /// and a bar at its foot (docs/apple/ios.md, New shell, "Attach panel").
    enum Attach {
        static let dim = 0.25
        /// The grid: three equal square columns a point apart; the camera's cell is one of them.
        static let columns = 3
        static let gridSpacing: CGFloat = 1
        /// A cell's selection mark: its size and inset from the cell's top right, the white ring round it.
        static let badge: CGFloat = 29
        static let badgeInset: CGFloat = 3
        static let badgeRing: CGFloat = 1.5
        static let badgeTarget: CGFloat = 45
        /// The tab bar: a pill at the sheet's foot, a tab's cell, its icon. A pill for five or more tabs is the screen
        /// less `barInset` each side.
        static let barInset: CGFloat = 22
        static let barHeight: CGFloat = 62
        static let barTab: CGFloat = 72
        static let barIcon: CGFloat = 30
        static let barLensInset: CGFloat = 3
        /// The pill's width for two, three and four tabs.
        static let barWidths: [CGFloat] = [160, 240, 300]
        static let thumbnailScale: CGFloat = 3
        /// How many photos a page of the grid asks the library for at a time.
        static let fetchLimit = 600
    }

    /// The image editor (`ImageEditor`, `ImageEdits`): Box, Pen and Crop over a picture on black, as Photos edits.
    nonisolated enum ImageEdit {
        /// A mark's line on screen: thin, medium, thick. Medium is the 3.5 pt Box and Pen drew with before there was a
        /// choice, and the default; the line is burned in at the same width relative to the picture as it is seen.
        static let lineWidths: [CGFloat] = [2, 3.5, 7]
        static let defaultLineWidth = 1
        /// Six inks, Apple's dark-mode system colors as fixed values (red, yellow, green, blue, white, black): a mark keeps
        /// its color in the picture whatever the appearance it is later seen in.
        static let inks: [(red: CGFloat, green: CGFloat, blue: CGFloat)] = [
            (1, 0.271, 0.227), (1, 0.839, 0.039), (0.188, 0.820, 0.345), (0.039, 0.518, 1), (1, 1, 1), (0, 0, 0),
        ]
        /// The ink swatch's ring: the one picked, the others.
        static let swatchRing: CGFloat = 3
        static let swatchRingIdle: CGFloat = 1
        static let swatchRingIdleOpacity = 0.4
        /// Room left round the picture while cropping, so the handles on its edges can be taken.
        static let cropInset: CGFloat = 28
        /// The crop frame's line, its corner marks (length and thickness), and how far round an edge a finger takes it.
        static let cropLine: CGFloat = 1
        static let cropCorner: CGFloat = 20
        static let cropCornerWidth: CGFloat = 3
        static let cropGrab: CGFloat = 22
        /// The smallest crop, a side on screen.
        static let cropMinimum: CGFloat = 64
        /// Outside the crop, dimmed; the thirds drawn inside it while it is dragged.
        static let cropDim = 0.6
        static let cropThirdsOpacity = 0.45
    }

    /// The composer's dictation bar (`DictationBar`, ChatGPT's): [✕] the wave [■] [↑] in the composer's capsule.
    enum DictationBar {
        /// A quiet column's dot, and the room from one column's centre to the next.
        static let dot: CGFloat = 3
        static let pitch: CGFloat = 6
        /// The tallest a column grows at full voice, as a share of the wave's height, and the curve from level to height
        /// (below 1: quiet speech already shows).
        static let tallest: CGFloat = 0.72
        static let levelCurve: CGFloat = 0.7
        /// A new column every this long: the row slides one pitch per column (about 66 pt a second).
        static let sampleInterval: TimeInterval = 0.09
        /// The wave's timeline while the bar is off: it stays (no view to build again on the tap) but does nothing.
        static let idleTick: TimeInterval = 3600
        /// The level follows the voice up quicker than it falls back (exponential smoothing time constants, seconds).
        static let attack: TimeInterval = 0.05
        static let release: TimeInterval = 0.18
        /// The microphone's loudness mapped onto 0…1 (decibels of full scale).
        static let floorDB: Float = -50
        static let ceilingDB: Float = -14
        /// The oldest columns fade toward the left over this many columns, down to `oldest` of their ink.
        static let fadeColumns = 7
        static let oldest = 0.25
        /// The newest column's ink as it comes in at the right edge (the wave's mask).
        static let newest = 0.35
        /// A column's ink, quiet and at full voice.
        static let quietInk = 0.5
        static let loudInk = 1.0
        /// After the bar closed, how long ■ and ↑ keep their order before following the mic's place again: past the bar's
        /// close (`Theme.Motion.dictationOut`, 0.32 s), so they never swap while they fade (#9619).
        static let reorderAfter: TimeInterval = 0.4
        /// After stop or send, how long the recognizer may take for its last words before the bar settles for what it has.
        static let finalWait: Duration = .milliseconds(1200)
        /// The bar's parts while it is off: drawn, but not seen. Fully transparent, SwiftUI skips drawing them, and their first
        /// drawing (the symbols, the circles, the wave) landed on the frames right after the tap.
        static let hiddenInk = 0.001
        /// The composer's row under the bar leaves the accessibility tree this long after the bar opens, past its spring.
        static let treeAfter: Duration = .milliseconds(450)
        /// The bar's haptic generator is prepared this long after the bar opens, past the first frames of its spring.
        static let warmAfter: Duration = .milliseconds(500)
        /// ■ / ↑: a spinner on the button once the last words have taken this long.
        static let spinnerAfter: Duration = .milliseconds(150)
        /// ↑: the longest the bar waits for the send to take the draft before it closes.
        static let sendWait: Duration = .milliseconds(160)
        /// After the bar closes, how long before the audio session is let go (past the close's spring).
        static let settle: Duration = .milliseconds(450)
        /// The columns' ink is drawn in this many steps of opacity (one fill each).
        static let inkSteps = 12
    }

    /// Hold a mic to dictate while held, slide left to drop it (`DictationHold`, `MicPress`, #9619): Telegram's thresholds
    /// for its voice button, but a longer wait before a touch counts as a hold, and no lock (a tap keeps it on).
    enum Hold {
        /// A touch held this long is a hold; let go sooner it is a tap (dictation on or off). Telegram's 0.19 s took a
        /// slow tap for a hold that ended, having heard nothing, as the finger lifted.
        static let delay: Duration = .milliseconds(300)
        /// While the finger is down, this far left drops what was heard.
        static let cancelDistance: CGFloat = 150
        /// Let go: a shorter slide counts, or a quick flick.
        static let releaseCancelDistance: CGFloat = 100
        static let flickSpeed: CGFloat = 400
        /// How far the label stays put before it follows the finger, at half its pace.
        static let labelDeadZone: CGFloat = 8
        static let labelFollow: CGFloat = 0.5
        static let labelFade: CGFloat = 10
        /// The circle under the finger, and how small it gets as the finger slides toward cancel.
        static let circle: CGFloat = 96
        static let circleSlide: CGFloat = 300
        static let circleSmallest: CGFloat = 0.4
    }

    /// Categories with no meaning beyond telling them apart (memory types).
    enum Hue {
        static let blue = Color(light: 0x2E6FD0, dark: 0x5B9BFF)
        static let orange = Color(light: 0xB35400, dark: 0xFF9F0A)
        static let green = Color(light: 0x1D7A4C, dark: 0x4CC38A)
        static let purple = Palette.purple
        /// System indigo.
        static let indigo = Color(light: 0x5856D6, dark: 0x5E5CE6)
        /// Health's heart (the health space, docs/assistant/health.md).
        static let pink = Color(light: 0xD62E5C, dark: 0xFF6B8B)
        /// A quiet mark: a list row's tile that is information, not a place to go.
        static let gray = Color(light: 0x8E8E93, dark: 0x636366)
        // The rest of an extension's named colors (UI version 6, `ExtensionUIStyle.color`, #827): marks only (icons,
        // badges' tints, tiles, chart series), never text.
        static let red = Color(light: 0xD1342C, dark: 0xFF6961)
        static let amber = Color(light: 0xB7791F, dark: 0xF5B342)
        static let yellow = Color(light: 0x9A7B00, dark: 0xFFD60A)
        static let teal = Color(light: 0x0F7C80, dark: 0x40C8C8)
        /// A blue-gray that reads as a mark on a card (the system gray is under 3:1 there in both modes).
        static let slate = Color(light: 0x5E6B7D, dark: 0x9AA7B8)
        /// A meeting's summary card (#6563): the four topic tints a group's cards share, by the group's place (the first
        /// group the first). They only tell groups apart: never a state's color (red, orange, green, yellow) or the AI's.
        static let minutesGroups: [Color] = [blue, teal, pink, slate]
    }

    /// A permission mode's tone where the Agents header shows it (#3719), by how much the agent does without asking.
    /// None is an agent state's color (blue, amber, green), which the same header uses for the state. Asks first is the
    /// quiet ink (`.secondary`), drawn by the header itself.
    enum ModeTone {
        /// Plan Only: it reads and plans, and acts only once the plan is approved.
        static let plans = Color(light: 0x0E7C86, dark: 0x4FD1C5)
        /// Auto-Edit and Auto: it acts by itself within limits.
        static var acts: Color { Hue.purple }
        /// Allow Everything: nothing is asked, in the warning the new-agent form gives it.
        static var allowsAll: Color { Theme.danger }
    }

    /// The user's own messages.
    static var userBubble: Color { spec.userBubble }
    static var userBubbleText: Color { spec.userBubbleText }
    /// Light bubbles (Feishu's) are flat; colored ones get a gentle gradient that only darkens toward the bottom
    /// (`Color.gradient` lightened the top, and the white words on it fell to 3:1).
    static var userBubbleFill: AnyShapeStyle {
        spec.deepensBubble ? AnyShapeStyle(deepening(userBubble)) : AnyShapeStyle(userBubble)
    }

    /// A color that darkens a little toward the bottom: depth without lightening what words sit on.
    static func deepening(_ c: Color) -> LinearGradient {
        LinearGradient(colors: [c, c.blended(with: .black, by: 0.12)], startPoint: .top, endPoint: .bottom)
    }

    /// The iPhone shell: neutral like ChatGPT's, in every style.
    enum Shell {
        /// The Assistant | Agents switch's selected segment, inside its glass capsule.
        static let segment = Color(light: 0xE6E6E6, dark: 0x3A3A3C)
        /// The dictation bar's quieter round buttons (cancel, stop): the switch's grey, under the composer's ink.
        static var roundMuted: Color { segment }
        /// The composer's field and its expanded card.
        static let composer = Color(light: 0xFFFFFF, dark: 0x1F1F21)
        static let composerStroke = Color(light: 0xE3E3E3, dark: 0x3A3A3C)
        /// Send / dictation's round button: ink on paper, like ChatGPT's (the style's accent tints nothing here).
        static let ink = Color(light: 0x0D0D0D, dark: 0xF2F2F2)
        static let inkText = Color(light: 0xFFFFFF, dark: 0x0D0D0D)
        /// The drawer behind the page.
        static let drawer = Color(light: 0xF9F9F9, dark: 0x171717)
        /// The composer: its field's corner when it is one line (a capsule) and when it has grown into a card.
        static let composerRadius: CGFloat = 32
        /// The colors that travel around the composer while a reply is generating (Glow While Replying, `ComposerGlow`):
        /// blue through violet to pink, each in its light and dark shade.
        static let composerGlow: [Color] = [Hue.blue, Hue.indigo, Hue.purple, Hue.pink]
        /// iPhone and iPad drafts grow through six lines before scrolling; keyboard height does not lower the cap.
        static let composerPhoneMaximumLines = 6
        /// The shared Mac/visionOS composer retains its existing eight-line cap.
        static let composerMaximumLines = 8
        /// Text and TextField round line heights differently; half a line distinguishes adjacent counts.
        static let composerLineTolerance: CGFloat = 0.5
        /// The switch's segments: width of each, and its height.
        static let segmentWidth: CGFloat = 72
        static let switchHeight: CGFloat = 44
        /// The largest text size the top bars grow with (their buttons, the face, the switch: up to 1.35 ×): larger, the
        /// bar reached into the search field pulled out under it on Agents, whose room is a line of the system's that
        /// does not grow with the text (`ShellNavigationClearance`). iOS 18's line is shorter (78 pt with the bar, #6730):
        /// there the bars stop a size earlier.
        static var topBarLargestTypeSize: DynamicTypeSize {
            if #available(iOS 26.0, *) { .xxxLarge } else { .xxLarge }
        }
        /// Room on Agents between the navigation bar and the search field for the top bar, which is taller (`AgentsHeader`):
        /// the height of the system's prompt line that made it before (`ShellNavigationClearance`), the same at every size.
        static let agentsRoom: CGFloat = 34
        /// The optional third segment must leave both side buttons reachable on narrow screens.
        static let compactSwitchLargestTypeSize: DynamicTypeSize = .large
        /// The switch as iPhone Duo's tab bar on the outer display (#4231: `ShellSwitch(axis: .vertical)` in its side
        /// column; the open Duo shows the system's tab bar): a bar of fixed
        /// items, as the system's tab bar it stands for, so its items keep the default text size (DESIGN_SYSTEM.md 6.2), each
        /// also shown large on a long press (the Large Content Viewer).
        static let switchTypeSize: DynamicTypeSize = .large
        /// A tab bar item there: the page's symbol in a `Size.well` circle over its name, a tap target wide and tall and
        /// more, so the names fit in every language (「メッセージ」 at the least scale).
        static let tabBarItemWidth: CGFloat = 56
        static let tabBarItemHeight: CGFloat = 56
        /// iPhone Duo's side column, where the system puts its bars down the side (both displays: an 84 pt safe-area
        /// inset): the strip at the screen's edge the system's bars keep clear of. The status bar is centred on the rest of
        /// the column (its clock 49 pt from the edge on both displays), and so is the side tab bar.
        static let sideColumnEdge: CGFloat = 14
        /// The side column from its foot up (`ShellSideRail`), one set of values on both displays, so the side tab bar stands
        /// at the same height when the Duo folds or unfolds: the column's foot (the safe area's) to the lowest button, then
        /// the face and the page's action a `sideRailSpacing` apart, then the tab bar a `sideTabBarGap` more above them. On
        /// the open Duo, where nothing is under the bar, their room stays empty (`ShellRailButtonRoom`), so the bar's foot is
        /// as far above the column's foot as on the outer display: the bar in the column's lower middle there too, as a tab
        /// bar down the side of the outer display stands (the owner's reference, #4231), not a small block at the foot.
        static let sideRailFoot: CGFloat = Space.l
        static let sideRailSpacing: CGFloat = Space.m
        static let sideTabBarGap: CGFloat = Space.s
        /// A tab bar item's count there (`TabBarCount`, the system's tab bar badge: `Size.badge` tall, its number
        /// `Font.labelHeavy`): ringed this wide in the page's color, apart from the chosen item's circle and the symbol, and
        /// centred this far inside the top trailing corner of the symbol's `Size.well`, so it rests on the circle's rim.
        static let tabBarBadgeRing: CGFloat = Size.lineMedium
        static let tabBarBadgeInset: CGFloat = Space.xxs
        /// The system's own items down that column (a page's bar items, a search at its foot), measured on iOS 27.2 on both
        /// displays (#9837): each a 48 pt glass, Back alone, 12 pt over the rest in one glass, 4 pt apart in it; at every text
        /// size. The first one's top is 174 pt below the safe area's on the outer display (its camera is over the status
        /// bar there), 124 pt on the inner one; a search stands at the foot, its top 38 pt above the safe area's foot.
        static let systemBarItem: CGFloat = 48
        static let systemBarItemGap: CGFloat = 4
        static let systemBarBackGap: CGFloat = 12
        static let systemBarTopOuter: CGFloat = 174
        static let systemBarTopInner: CGFloat = 124
        static let systemBarFootItemTop: CGFloat = 38
        /// The outer display is narrower than this (466 pt), the inner one wider (669 pt upright, 951 pt on its side).
        static let outerDisplayWidest: CGFloat = 600
    }

    /// The page behind lists and chats.
    static var pageBackground: Color { spec.page }
    /// Cards / panels in styles without glass.
    static var card: Color { spec.card }
    static var cardStroke: Color { spec.cardStroke }

    static let primaryText = Color(light: 0x1C1C1E, dark: 0xF2F2F7)
    static let codeBackground = Color(light: 0xF3F1EC, dark: 0x1C1D21)
    /// A reply's code block's title bar (#2984): its language and Copy, a shade off `codeBackground`.
    static let codeBar = Color(light: 0xE8E5DE, dark: 0x27282D)
    /// A reply's quote (#2984): its card under the accent bar, the accent faint.
    static var quoteBackground: Color { accent.opacity(0.08) }

    /// Syntax colors in the file viewer (docs/apple/ios.md「File viewer」), on `codeBackground` in every style (it does not
    /// change with the style, so neither do they): GitHub's palette, every one ≥ 4.5:1 on that background in light
    /// and dark (`SyntaxPaletteTests`). Words that are none of these stay `.primary`.
    enum Syntax {
        static let keyword = Color(light: 0xCF222E, dark: 0xFF7B72)
        /// Types, and variables in languages that mark them ($HOME, @name).
        static let type = Color(light: 0x953800, dark: 0xFFA657)
        static let string = Color(light: 0x0A3069, dark: 0xA5D6FF)
        /// Numbers and constants (true, nil), links in Markdown.
        static let number = Color(light: 0x0550AE, dark: 0x79C0FF)
        static let function = Color(light: 0x7547CF, dark: 0xD2A8FF)
        static let comment = Color(light: 0x57606A, dark: 0x8B949E)
        /// Markup tags and keys: HTML tags, JSON / YAML / TOML keys, CSS properties.
        static let tag = Color(light: 0x116329, dark: 0x7EE787)
        /// Decorators, annotations, preprocessor lines, HTML attribute names.
        static let attribute = Color(light: 0x7D4E00, dark: 0xD29922)
        static let all: [(String, Color)] = [("keyword", keyword), ("type", type), ("string", string), ("number", number),
                                             ("function", function), ("comment", comment), ("tag", tag), ("attribute", attribute)]
    }
    static let terminalBackground = Color(light: 0x16171B, dark: 0x0E0F12)
    static let terminalText = Color(light: 0xE6E3DA, dark: 0xE6E3DA)
    // Agent states (docs/protocol/agent-states.md): the dots. Text and soft backgrounds come from `AgentState.tone`
    // (StatusPalette.swift, shared with the watch and the Live Activity).
    static var needsYou: Color { StatusPalette.needsYou.dot }
    static var busy: Color { StatusPalette.working.dot }
    static var unread: Color { StatusPalette.unread.dot }
    static var inboxDot: Color { StatusPalette.needsYou.dot.opacity(0.65) }
}

extension AgentState {
    var label: String {
        switch self {
        case .needsYou: StateName.needsYou.text
        case .working: StateName.working.text
        case .unread: StateName.unread.text
        case .idle: StateName.seen.text
        case .offline: StateName.notRunning.text
        case .unknown: StateName.unknown.text
        }
    }

    var tone: StatusTone {
        switch self {
        case .needsYou: StatusPalette.needsYou
        case .working: StatusPalette.working
        case .unread: StatusPalette.unread
        case .idle, .unknown: StatusPalette.idle
        case .offline: StatusPalette.offline
        }
    }

    var color: Color { tone.dot }
}

extension SessionStatus {
    var label: String {
        switch self {
        case .working: StateName.working.text
        case .blocked: StateName.needsYou.text
        case .idle: String(localized: "Idle")
        case .done: StateName.done.text
        case .unknown: StateName.unknown.text
        case .offline: StateName.notRunning.text
        }
    }
}

nonisolated enum Fmt {
    static func relative(_ date: Date?, now: Date = .now) -> String {
        guard let date, date.isMeaningful else { return "" }
        let s = now.timeIntervalSince(date)
        if s < 45 { return String(localized: "Just now") }
        if s < 3600 { return String(localized: "\(max(1, Int(s / 60))) min ago") }
        let cal = Calendar.current
        if cal.isDateInToday(date) { return date.formatted(.dateTime.hour().minute()) }
        if cal.isDateInYesterday(date) { return String(localized: "Yesterday") }
        if s < 6 * 86400 { return date.formatted(.dateTime.weekday(.wide)) }
        return date.formatted(.dateTime.month().day())
    }

    /// Shortens ~/ paths for display.
    static func path(_ p: String, home: String?) -> String {
        if let home, !home.isEmpty, p.hasPrefix(home) { return "~" + p.dropFirst(home.count) }
        for prefix in ["/Users/", "/home/", "/data00/home/"] where p.hasPrefix(prefix) {
            let rest = p.dropFirst(prefix.count)
            if let slash = rest.firstIndex(of: "/") { return "~" + rest[slash...] }
            return "~"
        }
        return p
    }

    /// Markdown → plain one-liner for previews.
    private static let plainMemo = TextMemo<String>()

    static func plain(_ s: String) -> String {
        plainMemo(s) { s in
            var t = s
            for token in ["**", "__", "`", "### ", "## ", "# ", "> "] { t = t.replacingOccurrences(of: token, with: "") }
            return t
        }
    }

    /// Model names like "deepseek/v4.1-flash-chain" → "v4.1-flash-chain"; no model, including an older node's display placeholder, → nil.
    static func model(_ m: String?) -> String? {
        guard let m, !m.isEmpty, m != "默认" else { return nil } // i18n-ignore: legacy wire value, never displayed
        return m.split(separator: "/").last.map(String.init) ?? m
    }
}
