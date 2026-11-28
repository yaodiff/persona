# hub versions, update checks and automatic updates

User, 2026-11-02: "Can the phone manage the hub's version? Does the hub check for updates, can it update itself? …When the App opens, offer a one-tap hub update, or have the hub update itself."
Issue #284 introduced automatic installation of manually published public releases. Private Hub installations can also publish validated canonical-branch builds automatically, as described below. Both paths use the same updater and rollback mechanism.
Implemented in 4 steps; after each step, the corresponding "Plan" is rewritten as the current state, keeping the trade-offs.

## Automatic private Hub releases

### Web development updates

Operators of a Linux or macOS Hub that starts the built-in child assistant can choose
**Settings › Hub › Update source › Web development**, including when independent assistants
own some of the computers. This builds the Web client, Hub and computer services from a
single commit. Independent assistants follow that commit only after the Hub confirms the
new version. A candidate still building, awaiting confirmation or rolled back gives them
no new target.

The Hub advertises the confirmed commit, version and build. Each independent assistant
authenticates to the Hub and tries its package for the required platform first. The
assistant verifies the archive and executable against the manifest. If the manifest lacks
the platform, Git and Go on the assistant's host can build that exact detached commit.
Failed verification or missing tools leave the installed program in place. See [Node packages](node-update.md#node-packages).

An independent assistant that supports this update waits until it is idle to switch its
own executable. If startup fails, it restores the previous program. The computers it owns
can update separately through **Redeploy** or **Update computers automatically**. See [Automatic updates for an independent manager](../assistant/managers.md#automatic-updates-for-an-independent-manager).

This uses the existing update endpoints, hourly scheduler,
idle checks, binary verification, blue-green switch/watchdog, confirmation and node deployment
transactions. Public release updates keep their existing gates. Development automation is
off until explicitly enabled and is turned off whenever the source changes. An update already
accepted keeps its fixed candidate; source changes are refused until it ends. Repeated triggers
join that attempt. A rolled-back development commit is never automatically tried again.

When Web Settings opens on the idle Web development channel, it shows the cached status.
If the Hub advertises `supports_preview`, Web posts `{"preview":true}` to
`/api/v1/hub/update/check` once per visit. The preview refreshes the candidate without
starting an automatic installation. A failed lookup keeps the last successful check time.

Choosing **Latest version** and clicking **Update hub** posts `{"target":"latest"}` to
`/api/v1/hub/update`. The Hub reserves the update and reports `resolving` while it looks
up remote `main` and its commit time. It builds that exact revision, even if the branch
moves again. A match with the running commit returns to idle. A lookup failure releases
the reservation so the operator can retry. Older clients sending `{}` install the checked
candidate. A selected backup still applies through its own endpoint.

Development updates always use `https://github.com/nwverse/persona.git`, branch `main`.
The Hub can pause its own writers for a snapshot of the Hub and built-in assistant.
Remote assistants that keep their own databases and files fall outside that snapshot.
The **Development backup** section in Web Settings lets the operator choose a **Backup
folder** and the number of **Snapshots to keep**. Operators configure external coordination
commands on the Hub host.

The recommended folder is `<runtime>/update/development-backups` (where the Hub has always
kept these snapshots). It appears as a wrapping placeholder in the full-width folder field.
**Use recommended** appears for a custom folder and clears the field. **Save** is enabled
when the folder or retention changes. Saving a change to an empty folder explicitly chooses
the recommendation over a `directory` in `hub.json`. Omitting the folder from a request
keeps the saved folder. The Hub validates a custom folder with the backup command's rules
(`internal/operations/backup`):
it must be an absolute path (a leading `~` is expanded), may not be inside data a snapshot
collects (the knowledge base, `managers/assistant`, extensions, skills or
`hub.machines_kept`), may not be the runtime directory or contain it (or an external
knowledge base) — a snapshot leaves its own store out of the archive, so such a store would
drop what the snapshot is for — it may not be a symbolic link, and it must be private
(`0700`; an existing folder readable by other users is refused with the `chmod` to run). A
folder chosen inside the runtime is never archived into the snapshot. The folder cannot be
changed while the current one holds a `coordination-pending.json` marker or an
`.incomplete-*` snapshot. Retry in the same folder to clean up stale staging directories. A coordination
marker requires operator recovery before retrying or changing folders. The retention count
defaults to three and may be 1–50; an explicit `0` is refused (omit the field to inherit).

Another local manager under `<runtime>/managers/<id>/manager.json`, an `extra_databases`
entry or an `extra_roots` entry requires external coordination. Configure **both**
`update.development.backup.quiesce` and `resume` in private `hub.json`. The same pair is
an optional override for the built-in pause on other installations. A missing required hook
or a half-configured pair disables Web development, and Settings shows the Hub's reason.

```json
{
  "update": {
    "development": {
      "backup": {
        "quiesce": ["/absolute/path/to/pause-persona-writers"],
        "resume": ["/absolute/path/to/resume-persona-writers"],
        "directory": "/absolute/path/to/snapshots",
        "retain": 5,
        "extra_roots": [],
        "extra_databases": []
      }
    }
  }
}
```

These hook paths are placeholders, not installed programs. The operator must implement and
verify them for that installation. `quiesce` must return only after **all** writers of the
inventoried databases and files have paused at a consistent boundary, including background
ingestion, extensions, uploads and manager/node children. Keep the Hub updater itself running.
`resume` must safely release even a partially completed pause. Hooks have a 60-second deadline;
every failure attempts resume, and a failed resume blocks adoption and subsequent attempts.
Stopping a service that the Hub immediately respawns does not establish this contract. Do not
use no-op hooks on real data. Missing coordination disables installation and automatic opt-in.
`directory`/`retain` in `hub.json` are read when the Web UI does not set them; a Web choice
wins.

### Built-in writer pause

Without hooks the Hub pauses its writers while capturing mutable data: request writes are
refused with `503` until resume — the apps' API, signing in with a password or a browser, a
manager trading its install code, and the admin pages — writes already under way finish, and
the built-in child manager is stopped and kept stopped until resume. Background writers (app
registrations, extension reports, device-session rows, heartbeats and pruning, the
computer-trust sweep) leave their work pending and write it after resume; the pause is lifted
before those pending workers are woken, so the wake they use is not consumed by a refused
write. The Hub then checks the databases with the backup command's own `Quiesced` rules,
counting only its own connection-pool sessions as its own (`backup.QuiescedExcept`), and
refuses the snapshot when another session, or one it cannot see, is still connected. A
built-in manager whose tables still share the Hub's database (before
`moved_to_own_database`) needs only that database checked; one moved to its own is checked
separately (`backup.DiscoverDevelopment`, the same rules as the helper's inventory). Resume
runs on every failure path, including cancellation; a Hub process that dies mid-snapshot
may leave a staging directory. The next preflight removes it under the backup lock. Snapshot JSON, SQL dumps and file history keep their existing privacy rules.

The host needs Git, Python 3.11+, Node matching `web/package.json`, Go matching `server/go.mod`,
and compatible `pg_dump`/`pg_restore` tools with access to every included database. Their libpq
shared library must be discoverable by Python. The helper parses URI and keyword connections
with libpq, then passes their explicit fields through libpq environment variables. It does not
put connection strings or passwords in process arguments or receipts. Invalid connections and
parameters without a safe environment binding fail before pausing writers. Existing private
`PGPASSFILE`/`PGSERVICE` settings remain usable; credentials in a URI are kept in the private
child environment, which is not protection from the installation user or privileged processes.
Git uses the
installation user's existing HTTPS credential mechanism, with interactive prompts disabled.
Updates execute build code from this project's repository.
Docker Hubs and Windows Hubs retain their original update paths.

In Settings › Hub the development pipeline runs **Build → Waiting for idle → Backup → Restart**.
Build includes staging and verifying the programs. For a saved version, **Prepare backup** takes
the place of Build. Before these stages, the hub checks the backup settings. Choose **Cancel update** and confirm to stop before binary adoption. Cancelling during this first check stops the attempt before the build.
The hub interrupts resolution or a running build, or stops at a safe staging or idle boundary.
During a snapshot it records the request in memory. Writers resume after data capture. Program
packaging, verification and saving the backup receipt finish before cancellation takes effect. After cleanup the hub is idle, history shows **Cancelled**, and another
update can start immediately.

`GET /api/v1/hub/update` reports `can_cancel` and `cancelling`. The authenticated
`POST /api/v1/hub/update/cancel` remains available during the built-in writer pause.
Repeated requests during cancellation return the current status. Once the installer claims
adoption, or when there is no cancellable attempt, cancel returns HTTP 409.

Settings › Hub shows the current step and elapsed phase time during a development build,
snapshot backup or saved-version preparation. Build progress covers the pinned checkout,
`npm ci`, the Web development and public-share bundles, precompression and runtime assets,
program/platform targets, and manifest checksum verification.

The backup checks its settings and storage before pausing writers. Database progress shows completed dumps and the
cumulative bytes written by `pg_dump`; the final dump size is unknown while it runs.
Before writing `files.tar.gz`, the helper scans the included trees for regular file counts
and sizes using the archive exclusions. The archive reports completed files and uncompressed
file bytes read. Writer resume, program and Node package archiving, and snapshot verification
each have a step, in that order, for both writer coordination modes. Applying a saved version shows the snapshot
checksum scan and the copy of compressed program and Node packages into staging. The byte
totals describe each step's inputs. A percentage describes that step's bytes or completed build
targets. The Web uses its standard file-size units and shows a smoothed rate in MB/s.

`GET /api/v1/hub/update` adds the optional `development_progress` object while these phases
run. It carries `phase` (`building`, `backing_up` or `applying_backup`), `step`, the one-based
`step_index`, `step_total` and `elapsed_seconds`. Newer Hubs include the ordered step names in `steps`. When available it also carries `item_label`,
`item_done`, `item_total`, `bytes_done`, `bytes_total`, `bytes_per_second` and `percent`.
The page polls for counters about once per second. The helper reports counter samples at most
about every 750 ms, with an immediate event at step changes and when totals become available.
The structured pipe carries only fixed step and target labels and numbers. Tool diagnostics
stay in private logs and the result stays in its own file. On a failure the error names the
failed step, available backup reason and phase log. Progress is cleared when the phase ends. Existing clients can keep
reading `state` and the legacy `progress` string.

The helper resolves the full commit of `refs/heads/main`; a missing `main` branch is an error.
Settings shows `main` and the installed/target commits. A detached private checkout fetches that
exact object even if the remote advances while building. There are no repository/branch overrides
or default-branch mappings.

For Web development only, **Update version** combines the latest `main` candidate and retained
program backup versions in one selector. **Current commit** and **Latest commit** remain two rows,
each with its update date in `yyyy-mm-dd` before the hash. The current/backup date comes from
successful installation; the latest date is the pinned Git commit's committer date, not check time.
Unknown dates stay unknown. Backup creation time is shown separately. Selecting a backup exposes **Apply backup**:
after confirmation it disables development auto-update and installs the saved programs and embedded
Web, keeping the **current database, configuration, keys and history data**. It does not check out
Git, rebuild, extract the history archive or restore a database dump. Preparation verifies the
snapshot, stages private program copies, then reuses the ordinary idle wait, fresh pre-install
backup, binary checks, service switch and confirmation/rollback. A snapshot rotated during the new
backup does not invalidate those staged copies. Missing/damaged selections fail before adoption.
`GET /api/v1/hub/update/backups` lists program-version metadata;
`POST /api/v1/hub/update/backups/{id}/apply` accepts an owned retained ID, never an arbitrary path.
Only snapshots with complete program version/build/commit/platform metadata are selectable.
Startup recovery errors stop both standalone and edge-slot startup before configuration/database
initialization. Failed program restoration or exec retains the development journal and staging
for operator repair; the attempted build cannot continue as an ordinary startup.
Edge restoration also stops on a failed program/Web restore, retaining both pending and
adoption journals and required staging. The callback distinguishes a failed restore from no
matching update; activation and confirmation-timeout paths never restart a fallback executable
after partial restoration. Repair the retained preimage/conflict before retrying. The backup
list/apply endpoints are Hub installation administration, declared with the other owned routes.
Listing validates the full manifest schema, typed hashes and exact artifact mappings, without
rehashing payloads. Verification and preparation additionally check the physical inventory,
compressed and native program hashes and history members. Staged programs get an independent
native-hash check after copying. Malformed metadata is rejected as `ValueError`; unknown or
damaged snapshots stay on disk for manual inspection and are not pruned as verified material.
Invalid schemas are omitted from listings; payload damage is checked before application.

Source settings accept only the exact values `release`, `beta` and `web-dev`; invalid supplied values
are rejected before changing any preference or selection. A manual request overtaken while
probing services is rejected without transferring its interruption consent to the accepted
operation. Explicit Update now for an already accepted waiting attempt still requires confirmation.
The install script's independent release-checksum refresh loads the saved source first;
a late release response cannot overwrite a development selection or active install progress.

Ordinary automatic/manual updates wait for the shared Hub idle check before backup and switching:
working turns, conversations awaiting blocking user input, and active redeploys can hold the update.
Non-waiting `ask_user` cards alone do not hold it; running turns and blocked terminals still do.
A manual attempt stops waiting after two hours; it does not force a restart at that deadline.
Choosing **Update now** explicitly bypasses this Hub wait and can interrupt local or connection-owned
conversations. Independent remote resident-node conversations can survive a Hub reconnection.
The idle check is not a lock against new turns: a turn starting during the subsequent backup/switch
can still overlap the restart. This path does not guarantee interruption-free session execution.
Link/connector node updates retain their own idle and commit checks; direct SSH deployment instead
replaces the program and restarts the node, so it can interrupt work. See [node-update.md](node-update.md).

The compile gate runs `npm ci` and `npm run build:development` for Web, generates runtime packages,
then runs tagged `go build`. Package targets include the Hub's platform, the last known
OS and architecture of computers across all owners, and the reported platforms of connected
independent assistants. Machines on the same platform share a build. The Hub also builds
its own `persona-hub` and `persona-manager` programs. Web assets
are embedded and precompressed. No full tests, vet, mobile builds, release publication or hosted
CI are required. The development command retains TypeScript, Vite, WebAssembly, localization
and asset-graph checks, but skips the release's first-screen/idle performance budgets. The
normal `npm run build` budgets are unchanged. Bootstrap and target source must contain this
development build command; an older target without it fails before adoption. All components
share the same commit, version and build identity; automatic
adoption also retains the existing ancestry guard. Fetch depth and the returned ancestor list
are capped at 1024; if the installed commit cannot be proved within that window, automatic
adoption stops and a manual update is required. An unknown/dirty installed commit also requires
a manual first update. Verified programs and Node packages are copied into independent updater
staging before the disposable checkout is removed; staging survives the idle wait and process
switch until confirmation or rollback. The binaries retain the `release` ABI/channel for existing self-checks;
the independent persisted `source: web-dev` identifies this update channel.

After compilation and the existing idle wait, before changing executables, the helper makes
a recovery point in the configured backup folder (the recommended default is
`<runtime>/update/development-backups`). It includes:

- Custom-format PostgreSQL dumps of the Hub database and every local manager database found
  in `managers/*/manager.json` (shared databases deduplicated), plus `extra_databases`.
- The runtime tree's configuration, encryption keys, knowledge/history, attachments and other
  ordinary data files, plus configured external knowledge directories and `extra_roots`.
  The runtime's top-level `update`, `bin`, `cache` and `logs`, the snapshot store, sockets,
  FIFOs, and ephemeral hub-transcribed meeting audio under `<kb>/recordings/<id>/attachments/audio/`
  in inventoried knowledge-base directories are omitted (recording metadata, transcripts, minutes,
  meeting photos under `recordings/<id>/attachments/`, and non-recording attachments remain included).
  The top-level `web.prev/` directory is a previous standalone Web bundle.
  Recovery uses the captured active `web/` override or the saved program's embedded Web.
  The previous bundle is kept if the active override resolves into it or it contains an explicitly
  inventoried knowledge directory or extra root. A nested directory named `web.prev` stays in
  the archive. Unknown runtime directories are included. Links must satisfy the offline
  extraction rules below.
- Separate compressed copies of the current `personad` and existing Hub/Manager programs,
  with hashes, original paths, installed version/build/commit and installation/backup times.
  All matching locally available Node packages are saved, including platforms with no currently
  registered computer; symlinks and packages marked as another version are excluded. Each snapshot
  retains its own matching programs. If the host Node and installed `personad` have identical
  native SHA-256 hashes, their original `node-<platform>.gz` and `program-personad.gz` names
  share one compressed file through a hard link. Stores without hard links receive a copy.
  Both layouts use the original manifest and both names work independently after copying.

Writers remain paused across **all** database dumps and the file archive. A dump must have its
PostgreSQL header and pass `pg_restore --list`. Once the helper saves the effective database
connections and opens the selected program files, it runs the resume hook or requests and
waits for the Hub's built-in resume acknowledgement. The open files preserve the selected
binaries across atomic replacements. Compression, SHA-256 hashing of staged data and programs,
verification, publication and retention all run with writers resumed under the snapshot lock.
Each private snapshot directory is synced, published and verified before cleanup. Credentials
remain inside private config/key backups (0700 directories, 0600 files), never in receipts or
the API. `database-0.dump` is the Hub; subsequent dumps follow sorted manager config paths,
then configured extra databases, with identical URLs deduplicated. The archived configuration
preserves that mapping. `effective-databases.json` privately retains the effective DSNs,
including `PERSONA_DATABASE_URL` overrides; no DSN is written into the manifest or API.
The built-in manager's shared database remains included until its own-database move commits.
After that move, `database_name` selects a database on the effective Hub server before a
saved `database_url`, matching the built-in manager's startup selection. Other managers use
their own configured URLs. A stale URL cannot replace the named manager database in a snapshot.
Protect these snapshots like the original data.

Only verified directories with this helper's exact name and ownership manifest are rotated,
oldest timestamp/name first, retaining the newest **three** by default (the **Snapshots to
keep** setting, 1–50, replaces that count). Unknown directories, changed manifests, symlinks,
additional user files, damaged archives and other backup systems are left alone. A
failed/incomplete snapshot is not counted and removes no valid recovery point. Cleanup failure
stops adoption; it can temporarily leave more valid snapshots than the configured count rather
than discard recovery evidence. The API retains the last snapshot/rotation receipt; the Web selector
lists available program versions. Before building, preflight checks the inventory, connection
settings, folder, lock and coordination marker. The snapshot repeats these checks under its
publication lock. Settings shows the failed step and a localized reason when available. Private
phase logs are `update/development/<operation>.log`. Starting a snapshot moves its non-empty
previous log to `update/development/backup.prev.log`.

The data archive intentionally captures the existing standalone `web/` override before it is
set aside in the same update transaction so the new embedded UI actually serves. Failed adoption
restores that override and all swapped programs. Explicit **Apply backup** installs saved programs
and their embedded Web only; it never extracts this data archive or its standalone override. After
Hub confirmation, compiled node packages are installed locally even with automatic fleet updates
off, so **Redeploy** can use the matching build. Automatic deployment to computers requires the
separate `auto_update_nodes` choice. Applying a backup with fleet auto-update enabled requires saved packages
for all currently known platforms; otherwise it stops before adoption. With fleet updates off,
remote Nodes keep their current programs and available saved packages are installed locally.
A backup replaces only its staged platforms. Other regular local packages remain usable at
their normal paths; an unlabelled retained package receives its **pre-rollback** `.version`
identity, while an existing valid explicit identity is preserved. An invalid retained version
marker stops package adoption and keeps staging for inspection.
Online/offline Link nodes, SSH/tailcat, busy checks, old capabilities,
fresh ownership and guardian rollback retain their original behavior in [node-update.md](node-update.md).
The Hub includes offline computers whose platforms it already knows. If an independent
assistant learns a computer's platform after the Hub builds its packages, the assistant
can build the missing package from the current confirmed commit. The Hub includes that
platform the next time it builds. With no known remote platforms, it builds only for its
own platform. For a freshly compiled development update, old
unversioned packages are set aside before installation so omitted platforms are not mistaken
for the new version. A saved-version application instead retains and labels omitted packages
as described above. Any set-aside or version-label failure stops fleet deployment.
A publisher-owned `bin/nodes` symlink remains an unsupported installation boundary.

In Web Settings, open a computer's row to reach **Redeploy** when available. The row
shows the installed version, any reported update phase or failure, and the reason a
deployment is blocked. With several assistants it names the known owner. The row shows
the package source when the assistant reports whether it used a Hub package or a local build.

#### Development recovery

Build, backup and known adoption failures leave the running build unchanged or restore it
through the existing transaction. A migration/startup failure uses the existing binary rollback;
**it never automatically overwrites databases with a snapshot**. Database migrations may require
the following operator recovery. **Apply backup** is program-only and does not perform this data
recovery procedure; use the offline procedure only when data recovery is actually intended.

1. Disable development automation. Stop every data writer and the Hub through their original
   service controls (including its socket/edge), and preserve the failed attempt's markers/logs.
2. Verify the chosen snapshot offline:
   `python3 server/internal/update/webdev.py verify /absolute/path/to/snapshot-<id>`.
   Do not continue on a failed checksum or archive check.
3. Extract history offline with
   `python3 server/internal/update/webdev.py extract-history /absolute/path/to/snapshot-<id> /absolute/path/to/empty-recovery-directory`.
   The destination must be absent or empty and cannot be a symlink. This command verifies the
   snapshot and applies the same canonical member/link checks before extraction on Python 3.9+.
   A restricted writer creates private directories, exclusively creates regular files, then
   recreates validated hard links and symlinks after all file writes. It preserves timestamps and
   safe file permissions, removes special and group/other write bits, and never applies archive ownership.
   A hard link must name a regular file in the archive. A stored symlink must point directly to
   an archived file or directory within its own root, using a canonical relative path. During
   backup, absolute symlinks within the same collected root are converted to this relative form.
   The extractor rejects absolute, drive/UNC, backslash, noncanonical and duplicate member paths,
   dangling links, link chains and links used as parent directories.
   `manifest.json` maps `root-0`, `root-1`, etc. to their original roots.
   Inspect the archived configs privately to identify each database. Restore the dumps with
   `pg_restore` into fresh, empty databases, using a private `PGSERVICE`/`PGPASSFILE`; then select
   those databases in the restored configuration. Preserve the failed databases until recovery
   is confirmed. Put the matching config, encryption keys, history and attachments back together.
4. Decompress the snapshot's `program-*.gz` files to the recorded program paths, verifying
   their native hashes in `manifest.json`. Restore each saved Node artifact from the manifest's
   `nodes` mapping to the installation's Node binary directory and verify its native hash.
   Use the extracted active `web/` for a whole-install restore. For an immediate transaction
   rollback, the journal records the program preimages and any detached override in `web_prev`.
   Preserve a subsequently changed Web directory. Start the original version, check history/data and node connectivity, then enable
   only the automation you want.

`coordination-pending.json` means a pause/resume outcome needs inspection: run the original
resume procedure and verify the writers before removing that exact marker. `.incomplete-*`
directories are disposable staging. A retry logs and removes them only after acquiring the lock
and finding no coordination marker. Interrupting a helper process signals the helper and its active child
group with SIGINT, then escalates child termination within bounded waits. The resume hook gets
its own 60-second deadline; additional SIGINT/SIGTERM signals are deferred until it finishes.
A failed/timed-out resume or a force-killed helper retains its coordination evidence and blocks
adoption. An interrupted snapshot is never published or rotated, even after successful resume.

`update/development/adopting.json` reserves an attempt, then records every program preimage
before replacement (`recovery_ready: true`). The Web rename intent is synced there before moving
the override. Startup of the previous or attempted build restores this journal when `pending.json`
is absent; recovery can repeat after a partial restore. Failed recovery retains the journal,
pending marker when present, and independent staged programs/Node packages across startup cleanup.
Only a complete restore permits cleanup; the standalone watchdog also retains the development
journal until restarting the restored program succeeds. A failed restart leaves the journal
and staged packages available to the next startup. An incomplete reservation (including a crash while
preparing preimages), unreadable journal or older fence remains blocked for manual inspection;
no live replacement occurs before the new journal is ready. Inspect the installed binaries,
`.prev`, Web recovery directory and backup, complete rollback
or confirmation, and only then clear the matching fence. Regular confirmation/rollback clears
its own matching fence. Never delete these records merely to make Retry available.

Local evidence: `scripts/test/test_web_development_update.py` uses controlled Git, compiler/DB
tool fixtures and a real synthetic writer process; `TestDevelopment*` exercises the existing
HTTP settings, shared install/confirmation, failure rollback and local node staging without
touching a running Persona installation. Database-tool fixtures do not prove restoration on a
particular deployment's PostgreSQL version or validate its operator-supplied coordination hooks.

### Private release worker

`scripts/release/hub_release.py --config <private-config.json>` can run from a
systemd user timer on a dedicated Linux build machine. It fetches the configured `source_branch` (default `main`) into an
exclusive checkout (never a developer checkout), runs server vet/tests and race checks, script suites, web
checks and the binary privacy scan, then publishes the tested commit. GitHub commit statuses are disabled by default.
Only the JSON boolean `"report_commit_status": true` enables pending/success/failure
reports (`hub-release/worker`, or the corresponding validation/manager context).
Missing or false makes no status API requests; local logs, receipts and Hub
publication metadata remain authoritative. This option does not rewrite historical
statuses. Opted-in statuses are independent CI results, not GitHub Actions results. This provides an execution path when hosted runner labels
have no available capacity or the operator cannot administer those runners.

The private config supplies `repository`, `checkout`, `state_dir`, `update_url`
and `publish_command` (an argv array), with optional `source_branch` (default
`main`). The dedicated checkout must use the selected repository as `origin`.
The worker verifies that origin identifies the configured status repository before
fetching or reporting statuses. State records bind the repository, source branch, update URL and commit: another source cannot
reuse a successful validation or an interrupted publication cache. The existing
build floor is retained when the source changes.
Use `--validate-only` to run the complete gates and retain a validated artifact
without publication. Its `hub-release/validation` status and `validated` record
are distinct from a confirmed `hub-release/worker` publication result. It never
invokes the publisher or changes the remote manifest. Normal retries can reuse
that artifact only for the same repository, branch, update URL, commit and package policy.
Use `--commit <full-sha>` to validate or reconcile one fixed candidate even when
main advances; both source checks must still prove it belongs to the configured
branch. Without this option, the worker follows the current branch head.

Templates are in `deploy/hub/`;
`hub-release.env` supplies `PERSONA_HUB_RELEASE_CHECKOUT`, the build-tool `PATH`,
and `PERSONA_CONFIG`. Use a private launcher when credentials need to be loaded
from a vault at runtime rather than stored in the environment file.
Enable the timer only after the bootstrap and publication checks below pass. The publisher is
`scripts/release/publish_hub_release.py --config <ssh-config.json>`; its config
supplies `remote_root` and `credentials_command`, which returns SSH credentials
as JSON (`host`, `port`, `username` and either `password` or `key_filename`, or a
fields array keyed by id). `key_filename` is the path of a private key file on the
build machine, for hosts that only accept key logins; exactly one of the two must be
set, and no other key or SSH agent is tried.
SSH host keys must already be trusted. Credentials are never written to release
artifacts. The publisher needs Python and Paramiko; the remote needs Python.
The optional `release_namespace` defaults to `hub` and selects a feed below
`<remote_root>/releases/`. Named feeds must start with `hub-` and use lowercase
letters, numbers and hyphens. Set the build's update URL to that same feed. A new
feed preserves the old manifest; it does not establish source ancestry or switch
an installation to the new feed. A history transition requires a separately
verified bootstrap. Standard publication and updater ancestry checks remain
unchanged within each feed. All feeds share one publication lock because the
installed Hub and managed node pointer are shared.
The build environment needs Node 24, Go matching `server/go.mod`, and a Python
3.11 environment with Pillow 12.0.0 and PyYAML 6.0.3 for the committed image and project checks. Keep these
in the release user's tool directories. For an isolated Docker Compose CLI plugin,
set `PERSONA_TEST_DOCKER_CONFIG` to a credential-free directory containing
`cli-plugins/docker-compose`; deployment tests only render configuration offline.
The worker passes its Python interpreter through to every script suite.
The SSH publisher uses Paramiko 4.0.0 in the same private environment.
The remote runs the publication program with Python 3.11 or later: `python3`, unless the
publisher config's optional `remote_python` names another interpreter (one program name or
absolute path, for hosts whose `python3` is older). The node and manager publishers use it too.

Build with `update_url=file://<remote_root>/releases/hub` for a private source.
By default, SSH uploads only the Hub platform and atomically promotes its manifest. The
existing updater reads that local source with the same checksums, binary version,
ancestry, idle checks, edge handover, confirmation and rollback as HTTPS sources.
An authenticated update check also evaluates the existing automatic-update policy;
it never overrides a disabled setting or retries a marked bad build.

The worker holds a local lock for the whole cycle, and publication holds a remote
lock through confirmation. Build numbers must increase and new source must contain
the previous source. Files have immutable checksum names. Interrupted attempts
reuse the same build and artifact, with a failure backoff; they cannot
turn a rolled-back build into a new version just by retrying. The backoff applies to
every new head of the followed branch, not only the failed commit, so a red `main`
is not rebuilt in full at every timer firing: 15 minutes after the first failure in a row,
doubling with each further failure up to 2 hours; `last.json` keeps the `failures` count
and a success resets it. `--retry` and a pinned `--commit` run immediately. Recent local attempts
(the last five, failed ones included) and remote artifacts have bounded retention. After each
attempt the worker runs `go clean -cache` when `go env GOCACHE` is larger than
`go_cache_limit_gib` from the worker config (default 20): Go itself only drops entries
unused for five days, which a worker validating every new head outgrows by tens of GB a day.
Failure leaves the updater in charge
of rollback; the publisher never restarts the host or the Hub service.

Bootstrap once with a verified binary containing the private update source, set
`auto_update_nodes: false` in `hub.json`, and enable Hub automatic updates using
`PUT /api/v1/hub/update/settings` with `{"auto":true}`. Use `hub switch` for the
bootstrap, preserving the previous binary. The worker then publishes and requests
an update check; confirmation requires the expected version/build, healthy Hub,
unchanged remote node versions and a matching installed binary checksum.

### Node upgrade policy

`hub.json`'s `auto_update_nodes` defaults to **false**. This applies to reconnect
upgrades, automatic fleet deployment, resuming queued transfers, downloading node
platforms and installing node packages after Hub confirmation. Explicit **Redeploy**
continues to work; an interrupted queued transfer requires another explicit
Redeploy while the policy is off. Already installed candidates still receive their
confirmation, so disabling upgrades cannot strand them.

A confirmed update to another version sets aside the node builds the Hub leaves in
`bin/nodes/`: each `personad-<os>-<arch>[.exe]` with no `.version` file beside it
becomes `<name>.prev`. The manager takes an unlabelled personad there for a build of
the Hub's own version, so left in place it would put the previous version back on
every computer that Redeploy reaches (#1825). Set aside, Redeploy fetches the Hub's
version from the release (`docs/operations/node-update.md`, "Node packages"). A build
with a `.version` file says which one it is and stays; the directory is never
created. With the policy on, this happens too, before the release's builds go in:
a platform the release does not carry would otherwise keep the old build. Releases
up to v0.9.1 carry no Windows `personad` in `files`, so a Windows build stayed
behind and every Redeploy to a Windows computer failed its self-check (#12958).

The manager receives this policy in `devices.config`. An old external manager that
does not understand it must be updated separately before relying on that gate;
the built-in manager changes together with the Hub. Existing package containment
must remain intact during a Hub-only rollout. The publisher rejects unmanaged
`bin/nodes` contents and never writes Android artifacts.

Set `publish_nodes: true` in both private worker and SSH publisher configs to
continuously publish matching `personad` and `persona-node` packages for
`linux-amd64` and `darwin-arm64`. This is package publication only: the publisher never requests
computer deployment and `auto_update_nodes` remains false. Users choose when
to update each computer in the client.

All four binaries are built from the same fixed clean commit with the same version,
release channel and build flags, after the full validation gates. The standard
release manifest binds compressed and executable SHA-256 hashes to that source
and build. The Linux `personad` descriptor must equal the Hub descriptor. Before
promoting the Hub manifest, publication verifies all archives, executable platforms,
Go main packages, clean VCS commits and hashes, and runs both Linux self-checks. Go
`-trimpath` omits linker flags from build info, so foreign runtime version values
are established by this controlled build manifest rather than a `.version` file.
An old Hub-only cache does not satisfy an enabled node publication requirement.
Existing `personad`-only node manifests remain valid for their original versions.

Verified executables and version sidecars are staged under the immutable
`releases/nodes/<version>/` directory. Once the matching Hub is healthy and
confirmed, `bin/nodes` atomically points there. Subsequent publications accept
only a managed pointer with matching metadata, sidecars and executable hashes;
they never restore quarantined packages. The pointer remains available for
explicit client updates. Existing computer versions must stay unchanged during
publication. `GET /api/v1/machines` can report package capability, but a previous
missing-package result may remain cached for ten minutes.

The same publisher also retains a consumer download mirror under the selected
`releases/<release_namespace>/node-packages/` (the private feed stays separate from
`releases/hub`). Each immutable `<version>/` has `manifest.json`,
`personad-linux-amd64.gz`, `personad-darwin-arm64.gz`,
`persona-node-linux-amd64.gz` and `persona-node-darwin-arm64.gz`. The manifest lists
the dedicated packages under `nodes.persona-node`. The mirror copies the verified
archives byte for byte and preserves their sizes, hashes and release metadata.
`source-manifest.json` retains the original producer descriptors. The publisher
reuses verified native packages. Versioned mirrors outlive native-cache pruning
and upload cleanup.

Download preparation runs inside the original `.publish.lock`, before Hub
promotion. The version directory and download `latest.json` become public only
after the original Hub health, confirmation and serving-process hash checks.
Linked directories/files, changed immutable contents and a different namespace
fail closed. Publishing these files never deploys a node or enables node updates.

A directory on disk is not a working remote package source. The configured
HTTPS provider must mount **only** this mirror as a read-only static root and map
`<base>/<version>/manifest.json`, its listed archives and
`<base>/latest.json` to it. An installation may expose only these privacy-checked
release artifacts anonymously over HTTPS. If its provider requires authentication,
the consumer supports credentials in `node_packages.url` through HTTP Basic;
it does not inject the Hub API token or arbitrary custom headers. Never put
credentials in artifact files. Allow only `latest.json`, valid versioned
`manifest.json` and the listed canonical archives. Return 404 for the release root,
directory listings, hidden staging directories, the producer's
`source-manifest.json`, unlisted paths and missing versions/files. A
reverse-proxy-only route does not serve these files. Select the formal
`node_packages.url` through the installation's managed configuration; an existing
`off` setting remains off until that owner changes it. This helper never rewrites
Caddy, mounts, credentials or a running Hub configuration.

Before reporting a remote source ready, fetch the versioned manifest and each listed
archive through the configured HTTPS provider, compare the manifest's
source, version and build to the confirmed running Hub, and verify each archive's
size, compressed hash and executable hash. Check that an absent version returns
404 and that a private provider rejects requests without credentials. Local
mirror tests and Hub confirmation do not establish remote reachability or
consumer installation. The publisher's confirmed receipt reports the relative
mirror directory and consumer manifest hash for this separate read-only check.

An explicit single-node publisher transfer can recover a transient Link failure
without starting another publication. Only identical chunks for the already
selected transaction, offset and bytes are retried (at most four attempts), inside
the original install deadline. Every relay call retains the manager owner, machine
UID, executable and platform checks. Identity conflicts and invalid offsets fail;
expired transactions never trigger a replacement `begin`. A commit is sent once:
a lost or gateway-error reply is ambiguous, so only a fresh matching handshake,
required capability and guardian confirmation establish success. Rejected commits
still fail, and cleanup errors retain the original transfer failure.

The SSH publisher sends keepalives and drains node-publisher stdout and stderr
concurrently into the original worker log, including transfer offsets and remote
tracebacks. The existing command timeout also bounds output collection. Missing
SSH exit status or an interrupted connection cannot create a confirmed receipt;
it does not reconnect and replay the remote publication. Keepalives reduce idle
connection loss but do not prove a broken route has recovered. Preserve the failed
run and let its sole runtime owner decide a subsequent explicit retry.

A Hub-only edge switch may proceed while sessions on proven resident nodes or remote herdr, herdr-core or Orca hosts
run: their processes belong to the terminal host (Orca's terminals to the Orca app) and survive the transport reconnect
(`hub/selfupdate_install.go` `updateBusy`).
Local and unknown/direct assistant sessions still wait for idle. Fleet upgrades
retain the conservative wait for every working session.

This private automatic publication mode supersedes the manual-only publishing
restriction below for configured private installations. Public `publish.sh`
releases retain their separate publishing workflow.

## Implementation progress

- **Step 1 (one binary and a version manifest) done** (the "Current state" below this section describes things before work started):
  - Skills out of the binary: `internal/assistantskills` only holds built-in skills (currently empty); node runs `Sync` every time it starts the assistant: it links the user's skills in `~/.persona/skills/`,
    deletes broken links, and automatically moves skills an older version installed into the assistant directory (`TestSyncMovesTheOldCopy`); the gate command only appears when a skill that carries it exists (`TestGatedCommandOnlyWithTheSkill`);
    without it, the assistant's CLAUDE.md only covers MCP's `lark_*` (golden `assistant_claude_md.bare`); the hub asks node via `skills.list`;
    `GET /api/v1/skills` is the read-only list for the App. The copy of the Lark gateway skill was moved under the repo's `skills/` at the time (#1571 deleted that copy and its sync script; the skill is now provided by the Lark extension).
  - One binary: no persona file builds differently with any tag of `server/build-tags.txt` (`TestOneBuild`), the line every release build uses. Its `public` tag is only a marker for the privacy scan (the scan uses it to recognize dependency constants it can skip); its `ts_omit_` tags leave out only the tailscale.com features persona never uses (`docs/operations/release.md`).
  - Channel and build number: `main.channel`, `main.build`, `main.updateURL`; publish.sh and deploy.sh stamp `release`, Docker stamps `docker`;
    `personad version -v` shows the channel and build number; `/api/v1/health` and `hello` carry `channel`, `build`, `protocol`, `min_app_build`.
  - Version manifest: `internal/update` (manifest, `CheckNext`, no downgrade `TestNoDowngrade`), `personad release manifest|check-next`;
    publish.sh publishes `latest.json` (sha256 of each file), and before publishing checks that the build number increases and that the live version's commit is an ancestor of this one.
    Step 1 originally had ed25519 signatures; after the user changed their mind, step 2 removed them (see "Security").
- **Step 2 (hub update and rollback) done**: `hub/selfupdate.go` (check, download and verify, wait for idle and "Update Now", replace, confirm or roll back, bad versions, update history, node updating along),
  `internal/update`'s download / replace / watchdog, `personad update-watch`; APIs in section 12.
- **Step 3 (App) done**: `apple/Shared/HubUpdate.swift` (state, history, Skills, compatibility check `HubCompat`, which banner to show `HubNotice`),
  `Views/Settings/HubUpdateViews.swift` (Settings › hub, banner, Skills page); after an automatic update is installed or rolled back, the hub sends a regular notification. Section 10 has been rewritten as the current state.
- **Step 4 (Docker check and notice) done**: a Docker hub asks the registry every hour which tags its image has (`internal/update/image.go`);
  if the newest release is newer than itself it shows a notice, and the App gives the command to run (pinned versions edit `.env` first). See the "Docker" section.

## Current state (verified 2026-11-02)

- Version: `personad version` is a string written in with `-X main.version`.
  - `deploy.sh`, `publish.sh`: `git describe --tags --match 'v[0-9]*' --always --dirty` plus a minute-resolution timestamp, like `v0.1.1-23-gabc1234-202611021530`. The nearest `vX.Y.Z` tag names it, a lightweight one too (a GitHub release's tag is one; plain `git describe` skipped v0.4.0, #7201), and `testflight/<build>` never does;
  - Docker image: built by CI when a `v*` tag is pushed; the version is the tag without `v`.
- hub:
  - `GET /api/v1/health` returns `{ok, version, features}`; the WebSocket `hello` carries `server_version`;
  - when a node's version differs from the hub's, the machine list marks it and offers **Redeploy** (`devices/deploy.go`, using binaries in `~/.persona/bin/nodes/` on the hub machine).
- The **only difference** between the full build and the public build is `-tags public`:
  - the full build embeds the Lark gateway skill into the binary with `go:embed` (`internal/assistantskills`, copied from upstream, not redistributable);
  - when node starts the assistant process, it installs it into the assistant directory's `.claude/skills/` and puts its command on PATH (`node/larkgate_env.go`);
  - the public build does not include it.
- Publishing: `publish.sh` builds the public build for four platforms, gzips them and, together with `SHA256SUMS` and `VERSION`, uploads them with wrangler to `dl/` under `publish.site_url` (Cloudflare Workers static assets),
  keeping only the latest version. wrangler uses an OAuth login that expires after about an hour, so it cannot run unattended.
- `install.sh` only installs the binary (`~/.persona/bin/personad`), not a service; the resulting hub machine has no `~/.persona/bin/nodes`, so tapping **Redeploy** on the public build fails.
- Our own hub is deployed over ssh (`deploy.sh`, plus the main session's deploy script), with three gates:
  - run all tests against real Postgres on the hub machine;
  - wait if the assistant has a turn running;
  - keep the previous version; done only once every machine has reconnected after the restart.
- App: Settings only shows the App's own version; machine details show the node version. There is no update check or self-update at all.

## Principles

1. **Only one binary**: skills are not in the binary; every hub (ours included) runs the same release build and can update automatically.
2. The user's skills live in their own space, and updates never touch them.
3. The publisher releases manually (`publish.sh`); each hub (ours included) updates itself when idle, then updates its own nodes only when `auto_update_nodes` is explicitly enabled. When a change on `main` is urgently needed, it can be deployed directly and will not be downgraded by an older public release.
4. Check sha256 before installing and have the new binary self-test; if any step fails, the current binary is left alone.
5. Do not restart while a chat is running or waiting for an answer.
6. If the new version does not start or machines cannot connect, automatically go back to the previous version; a version that was rolled back is not retried automatically.
7. No staged rollout: every gate before release must pass; after release all hubs update at once, and each rolls back on its own if it has problems.

## 1. Skills move out of the binary

- **The user's skill space**: `~/.persona/skills/<name>/` on the machine that runs the assistant.
  - Each directory is one skill (with a `SKILL.md` inside); users can add and edit them. Updates never overwrite them;
    an explicitly confirmed Web ZIP import can add a new name, with a narrow Undo for that new installation.
  - The machine that runs the assistant is usually the hub machine's local node; when the assistant is configured on another machine, it is that one.
- **Syncing into the assistant directory**: every time node starts the assistant process (both Claude and Codex assistants; `/agent` and the AI twin also use the assistant directory):
  1. For each skill under `~/.persona/skills/`, create a symlink at `<assistant dir>/.claude/skills/<name>`.
     When the user edits a skill, it takes effect the next time the assistant starts; Claude Code only reads skill files, so symlinks are fine.
  2. Delete links that point into `~/.persona/skills/` but whose target is gone; real directories the user placed directly in `.claude/skills/` are not touched.
  3. Built-in skills shipped in the binary (only ones we may redistribute; currently none) are still copied in by version stamp; a same-named skill in the user space wins.
  4. The gate command is only put on PATH when a skill that carries it is installed; the section about it in the assistant's prompt (`prompts/assistant/claude_md.md`) is also only written in when it is installed.
- **Migration**: two ways, each done only once.
  - **Automatic**: when node starts the assistant, each real directory under `<assistant dir>/.claude/skills/` installed by an old personad (with a `.persona-skills` version stamp next to it) that has no same-named entry in `~/.persona/skills/` yet
    is moved to `~/.persona/skills/<name>`, replaced in place with a symlink, and one log line is written; if a same-named user skill already exists, the user's wins.
  - **Manual** (on our hub, done by the main session): copy from the assistant directory's `.claude/skills/<name>` to `~/.persona/skills/<name>`.
    (Since #1571 such skills are provided by the Lark extension, see the top of `docs/extensions/lark-agent.md`; no more manual copying.)
  - That way the assistant still has its original skills when the new version starts. The copy of the upstream skill was moved under the repo's `skills/` at the time (the upstream sync script stayed, now writing there), and does not go into any binary;
    it does not matter that the public build lacks it: users put it (or their own skills) in `~/.persona/skills/`.
- **Drop `-tags public`**: there is no difference between the two anymore; `files_public.go` / `files_bundled.go` merge into one embed holding only built-in skills.
  - The privacy scan before release stays, and also checks that every skill in the binary is registered as redistributable (`assistantskills.Redistributable`);
  - A test guards it: the built-in skills directory may only contain skills registered as redistributable.
- **App**: Settings › Assistant › Skills lists name, description and user / built-in / plugin source.
  `GET /api/v1/skills` remains read-only and compatible with older clients; the hub asks the current assistant's node.
  The Web additionally offers **Upload ZIP**, described below. Native clients are unchanged.

### Web skill ZIP installation

For people adding a trusted skill package, Settings › Assistant › Skills has one **Upload ZIP** action.
Choose a file, wait for checks, review the plain-text name, description, actual destination and computer, then
**Install skill** or **Cancel**. Selecting a file never installs it. No shell, dependency installer, HTML renderer
or model is used to check a package. The feature changes no model/profile setting.

The manager authorizes the current assistant and its execution computer using the existing full-access route,
then sends `skills.import` to that node. The browser cannot select another machine or supply a path.
The node uses `assistantskills.UserDir`: normally `~/.persona/skills/<name>/`, respecting `PERSONA_SKILLS_DIR`
and the existing `PERSONA_HOME` resolver. This store is shared by assistants running as that machine's user;
it is not silently changed to a private manager store. A changed computer, root identity, expired reference,
offline machine or old node fails explicitly. Open the page again after changing environments.

Package contract:

- One ZIP with exactly one uppercase `SKILL.md`, either at the root or inside one enclosing directory; all
  resources must be in that same package. Nested skills, several manifests and outside-wrapper files are refused.
- The entire manifest is UTF-8, at most 64 KiB, with YAML frontmatter between `---` lines. `name` and `description`
  are non-empty YAML strings. The name (not the ZIP filename) is at most 64 ASCII lowercase letters/digits with
  single separating hyphens, not a reserved device name. Description is at most 4096 UTF-8 bytes.
  Duplicate YAML keys, aliases/anchors and custom tags are refused.
- At most 4 MiB compressed, 16 MiB expanded, 4 MiB per file, 512 archive entries and 12 path levels; maximum
  expansion ratio 200 per entry. Both declared and actually read sizes/CRC are checked. Only Store/Deflate,
  ordinary files/directories are accepted. No links, special files, traversal, absolute/drive/UNC/backslash paths,
  reserved names, duplicate entries, case/Unicode-normalization collisions or internal provisioning stamps.
- Preview extracts into a private temporary sibling and removes it before replying; only bounded, expiring metadata
  receipts remain (32 operations per node, ten minutes, with lookup aliases for completed installs). Install rechecks the exact same ZIP, current environment and conflicts,
  then publishes by an exclusive atomic rename. No existing directory, built-in, plugin or local runtime skill is
  overwritten or shadowed. Unsupported exclusive-rename platforms fail closed. Failure/cancellation removes staging.

After installation the existing list refreshes. A ten-minute **Undo installation** receipt exists only on this
connected page/node lifetime; reloading, switching assistant or node restart can invalidate it. Undo only detaches the
same unchanged new directory, with identity/content/mode checks; modified or replaced packages are refused. It moves
files to a hidden `.undone-…` sibling and reports that recovery path, rather than deleting them. This also protects a
local writer holding an open file during the move. There is no arbitrary-path delete or edit API. Recovery is retained
for the user; no background job deletes it or reinstalls an undone package.

A canceled preview has no durable staged files. Cancellation before the atomic publish cleans staging; a lost response
after publish may mean the whole installation completed, so refresh the list before retrying. Writes are never
automatically retried. Repeated/concurrent installation cannot replace the winning name.

Assistant-only MCP `skill_import` exposes `preview`, `install`, `status`, `cancel` and `undo` through that same manager
service and importer. It accepts base64 ZIP bytes, not download URLs or local paths. Preview returns an environment
and receipt; install sends the identical bytes with both. Before install or undo, the manager reads the exact node
receipt and shows a localized permission card naming the validated skill, computer and shared-account impact.
Only the user's own approval counts; no archive bytes or skill instructions appear in the card. The settings lock
is released while waiting and reacquired to recheck the target before the node call. New tools remain subject to
existing assistant/meeting restrictions; ordinary coding Agents do not receive them.

`status` accepts the original preview or install reference and environment. It records `previewed`, `installed` or
`undone` and recovers the installation's undo receipt or retained recovery location after a lost response. It does
not replay the effect or claim to be a live inventory. Receipt metadata survives only for its ten-minute node
lifetime; expiration or node restart requires inspecting Settings › Skills instead of assuming failure. Older
nodes without receipt status require an update before MCP installation. HTTP clients keep their existing flow and
may also query status; the additive result fields are backward compatible.

Discovery uses existing provisioning: `assistantLarkEnv` links the user store into `.claude/skills`, and
`assistantRuntimeSkills` links it into `.agents/skills`. Claude reads the existing assistant directory; Codex's
existing `skills/extraRoots/set` points at `.claude/skills`. No new discovery engine or `@skill` behavior is added.
The settings list is the Persona-provisioned inventory, not every skill a runtime might discover. Refresh lists
immediately; start a new chat to safely pick up installation/Undo, as already-running sessions may retain old context.
Tests exercise actual import/list/provisioning entry points and resource reads, not a paid Claude/Codex turn.

`TestImport*`, `TestSkillImport*` and `web/src/views/settings/Skills.test.tsx` cover archive safety, atomic races,
environment/credential changes, strict server-derived targeting and the confirmation/Undo states. The opt-in
`TestSkillImportBrowserHarness` uses the real manager handler and archive store with synthetic device transport
and a temporary directory; unrelated routes use `scripts/mock/mock_hub.py`. It is UI integration evidence, not a
production or live-model test. Its header documents loopback ports and shutdown; run with `SKILL_IMPORT_QA=1`.

The Skills group keeps only the list in its card; where the skills live, who shares them and when changes apply are
its one footer line. The upload dialog reuses existing web patterns: LookUpload's drop zone (choose or drop a ZIP, the
check's progress in its place), ProviderImport's preview card and Cancel/primary footer, and `ErrorNotice` for a refused
package.

Curated synthetic captures, actually inspected in a browser (not production assets):

| Capture | Light | Dark |
| --- | --- | --- |
| Desktop, standard text | [Light](../web/screenshots/skill-zip/preview-light.png) | [Dark, keyboard focus](../web/screenshots/skill-zip/preview-dark.png) |
| 390 px, largest supported text setting | [Light](../web/screenshots/skill-zip/narrow-light-large.png) | [Dark, keyboard focus](../web/screenshots/skill-zip/narrow-dark-large.png) |
| 390 px, Simplified Chinese | [Light](../web/screenshots/skill-zip/zh-preview.png) | [Dark](../web/screenshots/skill-zip/zh-preview-dark.png) |
| 390 px, Japanese | [Light](../web/screenshots/skill-zip/ja-preview.png) | [Dark](../web/screenshots/skill-zip/ja-preview-dark.png) |
| Skills group, Simplified Chinese | [Light](../web/screenshots/skill-zip/zh-group-light.png) | [Dark](../web/screenshots/skill-zip/zh-group-dark.png) |
| Choosing a ZIP | [Light](../web/screenshots/skill-zip/zh-choose-light.png) | [Dark, a file dragged over](../web/screenshots/skill-zip/zh-drop-dark.png) |
| Checking the ZIP | [Light](../web/screenshots/skill-zip/zh-checking-light.png) | [Dark](../web/screenshots/skill-zip/zh-checking-dark.png) |
| Refused package | [Light](../web/screenshots/skill-zip/zh-refused-light.png) | [Dark](../web/screenshots/skill-zip/zh-refused-dark.png) |
| Installed | [Light](../web/screenshots/skill-zip/zh-installed-light.png) | [Dark](../web/screenshots/skill-zip/zh-installed-dark.png) |
| Undone, recovery retained | [Light](../web/screenshots/skill-zip/zh-undone-light.png) | [Dark](../web/screenshots/skill-zip/zh-undone-dark.png) |
| Post-install Undo row, 390 px Large | [Light](../web/screenshots/skill-zip/zh-undo-row-light.png) | [Dark](../web/screenshots/skill-zip/zh-undo-row-dark.png) |

The 2026-11-15 capture run uses `web/e2e/skill-zip-visual.py` through Browser Use against the opt-in Go fixture above
and Vite. Its 22 captures were opened and visually inspected by Codex: text and status marks remain readable in both
appearances, the long install and recovery paths wrap, localized buttons stay on one line, and the dark preview has a
visible keyboard focus ring. `docs/web/screenshots/skill-zip/verification.json` records the source commit, source and
PNG hashes, viewport checks and inspection observations. The evidence commit changes no application source.

All Skills actions use `touch-hit`: the drawn buttons remain 32 px tall while their targets expand to at least
44 x 44 px on coarse-pointer devices. The browser run measures the pseudo-element's target bounds and hit-tests
points across that area, including the separate post-install Undo row. Native touch events below the painted button
activate Upload ZIP, Choose ZIP, Cancel, Install, Close and both Undo actions. The file input's click is intercepted
to keep the operating-system picker out of the automated run; CDP supplies the real ZIP afterwards.

To reproduce, start an exclusively owned mock on `<mock port>`, run the fixture from `server/` with
`SKILL_IMPORT_QA=1 SKILL_IMPORT_QA_ADDR=127.0.0.1:<manager port> SKILL_IMPORT_QA_MOCK=http://127.0.0.1:<mock port>
go test ./internal/assistant -run '^TestSkillImportBrowserHarness$' -count=1 -v -timeout=30m`, and start Vite with
`PERSONA_HUB_URL=http://127.0.0.1:<manager port>`. Then, from the repository root:

```sh
PERSONA_SKILL_ZIP_ORIGIN=http://127.0.0.1:<web port> \
PERSONA_SKILL_ZIP_SHOTS=<evidence directory> \
PERSONA_SKILL_ZIP_WORK=<scratch directory> \
browser-use < web/e2e/skill-zip-visual.py
```

After inspecting the PNGs, record the observations in `verification.json`; the capture script initially marks visual
inspection pending. POST `/_qa/stop` to the manager fixture, then stop the owned Vite and mock processes. The browser
check delays one real preview response to exercise the busy state; native CDP file drag events must still cancel the
browser defaults without another preview request. The real importer supplies all validation, install and Undo replies.
Native link and plain-text drags also cancel `dragover` and `drop` without navigating, highlighting the zone or
starting an upload; only file drags can highlight it.
The screenshot report is evidence for review, not an automated-review credential: the maintainer's watcher still needs
its trusted `ui_evidence_command` adapter (`docs/operations/pr-review-automation.md`, "Configure").

Browser observations: Cancel initially owns preview focus; Tab reaches Install with a visible ring; Escape closes
and restores Upload ZIP focus. No horizontal overflow at 390 px, including Large. An empty description shows an
actionable validation error. Installation creates `example-visual` in the real fixture store/list; Undo removes its
list entry and retains its resources in the reported recovery sibling. These are local fixture results, not live
Claude/Codex discovery.

## 2. Build channels

The binary gets three more `-X` variables (`cmd/personad`):

| Variable | Value |
| --- | --- |
| `main.channel` | `release` (published, updates automatically), `docker` (in the image, only suggests pulling a new image), `dev` (default when unset: local builds, tests) |
| `main.build` | A monotonic build number, see "Version numbers" |
| `main.updateURL` | The build takes its release URL from a non-empty `PERSONA_UPDATE_URL`, then the config's `publish.update_url`, then `dl/` at the publish address (`publish.site_url`). See "Update source" for runtime overrides and the Web development fallback. |

- Only `release` swaps its own binary; `docker` tells the user to pull the image; `dev` only shows the version.
- Direct deploys (`deploy.sh` and the main session's deploy script) also stamp `release` and use the build time as the build number: newer than the public release at the time, so they are not downgraded; once the publisher releases a newer version, the hub takes over on its own (section 6).
- `/api/v1/health` and `hello` also carry `channel`, `build` and `protocol`.

## 3. Version numbers: release time as the build number

- Newer vs older is compared by **release time** (UTC, `YYYYMMDDHHMM`, an integer); the version string stays as is, for people only (decided by the user 2026-11-02).
- Why:
  - every version string already ends with this time, so there is no new counter to maintain, and a tag is not required every time;
  - there is only one publisher, so time naturally increases;
  - nobody has to decide whether this counts as minor or patch.
- Guarding against non-monotonic numbers: `publish.sh` reads the live manifest and refuses to publish in two cases:
  - the new build number is not larger than the live one;
  - the live version's commit is not an ancestor of this one.
- A build number only says it was released later, not that the code is newer: before installing automatically, the hub also checks whether the new version's code contains its own commit ("no regression").
- Docker keeps the image tag's semver, compared only within the docker channel.

## 4. Version manifest

`dl/` gets one more file, `latest.json`:

```json
{
  "version": "v0.1.1-30-gabc1234-202611021530",
  "build": 202611021530,
  "commit": "abc1234…",
  "released": "2026-11-02T07:30:00Z",
  "min_app_build": 95,
  "protocol": 3,
  "notes": "Highlights of this version",
  "files": {
    "linux-amd64": {"name": "personad-linux-amd64.gz", "size": 12345678, "sha256": "<of the archive>", "binary_sha256": "<after decompression>"},
    "linux-arm64": {}, "darwin-arm64": {}, "darwin-amd64": {}
  },
  "ancestors": "<base64>"
}
```

- `edition`: left out for the full release; `"core"` in the core edition's manifest, which is served as
  `core-latest.json` beside this one with every download under `core-<name>` (each file's `served`). A build reads
  only its own edition's manifest and refuses one of the other edition, so a core-only hub never installs the full
  build and a full hub never the core-only one ([core-install.md](core-install.md), "Updates stay core-only").
- `ancestors`: all commits reachable from this commit (`git rev-list <commit>`), the first 6 bytes of each, sorted, then base64 ("no regression").
- `programs`: programs published together with personad that the hub runs alongside (`update.ProgramNames`:
  `persona-hub` and `persona-manager`, [../server/server.md](../server/server.md) ("Programs")):
  `{"persona-manager": {"linux-amd64": {name, size, sha256, binary_sha256}, …}}`. Old hubs ignore this field
  and still only swap personad. Messaging extensions, including WeChat and Telegram, are published through the
  extension marketplace, not bundled program downloads.

- `nodes`: the other programs a node can run, published with personad (`update.NodePrograms`: `persona-node`, the device's own): `{"persona-node": {"linux-amd64": {name, size, sha256, binary_sha256}, …}}`.
  `node_protocol`, `node_protocol_min`, `node_capabilities`: what the release's nodes announce in `node.info` (`docs/protocol/protocol.md` ("Node handshake")), so a manager of another build sends one only when it drives it (`docs/operations/node-update.md` ("Node packages")). Old hubs ignore all three.
- `helpers`: native programs a node installs on demand, by name then platform (`persona-iphone-bridge`: `{"persona-iphone-bridge": {"darwin-arm64": {name, size, sha256, binary_sha256}, …}}`, [iphone-bridge.md](../extensions/iphone-bridge.md) "Releases"). Each has its own version, so no build number is checked in it; a platform the release could not build is left out. Hub and manager updates never swap them. Old hubs ignore this field.
- `links`: independently installed persona-link downloads: `{"linux-amd64": {name, size, sha256, binary_sha256}, …}`. Windows names keep `.exe` before `.gz`, for example `persona-link-windows-amd64.exe.gz`. The publisher verifies these downloads like the others, but hub and manager updates never install or swap them. Old hubs ignore this field.
- Every release is also kept at `dl/<version>/` (its node builds, `personad-*` and `persona-node-*`, and this manifest as `manifest.json`; also `dl/X.Y.Z/` for the tag `vX.Y.Z`), where managers fetch the node build of their hub's version; `dl/latest.json` and `dl/*.gz` stay the newest release.
- A downloaded file must match both `sha256` (archive) and `binary_sha256` (decompressed), and the new binary's `version -v` must report the version and build number in the manifest.
  `SHA256SUMS` stays for install.sh.
- The publisher checks the same before anything goes up (#4522): v0.2.1's `latest.json` said build 202611110749 while every binary beside it was 202611110745. A later run had written the manifest over an earlier run's binaries, so every hub downloaded the release and then refused it.
  `personad release manifest` now refuses to describe binaries of another build, and `personad release verify --dir <dir>` reads a written `latest.json` back against its downloads: sizes and sha256s; the binaries this machine runs (its own platform's) must report the version and build in `version -v`; every other binary must hold the build number (the `-X main.build` value is kept in each binary as it is, whatever its platform).
  `publish.sh` runs it right before uploading. The tag publisher (`scripts/operations/tag_release.py`) checks a receipt that carries `latest.json` the same way before attaching it to a GitHub Release (running this machine's `personad version -v`), also checks the installer's `DL` default and refuses any `personad` that does not carry the Release's exact GitHub update base, and uploads `latest.json` after the files it lists. Before a stable hub publication marks the Release Latest, it reads the immutable tag's served `latest.json` and requires this tag's version and build; an older stable backfill never moves Latest backward.
- `min_app_build`: Apps below it cannot use this hub version. For `protocol`, see "Compatibility".
- There is no minimum node build: a node reports a version string, and the hub redeploys any node whose version differs from its own. That redeploy runs after the new hub has confirmed
  (section 8, step 9 "Update nodes along with it"), not before the new hub starts; a machine that fails to update keeps the reason.

## 5. Public publishing (manual, publish.sh; user, 2026-11-02)

Public releases use `publish.sh` manually on the publisher's dev machine, and the site's Worker is under the publisher's Cloudflare account. Private canonical-branch releases use the serialized worker described in "Automatic private Hub releases".
- The push relay (`docs/operations/push-relay.md`, `/relay/*`) is in the same Worker as the downloads, so every time `publish.sh` redeploys the site it deploys the relay too
  (a Worker deploy is an atomic switch, never half old and half new; relay code changes also go live with this release).
- Tests must still pass before publishing, plus `publish.sh`'s own checks (if any fails, nothing is published):
  1. Read the live `latest.json` (if any): the new build number must be larger, and the live version's commit must be an ancestor of this one (`git merge-base --is-ancestor`);
  2. Build the four platforms (with `channel=release`, `build`, `updateURL` written in), privacy scan (the binary may only contain redistributable skills);
  3. Generate `latest.json` (version, build number, commit, the commits it contains `ancestors`, release time, sha256 of each platform's archive and decompressed binary, `min_app_build`, `protocol`), refused when a binary is not that build, then read it back with `release verify` before any upload ("Version manifest");
     do not publish when the working tree has uncommitted changes in server/ or web/ (`NO_DEPLOY=1` still builds, and the commit in the binary carries `-dirty`): whatever commit the manifest names, the binary must be exactly it;
  4. After publishing, read back `VERSION`, `SHA256SUMS`, `latest.json` and compare them with the local ones.

### Security: no signing (decided by the user 2026-11-02)

User: "Why sign at all? Skip signing for the Linux service, it's too complicated." The trade-off, spelled out:
- **Protects against**: corrupted or truncated downloads (if sha256 does not match, delete and do not install), and installing a binary that does not run or is not this version (`version -v` self-check, plus confirmation after it starts, otherwise roll back).
  Downloads go over HTTPS; tampering in transit requires breaking TLS first.
- **Does not protect against**: the publish site (the Worker at `publish.site_url`, the publisher's Cloudflare account) being impersonated or compromised. Then an attacker could replace both the binary and the sha256 in `latest.json`,
  and every hub with automatic updates on would install it, running as the user that runs the hub.
- The user knows this and decided against signing. To add it later: put a signature next to the manifest, compile the public key into the binary, and have the hub install only what verifies (step 1 once implemented this; see git history).
- Hubs that do not want this risk: turn automatic updates off in the App, or deploy with Docker (you decide when you pull the image; but see the next point for the node builds a Docker hub fetches).
- Node builds the manager fetches for the computers (`docs/operations/node-update.md` ("Node packages")) are checked the same way: the manifest's sha256s over HTTPS, the build's platform and kind, and `version -v` where it can run (the manager's own platform; over the node RPC the node's `node-update-check` reads the version too, the ssh and tailcat paths do not). Not signed either. A hub sends builds of its own version (the release's of that version included) to its computers by itself, Docker hubs too and whatever the automatic-update setting; a fallback build (another version) only when someone chooses **Redeploy**, and an update queued for the hub's version never resumes as one. A build put in `bin/nodes` by hand with a `.version` of another version goes out by itself too: putting it there is the owner's choice. `node_packages.url` set to `off`, or builds put in `bin/nodes` by hand, avoids the site for them.

## 6. Direct deploys (when a change on the default branch is urgently needed)

The existing path of building from the default branch and deploying directly (`deploy.sh`, plus the main session's deploy script) stays, for when waiting for a release is not an option.
- The resulting binary also gets `channel=release`, `build` (build time, UTC `YYYYMMDDHHMM`) and `updateURL`:
  - it is newer than the public release live at the time, so the hub's auto-update will not install the older public release: **no downgrade**;
  - once the publisher releases a newer version (larger build number), the hub takes over on its own;
  - `TestNoDowngrade` guards this: if the manifest's build number is not larger than its own, it is not installed; equal is not installed either.
- It also gets `commit` (`-X main.commit`, with `-dirty` appended when server/ has uncommitted changes): a later public release only installs itself if it contains this commit ("no regression").
- The main session's deploy script lives outside the repo and must add the same four `-X` flags to its `go build` (the report gives the line to add).
- **Deploy only from a working tree that contains the default branch**: `deploy.sh` first fetches the remote's default branch (how it finds the branch: the comment above the check); if the current HEAD does not contain it, nothing is deployed and it exits 2 (same if it cannot be fetched);
  deploying a branch that is behind would swap changes someone else just deployed (hub and web) back to old ones. For a deliberate rollback, use `PERSONA_DEPLOY_ANY_COMMIT=1`. See `docs/lessons/server.md` ("Deploying from a stale branch").
- WeChat and Telegram are installed and upgraded from the extension marketplace; `deploy.sh` does not build their programs.
- **Update a hub with `deploy.sh <hub machine id>`, not by copying a binary and running `personad hub switch` by hand.** `deploy.sh` first puts this build's node packages for every platform in the hub's `bin/nodes/`, then switches the hub blue-green (no restart while the socket units are unchanged, `docs/operations/hub-restart.md`).
  A hand switch leaves `bin/nodes/` on the previous build. The manager takes an unlabelled `personad-<os>-<arch>` there for its own version only when the hub's version string is in the file (the node's self-check would refuse it otherwise, #12958), so the old builds are no longer sent as the new version; the computers fall back to the release of the hub's version, or wait until the right builds are in place (a changed file is looked at again at once). Before that check, the new hub pushed the old builds to every computer the moment they reconnected.
  To check: every `bin/nodes/persona-node-*.version` on the hub machine equals the hub's `/api/v1/health` version.
- No staged rollout: after a release all hubs (ours included) update at the same time when idle; problems are handled by each hub's own rollback and bad-version marking.

### No regression (2026-11-02, #454)

Comparing only build numbers can go backwards: a directly deployed hub runs commit D on `main` (build number = deploy time); later someone runs
`publish.sh` from code that is behind `main` (commit B), the build number is larger, and the hub would "update" itself to the older B. `publish.sh`'s `check-next` only compares against the live release and cannot cover directly deployed hubs.

- **How it decides**: the binary records its own commit (`-X main.commit`), and the manifest carries all commits the new version contains (`ancestors`).
  The hub only installs automatically when the new version is this very commit, or `ancestors` contains it (`update.Build.Hold`, `TestHold`).
- **Why not ask GitHub**: the repository is private, the hub cannot necessarily reach GitHub, and it should not be given a token; with the manifest carrying the list, no network lookup is needed.
  6 bytes per commit, 900 commits is about 7 KB (base64); a few hundred KB a year from now is still within the manifest's 1 MB limit;
  the chance that a commit not in the list happens to collide with some prefix is about 10^-10 at ten thousand commits.
- **Cannot confirm counts as not satisfied**: the binary has no recorded commit (old deploy scripts), it has uncommitted changes (`-dirty`), the commit is too short, the manifest has no `ancestors` (old `publish.sh`), or it cannot be read.
  A direct deploy from a PR branch that is later squash-merged into `main` cannot be confirmed either (the merge produces a different commit): the next deploy from `main` or a manual update fixes it.
- **When not satisfied**:
  - It is not installed automatically. The App still says a new version is available, also when automatic updates are on; Settings › hub shows the reason (`latest.held`), and manual **Update Hub** can still be tapped;
  - The update history gets one "not installed automatically" entry per new version (`result: held`) with the reason, e.g. "新版本（提交 b2b2b2b2）不包含这个 hub 正在运行的提交 d4d4d4d4" ("the new version (commit b2b2b2b2) does not contain commit d4d4d4d4 that this hub is running"; text emitted by the hub).
- When automatic updates are off it would not install by itself anyway, so nothing is recorded.

### Programs swapped together with personad (2026-11-03, #533)

The programs (`update.ProgramNames`): `persona-hub` and `persona-manager`, the hub's and the manager's own programs ([../server/server.md](../server/server.md) ("Programs")).

- When the manifest has `programs` for this hub's platform, they are downloaded and verified (same sha256, and `version -v` must be this version), then swapped in together with personad: placed next to personad,
  existing ones kept as `.prev`, and ones that did not exist before recorded as newly installed. If any one fails to download or verify, the whole update is skipped. A program whose swap fails midway is left as it was, and going back leaves it too.
- Rollback (the new version does not start, the watchdog, an error during the swap) swaps them back together with personad and deletes the newly installed ones (`update.RestoreAll`, `programs` in `pending.json`).
- **Which program the hub goes on as** (`run` in `pending.json`, `Pending.RunPath`): a hub run as `persona-hub` restarts, hands over and rolls back as the `persona-hub` the update swapped, so it stays on its own program; a hub run as `personad` stays on `personad` (no `run`).
  `target` stays `personad`, the release's main download and what the watchdog runs as `personad.prev`. A build that predates `run` (an older edge or watchdog) reads only `target` and brings the hub back as `personad hub run`, which serves the same hub; a `pending.json` an older build wrote has no `run` and goes back to `target` as before.
  A release without `persona-hub` for this platform leaves a hub run as `persona-hub` by hand on `personad` (which the update did replace), not on a `persona-hub` it did not.
- **Which program a service runs** is its unit's (or agent's) to say: an update replaces the files and never edits a service. On Linux `hub setup` writes the hub's unit to run `persona-hub run` and the built-in manager's to run `persona-manager assistant` when each is beside `personad` and its build, and on a Mac the launchd agents by the same rules, but an agent that ran `personad` before stays on it ([../server/server.md](../server/server.md) ("Programs")). A unit it wrote earlier runs `personad` until setup runs again (the install command with `--hub` runs it), which moves the unit to the program and restarts it; `PERSONA_SPLIT_PROGRAMS=0` in setup's environment keeps it on `personad`, and the unit keeps the variable until setup runs again without it. A self-update alone never moves a hub between `personad` and `persona-hub`.
- **A release without `persona-hub` or `persona-manager` for this platform** (`hub.unreplaced`) leaves that file as it was. That is fine while nothing runs it, and the update goes ahead.
  It is not installed while a service runs that program, because the service would go on starting the old build beside the new `personad` (`hub/selfupdate_services.go`). A service here is one of:
  - A systemd unit whose `ExecStart` runs it: the last one in `[Service]`, drop-ins read after the unit, read as systemd and `hub-systemd.sh` read it. The user's units come from `~/.config/systemd/user`, `/etc/systemd/user`, `~/.local/share/systemd/user` and `/usr/lib/systemd/user`, the machine's from `/etc/systemd/system` and `/usr/lib/systemd/system`. A unit in an earlier directory shadows one by the same name in a later one, and drop-ins come from all of them. It counts when systemd says it is enabled (or static, generated…) or active; when `systemctl` cannot answer (no user manager to ask, say), every such unit counts.
  - A launchd agent or daemon (`~/Library/LaunchAgents`, `/Library/LaunchAgents`, `/Library/LaunchDaemons`; XML, or binary through `plutil`) whose program is it: `Program`, else `ProgramArguments[0]`. One that says `Disabled` does not count.
  - The systemd service the hub itself runs in, when it runs as `persona-hub` and that unit's own `ExecStart` is this `persona-hub` or `personad` (switched to `persona-hub` inside it). In any other service (a terminal multiplexer's, `systemd-run`'s) it was started by hand.
  Not seen: units under `/run`, units under `/lib/systemd/{system,user}` where `/lib` is not merged into `/usr`, template instances (`name@instance.service`), a launchd job that says `Disabled` but was enabled in launchd's override database (`launchctl enable`, `launchctl load -w`), a program wrapped in another command (`sh -c '…/persona-hub run'`) or named without a path. Such a service does not hold the release back.
  The services are looked at when the release is read and again just before the swap (a unit pointed at the program while the update waited for idle).
  Settings › hub shows the reason (`latest.held`, as for a release behind this hub's code above), the history gets one `held` entry per release, and **Update Hub** is refused with the same reason (409).
  `install.sh` also refuses such a release before anything changes, with a wider check of its own: a unit file or drop-in in those systemd directories that has the program's path in an `ExecStart` line (as is, under `%h`, or as systemd escapes it), or a property list that has it in a `<string>`. A unit a later drop-in took back to `personad`, a disabled one and a wrapper count there too, so it refuses some releases the hub would install.
- The first update that carries programs: the watchdog running is the old personad, which does not know `programs`, so a rollback only swaps personad back; the new programs left behind do not match the old hub version, and the hub will not start them
  (status says "not the same version"), until the next update.

## 7. Checking for updates

- **Release build**:
  - The hub checks 1 minute after startup, then every hour (every 6 hours until 2026-11-07, when the user asked for hourly so a release reaches hubs sooner); the App can "Check now" (`POST /api/v1/hub/update/check`);
  - It only fetches the manifest (a few KB): there is an update only if the build number is larger than its own and not in the bad-version list; the result is broadcast to open Apps.
  - While an install is running or the new process has not confirmed the update, checks refresh `latest` and `last_check` without changing `state`, `progress` or `error`. This applies to manual checks, the first check after startup and hourly checks, including responses to requests started before the install. A failed release check advances `last_check` and keeps the previous `latest`; it still returns an error to the caller. Confirmation also prevents a check from starting another automatic install.
- **Docker build**:
  - Every hour, query the image repository's tags (anonymous GHCR `tags/list`), take the highest stable semver, and show a notice if it is newer;
  - The hub installs nothing; Docker pulls the image and verifies by digest;
  - CI writes the image name into the binary at build time; it is not hard-coded in the source.
- **dev**: no checks, only shows the version.
- Network unavailable or bad manifest: show the reason under **Check for Updates** in the App when no install or confirmation is in progress; otherwise keep the update's progress and error. Do not hammer retries (keep the hourly interval).

### Update source

Where a release hub reads `latest.json` and downloads from (#4522). v0.2.1's binaries were built pointing at a publish site that stayed on an older release, and nothing short of a new build could change that.

- **Which source**: `hub.json`'s `update.url` when set (`docs/operations/config.md`), else the URL the build was made with (`main.updateURL`). The value is the directory `latest.json` is in. A GitHub repository's release base works as it is:
  `https://github.com/<owner>/<repo>/releases/latest/download` redirects `latest.json` and `personad-<os>-<arch>.gz` to the newest release's assets, and the client follows the redirects. A release build made without an update URL updates itself once `update.url` gives one. The Release installer reads that moving feed's `latest.json` once, then downloads `SHA256SUMS` and every archive from the immutable `/releases/download/v<version>` tag, so a Latest promotion during installation cannot mix two Releases. Legacy manifests whose version is `vX.Y.Z-YYYYMMDDHHMM` pin the immutable `vX.Y.Z` tag in the same way.
- **Web development fallback** (#17646). Outside private network mode, a `web-dev-*` release-channel build with no embedded or configured source uses `https://github.com/nwverse/persona/releases/latest/download`. Operators can switch to Release or Beta and check the chosen channel. New Web development builds embed the effective release URL in `main.updateURL`, using the same default when there is no configured or embedded URL to inherit.
- **Private network mode.** With `security.private_network_only` enabled, release updates require an explicit, valid `update.url` in `hub.json`. Set it to an internal release URL and restart the hub.
- **A release published between a check and the update** (#9263): the update installs the release it checked, and takes every file of it (the hub's binary, the programs beside it, the node builds) from where that release stays. From a GitHub repository's `releases/latest/download` that is the release's own tag, `releases/download/v<version>/` (the tags releases are published under, `docs/operations/release.md`); a repository whose tags are not `v<version>` has no such tag, and its files come from the base as before. Any other source (a publish site's `dl/`, a mirror, a `file://` copy) has the files only beside `latest.json`, where a newer release replaces them, so a download there that does not match its manifest is followed by one more read of `latest.json`. When that names another release, the update stops saying a newer release was published while the checked one was downloading, the hub offers the newer release from then on as a check would, and the next **Update** (or automatic update) installs it. When it names the same release the download is broken, and the update fails with the mismatch as before. Nothing is downloaded again either way. Before this, a hub on the GitHub source failed every **Update** with "the download does not match the release's manifest" from the moment a newer release was published until it checked again.
- **What follows it**: everything the hub fetches as "the release": its checks, downloads and node binaries for every platform; the node builds the manager fetches when `node_packages.url` is empty; the manager install script's download address (its sha256s come from the same `latest.json`); and the `install.sh` line for a new computer (the source without its `/dl`).
  GitHub's base has no `<version>/manifest.json` or `<version>/` builds, so a manager there takes the node builds `latest.json` lists, from beside it: the hub's own version's when the newest release is the hub's, else the newest release's when it drives them (`docs/operations/node-update.md` ("Node packages")).
- **What it may be**: `https://`, `http://` only to this machine (nothing is signed, "Security"), or `file:///<directory>` (a private publisher's copy), with no user, password, query or fragment, and not `latest.json` itself. An invalid `update.url` blocks release checks. Settings › hub shows the reason (`why_not`), and the log records it at startup. Both omit the address.
- **Proxy**: `update.proxy` (`http://[user:pass@]host:port`, `internal/netproxy`) carries these requests alone (manifest, downloads and a Docker hub's registry checks). It is also the node builds' proxy when they come from the update source and `node_packages.proxy` is empty. Everything else the hub talks to goes as before: a proxy in the service's environment (`HTTPS_PROXY`) would carry all of it.
  Without `update.proxy`, these requests use the environment's proxies, as before. One that does not read fails every check with the reason rather than going around it. The log, `GET /api/v1/hub/update` and the errors show the proxy as `http://host:port`, never its user and password.
- **Seeing it**: `GET /api/v1/hub/update` carries `update_url` (the source in effect), `update_url_from` (`hub.json` for a configured source or `build` for an embedded or built-in one) and `update_proxy`. Changes to `hub.json` take effect when the hub restarts.
- **A source that stopped** (#8108): when the newest release a check reads is older than the hub's own build and older than the release its code contains (the `vX.Y.Z` its version starts with: `git describe` names a build after the newest tag in it), the source no longer takes releases, and the hub would otherwise find itself up to date at every check while its phones get no newer Android app. `GET /api/v1/hub/update` then carries `source_behind: true` (`latest` is the source's newest release), Settings › hub says so in place of "Up to date", naming the source and `update.url`, and the log says it once per source and release, not at every check. A source as new as the hub, newer, or holding the release the hub's code starts from (GitHub Releases for a hub deployed from `main` after that release) reports nothing. A version that names no release (`dev-…`, `master-<commit>-…`) cannot tell a stopped source from a quiet one, and reports nothing either.
- **A hub deployed with `deploy.sh`** carries a non-empty environment `PERSONA_UPDATE_URL`, then the config's `publish.update_url`, else `<publish.site_url>/dl`: a config with only `site_url` pointing at the publish site gets the frozen `dl/` below, so the hub never sees a newer release or offers its phones the Android app. `deploy.sh` prints which source the hub will carry when `publish.update_url` is not set; set it (for example to the GitHub Releases base) before deploying, or `update.url` in the hub's `hub.json` afterwards.
- **The default of official builds** is the GitHub Releases base (`https://github.com/nwverse/persona/releases/latest/download`, through `publish.update_url`), the same base the Release's `install.sh` downloads from; v0.5.2 and later carry it. Releases up to v0.4.0 were built with the publish site's `dl/`, which has stayed on the 2026-11-07 build since the repository history was rewritten (the rollback guard refuses to publish over it), so those hubs never see a newer release: set `update.url` to the GitHub base, or upgrade once by hand. The getting-started guide and the website's install command point new installs at the Release's `install.sh` for the same reason. `update.url` still lets a hub choose another source without a new build.

On the web, **Settings › This hub** shows the latest version from the selected Release or Beta source and its publication time when available (#15710).
With no result yet, it prompts for a check. If the installed build number is higher than the source's latest build, it explains that updating from that source would require a downgrade. A stopped-source warning takes precedence.

### Beta updates (opt-in)

**Stable remains the default.** Settings › hub lets a person choose the beta update source explicitly; nothing about an existing installation switches it to prereleases. Choosing **Release** returns to the stable source. A running check, download or confirmation must finish before changing channels, and switching discards the other channel's candidate rather than installing it. The existing `web-dev` private build/backup channel stays separate.

- **Feed contract**: the official stable feed is `https://github.com/<owner>/<repo>/releases/latest/download`. Beta uses the independent prerelease pointer `https://github.com/<owner>/<repo>/releases/download/beta-channel/latest.json`; it never moves the stable `releases/latest` pointer. Its manifest has `"channel":"beta"` and a `X.Y.Z-beta.N` version. Stable manifests without a `channel` remain valid only on the stable source. The hub rejects a stable/beta manifest or pointer mix-up rather than silently falling back. Custom/private `update.url` sources have no automatic beta replacement.
- **Immutable files**: `beta-channel` carries only the index. `scripts/operations/beta_channel_release.py` verifies the immutable prerelease's source, manifest and remote index digests, then points it at the newest verified beta. Actual programs still download from `releases/download/vX.Y.Z-beta.N/`, so a subsequent prerelease cannot replace bytes selected by an earlier check. Beta build numbers are UTC timestamps like stable ones and must increase; the channel choice is not permission to downgrade silently.
- **Android**: the same selection controls `GET /api/v1/app/android/latest`. Stable keeps the existing `apps/android/` cache; beta uses a separate `apps/android/beta/` signed-APK cache. This avoids a higher stable `versionCode` blocking a return to stable, or a beta APK masquerading as the stable offer. Package and signing-certificate checks remain mandatory. A beta without `android.json` never falls back to the stable APK. Android installation still asks the user; no device is silently upgraded.
- **Publisher**: use a new immutable `vX.Y.Z-beta.N` tag and GitHub prerelease, not an existing stable tag or `beta-channel` as the version tag. Build with `RELEASE_CHANNEL=beta scripts/release/publish.sh` so `latest.json` declares the channel, and use the normal formal Android signing pipeline (`--version X.Y.Z-beta.N`). Only after those verified assets exist may the pointer publisher run. A Release marked stable is rejected for a beta tag.
- **Apple**: TestFlight has its own signed beta build/review lifecycle. A GitHub prerelease does not make an iPhone or Watch beta available; report `VALID`, Internal availability and external Beta App Review separately. Formal stable and beta IPA/TestFlight identities are never inferred from a development IPA.
- **Compatibility**: an older hub ignores the new setting and remains on its configured stable source. New clients do not invent a beta source when `update.url` is private or a hub is Docker/development; Docker images still follow their registry's stable tags. The beta setting is stored in the same `hub_update` Postgres settings record and follows the same save-failure rollback as the other update choices.

### The Android app

A hub with an update source offers its phones the Android app of the newest stable release that carries one, with no setup (#5563). The phones update from their hub (`GET /api/v1/app/android/latest`), which serves what is in `apps/android/` (`docs/protocol/protocol.md` ("Android packages")); until now only a publisher copying an APK over SSH put anything there.

- **What a release carries**: a version-tag release has `android.json` beside `latest.json`, naming its signed APK (`Persona-<version>-android.apk`, `docs/operations/release.md`). It holds the fields of the hub's `latest.json` for the app (package, version code and name, minimum SDK, the signing certificate's SHA-256, size, SHA-256, source commit, notes) plus `version` (the release) and `name` (the APK's file name). For the official GitHub `releases/latest/download` source, a latest release without `android.json` makes the hub read the Release list and use the newest earlier stable release with both `android.json` and its named APK; drafts, prereleases and incomplete entries are skipped. It checks at most 10,000 releases and reports an error if more remain unchecked. A custom source without `android.json` changes nothing.
- **When**: at every update check (a minute after start, then hourly), before the hub's own update so an install's restart cannot cut the download short, from the same source and through `update.proxy`. A Docker hub checks too, but has a source only when `update.url` gives one (`docs/operations/docker.md` ("Upgrading")).
- **What it writes**: it downloads the APK, checks its size and SHA-256, and writes `<sha256>.apk`, its gzip copy and then `latest.json` under `.publish.lock`: the layout and lock of `scripts/release/android_apk_atomic.py`, so an SSH publisher and the sync never write at the same time, and the endpoints and the app need no change. It keeps the APK it replaced (a phone may still be downloading it) and removes older ones.
- **What it never does**: go back to a lower or equal version code (a publisher building every commit on main stays ahead of the releases); put an APK of another package, or signed with another key, over the published one (the phones could not install it over the app they have, so a hub publishing its own builds keeps them); overwrite a `latest.json` it cannot read. A refusal or failure is logged once, not at every check, and the phones keep the app they have.
- **On the phone**: the app finds the version at launch or in the foreground. Settings shows the last check, its hub and the result: update available, up to date, no APK, check failed, or an incompatible package or signing key. The system still asks before installing an update.
- **Security**: as for the hub's own binaries ("Security"), the publisher signs no manifest; the SHA-256 guards against a broken download. The phone itself refuses an APK signed with another key than the app it has.

Code: `server/internal/update/android.go`, called from `server/internal/hub/selfupdate_check.go`.

## 8. Updating (release build; automatic and manual use the same flow)

```
Download → Verify → Prepare → Wait for idle → Replace → Restart → Confirm → (on failure) Roll back
```

1. **Download**:
   - Download into `~/.persona/update/<build>/`: the archive for the hub's own platform, plus node binaries for **every platform** in the release (four platforms, for updating nodes along with the hub);
     not just the platforms of machines currently connected or known to the hub at the moment of the update. Machines added after the update, or that had never connected when it happened,
     need these for **Redeploy** too, and the manifest is only read during this self-update (#3186). The node binary for the hub's own platform is the hub binary itself,
     so reuse the copy already downloaded and verified rather than downloading it twice;
   - If a node binary for any platform fails to download or verify, the self-update fails (just like programs below). Previously it downloaded only the platforms of the machines known at the time;
     now a missing platform is one that should have been in the release, and installing cannot recover it later. The release script uploads every `.gz` first, then switches the site serving `latest.json`
     (`scripts/release/publish.sh`), so "listed in the manifest but not uploaded yet" cannot happen;
   - Restart interrupted downloads, at most 3 attempts; rename the temporary file only after the download finishes.
   - Every file comes from where the release checked stays, its tag on GitHub, so a release published since the check cannot serve its files in its place; another source is read once more when a download does not match, to tell a newer release from a broken download ("Update source", #9263).
   - The downloads live only as long as the update: `<build>/` is removed when the update fails (download, check, swap or restart), once it is confirmed and its node binaries are in place, and when it goes back. A hub start also removes every other `<build>/` (one that went back through the watchdog or the entry process, which run the old build, or one an older build left), keeping only the one it is confirming.
2. **Verify**:
   - Continue only if both sha256 values match the manifest; otherwise delete the download and report an error, leaving the current binary untouched;
   - After decompression, run `<new> version` once; it must report the manifest's version and the `release` channel.
3. **Prepare**:
   - Put node binaries in `~/.persona/bin/nodes/`: publicly installed hubs can now **Redeploy** too;
   - Write `~/.persona/update/pending.json`: from/to versions, previous version's path, start time, machines online before restart, startup count.
   - This is the only exception to "all server state goes into Postgres": rollback must work even when the new version cannot connect to the database or cannot start at all, so the marker sits beside the binary and is deleted when the update ends.
     Update history and bad versions still go into Postgres.
4. **Wait for idle** (check every 30 seconds; also do not restart while redeploying a machine): automatic and manual are the same: **restart only when the assistant and every Agent are idle**, with no time-of-day restriction (user, 2026-11-02).
   - Idle = no session working or waiting for the user's answer (both assistant chats and Agents in herdr count). If they never become idle, never install; keep the download ready.
   - A chat that waits only on the assistant's `ask_user` cards asked without waiting (`wait: false`, or a question that let go of its turn) is idle for this: no turn waits on them and they outlive a restart, so a card that is up for two hours does not hold the update (`hub/selfupdate_install.go` `updateBusy`, #7392; `docs/assistant/assistant.md`, "Asking without holding the turn"). A running turn, a blocked terminal, any other card of that chat, or a card the hub was not shown still holds it.
   - For a manual update, the App shows "what is still busy" (which chats, on which machine), and offers **Update Now** in a second confirmation:
     the confirmation states, "This interrupts anything waiting on the hub (the assistant's current turn, uploads in progress, terminal connections). Agents in herdr aren't affected and reconnect on their own."
     After confirmation, skip waiting for idle; all other steps (verification, rollback) stay the same.
5. **Replace**: backups and temporary files still use the compatible names `personad.prev` / `personad.new`; atomically replace the actual server binary in the same directory. The target path follows the installation layout and runtime migration implementation; the product becoming Persona does not mean the updater has already renamed these files.
6. **Restart**:
   - **With an entry process** (`personad hub run` on Linux and macOS, since 2026-11-06), use blue-green switching ([hub-restart.md](hub-restart.md), "Blue-green switching"):
     - The hub asks the entry process to switch to the new binary and gives it the expected build number;
     - The entry process first starts the new version as standby; only after its version matches and self-check passes does it hold connections, release the old version, activate the new one and switch routing;
     - If the new version cannot start, the entry process returns `switch_failed`; the old version restores the files, deletes pending and records this attempt as failed (not as a bad version, since the new version never ran);
     - No watchdog is started: the entry process is the watchdog.
   - **Without an entry process** (Windows, `PERSONA_HUB_EDGE=0`): start the watchdog first (below), then `syscall.Exec` the new binary with the same arguments and environment
     (the program the hub runs as: `persona-hub` for a hub run as `persona-hub`, see "Programs swapped together with personad").
     Carry the socket passed by systemd along (`LISTEN_FDS`, `execListenEnv` in `listen.go`; this step used to be missing, so the new process failed because the port was occupied);
     restart under the same process id, without relying on the service manager.
   - The local node is a child process of the hub and exits with it; the new hub starts it with the new binary. It runs as the `personad` beside the manager's program (or as that program when no `personad` of its build is there), and as `persona-node` only when one of the new build is beside it, which the update does not put there ([../server/server.md](../server/server.md) ("Programs")).
7. **Confirm**: on startup the new hub sees `pending.json`; success requires all three conditions below (delete pending, write update history, broadcast "Updated to …"):
   - HTTP is up;
   - The database is connected;
   - Every machine that was online before restart reconnects within 2 minutes (offline machines do not count).
8. **Rollback**, two layers:
   - **The new process itself**: if conditions are not met within 2 minutes, or startup count exceeds 3 (repeated crashes under a service manager), replace with `.prev` and `exec` it.
     If the startup count cannot be written to `pending.json` (disk full, read-only, wrong directory owner), roll back immediately too: without a stored count, repeated crashes can never reach 3 (#2613).
     If the directory remains unwritable (read-only, wrong owner), the rollback record cannot be written and `pending.json` cannot be deleted either: this rollback does not enter history, mark a bad version or send a push; after 3 minutes the watchdog restarts the already-rolled-back hub once more, and every later startup logs a deletion failure.
   - **With an entry process**, a new process that needs to roll back asks it to switch to the old binary: if already serving, perform a `rollback` switch; if not yet serving (startup count exceeded),
     exit with code 75 so the entry process immediately restarts the restored old version at the same path. In the two cases below, the entry process handles rollback: restore the files from pending, write the rollback marker, start the old version:
     - The new version fails to activate;
     - The new version does not report "confirmed" within 3 minutes.
   - **Watchdog** (without an entry process; code from the old binary, `personad update-watch`, started separately before exec and detached from the process group):
     if it does not see the new build number and "confirmed" from the local `/api/v1/health` within 3 minutes, it rolls back itself:
     - Under systemd (with `INVOCATION_ID`) or launchd: restore `.prev`, kill the main process, and let the service manager start the old binary;
     - Without a service manager: after restoring it, start the old hub in the background with the original arguments, as the program it ran as (`run` in `pending.json`, else `target`).
     - It signals the hub only while the hub is still its parent process (SIGTERM, then SIGKILL after 10 seconds if it is still there): once the hub has exited, its pid may belong to another process.
     - Before restoring anything it claims the marker by renaming `pending.json` to `pending.json.watch`. A confirm that removed the marker first (while the last health check was still out, or timed out) ends the watchdog with nothing undone; a confirm that finds it already claimed records nothing and leaves the update to the watchdog. If the restore fails, the marker goes back. A marker it cannot rename (an update directory that is read-only or has the wrong owner, where a confirm cannot remove it either) does not stop it: it goes back without the claim. A watchdog that dies while it holds the claim (power loss, or a stop of the hub unit: under systemd it shares the hub's cgroup) leaves `pending.json.watch`; the hub's next start renames it back to `pending.json` and handles the update as usual.
   - After rollback, record this build number in the **bad-version** list: automatic updates no longer try it and wait for a newer version. The user can still install it manually; the App says, "This version failed to start last time and was rolled back."
9. **Update nodes along with it (only with `auto_update_nodes: true`)**:
   - After the hub confirms success, run `deploy.go` for each online machine with a different version (push binary, restart the persistent node, wait for it to reconnect), one at a time;
   - Keep the failure reason for machines that fail; it does not affect the hub. If an old node does not support the update protocol, ask for one local upgrade first;
   - SSH / tailcat keep their original deploy path; Persona Link active / passive connections and nodes forwarded by member hubs update through existing RPC, with local rollback and reconnect confirmation. Offline Link machines store their target in Postgres and resume on their next connection; old targets stop when the hub version changes. See [node-update.md](node-update.md).
10. **History**: one record per update (automatic or manual): from/to versions, start and end times, result (updated / rolled back with reason / failed with reason), automatic or manual.
    Stored in Postgres (`hub_update_history` in settings, most recent 50 records kept).

## 9. Three ways to run

| | Release (our hub, installed with install.sh) | Docker | dev |
| --- | --- | --- | --- |
| Check | Version manifest | Image tags in the registry (see "Docker") | No checks |
| Update | Automatic (on by default) / one tap in the App | Suggest `docker compose pull && docker compose up -d` (edit `.env` first if a version is pinned) | None |
| Restart | `exec` the new binary itself | User recreates the container | — |
| Rollback | Automatic (`.prev`, bad versions are not retried) | Restore the previous tag | — |
| node | Automatically redeployed with the hub | An image built with `UPDATE_URL` carries its own platform's build only, and the manager fetches the others from the release of the image's version, or the newest one it drives; one built without carries all four platforms' personad (`docs/operations/node-update.md` ("Node packages"), `docs/operations/docker.md` ("Node builds")) | The hub's `bin/nodes` (`deploy.sh`) |
| Urgent change on `main` | Direct deploy (stamped `release` and build time; an older public release will not downgrade it, a newer one takes over automatically) | — | — |

### Docker (step 4)

- **Which image to check**: compose passes `.env`'s `PERSONA_IMAGE` into the container, and that is what the hub checks; if absent (old compose files), use the build-time
  `main.image` (`release.yml` passes `ghcr.io/<owner>/<repo>`, not hard-coded).
- **How to check**: registry `GET /v2/<repo>/tags/list`; first obtain a pull token as directed by its `WWW-Authenticate` response (public images allow anonymous access, both ghcr.io
  and Docker Hub do this), and follow `Link` for pagination. Only recognize `X.Y.Z`, and take the highest; it counts as a "new image" only if newer than the hub's own version. Its own version must also be `X.Y.Z`
  (or a prerelease such as `-rc1`): a manually built `edge` image uses `git describe`, is newer than the tag, and cannot be compared, so only the latest stable version is shown.
- **Private images**: when the repo is private, its ghcr image is private by default too, so anonymous access cannot get a token. Set `PERSONA_REGISTRY_TOKEN` in `.env` (a token that can read this image),
  or make the image public; when unset, Settings › hub explains why it cannot check and what to set. It does not read credentials saved by `docker login` on the NAS (they are on the host, not in the container).
- **App**: at the top of Agents, "New hub image: X", with only × (remembered per version; the next version is shown again). Settings › hub gives the commands; when a version tag is pinned, first show the line to edit `.env`.
- Not done: pulling images or recreating containers for the user (requires mounting the Docker socket, effectively handing the NAS to the hub).

## 10. In the App

- **Settings › hub** (a new row in the **Server** group, with version and status on the right; `HubSettingsPage`):
  - Version, channel (**Release** / **Docker** / **Development**), last check, **Check for Updates** (pull down also checks);
  - Update: when available, show the version and notes, and **Update Hub**; mark versions that were rolled back (automatic updates will not install them again);
    while updating, show progress (downloading / waiting for idle / restarting / **Confirming the new version**); while waiting for idle, list busy chats and machines, with **Update Now…** below,
    followed by a second confirmation explaining what will be interrupted (the assistant's current turn, uploads in progress, terminal connections; Agents in herdr are unaffected);
    Development explains why it will not update itself; Docker shows the command to run on the NAS, with copy;
  - **Update Hub Automatically** toggle (release builds only, on by default);
  - personad on each computer: version, **Mismatch** if not from the same deployment as the hub; open the machine page for **Redeploy**;
  - **Update History**: last 10 attempts, result (updated / rolled back / **Update failed** / **Not installed automatically**), automatic attempts marked, from/to versions, time, rollback and held-update reasons.
  - Old hub (no `/api/v1/hub/update`): explain that install.sh must be run once on the hub machine to update it.
- Android Settings has the same items (**Hub Version**, `HubUpdatePage`, see `../android/android.md`, "Hub version"); Android does not yet have the row at the top of Agents.
- **On Apple, a row at the top of Agents** (`HubNoticeRow`, same position and appearance as **Persona is signed in on your Mac**, hidden while searching): only one at a time, most important first:
  1. App too old (its build number is below the hub's `min_app_build`): "This app is too old for this hub. Update it in TestFlight", cannot dismiss;
  2. Hub too old (`protocol` below what the App requires; old hubs that do not report it count as 0): "The hub is out of date; some features won't work", dismissed with × and not shown again;
  3. Updating: e.g. "Hub updating: waiting for 2 busy chats to finish";
  4. Rolled back: reason, × to dismiss (remembered for this version);
  5. First-time notice: "The hub updates itself when idle", with **Turn Off** (disables automatic updates) and **OK**; tapping either makes the hub record `auto_notice_seen`, and it does not appear again;
  6. New version with automatic updates off: "Hub update available: X", with **Update** and × (not shown again for this version). Not shown with automatic updates on; it will install itself;
     Docker hubs show "New hub image: X", with only ×. After dismissing one, the next appears as usual; the App remembers the most recently dismissed notices.
  Tapping elsewhere in the row opens the hub page (sheet). No popup or blocked interaction; button labels stay single-line, and each button handles only its own action (`.borderless`, `HubNoticeUITests`).
  On the web, the notice sits directly below Settings in the sidebar's Pages group. Folding Pages hides both rows.
  Reopening it reads the current status. In the phone drawer the notice spans the width beneath the grid row containing Settings.
  Tapping the text opens Settings › Hub. Each adjacent button performs its own action and keeps the sidebar open.
  The web follows the same priority and remembers the last eight dismissals in this browser. It skips the app and hub
  compatibility notices because the hub serves the web client.
- **Notifications**: after an automatic update installs, push "Hub updated to X"; after rollback, push "Hub update failed" with the reason (hub's `updater.notify`). Manual updates are visible in the App and do not send an extra push.
- **During restart**: disconnection is expected; the usual "Connecting…" title becomes "Hub updating…", and its explanation says the hub is restarting and will automatically return to the previous version if the new one cannot start; after reconnect, show the new version.
- **Settings › Assistant › Skills** (`SkillsPage`): skills on the computer running the assistant (`GET /api/v1/skills`), read-only, marked as user-provided or built-in, explaining where to put them and when they take effect.
- All use the design system; screenshots reviewed for both styles (Liquid Glass, BeautifulUI) × light/dark.

## 11. Compatibility

- `protocol`: the App ↔ hub major version (`proto.Protocol`), incremented only for incompatible changes; fine-grained capabilities still use `features`.
  The hub reports `protocol` and `min_app_build` in `/api/v1/health` and `hello`; the App records its minimum required `protocol`.
- Old Apps ignore these fields and keep working; they just do not show update-related features.
- Old hubs lack these APIs: the App shows the version, and **Check for Updates** says, "This hub is too old to support updates in the App."
  Publicly installed old hubs need one manual install.sh run to get an update-capable version; our hub gets one final ssh deploy from the main session.

## 12. API (draft)

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/v1/hub/update` | `{version, build, channel, can_update, supports_preview, latest?{version, build, released, notes, bad}, state, progress, busy[]{machine, session, title}, error, auto, auto_update_nodes, auto_notice, last_check, update_url?, update_url_from?, update_proxy?, source_behind?}` (the last four: "Update source"; `source_behind` only when true) |
| POST | `/api/v1/hub/update/check` | Check immediately. An optional `{"preview": true}` refreshes status without automatic installation. |
| POST | `/api/v1/hub/update` | Start update. On Web development, `{"target": "latest"}` resolves the latest commit under reservation. `{"now": true}` is **Update Now** after the second confirmation, skipping the idle wait |
| PUT | `/api/v1/hub/update/settings` | `{auto}`; `{auto_notice_seen: true}`; `{auto_update_nodes: true\|false}` |
| GET | `/api/v1/hub/update/history` | Update history, newest first |
| GET | `/api/v1/skills` | Skills on the machine running the assistant: `[{name, description, source: user\|builtin}]` |
| WS event | `hub_update` | Push to open Apps when state or progress changes |

`state`: `idle` / `checking` / `resolving` / `available` / `downloading` / `waiting_idle` / `restarting` / `confirming` / `updated` / `rolled_back` / `failed`.

### Runtime node update policy

`PUT /api/v1/hub/update/settings` accepts `{"auto_update_nodes": true}` or
`false`. The choice is persisted in Postgres under `hub_update`, independently
of `auto` (Hub self-updates). An omitted or null field preserves the choice;
until a choice has been saved, `hub.json`'s `auto_update_nodes` is the fallback
(default false). GET and the successful PUT response always include the
effective boolean, including false. A failed read returns 503; a failed save
does not change the running choice or notify managers.

After a successful choice, existing `devices.config` broadcasts and reconnect
handshakes carry the same effective policy to managers without changing machine
ownership. An explicit true PUT also requests `devices.update_nodes` after the
configuration is sent, even when the value is unchanged. This requests a package
check; it is not an installation receipt. Matching Hub versions, usable packages,
busy checks, ownership, installation health and rollback still govern deployment.
An offline manager receives the policy on its next handshake; node reconnects
retain the existing automatic update path.

If `bin/nodes` is a symlink, the Hub updater never swaps binaries, removes version
markers or sets aside old binaries through it. The release publisher owns the
immutable package directories and activates the pointer only after confirming
Hub health. Pointer activation alone has no file-watch notification: after
activation, repeat the authenticated true PUT to request a fresh package check
on connected managers. Repeating false never requests deployment. Publication
and node confirmation must be reported separately.

This policy governs updates to computer services. An independent manager's own executable
has a separate idle check, startup confirmation and rollback. A node deployment confirms
only the computer it updated. See [Automatic updates for an independent manager](../assistant/managers.md#automatic-updates-for-an-independent-manager).

## 13. Tests

- Go:
  - Bad download: delete without installing when sha256 does not match; restart an interrupted download;
  - Retry after interrupted download; delete without replacing on a sha256 mismatch;
  - New version cannot start: compile a deliberately unstartable binary in the test; verify both the new process's self-check and the watchdog can roll back, record the bad version and do not retry it automatically;
  - Wait for idle: do not restart while the assistant or any Agent is busy (automatic and manual alike); `now` skips the wait;
  - Checks during installation or confirmation preserve the update state, progress and error on success and failure, including when an install starts while the manifest request is in flight;
  - Skills: symlink creation and cleanup, automatic migration of old installs, user precedence on a name collision, gate command only present with its skill; built-in skill directory contains only redistributable skills;
  - dev / docker channels never install, however new the manifest is (API 409);
  - **No downgrade** (`TestNoDowngrade`): a directly deployed binary (build number = build time) does not install a manifest with a smaller or equal build number; only larger ones install;
  - **A release published between check and update** (`server/internal/update/pin_test.go`, `server/internal/hub/selfupdate_pin_test.go`, `server/internal/devices/packages_pin_test.go`): with a source that publishes a newer release (its `latest.json` and files) after the check, the update installs the release checked from its GitHub tag, hub binary, programs and node builds alike; another source is read once more and the update stops naming the newer release, which it offers and installs next; the node build a manager looked up is fetched the same way; a broken download still fails with the mismatch, and no file is fetched twice;
  - **Update source** (`selfupdate_source_test.go`): `hub.json`'s `update.url` replaces the build's for checks, install scripts and node builds, and the view says which; a URL that does not read (plain http elsewhere, a password, a query, `latest.json` itself) leaves no source and never shows the password; `update.proxy` carries the update's requests with its credentials and nothing else, and one that does not read fails checks without a direct request;
  - **Release manifest** (`update/verify_test.go`, `cmd/personad/release_test.go`): a manifest over binaries of another build (this platform's runs and says so, another's lacks the build number), another version, a changed or missing download is refused, and `release manifest` writes nothing then;
- publish.sh: no publishing if the build number does not increase or the live commit is not an ancestor.
- tag_release.py: a receipt whose `latest.json` is not its binaries' build, whose installer or any `personad` carries another update URL is never uploaded; `latest.json` goes up after the files it lists, and a stable Release becomes Latest only after remote digests and the immutable tag manifest verify, never for an older stable backfill (`scripts/operations/test_tag_release.py`).
- App: FakeHub tests for the hub row in Settings, first-time notice, banner (appears, dismissed with × and not shown again, progress, explanation while disconnected, both incompatibility notices), update history and Skills list.
  Screenshots: two styles × light/dark.

## 14. Implementation steps

1. **One binary and a version manifest**: move skills out (user space, symlinks, migration, `ol` and prompts on demand, read-only list API), remove `-tags public`,
   channel and build number (publish.sh, deploy.sh, Docker), `internal/update` (manifest, pre-release checks), publish.sh publishes the manifest and performs the two monotonicity checks;
   `/api/v1/health` carries the new fields. Before deploying, put the Lark gateway skill in our hub's `~/.persona/skills/` (or rely on automatic migration).
2. **Hub update and rollback**: check, download and verify, wait for idle (**Update Now**), replace, watchdog, bad versions, history, no downgrade; update nodes along with it (add `via` and tailcat).
3. **App**: hub row in Settings, first-time notice, banner, progress, what it is waiting for, update history, incompatibility notices, Skills list.
4. **Docker checks and notices**.

## Public-release decisions (2026-11-02)

The private publication and Hub-only session policies are described in "Automatic private Hub releases" above.

1. Move skills out of the binary into the user's own space (`~/.persona/skills/`), never overwritten by updates; merge full and public builds into one, and every hub updates automatically,
   ours included (user: "Skills definitely need to be highly customizable"). Keep ssh deployment only for emergencies.
2. Automatic updates are **on by default**; clearly tell the user the first time and offer a way to turn them off; record every update; do not automatically retry rolled-back versions.
3. No time-of-day restriction: install when the assistant and Agents are all idle. Do not restart while a chat is running or waiting for an answer.
4. Use release time (UTC `YYYYMMDDHHMM`) as the build number.
5. **Manual public publication**: the publisher runs publish.sh (the Worker is under their account); enforce manifest, build-number and commit-ancestry checks.
   Our hub also updates automatically to their releases; direct deploys remain for urgent changes on `main` and will not be downgraded.
6. No staged rollout; all hubs update at once after release, each handles its own rollback.
9. **No signing** (user: "Skip signing for the Linux service, it's too complicated"): the manifest has only sha256, fetched over HTTPS; install automatically when it matches. This only protects against corrupt transfers, not an impersonated or compromised publish site (see "Security").
7. Manual updates also wait for the assistant and Agents to be idle; the App shows what it is waiting for and offers **Update Now** in a second confirmation.
8. The skill space is `~/.persona/skills/` on the machine running the assistant.
