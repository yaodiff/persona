# Automated PR review and merging

This maintainer-side service watches explicitly configured GitHub repositories, performs an independent semantic code review, runs the configured lightweight or repository checks, and can squash-merge a passing exact head. It is not part of the Persona hub, does not add an Assistant or Agents UI, and does not change hosted CI or repository rulesets.

**Local-runner evidence is required before an automatic merge of this repository.** The watcher may use `semantic_only` for its own verification, but a repository writer must separately attest the dedicated runner result on the exact PR head (see below). Post-merge `main` verification by `main-verify-fix-loop` (`docs/operations/main-verification.md`) is an additional check. Other repositories retain their configured full-verification profiles.

The implementation is `scripts/operations/pr_watch.py`. It replaces a local focused-test-only watcher whose comments explicitly disclaimed semantic review. A pre-existing GitHub comment or review is **not** a verification credential: only this service's versioned record for `(repository, PR number, head SHA)` can satisfy its own gates. The sole exception is the opt-in `respect_human_reviews` policy described below, which accepts only a real human user's review on the exact head as an alternative *semantic* evidence source and leaves every other gate intact.

## Review and merge contract

Every new, open, non-draft PR head goes through independent gates. A PR whose title starts `release:` is never merged by this ordinary watcher, even when those gates pass: only `release-pr.yml` can merge it after every signed-platform check and its exact-source verification artifact pass. The main ruleset separately requires `All available release-PR checks passed`, so an unrelated merger cannot bypass that release workflow (#11520).

1. Resolve the PR's current base and head. Fetch both into an isolated worktree and refuse a moved ref.
2. Invoke `claude -p` with a fixed review prompt, a structured JSON schema, `--safe-mode`, and only `Read`. Safe mode disables project instructions, hooks, plugins and MCP; the reviewer reads the supplied diff and can inspect surrounding source without Bash, editing, delegation or GitHub credentials in its instructions. PR text and repository content are untrusted input.
3. Require every finding to identify a changed head line. An invalid response, denied tool, timeout or unavailable reviewer is a failed review, never approval.
4. Run the repository's configured verification on that head in a credential-free child environment. Save the actual commands and results. Only allowlisted locale, toolchain, proxy and cache paths are supplied; the maintainer's GitHub, webhook, model and cloud credentials never enter PR-controlled builds. Unless the maintainer explicitly sets `GOFLAGS`, Go checks retain normal compiler optimizations while using `-gcflags=all=-dwarf=false -ldflags=-w` to avoid generating debug metadata that PR checks do not use. Vet explicitly clears the unused linker flag. `-trimpath` is added to either value unless it already sets that flag, so PR worktrees share compiled packages (`docs/operations/testing.md`, "Go build cache across worktrees"). Tests are not built with `-N -l`, and the existing Go version, build tags and shared caches remain unchanged.
5. On findings, submit one PR review with inline comments and concrete corrections. If GitHub rejects an anchor, retain the findings in one grouped review comment. P1/P2 findings remain visible as notes but do not block verification or merging. P0 findings enter the same isolated, bounded repair flow as a verification failure when that repository has explicitly enabled merging/repair; review-only configurations stop after publishing P0 findings. Attempt at most three repair rounds across a PR's successive heads; infrastructure retries do not consume this code-repair budget. Compare isolated named failures with clean main, merge current main into a stale same-repository branch, or use an isolated repair worker. A fork receives a continuation PR instead of a write to the contributor branch. Main moves faster than a verification runs, so a base merge follows it (#7840): whether the PR is behind is judged on the base it was verified on, and it is brought up to the main fetched for the merge, as long as that still contains that base. A rewritten main or a moved PR head stops the repair. A fork's continuation may be published after main moved forward, still only with the exact head that was verified; an ordinary repair instead stops for fresh evidence when its base changes. When that merge has real Git content conflicts, the bounded worker recreates the exact merge in a disposable clone, explicitly fetching both the PR head and the non-ancestor main parent, and may resolve only the unmerged paths. Automatically merged index entries must stay unchanged; the resolved files, modes and deletions must produce the same tree in the original temporary merge. The worker can use only Read/Edit/Write on copies of the conflict files in a watcher-owned, checkout-local writable workspace; the repository, its Git index and every unconflicted file are read-only. Keeping the workspace inside the checkout lets the CLI read the copies without opening unrelated host paths. This bounded workspace supports atomic file replacement without enumerating a large checkout into bwrap arguments. The trusted run wrapper passes the remaining options through a private file descriptor, rewound before launch. The outer process accepts only regular files at the listed conflict paths, checks deletions and modes, applies the copies to the original merge and performs staging; extra files and symlinks in the workspace are rejected. It cannot commit, push, use shell commands, or access the maintainer HOME, parent directory or host device directory. A resolution must be a regular file, executable or symlink (deletion is supported by the outer stager); gitlinks and unsafe mount cases are rejected. Any permission denial or invalid worker envelope fails the round even if the file contents look correct. A real conflict consumes one round even when the worker fails; an ordinary merge/setup failure does not. The staged resolution and commit message pass the privacy scan before committing, and the new head is verified, scanned and semantically reviewed before merging. A failed conflict repair aborts only the explicitly owned temporary merge. Re-run semantic review and verification on every new head. A failure independently reproduced on clean main is recorded as base-owned and does not block the remaining gates; it is never described as a passing test. Stop with the last precise failure in state and one PR comment after three unsuccessful rounds. Do not merge any unresolved P0 finding or PR-owned failure.
6. On success, post a factual summary naming the checks that actually ran. Reread the PR, require a linked issue (unless the repository sets `require_issue_link` to `false`), mergeability, the same base/head and no outstanding changes request, then squash-merge with `--match-head-commit`.
7. Read back `MERGED`. A failed `gh` command after a remote merge is not blindly retried.

New commits invalidate all previous results. Duplicate comments are suppressed per head: publication history is kept by head, outside the evidence record that a moved base replaces, so re-reviewing an unchanged head after `main` moves publishes a head's findings once and afterwards only a P0 at a new path and line, posting that P0 alone, and posts the success review once (#11738, #12302). Merge failures use bounded exponential backoff; an unchanged conflict may use a bounded repair round instead of waiting forever for a new head. A contributor branch is only changed by the explicit maintainer-authorized repair flow: a normal main-merge or repair commit, never a force-push. A fork is never written; a continuation PR is used instead. Each watcher's state is private, so before reviewing or repairing a fork PR the watcher looks on GitHub for an open or merged `fix: continue #N verification repair` PR that already contains the fork's current head, whoever published it, and leaves the fork to that continuation instead of opening another (#10881). A fork head pushed later gets a fresh review. Other people's review threads are never resolved automatically.

Before publishing a fork continuation, the watcher checks GitHub again. It matches continuation references in the title or body and reuses an open continuation that contains the current fork head, or one merged after the fork head commit's committer timestamp. The watcher saves either lookup's result against the fork head SHA, so a restart or an advance of the base branch does not restart repair for the same fork head.


### Dedicated local-runner merge evidence

Automatic merges of this repository additionally require the latest GitHub commit status with context
`persona/local-gate` on the exact PR head. The status must be `success`, created by a user who currently has
repository write, maintain or admin permission. A human review, hosted check, cached semantic pass or the
privacy merge wrapper cannot substitute for this evidence. Missing, pending, failed, unreadable or malformed
statuses block merging with a stored diagnostic and a deduplicated PR comment. A newer failure supersedes an
older success. This guard is mandatory even for `semantic_only` verification and merge retries.

The maintainer or trusted local-runner adapter publishes this attestation **only after** collecting a completed
run from the dedicated runner, checking its full `COMMIT` against the PR head, and recording the selected shards
and sanitized result in the PR. Use description `EXIT_CODE=0` for a successful run. If existing failures remain,
run the same shards on the fixed base with the same environment, compare concrete failure identities, record both
runs and the comparison in the PR, and use `BASE=<full current base SHA> NEW_FAILURES=0`. A changed base invalidates
that exception. A timeout, interrupted SSH connection, missing shard or unclassified failure is not a pass.
Never publish raw runner logs, machine names, private paths or addresses to GitHub.

For example, after validating and recording a successful runner result, publish its status with:

```sh
gh api --method POST "repos/<owner>/<repo>/statuses/<full head SHA>" \
  -f context=persona/local-gate -f state=success -f description='EXIT_CODE=0'
```

The status is a repository writer's attestation of local-runner evidence, not an independently executed hosted
check. The watcher reads it directly from GitHub immediately before invoking the exact-head merge wrapper; it
never manufactures this status from its own lightweight checks. Other repositories retain their configured gates.

## Closing PRs after a week without progress

A non-draft PR with no substantive progress for **more than seven days** is marked `stale`, told in English that it is being closed for long inactivity, and then closed without merging. New commits, reviews, contributor comments, publication of a newly found failure, or an automatic repair publication reset the interval. Polling, retrying an already recorded failure, and writing local retry counters do not. A fork PR with a published continuation is left to that continuation instead of being closed. The `close_stale` repository switch is an explicit local opt-in; dry runs and review-only examples do not close PRs.

The watcher reads the PR's actual comments, reviews and commits before deciding. It adds the label first, saves that the marker was published, posts the explanation, then rereads activity before closing so newly arriving work wins. The watcher's own stale notice is not new work and cannot prevent a later retry. Failure to mark, comment or close leaves the PR open and records the failure; it never counts as a merge or a successful close. A closed PR is only reopened by a person resuming work, and any reopened or changed head goes through fresh semantic review and verification. This is distinct from the 24-hour issue-claim expiry in `docs/operations/collaboration.md`; that rule governs ownership, not PR closure.

The default is **review-only**: no configured repositories, and `"merge": false` for every added repository. Enabling merge is a separate local maintainer decision. `scripts/hooks/merge-pr.sh` and the generic `pr_watch_merge.py` hold the same shared-Git-directory lock, so manual and automated merges cannot race. The wrapper retains the machine-private scan of the exact changed range and squash message. Do not apply one checkout's private-word configuration to another repository.

## Respect human reviews on the exact head (opt-in)

A repository may set `"respect_human_reviews": true`; the default is `false`, so an existing deployment keeps running its own independent semantic review on every head unchanged. When enabled, before preparing a checkout or invoking the reviewer the watcher reads the PR's GitHub reviews for the current exact head and classifies only reviews from real user accounts (`user.type == "User"`):

- A human **APPROVED** review on the exact head replaces the watcher's independent semantic review. No reviewer CLI call is made and the watcher publishes no review or comment for that head. The semantic evidence is stored with `semantic_source: "human"` plus the GitHub review id and commit id. Every other gate still runs on that exact head: the configured verification profile (including the privacy scan), linked-issue, mergeability, exact-head/base re-read and the outstanding-change-request merge block. A human approval never merges a PR by itself.
- A human **CHANGES_REQUESTED** review on the exact head also skips the reviewer, but the head is parked (`human_changes_requested`) and cannot merge until a new commit or a new human state on the same head resolves it.
- When the exact head has only human **COMMENTED** reviews it is parked (`human_commented`): the reviewer is skipped, but comments are neither approval nor a merge pass.
- Reviews bound to an older commit are never reused. After every push the head changes, classification filters on `commit_id == head`; a head without a qualifying human review receives the normal independent review.
- Bot and GitHub App reviews, missing reviewer identities and reviews authored by the watcher's own authenticated login never qualify as human. Dismissed and pending reviews are neutral; when one reviewer submits several reviews on one head, their latest effective state wins, so an approval followed by a change request on the same head blocks.

Parked heads are re-read on every poll, so a reviewer approving or changing their request on the same commit is observed without waiting for a new push. Human-sourced semantic evidence is also reclassified before verification or merging resumes: a later comment or change request parks the head even after an earlier approval, and when no effective human review remains the human pass is removed and the normal independent review is required. Exact-head verification evidence is retained; a restart before verification is recorded resumes that gate instead of skipping it. Toggling the switch changes the configuration fingerprint and therefore goes through the offline `--migrate-config` flow, but the field is excluded from the repository-policy fingerprint: it only adds an evidence source and never weakens a gate, so existing exact-head semantic and verification evidence, repair rounds and publication history are preserved.

## Configure

Copy `scripts/operations/pr-watch.example.json` outside the checkout, for example to `~/.config/persona/pr-watch.json`, and restrict it to the maintainer (`chmod 600`). Use absolute paths. For the Persona and Persona Extensions repositories, start with `scripts/operations/pr-watch.persona.example.json`: it has both repository profiles, ten workers and a one-hour reviewer timeout, and keeps merging disabled. Replace the `/path/to/...` placeholders with your own absolute paths; opt into merging separately in that private copy if wanted. No account, machine-specific path, credential, token or live state belongs in either example. The service uses the local `gh` login; tokens stay in `gh`'s credential store, not in this file or the service unit.

Each repository entry has:

| Field | Meaning |
| --- | --- |
| `name` | `owner/repository`; also checked against the checkout's `origin` through `gh repo view` |
| `checkout` | Trusted local checkout used only for refs and temporary worktrees; its working tree is not edited |
| `base` | Base branch to watch, normally `main` |
| `merge` | Opt-in live squash merging; `false` means review and verification only |
| `close_stale` | Opt-in stale-PR closure; `false` keeps a repository out of this cleanup. The live maintainer configuration enables it separately |
| `respect_human_reviews` | Opt-in, default `false`: real-user reviews on the exact head can replace the watcher's own semantic review (approval skips it but keeps every other gate; change requests and comment-only heads park without merging). See "Respect human reviews on the exact head" |
| `require_issue_link` | Default `true`: a PR must link an issue in its own repository before merging, and a fork repair opens its continuation PR with the link. Set it to `false` only for a repository whose contribution rules have no issue workflow; a merge-time gate only, so turning it off does not discard exact-head review or verification evidence |
| `verify.profile` | `persona`, `extensions`, or an explicit `commands` array |
| `verify.timeout_seconds` | Bound for each command, 60–21600 seconds |
| `privacy` | Optional public-content scanner for a generic repository, not a path to another checkout's private configuration |
| `notify_command` | Optional trusted argv adapter, called with repository, number, head and outcome |

For this repository, `profile: persona` uses the trusted watcher checkout's `scripts/ci/paths.py` selector (never importing the PR's Python) and runs every selected portable CI lane, including `core` and `desktop`. A test-file-only change instead runs its changed Web, Go or Python tests, locked frontend dependencies and typecheck/build where applicable, plus privacy, documentation and changed-file baseline guards. It does not run unrelated frontend tests, every Python suite or unrelated full lanes. An Apple test-only change uses semantic review and portable guards without claiming a Swift build or simulator acceptance. Shared test setup, lockfiles, CI/build configuration, production source and uncertain cases retain the full existing lanes. A maintainer-approved Linux exception applies to titles beginning `test:`/`test(...)`/`fix:`/`fix(...)` and test-file-only changes: these never require the visual-evidence adapter, including production UI fixes. Feature and other UI changes require visual evidence only when `ui_evidence_command` is configured. Without that adapter, every other semantic and portable gate still runs; the success review explicitly says that screenshot acceptance was not performed. Configuring the adapter restores its required check. A Mac verifier is not a merge prerequisite for Apple production changes: semantic review and every selected portable check still run, with no claim of an Apple build, simulator or device acceptance. A file below `Views/` is not automatically visual when no test/fix exemption applies: its actual changed lines decide whether it changes presentation or only backend behavior. Web/other-client feature UI changes, unclassified files and uncertain cases use the configured visual-evidence adapter when present; otherwise they retain every other gate and explicitly record the absent screenshot acceptance. The exception records only semantic review and the non-Apple checks that actually ran; it never claims an Apple build. A trusted adapter may use `{worktree}`, `{base}`, `{head}` and `{number}` placeholders in its argv. Large diffs are partitioned by complete files and, when necessary, whole hunks. A hunk larger than the 60000-byte chunk bound is partitioned between complete added or removed lines, retaining the file header and generating each part's own old/new start lines and counts; a newline marker stays attached to its content line. An individual oversized diff line or binary file still fails closed. Smaller chunks bound each independent review's source-reading scope. Every chunk must return a valid review; the union of their findings is the result. A failed or timed-out chunk is not an approval.

