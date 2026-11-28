# Inbox

The Inbox tab is Persona's information entry point: Lark messages and mail from all email accounts share **one list**, grouped into items. AI triages first; people see only the conclusions.
(Requested direction 2026-10-27: no group-by-group browsing, no second Lark client; see `docs/extensions/feishu.md`.)

Android's navigation badge counts each unread open item in Needs you once, including separate items from the same source conversation; Web and Apple use the same item-level rule. The current hub `status` decides, with the existing tier fallback; FYI, Noise, In progress, read and completed items do not count. Reading an item never files it: read-but-pending items remain in their hub sections and keep their completion state, and Android has no separate read-pending section. Zero keeps the existing hidden-badge behavior. The roster's `inbox_needs_you` and the home page's pending count retain their separate meaning: open items requiring action, including ones already read. A receipt can precede the next item-list update, so Android's badge also reads the reconciled `InboxConversations` state rather than only the cached item flags.

## Opt-in automatic replies (#9762)

Suggested replies still wait for Send by default. A scoped `send` standing
authorization can enable plain-text Lark via lark-cli replies for an exact
group/chat (or explicitly all chats in that channel), scenario `inbox_reply`,
and one sender identity. Configure and revoke it in Web Settings > Persona;
see [Scoped outward messages](assistant.md#scoped-outward-messages-9762).

The suggestion must be persisted before the existing version-checked
confirmation path consumes it. Handbacks, document writes, conclusions, and
Agent/to-do suggestions remain manual. Each automatic attempt is durably
audited before transport. Each source message permits only one automatic reply
attempt per sender, even across rewording, suggestion versions, or a manager restart.
Failure or uncertain delivery needs inspection and a deliberate manual retry.
Revocation and identity changes serialize with the final grant check/send.

## Ownership: the manager owns the Inbox (2026-11-04)

The Inbox belongs to the **manager** (assistant process `personad assistant`; `docs/assistant/assistant.md`, "Boundary with hub"): items and their state (pre-admission triage,
per-source rules and fallback tiers, `triage` status, event grouping, muting, done state, bulk actions), the AI twin (judgment, starting its chat, drafts, message cards), item translation,
and generic App translation (`docs/apple/ios.md`, "Translation"), plus every endpoint under `/api/v1/inbox…`, `/api/v1/translate`, and `/api/v1/translate/mail`
(hub forwards unchanged through `assistantwire.InboxRoutes`, with the same fields as before). Code: `server/internal/assistant/inbox*.go`.
Table `inbox_items` and setting `inbox_mutes` are unchanged; no data migration, and only the manager reads / writes them. All models (triage, judgment, drafting, translation) are called by the manager
through hub's model configuration (`app.call models.*`, centralized on the manager side in `assistant/inbox_hub.go`). Every call has a deadline (`timeout_ms`);
when the manager stops waiting, hub stops too, leaving no queued request. Inbox and translation no longer call models inside hub.

**Sources also belong to the manager** (2026-11-05, moved with connectors; `server/internal/extmgr/inbox.go`, `docs/extensions/extensions.md`, "Who owns what"). Source-related mentions of "hub" below now live in the manager, in the same process as the Inbox, without bus handoff. Connectors and plugins collect through `inbox.collect` on a schedule
(every 2 minutes, connector `changed`, Lark events, mail every 3 minutes) and deliver raw collected data to the manager (bus call `inbox.collected`, once per round: each source's items and capabilities,
each mail account's latest page, reading at most 4 accounts concurrently and delivering in account order. Failed connector reads still appear in the round with empty `things` and a short `read_error` code (see "Triage status" below); the manager retains their existing items. A mail account that fails to read is left out of `mail.accounts` as before and named in `mail.failed: [{id, email, read_error}]` instead (#1849), so the manager keeps its mail and says it is behind. Each round carries the hub run's `epoch` and round `seq`; delayed rounds older than one already used are discarded). Source operations requested by the manager use
`app.call inbox.*` through hub: read a mail's full body (translation needs text preserving paragraphs and links), send replies (mail, connectors, or the Lark plugin),
`settle`, event grouping's `group.tool` / `group.fallback` / `group.append`, read hub triage rules, AI twin configuration, and plugin-provided connectors
(`inbox.config`), and current translation model (`inbox.translate_fingerprint`). On item changes, the manager emits `inbox.changed`; outside the bus read loop, hub
sends plugins the `inbox.changed` event and Apps `{"type":"inbox"}` (one slow plugin reader does not block others). Hub asks the manager for items needed by plugin detail slot `inbox_item`,
items when deleting a source's data, and GitHub Settings' muted repositories (`inbox.item`, `inbox.forget`, `inbox.repo_mutes`). The Inbox Sources
pages (#5278, `docs/extensions/subscriptions.md` "Pages") list and remove a source's mutes (`inbox.mutes`, `inbox.unmute`) and have the triage model try a
subscription's dry run on request (`inbox.triage_preview`: nothing kept).
Hub still enforces its own boundary: reply-only devices (Watch) may send only text, not change document state or send `/agent …` (which starts an Agent when sent as the user).
Hub rejects these before forwarding (403; the manager checks too). Connector settings, including credentials, remain hub APIs; only muted repositories belong to the manager.

**Send once**: replies (mail, connectors, Lark plugin) carry manager-derived `send_id` (use App `client_id` if supplied; otherwise derive from item, text, and changes,
excluding `last`, like Lark `clientId`). Hub stores it in `sent_messages` (the same table as App sends; the records do not expire). Repeated requests return `already` without resending (safe if a reply is lost in transit
or the manager restarts and retries). A previous interrupted send (hub stopped or timed out) returns `assistant_in_doubt`, also without resending. Event grouping's `group.append` similarly carries
`append_id`: hub retains the plugin's response and returns the same event on retry, never appending twice.

**When the manager is disconnected** (assistant restart, or crashed before being restarted):

- Inbox and translation endpoints return `503 assistant_offline`, "Assistant temporarily unavailable; try again later" (including pull-to-refresh, replies, marking done, muting). Replies rejected this way
  are not recorded in receipts (`reply_receipts.go`), so the same reply can send normally after recovery.
- If the connection drops or times out after handing the reply to the manager, it may already have been sent. Return `502 assistant_in_doubt`, `delivered: "unknown"` (the App asks users to check before deciding,
  not "Not sent"). No receipt is written; retry reaches the manager with the same `send_id`, and hub does not resend anything already sent.
- **Collection loses nothing** (since sources and Inbox share a process from 2026-11-05, this now matters only when the assistant Inbox temporarily cannot read stored state). Hub keeps collecting on schedule and buffers for the manager (`held`: latest version per thread / event / mail, retaining items that slide out of the window;
  at most 2000 items per source and 500 mails per mailbox, memory only, lost on hub restart—when the manager reconnects and immediately collects a round). Sources disabled at collection are not retained.
  On manager connection, hub immediately collects a complete round including mail and delivers it with buffered items, including those that slid out of windows such as latest 40 mails / 30 iMessages.
  If delivery fails (no manager response or database unreadable), keep buffering for the next round. Rounds keep their usual pace meanwhile (a Lark event reads chats only, mail every 3 minutes);
  the complete round owed for the failure waits until a round is accepted again. Incompletely written event batches are resubmitted by the plugin next round.
- Plugin detail slots cannot render (the App keeps its original body). GitHub Inbox settings (CI, muted repositories, ignored branches) are hidden as a whole: hub never supplies settings missing muted repositories,
  which would look unmuted and clear everything on save. Saving without `muted_repos` (or with `null`) preserves mutes; only a supplied list, even empty, changes them.
  Deleting connector data still deletes knowledge-base data but reports the Inbox failure; retry deletion completes it.
- Manager items are in the database: manager or hub restarts reload `inbox_items`. Hub restarts launch the manager again, with a full round on connection.
- Connected manager but stored items / mutes unreadable (database unresponsive): Inbox APIs return `503 inbox_unavailable`, submitted rounds are rejected (hub retains them for retry),
  and nothing is written, avoiding overwriting database state with incomplete memory. Retry reading on next use.
- A pull to refresh whose collect call fails (the sources' side not started yet, the round not handed over): `POST /api/v1/inbox/refresh` answers `502 inbox_refresh_failed`
  (`error` says in the request's language that the sources couldn't be read and the list wasn't updated, `detail` is the collect's error, `retryable: true`) instead of
  `200` with the list as it was, which looked like a refresh that worked (#1849). The web shows its "Couldn't refresh" toast, Android its refresh-failed notice with Retry;
  iOS / Mac keep the list and show the error only while the list is empty. A source that fails inside a round that ran is not this: that round answers `200` and names
  the source in `triage.sources[].read_error`.
- Readable store, but an item's row cannot be written during a collection round: what the round changed in that item (new items, new messages or mail, a mail's text, items filed as
  over, read elsewhere, settled, muted or superseded) is undone to what the row holds, and nothing is announced for it (`inbox_rollback.go`, #91), unless something else moved the item
  meanwhile (that move's own save writes the row). It comes back with the next round, because the sources hand in the same things again: connectors answer `inbox.collect` from their
  memory, mail rounds read each account's newest page, settling connectors are asked every round. Triage, admission and translation undo their failed rows the same way.
- Immediately after restart, before a round lists sources, the manager still knows plugin-provided connectors (`extensions` in `inbox.config`). Their items still have no fallback and must wait for the model;
  unknown sources behave likewise, never defaulting to unimportant.

### Route external updates to work

Inbox source routing is for people who want specific chats, repositories, mail or subscriptions to update their work.
Forwarding starts after the owner configures a source mode. Persona uses the closest applicable binding and sends
actionable updates to one owner. FYI updates are kept in the target's activity. When a source serves several topics,
Persona can sort short summaries against the bound work. New work appears as a to-do confirmation unless the owner
has enabled automatic to-do creation for its owning direction. An update to work already in progress goes to the
existing owner. The Inbox can receive the update before it is routed. Digest sources wait for their configured window.
See [External source bindings](../protocol/protocol.md#external-source-bindings) for configuration and source identities.

### Move existing forwarding rules

Existing Lark topic links and the Inbox option to forward matching GitHub pull-request and issue updates continue
working. The option is saved as `inbox.todo_forward.enabled` in Assistant settings. Existing repository links
are available to the new routing rules. Persona also recognizes an already linked Lark topic when you inspect that
topic. You can continue opening work and replying to it through the Lark bot as before.

1. Ask Persona in the main conversation to inspect a specific source and check whether the destination you expect
   is already linked. For an older rule that matched a link in message text, add an explicit source link if you want
   that source to use scoped routing.
2. Start with one pull request, issue or topic. Set how often updates should arrive and who handles them. Keep
   **Ask before creating a to-do** if you want to decide about new work. Review the approval card when changing a
   rule through chat.
3. Verify where a new update goes before configuring a wider source. Leave the legacy forwarding switch enabled
   while you still want its behavior for sources you have yet to configure.

To restore the old behavior for new updates, remove the overrides that set a forwarding mode on that path or
restore the original policy. If a parent still sets a mode, the source inherits it. **Off** and **On request**
keep automatic routing off for that source. An update already admitted has a receipt, so changing the rule does
not send it a second time. Items still in Inbox can be handled there. [Routing and compatibility details](../protocol/protocol.md#legacy-routing-compatibility).

### Send failures (2026-11-16, #9125)

A reply that did not go out says why, on `POST /api/v1/inbox/{id}/reply` and on a confirmed reply suggestion (`…/suggestion`), instead of a bare 502.
The extension manager classifies the source's failure where it sends (`server/internal/extmgr/inbox_send_reason.go`) and answers `inbox.reply` /
`inbox.reply_mail` with a coded `rpc.Error` whose message is a fixed sentence; the manager answers the app with the status below and
`{code, error, delivered, retryable, retry_after?}`, `error` a sentence in the request's language (`inbox-errors.json`), no `detail`.
Nothing the source printed reaches the response: no lark-cli stdout or stderr, extension or mail-server message, chat, thread or message id, path or token.
The manager's log line names the item, the code and the request id (`X-Request-Id`); the extension manager's names the send id, the source, the code and an
allowlisted cause (lark-cli's own error kind, else only an RPC code or a Go type).

| Code | Status | `delivered` | `retryable` | When |
| --- | --- | --- | --- | --- |
| `inbox_cannot_reply` | 409 | `none` | false | The item has no way to be answered through its source: the item says so, a lark-cli document comment (read-only), a thread that names no chat, a subscription's row whose item or `reply_to` is gone (#9194). Reply in the source's own app. |
| `inbox_source_unavailable` | 503 | `none` | false | The source's connector is switched off or missing, mail is not set up, lark-cli is not installed or too old, or a subscription is gone or its connector can't be called now. |
| `inbox_source_signed_out` | 503 | `none` | false | lark-cli is not signed in, or its sign-in expired. |
| `inbox_send_denied` | 502 | `none` | false | The source refused the send: a missing permission or scope, or the chat or message is gone or closed to the bot. |
| `inbox_send_rate_limited` | 429 | `none` | true | The source is rate limited (lark-cli, or an extension's `rate_limited` error); `retry_after` seconds and a `Retry-After` header when it said how long. |
| `inbox_send_failed` | 502 | `none` | true | The source answered with any other error (lark-cli exited with an error it doesn't classify, an extension's or a mail server's error). |
| `inbox_send_unrecorded` | 503 | `none` | true | The once-only record (`sent_messages`) could not be claimed, so nothing was tried. |
| `assistant_in_doubt` | 502 | `unknown` | false | Unchanged: it may have gone out (see "Send once" above). |

**Not sent versus in doubt.** Every code but `assistant_in_doubt` means nothing went out: the failure came before the send (no connector, no record, no way to
reply) or the source answered that it refused it. Such a send's record is forgotten, so the same reply asked again is really tried again; `retryable: true`
says the cause is passing, `retryable: false` that the same request fails the same way until the user acts (signs in, turns the source on, replies in the
source's app). A send that may have gone out is never one of them: a dropped link, a send that ran out of time (lark-cli stopped by its own deadline included),
a mail whose SMTP server got the whole message and never answered it (the connection dropped or the wait ran out, `mail.ErrMaybeSent`, #9166; a server's
refusal of it is still not sent) or a manager further on that was in doubt is `assistant_in_doubt` whatever error came with it, its record stays `unknown`,
and the same reply is not sent again.

**For the apps**: say what to do per code (sign in, turn the source on, reply in the source's app, wait `retry_after`), offer
sending again only when `retryable` is true, and keep "check before sending again" for `assistant_in_doubt`. An older manager still answers these
failures with a bare 502.

**Reply-suggestion recovery (#9126)** keeps the existing send-once contract. The first confirmation persists its actual
client identity, edited reply and bound source/target before outward work. An item exposes `suggestion_attempts`, one
authoritative record per version: `version`, `status` (`sending`, `failed`, `unknown`, `sent`), `retryable`, optional safe
`code` / `retry_after`, actual `text`, `at`, and optional `conversation`. `at` is the latest attempt reservation or outcome,
not a later proposal's time. Internal client IDs, frozen action and target fingerprints stay in the manager's stored JSON;
they are not listed. No relational table or connector acknowledgement protocol is added.

- A confirmed, retryable non-delivery retains the current version. Retry uses the first attempt's actual payload and
  send identity, including after reload; a fresh caller UUID cannot turn it into another send or edit that payload.
- A live attempt is Sending. A send whose receipt is unknown, or an inactive reservation loaded after restart, is
  explicitly unknown and not retryable. The server enforces that boundary as well as the Web controls. A missing
  historical record is not evidence that nothing was sent.
- Completion records the old attempt's result, but files, closes handling, clears the current suggestion or marks a
  card sent only for the still-current attempt or its explicitly bound same-operation alias, with the full
  version, authority and source/target fence intact. A new suggestion or source message
  cannot be overwritten by an earlier request returning. Failure to durably reserve an attempt prevents outward work.
- Web cards and the item's reply confirmation show the frozen edited text and outcome. A replaced card keeps its own
  outcome/time and can open **Current suggestion**; only the current safely retryable version offers Retry. Unknown
  delivery tells the user to check the source, rather than claiming Not sent or offering a blind send.
- An equivalent waiting reply proposal reuses its version and card; equality includes the complete action and bound
  conversation/target, including the confirmed edited action. Identical words alone do not authorize another delivery.
  Different actions or a changed authority/target keep the existing replacement rules.
  An intervening different proposal, a later edit back to the uncertain text, or a fresh UUID cannot clear an older
  same-operation uncertainty. A new version inherits that protected result; known outcomes settle its matching aliases
  without touching a genuinely different current action. Resolved display history follows the existing chain bound
  (50 records); at least one proof of every unresolved operation survives that bound. An omitted old receipt remains
  unknown history, never evidence of non-delivery.
- Default **Wrap up** reveals the waiting card. Its sending handoff request opts into `reuse_pending: true` so the
  manager also reuses pending work for the same incoming item across the request-return / suggestion-push gap.
  New incoming work may start fresh, and explicitly typed instructions keep the normal handoff behavior. This adds
  neither a regeneration control nor a permanent client cache.

## One item = one row

| Source | An item is | id |
| --- | --- | --- |
| Lark | Unhandled new messages in one chat (starting at the first new message; later ones merge into it) | `lark:<feedId>:<first message position>` |
| Lark Docs | One document notification-center card (latest mentions / comments / replies to me in one document) | `doc:<notification id>:<update time in milliseconds>` |
| Mail | Unread mail in a thread within one account and folder, or mail already read when it arrived, filed at once (#8298) (same subject after stripping Re: / Fwd: / 回复： [reply] / [list name] means the same thread; when nothing is left after stripping, mail is split further by sender address: one person's subject-less mail is one item, different people's are separate items, #4852) | `mail:<mailbox key>:<thread>:<first mail uid>` |
| Subscription | New items of one row: a thread's for `group_by: thread` (an item without a thread is its own), the whole subscription's for `group_by: source` (#5671), like a Lark chat (titled by the subscription, no tag); its count goes up by the items each pull brings | `<subscription>:<hash of the row's thread and first last>`, thread `sub:<subscription>:<thread, uid or all>` ([subscriptions.md](../extensions/subscriptions.md) "Destinations") |

- Subject-less mail used to be one item per account (sender, preview and "sent to me" taken from the latest); since #4852 it is split by sender, so a subject-less back-and-forth among several people also becomes several items.
  The "no subject" item already open before the upgrade (the old thread key): when every unread mail in it on this page has a sender, it is filed (`done_by=superseded`) and the mail goes into each sender's item, each listed once;
  while some mail has no sender, it stays to hold those (the ones with a sender go to their own items); if it is Needs you or the AI twin worked on it (has a draft), it is kept as it is (mail, tier and draft untouched) and handled as usual, and a sender item a round since #4852 opened with only that mail is filed (`superseded`), so the mail is still listed once;
  when all its mail was read elsewhere it counts as `read` as usual. An item reopened this way is handled by the same rule on the next round.
- A chat / thread has only one **unhandled** item at a time. Messages arriving after it is handled open a new item (only messages after the last one at handling time count).
  Exception: Reopen (single `done:false`, bulk `reopen` / Undo) does not check whether a newer item exists for the thread; it reopens the old one anyway, yielding two unhandled items.
  Later messages always merge into the newest (`UpdatedAt`, ties by ID, `threadIndex.openFor`, #3796); the old item stays unchanged.
- Included Lark messages: gateway `unread` scope (Lark Unread, excluding muted chats) + `mentions` scope (mentions of me in muted groups still count).
  When `lark.cli` is enabled and is the primary source (or the gateway is absent / unavailable), the built-in `larkcli` connector polls `im +messages-search --start … --as user` every two minutes (minimum one minute, with a bounded page count). It keeps messages that mention me or @all, messages in direct chats, and replies in topics where I posted, replied or was mentioned; my own messages never create an item. Overlapping pages are deduplicated by `message_id`, consecutive messages in one chat/topic are one row, and context comes from `+chat-messages-list` / `+threads-messages-list`, which `contextFor` renders through `prompts/inbox/larkcli_context.md` (#9789): a line saying only the current messages are new activity, a `Current messages (new activity):` section with the round's new messages, then, if there is background, a `Background history (context only, not new requests):` section with at most 10 earlier messages from the 24 hours before the first new one, but always the last 3 however old, so a short answer keeps its question (#9865). Each section is in time order; each message starts on a new line with its time in brackets as RFC 3339 in UTC (`[2026-11-05T04:00:00Z]`), and a background message is cut to 300 characters, a current one to 2000. The larkcli kinds' prompt (`prompts/inbox/kinds/larkcli.md`) tells the models to judge by the current section and to read the background only as context. The chat's page of 40 used to come whole, weeks-old messages and cards of thousands of characters included, into every triage of a chat with a new message. Each context line marks whom its message @'s with the tier criteria's `（@了我）` / `（@所有人）` after the sender (both when it @'s the owner and everyone), taken from the message's structured mention ids, never from a display name another person may share (#9124). A later message from me marks that row replied. The cursor and topic set are kept in Postgres settings, not files under `~/.persona`.
  Search indexes most messages at once but some only minutes later (a direct message was seen arriving in search about ten minutes after it was sent), and a message indexed after the cursor passed its time used to be lost. So the cursor stays 10 minutes behind the newest message it saw. Each round also reads up to 10 direct chats from their own history since two hours ago (`+chat-messages-list --start`), rotating through the 50 most recently active direct chats (`+chat-list --types p2p --sort active_time`), which has no indexing delay; the `message_id` ledger drops what was already collected. The window leaves room to recover a missed conversation more than an hour after it was sent, including time spent investigating and rotating through the candidates; each chat read is still capped at 50 messages. Mentions in group chats still rely on search and its 10-minute overlap. A failed direct-chat read keeps what search found that round and reports the error (`server/internal/modules/lark/connector/larkcli/inbox.go`, `sweepDirectChats`).
  An owner reply settles only earlier incoming activity in the same chat or topic. A later peer follow-up stays open, including one found in the context read before search indexes it. Within a chat, source message positions resolve the CLI's minute-resolution timestamps; an opaque message ID or uncertain order never proves the owner replied later.
  As a maintainer opt-in addition, `larkcli` has an `inbox_all_chats` connector option containing chat ids whose every new incoming message is collected. The default is an empty list; this option is intentionally separate from the L4 collection rules.
  `inbox_exclude_chats` (an empty list by default) excludes the named group or direct-chat ids from collection, the direct-chat history sweep, retained things and the read tools. `inbox_exclude_direct_chats` (off by default) instead excludes every direct chat; ordinary groups remain eligible. With either control on, an unverifiable chat type or thread-only read fails closed. Global chat/message search and the `at_me` chat-list mode are refused because they return bodies before result filtering; polling skips the same body-returning search and uses only pre-vetted named-chat history reads, so group mentions that have no named-chat history read are not discovered in this mode. The explicit-id veto runs before any message-body subprocess; the all-direct-chats switch permits one chat-metadata preflight. Empty options add no visibility preflight and preserve the old reads.
  The manager's optional `inbox.visibility` applies the same boundary to stored items: `sources` limits it to named sources (empty means `lark` and `larkcli`), `chats` names group or direct-chat ids, and `direct_chats` is the all-direct-chats switch. Before source startup, the manager applies it to the built-in `larkcli` connector separately from that connector's persisted options; for an extension contributing a selected connector, it sends the boundary in `initialize.inbox_visibility` and in the existing `initialize.config.inbox_visibility` config key for pluginkit providers, only to that extension. A provider that enforces this boundary before returning source content, including positive group-type verification for `direct_chats`, declares the `inbox_visibility` connector capability. With either control active, a selected older provider without that declaration is not initialized: unknown JSON fields must not let it start collection and ignore the boundary. The manager's fallback vetoes explicitly listed targets, mixed chat/thread reads, global or body-returning searches, and every read whose direct/group type an older provider cannot verify before dispatch. The ids therefore stay in manager.json and runtime initialization, not the extension registry. A match is persistently marked `visibility_excluded`, queued or running handling returns to the owner, its handling session is detached, new suggestions (including to-do and conclusion) and confirmation are refused, and repeated quarantine does not resend anything. The marker does not delete message history, suggestions or sent-attempt audit. An unknown Lark chat type fails closed when `direct_chats` is on; with no visibility configuration, legacy eligibility is unchanged.
  Private chat → `dm`, bot / AI private chat → `bot`, group mentions of me / everyone → `mention`, unmuted group → `group`; unread older than 7 days is excluded.
  Only other people's messages count; your own messages never make a chat Needs you.
- Messages sent by the app bound to `lark-cli` (the `appId` reported by `lark-cli auth status --json`) are marked seen and omitted to prevent Persona's own bot replies from looping back into the Inbox. If that app id is unavailable, the connector keeps the existing behavior; messages from other apps remain eligible.
- Mail: the latest 40 messages in each eligible folder per account, within 7 days (#8297). This includes INBOX, selectable archive and custom folders such as GitHub or Apple.
  Sent, drafts, junk, trash, non-selectable folders and aggregate views (All Mail, Flagged, Important) are excluded.
  Rows for custom folders show the folder's name. Matching subjects in separate folders get separate rows, and body reads, replies and attachments use the row's account, folder and UID.
  The list API reads only the first 2 KB of body (HTML mail often contains nothing but `<style>`),
  so new mails fetch the full body again (IMAP `BODY.PEEK`, without marking read) for preview and AI context (`BodyOf` tracks which mail was read).
- Mail already read when a round first sees it (read in another client, or marked read by a forwarding rule, in the
  minutes between two rounds) gets an item too, so no mail the inbox saw is missing from Done (#8298, `mailReadOnArrival`
  in `inbox_mail.go`): one item per thread, read and filed as `read` the moment it is created, with its rule tier
  ("Rule-based", no model is asked). It is never unread and never in Needs you, so it raises no push or badge; like any
  changed item it goes out in `inbox.changed`. Each folder's first successful page sets its **baseline** to the newest UID (zero for an empty page).
  Later read mail above that UID is filed. A change between known UIDVALIDITY values starts a new generation in that folder and invalidates its previous UID targets.
  Earlier hubs without UIDVALIDITY use the lower-page fallback. The setting `inbox_mail_baselines` and the item ID retain the account key for legacy INBOX data.
  Other mailbox keys append `:m:` and the unpadded base64url of the exact folder name. After a known renumbering, new item IDs also include the generation.
  Mail in a thread that already has an open item stays that item's, as before. The mail listing carries no
  `Delivered-To` / `X-Forwarded-To`, so forwarded mail is still not counted as direct (its rule tier is noise).
- Mail that leaves that page (archived or deleted in the mail client, or pushed off by newer mail) follows the same rules (#5993,
  `inbox_mail_off_page.go`): after a round applies a folder's page, its open, unread mail items with no mail on that page are
  settled. One whose newest mail is past the 7 days is handled like a mail past the window on the page; for the others the manager asks
  the hub once a round which of their mails are still in the mailbox, read or not (`inbox.mail_flags`, one read-only IMAP `UID FETCH
  (FLAGS)`, at most 200 UIDs per account, the longest unasked first). Every mail read or gone means read elsewhere ("Lifecycle"); one still
  unread keeps the item as it is, and so does a lookup that fails (a hub from before it). Needs you and an FYI conclusion waiting for
  Got it are only marked read, as on the page.
- Mail's **Add to Inbox** (#5307; Settings › Connectors › Mail and Settings › Inbox Sources, `PUT /api/v1/connectors/mail/settings {inbox}`) is on
  until turned off and kept with the other connector switches. Off, the mail rounds read no account (`readMail`), so the mail items the
  Inbox has stay as they are (and its accounts' read errors as last read); search, replies and the knowledge base go on. Turned on again, the
  next round is a full one and reads mail at once. `mail` declares no inbox capability: `Manifest.InboxSwitch` gives it the switch.

### Duplicate group mentions from Lark connectors

The Inbox list shows one row when the built-in CLI connector and the Lark
companion connector report an unambiguous copy of the same latest group mention.
The connectors can use different message, chat, and sender IDs, so the manager
compares the chat name, sender name, message minute, and complete message text
from both sources. It ignores background messages and allows the known
difference in how the sources render a trailing emoji. Short messages, clipped
text, multiple matches, and a CLI batch with other new messages stay visible.

The companion row keeps its own reply target and state, including a confirmed
reply. When the CLI row has the only reply route, separate work, or an open
request that the companion row would downgrade, both rows stay visible. The
companion's related items link to the original CLI record and its history.
Neither connector changes its collection, stored records, or sign-in. The list
rechecks the match as each source moves. A later distinct message becomes
visible on its own, and the original is still accessible by its ID.

Different groups or people with identical display names can send identical
text in the same minute. Without a shared upstream ID, the manager cannot tell
these cases apart when only one candidate per source is present. The match
therefore affects list visibility only and keeps each original record.

## Docs: mentions in sheets, bases, and documents (2026-10-31)

Handled by the Lark plugin (formerly hub `inbox_docs.go`). Mentions / comments to me in Lark documents, sheets, and bases do not enter the IM chat list, only the **document notification center**
(Lark gateway `GET /documents/notifications`). Each document has one card with only its latest activity (`summary`, such as "A colleague mentioned you"), `atMe`, `url`, `type`
(drive object types: 3 sheet, 8 base, 22 docx, 2 legacy document, 16 wiki). Read once per Lark collection.

- Included: `atMe` or kind mention / comment / reply, within 7 days, not muted. Shares and likes do not count. `kind = "doc"`, fallback Needs you, included in the Mentions filter.
- Cards do not identify the row mentioning me, so hub reads the document to find it (wiki links first use `documents/header` to resolve the underlying sheet / base / docx):
  - **Sheets**: on each tab (at most 6), the first nonempty row is the header. A row containing "@<my name>" in any cell is a mentioning row.
    Columns with state-like headers (状态 / 进度 / 进展 / 阶段—status / progress / progress / stage—or status / stage, etc.) are **status columns**. Allowed values come from the cell's dropdown (data validation), or existing column values if no dropdown exists.
  - **Bases**: records on each table (at most 6, each up to 5 pages × 200 records) containing "@<my name>" in text cells or me in a people field (gateway `accounts` provides names and userId).
    **Single-select** fields with state-like names are status columns; choices are their options, with option colors. Real tables often use a Follow-up owner text column containing "@name."
  - **docx**: take 700 characters on either side of mentions of me; include the latest 3 unresolved comments mentioning me (`documents/comments`, or `base/comments` for bases).
  - Order unfinished rows first (state values resembling 完成 / 已解决 / 关闭 / 上线—completed / resolved / closed / live—count as finished), up to 5 rows. Each includes a title (first column / primary field), at most 6 other fields, and status columns.
- Reply destination (`doc.reply`): reply to a comment mentioning me if present; otherwise comment on the base record (`comments/record`), the sheet cell mentioning me
  (`comments/cell`), or the whole docx document.
- **Change status**: `POST /inbox/{id}/doc/status`. For bases, read the record for an `expect` fingerprint, then `base/edit set`. For sheets, read the revision first;
  dropdown cells require `sheet/structure` `choose` (ordinary `sheet/edit` rejects dropdown / link / mention cells with `DOCUMENT_EDIT_UNSUPPORTED`),
  while plain cells use `sheet/edit` with original `base` values. Reread the document afterward to update the item. Changes may use only allowed values for that column.
- AI twin: prompts include these rows, ref / field / allowed values. `inbox_draft` adds `changes:[{ref,field,value}]`, accepting only rows, columns, and values listed for this item.
  Draft cards show one line per change ("Status: Pending review → Completed · Load-test report"). Send changes status first, then posts the comment; each change can be unchecked in the Edit sheet.
- App: replace the row avatar with a document-type tile (sheets green, bases purple, documents blue), type label Mentions / Comment, second line "Base · A colleague mentioned you."
  Below, show the first mentioning row and its status (tap for a menu; selection writes directly into the document), plus "+N rows." Open `InboxDocDetailView`:
  Open base (in-App document reader), one card per row (fields + a menu per status column), and reply destination.
- Tests: row detection, status columns, fingerprints, reply targets, AI-change validation, plus a live change-and-restore test on your own demo document. Since #1571 these live with Lark Inbox in the Lark plugin (formerly hub `inbox_docs_test.go`, TestDocsLive).
  This repository retains `inbox_agent_test.go` / `inbox_golden_test.go` for block types (`assistant/inbox_blocks.go`) and UI `InboxDocTests` (`MOCK_DOCS=1`). Never test on someone else's sheet.

### Lark via lark-cli: watched document comments (#2691)

The `larkcli` connector has a separate, read-only document-comment poller. It is enabled only for links the user puts in
Settings › Connectors › the lark-cli connector's page › Watched documents; an empty list watches nothing. Options accept `documents`
(`docx`, sheet, Base, or wiki-node links) and `wiki_spaces`. A selected wiki space is expanded through bounded
`wiki spaces list` / `wiki nodes list` reads, so a space never turns into an unbounded crawl.

On each Inbox round the connector runs `drive +list-comments --solved-status false` as the lark-cli user, then reads each
thread with `drive +list-replies`. It keeps an opaque cursor per watched document and deduplicates comment and reply ids.
Rate limits pause the document poller. A permission or not-found result marks that document unreadable: it is probed again every 10 minutes, and at once after a new lark-cli login (another account or other granted scopes); a successful read clears the mark. A missing comment scope belongs to the login, not to a document: no document is marked, the round stops and the connector's status lists the scope until a read succeeds again.
The title, quoted passage (or “Whole document”), latest matching comment text, and a link with the comment id are kept in
one Inbox item per thread (`kind: "doc"`). A comment that mentions the user by open_id is included, as is a later reply
when the user has written in that thread; resolved threads are skipped. The connector never replies or changes a comment.
With `owned_all_comments` (off by default; "All comments on documents I own" in the Lark dialog's "Collect my Lark messages" section), the user's own documents need
no watch list: every 30 minutes the connector lists the documents the user owns (`drive +search --mine --sort edit_time`, only types whose comments can be listed, so no folders),
keeps those edited or opened in the last 30 days, at most 50, and reads each one's comments every 10 minutes (the first
reads spread over that interval). On those, and on watched documents the user owns (owner read once with `drive metas
batch_query`), every new comment and reply from someone else comes in, mention or not. A comment that mentions the user
is Needs you; the rest are left to the Inbox's triage. A document untouched for longer than the window is not read, so a
new comment on it is missed until someone edits or opens it.

The existing document block is used for the detail page (`doc.url`, `doc.link`, `doc.type`, `doc.notice_id`), while the
quoted passage and comment text are in the item's context. An unreadable watched document appears as a warning in the
connector's Settings status and can be removed from the watch list there. The comment poller and state live in
`server/internal/modules/lark/connector/larkcli/doc_comments.go`; the parallel L4 chat poller should be joined in that connector's
manifest and lifecycle, with its own files and state kept separate to avoid making either poller own the other's cursor.

## AI triage

`server/internal/assistant/inbox_triage.go` (manager-owned): every new item, or item with new messages, goes to the small-task model (`inbox.triage` in model configuration, falling back to `ai` in the manager's `manager.json`, default DeepSeek flash), which assigns one of three tiers,
a summary up to 40 characters, and a short reason:

| tier | UI | Meaning |
| --- | --- | --- |
| `action` | Needs you | Requires the user's reply / decision / action or prompt awareness: direct questions, actionable mentions, private requests, approval confirmations, relevant alerts, email needing replies, approaching deadlines |
| `fyi` | FYI | Relevant and worth a glance, no action needed: group progress, CCs, informative notifications |
| `noise` | Collapsed | Marketing, subscriptions, newsletters, automated notices, irrelevant chatter, bot spam |

- Model input is item type (private chat / mention of user / group / bot / mail directly to user / CC or list) plus item text (`context`: at most 20 recent new Lark messages,
  first 3000 characters of the latest mail's full body). Prompts are in `server/internal/prompts/inbox/` (`triage.md`, drafting `draft.md`, judgment `judge.md`, AI twin `agent.md`; `docs/assistant/prompts.md`). Address the owner using `assistant.owner`; `inbox.rules` in the manager's `manager.json` (or assistant-edited settings) contains the user's rules, appended last and overriding defaults.
- **Per-source rules (replacement, 2026-11-04, #1644)**: sources / accounts need different standards; one global rule set cannot cover them. Connector items may include `rules`
  (the user's original words, such as Lark-plugin Triage rules `triage_rules`; `docs/protocol/connector-protocol.md`). The item **uses only its own rules**, with no global `inbox.rules` appended.
  Only items without rules use global ones. Default tier criteria above are unchanged; user rules remain last and authoritative. Rules are stored with the item (not listed), updated from each new version when activity arrives,
  up to 4000 characters. The assistant can change plugin-source rules (`inbox.rules.<plugin>` in `persona_settings`, confirmed on the phone; `docs/assistant/assistant.md`). Built-in GitHub and Messages connectors do not yet have their own rules (the Telegram extension has `triage_rules`).
- **Needs you is what is aimed at the owner** (#7986). The criteria list what is *not* Needs you: requests that @ someone
  else or a bot / on-call assistant / agent, group discussions that neither @ nor name the owner, automated alerts, monitoring
  and capacity reports, on-call and ticket notices (unless one assigns the owner to act now), @all notices, small talk, threads
  already answered or closed (or answered by the owner), and Persona's own Lark bot pushes and cards ("要你批准", "在问你",
  "请验收", "任务进展" and the like, which the app handles). When unsure, the question is whether someone waits on the owner or
  the owner's own work slips (it used to be "choose action"). @me no longer makes something Needs you by itself.
  A real person's direct message stays Needs you by default, even without a question, unless it is plainly small talk.
  The tier criteria (`tiers.md`, shared by triage and grouping) spell out what the `（@了我）` / `（@所有人）` markers mean:
  a message marked with both counts as @-ing the owner; a Lark group message (the Feishu plugin's batches and the `larkcli`
  connector's context) without them did not @ the owner, and a name @'d in the text is someone else. Other sources write
  no markers, so their absence there says nothing and the kind and text decide (#9124). Grouping's input names the other side of a direct chat in
  the connector's kind words (`group_input.md`: a person, a bot or an app), so a bot's "在问你" isn't read as a person asking.
  The user's own rules are unchanged and still come last and win.
  For a `larkcli` human DM, the triage input also names the newest message's preview separately from the conversation.
  This boundary belongs only to triage and its no-write preview; drafts, task judging and handling conversations keep
  their existing item inputs without a repeated preview.
  An older answer or acknowledgement does not resolve a later request. An `[Image: ...]` marker is an unread image,
  not its contents: a new work-related DM screenshot still needs the owner to look or respond unless explicit text
  makes it informational or confirms that the current issue is resolved. This does not add OCR or a fixed connector
  tier; the model and the user's rules still decide. Already-triaged FYI items are not replayed automatically (#9124).
- **Sorted again once after the criteria change**: `inboxTriageRev` (`inbox_triage.go`) is the criteria's revision, and an item
  records the one its tier was given under (`triage_rev`, kept, not listed; a rule tier given under the current criteria stays,
  unless it is provisional: below). Each triage pass also takes up to 8 open Needs you items tiered under an older revision (newest activity first): Lark and
  mail items and connectors' events, skipping anything the AI twin or a follow-up touched (handling, a suggestion, a due check, an
  agent record) and tiers a policy sets, and asks the model again with `triage.md`. A new tier records a `triaged` chain entry as
  usual (an FYI one the owner already read elsewhere is then filed as read, like any FYI item). A model error leaves the item as it
  was (no rule tier replaces the model's) and it is tried once a run; a model that is held, missing or too slow gives the try
  back for a later pass.
- If the model cannot triage (unconfigured, no quota / concurrency full, error, timeout), use fallback rules (private chat / mention of me → action; everyone-only mention without me → fyi, an announcement; group → fyi; direct mail → fyi; others → noise), with reason "Rule-based."
  **A rule tier given because the model failed (an error such as a 404 for a removed model, or a timeout) is provisional**
  (#8298): the item keeps `triaged_at` behind and records `rule_of` (its `last` then; kept, not listed), and later
  passes ask the model again until it answers, then take its tier as for any triage. The retries are capped: up to 8 a
  pass (newest activity first), none until 10 minutes after the last failure, the wait doubling with each pass whose
  retries got no answer, up to 2 hours (memory only, so a restart allows one early pass); any model answer, for a new
  item too, makes the next pass retry at once. Items the AI twin or a follow-up touched (handling, a suggestion, a due
  check, an agent record) and handled items are not retried. The AI twin and policies treat a provisional rule tier like
  a triaged one (`tiered()`). With no model set up, or a held connection, the rule tier stays as before.
  Connector items use their rule-provided fallback (`tier`). Model-triaged items (empty `tier`) from hub-owned connectors (GitHub, Telegram, Messages) still fall back to noise.
  **Plugin-provided connectors have no fallback**: noise would silently hide items the user must see (below).
- **Scope (requested 2026-11-04)**: "Triage before admission" and "No admission without fallback" below **apply only to plugin-provided connectors** (installed plugins or plugins in the manager's manager.json, not bundled personad connectors;
  currently the Lark extension). Hub-owned mail, GitHub, Telegram, and Messages behave unchanged: new items appear immediately, then triage in the background; timing and fallback (no rule tier means noise) remain as before.
  Identify by plugin ownership (`extensionConnectors`, recorded when hub starts a plugin connector and passed each round as `InboxSource.extension`), not name.
  Source subscriptions (#2039) are handed in the same way, one source per subscription with `extension` set: their items carry a fallback tier unless the user chose to wait for the model ([subscriptions.md](../extensions/subscriptions.md) "Destinations").
  A subscription's rows follow its recipe's `group_by` (#5671): one a thread, or one for the whole subscription, which is triaged as one row like a Lark chat (an item in it that needs the user makes it Needs you). The source also has `counts`, so a move adds the thing's `extra.count` (the items its newest pull brought) to the item's count instead of one; for every other generic connector `extra.count` is not read there and a move counts one. A row is `read` when its recipe maps `seen` and its newest item was seen in the source (#6176), and is then handled like any connector's thing read elsewhere. A subscription that failed its last run, needs a new recipe or was asked to wait carries a `read_error` (#6189), so it is listed as behind like a built-in. A row whose recipe maps `reply_to` can be replied to (#6198): `inbox.reply` for it goes to the subscription's connector with the item's own thread and id.
- **Triage before admission (#1644)**: hold new plugin-connector items aside for a collection round (`inbox_admit.go`). After collection, triage in the background; only then write to the database and list them:
  - Model succeeds: use its tier and summary.
  - Model fails, fallback exists: admit by fallback with "Rule-based." When the model errored or was too slow that tier is provisional and retried as above (`rule_of`, #8298); under no model or a held connection it is final.
  - Model fails, no fallback: **do not write this round**. Do not store hidden rows (hard to inspect). The connector answers `inbox.collect` from its own memory and resubmits next round, naturally retrying until the model works.
  - Time limit: new items must begin triage within 20 seconds of a batch start (concurrency 4, at most 30 seconds per item). Unreached items wait for the next round (a round with progress immediately triggers another).
    Background triage does not block collection or pull-to-refresh (`{"type":"inbox"}` pushed afterward). Only one batch runs at once; new items arriving during it are not written that round and are resubmitted next round.
  - Lark-plugin items always have a fallback (hub type rules: private / mentions of me → Needs you, groups → FYI, etc.), so **new Lark items are never withheld**; triage merely moves before listing.
    Only items from other plugin connectors without a rule tier are withheld, all reflected in triage status below. Muted new items are still marked done directly.
  - Existing Inbox items with new activity update first, then retriage in the background as before (concurrency 4, at most 90 seconds per item, batches sequential), with fallback on failure.
    When quota is exhausted, untriaged items keep fallback until model recovery. Plugin items without fallback retain their previous tier and retry next round, never turning into noise.
  - Empty text (`context` empty) skips triage and enters directly as before, shown as Needs you.
- **Triage status**: `GET /api/v1/inbox` (and refresh responses) includes `triage`: `{state, reason, waiting, sources?}`. `waiting` counts each plugin source's new items withheld in its latest triage due to failure without fallback,
  or not reaching them (hub-owned sources are never withheld and are excluded). `state` is the most important cause: `unconfigured` (no triage model) > `paused` (no quota / full concurrency) > `error` (model error) >
  `timeout` (too slow) > `behind` (unfinished batch). `reason` is human-readable; `sources: [{source, name, state, reason, waiting, read_error?, read_reason?}]` separates sources.
  `read_error` reports a failed source read in the latest round (`timeout` / `rate_limited` / `login_refused` / `failed`; unknown codes become `failed`), and `read_reason` is a human-readable sentence in all three languages.
  Existing items remain; previously collected data still present in a hub-held round is also used. Clear the diagnostic on the next successful read. Memory only: after hub or manager restart, it is unknown until the next round.
  Memory only, recomputed per round for read sources; unread sources retain status until removed. Nothing waiting yields `{"state": "ok", "waiting": 0}`.
  - **Mail accounts** (#1849): each mail account that the latest mail round (every 3 minutes, or a refresh) could not read is its own entry, `source: "mail:<account id>"`
    (the name a source that moves between managers has) with `name` its address, `state: "ok"`, `waiting: 0`. Codes: `timeout` for the round's deadline, the network's, or the mail service's own
    20-second limit; `login_refused` when, after the failed read, mail settings show the account's error (its server refuses the login: a wrong password or authorization code,
    a revoked OAuth grant); `failed` for anything else. `login_refused` gets its own words because it doesn't clear by itself: the line tells the owner that the account needs
    fixing, while the server's exact answer stays in mail settings. An account deleted during the round is not named. A round without mail (a poll) keeps the entries;
    transient failures (`timeout`, `rate_limited`, `failed`, including unknown codes) appear only after three consecutive
    failed mail rounds for that account. Non-mail rounds do not count. A refused login appears immediately; once shown,
    a notice remains until a successful read or removal. Failure counts are capped at three and reset on recovery or
    account/source removal. Explicit source diagnostics still report each read immediately.
    The next mail round replaces the notices, and they go with mail itself. Mixed versions: a sender from before this change (an older hub or extension manager) has no
    `mail.failed`, so the account is left out as before and nothing is named here; an Inbox from before this change ignores the field and reads the round as before.
  Apps use it for the waiting-for-triage banner below, and iOS / iPadOS, Android and web show sources with a `read_error` in a quiet neutral notice under it, one line per source, "name: `read_reason`" (#1849; mock `MOCK_INBOX_UNREAD`). All three use the same fallback words when `read_reason` is absent, and the notice clears with the next good read.
- **Waiting-for-triage banner** (#1773, consistent on web, iOS / Mac, Android): if `triage.state` is not `ok` and `waiting > 0`, show a non-dismissible caution-colored (orange) banner at the top of the Inbox list, below search / filters and above the first section.
  It follows list refresh (Inbox pushes, reconnect, pull-to-refresh), disappearing on recovery to `ok`. Older hubs without `triage` show none.
  - Text: with exactly one waiting source (`waiting > 0` in `sources`, with `name`), "{waiting} {name} messages waiting for triage: {reason}"; otherwise "{waiting} messages waiting for triage: {reason}."
    Use hub `reason`, trimmed. If empty, use state defaults: `unconfigured` no triage model configured · `paused` triage model paused · `error` triage model error · `timeout` triage model too slow · `behind` too many new messages, queued.
    Unknown state without reason shows only "{waiting} messages waiting for triage." Chinese labels place no spaces around the source name ("有 2 条GitHub消息在等分拣", meaning 2 GitHub messages waiting for triage).
  - Button: Set Up only for `unconfigured`, opening Settings › Model configuration › Information filtering (`inbox.triage`). Show only if this device can edit settings, otherwise text alone. Other states only explain the cause.
  - All clients use a clock icon. Web `TriageBanner` (`docs/web/web.md`, "Inbox") has Set Up to the text's right. iOS / Mac `InboxStore.triage` uses design-system `NoticeBanner` (`apple/Persona/DesignSystem/Notice.swift`, `Theme.Icon.time`)
    as the first list row (iPhone below filters with Set Up below text; Mac first row in the list column, Set Up on the right). Set Up opens an Information filtering model-edit sheet, hidden in demo mode.
    Android shows Set Up only if it can read `GET /api/v1/model-config/purposes/inbox.triage` (reply-only devices or hubs lacking that purpose show text only; hidden in demo); details in `docs/android/android.md`.
    Normally paired devices behave identically across clients: devices able to save this setting get the button.
  - Screenshots / integration use mock `MOCK_INBOX_TRIAGE=<state>[:<count>[:<source names, comma-separated>[:<reason>]]]` (`unconfigured`, `behind:8:Lark`, `error:7:Lark,Mail,GitHub`, `error:2:GitHub:Model has no quota`).
- First run 2026-10-27: of 51 items, 3 needed you and 29 were noise (all marketing mail). An example summary: "A contact asks whether the Codex code was merged into 1013, awaiting your confirmation."

## Event grouping (2026-11-04, #1680)

One row per chat is too coarse: a group can discuss several things at once, some requiring me and some not. Combining them prevents accurate prioritization and obscures the subject. Connectors declaring `group` (the Lark plugin)
submit new messages from a chat as **a batch awaiting association**. The manager asks the model which item each message belongs to; **one item (event) is one Inbox row** (`server/internal/assistant/inbox_group.go`,
protocol in `docs/protocol/connector-protocol.md`, "Event grouping").

- **One ledger**: message-to-item membership is recorded only in that row's `meta`. For connectors declaring `meta`, their program writes it (`group.append`: one item at a time, submit meta plus messages, return appended meta).
  With only `group`, hub writes events from the batch itself (no meta; `context` is the text of messages assigned to the item; protocol "Two writing modes").
  **Hub does not parse meta**; it stores and passes it unchanged to the model and plugin. Initially, association stays within one chat.
- **Capability negotiation**: hub declares `hub.capabilities` including `inbox_group` and `inbox_group_state` in plugin `initialize`; connectors declare `group` in the manifest. Those maintaining meta also declare `meta` and `meta_description`
  (prose explaining meta fields; disallowed without `meta`). `group.tool` and `group.fallback` need no declaration; if absent, return `unknown_method`. The model sees unavailable tools; with no fallback, an untriageable batch waits for the next round.
  If either side lacks support, retain one row per chat. Old hubs / plugins do not affect each other (old hubs do not recognize `group`; see protocol "Old hubs").
- **Model behavior** (`inbox.triage`): prompt = generic association rules (`prompts/inbox/group.md`: replies / quotes, same topic, consecutive messages from one person belong together; a long gap plus new topic starts another;
  uncertainty merges into an existing event; the prompt asks for at most 3 new events per batch; tier criteria shared with triage in `inbox/tiers.md`) + plugin `meta_description` + source rules (`rules`, otherwise global `inbox.rules`)
  + batch text. Existing events for the chat are preloaded (the no-query result of `events` below), saving one call. Each model response is JSON, either one tool call or a result.
  Each touched event is tiered as a whole using its own assigned new activity and unresolved requests, not the batch's last message.
  A reply or acknowledgement assigned to another event cannot resolve it; an earlier reply cannot close a later request in the same event.
  The output budget is `256 + 48 × new messages + 160 × min(new messages, candidate events + 3)`, clamped to 1024–4096 tokens on every model call.
  The batch `context` may interleave the user's own messages by position: their sender is `我（自己说的，不归类）`, they have no message id of their own (a trailing `(回复 <id>)` or `(话题 <id>)` names another message), they are context only, they are not in `new`, and the model must not assign them.
  At most 4 tool calls per batch; at most 2 minutes total, 45 seconds per call.
- **Tools**: `events` (hub-owned: up to 10 open events in the same chat plus up to 6 handled within 3 days, open first and each group newest-first, `{id, title, summary, tier, version, done, meta}`; `query` filters text in titles, summaries, meta before the independent caps);
  `messages_before`, `messages_around`, `search` (plugin-owned through `group.tool`, avatars / media stripped, at most 8 KB).
- **Result**: `assign` (each new message's event: existing ID or `new:1`…) and `events` (title, summary, tier, reason for each touched event; new events may `continues` a handled one).
  For a handled event, the optional `reopen` in its `events` item defaults to true: a genuine follow-up appends to and reopens the same event. Only pure closeout (thanks, acknowledgement or emoji) sets `reopen: false`, which appends and records the messages but keeps the event done; `reopen` has no effect on open or new events.
  Hub validates: every new message assigned exactly once; existing events must have been queried in this batch; handled events are allowed except done noise events and done events in group and bot conversations (all non-dm event kinds) with at least 50 messages, which are excluded from candidates; direct-chat done events have no message-count exclusion; tier must be one of three and title required. The 3-new-event limit is prompt guidance only: exceeding it does not invalidate the result.
  An invalid result or unparseable JSON is handed back to the model once for correction, consuming one call allowance without adding a tool call; if no allowance remains, or the correction is still invalid, the model is treated as unavailable and the fallback is used.
- **Counts**: hub maintains total message `count`, initialized from assigned messages and incremented by newly assigned messages each batch, ignoring plugin `extra.count`. `started_at` is fixed at creation.
- **Inbox writes**: for connectors maintaining `meta`, call `group.append` once per touched event; retry only failed events, 3 attempts total. Write Inbox items using returned `id`, model title / summary / tier, `triaged_by: "model"`,
  treating this version as triaged. Grouping records `title_by: "model"` or `"rules"` when it writes the title; once `title_by` is `"assistant"` or `"user"`, grouping leaves the title and summary unchanged while tier, reason and urgency still advance. If the user handled an event meanwhile, append to and reopen that event rather than replacing it with a new continuation. An unchanged `last` is a retransmission, with no store / push (push once per ID + version). Muted events remain directly done.
- **Fallback**: unavailable model (unconfigured, no quota, error, timeout) or invalid result invokes plugin `group.fallback` for structural grouping (reply / quote chains, topics, consecutive messages by one person, time gaps).
  Without `group.fallback`, do not write the batch; wait for the model and resubmit next round, like Inbox items without fallback.
  It receives events under the same rules as `events`. For an event the fallback opens, use plugin `rule_tier`, reason "Rule-based," `triaged_by: "rules"`; do not send this version back to the model.
  A fallback-opened event's title drops a leading `<where>：` when non-whitespace text remains; the conversation name is already stored in `where`.
  Ordinary fallback assignment to an existing event updates its messages and version but keeps its title and summary. Its tier is monotonic: it takes the higher of its current value and the appended event's rule tier (`fallbackEventTier` when that tier is absent), so it never falls. Existing noise events and events their processing conversation is handling (`handled()`) do not rise. A rise uses the rule reason ("Rule-based"), preserves `triaged_by` and urgency, and records a `triaged` chain entry. `triaged_at` advances so this version is not retriaged (user policy tiers still apply).
  In a direct chat, fallback-new events join the most recently updated supplied candidate whose latest message activity is within 2 hours, whether that candidate is open or done; a done target reopens. The rewrite keeps the title and summary unchanged and applies the same tier and rise behavior, using the rewritten events' own rule tier.
  Fallback may also assign supplied done events directly, and the hub reopens them by default.
  Validate fallback results by the same rules; invalid batches remain unwritten until the next round.
- **One pass per batch**: association runs in the background without delaying collection or pull-to-refresh. One batch per chat at a time (resubmitting the same batch or twice in one round still counts as one); at most 4 chats concurrently, others retry next round.
  Once every batch message belongs to an item, the plugin stops submitting it; unfinished batches resubmit remaining messages.
- **List**: events are ordinary items with added `triaged_by`. `meta` and their chat membership stay on hub, not the list (plugin detail slot `inbox_item` receives meta through `ui.render`, #1681).
- The manager accepts plugin-reported read / done state for chat events; a connector sends it only after the hub advertises `inbox_group_state` (see "Following up"). A chat's pre-grouping row is not merged into events; it remains until handled.

## Moments (2026-11-01)

Relevant Lark Moments activity (formerly Toutiao Circle) also enters the Inbox, via the Lark plugin (formerly hub `inbox_moments.go`): comments on my posts,
replies to my comments, and mentions of me in posts or comments. Likes and new followers are not items. No feed (`docs/extensions/feishu.md`, "Persona is not a second Lark"); only activity involving me.

- Source: gateway `moments/notifications?category=message`, collected with Lark every 2 minutes (one call, latest 50 notifications); older than 7 days excluded.
- **One post per item** (Thread): new comments / replies on the same post merge into its unhandled item; `count` is notification count. Activity after handling opens a new item.
  Title is the post's opening; `sender` / preview come from the latest comment.
- Context: post (whether mine or someone else's) + latest 15 comments (mine marked Me, reply target marked), explicitly noting company-wide visibility.
- Triage: mentions default to Needs you; comments / replies default to FYI (often encouragement or chat), with normal model judgment.
- **Send** posts a comment (`POST /moments/posts/:id/comments`), replying to the latest relevant comment, or commenting on the post if it mentions me directly.
  Drafting and AI twin prompts warn that it is visible company-wide; say only what can be public.
- If I already replied elsewhere (the latest post comment is mine and newer than the item), mark done (`done_by=replied`). If all notifications were read in Lark and the item is not Needs you, mark `read`.
- App: Moments icon and label (Mentions for mentions of me, also in the Mentions filter). Opening shows post and comments (`LarkMomentsPostView`, `pushed`).
  DEBUG: `-openInboxItem <id>` opens an item for detail screenshots; mock includes `moment:mp1:mn1`.

## Connector: GitHub (2026-11-02)

Connector activity needing me (`docs/plans/plan-connectors.md`) also enters the Inbox. A connector implements `connector.Inbox`, returning candidates (`InboxThing`, one per thread),
which the manager turns into items (`server/internal/assistant/inbox_connectors.go`; hub delivers candidates each round). Rules match Lark chats: one unhandled item per thread, later activity merged with `count` incremented;
after handling, new activity opens another. A thing whose `at` is older than its open item's latest step is an earlier step read again (a connector re-reading after a restart): it is not counted and never moves the item back (#1527), and its read state is not applied to the item (#2149). This errs toward unread: a thread read elsewhere that the connector reports only at an older step stays unread here until its next activity or its end, instead of an unread one turning read.
A new item never takes the id of an existing one (`sha1(thread, last)`): that step already made an item, perhaps handled since. If the connector says an item ended (merged, closed, replied by me), automatically handle it (`done_by` = read / replied); read elsewhere and not Needs you means `read`.
Triage, drafting, AI twin, done state, and retention use hub's generic logic.

- **GitHub** (`server/internal/modules/github/inbox.go`): with no public hub endpoint for webhooks, poll `/notifications` (unread only), following `X-Poll-Interval` (usually 60 seconds),
  with `If-Modified-Since`; 304 fetches no details. Every 10 minutes, fetch unconditionally (reading notifications on GitHub may not change Last-Modified). Fetch PR / issue, comments, and reviews only for changed threads, up to 30 per round.
  Read up to 5 pages of 50 (only the first page sends `If-Modified-Since`). A thing counts as read elsewhere when it is missing from two list reads in a row that would have had its notification: the whole list was read (it ended on a short page),
  or, with 250 or more unread or when a later page fails, its own newest notification is newer than the oldest notification read (the list comes newest first, so it would have been there).
  GitHub pages by offset, not from a snapshot: a notification read, or moved to the top by new activity, while the poll is between pages shifts the rest, so one still unread can be on no page once. A thing missing once makes the next poll read the list in full (no `If-Modified-Since`) to settle it.
  Any notification in the list keeps its thread unread, also one the inbox leaves out (a reason it does not take, a muted repository).
  The time compared is the notification's `updated_at`, kept apart from the thing's time: a CI item the follow-up moved to a later run (GitHub sent no notification of it) is not inferred read while the connector runs, and that run makes a read item unread again (#2149).
  These times are kept in memory only: after a restart the connector rebuilds the item at its notification and may infer that read, which the hub then ignores for an item already at the later run (above).
  With under one tenth of core request quota remaining, poll notifications only and show a localized quota warning with the minutes until reset.
  A rate limit (429, or 403 for a secondary limit or a used-up hour) on any GitHub request pauses the background reads, the poll included: until `Retry-After`; without it, until `X-RateLimit-Reset` only when the hour is used up (`X-RateLimit-Remaining: 0`),
  otherwise for a minute, doubling up to an hour while the limit keeps coming back: within one step of the end of the last such pause, whatever was answered in between (a pause GitHub named is not a step).
  A pause also stops the notification pages still to read. The poll waits it out instead of failing, so the watch is not restarted early.
  During a pause, the localized status says no content is being read and shows the minutes until the pause ends. This takes precedence over the low-quota notification-only warning, even if the quota resets later.
- Included `reason`: PRs requesting my review (`review_requested`), mentions (`mention`), team mentions (`team_mention`), new comments / reviews by others on my PRs / issues (`author`),
  CI results (`ci_activity`), assignments (`assign`). Others (`subscribed`, `comment`, `state_change`…) are excluded. One item per PR / issue (`example-org/example-repo#12`).
- **Noise reduction (2026-11-03, #577)**: the user found it too noisy. A read-only hub count showed all 52 open GitHub items as Needs you, including 42 CI failures on feature / automatic branches (many pushed by Agents using the user's account),
  multiple items per branch, with reruns counted separately. Causes: one item per CI notification; model-only tiers with prompts saying CI failures and review requests generally need the user, so everything became Needs you. Changed to:
  - **Rule-based tiers** (`InboxThing.Tier` + `Why`, item `reason` identifies the rule); only undecidable cases go to the model:
    - Human review requests, human mentions, requested changes (`CHANGES_REQUESTED`), assignments: Needs you.
    - Bot PR review requests, bot mentions / comments on my PRs, approvals, team mentions: FYI.
    - Ordinary human comments on my PRs: model decides (prompt: requests for changes / answers need you).
  - **Group CI failures by (repository, branch)** (thread `ci:<owner>/<repo>@<branch>`, title `<repo name> · <branch> 的 CI 失败` so items of several repositories' `main` differ). Read the branch from notification title (`<workflow> workflow run[, Attempt #N] failed for <branch> branch`;
    these notifications have no URL), merging reruns and later failures:
    - The thing holds the newest failure: notifications come newest first, and one older than the thread's thing is skipped (several notifications per branch; after a restart all unread ones are read again, 30 per poll).
    - GitHub notifies only the account that started a run, so failures from other people's pushes come from the follow-up check instead: the branch's newest failed run, ended at least 3 minutes before (its own notification, about 20 seconds after the run, is the same failure), joins the open item as its latest step.
    - Default branch and my open PRs: Needs you; other branches: FYI; automatic branches (`train/*`, configurable): excluded entirely.
    - Automatically mark handled without deleting (`done_by=read`, explanatory `reason`) when the branch is green again: every workflow's newest finished run on it passed, cancelled and running ones aside ("GitHub: later succeeded";
      a succeeded notification has it checked at once, otherwise Actions runs are checked every 10 minutes), branch deleted, or PR merged / closed.
  - **Configuration** (Settings › Connectors › GitHub › Inbox; `github.inbox` in the manager's manager.json supplies defaults; stored as `connectors.options.github` in settings):
    - CI failures: Off / Default branch only / Include my PRs (default) / All (all Needs you).
    - Muted repositories: exclude all activity and handle existing items.
    - `ignore_branches`: silent branches (`path.Match` patterns, default `["train/*"]`, editable only in manager.json).
  - **Backlog**: after each collection, hub passes open GitHub items to connector `Settle` (`connector.InboxSettler`). Old one-per-notification CI items ("GitHub noise reduction: group CI failures by branch"),
    muted repositories, automatic branches, and CI items when CI is off are marked done, not deleted. Items still in unread notifications are retiered under new rules.
  - Read-only count comparison on real hub data 2026-11-03: Needs you fell from 52 to 13 (42 old CI items handled; remaining 6 human mentions, 2 review requests,
    4 CI failures on default branch / my open PRs, 1 PR comment). Seven feature-branch CI items and one bot-PR review request became FYI.
- Ended: merged / closed → read; latest thread entry is mine → replied; review request withdrawn (I am no longer a reviewer) → replied if I reviewed, otherwise read. Done state **does not write back** GitHub read state.
- Context: title, state, author, requested reviewers, description (1500 characters), latest 8 comments / reviews oldest first (600 characters each). Prompt kind line: `prompts/inbox/kinds/github.md`.
- **One account at a time (#7832)** (`server/internal/modules/github/account.go`): the things, the notifications cursor and the thread page's kept answers belong to the account they were read as. The connector asks GitHub whose a token is (`/user`) before it reads or answers anything as an account with it, and counts the accounts it has read as (the epoch). A login that differs from the last one, however it arrives (another sign-in source chosen, a token a computer hands over after GitHub refused the last, a device-code sign-in, a login that changed while the manager was down), retires the old account's state in the same step: its things leave `Collect`, the cursor and the caches start again, and the change wakes Watch, which reads the new account's inbox and says the inbox changed without waiting a poll. A `/user` answer to a credential since replaced is dropped, so answers arriving out of order never put the old account back. A poll, a thread page or a reply that began under the old account is refused before its next request and writes nothing back. The same login again (a refreshed token, the same account from another computer) keeps everything.
  - **Kept across restarts**: the login, the epoch and whether the old account's items are still to be closed are kept with the connector's sign-in (the manager's sealed store for `github`), so a restart neither forgets the account nor takes the next login for the first. It is read again after the hub gives the connector back from an extension that held its id, and written back if that extension saved over it. A store that cannot be read stops nothing: the connector goes on with what it has in memory until it can, and a device sign-in writes over a secret that no longer opens.
  - **Replies** are checked before anything is sent. From the first change of account on, every move's `last` carries the epoch it was read under (`<time>#<epoch>`; a hub that has only ever read as one account keeps plain times, so nothing changes for it), and the hub hands a reply the `last` its item holds: a reply to a move of another epoch is refused, whether or not the connector still holds the thing. The thread alone is never enough, since both accounts may have it.
  - **Closing the old account's items**: after a change, once the account in use has tried to read its inbox, the next `Settle` answers "over" ("GitHub: the account changed") for every open item the connector holds no thing of, once. The hub's items come only from the things it was handed, and it asks in the round it takes them, so whatever has no thing here was read as the account before, including items answered through Persona, items older than things are kept, and items from before a restart. A thread both accounts have unread is held: its item stays and the new account's thing, a new move to the hub, takes it over. That the closing is done is saved before the hub is told, so a save that fails, a manager that stops or an answer that is lost leaves old items open (they cannot be answered) and never closes one of the account in use.
  - Limits, all from the hub starting an item only for a move later than the thread's closed one: going to another account and back leaves the first account's still-unread notifications out of the inbox until their threads move (their items were closed when the account changed); so does a shared thread whose item was closed before the new account's thing was read (past the 30 threads of the first poll, a detail read that failed, or a first poll that failed or ran with few requests left). Where the old account's move on a shared thread is later than the new account's notification, the old item stays open and cannot be answered. An item taken over keeps a reply suggested for it while it was the old account's until the new move replaces it. A renamed login counts as another account. The knowledge base's GitHub cursor is the hub's and is not reset. Items a hub collected under another account before this change carry no mark and are not told apart. Without a store that can be written the account is not kept, and a login that changes across a restart is not noticed.
- **Send** = PR / issue comment (`POST /repos/{o}/{r}/issues/{n}/comments`); tapping Send is confirmation. `reply_to` says "Comment on … (visible to everyone with repository access)." CI failures cannot be replied to (Hand to AI only).
- App (only iOS passes `?sources=all`; Mac, Watch, Android do not see this step):
  - Row: the latest actor's initial (GitHub sends no picture yet: whose a row would show is #10931), GitHub badge, `label` (Review requested / Mentions / CI failed…), second line "repository#number · who: content."
  - GitHub filter appears only when items exist.
  - Generic `InboxConnectorDetailView`: title, thread, Open in GitHub (in-App web card), hub-read content. No PR page or diff page.
  - With no GitHub items, the UI is unchanged.
  - Mock `MOCK_GITHUB_INBOX=1`; UI tests `InboxConnectorTests`.

## Assistant rows (2026-11-13, #6430)

The assistant can put one of its conversations into the Inbox as a row, so a result is not missed behind a quiet unread dot. It happens only on the user's explicit request (the `inbox_post` tool's description says so; its required `reason` is shown on the row) or when a scheduled task is set to put its runs there (`docs/assistant/assistant.md`, "Put it in the Inbox when done"). Who it serves: the Assistant's everyday users, who say "tell me if there is anything worth a look" or "send me the briefing every time"; no engineering words anywhere.

- **An internal source** `assistant`, made by the manager like a push subscription's: no connector, no hub reading; the hub only forwards `/api/v1/inbox`. Code: `assistant/inbox_post.go`.
- **The generic row.** An item carries the `connector` block an app draws for a source it does not know (`thread` = `<machine>/<conversation>`, `label` Assistant, `url` the conversation's page in the web app at the hub's `public_url`), `kind` `assistant_post`, the tier the post asked for (`action` or `fyi`) with `reason` as its why, the title and a summary line. Without a `public_url` the row has no `url`: it still draws and its content is in the row. Checked on the mock hub (`MOCK_ASSISTANT_INBOX=1`): the web app draws it in its source group and opens the conversation in the side panel from the row's Open button; the iPhone app takes the same path for any item with a `connector` block (`InboxGroups`, `InboxConnectorViews`). An app that knows the source can open the conversation directly from `thread`. The row's link is a page of the route table (`docs/protocol/deep-links.md`): the iPhone opens it as the conversation in the app, from Open the Chat (a chat icon, not the browser's) and from the Timeline's received entry (#9928).
- **Its text is Markdown (#9041).** The row keeps it as written in `body` (line breaks kept, cut past 6000 characters), and its `summary`, `preview` and the chain's received message are the same words without the marks on one line (`textfmt.PlainMarkdown`), so a list row, the Timeline and an app that does not know `body` show words, never `**` or `- `; `context` is the title and the Markdown, for the assistant. A task set to every time posts the run's whole last reply, read from its conversation (the session list's `last_text` is one line cut at 200 characters; it is used only when the reply cannot be read). The iPhone and the web draw `body` in the detail with the chat's Markdown, folded to ten lines with Expand when it is long, and the Timeline's received message under it to three lines (the whole text is above; on the iPhone and the web the received entry of a row with a link only goes to the conversation, one arrow, #9928, #9930); a row from before has no `body`: its summary is drawn as Markdown there and without its marks in the list row and the Timeline. Checked on the mock hub (`MOCK_ASSISTANT_INBOX=1`, the row `assistant:s3`; `GET /mock/assistant_inbox?mode=before|1` switches it to how older hubs kept it and back): UI suite `InboxAssistantMarkdownUITests` (`TEST_RUNNER_PERSONA_MOCK_HUB`, `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps `assistant-body-<phase>-<hub|older-hub>-<light|dark>[-expanded|-timeline].png`); web `InboxEvent.test.tsx` and `lib/inbox.test.ts`.
- **One row a conversation.** A post for a conversation within 30 minutes of its row's last one updates that row (title, summary, tier, the newest content) and opens it again if it was handled; a later post starts a new row. A task set to every time posts after each run that answered; a failed or empty run posts nothing.
- **The user's rules still apply**: a policy set for the source (Rules, "Policies") changes the tier like any source's.
- **One switch.** Inbox Sources lists an **Assistant** row (`b.assistant`, icon robot) whose Add to Inbox switch is `Settings.InboxPost` (`inbox.post` on the bus; on unless turned off). Off, `inbox_post` answers that nothing was added and says to tell the user in the conversation, and tasks set to every time add nothing; rows already there stay. The Relationships view shows it with the other Persona-side sources.

## Merge rows and Add to to-dos (2026-11-14, #6480)

Two manual actions in Select mode (web, iPhone), the row menu and the detail page. Who it serves: anyone whose Inbox split one matter into several rows, or who got a deadline in a message no machine can read. Plain words only. Code: `assistant/inbox_merge.go`, `assistant/inbox_todo.go`.

- **Merge** (`POST /api/v1/inbox/merge {items:[{id, version?}]}`, 2 to 20 rows, across sources too): the newest row survives and stands for the rest. The others become its **members**: hidden from every list (`merged_into`), not open as far as triage, the AI twin and the counts go (`InboxItem.open`), but still collected into by their own source, so a new message in a member's thread updates the survivor (`followMerged`, after each round and each grouped batch): newer preview and sender, count plus one, highest tier, reopened if it was handled, judged again. The survivor lists its members (`merged_from`: id, source, account name, title) so a reply can name the side (`POST /api/v1/inbox/{id}/reply` with a member's id replies in its source). It takes the highest tier (and keeps it: a model's verdict on the merged row, or a member's tier that moved since, never lowers it below the rows it stands for, `liftMerged`, #7230), the members' counts, their chain entries merged by time and "Merged from N rows"; its title starts as the newest row's and the small-task model then writes one for the whole row in the background (`prompts/inbox/merge_title.md`, from every row's title, summary and text; `title_by` is `merged` and grouping leaves it alone; a title the user wrote is never replaced, one the processing conversation wrote is, by a merge; no answer leaves the newest row's title; Undo puts the old title back unless it was changed since, #7229). Its processing conversation is the newest row's; a member's conversation stays with the member and its entries are in the chain. A member whose survivor left memory is let go (it shows again). Merging a survivor again takes its members along. A row filed, a member already, gone, or changed since the list (`version`) is skipped; fewer than two left merges nothing (409).
- **Undo** (`POST /api/v1/inbox/merge/undo {id}`): the members get their rows back, the survivor its tier and count; a message that came meanwhile stays in the row it came into. A link or push naming a member opens the survivor (`GET /api/v1/inbox/{id}`).
- **Add to to-dos** (`POST /api/v1/inbox/todo {items:[{id, version?, title?}], due_at | due_date}`): the user picks the due time and the client works it out in its own zone (1 hour from now, today all day = `due_date`, a date and time, or neither). One to-do per row, all with that due time, titled like the row (or the user's edit) with its summary as notes, pointing back at the row (`todos.inbox_item`, migration 0512). The row counts as handled: `done_by=todo`, "Added to your to-dos" on its chain with a `todo` link. A row that cannot be filed gets no to-do. Several rows that are one matter: merge first. `POST /api/v1/inbox/todo/undo {items:[{id, todo_id}]}` deletes the to-dos and reopens the rows.
- **AI suggestion**: `inbox_suggest` kind `todo`: `text` the title, `due` a day (all day) or an RFC 3339 moment, only when the message states a deadline. The detail page's capsule shows it with both editable; `POST /api/v1/inbox/{id}/suggestion {action: confirm, version, text?, due?}` adds the to-do and files the row as above. `now` is `confirm_todo`. Only a full device confirms it; only the processing conversation suggests it.
- **Web client** (`web/src/lib/inbox-actions.ts`, `views/inbox/InboxTodoDialog.tsx`): the selecting bar has **Merge** (two or more rows of one manager; icon only on a phone) and **Add to to-dos**, which opens one dialog for the due time: 1 hour later, today all day, a picked date and time, no due date. The row menu and the item's ⋯ menu carry Add to to-dos for one open row. A merge removes the members from the list when the hub answers and a row with members says "Merged from N rows" in plain grey small text; both end with a toast whose Undo calls the undo routes. A `todo` suggestion is a card with the title, day and time as fields and one **Add to to-dos** button (a day alone is all day). A chain entry with a `todo` link opens that to-do.
- **iPhone client** (`apple/Shared/InboxMergeTodo.swift`, `Persona/Views/Inbox/InboxTodoViews.swift`, `InboxStore.merge` / `addTodos`): Merge and Add to To-Dos are two actions. Selecting's bottom bar has **Merge** (two or more open rows of one manager) and an **Add to To-Dos** menu (1 Hour Later, Today All Day, Pick Date & Time… in a sheet, No Due Date); a row's long-press menu and the item page's ⋯ menu carry Add to To-Dos for one row. Both show at once when the hub answers and end with a toast whose Undo calls the undo routes. A row with members says "Merged from N rows" in plain grey caption text. The to-do suggestion card has the title, a Due Date switch, the day, an **All Day** switch (on: the day alone is all day; off: a time, 9:00 to start with) and one **Add to To-Dos** button. A chain entry with a `todo` link opens that to-do in a sheet.

## Muting (2026-11-03, connector v2 · V3, #670)

All sources work alike. Long-press an Inbox row for Done / Reopen plus available mute targets: Mute repository (GitHub), Mute chat
(Lark chats, Messages, Telegram), Mute sender (people in Lark groups, mail senders, latest GitHub actor). Names appear below (repositories omit owner; menu labels never wrap).
Muting handles this item and other covered open items in the same source, reason "Muted: <name>," with toast "Muted ‘…’; future items will not enter the Inbox."
The handling chain says "Muted, done" for existing items, newly admitted items and grouped events; `done_by=read` stays unchanged for compatibility.
Muted under More at the Inbox's top right lists all mutes (name, source · repository / chat / sender), with swipe-left or long-press Unmute. Unmuting admits only future items; already handled ones stay handled.
Settings › Inbox Sources (#5278) shows the same mutes per source on each built-in source's page, with Unmute (`inbox.mutes`, `inbox.unmute`).

- **APIs**: `GET /api/v1/inbox/mutes` (creation order, oldest first); `POST /api/v1/inbox/mutes {item_id, tag}` mutes the item's value for `tag` (read from the item,
  not supplied by the client; nonexistent item 404, missing tag 400), returning `{mute, settled}` with additionally handled items. `DELETE /api/v1/inbox/mutes/{id}` unmutes.
  Item `mutes` lists allowed mute targets, which the menu renders.

- **Manager** (`assistant/inbox_mutes.go`): mutes live in `inbox_mutes` settings (source + tag + value + name). Each collection handles covered open items, so future
  messages arrive already done. Item mute targets (`mutes`): connector `InboxThing.Tags` (`repo`, `chat`, `sender`; a subscription's rows carry
  their newest item's `sender`, #5306, by its `sender_id` when the recipe maps one, and its `chat`, #6165); Lark chat (`lark.feed_id`) and latest group speaker (`lark.sender_id`); lowercase mail sender address.
  Lark Docs and Moments items have none.
- **GitHub muted repositories**: formerly GitHub connector setting `muted_repos`, now source `github`, tag `repo` mutes. On manager connection,
  hub migrates old entries through `inbox.repo_mutes` and clears them from GitHub settings. Settings › Connectors › GitHub (and old Apps) still read / write `muted_repos`;
  hub translates through the manager, so both entry points share the same data.

## Policies (2026-11-06, #5747)

A policy says, for a chat, a sender or a repository, how its items are handled, decided in code rather than by a model. It targets exactly what a mute targets (the item's `mutes` options, read off the item, the same for every source) and has two parts, each optional:

- **Tier**: `action` (always Needs you), `fyi`, `noise`, or unset (triage decides).
- **AI twin**: `first` (escalated even when not Needs you, and started before other queued events) or `never` (never escalated; queued items without a processing conversation return to the user on the next handling pass), or unset. Conversations already running or starting continue as before.

Rules:

- **A mute wins**: a muted item is filed whatever its policy says. Among policies the most specific tag wins: `sender`, then `chat`, then `repo` (so a Lark group can be FYI while one person in it is always Needs you; in groups `sender` is the newest message's sender).
- **The tier is applied after triage**, so the model's title, summary and urgency stay: after model triage (`triage`), at admission (`admitInbox`), when an event is grouped (`putEvent`), and for a connector's rule tier when the round files it. A connector's rule tier (`InboxThing.Tier`) files its item without triage at all — for generic connector things and for a migrated connector's block items alike (`fillConnectorItem`, `fillMigrated`) — and a policy read in the same round overrides it. When the policy changes the tier, the reason reads "Your rule: <name>"; a tier other than Needs you clears `urgent`. If the model fails, the fallback tier is overridden the same way. Items an event's processing conversation is handling keep their tier (the conversation decides).
- **Setting or changing a policy** re-applies it to open, triaged, unhandled items at once (`applied` count). An item whose row cannot be written keeps its old tier and nothing is announced for it; the call then answers 500 and the policy stays kept, so setting it again applies it. **Deleting one** leaves the tiers it gave; the next triage of new content decides again.
- **The twin's part** is read by `escalate` and the queue (`sortQueued`, `startInboxAgents`). Before starting or adopting a conversation, the queue rechecks the effective policy, including sender/chat/repository precedence. A queued item with no session and a `never` policy returns to its tier's section, clears pending notes and retry metadata, and records the rule in its handling chain. This also applies during backoff or when every conversation slot is occupied, and to queues restored after a restart. A failed release write is rolled back and retried on the next pass without starting a conversation. `inbox.agent.enabled` still gates the twin as a whole; nothing here sends anything without confirmation.
- Stored in the settings key `inbox_policies` (`assistant/inbox_policies.go`); one policy per source + tag + value.

**APIs**: `GET /api/v1/inbox/policies`; `POST /api/v1/inbox/policies {item_id, tag, tier?, twin?}` → `{policy, applied}` (the value is read from the item; a policy for the same target is updated; one left with neither part is removed); `PATCH /api/v1/inbox/policies/{id} {tier?, twin?}` (both as they should be; neither removes it); `DELETE /api/v1/inbox/policies/{id}`. Clients find an item's policy by matching its `source` and `mutes` options against the list.

**Clients** (web; iOS, Mac and Android #2140): an item's menus offer "Rule for This Repository / Chat / Sender…" after its mute options, one per option (web: a row's right-click menu and the item page's mute button; iOS: a row's long-press menu and the item page's ⋯; Mac: a row's right-click menu; Android: a row's long-press menu). The rule sheet sets the tier ("Leave to Triage", "Always Needs You", "FYI", "Collapsed") and the twin ("As Set", "AI Twin First", "Never AI Twin"), prefilled from the item's rule in the list (same source, tag and value); Save posts a new rule or patches the one there, **Remove** patches both parts empty. While it saves, the open, triaged items of that source carrying that option that no conversation is handling take the new tier at once (status with it, reason "Your rule: <name>", `urgent` cleared unless Needs you), and a failure puts them back. **Rules** in the Inbox's ⋯ menu (Mac: the toolbar and the Inbox menu) lists them newest first with Edit and Remove. Code: web `views/inbox/PolicyDialog.tsx` and `lib/inbox.ts`; iOS / Mac `Views/Inbox/InboxPolicyModel.swift`, `InboxPolicySheet.swift`; Android `feature/inbox/…/InboxPolicies.kt`. With several managers every client lists each manager's rules (and, in Muted, its mutes) under its name, read with that manager named, and edits, removes and unmutes on it; a manager that can't be read says so in its place. Screenshots: the mock hub with `MOCK_POLICIES=1 MOCK_GITHUB_INBOX=1` (two rules to start with); iOS DEBUG `-showInboxRules YES` opens Rules, `-openInboxRule chat|sender|repo` the sheet for the first item with that option, `-openInboxRule edit` the newest rule's sheet; the UI test `InboxRuleTests` (`TEST_RUNNER_PERSONA_MOCK_HUB`).

Free-text triage rules (`inbox.rules`, a source's `triage_rules`) still exist for what is not a chat or a person ("release notifications are FYI"); policies are for the cases where the user knows exactly who or where.

### Reply policy (opt-in, #11901)

`inbox.reply_policy` in `manager.json` (`docs/operations/config.md`) adds an opt-in, server-side category gate for AI owner reply drafts (`server/internal/assistant/inbox_reply_policy.go`). It serves people who explicitly configure advanced Inbox AI handling; it adds no everyday-facing UI control and is **disabled by default** (`enabled: false`, or omitted/empty), preserving existing queue, suggestion, and confirmation behavior byte-for-byte.

- **Server-side routing**: the classifier (`inbox.triage`, prompts `prompts/inbox/reply_policy.md` and `reply_policy_input.md`) outputs only a category name and short reason. The server maps that category to one of four routes — `owner_draft`, `bot`, `owner_only`, or `no_reply`. A model-supplied `route` or any other undocumented field is rejected rather than treated as authorization. Only `owner_draft` allows creating or confirming an AI owner reply suggestion.
- **Default category table** (used when `enabled: true` and `categories` is omitted; a non-empty `categories` map replaces it):
  - `owner_draft`: `routine_reply`.
  - Four `owner_only` categories: `approval_commitment`, `sensitive_personal`, `external_public`, `escalation_decision`.
  - Five `bot` categories: `bot_notification`, `system_alert`, `workflow_notice`, `automated_digest`, `bot_command`.
  - `no_reply`: `no_reply`.
- **Scope and independent review**: `sources` limits the policy to the named sources (an empty list covers every source), and `require_independent_review` requires a second classifier to agree before an owner draft is authorized. The effective revision digest binds the sources, categories, rules, timeout, and review requirement, so changing any of them invalidates older authorization snapshots.
- **Fail-closed on every uncertainty**: unknown categories, invalid routes in configuration, ambiguous normalized category keys, classification failures or timeouts, missing snapshots, and stale content versions or policy revisions always fail closed and never produce or send an owner draft.
- **Three independent gates**:
  1. **Queue gate** (before AI handling in `handPass`, `escalateWithPolicy`, `startInboxAgents`, `retryInboxHandling`, and manual `draftReply`): classifies candidate items and refuses to queue or start AI handling unless the live server route is `owner_draft`; pre-queued items whose policy revision or content changed are released without starting a conversation.
  2. **Suggestion gate** (when accepting `inbox_suggest` with `kind: "reply"` or legacy `inbox_draft`, including from handed-off conversations): refuses non-`owner_draft` categories, unknown/invalid classifications, failures/timeouts, and mid-call revision/content changes; stamps `{category, route, content_version, policy_revision}` onto the accepted `InboxSuggestion` (persisted, not listed).
  3. **Send gate** (immediately before the confirmed outward sender call in `confirmSuggestionReplyLocked` and under `once.mu` in `replyInboxDoMode`): verifies the suggestion's policy snapshot matches the live content version, live policy revision, and live `owner_draft` route, then runs a **final-text veto** on the actual text to be sent (original or owner-edited). Any veto returns `409 inbox_reply_policy_blocked` (`delivered: "none"`, `retryable: false`) with **zero sender calls**.
- **Independent owner confirmation & audit snapshot**: normal `owner_draft` items still require owner confirmation; the persisted `InboxSuggestionAttempt` stores the original `proposal`, actual confirmed `text`, `category`, `route`, `content_version`, and `policy_revision` (kept in storage, omitted from listed copies). Direct manual owner replies via `POST /api/v1/inbox/{id}/reply` remain available for the owner.

## Folders (2026-11-09, #2463)

Each item can be filed under a **folder path**: a `/`-separated string such as `work/alerts/engine`, every segment a folder level, so the inbox can be browsed as a tree next to the tiers. Tiers say how urgent something is; folders say what it is about.

- **Who files**: a step of the triage pass. When `triageInbox` ends (and when it has nothing to triage), `fileFoldersSoon` starts `fileFolders` in its own background goroutine, one pass at a time. It takes the listed items with no folder that the model has not been asked about at their current content (`FolderOf != Last`), open ones first and then newest first, at most 60 per pass, and asks the `inbox.triage` model about them **20 per call** (`prompts/inbox/folders.md`, `folders_input.md`). It covers every item, including those triage never sees (handled items, rule-sorted connector items, items from before folders were on). A failed call stops the pass, and the next pass asks again. Ingestion and the list never wait for it.
- **How the model picks**: it is shown the folders in use with their item counts (up to 80 of the most used) and told to reuse one whenever it fits, opening a new branch only when nothing does (≤ 4 levels, short lowercase segments, project / service names as written). The user's **folder rules** come first, and `inbox.rules` are shown as context. Up to 12 items the user filed by hand are shown as examples of their habits. Answer: `{"folders": {"<id>": "a/b/c"}}`. Unknown ids are ignored. An empty or unusable path leaves the item unfiled but asked (`FolderOf`), so the same content is not asked about again.
- **Stability**: an item the model filed keeps its folder when new messages come in. Only unfiled items are asked about. An item the user filed (`folder_by: user`, even into no folder) is never touched by the model.
- **Paths** are normalized the same way everywhere (`normalizeFolder`, `web/src/lib/inbox-folders.ts`): segments trimmed, inner whitespace collapsed, control characters, empty, `.` and `..` segments dropped, each segment ≤ 40 characters, ≤ 6 levels, ≤ 200 bytes. Case is kept.
- **Data**: `folder` and `folder_by` (`model` | `user`) on the item (listed), `folder_of` as bookkeeping (not listed). Items are JSON rows in `inbox_items`, so there is no migration. The settings `{enabled, rules}` are kept under the settings key `inbox_folders`. **Off by default**: it spends model calls and adds the folder bar, so the user turns it on.
- **APIs**: `GET /api/v1/inbox/folders[?sources=all]` → `{enabled, rules, folders: [{path, count, open}]}` (the folders the items of the list's window are filed in, most used first: open items and those done within 3 days, so `count` also counts done items past the list's newest 200). `PUT /api/v1/inbox/folders {enabled?, rules?}` → the same (turning it on starts a pass). `PUT /api/v1/inbox/{id}/folder {folder}`: a path files it by hand, `""` takes it out of every folder (the model leaves it alone too), and `null` hands it back to the model. A path that normalizes to nothing is 400, and a failed write is rolled back (500). It answers with the listed item.
- **Web**: with folders on (or any item filed), a folder bar sits under the filter chips: crumbs from **All folders** down to the folder picked, and the folders right under it as chips with their open counts (plus **Unfiled** at the top). Picking a folder filters the list, its sections and **Clean up all** to that folder and everything under it. Agents waiting are left out, and the choice is remembered like the other filters. Rows show their folder when the list is not already in it. **Move to folder…** is in a row's menu and the item's header, with the folders in use as suggestions, **Let AI decide** and **No folder**. **Folders…** in the ⋯ menu turns automatic filing on and edits the folder rules.
- **Assistant tools** (#3099, `assistant/inbox_folders.go`, descriptions in `prompts/mcp/inbox/`): `inbox_folders` lists the folders in use across every source (path, items, open) and whether automatic filing is on; with `folder` it also lists the items filed there and below, with `unfiled: true` the items in no folder, open first and then newest first, at most 50, each with the id `inbox_move` takes. `inbox_move {items, folder}` makes the same move as `PUT /api/v1/inbox/{id}/folder` (`setInboxFolder`): a path files the items by hand, `""` takes them out of every folder, and the model leaves them alone afterwards. It has no "back to the model" form. Every id is checked first: an unknown id, or a path that normalizes to nothing, moves nothing. A failed write leaves that item as it was and names it in the error.
- **Not yet**: iOS, Mac and Android browse and move by folder (they ignore the new fields until then), and policies can't target a folder.

## Events and handling chain (phase 1, 2026-11-05)

Requirements are in the team's design description "Inbox evolution, phase 1." Existing items are **events**: arrival, triage, manager handling, result, user confirmation, and execution all happen on one item,
with a chronological **handling chain**. Additive only: IDs, sources, existing fields / APIs unchanged; only a new Working state. Code: `assistant/inbox_events.go` (chain, sections, relationships),
`inbox_handling.go` (handling chats and suggestion shape). All fields optional; old hubs send none (Apps keep sectioning by `tier`), and old Apps ignore them.

**Added item fields** (included in lists, details, and all item-returning APIs):

| Field | Meaning |
| --- | --- |
| `status` | Manager-derived section for unhandled items: `action` Needs you · `working` Working (new, between Needs you and FYI) · `fyi` FYI · `noise` Collapsed; absent when done. `tier` keeps its meaning, so old Apps show Working under Needs you |
| `urgent` | Urgent: triage (`triage.md` and grouping `group.md` also return `urgent`) says someone is waiting or a deadline is near (action only). Urgent items stay in Needs you while the manager prepares advice, with "AI is preparing a suggestion" on the card |
| `recap` | Manager-written summary: background, progress, blockers; plain text, up to 600 characters |
| `handling` | Bound handling chat: `{owner: "manager", state, machine_id?, session_id?, since, error?}`; `state` is `looking` (AI reviewing) / `agent` (its sub-chat working) / `waiting` (confirmed action sent, awaiting response) / `done` (turn finished with suggestion or conclusion) / `failed` (failure / timeout, returns to Needs you without suggestion) |
| `suggestion` | At most one current suggestion (see "Handling chats and suggestions") |
| `chain` | Chronological handling chain, oldest first, latest 50 entries at most. A list asked with `chain=last` (`docs/protocol/protocol.md`, "Inbox delta") gives a done item its newest entry alone |
| `chain_count` | Only in a list asked with `chain=last`: how many entries a done item's chain has, when it has more than the one listed |
| `related` | Cross-atomic relationships: `[{id, title, source}]`; tap to jump to that item, each retaining its own state |

**Section calculation** (`status()`): collapsed stays collapsed (noise never upgrades); reply / Agent suggestion → Needs you; handling chat reviewing / sub-chat working → Working (urgent stays Needs you);
awaiting response → Working; failure → Needs you; otherwise `tier` (FYI / Needs you; untriaged counts as Needs you). A `conclusion` suggestion uses its chosen tier.

**Handling chain** records only key calls. Program-observed steps are **written by code where they occur**, not model-authored or investigative steps. The sole exception is `reported`: the processing conversation says that it or an Agent did something, the program records that statement without verifying it, and the apps draw it apart as "Assistant says". Each entry is `{id, at, kind, text, detail?, count?, sender?, messages?, link?, via?, edited?}`.
`text` is a manager-written localized line; Apps may also render by `kind`. `link` is the tap destination: `{type: "source", position?}` (Lark: item's chat at that message),
`{type: "source", uid}` (mail: that mail in the item), `{type: "source", url}` (connectors / Docs: in-App opening), `{type: "source"}` (item's source page),
`{type: "session", machine_id, session_id}` (chat / sub-chat), `{type: "item", id}` (another item). Without `link`, tap expands `detail` / `messages` inline.
A `received` entry's `via` is the item's `source`; a `triaged` entry's is `assistant`.

| kind | Recorded when | Tap destination |
| --- | --- | --- |
| `received` | New messages enter this item: "Received 3 messages · A contact" (mail: "Received 2 emails · …"), with `count`, `sender`; `messages` is this batch (up to 10, 200 characters each; Lark from plugin context lines without their trailing notes for the AI (`(id=…, …)`), omitted if unreadable, preview for one message; mail uses summaries). Consecutive `received` entries merge as they are written: `count` adds up, `at` is the newest, `messages` keeps the newest 10, and `link` and `id` stay those of the first batch in the run. `sender` stays when it is the same and is cleared when it differs, with `text` regenerated from the merged count and sender. Replies from others also go here. Messages outside this item are not recorded | First batch message's source position: Lark first batch `start_position`, later batches by chat sequence (count backward by new-message count, never earlier than previous batch's last message); mail first uid; others source page |
| `triaged` | Triage: "Triaged: Needs you" / "Triaged: Needs you (urgent)," `detail` = tier · reason. Same-tier retriage with unchanged urgency is not recorded | Expand inline |
| `handling` | A handling chat takes over this item (once per chat) | That chat |
| `sent` | Persona sends on your behalf (`/reply`, confirmed reply suggestion): "Sent a Lark message" / "Sent an email" / "Posted a comment" | Lark chat tail (where reply lands); replied mail; others source page |
| `agent` | Handling chat opens a sub-chat: "Opened sub-chat: <title>" | That sub-chat |
| `confirmed` / `rejected` / `instructed` | You confirm / reject / say something (`detail` = executed content or your words; edited text has `edited: true`) | Expand inline |
| `self_replied` | You reply directly at the source, outside Persona: "You replied in the source"; without a processing conversation the item becomes done (`done_by=replied`), while with one it stays open in `waiting` or `agent` | Source page |
| `failed` | Handling fails / times out (`detail` = reason) | Expand inline |
| `concluded` | Manager conclusion (`detail` = conclusion) | Expand inline |
| `done` | Handled: "You marked done" / "You ignored it" / "Read elsewhere; done" / "Muted, done" / "Manager reviewed; no action needed." **An added kind outside the contract table**; Apps render unknown kinds from `text` with a generic icon | Expand inline |

- Stored in item JSON (`inbox_items.data`, no new table), IDs `c1`, `c2`… increasing indefinitely; discard oldest beyond 50.
- **Old items** (stored before this phase) have no chain. On read, backfill two entries from existing fields (`received`: start time, count, sender; if triaged, `triaged`: tier / reason), then append normally.
- Record even with AI twin disabled (`inbox.agent.enabled`): arrivals, triage, your own replies, and done state remain.

**Event splitting**:
- **Atomic threads** come from sources: connector `InboxThing.thread` (one Lark chat, mail thread, GitHub PR / issue).
- **Noise stays one item per atomic thread** (`foldNoise`): all noise events newly created by the model in a grouping batch merge into that chat's open noise event, or one new noise event if absent. `foldNoise` skips done targets: they neither absorb folded noise nor are merged into another event. Noise events do not absorb higher-tier messages
  (those create separate items), so noise is never split by topic, merged with other work, or upgraded. Fallback messages joining an existing noise event likewise leave its tier and title unchanged.
- **FYI / Needs you** still split by topic ("Event grouping" above).
- **New messages follow the existing event**: while it is being handled (`handling` looking / agent / waiting, or a suggestion awaiting you), new messages record only `received`; tier, title, summary, urgency stay unchanged,
  with no retriage (`TriagedAt` advances directly). Its handling chat decides what to do (below). Inactive items retriage as before. A genuine follow-up to an already handled event appends to and reopens it, adding a `reopened` chain entry after `received`; pure closeout stays done with only the `received` record.
- **Cross-atomic related items**: phase 1 does not merge them, only records `related` on both sides, at most 10, excluding same-atomic-thread items. Currently populated when a connector says an item grew from another (`InboxThing.parent` points to an item in another source / chat),
  and from `related` submitted by a handling chat with its suggestion (only existing Inbox items are accepted).

## Following up (2026-11-13, #6611)

An item stays one matter for as long as it goes on: a handled event takes the messages that continue it and reopens, the processing conversation names it, and the Inbox knows what it waits on and when to look at it again. Nothing here is a new table, section, handling state or endpoint; the fields are optional on the item's JSON (`inbox_follow.go`), so old apps ignore them and new apps keep today's drawing when a hub sends none. This section is the contract the lanes of #6611 build on; each behavior lands in its own PR.

**Item fields**

| Field | Meaning | Listed |
| --- | --- | --- |
| `where` | Where the matter happens, for people: a group's name, the other person of a direct chat, a repository. Empty for mail | Yes |
| `title_by` | Who wrote `title` and `summary`: `model` (grouping), `rules` (the connector's fallback), `assistant` (the processing conversation's `inbox_suggest` `title` / `summary`), `merged` (the model's title for rows merged by hand), `user`. Once `assistant`, `merged` or `user` wrote them, grouping leaves both alone | No |
| `follow` | `{who, next, check_at, checks, due}`: who or what it waits on (the model's words), the next step, when to look at it again (RFC 3339; 24 hours when not given), how many times in a row a look found nothing new, and whether that time came with nobody to look (the AI twin off). Apps read only `who`, `next` and `check_at` | Yes |
| `now` | The item's state key (table below), worked out from the item alone: no clock, no settings, so the list memo holds (`inbox_list_memo.go`) | Yes, never stored |

The manager refreshes `where` from the connector's `chat` tag (else its `repo` tag) on every write, and the mute menu uses it for the chat's name, falling back to the title.

**Waiting entries and defaults.** An `inbox_suggest` call with `follow` and no `kind`, a confirmed reply, or a direct `/reply` that has a processing conversation puts the item in `handling.state = waiting`, writes `follow`, and records a `waiting` chain entry. With no time supplied, the default is to look again 24 hours later. A confirmed or direct reply records that entry with `via=user`; a direct reply records `sent` first, clears any remaining suggestion, and tells the processing conversation with `NoteSent` without waking it. Empty `who` means waiting for the other side's reply. The chain line uses `assistant-inbox_chain.waiting_who` when `who` is present and `assistant-inbox_chain.waiting_reply` otherwise, with `{who}` and `{at}`. A direct reply without a processing conversation still files as replied, and new messages arriving while any reply is out keep the existing kept-open behavior. Old-App card sends and replies confirmed in a handed-off conversation also keep filing as replied. Rewriting a follow preserves `checks` and clears `due`; new messages clear both while keeping `who`, `next` and `check_at`.

**Due checks.** Before the disabled-twin release, each handling pass finds open `waiting` items whose `follow.check_at` is due and which have no current suggestion. An item whose latest content has not yet reached its processing conversation (`agent.of != last`) is left for `escalate`, so a source-message wake never also records an empty due check and content that arrives while the twin is off is not lost. With the twin off or no handling-chat session ID, it calls no model and wakes nothing: it clears `check_at`, sets `due`, sets `handling.state = done` and `tier = action`, and records the localized `check` entry so the user can look. Otherwise, when `checks` has already reached 3, it likewise clears `check_at`, sets handling done and the tier to action, leaves the item as `follow_up_ask`, and records the ask without waking the chat. Otherwise due checks are admitted within the same running count used by `startInboxAgents`: existing `looking` chats and admitted checks consume capacity, and queued new chats use the remaining slots. An admitted check increments `checks`, clears `check_at`, records `check`, and wakes its existing chat with `NoteCheck` carrying `who`, `next` and the consecutive check number; `Agent.Back` is `waiting` and handling is `looking`. After reading the current state, the chat chooses exactly one result: a conclusion (`kind: conclusion`, `outcome: done` when the matter is handled), one suggestion such as a nudge (sent or executed only after user confirmation), or continued waiting with no `kind`, an updated `recap`, and `follow` carrying a new `check_at` or `check_in`. A recap-only response, a timeout, or a turn ending with no draft (`no_draft`) is not a failure: the item returns to `waiting` with a fresh default 24-hour `check_at`, without a `failed` entry. New source messages reset `checks` and `due`; the user's instruct, reject or confirm actions and a direct Persona reply reset them too, so later automatic checks start a fresh sequence.

**State keys.** The manager sends a key; every app puts it in words from one table, `spec/fixtures/inbox/now.json` (English, Simplified Chinese and Japanese, with the cases Go and the apps check). Without `now` (an older hub), or with a key an app does not know (a newer hub), the apps keep their own state words. When several states hold, the first wins: done, folded, a suggestion, queued (the agent record says queued and has no session yet, while `handling` already says looking), the processing state (looking, Agent working, waiting, failed), `follow_up_ask`, `check_due`, then the tier. `{reason}` is `handling.error` as it was written (already words, in the language of its time); without it the plain "AI couldn't handle it". An unrecognized `done_by` leaves `now` empty so the apps keep their own done words. A `conclusion` is not an action suggestion: as in `status()`, it suppresses the queued and processing-state keys, and the later follow-up or tier rules apply.

**Visible in the assistant.** When Inbox is enabled and available, the main conversation's live snapshot has a "Following up" section for open `waiting`, `follow_up_ask` and `check_due` items: at most five, by `check_at`, with the title, place, who or what is waited on, next step and local check time. With no qualifying items it says there are none, so an incremental snapshot replaces rows that have since ended. When Inbox is disabled or unavailable, the whole section is omitted. Briefing facts separately give the model up to five matters waiting for more than a day and up to five whose check is due in the owner's today (including `follow_up_ask` and `check_due`). Both views are read-only: they never advance a check, rewrite a follow or change an item's state.

| `now` | When | Section | English |
| --- | --- | --- | --- |
| `confirm_reply` / `confirm_agent` / `decide` | A reply or Agent suggestion waits; `decide` when it is handed back (`handback`) | Needs you | Waiting for you to confirm a reply / Waiting for you to start an Agent / Waiting for your decision |
| `queued` | Queued for the AI twin | In progress (urgent: Needs you) | Queued for the AI |
| `looking` | A processing conversation is looking | In progress (urgent: Needs you) | AI is looking |
| `agent_working` | An Agent it started is working | In progress | Agent working |
| `waiting` | Waiting on the other side | In progress | Waiting for {who} · look again {time}: {next} (parts left out when empty; no `who`: Waiting for a reply; `{time}` as `spec/fixtures/inbox/time.json` writes it) |
| `follow_up_ask` | Three looks in a row found nothing new | Needs you | Checked 3 times with no news. Keep following? |
| `check_due` | The time to look again came while the AI twin was off | Needs you | Time to look again: {next} |
| `failed` | The AI could not finish | Needs you | AI couldn't finish: {reason} |
| `needs_you` / `fyi` / `folded` | Nothing prepared, by tier | Needs you / FYI / Folded | Needs you / FYI / Folded |
| `done_replied` / `done_manager` / `done_user` / `done_read` / `done_dismissed` / `done_muted` / `done_superseded` | Done, by `done_by`; a muted item is `read` and its newest `done` entry has `by: "muted"` | Done | You replied / Assistant checked: nothing to do / You marked it done / Read elsewhere / You dismissed it / Muted / Replaced by a newer item |

**Chain.** An entry may carry `via`, where its step happened: a source's id (the item's `source`, or another source's), `agent`, `assistant` or `user`; entries from before have none and the apps go by `kind`. A `done` entry may carry `by`, how it filed the item as a value (a `done_by`, or `muted`). New kinds, each with its `text` written by the manager so older apps draw it generically:

| kind | Recorded when |
| --- | --- |
| `reopened` | New messages reopened a handled event |
| `waiting` | The processing conversation waits on the other side, until a time to look again |
| `check` | It was looked at again when that time came |
| `agent_stopped` | An Agent started for it stopped, with its last words or failure |
| `reported` | A step the processing conversation says it or an Agent took but the program did not observe, submitted as `inbox_suggest` `reported: {text, by}` (`by`: `agent` or `assistant`, default `assistant`). The entry's `via` matches `by`, its `text` is the server's localized "Assistant says" prefix plus the statement, and `detail` keeps the unprefixed statement on one line, clipped to 120 characters. It may accompany a suggestion, recap, `title` / `summary` or `follow`, but is refused on a filed item. Identical text matching the item's latest `reported` entry is not recorded twice. The program does not verify the statement; apps draw it apart from their own steps: `text` (the label and the words, "Assistant says: …") is for apps from before, and apps that know the kind show `detail` under their own label (`text` when `detail` is empty) |

**An Agent's stop returns to the matter.** After a turn stays settled (not working, starting, blocked or waiting for the user, with no queued input or background work; a failed turn counts too), every open item whose `agent` entry links that session records `agent_stopped` with `via=agent`, the session link, and its last words or failure (one line, at most 300 characters). The persisted marker is the node's activity time, kept separately from the display timestamp and never listed, so another callback or a manager restart does not record the same stop twice; genuinely working again advances it and permits the next stop. The latest linked Agent's stop returns its handling chat from `agent` to `looking` and wakes it with the result to offer the next suggestion. A follow outcome submitted while that Agent is still current still writes the follow and `waiting` chain entry but keeps handling in `agent`; its stop then returns handling to `looking`, leaving no stopped Agent to restore. After a stop is saved, its notice is scheduled for delivery whether it is waking or informational. An older Agent's stop is still recorded and delivered as a non-waking notice; it does not move a newer current Agent out of `agent`, change its `Agent.Back` restoration state, or reset the current Agent's turn clock. An item without one only records the chain, and the AI twin being off records without waking. Task supervision notices are independent and may arrive separately; they never replace or suppress this Inbox wake. The sweep uses the same recording path to repair a missed report. Only a session known to be unsettled waits past four timeouts; an Agent link evicted by the chain cap, a session the node definitively reports missing, or an unreachable node uses that bounded failure fallback instead of hanging forever.

Handing an item to a conversation or an Agent records `handling` or `agent` with the session's link (an app that sent the text to a new conversation or an Agent itself records it with `POST /api/v1/inbox/{id}/handoff` and `{session: {machine_id, session_id}, kind: "agent" | "conversation", title, client_id}` without `text`: nothing is sent, and a hub from before answers 400 because `text` is missing, so nothing goes to the main conversation by mistake); a reply the user made in the source is `self_replied` with the source's `via`. Consecutive `received` entries merge as they are written (`count` adds up, `at` is the newest, `messages` keeps the newest ten), so a busy group does not push older steps out of the 50 kept. An Agent step shows its session's live state, in the words of the Agents list, only on the newest step linking that session. The apps show the three newest steps folded and every step expanded, with icons by `via` (`spec/fixtures/inbox/timeline.json`); the row's third line and the detail's small line read `where · sender: preview`, with `where` said once when it is the sender (a direct chat) (`spec/fixtures/inbox/row.json`). `spec/fixtures/inbox/showcase.json` is the scenario every client draws for side-by-side screenshots.

**Read and replied state from a grouping connector.** The hub advertises `inbox_group_state` (`extension.HubInboxGroupState`) in `hub.capabilities`; without it, a connector behaves as before. The same advertisement permits its fallback plan to assign messages to an already handled event, which the manager accepts and reopens. When the capability is present, a grouping connector may hand, in its collect answer, one thing per event the user read or replied to in the source: `{thread, kind, id: <the event's item id>, group: {batch: "", new: []}, read, done: "" | "replied", at, last}`, with no messages and optionally `rules`. The manager ignores the report's `rules` and all its other content, leaving the item's title, summary, reason, tier, count, meta, rules, preview and sender unchanged. A report is accepted only for an existing, open grouped event of that source connector, in the same conversation or one nested under it, whose current `last` equals the report's; a stale position, a different source or conversation, a handled event and an unknown id are ignored. `done: "replied"` takes precedence when `read` is also true. A replied report whose `last` equals the item's recorded `Agent.SentOf` is Persona's own send, not a new source reply, even when a due check or another wake has temporarily moved the item out of waiting: it records no `self_replied` step, requests no wake, and leaves the current handling and follow-up state, including an active due check, undisturbed. A read at that position only sets `Read`; a read at a position already recorded as `self_replied` likewise only sets `Read`, without another note or wake.

With a processing conversation, a replied report records `self_replied` with `via` set to the item's source and a source link whose `position` is `last`, does not archive the item, and puts it in `waiting` with a fresh default 24-hour `follow.check_at`. It preserves the existing `who` and `next`, resets `checks` to zero, clears `due`, and removes the reply suggestion superseded by the user's reply together with its old-App draft mirror. A matching `self_replied` position makes repeated reports idempotent; the item also keeps that position (`replied_of`, kept, not listed), so they stay idempotent after later steps at the same `last` push the `self_replied` out of the 50 kept (#8424). A row stored before `replied_of` has only the entry. With the twin on, the wake queues a `self_replied` note with `at` rendered in the user's timezone, sets handling to `looking`, and records `Agent.Back = waiting`. A recap-only response, `no_draft`, or a timeout returns the item to `waiting`, filling a missing `check_at` with the default 24 hours rather than marking it `failed`; a `conclusion` with `outcome: done` archives it. The conversation can otherwise submit a suggestion or write a new follow as usual.

For an item already in `handling.state = agent`, the same reply record, `checks` / `due` reset and superseded-suggestion cleanup apply, and the twin-on wake still runs, but its handling state remains `agent` rather than changing to `waiting` or `looking`.

Read-only on an item already `waiting` or `agent` only sets `Read`: it adds no chain entry, changes no handling state, and queues no note or wake. Otherwise, with a processing conversation, read-only sets `Read`, adds no chain entry, returns handling to `looking`, records the interrupted state in `Agent.Back`, and queues a waking `self_read` note; an already-read item at the same `last` is skipped. Without a processing conversation, a reply records the same `self_replied` step and archives the item with `done_by: "replied"`; read-only sets `Read` and archives FYI or folded items with `done_by: "read"`, while Needs you stays open. For an item with a processing conversation, turning the AI twin off does not undo these reply transitions: a replied report still leaves the item `waiting` (or `agent` when it was already working with an Agent), with the same follow changes and suggestion cleanup, but queues no note, requests no wake and does not force handling to `looking`. Read-only reports only set `Read` in this case. The usual disabled-twin release on the next handling pass is unchanged, so an open `agent` state can then be released as described below. Ordinary non-empty batches still go through the existing association path. State changes join the collection round's normal changed-item save and notification path.

## AI twin: handling chats and suggestions (from phase 1, 2026-11-05)

The AI twin becomes a **handling chat**. With `inbox.agent.enabled` on, every Needs you item goes to the manager (no longer filtered by model 2), **one chat per item**,
bound to that item (`handling` on the item; chat source `inbox`, `origin_ref` = item ID). It runs in the manager background. Code: `assistant/inbox_agent.go` (promotion, chat creation, reconciliation),
`inbox_handle.go` (chat notifications), `inbox_suggest.go` (suggestion tool), `inbox_suggestion_api.go` (confirmation API). Actions visible to others **produce suggestions only**; code executes them verbatim after your confirmation. With the switch off, the manager stops automatic promotion, new handling chats, and chat notifications; chain recording continues.

- **Turning the twin off releases unfinished work**: the next handling pass (including collection after a restart) returns open `looking` / `agent` items, queued or running, to Needs you with `handling.state = failed` and the localized reason "The AI twin is off". Queued messages and start retries are discarded, so later messages can be triaged normally. The release is saved and announced through the normal item-change path; a failed save is retried on the next pass.
- **Re-enabling resumes only work interrupted by the off transition**: that release persists an internal `agent.interrupted_by_disable` marker, which is not exposed on listed items. The next enabled handling pass lets the marked item through the existing escalation, delivery and start paths once, then clears the marker; repeated passes and a manager reload do not duplicate the turn. Nothing runs while the switch remains off. A genuine processing failure clears or leaves the marker false and stays failed rather than being retried automatically.
- Existing completed suggestions remain available. Rejecting, adding instructions, or confirming a handback while the twin is off returns the item to Needs you immediately with the same reason, without waking or starting its handling chat. Confirmed replies already `waiting` on the other side retain that state. Explicitly handed-off conversations keep their separate workflow. Turning off the twin does not terminate processes already running on a computer.

| Step | Who | Purpose |
| --- | --- | --- |
| 1 Triage | `inbox.triage` (small model) | Triage above, also returning `urgent` |
| 2 Promote | Code | Triaged Needs you items with triaged content and latest message within `max_age_hours` (default 24) queue a handling chat; `handling.state = looking` (Working; urgent stays Needs you). The task comes from the triage summary and reason |
| 2′ Link task | `inbox.judge` (small model, only when tasks are active) | Use only its `task_id`, and only with `evidence` quoted from that task: which task the item concerns, among the tasks the item's repository or thread allows (#6424; [unified-entry.md](unified-entry.md), "Message cards link to tasks"). Before it, the deterministic pass links a GitHub item to the one open task naming its number in its repository: a saved repository issue/PR association (including Agent closeout and work-artifact PRs), a legacy link, `owner/repo#N`, a bare `#N` in a task of that repository, a PR the task's Agents named earlier, or the PR that says it closes the issue (#6507, #9725). Its `handle` no longer decides whether to process the item |
| 3 Handle | `inbox.agent` (a launcher: Agents' unless set apart, see below) | Headless claude in the assistant directory; investigate, then submit a suggestion and recap through MCP `inbox_suggest` |

- **The twin's model (#4232)**: by default the handling chat runs on **Agents** (`agent.new`): its provider / launcher, model and effort, including later changes there. Because the twin is a high-volume, short-lived job, it can be set apart under **Settings › Model configuration › AI twin runs** to run on a cheaper or faster launcher, model or effort without changing new Agents; **Follow Agents** clears that again. Provider, model and effort changes apply only to new handling chats. Permission changes apply to new chats and stopped chats when they resume; running chats keep their mode. The setting is stored as `model_config.overrides.v1.inbox.agent`; the retired `model_config.inbox.agent` record and the old `inbox.agent.launcher` are never read. It does not touch the twin's switch, its rules or which computer it runs on (`inbox.agent.machine`), and the model does not decide what the twin may do (`docs/assistant/model-config.md`, "AI twin override"). Permission is the one inheritance exception: an unset AI-twin `permission_mode` means `default`, never Agents' `bypassPermissions`. The existing selector in this row makes an explicit AI-twin mode visible and editable, and clearing it restores `default`.

- **Lark screenshots**: A handling chat can call the read-only `lark_image` with a message ID or the item's `chat_id` and its Lark account. It can also read a topic by `thread`. A chat or topic page returns up to ten messages with IDs. Only their newest three image parts are attempted, each at most 1 MiB and 2000 pixels a side. If a picture fails, the chat still receives the message text. Reading leaves Lark's read state alone. See [Reading message images in a conversation](../extensions/feishu.md#reading-message-images-in-a-conversation).
- **Where the item comes from** (`prompts/inbox/agent_ref.md`): a connector's item gives its link (GitHub's with its read tools), and where a reply goes when the row says (`reply_to`; iMessage's "reply in Messages" too). A row with no `reply_to` gets no reply line. A subscription's item also names the subscription and the connector and source it reads, so the twin reads more with that connector's tools. While the subscription's knowledge base is on, the twin gets the knowledge base's summary of the row's chat, as for Lark and mail chats (#6222). The round's `via`, `via_source` and `chats` carry this; the assistant keeps them in memory per source, refreshed every round.
- **Continue on new messages, do not restart**: new messages for an item with a handling chat (a response or additional detail), or your confirmation / rejection / instruction, are **sent into the same chat**
  (`sendToSession`, prompts `prompts/inbox/handling/*.md`: new messages, sent, Agent started, handed back, rejected, your instruction, item done). No new chat or fresh judgment.
  Messages merged into active items do not retriage or change tier ("Event splitting" above). Work requests (new messages, rejection, instruction, handback) start a turn (`looking`); informational notices (sent, Agent started) do not.
- **Handling chat missing**: if sending fails or its computer reports no such chat, record its absence. When work is needed, **recover by event ID** (another handling chat for the same item); create a new one only if none exists,
  including `recap`, handling chain, and intervening messages in its first message. **Manager restart**: items remain in the database, and existing handling chats reconnect normally. A chat still starting at restart (no recorded session ID) waits through the startup interval,
  then recovers by event ID before creating another, avoiding duplicate chats.
- **Concurrency, timeout, and 24-hour eligibility** keep existing settings. `inbox.agent.concurrency` (default 1) limits automatic admissions: existing `looking` chats count in `startInboxAgents`' running total, and queued new chats use the remaining slots. Scheduled follow-up checks, Agent-stopped wakes, and chat-connector replied/read wakes share this capacity. In one pass, conversations already `looking` plus admitted programmatic wakes cannot exceed the limit; every admitted wake counts immediately, including one that just woke its chat. Excess programmatic wakes remain pending for a later pass and are admitted in arrival order without losing or duplicating chain entries, handling state, or notification content. Wakes from a user's instruction, rejection, confirmation (including after handoff), or direct reply, and from new messages from the other party, bypass these slots and retain their existing behavior. A disabled AI twin still suppresses these programmatic wakes.
  Outside turns that restore an earlier `waiting` state described under "Following up", no suggestion after `timeout_minutes` (default 15), or a turn ending without one (after the first minute, chat no longer running), means failure: `handling.state = failed`, return to Needs you,
  no suggestion, and a `failed` chain entry. A waiting-backed turn ending with only a recap, timing out, or ending without a draft instead returns to `waiting`, filling a missing `check_at` with the default 24 hours, without counting a failure. Waiting on a sub-chat (`agent`) times out after four times that duration unless the sub-chat is known to be unsettled; an unreachable node has the same bounded timeout, not an indefinite wait.
- **Retry failed handling.** On the web, **Try again** asks the item's manager to continue. `POST /api/v1/inbox/{id}/retry` records the request and the previous failure reason before waking the processing conversation. If that conversation is gone or was never created, its replacement receives the recap and handling chain. A linked Agent that is still working stays linked and keeps running. When it settles, its current stop report goes to the processing conversation, even if the chain already recorded the stop. If the node reports the Agent missing, processing resumes from the saved context. A concurrent retry sees the new state and does not start a second turn.
- **Prompt and server policy** (`prompts/inbox/agent.md`, `assistant/inbox_policy.go`, `node/inbox_policy.go`): bound event ID, item content and source; on continuation, recap, chain, and intervening messages. Read-only investigation may run directly. Work that changes code or runs tests is submitted as an `agent` suggestion, not started directly.
  Direct sends (`lark_send` / `mail_send` / `agent_send`, every connector `Actions` tool), delegation (`agent_start`, `conversation_send`, task scheduling), and user interruptions through `ask_user` / `notify_user` are denied by the manager with an error directing the model to `inbox_suggest`. Connector `ReadTools`, the documented phone read-only catalog, read-only assistant investigation tools, `kb_update`, and `inbox_suggest` remain available; unknown tools fail closed.
  With no AI-twin-specific override, handling chats start in non-bypass `default` mode. An explicit `inbox.agent` permission override can select another supported mode; a caller cannot supply one, and clearing the override restores `default`. The manager writes this choice to the separate manager-only `inbox_permission_mode` field on starts, later sends and `assistant.model` answers, never trusting the caller-controlled `permission_mode` for this decision. A new node honors only that dedicated field: an older manager's missing field, Plan and unknown values stay in `default`, while an older node ignores the field and keeps its previous default clamp. A live process keeps its current mode; a stopped process reads the latest choice when it resumes. The manager passes `origin: inbox` and an explicit `origin_known: true` decision to the node before the first model call and again on every later `assistant.send`, `session.send` or `session.send_once` (including `deliverNotes` and the hub's send-once forwarding). Only that manager field attests an ordinary empty origin: a successful empty `assistant.model` response, an older manager's send, an older hub's answer, a missing origin-book entry before backfill and a manager origin-store read failure all leave a pre-upgrade record unknown. A successfully read current guest registration is the separate explicit-ordinary exception to a missing origin-book entry because guests do not share the assistant's origin book. The node records both fields in the existing `node_model` settings JSON, so `state.load` restores them before node-owned background-task recovery. For a pre-upgrade record without that decision, the node takes the manager's re-attestation from `assistant.model` before recovery or cold compaction; if an explicit decision is unavailable, it keeps lost tasks for the next connection and refuses the cold start rather than treating the record as ordinary and starting ordinary tools. The same boundary covers direct `assistant.send` and `session.send` continuations from an older manager, and model-error recovery or impossible-usage resend before they restart an old process. A known ordinary record retains the older-hub recovery and compaction fallback. The node repeats origin on MCP calls, including calls made by a native child Agent in the same process. Claude's explicit `mcp__persona` allowlist lets read-only investigation and `inbox_suggest` run without a permission prompt; Plan mode is rejected when saving the AI-twin setting and falls back to `default` for legacy records because it rejects MCP calls before consulting that allowlist. The Inbox-only built-in `Read` allow covers local files outside the working directory except the credential deny list below; `WebFetch`, `WebSearch`, `Bash`, `Write`, and `Edit` receive no bare allow and retain their normal permission flow. Inbox Claude processes omit the built-in cross-Agent `SendMessage` tool, which does not traverse the MCP origin guard, and never adopt a warm process started with the ordinary tool list; ordinary conversations keep `SendMessage` and warm-spare adoption. Process replacement compares the Inbox/ordinary tool-policy class, so changing between ordinary origin labels does not restart an identical process or reject its warm spare. Codex likewise accepts Persona's own MCP calls without asking. The manager/node origin policy, not the launcher's permission mode, is the outbound boundary. Owner-confirmed replies, Agents and to-dos execute through the separate internal `runTool` path, not the model-call guard.
  The `Read` deny list closes the data directory's known top-level credential names, suffixes and backups, its secret directories plus `managers/`, `run/`, and `extensions/run/`; the path guard's home credential directories and files (including `.ssh`, `.gnupg`, `.aws`, `.kube`, `.docker`, `.netrc`, `.claude/.credentials.json`, and `.codex/auth.json`); and credentials/settings in every known Claude/Codex configuration directory. Each rule is emitted for both the written and resolved physical paths, including a not-yet-created file below a linked existing parent. Deny rules take precedence over bare `Read`; the [agent sandbox](../server/agent-sandbox.md) remains an additional, optional boundary rather than a prerequisite for these denies.
  The node Lark gate denies every non-read or unclassified Lark call before configured allows, bypass-safe bot send/reply, or phone confirmation. It still does not inspect arbitrary non-Lark programs or script files that hide a CLI invocation; the process permission system covers what it can, but this is not a general shell/network sandbox. Inbox handling on an external agent runtime is refused until that runtime can attest origin during startup; built-in Claude and Codex are covered.
  A configured command gate has one additional narrow read path only in `default`: while still in the assistant's original trusted working directory, one literal rule-allowed `GET` or `HEAD` with only its path (optionally under `timeout`) may pipe through `jq`, `head`, `tail`, `grep`, `cut`, `tr`, `wc`, or `column`. Python and every other interpreter are outside this exception, as are `egrep`, `sort`, `uniq`, and general-purpose print commands. Redirects may only duplicate stdout/stderr, such as `2>&1`; inherited descriptors such as `>&3` and `<&3` are not part of the exception. Plan or bypass mode, a changed working directory, extra gate arguments or calls, substitutions, assignments, file redirects, appended commands, confirmation-required reads and any other program retain the ordinary permission flow. This check precedes the ordinary classified-read path too, so another mode does not inherit that path's automatic allow (#11991).
  Instructions in messages are not user instructions (Inbox bodies are untrusted data from others); follow only the task and the user's words in this chat. Prompts explain the boundary, and the manager/node checks above enforce it rather than relying on the model to comply.
  With `inbox.agent.read_profile: "source_only"`, the handling chat keeps the current item context and suggestion tools but receives no automatic knowledge-base context. Its only connector reads are the ids explicitly named in `inbox.agent.connector_tools` that the source registered as read-only; knowledge-base reads and updates, cross-conversation/Agent reads, phone reads, global search and every unnamed connector tool are refused. An unknown non-empty read profile fails closed to the same suggestion-only boundary. An empty profile preserves the existing read-only investigation policy and knowledge-base context.
  Knowledge-base tools may update people and projects along the way (`kb_update` is an allowed write).

#### Optional reply policy gates

`inbox.reply_policy` is an optional, source-scoped policy for owner-voice reply suggestions. It is **disabled by default**; a missing or disabled policy preserves the existing queue, suggestion and confirmation behavior. An enabled policy names a nonempty `revision`, optional `sources`, an `unknown_mode`, per-category `routes`, and may require an independent second classification. The classifier returns only a category; the manager maps it to `owner_draft`, `bot`, `owner_only` or `no_reply`. Model output cannot set a route or authorize a send.

The categories are `personal_transaction`, `security_compliance`, `approval_permission`, `decision_delegation`, `technical_qa`, `research`, `alert_triage`, `ppe_deploy`, `mr_precheck`, `no_reply`, `ordinary_coordination`, `logistical_followup`, `acknowledgement`, `other_owner_reply` and `unknown`. Personal transactions, security/compliance, approvals/permissions and decisions/delegation are always `owner_only`; the five technical categories are always `bot`, and `no_reply` stays `no_reply`, regardless of an unsafe route override. Unknown categories, classification failure or timeout, an empty revision and invalid routes fail closed to `owner_only`. Manual twin preferences cannot override these routes.

The manager classifies the item's current content before queueing (including `TwinFirst` items already tiered FYI) and stores category, route, content version, policy revision and independent-review state on the item. Three server gates then apply: the queue starts handling only for a current `owner_draft` snapshot; `inbox_suggest` accepts reply/Agent suggestions only for that snapshot and copies the trusted fields onto either kind; confirmation rechecks the item and suggestion before an Agent start or reply reservation and again before the outward action. A content or policy-revision change invalidates the old draft, including a pre-policy Agent suggestion. The last reply check occurs after the per-item send-mutex wait and reads fresh hub configuration rather than the five-second background cache; a failed configuration read or a revision change during edit review refuses the send as known-not-sent. An owner-edited final reply is independently reclassified together with the original conversation, so a short assent is evaluated with the request it answers; an edit into a restricted category is refused. These checks do not replace the owner's confirmation: an ordinary `owner_draft` still sends only through the existing suggestion action and send-once attempt path. The attempt retains the original proposal, actual confirmed text and policy snapshot for authorization, while those policy fields are excluded from physical send identity so a revision cannot duplicate an uncertain reply. No model call can send directly.

On-demand `POST /draft` and workbench draft/redraft use the same boundary. A missing, disabled or non-applicable policy keeps their transient result unchanged. For an applicable policy, the manager reads fresh configuration before and after the model call: `owner_only` is refused before generation; a still-current ordinary result is persisted as a normal reply suggestion with its old-App mirror; a policy change, content/suggestion/attempt state change, replaced mirror or failed persistence returns no sendable draft. A draft/redraft instruction is owner-introduced text: the generated result is independently classified with that instruction and the original conversation before publication, so an instruction cannot turn an ordinary item into an unrestricted approval. A result matching an unresolved send attempt stays with that attempt rather than becoming another old-client send. Policy-scoped old-client and workbench mirror sends reuse the normal suggestion confirmation, including its durable attempt, uncertain-result and recipient-binding fences. Rejection while queued cannot turn the request into a direct owner-authored reply; a workbench draft rejected before its send begins is stale too. A genuine direct owner reply, including an old-App card that began without a mirror, keeps its legacy path.

A merge carries every member's source and truncation state into the survivor's authorization scope, including sources and incomplete context from earlier merges, even when identical text needs no append. If appending or prepending merged context is clipped, the item is durably marked incomplete: reply classification writes `unknown` / `owner_only` without calling the model, and no revision can reauthorize it from the truncated text. A still-merged row already at the 6000-rune cap from before this marker existed is treated as incomplete too. Undo restores the member rows but does not remove text already copied into the survivor, so it first retains the source and truncation boundary (including boundaries found only on pre-upgrade rows) and invalidates the old classification rather than restoring a pre-merge draft.

- **Handling chats stay out of the default conversation lists** (#5723): the manager's session snapshot marks
  `origin: inbox` conversations as archived from the start, including existing chats after an upgrade. They keep
  their subject-based titles, transcripts, event IDs, and pending work; archiving is presentation, not completion.
  Open the processing transcript from the item's handling-chain link, or from archived conversation history.
  New messages, instructions and suggestions continue in the same chat without bringing it into Recent.
  The main conversation is exempt, and explicit Hand to AI keeps its normal user-conversation behavior.
  This uses the existing archive flag on every client, without a new setting, migration or separate Agent workflow.
  It sends no completion push (`notifyFinished` skips it); the result is the Inbox suggestion.

**Suggestion tool `inbox_suggest`** (`assistant/inbox_suggest.go`, `assistant/inbox_handoff_suggest.go`, description `prompts/mcp/inbox/inbox_suggest.md`; accepts the handling chat started for an item, and with `item` a
conversation the item was handed to ("Item actions", Hand to AI), rejecting other chats):
`{item?, processing_turn?, kind, text, note?, handback?, outcome?, recap?, title?, summary?, machine?, session?, cwd?, related?, changes?, follow?, reported?}`.

| kind | Meaning | After confirmation |
| --- | --- | --- |
| `reply` | A reply (`text` sent verbatim, no 【…】 blanks for users to fill; Docs may include `changes`) | Send through `/reply`'s send-once path (`send_id`), **without marking the item done**. `handling.state = waiting` (Working, awaiting response) with the default 24-hour follow until the chat supplies one; chain records your confirmation and the send, then `waiting` with `via=user`, and tells the chat the user confirmed and it was sent |
| `agent` | Assign an Agent (`text` is its instruction; optional `machine` / `cwd` default to assistant computer / home directory) | Code starts an Agent with that instruction (like the chat calling `agent_start`: `agent` chain entry links to it; completion reports back), `handling.state = agent` |
| `handoff` | Hand the item to an existing eligible conversation on this manager (`machine` + `session` from the main conversation, an active leader conversation, or the linked open task's conversation, with `text` as the note for that conversation; processing chat only; #13562) | Re-validates the target and delivers the item + note through the existing Hand to AI path (`session.send_once`, `prompts/inbox/handoff.md`), records the conversation in `handoffs` and the chain (linked to that session), leaves the item open (`handling.state = done`), and allows that conversation to write back with `inbox_suggest` `item`. Refuses archived, deleted, unknown, ineligible, or other-manager targets with a clear error back to the model (and returns `409 handoff_target_unavailable` if the target became unavailable before confirmation). Processing chats still cannot delegate directly (`agent_start`, `conversation_send`, scheduling remain refused). `now` is `confirm_handoff`, and the listed suggestion includes `machine_id`, `session_id` and `target_title`. The web Inbox shows the proposed conversation in the list and detail before the user confirms |
| `conclusion` | Conclusion: `outcome: fyi` (move to FYI with the conclusion; OK marks done) or `done` (review found no action needed; immediately done, `done_by=manager`) | OK marks done (`done_by=user`) |

- The manager includes a new `processing_turn` in a processing conversation's first prompt and each subsequent Inbox delivery while the item is open. The call echoes its prompt's value. The manager checks it under the Inbox lock before changing the item, so a delayed write from a superseded prompt cannot replace a suggestion or change follow-up or handling. Existing processing chats without a value can omit it until their next Inbox delivery. Handed-off conversations still write back using `item`.
- `handback: true`: a suggestion requiring your judgment ("Ask a colleague before replying to the client"). Confirmation executes nothing; hand it back to the handling chat as instructions to act, with the normal chain recording.
- `follow` is valid only without `kind`; the two are mutually exclusive. It contains `who`, `next`, `check_at` and `check_in`: `check_at` is an RFC 3339 timestamp with a time zone and takes precedence over `check_in`; `check_in` accepts durations such as `30m`, `4h` or `2d`. With neither time field, it defaults to `24h`; the resulting time is clamped to between 30 minutes and 14 days from now. `who` and `next` are single lines clipped to 80 characters. `recap`, `title` and `summary` may be written together with `follow`, and an existing suggestion stays in place.
- `recap` without `kind` updates the recap (for example after a send), up to 600 characters. `related` contains IDs of related Inbox items from other chats / sources, recorded on both sides.
- A recap-only call with neither `kind` nor `follow` creates no suggestion. When the matter was already waiting on the other side or its Agent is still working, ending the turn with only that update returns `handling.state` to `waiting` or `agent`, respectively.
- `title` names the matter (one line, clipped to 40 characters) and `summary` says where it stands and who is waited on for what (one line, clipped to 80); either may accompany a suggestion or a recap-only call. A non-blank accepted value sets `title_by=assistant`; blank values leave the item unchanged. Only chat events (a grouped event or a built-in chat) accept `title`, and only when they have no mail, document or company-feed block; a grouped event may still carry its connector block. Connector threads (including PRs / issues and iMessage), mail, documents and company-feed posts keep their own title: the tool ignores `title`, says why, and still writes `summary`.
- A semantically new submission increments `suggestion.version` and invalidates the old suggestion; an equivalent waiting reply reuses it ("Reply-suggestion recovery" above). Producing a suggestion sets `handling.state = done`. Chain records "Manager suggests a reply / an Agent / advice awaiting your decision" (`handling`, linked to the handling chat); conclusions record `concluded`.
- A mail reply suggestion also keeps its exact reply target internally (not in the listed suggestion). Opening mail marks it seen, and a later collection can consequently reduce a thread to an older unread UID; confirmation checks the bound target under the collection lock and returns `409 mail_reply_target_changed` instead of answering that different mail (#5471). A pre-upgrade mail suggestion has no bound target and is refused the same way rather than guessed; non-mail suggestions keep their legacy confirmation path. An FYI conclusion likewise remains open when its mail is read elsewhere: the item becomes `read`, but only **Got it** files it.
- Old `inbox_draft` (reply only) remains supported, equivalent to `kind: reply`, for chats started before the tool list changed.
- `item` (an item id) is needed from a conversation the item was handed to (`assistant/inbox_handoff.go`); from the item's own handling chat it changes nothing. A tool error refuses an unknown item,
  one not handed to this conversation (only a conversation in its `handoffs` may write back) and one the user already filed ("answer in the chat instead"; a filed item is not reopened);
  nothing changes then. The item's own handling chat is likewise refused when it submits a kind or `follow` after the item was filed or has otherwise ended: neither changes anything, while a call with only `recap`, `title` or `summary` still updates those fields.
  Without `item` the tool works as before. A suggestion from a hand-off records where it came from (`suggestion.conversation`), its chain entry links that
  conversation, and the item's `agent` / `handling` stay as they were (no message card, no old-App draft).
- **Task-related items**: for linked tasks (2′), prompts include task progress; a reply suggestion inserts a message card into the chat that started the task (`message`, retaining sent state after sending).
  A Lark CLI `bot_handoff` is the owner's request for the main conversation, not somebody else's task update: neither task-link router nor the task judge associates it with a task (#8351).
  A stored stale association cannot raise a supervisor signal or follow a task's continuation. The link recheck clears only that association, preserving handling, suggestions and handoff history without resending the request.
  When the linked task is supervised and the assistant's `inbox_signals` is on (off by default, #4293), each pass that takes new messages from other people into the item also tells the task's supervisor:
  the signal `inbox` in its next supervision message, naming the item (id, title, sender, one line) and how many messages came ([unified-entry.md](unified-entry.md), "Supervision (#3124)").
  Persona's own confirmed reply and bot chats never raise one, and nothing the handling chat does goes into the Inbox.
  A link follows the work (#6423): when the linked task is closed (done or cancelled) and exactly one open task carries it on through `continues`
  (down a line of continuations, at most 8 deep), the next pass moves the item's `agent.todo_id` there and saves it, whatever `inbox_signals` says; the move
  itself raises no signal. A message is counted as told only once a signal took it, so one that came while the linked task could not hear it reaches
  the task that carries it on. A link to a closed task that nobody carries on (or that two tasks carry on) counts as none: the next escalation asks the judge again,
  even when the item already has its handling chat.

**Confirmation API** `POST /api/v1/inbox/{id}/suggestion`: `{version, action, text?, client_id?}`, `action` = `confirm` / `reject` / `instruct`.

- `confirm`: execute exactly what is shown; before the first reply attempt, supplied `text` replaces suggestion text (edit then confirm; chain `edited: true`, chat learns the changed text). A retry uses the persisted first-attempt payload; conflicting edits cannot send under that identity.
- `reject`: record rejection, clear the suggestion, and return to the handling chat to reconsider. `instruct` requires `text` (such as "Do not reply this way; ask about timing first"; absent text returns 400), records your words, clears the suggestion, and asks the chat for a new one.
  Without a current suggestion, `instruct` with `version: 0` records your words and wakes or queues the handling chat; `confirm`, `reject`, and nonzero versions still return `suggestion_stale`.
- Responses: `200` item; `409 {"code":"suggestion_stale","error":"建议已更新","item":{…}}` ("Suggestion updated": version is no longer current or no suggestion exists; App shows the notice and redraws from the returned item,
  including the second tap on the same suggestion); `409 inbox_item_done`; `409 mail_reply_target_changed` (the message the reply suggestion answered is no longer the item's target; nothing was sent); `409 handoff_target_unavailable` (a `handoff` suggestion's target was archived, deleted or became ineligible before confirmation; nothing was sent); `400`; `403`: reply-only devices (Watch) may confirm only replies / conclusions, as text (no Agent creation, handoff, handback, rejection, instructions, document-status changes, or `/agent …` sends;
  hub allows the route for Watch, while the manager checks the actual suggestion); `502 assistant_in_doubt` (as `/reply`: it may already have sent; another confirmation cannot bypass the attempt outcome); a send that did not go out answers its reason ("Send failures": `409 inbox_cannot_reply`, `429 inbox_send_rate_limited`, `502`/`503 inbox_*`, `delivered: "none"`); other 502 errors.
- **Direct reply**: `POST /api/v1/inbox/{id}/reply` without a card keeps an item with a processing conversation open in `waiting` with the default 24-hour follow, records `sent` then `waiting` (`via=user`), clears any remaining suggestion and its old-App draft mirror, and queues `NoteSent` without waking that conversation. Without a processing conversation it still files as replied; new messages arriving while the send is out keep the existing kept-open behavior. This mode does not change old-App card sends or replies confirmed in a handed-off conversation.
- **Execution failure**: a confirmed reply retains its versioned attempt and the safe retry/unknown state above; it stays Needs you. Agent-start and other non-reply failures keep `handling.state = failed` without a suggestion and the existing `failed` chain entry.
- **Persona's own sends are not your replies**: after Persona sends a reply, the next Lark-plugin round reports the chat's latest message as yours (`done: replied`). While the item awaits the other person's response and no new messages arrived (`last` unchanged),
  treat it as Persona's send: do not archive or record You replied in Lark. If the other person replies and you later reply in Lark yourself, mark done as usual (`done_by=replied`), record your reply,
  and tell its active handling chat to stop. Phase 1 ambiguity: if you add another Lark message after sending but before the other person replies, it is also treated as Persona's send.
- **Marked done / dismissed / Send in an old App**: marked done and dismissed clear the suggestion and tell an active or suggestion-waiting handling chat the item is done and to stop. A genuinely legacy old-App card send still uses `/reply` and marks done. A policy-scoped processing or on-demand mirror instead follows suggestion settlement: with an active handling conversation it stays open waiting for the other side rather than being filed by the old-App send.

**Old-App compatibility**: reply suggestions also write `agent.draft` / `agent.note` (`agent.state = drafted`). Old Apps still show an AI twin draft, whose Send uses `/reply`; its filing behavior is the legacy-versus-scoped-mirror distinction above, not an unconditional done mark for scoped suggestions.
Processing uses `agent.state = running`, failure `failed`; other suggestions (Agent, conclusion, handback) show no draft on old Apps (`skipped`). Previously stored AI twin drafts become version-1 reply suggestions (`handling.state = done`),
confirmable in new Apps. With reply policy absent or disabled, `/reply`, `/done`, `/draft` are unchanged; the policy-scoped `/draft` behavior is described above.

- Local model 1 verification: `PERSONA_LIVE_AI='{"base_url":"…","api_key_file":"…","model":"…"}'` runs `TestInboxModelsLive`
  in the manager package: `go test -run InboxModelsLive ./internal/assistant`, on the hub machine, with the key file there.

## Relationships: parents and children (#2128)

A parent is another Inbox item (`inbox`), a to-do (`todo`), a coding Agent's session (`session`, ID `<machine>/<session>`), or a
personal Agent's session (`agent`, ID `<agent source>:<sessionKey>`; the id `GET /api/v1/agents/sessions` lists). The two session
kinds stay distinct: `session` opens a conversation on a computer, while `agent` opens an entry on the Agents page. The web Inbox
Trail opens `agent` at `/a/<id>`; the native apps keep their existing pages and may follow with their own Agents routing.
Persona stores, lists, and navigates only; it never guesses parents.
To-dos do not store a separate tree. Code: `assistant/inbox_parent.go`.

- **Origin** (`by`): a new event continuing an earlier one from grouping (`continues`), connector `InboxThing.parent` (`connector`), or user-set (`user`).
  Automatic values never overwrite user values; connector values may overwrite `continues`, not vice versa.
- **Set**: `PUT /api/v1/inbox/{id}/parent {kind, id}`; `null` or `{}` removes it (no DELETE, avoiding collision with `DELETE /api/v1/inbox/mutes/{id}`).
  Item parents must exist (404 otherwise); no cycles, at most 8 ancestor levels (409 otherwise). IDs up to 256 characters, no control characters (400). Return updated item.
- **Read a thread**: `GET /api/v1/inbox/tree/{kind}/{id...}` returns `{node, item?, ancestors, children}`. `ancestors` nearest first (last may be a to-do / session / personal Agent without an item,
  or an item no longer in Inbox, with no `item`); `children` are direct child items, oldest start time first. `kind` may be `todo` / `session` / `agent` to list items growing from it.
- Extension tokens require `inbox.read` for trees, `inbox.write` for parent changes (`extwire/inbox_scopes.go`); Watch cannot call either API.
- Client rendering: "Relationships" under "App" below (web `web/src/views/inbox/InboxLine.tsx` offers **Not the same matter** and **Same matter as…**, the latter also in the detail's More menu; they use the existing `PUT /api/v1/inbox/{id}/parent` with `null` and `{kind, id}` respectively).
- Tests: `inbox_parent_test.go` (`continues`, PUT / removal, depth, precedence, trees).

## Lifecycle

- Hub background collection (`server/internal/extmgr/inbox.go`, delivering data to the manager for item composition): collect Lark chats 3 seconds after real-time Lark events; this round reads chats only. Every 2 minutes, also read document notifications,
  Moments, and other connectors (their events / setting changes also trigger this full round); mail every 3 minutes. Previously every Lark event read all of them (#674). Pull-to-refresh
  (`POST /api/v1/inbox/refresh`) collects everything immediately (a failed collect answers `502 inbox_refresh_failed` instead of the list, see "Ownership" above). Changes write `inbox_items` and push `{"type":"inbox"}`, prompting App refetch; new plugin-connector items store / push only after triage ("AI triage").
- **Done** is recorded only in Persona (`done_at`, `done_by=user`; ignored items use `dismissed`, see "Clean up all"; one replaced by the new thread rule uses `superseded`). It does not change Lark or mail read state. Done items remain listed for 3 days, reopenable at the bottom,
  at most the newest 200 of them by `done_at` (`inboxListDone`; after thousands are marked done at once the list stayed small, and the rest are on Done's history),
  and the manager holds them in memory for 8 days (meanwhile preventing old still-unread Lark messages from resurfacing); it loads only open items and those done within 8 days.
- **History** (#2200): done rows stay in `inbox_items` for 90 days (`inboxKeepHistory`); the manager deletes older ones at load and from the collection rounds at most hourly.
  `GET /api/v1/inbox/done` pages them newest done first (`inbox_history.go`, `docs/protocol/protocol.md`), keyset on `(done_at, id)`: a bulk action files many items at one instant,
  so a `done_at`-only cursor would repeat or skip them. The index `inbox_items_open` (`done_at, id` since manager migration `0471`) serves the pages in their order. A manager without a database pages the done items it holds.
  Each manager pages its own: the hub stamps the items with `manager` but never merges pages (a cursor is one manager's); the apps ask each manager and merge.
  A history item memory no longer holds still opens (`GET /api/v1/inbox/{id}` reads its row, read-only) and can be reopened (`/{id}/done` with `done:false`, `/bulk` `reopen`,
  `/done` with `done:false` load its row into memory first); filing it again finds no item (404 / `missing`), as before. Without a database there is nothing to fall back to.
  Deleting a source's data (`inbox.forget`) deletes its history rows too.
- **Days** (#2967): `GET /api/v1/inbox/done/days?tz=` counts the history per calendar day in the zone the app names (Postgres's `AT TIME ZONE` on `done_at`, so a day follows the zone's offset on its date; a manager without a database counts what it holds). With `sources=all` it reads only `done_at` from `inbox_items_open`. The apps' Done lists every day's count from it and jumps to a day with `GET /api/v1/inbox/done?before=<the day's end>` and no `before_id`, which lists the items done before that moment.
- **Storage** (#2779, manager migration `0471`): `inbox_items.data` is `json` compressed with lz4, the bytes the manager wrote; every reader unmarshals them in Go,
  so the start read no longer turns jsonb back into text. `source` is a generated column of `data->>'source'` with its own index, which `DeleteInboxSource` reads
  instead of every document. `inbox_items_open` is `(done_at, id)`, so an upsert of an open item changes no indexed column and can be a HOT update.
  Writes still need valid JSON without `\u0000` in a string, as with jsonb: computing `source` refuses them (`docs/lessons/server.md`, "Inbox rows as json").
  A server built without lz4, or a Postgres before 14, gets the same rewrite with the default compression. The migration also adds `jsonb_typeof(json)`, so the
  done page of a build from before it still runs if a hub goes back to that build (`docs/operations/hub-restart.md`); it can be dropped once no such build can run.
  The start read (`InboxItems`) has no `ORDER BY` (the manager keys the items by id) and hands each row to the manager straight from the driver's buffer,
  where it is decoded; the rows are never collected as bytes first. A read that fails midway takes nothing in, as before.
- Read elsewhere (Lark / another mail client; for mail also gone from its folder or past the window, on the page or off it, #5993): mark `read`; non-Needs you items become done (`done_by=read`). Needs you remains open, but unread count becomes zero and me / everyone mention flags clear (no longer unread mentions). Rule-tiered items are retiered using current rules after reading; no longer Needs you means done.
  Already-read open items with remaining unread count or stale rule tiers (read before a rule change) are reconciled next round (#798).
- A reply by me in Lark (latest chat message is mine, newer than the item) handles it (`done_by=replied`).
- Lark collection reads messages without holding a lock. If an item is marked done during the read (manually or in bulk), newly read messages create another item rather than merge into the done one (previously swallowed and invisible, #1105).
- What the owner handled elsewhere is filed too: see "Handled elsewhere" below.

## Handled elsewhere (2026-11-15, #7986)

An open item the owner already dealt with in the source, or that was closed there, leaves the Inbox by itself. Every filing
writes a chain entry saying why, can be reopened as usual (`done:false`, bulk `reopen`), and records its evidence on the
item (`settled_of`, kept, not listed) so a reopened item is not filed again on the same messages, only on newer ones.
Code: `assistant/inbox_settle.go`.

- **Grouping says so** (`group.md`): for an existing open event, the model may answer `"settled": "replied"` (the owner's own
  lines in the batch answered it) or `"over"` (the other side said it's done, no longer needed, or someone else answered). It
  is taken only for a tier other than action, never for a new event (one the owner hasn't seen is not filed unseen; its tier
  says what it is), and not for an event its conversation handles or a suggestion waits on; noise folding keeps such an event
  apart. A value that isn't one of the two (`false`, `null`) is ignored, not a reason to re-ask the batch: `replied` files it
  `done_by=replied` with a `self_replied` entry, `over` files it `done_by=manager` with a `done` entry ("Looks settled in the
  conversation") whose `detail` is the model's reason. Any other value is ignored.
- **A pass over the conversation**: the owner's own messages never start a batch (connectors hand in other people's messages), so
  after a round a background pass (one at a time, at most one a minute) looks at up to 8 open Needs you events of a grouping
  connector, each at most every 20 minutes, the longest unasked first. It reads 5 messages either side of the event's newest
  message from someone else (`InboxLark.reply_to`) through the connector's `messages_around` group tool (whose `mine` marks the
  owner's messages; the hub cuts an answer past 8 KB, so the pass reads what is whole). Only messages after the event count, and
  after a hand reopen only those newer than what it was filed on; a recalled message of the owner's counts for nothing.
  - The owner's message quoting or in the thread of one of the event's messages (the ids its context names), or in a direct chat
    any message of the owner's with no other open event of the chat in between (it may answer that one), is a reply:
    `done_by=replied`, a `self_replied` entry linking that position, without a model.
  - Anything else after the event goes to the triage model with `settled.md` (the later lines carry what they reply to and their
    topic): `replied`, `over` or `open` (left alone, the default when unsure). `replied` without a message of the owner's after
    the event is taken as `over`, so the chain never says the owner replied when they didn't. An `open` is not asked again until
    something newer comes; a failed call is.
  - Nothing after the event: nothing to do. In a busy chat the owner's reply may lie beyond the five messages read; such an event
    waits for its next move, or for the owner.
  A connector's repeated `replied` report for an event the manager filed and the owner reopened doesn't file it again either
  (`groupStateHasSelfReply` reads `settled_of`).
- **Mail answered from another client**: the mail page carries IMAP `\Answered` (`InboxMailSummary.answered`); an open mail
  item with an answered mail on the page is `done_by=replied` with a `self_replied` entry, whatever its tier, unless a
  conversation handles it, a suggestion waits, or an unread mail of the thread came after the answered one.
- **Its to-do finished**: an open item linked to a to-do (`agent.todo_id`, `inbox_task_links.go`) whose to-do is done (not
  cancelled), after a closed task's continuation was followed, is filed `done_by=manager` with a `done` entry ("Its task is
  done") linking the to-do. An item with the AI twin's processing conversation is left to it (its judge links the item to the
  task carrying the work on). Reopened, it is not filed again for the same to-do (`settled_of` is `todo:<id>`; an answered mail's
  is `mail:<uid>`). A to-do continued by a task planned after the item was filed does not reopen it.
- Not covered: emoji reactions. The Lark plugin's group tool doesn't say who reacted; reading them needs the plugin.

## Mark All as Done (2026-11-04, #1105)

The user's "Mark all read." Inbox read means done, recorded only in Persona, **not written back** to Lark, mail, or GitHub notifications, just like single items ("Lifecycle" above). Bulk handling therefore has no source sync / retry.

- **Entry point**: the original Mark All as Done entry has been combined with Dismiss All into Clean up all (#5114, next section). This section retains only request / Undo rules. Scope = **unhandled** items in the current filtered / searched list.
  Waiting Agents are not Inbox items (they await answers, not archiving), so excluded. Disabled with Agent filter or no unhandled items.
- **One request**: `POST /api/v1/inbox/done` (`docs/protocol/protocol.md`), with each listed item's `version`. Hub checks versions and marks within one lock. Items with new messages merged after listing remain unhandled,
  with toast "N items have new messages and remain in the Inbox," then App refetch. Already-done items do not change `done_at` (idempotent); one database write and one Inbox push for the batch.
  `version` is a short hash of `Last` (latest message position / uid / notification), unchanged by triage, reading elsewhere, or marking done.
- **Undo**: update locally first (badges and sidebar counts immediately), then show a toast ("Cleaned up N items" for Clean up all) with Undo after hub responds. Within 5 seconds, Undo reopens `changed` items through the same endpoint.
  Undo rather than prior confirmation matches existing Mark All Seen / swipe-to-done behavior and is smoother. Failure restores everything and shows an error.
- FYI · Mark All Seen, Collapsed · Clear All, and Mac multi-select E use this endpoint too (previously per-item requests), with Undo.
- The App icon has no badge (only tabs / sidebar counts), so no separate update.
- iOS new-shell toasts use a non-touch-intercepting window (`ToastWindow`). Button toasts intercept only near the button, leaving other touches to content below.
- Tests: `inbox_bulk_test.go` (scope, changed-version skip, idempotence, Undo, new item when marked done during message read, concurrency), `InboxBulkTests`, UI `InboxMarkAllTests`
  (mock `/mock/inbox_bulk` records every request), web `inbox.test.ts`.

## Clean up all (2026-11-05, #2609; unified 2026-11-06, #5114)

Extends Mark All as Done above with hub-side filter handling and confirmation for large selections. Each client previously had Mark All as Done and Dismiss All,
which users saw as one action: clear this batch they do not need to handle now. #5114 combines them into Clean up all (English "Clean up all", Japanese "すべて片付ける").

- **Record as dismissed**: Clean up all sends `action=dismiss` (`done_by=dismissed`), since the user did not actually handle these items. The only visible difference is the relationship-chain line
  saying Dismissed rather than Done. No statistics, triage, or other logic distinguishes `user` from `dismissed`, so using dismissed is the least disruptive choice.
- **Compatibility**: hub `/bulk` continues accepting `done` and `dismiss` from shipped iOS / Mac / Android clients, preserving their respective semantics.
  New clients send only `dismiss` for Clean up all. Section Mark All Seen / Clear All remain `done`.

- **Dismiss** means "I do not need to handle this." Like done, Persona-only (`done_at`, `done_by=dismissed`), no source writes, not mute (later messages in the same chat still arrive; use Mute to stop them).
  Side effects match single-item done: one batch write and Inbox push (lists, tab badges, sidebar counts update), queued AI twins do not start (only unhandled items qualify),
  existing drafts remain on the item and survive Undo. Single dismiss: `POST /api/v1/inbox/{id}/done {done:true, dismiss:true}`.
- **API**: `POST /api/v1/inbox/bulk {action: done|dismiss|reopen, items?|ids?, filter?, as_of?}` (`docs/protocol/protocol.md`).
  Clients still pass each listed `id` / `version` (correct for client-only filters such as search / Agent); without IDs, supply `filter` (source / account / section / kind) and `as_of`.
- **Cutoff**: `GET /api/v1/inbox` returns `as_of` (list-read time). Each item tracks when its current content reached hub (`arrived_at`, first listing / persistence;
  reset when `Last` changes; old data backfills `updated_at`). Filter handling leaves items with `arrived_at` after `as_of` unchanged (new arrivals / merged new messages), returning them in `skipped`, `reason=changed`.
  Do not compare message times: Lark / mail messages may predate list viewing but be collected afterward.
- **Undo**: response `ids` are changed items; `undo` is `{action:"reopen", ids}`. Send it unchanged to reopen only those items. Toast's 5-second Undo sends this.
- **Confirmation**: 10 or more items require confirmation ("Clean up N items (scope). New messages arriving later are unaffected; you can undo afterward."); fewer act directly with Undo. Scope / count are from the list at tap time.
- **Entry points (one per client)**: web has Clean up all to the filter row's right (hidden at narrow widths), and one Clean up all item in ⋯ with scope / count below. With nothing open under filters or a search the web names the scope without a count ("Lark · Mail", not "Nothing to do"), and before the list has loaded, or after a failed read, it shows no scope (the Inbox entry in `docs/web/web.md`).
  Android top-bar More has one item; iOS More has one item with scope / count below ("All · 12 items", "Lark · Mentions · 3 items", "Search ‘weekly report’ · 2 items").
  With nothing open iOS says "Nothing left to handle" only for the whole Inbox; under filters or a search it names them without a count ("Lark · Group chat"),
  and while nothing is shown because the list is still being read, or its read failed, the item has no line at all (#7551, as the web).
  Mac has one toolbar button (scope on hover) and one Inbox-menu item, ⇧⌘E while Inbox is active. Apple passes `items` (ID + version) for current filtered / searched unhandled items, not `filter`.
  Section Mark All Seen / Clear All (and the same from a group's title, "Group titles") still use `/done`. Watch has only single-item done, no bulk entry, and reply-only devices cannot call `/bulk`; extensions need `inbox.write`.
- **Old hubs**: without `/bulk` (404 / 405), fall back to `/done`. It lacks dismiss, so Clean up all records done (`done_by=user`), still removing items from the list. Reopen also falls back to `/done`.
- Tests: `inbox_bulk_test.go` (`TestInboxBulkFilterAsOf`: post-cutoff items / messages unchanged, dismiss, Undo only changed items; IDs, single dismiss, bad requests),
  `hub/devicetokens_test.go` `TestAnswerRoleOnlyWhatTheWatchDoes` (Watch blocked), `hub/testdata/route_scopes.txt` (`inbox.write` required); web `inbox.test.ts`; Android `InboxBulkTest`, `InboxRepositoryTest`, `InboxAssistantTransportTest`;
  Apple `InboxBulkTests` (responses / `undo`, old-hub fallback, failure rollback, confirmation threshold), UI `InboxMarkAllTests` (Clean up all + returned Undo; confirmation with `MOCK_INBOX_EXTRA`); hub `TestInboxBulkOldClientsDoneAndDismiss` guards old clients' `done` / `dismiss`.

## Section bar (2026-11-16, #9053)

One row of choices at the top of the list picks which section the Inbox shows: **Needs you · In progress · FYI · Done · All**
(zh-Hans 需要你 · 进行中 · 知悉即可 · 已处理 · 全部). It replaces scrolling past every Needs-you row to reach FYI or Done.
Who it serves: everyone using the Inbox on the Assistant side; it adds no setting. Shared cases: `spec/fixtures/inbox/bar.json`,
`bar_landing.json` and `bar_search.json`, replayed by web, iOS / iPadOS and Android (README "Section bar").

- **Choices**: Needs you and All always; In progress and FYI while they have a row (or are chosen); Done while it has a row, the hub's
  day counts name a done item (no filter or search on; the apps read the counts when the Inbox is shown, as Done already did), or it
  is chosen. A choice shows that section alone, with its source or time
  groups and their folds as in "Grouping and selection". **All** is the page as it was: every section stacked, folding as before.
  Collapsed has no choice of its own; it stays folded near the end of All.
- **Default**: the Inbox opens on Needs you each time it is entered (the sidebar, a widget, a quick action, "N more in Inbox", launch).
  The choice stays while you are in the Inbox (an item opened and closed, a refresh, a filter) and is not kept across visits, so a
  remembered FYI or Done never hides new requests.
- **Counts**: Needs you shows its rows (items and waiting Agents under the filters and search, the section's own count) in the
  Needs-you `CountBadge` tone; In progress and FYI show their rows in the neutral tone; Done and All show none. Zero hides the badge,
  99+ caps it, and the number is part of the choice's spoken name ("Needs you, 12"). The sidebar's Inbox badge keeps counting unread
  Needs-you items (#5974 decides one number).
- **Inside a choice** the section's title stays as a heading with its actions (FYI's Mark All Seen, Done's Jump To and Fold All /
  Unfold All) and does not fold, as Working's title already behaves. Done reads its history (day counts, groups, Show Older) while
  Done is shown.
- **Grouping**: By source / By time is the bar's trailing grouping menu, as on the Agents list (#7033), not a row of its own. It is
  hidden while In progress or Done is chosen: neither is grouped by source or time. Android puts the two choices under "Group by" in
  the Inbox's ⋯ instead: its `QuickFilterBar` has no trailing control (only the Agents tab's home capsule has one), its five choices
  already run past a phone's width, and a grouping may sit in the ⋯ (`docs/design/DESIGN_SYSTEM.md` "List controls").
- **Search** looks through the whole Inbox: text in the field shows All, the counts say where the matches are, a choice narrows them,
  and clearing the field returns to the choice you had. Source and kind filters narrow whatever is shown, and the counts follow them.
- **Empty**: All caught up only when nothing is open anywhere. A chosen section with nothing in it says so in its own words
  (Nothing needs you, Nothing in progress, Nothing to read, Nothing done yet), under them "The rest are in the other sections
  above." (Done: "What you mark done shows up here."), with the other counts right above it. zh-Hans 没有需要你处理的 / 没有进行中的 /
  没有要看的 / 还没有已处理的, 其余的在上面的其他分区里。 / 标为已处理的会出现在这里。; ja 対応が必要なものはありません / 進行中のものはありません /
  読むものはありません / 完了したものはまだありません, 残りは上のほかの区分にあります。 / 対応済みにしたものがここに表示されます。 Apple writes the
  English titles in title case. The filtered and no-match states stay as in "Nothing open" below.
- **Opening an item from outside the Inbox** (a link, ⌘K, a Needs-you line) lands the list on the item's choice: Done for a done
  item, All with Collapsed open for a collapsed one (its time group opened too). Beside the item on a wide screen its row is in sight
  and marked; on a phone, closing the item shows the list there, so a link lands where the item lives. **Opening one from inside**
  (another item's chain or relations) moves a list shown beside the item the same way and leaves a list that isn't shown alone;
  Back or closing returns it to the choice it had. A list that already shows the item (All, or the item's own choice) does not move.
  No notification opens an Inbox item today; one that does is an outside opening.
- **When the choices don't fit** (a phone at large text, Japanese, a narrow list column), the strip scrolls sideways with a fade at
  the cut edge, and choosing one, or landing on it, scrolls it fully into view; the web keeps All pinned whole beside the grouping
  menu. The Inbox opens on Needs you, the first choice, so its count starts in sight.
- **Selection**: Select All picks the rows in sight. Picks survive a switch, and the pick bar counts the ones out of sight as it
  counts folded rows. Clean up all keeps its scope (every open item under the filters).
- **Wide screens** keep the same bar at the top of the list (iPad's detail column, the web's list column, Android's list pane). No
  section sidebar: a third column beside the app sidebar and the item, and a control that only narrows a list drawn as navigation
  (DESIGN_SYSTEM 3.4).
- **Not chosen**: a dropdown (hides the state and its counts), filter chips (they already mean source and kind), only capping rows
  with Show N more (FYI stays a long scroll away), sideways swipes between sections (a row's swipe marks it done, and the iPhone
  shell pages sideways). The research and comparison are in #9053.

## Grouping and selection (2026-11-10, #2059)

The same rules on the web (`web/src/lib/inbox-groups.ts`) and iOS / iPad (`Persona/Views/Inbox/InboxGroups.swift`); Android follows in #2576. The retired native Mac app kept its earlier list behavior ([mac.md](../archive/apple/mac.md)).

- **Groups**: Needs you, FYI and Collapsed group by source (default) or by time, switched in the section bar's grouping menu ("Section bar") and kept on this device
  (web `localStorage` `inbox.grouping`, Apple `@StoredValue("inbox.grouping")`). Done always groups by when it was done. Working is never grouped.
  A section that would have one group shows no group titles. Waiting Agents get their own group first in Needs you ("Agents waiting on you"); they are counted but can't be picked.
- **Time groups** are calendar days in local time, not 24-hour windows: Just now (within the hour, Done only, so a slip is easy to find and undo), Today, Yesterday,
  each of the 2 to 6 days before by its date (the last week, not the calendar week), 7+ days ago (7 to 29 days) and 1+ month ago (30 days or more, or no time).
  A moment in the future counts as today. Open items use their last activity (`updated_at`), Done items `done_at`; newest group first.
- **Source groups** follow the filter chips' order: Lark, each mail account, each connector (by its account), then sources the chips don't name in the order they first show up.
- **Filter chips (web)**: sources (Lark, which also holds lark-cli's items; each mail account; each other connector), then kinds: @me (mentions, plus document and Moments mentions), Direct, Group, Bot (a bot's own chat, no longer under Group), Doc comments, Mail, Agent.
- **Module availability and stored sources (#17399)**: an existing Lark source entry or Lark mention in empty-state copy is hidden only when the hub offers neither the Lark extension capability nor `module.lark`, **and** `stored_sources.lark` is false. If either is present, the current entry and wording stay; a missing or `null` `stored_sources` is unknown and keeps today's behavior. The same rule applies independently to mail through `module.mail` and `stored_sources.mail`. Android keeps its record-derived source options rather than gaining an always-visible chip. Stored items remain listed and readable whatever the hub offers. Generic @Me / Direct / Group filters are unchanged because they match item kinds across sources, not Lark alone. Shared cases: `spec/fixtures/inbox-sources/presence.json`; wire compatibility: `docs/protocol/protocol.md` ("Inbox API").
- **Folding**: every group folds to its title and count; folds are kept on this device per manager, section and group (web `inbox.folds`, Apple `@StoredValue("inbox.folds")`).
  Open at first: every group of Needs you, FYI and Collapsed; in Done only Just now and Today. Group titles stick to the top while their rows scroll by.
  On iOS, Done's title is a list row while Done lists its groups, not the header of an empty section: as that header (with its Jump To menu)
  it came out washed like a stuck title's, and on an iPhone the scroll edge blurred every row above it once a title stuck (#4218).
  Done's title has Jump To (its groups with counts; picking one unfolds it, loads it if it isn't, and scrolls to its first row, since an iOS `List` can't scroll to a header)
  and Fold all / Unfold all.
  On iOS each title a finger taps (a section's and its Mark all seen / Clear all, a group's and, selecting, its tick, Done's and its Jump To) is at least 44 pt tall,
  its header or row adding no room above or below it (#6576, `InboxTitleTargetUITests`); the tick is a finger wide too, over the header's inset and the gap before the title, with nothing drawn moving (#6658); a tap on Jump To opens its menu and leaves Done's fold beside it alone.
  A Jump To still waiting for its group's first page gives way to what comes after it: the next Jump To, the user dragging the list, or the list going out
  of sight (an item opened over it, the Inbox left) cancels it, so the page coming late does not take the list from where it was sent since (iOS, #6661, `InboxJumpToRaceUITests`).
- **Done's history**: past what the list holds (its 3 days, at most 200 done items), Done reads each manager's `GET /api/v1/inbox/done` ("History" under "Lifecycle"; 100 a page; on iOS the default manager's).
  Items the hub didn't stamp with a manager get the default one when there are several, so the per-manager filter keeps them. Done from history reopens through the item's own manager.
  - **With day counts** (#2969; "Days" under "Lifecycle", #2967): when the list is shown the apps ask for the counts in this device's zone, and Done lists every group they name
    with its exact count, loaded or not (Today less what Just now holds). Groups older than today start folded, so nothing more loads until one opens: opening a group, or
    picking it in Jump To, loads its first page from its end (`before=<the group's end>`, no `before_id`; on the web, past the oldest done item the list holds when that falls in the group, so the page brings rows the list doesn't have), and Show Older inside it loads the next, until a page reaches
    the group's start, or (unfiltered) once as many are loaded as the hub counts. The list's own done rows show at once, but its 3 days page like the rest, since it carries
    only their newest 200 (web, #5549). iOS builds (#5558) and web bundles from before that take the groups of the list's 3 days as complete without a page, so past 200 done
    items in 3 days they show the newest 200 there and the counts; the history still pages them all. A web link to a done item neither the list nor a loaded page has
    reads it by id (`GET /api/v1/inbox/{id}`). Filtered or searched, the hub's counts don't apply: Done shows the groups with
    matching items, counted as loaded and "n+" until complete, and Show Older under Done loads the newest group the hub counts that hasn't all come.
  - **Without them** (a hub from before #2967: 404 / 405): the first page from the newest when the list is shown, the next from Show Older at the end of Done; the last
    group's count reads "n+" while older pages remain. Old hubs without the history (404 / 405) quietly have none.
- **Selection** (web, iOS / iPad): Select in the header (iPhone hides the tab bar meanwhile; Esc ends it on the web, and on the web Select and Done are one button, so the keyboard focus stays on it); rows get a tick. Select All picks only rows in sight;
  a folded group is picked through its title's tick (none / some / all of it), which picks or unpicks the whole group, folded or not, and its title says how many are picked.
  The web's bar also says how many picked rows are folded away. The one action is Mark as Done: `POST /api/v1/inbox/bulk` `action: done` per manager, with Undo ("Clean up all").
  There is no Mark as Read: Done stays Persona-only.
- **Nothing open** (web, iOS / iPad): the check mark and All caught up only for the whole Inbox. When the chips leave nothing open (with or without a search), a neutral
  filter mark, "Nothing open matches these filters", "Clear the filters to see the whole Inbox." and **Clear filters**; a search alone, the search mark, "No matches",
  "Try a different search." and **Clear search**. Either button clears the chips and the search: the chips are kept on this device, so a list left filtered opens
  filtered, and the Inbox outside them may still hold plenty (web #7478, `docs/web/web.md` "Pages"; iOS #7551, `InboxPage.emptyKind`).
- DEBUG (iOS): `-showInboxSection action|working|fyi|done|all` (the section bar on that choice; without it `-showInboxDone` and `-showInboxJump` choose Done and `-showInboxPickGroups` All, and `-showInboxPickedDone` shows Done after marking), `-showInboxPicked <n>`, `-showInboxPickedDone YES`, `-showInboxDone YES` (Done and its newest group open), `-showInboxPickGroups YES`, `-showInboxDue YES` / `-showInboxMerge YES` / `-showInboxTodo YES` (the first two rows picked and the due sheet open / merged / added to to-dos for today, #6480; `MOCK_INBOX_TODO=1|time` puts a to-do suggestion on the mock's design-review mail),
  `-showInboxJump <group>` (Done jumped to that group, `week` or `month`, its first page loaded). Mock hub: `MOCK_INBOX_HISTORY=busy` (36 done a day for two months), `MOCK_INBOX_DAYS=none`.
- Tests: web `lib/inbox-groups.test.ts`, `views/inbox/Inbox.groups.test.tsx`, `views/inbox/Inbox.select.test.tsx`; Apple `InboxGroupsTests`, `InboxBulkTests`, `InboxFilteredEmptyTests`, and for the section bar `InboxBarFixtureTests` (the three shared files), `InboxBarTests` and `InboxSectionBarUITests`.

## Group titles (2026-11-18, #10422)

A group's title in **FYI** offers **Mark all seen ⌄**, and in **Collapsed** **Clear all ⌄**: a pull-down with two choices, the
group's items ("Mark all 4 in “Assistant” seen") or the whole section's ("Mark all 6 in “FYI” seen"). Group titles stick to
the top while their rows scroll by, so once the section's own button has scrolled away this is the one in sight. Who it
serves: everyone reading the Inbox on the Assistant side; it adds no setting and no request.

- **Where**: only the sections with a section-wide action (FYI, Collapsed). Needs you and In progress get none: their items
  need the owner (or the AI is on them), and a batch there is how things get missed; Done is already done. A section with one
  group shows no group titles, so its own button acts at once, as before. Not while selecting.
- **Scope and counts**: the open list is complete (`GET /api/v1/inbox` lists every open item; only Done pages), so "all" is
  every item the group or section shows under the current filters, folder and search, the number on its title. With any of
  them on, each choice says under it what it covers in Clean up all's words ("Lark · @Me", "Search “report”"). The request is
  the section button's own (Apple `POST /api/v1/inbox/done`; the web `/bulk` with `action: done`, falling back to `/done`):
  ids and versions, one request per manager. Items that took new messages after the list was read stay open, and the toast
  says how many.
- **Undo**: the rows leave at once and the toast's Undo reopens what moved; a failure puts them back ("Mark All as Done").
- **Look**: the section's own words and a small chevron (iOS `Theme.Icon.pullDown`, the web's `chevron`) at the right of the
  group's title, its target 44 pt tall on iOS; the words stay whole and a long group title gives way.
- **Android (#10425)**: every text size uses `JTextButton` and an anchored `JMenu` with `PersonaIcons.expand`. Above 1×,
  `JMenu(multilineActions = true)` keeps the full choice and its scope subtitle readable instead of ellipsizing them.
  Both choices use the existing versioned `markAll` bulk-Done path and `bulkScope`; the FYI section header's separate
  Mark all as read receipt is unchanged. Tests: `InboxGroupClearTest` (targets, filters, Undo, failure, en / zh-Hans / ja)
  and `InboxGroupClearShotsTest` (every style, light / dark, 200% text, visible characters).
- Tests: Apple `InboxGroupClearTests` and UI `InboxGroupClearUITests` (with the mock's `MOCK_INBOX_EXTRA=<n>` it also shows the
  mail group's title stuck at the top); web `views/inbox/InboxList.group-clear.test.tsx`.

## App

- `InboxStore` (`Persona/App/InboxStore.swift`): list cached locally (`inbox-items`), shown before refresh on cold start; fetch on Inbox events, reconnect, page entry.
  Mark done locally before request; restore on failure. A mark the hub may not have yet is laid over every list that comes in (`InboxMarks`, #2201): while its request is out, over every list; once the hub took it, over the lists that started before then. So a reload taken before the hub had it (the push after another item's Done) can't bring a swiped-away item back; the web does the same (`withPending`). Tab badge = unhandled Needs you count.
- Page (`Persona/Views/Inbox/InboxView.swift`): top filters (All / Lark / each mail account | Mentions / Private / Groups / Mail); search titles, senders,
  body, summary. The section bar is the list's first row, above the filters (`CapsuleFilterBar` with the grouping menu at its end, hidden under In Progress and Done; `Persona/Views/Inbox/InboxBar.swift`, "Section bar" above): a choice shows its section alone under a heading that doesn't fold, and All stacks the sections as follows.
  Every entry opens on Needs you, also the Inbox chosen again while its page stays up (the place in front picked again, a widget, a quick action,
  visionOS's Inbox tab: `AppModel.inboxEntries`). iOS and iPadOS never show the list beside an item, so they follow the phone's rule: an item
  opened from outside (a Needs-you line over the composer) is pushed over the Inbox, the list landed on its choice (`InboxBarLanding`), so
  closing it shows the list there, as an item a place kept and brings back does; an item opened from another item's chain or relations is
  pushed over that item and leaves the choice alone, so Back returns to it. A strip that doesn't fit scrolls with `.edgeFade()`, and the
  choice landed on or tapped scrolls clear of the fade (`revealsSelection`).
  Sections: Needs you (expanded), Working (always expanded, non-tappable heading, Inbox phase 1; present only with hub `status`, absent on older hubs using `tier`; excluded from badges and Watch Needs you, while urgent `status: action` still counts), FYI (expanded, Mark All Seen to the heading's right), Collapsed (collapsed, Clear All), then Done (collapsed); groups and selection in "Grouping and selection".
  Tap headings to expand / collapse. Row: 40-point avatar (18pt source badge at bottom right: Lark blue, mail neutral gray), title (headline) + type `Chip` (mentions red), time;
  a connector's row shows its thread's picture (`connector.avatar_url`; iOS `ChatAvatar`, Android `JAvatar` through `rememberLarkAvatar` at the avatar's actual dp size times display density; #10923, #10932, `docs/protocol/protocol.md` ("Avatars (#10923)")), the initial while it loads, without one or when it fails. Android keeps the item's manager on deferred reads and cache keys, and does not request a loaded picture again on scroll-back;
  with summary, second line ✦ summary, third small "sender: content" (secondary gray, not tertiary); before triage, just "sender: content." Count badge (normally gray fill, primary-color fill for mentions).
  Separator starts at text column (16 + 40 + 12). Lark avatar palette avoids red / orange that imply Needs you. Full left swipe = done. More contains Clean up all (see "Clean up all" above).
- **Waiting Agents also appear under Needs you**: pending questions, plans, permissions, terminal prompts, including assistant `ask_user`, excluding muted Agents, sort first
  (`InboxAgentRow`: rounded square like Machines session rows, request-type icon on light red fill; Agent title, question second line,
  red "Claude is asking / Plan needs approval" + machine third line. No type chip after the title, which gets full width). Tap opens the Agent chat (assistant in Chats tab). Combined only in the App,
  not stored by hub Inbox; Agents unchanged. Agent filter shows only these, while source filters hide them.
- Opening: Lark chat at the item's first new message (`LarkChatView`, focus = start_position), mail at latest message; Done at top right.
  Visible Lark messages are reported read, including the first screen opened from Inbox (confirmed 2026-10-31; gate in `docs/archive/redesign/ux.md` 3.5 / 3.6). Done itself stays Persona-only.
  Under navigation, `InboxActionBar`: ✦ summary + AI Draft Reply / Hand to AI.
  Relationships below summary (#3071, matching web #2352; `Persona/Views/Inbox/InboxLine.swift`): fetch `GET /api/v1/inbox/tree/inbox/{id}` on open,
  list parent chain first, distant ancestors above (to-do / Agent session / event), then child Next steps. Tapping an event pushes in the current stack,
  a to-do opens details, an Agent opens its chat. Missing ancestor items show "No longer in Inbox" and cannot be tapped. No ancestors / children or old hub without API means nothing drawn.
  Mac puts it below summary in the right action column (`MacInboxActions`); event taps push in the detail column's own stack, returning to the original item.
- **Events (Inbox phase 1, iOS / Mac)**: models in `Shared/InboxEvents.swift` (`handling`, `suggestion`, `chain`, `related`, tolerant per-entry chain decoding;
  unknown `kind` rendered from `text` with a generic icon). An item is an event (`isEvent`) only if it has one of these fields or `recap`; otherwise rows / details remain pixel-identical.
  - Row: suggestions show their kind + first line below the summary (reply / Agent / conclusion / judgment needed), replacing the old AI twin draft card (hub mirrors replies into `agent.draft`; do not render twice).
    Working shows handling state (AI reviewing / Agent working / awaiting reply); urgent items without suggestions show AI is preparing a suggestion; failures show "AI couldn't handle it" (`InboxEventText.mark`).
  - iPhone details use one page (`InboxEventPage`): the small where · who: newest line with View original at its trailing edge; state, Conversation and Say something / Try again;
    ✦ summary in the state's secondary type; suggestion card (adjacent buttons: reply Send · Edit · Reject; Agent Start · Edit · Reject; conclusion OK · Reject;
    judgment Confirm · Edit · Reject); recap; handling chain; related items and relationships; then AI Draft Reply (absent with a reply suggestion) and Hand to AI at the bottom.
    Conversation appears when `handling` has both `machine_id` and `session_id`; it pushes the processing conversation over the item, so Back returns to the detail.
    Original source pages (Lark chat, mail, plugin-rendered) sit behind View original and Received messages chain entries (`InboxSourceRoute` → `InboxSourcePage`, Lark at link position, mail at link uid).
  - Mac keeps the source on the left rather than opening a separate page. Below summary in right-side `MacInboxActions`: state, recap, suggestion card, chain, related items.
    Chain entries linking only to the source do not navigate (already beside them); position / uid links push within the detail column's own stack. At narrow width with only the top action strip, recap keeps three lines.
  - Edit (`InboxSuggestionEditor`): changing text then Send / Start / Confirm = `confirm` with `text` (omit if unchanged); Say something + Tell AI below = `instruct`.
    One `client_id` per attempt, with the card's displayed `version`. `409 suggestion_stale` shows Suggestion updated and redraws from the returned item, closing the editor. Failure stays in the editor for retry (`InboxSuggestionModel`).
  - Chain initially shows only the latest entry; Expand all (N) reveals all, oldest first. Each row: time · kind icon · text. Taps follow `link` (`InboxChainTarget`):
    source → source page; `session` → chat (manager handling chat in assistant, sub-chat as Agent; on the iPhone shell both, and AI Draft Reply's See How the AI Twin Handled It, push over the item (`InboxChatRoute`), so Back returns to it, #3114); `item` → that item; no link → inline `detail` / `messages`.
    Entries with both navigation and messages (Received messages) have a separate expand arrow on the right.
  - Manager-archived items (`done_by: "manager"`) show "Handled by the manager" in detail state. Chain `done` (user marked done / manager found no action needed) uses ✓ and is not tappable.
    FYI conclusions remain under FYI with their card; OK archives them.
  - Watch unchanged: uses `agent.draft` as before.
  - Unit tests `InboxEventsTests` (decode every event state in `PersonaTests/Fixtures/inbox-events.json`, sections, row text, chain targets, suggestion buttons with FakeHub).
- DEBUG: `-tab mail` (Inbox), `-showInboxSection action|working|fyi|done|all` (the section bar's choice, #9053), `-openInboxFirst YES` (first unhandled item), `-openInboxItem <id>` (specific item; on the iPhone both put the item on the Assistant stack in the list's place, so `InboxSectionBarUITests` walks the landing through a Needs-you line on an iPhone and a kept place on an iPad), `-showInboxAction draft|handoff` (then that sheet),
  `-showInboxSuggestionEdit YES` (suggestion editor on iPhone event page).
- **Plugin-rendered details** (#1681; `docs/extensions/extensions.md`, "Inbox items"): for items from a plugin connector (`source` = connector) contributing `inbox_item`,
  the plugin's item View is the slot's source body. Hub passes `{id, title, summary, tier, reason, done, meta, extra}`; meta unchanged, unparsed.
  On iOS and visionOS, View original and source chain entries whose existing source precedence reaches a native chat open `LarkChatView` directly,
  at the chain link's position or the conversation's own focus; they skip the plugin's intermediate item View and its Back-to-event strip.
  Document, connector and Moments content keeps its pages ahead of a native chat when an item carries both; mail and the empty fallback keep their slot behavior too.
  Sources without a slot, old hubs, read-only devices and rendering failures keep the original body. The Mac detail root is unchanged; its position links use the same source page.
  For plugin pages still shown, View in chat returns `persona://inbox/<item id>/source`; the App opens its original source page.

## Item actions (`Persona/Views/Inbox/InboxActions.swift`)

- **AI Draft Reply**: `POST /api/v1/inbox/{id}/draft {instruction}`. The manager uses the Draft reply model in the user's voice (Lark short and conversational; mail matches the incoming language and formality). With nothing the hub can call directly for it (only launchers such as Claude Code default, no Direct API provider or keyed Messages launcher) it answers `503 model_unavailable`, localized and naming Settings › Model providers and Model configuration; the web shows where to set one up instead of a bare HTTP error. Other failures stay `502`.
  The draft knows "About me" as the user last edited it in the app (else `assistant.notes` of the manager's config; #5769).
- **Extension context** (#5771): extensions with an `inbox_context` contribution add a short block (1 KB each) to both the triage and the
  draft prompts (`prompts/inbox/extension_context.md`), read through the extension manager at most once a minute. Its optional
  `never_quote` list holds sentences a draft must not repeat word for word (an extension's private notes about people): it is never put
  in a prompt; after a draft is written the manager compares it with each sentence of at least 6 letters or digits (NFKC, lower case,
  letters and digits only, so spacing, width and punctuation do not hide a repeat). On a hit it writes the draft once more, told not to
  copy the background; if that one repeats a sentence too, there is no draft: `422 draft_withheld` with a localized message asking the
  user to write the reply themselves. Details: `docs/extensions/extensions.md` "Assistant content from the data directory".

- **Recheck task link (maintenance)**: authenticated `POST /api/v1/inbox/{id}/task-link/recheck` reuses `inbox.judge`
  with all current open tasks, including for an old open event with an existing processing conversation or draft.
  Judge candidates do not inherit the assistant briefing's item limit or age window; closed/cancelled tasks are excluded.
  Each candidate includes up to 1200 characters of its original request as subject context. The judge compares the
  actual requested work, not names or messages merely quoted as examples in another task's notes.
  An existing event also supplies up to 1600 characters of its investigation recap, marked as possibly stale subject
  context rather than an instruction or confirmed reply; the current message takes precedence.
  Eligible events require the AI twin to be enabled and preserve existing links. Bot/app broadcasts are excluded
  before either exact-link routing or the model judge. Legacy Lark private chats with an explicit user sender
  identity remain eligible, even if their old kind says `bot`; quoted notification text does not make a human a bot.
  For an excluded event the maintenance action needs no model or enabled twin: it answers `excluded`, or `cleared`
  after removing a stale task association through normal rollback-aware writes (`method: "policy"`). It preserves
  the draft, suggestion, processing conversation and completion state, and publishes no task card or supervisor signal.
  Other results are `{status, method?, item}`: `linked`
  or `unmatched` (with `method: "judge"` when the model ran), or `existing`; `409` distinguishes `disabled`, `closed`,
  `changed` and `task_closed`. Model failures answer `502`, storage failures `503`, and a missing event `404`.
  A changed message or a task closed during judgment invalidates the result. A new link is persisted through normal
  Inbox writes before raising the existing supervisor signal; a saved reply draft also gets its task message card.
  This action never starts processing, sends a reply, changes a suggestion or files an event/task. It does not
  periodically rejudge old events: an operator explicitly requests each bounded recheck. Normal initial escalation
  still judges only events without a processing session; refresh alone does not rejudge an existing draft.
- **Linked work (Web)**: the detail header shows the linked task and its current task-lineage Agents, each as an
  in-app link. These are the business task's workers, not the Inbox draft-processing conversation. Older hubs without
  lineage use unreleased workers; a task still starting never substitutes a previous lineage worker. The shared
  task list supplies live titles and manager ownership, with the task ID as a fallback before it is loaded.
  Do not invent unknown facts or leave 【】 placeholders for the user; omit unknown details. Include `assistant.notes` as About the user. Edit directly in the sheet or enter "Have AI revise: …" to rewrite.
  Send uses `POST /api/v1/inbox/{id}/reply {text}`: the manager asks hub to post in the original Lark chat through its plugin, or reply by mail (Re: subject, In-Reply-To / References,
  quote first 40 lines, using the receiving account). With a processing conversation, it keeps the item open in `waiting` with the default 24-hour follow and clears any remaining suggestion; without one, it marks done (`done_by=replied`). Old-App card sends and replies confirmed in a handed-off conversation still file as replied. Phone and Watch use the same API.
  A reply handles only messages present in the item when sending began. New messages arriving during sending (parallel collection changes `last`) leave the item unhandled; new messages are not archived with it (#1828).
  Lark replies send once: phone / Watch taps or timeout retries send identical text only once (queued per item, `clientId` = UUIDv5(item ID + text)).
  Retrying identical text for an item left unhandled above does not resend. Requests with `client_id` use their own IDs; a new ID is a new reply. Web / Apple do not pass it yet,
  so identical replies to newly arrived messages are treated as retries. Manager `send_id` to hub ("Send once" above) is derived the same way
  (use `client_id` if supplied, otherwise item ID + text + changes, not `last`), so retries after manager restart also do not resend.
  Lark replies go where the other party can see them (`lark.reply_to` / `lark.thread_kind`, recording the last message by someone else and whether it is in a topic during collection):
  private chats post normally; topic items (topic-group topics or topic replies in ordinary groups) reply to that message within its topic (`replyMode:"thread"`);
  group mentions outside topics quote that message; other group items post normally (unclear reply targets would make quotes odd); legacy data without `reply_to` also posts normally.
  Topic groups collect only the latest page of topic roots (P-04 unfinished), so the last message from someone else is the latest topic, not necessarily the one with new replies.
- **Hand to AI**: attach the item as `<persona-context kind="context" title="Lark · Group name">` (hub `context` + feedId / mail uid, letting tools read more and
  reply), plus the requested action (default "Summarize this, tell me what I need to do, and suggest a reply," with two other presets), sent as one message.
  Targets: **Persona (main chat)**, the default (#1194, every client): the item's own manager's main conversation (another manager's item goes to that manager's,
  found by asking it; with none yet, the message starts it), which then opens; **Persona (new chat)** starts a new conversation with the item's manager (iOS / Mac switch to the
  Persona tab); an Agent (latest 8) receives it in its session (navigate there). Only live, writable Agents on online computers are offered. The web, iOS, Mac and Android leave the item open by default for every target after the message arrives.
  Apple sends an Agent hand-off as one pending outbox message: a wait for the connection keeps the same message and operation id through the outbox's retries, not a new send; the item is filed only at the terminal sent state, and a refusal keeps the sheet open with its reason (#1846). The filing intent and whether it was initiated are restored with the outbox, so delivery after an app relaunch still files the item once.
  On iPhone, iPad and the Mac, after a new chat or Agent (including New Agent) accepts the hand-off, the client records the session with `POST /api/v1/inbox/{id}/handoff` and body `{session: {machine_id, session_id}, kind: "conversation" | "agent", title, client_id}`, with no `text`. This only records and links the session; it sends no second message. A new chat uses the item's title; an Agent uses its session title when listed, with the item's title as the fallback. The main chat's original request already records itself and is not repeated. Any record failure, including an older hub's 400 / 404 / 405, is ignored without changing the successful hand-off or its UI.
  For this record-only form, the manager requires both session ids and `kind` `agent` or `conversation`, and rejects `text`. It appends the same `handoffs` receipt (the latest five; idempotent by `client_id`, otherwise derived from the item, session and kind) and one chain entry per kind and session: `agent` or `handling`, linked to that session with `via: user` and manager-written localized text (the title clipped to 40 characters, generic without one). It changes no processing state, files nothing and wakes no conversation. The recorded conversation can write back with `inbox_suggest` `item`; only `agent` entries participate in Agent-stopped reporting.
  **New Agent…** (#5933; iOS / iPadOS bar and sheet, the Mac through the same sheet, web; Android later) is the first row of the Agents, shown even when none is running,
  for giving the item to a fresh Agent in another folder. It reads the item and opens the existing New Agent page prefilled:
  - **Context:** the item as a card over the first message, sent ahead of it: the same `<persona-context>` an Agent target gets.
  - **First message:** the typed ask or the chosen preset (the first preset when nothing was typed), editable there.
  - **Name and computers (Apple):** the item's title as the name hint; only the computers of the item's hub.
  - **The rest:** computer, folder, runtime and model are chosen there as for any new Agent, from that page's own last choices.
  - **Confirmation:** nothing is sent before Start there, so there is no Undo toast. Cancel comes back to the item with the ask kept: the bar's field or the sheet / dialog as
    it was.
  - **After start:** the Agent opens (on the Mac once the sheet has closed, as for the other targets), and the item's source remembers that Agent, never "New Agent…" (the bar's `inbox.handOffTarget` too), so the next item from it starts there
    while it can take messages. "Also mark as done" is off by default for every target on the web, iOS and Mac; Android's existing Agent target defaults off too. The user's choice stays, and a checked item is filed once the Agent started (web: once its first message went).
  - **Hub and manager:** the page's start (`POST /api/v1/sessions`) and first message are New Agent's own; on iPhone, iPad and the Mac, the client records the Agent on the item through the hand-off endpoint above after Start.
  **Write-back to the item** (#1194, web, iOS, Mac and Android): the main-chat target calls `POST /api/v1/inbox/{id}/handoff` `{text, client_id}` on the item's manager (`assistant/inbox_handoff.go`),
  which sends the text after a block naming the item (`prompts/inbox/handoff.md`) to its main conversation through `session.send_once` (the client id is the message id, so the same
  request again sends nothing more), or starts the main conversation with it, and answers `{item, session}`; a work machine is refused, and a failed send records nothing (`apierr.Call`'s status, as for a new conversation).
  The item keeps the hand-off (`handoffs`: `machine_id`, `session_id`, `at`, the latest 5) and its chain gets one `handling` entry per conversation ("Handed to Persona's main chat",
  linked to it). Its processing conversation (`agent`, `handling`) is not touched, so the AI twin's sweep and timeouts never take the main conversation for one of its own.
  That conversation can pass an explicit `inbox_item` on each `task_plan` task. The manager validates the conversation,
  machine, open item and link policy against its persisted handoff ledger, records the handoff operation on the plan,
  and rechecks it at Start. Bot notifications and owner-bot handoffs fail this policy. Unrelated tasks omit the field.
  When a plan omits the ID, [recent topic matching](#chat-created-tasks-and-recent-inbox-topics-15557) can infer a
  source for new work.
  Admission persists the to-do's existing `inbox_item` back-reference and the item's `agent.todo_id`; an explicit handoff replaces an earlier inferred link
  without filing the item, changing its draft or sending a reply. New task rows include their source in the initial insert and stay reserved
  through binding and first start, so another plan cannot adopt a half-admitted task.
  Explicit item choices resolve multiple-item ambiguity. Multiple tasks retain their own back-references:
  the web detail lists each task and its current workers, scoped to the item's manager, and one completed task cannot file an item with other unfinished tasks.
  Reusing a to-do preserves its source; assigning it to a different item is refused. A failed item write rolls back the binding before any Agent starts.
  If a newer item write retains that target, admission instead keeps the to-do and its source, reports the failure without starting an Agent,
  and supplies the same `todo_id` for retry rather than deleting a target the newer write still references.
  An explicit `inbox_item` on `agent_send` (with or without `task`) or `task_scope` can add this context to an existing
  task. Omission leaves Inbox links alone. With a valid ID, a successful send (including queueing) or scope change saves
  the item's task backlink and session link on its Agent chain; it creates no task or Agent and does not replace the
  task's original Inbox source or the item's processing conversation. A failed send claims no assignment.
  An item already linked to another task is refused. Rechecking the exact handoff before saving prevents a filed
  item from gaining a new link. An association-save failure reports that the message or scope change already
  succeeded, rather than suggesting that the user resend the message; normal item-write rollback removes the
  unsaved backlink and chain entry.
  That conversation may then write back with `inbox_suggest` and `item` (above). What the user does with its suggestion is told there, wrapped to name the item
  (`prompts/inbox/handoff_note.md`; a Persona notice sent as a plain `session.send` without a message id, a failure only logged, which the hub marks `injected` (`system` / `inbox`)
  so the apps show one line, "Inbox: an update on “title”", not the model's text as a user bubble): reject, instruct, handback, a confirmed reply sent (which files the item as replied:
  nobody there follows the other side's answer) and an Agent started (as that conversation's own task; the chain's `agent` entry links it). A confirmed conclusion files the item as
  usual; a processing conversation still on the item is told it is over.
  **The write-back in the chat** (#5932): a reply or a conclusion handed in that way is also a card in that conversation (feed kind `inbox_suggestion`,
  `assistant/inbox_handoff_card.go`; `docs/protocol/protocol.md` "Tasks and the chat feed"), under the turn that wrote it: the item's title and source, its
  recap folded to three lines, the reply (editable) or the conclusion, and Send / Got It with Later. The card is a snapshot of that suggestion version; the
  apps read the item for the rest, so the item stays the one home of the request and Needs you counts it once: while the item's suggestion is still that
  version from that conversation the card confirms it (`POST /api/v1/inbox/{id}/suggestion`, the same path as the item's card); otherwise it says how it
  ended (replied as you, the user answered at the source, handled without it, or no longer current). An Agent suggestion, a handback and a recap alone
  get no card. While items handed to the main conversation are open, its composer shows "Wrap up: <title>" for the two most recently handed (web
  `views/assistant/HandoffCard.tsx`, iOS `HandoffCardView.swift`): it hands the item over again with a fixed ask for the final summary and reply, so the
  block naming the item goes with it and nothing goes to anyone else. On the web (#6431, `lib/handoff-card.ts` `wrapUpItems` / `wrapUpWait`)
  an item whose handling is done with no suggestion waiting gets no chip (nothing is left to wrap up; it stays in the Inbox), and a chip whose item has
  a suggestion waiting says so: "To send" for a reply, "To confirm" for anything else. Needs you still counts the item once, in the Inbox; iOS still
  shows a chip for every open item handed over. `prompts/inbox/handoff.md` tells the conversation to write back when the user wants
  a reply or a conclusion or the matter is settled, and again whenever it changes. The web's card has "Open in Inbox"; iOS has no way yet to open an
  item from the chat, so its card leaves rejecting and instructing to the item's page. Mock: `MOCK_TASKS=1 MOCK_HANDOFF_CARD=1` (`sent`: the reply went).
  "Also mark as done" is off by default on the web, iOS, Mac and Android for every target. Switching targets follows that default until the user sets the box,
  whose choice then stays. Checked, the item is filed once the hand-off is answered, and a later `inbox_suggest` on it is refused as already filed.
  Not covered: later messages on the item are not forwarded to the main conversation (it reads the source with its tools when asked), and filing the item is not told there.
  **The iPhone's event page** (#2055, `Persona/Views/Inbox/InboxHandOffBar.swift`) has no sheet: a bar pinned to the bottom, like iMessage's, at most two rows high and riding up
  with the keyboard (the page gets its height as a bottom inset). The top row scrolls sideways through one-tap chips: the three presets (Summarize & Suggest Reply, Look Into It,
  Handle It) and Draft Reply / View Draft, which opens AI Draft Reply's sheet. Under them: a target capsule (a system menu: Main Chat, New Chat, then New Agent…, the latest 3
  Agents and More… with the next ones), a one-line "Hand to AI…" field and Send. A chip or a send goes without a confirmation, held for 5 s behind a toast at the bottom (`InboxHandOffModel.undoWindow`):
  Undo takes it back (a typed ask goes back into the field), View sends it at once and opens the conversation; left alone it goes and the toast says where, with View. Sending
  never leaves the page. The last target is remembered (`inbox.handOffTarget`); an Agent no longer listed falls back to Main Chat. "Also mark as done" is off, so the toast does not say the item was filed.
  **Android's event page** (`InboxHandOffBar.kt`, `InboxHandOffBarModel.kt`) follows the same two-row pattern and local five-second wait, with Main Chat (no New Chat target), the latest 3 Agents and More… covering at most 12. The target menu reads the current sessions only when opened. Draft Reply reuses the item's existing `ReplyComposer` and `InboxDraftSheet`, and is hidden for a reply suggestion or an item that cannot accept replies. Undo only cancels the local wait and never calls the hub; Queued View fires immediately and opens after success, while Sent View only opens the returned conversation. Sending and success leave the user on the event, and a persistent status row keeps View / Retry reachable after the local toast expires. A failed explicit Retry keeps the first prompt and operation id. Mark-done failure is reported separately after an accepted hand-off, never as a failed hand-off or an automatic retry. The model is kept per item in the pairing's `InboxHandOffBars` (`InboxDataOwner`'s facade, and `InboxExperienceRepository` where used), so leaving or rotating the page does not cancel it. Each store binds the fixed repository and preferences of the pairing that created it: queued work, an accepted hand-off's memory write and Done never resolve the currently selected hub group again. Replacing or closing the pairing cancels the scope. Settled models are retained in a 64-entry access-ordered cache; Queued and Sending models are never evicted, and revisiting an evicted item starts Idle without replaying its old operation. Done rows already present in the live list and missing items offer no interactive composer; a completed item opened from the Done history keeps the hand-off bar and stays Done after hand-off.
  The Mac and the iPhone's source pages (whose own reply composer sits at the bottom) keep the sheet, which opens the conversation after sending.
  **Per source** (#2121, `InboxHandOffMemory`, shared cases in `spec/fixtures/inbox/handoff_memory.json`), the bar and the sheet remember where an item went:
  - **Key:** the item's source plus its most specific mute option (`repo`, else `chat`, else `sender`), in the hub group.
  - **Next item from that source:** starts on that target, marked **Last time**. Nothing is sent until the user sends. An item without one of those options starts as before (the bar's last target, the sheet's Main Chat).
  - **The remembered Agent can't take it now** (it has ended, can't take messages or its computer is offline: the same test as the Agents offered): Main Chat stands in, with a line saying so.
  - **Visible Mark as Done:** on iPhone, iPad and the Mac, a preselected Agent leaves Mark as Done off, like every other default target; only the user's explicit choice turns it on. The bar's toast says "· marking done" / "· marked done" only for that choice, and the sheet shows its toggle.
  - **Storage:** local to the device, ids only, the 200 most recent sources, not synced, and forgotten with the group (`forgetGroups`, `unpair`).
  iOS and Mac (`InboxHandOffModel`) send it with one `client_id` per hand-off. Whenever the answer leaves open whether the text went (no answer or a timeout, the connection lost
  midway, a 5xx without a `code` from something in front of the hub or from the hub unable to keep its receipt, `assistant_in_doubt`, `delivered: unknown` or `yes`, the node lost
  without `delivered: none`, a pending (`202 {"state":"pending"}`) or unknown receipt), the sheet says to check the chat before sending it again, and the same ask sent again is the
  same hand-off: its first text, not one rebuilt from the item (which may have changed since), under the same id. From then on every other answer under that id short of success (all
  but the three below that say nothing about an earlier try) is taken the same way, even one saying nothing went (`delivered: none`): the hub gives back what it kept for that id,
  which may be a relay the phone or a proxy cut off (a bare `internal`), its own refusal (`op_id_reused`) or, after `assistant_in_doubt` (whose receipt it drops, and which may never
  have reached the phone), a new run's answer at the manager. The phone can't tell which of these it got, so none is taken as final, and the sheet adds that changing the instructions
  or reopening the sheet sends it anyway. A 404 / 405 then says to look first instead of falling back to the plain message, and a plain message that may have gone is not sent again
  by the same ask. On any try, the id and first text are also kept through no connection, a cancelled request and `assistant_offline`, which say nothing about an earlier try and show
  their own words (a cancelled request shows nothing). On a first try, any other answer is final for that id, and a failed load of the item, which sent nothing, says the hub can't be
  reached (a timeout or a bare 5xx) or what went wrong. The hub answers what it kept for that id or runs it again, and the manager's `session.send_once` drops a repeat, so the same
  ask sent again from one sheet, with no other ask handed to the main chat in between, hands the item over once (except a hand-off that started the main conversation and whose record
  the manager lost); another ask or a reopened sheet is a new hand-off, and so is the first ask again after another one went to the main chat. Once it went, they open the main
  conversation on the message as sent under that id (its transcript copy carries it and takes the bubble's place; no bubble when the copy is there already) and read the assistant
  settings again, since the hand-off may have started the main conversation. Like every other inbox request from iOS and Mac, it names no manager, so it goes to the default manager.
  Android (#1384, `RealInboxRepository.handOff`) sends it to the item's manager, refuses it while offline, keeps the operation id of a failed attempt so a retry is the
  same hand-off, and takes an answer without `session` as an error rather than sending again; it then opens the session the answer names.
  Every client falls back to plain chat text when the hub or manager answers 404 / 405. On iOS and Mac the fallback goes
  through the main conversation's outbox when it is listed (a failure shows there with Retry); otherwise it is sent or the conversation started from the sheet, which stays open
  with the error when that fails.
- **Done**: row swipe-left, top-right ✓ in details, or alongside Send / Hand to AI.

## Watch

- Home Needs you = waiting Agents (formerly Waiting for you) + Inbox `action` items (`WatchInboxRow`: source / kind, title, AI summary).
  iPhone's Watch summary carries up to 20 items (`WatchSummary.inbox`); while open, Watch fetches `/api/v1/inbox` directly every 8 seconds. New items vibrate.
- Open `WatchInboxItemView`: summary, latest message; AI reply drafts first and displays it, then Send (`/reply`); Done.
- DEBUG: Watch `-openInboxFirst YES` opens the first item.


### Chat-created tasks and recent Inbox topics (#15557)

For people turning a chat request or screenshot into a task, `task_plan` looks for a recent matching group item when
`inbox_item` is omitted. An explicit validated handoff takes priority. Continuations and `inbox_item: "none"` skip inference.
Main chat, an explicit supervisor assignment and a direction receiving the owner's verified quote use the same path.
Only the title, prompt and manager-verified original request are evidence; generated context and image pixels are not.

The manager considers open, visible action group items that arrived within 24 hours (legacy rows fall back to their
source update time), excluding merged members, mutes and existing task links. The normalized group name must match
as a complete name, together with a sender, full non-root URL or a distinguishing verbatim passage. Group-only matches,
short stock phrases and ambiguous topics yield suggestions rather than a link. URL matching never collapses a domain.

Two sources remain separate. A shared source-local message position is usable only in its account and conversation;
cross-source equivalence needs an individual received message's sender, exact distinguishing text or complete URL,
and timestamps within two minutes. Mere context overlap or a general Related link is not proof. A unique pairwise
verified topic uses a deterministic primary item and existing Related navigation; both reply routes and per-source
processing states survive. Neither planning nor binding files the item or sends a source reply.

The plan stores manager-owned evidence and a content fingerprint, even when a strongly matching item was already
handed to the calling conversation. Start checks freshness, visibility, ownership,
uniqueness and source changes again, including after a restart and card edits. An invalid soft link is removed and a
localized notice is recorded in the conversation; the task can still start. Admission repeats the ownership check
under the Inbox lock, so competing automatic links cannot replace a winner. The existing task/Inbox rollback handles
failed persistence. Explicit handoffs retain their existing admission and legacy-link correction behavior.
