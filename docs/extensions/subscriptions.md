# Source subscriptions

Source subscriptions let the manager pull new information from a connector's read-only tools into the Inbox or knowledge base. The design is tracked in #2036. The optional catalog protocol (#2037), the manager's storage, validation, scheduler, runner and API (#2038), delivery to the Inbox and the knowledge base (#2039) and drafting a recipe with a model (#2040), the pages (#2041, "Pages"), push sources a device fills itself (#3848, "Push sources") and the assistant's tools (#5240, "Assistant tools") are implemented. A catalog alone does not turn on polling or store source content.

Moving the Inbox's built-in sources (iMessage, GitHub, Lark, mail) onto subscriptions, with what subscription items still lack and the per-source switch, is planned in `docs/design/proposals/inbox-sources-migration.md` (#6150).

## Source catalog

A connector may declare `catalog`, without changing connector protocol version 2. It implements `connector.Catalog` in process, or answers `connector.catalog.list` and `connector.catalog.call` through the extension protocol. Schema and SDK types live in `schema/extension/connector.schema.json`, generated into the Go and TypeScript SDKs. `ext.Methods` accepts the generated request and result types like other connector methods.

`catalog.list {}` returns `{sources: [...]}`. Each source has a stable `id`, a display `name`, and `tools`. An id does not change when the name is renamed or translated. Optional fields are:

- `icon`: one of the declarative UI's `NavIcon` values.
- `service`: a `<kind>:<domain>` hint, such as `mail:example.com`, for recognizing a service already read natively. It is a hint, not an account identity or permission.
- `min_interval_seconds`: a positive minimum interval.
- `max_calls_per_hour`: a positive budget shared by all sources of this connector/account, not a separate allowance for each source.
- `recipes`: pull recipes using this source's tools.

Each tool has a short `name`, a `description`, an object `input_schema`, and `read_only: true`. Its optional `title` is for display. Short names are unique within the source; source ids are unique within the connector. Optional `annotations.read_only_hint` and `annotations.destructive_hint` must agree with the read-only declaration: an explicit false read-only hint or true destructive hint refuses the catalog.

`catalog.call {source, tool, args}` returns `{text}`. `args` is an object and defaults to `{}`. The source and tool must still appear in the current read-only catalog before dispatch. A missing or non-read-only tool is refused with `not_read_only`, without invoking it. Upstream `rate_limited` errors retain their `data.retry_after` in seconds, including fractional values. Programs written with the SDK must apply the same refusal in their handler; an `ext.Method` decodes its parameters and does not establish that an arbitrary tool is safe.

The Go SDK's `wire.CheckConnectorCatalogJSON` validates the original wire result, and `wire.CheckConnectorCatalog` validates typed results. Both check schema, identities, limits, read-only declarations, object schemas, and recipe shape/references. Server adapters and `exttest.CheckConnectors` use these shared checks. `connector/conformance.CheckProgram` reads the declared catalog and validates it, without executing catalog tools. A conformance check never polls a real source or invokes a write.

## Existing connectors

Without `catalog`, `connector.ReadCatalog` derives one source from the connector's declared `ReadTools`: its id and name are the connector's, and a tool's `<connector id>_` prefix is removed from the short name. `connector.CallCatalog` calls the existing read tool with its full name through `tools.call`. It never falls back to `actions.do`. A name present in both the read and write lists is refused.

A built-in connector of this kind can also implement `connector.ReadJSON` (found by type assertion, declaring nothing). `CallCatalog` then asks `CallJSON` first, so subscriptions and recipe drafting get a JSON answer they can follow item by item, while the models keep the text `tools.call` gives them. A tool without a JSON answer still answers text. The built-in ones answer an object with one top-level list, so `items_path` can be left empty. Each item carries the names the setup form's pickers guess (`id`, `title`, `time` in RFC 3339, `sender` or `author`, `body`, `link`, `thread` where the source has one). The summary line becomes fields (`total`, `more`, paging cursors):

| Connector | Tool | List | Item id |
| --- | --- | --- | --- |
| `github` | `search` | `items`: issues and pull requests (`kind: "code"`: files) | `owner/name#number` (code: `owner/name:path`) |
| `github` | `read` | `comments`: the newest comments and reviews, beside `issue` and `pull` | `comment:<id>`, `review:<id>`; `thread` is the issue's id |
| `github` | `diff` | `files` (with `path`: that file and its patch, no list) | the path |
| `github` | `checks` | `checks`: the check runs, a failed Actions job's log end as `body` | the run's id |
| `imessage` | `chats` | `chats`: one item per chat's latest message | the chat's id and that message's time; `thread` is the chat |
| `imessage` | `read`, `search` | `messages`, `hits` | the message's id; `thread` is the chat |
| `imessage` | `inbox_things` | `things`: the Inbox's rows ready-made, for a `things` recipe | each row's `thread` (the chat) and `last` (its newest message's time) |
| `mail` | `accounts` | `accounts` | the account's id |
| `mail` | `search` | `messages` | the Message-ID, else `account:mailbox:uid` |

iMessage declares a catalog of its own (`connector.Catalog`, #6317): the three tools above as the derived catalog had them (same names, descriptions and schemas, so existing subscriptions keep theirs), and `inbox_things`, which answers the rows its `Collect` has, computed by the same function (`rows` in `connector/imessage/persona.go`). `inbox_things` is a catalog tool only, not one of the assistant's. `connector/imessage/inbox_things_test.go` compares the rows field by field: `Collect`'s with the tool's, and with what iMessage's default things subscription hands the Inbox once its feed is switched (P5a).

`mail` `read` stays text: one message has no list to follow. The answers are pinned in each connector's `testdata/readjson`, and `server/internal/subscriptions/builtin_test.go` dry-runs them item by item. A snapshot subscription already made on one of these tools keeps working; its state is now the JSON.

An absent or undeclared catalog does not expose an implementation merely because it has a similarly named method. Existing Inbox/archive capabilities, feed switches, and `poll_seconds` keep their meaning. An older hub filters the unknown `catalog` capability at extension registration and keeps the connector's other capabilities; it can still use its ordinary tools.

## Recipe format

A recipe is a description of one pull, independent of its schedule and enabled destinations:

```json
{
  "mode": "items",
  "tool": "list",
  "args": {"since": "{{since}}"},
  "items_path": "$.items",
  "fields": {
    "id": "$.id",
    "thread": "$.thread",
    "title": "$.title",
    "sender": "$.sender",
    "time": "$.time",
    "body": "$.body",
    "link": "$.link",
    "due_date": "$.due_date",
    "sender_id": "$.sender_id",
    "chat": "$.chat.id",
    "chat_name": "$.chat.name",
    "seen": "$.read",
    "reply_to": "$.reply_line"
  },
  "kind": "sub_notice",
  "group_by": "thread"
}
```

Required fields are `mode`, `tool`, `kind`, and `group_by`. The tool must be a short name in this source. `args` defaults to `{}`; it is an object, and strings can contain the runner's `{{since}}` and `{{since_date}}` variables. `items_path` defaults to `$`. Field paths are relative to one item; all are optional at the protocol layer. Supported path syntax is `$`, `.field` (letters/underscore followed by letters, digits, underscore or hyphen), and `[index]`. Wildcards, filters, recursive descent and expressions are not supported.

`sender_id`, `chat` and `chat_name` say who and where, for mutes and policies (#6165). They are not part of an item's content: they don't enter its content hash, so adding them to a recipe makes no item look changed. A row's `sender` tag matches by `sender_id` when the recipe maps it, kept as the source writes it, as a built-in's mutes match (a Lark sender id). Without it, the tag is the lowercased display name. With `chat` mapped, the row also gets a `chat` tag named by `chat_name` (the id when there is none), so its menu offers to mute the chat. The tags are the row's newest item's.

`seen` is whether the item was already seen in the source (#6176). It is seen when its value is true, a non-zero number, or `true`, `1`, `yes`, `read` or `seen` in any case; anything else, or nothing, is unseen. An item pulled seen is held seen. When a later run reads an item it already holds as seen, it marks the held item seen in the same transaction as its other writes. Seen never turns off, as the Inbox's read state comes in from the source only. A row is read when its newest item is, a digest when all its items are, and the Inbox treats a read row as it treats a connector's thing read elsewhere (`docs/assistant/inbox.md` "Connector: GitHub (2026-11-02)"): it closes it unless it needs the user, and a read move never starts an item. Rows are sent again on every round for 8 days, so no separate settling is needed for read state. Like the identity fields, `seen` isn't part of the content hash.

`reply_to` is the line saying where a reply goes and who sees it, as a built-in item's `ReplyTo` ("Comment on example-org/example-repo#12 (anyone who can see the repo)"; #6198). A row gets it with `can_reply` when its newest item has one, the item has an id from the source (not the weak hash id), the subscription's connector is on and has an inbox (`connector.Inbox`), and the subscription hasn't found it unreachable (`not_connected`). The connectors' status isn't asked on every round. The Inbox's `inbox.reply` for the row then goes to that connector's `Reply`, with the item's own thread (the recipe's `thread`, else its id) and its id as `last`, after the manager checks the connector can be called (`Service.ReplyTarget`). A row whose item or `reply_to` is gone, or whose connector can't be called, gets the usual "can't reply". The assistant and the apps need nothing new: they offer Draft and say where a reply goes only for a row with `can_reply`.

The setup form has no pickers for these five: saving it keeps the recipe's own while the subscription reads the same tool.

**Things** (#6296). In `mode: "things"` the tool's answer lists Inbox rows already made, in the connectors' own shape (`ConnectorInboxThing`: thread, kind, title, sender, preview, context, at, last, url, open_url, reply_to, can_reply, tier, done, read, tags…), at `items_path` (found as for items when empty). Each row needs a `thread` and a `last`; a row without a `kind` takes the recipe's. `group_by` must be `thread` (one Inbox row per the connector's thread), and the recipe maps no `fields` and has no pagination: an answer is the connector's current rows.

- **Kept** as the connector made it, as this build reads the connectors' rows, keyed by a hash of all of it. A row whose state changes (read on the phone, replied, a new move) is kept again; an unchanged one isn't.
- **Delivered** as each thread's newest kept row, exactly as it came: no `sub:` prefix, tag, tier or count is added, and there is no digest. The rows a built-in's `Collect` makes and the ones its `inbox_things` tool answers then reach the Inbox the same way, which is what lets a built-in's default subscription (below) keep every row as it was.
- **Read-only.** The tool is held to the same read-only rules as any recipe's.
- **The generic `inbox_things`** (#6381). The subscriptions add an `inbox_things` tool to the catalog they read of every connector with an Inbox that doesn't declare its own, in the connector's own source. It answers the connector's `Collect`, the very rows the Inbox's own round gets. It is the Inbox's own rows, so it is left out of the catalogs the pages and the assistant list, no subscription is made with it, and a default subscription's recipe can't be changed. Only a built-in's default subscription reads it; each source's M step confirms with a comparison test that its `Collect` has no side effects.
- **Inbox only.** The knowledge base takes none of a things subscription's rows (it reads the source itself); turning it on is refused, and the pages leave its switch out.
- **The dry run's** fields check becomes "every row is an Inbox row" (`bad_thing` otherwise).
- **The pages** say the rows are provided by the connector, and offer the raw recipe but not the setup form, which maps fields.

An items recipe can opt into resumable pagination. Cursor pagination sets both `cursor_arg` (the top-level tool argument that takes the cursor) and `next_cursor_path` (where the result's next cursor is); a missing, null or empty cursor is terminal. Page pagination sets `page_arg` (a positive integer argument, default 1) and can set `next_page_path` to the result's next page number, or to a boolean that is true when another page exists. Without `next_page_path`, a full requested page continues and a short page is terminal. The two strategies cannot be combined. Every leased run reads one page and commits its new items, dedup keys and bounded continuation in one transaction. A continuation keeps the original window and is due immediately, still subject to the connector's call gap, budget and backoff. `since_at` and destination catch-up advance only after the terminal page. Repeated cursors/pages, invalid values and the 100-page safety cap pause the subscription in `needs_recipe` instead of skipping ahead. Recipes without these mappings keep their legacy one-page behavior.

`mode` is `items` for arriving items or `snapshot` for state such as a balance. Text results can only be snapshots. `things` is for a tool that answers Inbox rows ready-made (below). `group_by` is `thread` or `source`: how the items are grouped into Inbox rows and knowledge-base chats ("Destinations"). `source` is for a source that is one conversation (a group chat, a channel), `thread` for items that stand on their own (articles, notifications) or belong to one of several conversations, with `thread` mapped where there is one. `kind` is one of `sub_mail`, `sub_message`, `sub_notice`, `sub_deadline`, `sub_event`, `sub_update`, or `sub_digest`.

The catalog stage checks parse, shape and references. The runner must additionally validate filled arguments against the tool schema, enforce the template-variable allowlist, dry-run field mapping, parse titles/times, and prove a repeated result yields nothing new. Missing ids fall back to content hashes and are marked as weak deduplication. A tool changing incompatibly, or repeated missing mapped fields, pauses the subscription for a new recipe.

## Subscriptions in the manager

The core is `server/internal/subscriptions`; the manager wires it to its connectors, database, audit and broadcasts in `server/internal/extmgr/subscriptions.go`. A subscription is one source of one connector, pulled by one recipe.

### Read-only tools

A tool is admitted at save and checked again before every run by one rule, the same as `catalog.list` / `catalog.call`:

- It must be in the connector's current catalog: `catalog.list`, or the `tools.list` read tools of a connector without a catalog. `actions.list` is never used, and the run goes only through `catalog.call` / `tools.call`.
- A catalog whose hints mark a tool as not read-only or destructive is refused as a whole. Hints only refuse: a tool without hints is not refused for that.
- A tool whose name contains a write word (`connector.WriteVerb`: send, create, delete, order, transfer…) is refused, even when it is declared read-only. Subscriptions call it again and again with nobody watching.

A refusal at save answers the error below. Before a run, a tool that left the catalog or is now named as a write pauses the subscription in `needs_recipe`. A catalog refused as a whole is a failed run, retried, without calling anything.

### Validation and dry runs

A dry run calls the tool once and runs five checks in this order: `read_only`, `args` (the arguments with `{{since}}` / `{{since_date}}` filled in, held to the tool's `input_schema` with the JSON Schema subset of `model.complete`, `server/internal/jsonsubset`), `fields` (`items_path` is a list, some item has a title, times parse), `dedup` (the same result again brings nothing new) and `interval`. A text result can only be a snapshot. A recipe that leaves `items_path` empty has its items found: the result itself when it is a list (the protocol's `$`), else its one top-level list. None or several is `no_list`, with the shape's `arrays` naming the top-level lists. The dry run says where it read them (`items_path`), and a saved recipe keeps that path. An item without an `id` field is keyed by a hash of its title, time and sender, marked `weak_dedup`.

A dry run returns up to three items and the first item's leaf paths with examples, for the pages' pickers. Each item is what the Inbox would show: its title, sender, time, due date, link and thread, the body cut to 200 characters, `summary` (its row's preview line: the body's first 200 characters on one line, as delivery makes it) and `problems`, the mapped fields this item has no readable value for (`{key: "fields.sender", code: "empty" | "bad_time"}`). These values exist only in that answer. Its `warnings` do not fail it but say what will not do what it seems: `unknown_key` for a key the recipe format does not know (`fields.author`, `fields.url`, `item_path`: decoding drops it, so it used to pass with that column silently empty; the request's raw recipe is read for them) and `empty_field` for a mapped field whose path is empty in every item pulled (the title is the `fields` check's). The subscription keeps the result's shape (`json` or `text`, top-level keys, which of them are lists, list length), never a value. Saving a recipe runs the same checks, and a recipe that fails is not saved.

### Drafting with a model

A source whose extension ships no recipe, or a subscription that needs a new one, can have a model draft it (`Service.Generate`, `server/internal/subscriptions/generate.go`). Only the API offers it now: since #5278 the pages hand setting up to the assistant instead (Set Up with the assistant, "Pages"), and removing the endpoint is left to its own issue. It runs on the model purpose `subscription.recipe`, which belongs to Information processing (`kb`) and follows its model (`docs/assistant/model-config.md`). The manager holds no model keys: it asks the assistant with `models.purpose_complete`, which takes only the manager's built-in purposes and is not callable by extensions. Each call uses `model.complete`'s `json_schema` check and its one retry that states the mismatch, and has at most 60 seconds; a whole drafting has at most 4 minutes.

1. **Pick**: the source's subscribable tools (name, title and description cut to 300 characters, compact `input_schema` cut to 1500 bytes, at most 40 tools), no result. The model answers `{tool, args}`; `tool` is one of those offered.
2. **Sample**: the arguments, with `{{since}}` / `{{since_date}}` filled in, are held to the tool's `input_schema`, then the tool is called once. The answer is cut for the model: at most 3 entries a list and 120 characters a string, and further (fewer entries, shorter strings, fewer keys, less depth) until it is at most 8 KB. A text answer sends its first 120 characters.
3. **Map**: the model answers `mode`, `items_path`, `fields`, `kind`, `group_by` and `note`: one or two sentences for the user in the request's language (else the user's).
4. **Check**: the draft goes through the dry run's full check (another call of the tool).

A pick that does not fit or a draft that fails a check is asked for once more, with the check's code and sentence; one more try in all. A failure of the connector itself (`not_connected`, `rate_limited`) is not retried. The sample is in the model call only: never stored, logged or answered. Logs say which call failed and how many calls a draft took.

The prompts are `server/internal/prompts/subscriptions/` (`pick`, `pick_input`, `map`, `map_input`, `retry`), pinned by `server/internal/subscriptions/testdata/prompts/`.

### Storage

Manager migration `0482_subscriptions.sql`:

- `subscriptions`: one row per subscription. The settings, recipe, run state and last 20 runs are a `json` document, with `revision` (a change is written only at the revision the client saw) and `lease_until` beside it.
- `subscription_seen`: dedup keys per subscription, the newest 5000 and none older than 400 days.
- `subscription_items`: new items for the destinations, bodies cut to 8000 characters. They are kept past what the destinations read: 8 days (the Inbox's 7-day window and a day), 30 days while the knowledge base is on (one that stops reading for longer misses them). Each run prunes the older ones; their dedup keys stay.

A run writes its seen keys, its items and the cursor or bounded page continuation in one transaction; nothing is kept from a run that did not finish.

### Running

- The manager looks for due subscriptions every 30 seconds, and at once after a change or Run now, after waiting a random 0–60 seconds at startup. A run holds a 5-minute lease. A run that died is made again once its lease ends, and the seen keys keep it from adding anything twice.
- Each run reads from an hour before the newest item it has, or a day back the first time. A paginated recipe reads one page per leased run, resumes the same original window after a restart and schedules the next page immediately after each commit; its window advances only after the terminal page. A page that fails is tried again from the same position once; when it fails in the run before too (an expired page token is refused every time), the position is dropped and the next run pulls afresh from the window's start, the seen keys holding back what earlier pages delivered (#6310). A rate limit or an unreachable connector is no such failure and keeps the position; turning the subscription off drops it. A legacy recipe reads one page, and a full page of new items is reported as `maybe_more` by the dry run.
- Limits are per connector, shared by its subscriptions:
  - one call at a time, at least 20 seconds apart with jitter;
  - at most `max_calls_per_hour` calls an hour (default 120); a run over the budget moves to when the budget allows;
  - `rate_limited` pauses all of the connector's subscriptions until `retry_after`, and a restarted manager keeps the pause;
  - three failed calls in a row pause the connector, 1 minute first, doubling up to 6 hours.
- A subscription's own failure retries after its interval × 2ⁿ, at most 6 hours, and never stops it. Two runs in a row whose items lose their title, or whose list is gone, pause it in `needs_recipe`, as does a tool that changed so that it no longer takes the recipe's arguments; a tool whose description changed while it still fits carries on.
- A connector that is not `ok` or is switched off is `not_connected`: no failure, no run recorded, and it resumes by itself.
- A connector's `changed` event (any scope) brings forward the next run of its subscriptions that are on and pull items (#6247), as it kicks the Inbox for the connector's own things. Each runs this way at most once a minute after its last run, and never sooner than the source's `min_interval_seconds`. The limits above still hold, and a run they hold back is left to the schedule. The run is recorded as `scheduled`; snapshots and push sources keep their own timing. A source that announces new items so reaches the Inbox in seconds, not at its next pull.
- A switched built-in's default subscription ("Built-in sources switched to their default subscription") runs 3 seconds after its connector's `changed` event instead, as the Inbox's own kick of the built-in does (#6381), and a burst of events runs it once. Its runs replace the built-in's `Collect` calls one for one, so they are outside the hourly budget and the call gap and aren't counted in them; a pause the connector asked for still holds.
- Intervals are at least 5 minutes and the source's `min_interval_seconds`; snapshots at least an hour, by default once a day at `daily_at` (08:00 in the user's time zone). Items default to every 15 minutes, or the source's minimum when longer.
- Logs carry counts and states only. Creating, changing, running now and deleting are recorded in `plugin_audit` (`subscription_created`, `subscription_updated` with the changed field names, `subscription_run_now`, `subscription_deleted`).

### API

Reading is open to every device; changing anything, dry runs included, needs full access (403 `read_only_device`). Errors are the API's usual `{error, code}`, with `dry_run` when checks refused a recipe, `subscription` (the current one) on a 409 `conflict`, and `retry_after` on `rate_limited`.

| Route | Answer |
| --- | --- |
| `GET /api/v1/subscriptions?connector=&extension=` | `{subscriptions, features: {deliver, generate}}`; both are true: `deliver` (#2039) and `generate` (#2040) |
| `GET /api/v1/subscriptions/summary` | `{connectors: [{connector, extension, subscribable?, on, failing}]}` from cached catalogs only (never a live call); `subscribable` is absent until the catalog was read |
| `GET /api/v1/subscriptions/catalog?connector=` | the connector's sources and tools read now, each tool with its `input_schema` and `subscribable` or `refused` with a code, the source's own recipes and its subscriptions; `direct` says whether the connector's own Inbox / archive feeds are on. Without `connector`: `{catalogs: [...]}`, every connector's catalog from earlier reads only (never a call; a connector not read yet is left out and read in the background) |
| `GET /api/v1/subscriptions/{id}` | `{subscription, runs}`: the newest 20 runs first, `{at, ms, trigger, new, count, error, baseline?}` (`baseline`: items a run recorded without keeping them after a recipe changed, "Destinations") |
| `POST /api/v1/subscriptions/-/dry-run` | `{id}` or `{connector, source}`, with `recipe?` → the dry run with its `warnings`; nothing is written |
| `POST /api/v1/subscriptions` | `{connector, source, recipe?, recipe_origin?, label?}` → 201 and the subscription, both destinations off; without a recipe, the source's first own one (422 `needs_recipe` when it has none: draft one). `recipe_origin: "model"` with a recipe records a draft taken as it was; otherwise a given recipe is the user's |
| `POST /api/v1/subscriptions/{id}/generate` | Drafts a recipe with the model. A subscription's `id`, with `{revision?}`: the draft that passes is saved (`recipe_origin: "model"`, `recipe_at` now), its run state starts over (`needs_recipe` clears), and its switches, label, rules and interval stay (a new mode takes that mode's default interval) → `{recipe, note, dry_run, subscription}`; 409 `conflict` when it is at another revision, before or after drafting. `-` with `{connector, source}`: a source not subscribed yet → `{recipe, note, dry_run}`, nothing written. A draft that still fails is 422 with the check's code, `dry_run` and `recipe` (the draft as far as it got, for the manual editor). 503 `no_model` when no model is set up for it, 502 `model_failed` when a call failed (503 with `retry_after` while the model's connection is held) |
| `PUT /api/v1/subscriptions/{id}` | `{revision, label?, recipe?, existing?, to_inbox?, to_kb?, interval_seconds?, daily_at?, triage_rules?, fallback_tier?}`; `existing` goes with a recipe that changes ("Destinations", A changed recipe); 409 `conflict` at another revision, 422 `interval_too_short` |
| `POST /api/v1/subscriptions/{id}/run` | 202: runs as soon as the connector's limits allow, also while it is off; a manually started paginated pull finishes its continuation, then stops |
| `DELETE /api/v1/subscriptions/{id}?data=1` | 204; removes its seen keys and pending items. With `data=1` it first switches both destinations off and has the Inbox and the knowledge base remove what they took ("Destinations"); 503 `assistant_offline` while the assistant is away, the subscription left off for another delete |
| `POST /api/v1/subscriptions/-/push` | `{items: [{key?, title?, body?, app?, link?, time?}]}` from a full-access device → `{subscription, new, count}`: the device's push source, made on its first push (no items: only made). 400 `bad_request` for more than 20 items or one with neither a title nor a body, 429 `rate_limited` with `retry_after` past 300 items an hour ("Push sources") |
| `POST /api/v1/subscriptions/-/ui` | the pages below, with an extension page's request and answer; every device may draw them, actions need full access |
| `GET /api/v1/subscriptions/-/graph?locale=&tz=&ui_version=` | Inbox Sources as relations (#5741, "Pages"): `{extensions, reads, sources, destinations}`; every device may read it |

A subscription's `state` is `off` (both destinations off), `ok`, `failing`, `not_connected`, `rate_limited` or `needs_recipe`, with `error {code, message}`. The message is one English sentence without source content; clients localize the code. The manager reports the `source_subscriptions` feature now that subscriptions deliver (`features.deliver`, #2039), and the apps show their entry rows only with it; beside it `subscription_push` says a device can push ("Push sources"); it broadcasts `subscription` `{id, connector, revision, state, removed}` when one is saved, runs or is deleted.

### Destinations

Every subscription is an internal source of the manager, beside the connectors and not listed with them (`server/internal/subscriptions/deliver.go`, wired in `server/internal/extmgr/subscriptions.go`). Neither destination is written by the subscription: the Inbox keeps what it took by thread and `last`, the knowledge base by its cursor and message-id receipts, so a replay, a run made again after its lease, or a restarted manager hands them the same things and they add nothing.

**Inbox.** Each collection round names every subscription as a source (id the subscription's, name its label) and, in rounds that read the connectors, hands in its things as a plugin's connector's (`extension`): new items are triaged before they enter (`docs/assistant/inbox.md` "AI triage"). A run that keeps items or ends a catch-up asks for a round at once. Things are made from the held items each round, as a connector answers `Collect` from its memory:

- **Rows follow `group_by`** (#5671), as the knowledge base's chats do: `group_by: thread` makes one row a thread, `sub:<subscription>:<thread or uid>` (an item without a thread is a row of its own); `group_by: source` makes the whole subscription one row, `sub:<subscription>:all`, like a chat row of a Lark group (its `thread` field, if mapped, is left out): titled by the subscription's label, with no `label` (the row's tag would say the title again). A row's newest item (by its time, never later than when it was stored) is its title (a thread's row), sender, preview (the body's first 200 characters), `at` and `last` (its uid); its earlier items are listed above it in the context, one line each (at most 10), then the newest's title, sender and time, due date, body (3000 characters; left out when it only says the title again, as a chat message mapped to both does) and link. The row is triaged as one, with that context, so an item in it that needs the user puts the row in Needs you. `url` is the link when it is https. The kind is the recipe's, the label (the row's tag) the subscription's except on the one row, `can_reply` false. The row opens to its content, which is the whole of it: it has no conversation page, and the thread request for it answers a page that is none (`docs/protocol/protocol.md` "Connector API", #5537). Its tags are `source` (the subscription's id and label, which names it and is no mute target: turning the Inbox off is that) and, since #5306, `sender` (the newest item's sender, lowercased, with its name as shown), so the user can mute a sender from the row's menu as for a connector's rows and give it a policy. A digest, a snapshot and the failure notice have no one sender and no `sender` tag; rows delivered before keep the tags they had.
- **Count.** A row's `extra.count` is how many of its items the newest pull stored (the catch-up's count as one pull: they come in together), so the Inbox adds that many to its open item's count, as a chat row counts unread messages; one item leaves it out (one is the default). Only subscriptions say so (`InboxSource.counts` in the manager's round): another source's `extra.count` is not read there, and each of its moves counts one. A pull the Inbox did not take before the next one (new items waiting for the model) is not added; its items are in the row's context all the same.
- **When the grouping changes** (a recipe changed to the other `group_by` without a new catch-up, i.e. `existing` `baseline` or `deliver`; and the upgrade to #5671, before which every row was a thread's or an item's whatever `group_by` said), the rows made before stay as they are: open ones stay open and go down the list as newer rows come in, and nothing is regrouped into the new row. The subscription keeps the grouping its rows are made by and from when (`inbox_group`, `inbox_group_from` in its data); until its next run that succeeds marks the change, the Inbox gets no rows of it, and from then on only the items stored by that run and later. Moving the earlier rows into the new one was not done: it would take rows the user may be reading or have in Needs you out from under them, and fold what they had read into a row that looks new. A pull that a new catch-up ends (turning the Inbox on, a recipe changed with `digest`) starts the rows with the recipe's grouping, as the catch-up holds nothing delivered before. A new item still waiting for the model when the grouping changes (fallback `wait`) does not come in, as it was stored before the change.
- Rules: the subscription's `triage_rules`, else its extension's own `triage_rules` setting, else none (the Inbox's `inbox.rules`).
- Fallback (`extra.rule_tier`, used only when no model can triage the item), by `fallback_tier`: `""` FYI, and Needs you for a row with an item with a due date or of kind `sub_deadline`; `fyi` or `action` for everything; `wait` none, so new items wait for the model.
- **Catch-up.** Turning the Inbox on (also after the knowledge base was on alone, and again after it was off) records the time, and the first terminal page that succeeds after it marks the end of the catch-up. Until then the Inbox gets nothing. The catch-up is what was stored from a day before the Inbox was turned on up to that run: three items or fewer come in as rows (as `group_by` groups them); more become one `sub_digest` row, `sub:<subscription>:catchup`, triaged once, its context a line an item (time · sender · title link), newest first, at most 50 and then "and N more". Items whose due date was still ahead when the catch-up ended stay out of the digest, in the rows. Later items come in as rows, a new thread row even for a thread in the digest.
- **Snapshots** come in as one row, `sub:<subscription>:snapshot`, the newest kept state (a snapshot is kept only when its content changed, so an unchanged one never moves the row).
- **Failure notice.** A subscription that is on and has not pulled for 24 hours (`failing_since`) adds one FYI row by rule, `sub:<subscription>:failing` of kind `sub_failing` ("<label> hasn't pulled anything since <time>" with the reason for the error's code). It is one row a failure (`last` is the failure's start) and is filed as done (read) once the subscription pulls again.
- **For the AI twin** (#6222): a subscription's source in the round also says what it reads (`via`: the connector's id; `via_source`: the source's name), and, while its knowledge base is on, the knowledge-base chat of each row's thread (`chats`, under the subscription's archive). The twin's prompt names the subscription and the connector, and gets that chat's summary (`docs/assistant/inbox.md` "AI twin: handling chats and suggestions (from phase 1, 2026-11-05)").
- **Read error.** Before that, from the first round after a failed run, a subscription whose Inbox is on hands its round a `read_error`, as a built-in's failed read does (#6189). It is `failed` while it is `failing` or `needs_recipe`, and `rate_limited` while it is `rate_limited`; there is none while it is `ok` or `not_connected` (as a built-in whose connector can't be called now is off). The apps list the source as behind, and the rows it holds are still applied. The error clears on the first round after a run succeeds.
- The texts the manager writes (the digest's title and its "and N more", the due line, the notice) are in the user's language (`subscriptions-deliver` catalogs). The kinds are described for the models in `server/internal/prompts/inbox/kinds/subscription.md`, handed in with the round as the source's kinds.
- A round reads at most the newest 300 items stored after the catch-up and 500 of the catch-up.
- **A changed recipe.** Everything a new recipe pulls is new to the dedup keys, though much of it happened before the change. `existing` on the change says what its first run's items become (#5240); the same recipe saved again changes nothing here:
  - `digest`, the default (also for a recipe regenerated with the model): while the Inbox was on and stays on, its catch-up starts again at the change instead of a day before, so what the new recipe finds at once comes in as rows when 3 or fewer, else as one `sub_digest` row of its own (`sub:<subscription>:catchup-<unix time of the change>`, "<label>: N items found when its recipe changed"); later items come in as rows. Turning the Inbox off and on again catches up on the day before as always. The knowledge base keeps each item.
  - `baseline`: the next pull that reaches a terminal page records what it finds as seen and keeps none of it (each page's run counts its baseline items); only what arrives later comes in, to the Inbox and the knowledge base alike. For a change that finds what was delivered already, such as a new field mapping.
  - `deliver`: each comes in as a new item.

**Knowledge base.** `connectors.archives` lists the subscriptions with "Add to knowledge base" on, and `connectors.changes {id: <subscription>, cursor}` pages through their held items (100 a call): the cursor is the last stored position read, and the knowledge base moves it once it has written the batch (`cursors/<subscription>.json`). It starts a day before the knowledge base was turned on: the first run's page for a new subscription, and the same for a cursor kept from before the knowledge base was last off (what was stored while it was off is skipped). The knowledge base reads it on its own schedule (10 minutes) and soon after a run that kept items.

- Chats: one a thread (`t` and a hash of the thread, named by the thread's first item it reads) for `group_by: thread`, else one for the subscription (`all`, named by the label); a snapshot always the latest. The chat kind is `subscription` (the selection keeps every message, for `text_days`).
- Messages: the item's uid as the message id (receipts drop it on a replay), its sender's name (no sender id, so no person is made), its time, and the text `【title】` and the body, then the due line and the link on lines of their own.

**Deleting with the data.** `DELETE …?data=1` switches both destinations off (so neither takes more meanwhile), then has the knowledge base forget the subscription's chats and cursor (`kb.forget`) and the Inbox its items (`inbox.forget`), and only then deletes the subscription. While the assistant is away that answers 503 `assistant_offline` and the subscription stays, off; deleting it again finishes. A delete without `data` leaves the Inbox items and knowledge-base chats as they are.

### Pages

The manager draws the subscription pages (#2041) as declarative UI in `server/internal/subscriptionsui`, served by
`POST /api/v1/subscriptions/-/ui` with the request and answer of an extension page (`context`, `state`, `action`, `values`;
[extensions.md](extensions.md) "Declarative UI"). Every app draws them with the renderer it already has and adds only entry rows.
Unlike an extension page, every device may draw them: a device without full access sees the same pages with a line saying it
can only view, switches disabled, fields shown as values and the buttons that change something left out; an action from it
is 403 `read_only_device`.

**Inbox Sources** (#5278). Unnarrowed, the list is every source that brings things into the Inbox or the knowledge base: the
manager's built-in sources (the connectors with an inbox or an archive that are set up, and mail; `server/internal/extmgr/inbox_sources.go`)
beside the subscriptions, and the assistant's own source (#6430, `docs/assistant/inbox.md` "Assistant rows"), always last: a row named Assistant whose Add to Inbox switch says whether the assistant may put its conversations into the Inbox (`inbox.post`). Settings has an **Inbox Sources** row for it (iOS, Android and the web), and the Inbox's ⋯ menu
opens the same page. The manager reports `inbox_sources` with these pages, and the apps show the Settings row only then.

**Relationships** (#5741, `server/internal/subscriptionsui/graph.go`). The same sources as relations, for the apps to draw
(the web's List / Relationships switch on Inbox Sources, an iPhone row that pushes it): `extensions` (what brings sources: an
extension, or `persona` for what Persona reads itself), `reads` (how: one of a catalog source's tools, `direct` for a built-in
source, `push` for a device's own), `sources` and `destinations` (`inbox`, `kb`). A source carries its list row's key as `id`
(`s.<subscription>`, `b.<connector>`), its row's subtitle and badge as `line` and `badge`, `reading` (it reads now without a
problem: signed out, failing, `needs_recipe` and `not_connected` don't; `rate_limited` does), `to`, the sources it may duplicate
(the list's Possible duplicate), the page `state` that shows it, and for a subscription `ask` (Set Up with the assistant's
`open_url`). A destination counts the sources with it on and how many of them are reading. A source with neither destination on
is left out. A source's `icon` is one the app's `ui_version` has, as on its pages (the hub's own version when the request
says none, as the web's doesn't; the iPhone sends its pages' version). Sources come grouped by extension (Persona's own first) and, inside one, by read, reads in the order of their first
source, so columns drawn in that order cross no links. The manager reports `inbox_sources_graph` with it. The iPhone app says it
draws the view itself with `context.graph` in these page requests (never handed to an extension), and the list then starts
with a **Source Relationships** row whose press answers `open_url` `persona://sources-graph`: the app pushes the view (a tree per
extension with filters, a sheet for the picked source). Only these pages may answer it, and a device that may only look may
press it too, as it changes nothing. The frozen Mac app sends no `context.graph`.

| Page (`state.p`) | What it shows |
| --- | --- |
| list (none) | Needs attention (built-in sources that can't read: signed out, a failed read, an account that failed; `failing` and `needs_recipe` subscriptions), Read by Persona (the built-in sources: where their things go, how often, when they were read), Subscriptions, Turned off, Recommended, This device's Shortcuts source, and a row to More to Subscribe. `{"ext": id}` or `{"c": id}` narrows it to one extension or connector, with its sources to subscribe to listed on it (an extension's or connector's Subscriptions row) |
| `builtin` (`c`) | A built-in source: what may duplicate it, what keeps it from reading, its two destinations (its connector's own switches; mail's Add to Inbox, #5307, stops every account's mail rounds), how often the Inbox and the knowledge base read it (shown, not chosen), its connection and the latest read of it or of each account, its triage rules (its own where it has them: a plugin's `triage_rules`, lark-cli's option; otherwise the Inbox's), and Muted. A source with a default subscription (iMessage) also has How it's read: the Read through its subscription switch ("Built-in sources switched to their default subscription") |
| `mutes` (`c`) | The source's mutes, newest first, each with Unmute (asking first); what a mute filed stays done |
| `more` | Per connector the sources that can be subscribed: at least one subscribable tool, no subscription yet and nothing recommended for it, from cached catalogs only |
| `source` | The two destination switches, the interval, the state and the last and next pull, the recipe, triage rules, what happens without a triage model, Muted (its senders muted in the Inbox, with the `mutes` page; #5306), Set Up with the assistant, Preview What Comes In, Pull Now, and Turn Off and Delete Stored Content. A failing, paused or unconnected one starts with what is wrong and when it tries again. For a source not subscribed yet (`c`, `s`): the extension's own recipe (the switches create the subscription) with its preview, or Choose a Tool |
| `recipe` | What it does in plain words (made from the recipe, never by a model), the field table, Set Up with the assistant, and on request a dry run drawn as the preview with the five checks |
| `tools`, `setup` | The source's subscribable tools and the refused ones with their reason; a tool's arguments drawn from its `input_schema` (`{{since}}` for a date-time), a dry run with `items_path` left empty, field pickers from its `paths` preselected by name, kind and grouping, then Try and Save |
| `runs`, `interval`, `fallback`, `rules`, `raw` | The last runs; pickers that apply at once (the page stays and the choice moves); triage rules (a subscription's, or with `c` a built-in source's own); the recipe's JSON |

- A switch that is off carries the confirmation of what turning it on does: what is pulled, how often, that it is kept on the hub,
  and which model triages or summarizes it, with its connection's name (`Service.ModelOf`). Turning one off asks nothing.
- **Built-in sources keep their own mechanism.** Their switches are `setConnectorFeed`, the same as `PUT /api/v1/connectors/{id}/settings`,
  so Settings › Connectors and an extension's page show a change made here and the other way round; their rules are the
  extension's `triage_rules` setting (as the `inbox.rules.<plugin>` persona setting writes it) or lark-cli's option; their
  mutes are the Inbox's (`inbox.mutes`, `inbox.unmute`). A source's latest read is kept in the manager's memory as each round
  reads it: a restart forgets it until the next round. A connector that was never set up (`not_configured`) is no source yet
  and is not listed.
- **Possible duplicates.** A subscription whose catalog `service` names what a built-in source reads (a mail account on that
  domain, a switched-on connector whose links live on that host) is marked on both rows, and the built-in source's page lists
  the subscription. Subscribing stays possible.
- **Recommended.** A source whose extension brings its own recipe and that nobody subscribed to has a switch on its row. It
  asks first, saying also that every new item is one call to the triage model, then creates the subscription with that recipe
  and turns its Inbox on. They stay off until then.
- **Set Up with the assistant** (in place of drafting with a model) is named as the app names the assistant in its composer
  ("Ask {name}…"): the apps send that name as `context.assistant` with these page requests only, never handed to an extension,
  and with `""` the pages say "the assistant". The key itself says the app opens the press: without it (iOS builds before
  #5278's iPhone part) the pages leave Set Up with … out, since such an app would drop the link and the
  row would do nothing (#5661); Choose a Tool and the rest stay. The press answers `open_url`
  `persona://ask?text=<percent-encoded>`; the apps take it only from these pages (`-subscriptions`), close them and send the
  text to the assistant's main conversation as the user, as Ask Persona does. The text is a `<persona-context kind="context">`
  block, then one sentence of what the user wants. The main conversation shows the block's title and body, so both are plain
  words: what it is about and a line of how it is set up now. `connector`, `source` and `subscription` (left out before there
  is one) are its attributes, which the model reads and the apps never draw (they keep attributes they have no field for,
  docs/protocol/protocol.md "Tags in messages"). The assistant
  reads the rest and changes it with its tools ("Assistant tools"), every save confirmed on the phone.
- **Preview What Comes In** pulls once (a dry run) and draws the items as the Inbox would list them: an avatar, the title,
  "sender: summary", and the section the rule fallback gives (`fallback_tier`; with a due date Needs You). Under each, its time,
  whether it is new, and what it could not read (`items[].problems`); under the list, what the recipe says that does nothing
  (`warnings`: an unknown field key, a field empty in every item), all localized by code. **Try Triage with the Model** asks
  first (the first three items to the Information filtering model, each one call, nothing kept), pulls once more, and shows the
  model's section, summary and reason on each row (`Service.PreviewThings` makes the things a delivery would; the assistant's
  `inbox.triage_preview` asks the model and keeps nothing).
- All text is the manager's, in the request's language (en, zh-Hans, ja); the service gives only codes, localized here.
- An app below UI version 6 gets the hub's usual downgrade (a switch becomes a row that turns it on or off; a recommended row
  keeps opening its page, its switch a row under it); a source icon only version 6 has is drawn as an older one, never as none.

### Assistant tools

The assistant has six tools for subscriptions (#5240), served by the manager beside its other assistant-only tools while
the service runs (`server/internal/extmgr/subscription_tools.go`; descriptions in `server/internal/prompts/mcp/subscriptions/`).
They call the same `Service` methods as the API and the pages, so a change made by the assistant is the change the
pages would make.

| Tool | What it does |
| --- | --- |
| `subscription_list` | Every subscription: label, source, tool, schedule, destinations, state and error, last and next run, new items, revision |
| `subscription_get` | One in full: its recipe, its newest runs and its newest held items (bodies cut to 300 characters) |
| `subscription_sources` | What can be subscribed to (cached catalogs, or one connector's read now with input schemas) and `recipe_format`, built from the code: the recipe and `fields` keys, paths, template variables, modes, kinds, `group_by`, intervals |
| `subscription_dry_run` | A dry run of a recipe, or of a change to a subscription's (an RFC 7386 merge onto its recipe), with the recipe tried |
| `subscription_save` | Create, update (label, recipe with `existing`, interval, daily time, triage rules, fallback tier, destinations, pause = both off) or delete (with its data or not), at the revision read |
| `subscription_run` | Run now, as Pull Now does, and wait up to two minutes for the run's result |

- **Every save is the user's.** The assistant's Persona tools are allowed in advance, so Claude Code asks nothing:
  `subscription_save` puts a permission card on the phone in the asking conversation (`subscriptionsui.Card`) and changes
  something only on the user's own approval (`Resolution.UserAllowed`). A card that expired, was closed or was answered
  for the user changes nothing and is not reported as a decline. Reading, dry runs and running now ask nothing, as on the
  pages.
- **The card** is in the user's language, in the pages' words: before → after for each setting that changes, the new
  recipe in plain words with the dry run's count and what `existing` does, and for a destination turned on the consent
  its switch asks (what is pulled, how often, that it is kept on the hub, which model triages or summarizes it). A
  delete says whether what it added goes too.
- **Checks first.** A recipe is dry-run before the card; one that fails, or has a key the format does not know, is not
  saved and nobody is asked. A stale revision is a conflict with the subscription as it is now, also before any card.
- **Undo.** Changes are recorded in `plugin_audit` with the actor `assistant`; an update's answer carries `previous`,
  the values that put it back with another confirmed save.
- A meeting's turn refuses `subscription_save` and `subscription_run` like any tool that is not a meeting's.

### Built-in sources switched to their default subscription

A built-in source's Inbox feed can be switched to its default subscription and back (#6265; `docs/design/proposals/inbox-sources-migration.md`, decisions 1–3). The code is `server/internal/subscriptions/migrate.go`.

- **Identity.** The default subscription has a fixed id, `sub_m_<connector>` (`MigrationID`); a random id's rest is hex, so no other starts so. While it is switched on (`migrated`, the time it was), the manager leaves the built-in out of the Inbox round (`readConnectors`). It hands the subscription's rows under the built-in's own source id, with the items' own threads and ids instead of `sub:<id>:` threads. The Inbox's item ids (`<source>:sha1(thread, last)`), mutes, policies, done threads, read errors and replies (through the built-in's connector) then match the built-in's, as long as the recipe maps the same thread and id.
- **The switch** is `Service.Migrate(Migration, on)`, given each source's default recipe.
  - **On** makes the subscription when there is none, both destinations off, and sets `migrated`; it is pulled at once. Its catch-up reaches a day back and comes as rows, never a digest. The rows the built-in already brought are known by their ids, so nothing between the built-in's last round and the subscription's first is missed, and nothing comes twice.
  - **Off** clears `migrated`. The subscription stays, not pulled, with its seen keys and items; the built-in is read again from the next round.
  - Deleting the subscription switches it back first.
- **Mixed versions and rollback.** `migrated` is kept with the subscription's run data; its own `to_inbox` and `to_kb` stay false. A manager from before this knows neither the field nor the id, sees an off subscription, and never pulls it. The built-in's own feed is never switched off, so rolling the hub back restores the built-in on its next round, with no duplicates. Its pages are drawn by the manager, so no client needs a feature flag.
- **The knowledge base** stays on the built-in's path (decision 3): `migrated` moves only the Inbox feed.
- **For the AI twin** a switched source's rows read as the built-in's: the round names no subscription (`via`) for them.
- **In the round** (#6381) a switched source's entry is its built-in's, as `readConnectors` makes it: whether it is a plugin's, `migrated`, grouping (`group`, `meta`), settling and its kinds. It has the subscription's rows and read error. Its `counts` are the subscription's only when a recipe makes its rows; a things recipe's are the connector's.
- **On the pages** (#6345). A built-in source with a default subscription has a How it's read section on its page, with a Read through its subscription switch ("通过订阅读取").
  - Turning it on asks once (decision 2): the Inbox gets the same rows, so nothing there changes, and it can be switched back. Turning it off switches back at once.
  - While it is on, the page's Inbox destination says "Through its subscription" instead of offering the connector's own switch, and a row opens the subscription's page (runs, schedule, a recipe "provided by the connector").
  - The default subscriptions are left out of the list and the relationship view, which reads the built-in as through its subscription, so a source shows once. A default subscription's own page has no Inbox switch, only a line saying it is switched on its built-in's page; its stored switches stay off.
  - A default subscription's recipe is the manager's (origin `persona`, "Comes with Persona"): its page offers a try, not the assistant, another tool or the raw recipe, so its rows stay the built-in's. A things recipe's row reads "Provided by the connector", never its tool's name.
  - The manager keeps each source's default (`migrations` in `server/internal/extmgr/inbox_sources.go`). iMessage's is a `things` recipe over its `inbox_things` tool (#6317), so its rows stay the ones its `Collect` makes. GitHub's (#6404) is a `things` recipe over the generic `inbox_things` (#6381), which answers its `Collect`; a test reads the same notifications both ways and compares the rows field by field.
  - A default can be held back until the source before it has had its week on the owner's hub (decision 6; `withheld`, now GitHub). Its source then has no How it's read section, and the manager refuses to switch it on; one already switched on keeps the section, so it can be switched back.

### Push sources

Some things reach the phone only as a notification or something seen in another app, with no connector behind them. A
push source (#3848, `server/internal/subscriptions/push.go`) takes them from the device instead of pulling: the iOS
Shortcuts action **Handle Notification** ([intents.md](../apple/intents.md) "Handle Notification") hands an item to
`POST /api/v1/subscriptions/-/push`, and it goes through the same destinations, triage and history as any subscription.

- **One per device.** Its connector is the reserved `-shortcuts` (no extension or connector id starts with `-`) and its
  source the device (`device:<id>`). It is made on the device's first push, or from the list's **Shortcuts on This
  Device** row on an iPhone or iPad (a push without items), with both destinations off like every new subscription. Its
  label is "Shortcuts on <device name>" in the user's language, its recipe origin `push`, its icon `bell`.
- **Items.** Only what the shortcut passes is sent: a title, a body, the app it came from, an optional link and time.
  Nothing reads other apps' notifications (iOS does not allow it). A title is the body's first line when it has none;
  an item with neither is refused. Titles are cut to 500 characters, bodies to 8000; a link that is not http(s) is
  dropped; a time more than 5 minutes ahead is now. The app is the item's sender, and the app with the title its
  thread, so one conversation's messages share an Inbox row and a knowledge-base chat. The kind is `sub_notice`.
- **Dedup.** `key` is the item's id (`push:<key>` in `subscription_seen`); the action sends a hash of the app, title,
  body and the minute, so a shortcut that fires twice adds one item. Without a key the manager makes the same hash.
- **Limits.** At most 20 items a push and 300 a device an hour (429 `rate_limited` with `retry_after`). Pushes are one at
  a time, so the same item pushed twice at once is kept once.
- **History.** Each push with items is a run (trigger `push`, `new` and `count`, no duration), so the runs page and the
  source's last line read as for others; it never fails, pauses or needs a recipe.
- **No pulling.** The scheduler skips it, and a recipe, an interval, a daily time, Run Now, a dry run and drafting are
  refused with 400 `push_source`. Turning its Inbox on ends the catch-up at once (nothing has to be pulled first): what
  came in the day before comes in then. Its page has the destination switches (their consent says what a shortcut
  sends instead of what is pulled), the last item, runs, how items come, triage rules, the fallback, Muted (the apps its
  items came from, muted as their sender) and Delete.
- **Older hubs and apps.** A hub without `subscription_push` answers 404 and the action says to update it. Pages drawn
  for the web and Android show the source like any other, and while nobody has one, a line saying how an iPhone or iPad
  sends items; only iOS (`context.platform` `ios`) offers to add the device's own.

Example automations (Shortcuts › Automation › New Automation, "Run Immediately"):

- **Message**: when a message from a chosen contact arrives, run Handle Notification with the sender as Title, the
  message as Body and "Messages" as App.
- **Email**: when an email from a chosen sender arrives, run Handle Notification with the subject as Title, the
  sender as App and the message's body as Body.

## Follow-up behavior

Both destination switches start off. Enabling a destination requires a full-access device and confirmation of what is pulled, how often, where it is stored, and which model triages it (the pages, #2041). Model recipe drafting (the API's only since #5278) sends a trimmed sample to the Information processing model; the purpose `subscription.recipe` runs on `Service.RecipeModel` (`{ok, provider, model}`, from the assistant's `models.usable`; `ok` false means drafting would answer `no_model`). Trying triage on a preview asks first on the pages, naming the triage model. `Service.ModelOf` answers the same for `kb` and `inbox.triage`, which the destinations' consent names. The sample is never stored. Destination storage, deletion, status pages and scheduling are not enabled by the catalog capability itself.
