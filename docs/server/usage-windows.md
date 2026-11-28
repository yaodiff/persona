# Subscription usage windows (#2432)

Subscriptions like Codex and Claude Code have "5-hour" and "weekly" usage windows. This page explains how Persona collects, merges and draws them, and why some sources are not used.
The UI is at the bottom of the web sidebar (`docs/web/web.md` ("Subscription usage")), on the iOS Machines page and at the bottom of the Mac section column ("UI (iOS / Mac)" below). The data comes from the node on each computer, and from account-pool gateways the manager queries (such as relay gateways, see "Account-pool gateways" below, #2595).
Collection, merging and the gateways all belong to the **manager** (the one that connects computers, the assistant process `personad assistant`, see "Who owns this" below); the hub only forwards the API.

## Read-only collection without access to credentials

Persona collects usage on each computer without reading or handling agent login credentials.

| Source | How it is obtained | Used? |
| --- | --- | --- |
| Codex app-server | In a bounded background poll, the node asks the official local Codex app-server for `account/rateLimits/read`. Codex makes the authenticated usage read and can return `accountId` with the same snapshot. Persona sends only a digest of a personal account ID to the usage manager | Preferred |
| Codex rollout | `token_count` events in `~/.codex/sessions/…/rollout-*.jsonl` carry `rate_limits`: `primary` / `secondary` with `used_percent`, `window_minutes`, `resets_at` (seconds), plus `plan_type`, `limit_id`. The node reads the tail (512 KB) of the most recent few rollouts and takes the last event whose `limit_id` is `codex` (or unset). This sample identifies its reporting source only | Fallback |
| Claude Code stream (#12855) | After a model reply, Claude Code's stream-json output carries a `rate_limit_event` whose `rate_limit_info.unifiedWindows` holds `five_hour` / `seven_day`, each with `utilization` (a fraction) and `resetsAt` (seconds). The node reads it in the Claude assistant conversations it runs (`readAssistant`) and keeps the newest per launcher and config directory choice; an event without `unifiedWindows` (an API key or a gateway) adds nothing | Yes, for the conversations Persona runs: no setup, no extra process or request |
| Claude Code CLI (`get_usage` / `/usage`, #12412) | In a bounded background poll (`usagewindows_claude.go`), the node invokes each local Claude Code config directory through the computer's built-in `claude` launcher in the login shell (matching shell functions, aliases, and PATH wrappers), reading subscription windows via the stream-json `get_usage` control request (falling back to `claude -p /usage --output-format json --verbose` when `get_usage` is unsupported or returns no windows). No login credentials are read and no model request is started; after the first poll, each directory is re-read only after Claude Code activity or a window reset (at most once every 5 minutes). Complements the stream source for computers without active Persona conversations and also supplies plan names and per-model windows | Preferred (complements stream) |
| Claude Code status line | Claude Code puts `rate_limits` (`five_hour` / `seven_day`, each with `used_percentage`, `resets_at`) in the **JSON input of the status line command** (see its changelog), and writes it to neither the transcript nor any other file. A wrapper dropping it into `~/.persona/usage/claude*.json` still works and wins over the CLI probe when fresh (within 15 minutes) | Optional override |
| Antigravity native quota summary | The agy companion asks a local ACP worker for `RetrieveUserQuotaSummary` with `forceRefresh: false`, so agy's own cache controls upstream reads. It can start a worker on demand for a running or recently updated native conversation, including in a terminal. See "Antigravity terminal activity" below. Valid buckets and groups become usage windows using `remainingFraction`, `window`, `resetTime` and the optional model. `window` is read as a duration (`18000s`), a span (`5h`, `7d`) or a name (`daily`, `weekly`); a bucket whose `window` is none of these falls back to its bucket id (`…-5h`, `…-weekly`). agy 1.5.14 sends names (`5h`, `weekly`), checked against a real agy on Linux on 2026-11-22 (#12910). On the same live worker it also makes a best-effort read-only `GetUserStatus` (`{"metadata":{}}`) call to populate the signed-in account name (`userStatus.email`, falling back to `userStatus.name` and then `"Antigravity"`) and subscription plan (`userStatus.userTier.name`, falling back to `planInfo.planName` or `userTier.id`), checked against a real agy 1.5.18 on Linux on 2026-11-26 (#16277); failures degrade to `"Antigravity"` without blocking quota publication. No conversation or model request is started, and no OAuth token or credential file is read | Yes |
| Codex account usage API (ChatGPT's `wham/usage`) | Needs the login token in `~/.codex/auth.json` | **No**. Moshi uses it; Persona does not read `auth.json` |
| Claude account usage API (`/api/oauth/usage`) and the `anthropic-ratelimit-*` response headers | Needs Claude's OAuth token (Keychain / `.credentials.json`); the headers also need a request of your own | **No**, same reason |
| Others (Kimi, Grok, GLM / MiniMax API key plans) | All need an account API or a key | Not doing |

The app-server starts no conversation or model request. The account ID applies only to the usage returned in that response, so a saved rollout from a previous login is never assigned to the current account.

### Antigravity terminal activity

The companion checks its session list on hub connect and during regular inventory updates. A running native conversation or an update in the past 20 minutes can trigger a quota read if the last sample is at least five minutes old or missing (#16118). The read reuses a live ACP worker or starts one on demand, backs off for five minutes on failure, and leaves the worker's idle timer alone. Without recent activity or a live worker, quota collection leaves the computer idle (#15253).

### Wiring in Claude Code's usage (optional)

By default, Persona reads Claude Code's subscription windows from two built-in sources without touching credentials or starting a model request: `rate_limit_event` on every reply in conversations Persona runs itself (`usagewindows_claudestream.go`, #12855), and a background CLI probe through the computer's built-in `claude` launcher in the login shell (`usagewindows_claude.go`, #12412) that starts a short-lived non-persistent process with hooks and settings sources disabled, asks the structured `get_usage` control request (falling back to `claude -p /usage --output-format json --verbose` when `get_usage` is unsupported or returns no windows), and polls each Claude config directory (`CLAUDE_CONFIG_DIR`) once after startup and then at most every 5 minutes when Claude Code has activity in that directory or a window resets.

If you prefer to feed Claude's usage from a status line hook (or run an older CLI that has neither `rate_limit_event`, `get_usage`, nor `-p /usage`), **Persona never edits your `settings.json`**; you can still wrap your status line command so it drops `rate_limits` into the node's directory `~/.persona/usage/claude.json` (for multiple config directories use `claude-<name>.json`; each file counts as one source, and a fresh file within 15 minutes wins over the CLI probe for the same account):

```sh
#!/bin/sh
# Status line command wrapper: save rate_limits for Persona, then pass the input unchanged to the original status line command
payload=$(cat)
dir="$HOME/.persona/usage"
mkdir -p "$dir" && chmod 700 "$dir"
printf '%s' "$payload" | jq -ec 'select(.rate_limits) | {rate_limits}' > "$dir/claude.json.tmp" 2>/dev/null \
  && mv "$dir/claude.json.tmp" "$dir/claude.json"
printf '%s' "$payload" | <your original status line command>
```

The file keeps only `rate_limits` (no session, directory or cost). The node uses its modification time as the sample time; `resets_at` is read as seconds, and milliseconds and RFC 3339 strings are accepted too.
The gateway's `rate_limits.spend_limit` (an amount of money) is not a window and is not read.

> Not verified: the status line field format comes from Claude Code's changelog ("5-hour and 7-day windows with `used_percentage` and `resets_at`"),
> and the unit tests use samples written from it; the unit of `resets_at` has not been checked against real status line input from a Claude session.

## Collection and merging

- **node** (`server/internal/node/usagewindows.go`, `usagewindows_codex.go`, `usagewindows_claude.go`, `usagewindows_claudestream.go`): refreshes every 5 minutes (the first time 20 seconds after connecting to the hub, so the hub's `node.configure` delivers the switch first).
  Each source (one Codex home, one Claude config directory, one `claude*.json`, one local runtime id, or the Claude conversations of one launcher and config directory choice) keeps its last sample, kept even when the source goes quiet, and dropped after 3 days. Windows whose `resets_at` has passed are dropped (those numbers belong to the previous period);
  a source whose windows have all passed is not listed. A local runtime reports through the optional `usage.windows` event when the node advertises `usage_windows`; its runtime id replaces the same source after a companion restart, and a valid empty snapshot clears it. `usage.windows` uses the cache or local files without waiting for the Codex app-server or Claude CLI, and reconciles same-account Claude CLI readings with fresh status-line drops and stream samples.
- **Off**: `machines[].usage_windows` in `hub.json` (`docs/operations/config.md`); with `false`, that computer's node stops collection and the manager does not ask it. Unset means on.
  If the node is older than this version (no `usage.windows`), the manager treats it as having no data.
- **Manager** (`server/internal/devices/usagewindows.go`): on `GET /api/v1/usage/windows` it asks the online machines (in parallel, 8 seconds each; repeated requests within 30 seconds reuse the previous merged result; a result up to 3 minutes old is answered at once and refreshed in the background, so the apps' once-a-minute poll does not wait for the machines; the scheduler's capacity view still asks when it expires),
  remembers each machine's last answer (in memory only, forgotten after 3 days or when the machine is removed from hub.json), then merges:
  - **Codex**: samples with the same upstream personal account ID form one row, with the newest sample's windows and the names of the computers that reported it. Without a personal account ID, each source gets its own row with its source label. A shared workspace ID does not identify a member's allowance. A reset time describes a window and plays no part in Codex account grouping.
  - **Antigravity** (#13964, #14585) uses the provider ID `antigravity` for both the companion's `agy` and external sources. The manager first pairs local and external rows by model scope, window length and exact reset second. Each side must match exactly one row on the other side within that scope.
    A local window without a reset can then fill a missing duration or combine with an external window that also lacks a reset. This requires a known scope whose comparable timed windows agree. A full window keeps its 100% remaining and has no countdown. Gemini and Claude/GPT match independently. Ambiguous seats and conflicting windows stay separate. The display keeps external row identity and window metadata, prefers fresh readings, and uses the external value on a tie. The gateway poll cache keeps its original windows.
  - **Other providers**: samples for one provider can merge when they have a plan window with the same length and the same reset second. Each window length takes the newest sample with that window.
  - **Prepaid balances** (#11825): an official DeepSeek launcher reports money left, not an allowance window. One row per launcher key carries `balance` instead of `windows` (below); it takes no place among the two fullest window bars, and the popover lists it beside them.
  - Samples older than 30 minutes are marked `stale` (sorted last, faded in the UI and labeled "out of date"); windows older than 3 days or past their reset time are dropped.
    Each account also says when it turns stale if nothing newer comes, `stale_at` (the newest sample's time plus its source's threshold: 30 minutes for a computer, 6 hours for a gateway). A client keeping an answer while refreshes fail judges it again with `stale || now >= stale_at`, so old numbers fade and read "out of date" however long ago it last heard from the hub, and no client hard-codes the thresholds (#1905).
  - **Every row has its own id** (clients use it as the row key, #1904). Codex row IDs are derived from the account or reporting source and stay stable across resets. Other providers use a digest of the longest plan window with the gateway reference or reporting source where needed. The node's grouping keys and the raw upstream ID stay out of the client response.
  - Known accounts with the same name are numbered by id. Codex reports without a verified account use the existing source labels and are numbered only when the name and visible sources are identical.
  - Sorted by the fullest window, highest first.

API (read-only, not usable with extension tokens, `testdata/route_scopes.txt`):

```json
{"accounts": [{
  "id": "codex-1a2b3c4d", "provider": "codex", "name": "Codex Pro", "plan": "pro",
  "sampled_at": "2026-11-05T09:08:06Z", "stale": false, "stale_at": "2026-11-05T09:38:06Z",
  "local": true, "machines": ["MacBook"],
  "windows": [{"label": "5h", "window_minutes": 300, "used_percent": 22.0, "resets_at": "2026-11-05T13:08:06Z"}]
}, {
  "id": "deepseek-7f2a1c3d", "provider": "deepseek", "name": "DeepSeek",
  "sampled_at": "2026-11-05T09:08:06Z", "stale": false, "stale_at": "2026-11-05T09:38:06Z",
  "windows": [], "balance": {"amount": "12.34", "currency": "CNY"}
}], "checked_at": "2026-11-05T09:08:20Z"}
```

A window from a usage extension may also carry the gateway's protection line:

```json
{"label": "7d", "window_minutes": 10080, "used_percent": 55.0, "resets_at": "2026-11-27T09:00:00Z", "protect_percent": 99.0}
```

A separate Codex reading from a local rollout file carries `"reading_source": "rollout"`. When it is stale, Web Usage keeps its machine, sample time and quota values available as source details and excludes it from the account count and reset timeline. Existing matches between local readings and extension accounts retain their merged account row. Older nodes and managers omit the field. A live team seat may also lack a personal account key, so a missing key alone does not identify a file reading.

`local` says the numbers were read on one of the computers. `machines` names those computers (their names in hub.json now, #1907), and `sources` names the account-pool gateways that reported it (below).

With no data, `accounts` is `[]`; old hubs lack this route (404), and the web treats that as no data.

`hidden_providers` (#13179) is added to the answer, as in `"hidden_providers": ["claude", "codex"]`, when the owner hid providers (`usage.hidden_providers`, "Configuration" below): their lowercase names, left out when none is hidden, so the answer is what it was before. It lets a client leave those providers out of everything else it draws for the bars: the web's "not connected" rows (a model-switcher launcher with no usage account, such as "Claude Code") are listed only for providers that are not hidden. Old clients ignore the field; an old hub never sends it, and the web then hides nothing extra.

## Account-pool gateways (relay…, #2595)

An account-pool gateway maintains several Codex / Claude / other subscription accounts for you, and **already knows how much of each account's 5-hour and weekly quota is used** (for example, a relay stores the response headers that upstream returns with ordinary requests).
There is no need for every computer to query these numbers again: the manager asks the gateway, reads the numbers it has stored, and merges them into the sidebar. Accounts the computers cannot see (kept in the gateway, not logged in locally) show up too.

### Why it is a built-in source, not an extension

The principle in `docs/extensions/extensions.md` is "the open-source repository holds only generic capabilities and extension points; anything that only makes sense for one company or one service becomes an extension". The trade-offs here:

- **The existing `quota` extension point does not fit**: it answers "how much budget does this launcher key have left in the gateway", using the launcher's key and returning `quota.Report`; what we need here is the subscription windows of the **upstream accounts the gateway maintains**, using the gateway's admin key, a completely different shape.
- **A new extension point needs the whole set**: manifest fields, schema-generated types on four platforms, permissions, a process host, a package in the market. The gateway needs only "one address + one key, read every 5 minutes, map the numbers to windows", a few hundred lines.
- So: **a source is a very small concept** (`usage.sources[]` in the manager's `manager.json`, one `type` per source), with three built-in adapters (in the manager):
  - `generic`: **a generic format defined by Persona itself**; any account-pool gateway that implements this one GET can plug in (below).
  - `relay`: **the admin API of an account-pool relay gateway**, with two read-only endpoints ("What is read" below); any relay implementing them can connect, and the adapter just maps them to the same shape.
    Older files used the name of the first connected relay as the type; they remain readable (every type other than these three is treated as `relay`), and editing the source in the admin UI writes `relay` next time.
  - `relay_key` (#12311): **the same relay's key self-service usage**, one read-only GET with an **ordinary API key** (the kind a launcher uses) instead of a management key. It reports the windows of the accounts that key can use, in the vocabulary `relay` already maps, so it is the same relay family read with less privilege, not a third gateway shape.
- If a third or fourth gateway with a different shape really shows up, the adapters get pulled out into an extension point then (`usagesource.Fetch` is the single entry, so nothing else changes). **This is a trade-off, not dogma**: the core `relay` adapter is named for its API shape, not a specific service, and works out of the box without installing an extension.

### Usage extensions (`type: "extension"`, #12402)

A gateway whose answer fits none of the three adapters is read by an extension that contributes `usage` (`docs/extensions/extensions.md` ("Usage extensions")): the manager asks it (`usage.query`), it reads the gateway through `usage.http` with the launcher's key (which stays with whoever holds it), and answers with accounts and windows. The source names the extension and the launcher:

```json
{"id": "src-3a4b5c6d", "name": "Pool", "type": "extension", "extension": "example-usage", "launcher": "pool"}
```

It is written in `manager.json` (the admin page lists it and can rename, switch and remove it, not create it). Its rows are shown as the extension sent them and are never merged with the computers' accounts. Besides the usual fields a window may carry `group`, `title` and `note`, and `protect_percent` (below), and an account `headline` and `notice` (plain text in the user's language): windows next to each other with the same `group` belong together, an account with a `notice` and no window says why there is nothing to draw. All are optional; without them a client draws the flat windows as before.

`protect_percent` (0 to 100, #13173) is the used percentage at which the gateway stops sending work to that account for that window (its quota protection line). It can differ per account and per window, so an extension sends the value that applies to each window it lists. A number outside 0 to 100, or one that is not a number, is dropped (never clamped into a value); a window without it simply has no line. The hub carries it unchanged in `GET /api/v1/usage/windows` as `protect_percent` on the window, only for rows an extension sent; windows read by the computers or by the built-in gateway adapters never have it. Clients draw a marker on the window's bar at that percentage and ignore the field when it is absent.

### Configuration

The manager's `manager.json` (the built-in manager's `~/.persona/managers/assistant/manager.json`, `docs/operations/config.md` ("manager.json")); the key lives only here (0600), never in the API, logs or the bus. This section belongs to the manager: it reads and writes it beside the extension sections, under a lock on the file's directory (`managerconf.Edit`), so two writes never overwrite each other:


```json
"usage": {
  "poll_minutes": 5,
  "sources": [
    {"id": "src-1a2b3c4d", "name": "Relay", "type": "relay", "url": "https://gateway.example.com", "key": "<admin key>"},
    {"id": "src-9c0d1e2f", "name": "Shared relay", "type": "relay_key", "url": "https://relay.example.com/v1", "key": "<API key>"},
    {"id": "src-5e6f7a8b", "name": "Team pool", "type": "generic", "url": "https://pool.example.com/persona/usage", "enabled": false}
  ]
}
```

| Key | Meaning |
| --- | --- |
| `usage.enabled` | `false` is the master switch: no gateway is queried any more (what computers read themselves is unaffected). Unset means on |
| `usage.poll_minutes` | How often each source is read, default 5, 1 to 1440 |
| `usage.hidden_providers` | Providers the usage bars leave out, such as `["codex"]` (#11753). **Display only**: `GET /api/v1/usage/windows` drops their accounts and names them in `hidden_providers`, so the web strip's "not connected" rows leave them out too, while nodes and gateways are still read and the scheduler's capacity view (below) still counts them. Names are lowercase (`^[a-z0-9][a-z0-9_-]{0,31}$`, at most 16). `agy` and `antigravity` both select Antigravity. Set on the admin Usage sources page ("Providers in the usage bars"), one switch per provider (the usual two, what the bars last showed, and anything already hidden) |
| `usage.sources[]` | At most 8. `id` is assigned by the manager (`src-…`, generated by the admin UI), `name` is the source name in the sidebar (at most 40 characters), `type` is `relay` / `relay_key` / `generic` (or `extension`, "Usage extensions"), `url` is an http(s) address (no username or password in it), `key` is sent as `Authorization: Bearer` (required for both relay types: a management key for `relay`, an ordinary API key for `relay_key`), `enabled: false` disables this one |

The admin UI (`/admin/usage`, `docs/operations/admin.md`) adds, edits, deletes, toggles and has "Test connection"; changes take effect immediately with no restart. If you edit `manager.json` by hand, the manager adopts the version on disk the next time this page is opened.

### What is read and how it maps

**Read-only, never touching upstream**:

- `relay`: `GET /api/admin/accounts?view=compact&limit=500` (the account list: `id`, `name`, `provider`, `status`, `schedulable`, `cooldown_until`, `last_error`, `quota_summary`, **no email, credentials or proxy address**;
  when the answer carries `next_after_id` / `next_after_priority`, the next page is read with `after_id` / `after_priority`, up to 2000 accounts; older gateways that ignore `view` / `limit` and return the whole list or a bare array are accepted too, taking only these fields),
  then `POST /api/admin/accounts/usage {"ids": […], "refresh": false}` for accounts that are not disabled (at most 100 per call, 500 accounts in total). One read round yields both the sidebar samples and the "gateway's account list" below; the list is not read separately. `refresh` is hard-coded to `false`: the gateway only reads its database and never asks upstream
  (`refresh=true` would make the gateway probe upstream from its single egress address, using up the whole pool's probe quota). No other route is called, let alone any write route.
- `relay_key`: `GET /me/usage` with the API key, and nothing else: no query parameters (the relay refuses ones it does not know; its optional time zone only shapes the key's own request counts, which are not read), no write route. A trailing `/v1` on the address is dropped, so the base URL a launcher uses can be given as it is; a path prefix in front of it is kept.
  The answer lists the accounts that key can use (`account_id`, `provider`, `plan`, `plan_raw`, `state`, `windows`, `checked_at`), with the same window fields as the usage call above; the relay answers from its stored snapshot (it caches the document for about 30 seconds per key) and never asks upstream for it. The document carries **no account names, emails or credentials**; the key's own requests, tokens and costs in it are not read, nor are the seats behind another relay (`upstream`).
  `account_quota_shared: false` means the relay shares no account quota with this key (its accounts are then an empty list): that is a successful read with nothing to draw, and **Test connection** says so instead of "0 accounts". A missing field means shared (an older relay). An answer without an `accounts` list is `bad_response`; a relay without the route gives `not_found` ("This address has no usage document for API keys…").
  Such a source has no account list for extensions (below): a key cannot see the whole pool.
- **Prefer a group key**: relay group admin keys (for example `onboarder` / `operator` groups) only see their group's accounts (including `/accounts/usage`, see that relay's access-control docs) and cannot read other groups; far narrower than a deployment key.
  If an account is moved to another group / deleted between the two calls and the usage endpoint returns 404, this round fails (the UI says "This address is not the gateway's management API, or the gateway is too old" and the next round retries).
- Timeouts (15 seconds per request, 45 seconds per source per round), answers capped at 8 MB, no redirects followed (the key is never carried to another address), errors keep only a code, never the gateway's text, which also never goes into logs (the log writes one line `usage source <id>: <code>` only when the code changes).

Mapping (`server/internal/usagesource`):

| Gateway | Sidebar |
| --- | --- |
| `provider` (`codex`, `claude`, `kimi`…) | The same. Codex gateway entries use the gateway's source label because these adapters report gateway-local IDs |
| Account `name` | Account name (a name someone gave it in the gateway, easier to recognize than "Codex Pro" from a computer; the gateway's name wins when merging). `relay_key` has none: an account is named after its provider and plan, as the computers name theirs ("Claude Max", "Codex Pro"), and same-name accounts are numbered ("Claude Max 2") |
| `plan_display_name` (or `plan` if missing) | Plan, written next to the name (not repeated if the name already contains it) |
| Window `kind` | Window length: `five_hour` / `5h` → 5h, `weekly` / `7d` → 7d, `hourly`, `daily`, `monthly` (30 days), `<n>m|h|d|w`; kinds like `weekly_fable`, `gemini_argon_weekly` that use the length as one of their words are recognized too. **Kinds with no readable length (`credits`, `blocked`…) are not time windows and are not drawn** |
| `scope: "model"` / `scoped_model` | Per-model quota: the model name is shown next to the window of the same length ("7d Fable"); it **does not affect account sorting and is not used to identify the same account** (one model being used up does not mean the account is) |
| `used_ratio` (0–1) | Percent used, rounded to 1e-9 so 0.29 reads as exactly 29 (without `used_ratio` but with `remaining` and `limit`, computed from those; windows with neither are not drawn) |
| `resets_at` | Reset time |
| Window `checked_at` (the newest, or the entry's if missing) | Sample time |
| `unsupported`, an account with no drawable window, `status: disabled` | Not listed |
| `stale`, `error` | Marked out of date; accounts with `error` show "The gateway couldn't read this account just now; these are the numbers it had stored" in the popover (the error text is not shown) |

Relay numbers come from response headers returned with ordinary requests, so **they stop updating while an account sits idle**, and its samples age more slowly than a computer's: they are marked out of date only after 6 hours (30 minutes for computer samples), and the sample time shown in the UI is the numbers' real age.
The values of each `kind` have not been checked against a real relay: the mapping table follows the first connected relay's public window names, and unit tests use samples written from its JSON fields (see "Limitations").

### Merging with what computers read

The manager shows gateway accounts alongside each computer's samples using "Collection and merging" above.

- Codex gateway entries stay separate from computer samples. The gateway-local account ID identifies a source entry and cannot identify the upstream Codex account across sources.
- For other providers, two different accounts in the same gateway stay separate even when their reset times match.
- A merged account takes the newest sample's numbers and lists its sources: the computers that read it, by name (`machines`), and the gateway's name; the line under the popover title reads "MacBook · Relay"; with only one source, just that one. Never a word like "this computer": the clients run on phones and other computers, where it would point at the wrong device (#1907). Against an older hub that only sends `local`, every client says "Computer" (zh-Hans 电脑, ja コンピューター) instead.
- When a gateway's **read fails, its last answer is kept and marked out of date**, so accounts do not disappear; dropped after 3 days. `problem` says why, from the source's error code, and the popover words it (the admin page's "Usage sources" gives the exact reason):
  - `rejected` (`unauthorized`, `forbidden`: the gateway turned the key away): "The gateway turned the key away; update it in Admin → Usage sources. These are its last numbers";
  - `unreachable` (`unreachable`, `timeout`): "The gateway can't be reached; these are its last numbers";
  - `failed` (any other code: `not_found`, `server_error`, `bad_response`, `too_large`, `redirect`, `bad_config`): "Couldn't read the gateway; these are its last numbers".

  The source failing replaces a relay's own per-account `error` (it is the newer news). Clients show no text for a value they don't know, but still fade the account.
- Sorting is unchanged: by the fullest of the account's plan windows, highest first, out-of-date ones last. With no data at all (and no sources), the whole block is not drawn.

### The gateway's account list (#4576)

The sidebar's usage bars only draw accounts with time windows; extensions (for example, an account page in a relay usage extension's sidebar) want **every account in the gateway**: disabled, quarantined, needing re-login, Kiro-credits-only, all of them.
The same read round (above) keeps a list on the side, stored in the manager's memory; extensions read it with the hub method `usage.gateway_accounts {}` (permission `usage_gateways`, `docs/extensions/extensions.md` ("Usage")). It only returns what was last read and **never queries the gateway for it**.

```json
{"sources": [{"id": "src-…", "name": "Relay", "type": "relay", "checked_at": "…", "error": "", "listed": 71, "complete": true}],
 "accounts": [{"source": "src-…", "account_id": "<gateway's id>", "name": "", "provider": "kiro", "plan": "free", "plan_raw": "", "plan_display_name": "",
   "status": "active", "schedulable": true, "reason": "", "cooldown_until": null,
   "windows": [{"kind": "monthly", "scope": "account", "scoped_model": "", "used_ratio": 0.18, "limit": 50, "remaining": 41, "resets_at": "…"}],
   "checked_at": "…", "cached": true, "stale": false}]}
```

- Only enabled sources of type `relay` are listed (a `relay_key` source reads one key's accounts, not the pool's); sources not read yet (just added, or the hub just restarted or switched and memory is empty) are listed too, with `checked_at` `null`, no accounts and `error` `pending`: that means "not known yet", not "the gateway has no accounts", and consumers should treat it as no data (the usage extension then falls back to reading each launcher). With no sources it returns `{"sources": [], "accounts": []}`.
- `status` is the relay's as-is (`active`, `cooldown`, `exhausted`, `refresh_required`, `quarantined`, `expired`, `disabled`); when the gateway does not say, `schedulable` is treated as `true`.
- `reason` is the part of `last_error` before the first `:`, kept **only when it is a short word matching `^[a-z_]{1,32}$`** (`quarantine`, `sign_in_required`, cooldown reasons); the text after it may be upstream's own words (addresses, messages containing accounts) and is neither forwarded nor logged.
- **Windows are passed through as-is, whatever the `kind`**: `five_hour`, `weekly`, `monthly`, `kiro_bonus`, `kiro_free_trial`, `kiro_overage` (no numbers when uncapped), per-model windows (`scope: "model"`)… Missing numbers are `null`, never 0.
- **Disabled accounts are included too**: the usage endpoint does not read them, so their windows come from `quota_summary` in the account list (`kind`, `scoped_model`, `used_ratio`, `resets_at`), marked `stale: true`. Accounts beyond 500 that the usage endpoint did not read are handled the same way.
- `error` is only a `usagesource` error code; when the last read failed, the list is kept with every account marked `stale`. When a source changes its address or key, the list is cleared until the next round.
- `listed` is the number of distinct account ids read this round (an account appearing on two pages counts once), matching the number of this source's entries in `accounts`.
- At most 2000 accounts; when the limit is hit, the gateway returns a page cursor already used, or a page after the first fails, `complete` is `false` (a failure on the first page fails the whole round).
- The sidebar samples (`GET /api/v1/usage/windows`) are as before: disabled accounts and those with no drawable window are still not listed, and length-less kinds like `kiro_bonus` are still not drawn.

### Generic format (`type: "generic"`)

Any account-pool gateway that wants to feed usage into the sidebar implements one GET: the manager reads this full address with `Authorization: Bearer <key>` (omitted if no key is set) and expects JSON back (the same as the answer of `GET /api/v1/usage/windows`, so one Persona hub can feed another):

```json
{"accounts": [{
  "id": "acct-1",
  "provider": "codex",
  "name": "Team seat 3", "plan": "Pro",
  "sampled_at": "2026-11-05T09:08:06Z",
  "windows": [{"label": "5h", "window_minutes": 300, "used_percent": 22.0, "resets_at": "2026-11-05T13:08:06Z", "model": ""}]
}]}
```

`id` is optional (a stable identifier for the account in the gateway, used to guarantee that two accounts are never merged); `label` (`5h`, `7d`, `45m`) is used if its length can be read, otherwise `window_minutes`; `used_percent` is 0–100, kept as sent and clamped when out of range;
`sampled_at` defaults to the time of reading; each source has at most 500 accounts and each account at most 32 windows; accounts with an empty `provider` are dropped.

### Scheduler capacity view

Besides the sidebar, the same in-memory data feeds Agent scheduling: `devices.CapacitySnapshot` builds a `proto.CapacitySnapshot` of one `ProviderCapacity` per upstream provider
(known account count, schedulable count, earliest recovery, all-stale flag) from the gateway inventory and the merged windows. A gateway account counts once, from the inventory
(it is authoritative for the accounts it lists; a sidebar account with a gateway source is skipped); accounts only the computers saw fill the rest, an exhausted plan window making
one unavailable until its reset. It is read-only like everything here — the call performs no gateway or machine read beyond the windows' own short poll cache — and carries no
credential or gateway-side account id. How the launcher picker consumes it is in `docs/server/launcher-health.md` ("Capacity-aware notes").


## Prepaid balances (#11825)

DeepSeek's official API is prepaid: an account has a balance, with no 5-hour or weekly allowance window to draw. Its read-only `GET /user/balance` answers the remaining amount and its currency. `amount` is the provider's own decimal string, kept verbatim, and is never converted into a percentage.

- **Who reads it**: the manager, which already holds the launcher's key sealed (a launcher added in Persona; `docs/integrations/launchers.md`). It asks once every 5 minutes, concurrently with a bounded worker count under a 20-second overall deadline (10 seconds per launcher; a read cut short or not attempted because the overall deadline ended keeps its previous state instead of being reported as unreachable), and only when the launcher's base URL is one of the official DeepSeek bases (`officialDeepSeekURL`: HTTPS, `api.deepseek.com` with no user info, query, fragment or extra path). A gateway, another host or a lookalike is never sent the key there. A launcher found in a shell rc file keeps its key on that computer, so it is not read.
- **Key handling**: the sealed key is opened with the manager's `secret.key`, and a reference (`op://…`) resolves through its provider extension. Per-computer keys take precedence over the launcher's global key, matching `keySealedFor`: when `via_machine` is set, that computer's effective key (its per-computer override, falling back to the global key) is used; otherwise the manager tries in-scope per-computer keys in machine-name order first and falls back to the global key, using the first that opens so a broken key on one computer does not hide a usable key on another. An open or resolution failure happens before any request and is a local credential problem: it reports `problem: "failed"`, never `rejected`, which is reserved for the provider's HTTP 401/403. The read follows the launcher's own route like every other call — through a configured proxy (its `no_proxy` bypass list honored) or through the computer its `via_machine` names — with redirects never followed. The key goes only into the request's `Authorization` (and `x-api-key`, which the launcher also uses) at the official origin; no API answer, log line or stored sample holds it.
- **One row per key**: two launchers with the same key are one account. That holds for a key that cannot be opened too: two launchers sharing one broken key report one "Balance unavailable" row, not two. The row's `provider` is `deepseek`, its `name` is the launcher's display name (or its name), and its `id` is derived from the launcher, so it stays stable while the amount moves.
- **Success, zero and failure**: a successful read replaces `balance` and its `sampled_at`. A genuine zero is `"amount": "0.00"` with no `problem`: the account is out of prepaid credit, which the panel shows as an amount in the caution colour. A failed read keeps the last amount with `problem` (`rejected`, `unreachable` or `failed`) and `stale`, and says the amount is the last known one; a launcher whose first read failed has no `balance`, so the strip shows `—` and the popover says "Balance unavailable" and why, rather than zero. A replaced key (including a rotated secret reference, or another computer's key resolving to a different key) makes the kept amount belong to another credential, so it is dropped instead of being shown as the new key's last known balance. A machine-key store that cannot be read is not an empty one: the manager answers that it does not know, and the caller keeps its samples. A failed balance never hides a window account. When the provider lists more than one currency, the first valid one is shown.
- **Coexistence**: a balance row has `windows: []` and is merged like any other account, but it is not one of the two fullest window accounts the collapsed strip draws. The strip shows it as name + amount/currency (for example `CNY 12.34`), and the popover lists it with the window accounts. A balance is money, not rate-limit capacity: balance rows are left out of the scheduler's `CapacitySnapshot`, so a zero balance or a failed read never counts as an available account.

## Who owns this (2026-11-05)

User decision: the hub is only a communication center, and computers belong to the manager (`docs/server/machines.md` ("Who connects to computers")), so subscription usage moved to the manager as-is, with no design change; the routes, fields and errors the App sees are unchanged.

| Part | Code |
| --- | --- |
| Manager: asks each node for `usage.windows`, merges accounts, reads account-pool gateways (it holds the keys), keeps the gateway's account list (`devices.usage_gateway_accounts`; the extensions' `usage.gateway_accounts` is served by `extmgr/extension_usage.go` asking it in the same process), the API for the admin UI's "Subscription usage sources" page | `server/internal/devices/usagewindows.go`, `usagegateways.go`, `admin_usage.go` (moved from the hub as-is, receiver changed from `Hub` to `Devices`); edits to `manager.json` go through `managerconf.Edit` (`server/internal/managerconf`, under a lock on its directory) |
| Route tables shared by both sides | `server/internal/devicewire/` (`UsageRoutes`, `AdminUsageRoutes`), the admin UI marker `assistantwire.AdminHeader` |
| hub: forwarding | `server/internal/hub/devices_routes.go` (`usageRoutesRelayed`, `adminUsageRoutesRelayed`) |

- The hub forwards `GET /api/v1/usage/windows` as an `http.request`, like the manager's other routes (still not usable with extension tokens, `testdata/route_scopes.txt`).
- `/api/v1/admin/usage-sources…` is forwarded only from the hub's admin UI route group: the hub first checks the admin session (plus CSRF for writes), then adds `X-Persona-Admin: 1` (the header is always stripped if a client sends it); the manager returns 403 without this marker, and the App's API has none of these routes.
- The gateway's key exists only in `manager.json` (0600) and in the one request header sent to the gateway: the hub does not read this section, the key never crosses the bus, and it is in no answer or log. When the manager runs on another machine (`assistant.external`) it uses its `manager.json` there, like the extension sections.
- **When the manager is away**: these routes all return `503 assistant_offline` "助手暂时连不上，稍后再试" ("The assistant can't be reached right now; try again later") (every client keeps its last answer and ages it by `stale_at`, see "Collection and merging"; only a 404 from an older hub without the route counts as no data); once it is back, the first request asks each computer again, and the gateways finish their first round within seconds.

## UI (web)

- **Bottom of the sidebar**: one thin bar each for the two accounts with the least headroom (`h = r − R`, below), each filled to what is left and labelled "N% left" in its `remainingTone` colour; out-of-date ones fade; the hover tooltip shows the source. **With no data the whole block is not drawn**, and the sidebar looks as before. Users can optionally pin one or more live accounts or individual usage windows (such as a specific per-model `7d` window) from the popover to stay visible in the collapsed strip in pinned order (browser-local `localStorage` under `persona.usage.pinnedAccounts`, display-only, with session-stable in-memory fallback when storage is unavailable, one-click **Reset to automatic**, and automatic fallback when all pinned items are unavailable; #12418, #13870).
  Accounts tied at the displayed whole headroom are ordered by what is left, then provider ID, then row ID.
- **Opened**: one row per window per account: window length (5h / 7d), percent left, a bar, the reset countdown, a pace hint; the sample time ("12 seconds ago") sits right of the account title.
  The line under the account title gives the plan and sources ("MacBook · Relay"); per-model windows show the model name next to the length; when the gateway cannot read, a one-line explanation (see "Account-pool gateways"). Esc or clicking elsewhere closes it.
- **Reset countdown**: under 1 hour reads `Resets in 45m`; within today, the time `Resets at 13:05`; longer, `Resets in 2d 3h`, or `Resets in 5d` when no hours are left over (never `5d 0h`). Each language writes its own units: Chinese and Japanese spell out minutes, hours and days as words rather than the letters m / h / d.
- **Pace** (`pace` in `lib/usage-pace.ts`) uses the window length from the label (`5h`, `7d`, falling back to `window_minutes`). Expected progress is elapsed time divided by the window length.
  More than 5 percentage points above expected is "Ahead of pace", more than 5 below is "Behind pace", nothing in between; a tick on the bar marks the expected position.
  No judgment and no tick in the first 5% of a window (in the first few minutes, using even a little counts as "ahead").
  No judgment and no tick for an out-of-date account either (`accountPace`; iOS `UsageWindows.pace(_:stale:now:)`, Android `pace(w, now, stale)`, #12411): an old reading says nothing about how fast the window is used now, so its numbers and reset stay, faded and dated, without a verdict.
- **Providers without data**: an active launcher with no account gets a row that says why in plain words, with **Set up with assistant** handing a prompt to the main chat. Claude Code's and the other built-in subscriptions (Codex, Antigravity) "haven't been read yet"; any other launcher, a provider run through Claude Code included, is one Persona "can't read by itself" unless its relay reports usage. Engineering terms (status line, gateway source) stay in the prompt, not the row (#12411).
- Refreshes once a minute; no requests while the page is not in the foreground. A failed refresh keeps the last answer (iOS / Mac and Android do the same), which turns out of date at its `stale_at`; only a 404 (an older hub) hides the block.

## Provider cards and the reset timeline (#13173)

One rule on every client; web first (`web/src/lib/usage-windows.ts`: `remainingTone`, `tightestRemaining`; `web/src/lib/usage-popover.ts`: `groupByProvider`, `sortForCard`, `sortedWindows`; `web/src/lib/usage-timeline.ts`: `unifiedTimeline`; view `web/src/views/UsagePopover.tsx`). It is computed on the client from data already loaded: no new request, and the hub's answer is unchanged. Since #14690 the web draws what is left; iOS and Android still draw the used scale and a timeline per card until they follow.

- **Remaining scale**: a window shows `r = 100 − used`. With a protection line its reserve is `R = 100 − protect_percent` (0 without one) and its headroom `h = r − R`. Colour (`remainingTone(r, R)`): green from `h = 20`, orange from 5, red under 5. At `r ≤ R` the account is paused for scheduling.
- **Cards**: the accounts are grouped by provider (account type) in the order the hub listed them. A card has the provider's name, its account count and the newest sample age. **Every account is listed** (no folding to two in the details view; the collapsed sidebar strip shows the two with the least headroom).
- **Order inside a card**: by the plan's 7d window reset, soonest first; accounts without a 7d window or reset time last, then by name.
- **Rows**: per account the 5h window, then 7d, then other plan windows by length, then per-model allowances; each with the bar (filled left to right to `r%`), the remaining percent in the bar's colour, the pace tick at `100 − expected used` (a bar shorter than it is "Ahead of pace") and the reset countdown. Out-of-date accounts stay faded and have no pace.
- **Reset timeline**: one chart for the whole popover, under the cards and across every column, drawn when any account has a reset time. A shared axis (now, 6h, 12h, 1d, 3d, 5d, 7d; the first 24 hours take 45% of the width, 1 to 7 days the other 55%, `timelinePosition`), one row per account in card order and then the card's account order, a hairline between providers, the name with the provider's dot, without its provider prefix (`stripProviderPrefix`: "Codex #alpha" reads "#alpha"), and the plan under it; in each row a teal capsule for the 5h window over a blue one for the 7d window, from the now line to the reset, with an end dot and the time left. Windows of one length merge into one lane at the soonest reset (a provider's per-model windows, listed in the tooltip); a lane whose own window is paused ends in an orange dot. Windows without a reset time and ended ones are left out; an account with none has no row. A row's accessible label lists each lane's reset ("5h · Resets in 4h", "7d · Paused · …").
- **Protection line**: a window may carry `protect_percent` (0-100; it can differ per account and per window): the use at which scheduling stops using the account. When present, its bar shows the reserve as a hatched warning-colour zone on the left `[0, R]`, a thin line at `R` and a small "Keep R%" flag; at `r ≤ R` the bar is red and its reset reads "Paused · …"; the bar's accessible label says what is left and where scheduling pauses. When absent (an older hub, or no such rule) nothing is drawn. The hub side of the field is a separate change; clients only read it.
- **Layout (web)**: the popover sits fixed above its trigger, one 320 px column while everything fits the window's height. Otherwise it takes the fewest columns (at most 3, as the width allows) in which the provider cards, kept whole and in order, plus the full-width blocks fit without scrolling, re-measured on resize with 24 px of hysteresis; only when 3 columns still overflow does it scroll, with faded edges.
- **Switches**: one switch per provider on **Admin › Usage sources › Providers in the usage bars**, writing `usage.hidden_providers` (display only, #11767). No new setting.

## UI (iOS / Mac, #2889)

Same rules as the web (pure functions in `apple/Packages/PersonaWire/Sources/PersonaWire/UsageWindows.swift`, unit tests `UsageWindowsTests` share the web's cases; views in `Persona/Views/Machines/UsageWindowsViews.swift`):

- **iOS**: on the Machines page (**Machines** in the drawer / Settings / Agents menu), a **Subscription Usage** section below the cards lists every window of every account directly (no hover on a phone, so no two layers of thin bar + popover).
  One card per account type (#13173): the type's name, how many accounts, when the newest was read; every account of it is listed (no folding), soonest 7-day reset first (accounts without a 7-day window or reset time last, then by name; `UsageWindows.sortedByWeekReset`, `UsageCardsTests`).
  The sample time sits right of the account title, the next line gives the plan and sources, and there is a one-line explanation when the gateway cannot read; one row per window, the 5h window then the 7d window then any other: length, used, ahead / behind pace, reset countdown, with a bar carrying the pace tick below. Under the rows an always-shown reset timeline: a strip from now to the farthest reset (at most seven days) with a segment per window ending at its reset (`UsageWindows.timeline`). With no data the whole section is not drawn.
  A menu in the section header switches each account type off or on. The hub's `usage.hidden_providers` is written only from the admin console (a phone's credentials do not open it), so the phone's switches are a filter kept on the device: they can hide more, never show what the hub left out.
  It asks once a minute while the page and the App are in the foreground (`tabActiveTask`), and also on pull to refresh. DEBUG: `-tab dev -openMonitor all`.
- **Mac**: at the bottom of the section column, above the connection status, one thin bar each for the two fullest accounts (`MacUsageFooter`), warning color at ≥ 80%, out-of-date ones faded, hover tooltip shows the source; clicking opens the same detail popover (Esc or clicking elsewhere closes it).
  It asks once a minute while the App is in the foreground; the Machines page does not draw it again.
- Demo mode (`DemoHub`) has one Claude and one Codex account; the mock hub's `MOCK_USAGE=gateways` shows a mix of computer and gateway accounts.
- With multiple hub groups, each group is asked separately and the results are sorted together (the same account in two hub groups appears once in each; no merging across groups); old hubs (404) count as no data.

## Limitations

- Outside conversations Persona runs on Claude Code (the stream source, #12855), Claude Code's built-in CLI collector (#12412) depends on the installed `claude` supporting either the stream-json `get_usage` control request or `-p /usage`; API-key and gateway setups report no subscription windows (use an account-pool gateway source or the optional status line wrapper when needed). A sample turns out of date like any other 30 minutes after the last reading.
- Codex rollout fallback numbers update when Codex is used. The sample time in the UI shows how old they are.
- After the manager restarts (it restarts along with the hub), the last answers of machines that have gone offline are lost until they come back online and are asked again; the gateway's last answer is also in memory, so after a restart the sidebar lacks its accounts until the first round (within seconds) reads them.
- **The different window types have not been verified against a real relay** (no gateway is running on this dev machine): the adapter follows the first connected relay's public API documentation (account read views, access control);
  the tests use a fake relay HTTP service (account list, usage, errors, expiry, the group key's 404, an old gateway's bare array). Once it is really connected, use **Test connection** in the admin UI to check the account and window counts.
  On 2026-11-06 (#4576) the account list was checked read-only against a real relay: the paging parameters and the shapes of `quota_summary` and `last_error` match what is written here; all 71 accounts (including 5 disabled ones) were in the list, and Kiro's `monthly` / `kiro_bonus` came through as-is.
  On 2026-11-21 (#12311) another relay's key self-service document was read once with an ordinary API key: 9 accounts, with `five_hour` and `weekly` (account), `weekly_fable` and `gemini_five_hour` / `partner_weekly` (`scope: "model"`), `daily`, and length-less `balance_cny*`, all read as the mapping table says; its management API turned the same key away (401), and its single-account `/v1/usage` answered `unavailable` for a key that reaches several accounts. The `relay_key` test fixture follows those shapes.
- The same Claude account on a computer (the status line's `resets_at`, seconds) and in a gateway (time from response headers or the usage endpoint, possibly with fractional seconds) merges into one only if the reset times land on the same second; one second apart, it shows as two accounts. Not checked against real data.
- Windows whose `kind` in the gateway has no readable length are not drawn (mapping table above); if a relay adds new `kind` words later (e.g. `three_hour`), add them to `usagesource.KindMinutes`.
- **A current Codex CLI may emit only the weekly window**: on one machine, all 78 rollouts and ~131k `token_count` events (2026-09 to 2026-11) had `rate_limits.secondary` set to `null`, so no 5-hour window exists in the local data and the sidebar can only draw `7d` — this is not a missed sample. The parser still reads both primary and secondary, so the 5-hour window appears automatically if the upstream starts sending it again.
- The same events carry a `credits` object (`has_credits` / `unlimited` / `balance`): a separate credit balance, not a time window. It can remain unchanged for days while the weekly window is being consumed, so it is not drawn (per the "no readable length" rule above) and must not be used as a sample-freshness signal.
- **The reset time is owned upstream and can re-anchor or jitter**: the weekly `resets_at` was observed re-anchoring mid-cycle (a fresh window starting early, the percentage resetting and the balance stepping), and adjacent events of one rollout can differ by one second. The displayed countdown uses the newest Codex sample's reset time.
