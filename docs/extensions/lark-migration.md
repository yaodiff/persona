# Bringing Lark features in (#392)

> **Since 2026-11-04 (#1520, plugin step 3, 3c)**: the whole path that connects Lark through the Lark gateway (the App's `/api/v1/lark/…`, the Lark source for the Inbox and knowledge base, Moments,
> the assistant's `ol` gate and credentials, /agent, personal Agents) is no longer in the open-source repository; it is provided by the extension that connects Lark (`docs/extensions/extensions.md`, `docs/plans/plan-extensions.md` ("Step 3")).
> The behavior described below is provided unchanged by that extension; the hub source files mentioned here (`hub/lark*.go`, the `internal/lark` client…) have been deleted. The open-source version keeps only the official lark-cli, the Lark bot and Lark Mail.

Build on Persona's native Inbox, hub authentication, chat page, outbox and live sync, and bring missing features in one at a time; don't maintain two complete clients side by side.

## Entry point

Reuse **Settings → Connectors → Lark**, and while the account is connected add a **Show Messages on Home** switch, off by default.
When on, the top of the iPhone home screen shows **Assistant / Agents / Messages**; when off, it goes back to the original two pages. The setting is stored on the device and survives restarts.
The regular-width iPad gets a matching **Messages** tab; it shares the hub's Lark account, so no second sign-in is needed.
On the retired native Mac, the same switch added a **Messages** section above the Inbox in the sidebar: chat list on the left, chat on the right (#3162, [mac.md](../archive/apple/mac.md), "Main window").

- The Inbox keeps doing cross-source aggregation, AI triage, drafts and to-dos; the Lark filter keeps its original purpose.
- **Messages** browses all direct and group chats; tapping one reuses `LarkChatView`. Swiping right in a chat returns to the message list; the list and the neighboring home pages switch with a horizontal swipe.
- The top right of **Messages** opens the existing Lark settings. If the switch is turned off on that page, the settings stay open; closing the settings returns to **Assistant**.
- The list first shows a cache isolated by hub address and device credentials, then updates through the existing session API; live changes update the loaded page window, and the cache is replaced once a whole batch succeeds; a failed refresh keeps the existing list, and a successful one removes chats you've left.
  The list does not add every chat to background message prefetch; prefetch keeps the existing Inbox / recent chats policy.
- Search is explicitly limited to loaded chats. When not connected it goes back to the Lark settings; on network errors it keeps the cache and offers a retry.
- Quotes, thread replies, read state, media, time grouping and message alignment follow the mainline implementation.

## Later scope

### Pinned chats on the home page (#949 phase 1)

The top of the Messages page shows the Lark gateway's full set of pinned chats, with order and unread counts following the server. By default it's one row of avatars; when there are more than fit in a row, tap **Expand** or pull down and release to expand, and pulling down again doesn't refresh messages. Once the expanded area scrolls off the top it collapses automatically; scrolling back keeps it collapsed, without requiring a return to the very top of the page. Tapping an avatar uses the same chat page and back path. Pinned chats appear both in the avatar row and in the list below (as in the official Lark client): the hub's chat pagination puts pinned chats first, and the list puts them back where they belong by their latest message time; ones older than the last loaded chat, with more pages left, appear once that page is loaded. While searching, the avatar row is hidden and results include pinned chats as usual (the de-duplication in #1307 was a misunderstanding and has been corrected). Expand / collapse uses `Theme.Motion.expand` (0.4 s), slower than state changes so the eye can follow.

Pinned chats and the loaded chat pages form one snapshot, isolated by hub address, device credentials and the Lark account the server confirms. The cache is shown first and read silently in the background; on failure the last complete snapshot is kept. Switching accounts or signing out removes the old account's data immediately, and late responses can't be merged into the new account. Live replacements for the same account are deferred while dragging, then the latest snapshot is applied after it stops, keeping the reading position. New data does not trigger a whole-page refresh prompt.

This requires hub support for `account_bound=1`; when an old hub doesn't return account ownership, records without an owner are not stored in the new cache and the user is asked to update the hub. Reading the Lark gateway's actually selected account and checking it before and after the page read is not a database transaction spanning sign-in operations. Pins inside chats are phase 2 below; this phase does not include editing the pinned chats on the home page.

### Pinned messages in chats (#949 phase 2)

The Pin button at the top right of a chat opens a native list that pages automatically near the bottom. Tapping a pin first confirms the original message can be located, then closes the list and scrolls to and highlights it in the original chat; thread replies use the existing thread page. If the original message can't be found, it stays in the list and does not lose the chat's reading position. Closing the list doesn't change the reading position either.

Long-pressing a message that supports it offers **Pin**; it can be removed from the list. Both require confirmation. Permission comes from the capabilities of the current message from the Lark gateway; before writing, the hub checks again that the original message, chat, account and pin match. Recalled or deleted messages can't get a new pin; whether an existing pin can be removed is still up to the server's permissions. A timeout or lost receipt is not retried automatically; the list is reread first to confirm the result.

The list reuses the OLK external display, decrypted body and keep-recalled settings; when the original message is missing or not permitted, it keeps an understandable placeholder. The pin list lives only in the current page's memory, responses are `no-store`, and there is no separate plaintext cache on disk. Offline, an open list keeps the last complete data; a cold start offline cannot restore the pin list. Switching accounts or signing out clears the old content, and late responses can't refill it.

The Lark gateway's pin list GET **marks pins as read**. So it's only read after the user opens the list or explicitly acts on a pin, not prefetched on the home page, and automation never runs against a real account. Subscribe to `pins.updated` and ignore `pins.read-state.updated`, so reading the list doesn't trigger an event that reads the list again in a loop. An old hub / old Lark gateway each return a specific upgrade prompt; a missing original message is not misreported as an outdated version.

Locating a normal message uses an around window; thread replies use the Lark gateway's existing before cursor for at most 20 pages of 100 messages each, and beyond that it says clearly that the message can't be located for now. Checking the account before and after the read is not a transaction across upstream sign-in operations, so a very short account-switch race remains; don't describe this check as an atomic upstream write.

Verification: `LarkChatPinsTests`, `LarkChatPinFlowTests` and the Go pin API tests cover paging, account switching, OLK redaction, keeping data on failure, locating, and unknown write results; screenshots and execution boundaries are in Pin message verification.
### Automatic history paging (#1033)

In normal group chats and direct chats, scrolling up to within half a screen of the top of the loaded content automatically reads the next earlier page; there is no more tapping **Load Earlier Messages**. Only one request runs at a time, and the top shows a single small spinner. A failed read shows one tappable retry row and doesn't retry automatically; an empty page, a duplicate page or a cursor loop keeps what has been fetched and stops until tapped, so no request loop forms. When there are no earlier messages, nothing shows at the top. Topic groups keep the original manual button (#333's topic paging is unchanged).

After a page is prepended, the message being read stays at the same place on screen (to the pixel): when the page lands, the message in the middle of the screen is taken as the anchor, only that row reports its position in the content, and the list shifts by however much the content above it changes (`LarkHistoryHold`, the same approach as `ChatPin` in Agent chats). New messages arriving, finger drags and momentum scrolling work as usual meanwhile; when following the newest end (short chats), the original follow logic takes over. Keeping the scroll position by id and `defaultScrollAnchor(for: .sizeChanges)` were both tested and don't work; see `docs/lessons/ios.md`.

History requests carry the Lark account `account_id` that the current chat belongs to (with `ol_account` when there are several accounts); the extension that connects Lark checks that account before and after reading, and the response carries `account_id` and is `no-store`. If the response's account is wrong, or an old version returns no ownership, it is not merged, with the prompts "Your Lark account changed. Open the chat again." and "Update the hub to load earlier messages" respectively. Old versions only affect history; reading the latest messages is unchanged. Leaving the chat, going to the background, switching accounts or jumping to a pin's location cancels the request, and late responses are not written back.

Verification: `LarkHistoryPagingTests` (unit), `LarkHistoryFlowTests` (simulator + isolated mock `MOCK_LARK_HISTORY=1`, light / dark): position kept after prepending, retry on failure, stopping on duplicate / empty pages / cursor loops, stopping after a short page is auto-filled, no late responses landing after leaving or switching accounts, direct chats, flicking fast to the earliest, no movement when new messages arrive while reading, single line at large text sizes; tests for the account check and unchanged old calls live in the extension that connects Lark.

### Jump to latest (#1034)

Tapping **Jump to Latest** goes back immediately to the newest messages already on the device, without waiting for the network: when the window moves away from the newest end (opened at the unread position, jumped to @Me, located a pin), the newest 80 messages at that moment are remembered; if there are none, the chat's local cache is used, whichever of the two is newer. After reaching the bottom, it silently fetches the latest page once, with no spinner: if it connects to the local tail (the latest page starts right after the tail, overlaps it, or is all the messages), new messages are appended in order and de-duplicated; if there's a gap (many messages arrived while away) and you're still at the bottom, the latest page replaces what's there (scrolling up auto-pages as usual); if you've already scrolled up, the position you're reading is left alone and only a small progress indicator hangs at the bottom, and the gap is filled in order when you scroll to the bottom. Only when the device has no latest message at all does it wait for the hub, with the button showing loading. The button hides once you're truly at the bottom.

Verification: `LarkPagerTests` (keeping the tail page, taking the newer of the cache and the kept copy, connected / gap, filling in order after a gap), `LarkJumpLatestFlowTests` (simulator + the "跳转测试" (jump test) chat of `MOCK_LARK_HISTORY=1`, with a new chat id per test case so earlier caches don't interfere; holding back the latest page proves it doesn't depend on the network).

Sending images and files is tracked separately in #410 and extends the existing chat page. Contacts are on hold at the user's request.
Differences such as encrypted messages and cached scrolling are verified separately later, each in its own issue.
Claude / Codex, host connections and the terminal keep using the Persona architecture.

## Verification

- `LarkConversationsTests`: cache first, page window refresh, unread updates on the tail page and removal of chats you've left, concurrent refresh and load-more, page de-duplication and stopping on a repeated cursor, keeping the list offline and on cancel, credential isolation.
- `DemoHubTests`: demo-mode chats decode with the real model and can read chat history.
- `LarkEntryTests`: switch on → Messages on home → chat → swipe right to go back; swiping from Agents to Messages; opening settings from the Messages page and turning the switch off; switch state kept across restarts; two pages kept by default.
- `LarkMessagesShellTests`: swiping across three pages, cancelled gestures, overscroll on the last page and removing the third page.
- iPhone 18 Pro simulator, light and dark; screenshots. All with mock data; no test messages were sent to real accounts.
### Re-verifying against the mainline shell (2026-11-03)

Adapted to mainline `a2e28039`'s single page state in `ShellState`, synchronized routing, lazy creation and top bar animation guards; the three-page switch reuses these mechanisms, and the Mac doesn't get the iOS-only tab. The combined build passed 699 unit tests; then 15 targeted shell and encrypted-display tests passed. On iPhone 18 Pro, three entry-flow runs each passed in light Glass and dark BeautifulUI, and eight screenshots were updated and checked. iOS (including Watch / extensions) and Mac arm64 builds passed with Xcode 26.0.

The GitHub Actions smoke run was blocked by an account billing / quota limit and is not counted as passing; the results above are local. New review items in the attachments and persistence PRs are handled separately and are not claimed as done in this PR.

## Message Tweaks display (#882)

The entry point is **Settings → Connectors → Lark → Message Tweaks**; no new home page is added. Keep-recalled-messages and edit history share the Lark gateway's settings for the current account; the hub returns only these two booleans and the actually signed-in user ID, and doesn't forward script, stand-in or auto-send settings.

- Keep recalled messages shows the original content the Lark gateway kept, while keeping the **Recalled** label. It can't be restored when nothing was kept or only a system notice exists. The recall flag still drives permission checks; showing the original doesn't re-enable replies or reactions.
- Edit history offers the **Edited** entry only when enabled, and shows the versions that were kept. Encrypted content in the body, quotes, forwards and versions keeps using the OLK projection; when locked, only the public external display is shown. iOS's version cache strips decrypted bodies just like messages / quotes do; old caches are also cleaned of OLK bodies and forward previews before being read.
- The switches are held by the model for each hub connection, and responses are bound to the actual account; writes carry the account and the old value. Conflicts and unknown results are not retried automatically; after rereading, the user decides. When the settings can't be confirmed, kept content / the edit entry are hidden; a cold start offline doesn't borrow the switches of another account or the last connection either.
- The `local.settings.updated` event triggers a reread; when the hub merges a batch of messages it keeps a separate `settings_changed`, so settings changes don't depend on an empty chat list. Entering a chat, returning to the foreground and foreground polling also read the settings, for compatibility with old deployments that don't push this event.
- The Lark gateway needs to support `PATCH /agent/v1/settings/local/message-display` (upstream #987). Old deployments can still read the existing switches; changing them explains that an upgrade is needed.

These two switches only control display, matching the web semantics; they don't control retention or access authorization. After turning them off, the server doesn't delete kept content; clients with permission to read messages still receive recalled originals and history versions, and ordinary unencrypted versions may still be saved in the phone's cache. OLK decrypted bodies separately follow the never-written-to-disk rule.

The Inbox's AI triage and the assistant's quoting don't yet have a unified policy for recalled content, and may still put recalled `Text` into context; this is handled separately as follow-up to #882, and this display switch must not be described as AI access control.

Migration list:

| Feature | Current plan |
| --- | --- |
| Keep recalled messages, edit history | This phase, #882 |
| Encrypted body, external display, key distribution prompts | #764 merged |
| Unlock on phone, encrypted sending | #689 migrated; protocol in [encrypted messages](lark-encryption.md) |
| Disable read receipts | Later; must cover both chats and threads, and an old web switch must not be passed off as supported on the phone |
| Auto-recall, auto-signature, stand-in | Stay under the Lark gateway's existing server-side control; must not be turned on automatically by the migration |
| Watermark, Moments anti-deletion, desktop floating window, decorative counters | Web features; not copied as phone messaging features |

The associated PR records simulator flows for keep-recalled-messages and edit history; development screenshots are not stored in this tree.
