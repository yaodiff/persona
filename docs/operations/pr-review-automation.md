# Automated PR review and merging

This maintainer-side service watches explicitly configured GitHub repositories, performs an independent semantic code review, runs local repository checks, and can squash-merge a passing exact head. It is not part of the Persona hub, does not add an Assistant or Agents UI, and does not change hosted CI or repository rulesets.

The implementation is `scripts/operations/pr_watch.py`. It replaces a local focused-test-only watcher whose comments explicitly disclaimed semantic review. A pre-existing GitHub comment or review is **not** a verification credential: only this service's versioned record for `(repository, PR number, head SHA)` can satisfy its own gates.

## Review and merge contract

Every new, open, non-draft PR head goes through independent gates:

1. Resolve the PR's current base and head. Fetch both into an isolated worktree and refuse a moved ref.
2. Invoke `claude -p` with a fixed review prompt, a structured JSON schema, `--safe-mode`, and only `Read`. Safe mode disables project instructions, hooks, plugins and MCP; the reviewer reads the supplied diff and can inspect surrounding source without Bash, editing, delegation or GitHub credentials in its instructions. PR text and repository content are untrusted input.
3. Require every finding to identify a changed head line. An invalid response, denied tool, timeout or unavailable reviewer is a failed review, never approval.
4. Run the repository's configured verification on that head in a credential-free child environment. Save the actual commands and results. Only allowlisted locale, toolchain, proxy and cache paths are supplied; the maintainer's GitHub, webhook, model and cloud credentials never enter PR-controlled builds.
5. On findings, submit one PR review with inline comments and concrete corrections. If GitHub rejects an anchor, retain the findings in one grouped review comment. On verification failure, attempt at most two repair rounds: compare isolated named failures with clean main, merge current main into a stale same-repository branch, or use an isolated repair worker. A fork receives a continuation PR instead of a write to the contributor branch. Re-run semantic review and verification on every new head. A failure independently reproduced on clean main is recorded as base-owned and does not block the remaining gates; it is never described as a passing test. Stop with a precise comment after two unsuccessful rounds. Do not merge any unresolved PR-owned failure.
6. On success, post a factual summary naming the checks that actually ran. Reread the PR, require a linked issue, mergeability, the same base/head and no outstanding changes request, then squash-merge with `--match-head-commit`.
7. Read back `MERGED`. A failed `gh` command after a remote merge is not blindly retried.

New commits invalidate all previous results. Duplicate comments are suppressed by head and finding fingerprint. Merge failures use bounded exponential backoff; a conflict waits for a new head unless an authorized same-repository base merge resolves it. A contributor branch is only changed by the explicit maintainer-authorized repair flow: a normal main-merge or repair commit, never a force-push. A fork is never written; a continuation PR is used instead. Other people's review threads are never resolved automatically.

The default is **review-only**: no configured repositories, and `"merge": false` for every added repository. Enabling merge is a separate local maintainer decision. `scripts/hooks/merge-pr.sh` and the generic `pr_watch_merge.py` hold the same shared-Git-directory lock, so manual and automated merges cannot race. The wrapper retains the machine-private scan of the exact changed range and squash message. Do not apply one checkout's private-word configuration to another repository.

## Configure

Copy `scripts/operations/pr-watch.example.json` outside the checkout, for example to `~/.config/persona/pr-watch.json`, and restrict it to the maintainer (`chmod 600`). Use absolute paths. The service uses the local `gh` login; tokens stay in `gh`'s credential store, not in this file or the service unit.

Each repository entry has:

| Field | Meaning |
| --- | --- |
| `name` | `owner/repository`; also checked against the checkout's `origin` through `gh repo view` |
| `checkout` | Trusted local checkout used only for refs and temporary worktrees; its working tree is not edited |
| `base` | Base branch to watch, normally `main` |
| `merge` | Opt-in live squash merging; `false` means review and verification only |
| `verify.profile` | `persona`, `extensions`, or an explicit `commands` array |
| `verify.timeout_seconds` | Bound for each command, 60–21600 seconds |
| `privacy` | Optional public-content scanner for a generic repository, not a path to another checkout's private configuration |
| `notify_command` | Optional trusted argv adapter, called with repository, number, head and outcome |

