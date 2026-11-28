# Sidebar navigation: places, tasks and the drawer (iPhone shell and iPad sidebar)

Status: option 2 chosen and implemented for the iPhone shell, with the one-button amendment below (#7799). Option 3 adopted
for the Sidebar layout's page (iPad, the open iPhone Duo and the phone's drawer in that layout, #8359, section 6). Baseline:
`origin/main` @ `65cbb6b95` (2026-11-13). Audience: everyone who uses the Assistant side of the app; nothing here is Agents-only.

User report: in the iOS app, choosing a page in the sidebar closes the sidebar while a sheet, a cover or a pushed page comes
in at the same moment, and the path the user was on is lost: they cannot get back to where they were, or going back lands
somewhere unexpected.

## 1. How it works today

The iPhone shell (`docs/apple/shell.md`) is `ShellView`: the drawer (`AssistantDrawer`, drawn by `AssistantDrawerLayout`) slides
the strip of two or three pages aside (Assistant, Agents, optional Messages). Each page has its own `NavigationStack`:
Assistant's `path` plus seven `show…` flags in `AssistantView` (`apple/Persona/Views/Assistant/AssistantView.swift:19`, `:26-35`),
Agents' `path` in `ChatsView` (`apple/Persona/Views/Chats/ChatsView.swift:235`). The drawer's places are pushed onto Assistant's
stack. On the iPad the same `AssistantSidebar` is the column of a `NavigationSplitView` (`AssistantView.swift:94-106`) and its
places go through the same `show(_:)` (`padPlaces`, `AssistantView.swift:111-138`).

A drawer row on the iPhone runs `ShellView.go(_:)` (`apple/Persona/Views/Shell/ShellView.swift:127-131`): it sets
`selectedTab = .assistant` (the strip slides there, animated, `ShellState.select`, `ShellState.swift:118-131`), records a one-shot
request (`ShellState.show`, `ShellState.swift:91-93`) and starts the drawer's closing spring (`AssistantDrawer.settle`,
`AssistantSidebar.swift:117-131`), all in one turn. `AssistantView` takes the request on its next update
(`AssistantView.swift:279-283`) and runs `show(_:)` (`AssistantView.swift:378-392`), which first calls `backToRoot()`
(`AssistantView.swift:412-425`) and then pushes.

### Entry → presentation → what happens to the path

