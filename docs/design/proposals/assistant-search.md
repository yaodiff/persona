# Proposal: find an assistant chat by what was said in it, and one rule for search in chat

Status: approved by the owner on 2026-11-13 (#6280), decisions in section 9. Building in order: the shared rule and web
search in chat (#6281, merged), the manager's index and endpoint (#6282), web (#6283), iOS (#6284); Android is #6285.
As built in #6282: no `ts` index (every search scans the text anyway), and the rebuild / empty command of section 5 is
#6340: `search-reindex` without `--purge` (emptying and rebuilding are one act, since the manager fills the index again).
Audience: everyday Assistant users (super rules 0 and 4). Agents' own history is out of scope (#757).

## 1. Problem

Searching the assistant's chats does not find what people remember: what they asked, or what the assistant said.

- **Finding a chat**: web ⌘K and the sidebar's "Search chats", web "All chats", the iOS sidebar (and the iPhone Duo's
  full-window search) and the Android drawer match only the title and `last_text`. That is the first 200 characters of
  the assistant's **last** reply (`snippet` in `server/internal/node/sessions.go`). The user's own messages, earlier
  replies and older history are never found. Every client filters the list it was pushed; neither the hub nor the
  managers search conversations (`GET /api/v1/kb-search` is the knowledge base).
- **Search in chat** (web and Android; iOS has none) searches what is loaded, and:
  - work the hub folded (`docs/protocol/protocol.md` ("Folded work")) is not searched until its summary is opened by hand;
  - a card answer's hidden `<persona-action>` tag is searched, and its snippet shows the raw tag and JSON while the
    bubble shows only the choice;
  - web and Android disagree: web strips quoted contexts (`<persona-context>`) and searches injected messages by their
    raw words; Android searches the raw user text (contexts and action tags included) and injected messages by their line.

Checked on 2026-11-13 against the mock hub, word by word on web and on an iPhone simulator, with one word placed only in
the title, only in a user message, only in a reply, only in tool output (folded and not), only in a thought and only in
early history.

## 2. One rule for what is searchable

A fixture shared by Go, web and Android tests (new: spec/fixtures/chat-search/cases.json, the way `spec/fixtures/injected`
is shared today) gives, for a list of items, each item's searchable fields as group + text. "Searchable" means what the
chat can show, including behind a disclosure; nothing that is never shown.

| Content | In the chat | Search in chat | Finding a chat (the index) |
|---|---|---|---|
| The user's words outside tags | the bubble | Messages | yes |
| `<pasted_content>` | its text | its text, without the tag | its text |
| A quoted context (`<persona-context>`) | a "Context" chip that opens | **Quoted** (its own group): title and text | no |
| A card answer's `<persona-action>` | never | no, and never in a snippet | no |
| `<persona-view-errors>`, attached image paths, `[Image #n]` | never | no | no |
| A file card (`<persona-file>`) | its name | its name | no |
| An injected message (`docs/protocol/protocol.md` ("Messages injected by Persona")) | one line (and its detail) | the line and detail, not its words for the model | no |
| The assistant's reply, cards included (their visible text, `AssistantCardText` in `sdk/go/wire`) | the reply | Messages | yes, without Markdown's markup (#7072) |
| Thoughts | open to read | Thinking | no |
| Tool names, input and output, folded or not | open to read | their groups | no |
| Notices, recaps, task events | shown | System events | no |

Snippets are cut from the cleaned text only, so no hidden tag can appear in one.

## 3. Finding a chat: the endpoint

`GET /api/v1/assistant/search?q=<text>&limit=30` on the manager, routed like the other `/api/v1/assistant…` routes
(`X-Persona-Manager` names the current assistant; the route is added to the forwarded list in `server/internal/assistantwire/bus.go`;
the logic is in `server/internal/assistant`, so the hub only forwards it).

- **Capability**: the manager names `assistant_search` in its hello; the hub lists the feature while the default
  manager names it. Without the feature, or on a 404 from the current assistant's manager (an older build), the apps
  keep today's title + `last_text` filter.
- **Query**: trimmed, at most 200 characters, split on spaces; every word must be in the same message (or the title).
  Matching is a substring match after NFKC and lower case: case and full / half width do not matter, and Chinese needs
  no word splitting.
- **Answer**:

```json
{"results": [{"session_id": "…", "machine_id": "…", "title": "…", "archived": false, "origin": "user",
              "updated_at": "…", "title_match": true,
              "hits": [{"item_id": "…#0", "seq": 1234, "role": "user", "ts": "…",
                        "snippet": "…I'd like to see the old lane", "ranges": [[21, 29]]}],
              "more_hits": 4}],
 "partial": false, "indexed": {"done": 140, "total": 145}}
```

- **Order**: chats whose title matches first (starts with, then contains), then by their newest hit. Up to 3 hits per
  chat, newest first; up to 30 chats. `ranges` are UTF-16 `[start, end)`, as search in chat counts them.
- **Which chats**: the ones "All chats" lists (`conversations` in `server/internal/assistant/conversations.go`, archived ones
  included): the main chat, the user's chats and the automatic ones (Inbox AI twin, scheduled tasks, the background
  supervisor), each with `archived` and `origin` so the apps mark them as "All chats" does. Never the health space.

## 4. The index

A manager migration (`docs/server/server.md` ("Migrations"); the number is picked after looking at every worktree):

```sql
CREATE TABLE assistant_search_items (
  machine_id text NOT NULL, session_id text NOT NULL,
  seq integer NOT NULL, item_id text NOT NULL,
  role text NOT NULL,                -- user | assistant
  ts timestamptz,
  body text NOT NULL,                -- the visible text by section 2; snippets are cut from it
  folded text NOT NULL,              -- body after NFKC and lower case; only matched
  PRIMARY KEY (machine_id, session_id, seq, item_id));
CREATE TABLE assistant_search_cursors (
  machine_id text NOT NULL, session_id text NOT NULL,
  history text NOT NULL,             -- the transcript's history token
  after_seq integer NOT NULL, indexed_at timestamptz NOT NULL,
  PRIMARY KEY (machine_id, session_id));
```

Titles are not stored: the manager has the list in memory and matches them at query time.

**Substring scan, no `pg_trgm` for now.** Measured on `postgres:18-alpine` (the hub's image, `en_US.utf8`) in a rolled-back
transaction with 100,000 synthetic rows (91 MB of mixed Chinese and English text):

| Query | Scan (`strpos`) | With a trigram GIN index |
|---|---|---|
| 4 Chinese characters | 87 ms | 1.9 ms |
| 2 Chinese characters | 110 ms | 170 ms (a two-character pattern gives the index nothing; it scans anyway) |
| an English word | 90 ms | 0.7 ms |

The index was 1.5 times the text (137 MB), took 32 s to build and makes every insert dearer; a `--database-url`
database may not have the contrib modules. A heavy personal install's visible text (the user's words and the replies,
without injected prompts or tools) is a few tens of MB at most: a scan of 10 to 25 ms. A later migration can add
`pg_trgm` for queries of three characters or more if the visible text passes about 100 MB or p95 passes 100 ms.

## 5. Keeping it current

- **When**: the manager already takes each computer's `sessions.changed` (`server/internal/assistant/fleet_events.go`). A chat
  of section 3's set whose `rev` or item count moved is queued once.
- **How**: one worker reads `session.items` with `after_seq` (about 500 items a page, `more_after` to go on), with lazy
  images and **without** `fold_work`, extracts section 2's text, and writes each page and its cursor in one transaction.
- **Rewritten transcripts**: a `history` token other than the cursor's (the file was replaced or rewritten and seqs
  start over) drops the chat's rows and indexes it again from 0.