For the extensions repository, `profile: extensions` calls its `scripts/check-layout.py modules` and selects affected independent Go modules, including SDK dependents. Shared scripts/schema/SDK changes run the complete CI script from that repository. No separate extension ID list is maintained here.

The `commands` profile is intended for other trusted repositories and synthetic tests. The opt-in `semantic_only` profile is for a high-volume maintainer queue that wants independent semantic review without repeating the complete CI suite on every PR. It retains exact-head privacy/documentation guards (each repository's own documentation checks from the exact base: the test_doc suites here, check-docs.py and test-docs.py under persona-extensions' scripts directory), P0 semantic repair, linked-issue, mergeability and exact-head merge gates. It does not run full build/vet, cross-platform, build-tag or full-test lanes, does not start CI failure repair, and does not claim build or test acceptance. Success comments explicitly disclose this mode. Other profiles retain their existing full-verification behavior.

The `commands` profile is intended for other trusted repositories and synthetic tests. Commands are argv arrays, never shell snippets. PR titles, descriptions or repository instructions cannot add or change commands. For example:

```json
{
  "name": "<owner>/<repository>",
  "checkout": "<absolute path>",
  "base": "main",
  "merge": false,
  "verify": {
    "profile": "commands",
    "commands": [{"name": "tests", "argv": ["python3", "-m", "unittest"]}]
  }
}
```

