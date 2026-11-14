# Lark's role in Persona

> **Since 2026-11-04 (#1520, plugin step 3c)**: the entire Lark-gateway path (app `/api/v1/lark/…`, Inbox / knowledge source, Moments, assistant ol gate / credentials, `/agent`, personal Agent) moved out of open-source repository into Lark extension (`docs/extensions/extensions.md`, `docs/plans/plan-extensions.md` “Step 3”).
> Extension preserves behavior below; referenced `hub/lark*.go` / `internal/lark` sources removed. Open-source keeps official `lark-cli`, Lark bot, Lark mail.

**Persona is not a second Lark.** It gathers life / work information for AI processing; Lark is one source.
(User correction 2026-10-27: previous Lark-like Workbench—search, cloud docs, calendar, meetings, Minutes, tasks, approvals, OKR, contacts, Moments feed, mail, favorites, topics, chat info, Today—entirely deleted.) Do not restore those pages.

**Explicit #392 exception**: off-by-default Show Messages on Home in Lark settings adds Messages for browsing all direct / group chats. Shared accounts / chat pages / outbox / sync with Inbox, no second implementation; other Lark pages stay out. [Lark capability consolidation](lark-migration.md).

**iOS Messages filters (#6553)**: Messages is initially selected and keeps the pinned shelf. Its neutral Messages / Unread segmented bar follows the shelf and is a native plain-list section header: the shelf scrolls away and the bar stays under the page toolbar. The two buttons reuse `Theme.Font.caption[Strong]`, `Fill.well` / `Fill.pressed`, `Radius.control` and `tapTargetHeight()` (at least 44 pt). The group hugs its labels at the leading edge; its visible fills sit inside the full-height touch regions, and the header adds no extra vertical padding. English uses the shorter All label and Japanese uses すべて so both choices fit on one line at accessibility text sizes. This grouped filter pattern replaces the separate accent `FilterChip` pills to match the requested Lark iOS hierarchy while retaining full-height touch targets. Unread shows only unread or manually marked-unread chats, including matching pinned chats as ordinary rows. Switching never reports a chat as read. The account-bound gateway uses its `unread` scope (and its normal muted/done/boxed exclusions); generic messaging sources, whose contract has no scope parameter, filter each ordinary page by unread count or the manual flag and retain its cursor. A read-only page is not an empty unread list: the visible footer continues through up to eight older pages before offering a button to continue. Search stays within the selected list. Filters reset their rows, cursor and pending requests on a switch; only All uses the existing hub/account-specific disk cache. The filter drawer, Marked and custom labels are outside this change.

Verification: `LarkUnreadTests` covers scope/cursor isolation, late replies, manual unread, retry, account changes and read events. `LarkUnreadUITests` uses `MOCK_LARK=1 MOCK_LARK_UNREAD=1`; add `MOCK_MESSAGING=1` for generic page filtering. For its bounded-scan assertion, also set mock `MOCK_LARK_UNREAD_PAGES=9` and runner `TEST_RUNNER_PERSONA_EXPECT_UNREAD_CONTINUE=1`. `MOCK_LARK_LIST=180` supplies a paginated list for the existing optimized `PerfInboxTests/testFeishuMessagesListScroll` measurement.

For a visibility check, add `MOCK_LARK_UNREAD_PAGE_DELAY=0.5` to the long generic fixture. Switch to Assistant after cursor 60 starts: the in-flight page may finish, but cursor 120 must not start until Messages is visible again. Scans use live mounted/tab/scene state and pause while a chat is open.

Three optional hub integrations: Lark gateway (third-party user-authenticated web client through private extension), official `lark-cli` (`docs/plans/plan-lark-cli.md`), and a private Lark Agent extension. This document covers gateway; its deployment / variables / commands in extension instructions.

## What stays

| Capability | Location | Notes |
| --- | --- | --- |
| Messages | Inbox (`docs/assistant/inbox.md`), optional Messages | Unread / mentions / direct chats mixed with mail, event-grouped / AI-sorted; opens `LarkChatView` |
| Document links | `FeishuDocReader` (`Views/Feishu/Docs/`) | Native in-app rendering from any link, `LinkSheet` / message. With `lark_doc_comments`, comments reply threads / whole doc, resolve / reopen, mention people (#1290, `FeishuDocsComments.swift`; web `docs/web/web.md` “Links open inside the page” (**Lark doc comments**), `docs/protocol/protocol.md` Lark APIs) |
| Moments links | `LarkMomentsCard` / `LarkMomentsPostView` | Shared-post preview opens one post / comments; no browsable feed |
| User-related Moments | Inbox via extension, formerly hub inbox_moments.go; knowledge | Comments on own post, replies / mentions: one event per post, AI sorting / comment drafts, Send posts comment back (`docs/assistant/inbox.md` “Moments”) |
| Other links | `LinkSheet` WebView | Minutes / events / tasks / approvals in in-app web cards |
| AI Lark tools | Hub, `server/internal/lark` / assistant `lark_api` | Read-only search / read messages, docs, Moments, calendar, Minutes as context |
| Persona in Lark | Bot (`docs/extensions/bots.md`) | DM self-built bot = main chat; extra Persona entry, not copied Lark feature |

`FeishuLinks.route(_:)` routes docs→`.doc`, Moments `…/client/moments/detail?postId=`→`.momentsPost`, others nil→web. `FeishuRoute` only chat / doc / profile / momentsPost, destinations from individual messages / links.

## Data paths

- `GET /api/v1/lark/raw/<path>` = gateway `GET /agent/v1/<path>`, read-only; JSON `/api/...` media rewritten to hub. POST only `search/page` / `search/mention-history` (large-body read search). Assistant `lark_*` MCP same `lark.Read` allowlist.
- **Assistant calls gateway itself** (2026-10-30), gateway skill from extension since #1571, no repo copy. Commands directly call agent API for reads / writes; node gate confirms externally visible writes on phone. `docs/assistant/assistant.md` “Lark: skill supplied by the Lark extension”. Quick reference below supplements local tests; gateway skill references more complete and authoritative.
- Chats / messages / read / send / reactions / card buttons use dedicated hub routes (`docs/protocol/protocol.md` Lark APIs).
- **Without gateway, official `lark-cli`** (`docs/plans/plan-lark-cli.md`,#273): assistant integration and the `larkcli` connector are available, with `lark.cli` in the manager's manager.json, assistant PATH, and the same write gate (`docs/assistant/assistant.md` “Lark: `lark-cli`”). Watched document comments enter the Inbox as `kind: "doc"` (#2691); chat Inbox / knowledge / `/agent` / app Lark pages are added progressively in the L4 branch and later plan steps.

## Gateway and hub on separate computers

Gateway Agent API remains source-loopback-only. If source can reach hub, dedicated SSH user reverse-tunnels hub loopback port to source Agent API. Source user systemd service `Restart=always`, `ExitOnForwardFailure=yes`, SSH keepalive, linger, independent of developer machine. Pin hub SSH host key.

Hub tunnel user public-key / remote-forwarding only: `GatewayPorts no`, PermitListen 127.0.0.1:<tunnel port>, `PermitOpen none`, `MaxSessions 0`; disable TTY / agent / X11 / Unix forwarding. Key restrict,port-forwarding,permitlisten="127.0.0.1:<tunnel port>", ssh -N -T. Check `sshd -t` / `sshd -T -C` user=<tunnel user>,host=localhost,addr=127.0.0.1 before reload, preserve admin account.

The manager's `manager.json` lark.base_url=http://127.0.0.1:<tunnel port>/agent/v1, `lark.token`,0600. Transfer token securely from source env, never print / commit, backup config. Verify loopback listener, unauth health 401, authorized health / chat reads, `/events` WS, repeat after restart. No sending / read-state changes required.

Same-machine Docker gateway / hub may use configured `lark.container`, no default, reading agent token from container env rather than config; recreated-token 401 rereads. Now extension owns this, see instructions.

Distinguish two 401 types (2026-10-31): gateway lost Lark login returns `AUTH_SESSION_EXPIRED` / `AUTH_*`, valid agent token. Old hub discarded configured token on every 401 then inspected default Docker container, breaking all requests / live events until personad restart. Now `AUTH_*` gives Gateway Not Signed In to Lark (API 502 `lark_not_logged_in`, logs / Inbox / app; extension logic). Never discard configured token. Event reconnect backoff max 1 minute, same error logged once, auto-recovers on user login.

## Multiple accounts

Gateway x-multi-account `GET /agent/v1/accounts`; choose account via gateway header / ol_account. Account number is accounts.id, not Lark user ID; added-account resource URLs include ol_account. Persona reads all (#1321, previously `lark.account` only):

- **Default account**: `/api/v1/lark/*` requests without `ol_account` use the default account from the extension's own Lark-gateway configuration. Hub does not pass `manager.json`'s `lark.account` to the extension. The Inbox also follows the extension's default: the manager reads `default` from `GET accounts` on the extension messaging source `lark`. If unavailable, it uses the last successful value; only if none was ever obtained does it fall back to `lark.account`, or primary account 1 when omitted (#5098). Old Android / web clients without `ol_account` still see only the default account. Assistant `lark_*` tools / `ol`, knowledge, `/agent`, personal-Agent settings, and document reading also use it; `lark_messages` can pass `account` to read other accounts, and the extension documents account selection for its commands.
- **Per-request**: all Lark APIs accept `ol_account`=<number>, extension-provided, formerly lark_accounts.go per-account clients. Invalid 400, nonexistent gateway rejection, never default fallback. Preserve media ol_account. Raw proxy forwards query, not client account header; never query in base_url.
- **Account list** `GET /api/v1/lark/accounts` fetches at most once/minute, selecting logged-in nonquarantined Inbox / events accounts. Old gateway without accounts treated default-only.
- **Events**: default connection, others reconciled every 2 minutes. App lark events carry account, nondefault `ol_account` for cache targeting. `/agent`, personal Agent, knowledge only default events.
- **Inbox**: sequential each signed-in account unread / mentions. Default errors fail round; other errors log only. `lark.account_id`, old 0=1; `lark.ol_account` for nondefault app open / reply. Same group in two accounts distinct events. Multiple sources “Lark · <name>”; reply as item's account. Never change Lark read state.
- **iOS**: `APIClient.lark(_:)` adds `ol_account`; `LarkConversation.account` follows chat / topics / profiles / cards / mentions / previews. Cache / listeners keyed `LarkConversation.key` a<number>-<feed>. Multiple-account Messages capsules with unread counts, independent lists / pins; subtitle Lark · <name>. Settings › Connectors › Lark lists all.
- **Web**: `web/src/lib/lark-accounts.ts` list / browser selection / `larkPath` query. `LarkChatRef.account` follows reads / read reports / sends / replies / topics / pins, accepts same-account pushes only. Messages account buttons below search, lists `larkChatsFor`, heading Group Chat · <name>. Inbox uses `lark.ol_account`; settings lists accounts. E2E `scripts/test/web_lark_accounts_e2e.py`, MOCK_LARK_ACCOUNTS=1. Android no switch, default only.

## API quick reference (tested on hub; assistant uses extension skill, gateway references authoritative)

### Search
- `search/page` POST body `{tab,query,cursor,limit,senderIds,chatIds,feedId,startTime,endTime,attachmentTypes,chatType,docTypes,ownerIds,sortType}` → `{items,nextCursor,total}`. Tabs from `search/tabs`: messages / cloud docs / apps / contacts / groups / mail / help desk / events. Query nonempty or 400; Unix seconds; `nextCursor` several-KB token.
- Quick search: `search?q=` messages, `search/people?q=`, `search/documents?q=`, `calendar/search?q=`, `mail/search?q=`, `minutes/search?q=`, `todos/search?q=` with token pagination; search/in-chat?feedId=&q=.
- Highlights UTF-16 offsets; negative message position means topic reply: `GET messages/:id` for `threadId` before navigation.
- Event hit ID <eventId>:<instance start>; `calendar/event/:id` only prefix before colon.

### Documents
- `documents/read?url=&format=markdown`;403 `DOCUMENT_EXPORT_FORBIDDEN` /422 `DOCUMENT_FORMAT_UNSUPPORTED` → `format=json` ProseMirror tree. Base / files / boards Open in Lark. Also `documents/header`, `documents/reader/info`, `documents/ai-summary` (read-only cache), `documents/comments`.
- Markdown export fixed start # Title + `[Source](<url>)`; images absolute gateway URLs rewritten to `/api/v1/lark/raw/documents/…`.
- Each list item separate `bulletList` / `orderedList`, continue numbering across siblings. Table colwidth pixels; implement rowspan placeholders. `FeishuDocRenderer.swift` / PersonaTests/FeishuDocsTests.swift.
- `authority.copy=false` disables selection.

### Calendar / meetings / Minutes
- `calendar/week?start&end` UTC …Z; `+08:00` gives 400. `calendar/event/:id` recurring uses sourceId.
- `meetings?count&cursor`, `meetings/:historyId`, `meetings/:historyId/insights` AI notes.
- `minutes?scope=all&cursor`, `minutes/:token`, `/transcript` `/summary-items` `/media`, hub Range forwarding for streaming.

### Tasks / approvals / OKR
- `todos?count` no cursor max 200; `todos/completed`, `todos/:guid/detail` lacks title, lookup list; task-lists. All-day due date UTC midnight.
- `approvals?tab=todo|done|initiated|ccUnread|ccRead&cursor&q`, `approvals/detail?instanceId`; external-system 502 APPROVAL_DETAIL_UNREADABLE.
- `okr/people`, `okr/owners/:ownerId/periods`, okr/:okrId.

### People / Moments / Lark mail
- `users/:id` profile, `contacts/org`, `contacts/departments/:id`, search/people?q=.
- `moments/posts/:id`, `/comments` dedicated hub route, `moments/feed?tabId=`, moments/notifications.
- `mail/threads?label=INBOX&cursor…`, required label; mail/messages/:id(/html). Base64 ID encoded exactly once in path, %2F.

### Chats
- `conversations/:feedId/settings` announcement, `/pins` `/resources?kind=docs|files|media` `/todos` `/members`; favorites; threads/feed.
- List pitfalls `docs/lessons/lark-mail.md`: unreliable `isDone`, `onTopRankAt` pins, unknown scope treated as inbox.

<a id="话题分页限制h06"></a>

### Topic pagination limit (H06)

Topic replies (`GET /agent/v1/messages/:messageId/thread`) page only backward: the gateway returns the latest page or the one before `beforePosition`, never a newer page (source review: `docs/archive/extensions/feishu-records.md`).

Hub topic route therefore has no after yet. Ordinary messages support `afterPosition`, but topic replies use independent `threadPosition`; cannot borrow ordinary cursor or append latest page as continuous. Android ↓ Latest Replies reloads / replaces current window; older pagination / post-reply refresh unchanged. Add newer pagination once upstream fetch / query / cursor contract exists. No real Lark / live hub accessed; conclusions from source review.

### lark-cli Inbox conversation details

The lark-cli connector exposes read-only `Threads` pages for the conversation keys
produced by its Inbox source (`larkcli:chat:<chat>` and the optional `:thread:<topic>`).
The CLI supplies each message ID, sender, source time, direction and older-page
cursor; legacy collected context is never split into guessed messages. When the
shortcut omits the conversation kind, the web page keeps the header neutral.

The web page renders the CLI's textual card envelope and safe Markdown using the
existing message renderer. Known merge notifications put the linked change and
description first; author, repository and original text remain available in
details. Long descriptions expand and collapse without discarding their content.
Unknown or incomplete envelopes remain literal text. A reply destination shows a
friendly name and a preview only when its message is present; its routing ID stays
in details and is unchanged.

**Open in Feishu** (or **Open in Lark**) is available beside AI draft reply when
the CLI supplies the original message's AppLink. Its chat or topic positioning
parameters are preserved. Missing or unrelated links do not produce an action;
opening the source does not send a message or replace the existing draft action.
For previously collected items, the loaded original message can supply the link
without a history backfill. A different message is never used as its substitute.

Conversation reads distinguish unsupported capability, missing access, rate limits
and transient failures, retaining collected content and previously loaded pages.
This is the text/card portion of #856 L6. Collection remains L4; attachments,
reactions and new write actions are not provided by these pages. Existing Inbox
reply actions and their confirmation gates are unchanged.