- **Pace**: a chat that is working is read at most every 5 s, and at once when its turn ends: a new message can be
  found within seconds.
- **Backfill**: chats without a cursor, the most recently updated first, one at a time, 200 ms between pages; the cursors
  make it resume after a restart. Until it is done the answer has `partial: true` and `indexed`, and the apps say
  "Still going through older chats…".
- **Offline and gone**: an offline computer's chats wait for its next `sessions.changed`. A transcript the node says is
  gone, or a deleted chat, drops its rows. `personad assistant search-reindex` (`personad manager search-reindex` for a
  manager made in the app) empties it, and the manager reads its chats again from the start (#6340).
- **Several assistants**: each manager indexes its own chats in its own database.

## 6. Speed

- Server: p95 under 100 ms at tens of MB of visible text; a query is cut off after 2 s.
- Apps: the query goes 200 ms after the last key and cancels the one before. **Local first** (super rule 5): the list
  the app already has is filtered by title and last message at once, and the server's results merge in when they come.
- Indexing: one row per message in the normal course; the backfill is throttled so it never competes with a chat.

## 7. Privacy

- **Only what the user sees**: the user's bubbles and the assistant's replies. Never thoughts, tool input or output,
  messages injected for the model, hidden tags, system prompts, pictures or file contents.
- **Where**: the manager's own Postgres, beside its to-dos, inbox and knowledge base index (`docs/assistant/managers.md`
  ("The built-in manager's database")). No new files. A backup of the manager's database includes it.