The reviewer uses the local Claude CLI by default. `reviewer.command` may be an executable or trusted argv prefix, `reviewer.model` an optional single model argument, and `reviewer.timeout_seconds` a 30–7200 second bound for the complete review, including all chunks, defaulting to 3600 seconds. `reviewer.repair_timeout_seconds` defaults to 3600 seconds. `reviewer.concurrency` selects 1–20 actual semantic-review workers, defaulting to sixteen; it is independent of lifecycle worker threads and is scheduling-only, so its default does not change the existing evidence fingerprint. A fresh `claude -p` process runs per chunk; it has no persisted session or working Agent transcript. Up to three reviewer attempts are made for an unavailable or timed-out review; the last failure remains retryable with bounded backoff. The prompt follows the existing independent-review design in `server/internal/assistant/supervise_autoreview.go`: judge the change, not the working Agent's claims.

The optional Herdr adapter preserves one prompt per review session. If its five-second lifecycle observation stalls, it waits on the same pane and current-session transcript for a bounded startup grace period rather than resubmitting or treating idle as a pass. Working or blocked state accepts the pending prompt; idle or done requires the invocation's complete, schema-valid marked result. Persistent idle state without that result or an exited scope fails the attempt. The grace period, total review timeout and bounded reviewer slots remain separate limits.

