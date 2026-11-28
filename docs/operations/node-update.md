# Updating the node over Persona Link

When a machine connects to the hub actively through Persona Link (`kind: link`), or the hub connects to the computer (`kind: service`), the resident personad is updated over the existing node RPC. Link still only forwards byte streams: no new listening port, no SSH required, and it is not responsible for installing anything.

This updates the executable used by the resident node; it does not update the connector persona-link or its independent Mac helper package. Implementation: `server/internal/devices/deploy_rpc.go`, `server/internal/node/update.go`, `server/internal/nodeupdate/`. The old SSH / tailcat deploy entry points stay.

## Usage

The manager selects the program the resident node reports in `node.info` ([link.md](link.md) "The device's program"). A `persona-node` resident requires a `persona-node` package. A `personad` resident, or an older node that omits its program, gets `personad`. The executable path may differ from the program name. For example, a device file named `persona-node` that still runs `personad` receives a `personad` update in place.

Use `persona-link node switch` on the computer to change which program the service runs. The resident preflight checks the replacement's reported program, platform and listener handover support before reloading.

Both `deploy.sh` and the old SSH deploy run the staged `.new` file's `version` command before renaming it over the installed program, over direct and nested SSH alike. A failed check removes the staged file and leaves both the current binary and the resident node process untouched. The old SSH deploy never replaces an installed file that is a script (it starts with `#!`): a `remote_bin` that is a wrapper, setting the node's environment such as `PERSONA_HOME` and then running personad, would otherwise become the plain binary, and the node would start on another data directory and machine uid. It removes the staged file, exits 5, and **Redeploy** says which file is a script; update the personad the wrapper runs by hand. Before it replaces a binary it keeps the old one as `<remote_bin>.prev`. Uploading `personad-<os>-<arch>` and `persona-node-<os>-<arch>` into the hub's node binary directory likewise uses a temporary file and an atomic rename, so a reader never receives a partially written executable.

## Node packages

Hub self-update and `deploy.sh` publish platform packages of `personad`, `persona-node`, `persona-manager` and
`persona-link` by atomic rename. The publisher refreshes the legacy daemon name with a relative symlink, including
replacing prior publisher-owned copies. A node package directory supplied by a release publisher stays under that
publisher's control.

Resident nodes that support in-place updates use the same idle handover on local, SSH, tailcat and Persona Link
connections. A local stdio node takes its manager's build when it starts. The legacy SSH installer signals the main
process of the resident service using the executable it replaced. Nodes that cannot reload through their supervisor
can still be updated through their supported installer.

### Confirmed Web development builds

The Hub tells each independent assistant which development commit it has confirmed, along
with the version and build. An assistant receives no target for a candidate that the Hub
is still trying or has rolled back. For a computer it owns, the assistant selects a package
for that computer's operating system, architecture and running program.

1. Authenticate to the Hub and request the manifest and matching package. Check the target
   identity, archive SHA-256 and unpacked executable SHA-256 before using the package.
   This path works on an assistant host without a compiler.
2. If the manifest has no package for that platform, the assistant can fetch the exact
   40-character commit from `https://github.com/nwverse/persona.git` and check it out
   detached. Git and Go must be available on the **assistant's host**. The assistant
   builds for the target platform and checks the program's version, commit and build.
   A newer `main` and a public release are ineligible for this request.

A failed authentication, download or checksum check stops the update. If a platform is missing and the
assistant lacks Git or Go, provide the matching Hub package or install the missing tools
on the assistant's host, then retry **Redeploy**. The computer keeps its installed program
until a replacement passes verification. A verified package goes through the usual
deployment for that computer. For a Link or service node this includes the idle check,
transfer, restart, confirmation and rollback described below.

In **Settings › Hub**, a computer's row shows its update phase or failure and any reported
package source. **Package from hub** and **Built locally by the assistant** describe how
the assistant obtained the update. The installed version comes from the computer.

### Release packages

For a release source, the manager looks up the computer's OS, architecture and kind of
program in the following order (`server/internal/devices/packages.go`).

