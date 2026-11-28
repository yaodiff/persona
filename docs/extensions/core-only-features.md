# Adding a feature to a core-only install

Who this serves: someone who runs Persona Core alone (a hub built without any feature module,
`docs/architecture/core-and-modules.md` "How to build and run the core alone") and wants a feature of their own on top
of it, **without changing Core code** and without rebuilding Persona. People who only use Persona never see any of this.

The short answer: **write an extension.** It is installed onto the running core-only hub, the hub and the apps are the
unmodified package, and switching it off takes its entry points away and leaves what it stored. The sample
`sdk/examples/quicknotes` does exactly this, and `scripts/ci/core-sample.py` follows this page with it on every CI run.

## Which route

| Route | Changes Core code | Rebuild | Installs onto a core-only package | Use it for |
| --- | --- | --- | --- | --- |
| **Extension** (out of process, `persona-extension.json`) | No | No | Yes | Everything a third party adds. This page |
| **In-tree module** (`server/internal/modules/<id>`, compiled in) | **Yes** | Yes, every binary | No: it makes a different build | A feature maintained in this repository |

An in-tree module cannot be added without a Core change today. Besides its own directory (free to change), it must add
`server/internal/modules/all/<id>.go` (its registration), a `module <id>` line in `schema/module-classification.txt`
and, to pass the module gate, its rows in `scripts/ci/module-e2e.py`; all three are tier-core paths of
`schema/core-paths.txt`, so the PR carries the `core-change` label, a `## Why this must change the core` section and
the owners' review (`CONTRIBUTING.md`, "Changing Persona Core"). Its web half is listed in `web/src/modules/index.ts`,
a file of the default panel. And `persona_core_only` leaves every in-tree module out by definition. That route is
`docs/architecture/core-and-modules.md` ("How to add a module"); the rest of this page is the extension route.

## What an extension can add to Core

All of these are extension points of the extension host, which is part of Core (`docs/extensions/extensions.md`,
"Extension points"). None needs a module.

| You want | Extension point | On a hub with the extension off |
| --- | --- | --- |
| An HTTP route for the apps or your own client, `/api/v1/<prefix>/…`, authenticated by the hub | `api` (needs `runtime.http`, `on_startup`) | 404, answered by the hub |
| A hub feature flag clients can gate on | `api.features` | Gone from the hello's and `/health`'s `features` |
| An entry in the main navigation that opens a page (web, Mac, iOS, Android) | `ui` with `placement: nav_page` | Not listed by `GET /api/v1/extensions/-/ui`, so no client shows it |
| A card on Overview, a section on a computer's page, an Inbox item's body, a widget | `ui`, other placements | Not listed |
| A stored value | The extension's data directory (`data_dir`), or the knowledge base through `kb.*` | Kept, untouched |
| Settings and secrets the owner fills in | `configuration`, `secrets` | Kept, hidden with the extension |
| Something for the assistant: a tool, a skill, instructions | A connector's `agent_tools`, `assistant` (skills, a gated command and its `tool`), `mcp_servers`, `skills`, `prompts` | Gone at the assistant's next start |
| To hear what happens on the hub | `events` | Not delivered |

What it cannot add is under "What you cannot do yet" below.

## The sample: Quick notes

`sdk/examples/quicknotes` is a complete extension in about 400 lines of Go:

| Part | Where | What it shows |
| --- | --- | --- |
| A server capability | `api.go`: `GET` and `POST /api/v1/quicknotes/notes` | The request body is decoded once, strictly, into a struct; an unknown field or a mark outside the set is a 400 |
| A hub flag | `persona-extension.json`: `"features": ["quicknotes"]` | Announced only while the extension is on |
| A visible entry | `persona-extension.json`: a `nav_page`; `page.go` draws it | The web sidebar shows **Quick notes**; the page's words come in en, zh-Hans and ja (`text.go`, `l10n/`) |
| A stored value | `notes.go`: `notes.json` in the data directory | Written whole (temporary file, then rename); read back unchanged by a new process |
| Typed values | `notes.go`, `text.go` | `NoteID` is a named type only the book mints; `Mark`, `Lang` and `Text` are closed sets with a parser that refuses anything else; no `map[string]any`, no bare string for a state |

Keep to the same typing rule in anything you hand to Core or to a client: closed sets, named id types, strict decoding.

## Step by step

You need the core-only programs (`personad`, `persona-hub`, `persona-manager`), a running core-only hub, and Go for a
Go extension. `personad` carries the SDK and the templates, so nothing else of this repository is needed. For a
completely disposable proof, `python3 scripts/ci/core-sample.py` builds and starts that hub itself, follows these steps,
and removes its own container and temporary home; the commands below are the same steps by hand.

> **Test hubs: say which hub, every time.** `personad extension …` reads the hub's address and token from
> `$PERSONA_HOME/hub.json`, and only when `PERSONA_HOME` is unset from `$HOME/.persona/hub.json`. A shell on a machine
> that also runs a real hub (or an Agent started by one) can **inherit `PERSONA_HOME`**, so changing only `HOME` does
> not move the command: it can install onto the real hub. That happened while this sample was being developed; the
> owner removed the mistakenly installed extension. Never rely on `unset`, and never read or use the real home for the
> test. Run every CLI command with a clean environment and an explicit `PERSONA_HOME`.