## Run and inspect

```sh
python3 -I scripts/operations/pr_watch.py --config <private config> --once --dry-run
python3 -I scripts/operations/pr_watch.py --config <private config> --repository <owner>/<repository> --pr <number> --once --dry-run
python3 -I scripts/operations/pr_watch.py --config <private config>
```

`--dry-run` still runs the model and repository checks but never comments, sends notifications or merges. It shares the persistent state, so a later live run can use its completed evidence only for the exact same head. Use a separate state file for an isolated experiment. A second process cannot acquire the state lock while the service is running.

The service polls every 60 seconds, uses 1–20 worker threads selected by the explicit `workers` setting, and finishes GitHub comments/merges serially. A separate ready-review executor runs at most `reviewer.concurrency` model calls (sixteen by default), refilling a slot as soon as one returns. PR reads, checkout preparation, publication and merging never consume reviewer slots and do not create additional agents; the external reviewer adapter's own bounded slot count must match this setting. An idle queue does not manufacture review work. A transient discovery failure, including a GitHub rate limit, retains active reviewers and retries at the next normal poll instead of tearing down the service. The public example keeps ten workers; an external reviewer adapter must have enough bounded slots for the selected worker count. PR discovery uses GraphQL and lists every open PR: `gh pr list` returns the newest first, so its bound (2000) must exceed the open PRs, and reaching it is logged (#14864). An unchanged head with a terminal record or an active backoff does not need another PR snapshot; a new head or an expired retry still gets a fresh snapshot. The snapshot resolves the live base branch tip in the same GraphQL query, and every exact-head gate rereads it rather than treating a cached result as approval. Stale-PR activity uses bounded GraphQL cursor pages for issue comments, reviews and review comments; incomplete pagination fails closed and never authorizes closure. Superseded incomplete/failed verification is dequeued before fresh reviews so a large discovery backlog cannot indefinitely delay the policy migration; all other work retains discovery order. A nonblocking instance lock prevents accidental duplicate services. State, including which gates actually completed, is stored atomically with mode `0600`; temporary worktrees live below that state directory. An interrupted `reviewing` or `repairing` record is recoverable on restart. Worktree preparation prunes stale metadata and removes only zero-byte loose objects; temporary pack files are left untouched. On a full filesystem it can reclaim registered watcher-owned worktree leases that have been inactive for at least one day; active, recent, unregistered and external checkouts are never cleanup candidates. Refused fetch updates can be resolved by rereading the exact remote SHA and checking that the local commit exists. These recovery steps never turn a changed or missing head into approval. Inspect logs with `journalctl --user-unit persona-pr-watch.service`. Repository/PR selection is part of the configuration fingerprint; a focused run needs an isolated state file, or an explicit state migration when reusing the live evidence is intended.