1. **The hub's own builds on its machine**: the node binary dir (`~/.persona/bin/nodes`, `PERSONA_NODE_BINARIES`: what `deploy.sh` puts there, what a self-updating hub's release brings, what an air-gapped hub's owner drops in) and the bundled dir (`PERSONA_NODE_BUNDLED`: the Docker image's own platform), checked in that order. A local candidate's Go build info must match the requested OS, architecture and program; a mismatched or non-Go file is ignored rather than sent, so the release lookup can supply the right build. `personad-<os>-<arch>` is what `<name>.version` beside it says, or else the hub's version, but only when the hub's version string occurs in the file: a node's self-check compares the version the build was linked with, so a file without it is an older build left in place (a platform the hub's update did not replace, a build a later step of a deploy has not replaced yet) and is skipped for the release (#12958). Finding the string proves nothing (a short version can occur in any build), so this never skips a build that would pass. A Windows build is `personad-windows-<arch>.exe`; one named without `.exe` (put there before Windows builds kept it) is still taken under the same check. A lookup is looked up again as soon as one of these files or its `.version` changes, not ten minutes later; `persona-node-<os>-<arch>` counts only when its `.version` says the hub's (`scripts/deploy/deploy.sh` and [private Hub publication](hub-update.md#node-upgrade-policy) put both programs there with their version sidecars).
2. **The release of the hub's version**, from the hub's `node_packages.url` (`docs/operations/config.md`; default: the hub's update source, `hub.json`'s `update.url` or the build's update URL, the publisher's `dl/`, fetched through `update.proxy` when it has one: `docs/operations/hub-update.md` ("Update source")): `dl/<version>/manifest.json`, which `scripts/release/publish.sh` keeps for every release with its node builds (and under `X.Y.Z` too when the release is the tag `vX.Y.Z`, a Docker image's version). A persona-node device gets the release's persona-node (`latest.json` and the release manifest list it under `nodes`).
   **A source without `dl/<version>/`** (a GitHub repository's `releases/latest/download`, the official update source) answers 404 there, so the manager reads its `latest.json`: when that release is the hub's own version it is the release of the hub's version (2. above, not a fallback, so automatic node updates send it), else the fallback below. A build listed in `latest.json` is downloaded from `dl/<version>/` first and, when that answers 404, from where hub self-update fetches it: on GitHub the release's own tag (`releases/download/v<version>/`), elsewhere beside `latest.json` (`docs/operations/hub-update.md` ("Update source")); it is checked against the same manifest either way. A release published between the lookup and the download therefore cannot replace a GitHub build; from another such source the download fails saying a newer release was published (the source's `latest.json` is read once more to tell), not that the build did not check out, and the next **Redeploy** looks the build up afresh. Through v0.5.3 the manager asked `dl/<version>/` only, so on a GitHub source every lookup failed with "not found".
3. **The fallback, on by default**: with no build of the hub's version anywhere (an `edge`, dev or pull request image), the newest release (`dl/latest.json`) when it has a package for the running program that this manager can drive. The manager checks the manifest's `node_protocol`, `node_protocol_min` and `node_capabilities`, judged like a connected node's handshake (`docs/protocol/protocol.md` ("Node handshake")). Such a node runs another build than the hub, and that is up to date: the machine shows the build it runs, and no update is offered while it runs it. A fallback build is never sent by itself (on connect or after the hub's update): it is offered on the machine's card and sent only on **Redeploy**, as nobody chose to run another version than the hub's. Builds of the hub's own version, its own or its release's, go out by themselves as before when automatic node updates are on (`auto_update_nodes`, below).
4. **Nothing**: **Redeploy** is not offered (`can_deploy` false) and `deploy_reason` says why and what to do, in the app's language.

**Editions** ([core-install.md](core-install.md), "Updates stay core-only"): a node says in `node.info` whether it is
the core edition (`edition: "core"`; a full build and every older node say nothing). The manager reads only its own
edition's manifests (`core-manifest.json`, `core-latest.json` for a core-only hub) and sends a node only a build of
the node's edition: a build in the node binary dir is checked by the build tag its Go build info records, and a node
of the other edition than the hub gets no release build (`deploy_reason` says so). The node checks the staged file
the same way before it replaces its program.

The Persona Link connector a switch from SSH sends ([link.md](link.md) "Switching from SSH") is found the same way, of the hub's version only: `persona-link-<os>-<arch>` in the node binary dir when its `.version` says the hub's (`scripts/deploy/deploy.sh` puts it there beside the node builds), else the release's (`dl/<version>/`, listed under `links` in its manifest), with the same checks and cache. There is no fallback to another release; without one the switch is not offered for that platform (`no_package`).

A fetched build is used only after it checks out, the way hub self-update checks its own (nothing is signed, `docs/operations/hub-update.md` ("Security: no signing")): size and sha256 of the archive and sha256 of the unpacked binary, from the manifest over HTTPS; its Go build info says that OS, architecture and kind of program; on the manager's own platform it runs and its `version -v` says the release's version and build. Other platforms' builds cannot run on the hub. Downloads go through `node_packages.proxy` (an `http://` proxy, `internal/netproxy`; the environment's proxies are not used) with a timeout, are staged and checked beside the cache, and land in `<data dir>/cache/nodes/<version>/` with a record of what they are only then (nothing half written is ever used); the cache serves them again, also while the site does not answer. Lookups are remembered for ten minutes, a site that does not answer for one; the machine list never waits for one: it is looked up in the background, the build found before standing meanwhile, and **Redeploy** looks it up afresh. While the site does not answer, the build found or fetched before stands (after a restart, the newest cached fallback this manager drives); a queued update waits, and a node already running what it was sent is confirmed. A mirror must answer 404 for a file it does not have: any other error counts as the site being away.

Windows release downloads keep `.exe` before `.gz` (`personad-windows-amd64.exe.gz`, `persona-node-windows-amd64.exe.gz`), and their unpacked cache files retain `.exe`; only the `.gz` suffix is removed. They are node packages, never a native hub self-update selection. Releases up to v0.9.1 list no Windows `personad` in their manifest (the downloads were added to the GitHub release afterwards), so a hub's own update installs a Windows build into `bin/nodes` only from the next release on; `scripts/deploy/deploy.sh` puts the Windows builds there itself.

### Redeploy and automatic updates

When the web Machines page or machine detail shows a version mismatch, click **Redeploy**. The page then shows **Waiting for idle**, **Transferring**, **Waiting to restart**, **Restarting**, **Confirming** in turn. Finishing the file transfer does not mean success: the machine must reconnect and actually run the target version before the hub confirms the update is complete.

The assistant's `machine_redeploy` tool starts this same operation for one machine and waits for its confirmed success or failure. Its wait is separate from the job: canceling the tool call does not cancel the deployment, and the machine card keeps the same phases and result as a button-started update. The tool refuses to target the machine running its own conversation, since that conversation would keep the node busy while waiting for the result; use a conversation on another machine.

- On a release source the target is the matching build described under [Release packages](#release-packages). A Docker Hub reports `v1.2.3-<build time>` for image `1.2.3`. A Web development deployment targets the Hub's confirmed commit, version and build.
- A normal update waits only for what the node's restart would interrupt (#6364): a conversation the node runs headless (the assistant's) that is answering, compacting or waiting on its background tasks, and an Agent still starting. The restart runs the new program in place after stopping the idle headless conversations, which resume on their next message. Terminal Agents (herdr, Orca, Claude Code or Codex started in a terminal) are other processes: they keep running through the update with their background tasks and subagents, their hooks reconnect to the new node, and their cards on the phone stay. The phone loses the computer for a few seconds. The node judges this itself: `sessions.list` answers `update_busy`, the same check `node.update.commit` makes, and the hub waits on it, since a headless conversation's row does not say whether it is busy. The node checks again before installing, in case something started during the transfer. A node that does not announce `node_update_idle` (older builds) still refuses while any Agent works or has background tasks, so the hub waits for that before sending it anything; such a machine needs one update on the computer itself ("First upgrade of an old node") or a moment when nothing runs there.
  Continuous headless work cannot hold this wait open forever. After `assistant.node_update_handoff_grace_seconds` (default 60, 0 disables), the manager's next `sessions.list` carries `update_handoff: true`. The manager and a current node then refuse new `assistant.send` / `assistant.warm` turns with `node_restarting`; unrelated RPCs and already admitted turns continue. When the current turn finishes, transfer begins. The same grace reaches the resident through `node.update.commit`: its drain keeps admission closed after the deadline instead of reopening it between RPCs, while admitted replies and current turns finish. Natural idle still takes the immediate path, and a failed deployment reopens admission.
- Only one update transaction is accepted per machine; a dropped connection does not lose the transaction's mutual exclusion.
- **Automatic node updates are off by default.** In Web development, **Settings › Hub › Update computers automatically** controls deployment after Hub confirmation and when a computer reconnects. The saved setting takes precedence over `hub.json`'s `auto_update_nodes`. With the switch off, open the computer and choose **Redeploy** to update it. See [Runtime node update policy](hub-update.md#runtime-node-update-policy).
- With `auto_update_nodes: true`, after the hub updates itself it updates the online nodes one by one; Link machines that are offline at that moment keep a pending target and continue on their next connection. The connection method is never changed for the sake of an update.
- With the same policy, a plain deploy is covered too: `scripts/deploy/deploy.sh` writes the binaries and restarts the hub normally (not a self-update), so when a machine connects with a node that is not the build it should run, the hub sends it that build once (`updateOnConnect`; the same for ssh / tailcat and Link), when it is of the hub's own version (a fallback only on **Redeploy**). Each build is tried automatically only once per machine, and the outcome is kept in `node.deploy.<machine id>`; a failed attempt (unreachable, could not install, rolled back) is not pushed again on every reconnect, and a manual **Redeploy** can try again.
- The hub's task target, progress and result are stored in Postgres settings (`node.deploy.<machine id>`). After a hub restart, accepted tasks can continue; if the hub version has changed, the old target is stopped and the user is told clearly to update again, so an outdated target is never installed.

## First upgrade of an old node

A new protocol cannot make an old node that does not know it update itself. When a machine does not report the update capability, the web page does not offer a button that is bound to fail; it explains that the resident node must first be upgraded once on the computer.

1. Get a personad that includes this capability and fits the machine's OS and architecture, and make sure it includes the features and custom changes this machine uses.
2. On the computer, find where the resident node is actually installed: `bin` in `persona-link node status --json`. Respect the existing service's runtime root, launch arguments and environment; do not guess from the default directory alone.
3. Replace that binary through the original install or deploy channel, then restart the corresponding resident node service. On Linux the standard user service is `personad-node`; on Mac the standard LaunchAgent label is `com.persona.node`.
4. Go back to the Machines page and confirm the node is connected and supports remote updates; later upgrades can then be done from the client.

Reinstalling the independent Persona Link helpers on Mac does not replace an installed resident node automatically; upgrading the connector does not count as doing this step. The first migration should use a node artifact that includes this capability.

Updates are offered only for resident nodes with a supported way to recover the process: systemd or launchd, recognized by the environment they set (`INVOCATION_ID`, `XPC_SERVICE_NAME`), or another supervisor that starts the node again whenever it exits and says so by running it as `node --attach --supervised` (a container's entry script, supervisord, runit, s6 on Linux and macOS, [link.md](link.md) "Without systemd"; persona-link's scheduled-task supervisor on Windows, [windows.md](windows.md) "Updates through the hub"). A stdio node run directly, or a resident process started by hand with nothing to restart it (nohup, cron `@reboot`), shows why it cannot be updated remotely. An old hub lacks the new orchestration logic and must also first be upgraded through its original channel.

## Protocol and install boundaries

`node.info` gains the optional `node_update`, `node_update_reason`, `update_draining` (present with `{id, target}` while a resident node is draining active work for an accepted update), and `edition` (above). Nodes that report draining advertise `node_update_drain_v1` in `capabilities`. Old nodes do not report these fields and are treated as unsupported or legacy-confirming. The existing `POST /api/v1/machines/{mid}/deploy` is unchanged; the machine model gains `can_deploy`, `deploy_reason` and `deploy_phase` (`waiting_idle`, `transferring`, `draining`, `restarting`, `confirming`), and old clients that ignore the new fields still work.

node RPC:

| Method | Parameters | Result / meaning |
| --- | --- | --- |
| `node.update.begin` | `size`, `sha256`, `version` | `id`, the received `offset`; the same target can be retried |
| `node.update.chunk` | `id`, `offset`, `data` (JSON base64) | the acknowledged `offset`; checks order and size |
| `node.update.abort` | `id` | abandons an uncommitted staged transaction |
| `node.update.commit` | `id`, optional `handoff_grace_seconds` | accepts the install after the integrity, self-test and idle checks pass; restarts only after the success response is written. The grace defaults to 60 seconds when omitted and zero preserves the indefinite natural-idle drain; older nodes ignore the field |
| `node.update.confirm` | `version` | confirmation after the new node reconnects to the hub; must match the running version and the pending target; can be called repeatedly |

The node determines the install target from the real executable and its symlinks; the hub cannot name an arbitrary path to overwrite through parameters. The new file is staged in the same directory and checked for size, SHA-256, OS / architecture and the output of running `version`; then the old version is kept and the file is replaced atomically. A corrupt, truncated or wrong-version file never replaces the existing program. Integrity checking follows the existing release trust model; no new release signing requirement is added. On a Mac whose node has a code signing certificate set up, the checked file is signed before the swap and the transaction records the signed file's sha256 ([mac-node-signing.md](mac-node-signing.md)).

The confirm call follows the trust boundary of the paired hub; HTTP deploy permission is still governed by the existing authentication and role filtering, and the new node RPCs do not widen client permissions.

## Restart and rollback

The update manager belongs to the resident process and is shared by all attach connections. Ending one RPC connection only closes that stream; it cannot stand in for restarting the whole resident node. A normal switch first replies to the commit request, then closes connections and listeners, drains the resident service, and starts the new executable with the original arguments and environment.

When a co-located Hub update has already replaced the shared executable, the resident node can adopt the file if it is
executable and its SHA-256 matches the verified target under the node's signing policy. It removes the redundant upload
and follows the same reload and exact-version confirmation flow. Its confirmation record carries no rollback backup and
keeps startup signing from modifying the shared executable while confirmation is pending. Older targets confirm by
running version and are eligible for adoption when their startup can use the verified file with its existing signature.

If a different valid executable is already on disk when the commit takes its snapshot, the commit backs up that file
and records its version for rollback, even if the resident process still runs an earlier version. An active swap
must finish before another commit can adopt or replace the installed file.

Before draining, the resident checks the installed replacement by running its `node-update-check`. Endpoint protection
on some Macs holds a new executable's first start until its scan ends (about 30 s) and then caches the verdict by
content (#12528), so the first run of a file the process has not seen pass gets up to 60 s; once that file passed, its
later runs get the short limit again (5 s for this preflight, 20 s for the update's own staging and signing checks,
which follow the same rule). Each check is one run, so 60 s is the most it waits, and a replacement that never answers
still fails. A check that ran out of time says `node-update-check timed out after …`, which tells it apart from a
version, platform or program mismatch. `node.update.commit` answers only after the upload's checks, and with a signer those of up to
two signed copies, each a new file. The hub's deploy and its relay therefore wait for that answer for
`nodeupdate.CommitWait` (three first runs, two signings with their checks of the signature, and a margin: 4 min 40 s) and 10 s more, instead of taking a slow scan for a lost
answer and showing a failed check as the two-minute `not_confirmed`. The relay's one client, `scripts/release/promote_node_release.py`, reads
that commit for `COMMIT_WAIT` (300 s), and a hub test keeps it at least the relay's wait. If preflight fails, the resident keeps serving and
logs the exact error along with the replacement's reported version, OS, architecture, program and listener handover
support when it could read them. For an RPC deployment, the node sends `node.update.preflight_failed` with the
transaction `id` and `error`. The hub ends the matching deploy job with that error, and the local watchdog still
handles recovery. Retrying the same commit while recovery is pending returns the same error. When no failure
notification arrives, including from older nodes, the hub keeps the two-minute confirmation timeout. Older hubs
ignore the notification.

Once `node.update.commit` succeeds and its reply is written on a `node_update_drain_v1` resident node, the node enters
the `draining` phase (`node.update.draining` event and `node.info.update_draining`) and holds the local rollback
watchdog in draining mode while active work finishes, rather than starting the post-restart confirmation deadline
before handover (#16517). The resident keeps the marker's `draining` flag set and slides its deadline while alive (so
an older backup watchdog from disk drift also stays alive without falsely rolling back a live drain); for an adopted
external binary (#15708) it refreshes only the adoption deadline without ever creating a backup or watchdog. Once
draining finishes, the resident emits `node.restarting` (carrying `{id, target}`) and arms the fresh post-`exec`
rollback deadline right before `exec`. On the hub, the two-minute post-restart confirmation timer (`not_confirmed`)
starts only at that handover (or after a sustained disconnect during `draining`), not on the commit reply. During
`draining`, the hub tolerates brief connection flaps that reconnect with a matching `update_draining` state, resumes
tracking across a hub restart without misclassifying an online draining old-version node as `rolled_back`, and caps
draining at a bounded maximum wait (`devices-deploy.drain_timeout`). Older nodes without `node_update_drain_v1` keep
the legacy post-commit confirmation timer.

An independent recovery process from the old version stays next to the executable; it restores the backup if the new version crashes, fails to start or times out before confirmation. Under systemd it is a separate transient user service, so it does not exit when the node's cgroup is cleaned up; under launchd, and under a supervisor declared with `--supervised`, it is a process detached in a session of its own, outside the node's process group. Replacement is allowed only after the recovery process is ready. The switch itself is the same on Linux and macOS: the node runs the new executable in place (exec, same process id), and the supervisor only matters when the node exits: a crash, or the recovery process stopping a new version that never confirmed, after which the supervisor's restart runs the restored old file. Windows has no exec and will not replace a running program's file: the node moves its file aside, puts the new one in place and exits with `nodeproto.RestartExitCode`, which persona-link's supervisor answers by starting it again at once; the recovery process there is detached and reports itself ready through the transaction marker, and rolls back the same way ([windows.md](windows.md) "Updates through the hub"). The recovery process distinguishes the node from a process that later reused its id by its exact start time, with `ps -o lstart=` as the portable fallback; with a declared supervisor the node checks at startup that `ps` can report it (BusyBox's cannot) and otherwise says why it cannot be updated. Recovery does not depend on the new node starting successfully, nor on the hub being online at the time. The necessary local recovery transaction marker is stored next to the binary, for the same reason as the hub's self-update: recovery must work when the database and remote connections are unavailable; the hub's business state and update tasks are not written to local JSON.

A commit checks the installed file against the snapshot it took before preparing rollback. A rollback checks it against
the replacement the transaction installed. Both read the hash through one open descriptor and recheck the inode, mode,
size and modification time against the path immediately before the rename. A replacement detected between the snapshot
and the swap causes the commit to report `node executable was replaced by another deployment`, clean up its rollback
state and leave the deployed file in place.
The recovery process identifies the node it watches by pid plus an exact start time where the platform gives one (clock
ticks on Linux, microseconds on Mac, the creation time through a process handle on Windows). On Linux and Windows the
check and the kill act on one pinned process instance, so a reused pid is never mistaken for the recorded process.
Markers written by binaries without the exact identity still work through their second-resolution start time.

On Linux, a killed or exited process that has not yet been reaped (state `Z` or `X` in procfs) has no live identity. The node treats such a watchdog as gone and starts a replacement, even if `ps` still reports its old start time. An authoritative missing-process or zombie result never falls back to `ps`; the fallback remains for unavailable exact readings and older transaction markers without an exact start time, after checking that the process is not known to be gone.

After reconnecting, the hub first confirms the node can read sessions normally, then confirms the target version; confirmation can complete even if the original update task vanished because the hub restarted. If a previous deployment record in Postgres (`node.deploy.<machine id>`) had already been marked `failed` (for example after a late confirmation), confirming the matching target version on reconnect updates that persisted record to `succeeded` and clears the stored error so stale `not_confirmed` failures never resurface across later disconnects or hub restarts. Until it is confirmed online, the client keeps the failed or confirming state and does not treat a successful file transfer as a finished update. A failed update of one node does not trigger a rollback of the whole hub.

## Verification

- hub: `TestLinkedDeploy*`, `TestUpdateNodesQueuesOfflineLinkedMachine`, covering chunking, resume position, bad confirm positions, no update while busy, becoming busy before commit, target version confirmation, recovery after a hub restart, and offline targets.
- node / local updater: isolated executable and temporary HOME, covering a complete package, a corrupt package, the wrong version, concurrent requests, resident process restart, confirmation and rollback on failure. Never install or stop the dev machine's real `com.persona.node` service.
- Windows: `TestWindowsSupervisedUpdateConfirmsOrRollsBack` runs the node as persona-link's supervisor does (restarted at once on the restart code): the update confirms after the restart, and an unconfirmed version, one that crashes at startup and a build that cannot take updates on Windows (never records its new pid) are rolled back; `TestReplaceFileMovesARunningProgramAside` and `TestDiscardRetiresAFileInUse` cover the swap of a running program's file and a backup still in use. CI cross-compiles Windows only, so run these on a Windows computer when changing `nodeupdate`.
- Declared supervisor: `TestDeclaredSupervisor*` run the node without systemd's or launchd's environment, as `node --attach --supervised` under a loop that restarts it on exit: the update confirms in place (same pid, no restart, no `systemd-run`), an unconfirmed version and a version that crashes at startup are both rolled back and the old one comes back through the restart, and a `ps` that cannot report start times (BusyBox) is refused with its own reason. Also run as uid 2000 in a Debian container with `--cap-drop ALL`, `no-new-privileges` and `--init`.
- Web: `machine-deploy.test.ts` covers old and new hubs, the update capability and each phase; anonymized data is checked in real screenshots in light / dark, list / detail and narrow screens.
- The full server suite uses an isolated test database on real Postgres and follows the test environment isolation rules in `docs/operations/testing.md`.

## Fixed release bootstrap of one node

A validated full manager release also contains the complete `personad` node executable. It can be reused without rebuilding it or publishing a new global node feed. The release worker accepts `--scope node --node-id <machine> --commit <full source SHA> --artifact-dir <validated directory>`. It requires that directory's original `worker-receipt.json` to say `scope: manager`, `result: validated`, and to match the manifest's source and build. Both archive hashes and embedded executable build information are checked. No build command runs in this scope.

The existing release service can select the same operation through its existing config: `scope: node`, `node_id`, `node_commit`, `node_artifact_dir`, and `node_targets: {"<machine>": {"publisher_config": "/absolute/path/to/node-publisher.json"}}`. The worker still uses the original `state_dir/lock`. It checks by default; installation requires both `node_apply: true` in the worker config and `activation_enabled: true` in the selected publisher config. `--validate-only` always prevents installation. A checked receipt is recorded as `checked` under `last-check.json`, never `confirmed`; it cannot overwrite the activation receipt in `last.json`. The worker records a pending transaction before invoking the publisher. A previous pending, failed or confirmed transaction with the same target identity is retained; installation is not retried automatically, and an explicit `--retry` is required.

The publisher config uses the existing Hub `remote_root`, `credentials_command`, optional `ssh_proxy`, `remote_python` and `release_namespace`. Its `node_target` has exactly `machine_id`, `manager_id`, `machine_uid`, `executable`, `os`, and `arch`, all taken from the selected running node and its registered owner. Its `candidate` pins `source` (full commit), `version`, `binary_sha256`, and `binary_size` to the selected native artifact; a mismatch is rejected before any transfer. It holds the original Hub `.publish.lock`, requires `auto_update_nodes: false`, and prepares only immutable release files. It never changes `bin/nodes`, the Hub release pointer, other node targets, or manager executables. The fixed candidate goes through `node.update.begin/chunk/commit/confirm`, preserving the installed node's own idle check, guardian and rollback. A rejected commit fails; an ambiguous lost reply requires a fresh target handshake and confirmation. Failure to reconnect leaves recovery to the original node guardian.

The Hub must support `POST /api/v1/machines/{mid}/node-update/{action}`. This full-credentials-only relay accepts only `info`, `sessions`, `begin`, `chunk`, `commit`, `abort`, and `confirm`. Each call checks the explicit owner and fresh node UID, executable and platform. It forwards through the existing `devices.call` contract, so the carrying manager needs no new package-selection code. The old node must already advertise `node_update` and enable transactional updates; lacking that capability is an explicit unsupported bootstrap boundary. After replacement, the publisher requires the requested version and `manager_update_inventory_v1` before confirming. A new invocation finding the version already installed fails closed rather than manufacturing native hash transfer proof; retain the original transaction receipt. Node confirmation does not prove a manager is idle or permission defaults have taken effect; obtain fresh manager inventory separately.

An older Hub without this relay fails closed. Upgrade that Hub through its original release service first; upgrading an independent manager first would create a circular prerequisite. The fixed node artifact remains unchanged. Normal Redeploy keeps its existing package ordering, and automatic fleet updates remain disabled throughout this explicit single-target operation.
