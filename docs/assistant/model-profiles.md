# Model profiles

A model profile is an editable, named configuration. It holds default bindings for the six model categories (including Translation), selected model fields of managed launchers, and named execution tiers: the [Agent presets](#agent-presets-tiers). Launcher-backed purpose bindings and Agent presets may optionally set `permission_mode`. A profile does not hold credentials, endpoints, or the main chat's runtime.

Model profiles are optional. A new hub starts with an empty catalog and uses Model Configuration directly. The hub creates a profile only when you save one. Apply a saved profile when you want to use it. Hubs with saved profiles keep their catalog, active profile, presets, and switch history when they upgrade.

## Switch in conversation

Ask Persona to switch to a profile you have saved. Persona uses:

- `model_profiles_list`: current profile, configurations, drift, rollback previews (latest and bounded history), and recent history.
- `model_profile_get` with `id`: inspect one profile.
- `model_profile_switch` with `id`: apply it atomically.
- `model_profile_rollback`: undo the most recent switch (stepping back through earlier switches across quick round trips such as `performance` → `economy` → `performance`; rolling back the last remaining point redoes on the next rollback).
- `model_profile_capture` with `id`: show what capturing the current six purpose bindings and their managed-launcher model fields would change in that existing profile, then save only after explicit phone approval. Every tier stays byte-for-byte unchanged. The card's catalog revision and exact target before-image are checked again at apply; a conflict writes nothing. The saved profile is read back and returned with the new catalog revision. Current configuration is not changed.
- `model_profile_deactivate` with `{}` turns off the active profile and its presets after owner approval. Apply the profile again to use those presets.
- `model_profile_clear` with `{}` deletes all saved profiles and their rollback points after owner approval. Clearing is permanent. Your current models continue to run.

Deactivation and clearing show a phone confirmation with the affected profiles. When exact owner approval is enabled, either tool can use an `owner_action_prepare` item. The server rechecks the catalog revision and the card details before writing. Declining the card leaves the saved settings as they are.

Related `model_profiles.bindings` / `model_profiles.tiers` writes and a following `model_profile_switch` can share one `owner_action_prepare` approval. Preparation builds a private sequential catalog plan, so every card and later switch sees the preceding items' after-images. Each execution checks the exact display and expected catalog revision against real storage: revisions advanced by earlier items in this batch match, while any external write causes a conflict. The first failure stops later items and reports completed and unexecuted positions. Rollback uses the same exact dry-run, rollback-point display and revision check. Ordinary direct profile switch/rollback behavior is unchanged when no exact batch item matches.

The live `persona-context` snapshot identifies the current profile and its tiers (with what each runs on and is for) on every turn, without waiting for the ordinary snapshot cache. Model calls use the changed configuration on their next invocation. Existing Agent sessions and running turns are not interrupted.

## Choose what goes in a profile

You can save a profile from the current configuration with `capture: true` or choose its settings yourself. Saving leaves Model Configuration in use as it is. Profiles and their presets take effect after an explicit apply.

A new capture copies any launcher-backed Main chat and New Agent choices into the `core` and `dev` presets, with the legacy aliases `strong` and `standard`. The saved choices stay fixed until you edit them. Older saved profiles still resolve those names as described under [Candidates and aliases](#candidates-and-aliases). If you want separate performance and economy profiles, you can use the following as a starting point. Model availability, billing, and CLI compatibility depend on your provider.

| Purpose or tier | Performance | Economy |
| --- | --- | --- |
| Main chat and question delegation | Strong available model | Available supervisor model; no unavailable-model dependency |
| New Agents / dev | Strong available coding model | Standard coding model, using its native runtime where available |
| Knowledge processing and screening | Strong model where useful | Fast economical model |
| core | Design and implementation model | Available supervisor model |
| deploy | Model that follows a runbook reliably | Fast model for mechanical steps |
| research | Long-context model | Economical long-context model |

Every purpose binding has `provider`, `model`, optional `effort`, and (for launcher-backed purposes only) optional `permission_mode`. Empty provider with empty model inherits the existing default source; empty model or permission mode uses its inherited/launcher default. Legacy model-only category bindings (non-empty `model` with empty `provider`) are normalized to their resolved provider on read, capture, save, apply, and rollback when a configured provider exists, so they display and apply predictably without drifting when defaults change (empty `{}` bindings and effort-only bindings keep inheriting). Profiles cover `assistant`, `agent.new`, `kb`, `inbox.triage`, `agent.delegate`, and `translate`; independent extension overrides such as `lark.agent` are not changed.

A scheduled task whose saved new-namespace override outlives the task can be cleaned up without recreating that task. `persona_settings` accepts `{"action":"set","key":"model_config.task.<id>","value":null}` only when the task is absent and a non-empty override actually exists. The ordinary card (or an `owner_action_prepare` item) shows the old binding → inherit Main chat, checks its record revision again, and reads back the empty override. Any non-null value is refused: a nonexistent task can never receive a new binding. The retired legacy `model_config.task.<id>` records remain inert and are not resurrected.

## Agent presets (tiers)

A tier is Persona's equivalent of a Paseo Agent profile: a named launch preset (`launcher`, optional `model`, `effort`, start `speed`, and `permission_mode`) with a `description` saying when to use it (one line, at most 100 characters). Because tiers live in the profiles, a role keeps its name while what it runs on follows the performance / economy switch. Only the profile in use's tiers apply; with none in use there are none.

A tier can set an optional `host` for `task_plan` (including leaders and their workers) and `agent_start`. For example,
set `"host": "herdr"` on a tier that launches `agy` to open those tasks in Herdr tabs. The host applies to all
candidates in the tier. An explicit start host wins, and a different launcher that bypasses the tier also bypasses
its host. With neither a preset host nor an explicit host, Antigravity uses its companion when available.
Read-only Antigravity tasks require the companion.
Set or clear the host through `persona_settings` (`model_profiles.tiers`) or the model-profile API. An omitted
field keeps the saved host during an edit. Send `"host": ""` to clear it.

- **The assistant** sees them on the snapshot's 模型档位 line (`name = launcher · model · effort · speed · permission_mode（description）`; unset fields are omitted) and passes `tier` to `agent_start` or a `task_plan` task. Tiers are named and described by the kind of work they are for (`dev` for development, debugging and research, `core` for core design and implementation, `deploy` for following a runbook), not ranked by strength: the assistant picks the tier whose description matches the task, and the preset's model, effort and permission mode then run in place of the Agents binding of Model Settings for that task; without presets, or when none fits, it leaves the task's tier, model and effort empty. A rework raises the effort rather than moving the task to another kind of tier. `agent_start` resolves the tier first (an unknown tier fails before anything is asked or started) and says what runs, including its start speed and permission mode, the tier with Model Settings' Agents laid over it as the start lays it, in its answer and, where it asks first (a hand-off from the health space), on the confirmation card. A launcher given with the tier that is not the tier's own runs without the tier. A plan task's tier is resolved when it starts, with the profile in use then; the web and Android plan cards show the tier (with the task's own launcher, when it has one; an explicit model still wins).
- **An explicit tier is a caller choice, not a default** (#12032): when the target computer does not have the tier's launcher, the start is refused with `launcher_not_on_machine` before any terminal or process starts — the same refusal a launcher named by hand gets — instead of silently substituting another launcher and clearing the tier's model, effort, fingerprint and speed. This holds for `task_plan` (the task is rejected when the card is made), `agent_start` (nothing starts before the confirmation), New Agent (a 400), a plan whose launcher disappears between preview and `session.start` (the task fails at dispatch), and a tier a start-defaults policy selected for that start. The implicit Model Settings › Agents default still gives way to a launcher the computer has (see [launcher health](../server/launcher-health.md) "A launcher the computer doesn't have"), and a computer whose launcher list cannot be read is never refused on a guess. When the tier's launcher is present, its full preset reaches the start unchanged.
- **Another Agent for the same task keeps the candidate its previous Agent ran on** (#14701, reversing #12618 for tiers): `task_retry`, the card's Retry, a continuation (`task_plan` with `continues`) and a queued or restarted start read back resolve the launch again when they start. Field by field, what that call names wins (`task_plan`: `launcher`, `tier`, `model`, `effort`; `task_retry`: `launcher`, `model`). When the call names no launcher, model or other tier and the previous Agent of that task (or of the to-do a continuation takes over) ran on a tier, the start keeps that tier's candidate it ran on, as long as the tier still lists it, the computer has it and its health check has not failed; an effort the call names is laid over it. See "Continuations keep the candidate" below for when it re-picks. Otherwise a retry keeps what the task's own plan named; then the task's tier resolves against the profile in use now, and with neither a launcher nor a tier the Agents binding applies as it is now. A previous Agent that ran on no tier leaves nothing to keep. A start never writes the launcher, model, effort or speed it resolved from a tier, the Agents binding or a start-defaults policy back into the plan as the task's own (`storedLaunch` in `server/internal/assistant/launcher_fit.go`): what ran is in the Agent's session and, for a failed start, in the plan task's `error_args.launcher`. Only a launcher put in place of the resolved one is kept, as the card shows it: the one the computer has for a default it lacks (#2661) or the one an engine ran instead (#8140).
- **Agents that start other Agents** (#14672): the new-Agent form of `a2a_send` and `dispatch_start` take `tier`, resolved as `agent_start` resolves it: an unknown tier, or one whose launcher the target computer lacks, is refused (`err_tier_not_found`, `launcher_not_on_machine`) before anything is recorded or started, and a launcher named with it that is not the tier's own wins while the tier gives nothing. The tier's launcher, model, effort and faster speed reach the start; its permission mode deliberately does not, so an owner's presets never raise what an Agent-started Agent may do (an `a2a_send` launch stays in `plan` unless the caller requests a higher mode it is attested for; `dispatch_start` keeps its own rule). Agents have no snapshot, so they read the presets on request instead: `a2a_sessions` with `presets: true` lists them for every computer a launch can go to (the caller's own included), and `dispatch_read`'s overview for every computer a task can go to, each with what it runs and its description, filtered as New Agent's list is (only the presets whose launcher that computer has where its new Agents start, one whose launcher failed its latest check marked unavailable). The tool texts only describe `tier`: they are static and sent with every request. A retry with the same `message_id` matches only with the same tier ([A2A](../integrations/a2a.md)).
- **New assistant conversation**: `conversation_new` accepts an optional `tier` from the active profile. Its launcher must be available for assistant conversations on the assistant’s computer (the `?purpose=assistant` picker). An unknown tier or incompatible launcher is refused before the first message. The reply names the running configuration, and the conversation keeps the preset’s launcher, model and effort as its own `model_pick`. Agent speed and permission settings do not carry over.
- **Caller intent and generated launch state stay separate** (#12618): an explicit model survives when #2661 replaces an unavailable default launcher; only a model supplied by that old default gives way to the replacement's own default. A `task_retry` override that names a launcher but no model selects that launcher's native default even when its provider matches the Agents binding or it is already the plan's launcher. The hub persists that choice as the optional `launcher_default_model` field, and `task_spawn` honors it for the assignment even when an older node reports only a resolved model. The optional `start_permission` separately records the assignment's effective last-start mode, so an older node with no live parent permission does not need to infer Allow all from the launcher. Likewise, the optional `caller_permission` pointer records the actual caller permission before the card or a start settles one: empty means the caller named none, so a generated Allow-all mode is settled again when a later start changes runtime; a non-empty value, including an explicit `bypassPermissions`, remains the caller's. New `task_plan` input cannot set these hub-owned fields, and a real card permission edit updates `caller_permission`. Each retry refreshes optional `retry_from_agent` and `retry_from_allow_all` with the failed start's actual runtime kind; an admission-queue read-back or cross-machine reroute uses that durable evidence instead of reinterpreting a machine-local launcher name on the destination.
- **Compatibility boundaries are explicit**: a launcher substituted while the plan card is prepared is plan-owned and stays on later retries rather than reverting to the current default. A legacy row with no `caller_permission`, `start_permission`, `launcher_default_model`, `retry_from_agent` or `retry_from_allow_all` is opaque: its non-empty launcher/model/effort/permission tuple is retained, with no migration or guessed re-resolution. An older writer that drops the new optional fields restores that conservative behavior. Generated tier/default speed is not written back as the caller's speed; a genuinely explicit speed is retained.
- **New Agent** (web, iOS/Mac and Android): `GET /api/v1/machines/{mid}/launchers` adds `tiers` (the profile in use's tiers with a candidate that computer has, each shown on the first such candidate, `candidate` its position when it is not the first; a direct API provider is in no computer's list) and `tiers_profile {id, name}`. Picking one fills launcher, model, effort, and (when set) permission mode as picking them by hand would; they can still be changed, a change by hand unmarks the preset, and picking it again goes back to the defaults. On iOS/Mac a **Preset** row appears only when tiers are offered and opens a native picker with the active profile's name, each tier's description and launcher/model/effort. A preset's model that is the launcher's own default is still sent when Model Settings would otherwise run another model there. The start request is unchanged and contains no `tier`, so an older hub simply shows no preset row and keeps the existing defaults. The current forms do not show, edit or apply a preset's speed; speed is for `agent_start`, planned tasks and API consumers. A preset whose launcher failed its health check is disabled in the native picker and names that health problem (the web marks it and the launcher list explains Try anyway). When the launcher can't take the preset's model (it fixes its own `--model`, or takes no typed ID) or the model can't take its effort, the form says what is not used instead of quietly running something else; those explanations remain available when the model list is absent or fails. On iOS/Mac and Android, Start is disabled and guarded while a picked preset with a model or effort is waiting for that launcher's model list, with a loading note, so it cannot validate against or send fields from the previous launcher's list. Android draws the tiers in a native section before Agent type with the profile name, each tier's description and launcher/model/effort and its health reason. A blocked tier opens the existing blocked-launcher help; **Try anyway** applies the whole preset, with duplicate background health reasons hidden only while preset help is open. Compatibility text names any model or effort left out.
- **Native plan cards**: iOS/Mac and Android preserve a plan task's optional `tier` when decoding and encoding it. The model chip gives an explicit model first precedence; otherwise it shows `launcher · Preset <tier>` when a launcher is present, `Preset <tier>` without one, and otherwise retains the launcher/Default Model display. Android shares this rule across Glass, Brief and Board, and its plan editor leaves the tier data unchanged. Starting the plan still resolves the tier on the hub at dispatch time, using the profile active then.
- **Editing in a conversation**: `persona_settings` key `model_profiles.tiers`. `get` lists every profile's tiers, each with all its candidates and its aliases. `set` takes `{"tier", "launcher"?, "model"?, "effort"?, "speed"?, "permission_mode"?, "host"?, "description"?, "candidates"?, "aliases"?, "profiles"?, "remove"?}`; `tier` may be an alias of the tier to edit:
  - The flat fields edit the first candidate and keep the others; `candidates`, when given, replaces the whole ordered list (at most 6), and `aliases` the older names the tier answers to.
  - It changes the tier in **every** profile unless `profiles` names some, so a switch keeps it; the answer names profiles still without it.
  - A field left out keeps the tier's own; a different launcher drops the old model, effort, speed and permission mode (they were the old launcher's). A tier new to a profile needs its launcher.
  - Checked before the approval card: the tier name, that the launcher is plain `claude`, a provider added in Persona that runs as a launcher, or one a computer lists (the lists are asked again before refusing, so a launcher just added to an rc file is not refused), the model ID, the effort, the speed (`standard`, `fast` or `ultrafast`), the permission mode (when supported by that launcher), and the description.
  - The card shows each profile's tier before → after, every candidate in order. Approval saves at the revision the card was made from, and only what the card showed: profiles edited in the meantime (or templates not yet saved, made again from a configuration changed since) fail with a conflict and nothing is overwritten. Only the tiers are checked, and only the edited tier's model, so a deleted provider or another tier's old model does not block a tier edit.
- **Permission mode precedence & safety**: when a tier or launcher-backed purpose sets `permission_mode`, effective precedence is `explicit caller/session override > tier preset > purpose binding > CLI native default`. Read-only task constraints (`readonly: true`), Inbox AI twin allowlists, and guest tool sandboxes are never weakened by a tier or purpose `permission_mode`. Unsupported modes for backends without permission modes (such as `omp` or `traex`) are rejected when saving and omitted when inherited.
- **Dispatch record**: every start a tier gives its launcher records the candidate it took. A plan task carries the hub-owned `dispatch` field (`{tier, candidate, of, launcher, model, reason, args}`, the latest start's; a `task_plan` call cannot set it), and each Agent that starts writes one row to the manager table `model_routing` (time, to-do, plan task, session, profile, `active`, tier, candidate and how many, launcher, model, effort, reason, and every candidate as judged in `detail`). `reason` is `only` (one candidate), `first` (the first candidate, or the first because none was usable), `unavailable` (an earlier candidate cannot start on that computer, named in `args.passed_over`), `score` (a later candidate scored better), `quota` (an earlier candidate has no headroom, named in `args.passed_over`), `over_pace` (every usable candidate is over pace: the first usable one), `kept` (the previous Agent's candidate, `args.from` its position then) or `switched` (the previous Agent's candidate was re-picked, `args.rule` `quota`, `rework` or `failed_starts` with its numbers). With several candidates `args.candidates` lists each one's score, headroom and 14-day results when the choice is not the first, and the `model_routing` detail always does; `dispatch.failed` counts the candidate's failed starts in a row. Rows of the profile in use are `active`; trials of profiles not in use (#14702) are not.
- **No silent fallback**: an unknown tier, or one whose launcher a computer lacks, fails with the reason; it never starts on another model. On a computer whose start goes to Paseo, which runs only its own providers, a tier whose launcher Paseo does not have runs on Paseo's provider of the same kind of agent, said on the task, its card and the session (its effort lowered to one the model offers, also said), or fails when Paseo has none of that kind (#8140, `docs/integrations/paseo.md` "A launcher Paseo does not have"). This is the rule for resolving an Agent preset. It does not define or disable the headless assistant's bounded transient recovery (#3204), which uses the current launcher's ordered `/model` list, not this unordered set of named presets.

## Candidates and aliases

A tier may list several **ordered candidates** (#14700), each a launcher with its own optional model, effort, start speed and permission mode, at most 6. The order is the user's preference. The flat fields (`launcher`, `model`, `effort`, `speed`, `permission_mode`) are always the first candidate, so an older client that reads or writes only them keeps working; `candidates` is present only when there are two or more. **A tier with one candidate behaves exactly as a tier did before**: nothing more is read and the start runs on the same launcher, model, effort, speed and permission mode.

Every start on a tier picks its candidate the same way (`pickTierCandidate` in `server/internal/assistant/tier_pick.go`), so the plan card's preview, a task's start, `agent_start`, `conversation_new` and the start-defaults preview agree:

1. The tier is found by its name, else by one of its `aliases`.
2. One candidate: it is used as it is.
3. Several: a candidate whose launcher the target computer does not have, or whose latest health check failed, is passed over (the same judgement start-defaults presets use), and the first usable one in order is taken. With none usable, the first is taken, so the start's own refusal or health detour says why, as it always did.

Nothing outside the tier's list is ever chosen; choosing a later candidate is said on the plan card (`dispatch`). The New Agent form offers a tier on the first candidate that computer has, and a start-defaults preset counts as available when any of its candidates is. Among the usable candidates the hub chooses by score (below); with no data and room everywhere that is the first.

### Choosing a candidate (#14701)

Only a tier with several candidates is scored; one candidate starts as before. Each usable candidate gets a score, lower is better:

`Score = T × (1 + β(1 − FP)) + κ·T / √(n + 1) + μ / (1 + 10·H)`, with β = 1, κ = 0.5, μ = 30.

- **T** is the median wall-clock minutes from dispatch to acceptance (`task_estimate_rounds.actual_minutes`) and **FP** the first-pass rate (accepted with no `rework` review in `todo_supervision.reviews`), over the last 14 days of the same tier on the same launcher and model, joined to the candidate through the to-do's latest `model_routing` row of the profile in use. **n** is that sample count. Both are shrunk toward the tier's pooled values with five samples' weight, `(n·observed + 5·prior) / (n + 5)`; a tier with no samples at all uses 30 minutes and 0.5. Once a candidate has been tried, few samples cost more (the κ term); an untried candidate (`n = 0`) carries no κ term, so a candidate the owner listed is tried once before the samples decide (#17011: charging it the full `κ·T` while a tried one paid `κ·T/√(n+1)` locked it out as soon as another candidate had one sample).
- **H** is the candidate's quota headroom, in account-windows (0.17 = 17 percentage points of one account's window). Per account and window: `max(elapsed share, start share) × (1 − reserve) × 100 − used`, where the elapsed share is the web's pace (`web/src/lib/usage-pace.ts`), the start share is 0.2 for the 5-hour window and 1/7 for the weekly one, and the reserve 15 % and 10 %. What was not used earlier stays available later. An account counts its tightest window that applies (a per-model window only for the models of its group), floored at zero; H sums the accounts of the launcher's usage provider: an admission group's gateway `quota_pool` provider, counting only that gateway's accounts and never a computer's own subscription of the same provider, else the agent the launcher runs (`agy` reads `antigravity`), read from its health record, its managed entry or the machine's last-seen rc-file and companion launchers, a launcher named after its agent (`agy`, `codex`) running that agent. A window with nothing used and no reset time has not started its clock and counts its start share. The accounts are the merged usage accounts the manager already holds (`Devices.UsageAccounts`, the same 30-second cache as `GET /api/v1/usage/windows`; nothing extra is read). When no fresh window says anything about the provider (a gateway lists accounts whose numbers carry no time window), the account-pool view answers: the gateway's account list for a `quota_pool` launcher, the provider's capacity (`Devices.CapacitySnapshot`) otherwise; each schedulable account counts one start share (0.17), and none schedulable is known H = 0 until the nearest cooldown end or reset. A stale reading, or none, is **unknown**: it adds nothing to the score and excludes nothing.
- A candidate with known H ≤ 0 is passed over (`quota`); when every usable one is, the first usable one runs (`over_pace`). Scores within one point go to the earlier candidate.

### Continuations keep the candidate

The next Agent of a task keeps the candidate its previous Agent ran on (see "Another Agent for the same task" above), read from the plan task's `dispatch` or, for a continuation, the to-do's latest `model_routing` row. It re-picks among the other candidates, with reason `switched` and the rule in `args`, only on a clear problem:

- **quota**: the candidate has no headroom (known H ≤ 0) and does not recover within two hours;
- **rework**: the to-do has been sent back for rework twice (`supervision.round` ≥ 2);
- **failed starts**: three starts in a row on it failed (`dispatch.failed`).

A tier with one candidate keeps it whatever the rule says: there is nothing else to choose. A candidate the tier (or, after a switch, the profile in use) no longer lists, or one this computer cannot run now, is chosen again as for a first start.

### Trial of inactive profiles (#14702)

To try a profile before switching to it, copy the active profile, add ordered candidates to its tiers, and leave it inactive. On every dispatch that records an active `model_routing` row, the manager also evaluates each inactive profile (up to 20, in stable ID order) after the active row is written, on its own goroutine and its own 5-second budget, so the dispatch never waits for it: it looks up the same tier by the active tier's name or one of its aliases, matched against each profile's tier names and aliases (`lookupTier`), runs the same candidate pick against that profile without starting anything, and writes a `model_routing` row with `active = false` carrying the same to-do, plan task, session, tier, chosen candidate, reason, and per-candidate scores/headroom detail. A profile without that tier writes nothing. Trial errors are logged only and never fail, delay or alter the real start. The trial picks as for a first start: it does not apply the continuation rule that keeps the previous Agent's candidate.

`GET /api/v1/model-profiles` adds `trial: {days: 7, dispatches, would_switch}` to every profile not in use and omits `trial` on the active profile (and on every profile when the counts cannot be read, rather than showing 0):

- **`dispatches`**: distinct dispatch decisions in the last 7 days that have a trial (`active = false`) row for that profile.
- **`would_switch`**: the subset of those dispatch decisions where the trial row's `launcher` or `model` (either one) differs from the active (`active = true`) row of the same dispatch decision. A decision is matched by its session when it has one, otherwise by the same to-do, plan task and recording time.

### Candidate status (#14702)

`GET /api/v1/model-profiles/{id}/candidates` is a read-only endpoint returning each tier's candidates in 0-based order with their current pace-based quota headroom and 14-day measured efficiency:

```json
{
  "days": 14,
  "tiers": {
    "dev": [
      {
        "index": 0,
        "launcher": "coding",
        "model": "standard-coding",
        "headroom": { "known": true, "percent": 17.0, "stale": false },
        "stats": { "n": 5, "minutes_p50": 18.4, "first_pass": 0.6 }
      }
    ]
  }
}
```

When a candidate's quota cannot be read or all matching accounts are stale, `headroom.known` is `false` (and `headroom.stale` is `true` when accounts were present but out of date); unknown headroom is never reported as full. `recovers_at` is included only when `known` is `true` and headroom is at or below zero. `stats.minutes_p50` and `stats.first_pass` are present exactly when `stats.n > 0`, so a measured 0 is still sent. Headroom is read for the hub's own machine. An unknown profile ID returns 404.

**Aliases** are older names a tier still answers to. When a profile is read, the two tiers Persona seeded under strength names are renamed by the kind of work: `strong` becomes `core` and `standard` becomes `dev`, with the old name kept as an alias and nothing else changed; the next save stores it. A tier you made yourself keeps its name, as does a seeded one whose new name is already taken. So a stored plan task, a start-defaults preset or an older client naming `strong` or `standard` resolves to the same launcher and model as before. Within a profile an alias is a valid tier name that is neither another tier's name nor another tier's alias. Renaming a tier yourself does not add an alias; list the old name in `aliases` if anything still names it. History entries keep the names they were written with.

A PUT from an older client sends a tier without `candidates`: its flat fields replace the first candidate, and the other candidates and the aliases stay as they were. A body with `candidates` (or `aliases`) states them itself, and an empty list clears them; when it sends both, the flat fields follow the first candidate. A client that knows `candidates` sends a one-item `candidates` list when it cuts a tier down to one candidate, so the hub does not mistake it for an older client's flat shape and keep the removed ones. Twelve tiers of six candidates fit well within the 64 KB body limit.

An older client renames a tier by sending it under its new name in the flat shape, with no earlier name. The hub pairs such a tier (a new name, no `candidates`, no `aliases`) with the stored tier the body no longer sends when it repeats that tier's first candidate and description unchanged, or, when exactly one tier went and one came, when both have the same non-empty description; a paired tier keeps its other candidates and its aliases, as an edit under the same name does. Anything else is a new tier: deleting a tier and adding an unrelated one in the same save hands it nothing. An alias the body did not send, kept for a client that cannot see it, gives way to a tier name or an alias the body sends (and to one another tier kept first, in name order), so an older client can add a tier named `strong` while `core` answers to `strong`: the save drops `strong` from `core`, and `strong` then names the new tier in that profile only; stored references to `strong` follow it. An alias a client sends itself that is another tier's name or alias is still refused. In a conversation, `tier` resolves aliases, so to add a tier under a name another tier answers to, first clear it from that tier's `aliases`. Known quirk of the read-time rename: an older client renaming `core` to `strong` (or `dev` to `standard`) is accepted, but the tier reads back as `core` (`dev`) answering to the old name.

## Edit and add profiles

### Managed purpose and launcher edits in a conversation

`persona_settings` key `model_profiles.bindings` reads every profile's purpose and managed-launcher model fields with the catalog revision, and atomically patches one existing profile with:

```json
{
  "profile": "performance",
  "purposes": {
    "agent.new": {"provider": "launcher.coding", "model": "standard-coding", "effort": "medium"},
    "translate": null
  },
  "launchers": {
    "coding": {"small_model": null, "effort": "medium"}
  }
}
```

Each purpose object replaces that purpose's whole binding; omitted purpose fields are empty. `null` or `{}` stores an explicit empty binding, so the next switch restores the purpose's inherited default (this differs from omitting the purpose, which leaves its current override untouched on a switch). Launcher entries patch only the fields sent: an omitted field keeps the profile's saved value, while `null` or `""` clears that field to the launcher's own default on the next switch. The main launcher `model` cannot be cleared. Tiers are not accepted by this key and remain unchanged; use `model_profiles.tiers` for their existing candidates/aliases behavior.

The setter validates known purposes, model identifiers, permission modes, launcher names and fields, and rejects every credential/endpoint key in `forbiddenProfileFields` before creating a card. Provider availability and whether a model can actually run retain the existing profile boundary: they are checked when the profile is explicitly switched to, not by a save-only edit. The approval card lists each actual `purposes.<id>` or `launchers.<name>.<field>` before → after value and says that current configuration stays unchanged. Denial or timeout writes no model state. Approval saves at the catalog revision read while preparing the card and also compares the exact target before-image; any intervening profile edit is a conflict, never a silent overwrite. A successful apply reads the profile back and returns it with the new revision. The card retains the approved before-values for reversing a profile-only edit with another managed patch; a later explicit switch still creates the ordinary switch rollback point.

Create a saved profile before using the `model_profiles.bindings` or `model_profiles.tiers` settings. Reads and edits of either setting return an error while the catalog is empty.

The web app's active-profile save view separately shows when current settings still differ and offers Apply. Those `profiles[].changes` are live drift, not the save event's `changed` audit list.

The web app adds **Settings › Models › Model profiles**: start from an optional empty state (using direct Model Configuration as-is) and create profiles from the current configuration, inspect a profile (its Agent presets, purpose bindings, launcher model overrides, and live drift), update any profile from the current configuration (`capture: true`, preserving its Agent presets), edit it interactively using the same provider/launcher/model/effort/permission-mode pickers as Model Configuration, review drifted purposes/launcher fields before applying (with an option to capture current settings into the active profile first), apply it, turn off the active profile without changing live settings, delete any profile (including the active one), clear all profiles and rollback history, or roll back; with no profile in use it says presets are off. You can also ask Persona to manage profiles and Agent presets (above) or use the hub API (authenticated as usual) to automate edits. Native settings controls require an app release and are deferred in v1.

A managed launcher's folded model and effort pick sets the variant ID and clears its saved standalone effort on
switch. When the model is left unchanged, the effort can still be left unchanged or cleared separately. Additional
launcher model fields expose concrete variant IDs because they each have their own model choice.

| Method | Path | Operation |
| --- | --- | --- |
| GET | `/api/v1/model-profiles` | Catalog, revision, active profile, per-inactive-profile 7-day `trial` summary, rollback preview (`rollback`) and bounded history (`rollbacks`), audit history |
| GET | `/api/v1/model-profiles/{id}/candidates` | Read-only per-tier candidate list (0-based index) with current pace headroom and 14-day `n` / `minutes_p50` / `first_pass` stats (404 for unknown profile) |
| PUT | `/api/v1/model-profiles/{id}` | Create or replace a profile; include catalog `revision` (ignores unknown optional fields from newer clients; rejects credentials/endpoints and invalid known fields) |
| DELETE | `/api/v1/model-profiles/{id}?revision=N` | Delete a saved profile. If it is active, also turn off its active designation. |
| DELETE | `/api/v1/model-profiles?revision=N` | Clear the saved profiles and switch rollback history. |
| POST | `/api/v1/model-profiles/{id}/apply` | Apply the profile |
| POST | `/api/v1/model-profiles/deactivate` | Turn off the active profile and its presets. Repeating this with no active profile succeeds without a write. |
| POST | `/api/v1/model-profiles/rollback` | Restore the latest saved before-image, or a specific point with optional `{"id"}` |

An unconfigured catalog returns `profiles: []`, `active: ""`, and `revision: 0`. Use revision `0` to save the first profile. Deactivation, deletion, and clearing leave the model category bindings and managed launchers as they are. Deactivation and single-profile deletion keep switch rollback history. Clearing removes the rollback points, keeps the audit history, and advances the catalog revision. The cleared catalog stays empty until you save a profile. Both DELETE routes accept an optional revision and return HTTP 409 when it is stale. These writes require full access and return the updated catalog.

A PUT body looks like this (use your actual provider and launcher names):

```json
{
  "revision": 1,
  "name": "Economy",
  "description": "Available models for everyday work",
  "purposes": {
    "assistant": {"provider": "launcher.supervisor", "model": "available-supervisor"},
    "agent.new": {"provider": "launcher.coding", "model": "standard-coding"}
  },
  "launchers": {
    "supervisor": {"model": "available-supervisor", "small_model": "fast-model", "subagent_model": "standard-coding"}
  },
  "tiers": {
    "core": {"launcher": "supervisor", "model": "available-supervisor", "description": "Core design and implementation", "aliases": ["strong"]},
    "dev": {"launcher": "coding", "model": "standard-coding", "speed": "fast", "description": "Development, debugging and research",
      "candidates": [
        {"launcher": "coding", "model": "standard-coding", "speed": "fast"},
        {"launcher": "supervisor", "model": "available-supervisor"}
      ],
      "aliases": ["standard"]},
    "deploy": {"launcher": "supervisor", "model": "fast-model", "description": "Follow a runbook"}
  }
}
```

PUT replaces the profile; omitted purposes and launcher fields are not touched on application. For cross-version compatibility, `PUT /api/v1/model-profiles/{id}` ignores unknown optional fields sent by newer clients while strictly validating all known fields and rejecting malformed JSON, trailing tokens, non-object bodies, and credential/endpoint keys (`api_key`, `key`, `key_sealed`, `headers`, `headers_sealed`, `env`, `env_sealed`, `base_url`, `token`, `secret`, `password`, `auth`, `proxy`, `no_proxy`) with 400. A tier's description is one line of at most 100 characters, and a candidate added or changed must have a model ID as its model; one saved before that was checked is left as it is, so it never blocks a switch or another edit (a start on it says why it fails). Capturing falls back to the launcher's default for the corresponding preset when the current model is not a valid model ID. A launcher field containing `""` clears that field (except the main model); omitted or null fields leave it unchanged. Saving a profile does **not** apply purpose or launcher settings. Tiers are looked up from the saved active profile when a new Agent actually starts, so editing its tiers affects subsequent dispatches directly. Use `capture: true` with `name` and `revision` to capture today's bindings/launcher settings; existing tiers are retained when recapturing a profile.

`agent_start` and individual `task_plan` tasks accept `tier`, `model`, `effort` and `speed`. Explicit choices win over the tier. A different explicit launcher does not inherit the tier's model, effort or speed; a different explicit model does not inherit the tier's effort or speed. `speed` is `standard`, `fast` or `ultrafast`; omitted or `standard` requests no override. Faster choices apply only to new terminal Codex or Claude Code starts, and an unsupported launcher, runtime or resume refuses rather than starting at the default speed. A delayed plan resolves its tier when dispatched, using the profile active then. Unknown tiers fail rather than falling back silently. See [Launchers](../integrations/launchers.md) "Speed" for the native mappings, catalog check, old-node gate and known Claude Code fallback.

## Verify and finish a switch

1. Read `GET /api/v1/model-config` and `GET /api/v1/model-profiles`; keep the purpose revisions and the rollback preview.
2. Create or edit the chosen profile with the current catalog revision, then apply it. Read the catalog again and check the active profile for drift.
3. Inspect each affected managed launcher with `GET /api/v1/launchers/{name}`. Check `model`, `small_model`, `subagent_model`, and every strong/fast alias (`opus_model`, `sonnet_model`, `fable_model`, `haiku_model`). Alias names are runtime roles, not a requirement to use the model family they name.
4. Run `POST /api/v1/launchers/{name}/check?machine=<machine id>` on a chosen reachable computer. This is a real headless request **without a purpose model override**, so it tests the launcher's actual default. Check the returned model, success, and latency. Test the selected small/subagent models separately; a successful supervisor call does not prove those routes work.
5. Use `model_profile_rollback`, read back the restored fields, then reapply the profile you want to leave active. Confirm credentials and unrelated settings remained unchanged, and keep the before-image until acceptance is complete.

A launcher's custom environment or arguments can override its model fields. Review them privately rather than clearing credentials or unrelated settings. Connected nodes receive managed-launcher updates; offline nodes get them on reconnection. Do not claim fleet-wide verification from one computer's health result. Existing running sessions retain their own execution choices.

## Atomicity, rollback, and audit

Switches commit the purpose records, managed launcher model fields, active profile, bounded rollback history (up to 10 before-images, keeping `previous` synced to the latest entry for backward compatibility), and audit event in one Postgres transaction. A validation or storage failure leaves the switch unapplied. Keeping up to 10 rollback points means a quick round trip such as `performance` → `economy` → `performance` preserves the original pre-switch configuration: repeated `POST /api/v1/model-profiles/rollback` calls step back through each switch, or `{"id"}` jumps directly to a specific point from `rollbacks` in one atomic transaction by folding intermediate before-images. Rolling back the last remaining rollback point stores a single redo entry (`redo: true`), which a subsequent forward switch clears. Rollback restores only the purposes and launcher model fields changed by the unwound switches, preserving the current runtime, credentials, endpoints, and unrelated launcher settings. If a required launcher has been deleted, restore it before rollback. Manual edits to those touched model fields after a switch are replaced by rollback; inspect the preview first.

The history retains the last 50 seed/switch/edit/delete/rollback events and exposes the most recent 20, including actor kind (`device`, `hub`, `assistant`, `system`), device name where available, time, source/target profile, and changed fields. Every save event records its actual stored diff as `name`, `description`, `purposes.<id>`, `launchers.<name>.<field>` and `tiers.<name>` paths (an empty entry whose presence alone changed uses `launchers.<name>`); older readers can ignore this optional field. A tier edit through `persona_settings` remains one event with `<profile>.tiers.<name>` for each profile it changed. These audit paths are distinct from a profile view's `changes`, which compares the saved profile with current configuration. Saves and deletes send the `model_profile` event too, so open settings pages read the profiles again. Reapplying the already-active unchanged profile retains the previous rollback. In-memory serialization protects model-profile operations within the manager; this is not a distributed multi-manager configuration coordinator.
