# Installing Persona Core alone

Persona Core is the hub API and the data model for Agents, sessions, tasks and to-dos, without any built-in feature
module ([core-and-modules.md](../architecture/core-and-modules.md)). The **core edition** is that product as install
packages: the same programs as the full release, built with `persona_core_only`, for people who build their own
panels and modules on Core. Everyday installs use the full edition ([getting-started.md](getting-started.md)); nothing
on this page changes it.

Who this serves: people who work with Agents directly and develop on Persona. The apps and the web client connect to
a core-only hub as they do to any hub and hide what its modules would add (the hub hello's `modules` marker and
`module.<id>` flags).

## Packages

A release has two editions, side by side in one flat place (the publisher's `dl/`, or a GitHub release's assets).
The full edition keeps every name it has; the core edition's names start with `core-`.

| | Full edition | Core edition |
| --- | --- | --- |
| Manifest | `latest.json` (`dl/<version>/manifest.json`) | `core-latest.json` (`dl/<version>/core-manifest.json`), with `"edition": "core"` |
| Checksums, version | `SHA256SUMS`, `VERSION` | `core-SHA256SUMS`, `core-VERSION` |
| Hub and manager | `personad-<os>-<arch>.gz`, `persona-hub-…`, `persona-manager-…` | `core-personad-<os>-<arch>.gz`, `core-persona-hub-…`, `core-persona-manager-…` |
| Node | `persona-node-<os>-<arch>[.exe].gz`, `persona-link-…` | `core-persona-node-<os>-<arch>[.exe].gz`, `core-persona-link-…` |

Both cover the same platforms: Linux and macOS on amd64 and arm64 for every program, Windows on amd64 and arm64 for
the node programs (`personad`, `persona-node`, `persona-link`). The core edition has no `persona-iphone-bridge`: it
belongs to a module. `persona-hub` and `persona-link` link no module, so the two editions of them differ only in the
edition their build info records.

Once unpacked, a file has the same name in both editions (`~/.persona/bin/personad`). What tells them apart is the
build tag in the program's Go build info, which is readable without running it:

```sh
go version -m ~/.persona/bin/personad | grep -- -tags    # the core edition lists persona_core_only
```

## Install the hub by hand

This is the path the install check runs (`scripts/ci/core-install.py`, "The install check" below): a clean Linux
machine or container, no Docker and no service manager on it, and a Postgres it can reach.

You need `python3`, `gzip`, `sha256sum` (or `shasum`) and `curl` or `wget`, and a Postgres 16 or newer with a user that may
create databases (the built-in manager keeps its data in a second database beside the hub's, `<name>_manager`).

Say where the release is, how this machine downloads, and where the database is:

```sh
SITE='<the publisher's site>'
fetch='curl -fsSL'
DATABASE_URL='postgres://persona:<password>@127.0.0.1:5432/persona?sslmode=disable'
```

`fetch='wget -qO-'` does the same on a machine without curl. `$SITE/install.sh` is the installer and `$SITE/dl/` the
downloads.

Write the hub's configuration, with a token of its own ([config.md](config.md)). The TLS listener lets Persona Link
pair later; with no certificate files, the hub manages its own self-signed identity ([network.md](network.md), "TLS
and public-key pinning"):

```sh
mkdir -p ~/.persona && chmod 700 ~/.persona
TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
cat > ~/.persona/hub.json <<JSON
{"listen": "127.0.0.1:18660", "token": "$TOKEN", "public_url": "http://127.0.0.1:18660", "database_url": "$DATABASE_URL", "machines": [], "public": {"tls_listen": "127.0.0.1:18662"}}
JSON
chmod 600 ~/.persona/hub.json
```

Install the core edition. With a `hub.json` there, the installer brings `persona-hub` and `persona-manager` with
`personad`, each checked against `core-SHA256SUMS`. `PERSONA_SKIP_HERDR=1` leaves the Agent terminal engine out, for
a machine that runs no Agents itself; drop it on one that does.

```sh
$fetch "$SITE/install.sh" | PERSONA_SKIP_HERDR=1 sh -s -- --core
```

Start the hub. It starts the manager beside it. `PERSONA_LOCAL_NODE=0` is for the same machine without Agents; drop it
to have the hub run a node here.

```sh
PERSONA_LOCAL_NODE=0 nohup ~/.persona/bin/persona-hub run > ~/.persona/hub.log 2>&1 &
```

Check it, once the hub has had a few seconds to start: the hub answers, the manager loaded no module, and the install
says which edition it is.

```sh
$fetch http://127.0.0.1:18660/api/v1/health
grep 'modules: loaded none' ~/.persona/hub.log
cat ~/.persona/bin/.edition
```

The first prints `{"ok":true…}`, the second the manager's line, the third `core`. The hub listens on this machine
only; [network.md](network.md) says how to open it to your phone, and `~/.persona/bin/personad hub pair` prints the
pairing link.

## The usual install, with Docker and systemd

On a machine with Docker and a service manager, the installer and `personad hub setup` do everything above (the
database container, `hub.json`, the services), as for the full edition ([getting-started.md](getting-started.md)):

```sh
curl -fsSL <the publisher's site>/install.sh | sh -s -- --hub --core
```

The install check does not run this path: `hub setup` needs Docker and systemd or launchd, which its clean container
does not have.

## A computer's node

On the hub machine, make a pairing link for the new computer, then copy it to the computer:

```sh
~/.persona/bin/personad hub pair --machine <machine id> --add
```

Install the node's programs and pair them with the printed link (kept in `$PAIR_URL` in this example):

```sh
PAIR_URL='<the printed pairing link>'
$fetch "$SITE/install.sh" | PERSONA_SKIP_HERDR=1 sh -s -- --core --node --pair "$PAIR_URL"
```

This installs `persona-node` and `persona-link` of the core edition, as `--node` does for the full one
([link.md](link.md), "Install the node service"). The node's modules (Messages, iPhone control, screen sharing, the
Office commands) are not in it.

On a machine without a service manager, such as the install check's clean container, the installer still installs and
pairs the programs, then exits 1 and prints these alternatives. Run them now:

```sh
nohup ~/.persona/bin/persona-node node --attach > ~/.persona/node.log 2>&1 &
nohup ~/.persona/bin/persona-link run --log ~/.persona/link.log > /dev/null 2>&1 &
```

`persona-link status --json` reports the pairing and connection; the computer appears in the hub's **Machines** list
([link.md](link.md), "Without systemd"). A real machine needs a supervisor that starts both programs again after a
restart, as that section explains.

## Updates stay core-only

An install keeps the edition it was installed as. Nothing turns a core-only install into the full edition, or a full
install into a core-only one, unless a person asks for it ("Changing the edition" below).

- **A build knows its edition** (`update.ThisEdition`, from the build tag) and reads only its own manifest: a
  core-only hub asks its update source for `core-latest.json`, a full one for `latest.json`, as always. A full install
  never requests a `core-` name, so the full feed and everything that reads it are unchanged.
- **A manifest says its edition**, and a build refuses one of the other edition whatever name it was served under
  (`Manifest.ValidateEdition`). `personad release verify` refuses it too, so a publisher cannot list a full build in
  the core's manifest unnoticed: `scripts/release/core_edition.sh pack` checks every program's build info and that
  neither edition's `personad` takes the other's manifest.
- **A swap checks the file**: before a program is replaced, the new file's build info must say the edition of the one
  it replaces (`update.CheckReplace`, `update.CheckBinary`). This covers the hub's own update (`personad`,
  `persona-hub`, `persona-manager`) and a node's update over RPC, where the node itself refuses the file.
