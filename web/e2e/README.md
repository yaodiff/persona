# Bounded Agents list verification

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
MOCK_TASKS=1 MOCK_MARKDOWN=1 MOCK_EXCHANGES=1 MOCK_MANAGERS=1 MOCK_RICH=1 MOCK_TASKS_SENT=1 PORT=<port> python3 scripts/mock/mock_hub.py &
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
covers no header words of a table that fits (item 77). `--big` runs against the mock started with `MOCK_BIG=1` instead
(item 90): opening the "worked 1 h 10 min" fold, no animation frame draws more than 60 rows at once, all 650 rows are
drawn after it with the header where it was tapped, and a search for `make step-600` lands on that row inside the closed
fold. Only the mock's own reads leave the browser; other origins are blocked. Screenshots and `results.json`
(`results-big.json`) go to the artifact directory; a 240-second watchdog ends the run, and the last line is the verdict.

## User-input navigation acceptance

With web dependencies and Chrome already available, point the runner at an **already-running, exclusively owned loopback web server** backed by `scripts/mock/mock_hub.py`. Start the mock on an unused loopback port and configure the web server's `PERSONA_HUB_URL` for that mock; a Vite server proxying a real hub is not acceptable. The runner does not install dependencies, start services, create conversations, or stop the caller's services.

```sh
node web/e2e/user-navigation.mjs <loopback-web-origin> <artifact-directory>
```

The URL is an origin without a route or token. The runner uses `playwright-core` with the installed `chrome` channel, checks the mock's HTTP health and WebSocket hello, and opens the actual app's conversation page. Browser routes supply entirely invented inputs and answers, including instructions without question marks. All browser HTTP writes and WebSocket client commands are intercepted rather than forwarded to the hub; unrelated mock messages are suppressed, external requests are blocked, and no existing browser profile or account is used. It does not exercise model delivery or claim production-hub acceptance.

