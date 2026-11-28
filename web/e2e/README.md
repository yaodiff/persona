# Bounded Agents list verification

## Paseo workspace pins

```sh
node web/e2e/paseo-workspace-pins.mjs <loopback-web-origin> <artifact-directory> [Chromium-executable]
```

Uses the real Agents page and sidebar against an owned local web server, with all hub HTTP and WebSocket requests
answered by synthetic data. Checks a pinned empty workspace ahead of its main sibling and an unrelated repository,
refreshes an unpin, and verifies that selection, disclosure and Persona's separate session pin survive. Covers desktop
and phone widths, light and dark, and standard and large text. No preference or Agent writes are allowed. Screenshots
and `results.json` are written outside the repository; a 180-second watchdog bounds the run.

## Direct completion of a decision task

```sh
node web/e2e/direct-completion.mjs <loopback-web-origin> --shots <artifact-directory> [--chromium <executable>]
```

Run against an exclusively owned local web server with web dependencies installed. The runner opens the real Assistant app in a fresh headless browser, intercepts all hub HTTP/WebSocket traffic with synthetic conversations, to-dos and an idle Agent, and never uses an existing profile or a live hub. It verifies Open / Finish now / Take recommendation ordering and emphasis, light/dark wide and narrow layouts, all three languages, failure/retry, completion removal and reload. Only `PATCH /todos/{id}` with `{"done":true}` is admitted as a task action; Agent sends, resumes and recommendation acceptance fail the run. Screenshots include before, after and a synthetic save failure. A 180-second watchdog exits with a timeout verdict. These are browser/UI checks, not production-hub delivery evidence.

## Ask card above the composer (#3083)

```sh
node web/e2e/ask-card-dock.mjs <loopback-web-origin> <artifact-directory> [--chromium <executable>]
```

Opens the real app against a loopback web server (Vite or a preview build) with every hub request and the socket answered in the browser by invented data. The main conversation shows an ask_user card with four described options in the Pending tray at 1280 × 600, 1024 × 520, 390 × 664 and 360 × 560 (touch), light and dark. It checks the card's send button and the three tray entries are not under the composer before any scrolling, and that "Or write your own" is fully visible (not under the sticky answer row or the tray's title) once tapped and typed into. Writes `results.json` and before/after-typing screenshots; exits nonzero on failure or the four-minute timeout.

## Waiting cards cut off below the dock (audit item 10)

```sh
node web/e2e/pending-cards-cut.mjs <loopback-web-origin> <artifact-directory> [--chromium <executable>]
```