For this repository, `profile: persona` uses the trusted watcher checkout's `scripts/ci/paths.py` selector (never importing the PR's Python) and runs every selected portable CI lane, including `core` and `desktop`. A test-file-only change instead runs its changed Web, Go or Python tests, locked frontend dependencies and typecheck/build where applicable, plus privacy, documentation and changed-file baseline guards. It does not run unrelated frontend tests, every Python suite or unrelated full lanes. An Apple test-only change uses semantic review and portable guards without claiming a Swift build or simulator acceptance. Shared test setup, lockfiles, CI/build configuration, production source and uncertain cases retain the full existing lanes. A maintainer-approved Linux exception applies to titles beginning `test:`/`test(...)`/`fix:`/`fix(...)` and test-file-only changes: these never require the visual-evidence adapter, including production UI fixes. Feature and other UI changes still require visual evidence. A Mac verifier is not a merge prerequisite for Apple production changes: semantic review and every selected portable check still run, with no claim of an Apple build, simulator or device acceptance. A file below `Views/` is not automatically visual when no test/fix exemption applies: its actual changed lines decide whether it changes presentation or only backend behavior. Web/other-client feature UI changes, unclassified files and uncertain cases require the configured visual-evidence adapter. The exception records only semantic review and the non-Apple checks that actually ran; it never claims an Apple build. A trusted adapter may use `{worktree}`, `{base}`, `{head}` and `{number}` placeholders in its argv. Large diffs are partitioned by complete files and, when necessary, whole hunks. Every chunk must return a valid review; the union of their findings is the result. A failed or timed-out chunk is not an approval.

For the extensions repository, `profile: extensions` calls its `scripts/check-layout.py modules` and selects affected independent Go modules, including SDK dependents. Shared scripts/schema/SDK changes run the complete CI script from that repository. No separate extension ID list is maintained here.

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

The reviewer uses the local Claude CLI by default. `reviewer.command` may be an executable or trusted argv prefix, `reviewer.model` an optional single model argument, and `reviewer.timeout_seconds` a 30–7200 second bound per chunk, defaulting to 2700 seconds. A fresh `claude -p` process runs per chunk; it has no persisted session or working Agent transcript. The prompt follows the existing independent-review design in `server/internal/assistant/supervise_autoreview.go`: judge the change, not the working Agent's claims.

## Run and inspect

```sh
python3 -I scripts/operations/pr_watch.py --config <private config> --once --dry-run
python3 -I scripts/operations/pr_watch.py --config <private config> --repository <owner>/<repository> --pr <number> --once --dry-run
python3 -I scripts/operations/pr_watch.py --config <private config>
```

`--dry-run` still runs the model and repository checks but never comments, sends notifications or merges. It shares the persistent state, so a later live run can use its completed evidence only for the exact same head. Use a separate state file for an isolated experiment. A second process cannot acquire the state lock while the service is running.

The service polls every 60 seconds, uses bounded worker concurrency, and finishes GitHub comments/merges serially. Superseded incomplete/failed verification is dequeued before fresh reviews so a large discovery backlog cannot indefinitely delay the policy migration; all other work retains discovery order. A nonblocking instance lock prevents accidental duplicate services. State, including which gates actually completed, is stored atomically with mode `0600`; temporary worktrees live below that state directory. Inspect logs with `journalctl --user-unit persona-pr-watch.service`. Repository/PR selection is part of the configuration fingerprint; a focused run needs an isolated state file, or an explicit state migration when reusing the live evidence is intended.

An optional loopback webhook can supplement polling. Set `PR_WATCH_WEBHOOK_SECRET` in the service's private environment; it must contain at least 32 characters. The receiver verifies HMAC SHA-256, size, event type, repository, PR number and full head SHA. It listens on `127.0.0.1:18777` by default and needs an authenticated HTTPS reverse proxy if used remotely. Polling remains sufficient without it.

To install, adapt `scripts/operations/persona-pr-watch.service`, replacing the source/config placeholders and choosing the local Python path. Keep `UMask=0077`, `NoNewPrivileges=true` and the bounded stop behavior. Do not put secrets in the unit. Then:

```sh
systemctl --user daemon-reload
systemctl --user enable --now persona-pr-watch.service
systemctl --user status persona-pr-watch.service
```

Stop or restart only this unit when changing its implementation. Preserve its private state across upgrades. An older state file without version `1` semantic evidence is not treated as approval; affected PRs are reviewed again. Verification-policy version `2` retries an incomplete or failed verification recorded by an older policy on the same base/head, while retaining its valid semantic review and repair budget. Completed checks, base-owned failure evidence, published findings, merged results, continuations and exhausted repairs are not reset by this migration.

## Verification and limitations

```sh
python3 -B -m unittest -v scripts.operations.test_pr_watch
python3 -B -m unittest -v scripts.test.test_merge_pr_sh
scripts/ci/scripts.sh
python3 scripts/privacy_check.py
```

The tests use temporary repositories and fake GitHub/model commands. They cover two-repository identity, new-head review, malformed model output, findings, inline-comment validation, failed tests, conflicts, exact-head merge, restart/deduplication, webhook authentication and instance locking. No test needs or modifies a real PR.

Local verification is required because opening a PR does not automatically start hosted CI in either repository. An empty check list or `mergeStateStatus=CLEAN` is not evidence of tests. Unsupported platforms, unavailable tools and unverified manual acceptance are reported as not verified rather than passed. A successful semantic review is a defect-finding aid, not a proof of correctness; release, deployment and publication remain separately authorized operations.