Checks run in light and dark at 1600 × 900 and a mouse-driven narrow desktop width of 1024 × 820. They verify the bell-shaped mark profile, 16px card radius, roomy padding, bold single-line input and two-line excerpt, card/rail hover transfer and immediate removal when both are left (measured from the actual mouse-out event to DOM removal, independently of the rail's idle timer). They also cover body-scroll reveal, right-edge geometry, a single current-input `aria-current`, input-plus-short-answer hover previews, clicking, dragging between horizontal marks and releasing to jump, Arrow/Home/End and Enter through the focusable navigation region, accessible input labels and selected-input descriptions, Escape cancellation without interrupting the session, fade to zero after roughly two seconds of inactivity, and pointer/Tab recovery through the retained edge trigger. A separate 240-input conversation traverses **every** mark by keyboard and jumps to the first, middle and last inputs; wheel navigation also verifies that a bounded window can move both forward and back to the earliest input. Windowing may bound the rendered marks but must not discard earlier inputs. Every screenshot is checked for page/console errors and Vite overlays, and all runs reject failed reads and external traffic.

Artifacts include `results.json` (per-check verdicts, timings, geometry, reached-mark count, intercepted request metadata and errors) and light/dark screenshots of scroll, hover, click, drag/release, keyboard, idle and edge-focus states, plus the long conversation's earliest/middle/latest inputs. The runner exits nonzero on failure or its four-minute timeout and prints a final one-line verdict. The caller must run it against the completed implementation, actually inspect the images, and upload the evidence; writing or syntax-checking this script is not browser acceptance.

## Conversation scrollbar and navigation clearance (#4803)

```sh
node web/e2e/chat-scrollbar.mjs <loopback-web-origin> <artifact-directory>
```

Use the same exclusively owned mock-backed server as the navigation runner, preferably `vite preview` after a production build. All app HTTP/WebSocket traffic is answered by invented data; no writes reach the mock or a real hub. The runner checks 1600, 640 and 390 px pointer layouts, touch-only 390 px, the largest app text size, light/dark, reduced motion, forced colors and an unsupported-scrollbar-color fallback. It measures independent edge/rail hit areas and content clearance, scroll/idle colors and unchanged geometry, and drags the **native** thumb to verify real scroll movement. The 180-second deadline prints a final success/failure/timeout verdict. Screenshots and `results.json` stay outside git.

Presentation contract: `NAV_RAIL_METRICS.edgeGap` moves the existing 36 px navigation hit strip 16 px inward from the native scrollbar gutter; mark width calculations and all navigation gestures remain unchanged. Only missing right-side content padding is added on narrow layouts, so an already roomy centered column stays centered. `useChatScrollbar` changes native thumb color without cancelling input, changing scrollbar width between states, replacing the thumb or rerendering chat content. It reveals while scrolling, retains visibility over the scrollbar's edge or during a drag, and fades after 1 second of inactivity through `--chat-scrollbar-fade` (220 ms, disabled by Reduce Motion). The track stays transparent. Unsupported browsers and forced-color mode keep native presentation. This scoped fix reuses native browser scroll controls rather than introducing a second scroll widget, preserving platform keyboard, touch and drag behavior.

**Headless Chrome pitfall**: Playwright adds `--hide-scrollbars` by default. This runner removes that argument; otherwise screenshots can show no native thumb and a zero-width gutter even though CSS reports a visible color. Always inspect the actual scrolling/idle images as well as computed styles.

## Progressive user-input history (#4836)

```sh
node web/e2e/user-history-navigation.mjs <loopback-web-origin> <artifact-directory> [input-count] [main|session] [initial-body-count] [standard|large]
```

The production-built app uses only synthetic intercepted HTTP/WebSocket responses, not a live hub. The default fixture has 678 genuine inputs, with automated supervision and compaction records between them, and only two input bodies in its first page. In wide/narrow light/dark runs it verifies 50 initial marks with full-history ordinals, no body reads on metadata loading or hover, unloaded-target landing through backward pages, and more marks after upward scrolling. Pass `36` for the same short-history check: all 36 marks are present. Pass `120 main 120` to restore a complete larger first page: it still starts at 50 marks, and reaching its earlier body content reveals the remainder without an unnecessary backward request. It saves inspected-state screenshots and `results.json`, and ends with a pass/fail/timeout verdict.

## The sidebar's Agents groupings on a paging hub (#6662)

```sh
MOCK_SESSIONS_LIST=1 MOCK_SESSIONS_CATALOG=1 MOCK_MANAGERS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
node web/e2e/agents-paged-groupings.mjs <loopback-web-origin> <artifact-directory> [--before] [--channel chrome]
```

Run against an exclusively owned mock-backed web server. It checks the one grouping bar (待办 / 机器 / 仓库 / 项目 / 状态) over the paged and the complete list, By to-do paged as a section per open to-do (open) and 没挂待办的 (folded) against the mock's `todo` counts, By machine and By state paged, and By repository and a pin taking the complete list; it saves light and dark screenshots. `MOCK_MANAGERS=1` keeps one assistant, since with several the sidebar reads the complete list. The runner clears the mock's default pins for the run (they also keep the complete list), sets one for the pinned case, and puts them back. `--before` takes the same screenshots of a build from before #6662 with no checks. (It replaces `agents-browse-by.mjs`, whose sidebar view by catalog project the sidebar no longer offers; `PagedSessionList.groups.test.tsx` still covers that view.)

It reads the mock's own catalog and holds the page to it, in English at 1280 x 900 (light and dark), a 240 px sidebar and a 390 px touch phone: Browse by Computer draws one section per computer, the busiest first, the first three open on their newest six sessions and the counts adding up to the list's total; Project lists the catalog's projects in its order, the repository on two computers as one section with its remote and both directories and the sessions of both; the value pill's menu is as wide as its pill and names each computer under the project; a picked project is the one flat list of its sessions; Space names each computer's spaces, tells two of one name apart and ends with the sessions in no space; the chosen Browse by survives a reload; Recently updated is the flat list. With `--hub` it also drives 20 s of the mock's churn while the list is drawn by project and prints the mock's read counters by kind, failing when the groups were read more than once per 1.5 s or a complete list was read. Screenshots of each step go to the artifact directory. A three-minute watchdog ends the run with a timeout verdict. Browser and mock checks only: no real hub, node or Git repository is involved.

## Machines and Overview at desktop widths (web audit items 3, 25, 30 and 76)

```sh
MOCK_SESSIONS_LIST=1 [MOCK_MANAGERS=1] PORT=<port> python3 scripts/mock/mock_hub.py &
(cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort --host 127.0.0.1 &)
node web/e2e/overview-machines-layout.mjs http://127.0.0.1:<web port> <artifact-directory> [--channel chrome]
```

Run against an exclusively owned mock-backed web server, once with `MOCK_MANAGERS=1` (one assistant) and once with the mock's default roster (several assistants); `MOCK_SESSIONS_MANY=300` adds a list longer than its first page. The runner reads the mock's own complete session list (every page, `roots=1`) and holds each Machines card to it: the agents that are not offline, without the assistant's conversations, on a hub that pushes no sessions. At 768, 1024, 1280 and 1920 px, light and dark, it checks every online card's count, a gap of at least 8 px between the card's state line and its versions, and that no metric value is cut (the text's own width against its box, since `scrollWidth` rounds away the half pixel Chrome already cuts with "…"), with the widest rate a card can show ("↓ 1023 MB/s", "↑ 1000 KB/s") answered for one computer in the browser, and that a card's metrics are one row or two columns, never three with one left alone (one row at 1280 and 1920 px). On Overview it answers an example extension's overview card in the browser (its place, list and view, with the UI-slot features added to the hub's hello) and checks that a lone card is the page's width at 1280 px and one of two columns at 1440, 1680 and 1920 px. Then it fails the list Overview counts from in the browser (the paged list's reads with one assistant, the complete list's with several) and checks that the summary says it couldn't read the agents, in the line's live region, with **Retry**, with no **All set** and no computer count; after the route is lifted, **Retry** brings the summary back, its counts outside the live region (left empty), and each online computer's count matches the mock's, or, while the paged list has more pages, no count. Nothing is written to the hub. It saves a screenshot of each page and size, prints one line per check, and ends with a verdict; it exits 1 on a failed check and 2 on its three-minute timeout.

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