- **Who**: the hub's device tokens, the owner's devices. Not in the extensions' scopes, and not a tool the model can call.
- **Logs**: never the query; counts and durations only. No query history is kept.
- **Snippets**: about 40 characters before the match and 80 after, from the cleaned text.
- **Removal**: with the chat (a chat gone from the list for a day); dropping the manager's database removes it;
  `search-reindex` empties it, and it fills again from the transcripts (#6340).

## 8. The apps

- **Web** ⌘K / "Search chats" and "All chats": a chat found by a message shows the snippet, match marked, as its second
  line; archived chats are marked. Opening it goes to `/persona/<id>?at=<item>`: the route keeps `at`, and the chat
  lands on that message, paging back (each page 10 exchanges, at most 20, cancellable, "Finding the message…") when it
  is not loaded; the message is ringed as search in chat rings it. If it is not reached, the chat stays at the latest
  message and says the message is further up. "All chats"' placeholder becomes "Search chats".
- **iOS** sidebar and the Duo's full search: a result is the title and one line of the snippet with the match in bold
  (a new design-system row, in `docs/design/DESIGN_SYSTEM.md`); opening it lands on the message through the existing
  `?at=` landing (`docs/apple/ios.md`), with a larger page budget for search. Offline, the local results stay and say why.
- **Android** (#6285): the same endpoint and rows, and the shared rule for search in chat.
- **Search in chat** (web now, Android in #6285): section 2's rule. Folded work is fetched for the search when the panel
  searches: the loaded folds' items through `from_seq` / `through_seq`, the request that opens a fold, two at a time
  and up to about 4 MB per chat, kept while the chat is open. Going to such a hit opens its fold as today. No protocol
  change, so it works with older hubs and in Agent sessions too. "Load earlier and keep searching" stays.

UI changes are checked in screenshots, dark and light, before they merge.

## 9. Decisions (owner, 2026-11-13)

1. A quoted context is searched in the chat, as its own group "Quoted"; finding a chat does not index it.
2. An injected message is searched, on every client, as the line and detail the chat shows.
3. Automatic chats are found like "All chats" lists them, with their origin.
4. No `pg_trgm` in this version.
5. iOS search in chat is not part of this work.

## 10. Tests each step adds

- **Go (#6282)**: the fixture's cases; Chinese, width and case; several words; the cursor's increments; a changed
  `history`; a transcript gone; archived in, health out; the order; snippet ranges; `partial`; no query in the logs;
  the route forwarded. The mock hub answers the same endpoint over its items, with a test.
- **Web (#6281, #6283)**: the fixture's cases, hidden tags, injected messages, quoted contexts, folded work searched
  without opening it and opened when gone to; the fallback, merging, snippets, cancelling, the `at` landing; an
  end-to-end browser check against the mock hub, one word per place.
- **iOS (#6284)**: the model with FakeHub (fallback, merging, `partial`) and a UI test against the mock hub (search,
  open, landed on the message).