Saying it is not enough on its own: a write made between the check and the request would still land wherever the
environment points. So every write is also **bound by the CLI itself**. `personad extension` prints the resolved home
and hub before a write and takes `--expect-home <absolute path>` and `--expect-hub <host:port>`: when either differs it
stops before any preview, request or write, with nothing changed (#16302). Reads (`list`, `show`) take no flags.

Set these to **new sandbox paths**, never to an existing hub's paths. `TEST_HUB` is the test hub's `listen` address,
printed by the first `sand extension list` below.

```sh
TEST_HOME=/absolute/path/to/a/new/test/home
TEST_HUB=127.0.0.1:18661                       # the test hub's listen, from the first line printed below
PERSONAD=/absolute/path/to/the/core-only/bin/personad
sand() {   # every command: print the home the CLI will read, then run it
  env -i HOME="$TEST_HOME" PERSONA_HOME="$TEST_HOME/.persona" PATH="$PATH" \
    python3 -c 'import json, os; p=os.environ["PERSONA_HOME"]; print("PERSONA_HOME="+p+" hub="+json.load(open(p+"/hub.json"))["listen"])' &&
  env -i HOME="$TEST_HOME" PERSONA_HOME="$TEST_HOME/.persona" PATH="$PATH" "$PERSONAD" "$@"
}
sandw() {  # a hub write: the CLI refuses any home or hub but this sandbox's, before any preview or request
  env -i HOME="$TEST_HOME" PERSONA_HOME="$TEST_HOME/.persona" PATH="$PATH" "$PERSONAD" "$@" \
    --expect-home "$TEST_HOME/.persona" --expect-hub "$TEST_HUB"
}
sand extension list
```

The first command is read-only. Continue only if its first line names the test home and the test hub's `listen` address;
otherwise stop, and set `TEST_HUB` to that address. Before **every** write below (`install`, `disable`, `enable`,
`uninstall`), run `sand extension list` again and make that same check, **and** use `sandw` so the write itself is bound
to the same target. `scripts/ci/core-sample.py` does both, and one of its steps deliberately passes a wrong
`--expect-home` to show the refusal.

1. **Start from the template or the sample.** A new extension of your own:

   ```sh
   sand extension new mynotes --template go --name "My notes"   # writes ./mynotes with a copy of the SDK
   ```

   To run the sample as it is, work in `sdk/examples/quicknotes` of a checkout (it builds against `sdk/go` beside it).

2. **Declare what you add** in `persona-extension.json`. The sample's whole contribution:

   ```json
   "runtime": {"command": {"linux-amd64": "bin/quicknotes-linux-amd64"}, "http": true},
   "activation_events": ["on_startup"],
   "contributes": {
     "ui":  [{"id": "quicknotes", "title": "Quick notes", "placement": "nav_page", "icon": "doc"}],
     "api": [{"id": "quicknotes", "prefix": "quicknotes", "features": ["quicknotes"]}]
   }
   ```

   The hub never gives an extension a prefix it owns itself, and never publishes an extension's flag in the `module.`
   namespace. Translate the name and the page's
   title in `l10n/en.json`, `l10n/zh-Hans.json` and `l10n/ja.json` (`docs/extensions/extensions.md`, "Translations").

3. **Write the program.** `ext.Main(ext.Program{…})` with `UI` (the page: `Render` and `Action`), `HTTP` (the route's
   handler) and `Init` (open your store in `init.Info().DataDir`). Draw text in `UIRequest.Locale`'s language.

4. **Test it without a hub.**

   ```sh
   go test ./...              # your tests and the SDK's conformance check (exttest.Check)
   sand extension test        # the manifest and package as at install, and the hub's stricter protocol checks
   ```

5. **Pack and install** onto the running core-only hub, on the hub machine:

   ```sh
   sand extension pack                      # builds each platform of the manifest, writes dist/<id>-<version>.tar.gz
   sand extension list                      # read-only: verify the printed sandbox home and hub first
   sandw extension install "$PWD/dist/quicknotes-0.1.0.tar.gz" --yes   # bound to $TEST_HOME/.persona and $TEST_HUB
   ```

   Give `install` an absolute path: the hub opens the file, not the command. Without `--yes` it shows what the
   extension may do and asks. The Admin UI's **Extensions** page installs the same package.

   Try the guard once with a wrong home: it must exit non-zero, name both homes, and change nothing, so a following
   `sand extension list` is exactly what it was before the attempt. (`sandw` cannot show this: it appends its own
   flags, and the CLI refuses a flag given twice, so write the call out.)

   ```sh
   env -i HOME="$TEST_HOME" PERSONA_HOME="$TEST_HOME/.persona" PATH="$PATH" "$PERSONAD" \
     extension install "$PWD/dist/quicknotes-0.1.0.tar.gz" --yes \
     --expect-home /not/this/sandbox --expect-hub "$TEST_HUB"    # refused: "PERSONA_HOME is …, not the expected …"
   sand extension list                                           # unchanged: nothing was installed
   ```

6. **See it work.** With `<hub>` the test hub's address and `<token>` a credential of that test hub:

   ```sh
   curl -s <hub>/api/v1/health                                   # "features" now has "quicknotes"
   curl -s -H "Authorization: Bearer <token>" <hub>/api/v1/extensions/-/ui      # the nav_page entry
   curl -s -H "Authorization: Bearer <token>" -d '{"text":"first","mark":"important"}' <hub>/api/v1/quicknotes/notes
   curl -s -H "Authorization: Bearer <token>" <hub>/api/v1/quicknotes/notes
   ```

   Open the test hub's web app: **Quick notes** is in the sidebar, and its page lists the note. The route answers a
   moment after `install` or `enable` returns (the list says `running`).

7. **Switch it off and on.**

   ```sh
   sand extension list                      # verify the target before the write
   sandw extension disable quicknotes       # flag gone, entry gone, the route 404; notes.json untouched
   sand extension show quicknotes           # "data_dir": where the stored value is
   sand extension list                      # verify the target again
   sandw extension enable quicknotes        # the same notes are back
   ```

   The apps' **Settings › Extensions** has the same switch. It takes effect at once: no hub or manager restart.

8. **Uninstall, if that is what you mean.** This is different from switching off: without `--keep-data`, it explicitly
   deletes the extension's data directory, including `notes.json`.

   ```sh
   sand extension list                      # verify the target before the write
   sandw extension uninstall quicknotes     # entry and route gone; notes.json deleted
   ```

   Use `sandw extension uninstall quicknotes --keep-data` instead only when you intentionally want the data directory
   left on disk. In either case the flag, entry and route are gone and Core remains usable.

## Switched off: what goes, what stays

The owner's rule for every optional feature (`docs/architecture/core-and-modules.md`, "A module that is off keeps what
the user has") holds for an extension too, and the gate checks each line:

| | Off (`disable`) | Uninstalled |
| --- | --- | --- |
| Feature flag, navigation entry, route (reads and writes), tools, events | Gone | Gone |
| Core: the assistant, Agents, to-dos, computers, every other extension | Unchanged | Unchanged |
| The data directory (`notes.json`) | **Kept, byte for byte**; the same notes are read when it is on again | Deleted, unless `uninstall --keep-data` |
| Settings and secrets | Kept | Deleted |
| What it wrote into a Core store (the knowledge base) | Stays readable in Core's own pages and search | Stays |

Off never deletes and never rewrites. Design for the part Core cannot do for you: **while your extension is off, no
Core page reads its data directory.** The file is there (`personad extension show <id>` names the directory) and comes
back with the extension; if the user must keep seeing the data in the apps while the feature is off, write it where
Core reads, for example onto a knowledge base entry (`kb.update`, permission `kb.write`), which stays listed, readable
and searchable without you.

## Compared with a module's switch

| | Extension | In-tree module |
| --- | --- | --- |
| Switch | `personad extension enable \| disable`, **Settings › Extensions**; immediate | The `modules` settings row or a build tag; at the next manager start |
| Flag | Its own name from `api.features` (never `module.…`) | `module.<id>`, with the `modules` marker |
| Client entry | The apps list extension pages from the hub; nothing to register in a client | A registration in each client's module list |
| Client code | None: a declarative View every client draws | Native code per client |

## Checks

- `go test ./...` in the extension: `TestConformance` (the protocol and every page's View), the route, the stored
  file, the closed sets and the three languages.
- `python3 scripts/ci/core-sample.py`: builds the core-only programs, starts a hub in a temporary home on a disposable
  Postgres container and runs steps 4 to 8 with `sdk/examples/quicknotes`: absent, a refused wrong `--expect-home`,
  installed, off, on again and uninstalled. Every hub write carries `--expect-home` and `--expect-hub` for the sandbox,
  and the refusal step shows the CLI stops a mismatched write before it reaches the hub. One PASS / FAIL / TIMEOUT line.
  `scripts/ci/server.sh build` runs it ('Core-only sample extension'), with the sample's own tests
  ('Sample extension tests').

## What you cannot do yet

Each of these needs a Core change today; none is worked around by the sample (#16244 has the list).

1. **Be a module to the clients.** An extension has no `module.<id>` flag and no place in the module registry: the
   kernel loads only built-in manifests, and an extension's report may not publish the `module.` namespace.
2. **A typed assistant tool** like a module's `modapi.Host.AddMCPTool`. An extension reaches the assistant through a
   connector's `agent_tools`, a gated command or an `mcp_servers` command.
3. **Native client code**: a native screen, a chat card kind, a composer action, a settings category. An extension
   draws declarative Views in the slots of "UI slots".
4. **Have Core show your own stored data while you are off** (above).
5. **`personad extension test` for a program that always serves HTTP**: the command starts the program without a
   socket and the Go SDK then stops. Serve HTTP only when `ext.Env().Socket` is set, as the sample does.
6. **The bounded `storage.*` store from Go**: only the TypeScript SDK has its client; a Go extension keeps a file in
   its data directory.
