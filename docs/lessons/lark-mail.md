# Lessons: Lark and mail

Pitfalls, odd behavior and the trade-offs made at the time (originally in `CLAUDE.md`; index in `docs/lessons/README.md`). Put new lessons in the matching section; if none fits, start a new one.

## Bot HTTP rejections with JSON bodies

A gateway can reject a bot request with valid JSON that omits Feishu's `code`, or even contains `code: 0` (#12643).
An integer's zero value is not evidence of HTTP success. After existing transient-status and business-code handling,
reject remaining non-2xx responses before decoding output or returning nil for an operation without output. Report
the operation and HTTP status without copying gateway details into the error; test through a real client and a local
fake gateway after a successful token exchange, including idempotency-aware retries and normal 2xx controls.

## Lark system events are not new conversation requests

A new message ID and timestamp do not establish new human activity (#9789). Consume system events and unidentified senders without creating or updating Inbox things, including the direct-chat sweep and history promotion path. Keep real attachments and independently authored bot notifications. When fetching conversation history for triage, retain timestamps and distinguish the incoming batch from background: an empty system event otherwise disappears from flattened context and makes an old request look current. An already collected follow-up remains background; promoting an unindexed follow-up must not rewind Last when another group of the same chat is processed in the same round.

Coalesce the entire incoming batch by conversation key before constructing an Inbox thing: interleaved chats must not turn an earlier new request into background or discard it when history cannot be read. Incoming message IDs own current/history membership even when history has different timestamps, positions or thread metadata. Empty attachments need the same bounded, normalized fallback in their preview and model context; localize the visible preview rather than exposing an arbitrary source type.

## Saving mail attachments

- Enforce a save limit while spooling, not after writing the attachment to disk (#9398). Keep reading and hashing the
  full decoded attachment when its identity depends on its content, so the ID still matches and the IMAP connection
  remains synchronized. Test a large attachment, the exact limit, a wrong ID, and a subsequent mailbox request.

## Sending attachments

- Don't extend the client_id idempotency of text / images to files: the Lark gateway's direct file upload doesn't go through the outbox, and an unknown result must not be uploaded again. An API success whose receipt is lost or can't be parsed is also unknown; only an explicit rejection allows a retry. Test mocks must tell a 429 rejection apart from a receipt lost after acceptance, rather than always returning 502 and assuming a retry is safe.
- When recovering from a force quit, clearing the draft and recording the pending send must be in the same atomic snapshot; copy the media first, then commit the reference. A failed save must not clear the input or start a network request. If the receipt arrived but writing it to disk failed, a message already sent must not be changed to "failed to send".

- Optimistic echo for attachments can't reuse plain text's "same text + delete after 20 seconds": an image's text may be empty, and files with the same name aren't the same message.
  Keep client_id and message_id, and replace once the echo is confirmed; the Lark gateway's local: staged messages don't count as send confirmation. Confirmation pushes that arrive after the request timed out must still be merged, rather than leaving a failure bubble forever.
- The native send queue must not serially wait for the polling delay of the message echo; only the network submission is serial, and echo sync runs independently. After a file is picked, hold a private copy until it is removed or the echo is confirmed, so retries don't reread an original file that may have changed.

- In iOS 27, Quick Look shows the close button first and then loads the file content; taps during the initial placeholder may not close the preview. UI regression tests must wait for the file-content toolbar to appear and assert the preview is really gone before operating the chat input, so they don't tap the preview's share button.

- To verify the back path of the new shell's Inbox, tap into the chat from the actual list. `-openInboxItem` rebuilds the path straight to the chat and leaves out the Inbox parent page;
  using that screenshot shortcut to test "go back to the Inbox and retry" gives a false result. Attachment regression tests enter from a real Inbox row, then verify leaving and recovery.

- An attachment's local row uses a UUID, while the server echo uses the message ID. If you only locate the local row after sending, the echo replacement loses the scroll target;
  follow the bottom edge for a just-sent message, and realign once the replacement is confirmed. Stop following as soon as the user starts dragging, so someone reading history isn't pulled back to the bottom.

### Inbox summary and the scroll position of attachment echoes

The summary above an Inbox chat changes the ScrollView's top inset. Using `contentOffset + containerSize` to decide "at the bottom" misses this: the image is already fully above the composer, yet **Jump to Latest** still shows. Compare `visibleRect.maxY` with `contentSize.height`. An attachment's local UUID disappears once the echo is confirmed; follow the bottom edge while sending, relocate after the replacement is confirmed, and stop following once the user starts scrolling. UI regression tests must assert that there is no jump-to-bottom button after the image is confirmed and that the button reappears after scrolling up by hand; asserting only that "Sending" disappears isn't enough.

Don't give the whole `LarkMessageRow` one accessibilityIdentifier on sending rows: SwiftUI propagates it to child nodes and overrides the identifiers of the sending / failed markers next to the bubble. Attachment echo confirmation asserts that the local preview button disappears; sending state keeps using mainline's `lark.outgoing.sending` / `lark.outgoing.failed`.

When mainline hides the send button for an empty composer, attachment drafts still count as sendable content: photos / files alone should still show the button, it hides after the last attachment is removed, and it's disabled while attachments are being prepared. Existing text, quote, thread composer and read markers stay.

## Versioned Inbox reply recovery (#9126)

- No current suggestion or no receipt means unknown history, not proof of non-delivery. Preserve the existing coded
  `delivered` / `retryable` distinction; only confirmed retryable non-delivery may send again. Enforce that in the
  manager, not only by hiding a Web button.
- Freeze the first confirmation's actual edited payload, client ID and source/target before I/O. Retry the same version
  with that identity after reload. Never reuse its ID with changed text, and never let an equivalent proposal of the
  edited text escape an unknown reservation through a new version.
  Check both re-proposed and actually confirmed edits against historical unresolved operations, including after
  intervening proposals. A display-history cap must not delete the last proof used by this guard; settle the result
  of equivalent current aliases without clearing a different proposal or source.
- Saving an empty mutation set is not proof of a durable reservation. A failed or missing store prevents outward work;
  an interrupted reservation stays conservative after restart. Keep the send-success helper's local side effects under
  the same version/attempt/source fence as completion; an outer restore check is too late if the helper already filed
  the item or marked a newer card sent.
- Deduplicate default Wrap up by explicit intent and incoming identity, not an arbitrary time window, component-local
  busy state or changed metadata timestamps. User-authored different instructions remain a fresh handoff. Compare the
  full action, conversation and target when reusing a waiting suggestion, not just its visible words.

## Messages and documents

- An all-account mail search must fail when any account fails, even if another supplies results; otherwise older HTTP, tool and subscription clients accept an incomplete page as complete. Keep cancellation distinct, and do not put upstream error details or credentials into the aggregate error (#4462). Verify the actual callers with two independent loopback IMAP servers, including retry after repair and a healthy empty result.

- Lark message rendering copies the Lark gateway's web client (`docs/apple/ios.md` ("Lark message rendering")): the hub forwards `parts` unchanged and only rewrites media URLs to `/api/v1/lark/media/`;
  to read the gateway's source, clone its upstream repository (the address is in the notes of the extension that connects Lark; the hub machine only has the deployed build).
- For personal Agent sources, look at the hub's `GET /api/v1/agents` first: when no Lark gateway is configured, or only the Lark CLI is, it returns `{"sources":[]}` and the sessions endpoint returns `{"sessions":[]}`, both HTTP 200; the Lark CLI doesn't provide a personal Agent bridge.
  The bridge is only registered when `LegacyGatewayConfigured()` is true (since #1571 personal Agent sources come from the extension); registering it unconditionally makes the list request return 502 with "gateway not configured" when nothing is configured. Once a gateway is configured, its agent-bridge is still off by default: turn it on in the gateway's deployment (the variable is in the notes of the extension that connects Lark) and restart; otherwise `/agent-bridge` returns `enabled:false`
  and sessions are empty; older Lark gateway versions don't have this endpoint, and the hub gets 404. After turning it on you still need to add the Cue / Grok Bot accounts in the Lark gateway's settings (only the web endpoint the owner is signed in to can add them;
  the Agent API is read-only). With the source registered, `setup` being `missing` / `disabled` / `no_accounts` means it isn't set up, and `lastError` is an account sync failure.
  After the Lark gateway auto-updates, check that this switch is still in the deployment config.
- The Lark gateway Agent API's resource URLs use `/agent/v1/media/` and `/agent/v1/messages/`; the hub's rewriting must handle these prefixes as well as the old `/api/` URLs, keeping query parameters.
  Checking for HTTP 200 isn't enough: an unrewritten image path falls through to the hub's web page and returns HTML, and the phone caches it before decoding. Verify the new URLs returned in messages, the image MIME type and the actual bytes; rewriting to the correct proxy URL also changes the phone's cache key.
- Pitfalls in the Lark gateway's chat list (`docs/protocol/protocol.md`, Lark API): the `isDone` field is unreliable (all false in the done scope), so "done" is decided by scope;
  pinned is `onTopRankAt` (`settings.isShortcut` is wrong too), and pinned chats are spread across both the inbox and done scopes; an unknown scope is silently treated as inbox.
- Don't declare the pinned set complete after reading a fixed three pages. Stop only at the first non-pinned item or when there is no next page; hidden items can filter a whole page to empty, and the cursor should still be followed. Duplicate / missing cursors and the safety cap all return errors, so the client can't overwrite its cache with incomplete data. Parse pinned times before comparing: sorting ISO times with milliseconds directly against whole-second strings reverses the order.
- Opening a Lark chat must not use `tail=1` (that's "Jump to Latest"): with unread messages use `aroundPosition=readPosition+1`, and only report messages that actually made it on screen as read.
- For topic groups (`chatMode: "topic"`), `GET /conversations/F/messages` usually has only the position 0 system message "创建了话题群" (created the topic group) and `hasOlder=false` (in testing, 4 of 5 topic groups were like this, for tail, around and before alike); the content is in `…/threads` (topic root messages, paged back with `nextOlderCursor`), and replies are in `GET /messages/ROOT/thread`. Don't count on callers passing the right `topic` (coming in from a person's card, old Inbox items and the web all leave it out): the hub decides by chatMode itself in `lark.ChatPage` (remembered from the chat list; for ones it hasn't seen, it queries `GET /conversations/F` once) and returns `topic: true`.
- Some networks (the author's hub machine) can't reach most of Google's IPv4 addresses (`imap.gmail.com` / `smtp.gmail.com` often resolve to 173.194.x.x, and every port times out); HTTPS works only because it goes over IPv6.
  The hub always reaches Gmail through `google.proxy` in `hub.json` (for example a local proxy on the hub machine); don't add a direct-connection fallback again (`docs/extensions/mail.md`).
- MIME bodies nest: mixed messages usually wrap multipart/alternative, so any walker that only looks at top-level parts finds no text. The list snippet (`textFromRaw`) and `GetMessage` must both recurse; an empty snippet silently blanks Inbox rows, `mail_search` hits and the knowledge-base archive body.
- One body can span several inline parts: Apple Mail (iPhone and Mac) splits the text around every file or photo placed in it (`multipart/mixed {text, file, text}`, rich text `multipart/related {html, image, html}`). Keeping only the first text part dropped everything after the file from `mail_read` and the Inbox (#6023). Join the parts of a mixed or related entity in order, keep one rendering per `multipart/alternative`, and walk the MIME tree for that: go-message's `mail.Reader` flattens it and cannot tell the two apart. Attachments keep their own flat walk so their IDs match the download.
- Attachment IDs use physical MIME attachment order, including undecodable parts omitted from the displayed list (#1497). Count the same `AttachmentHeader` parts as the downloader before decoding; counting successful decodes shifts later IDs and breaks downloads and saves. Exercise the real parser and IMAP download/save boundary with corrupt preceding parts, nested bodies, and repeated or empty names.
- Text-only sends (`RenderMessage` without html) must set `Content-Type: text/plain; charset=utf-8` themselves: go-message's `CreateSingleInlineWriter` adds none, and a message without one is US-ASCII (RFC 2045 §5.2), so UTF-8 bodies show as mojibake in clients that trust the header.
- Sent mail must not carry the hub machine's host name (a Mac's default one includes its owner's first name): go-message's `GenerateMessageID` puts `os.Hostname()` after the `@`, so build the Message-ID with `GenerateMessageIDWithHostname` and the sender's domain; SMTP servers record the EHLO name in the `Received` header of every message they accept, so the client greets with an address literal (`ehloName`).
- When the client's JSON shape doesn't match the hub's, Go's decoder returns 400 outright, and you won't see it in the simulator unless you exercise that path: iOS sent `references` as an array when replying to mail,
  the hub's `SendRequest` only accepted a string, and manual replies were rejected with 400 (#587, found by reading code). When changing a request field's type, have the hub accept both (`mail.MessageIDs`),
  and add an HTTP test that POSTs each client's real shape (`TestHTTPSendReferences`).
- A full mail parse can fill missing envelope recipients from the raw `To` header (#6172). Update derived fields on the parsed message; copying the original summary back erases that fallback. Test an empty envelope recipient list and preserve populated-envelope precedence.
- Build a mail server's dial address with `serverAddr`, never `fmt.Sprintf("%s:%d")`: a server set as an IPv6 literal (`2001:db8::1`, with or without brackets) needs brackets before the port; `2001:db8::1:993` is no address, for the dialer or in the `CONNECT` line to the Google proxy.
- SMTPUTF8 is declared on `MAIL FROM` for the whole envelope, recipients included: a server that implements RFC 6531 refuses a non-ASCII address in `RCPT TO` unless `MAIL FROM` declared it, even when the sender's address is ASCII.
- Check the capabilities the server advertises before using IMAP extensions: QQ Mail doesn't advertise LIST-EXTENDED and simply never answers a `LIST` with `RETURN`, so you can only wait for the timeout (`docs/extensions/mail.md` ("Known limitations")).
- A server answering "search succeeded, 0 results" doesn't mean there really are none: 163 returns 0 for any keyword `SEARCH` (`TEXT`, `BODY`, `SUBJECT`, `FROM`, Chinese or English, with or without `CHARSET UTF-8`); only conditions like `ALL` and dates work (tested 2026-11-03). So when a keyword search returns 0, match headers locally (`docs/extensions/mail.md` ("Known limitations")); tests simulate this kind of server with `blindSearch`.
- 163 is also slow to fetch, slower and more erratic the older the mail: 200 summaries per page, 4 seconds for the newest page, 6 to 20 seconds after that, over 20 seconds for the page around message 1000 (tested 2026-11-03). One IMAP operation gets only 20 seconds (`withConn`), so finding headers and fetching summaries must be done in small batches, with time counted from the start of the operation, connecting included (#858); batches of 500 with time counted from the start of the scan time out.
- Wait for each UIDPLUS move command in order. The go-imap `Move` fallback pipelines COPY, STORE and EXPUNGE, so use
  it only with native MOVE. Once the first write may have reached the server, even a dropped tagged reply must not
  trigger a replay. A native MOVE can also end in tagged `NO` after applying the move (RFC 6851 §3.3); only `BAD`
  rejects the command itself. Test by letting the fake server apply the command before replacing or dropping the
  reply (#16742).
- A cursor kept by UID must also record UIDVALIDITY: after the server rebuilds / migrates a mailbox, UIDs are renumbered from the start (RFC 3501 2.3.1.1), new mail's UIDs are all smaller than the old cursor, and comparing UIDs alone archives nothing until the new UIDs pass the old maximum.
  The cursor also can't jump straight to "the max UID of the newest page": if more than a page arrives between two runs, the ones in the middle are never read. The knowledge base's mail archiving (`extmgr/mail_connector.go`) now records `<account>#v`;
  when a page doesn't reach the old cursor it records the gap (`#f` < UID < `#h`) and fills it page by page; when UIDVALIDITY changes it starts again from the newest page and only takes mail later than the newest archived one (`#t`) (#2633).
- Resetting the UID cursor alone does not change knowledge-base receipt identity: a reused UID from the same sender can be skipped as already archived. Keep old decimal IDs until a known validity change, then persist a per-inbox epoch and include both that epoch and validity in IDs. The validity distinguishes another change before the first checkpoint is published; the epoch distinguishes later changes after checkpoints. A missing or zero validity cannot prove a change, and an already advanced old cursor cannot recover a lost message. Older writers ignore the epoch and are not safe to mix with the new writer. The existing timestamp filter and newest-page restart remain separate limits.
- @mentions of me and comments to me in Lark docs / sheets / Base don't go into the IM chat list (there's no "Docs Assistant" chat); they're only in the Lark gateway's document notification center
  `documents/notifications`, and the card only says "who mentioned you", not which row: you have to read the sheet and look for "@name" yourself (`docs/assistant/inbox.md` ("Docs: mentions in sheets, bases, and documents")).
  In sheets, `sheet/edit` on a cell with a dropdown always reports `DOCUMENT_EDIT_UNSUPPORTED`; change the value with `choose` in `sheet/structure`; Base `base/edit`'s
  `expect` fingerprint must be computed from the current value (`BaseCell.Fingerprint`), and the id format of person fields differs from the userId `accounts` gives (can't be written, can be read).
  Tests only use demo docs you created yourself (link in an environment variable); don't touch real sheets the user gave you.

- New chat entry points must make mock / demo mode go through real model decoding too: the chat fixture once lacked `is_mine`, which the server always returns, and the Inbox didn't expose it because it assembled chats itself.
  Complete the fixture and verify every chat demo mode returns can open its messages; don't loosen the real protocol for tests.

## OLK display and the local cache

Plain text returned by the Agent can't be used to guess whether something is encrypted. The new `olk` projection has to run through the hub and the message and quote models: body, public external display and key distribution are three separate display parts. The old `parts` stay for old-client compatibility; the native UI uses the projection, so public emoji and distribution notices aren't spliced into the body.

Don't strip decrypted bodies only where the main message cache is written: quotes also end up in drafts / the outbox. The Encodable implementations of messages and quotes clear legacy text, parts and the projection body together, and persistence keeps only public content. In a project with default MainActor isolation, a nonisolated model that implements encode in an extension in another file still needs explicit nonisolated, or it can't safely satisfy Encodable.

Review follow-up: the optional projection isn't a required field for a whole page of messages. The hub normalizes missing / null public content to an empty array, and the App degrades safely on unknown versions, missing state and incomplete fields, without reading the old flattened body. Offline external display must be labeled as public content so it isn't taken for the original. The encoding protection above only covers iOS message / quote snapshots; the hub's Inbox and Agent context still have separate flattened persistence paths, so this must not be summarized as no plaintext storage end to end.

- The external-display marker for encrypted messages must cover three paths: bubbles, chat quotes and composer quotes. With the marker only in bubbles, restoring a quote draft still treats the public disguise as the original; use the quote's display text everywhere, and verify the locked, cached and decrypted states separately.

- Native encrypted sending prepares the ciphertext first, then enters the existing persistent queue. If a retry calls encrypt again it produces a different envelope, and the existing client ID idempotency breaks; multiple key distributions also need stably derived IDs. The author's own encrypted draft, the optimistic echo body and the ciphertext all belong to one AES-GCM snapshot, so clearing only the received-message cache isn't enough. A temporarily unavailable keychain must block overwriting, not be treated as an empty draft.
- Messages and the Inbox share drafts: putting the mode switch only in the Messages page isn't enough; when the Inbox restores an encrypted draft, it must not downgrade to a plain send. Cross-page, force-quit recovery and the old format of plain drafts need regression testing together. The new shell keeps hidden pages, and a hidden chat may still write back old input when scenePhase changes; only let the current page save, flush once at the visibility exit boundary, and restore from the shared outbox when it reappears.

## Lark Open Platform bots (`docs/extensions/bots.md`)

- A retry retains the original authority and privacy decision (#12609). Bind a reminder to the first ready owner's pairing generation and enforce it at the actual private-chat route, using the same snapshot for identity validation and recipient selection; checking status only in the assistant leaves a re-pairing gap. Health masking is sticky, unknown sources are hidden, and a bot attempt pins that route even if a companion appears later. The provider's one-hour UUID rule is at-most-once delivery, not a promise to return an old message ID: a fake that returns one tests only that fixture's receipt behavior. Treat definitive pre-transport refusal, no attempt and unknown acceptance as different outcomes.
- A claimed to-do reminder and delivery of each channel copy are separate outcomes (#12609). Retrying the whole reminder would repeat the phone notification; recover only the failed Feishu copy with the existing finite retry policy. Key every card by the original reminder occurrence and index, so replaying a long reminder also covers an accepted continuation whose receipt was lost. Treat unknown status as temporary without sending, and stop on a confirmed disabled channel, refusal or obsolete reminder. Process-local retries do not survive restart; the durable reminder claim remains consumed.
- A settings setter must return a failed write before reporting success or recording an audit. Restore only its own previous field while holding the same settings lock; this preserves an unset default and other successful saves. A fake store must reject the intended write, since one settings request can make several separate commits (#12666).
- Inbound rich-text `code_block.text` can itself be a Markdown document (#12667). When flattening it for the bot's model context, use a backtick wrapper longer than every run in the body; a fixed three-backtick wrapper lets a literal fence become a closing delimiter and can turn following post nodes into code. Verify synthetic events through the public event/content parsers and check the resulting Markdown AST, alongside exact ordinary-output and adjacent-media controls. This is separate from escaping or folding outbound answer cards.

- With card JSON 2.0, **one unknown extra field rejects the whole card** (1.0 ignores it): 2.0 has no `action` module and no `note` (lay buttons out horizontally with `column_set` + `flex_mode: flow`,
  small text with `div` + `text_size: notation`), and button callbacks go in `behaviors`; a new card returned from a card callback must also be 2.0, otherwise you get 200830. Before writing, check every field against the 2.0 component docs.
- 1.0's Markdown doesn't support headings, tables or quotes, so a model answer dropped straight in shows raw symbols; use 2.0. Cards treat things like `<at>` and `<font>` as their own tags: escape `<` in the body first.
- The docs for card link buttons only mention http(s) and AppLink; don't put a custom scheme (`persona://`) there, or the whole card may be rejected.
- Long-connection events must be acknowledged within 3 seconds, or Lark redelivers the same event: do the processing in the background and de-duplicate by message_id.
- A message sent to a chat that's busy queues behind the current turn: the end of that turn isn't its answer; find this message in the transcript and take the answer after it (`botRun.Probe`).
- Redacting a health reminder means checking the whole outgoing card, including summary, header, fallback and button behaviors: a detail URL still identifies the private to-do when its visible title and notes are generic (#12694). Exercise the production reminder entry with a public example base and synthetic IDs, and keep ordinary navigation and reply-context controls. Gate reminder navigation with its existing privacy decision; shared URL and button helpers also serve explicitly requested navigation.

- The Lark gateway's 201 receipt for a file send may contain only `fileKey/name/size`; `messageId` is optional, so don't report a sent file as failed because of it and invite a resend. In image JSON, the slashes in base64 may also all be escaped; the HTTP body limit must cover this legal encoding, while the actual image size is still limited to 7.5 MiB after decoding.
- Unsent attachments are user data: put the private copies made after picking in Application Support, excluded from backup; not in tmp/uploads or tmp/lark-drafts, which clearing the cache deletes.

## Progress-card actions need current ownership (#12702)

A refused final edit can leave a progress card's Stop button visible after the answer is posted separately.
The button's conversation identifiers say where it was created, not which turn it still owns. Compare the callback's
actual message ID with the current watched Feishu run's `Ext["card"]`, then recheck the same run and live objects,
card, recall and answer/finish state under the lifecycle locks after target resolution. Historical thread routing,
stored runs awaiting watcher restoration, and runs awaiting final-answer delivery cannot authorize a stop. Keep
successful-press deduplication with the live progress state and leave failed delivery retryable. Typed `/stop` remains
a conversation-level request; assistant-side locking cannot provide a node-side atomic turn-generation precondition.
A test that posts progress directly must first wait for the run's real watcher, use that watcher's live object, and hold
its progress mutex. A run returned by `channelRuns` is only a snapshot: it cannot satisfy the ownership check. A missing
watcher after the initial `devices.list` is a different lifecycle state, not permission to weaken the Stop guard (#13310).

## Paged refresh of all chats (#530 review)

- If refresh replaces only the first page while paging always lets old values win, unread counts on the tail pages go stale forever; reread the window by the number of loaded pages, replace atomically on success, and leave the cursor at the end of the window. On failure keep the old snapshot, and don't write half a batch to the cache.
- Live events carry the chats that changed (`feed_ids`, #3006): a new message always pushes its chat to the first page, so when every chat the event names is on the new first page, read only the first page and move them to the top,
  leaving other rows and the cursor alone (pinned + first page = 2 requests, versus pinned + loaded pages + state + account list before). Pinned is read every time: when the event names a pinned chat,
  what changed may be its unread count or the pinned bar itself (count, order). After switching accounts, reread the whole window as usual. If one isn't on the new first page (read elsewhere, done, left the group: it may have changed in place in the window or left it),
  reread the whole window as in the previous item. Events for other accounts aren't read; reconnects and events without chats (pinned, settings) still reread the whole window.
- A load request at the end of the page can't just be dropped when a refresh comes along: SwiftUI doesn't restart the task when the cursor is unchanged. Wait for the refresh to finish, then continue with the new cursor; the queued refresh stays inside the page's own task, so leaving doesn't spawn a separate background task.
- The chat list isn't the message subscription list; listing every group shouldn't make every active group keep prefetching chat content.
- A plain `.task` following `LarkSync.changes(of:)` keeps listening and fetching on a hidden shell page (the shell keeps it mounted); follow from `.tabActiveTask` and catch up once when it starts again (#10337).

- The persistent send queue must record separately whether a submission has been attempted. On restart, only files whose submission started with an unknown result need checking; queued files that were never sent should keep an explicit retry. Offline, DNS or connection failures must not be misjudged as uploaded-but-unknown either.
- The Lark gateway's successful file receipt may omit the message ID; both the client and the hub must accept the success, and the client must not turn the optional field back into a send failure.

- Temporary picked copies in Application Support aren't cleaned up automatically by the system; after the process restarts, clean up old UUID directories first, then start new picks. Cleanup runs only once and shares a lock with picking, and must not delete copies the current process just created or the separate persistent media directory.
- The timeout for streaming uploads includes the phone's upload time, so give the phone and the hub the same budget based on file size; the Lark gateway's pending / failed local messages must not become ordinary sent bubbles even after they're removed from the local outbox.

## Feishu bot token refresh after transient retries

Track the one forced tenant-token refresh separately from the request attempt index: a retryable 503 or 429 can precede
an expired-token rejection. The refresh still consumes the existing attempt budget and must stop after one refresh;
an expiry on the final attempt returns that expiry. A confirmed auth rejection permits replay of a UUID-less POST,
while a transport/read failure or server error still stops it. Keep the original payload and UUID across keyed retries.

## Feishu bot concurrent token refresh

- Keep the tenant-token cache mutex out of HTTP exchanges (#12681). Concurrent calls share a per-client
  completion signal and select their own context cancellation while waiting. Check the in-flight exchange
  before a locally valid cached token: the API may already have rejected that token, and spending a final
  attempt on it loses the request. On wake, recheck the current exchange and cache under the mutex rather
  than returning a saved token that another refresh may have superseded. A successful cache generation
  satisfies waiting refresh requests; after failure, surviving callers fetch with their own contexts.
  Gate both initial and forced token exchanges in loopback tests, including final attempts and delayed wakes.
  This coordination preserves credential/domain isolation, retry budgets and unkeyed-write replay rules.

## Message Tweaks and account settings

A PUT of the whole local settings overwrites fields that the web or another client just changed. The phone changes the two display switches only through a restricted PATCH that carries the old values; unknown results are reread first and never retried automatically. The hub returns the user ID of the actually selected account, and must not substitute another primary user from the account list. The identity check before and after can reject a response from a switched account, but it isn't a database transaction spanning sign-in operations.

The original content for keep-recalled-messages is right there in the Lark gateway's parts; you must check hasRetainedRecallContent separately and not mistake the system recall notice for the original. Keep display separate from permissions: keep the recalled flag; when the switch is off, the body, quotes and forwards all show the recall notice. Edit versions have their own OLK projection, so don't clear only the on-disk plaintext of the main message and quotes and miss editHistory.

The metadata of merged-forward cards contains both messages and copied previews; don't handle only the expanded body. Previews are generated from each original record's recall state / OLK projection; when writing to disk, recursively strip decrypted bodies from records and their history, then rebuild the previews, so copied text without an OLK marker doesn't leak into the cache. Edit times may contain milliseconds; keep using JSON.parseDate rather than only the default ISO8601DateFormatter.

A settings-change push may arrive before the HTTP receipt of the save request, or while the old account's read is still in flight. Don't drop the refresh just because of `loading` / `saving`: coalesce notifications during that time, and after the current request finishes, reread using the most recent client, without publishing an old response that a notification has superseded. The reread is owned by the settings model, so leaving the earlier page or cancelling its wait must not also cancel new live changes; writes are still submitted only once.

When the hub coalesces events within 400ms, settings changes must accumulate separately as `settings_changed` rather than being signaled by an empty `feed_ids`; a batch that also has new messages must still reread settings. The not-signed-in error keeps using `lark_not_logged_in`, pointing at the Lark gateway sign-in on the hub's machine, rather than suggesting useless repeated rereads.

These two switches control display and follow the Lark gateway's semantics; they aren't retention or access authorization: turning them off doesn't delete what the server kept, the hub still returns original content and versions to clients with message read permission, and ordinary unencrypted content may still end up in the local cache. OLK decrypted bodies are separate from this and must be cleaned from persistence. The existing Inbox triage and assistant quoting may still use recalled `Text`; their AI context policy will be unified separately as follow-up to #882, and this phase doesn't claim to have changed it.

## Row spacing in the message list

SwiftUI List's default row insets stack on top of the content's own padding. #957 measured on iOS 27: a 46 pt avatar + 8 pt content padding top and bottom ended up with a 92 pt row; explicitly setting the row's top and bottom insets to 0 and using the existing 48 pt avatar token gives 64 pt for a normal row. Don't hide the duplicated padding with a fixed height; Dynamic Type should be allowed to grow the row. Set it only on the message list's NavigationLink, so the shared Inbox doesn't change.

After adding `.id`, geometry observation or a background probe to a row, put `.listRowInsets` back at the outermost layer. #949's first round of screenshots found the original zero insets had stopped working because of the wrapping order, and normal rows were back to about 94 pt; fixing the order and constraining the normal-text-size row height with a UI test restored 64 pt. Checking that `top: 0` appears in the Swift source isn't enough.

Pinned chats on the home page and normal paging both need to be bound to the actually selected account. The hub's `SelectedUserID` is checked before and after the upstream read; old clients keep the original API by not adding `account_bound=1`. Cache clearing and writing use the same serial queue, so a stale async write can't restore data after an account switch cleared it. The live event subscription must be set up before the first read, or changes during the read may be missed.

- When merging message-display controls with interactive quotes, keep the shared `QuoteCard` and `larkJump` action. Apply recall visibility to the card summary before rendering; UI tests should query the quote button, not the former static text.

- New native messaging features must check at the entry point the API of the hub used by the current chat, and name exactly what needs upgrading. When the client updates before the hub, a plain 404 must not be shown as not found; a missing upstream API must be distinguished by the hub as the Lark gateway needing an upgrade, and an unauthorized error must not be misreported as an old version. Keep the encrypted draft and a recheck entry, so a failure doesn't silently turn into a plain send.

## An item marked done while collection reads messages (#1105)

- `applyLark` first finds, under the lock, the open item for this chat, releases the lock to read messages, then takes the lock again to write them in. If the user marked that item done in between (swipe left, mark all as done),
  new messages used to be written into the done item anyway with `done_at` unchanged, so nobody saw them. Now, after retaking the lock, it first checks whether the item is still open; if it's done, it's treated as "the previous item" and new messages open a new one.
  Merges for other sources (mail, docs, Moments, connectors) all happen under a single lock and don't have this gap; when adding new collection, don't read outside the lock and then write into an item obtained outside the lock either.
- Bulk mark-as-done carries the list's `version` and compares it under the lock: an item that got new messages merged in after the copy the user saw is not marked.

- Checking the pinned avatar area at large text sizes means looking at avatars, badges, the expand icon and the three-segment switch at the top together. Avatars / badges can use the size caps the design system already has, while titles still follow accessibility sizes; when space at the top is tight, use design tokens to control a compact switch, so the entry points on both sides aren't covered.
- Paging at the bottom of the chat list relies on `.task(id: nextCursor)`: if a refresh comes in (a push, returning to the foreground) while a page is in flight, that page is invalidated, the cursor doesn't change, `.task` never runs again,
  and the spinner hangs forever (#1307). Normally you don't notice, because scrolling away and back makes the row reappear and request again; once pinned chats stopped being duplicated in the list, a first page made entirely of pinned chats left only this spinner below, and it got stuck.
  Paging invalidated by a refresh waits in `more()` for the refresh to finish and asks again; don't rely on the view's id to rerun it.

## Pins inside chats

- The Lark gateway's `GET /conversations/F/pins` marks pins as read when called, so don't use it for "read-only acceptance" automation on a real account just because it's a GET. Read only after the user opens the list or acts on a pin; tests for writing and pin reading use an isolated mock.
- `pins.updated` means the list changed; `pins.read-state.updated` is a read-state change. Subscribing to the latter and then refreshing the pin list forms a self-triggering loop.
- `capabilities` is a partial override: when the object exists but lacks a key, the Lark gateway's default applies; a missing object means an old response, and write permission can't be assumed. A normal message's `localState` is `synced`, so don't disable on the grounds that it's non-empty; recalled / deleted messages can't be pinned, but removing an old pin still follows the `unpin` permission.
- Pin write requests have no client idempotency key, so don't copy the auto-retry from text sending. Network failures, server 5xx and successes with missing receipt fields are all treated as unknown; reread the list, then let the user decide. Locating also fetches the complete target window first, and a failure doesn't clear the chat.

## Lark gateway with multiple accounts (#1321)

- `GET /agent/v1/accounts` returns the list in the Lark gateway's order no matter which account is asking, with primary account 1 first; which one is this request's account is in `activeId` (`active` in older versions).
  `lark.Client.Me` used to take "the first `active || primary`", so a request with the gateway's account header `2` also got account 1's identity; now it looks up `activeId` first.
- The same group can be in two accounts at once with the same feed id: Inbox items, the App's chat cache (`lark-chat-<key>`) and live listeners must all key on "account + feed",
  not feed alone. That's also why the hub doesn't guess the account from the feed (when an old client opens another account's chat, the default account is used: better to fail to read than to send under the wrong identity).
- Resource URLs (avatars, images, files) for added accounts come from the Lark gateway with `ol_account` already in them; the hub rewrites them to `/api/v1/lark/media/…?ol_account=N`, and passing that back unchanged is correct;
  when the App adds an account to such URLs, it must not overwrite the `ol_account` already there.

## Group delivery is not permission to trigger (#10238)

A platform's mention scope may coexist with broader message scopes. Enforce per-group mention-only policy before the bot's own settings commands as well as assistant handoff; a command reply is already an observable side effect. Match structured open IDs, fail closed while the bot identity is unknown, and read the policy after a queued message gets its turn. Keep omitted boolean edits distinct from false so older clients cannot silently loosen a saved policy. A strict first enrollment must not become the fallback home for unrelated requests.

## Native topic progress must not hold the card mutex (#12574)

Deferring native create/append until after the first card still holds the progress mutex for the entire call. Test a second progress tick and the final answer while the fake native API remains blocked. Serialize native events separately, coalesce bounded item snapshots, cancel progress at the end, and fence persisted results by current durable membership and the owning turn so a late response cannot modify a held next turn. Test real run folding: an attached worker owns native completion, while restored runs without one use direct cleanup. Distinguish `rpc.NotSent` / `ErrNotWritten` from uncertain cancellation before deciding whether to replay a batch; a terminal refusal repairs the existing card's process panel instead of posting another answer. Keep the worker mutex out of the existing ordered checkpoint I/O.

## A bot switch is two structs, and an agent message keeps its route (#16847)

`BotFeatures` and `assistantwire.FeishuFeatures` are converted by struct conversion: add a switch to both, at the same position, or the flags silently land on the wrong fields. Settings saved before the switch have no key and read as the zero value, so a new switch that must default on needs the same defaulting the older ones use; one that defaults off does not.

An agent message's route (the worker, or the older synchronous cursor) must not change mid-turn. The two cursors do not share a position, so handing a message the worker already opened to the other path appends the same events twice. Switching the feature off, or a hub restart that rebuilds the run, keeps the run on the path that opened the message.