An optional loopback webhook can supplement polling. Set `PR_WATCH_WEBHOOK_SECRET` in the service's private environment; it must contain at least 32 characters. The receiver verifies HMAC SHA-256, size, event type, repository, PR number and full head SHA. It listens on `127.0.0.1:18777` by default and needs an authenticated HTTPS reverse proxy if used remotely. Polling remains sufficient without it.

To install, adapt `scripts/operations/persona-pr-watch.service`, replacing the source/config placeholders and choosing the local Python path. `scripts/operations/persona-pr-watch.example.service` is a portable alternative with `%h` for the maintainer's home, `/opt/persona` for the trusted checkout, and an optional private environment file for the reviewer CLI. Keep `UMask=0077`, `NoNewPrivileges=true` and the bounded stop behavior. Do not put secrets, personal paths or tokens in the unit or tracked examples. Then:

```sh
systemctl --user daemon-reload
systemctl --user enable --now persona-pr-watch.service
systemctl --user status persona-pr-watch.service
```

Stop or restart only this unit when changing its implementation. Preserve its private state across upgrades. An older state file without version `1` semantic evidence is not treated as approval; affected PRs are reviewed again. Verification-policy version `2` retries an incomplete or failed verification recorded by an older policy on the same base/head, while retaining its valid semantic review and repair budget. Completed checks, base-owned failure evidence, published findings, merged results, continuations and exhausted repairs are not reset by this migration. Review-policy version `3` makes only P0 semantic findings blocking and retains P1/P2 as published notes. Verification lane comparisons use the same concrete failure signature on clean main, including infrastructure failures without a test name. Verification-policy version `3` adds the explicit `semantic_only` profile for high-volume queues: old full-CI failures are not reused as approval or as a reason to keep repairing CI, and the new lightweight evidence must complete before merging. With the service stopped, `python3 -I scripts/operations/pr_watch.py --config <private config> --migrate-config` takes the existing instance lock and performs this explicit migration, preserving repair rounds and comment fingerprints, then exits without processing PRs. Normal startup rejects an unreviewed configuration change.

## Verification and limitations

```sh
python3 -B -m unittest -v scripts.operations.test_pr_watch
python3 -B -m unittest -v scripts.test.test_merge_pr_sh
scripts/ci/scripts.sh
python3 scripts/privacy_check.py
```

The tests use temporary repositories and fake GitHub/model commands. They cover two-repository identity, new-head review, malformed model output, findings, inline-comment validation, failed tests, conflicts, exact-head merge, restart/deduplication, webhook authentication and instance locking. No test needs or modifies a real PR.

Local verification is required because opening a PR does not automatically start hosted CI in either repository. An empty check list or `mergeStateStatus=CLEAN` is not evidence of tests. Unsupported platforms, unavailable tools and unverified manual acceptance are reported as not verified rather than passed. A successful semantic review is a defect-finding aid, not a proof of correctness; release, deployment and publication remain separately authorized operations.