| Entry | Code | Presentation | Path |
| --- | --- | --- | --- |
| A conversation row | `ShellView.swift:94-100` → `AssistantView.swift:290-301` | Assistant's root shows that conversation; strip slides if Agents was in front | `backToRoot()`: a place pushed on Assistant, with everything under it, is dropped |
| New Chat | `go(.newConversation)` | Assistant's root, new conversation | dropped, as above |
| Inbox, To-dos, Machines, extension pages | `go` → `show` → `path.append(ShellRoute…)` (`apple/Persona/Views/Shell/ShellHome.swift:429-442`) | `NavigationStack` push on Assistant | the place in front and its sub-pages are dropped first; back returns to the conversation, also when it was opened from Agents |
| Scheduled Tasks, Knowledge, Profile, Recordings | `go` → `show` → `show… = true` (`AssistantView.swift:247-257`) | push by `navigationDestination(isPresented:)` | dropped first, as above; these pushes are not in `path` at all |
| Health | `go(.health)` → `enterHealth()` → `backToRoot(); present(.health)` in `AssistantView.swift` | the Health home (#13273), with its conversation list; a page root in the embedded Sidebar layout | returned to the root before presentation |
| Settings | `ShellView.swift:105-108` | `.sheet` on the shell (`ShellView.swift:30-36`) | the drawer closes under the rising sheet; closing Settings lands on the page, not the drawer it came from |
| Archived, Auto conversations | `AssistantSidebar.swift:904-915`, presented from the drawer's own hosting view | sheet over the drawer | picking a row dismisses the sheet, closes the drawer and resets the stack in the same turn |
| Search (iPhone Duo open) | `AssistantSidebar.swift:917-923` | `fullScreenCover` | as Archived |
| Assistant / Agents switch | `apple/Persona/DesignSystem/ShellComponents.swift:118-131`, `AppModel.selectHomeTab` (`apple/Persona/App/AppModel.swift:209-217`) | strip pages | choosing Agents returns to its list by design (#5708); Assistant's stack stays |
| Opening the drawer at all | `ShellView.swift:25` (`enabled: shell.atRoot`), `DrawerPan.pushed` | — | place roots show ☰ in place of Back; sub-pages yield sideways drags to the system's Back gesture; return from a sub-page to its root, then open the drawer |
| iPad sidebar column | `AssistantView.swift:111-138` | same `show(_:)`, push in the detail stack | switching place drops the previous place's stack; no drawer animation (the column stays) |

Where navigation state lives: Assistant's `path` and flags (`@State` in `AssistantView`), Agents' `path` (`@State` in `ChatsView`),
the request (`ShellState.request`, one-shot). Nothing remembers a place's stack once `backToRoot()` has run: that is why it is lost.

### Conflict scenarios

1. **Close vs push vs slide.** On the Agents list, ☰ → Inbox: the strip slides to Assistant, the drawer's spring closes and the
   stack pushes Inbox, three animations in the same ~0.4 s. The page card the user watches changes twice while it moves.
2. **Close vs sheet.** ☰ → Settings: `drawer.close()` and the sheet's presentation start together; after Done the user is on the
   page, not in the sidebar where they were.
3. **A sheet's result vs close.** ☰ → Archived (or Auto conversations, or Search) → a conversation: the sheet's dismissal, the
   drawer's close and `backToRoot()`'s pop all run in one turn.
4. **A place replaces a place.** Machines → a machine's page: the drawer cannot open there, so the user backs out to the
   conversation, opens the drawer, picks Knowledge; picking Machines again starts at the list. The same loss without backing out:
   Inbox → an Agent chat routes to Agents (`selectedTab = .chats`) with Inbox still pushed on Assistant; ☰ on Agents → Knowledge
   drops Inbox and the chat under it.
5. **Back goes elsewhere.** A place opened from Agents is pushed on Assistant; back returns to the Assistant conversation, while
   the user came from Agents. (Places belong to Assistant: `docs/apple/shell.md`.)
6. **iPad column vs iPhone drawer.** The column never closes, so 1–3 do not happen there, but 4 does: switching places in the
   column drops the previous place's stack through the same `backToRoot()`.

## 2. What other apps and the HIG do

- **HIG, Sidebars / Tab bars:** a sidebar or tab bar switches between top-level areas; each area keeps its own navigation state,
  and choosing the area already in front returns it to its root. The sidebar's selection changes the content; it does not stack
  areas on top of one another. **HIG, Modality:** a modal view is for a self-contained task; when it is dismissed, people are
  back exactly where they started, and a modal should not start while another transition is still moving.
- **Apple Mail / Files (iPad):** choosing a mailbox or a location in the sidebar replaces the detail; each keeps its scroll and
  selection, the sidebar stays. In compact width the sidebar is the stack's root; back returns to it.
- **Slack (iPhone):** Home, DMs and Activity each keep their stack; switching keeps it, tapping the one in front pops to its root.
- **ChatGPT, Claude (iPhone):** the sidebar is a drawer; choosing a place swaps the content under the drawer and only then the
  drawer closes (one motion). Settings and account open as a sheet over the open sidebar; closing it returns to the sidebar.

Two kinds of entry follow from these:

- **Places** (where the user goes): a conversation, New Chat, Inbox, To-dos, Scheduled Tasks, Knowledge, Health, Machines,
  Recordings, extension pages, Profile, and the Assistant / Agents pages themselves.
- **Tasks** (something done and dismissed): Settings, Archived, Auto conversations, Search, New Main Chat.

## 3. Options

### Option 1 — sequence the animations only

Keep every presentation as it is; wait for the drawer to finish closing before pushing, sliding or presenting.

- Pros: smallest change; fixes scenarios 1–3.
- Cons: a slower response (the push starts ~0.4 s after the tap); 4–6 stay: paths are still dropped, and the drawer is still
  unreachable from a pushed page.
- Risk: low, but it does not answer the report (the lost path).

### Option 2 — places keep their stacks; tasks stay over the drawer (recommended)

- **Places:** under the open drawer the page switches without animation (strip, `backToRoot()`, push), then the drawer closes:
  one motion. Each place keeps its own stack: leaving it (for another place, a conversation, or the Assistant root) saves its
  `NavigationPath`; choosing it again restores it. Choosing the place already in front returns it to its root (the tab-bar rule).
  Places stay pushed over the conversation, so back from a place's root still returns to the conversation, as today.
  All drawer places push by value (`ShellRoute`), so their sub-pages pushed by value are part of the saved path.
- **One button on each side (#7799, superseding the original “drawer from any depth” bullet):** a place's root hides
  the system Back button and shows ☰ as its only leading control; a sub-page has Back and no ☰. At the root, returning
  to the conversation means choosing a conversation in the drawer (or the edge-swipe below). A place still keeps its
  stack while the user is elsewhere. The top-trailing side also has at most one control on iPhone: Inbox's Select,
  Knowledge's tag filter and map, an Inbox item's Done, a Lark chat's Pins and summary, and Browser Sign-ins' Select
  join their respective ⋯ menus. Multi-hub Inbox's hub picker joins that menu too, and only on iPhone does selection's
  Select All move to the bottom action bar; outside the iPhone shell it stays at the original top leading, beside Back
  or the hub picker (`IPadInboxSelectionTests`). These menu layouts change only in the iPhone shell; the iPad and Mac
  keep their original ones.
- **Edge gestures:** the app's existing `UINavigationController` pop-gesture delegate still permits Back with its
  button hidden, and `DrawerPan.pushed` yields a sub-page's drag to that gesture. Thus an edge-swipe on a place's root
  still returns to the conversation; it does not open the drawer. On a sub-page it goes back one level. A sideways
  drag away from the screen's leading edge at a place's root opens the drawer. No two edge gestures compete for the
  same drag. This behavior is covered by `ShellPagerTests.testPushedPagesSwipeBack`.
- **Tasks:** shown over the open drawer, which stays open; closing the task returns to the drawer as it was. A task that
  ends in a place (a conversation picked in Archived, Auto conversations or Search) waits until its sheet is gone, then
  switches as a place does.
- **iPhone:** as above. **iPad:** the column never closes and Settings is already a sheet; places keep their stacks through the
  same code (`AssistantView.show`).
- Pros: fixes 1–4 and 6 with the existing routes (`ShellRoute`, `ShellDestinations`, `ShellState.request`) and no new shell
  structure; the place roots show only ☰ instead of the previous Back-and-☰ pair, and sub-pages show only Back.
  The five existing trailing-action pairs on these pages are consolidated into ⋯ menus on iPhone.
- Cons: scenario 5 stays by design (places are Assistant's); only pages pushed by value come back (pages pushed with
  `NavigationLink { … }` or by flags inside a place come back to the nearest value page); scroll positions are rebuilt.
- Risks: a saved path can point at something gone (a removed machine): those pages show their own empty state, and an
  assistant switch forgets every saved path (`assistantSwitches`). The one-leading-control and trailing-menu changes
  are visible changes, checked in light/dark screenshots.

### Option 3 — every place is a root with its own live stack (ChatGPT / Claude model)

The Assistant page shows one place at a time as its root, each in its own `NavigationStack` kept alive; there is no back from a
place to the conversation; ☰ is at every root.

- Pros: the cleanest model; keeps scroll positions and pages pushed any way; fixes 1–6.
- Cons: a rebuild of the shell's Assistant page (its top bar, the switch, the pager, the probes) and of every back flow; about ten
  UI suites (`ShellSwitchTests`, `ShellInboxAgentTests`, `ShellRaceTests`…) assert today's back to the conversation; a stack kept
  alive per place costs memory; the iPad's split detail needs the same change.
- Risk: high for a bug fix; better as its own proposal if the product wants places as roots.

### Comparison

| | Option 1 | Option 2 | Option 3 |
| --- | --- | --- | --- |
| Place: presentation | push, after the close | push under the open drawer, no animation | root swap |
| Task: presentation | sheet, after the close | sheet over the open drawer | sheet over the open drawer |
| When the drawer closes | before anything moves | after the place is in front | after the place is in front |
| Each place keeps its stack | no | yes (value pushes) | yes (everything) |
| Back from a place | conversation | conversation | none (☰) |
| After closing a task | the page | the drawer, as it was | the drawer, as it was |
| Drawer from a pushed page | no | Back to the root, then ☰; no second leading button | ☰ at every root |
| iPad column | unchanged | stacks kept | stacks kept, split rebuilt |
| Size of change | small | medium | large |

## 4. Decision

**Option 2.** Places switch under the open drawer and keep their own stacks; tasks open over the drawer and return to it.

Implementation:

- `ShellPlaces` (`apple/Persona/Views/Shell/ShellPlaces.swift`): which place is in front and the saved path of each place left.
  Pure value type with unit tests (`PersonaTests/ShellPlacesTests`).
- `AssistantView.show(_:)` saves the place in front, then restores or pushes the new one; `backToRoot()` happens without animation
  while the drawer is out; the drawer closes after the switch. Scheduled Tasks, Knowledge, Profile and Recordings push by value
  (`ShellRoute`) like Inbox, so their pushes are part of the path.
- `ShellView`: places snap the strip without animation and leave closing to the page; Settings opens over the open drawer.
- `AssistantSidebar`: Archived, Auto conversations and Search open the picked conversation from the sheet's `onDismiss`.
- `ShellPlaceRoot`: at a place's root on the iPhone shell, hides Back and supplies ☰; it is not applied to sub-pages.
  Trailing actions are consolidated in the five affected pages, without changing iPad or Mac toolbar branches.
- The iPad's column sends every place through `show(_:)` too (Knowledge, Scheduled Tasks, Profile and Health used to set flags).
- `DrawerSwipeTests.testDrawerSettingsReturnsToTheDrawerThenPagesSwitch` replaces the test that expected Settings to close the drawer.

## 5. Acceptance

| Item | Check |
| --- | --- |
| One leading button and return to the conversation | ☰ → Machines → a machine → Back → Machines → ☰ → a conversation row: root and child each have one leading control, then the drawer returns to the conversation (`ShellPlacesUITests.testAPlaceKeepsItsStack`) |
| Choosing the place in front pops it | `ShellPlacesTests.choosingThePlaceInFrontGoesToItsRoot` covers the re-pick-to-root rule |
| A place keeps its stack | `ShellPlacesTests` covers save on leave, restore on return, re-pick and assistant switch; the iPad flow checks it in the UI |
| One leading button | A place root shows ☰ and no Back; a machine sub-page shows Back and no ☰ (same test) |
| One trailing action | Inbox, Knowledge, an Inbox item, a Lark chat and Browser Sign-ins have one ⋯; their previous actions remain in that menu on iPhone |
| Root edge-swipe | From a place root, a slow drag from the leading screen edge returns to the conversation and does not open the drawer (`ShellPagerTests.testPushedPagesSwipeBack`) |
| A task returns to the drawer | ☰ → Settings: the drawer stays open under the sheet; Done: the drawer is open with its rows (`testSettingsReturnsToTheDrawer`) |
| Close and modal never overlap | the drawer's closing starts only after the place is in front or the task's sheet is gone, read from `ShellTrace` (`testArchivedPickClosesAfterTheSheet`, `testAPlaceSwitchesBeforeTheDrawerCloses`) |
| Saved paths are the place's | unit tests: save on leave, restore on return, re-pick pops, assistant switch forgets (`ShellPlacesTests`) |
| iPad column | Machines → a machine → Knowledge → Machines in the column: the machine's page (`testAPlaceKeepsItsStackBesideTheIPadColumn`) |
| Nothing else moved | `ShellTests`, `ShellSwitchTests`, `AssistantArchiveTests`, `DrawerSwipeTests` stay green |

## 6. Option 3 for the Sidebar layout's page (#8359)

Option 2 kept the Sidebar layout's page (`ChatsView` under `\.unifiedSidebar`, docs/apple/ios.md "Sidebar layout (#6332)")
as one stack with the main conversation at its bottom, everything the sidebar opened pushed over it. On the iPad and the open
Duo that read as a stack of top-level areas: from the conversation every pick pushed in from the right, back to the
conversation was a pop, page to page was an instant swap, the hidden Back still answered the edge swipe (back to the
conversation, another sidebar item), and on the open Duo upright the push ran together with the column sliding away. The HIG
(Sidebars; Tab bars, "preserve the current navigation state within each section") and Mail, Files, Notes and Music on the iPad
switch the detail without a push when a sidebar item is chosen; only drilling down inside an area pushes.

**Switching area replaces the page's root; only drilling down pushes.**

- **Roots**: the main conversation, each page (Inbox, To-dos, Scheduled Tasks, Knowledge, Recordings, Machines, extension
  pages, Profile), Health's page, Messages, an Agent's chat, and the other pages the Agents list opens (a space, Personal
  Agents, a machine, a to-do). Picking one replaces the root at once: no push or pop, no Back, nothing under it for an edge
  swipe or VoiceOver's escape to reach.
- **Drill-downs**: what a page opens inside itself (an Inbox item, a machine's details, a recording, a conversation an item
  links to) is pushed on the root, with Back, as before.
- **The top-leading button follows how the page was reached, not what it is (#8603)**: a root has the button that opens the
  sidebar (the main conversation's, `Theme.Icon.sidebar`; the persona's face in a phone's drawer; under side bars the side
  column's first item, where a sub-page has Back, #11636), never a ‹; a drill-down has ‹, which goes back one page. The same Agent's chat is a root when picked in
  the sidebar and a drill-down when opened from a waiting Agent on the main conversation or in Inbox. A place's root draws
  that button itself (`ListButtonItem`): the system's toggle was not drawn again after a drill-down on it was popped (the
  open Duo upright, Inbox → an Agent's chat → Back), which left the root with no way back to the sidebar. The system's
  toggle is removed on the roots' column, under the Duo's side bars too (#11636: it was missing on an Agent's chat, whose
  bar has no title row to draw it in, and on a place after a pop); so a sub-page
  has Back alone and, with the column laid over the page (the open Duo upright), the sidebar is reached from a root.
- **Each root keeps its drill-down** while the user is elsewhere (`SidebarPageRoots`, a pure value with unit tests,
  `PersonaTests/SidebarPageRootsTests`); picking the root in front goes back to it (the tab-bar rule); another conversation
  picked while the conversation is in front starts at its root without a pop. Another assistant or hub forgets every root.
- **The shaded row follows the root**, also while drilled in (`UnifiedSidebar.atRoot`, `shownPlace`, `shownAgent`): one
  row of the whole column at a time, an Agent's in every grouping, all in one shade (`SidebarSelectedShade`, #11032).
- **Motion**: a switch is never animated (the open Duo upright: the column sliding away is the only motion); a drill-down's
  push and a re-pick's pop are.
- **Structure**: `AssistantView` (embedded) stays the stack's root, so the sidebar's requests and routes keep landing there,
  and draws the root picked (`sidebarRootPage`); the split's path holds only drill-downs (`SplitNavigation(rootsSwap:)`).
  Only drill-down paths are kept, not each root's live views: scroll positions are rebuilt, the conversation comes back from
  its cache.
- **Unchanged**: the iPhone shell (option 2 above), and the phone's drawer in the Sidebar layout behaves as before (the page
  changes under the drawer, which then closes; the face where a back would be; a drag to the right at a root opens the drawer).
- **The Separate layout's Agents tab** (#8603): its list picks the page's root too (an Agent's chat, a space…), replaced
  with no push; nothing picked, "Choose an Agent"; picking the Agents tab again goes back to that. Its page writes
  `probe.sidebar-page-agents`.
- **Checks**: `SidebarRootsUITests` reads the app's `probe.sidebar-page` (root, path, and how many animated UIKit navigation
  transitions ran): from the conversation Recordings and an Agent replace the root with no transition and no Back; Machines →
  a machine pushes with Back; Machines keeps that page while the conversation is in front; Machines in front picked again goes
  to its root; an edge swipe at a root stays on it; an Agent's chat picked in the sidebar has the sidebar's button and no ‹,
  pushed from a page it has ‹ (`testTheLeadingButtonFollowsHowThePageWasReached`); in the Separate layout's Agents tab an
  Agent and then another replace the root with no transition (`testTheAgentsTabSwitchesItsRootWithoutAPush`). A task's card in the
  conversation pushes its Agent's chat with Back, the root staying the conversation (#9971, `SecondaryPageBackTests`). So do the
  other ways into an Agent from inside a page (#12006, `AppModel.openInPage`, `PageAgentPushTests`): a Brief card's Open, a
  to-do's Agent rows and the Agent it is handed to, a knowledge project's Agents, a meeting's hand-off, a card's session link and
  a held push. The conversation pushes on its path (`pushInboxChat`); a page that was itself pushed pushes the chat from its own
  destination (`pushedAgentChat`), since some are pushed by view and a value would land under them. A page in a sheet closes and
  the Agent opens as before; the sidebar's own rows, notifications and Open in Agents still pick a root. Run on the iPad, the
  open Duo upright and on its side, and an iPhone. What the conversation pushed stays with it (#13156): another Agent picked in
  the sidebar is the page's root with nothing over it, and Main Chat picked again comes back with the pushed page and its Back
  (`testAnAgentPushedOverTheConversationStaysWithIt`). A task's page opened from the conversation (a "Sent to" line, a card, a
  dispatch link) is a value on that path too (`TaskDetailRoute`, `testATaskPageOpenedFromTheConversationStaysWithIt`): pushed by
  view, as `detailPresentation` does elsewhere in regular width, it stayed in front of every root the sidebar picked.

**Android** (#9667). A regular window — the sidebar column beside the page, or over it — follows this section. `SidebarPageRoots`
(`android/app/src/main/java/persona/android/navigation/SidebarPageRoots.kt`) is the pure model, with the same steps as `SidebarPageRoots.swift`: a place
replaces the Assistant stack's root and does not animate; a drill-down pushes; each root keeps its path; picking the root in
front pops; the shaded row follows the root (`AssistantSidebarShade`). An Agent root shades its exact Directions row with the
same `JRow` selected look and semantics as the other rows; a nested sub-Agent opens its parent without selecting both rows.
An Agent pushed from inside Persona keeps Persona's root shade on either tab, including compact windows; compact Persona
retains its existing conversation shading. In the Merged sidebar layout (#13165), an Agent opened from inside a Persona page
(such as a card in the main conversation) is pushed onto that root's own stack rather than the Agents tab, so picking another
sidebar Agent or place leaves it on the conversation's stack, picking Main Chat again restores it with Back returning to the
conversation, and re-picking Main Chat while it is already in front pops to the bare conversation. The conversation and the fixed places (Inbox, To-dos,
Knowledge and Computers), plus the optional Lark Messages place while enabled, stay composed while another is in front, so the conversation keeps its non-saveable history, local
operations, running effects and list anchor, and those places keep their list, filter and scroll. Messages is a retained Assistant secondary place, not a primary tab; its Lark chat drill-down stays on the Messages host so Back returns to that list. Turning its switch off forgets only that host and path and restores the Assistant path retained before it, even when Agents is selected; background cleanup leaves the selected tab unchanged. Folding inserts the
conversation under that same list and unfolding removes it, so that page is not rebuilt. A to-do opened as its own root, or an
extension page, keeps only its path and is composed only while it is in front (`SidebarRootHosts`). Folding that root copies it onto the conversation and leaves the fixed places composed. A place the phone drawer
pushed over the conversation is promoted onto its own host when the window becomes regular, and that promotion rebuilds the
page. The root's top-leading control is the conversation's sidebar button (`resolveHomeLeadingAction`), never Back.
System Back at a place root returns to the conversation as a root switch, with no push or pop. System Back at the conversation
root leaves the app. The compact phone drawer still pushes a place that has no host yet over the conversation. A fixed place that already has a host is shown on that host. Health and
Scheduled Tasks are not navigation destinations on Android; picking them shows the conversation page, where those screens already open.

**Web** (#12055). The browser's history is the stack: an Agent opened from inside a page (a to-do, a knowledge project, a
meeting, a Persona conversation's card set to Full page) is a new entry naming that page, so Back returns to it and the
Agent's page offers "Came from … · click to return"; the sidebar keeps that page's row lit while the Agent is shown
(`web/src/lib/persona-nav.ts` `sidebarRoute`, `docs/web/web.md` "An Agent opened from inside a page is a drill-down"). An
Agent picked in the sidebar is a root and lights its own row. Reselecting the open Agent replaces its drill-down entry
without adding a history entry, clearing the origin and the way-back strip at the same address. Set to Full page, the
main conversation keeps the page it opened last while another place is in front (#13164, `pickMainChat` in
`web/src/lib/handoff.ts`): Main Chat picked from there opens it again over the conversation, so Back returns to the
conversation; picked while the conversation or its page is in front, it goes to the bare conversation.

## 7. To-dos places under the To-dos row (#10465)

On iOS, against a hub with `todo_view` and from #10465's switch-on, the To-dos row in the drawer and the sidebar folds open
the To-dos places (`docs/design/proposals/todos-redesign.md` 3.3; DESIGN_SYSTEM 3.5 "To-dos places"): **Today**,
**Upcoming**, up to five **projects**, then **All Projects…**. They are children of To-dos, not places of their own:

- A child is drawn as a sidebar row set in under To-dos (`TodoPlaceRows` draws them through `AssistantSidebar`'s own row),
  and picking one is picking To-dos (`ShellPage.todos`, `ShellRoute.todos`) on that place (`TodoWorkspace.open`). The
  shaded row is the place shown while To-dos is the page: To-dos itself for All To-dos, else the child; a place whose
  child is not in sight shades All Projects… for a project, else To-dos.
- The rules above hold with one addition: To-dos opened for another place starts at its root, never animated, and drops
  what it kept, which belonged to the place before (`ShellPlaces.enter(fresh:)` on the iPhone shell,
  `SidebarPageRoots.select(fresh:)` for the Sidebar layout's page; `ShellPlacesTests`, `SidebarPageRootsTests`). The same
  place picked again is the tab-bar rule as before.
- The To-dos row brings To-dos back as it was left, on its last place (kept for the app's run), as every place comes
  back (option 2, and option 3's kept drill-down); where To-dos is the place in front, beside the sidebar or under the
  drawer and whatever is pushed on it, it is All To-dos, To-dos' root. The page that opens To-dos decides from its own
  state, as it decides the tab-bar rule (`TodoWorkspace.opening(inFront:)`: `ShellPlaces.current`, the Sidebar layout's
  root, the classic page).
- All Projects… is a task: its sheet opens over the drawer and a project picked in it opens after the sheet has gone.
- The list's title menu ("All To-dos ⌄") is the same places a second way, inside the page: it changes the list in place.