Opens the real app on an Agent's session page against a loopback web server (Vite or a preview build) with every hub request and the socket answered in the browser by invented data (an Agent with a question and a Bash approval at once); no card is answered. At 1440 × 900 and 1280 × 820 the approval's answers stand at the bottom edge of the dock's box (its 55% cap) while its title and command are above it; at 1024 × 700 the question alone fills the box; 1440 × 1600 fits both. Light and dark. It checks that wherever the approval's Allow is in view, its title and command are too (in the card, or on the answer row's `data-card-answers` line), that the first card says "2 pending" while the stack is cut off, that the line brings the whole approval into view with the focus on it, that scrolling the box brings an approval wholly below it into view, and that neither the line nor the count is drawn when both cards fit. Writes `results.json` and screenshots; a four-minute watchdog ends the run with a timeout verdict, and the last line is the verdict.

## Chat content: sent lines, tables, bubble actions, model pill, runtime line, long folds

```sh
MOCK_TASKS=1 MOCK_MARKDOWN=1 MOCK_EXCHANGES=1 MOCK_MANAGERS=1 MOCK_RICH=1 MOCK_TASKS_SENT=1 MOCK_TABLE_CHIPS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/chat-content.mjs <loopback-web-origin> <artifact-directory> [--big] [Chromium executable]
```

Against an exclusively owned mock-backed web server (Vite or `vite preview`), in Simplified Chinese, light and dark. It
checks what jsdom cannot show from the web usability audit of 2026-11-14: Persona's message to a task folds to its first
line without Markdown marks and opens drawn as Markdown, and the supervisor's rework note keeps its lines apart (item 7);
the main chat's runtime line is absent when nothing is known and reads just "主对话 · 运行中" for a report intercepted in
the browser, whose place and harness Settings › Assistant shows instead (item 80); the model pill's name is not cut under
a 600 px composer and a phone keeps the 64 px tile and chevron (item 50); in a 390 px window with a pointer, Tab reaches
your bubble's first action inside the window, the bar under the bubble stays while the pointer goes down to it and takes
no clicks once hidden, and in a 1440 px window the bar stays beside the bubble (item 73); on a 390 px phone a "#" column is at most 40 px and the table copy button
covers no header words of a table that fits (item 77); in a 1440 px window a table with a sentence in its second column
takes the room it needs up to the column, wider than the old 18em cell cap, each long cell on at most two lines, and at
both widths no short code chip in a cell is broken across lines (#9146). `--big` runs against the mock started with `MOCK_BIG=1` instead
(item 90): opening the "worked 1 h 10 min" fold, no animation frame draws more than 60 rows at once, all 650 rows are
drawn after it with the header where it was tapped, and a search for `make step-600` lands on that row inside the closed
fold. Only the mock's own reads leave the browser; other origins are blocked. Screenshots and `results.json`
(`results-big.json`) go to the artifact directory; a 240-second watchdog ends the run, and the last line is the verdict.

## Overview's extension cards (#9745)

```sh
MOCK_EXTENSIONS=1 MOCK_OVERVIEW_CARDS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/overview-cards.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
# a hub without the route: the mock with MOCK_NAVIGATION=off; a default manager from before the field: MOCK_NAVIGATION=no-overview
node web/e2e/overview-cards.mjs <loopback-web-origin> <artifact-directory> --old-hub|--old-manager [Chromium executable]
```

Against an exclusively owned mock-backed web server, in Simplified Chinese. Four made-up cards (the gateway's usage,
Quota left, Needs me, Agenda): with nothing arranged they come in the hub's order, two a row; **整理总览** then drags
Agenda onto the first card, makes it a full row, moves Needs me earlier with the keyboard and switches Quota left off
(drawn as its outline while arranging). After **完成**, Agenda spans the row over two half cards, the hub keeps the
arrangement, a reload and a second browser draw the same and never send the hidden card's render request, and Reset
with Undo puts the cards back and the arrangement again. `--old-hub` / `--old-manager` check that the arrangement stays
in the browser, survives a reload and that the edit state says it is kept in this browser only. Light and dark
screenshots of Overview as it was, the edit state, the arranged result and the edit state with the hidden card at
1440 × 900, the arranged result and the edit state at 390 × 844, and `results.json`, go to the artifact directory; a
300-second watchdog ends the run.

## Overview's own sections (#10905)

```sh
MOCK_EXTENSIONS=1 MOCK_OVERVIEW_CARDS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/overview-sections.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
```

Against an exclusively owned mock-backed web server, in Simplified Chinese. With nothing arranged the sections come
first in their order and the cards last (#11147); **整理总览** draws each section as a bar (需要你 and 卡住了，要你去看
without a switch, 始终显示), drags 进行中的任务 onto 需要你 and switches 工作中 off. After **完成**, 进行中的任务 is right
under the greeting, above 需要你, the cards still last, and 工作中 is not drawn; the hub keeps the arrangement (every section named), a
reload and a second browser draw the same, and Reset puts Overview back. Light and dark screenshots of Overview as it
was, the edit state and 进行中的任务 moved first, at 1440 × 1300 and 390 × 1600, and `results-sections.json`, go to the
artifact directory; a 300-second watchdog ends the run.

## Sidebar More (#13798)

```sh
MOCK_EXTENSIONS=1 MOCK_EXTENSION_COLUMNS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/sidebar-more.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
```

Against an exclusively owned mock-backed web server, in Simplified Chinese. More starts visible with a put-away page. Its
settings switch hides the row at once, the hub keeps `more: false` with that page's choice, a second browser and a reload
keep it hidden, and the rail has no More menu. Turning it back on brings the page back. A direct link still opens while More
is hidden. Light and dark screenshots of the wide sidebar, phone drawer and setting, plus the rail and large text, and
`results.json`, go to the artifact directory; a 180-second watchdog ends the run.

```sh
MOCK_EXTENSIONS=1 MOCK_EXTENSION_COLUMNS=1 MOCK_SIDEBAR_GROUPS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/sidebar-groups.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
# a hub without the route: the mock with MOCK_NAVIGATION=off as well
node web/e2e/sidebar-groups.mjs <loopback-web-origin> <artifact-directory> --old-hub [Chromium executable]
```

Against an exclusively owned mock-backed web server, in Simplified Chinese. Made-up extension pages name 工作台, 资产 and
家里; the run checks that Today comes in under 工作台, then in Settings › General › Sidebar pages makes "Life" and renames
it 生活, moves built-in and extension pages between groups (Calendar from its manifest's 工作台 to 生活), moves 生活 up and
Pages to the end, and removes a group with Undo. It checks the sidebar's groups and their pages, that no page is lost, that
the hub keeps the arrangement and that a second browser draws the same groups; `--old-hub` that the arrangement stays in
the browser and the setting says so. Light and dark screenshots of the sidebar (the phone's drawer open) and of the editor
at 1440 × 900 and 390 × 844, and `results.json`, go to the artifact directory; a 300-second watchdog ends the run.

## Sidebar heading spacing (#10637)

```sh
MOCK_SESSIONS_LIST=1 MOCK_MANAGERS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/sidebar-heading-spacing.mjs <loopback-web-origin> [--shots <artifact-directory>] [--chromium <executable>]
```

Against an exclusively owned mock-backed web server, in Simplified Chinese, light and dark. It measures the sidebar's
top-level headings (Pages, Persona chats, Personal Agents when listed, Agents) with both Agents lists: the complete one
(the mock's pinned space keeps the sidebar on it) and the paged one (the run clears the pins and puts them back; the
paged list needs one assistant, hence `MOCK_MANAGERS=1`). At 1440 × 1000 (standard and Large text), 390 × 844 with a
mouse and 390 × 844 on a touch screen (the drawer, where Pages is a grid), folded and then unfolded, it checks that every heading's row is as
tall and its title centred as far down it, that folded the title-to-title gaps are equal, and that unfolded each title
stands as far below the group before it, all within 0.5 px (iOS: `SidebarHeadingGapsUITests`, #10561). Only the
mock's own requests leave the browser. Screenshots and `results.json` go to the artifact directory; a four-minute
watchdog ends the run, and the last line is the verdict.

## Unread Persona chats in the sidebar (#13425)

```sh
MOCK_LEADERS=1 MOCK_RECENT=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --host 127.0.0.1 --port <web port> --strictPort &)
node web/e2e/sidebar-unread-chats.mjs <loopback-web-origin> <loopback-mock-origin> <artifact-directory> [Chromium executable]
```

Against an exclusively owned mock-backed web server, in English, at 1440 × 1000 and 390 × 844 (the phone's drawer),
standard and Large text, light and dark. The mock's `GET /mock/incoming` puts a reply nobody has opened in two pinned leads (one with Agents
at work under it, whose arc its own Unread replaces) and two Recent chats, pushed as the hub pushes it; each row is
measured before and after: its height and where its mark's slot, title and time sit stay, the slot draws the unread
mark in place of the chat icon and the title takes the unread weight. Folded, Recent's heading counts its two and the
Persona chats heading all four ("Folded chats: N unread"); open, neither does. Opening a chat clears its mark at once
(the time from the click is recorded) and it stays clear after the hub has it; closed, a later reply brings it back.
Screenshots of the sidebar read, unread and folded, and `results.json`, go to the artifact directory; a four-minute
watchdog ends the run, and the last line is the verdict.

## Start page (#9071)

```sh
MOCK_EXTENSIONS=1 MOCK_EXTENSION_COLUMNS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/start-page.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
# a hub without the route: the mock with MOCK_NAVIGATION=off as well
node web/e2e/start-page.mjs <loopback-web-origin> <artifact-directory> --old-hub [Chromium executable]
```

Against an exclusively owned mock-backed web server, in English. It checks that a fresh hub opens on the main chat, that
Settings › General › Start page lists the main chat first and selected, then Overview and the made-up extension's Today;
that choosing Today saves it to the hub (`PUT /api/v1/navigation`) and a new tab opens on Today while the Persona row and a
reload stay on the main chat; that a second browser (another device) opens on the main chat the first time, reads the
choice at its hello and opens on Today from its next opening; and that a Today the hub's extension list leaves out opens
from the browser's copy and then gives way to the main chat, the choice kept and the setting saying why. `--old-hub`
checks that a hub answering 404 leaves the choice in the browser and the setting says so. Light and dark screenshots of
the setting and of the app opened on Today (Today moved first with Sidebar pages' ↑, as that setting already allows) at
1440 × 900 and 390 × 844, and `results.json`, go to the artifact directory. The run sets the mock's start page and puts it
back to the default; a 240-second watchdog ends it, and the last line is the verdict.

## An extension's dashboard page: UI version 8's columns layout (#9158)

```sh
MOCK_EXTENSIONS=1 MOCK_EXTENSION_COLUMNS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/extension-columns.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
```

Against an exclusively owned mock-backed web server (Vite or `vite preview`), in English, light and dark. It opens a made-up
extension's "Today" page and checks what jsdom cannot lay out: at 1440 × 900 the five cards are in reading order in two columns,
the "Needs me" card spans the row with its four rows two a line, Calendar | Lark today and WeChat | Running Agents sit side by side,
each pair as tall as its taller card (the shorter list's card is stretched, not as tall by chance), a card's head has its icon and
its link, the "Calendar ›" link pushes the card's page and Back comes back to the grid; at 390 × 844 there is one column, every card
the grid's width one under another, and the "Needs me" rows one a line; with Settings › Appearance's Large text, the same columns
at both widths and no card's content wider than the card. Only the mock's own requests leave the browser. Screenshots
(the window, and the whole page at the same width) and `results.json` go to the artifact directory; a 180-second watchdog ends the
run, and the last line is the verdict.

## Extension pages on reload: the last places and view at first paint (#9283)

```sh
MOCK_EXTENSIONS=1 MOCK_EXTENSION_COLUMNS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/extension-first-paint.mjs <loopback-web-origin> <artifact directory> [--shots-only] [Chromium executable]
```

Against an exclusively owned mock-backed web server, in English, light and dark, 1440 × 900. Every request the hub would
relay to the manager (`/api/v1/extensions…`) is held for a second in the browser, as a far hub takes. The made-up "Today"
page is loaded three times in one profile. The first load has kept nothing: its sidebar entry and header title come only
after the first relayed answer. The second draws the sidebar's entry, the header's title and icon and the page's last view
(marked as updating, inert) before the first relayed answer, and the extension's drawing then replaces it. The third's first
paint is photographed while every relayed request is still held (`first-paint-after-<theme>.png`). `--shots-only` takes only
that photograph (`first-paint-before-<theme>.png`), for a build without the cache. Only the mock's own requests leave the
browser. Screenshots and `results.json` go to the artifact directory; a 180-second watchdog ends the run, and the last line
is the verdict.

## An extension's quota card: UI version 9's gauge (#9337)

```sh
MOCK_EXTENSIONS=1 MOCK_EXTENSION_GAUGE=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/extension-gauge.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
```

Against an exclusively owned mock-backed web server (Vite or `vite preview`), in English, light and dark. It opens a made-up
gateway's "Quota" page and checks what jsdom cannot lay out: the three rings in order, each read out as "title: value unit, N%"
(a meter, or a button for the one with an action), its arc filled to the ratio in the named color the browser resolves, its value
centred in the ring and not cut, its title below, the ring inside its tile, and the reset times under the rings; at 1440 × 900 the
four status tiles in one row above three rings side by side of one width, and pressing the low ring answers its toast; at 390 × 844
two rings a line; with Settings › Appearance's Large text the values still whole and centred at both widths; with Reduce Motion the
arc does not ease. It waits for the page's finite animations to end, never for a fixed time. Only the mock's own requests leave the
browser. Screenshots and `results.json` go to the artifact directory; a 180-second watchdog ends the run, and the last line is the
verdict.

## An extension's work graph: UI version 10's graph (#9727)

```sh
MOCK_EXTENSIONS=1 MOCK_EXTENSION_GRAPH=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/extension-graph.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
```

Against an exclusively owned mock-backed web server (Vite or `vite preview`), in English, light and dark. It opens a made-up
"Work" page (a 200-node tree, one part folded by the view, a step selected) and checks what jsdom cannot lay out: the canvas
holds to 50%–100% on opening, centred on the selected node; the visible nodes in reading order with the group frames, edges
in one SVG and the selected node's ring; the fold beside a node both folds (hiding its subtree behind a +N) and presses a
node through to its own page and back; a drag pans and the zoom buttons zoom, with Fit centring everything (down to 25%,
and kept as the person's view across a redraw — Fit survives a live graph's polls); the person's fold, pan and zoom survive
the page's own redraw with the same key; Full screen covers the window and Esc closes it, the focus returning to the button
that opened it (the page's Full screen button, a card's preview button); at 390 × 844 the canvas is 70% of the visible height
clamped to 320–900; and the Overview card draws the same
graph as a 200-px still preview whose only press is opening Full screen. It waits for the page's finite animations to end,
never for a fixed time. Only the mock's own requests leave the browser. Screenshots and `results.json` go to the artifact
directory; a 180-second watchdog ends the run, and the last line is the verdict.

## An extension's home scene: UI version 11's scene (#12641)

```sh
MOCK_EXTENSIONS=1 MOCK_EXTENSION_SCENE=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/extension-scene.mjs <loopback-web-origin> <artifact-directory> [Chromium executable]
```

Against an exclusively owned mock-backed web server, in English, light and dark at 1440 × 900. It checks a nonempty Canvas,
`alt` / floor labels / pins reading order, opaque pin contrast of at least 4.5:1, the default camera, keyboard orbit, zoom buttons,
Reset, drag, wheel and real Chromium two-finger touch events. A pin opens its device page and Back keeps the touched camera through
a state redraw. Full screen keeps every pin inside the canvas and hit-testable, closes with Escape and returns focus. The Overview
card checks its 200 px still preview, absence of nested controls and interactive Full screen. Large text and Reduce Motion add
light/dark page, compact and full-screen captures, pin hit-testing and immediate camera input without a scene animation. Only the
loopback origin is allowed; expected mock 404 resources are excluded from console errors, while page errors and other console errors fail the run. Screenshots
go to the artifact directory, never the repository. A 180-second watchdog ends the run and the last line is the verdict.

## User-input navigation acceptance

With web dependencies and Chrome already available, point the runner at an **already-running, exclusively owned loopback web server** backed by `scripts/mock/mock_hub.py`. Start the mock on an unused loopback port and configure the web server's `PERSONA_HUB_URL` for that mock; a Vite server proxying a real hub is not acceptable. The runner does not install dependencies, start services, create conversations, or stop the caller's services.

```sh
node web/e2e/user-navigation.mjs <loopback-web-origin> <artifact-directory>
```

The URL is an origin without a route or token. The runner uses `playwright-core` with the installed `chrome` channel, checks the mock's HTTP health and WebSocket hello, and opens the actual app's conversation page. Browser routes supply entirely invented inputs and answers, including instructions without question marks. All browser HTTP writes and WebSocket client commands are intercepted rather than forwarded to the hub; unrelated mock messages are suppressed, external requests are blocked, and no existing browser profile or account is used. It does not exercise model delivery or claim production-hub acceptance.

Checks run in light and dark at 1440 × 900 and 390 × 844. Wide cases verify the visible resting rail in the left
gutter, rightward bell-shaped expansion, a bold single-line query and up to three lines of reply, a continuous hover
bridge, and immediate preview dismissal after mouse-out. They verify body-scroll ownership, click and drag landing,
keyboard previews and jumps, Escape cancellation and Tab reachability after idle. At 390 px this short fixture has symmetric padding and a separate scrollbar. The 240-input conversation
reaches every input by keyboard, jumps to three positions and checks native wheel scrolling and viewport-bounded
rendering with 7–8 px rows.

Artifacts include `results.json` (per-check verdicts, timings, geometry, reached-mark count, intercepted request metadata and errors) and light/dark screenshots of scroll, hover, click, drag/release, keyboard, idle and edge-focus states, plus the long conversation's earliest/middle/latest inputs. The runner exits nonzero on failure or its four-minute timeout and prints a final one-line verdict. The caller must run it against the completed implementation, actually inspect the images, and upload the evidence; writing or syntax-checking this script is not browser acceptance.

## Conversation scrollbar and navigation clearance (#4803)

```sh
node web/e2e/chat-scrollbar.mjs <loopback-web-origin> <artifact-directory>
```

Use the same exclusively owned mock-backed server as the navigation runner, preferably `vite preview` after a production build. All app HTTP/WebSocket traffic is answered by invented data; no writes reach the mock or a real hub. The runner checks 1600, 640 and 390 px pointer layouts, touch-only 390 px, the largest app text size, light/dark, reduced motion, forced colors and an unsupported-scrollbar-color fallback. It measures independent edge/rail hit areas and content clearance, scroll/idle colors and unchanged geometry, and drags the **native** thumb to verify real scroll movement. The 180-second deadline prints a final success/failure/timeout verdict. Screenshots and `results.json` stay outside git.

The navigation hit strip is 16 px inside the left edge of the chat pane when the natural gutter fits. The native
thumb uses the right edge. The transcript uses symmetric padding in both wide and narrow cases.
`useChatScrollbar` reveals the thumb during scrolling, scrollbar hover or drag, then fades after one second of idle
through `--chat-scrollbar-fade` (220 ms, disabled by Reduce Motion). Forced colors and unsupported browsers use native presentation.

**Headless Chrome pitfall**: Playwright adds `--hide-scrollbars` by default. This runner removes that argument; otherwise screenshots can show no native thumb and a zero-width gutter even though CSS reports a visible color. Always inspect the actual scrolling/idle images as well as computed styles.

## On-demand user-input history (#15813)

```sh
node web/e2e/user-history-navigation.mjs <loopback-web-origin> <artifact-directory> [input-count] [main|session] [initial-body-count] [standard|large] [legacy|slow|cancel]
```

Serve the production web build on an isolated loopback origin. The runner intercepts data requests and the
WebSocket with an invented conversation, so this scenario needs only a web server. The default starts with two
input bodies and a 120-input compact index. Light and dark runs at 1440 × 900 and 390 × 844 check the pixel-sized
native wheel step, lazy excerpt batches, the visible loading label, a direct context jump and return to the live
tail after a WebSocket reply. It saves all three states per run and writes the observed requests to `results.json`.
Pass `120 main 2 large` for the largest app text size.

`36 main 2 standard legacy` exercises a positions-only Hub. `75 main 12 standard slow` checks legacy read failure
and retry. `75 main 12 standard cancel` checks interrupted backward jumps. Runtime artifacts stay outside Git.

## The sidebar's Agents groupings on a paging hub (#6662)

```sh
MOCK_SESSIONS_LIST=1 MOCK_SESSIONS_CATALOG=1 MOCK_MANAGERS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/agents-paged-groupings.mjs <loopback-web-origin> <artifact-directory> [--before] [--channel chrome]
```

Run against an exclusively owned mock-backed web server. It checks the one grouping bar (待办 / 机器 / 仓库 / 项目 / 状态) over the paged and the complete list, By to-do paged as a section per open to-do (open) and 没挂待办的 (folded) against the mock's `todo` counts, By machine and By state paged, and By repository and a pin taking the complete list; it saves light and dark screenshots. `MOCK_MANAGERS=1` keeps one assistant, since with several the sidebar reads the complete list. The runner clears the mock's default pins for the run (they also keep the complete list), sets one for the pinned case, and puts them back. `--before` takes the same screenshots of a build from before #6662 with no checks. (It replaces `agents-browse-by.mjs`, whose sidebar view by catalog project the sidebar no longer offers; `PagedSessionList.groups.test.tsx` still covers that view.)

It reads the mock's own catalog and holds the page to it, in English at 1280 x 900 (light and dark), a 240 px sidebar and a 390 px touch phone: Browse by Computer draws one section per computer in the hub's order, the first three open on their first six sessions in stable computer → workspace → tab → pane order and the counts adding up to the list's total; Project lists the catalog's projects in fixed name order, the repository on two computers as one section with its remote and both directories and the sessions of both; the value pill's menu is as wide as its pill and names each computer under the project; a picked project is the one flat list of its sessions; Space names each computer's spaces, tells two of one name apart and ends with the sessions in no space; the chosen Browse by survives a reload; Recently updated is the flat list. With `--hub` it also drives 20 s of the mock's churn while the list is drawn by project and prints the mock's read counters by kind, failing when the groups were read more than once per 1.5 s or a complete list was read. Screenshots of each step go to the artifact directory. A three-minute watchdog ends the run with a timeout verdict. Browser and mock checks only: no real hub, node or Git repository is involved.

## Machines and Overview at desktop widths (web audit items 3, 25, 30 and 76)

```sh
MOCK_SESSIONS_LIST=1 [MOCK_MANAGERS=1] PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort --host 127.0.0.1 &)
node web/e2e/overview-machines-layout.mjs http://127.0.0.1:<web port> <artifact-directory> [--channel chrome]
```

Run against an exclusively owned mock-backed web server, once with `MOCK_MANAGERS=1` (one assistant) and once with the mock's default roster (several assistants); `MOCK_SESSIONS_MANY=300` adds a list longer than its first page. The runner reads the mock's own complete session list (every page, `roots=1`) and holds each Machines card to it: the agents that are not offline, without the assistant's conversations, on a hub that pushes no sessions. At 768, 1024, 1280 and 1920 px, light and dark, it checks every online card's count, a gap of at least 8 px between the card's state line and its versions, and that no metric value is cut (the text's own width against its box, since `scrollWidth` rounds away the half pixel Chrome already cuts with "…"), with the widest rate a card can show ("↓ 1023 MB/s", "↑ 1000 KB/s") answered for one computer in the browser, and that a card's metrics are one row or two columns, never three with one left alone (one row at 1280 and 1920 px). On Overview it answers an example extension's overview card in the browser (its place, list and view, with the UI-slot features added to the hub's hello) and checks that a lone card is the page's width at 1280 px and one of two columns at 1440, 1680 and 1920 px. Then it fails the list Overview counts from in the browser (the paged list's reads with one assistant, the complete list's with several) and checks that the summary says it couldn't read the agents, in the line's live region, with **Retry**, with no **All set** and no computer count; after the route is lifted, **Retry** brings the summary back, its counts outside the live region (left empty), and each online computer's count matches the mock's complete count, including while the paged list has more pages. Nothing is written to the hub. It saves a screenshot of each page and size, prints one line per check, and ends with a verdict; it exits 1 on a failed check and 2 on its three-minute timeout.

## Machines alerts and what fixes them (#8037)

```sh
MOCK_MACHINE_ALERTS=1 MOCK_MANAGERS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort --host 127.0.0.1 &)
node web/e2e/machine-alerts.mjs http://127.0.0.1:<web port> <artifact-directory> [--lang en|zh-Hans|ja] [--chromium <executable>]
```

Run against an exclusively owned mock-backed web server with the mock's alert fleet (made-up computers, one per alert: memory, disk, version, offline, launchers, healthy). Light and dark at 1280 and 390 px, it screenshots the cards with their alert chips, the memory sheet and its Stop idle Agents confirmation, the hand-off picker, the disk sheet with its cleanup confirmation, the version sheet, the offline and launcher sheets, and the Capabilities view; it checks that every chip is a labelled button, that the offline computer is first and the healthy one last, that the cache in use can't be picked, that the offline computer is not a hand-off target and an eligible one is ranked first, that the version sheet says how much older the node's build is, that no button's text wraps and nothing is wider than a sheet, and that the page logs no errors. Its last scene redeploys the mock's Studio Mac through the stepper to the node's new build (restart the mock to run it again). It prints one line per check and a verdict; it exits 1 on a failed check and 2 on its four-minute timeout.

## Read-only to-do progress board (#12713)

```sh
PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npm run dev -- --host 127.0.0.1 --port <web port> --strictPort &)
node web/e2e/todo-progress-board.mjs <loopback-web-origin> [--shots <artifact-directory>] [--executable <chromium>]
```

Runs against an exclusively owned mock-backed web server, replacing every to-do, session and project with synthetic data over REST and the socket; no real hub data is read. At desktop and phone widths, light and dark, and default and large text, it checks stored-project and No-project groups, responsibility, latest progress with its own timestamp, owner decisions, per-project counts, closed/shelved exclusion, the exact 72-hour boundary, positive-hour persistence and invalid input, read-only rows, detail navigation and browser Back. It records and rejects every non-GET request and permits no client socket command other than the ordinary read-only `client_hello`, including across a Board reload, then checks viewport fit and browser errors and optionally saves four screenshots. The browser clock is fixed; no fixed sleeps are used. A failing check exits non-zero, and a four-minute watchdog prints a timeout verdict.

## Download deadlines from the To-dos calendar

Run `node web/e2e/todo-calendar-export.mjs <loopback-web-origin>` with Chrome and the mock-backed Web app. The test supplies dated to-dos, opens a week at desktop and phone widths, disconnects the page and reads the downloaded files. It checks the filenames, visible dates, source revision times and removal of credential-bearing links.

## To-dos with a keyboard, narrow and wide (web audit items 2, 74, 75, 88)

```sh
MOCK_TASKS=1 MOCK_TODO_ASKS=1 MOCK_LEADERS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/todos-keyboard-layout.mjs <loopback-web-origin> [--shots <artifact-directory>] [--executable <chromium>]
```

Run against an exclusively owned mock-backed web server; the date checks save to the mock, so use a disposable one. In Chinese, light and dark, it types a due day and a reminder hour segment by segment in the to-do's page (1280 × 820) and checks the field keeps the focus and nothing is saved until the field is left or Enter is pressed, then the typed value is saved once, also when the field is left right after Enter; at 390 × 844 it opens a to-do from a focused row with Enter (the focus lands on Back), checks Esc in the title field stays there, and goes back by Esc, Back and the browser's Back, each time with the list scrolled where it was and the focus on that row, and Esc on a to-do opened by a link puts the focus on its row; at 1920 × 1080 it checks the detail is at most 760 px wide and centered; at 1440 × 700 it leaves the page for Machines and comes back with the browser's Back, the list scrolled where it was and the open row in view. A part that throws counts as a failed check and the run goes on; one line per check, a final verdict, exit 1 on a failure or after four minutes. `--shots` saves the typed date field, the narrow detail and list, the wide detail and the list after Back. Browser and mock checks only.

## Knowledge kind chips stay put

```sh
PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/knowledge-kind-chips.mjs <loopback-web-origin> <artifact-directory> [--channel chrome]
```

Run against an exclusively owned mock-backed web server; the runner only reads. On `/kb/projects` it checks that every kind chip reads its kind's name alone, then picks each other kind by clicking its chip and holds that kind's list read (`page.route`), so the kind is still loading: **Loading** must show under the chips, and every chip's box is measured. It then lets the read through and measures again once the list is drawn: no chip may move, change or go. It runs at 1280 x 820 and on a 390 x 844 touch phone, light and dark in Chinese and light in English, and saves the chip row with the first and the last kind picked, and with the last kind while it loads. A three-minute watchdog ends the run with a timeout verdict; it exits 1 on a failed check. Browser and mock checks only.

## The complete Agents list with hundreds of Agents

```sh
MOCK_SESSIONS_LIST=1 MOCK_SESSIONS_MANY=400 MOCK_LEADERS=1 MOCK_TASKS=1 MOCK_RECENT=1 MOCK_HISTORY=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && npx vite build && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite preview --port <web port> --strictPort &)
node web/e2e/sidebar-many-agents.mjs <loopback-web-origin> <artifact-directory>
```

Run against an exclusively owned mock with 400 extra Agents on no to-do (the mock's default pins keep the complete list). A dev server works too; the production build is what the long-task numbers are for. With By to-do and 4x CPU it checks, at 390 × 844 touch: no row under a folded group (`aside [inert] a` is 0, the sidebar under 2,000 elements), none of Not on a to-do's rows on the Agents page while it is folded, at most 90 of its 400 rows once opened, its heading stuck under the Agents controls and rows (not empty boxes) on screen in the middle of the list, and the last rows drawn at its end with the count still bounded. At 1280 × 800 with the group open in the sidebar: a bounded number of rows, Tab walking the rows in order across the parts, and an open Agent far down the group (Session 330) drawn and scrolled into view, also when it is opened from elsewhere while the group is folded (the group opens for it). By machine, with the mock's 400 Agents as tabs of one open workspace at 390 × 844: a bounded number of its tab rows in the sidebar and on the Agents page, and the sidebar under 3,000 elements. It prints the long tasks of a cold Overview and Agents page and of a By machine page switch, saves five screenshots and `results.json`, prints one line per check and a verdict, and exits nonzero on a failure or its four-minute timeout. Browser and mock only.

## Bounded list verification

The runtime chat uses the exact lookup added in server PR #6086 when `hello.features` advertises `sessions_lookup`: `GET /sessions?summary=0&limit=50&machine_id=<id>&session_id=<id>`. Initial entry, coalesced invalidations, reconnect and visibility recovery load only that complete record. Responses are identity-checked; missing rows and unauthorized reads never substitute another session or trigger a collection scan. Complete snapshots replace optional fields so stopped background/subagent/workflow/queued tasks do not survive as stale arrays.

On an older paged hub without `sessions_lookup`, list reads use bounded `summary=0` pages and retain complete loaded/clicked rows; revision deltas update only retained detail identities. A non-paged legacy hub retains its initial legacy initialization and socket delivery. Active-chat reads and reconnects never redownload the full collection: they use retained data and the existing per-session info/transcript paths. An unlisted legacy chat can open with an explicit metadata-unavailable notice rather than scanning every session.

Run from the repository after installing web dependencies:

```sh
cd web
npm run typecheck
npm run lint
npm test
npm run build
node e2e/agents-performance.mjs <scratch-directory> 18983
```

Use an unused loopback port and a scratch directory with space for a source copy. The runner needs Go (the version in `server/go.mod`) and Chrome; `playwright-core` drives the installed `chrome` channel. It never starts a real node, reads node process environments, connects to a production hub, or migrates a database. Test-database environment variables are removed from the fixture process.

The runner copies the existing `server/` and local Go SDK into its scratch directory, adds `agents_hub_test.go` to that copy, and compiles the existing hub test package. The fixture uses the real hub session HTTP/WebSocket handlers with the same `syntheticAgentHub` as #5963: 8,000 invented sessions, four computers, 20 projects, ten computer-local spaces, four states and long previews. Production web assets are served by this loopback fixture. Unrelated API responses are minimal synthetic stubs. Personal-agent acceptance separately intercepts that collection's HTTP responses with 8,000 camelCase rows; it does not claim to measure a real extension upstream. The legacy test removes `sessions_list` from the socket hello and verifies the unchanged full-list HTTP route.

Acceptance includes:

- Exactly one initial `summary=1&limit=50` runtime page and no complete-list GET in negotiated mode.
- Every frozen page reached by scroll, with 8,000 loaded identities and exactly 159 continuation pages.
- Main-list DOM bounded to visible rows plus overscan, including at the end of the complete traversal.
- Title-only invalidation using `since`, with no new baseline GET.
- Server state/search/project filters and machine-scoped space filtering/counts.
- Personal-agent first page, scroll continuation, virtual rows and its own revision delta.
- Older-hub fallback with a visible legacy list and all 8,000 rows returned.
- Cold deep link to the oldest row outside the first page: one exact full snapshot, including queued work/background data, with no cursor/list scan.
- Active-chat invalidation: full uncapped title changes and omitted task arrays replace cached metadata.
- Paged-but-no-lookup compatibility: complete 50-row pages plus revision recovery keep the chat current without an exact parameter or full-list redownload.

Artifacts are `results.json`, `agents-scroll.trace.json`, light/dark runtime, personal-agent and active-chat screenshots, and `hub.log`. Load the trace in Chrome DevTools Performance. Initial rendering is recorded on the second animation frame after the first visible main-list row enters the DOM. The rapid-scroll sample advances 240 CSS pixels per animation frame until all 8,000 rows are loaded and the scroll reaches the end (45-second ceiling). FPS is animation-frame cadence in headless Chrome, not a claim about a physical phone, GPU presentation or internet latency. Payload bytes are uncompressed HTTP JSON.

## Integration lessons and boundaries

- Do not fetch a legacy personal-agent list from a mount effect before the socket hello: that bypasses negotiation and downloads the entire collection on a cold `/a` entry.
- Keep frozen-page cursors, the completed delta checkpoint and aggregate-count revisions separate. Keep updates/removals for not-yet-loaded identities so later frozen pages cannot restore stale rows. Apply a delta checkpoint only after all its pages succeed.
- Summary rows and complete subscribed chat details are separate stores. Explicit `waiting: false` overrides old task arrays; missing arrays are not proof that no task runs.
- Counts are intersections of all supplied filters. State controls request counts without `state`, but retain the chosen computer/project/space/query scope. Directory suggestions use separate facet counts and render at most 50 options.
- The transport sorts by update time and lexical composite identity, not client pin/state/name preferences. Preserve nanosecond ties and decode runtime removals as JSON tuples, never by splitting on `/`.
- The server does not expose to-do/custom-section/pin/mute/botmux/quiet/assistant facets or custom sorting. Negotiated mode offers its supported computer/project/space/account/state/query filters and raw collection counts; it retains row actions but does not present incomplete-prefix counts as global preference-aware totals. Legacy mode keeps the original presentation. Extending those server facets is separate work, not a web-only approximation.
- This change only merges the web implementation. Production deployment and live validation are deliberately not part of the runner or the handoff.

## Approved rollout

After the deployment owner obtains user approval, deploy the hub binary containing server PR #6086 and the web assets containing #6051. Deploy the backend first (or together in the approved release), then reload the web client so it negotiates `sessions_lookup`. This endpoint extension needs no database migration or new node RPC. Until then the documented older-hub fallback applies. This task does not deploy or restart any production service.

## Shared chat sync cases end to end

`sync-cases.mjs` plays every case in `spec/fixtures/sync/` in a real browser against the mock hub playing it (#2777, #1073 step 4). The page is the real web app, with its own store, socket, composer and cards; nothing in it is faked.

```sh
(cd web && npx vite build)
MOCK_WEB=web/dist MOCK_SYNC_CASE=sends.json#send_tied_by_echo PORT=19051 python3 scripts/mock/mock_hub.py &
node web/e2e/sync-cases.mjs http://127.0.0.1:19051 --hub http://127.0.0.1:19051 [--case <name>] [--out <dir>] [--browser <path>]
```

- **Setup.** The mock serves the built app itself (`MOCK_WEB` while it plays a case). Behind a dev server's proxy, a write the case cuts would come back as the proxy's own error response, which the page takes as a definite failure. The runner loads each case in turn (`POST /mock/case/load`) and opens one browser context per device, signed in as `mock-<device>`. Each page's clock is Playwright's, so a case's `wait` (35 s, 10 min) passes in moments.
- **Each step.** The runner asks the mock for the step, then does the user action on the page. A refusal's dialog is closed before the failed bubble's Retry or Delete. After every step it checks the README's three rules and the step's `expect`: messages, cards and streamed text through `web/src/views/chat/sync/project.ts`'s projection; `requests` from `GET /mock/case`. The chat's `status` has no mark on the page and is not compared.
- **The end.** Every page settles, then is compared with `end`.
- **Known failures.** `KNOWN` lists cases that fail for a known bug, each with its issue. A known case that passes fails the run, so the list stays true.

Output: one line per case and a verdict, `results.json` in `--out`, and a screenshot of any page where an action failed. It exits 1 on any other failure, and after 20 minutes in any case.

## Worktree migration verification

Start `scripts/mock/mock_worktree_migration.py` on a task-owned loopback port, then point a separate Vite server's `PERSONA_HUB_URL` at that fixture. Do not use a real hub. With web dependencies and Chrome already installed:

```sh
node web/e2e/worktree-migration.mjs <loopback-web-origin> <loopback-fixture-origin> <artifact-directory>
```

The runner verifies the fixture marker before writes and resets only its synthetic in-memory state for each browser context. External browser requests are blocked; no existing browser profile or account is used. Desktop and 390px touch contexts run in light and dark. Checks cover empty catalog ancestors, title/disclosure separation, exact existing-workspace Agent launch, independent Git source/project/parent/base creation, explicit branch/note/sparse fields, no automatic Agent, acknowledged rename, dirty deletion confirmation and default branch retention. A lost create response is recovered across dialog close/reopen with the same receipt; an uncertain deletion permits only read-only reconciliation. `WORKTREE_ACCEPTANCE_PHASE=catalog` runs just the initial catalog scenarios while dependencies are being integrated.

Artifacts include `results.json` and screenshots of the tree, exact destination, composer, acknowledgment, rename, dirty deletion and uncertain outcomes. The runner exits nonzero on assertions or its four-minute watchdog. Inspect the resulting images on the combined revision. This is full Web application acceptance against invented runtime data; it does not replace node semantic tests or the separately bounded installed-Orca verification on disposable projects.

## `/permissions` and Full access by a restart (#6476)

```sh
MOCK_BYPASS_RESTART_SECONDS=4 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/permissions-command.mjs <loopback-web-origin> --hub http://127.0.0.1:<port> --theme light|dark [--shots <dir>] [--chromium <executable>]
```

Run once per theme against a fresh mock (the restart changes the session). On a resting Claude Code Agent started
without a bypass flag, it checks that "/" lists `/permissions`, that Enter opens all five modes with the current one
checked and sends nothing, that Auto is posted and shown at once, and that Full access says the session restarts and
asks first. It then checks that the session waits (`bypass_pending`, the chip still on the mode it runs in) until the
mock's restart comes up in Full access. It prints one line per check and a verdict, and exits nonzero on a failure or
its five-minute timeout.

## Settings: hints, search, provider errors, disconnect, hubs and lists (web audit)

```sh
MOCK_EXTENSIONS=1 MOCK_CHANNELS=1 MOCK_SENT=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort --host 127.0.0.1 &)
node web/e2e/settings-audit.mjs <loopback-web-origin> <artifact-directory>
```

Run against an exclusively owned mock-backed web server; a server proxying a real hub is refused (the runner checks the
mock's health reply). Each check opens a fresh headless Chrome context signed in to the mock; requests to other origins
are blocked, and a write that revokes a token or saves a provider is answered in the browser with an error and never
reaches the mock. It checks, in light and dark:

- General's setting sentences wrap instead of being cut at one line (1280 and a 390 phone), Translation's note keeps
  its spacing above Agent preferences, and on a phone the language menu sits under its sentence; in English, Chinese
  and Japanese nothing in General is cut off with an ellipsis on a 390 phone (audit item 17).
- In English, Chinese and Japanese, typing a setting's name ("Language") in the settings search lists that setting
  first, Enter scrolls to its row and lights it, and Esc clears the box; the settings of the other pages are found by
  their labels (thinking effort, time zone, default permissions, quiet hours, continue after errors, secrets, device
  tokens, revoke; in Chinese also the audit's own words, in English "timezone" and "effort"), Revoke is listed as
  Revoke, and an agent's setting opens that agent's section (audit item 18); the hub's category reads "This hub" and
  Models and Knowledge selection have different icons (audit item 66).
- Next on an empty custom gateway focuses the first field with a problem, marks it invalid, and its note is inside
  the dialog's visible area at 1280 x 600; every field that needs something says so, the others with a red edge;
  nothing is saved (audit item 19).
- Disconnect this browser asks first, saying pairing is needed again; Cancel revokes nothing and keeps the sign-in.
  With the question's chunk refused (a redeploy), Disconnect from the account menu leaves the app drawn, says
  reloading should fix it and revokes nothing (audit item 20).
- Add hub refuses "not a url" and an escaped host under the field and keeps the list as it was, and still adds a real
  address (audit item 67).
- On a 390 phone a connector's page, Sent and an extension's page each draw one Back, the top bar's, which goes to the
  category; so do the market's catalog, one of its extensions and the list of markets, whose Back goes to the catalog;
  under the Lark bot's address the top bar's Back goes to Settings; a wide screen keeps the page's own Back (audit
  item 64).
- With the list beside an open extension or connector, the open row is the only one marked `aria-current`, its name is
  whole and its state sits under it; a list on its own marks nothing (audit item 68).

It prints one line per check and a final verdict, writes `results.json` and screenshots to the artifact directory, and
exits nonzero on a failure or its five-minute timeout. Against a build from before these fixes nearly every check fails.

## The app shell's connection states, dead links and keyboard focus (web usability audit)

```sh
MOCK_TASKS=1 MOCK_SESSIONS_LIST=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/shell-connection.mjs http://127.0.0.1:<web port> http://127.0.0.1:<port> <artifact directory>
```

Needs a fake hub of its own: the runner takes it down (`/mock/down`) and refuses this browser's credential for a while
(`/mock/reject`), and puts both back at the end. In Chrome, light and dark, it checks that folding the sidebar to its
rail by the keyboard leaves the focus on the rail's expand button and opening it again on the collapse button; that an
Agent's page names its browser tab after the session's title; that an unknown address says **Page not found** and
keeps the address, a knowledge entry the hub doesn't have says **Entry not found** in the shared empty state (no
title made of its id, no disabled buttons), and `/a/<missing>` says the conversation isn't there once, under the list's
name, with no message box left loading; that the
first key press makes no `AudioContext` while the alert sound is off and one when it is on; that the reconnect pill
covers none of the to-dos' fields, buttons or titles at 390 px and 1280 px, and its details open over the page; that
the English Offline line wraps only between words; and that a credential refused while the page is open reaches the
sign-in page with "signed out" within 25 seconds. Each block of checks fails on its own and the run goes on; it prints
one line per check and a verdict, saves screenshots in the artifact directory, and exits 1 on a failure and 2 on its
five-minute timeout.

## Keyboard around the overlays

```sh
MOCK_RECENT=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/keyboard-overlays.mjs <loopback-web-origin> [--shots <dir>] [--executable <chromium>]
```

Run against an exclusively owned mock-backed web server, in Chinese, light and dark, a fresh browser context each. It checks that the session ⋯ menu shows every row at 1280 × 820, and at 1280 × 560 (too short for it) opens beside its button (not over it) with a fade at the edge where rows are cut and End scrolling the last row into view; that Escape anywhere in the ⌘K palette closes it and gives the focus back to the composer it was opened from; that ⌘/ and ⌘K replace one another instead of stacking two dialogs; that Tab in the shortcut help reaches "Change in Settings" after the scrolling list and wraps to Close; that Escape out of a menu's Rename leaves the focus on the row with the Rename row highlighted, so Enter renames again; that Escape clears the To-dos search; and that a theme, theme color or language picked in one tab reaches the other, the language's reload waiting (with a toast) while that tab holds a draft. Every hub write except the sign-in is answered in the browser. It prints one line per check and a verdict, and exits nonzero on a failure or its three-minute timeout.

## The Inbox by keyboard, its filtered empty state and long lists (web usability audit)

```sh
PORT=<port> MOCK_INBOX_EXTRA=300 MOCK_INBOX_HISTORY=busy python3 scripts/mock/mock_hub.py &
(cd web && npx vite build --outDir <scratch>/dist && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite preview --outDir <scratch>/dist --port <web port> --strictPort --host 127.0.0.1 &)
node web/e2e/inbox-keyboard-long-list.mjs http://127.0.0.1:<web port> <artifact-directory>
```

Run against an exclusively owned web server backed by the mock hub with the flags above (`npx vite` works too; a
production build is what the timings mean). It uses only the mock's invented data and answers the read failure in the
browser. In English, light and dark, at 1280 x 820 unless named: chips that leave nothing show the neutral filtered
empty state with no green check and **Clean up all** without "Nothing to do", and **Clear filters** brings the rows
back with the keyboard on the **All** chip; a failed read has its own **Retry** and a Clean up all with no scope; the detail's small line has the time right
after its words, not at the line's end; **Edit** on a drafted reply puts the caret at the end of the text; Select and
Done keep the keyboard on the one button, also after Esc on a row's tick; at 390 x 844 a row opened by keyboard moves
the focus to the item's title, with its focus ring, and Esc or the browser's Back returns it to that row. With Done
open, **Done** in an opened item's header leaves the list where it was (it used to jump to the row filed under Done), at
1280 and at 390, and at 390 the focus lands on the row now in the item's place. Last, a 390 x 844 touch phone at CPU 4x
opens the long Inbox and records how many rows were there at the first draw (at most 100), that all of them come in the
end, the elements under `main` and the long tasks. Screenshots and `results.json` go to the artifact directory; a
four-minute watchdog ends the run, and it prints a final pass / fail / timeout line.

## Long tasks in a long conversation (#9795)

```sh
node web/e2e/chat-long-tasks.mjs <web origin> <artifact-directory> [--items <n>] [--secs <s>] [--chromium <executable>]
```

Opens the real app in a fresh headless browser with every hub request and the socket answered in the browser by invented
data, and every other origin refused, so it can be pointed at any server of the web bundle (Vite, `vite preview`, or a
hub serving its build) without reaching a hub. The text is shaped like real chats: branch and repository names without
an extension, quotes and tables. Two scenarios: an Agent chat of `--items` items (2,000 by default) opening, watched for
10 seconds after its newest message is on the page; then the main conversation (240 items and a question) while a reply
naming 80 branches streams for `--secs` seconds (30 by default), a delta about every 80 ms and a tool call every
second. A trivial script is run in the page again and again; one not answered within 5 seconds counts as stuck.
`results.json` has, per scenario, when the newest message was drawn, the longest task and those over 1 second, the
slowest answer and the stuck ones; a screenshot of each is taken when the page can still draw. It fails on any task over
1 second, a stuck answer, a page error, a request leaving the origin, or a conversation not drawn within 30 seconds;
the last line is the verdict, and a watchdog ends a run that overruns with a timeout verdict.

## Local interface and code fonts (#10243)

```sh
PORT=<mock-port> python3 scripts/mock/mock_hub.py
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<mock-port> npx vite --port <web-port> --strictPort)
node web/e2e/font-preferences.mjs http://127.0.0.1:<web-port> <artifact-directory>
```

Use exclusively owned mock/Vite processes and a fresh Chrome profile. This drives the real Settings page at wide/phone widths, light/dark and standard/large text, captures Default and Custom, checks immediate and cross-tab application/reset, before-paint reload, zero additional font requests and actual CJK/symbol fallback faces. It mounts the production single-pane and Full UI renderers against the synthetic hub and checks their first font family, retained-view attachment and unchanged size; it does not verify a live shell. All network access is restricted to the supplied loopback origin. Results and sixteen screenshots go outside the repository; a four-minute watchdog bounds the run.

## Sidebar icon colors (#10822)

```sh
MOCK_EXTENSIONS=1 MOCK_NAV_ICON_COLOR=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/sidebar-icon-colors.mjs <loopback-web-origin> <artifact-directory> [--monochrome-only] [--chromium <executable>]
```

Against an exclusively owned mock-backed web server, in Simplified Chinese, light and dark, at 1440 × 900 and 390 × 844
(touch). With nothing stored (Monochrome), every page icon of the wide sidebar, the icon rail and the phone's drawer is
`--ink-3` and the open page's `--ink`. It then picks 彩色 in Settings › General › Appearance › 侧栏图标 and checks that
each built-in page's icon is its named color, that the made-up extension page naming `icon_color` (花园) is green and
the gateway's 用量 (none named) neutral, that the open page keeps `--ink`, that Inbox keeps its orange under the pointer,
and that every colored icon reads at 3:1 on the sidebar and on the hover fill, as the browser resolves them.
`--monochrome-only` takes only the Monochrome part, to set its screenshots beside another build's. Screenshots and
`results.json` go to the artifact directory; a five-minute watchdog ends the run, and the last line is the verdict.

### Native fetch receiver and Settings

After building the web bundle, run `node web/e2e/native-fetch-settings.mjs <artifact-directory>` with an output directory outside the checkout.
The runner starts its own loopback HTTP hub fixture on an ephemeral port and opens the actual Managers settings page in installed Chrome, in light and dark mode. It uses a fresh browser context and synthetic credentials, blocks external requests, rejects HTTP writes, and leaves native browser fetch untouched. It checks that hub-provided manager data reaches the settings UI, that health and explicitly bound fetch succeed, and that the original member-call pattern really throws `Illegal invocation`. JSON results and screenshots stay outside the repository. This verifies browser transport and rendering against an isolated fixture, not a deployed hub or account.

## Click a word to look it up (#10977)

```sh
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<unused port> npx vite --port <web port> --strictPort --host 127.0.0.1 &)
node web/e2e/text-lookup.mjs <loopback-web-origin> <artifact-directory> [--chromium <executable>]
```

Opens the real app with every hub request and the socket answered in the browser by invented data. In light and dark
it clicks a word in an assistant reply, an Agent transcript and an Inbox item and waits for the word card with its
meaning; it checks the saved-word underline is painted (Custom Highlight ranges, one style element, the text node
not split); and with `available: false` and with no `text_lookup` feature it checks the same click adds no card, no
style and no highlight. Fails on any page error. Writes `results.json` and screenshots; a 180-second watchdog ends
the run with a timeout verdict.

## macOS desktop Frosted Glass backdrop-filter suppression (#17761)

```sh
(cd web && npm run build && npx vite preview --host 127.0.0.1 --port <web port> --strictPort &)
node web/e2e/desktop-glass-backdrop.mjs http://127.0.0.1:<web port> [--chromium <executable>] [--shots <artifact-directory>]
```

Opens the built web client in headless Chromium with every hub HTTP/WebSocket request answered by synthetic data. Verifies in both light and dark modes under `data-surface-style="liquid-glass"` + `data-desktop-glass="1"` (including split-panel `<aside>`, Palette, Modal, and ImageViewer) that every visible `[class*="backdrop-"]` node resolves `backdropFilter === "none"`, while Web-only Frosted Glass (`data-desktop-glass` unset) retains shell and utility blur and Default appearance remains unblurred.
