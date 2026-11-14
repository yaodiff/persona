# Android bounded Agents lists

The repository supports the opt-in API introduced in server PR #5963. The runtime Agents page requests complete membership until the server can express its full visibility contract (#6108); its counts and rows use the same local projection (#6521). This does not combine runtime sessions with extension personal-agent conversations: Android's existing Agents page consumes `/api/v1/sessions`; `/api/v1/agents/sessions` remains a separate collection.

## Loading and memory

- HTTP list requests use `summary=1&limit=50`. `JListPage` already renders a `LazyColumn`; its existing composite row keys are retained.
- The first page renders before counts arrive. Scrolling near the end requests the next frozen cursor page; the footer also offers an explicit load action for folded/short lists and retries.
- At most ten pages (500 runtime rows per hub) remain loaded. Previously visited pages retain only their cursor strings, not their rows. The earlier-sessions action reloads an evicted page. The disk first-paint cache holds at most 50 rows; an old full-list cache is decoded incrementally.
- A filter change cancels old requests, discards old cursors, and obtains a new baseline. Runtime state, machine and row-search filters go to the server. The transport also supports exact project, group, source and provider filters.
- Counts are independent of the loaded window. Quick-state counts omit `state`; machine-chip counts additionally omit `machine_id`. Visible machine, exact project and machine-local space headings request their own small filtered `counts=1` response, including cross-state counts, without loading more session rows. Hub-group selection is a client routing namespace, never the server's machine-local `group` parameter.

## Revision recovery pitfalls

The scroll cursor is tied to a frozen snapshot. Its revision and the live delta checkpoint must stay separate. A later page from that snapshot can contain a row deleted or edited after page one. Before publishing a newly loaded/reloaded page, stream changes from the original baseline revision and reconcile only those candidates plus the already loaded window. Do not retain an overlay of every changed identity in the collection.

The WebSocket URL negotiates `features=sessions_list`. Its notices only invalidate the HTTP window: their revision is not a row checkpoint. Coalesce notices, follow every bounded delta page, update retained rows, and advance the checkpoint only after the final delta page. While page one is loaded, an unloaded upsert newer than the window's oldest row (any upsert once the whole list is loaded) joins page one, so new sessions, sessions active again and rows newly matching a quick filter appear; a burst of more than one extra page reloads page one instead (#6139). Older unloaded upserts wait for their frozen page. Runtime removal identities are JSON tuples encoded as strings, not slash-delimited paths. An expired cursor or checkpoint reloads page one.

A partial window must never be published as a complete per-machine verified list: notifications and open chats would otherwise interpret unloaded sessions as deletions. Summary fields (`waiting`, background/subagent/workflow counts) replace inference from missing arrays. Open chats retain hydrated detail and fetch their existing single-session `info` route for launcher/model/permission metadata.

## Compatibility and server limitations

An unsupported endpoint, a legacy response lacking the bounded envelope, or a caller explicitly requiring complete membership selects the full-list path. Authentication, network and arbitrary server errors do not trigger full-list fallback.

The server has exact single-state filters and machine-local project/group dimensions. It does not expose client-specific pin/mute ordering, assistant-row exclusion, the combined Running/All/History preference, botmux-versus-terminal origin predicates, custom to-do sections, computed repository trees, or crossed per-group-by-state count maps. The Agents view therefore sets the local `requiresCompleteMembership` policy, and the existing local projection derives range, machine, source and preference membership and counts together from the complete collection. This policy is not a filter parameter and is preserved across hub namespaces. Leaving a complete-membership view allows pagination to be probed again.

How the complete collection is held depends on the hub:

