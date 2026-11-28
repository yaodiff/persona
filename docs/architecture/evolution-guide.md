# Architecture evolution guide

Who this serves: anyone who changes Persona's code on any platform (server, web, Android, Apple, HarmonyOS), people and
Agents alike. It states what the architecture is for and the constraints every change must keep. It describes no
progress or plans; the details live in the docs it links, and when this guide and code or a linked doc disagree, the
code and the linked doc win and this guide gets fixed.

## Design goals

- **A small core that stands on its own.** Persona Core is the abilities and interfaces for the assistant, Agents,
  sessions, tasks and to-dos: the hub API, the data model, and the clients' headless code. Everything a person could
  say "I never use this" about is a module. The core must build, run and pass its tests with no module at all.
- **Features that can come and go.** A module can be left out of a build, switched off at run time, or tested on its
  own, without touching the core and without losing what the user already has.
- **One truth, computed once.** State, kinds and the numbers people see are decided in one place on the server and
  sent as typed values. Clients draw them; they don't work them out again, so every screen and device agrees.
- **Mistakes that cannot be written.** A rule is worth most where breaking it fails the build, not where someone has to
  remember it.
- **Steady improvement without big rewrites.** Old code that breaks a rule is held where it is and shrinks over time;
  new code follows the rule from its first line.
- **Old and new keep talking.** Hubs, managers, nodes and clients upgrade at different times, so every pair of versions
  in the field must keep working.

## Constraints

### Core and modules

- The core must not reference a module: no import, no call, no module name in core code. Only the composition roots
  list modules.
- A module must reach the core only through its declared contract (its manifest and the module API). A module adds
  nothing its manifest does not declare, and a host must refuse what it does not name.
- A module the core can do without must stay optional: the core-only build must keep building and running.
- A switched-off or left-out module is absent (no routes, tools, jobs, flags or entries), but it must not hide, change
  or delete data the user already has. Reading existing data is core; writing new data is the module's.
- Clients must show a module only as the hub announces it: off only when the hub says it publishes module flags and
  that module's flag is missing; with no word from the hub, on.
- The hub only communicates. Business logic goes into the managers.

Details: `docs/architecture/core-and-modules.md` ("The module contract", "The switch", "A module that is off keeps what
the user has", "Guards", "Clients") and `docs/server/architecture.md` ("The hub is only a communication center").

### A strongly typed core

- A closed set (a state, kind, role, reason or event) must be a closed type that code handles exhaustively, never a
  bare string or a bool. A value from a newer peer must decode to an explicit unknown case, never to a real value.
- An ID must have its own type, so one kind of ID cannot be passed where another is expected.
- A value that is derived (a count, an order, a phase, whether something needs the user) must be computed by the core
  and sent with the data. Clients must not derive it themselves from lists or raw fields.
- Wire types come from one schema for every language; clients must not hand-write their own copies.
- Server domain code follows the domain model: value objects, no exported fields, construction only through
  validating constructors, data out only through visitors and adapters.

Details: `docs/server/architecture.md` ("Domain model", "Closed sets and lifecycles"), `docs/protocol/counts.md`,
`docs/plans/plan-typed-core.md`.

### Put each constraint in the strongest layer

A constraint goes into the strongest layer that can hold it, in this order:

1. **Types**: a wrong use does not compile.
2. **Lint and architecture checks**: new code cannot add a breach.
3. **CI and merge gates**: a breach cannot merge.
4. **Docs**: only for what nothing above can check.

A rule written only in a doc, when a type or a check could hold it, is unfinished.

### Ratchets

- A breach that exists before a rule is recorded in that rule's baseline, and the baseline only goes down.
- New code must not add a breach, and must not add a new way around a check (an exemption list, an ignore marker, a
  raised baseline).
- Lowering a baseline is always allowed.

Details: `docs/operations/testing.md` ("Count ratchets"), `docs/server/architecture.md` ("The rules and their checks").

### Changing the core

- A pull request that changes a core path (tests-only changes excepted) must carry the `core-change` label and a
  `## Why this must change the core` section saying why the change cannot live in a module or an existing extension
  point.
- The core's code owners are asked to review it; the list of core paths is the one source for that and for the merge
  check.
- Changing the list of core paths, the checks or the guards is itself a core change.

Details: `CONTRIBUTING.md` ("Changing Persona Core"), `docs/architecture/core-and-modules.md` ("Guards").

### The same on every client

- Every client reads the same contract: the same manifest fields, the same wire types, the same flags and the same
  meaning for each count and state.
- Behaviour shared across clients is decided once (in the core, the shared Rust core or a shared spec with fixtures),
  not reimplemented per platform with its own rules.
- A client must not invent a fallback of its own for missing data; where a fallback is needed for older hubs, it is
  written once and shared.

Details: `docs/architecture/server-first-logic.md` (what the server owns, what clients own, and the bounded exceptions).

### Rolling upgrades

- Every change must keep working between an older and a newer hub, manager, node and client, in both directions.
- A new behaviour is used only when the other side announces it; a missing announcement means an older peer. Never
  guess from a build number, a date or a version string.
- Wire contracts change additively: new fields are optional, and an existing field keeps its name and meaning while
  older peers still read it.
- Old field names, values and identities stay readable; compatibility literals live in each platform's compatibility
  files.
- Compatibility must never loosen authentication, permissions, signatures or deduplication.

Details: `docs/protocol/rolling-upgrade-compatibility.md`, `docs/protocol/protocol.md` ("Health check and feature
flags"), `docs/architecture/server-first-logic.md` ("Rule 3 — Additive evolution, compatibility, and rollback").