- **Nodes**: a node says its edition in `node.info` (left out by a full build, so nodes from before editions are
  full). A manager fetches node builds only from its own edition's manifest and sends a node only a build of the
  node's edition. A node of the other edition gets no update from that hub; its card says why
  (`devices-deploy.other_edition`). To update it, put a build of its edition in the hub's `~/.persona/bin/nodes`
  (`personad-<os>-<arch>`, checked by its build info), or run the installer on the computer.
- **The installer** remembers a core-only install in `~/.persona/bin/.edition`: running `install.sh` again without
  `--core` installs the core edition again and says so.
- **Managers on other computers**: the install command a core-only hub prints for a manager downloads the core
  edition's `personad`.

Not covered: the core edition has no beta feed yet (a core-only hub set to beta updates finds no `core-latest.json`
at the beta pointer and reports that), and Web development updates build the full edition only.

## Changing the edition

Deliberately, on the computer: run the installer with `--full` or `--core`, then restart the hub or the node
service. Data is kept either way: a module that is absent keeps what the user has
([core-and-modules.md](../architecture/core-and-modules.md), "A module that is off keeps what the user has"), and the
full edition finds it again.

## Building the packages

`scripts/release/publish.sh` builds both editions in one run: the full one in `build-publish/bin`, the core one in
`build-publish/core` (`bin/` as built, `dl/` as served). `PERSONA_CORE_PACKAGES=0` leaves the core edition out.
`NO_DEPLOY=1` builds and checks only. A deploying run uploads the core edition only with `PERSONA_PUBLISH_CORE=1`.
A core build or check failure is otherwise a loud warning, removes that run's incomplete core output and never stops the
full release; with `PERSONA_PUBLISH_CORE=1` the core packages are required and such a failure stops the release.

Alone, for chosen platforms (`LDFLAGS` as a release sets them; the update URL is where the packages will be served):

```sh
LDFLAGS="-s -w -X main.version=$VERSION -X main.channel=release -X main.build=$BUILD -X main.commit=$COMMIT -X main.updateURL=$SITE/dl" \
  scripts/release/core_edition.sh build build-publish/core linux/amd64 linux/arm64
scripts/release/core_edition.sh pack build-publish/core "$VERSION" "$BUILD" "$COMMIT"
```

`pack` needs the build for the machine it runs on, whose `personad` writes the manifest. The Docker image is the full
edition; a core-only image is not built yet.

## The install check

`python3 scripts/ci/core-install.py` does what this page says, from packages alone:

1. builds this machine's packages of both editions, as `publish.sh` does, and serves them from one flat directory;
2. starts a disposable Postgres and a clean container (Alpine with Python 3) that holds no checkout, only what it
   downloads;
3. runs the commands of "Install the hub by hand" above, read from this file, in that container;
4. checks the hub, the manager's `modules: loaded none`, the hello's `modules` marker with no built-in module's
   `module.<id>` flag (a client-only flag such as `module.glasses` may stay), the edition of every installed program,
   and that the installer asked for `core-` names only;
5. completes the browser sign-in handshake and uses its web credential, then follows "A computer's node" in an
   isolated home and checks the core-only node is paired and online;
6. checks the update feed: the hub reports the core edition's newer release and never the full one's, and refuses a
   full manifest served as `core-latest.json`;
7. runs the installer again without `--core` and checks the install is still core-only;
8. installs the full edition from the same directory in a second clean container and checks it asked for no `core-`
   name and loads its modules.

It prints one line: `PASS`, `FAIL` or `TIMEOUT`. `scripts/ci/server.sh core-other` runs it ('Core-only install from
packages'). It needs Go, Docker (`DOCKER` names another client) and Python 3, and no private configuration.
