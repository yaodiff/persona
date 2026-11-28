# Links to Persona's pages (deep links)

One route table for every link that opens a page of Persona: the links the hub writes (Inbox rows, Lark cards, notices
in conversations, the assistant's answers), the links people paste, and the links other apps hand to Persona. The same
link opens the same page on the iPhone, on the web, in the desktop app and on Android (#9926).

Who it serves: everyone. A tap on a link to one of your own pages opens that page in the app you are in, never a
browser page of the same thing.

## The two forms

**A page's canonical form is its path in the hub's web app** (`web/src/lib/router.ts`). Every page has exactly one
canonical path, and the same path has two forms:

| Form | Shape | Example |
| --- | --- | --- |
| Web link | `<public_url><path>` | `https://hub.example.com/persona/c-7f3a` |
| App link | `persona://open<path>` | `persona://open/persona/c-7f3a` |

The app link is the web link with `https://<hub>` (and any sub-path the hub is served under) replaced by
`persona://open`. Nothing else changes: the path, its query and its escaping are the same.

- **Links the hub sends out are web links.** Lark cards accept only http(s), and a web link opens in a browser where
  Persona is not installed. The apps recognise their own hub's web links and open them natively.
- **App links are for the apps themselves**: a notification's target in the desktop app, a link handed from one app to
  another. The hub does not write them into content.
- **No Universal Links.** An iOS app lists its associated domains when it is built, and every hub is self-hosted on its
  own domain, so a released build cannot list them. Each app recognises its paired hubs' addresses instead (below).

## Route table

Query keys not listed for a row are dropped (not passed on, not acted on). Fragments are ignored.

| Page | Canonical path | Query keys | iPhone | Web, desktop | Android |
| --- | --- | --- | --- | --- | --- |
| Persona (the assistant's main page) | `/persona` | | Persona | in place | native |
| New conversation | `/persona/new` | | new conversation | in place | native |
| A Persona conversation | `/persona/<id>` | `machine`, `manager`, `at` | native | in place | native |
| An Agent's conversation | `/s/<machine>/<session>` | `at`, `view` | native | in place | native |
| Agents | `/agents` | | Agents tab | in place | native |
| Inbox | `/inbox` | | Inbox | in place | native |
| An Inbox item | `/inbox/<id>` | `manager` | native | in place | native |
| To-dos | `/todos` | `view`, `mode`, `date` | To-dos | in place | native |
| A to-do | `/todos/<id>` | | native | in place | native |
| Knowledge | `/kb`, `/kb/<kind>` | | Knowledge | in place | native |
| A knowledge entry | `/kb/<kind>/<id>` | | native | in place | native |
| Computers | `/machines` | | Computers | in place | native |
| A computer | `/machines/<id>` | `range` | native | in place | native |
| A computer subview | `/machines/<id>/<subview>` | `path` for files, `range` for monitoring, `mode=reclaim` for storage | web only | in place | web only |
| Settings | `/settings` | `tab`, the tab's item key, `manager` | the matching page | in place | native |
| An extension's page | `/extension/<extension>/<ui>` | `state` | native | in place | native |

On the web, a bare computer address opens Work. Existing links with a monitoring range
(`1h`, `6h` or `24h`) open Monitoring. The supported subviews are `work`, `monitoring`, `files`,
`storage`, `diagnostics`, `procs`, `services`, `toolchain`, `network` and `management` (`config` is an alias).
The legacy `#engine` bookmark on a computer opens Management. An unknown subview shows Page not found. Older fleet history entries carrying a reclaim request resolve
in place to `/machines/<id>/storage?mode=reclaim`. Opening reclaim displays the map and its suggestions. The older `/cleanup` and `/storage/cleanup`
suffixes select the same recovery view. Files accepts a URL-encoded directory in `path` (also accepted as `dir`).
The `fs` subview opens Files on the addressed computer. Switching computers starts Files at the new computer’s home.

- `<kind>` is one of `projects`, `topics`, `chats`, `people`, `docs`, `meetings`, `events`, `recordings`; a meeting is
  `/kb/meetings/<id>` and a recording `/kb/recordings/<id>`. An id that holds `/` is one escaped segment (`%2F`).
- `at` names one message to land on (`?at=<item id>`; Claude's ids hold `#`, written `%23`).
- Settings tabs are the web's (`web/src/views/settings/nav.ts`): `general`, `persona`, `prompts`, `notifications`,
  `models`, `agents`, `channels`, `connectors`, `sources`, `extensions`, `shortcuts`, `advanced`, `devices`, `hub`, `managers`,
  `knowledge`, `avatar`, `labs`. Four have an item key: `models` → `purpose`, `channels` → `channel`, `extensions` →
  `extension`, `connectors` → `connector`. An extension's settings are `/settings?tab=extensions&extension=<id>`. An
  unknown tab opens Settings. The iPhone opens the page it has for a tab, else Settings.
- The assistant's pages (Dashboards, `docs/assistant/dashboards.md`) are an extension's pages under the reserved id
  `-pages`: the Dashboards index is `/extension/-pages/index`, one page `/extension/-pages/pg_0123456789abcdef`
  (`persona://open/extension/-pages/pg_0123456789abcdef`). The apps draw them over HTTP only, never the native page
  stream, and take the header's title from the drawn page, so a page not pinned in the sidebar shows its title, not
  its id.
- A page the iPhone has no screen for opens in the in-app web card, as any other link to the hub does.
- **Not in the table**: every other path on the hub (`/admin`, `/share/…`, `/screen/…`, the web's own pages such as
  `/overview` or `/history`). The iPhone and Android open them in the web card; the web and the desktop app treat them
  as their own pages.

### Older forms every client keeps accepting

These name the same pages and are read wherever links are read. Nothing new writes them.

| Older form | Page |
| --- | --- |
| `/`, `/a` | Persona; Agents |
| `/t/<machine>/<space>/<slot>/<session>` | that Agent's conversation |
| `/todos?id=<id>` | that to-do |
| `/settings/sent`, `/settings?tab=sent` | `/settings?tab=connectors&connector=-sent` |
| `/plugin/<extension>/<ui>` | that extension's page |
| `persona://open?route=<escaped path>` | the path (the desktop app's first form) |
| `persona://open?machine=<m>&session=<s>` | `/s/<m>/<s>` (the Live Activity's rows) |
| `persona://session/<machine>/<session>[?at=<item>]` | `/s/<machine>/<session>[?at=<item>]` |
| `persona://task/<id>` | `/todos/<id>` |
| `persona://kb/<kind>/<id…>` | `/kb/<kind>/<id>` (the rest of the path is the id) |
| `persona://settings/extensions/<id>` | `/settings?tab=extensions&extension=<id>` |
| `persona://settings/extensions/<id>?set=<url-encoded JSON object>` | that settings page with the proposed ordinary configuration values filled in (Web; nothing is saved until Save) |
| `persona://extension/<extension>/<ui>[?state=]` | `/extension/<extension>/<ui>[?state=]` |

The older product name's scheme and paths are accepted as these are (`docs/protocol/rolling-upgrade-compatibility.md`).
The `set` form is deliberately narrow: one object of known ordinary configuration fields, at most 2 KiB decoded, with no
secrets, entry lists, unknown fields, extra path, extra query or fragment. Web applies it to the existing extension form;
the saved configuration stays the base, and Save is the only write. Apple and Android do not apply this prefill yet.

## Which hub

- **A web link names its hub by its address.** An app opens it natively only when the address is one of a paired
  hub's: the address it was paired with, addresses the user added, its `public_url` (the hub's hello) and its linked
  hubs' endpoints (`docs/protocol/multi-hub.md`). Host and port must match (host case-insensitive, an IPv6 host without
  brackets, the scheme's default port when none is written); the scheme may differ. A hub served under a sub-path
  (`https://<host>/<prefix>`) has its pages under that prefix. Any other address is an outside link.
- **An app link may carry `hub=<hub id>`** (the hub's id from its hello). Without it, the page opens on the hub the app
  is showing. `hub` is no page's query key: it is read and dropped before the path is handed on.

## Not routes: action links

These `persona://` links do something rather than open a page. They are not in the table, and each keeps its own
checks; none of them is ever written by the hub's link builder.

| Link | What it does | Checks |
| --- | --- | --- |
| `persona://pair?url=&token=` | pairs the app with a hub | asks "Connect to <host>?" first (#9929) |
| `persona://link?…`, `persona://peer?…`, `persona://connector?…` | a computer's, a linked hub's, a connector's pairing | not for this device; pasted where they belong |
| `persona://password-login?state=&code=` | finishes a sign-in | its `state` |
| `persona://browser-login?hub=&request=` | a browser asks to sign in (the web sign-in page's code) | read only by the app's own scanner in Settings › Devices; opened as a link it signs nothing in ([protocol.md](protocol.md) "Browser QR sign-in") |
| `persona://shortcut-callback/…` | a Shortcut Persona ran came back | its token |
| `persona://ask?text=`, `persona://sources-graph` | the subscriptions pages' actions | only from those pages |
| `persona://compose?prompt=&text=` | an extension page's own quick prompt, into the assistant's composer without sending | only that extension's declared quick prompt; id and text must match it exactly (Web and Android today; iOS not yet) |
| `persona://widget/<kind>`, `persona://record…`, `persona://allday?action=` | Home Screen widgets, Live Activities, watch complications | Persona's own surfaces |
| `persona://inbox/<id>/source` | an extension's link back to the Inbox item it is drawn for | only that item (`checkOpenURL`) |
| `persona://tracks/<machine>/<space>/<session>` | the `space-conversations` extension opens an existing assistant conversation | only that extension; bounded ASCII path segments, no query, fragment or credentials; Health and arbitrary internal routes refused |
| `persona://file/<machine>/<path>` | a file in a card | cards only |

## Security

- **A link only opens a page.** Every query key a row lists is view state: which message, which tab, which view. Keys
  that do something (`token`, `hubs`, `import`, `add`) are in no row, so a link cannot sign in, switch hubs, import a
  provider or start adding anything. A fragment is ignored.
- **Only a paired hub's address counts as the hub.** A look-alike host, another port, or a link with a user name or
  password is an outside link (a link with a user name or password is not opened as a page at all).
- **A malformed app link is refused**: a route that does not start with a single `/`, holds a backslash or a control
  character, or would leave the hub when resolved against it.
- **Extensions stay limited.** An extension's `open_url` is still only https, its own settings, the Inbox item it is
  drawn for, or the `space-conversations` extension's one narrow conversation handoff (`docs/extensions/extensions.md`).
  An https link to the user's hub that is in the table opens that page in the app, which only shows a page.
- **The assistant's links.** The model is told to copy the links it is given, never to build its own; an assistant
  view's `open` is still checked against https and Persona's own forms (`CheckAssistantOpen`).

## What each client does

- **iPhone**: one parser for the three inputs (a paired hub's web link, an app link, an older form) and one entry that
  every surface opens links with: chats, the Inbox (an item's source link and its Timeline), knowledge, to-dos, mail and
  extension pages, and links from outside the app. A page in the table opens natively; anything else opens in the web
  card as before (#9928). The web sign-in page's `persona://browser-login` code, read by the Camera app, only says where to scan it (Settings › Devices › Scan Browser Sign-in Code, #10293); approving a browser happens only in that scanner.
- **Web**: links to its own hub go through the router's own `parse` and open in place; app links and older forms in
  messages and cards open the same pages (#9930).
- **Desktop**: the packaged app registers `persona://`. An app link's path is handed to the web app in the window,
  which applies the table; the desktop app itself only refuses malformed links and drops query keys no row lists, in
  both app-link forms (`persona://open<path>` and `persona://open?route=`; #9931, #10281).
- **Android**: `core/navigation` `HubLinks` reads paired hubs' web links, canonical and older app links, and every
  route-table row. Cold starts and links in messages use the same native destinations. An app link's `hub` selects its
  paired group; the page opens after that group's session is ready. Action links keep their own parsers (#9932).
- **The hub**: one builder writes every path in the table, and the web link from `public_url` (#9927). Push payloads
  keep their typed keys (`machine_id`, `session_id`, `todo`, …), which each app maps to the same pages.

## Shared cases

`spec/fixtures/deep-links/cases.json` (rules in its README) holds the cases every client checks:

- `build`: a page to its canonical path, and the web and app links for a hub address (the hub's builder).
- `parse`: a link and a set of paired hubs to the page it opens, or to why it opens none (`external`, `not-a-page`,
  `unsafe`). The iPhone, the web and Android read every case; the desktop app reads the app-link cases.
