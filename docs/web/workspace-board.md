# Workspace board (#1646)

Off by default. Only after you turn on **Workflow board** under Settings › General › Agents in the web app does the sidebar show an entry, which opens `/workspace-board` (see "Toggle" below). A card binds one real code folder on one computer and can link several Agents in that folder; folders with no running Agent can keep their cards too.

## Usage

The default columns are "To do → In progress → In review → Done". You can add, rename, and move columns left and right. Deleting a column that holds cards requires choosing a receiving column; archived cards move there too.

**Add workspace** at the bottom of a column offers two entry points: pick from existing workspaces, or browse / type an existing folder on an online machine. The manager asks the node that owns the folder to verify it and resolve symlinks. An empty workspace with no trusted folder can appear in the list, but you must pick a real folder before binding it. The same folder on the same machine cannot be bound twice; after archiving, restore the original card instead.

Cards can be renamed, pinned, archived, and restored. Dragging moves a card across columns or reorders it within a column; the menu also offers move up / down and **Move to…**, so touch and keyboard users work through the menu. On narrow screens a column picker switches columns and shows only the selected one. Search matches name, folder, branch, and machine ID; the machine filter only affects what is shown. Manual order is kept by default, with pinned cards at the top of their column; Agent status updates never reorder or move cards.

Click a card to see its folder and linked Agents, open that machine's Files or Git, or use **New agent** to open the existing creation flow prefilled with the machine and folder. Binding, moving to Done, pinning, and archiving only update board metadata: they do not start or stop Agents, and do not run Git, PRs, releases, or delete folders. Starting an Agent requires an explicit submit in the creation flow.

## Organizing several workspaces (#7583)

For people managing several Agent workspaces, the manual view offers **Select** when the manager advertises bulk
editing and this connection has full access. Selection is local to this board, manager and sign-in; it is not saved.
Native checkboxes and **Select all visible** choose cards within the search, computer and archive filters. On narrow
screens, only the chosen column participates. Changing these scopes drops hidden selections. Agent activity does not
change manual placement. The selected count counts cards, not linked Agents or matching folders.

The selection bar offers **Move to**, **Pin / Unpin**, and **Archive / Restore**. These are board metadata edits:
they do not stop Agents, delete folders, or perform Git operations. One request applies all selected changes with one
revision check and one save. The client shows the complete projected result immediately, disables competing edits while
it waits, and restores the last confirmed board if the write fails. A 409 reloads the board and asks the user to retry;
it never silently repeats the operation. Failure keeps the still-visible selection available.

**Undo last change** restores only the fields that the last successful bulk operation changed, including the original
relative order within each column. It is available while the board still has that operation's acknowledged revision.
A later write, permission/capability change or refreshed revision retires it. If another device writes before Undo
reaches the manager, the same revision check rejects Undo and reloads; newer work is not overwritten. Undo survives
an unchanged refresh, but is not persisted across reloads. Single-card actions keep their existing behavior.

