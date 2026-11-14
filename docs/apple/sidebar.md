# Sidebars

Rules for every sidebar row: the Persona sidebar on iPhone (drawer) and iPad (column), the Mac's sections column and
Persona list, and the web sidebar. Rows get their size from one set of tokens, follow each platform's own sidebar, and
are the same size in every language (#1647). The file name is under `docs/apple/`, but the web rules are here too so
the platforms stay in step.

## Row size follows the platform

| Platform | Row | Where it comes from |
| --- | --- | --- |
| iPhone, iPad | 44 pt minimum | The system sidebar's row (`List(.sidebar)`) and the smallest touch target in the HIG |
| Mac | 24 / 28 / 32 pt | The system's Sidebar icon size (System Settings › Appearance: small / medium / large; `NSTableViewDefaultSizeMode`), medium when unset |
| Web | 32 px, 44 px for touch | The desktop sidebar row (macOS medium plus the web's larger 13 px text); `touch-hit` / `min-h-11` for fingers |

BeautifulUI keeps its own `BUISidebarRow` (32 pt), which is part of that style's spec.

The Agents list, the sidebar column beside a chat on iPad and an open iPhone Duo, keeps the same 44 pt floor for every
row, heading and fold control a finger taps, in every grouping and display (#3753): `Theme.Size.tapTarget` is its least
row height, and a row that is one button is that button from edge to edge (`docs/apple/ios.md`, "Information
architecture"). Its rows are not sidebar rows in the sense of this file: they keep the list's own fonts and insets.

## Tokens, not the text's height

A row's height is a minimum plus padding above and below its text, never the height the text happens to have. Text in
different scripts, weights or fonts then sits in rows of the same size.

- **Apple**: `Theme.Sidebar` (`apple/Shared/Design/SidebarMetrics.swift`): `row` (least height), `rowPadding` (above
  and below the text), `glyph` and `glyphColumn` (a symbol's box and the column it sits in), `gap` (symbol to words),
  `mark` (a conversation's round mark) and `radius` (the selected or hovered shade). Apply them through `scaledFrame`,
  so rows still grow with the text size:

  ```swift
  HStack(spacing: Theme.Sidebar.gap) {
      ThemedIcon(Theme.Icon.inbox).font(Theme.Font.body)
          .scaledFrame(width: Theme.Sidebar.glyph, height: Theme.Sidebar.glyph)
          .scaledFrame(width: Theme.Sidebar.glyphColumn)
      Text(title).font(Theme.Font.body).lineLimit(1)
      Spacer(minLength: 0)
  }
  .padding(.horizontal, Theme.Space.m)
  .padding(.vertical, Theme.Sidebar.rowPadding)
  .scaledFrame(minHeight: Theme.Sidebar.row)
  ```

- **Web**: `--sidebar-row` and `--sidebar-row-pad` (`web/src/styles.css`) through the `sidebar-row` utility
  (min-height, padding and a fixed 18 px line). A row adds no `py-*` of its own.

## Type

- Text uses the `Theme.Font` levels: on iOS they are text styles (`body`, `subheadline`, …), so they follow Dynamic
  Type; on the Mac they follow the app's text size. No point sizes (`.system(size:)`) in sidebar code.
- Row titles are `Theme.Font.body` on one line (`lineLimit(1)`); secondary lines and counts are `caption` / `secondary`.
- Group headings use sentence case ("Persona chats", "Recent"), never uppercase or letter spacing: Chinese and Japanese
  have no case, so all caps made English headings look heavier than the same heading in Chinese.

## Every language the same

- No language-specific branches for size: English, Chinese and Japanese use the same rows, paddings and fonts. If a
  language looks wrong, fix the token or the layout for all of them.
- The row must fit a line of CJK text without clipping: PingFang's line is taller than SF's at the same size, and the
  row's padding is what keeps its glyphs inside. Check zh-Hans screenshots, light and dark, whenever a row changes.

## Icons

Symbols in rows are `Theme.Icon` meanings drawn with `ThemedIcon` (or `Image(systemName:)` sized into `glyph` where a
wide symbol must scale down), in the style's icon language (`docs/apple/icons.md`). Every row's symbol sits in the same
`glyphColumn`, so the words line up down the column.

## Adding a row

1. Use `Theme.Sidebar` (Apple) or `sidebar-row` (web) as above; never a number, `Theme.Size.avatarL` / `box50` /
   `tapTarget`, or a row-specific radius.
2. A new Swift file that draws sidebar rows goes into `SidebarMetricsTests.files`.
3. Screenshot it in en and zh-Hans, light and dark (iPhone, iPad, Mac, web as it applies) and look at it.

## The Sidebar layout's headings

The Sidebar layout (#6332, `docs/apple/ios.md` "Sidebar layout (#6332)") heads its sections with a fold button: the name in
`captionStrong`, a chevron, what the folded section still counts, and the section's own buttons (Persona Chats' ✎). The
button is a sidebar row by size (`Theme.Sidebar.rowPadding`, `Theme.Sidebar.row`), so a finger always finds it and the
words sit as the rows' do; the fold is remembered on the device (`sidebar.fold.places`, `sidebar.fold.chats`).

## Pinned directions

The leader conversations (`assistant.leaders`) are the group headed **Pinned** (zh-Hans 常驻, ja 常駐; the catalog key
is still `Directions`, the owner's renaming of 2026-11-11). With the hub's direction tree (#4352,
`GET /api/v1/assistant/directions/tree`, `AppModel.directionTree`) each row folds open; without it (an older hub, or
before the tree is read) the rows are the plain conversation rows they were, and with no leaders the sidebar has no
Pinned group at all.

- **Row**: the leader conversation's own row as #4331 drew it (tap, long-press menu, swipe) with what its direction has
  going, read from the same tree (`DirectionTree.leader(_:)`): "N need you" in words, and one activity mark,
  `RowStateMark`'s spinner, while its own turn runs or anything under it does (the tree's `running`: an Agent of its
  to-dos, or one under it, at work, whatever the to-do's state; #6230, #6626). Its own Needs You / Unread dot wins over
  what runs under it, and VoiceOver says Working for the spinner either way. No "N working" beside the spinner: one
  meaning, one expression (#6626). A chevron button sits at its end
  (`safeAreaInset`). The button folds the row; its label is the direction's name with its "N need you", its value Expanded /
  Collapsed; it is at least `Theme.Size.tapTarget` wide and
  `Theme.Sidebar.row` high. The row's selected fill and Mac hover shade span the conversation row and the button
  together (`row(_:named:drawsShade:)` leaves its own off), as wide as every other row's. Folded by default; each device remembers each direction in
  `assistant.direction.open.<leader id>` (`@StoredValue`).
- **Open**: one row per open to-do of the direction, in the tree's order, set in as Auto Chats' sources are: its title
  and its state word in its tone (等你 Needs You's, 在做 Working's, 卡住 and 待验收 Caution's; `DirectionTaskStateWord`).
  A tap opens the to-do's card (`TodoDetailView` in a sheet, on iPhone, iPad and Mac alike). The sheet hangs on the
  direction's own row, not the to-do's: accepting or closing the to-do drops it from the tree, and folding the direction
  hides it, without closing the card. A to-do with Agents has its
  own chevron (folded by default, not remembered) that lists them a level in: the live state dot (the tree's state for
  a sub-Agent the app does not list), the title, the state's name in its tone (not the dot's color alone); a tap
  closes the drawer and opens the session (`openTodoAgentChat`). An Agent with sub-Agents has its own chevron too.
  These chevrons say what they fold ("Agents of <to-do>", "Subagents of <Agent>"), not the bare title the row's own
  button says, with Expanded / Collapsed.
- **Secretary** (书记官): no row in the sidebar (the main conversation's row stands for it, #4331); its to-dos are in the
  To-dos and Agents pages' Pinned grouping.
- **Code**: `AssistantSidebarDirections.swift` (`AssistantSidebarDirection`);
  `AssistantSidebar.swift` only places them where the direction rows were. Tests: `DirectionTreeTests`.

## Checks

| Test | Guards |
| --- | --- |
| `apple/PersonaTests/SidebarMetricsTests.swift` | Rows' `minHeight` is only `Theme.Sidebar.row` (headings: `HeadingMenu.minHeight`); each has `Theme.Sidebar.rowPadding`; no literal heights, vertical paddings or point sizes; the iOS row is 44 pt; a line of body text in SF and PingFang fits the row |
| Retired native Mac sidebar tests | Historical only: the Mac row was 24 / 28 / 32 pt for the system's small / medium / large Sidebar icon size; the target and its tests were removed |
| `web/src/views/SidebarRowTokens.test.ts` | `--sidebar-row` is 32 px; every sidebar row uses `sidebar-row` with no `py-*`; no heading is uppercased or letter-spaced |
| `apple/PersonaTests/TokenRatchetTests.swift` | Literal paddings, frames and radii in views only go down |
