# Contributing to Persona

Thanks for helping. This page is the starting point for people. The complete rulebook, which people and Agents follow alike,
is [`CLAUDE.md`](CLAUDE.md); [`docs/SUPER_RULES.md`](docs/SUPER_RULES.md) takes precedence over everything else. When this page
and those disagree, they win.

## What Persona is

Persona is a self-hosted personal SuperAgent, and an Agent product for everyone, not only for software developers. A Go
server (`personad`) runs as the **hub** on an always-on machine and holds all state; the iPhone, Apple Watch, Vision Pro,
Android, HarmonyOS, desktop and web clients talk only to it, and each of your computers runs a **node** where coding Agents
run. **Assistant** is for everyday people; **Agents** is for people who work directly with Claude Code and other Agents.
Before any design decision, ask who it serves. See [the architecture](docs/server/architecture.md) and
[the design principles](docs/design/PRINCIPLES.md).

## Getting set up

- **Try it**: [Getting started](docs/operations/getting-started.md) installs a hub with one command and pairs a phone.
- **Build from source**: [config](docs/operations/config.md) (`~/.config/persona/config.json` for build and deploy values;
  values specific to your machines and accounts for Agents go in `CLAUDE.local.md`, which is not committed), then the area
  you work on:
  - the hub and nodes: [`docs/server/server.md`](docs/server/server.md)
  - the web app: [`docs/web/web.md`](docs/web/web.md) (needs Node.js and Go)
  - iPhone, Watch and Vision Pro: [`docs/apple/ios.md`](docs/apple/ios.md) (Xcode 26.0 or later)
  - Android: [`docs/android/android.md`](docs/android/android.md)
  - HarmonyOS: [`harmony/README.md`](harmony/README.md)
  - desktop: [`docs/desktop/desktop.md`](docs/desktop/desktop.md)
  - extensions and Agent runtimes: [`docs/extensions/extensions.md`](docs/extensions/extensions.md),
    [`docs/integrations/runtimes.md`](docs/integrations/runtimes.md)
- Where things live: the "Layout" table in [`CLAUDE.md`](CLAUDE.md#layout); every maintained doc is in its "Docs index".

## How a change gets in

Every change, code, docs or config, goes **issue → claim → branch → PR → merge after local verification passes**. Details
and copyable `gh` commands: [`docs/operations/collaboration.md`](docs/operations/collaboration.md).

1. **Look for duplicates**: open and closed issues and PRs, so nobody redoes finished work.
2. **Find or open an issue** that says what and why (`bug` or `enhancement`).
3. **Claim it** with a comment starting `Claim:`, then `Plan:` and `ETA:`. Work that touches UI, settings, defaults or how
   something is counted adds `Acceptance:`, written against [the design principles](docs/design/PRINCIPLES.md). If an Agent
   does the work, add `Agent: yes (<machine id> / <branch>)`. Someone else's claim updated in the last 24 hours means leave
   it alone; after 24 hours without an update you may post `Take over:` with the reason.
4. **Work on your own branch** named with the issue number (`fix/123-short-name`). Bring it up to date by merging
   `origin/main` (preferred over a rebase), and never force-push.
5. **Open one PR per issue**, in English, with `Fixes #N` / `Closes #N` (or `Refs #N`) in the body, and fill in
   [the PR template](.github/pull_request_template.md). A PR that links no issue is closed until it does.
6. **Merge after local verification passes.** Opening a PR runs no hosted CI, so the PR says what you ran and what you
   didn't. With write access, merge through `scripts/hooks/merge-pr.sh <number>`; otherwise a maintainer merges.

## Verifying your change

Run the checks for every area you touched ("Build and verify" in [`CLAUDE.md`](CLAUDE.md#build-and-verify)). The portable
CI scripts run the same locally and on hosted runners:

```sh
scripts/ci/server.sh    # Go hub and node
scripts/ci/web.sh       # web app
scripts/ci/apple.sh     # iPhone, Watch, Vision Pro
scripts/ci/android.sh   # Android
scripts/ci/scripts.sh   # Python scripts, docs and repository guards
```

Every change also runs the privacy scan and, for docs, the doc checks:

```sh
python3 scripts/privacy_check.py
python3 scripts/test/test_doc_paths.py && python3 scripts/test/test_doc_sections.py
```

`git config core.hooksPath scripts/hooks` runs the privacy scan on every commit and push. UI changes need screenshots in
light and dark, which you actually looked at ([`docs/operations/testing.md`](docs/operations/testing.md)), plus the
"UI change self-check" from [`docs/design/DESIGN_SYSTEM.md`](docs/design/DESIGN_SYSTEM.md). Say in the PR which checks you
couldn't run (a missing toolchain, no device) rather than leaving them out.

## Rules that catch most first PRs

- **This repository is public**, and so is its history: code, docs, fixtures, commit messages, issues and PRs never hold
  private information (domains, IPs, host names, real people's names, accounts, mail addresses, tokens, keys) or internal
  company names. Use placeholders such as `<hub address>` and `me@example.com`. Secrets never enter the repository.
- **English** for everything people read in the repository. `README.md` and `README.zh-CN.md` change together.
- **User-facing text goes through localization resources**, and every new string ships in `en`, `zh-Hans` and `ja`
  ([`docs/protocol/i18n.md`](docs/protocol/i18n.md)).
- **UI uses only the design system's tokens and components**, and an existing screen's look doesn't change except for a
  bug fix or a requested change; a new look is a new style or theme color ([`docs/design/DESIGN_SYSTEM.md`](docs/design/DESIGN_SYSTEM.md)).
- **Server logic goes into the managers**, not the hub ([`docs/server/architecture.md`](docs/server/architecture.md)).
- Pitfalls others already hit are in [`docs/lessons/`](docs/lessons/README.md); read the file for your area first.

## Working with an AI Agent

Agents are welcome and follow the same rules. [`CLAUDE.md`](CLAUDE.md) is written to be read by Agents as their
instructions; point yours at it, and at this page for the workflow. The person whose GitHub account posts the claim and the
PR is responsible for the Agent's work.
