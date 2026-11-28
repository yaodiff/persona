# Quick notes

The sample of `docs/extensions/core-only-features.md`: a feature added to a Persona Core install without changing
Core. It is an ordinary extension, installed onto a running core-only hub.

| It adds | Where |
| --- | --- |
| A page on the main navigation, **Quick notes** (en, zh-Hans, ja) | `persona-extension.json` (`ui`), `page.go`, `text.go`, `l10n/` |
| An app-facing route, `GET` and `POST /api/v1/quicknotes/notes` | `persona-extension.json` (`api`), `api.go` |
| A hub feature flag, `quicknotes` | `persona-extension.json` (`api.features`) |
| A stored value, `notes.json` in its data directory | `notes.go` |

Switched off (`personad extension disable quicknotes`), the page, the route and the flag are gone and `notes.json`
stays as it is; switched on again, the same notes are back. Uninstall is different: it deletes `notes.json` unless
`--keep-data` is passed.

Use the `sand` wrapper defined in `docs/extensions/core-only-features.md`: it gives every CLI command a clean
environment with the test home's explicit `PERSONA_HOME`, prints the resolved home and hub, and uses a read-only list
before each write. Do not change only `HOME`: an inherited `PERSONA_HOME` can otherwise send the install to another hub.

```sh
go test ./...                                 # its tests and the SDK's conformance check
sand extension test                           # the hub's checks, no hub needed
sand extension pack                           # dist/quicknotes-0.1.0.tar.gz
sand extension list                           # verify the printed sandbox target first
sand extension install "$PWD/dist/quicknotes-0.1.0.tar.gz" --yes
```

`python3 scripts/ci/core-sample.py` runs all of this on a core-only hub in a temporary home, then checks off, on again
and uninstall.

It builds against the SDK beside it (`../../go`); an extension of your own starts from `personad extension new`,
which writes a copy of the SDK next to your code.