- **A `sessions_list` hub** (its hello, or the connection's last hello before the repository sees one): one baseline of complete pages (`summary=0&limit=200`, no filters, one frozen snapshot; machines read once beside it), then each coalesced hint reads `since=<checkpoint>` with the same query and applies every upsert (new rows join) and removal. A hint naming the revision the checkpoint already holds starts no read. A new baseline is taken only on `revision_expired` (a hub restart), a hello that changes the capability, another hub (another repository) or an explicit refresh; changing the view's own filters keeps it, since they apply locally. Counts are local (`listCounts` stays paged-only), and the first-paint snapshot is written at most every 10 s from hints. `CompleteSessionsFollowTest` pins one baseline, no legacy list, one machines read and one changes-only read per new revision during a minute of hints (#3760; before, each hint re-read `GET /api/v1/sessions` and `GET /api/v1/machines`).
- **Any other hub**: the complete legacy collection (`GET /api/v1/sessions`), with its whole-list WebSocket pushes merged.

Paging remains available for callers whose membership is expressible. The follow-up contract in #6108 must provide matching membership for pages, counts and deltas before Agents can use bounded pages. A paged WebSocket still emits list invalidations during the full-list fallback: those notices refresh the complete HTTP collection, including archive/restore changes; they must not be ignored merely because the HTTP view is in legacy mode. Network and authentication errors propagate without replacing retained sessions.

The single-session `info` contract does not include full background/subagent/workflow/port arrays. Existing hydrated arrays are retained, and list activity uses scalar summaries. Reconstructing complete fresh detail arrays needs a dedicated server detail contract, not a full-list request.

## Status and grouping capsule (#7034)

Audience: people monitoring and collaborating with Agents. The shared home uses the maintainer-approved final E capsule instead of separate status chips and a grouping pill. This is an explicitly requested presentation change, not a new style or preference. Other list entry points retain their existing controls.

`JAgentFilterCapsule` owns the interaction in `core/designsystem`; `AgentFilterTokens` owns its geometry. The capsule has a 42dp visual frame within a 48dp touch row, full circular corners, a horizontally scrolling status region and a fixed trailing three-line `PersonaIcons.filter` separated by a hairline. The icon opens an anchored `JMenu` containing only `offeredGroupings`, with the current choice checked; the former grouping pill and sheet are removed from the home. Status choices still come from `AgentQuickFilter.shown`, with the same conditional availability and count projection. Glass keeps its existing count-free All choice; Brief and Board keep All's actual count.

Unfocused labels show two Unicode characters and an ellipsis. The selected label and the choice nearest the viewport's central focus expand through a width animation; the number stays outside that label width. Initial scroll position expands only the selected choice. Focus resolves after scrolling settles, avoiding a feedback loop from animated item widths. Tapping selects locally and centers the selected choice; Reduce Motion uses final widths without animation. TalkBack reads complete labels and counts even when visually abbreviated, plus selection and the grouping's current value.

`AgentsFilterCapsuleShotsTest` drives the production Agents page through initial, centrally expanded, selected and grouping-menu states in all three styles and both appearances. `AgentFilterCapsuleTest` checks Unicode abbreviation, intermediate animation geometry and Reduce Motion. These are Native JVM Compose renders, not physical-device or transport/performance validation.

## Verification

`PagedSessionsRepositoryTest` covers 8,000 invented rows, bounded first-page/window loading, cursor continuation/backloading, multi-page revision updates, field-only changes, deletions, unloaded-row exclusion, newer rows joining page one and a burst reloading it, frozen-page catch-up, filter changes, expiry, reconnect and legacy fallback. `HttpSessionListsTest` covers the wire URLs/envelopes and errors. `AgentsPaginationScreenTest` drives the real `AgentsScreen` through scrolling, checks that an offscreen row is not composed, checks server filter forwarding and saves all three styles in light and dark using Robolectric native rendering.

`CompleteSessionsFollowTest` covers the `sessions_list` baseline and `since` reads, held hints, local filter changes, expiry, refresh and moving from the legacy list on an upgraded hub's hello. `CompleteSessionsRepositoryTest` covers complete legacy reads of 8,000 rows, archive/restore invalidations, failure retention and returning to pagination. `AgentsVisibilityTest` runs the actual repository, ViewModel and Compose page with an offline Codex history row: Running counts zero, All/History count and retain one, and notices refresh the list. It saves light/dark screenshots.

Native JVM screenshots are layout evidence, not emulator/physical-device launch or frame-time measurements. No deployment is part of this change.

Tests using real Main/list executor threads must `cancelAndJoin` the ViewModel scope before `Dispatchers.resetMain()` and before closing the executors. Calling `cancel()` alone leaves completion callbacks racing the next test's global dispatcher replacement; the error can surface in an unrelated following test. Keep the join timeout-bounded, and retain the background-list-context assertions.