The API accepts at most 1,000 distinct existing cards (the board's existing capacity). A malformed entry invalidates
the entire batch. Older managers omit `batch_cards`, so the web keeps their single-card controls without exposing a
bulk action that could not succeed. The automatic view does not expose selection.

The implementation extends the existing manager-owned mutation endpoint instead of looping over single-card writes
(which could partially succeed) or replacing the whole state from the browser (which would give it authority over
bindings). No schema migration or new endpoint is needed. The browser regression runner is
`web/e2e/workspace-board-bulk.mjs`; its fixtures and screenshots use made-up data, not a deployed hub.

## Activity history (#7599)

For people organizing Agent workspaces, **Activity** opens a read-only history of board edits. A workspace's detail
also has an Activity section, limited to revisions that affected that card. Expanding a revision shows all changes in
that update, including other cards edited together; it is labeled accordingly. Historical titles and column names
come from the saved event, not the current board. **Open workspace** is offered only when the card still exists.

One successful save creates one grouped entry, even when it changes many cards. Entries show before/after names,
columns, pin and archive state, column changes and ordering. Order changes describe the relative order of surviving
cards in a column; inserting, removing or moving a different card does not imply that every shifted card was reordered.
The journal does not guess which card was dragged, who acted, or why. It contains no folder paths, machine identities,
Agent messages or previous full-board snapshots. A save that changes no metadata still has an entry for its committed
revision. Activity never runs Agents or offers restoration to an old board state.

The manager stores the board and journal in the same Postgres transaction. A failed write or stale revision leaves
neither half committed. The history survives page reloads and manager restarts. Recording begins after migration
`server/internal/db/migrations/manager/0521_workspace_board_activity.sql`; earlier activity cannot be reconstructed.
The journal retains at most 500 recent revisions and 16 MiB of encoded change details, keeping the newest complete
entry. Metadata and indexes are additionally bounded by those 500 rows; the byte limit is not a total database-disk
measurement. A record removed by retention says it is no longer available when opened.

The first page fetches 20 compact summaries. Each expanded entry initially fetches 30 changes, with **Show more**
for the rest. Closing Activity or collapsing the card's activity section cancels pending requests. No history request
runs before opening, and history does not poll. A board event offers **New activity · Show latest** without replacing
what the user is reading; an explicit refresh loads the latest first page. Failures retain loaded pages and expose a
retry for the failed request. The current credential and default manager own every read, independently of the sidebar's
Assistant selection, and changing either retires old responses.

The capability `activity: true` in board responses enables the entry. `answer` connections can read the history;
extension tokens need `agents.read`. Neither gets a write permission. Older managers keep their existing board UI.
The API is documented in [the protocol](../protocol/protocol.md#workspace-board). Browser acceptance uses
`web/e2e/workspace-board-activity.mjs` and synthetic records; the database tests separately verify actual transaction
rollback, concurrent saves, persistence, filtering and retention against Postgres.

## Who owns the board

The board belongs to the manager (`personad assistant`) (moved from the hub on 2026-11-05; code in `server/internal/assistant/workspace_board*.go`). Cards bind folders on computers, and computers are connected by the manager ([machines.md](../server/machines.md), "Who connects to computers"), so the board, the toggle, and workspace discovery are all done there. Table and settings keys are unchanged, so no migration is needed. The hub only forwards: `/api/v1/workspace-board`, `/api/v1/workspaces`, and `/api/v1/workspace-board/settings` go as-is over the bus `http.request` to the manager (`boardRoutes` in `assistantwire`), carrying the requesting device's role. The `workspace_board` and `workspace_board_settings` events are sent by the manager via `ws.broadcast` and the hub pushes them unchanged to every client; their shape is the same, so clients need no changes.

- **When the manager is away** (the hub just started, the manager is restarting): these endpoints return `503 assistant_offline` ("Can’t reach the assistant right now. Try again later"). The board and snapshots live in Postgres and are not lost. The web app treats it as a read failure and recovers on the next read after the manager returns (the board and discovery are read again every 60 seconds while visible, and when the page comes back to the foreground). Until a board has loaded, the page says so once, in the board's place, with the reason and **Retry**, and keeps its search, filters and views; once one has loaded, a later failure is a line over it.

## Toggle (#2475)

Not everyone needs this feature, so it is off by default and the user turns it on under Settings › General › Agents.

- **Where it is stored**: the `settings` table in Postgres on the hub machine (read and written by the manager), key `workspace_board.settings`, value `{"enabled": bool}`. A hub belongs to one person, so the toggle follows the hub: it is the same across browsers and devices, and a change in another browser syncs instantly via the WS `workspace_board_settings` event. Endpoints are in [the protocol](../protocol/protocol.md#workspace-board).
- **When off**: the sidebar shows no entry. Opening `/workspace-board` directly shows "Workflow board is off" and an **Open settings** button. The web app does not read the board or poll `/api/v1/workspaces` (the board component is not mounted at all). The board and workspace snapshots are not deleted and the endpoints keep working; turning it back on restores everything as it was.
- **Compatibility**: before the toggle shipped, the board was shown to everyone, and people already using it must not lose it after upgrading. When no toggle value has been stored, the manager infers it from "has the board ever been changed": `workspace_board.revision > 0` (any change at all: bound a card, added / renamed / moved a column, and so on) counts as on, otherwise off. It does not go by "does the row exist": opening the page once used to write a default board at revision 0, so counting rows would turn it on for almost everyone who ever clicked in, and the toggle would be meaningless. People who only opened it and changed nothing have no data to lose. The inferred value is not written back; only the user's first flip of the toggle stores an explicit value, which wins from then on (a board turned off does not turn itself back on because it has data).
- **When the switch can't be read**: a hub from before the switch answers 404. It showed the board to everyone, so the web app shows the board (or the update message below, when the board is missing too), and Settings › hub › Agents says the hub's version doesn't have this switch yet and to update the hub, with no switch; "a read-only device can't change this" is only for a device whose role really is read-only (`can_write` false). Any other failure before the hub ever answered (a 500, the hub unreachable) shows why, with **Retry**, on `/workspace-board` and in Settings, instead of loading forever; the next `hello` reads the switch again too. A failure after the hub answered once keeps the value it gave. A `workspace_board_settings` push after such a failure says on or off but not whether this device may change it: the page follows it, Settings shows the switch (not "read-only device"; a change the hub refuses is put back with its reason), and the switch is read again for `can_write`.
- **Native clients**: iOS, Mac, watch, and Android have no board entry today and are unaffected; when they add one, they read the same endpoint.
- **Why not an extension**: the board is a draggable React page, its data is in Postgres, and it depends on the node's `workspaces.list` / `workspace.resolve` RPCs. An extension's declarative `nav_page` cannot carry that.

## Workspaces and status

Workspace discovery is independent of the session list. The node enumerates the spaces its host provides, including Orca worktrees with no terminal and empty spaces for which the host can provide a folder; the manager then adds the current session folders and the bound folders. When the host provides no folder, it does not guess a path from the name. Discovery errors show on the board; an unsupported RPC or a failed enumeration must not be treated as an empty list.

A card's column is a manual workflow state. "Needs you", "Working", and so on come from the linked Agents and are display only. Viewing a session, an Agent exiting, or a machine disconnecting never changes a card's column. Done and Archive are two separate actions.

When a machine is offline, the list uses the last discovery snapshot saved in Postgres and marks it offline and unavailable; the cards stay. When the machine is online but the folder is gone, the card shows the space as temporarily unavailable. Live actions such as Files, Git, and starting an Agent still depend on the machine connection and folder permissions; failures report errors through the existing endpoints.

Card IDs, columns, and order are independent of host space IDs. A workspace's identity is scoped by machine, source, and source ID, so workspaces on different machines with the same name or the same source ID are never merged. Once several connections of a machine are recognized as the same computer, responses use the machine identity that is currently routable and keep the original cards and their workflow; cards that were saved separately before are not silently merged. Web data comes from the current hub (which forwards to its manager); a board from another hub you switch to is not stored in shared local state.

The same workspace list feeds the Persona assistant's `@` menu (docs/assistant/assistant.md "Workspace mentions"), whether or not the board is on.

## Persistence, permissions, and refresh

The board is stored in Postgres in `workspace_board`, workspace snapshots in `workspace_snapshots`. There is no fallback to JSON files or in-memory writes. The migration is `server/internal/db/migrations/manager/0302_workspace_board.sql`. Every change carries a `revision`; the database does an atomic CAS compare-and-increment. A concurrent conflict returns HTTP 409, and the web app re-reads and asks you to retry instead of overwriting the other side's change. On failure it restores the last board the server confirmed.

The `full` role can make changes. `answer` can read the board and the workspace list, and the response has `can_write` false. `answer`'s permissions for Files, Git, starting Agents, and so on are still decided by the existing endpoints' allowlists; reading the board grants none of them.

A successful write pushes a `workspace_board` event. When the toggle is off, the web app neither reads nor polls. When the board is open, the web app reads the data, re-reads the board when a `workspace_board` push carries a newer revision and after a reconnect that did not resume (pushes may have been missed), refreshes the board and the discovery list every 60 seconds while the page is visible (the same pace as the sidebar's workspace catalog, #5168; every board write already pushes, so the timer only catches discovery changes and a manager that came back), and refreshes when it comes back to the foreground; polling stops while hidden. Session status refreshes only affect what is shown inside cards, never the manual order.

## Upgrades and compatibility

When upgrading the hub, apply the Postgres migration above, and deploy the new web app and the node on each computer at the same time. An old node that does not support `workspaces.list` or `workspace.resolve` shows a discovery or binding error; existing session folders still supplement discovery, but binding must ultimately be verified by a new node. An old hub returns 404, or 503 `not_supported` if it does not support board storage, and the web app asks you to update: one message in the board's place (the title says to update the hub, the line under it only why: an older version, or no board storage) with **Open hub settings**, without the board's search, machine filter, Archived, Manual / Auto or the auto view's banner (none of them could change anything) and without Retry; the page keeps its regular reads, so the board appears once the hub is updated. When the manager is away it is also 503, with `assistant_offline` (or any other code, such as a proxy's): that one passes, so the page offers **Retry** and keeps its controls (`boardUnsupported` in `web/src/lib/workspace-board.ts`). Existing native clients keep using the original endpoints.

A Session's optional `workspace_cwd` is the physical folder verified by the node, used to link the board to workspaces; the raw `cwd` keeps its runtime meaning. When an old node lacks this field it falls back to `cwd`; linking through symlink aliases requires upgrading the node. Detailed data structures, errors, and RPCs are in [the protocol](../protocol/protocol.md#workspace-board).

## Origins and boundaries

The interaction model follows the workspace board in [the open-source Orca project](https://github.com/stablyai/orca): manual columns are separate from Agent status, and done is separate from archived. This implementation does not copy Orca's code or assets. Binding plain folders is Persona's adaptation. This phase does not manage creating, deleting, or merging worktrees, does not sync external task status, and includes no autonomous task execution engine.
