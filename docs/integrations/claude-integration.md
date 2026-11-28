# Driving Claude Code in herdr

Except where a section states otherwise, conclusions were tested with Claude Code 2.1.310 (Mac) / 2.1.308 (Linux hub machine) and herdr 0.8.0 (2026-10-24).

## Starting sessions

Node's `session.start`:

1. Find a herdr workspace whose label equals the directory name (one workspace per project). If present, `tab create --label persona` inside it; otherwise `workspace create --label <directory name>`. Both use `--cwd <directory>` and `--no-focus`, without stealing focus on the computer.
2. `pane run <pane> "<launcher> --session-id <uuid> --mcp-config ~/.persona/mcp.json --allowedTools <the Agent's own Persona tools>,<office rule> --plugin-dir ~/.persona/agent-plugin --plugin-dir ~/.persona/claude-hooks --plugin-dir ~/.persona/claude-live-replies"`
   (local Claude Code ≥ 2.1; the last plugin only while the computer's live replies setting is on, see "Live replies (MessageDisplay)"; older or unreadable versions use `<launcher> --session-id <uuid> --settings ~/.persona/claude-settings.json --mcp-config ~/.persona/mcp.json --allowedTools <the Agent's own Persona tools>`; see Hooks below and "Persona tools Agents call without a card"). Launchers are functions in `~/.zshrc` (`<launcher-a>`, `<launcher-b>`, etc.) ending in `claude … "$@"`, so arguments pass through. Resume uses `--resume <id>`.
   A non-running session is read-only (#4754, `docs/integrations/engines.md` "Agents run in a terminal"): a message is refused (`agent_stopped`), and the user's Resume on its terminal page resumes it, in the shell it left when that is still there. A branch nothing has run yet is started by its first message. Launcher choice for both follows this order (`resumeLauncher` in `node/sendsafe.go`, #411):
   launcher recorded when Persona started it → inferred from transcript gateway address and model (`inferLauncher`, externally started sessions) → New Agent launcher and model supplied by hub (only for non-running Claude sessions with unknown launchers, `hub/send.go`) → bare `claude`.
   The first two retain the original model. Previously, failure to infer meant bare `claude`; on computers normally authenticated through launchers, the session showed only “Not logged in” and could not send.
3. `waitReady`: every 500 ms, read the screen (`pane read --source visible`) + `agent get`:
   - Directory-trust dialog (“trust this folder”) → compute `down`/`up` + `enter` from cursor position to select “Yes, I trust”.
     **Herdr reports this dialog as idle**, so Agent state alone is insufficient. Previously, immediately sending the first message fed its text into the dialog, selected “No, exit”, and Claude exited.
   - Claude Code's Bypass Permissions confirmation ("WARNING: Claude Code running in Bypass Permissions mode", `❯ No, exit` / `Yes, I accept`, shown by 2.1.322 for
     `--permission-mode bypassPermissions` and for `--allow-dangerously-skip-permissions` until the profile has accepted it once) → `down` + `enter` to "Yes, I accept",
     **only** on a start Persona itself asked for bypass (`permission_mode: bypassPermissions`, or `plan` with `plan_then: bypassPermissions`): the user chose Allow all
     for that task, and the confirmation asks the same thing (`bypassConfirmKeys` in `node/claudeready.go`). It is pressed at most 3 times; a confirmation still showing ends
     the start with what to do. Any other start that meets it (the launcher's own `--dangerously-skip-permissions`, no mode chosen) presses nothing and ends at once with the
     #3294 message. A per-start `--settings '{"skipDangerousModePermissionPrompt":true}'` also hides it, but Claude Code takes only the last `--settings`, which is
     Persona's hooks file or the launcher's own, so it would drop one of them. Persona writes no Claude config either way, but accepting the
     confirmation (by hand or by the node) makes Claude Code itself record `"skipDangerousModePermissionPrompt": true` in that profile's `settings.json`
     (checked on 2.1.322), so the confirmation shows once per profile, and later bypass starts there, Persona's or the user's own, no longer ask.
   - In an Orca tab, Orca naming claude counts as claude having come only with the start's own evidence (#10588, `node/startevidence.go`): Claude Code's
     screen (banner, input box, a dialog), or the start file gone (the shell ran the line) with no not-found or parse-error line on screen. Until then the
     start waits for the shell (3 minutes) and reads the shell's answers; a start line that never showed in the tab, its start file still unread, ends as
     `terminal_not_ready` (nothing ran, the tab is closed), not as a Claude stall. Orca keeps an exited agent's identity on its terminal, and its sends do not
     say whether the text arrived; other engines' word is taken as before.
   - State idle/done and composer footer visible (`shift+tab` / `? for shortcuts` / `bypass permissions` / `esc to interrupt`), twice consecutively → ready.
   - Other blocked state (model selection, a permission Claude asks even in bypass, etc.) → return immediately; hub pushes the screen to the phone as a `screen`
     interaction. The first message is held, not dropped (`firstAfterDialog` in `node/firstprompt.go`): the session is Needs you saying the message goes in once the
     dialog is answered; the node looks at the pane every second for up to 30 minutes and types it once Claude's input box is back and empty, then checks the transcript as
     in step 5. A message that reaches the session first (from the phone or the computer), claude leaving the pane (the dialog's "exit") or the 30 minutes end the wait.
4. Record pane → session, `agent rename j-<first 6 characters of id>`, send first message (`agent prompt`).
5. Confirm first message reached the transcript (`node/firstprompt.go`): a visible composer does not mean Claude has finished drawing startup auto-update notices or Auto mode explanations. Text typed then may be swallowed, leaving the Agent idle. A send counts only when the matching user message appears in a full incremental `promptWatch` from the byte offset captured before typing. The quick metadata head/tail is insufficient: a multi-megabyte image line can exceed both reads and hide an already-submitted first prompt, causing duplicate re-pastes (#4794). If the file does not exist before typing, attach when it appears and filter older rows by the send time. If the owned draft remains after `submitGrace`, retry only Enter with another grace between attempts, within the 15-second confirmation window. Image and long-text placeholders (`[Image #1][Pasted text #2 +26 lines]`) use the same `pendingPrompt` match as ordinary sends, not a visible-text-only comparison (#4794). Startup also pins the placeholder IDs before recovery; fresh sessions expect the image chips followed by their first text chip, and later or changed placeholders are left alone. Once a recovery Enter has been attempted, never paste the first message again: even an empty composer without a transcript receipt is ambiguous. Only a message dropped before any recovery Enter can be retyped after `waitReady` confirms an empty composer and Claude is not working, up to 3 attempts. If still unsuccessful, a dialog is open, or other input exists, mark the session Needs you (`blocked` + explanatory `attention_reason`), not silently idle. Any later message entering the transcript, from phone or computer, restores normal state.
   Confirmation runs in the background; launch still returns immediately. Messages sent meanwhile queue behind it under the same typing lock, without mixing with first-message retries.
   This marker exists only in node memory; after node restart, herdr state is shown.
   The first message was not made a `claude` argument: launchers may be zsh functions / aliases / `personad launch`, with inconsistent argument forwarding. Variable-length options such as `--allowedTools` can swallow later positional arguments, and long messages would enter the shell command line. Transcript verification works for every launcher.

For Persona-started sessions, node state records the launcher. Manually started Claude sessions using launcher functions have no such record; inference uses `matchLauncher` in `server/internal/node/launchers.go`. Every 30 seconds, node scans running Claude processes' environment variables (also `CLAUDE_CONFIG_DIR` in the same scan, see `claudedirs.go`; only processes owned by node's user; Linux shares one `/proc` traversal with Codex discovery, see `userprocs.go`), records `ANTHROPIC_BASE_URL` / `ANTHROPIC_MODEL`, and maps to sessions through `sessions/<pid>.json`. Match the launcher list (`_claude_launchers` functions and aliases calling claude):
if exactly one launcher matches both base URL and model, use it; otherwise, if exactly one launcher has that base URL (runtime-selected model, displayed as `(claude-choose)`), use it too.
Leave other cases blank (multiple launchers share a base URL with no model match, or no base URL), without guessing between candidates. Listing sessions does not wait for the launcher list, which requires a login shell: use cache, refresh expired cache in background. The first list after node startup may therefore lack inferred launchers.

## Running-session speed

The web model panel offers Standard and Fast in eligible Claude Code terminal sessions on supported versions and
models. The node only offers Fast for models that can keep their current identity when Fast is enabled. Claude Code
checks account and provider eligibility when it handles `/fast on` or `/fast off`. It may save the Fast preference
for future sessions under its own settings. The node never writes that preference itself.

The node checks the input box and waits for Claude Code to record its response. The selection shows the last
acknowledged setting, or the last recorded usage speed if there has been no settings acknowledgment. An account
limit can still make a turn run at standard speed.

## Default thinking effort

**Settings › Claude Code › Thinking effort** changes the machine's `settings.json` default. It offers `auto`, `low`, `medium`, `high`, and `xhigh`. Choosing `auto` removes `effortLevel`, letting the model choose its default. `max` is available through the session's effort control or `/effort max`; it cannot be stored as an effective `settings.json` default.

If an older configuration contains `effortLevel: "max"`, the settings page shows the model default and explains that the saved value is not applied. Reading leaves the file untouched. Choose a supported explicit level to replace it, or use the web page's **Reset to default** action to remove it. The node returns `value: null` with `ignored_value: "max"`, so existing mobile clients also display the default and the explanation without an app update. The hub forwards the requested interface language for the effort title and explanation; the web uses its own matching translations.

## Default permission mode

**Settings › Claude Code › Default permission mode** sets `permissions.defaultMode` in the machine's `settings.json`
(every configuration directory, like the other rows; the rest of the `permissions` object is kept, and a `permissions`
value that is not an object is refused rather than replaced). It offers Persona's five modes; resetting removes the key.
Claude Code starts in it when no `--permission-mode` is given: an Agent started from the Agents page with Follow, and,
through the hub, a planned task the assistant left unset (`docs/assistant/todos.md`, "Permission of a planned task").
A mode picked when starting an Agent, and the assistant's default permission mode, still come first.

## Model variables and the 1M marker (tested 2.1.322)

A Persona-managed provider names its models through `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_OPUS_MODEL` / `_SONNET_` /
`_HAIKU_` / `_FABLE_MODEL`, `ANTHROPIC_SMALL_FAST_MODEL` and `CLAUDE_CODE_SUBAGENT_MODEL` (`providerEnv` in
`node/launcherenv.go`). A model Claude Code does not know itself (a gateway's own ID) gets the 1M context window only
when its name ends in `[1m]`, in each of these variables alike: Claude Code strips the marker before calling the
gateway, adds the `context-1m-2025-08-07` beta, and counts the window as 1,000,000 (the result's
`modelUsage[…].contextWindow`); without the marker the same model counts 200,000 (Claude's current IDs, such as
`claude-sonnet-5-5`, count 1,000,000 either way). Measured with `claude -p` against a local fake gateway that logs each
request's model and betas: a tier through `--model opus|sonnet|haiku|fable`, the sub-Agent model through an Agent tool
call the gateway returns, the small model (and the Haiku tier when there is none) through the page summary of a WebFetch
call. `ANTHROPIC_SMALL_FAST_MODEL` wins over the Haiku tier for those background requests.

`CLAUDE_CODE_MAX_CONTEXT_TOKENS` is one value for the whole process: it sets the window of every unmarked model Claude
Code does not know (`400000` makes it count 400,000, and the session's `1000000` makes a 128K sub-Agent model count
1,000,000 too), while a marked model counts 1,000,000 whatever it says. So the node marks the session's model when its
window is over 200K and sets the compact window to that window (#3066); a tier, the small model or the sub-Agent model
set to a model of its own is marked only when that model's window is 1M or more (#5149), since nothing per model would
hold a smaller one to its size. An ID already ending in `[1m]` is left alone, and one with no known window is passed as
it is. The reply limit and thinking levels a gateway reports are in "What a gateway reports about a model" below.

A tier alias in one of these variables is not resolved again: Claude Code 2.1.291 sends `ANTHROPIC_DEFAULT_OPUS_MODEL`'s
value as the model's name, so a provider whose default model or Opus field was `opus` asked its gateway for "opus" and got
404 ("There's an issue with the selected model (opus)"). Behind a Base URL that is not Anthropic's own API, `providerEnv`
counts a tier alias (`opus`, `sonnet`, `haiku`, `fable`, `best`, `opusplan`, `default`, `inherit`, `[1m]` or not) in the
default model, a tier field, the model asked for or the provider's `env` as unset: the tier's own model, else the default
model, else nothing, and Claude Code uses its own ID (#10365). A `--model <alias>` the node passes for such a provider
(`claudeModelArg`: the assistant's start, a terminal Agent and a launcher switch, Test, a typed `/model`) is the model its
environment gives that tier (`opusplan` the Opus tier's), or is left out so the provider's default runs (a typed
`/model` then types `/model default`, which puts the session back on it); `personad launch` maps or drops one that
reaches it anyway, with a line on stderr. An alias asked for with `[1m]` keeps it on the model it maps to, as Claude
Code's own resolution does (`opus[1m]` runs the Opus tier's model with the 1M window, declared or not). Plain `claude`,
shell launchers and providers on Anthropic's own API keep the alias (`TestProviderEnvNeverWritesTierAlias`,
`TestProviderModelArg`, `TestManagedAliasModelArg`, `TestHerdrSetModelMapsATierAliasOnAGatewayProvider`).

## What a gateway reports about a model (tested 2.1.325, #8798)

Model discovery saves what a gateway says of each model (`docs/integrations/launchers.md`, "Provider model and gateway
parameters"). The launch passes on two of those facts; a provider whose models report neither launches exactly as
before (`TestProviderEnvUnchangedWithoutReportedLimits`). Measured with `claude -p` against a local fake gateway that
logs each request body, launched as Persona launches a provider (`ANTHROPIC_BASE_URL`,
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`), and checked against Claude Code's official
[environment variable reference](https://code.claude.com/docs/en/env-vars).

| Variable or flag | What Persona does | Why |
| --- | --- | --- |
| `CLAUDE_CODE_MAX_OUTPUT_TOKENS` | Set from the models' `max_output_tokens` (rule below) | A model Claude Code does not know is asked for at most 32,000 tokens |
| `--effort` (the provider's default level) | Left out when the session's model is marked `no_effort` | The gateway says the model takes no level |
| `CLAUDE_CODE_MODEL_CAPABILITIES` | Not set | Works on 2.1.325, but is not in the official reference |
| `CLAUDE_CODE_AUTO_MODE_SERVER` | Not set; a provider's `env` may set it | Turning it off changes how auto mode checks actions: a product decision |

**Reply limit.** Claude Code asks a model ID it cannot resolve to one of its own for `max_tokens: 32000`, so a
reasoning model, whose thinking counts toward the reply, stops at "Claude's response exceeded the 32000 output token
maximum". `CLAUDE_CODE_MAX_OUTPUT_TOKENS` raises it, up to 128,000 for such an ID (`200000` is sent as 128,000, the
reference says the same); a Claude model already gets its own limit (128,000 for Opus 5.5) and is held to it. Claude
Code knows a Claude ID anywhere in a name (`anthropic/claude-opus-5-5`, a dated ID), so Persona treats an ID with
`claude-` followed by a Claude family name and a version, or a version (`claude-opus-5-…`, `claude-3-5-…`), as one it
may know (`claudeKnows`); a gateway's `…-claude-distill` or `claude-opus-distill-…`, and `claude-sonnet-latest`, are
not. The variable is one value for the whole process, each model
capped at its own limit, so `outputTokens` sets it to the least of what the models the process runs may be asked for:
the session's, each tier's, the small model's, the sub-Agent model's and the `/model` menu's extra entry, after the
provider's `env` (which may name one). What a model may be asked for is its reported `max_output_tokens`, at most an
eighth of the window Persona knows for it (`outputLimit`, below), and nothing when either is unknown. The value is set
only when the session's model is not a Claude one (a tier alias such as `opus`, or `best`, which runs what `opus` does,
counts as its tier's model, and as Claude's own when that tier names none of the gateway's; `opusplan` and `default`
always count as Claude's own; a lower value would lower the Claude model's own limit), when
every other model has a limit or looks like a Claude one (a model with none could be asked for more than it writes,
and a strict gateway refuses that; a Claude-looking ID that reports one counts), and when the result is over 32,000 (a
lower value would lower what every model is asked for today). A session on a Claude model whose tiers are gateway
models therefore gets nothing, and its sub-Agents keep 32,000; the provider's `env` can set the value. The provider's
`env` wins (an empty value leaves Claude Code's own limit), and is pinned in the settings file like the rest of it.

**The window bound (#8816).** A gateway that counts input plus `max_tokens` against the model's window refuses a
request once the input passes the window less the reply asked for, while Claude Code keeps only `min(max output,
20000)` free below the window before it compacts. Measured on 2.1.325: a `400` worded like Anthropic's (``input length
and `max_tokens` exceed context limit: 150000 + 128000 > 200000``) is retried with `max_tokens: 49000` and goes
through, but one worded as an OpenAI-compatible upstream words it (`This model's maximum context length is … you
requested …`, a translating gateway passing it on) ends the turn as "Prompt is too long · the provider said …", and
in a session that fetches no feature flags (a gateway's) Claude Code then compacts. Asking 128,000 of a 200K model
would make such a gateway refuse every turn from about 72K of input on, where 32,000 reached the compaction point
first. So Persona raises a model's reply to at most an eighth of its window: what it adds is a stretch in the last
eighth, right before Claude Code would compact anyway (a 1M model up to 125,000, a 400K one 50,000). A window of 256K
or less gets no raise and keeps Claude Code's own 32,000, exactly as before #8805, with the stretch that default
leaves (a 128K model refused from about 96K, compacting near 108K); Persona does not lower it, which would cut replies
short for every gateway. The window is the smaller of the model's `context_window` and the provider's context limit
(`max_context`, or `CLAUDE_CODE_MAX_CONTEXT_TOKENS` in its `env`); a model with neither gets no raise, and stops the
raise for the whole process like one with no reported limit, since Claude Code's own guess for an unknown ID (1M on
2.1.325) says nothing of the gateway's. A provider whose gateway forwards
Anthropic's wording, or that wants more anyway, sets `CLAUDE_CODE_MAX_OUTPUT_TOKENS` in its `env`; one that sees
refusals near the end of the window sets a smaller value there.

**Which model, and which environment.** The value follows the model the session runs, not only the provider's
default. `launcher.env` sends what each model may be asked for (`max_output_tokens`), and `personad launch` works the
value out again for the model the start runs (its last `--model`, a tier alias resolved as above, else
`ANTHROPIC_MODEL`) beside the provider's tiers, which follow the provider's model there (`sessionOutput`). Persona
does not pin the value in Claude Code's settings: it is in the process environment, and a value in the user's own settings `env` wins over it, as it did before #8798 (a default the user
set elsewhere wins, `docs/design/PRINCIPLES.md`, "Defaults first"); one in the provider's `env` is pinned. Persona marks the value it writes
(`PERSONA_MAX_OUTPUT_TOKENS`, the same value): Claude Code passes its environment to the commands it runs (measured:
`env` in a Bash call shows both), so a `personad launch` inside such a session drops an inherited value that still
equals the mark when it sets none itself (`inheritedOutputClear`), and keeps one the user set (no mark, or a value
the mark no longer matches). A session started by #8805's version carries an unmarked value, which a launch inside it
keeps; and a plain `claude` run inside any Persona session inherits the provider's whole environment, its gateway and
models with this value, as it always has. A model picked in a running session's `/model` menu that is none of the models above is
still asked for the process's value. The reserve below the window is the same for 32,000 and anything above it, so
the value does not move where Claude Code compacts.

**Thinking levels.** The provider's default level reaches an Agent as `--effort` from `personad launch` (the `effort`
field in `docs/integrations/launchers.md`). `launcher.env` now also lists the provider's models marked `no_effort`, and `personad launch` leaves the provider's
level out when the session starts on one of them (`launchEffort`, the session's model read as above). That does not
make Claude Code send no level: to a model it does not know it still sends its own default (`output_config.effort:
high` with no `--effort`; `low`, `xhigh` and `max` are sent as given). The start decides it once: a `/model` switch
later in the session, to or from such a model, keeps the level the session has. When the
gateway forwards the model's refusal of the field unmodified, Claude Code retries without it and leaves it out after
([gateway protocol: automatic retry](https://code.claude.com/docs/en/llm-gateway-protocol#automatic-retry-and-error-forwarding)).
What else was verified, not used:

- `CLAUDE_CODE_MODEL_CAPABILITIES` (`<model>=<capability>,…;…`, a trailing `*` a prefix, `-` before a capability to
  turn it off) is read behind `ANTHROPIC_BASE_URL`: `example-model=-effort` drops `output_config.effort`, and
  `example-model=effort,-xhigh_effort,-max_effort` sends `high` for `--effort xhigh` or `max`. It is not in the official
  reference, so its format may change without notice; Persona does not write it.
- The documented per-tier `ANTHROPIC_DEFAULT_*_MODEL_SUPPORTED_CAPABILITIES` apply only on Bedrock, Vertex and Foundry
  ([model configuration: pinned model
  capabilities](https://code.claude.com/docs/en/model-config#customize-pinned-model-display-and-capabilities)); 2.1.325 ignores them behind `ANTHROPIC_BASE_URL`.
- The documented way to send no level is `CLAUDE_CODE_EFFORT_LEVEL=auto` (or `unset`), measured to drop the field. It
  is one value for the process and wins over `--effort` and `/effort`, so it would lock the session's effort control
  (#2307); a provider can set it in its `env` for an Agent, and the main conversation ignores it there.

**Auto mode's server-side review.** In auto mode, Claude Code asks the server to check the actions it would send for
review, as part of the model requests (`safeguards` in the body, a `dangerous-tool-use-…` beta); behind an
`ANTHROPIC_BASE_URL` gateway it does so by default from 2.1.278
([server-side classifier review](https://code.claude.com/docs/en/permission-modes#server-side-classifier-review)).
`CLAUDE_CODE_AUTO_MODE_SERVER=0` stops asking and makes Claude Code send its own classifier requests instead. Measured
on 2.1.325 as Persona launches a provider: a gateway that returns no review results gets every action that needs the
check refused ("Auto mode can't check actions in this session … every action that needs the check is refused"), not a
fall-back to the local classifier that the page describes; with `0`, the classifier requests go to the gateway on the
model `ANTHROPIC_DEFAULT_SONNET_MODEL` names (Persona sets it whenever the provider names a model: the Sonnet tier,
else the session's model; without one, Claude Code's default classifier model), and a
model that answers with no verdict Claude Code can read still gets the action blocked ("Auto mode could not evaluate
this action"). So `0` helps only when that model can act as the classifier, and it turns off the server's own check.
Persona does not set the variable; a user whose gateway cannot carry the review can put `CLAUDE_CODE_AUTO_MODE_SERVER=0` in
the provider's `env`, or use another permission mode.

## Refusal fallback behind a gateway (#7319)

When a reply stops on a refusal, recent Claude Code retries once and then switches the request to a fallback model whose
ID is fixed inside Claude Code: no setting changes it, `ANTHROPIC_DEFAULT_*_MODEL` included. A gateway that does not
serve that ID answers 404 "model not found" after two requests that could not succeed, and a sub-Agent just fails (on
one install over 7 days, all 4 automatic switches in a private profile ended in 404, while 71 such stops with the switch
off ended without one). Claude Code 2.1.322 was seen falling back to three models; which one depends on the session's
model and the stop's category:

| Session model | Fallback |
| --- | --- |
| `claude-opus-5-5` | `claude-opus-4-8` or `claude-opus-5` |
| `claude-opus-5` | `claude-opus-4-8` |
| `claude-sonnet-5-5` | `claude-sonnet-5` |
| Any other (a gateway's non-Claude models included) | `claude-opus-4-8` or `claude-opus-5` |

So `providerEnv` sets `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=1` for a gateway-shaped managed provider (`providerShaped`,
with a Base URL) whose host is not Anthropic's own API (`proto.AnthropicAPI`) and whose declared model list (`models`)
is not empty and lacks at least one of the three. The session's model can change at run time and the variable is one
value per process, so the list must have all three to keep the fallback. IDs compare trimmed, in any case and without a
trailing context marker such as `[1m]`; a vendor prefix or a date makes another ID, since Claude Code asks for the plain
one. A provider with no model list, or one whose list has all three, keeps Claude Code's fallback: nothing says the
gateway lacks them. The rule reads the list the provider declares, not what the gateway serves: a provider imported
from a computer's shell aliases lists only the model its alias names, so it launches with the fallback off even on a
gateway that serves all three, until its list names them or its `env` sets the variable empty. The three IDs are one
constant (`refusalFallbackModels` in `node/launcherenv.go`) with the version they were seen in, pinned by a test; a
later Claude Code that falls back elsewhere is a one-line change there.

The value goes wherever the provider's other variables go, in the process environment and the settings env alike:
`personad launch` (Agents in herdr and the CLI assistant, and the sub-Agents they run), the generated
`launcher-settings` files and a Persona conversation's own settings. A private profile gets it too, which the user's `~/.claude/settings.json` never reaches. Such a turn ends on its
refusal (`system/model_refusal_no_fallback` and a synthetic reply with `stop_reason: "refusal"`), which the headless
assistant's bounded recovery takes as a safeguards refusal ([Headless assistant](#headless-assistant-chat-tab)), and a
sub-Agent fails with the refusal instead of a misleading 404. The provider's own environment is applied last and wins
either way: `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=` (empty) keeps the fallback on a gateway whose list lacks a model it
serves after all, and `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=1` turns it off for one with no list; the next process start
picks it up. Plain `claude`, shell launchers and providers from before #2455 keep their own environment.

## Tool search behind a gateway (#10365)

Claude Code decides tool search from `ENABLE_TOOL_SEARCH` (read in the 2.1.291 binary): `false` (or `0`, `no`, `off`)
loads every tool up front; unset or `true` defers tools behind `ToolSearch`. With the variable unset it also keeps tool
search off when `ANTHROPIC_BASE_URL` names a host other than `api.anthropic.com` ("[ToolSearch:optimistic] disabled:
ANTHROPIC_BASE_URL=… is not a first-party Anthropic host"), and any non-empty value lifts that rule. With tool search on,
requests carry tools marked `defer_loading` (Persona's deferred tools, other MCP servers' tools and built-ins such as
`WebFetch`), `tool_reference` blocks and the `ToolSearch` tool. A gateway edge that cannot represent them answers `400
request is not representable on this edge: ToolSearch is not supported on this endpoint (defer_loading:true,
tool_search_*, tool_reference)`; on the gateway where this was seen it depended on the upstream model (its Claude model
carried deferral, two other vendors' models refused it). #7371 made every assistant process carry `true`, which put
deferral on every gateway conversation; `claude -p` checks, which set nothing, kept passing.

So Persona decides per start and keeps Claude Code's own rule where it cannot tell:

- **Managed providers.** `providerEnv` sets `ENABLE_TOOL_SEARCH=false` for a provider whose Base URL (its `env`'s
  `ANTHROPIC_BASE_URL`, else the stored one) is not Anthropic's own API, in the launch environment and the settings env
  alike (settings env beats the process environment), so herdr Agents, `personad launch` typed in a terminal or from the
  assistant's Bash, and the assistant all load every tool there. `personad launch` drops an inherited value when the
  provider sets one. The provider's `env` is applied last: `ENABLE_TOOL_SEARCH=true` opts back in.
- **The assistant's start** (`toolSearchFor`, `node/assistant_tool_search.go`): Anthropic's own API (no Base URL, or the
  host `api.anthropic.com`) gets `true`, which also lifts Claude Code's hold-off for a conversation that once ran without
  `ToolSearch` (#7317); a known gateway gets `false`; a start that cannot tell where its requests go (a launcher the probe
  did not see) gets nothing, so Claude Code's rule decides, as before #7371; a launcher that sets the variable itself
  keeps its value. A value the node inherited never reaches claude. Where the requests go is read with the compaction
  policy's layers (`claudeEndpoint`): a managed provider's Base URL; for any other launcher the first of the
  administrator's managed settings env (`managed-settings.d/*.json`, the last in alphabetical order first, then
  `managed-settings.json`, in the system directory of Linux, macOS or Windows: above every other layer, so an
  `ENABLE_TOOL_SEARCH` there counts as the launcher's own; MDM profiles and the server-policy cache `remote-settings.json`
  are not read), the launcher's `--settings` env, the project's `.claude/settings.local.json` and `settings.json` env,
  the profile's `settings.json` env, what the launcher exports and the shell's environment that names
  `ANTHROPIC_BASE_URL` (an empty value is Anthropic's own API).
- **A refusal.** A failed turn whose words are a 400, or a gateway's "not representable", that names tool search
  (`assistantwire.ToolSearchRefused`: `ToolSearch is not supported`, `defer_loading`, `tool_reference`, `tool_search`,
  `tool search`) is remembered for its launcher and model, with the endpoint that refused, in the node's memory only (no
  state file: a restarted node learns it again with one resend). The node restarts that conversation's process with
  `ENABLE_TOOL_SEARCH=false` in its environment and in the start's own settings env, which win over the provider's, the
  launcher's and the user's, and resends the turn once with the model-retry prompt: unseen, on its own budget (the
  same-model and next-model retries stay), with no model-recovery marker. A process started that way and refused again
  ends the turn on the refusal; with background tasks running the restart would kill, the turn ends too and the next
  start loads every tool. Other conversations on that launcher and model restart that way on their next message when
  idle (a busy one keeps its turn), and a spare warmed before is not adopted: the next warm replaces it with one that
  loads every tool. The readiness probe's assistant-turn check (`launchers.check` with `assistant_turn`) takes the same
  decision, by the model asked for (the one the start remembers a refusal by, before `--model` maps a tier alias), with
  `false` written over its own `--settings` env too, so "Try it" passes once the chat does.
- **What is left.** The hub gives such a turn error `turn_error_code: "tool_search_unsupported"` (`assistant/fleet.go`
  `turnErrorCode`): not `model_route` and not the user's to fix, worded "the gateway refused on-demand tool loading; try
  again" (readiness `tool_search_unsupported_*`, action `test_assistant`, not blocking). The node announces all of the
  above as the capability `assistant_tool_search_fallback`; only for a computer whose node does is the readiness step
  worded "Persona sends every tool to this gateway from now on, so try again" (`toolSearchWords`,
  `tool_search_unsupported_fallback_detail`). An older node keeps tool search on, so a retry there meets the same
  refusal, and the chat's card, which can't tell the node, promises only the retry.

Loading every tool costs a gateway that did carry deferral about 23K tokens per request (#7317's measurement), most of
it read from the prompt cache; a provider opts back in with its `env`, and its models that refuse are then resent once
and remembered (`TestToolSearchFor`, `TestAssistantToolSearchFollowsTheEndpoint`,
`TestToolSearchRefusalRetriesOnceWithoutToolSearch`, `TestToolSearchRefusalIsRetriedOnlyOnce`,
`TestProviderEnvToolSearchBehindAGateway`).

`TestGatewayToolSearchLive` (build tag `gatewaylive`, `GATEWAYLIVE=1`, never in CI) runs the machine's real claude with a
provider's environment as `personad launch` builds it, against an in-process emulation of such an edge: it refuses any
deferred tool with that 400 and answers the rest itself, or, with `GATEWAYLIVE_LAUNCHER=<a managed provider>`, passes it
on to that provider's gateway with the key the local node gives (kept in memory, never printed; `GATEWAYLIVE_MODEL`
picks the model). Measured on Claude Code 2.1.291: with tool search on, both requests were refused and the result's
words are a tool-search refusal; with the provider's environment (`false` over an inherited `true`) the turn answered
with no deferred tool; `--model opus` reached the gateway as "opus" with the old Opus variable and as the provider's
model now.

```sh
cd server && GATEWAYLIVE=1 go test -tags gatewaylive -run TestGatewayToolSearchLive -v -count=1 ./internal/node
```

## Persona tools Agents call without a card

An Agent Persona starts has Persona's MCP tools (`~/.persona/mcp.json`) and, in Claude Code's default mode, would ask
before each call. Its own tools run without asking (#4966): `report_to_supervisor`, `todo_decisions`,
`todo_decision_add`, `todo_bind_thread`, and a task leader's `task_plan` and `task_review`
(`assistantwire.AgentOwnTools`). They act only inside Persona and for the Agent calling them (the node names the session
of its pane, `callerTools`), the manager bounds them (a report is rate limited; a leader's `task_plan` adds workers only
below its own task, its `task_review` reaches only its direct workers), and a card for each one would ask the user about
the Agent's own bookkeeping.

- **How**: the start and herdr's resume command of every Claude Agent Persona starts carry `--allowedTools` with exactly
  these tools under both names `mcp.json` registers (`node/agentplugin.go` `agentAllowedTools`, beside the office
  skill's rule). It is Claude Code's own rule for that process only: nothing is written to the machine's Claude settings
  or to the files every session shares, so Claude sessions the user starts themselves ask as before, and a deny rule
  of the user's still wins. The Paseo bridge is given the same names (`--allow-tools`) and answers them itself for the
  Agents it created, whatever their provider (#8141, `docs/integrations/paseo.md` "Mapping"); an agent made in Paseo's own
  app still asks.
- **Still asked**: the phone's tools (`phone_*`) and the connectors' (mail and others), which act outside Persona, and
  `dispatch_start` / `dispatch_read`, which start an Agent on another computer on its model and read that computer's
  work: their effects reach past the calling Agent's own task, so they keep the card by default. Every other tool and
  MCP server is unchanged. Codex Agents in ordinary terminals have none of these MCP tools.
- **Task handoff notes** (#9846): the first message follows the final selected machine, Agent family and launcher
  source, including a health detour. Known Persona MCP integrations retain their tool guidance, including the
  Paseo Codex bridge. Known terminal Agents without that integration are given a verified node executable location
  and the existing `tool task_spawn` arguments when available, without the MCP-only repository tools; that entry's
  text also names the Agent's own tools it runs (`todo_decisions`, `todo_decision_add`, `report_to_supervisor`, and a
  task leader's `task_plan` / `task_review` in the leader's note) with their arguments' shape (#12136). Unknown
  capabilities or missing metadata omit unverified entries and do not block assigned work.
  The executable is the one the node named in the `node.info` its connection began with, which the manager keeps
  (#14446); the node is asked again, within a second, only when the manager holds none for that computer. A fresh
  `node.info` runs Claude Code's version in a login shell, and a computer whose shell is slow to start answered too
  late for every handoff.
  This changes guidance only, not scoped CLI authority or the rules protecting hub credentials.
- **The card's title**: whenever a card is shown for one of Persona's own MCP tools (the ones still asked, or any in a
  session started elsewhere), the manager titles it with the tool's name for people in the user's language
  (`tool_title`, `assistant-tool_titles`: "Report to supervisor · Done", "Note a decision"; the phone's tools as the
  chat's rows name them, "iPhone · contacts search"), never the raw `mcp__persona__…` name, and names the tool alone
  for the "Always allow" line (`tool_name_title`: every report, not only this "Done"; `docs/protocol/protocol.md`
  "Always allow"). The title is in the hub's language, like other text the hub writes. Agents already running pick
  the rule up when they are next started or resumed.

## Independent ultracode switch (#3427)

ultracode makes Claude orchestrate every substantive task as a multi-Agent workflow, independently of reasoning effort. The server supports the independent-switch semantics from Claude Code `2.1.284` onward. Earlier versions require an upgrade, including `2.1.203–2.1.283`, where ultracode occupied the slider's final position and locked the effort level. Running sessions use the version their transcript recorded since the current process started; new sessions use the computer's Claude version. An unknown version may be tried, but the result must still be read back. The switch is off by default, applies only to the current process, does not remember the user's last choice, and is not automatically re-enabled on resume. It is unavailable for headless assistants, including the main chat.

Node's `ultracode.go` sends only `/effort ultracode on|off`. For new sessions it waits until Claude is ready and executes this before the first prompt; that is best effort, never a failed start: a refusal or a verdict not read back goes into `ultracode_note` and the first prompt is typed anyway (#5510), unless the command may still sit in the input box (no verdict, and the box shows a draft or cannot be read), in which case the session needs the user instead of getting the prompt appended to the command. `"ultracode":false` on a start is the same as leaving it out. It does not use `--effort ultracode`, session-specific `--settings`, or change the user's defaults, avoiding replacement of reasoning effort, launcher settings, and login information. Changes share the ordinary-message typing lock and retain the empty-composer / no-dialog checks. While the foreground turn is working / blocked, node records the pending value and waits until the turn ends and the composer is empty; a workflow running on in `background_working` after the turn ended does not hold the switch back. The pending value only holds back the command, never a message: if the phone's next message arrives while Claude rests, the pending command runs first, then the message; while the turn runs, the message joins Claude's own queue and the command waits for the next rest. A draft or dialog keeps it queued with a fixed note (never the draft's words), retried with backoff. Hosts that cannot read or type into the terminal (botmux) refuse the switch with `ultracode_unsupported`.

Read-back chooses the latest evidence by its timestamp, never substitutes the requested value for actual state: post-command screen status, transcript `/effort` output, and `ultra_effort_enter` / `ultra_effort_exit` attachments, excluding sidechains. An explicit `/effort` status line without the `· Ultracode on` suffix means off; ordinary reply text is not evidence. Attachment `reminderType` may be `full` or `sparse`; both prove enabled, but only full and exit generate chat notices. Attachments appear only after a real prompt, and sparse reminders are periodic, not guaranteed every turn. Long transcripts require incremental state tracking, not tail-window-only reading.

Persona launch / resume, or `SessionStart` with `startup` / `resume`, establishes a new process boundary and invalidates earlier evidence. Session creation time is not process start time; `clear` / `compact` do not create a new process. A previously enabled resumed session is recorded off with `ultracode_lost_at` until explicitly re-enabled or the notice dismissed. For sessions Persona started, process boundaries, read-back state, and restoration notices persist in the `ultracode_process` field of their existing node state in hub Postgres (only that field is written, and only once the state has loaded), continuing to filter old-process records after node reconnect; they are not next-launch preferences. Sessions the user started by hand are only observed in node memory: a node state record would make them count as Persona's, and global report-only hooks would be dropped (#5510). Records an older node (#3782) wrote for them are ignored when the state loads. After a node restart, a toggle or dismissal request waits for the state to load before it changes anything. If the initial large-file scan is unfinished, start off and asynchronously recover the notice from the pre-resume file range. Dismissing the notice or re-enabling prevents late scans from resurrecting it. Model refusal reasons are tracked per model and cleared on model change. Dynamic workflows can be enabled through `enableWorkflows` in Claude Code Settings. If unspecified, the account plan determines the default (`default:null`); this does not guarantee bypassing organization policy or feature-availability restrictions.

Protocol, notice-dismissal requests, and old-node `409 node_outdated`: [protocol.md](../protocol/protocol.md), “Claude Code ultracode”. This delivery covers the server only: web #3428, Android #3429, and Apple #3430 add controls and restoration notices. The assistant's `agent_start` / `task_plan` already take `ultracode` per Agent (#12); the web and iOS / Mac show and change it on plan cards and to-dos, and the Android controls are still to come there. Apple's `ChatCache` version bump also belongs to #3430. Status text and attachment keys were checked against static code in the installed `2.1.318` CLI. Tests use synthetic screen / transcript fixtures; no real session was started. Workflow sub-Agent approval counts under default permissions, actual terminal echoes with fixed `CLAUDE_CODE_EFFORT_LEVEL`, and the complete toggle / resume flow still require acceptance in isolated sessions.

## Switching a running Agent to another provider (#5035)

Who it serves: someone in an Agent's chat who wants it to go on with another model provider (a cheaper gateway, a
provider whose quota is not used up) without starting over. Claude Code reads its gateway, key and model from the
environment it starts with, and `/model` only picks among the current provider's models, so there is nothing to type:
the node quits the agent and resumes the same conversation (`--resume <id>`) through the other launcher, in the same
terminal. Code: `server/internal/node/launcher_switch.go`; wire: [protocol.md](../protocol/protocol.md), "Switching a
running Agent to another provider".

- **Picking is not switching.** `POST …/modes {launcher, from, model?, effort?}` (node `session.modes`) checks the pick
  (`planSwitch`, outside the session's typing lock: the checks run a login shell and may ask the hub for a provider's
  secrets) and keeps it as the session's pending switch (`pending_switch` on its row); nothing is quit or typed, so a
  pick never cuts into a turn. The session's own launcher drops a pending switch. Recording takes the typing lock, so a
  pick made while a switch is being carried out is taken against the launcher the agent runs on afterwards; with
  `from` (the launcher the client saw) it is refused as `switch_stale` when that changed.
- **Only the user's own message carries it out**: a `session.send` with `apply_switch`, which the web sends from the
  chat of a session that shows the switch. The assistant's messages, a task's supervision, channels and the phone apps
  send without it: their message goes on the launcher the agent runs on, and the switch keeps waiting. A switch picked
  more than a day ago is no longer shown on the row and is dropped when a message comes.
- **The assistant's switch, when the user asks for it** (`agent_switch`, #5973): the main conversation picks the switch
  and sends at once with `switch_now`, which implies `apply_switch` and is carried out now or not at all. Wherever the
  user's message would go on the old launcher and leave the switch waiting (a busy agent, a draft or a dialog in the
  box, an agent that is not running, a runtime's session) the message is refused with nothing typed (`switch_busy` for a
  busy agent, else the usual code) and the switch is dropped, so none waits for the user's next message. A node without
  the `launcher_switch_now` capability is refused by the hub (`node_outdated`) before anything is typed. An agent a
  usage limit stopped is sent `agents/limit_continue` when the assistant gives no text; its pending usage-limit
  continuation is not sent afterwards (its turn moved on: `workGuard` drops the wake).
- **Claude Code's usage-limit question** (#6237): a turn that stops on a subscription limit leaves the question
  "What do you want to do? 1. Stop and wait for limit to reset / 2. Wait here… / 3. Switch to usage credits" on screen,
  and herdr reports the agent blocked. A launcher switch about to be carried out (a message with `apply_switch` or
  `switch_now`, or `session.switch`) answers it with the first choice when that one is selected, waits up to 10 s for the
  question to go and the agent to be idle, and goes on; it never picks the credits or the wait in the terminal. Any other
  dialog, or the question with another choice selected, still makes the switch wait (`switch_busy`).
- **Many at once** (`agents_switch`, #6008): when the user asks, the assistant moves a list of agents, or every running
  Claude Code agent on a computer and/or launcher, with one confirmation. One a usage limit stopped is switched with the
  continuation (`switch_now`); an idle one is switched with no message (node `session.switch`, capability
  `launcher_switch_idle`: the same quit and resume, then it waits on the new launcher for whatever message comes next);
  a busy one (working, waiting for the user, starting, background work, or refused `switch_busy` by the node) is queued
  by the manager and switched when its turn ends, with the continuation if that turn ended on a usage limit. The queue
  is the manager's (settings key `agents.switch_queue`, kept over restarts), apart from the user's own pick
  (`pending_switch`, carried out by their message): a queued switch is picked only when it is carried out, and is
  dropped when the agent moved to another launcher meanwhile or after a day. `agents_list` shows a queued one as
  `switch_queued=<launcher>`. A stopped agent Persona started (one in the batch, or a queued one that stopped before
  its turn ended) is not started: `session.switch` with `launcher` points its record at the new launcher and model, so
  its next Resume, or the message that resumes it, comes up there ("switched (takes effect when resumed)"). A switch
  that fails on the node (it did not come up and rolled back) is reported as refused with its reason and not retried.
- **An agent off its launcher** (`off_launcher`, #11792): a Persona Agent whose claude runs without Persona's start
  command (herdr's bare `claude --resume <id>` after a restart on herdr older than 0.9.2) does not run on the launcher
  its row names. `agents_list` says so after `launcher=`, and a switch to that launcher (`agent_switch`, `agents_switch`,
  the chat's pick) moves it back there rather than being refused as one it runs on already; `agent_switch` does not take
  such a pick back when its message fails, since picking the agent's own launcher is the switch back (a switch to
  another launcher it does take back: with that switch waiting, the own launcher's pick cancels it). Busy, it is
  queued and switched back when its turn ends.
- **A busy agent is not switched**: one that works, waits for the user, is starting, or has background work a quit
  would end gets the message on its launcher as always, and the switch waits for a later message. Background work is the
  row's (running subagents and workflows, a `/loop`) and every background command or monitor of its claude process,
  however old: the session list leaves out the ones started over a day ago, the switch counts them from the process's
  start (its ultracode process boundary; a day when that is not known). A transcript whose first read is still running
  counts as busy.
- **Everything is worked out before the quit** (`applySwitch`, inside the message's turn, after the usual draft and dialog
  checks, at most a minute): the launcher's checks below, the conversation's folder, the model to start with, the
  launcher and model to come back with. Then a last look (the agent's state, background work, a draft or dialog in the
  box: a turn may have started meanwhile), the session is listed as starting (messages and the user's Resume wait for it),
  and only then Ctrl+C until the agent leaves its pane (`quitAgent`, the pane kept, never
  `stop`, which would take the typing lock again), a wait until only a shell is in front and no claude has the
  conversation open any more (Claude Code still writes its transcript as it exits), and the launcher's command typed into
  that shell with `--resume`, the current permission mode (and the plan's next mode, so `--allow-dangerously-skip-permissions`
  stays), the picked effort (else the current one) and the picked model. No model picked: what a new session of that
  launcher runs (`resumeModelOf`), since `--resume` alone would bring back the old provider's model (#2487); when that
  cannot be seen (a provider that names no model, a wrapper script) the pick is refused as `switch_pick_model`.
- **In the conversation's folder**: `claude --resume` finds a conversation by the project folder named after the
  directory it started in. The folder is the first of the record's directory, the agent's and the transcript's whose name
  is the transcript's project folder (the transcript's own `cwd` may follow a Bash `cd`); none of them: refused. A shell
  that is elsewhere gets `cd <folder> &&` in front of the command (`cd <folder>;` in nu and elvish, which have no `&&`);
  the record keeps its folder.
- **Nothing else restarts it meanwhile**: from the moment the message takes the switch up (before its checks) until it
  ends, a pick or cancel is refused at once as `switch_stale`, and a stop or archive at once as still starting, rather
  than held for the minutes the message holds the session's typing lock. Whatever else would type into the pane or
  press its keys (a model, mode or effort change, a rewind, a dialog answer, keys, an interrupt, a close) is refused as
  `switch_under_way`: on a resident node's next connection the switch may run on the last connection's Node, whose
  typing lock this one does not hold.
- **From the first Ctrl+C on, on its own clock** (`context.WithoutCancel`, at most 5 minutes): a hub redeploy that ends
  the connection does not leave the agent quit and not started again. The start on the new launcher gets 90 seconds.
  The hub waits 8 minutes for a message with `apply_switch` (enough for a switch to the other engine too, below). A resident node (`personad node --attach`, Persona Link)
  makes a new Node for each connection; what the node keeps of switches outside the records is shared by its Nodes
  (`switch_share.go`, handed over as the background scans are): the next connection's Node lists a switch still under
  way as starting, its messages and the user's Resume wait for it, picks and stops are refused; the records the switch
  writes once its connection is gone go to the next Node, over the copy it loaded from the hub, and through it to the
  hub, in the order the switch wrote them, and only the fields the switch owns (launcher, model, folder, start time, plan
  mode, pane, pending switch), so what the new claude's hooks recorded on the new connection stays. When the switch
  ends the serving Node pushes its list (the row leaves starting) and shows the model and modes it started with. After
  the quit the switch needs nothing from the hub (the launchers' secrets were fetched by the checks before it). The
  computer stays reachable meanwhile: nothing waits for the old Node.
- **A failed switch is dropped and rolled back.** Whatever fails (the checks when the message comes, an agent that does
  not quit, a start that does not come up), the pending switch is cleared, so nothing tries it again on every message,
  and the message is refused with nothing typed. After a start that did not come up, the record goes back to what it was
  (launcher, model, folder, start time, plan mode); a claude that start left in front (stopped on a sign-in screen of the
  new launcher's directory) is quit with Ctrl+C until the shell is back, and when the own launcher still passes the
  checks (worked out before the quit) the agent is resumed on it in the same shell with the model it last switched to
  from the app, else the record's (`switch_failed`). When that fails too, the agent has stopped (`switch_failed_stopped`):
  Resume on its terminal page brings it back on its own launcher. An agent that does not quit runs on
  (`switch_not_stopped`). Every refusal says `delivered: none`, so the client keeps the words in the box.
- **What is refused** (when picking, and again when the message comes, since things change): not a Claude Code Agent
  running in a terminal Persona can type into (`switch_unsupported`: Codex and other CLIs, runtimes, assistant
  conversations, which switch in their own chip, botmux, hosts that cannot say what runs in a pane); a session on a
  launcher Persona cannot tell (one it did not start whose environment names no launcher: it could not be brought back on
  it); a launcher not on the computer (`switch_launcher_missing`) or of another agent (`switch_other_agent`); a model
  with a launcher that fixes its own `--model` (`switch_model_locked`); no model, and a default that cannot be seen
  (`switch_pick_model`); and a launcher whose Claude config directory does not hold the conversation at its latest copy
  (`switch_config_dir`). The last one is the main risk: `--resume` looks only in the config directory claude runs with,
  so a launcher of another profile would answer "No conversation found", or resume an older copy and lose the turns
  since; and the sign-in lives in that directory too, so forcing the conversation's directory on the launcher could run
  it on another account (`docs/lessons/agents.md`). The directory is the one the launcher sets (a managed provider's
  environment, `claude_start_dir` for plain claude, what the probe saw a shell launcher export, `~/.claude` when it unsets
  the variable with `env -u`), else the login shell's `$CLAUDE_CONFIG_DIR`, else `~/.claude`; it must hold the newest copy
  of the transcript (a symlinked `projects/` counts). One that cannot be told (a launcher the probe cannot follow or that
  is a script on PATH, a login shell that did not answer) is let through: a resume that finds nothing ends the start,
  and the switch rolls back. The pane's own shell environment is taken to be the login shell's.
- **Where the pending switch lives.** In the session's state record (`pending_switch`, persisted with the model in the
  hub's Postgres), so a node restart neither drops it nor carries it out by itself; a message right after a restart
  waits for the record to load first. A session Persona did not start has no record and gets none for this (a record
  means "Persona started it"): its switch waits in the node's memory (a resident node's reconnect keeps it), and a node
  restart forgets it, so its row shows none and nothing is switched behind the user's back. A switch carried out writes a record, as a Resume does, and so
  does one that failed: a session Persona did not start keeps a record of its own launcher after a failed switch, the
  one a Resume brings it back on once no process of it is left to name it.
- **Not carried over**, as with any resume: the ultracode switch (its resume notice says so), "allow for this session"
  rules, a running `/loop` (it holds the switch back), and bypass availability, which follows the new launcher's own
  flags. The new provider starts with a cold prompt cache. The row's `model` shows the picked model until a reply names
  one.
- A stopped agent with a pending switch stays as it is: Resume brings it back on the launcher it ran on, and the next
  message from the chat carries the switch out then.

## Switching a running Agent to another engine (#5054)

Who it serves: someone in an Agent's chat who wants the conversation to go on in the other engine (Claude Code ↔
Codex) without starting over, in the same terminal. Neither engine can read the other's conversation, so the node gives
the other engine a handoff of it; when the conversation ran on that engine before, the session it ran in then is
resumed instead, with only what happened meanwhile. It is the provider switch above with a launcher of the other
engine: the same pick, the same message that carries it out, the same time bounds and rollback. Code:
`server/internal/node/engine_switch.go` (the handoff text: `engine_handoff.go`); wire:
[protocol.md](../protocol/protocol.md), "Switching a running Agent to another engine"; the Codex side:
[codex.md](codex.md) ("Starting from the phone", "Resuming from the phone").

- **Picking**: `session.modes` with a launcher whose engine (`launcherModelsAgent`, after the checks that keep a word that
  is no launcher from being run) is the other one than the agent's is a switch to it (`planEngineSwitch`), kept as the
  pending switch with `agent` naming the engine. Checked: a Claude Code or Codex Agent in a terminal Persona drives
  (`switchSession` with `engine` set: still not an assistant conversation, a runtime's or botmux's session, a Codex
  thread an app-server or another program runs, an agent in a terminal only); its own launcher known, since a failed
  switch brings it back there (Claude Code: as above; Codex: its record's, which must still run Codex); its conversation's
  folder (Claude Code: `switchCwd`; Codex: its record's, else its rollout's `session_meta`); the launcher on the
  computer, running that engine (a shell function running Codex is refused, as for a start), and taking the model when
  one is picked. The effort is the one picked only (the engines' levels differ). A Codex session picking another Codex
  launcher stays refused: Codex has no provider switch.
- **The parked session**: the most recent stretch of the target engine in the session's `engine_history` (its record's).
  When there is one it is checked too, and one that cannot be resumed is no refusal: a fresh session takes the
  conversation. A Claude Code session needs its transcript, its project's folder, nothing having it open
  (`openElsewhereRefusal`), a launcher whose config directory holds its newest copy (`checkSwitchConfigDir`), and without
  a model picked one the launcher runs by default that can be seen, unless it ran on that launcher; a Codex thread
  needs what any resume of it does (`codexResumable`), a launcher running Codex in its home, and no unconfirmed resume of
  it still coming up.
- **Carrying it out** (`applyEngineSwitch`, inside the message's turn, before a Codex's app-server would take the
  message): a busy agent gets the message and the switch waits (Claude Code: `switchBusy`; Codex: working and not only by
  herdr's stale word, waiting for the user, a turn on its app-server, starting); a message with photos is refused
  (`switch_unsupported`) and the switch waits, since only text goes over; then the usual draft and dialog checks, the
  plan again, both handoffs (the whole conversation, and for a parked session only the stretches after its last one plus
  the current), a last look, the session listed as starting, Ctrl+C (`quitAgent`), and a wait for the shell
  (`shellAfterQuit` for the engine: for a Codex, until no Codex CLI holds its rollout; an app-server keeping the thread loaded does
  not count). Then the to-do link of the pane (`persona_todo_ref`) is written as its `persona_from`, so the new session
  joins the same to-do, and the other engine starts in that shell (`startEngine`, the pane adopted as "Run Agent Here"
  does): the parked session resumed (`--resume <id>` and the message; `codex resume <id> -- <message>` through
  codex-start in its folder), else a fresh one (`--session-id`, the prompt typed once it is ready, `cd` first when the
  shell is elsewhere; codex-start with `dir`). The start's prompt is the handoff block and the user's words (`handoffMessage`, template `prompts/agents/engine_handoff_message.md`); nothing is
  typed again. The permission mode goes where the other engine has it: plan both ways, bypassPermissions to Codex as
  its bypass flags, the rest is the launcher's.
- **Records**: the start writes the new session's record with the conversation's history (`withEngineHistory`: the
  history before it plus this session's stretch until the switch began), so the list never shows the two as separate
  rows; then the new record loses the pending switch (and keeps the model picked for Codex, what a Resume of it runs),
  and the session switched away from loses its history, switch and pane (a reload binds the pane to the new session
  only). The pane, when Persona opened it for the old session, is marked as the new one's (`persona_tab`). A switch
  whose connection went away hands the new session's record on as well (`switchShare.keep`: its last stretch names the
  switch's session).
- **The list** (`joinStretches`): rows get the history their records carry, and every session named in another listed
  row's history, other than that row's own, is left out while it is parked there (`parkedRow`: idle, done, offline or
  cut off by a restart, with no pane and no claude process outside Persona's panes that has it open; or the row of the
  switch moving it on). A Codex thread an app-server keeps loaded after a switch to Claude Code is parked too: listed,
  a message to it would start a turn on the conversation's old thread, and Persona no longer joins it again when that
  app-server comes back (`forgetCodexJoin`; the join of the running connection lasts until it ends). One that runs
  again (in a pane, resumed by hand; a claude in another terminal) or works or waits for the user is its own row, and
  so is one used after the conversation left it (its row's activity more than two minutes after its stretch's `until`,
  `stretchGrace`: a Codex resumed in another terminal or the Codex App; the switch itself quits it right then). The
  most recent conversation counts first, and a row it left out names none, so two records still naming each other
  keep the newer one unless the older one runs. A row continuing a conversation shows its first stretch's title unless
  its own is manual.
- **The parked session while it is resumed**: the switch holds it from just before the first Ctrl+C until the switch
  ends (`switchShare.hold`, unlisted): a message to it and the user's Resume (`awaitSwitch`) wait for it and then find
  it running, a stop, a pick or keys are refused, so no second writer of its conversation starts beside the switch's.
  The user's Resume registers itself while it starts a session (`resumeTurn`, `switchShare.resuming`, after it waited
  for any switch of it): a switch does not take up a session being resumed so (the hold is refused, and a fresh
  session takes the conversation). As soon as it holds the session, before the first Ctrl+C, the switch looks at it
  again (`parkedNow`: running, or no longer passing its checks: a fresh session takes the conversation at once), and
  once more right before the resume (`resumeParked`): the session's typing lock is waited out within the start's time
  (`waitTyping`: a resume of it already under way, a Codex's resumeCodexSession), and one that runs by then, is open in
  another claude or is no Codex thread to resume any more is not resumed; a start that finds it running elsewhere and
  typed nothing counts as one that did not come up. The typing lock is not
  what holds it: the claude start's first prompt takes that lock itself.
- **Fallbacks**: a parked session that does not come up gives way to a fresh one once (its record put back,
  `stateFile.putBack`); a fresh one that does not come up loses the history it was given, the pane is cleared and the
  agent resumed on its own launcher (`comeBack`: Claude Code as above; Codex `codex resume <id>` with its record's
  model and mode), `switch_failed`, or `switch_failed_stopped` when that does not come up either. A start in the adopted
  pane never closes it (`closeNewTab`, `closeCodexStart`).
- **A start that may have the message never falls back.** The message is in a Codex start's argv: once codex-start has
  claimed its file, the Codex in the pane may run it whenever it gets past where it stopped (a dialog, a thread not seen
  in time). codex-start claims the file by renaming it away before it reads it, and the node withdraws a file it gives
  up on by renaming it away too (`claimCodexStartFile`, `codexMessageMayHaveGone`): exactly one of them has it. Such a
  start's error is `delivered: "unknown"` (`unconfirmedStart`) when codex-start claimed the file and the pane does not
  show only its shell again (after a `personad codex-start:` failure line the shell gets three seconds to come back in
  front, `codexFailWait`); what codex-start printed is not otherwise read for this (its notes, `personad codex-start (note):`,
  come right before the Codex with the message; a codex-start that failed is back at the shell). So is a claude start
  whose first prompt was typed but not taken (herdr's error is not one of nothing typed). Then the switch stops where
  it is (`unconfirmedSwitch`): no Ctrl+C, no other start, no rollback, the pending switch dropped, and the message
  answered as uncertain so the app does not offer the words again as if nothing had gone. When the session that start
  was for is known (a fresh claude's id, the parked session) the records say the conversation went on there
  (`engineSwitched`, which makes a record for a session Persona has none of, a parked Codex thread started elsewhere):
  it is the conversation's row, and the session switched away from, which was quit, is an earlier stretch of it. A
  fresh Codex thread that did not show is not known: the pane stops standing for the session switched away from (its
  binding and its record's pane go, so its row does not show the Codex and a message to it is not typed into it), no
  other thread is guessed for the pane, and the node looks for the thread for five more minutes (`watchLateCodex`):
  once it shows, it is bound to the pane and takes the conversation; a pane back at its shell ends the look. A start
  whose codex-start file was never claimed withdraws it (so a line the shell runs late finds nothing) and falls back
  as before.
- **A first message a dialog held back** (the new claude came up behind a dialog of its own, such as a large session's
  resume prompt, or the ultracode command may still sit in its box: `firstPrompt`, `start`): the switch is done, and
  the session's row says its first message did not go in, as for any start. The handoff is kept on the new session's
  record (`PendingHandoff`, persisted in the hub's Postgres like the history, with when that start began) and goes in
  front of the next message sent to that session, whoever sends it (send.go): the words the user sends again do not go
  without it. It is cleared once that message is known to be in (`afterPrompt`), and dropped instead of put in front
  when the session got a message since it was kept or works (`promptedSince`: one typed at the computer, or the earlier
  message whose arrival was not confirmed), so it never comes twice; also when the session's lost first message is
  cleared that way, and on a session a later switch moves the conversation away from.
- **Time**: as the provider switch, but up to three starts (the parked session, a fresh one, the agent back), so the
  switch's own clock is 5½ minutes (`engineSwitchBudget`); with the minute of checks and the handoff built before the
  quit it stays under the hub's 8-minute wait for a message with `apply_switch`.
- **Not carried over**: the reasoning, tool results and everything the handoff leaves out; the prompt cache (once); the
  ultracode switch, "allow for this session" rules, background work (it holds the switch back), photos.
- **Known limits**: a Codex effort of `minimal` or `ultra` is refused when picked (a start takes Claude Code's levels and
  `auto` only); the block names a model only when the pick or the launcher's list names one (the list's placeholder for a
  launcher that names none is not shown); a switch whose connection goes away and then fails rolls its records back on
  the old connection's Node only (`switchShare.keep` hands on what the switch wrote, not the rollback), which leaves a
  started session that never came up with the history: it is not listed (no transcript), so nothing is hidden by it; on
  a node without `engine_switch`, a pick of a launcher of any other agent reads "Update the node" (`hub/ultracode.go`).
  A Codex thread in a `CODEX_HOME` other than the default (a launcher that sets one, a thread of another home) is not
  resumed on a switch back: a fresh thread takes the conversation with the whole handoff. Rolling a Codex agent back
  resumes it with its record's model and plan mode only: a bypass or an effort it was started with is not restored, so
  it comes back more restricted than it ran. Between the plan and the hold, a claude --resume of the parked session
  typed by hand in a terminal is caught only by the looks again (it runs, or a claude has it open). A first message
  found lost only later, by the background check after it was typed (the box not back, other text in it, three tries),
  does not keep the handoff for the next message. The late-Codex watcher belongs to its connection's Node: a resident
  node that reconnects within those five minutes loses the late join, and the conversation is two rows. A hidden
  earlier Codex thread still takes a message sent to its id directly (the web opens the conversation's row instead).
  While the watcher looks, its pane counts as a Codex starting (`codexStarting`: a node self-update waits) and it
  answers Codex's trust and update screens there, for up to five minutes.

## Live status and model retries (#887)

Measured with Claude Code 2.1.326 against local synthetic 429 and 500 responses: while the turn is still active, the CLI draws `✻ 429 <error> · retrying in 4s · attempt 5/10` (or the corresponding 500 error) above its input box. An early line can say only `API Error`; a later one names the status and detail. Interrupting the turn removes the line. This is separate from `system/api_error` transcript rows: a CLI that writes those early gets the existing first/final retry notices, while one that writes them only after exhausting its attempts still needs the screen status.

Node reads this banner through the same bounded, watched-session `session.live` path as the ordinary spinner. It sends only the observed error and current/max attempt, not the countdown, and never infers a model error from elapsed time. The last ordinary verb, elapsed baseline, output count and thought duration stay with the retry because the banner has no counters; current thinking is not carried over. Ordinary work replaces the retry; a disappeared line, turn end or unwatch sends `live: null`. Clients also forget it on unsubscribe, and a long backoff does not expire it. Web and Apple chats use their existing API-error classifiers for the plain reason, keep the observed error as secondary detail, and show a localized attempt count before the final failure.

## Session IDs and transcripts

- Mac has herdr's Claude integration; `agent list` includes `agent_session.value` (= Claude session ID). **The hub machine does not**, so it lacks the ID.
- Fallback order: herdr report → `HERDR_PANE_ID`→`session_id` recorded by our hook (hook inherits pane environment; only Persona-started sessions) → **the pane's Claude process's `~/.claude/sessions/<pid>.json`** (maintained by Claude Code, includes current `sessionId`, updated after `/clear`; obtain PID with `herdr pane process-info --pane <id>`, cache one minute) → title matching AI title → newest `.jsonl` in the project directory inferred from cwd (replace every nonalphanumeric cwd character with `-`, e.g. `/Users/me/code/persona` → `-Users-me-code-persona`).
- The final two are guesses and necessarily wrong for newly started Claude: `sessions/<pid>.json` appears several seconds later (four or five on Windows), and its own `.jsonl` does not exist before the first message. “Newest in directory” belongs to the preceding session, so a new tab initially shows the old session (#1980). Claude panes appearing after node startup therefore get a 20-second grace period (`freshPaneGrace`). With neither PID file nor pane binding, do not guess or list them during that period. Recheck PID files every refresh (normally missing files are retried every 5 seconds). After grace, resume guessing for old Claude Code versions without PID files. Panes already present at node startup are not new and are guessed immediately.
- Herdr's ID comes from the Claude integration SessionStart hook, which binds the process's `HERDR_PANE_ID` to the pane. Claude processes outside that pane can report it too if they inherit this variable: a tmux server first started inside herdr (as botmux does) passes `HERDR_*` to every later Claude process. Herdr's main tab then shows a botmux session while the actual pane Claude appears offline (2026-11-04). When a terminal Claude runs a session unclaimed by any pane, node also reads its own `sessions/<pid>.json`; if it differs from herdr, trust the pane process (`setAgentsAt`, `unplaced`).
- Transcript: `~/.claude/projects/<encoded cwd>/<session id>.jsonl`. Append a line when each content block completes (thinking, text, tool_use each on separate lines; tool_result in the next user line). Not per-token; word-by-word display comes from screen sampling.
- Skip `isSidechain` (sub-Agents), `isMeta` (system-injected user messages), and non-chat lines such as `attachment` / `api-request*` / `mode` / `permission-mode` / `file-history-*`. `ai-title` supplies the session title. From `system`, retain only `compact_boundary`, `api_error`, `informational`, `away_summary`, `local_command` (new versions write slash commands and output as system rows: `<command-name>` is user input, `<local-command-stdout>` becomes a notice after stripping terminal escapes), and `scheduled_task_fire` (/loop due, a wakeup). `api_error` writes a row per retry (`retryAttempt` / `maxRetries`), reason in `error.formatted`; show only the first and final abandoned retry. Other rows may have a string `error`; parse raw JSON. Identical `<task-notification>` content often appears both as a user row and queued_command; show once. Multiple notifications for the same task with different content are real events.
- **A turn that ended on a model API error** (`server/internal/transcript/turnerror.go`, #1710): Claude Code writes its own reply for the failed request, an `assistant` row with `isApiErrorMessage: true` and model `<synthetic>`, shown as the answer as before. Its text also becomes `Meta.TurnError` (→ `Session.turn_error`), and its string `error` field the kind: `rate_limit` with `quotaLimits.status: "rejected"` is a subscription's usage limit (`usage_limit`, reset = `quotaLimits.resetsAt`, epoch seconds), `authentication_failed` and `model_not_found` the user's to fix, `server_error` transient; anything else (a gateway's 429 is a `rate_limit` without `quotaLimits`, `unknown`, an older version with no field) gives no kind and the hub reads the text. A turn start (a message or a wake; not a local command) and a reply from the model clear it. `turn_error_after_work`: the turn had a non-error reply, tool call or thinking before the error. `turn_error_resumes`: an `informational` notice saying "… continuing automatically …" (not the reset's own "Usage limit reset · …"); "Automatic continue …" (cancelled, turned off, stopped, did not run) and "… Press enter to continue" take it back, as do a typed message (Claude Code cancels its continuation on any submit, silently), a reply and another kind of error. It survives the wake turns of background tasks during the wait: each fails again at once with no new notice. Rows of a sub-Agent (`isSidechain`) never touch it, except in that sub-Agent's own transcript. A turn gone back over by a rewind takes its error with it (`Tail.SettleTurnError`).

## Background sessions (/fork, move to background)

Tested 2026-11-04 with Claude Code 2.1.303 (`server/internal/node/claudebg.go`):

- Sessions started by `/fork <message>` or moved to background do not run in terminals. `claude daemon` launches a Claude process; `sessions/<pid>.json` has `kind: "bg"`, with `status` busy / idle / shell (turn over, a background command or Monitor still running; Claude Code 2.1.31x and later, idle before) / waiting (question or dialog). No pane; herdr does not receive its SessionStart hook. Transcript remains `projects/<cwd>/<session id>.jsonl` (fork copies history with a new ID and `sessionKind: "bg"` per row), with sub-Agents beside it. Previously these always appeared offline.
- Node lists live background sessions as running (PID exists, not an unclaimed warm `spare`, transcript exists): busy → working, idle and shell → idle, waiting → blocked, others unknown. Source `source_kind: "claude_bg"`, `capabilities` read (and send, when its peer inbox is one the node can use: "Claude outside Persona's terminals" below), with `read_only_reason` when it cannot send. The app can view them with streaming updates, sub-Agents, and background tasks, but cannot answer cards or view a terminal; hub creates no screen cards. A send goes through the inbox, or is refused with why, typing nothing.
- Interactive Claude parking its session: `sessions/<pid>.json` retains `sessionId` and adds `parkedJobId` (original transcript ends with `continued-in`). Pane shows Claude's agents view; returning removes `parkedJobId`. Herdr still identifies the pane by SessionStart ID and reports status from the terminal title of the background session opened in agents view. The original session thus appears Working, and phone messages type into agents view. Like Claude Code, node does not treat a parked process as holding this session open: no typing into that pane, no herdr-derived state; normal behavior returns when Claude returns to it.
- Agents view opens the background job the session moved to (`jobId` in background `sessions/<pid>.json` equals `parkedJobId`), showing that chat and composer with terminal title equal to its `name`. When pane title equals this job's `name`, node identifies the pane as that background session, lists it in the corresponding herdr tab, and sends / receives through the pane rather than a separate read-only `claude_bg` entry (2026-11-04: four herdr tabs, one missing on phone).
- In the agents list (title not equal to job `name`: job finished / stopped, or another selection), retain the pane under the original session's tab: idle, `capabilities` read + terminal, with `read_only_reason`. The phone can select with arrow keys / Enter in terminal just like the computer; entering associates the pane with that session. Do not type messages into the list; node rejects with `open_elsewhere`, typing nothing—the list composer would create a new background session. Previously such panes appeared nowhere (2026-11-05: tabs 2 and 4 disappeared after two jobs ended).

## Claude outside Persona's terminals (desktop app, other terminals)

`server/internal/node/claudebg.go`, `claudepeer.go`. Measured 2026-11-07 on Windows 11 with Claude Code 2.1.308 (the desktop app's own) and 2.1.291 (the CLI).

A Claude session can run where Persona has no pane: in Claude's desktop app (its Code page), or in a terminal herdr, Orca, Tern and botmux do not run. It writes `sessions/<pid>.json` like every claude: `sessionId`, `cwd`, `kind: "interactive"`, `entrypoint` (`claude-desktop`, or with a suffix in some builds, `claude-desktop-3p`; `cli` in a terminal), `status` (busy / idle / shell / waiting, with `waitingFor`), `version`, `procStart` (on Windows the process's creation time as a FILETIME, on Linux `/proc/<pid>/stat` field 22), `pidDomain` (`win32:<host name>`, never copied anywhere), and, since 2.1.29x, its peer inbox: `messagingSocketPath`, `peerProtocol: 1`, `peerFeatures` (`notify_idle`, `artifact_yield`). Before, these listed as not running, and a message from the phone was refused (`open_elsewhere`): a second `claude --resume` would write the same transcript.

Node lists every live one (`procStart` matches the running process: a reused pid is not it) as running, with source `claude_desktop` or `claude_cli`, its status from the file like a background session's (busy → working, idle and shell → idle, waiting → blocked with `attention_reason`), except that a desktop app session's idle is `done` (Unread) while its last turn ended unseen ("Unread when a turn ends unseen" below, #6477); and a desktop app session whose latest turn ended on the app's end-of-turn question to the owner (the last tool call of the turn is `mcp__ccd_turn__wrap_up` with `blocked: true`, and no prompt of the owner's follows) is blocked, with that question as its `attention_reason`: Claude Code's status file says idle and the app's index records only that a box exists, so the transcript is where the node reads it (`claudedesktop_turnask.go`, #16406), and its transcript as for any session. Its title is the transcript's (its AI or custom title), else a name something chose for that claude (`name` with `nameSource` `user`, `peer`, `collision`, `auto` or `hook`), else its first message as for any session; the name Claude Code derives itself (`nameSource: "derived"`, the directory's base name and two hex digits: `home-a8`) only stands in while there is none of these, since the sessions of one directory would differ by those two digits alone. A `cli` claude whose environment names an engine's pane (`HERDR_PANE_ID`, `HC_TERMINAL_ID`, `ORCA_PANE_KEY`, `TERN_PANE`, `BOTMUX_SESSION_ID`…) belongs to that engine and is not listed here, but only while an engine in use lists that pane with a claude in it. A pane variable alone is not enough: a claude in Orca's terminals on a node that does not enable Orca, or one the engine's process scan missed, is listed here as running instead of falling through to the offline list, once the node has seen it for 5 seconds (an engine reports a claude just typed into its pane a moment after it starts, so it does not flip to this row and back). Claudes run from an SDK (`entrypoint` `sdk-…`) are left to their own host. At rest, a program's one-shot run of the command line is left out of the offline list too, before a row is counted toward the 80 (#3828, `node/claude_oneshot.go`): a script running `claude -p` all day pushed the user's own sessions out. It is one from its first entrypoint to its last (`sdk-cli`, `transcript.ClaudeOneShot`: the last read within the transcript's last 8 KB, the first, only then, within its first 64 KB, since a program's first line is its prompt), so a session the user also had in a terminal, either way round, is listed, and so is one whose entrypoints are not within those bytes. Listed as before is everything Persona has a hand in: whatever was started under its state directory (its path and its resolved path: the assistant's, any manager's, a health space's, a guest's), a session the node keeps state for (none is left out while that state still loads), a fork, a kept session. The Agent SDKs' runs (`sdk-ts`, `sdk-py`) are listed: interactive hosts such as Paseo run the user's sessions through them. `CLAUDE_CODE_ENTRYPOINT` passes to child processes (`node/agentenv.go`), so a `claude -p` run from an interactive session's shell says `cli` and is listed. Capabilities: read, and send when the node can use the inbox (protocol 1, a path this platform dials, and on Windows a key file); otherwise `read_only_reason` says why. Interrupt while it works or waits (status busy or waiting), through the inbox ("Stop and answers from the phone" below, #5961). Approve only while it waits on a question it asked and the node can use its inbox: that question is a card ("Question cards outside Persona's terminals" below). Never terminal or attach, and never approve otherwise: a permission prompt, a plan to approve or any dialog other than its own question is answered on the computer (no hook of Persona's runs in such a claude, and its screen is not Persona's to read). Its permission mode, effort and model change there too: the inbox carries messages only and a message never gives the session a pane, so the row says `capabilities.modes: false` and a `POST …/modes` that arrives anyway is refused with `modes_on_computer`, not "not running, send a message first" (#1843; `node/modes.go` `noPaneForModes`).

### The peer inbox (read from Claude Code 2.1.308; 2.1.291 the same)

- **Where**: Windows, a named pipe `\\.\pipe\LOCAL\cc-msg-<32 hex>`; macOS and Linux, a unix socket `$XDG_RUNTIME_DIR/cc-socks/<pid>.sock`, or `/tmp/cc-socks-<uid>/<pid>.sock` when that path is too long. Only an interactive claude with cross-session messaging on opens one (a server-side switch, on by default on Windows; `CLAUDE_CODE_HARBOR_KITE=0` turns it off).
- **Frames**: one JSON object per line (at most 1 MiB), sender to inbox only. The inbox never writes back on the connection. A connection that sends no complete line in 30 s is closed.
- **Auth**: the first line `{"type":"auth","token":"<peerToken>"}`. Required on Windows (any other first line drops the connection); optional elsewhere. The token is in `<config dir>/sessions/<pid>.<sha256 of the canonical socket path>.key`, a JSON file only the user can read: `{"peerToken": <32 hex>, "procStartFt" | "procStart", "pidDomain"}`. The canonical path of a pipe is `\\.\pipe\` plus its name in lower case (with `LOCAL\` when it has one); of a socket, its absolute path. A second token (`CLAUDE_CODE_MESSAGING_TOKEN`) is given to the claude's own children; it marks a message as the session's own ("self-sent").
- **A message**: `{"type":"user","message":{"role":"user","content":"…"},"msgV":1,"msg_id":"<uuid>","priority":"next","session_id":"<id>"}`. `priority` next (the default) queues it; now runs it before what is queued; later after. `session_id`, when set and not the session's, drops the line (the claude moved on to another session). `from` is the sender's own inbox (`uds:<path>`) for receipts; a sender with no inbox leaves it out. Other types: `control` (`rename`, `peer_message_status` receipts, `notify_when_idle` / `peer_idle_notice`, `yield_artifact_replies` / `artifact_replies_yielded`), all addressed to a sender's inbox.
- **What the session does with it**: the content goes to the model after Claude Code's own line "Another Claude session sent a message:" and a warning that a peer is not the user. A Claude sender puts the body in `<cross-session-message from=… from-name=… from-mode=…>`; the session takes the sender's name and the body from that envelope into the transcript line's `origin` (`{kind: "peer", name, body, msg_id}`), and a message without one is written with neither. Then one of: **taken** (a `queue-operation` enqueue, then the hidden user line that starts a turn when the session rests, or a `queued_command` attachment absorbed at the next tool boundary while it works; a dialog on screen does not stop it); **held** for the person at the computer to approve (2.1.326: the sender states its permission-mode class in `from-mode`, `bypass` or `prompting`, and the session puts itself in one too, bypass in `bypassPermissions` or in plan mode entered with bypass available, prompting otherwise; a stated class other than the session's is held as `mode-mismatch`, and a bypass session holds a message that states none, `no-mode-asserted`; Claude Code's `crossSessionInbound` setting can say accept, hold or refuse instead, and a hold shows "Held peer message … Review it below" in the terminal); **refused** (that setting, or the switch off); **dropped** (a per-sender rate limit of 30 at once refilling one every 2 s, the same body as the last within 30 s, a full queue of 50). Receipts (held, denied, expired, delivered, refused, dropped) go only to a sender's `from` inbox. Slash commands are not run (`skipSlashCommands`).
- **`notify_idle`** means the inbox takes `notify_when_idle` (tell the sender's inbox once this session is idle); **`artifact_yield`**, handing artifact comment replies between sessions. Neither is used by the node.

### Sending from the phone

`sendOutside`: when no pane runs the session and a claude outside Persona's terminals has it open with an inbox, a message goes there, never as a second claude. Text is `prompts/agents/peer_message` (it tells the model the words are the user's own from the phone, to be answered in this session, not with SendMessage, that a choice is asked with AskUserQuestion as usual, since the phone answers that question as a card ("Question cards outside Persona's terminals" below), and that plan approvals, permission prompts and other dialogs are answered only at the computer) inside Claude Code's envelope signed `from-name="Persona"`, without `from`, and with `from-mode` for the owner's own message, card answer or Stop, or for a source-attested Agent message (`peerAttest`, #9607). That message (`owner_input`, which only the hub's check of a full-role device's own typed message sets, dropped from any caller in any letter case, and honoured by the node only as that exact key, once) goes in as the session's own user's words. Its class comes from receiver evidence read at the send: a readable external tmux footer wins because shift+tab appears there before the transcript, and a visible composer without a bypass or plan indicator is prompting even when a draft or dialog hides the idle hint (question and permission dialogs are not composers and keep transcript evidence); otherwise the newest transcript copy in the live claude's own config directory supplies the mode. If the live process started after that transcript mode was recorded, an explicit `--permission-mode` on its command line supplies the newer mode instead. `bypassPermissions` is bypass; plan is bypass only when the live process's arguments make bypass available (`claudeCanBypass`, read on macOS, Linux and Windows); prompting modes state none, and plan without that evidence or an unknown mode states none. That is the one authorization: the person who owns the session is the one its prompts and held messages wait on, and what they type at its terminal goes in whatever its mode; unreadable or stale evidence can only make the session hold the message. For card answers and Stop, the hub checks the same full-role device credential and the node uses the same live-receiver check (#9645). The devices manager uses separate owner-control RPCs on supporting nodes so an old manager forwarding extra parameters cannot accidentally attest a caller. Agent messages may instead state the source class through `source_permissions_v1` (below). Messages without reliable source evidence, and answers or stops without verified owner provenance, state no class, so a bypass session holds them for approval at the computer. Pictures go as paths in `<attached_images>` as with a pane; the inbox is not a paste, so the paths stay text (no `[Image #n]`, no `keepImagePaths`) and the model reads the files itself. A slash command is refused (`peer_command`): the inbox would hand `/exit` to the model as words. The send counts once the transcript shows the session took it (a `queue-operation` or a peer line with the words, within 6 s); one the inbox read but the session did not take (held, dropped) fails as `peer_unconfirmed` with `delivered: unknown`, so the app neither marks it sent nor resends it; when the session wrote that it holds a message of Persona's for approval (its transcript's `Held peer message … (peer claims name: Persona)` notice since the send, `peerHeldSeen`, #9607) it is `peer_held` instead, still `delivered: unknown`: held for approval at the computer, which the apps say in those words ("Waiting for approval on the computer") rather than only "not sure it arrived" (#8975). One taken while the claude's own status says `waiting` (a permission prompt or another dialog holds it, listed `blocked`) sits in its queue until that is answered at the computer: the send still succeeds, with `waits_on_computer: true`, and the iOS and web apps say the Agent reads it after the question on the computer is answered (#5890). A reply to a question the session asked is no such message: it goes at priority now and is the answer ("Stop and answers from the phone" below, #5961), with no `waits_on_computer`. Refusals with nothing sent: `peer_unsupported` (unknown protocol, a path this platform does not dial), `peer_unreachable` (no inbox there, no key), `peer_too_long`. A claude with neither a pane nor an inbox at all is refused as before (`open_elsewhere`). The hub words each code in the app's language (`rpc-errors.peer_*`).

### Source permissions on Agent messages

For `agent_send`, task planning/review/retry and conversation sends, the source node records the calling process's permission class while handling the MCP call. A managed Claude process uses its acknowledged mode; an in-flight mode switch gives no evidence. An external Claude uses its own config directory's latest transcript mode. The socket caller must descend from that live process; a nested Agent cannot claim its more privileged parent's session. The observation includes the process generation, source session, timestamp and a digest of the tool call. macOS uses the kernel process creation time and a registry file written after it; Linux and Windows compare the registry's process start with the kernel. Unknown modes (including plan), missing transcripts, unverifiable generations and unsupported runtimes state none.

The assistant carries that observation with the operation; it never looks up a cached session row's mode to grant a class. Worker reports retain their original observation through delayed digest delivery and retries. A digest can state a class only when every included signal has evidence in that same class. Automatic completion judgments, recovered historical turns and other manager-generated signals have no originating process permission evidence and state no class. A worker's earlier report cannot grant a class to a later judge's words. An older peer also leaves the class unknown.

The devices manager removes caller-supplied `source_permission` and `from_mode` in every letter case and rebuilds source evidence only from process-local provenance. It uses the reserved `session.source_send` RPC only on nodes announcing `source_permissions_v1`; older nodes receive ordinary `session.send` with no source fields. Ordinary sends ignore claimed source fields even when an older manager forwards them. Raw caller requests cannot select the reserved RPC on an updated manager. Evidence lost through an older intermediary is not reconstructed from text or session lists. This path keeps the Agent text inside the peer envelope without the owner-only phone-message preface. It never marks Agent text as owner input and never uses the owner's plugin delivery path. Claude Code still applies its own cross-session policy, including holding a class mismatch, and the existing unconfirmed-delivery escalation and backoff remain in force.

The session writes the message as a peer's: the transcript reader takes the user's words out of the envelope and the prompt (`prompts.PeerMessageBody`, which also strips the heads earlier versions of the prompt put there, kept in `olderPeerMessageHeads` for old transcripts and an older node during a rolling upgrade), shows them from "Persona", and the echo ties that line to the phone's `client_msg_id` (a peer line signed Persona is the only peer line that can be the user's send). It does not count as the user's prompt for `LastPromptAt`. In bypass permissions mode (what the desktop app's sessions often run in) a message that states no class or the other one is held: the person at the computer approves it in the terminal (or sets `"crossSessionInbound": "accept"` in that Claude Code's settings), and until then the phone says it is waiting there. A supervision message to a task leader the node reports as maybe delivered (`delivered: unknown`) is not sent again by the next digest, and not counted as told either (`sendToLeader`, #9607): the conversation above the leader hears once that the leader may not have it, the message goes to the leader again only after a growing wait, and the turn stays untold with its deduplication released, so a restarted manager can still deliver it; a retry the leader takes records its deduplication and marks its turn told, so a restart after it does not deliver it again. Before, each digest added another held copy.

Measured end to end on a throwaway `claude` 2.1.291 in an empty directory (its own `CLAUDE_CONFIG_DIR`, a pseudo console): with the default permission mode the message was taken in 0.15–0.3 s (also while a startup dialog was on screen) and answered in the transcript; a second message while it worked was queued and answered next; in bypass permissions mode the message was held, as above. The desktop app's sessions were not written to.

### Queue controls (#12789)

A message that comes while such a session works waits in Claude Code's own queue, which its transcript reports and the
apps list. Editing that queue and Claude Code's send-now are keys on the session's screen (`queue.go`). So a session with
no pane (the desktop app's, a background one) lists `queue_edit` and `queue_send_now` as false, and the apps offer
neither. A call that comes anyway is refused with 409 `queue_edit_unsupported` or `send_now_unsupported` and nothing
pressed; the messages go in order at its next step. A session in a tmux pane lists both as true and takes them as a
Claude in an engine pane does.

### Owner input in a tmux pane (#5965)

An outside Claude in tmux already has a terminal the node can read and type into (`outsidePane`, `hosttmux.go`). A normal
`session.send` with the verified `owner_input` mark therefore leaves `sendOutside` and uses the ordinary pane pipeline:
the same per-session typing lock, draft and dialog checks, image handling, paste and Enter, stuck-prompt recovery and
transcript confirmation as a Claude in an engine pane. A successful send answers `delivered_as: "user_input"`; it does
not also go to the plugin or inbox. A draft, a dialog or a pane whose screen cannot be read refuses it with nothing typed,
and an unconfirmed submission is not reported as delivered. Slash commands keep their `peer_command` refusal. This route
needs no peer inbox.

Only that verified mark authorizes typing. An unmarked message, a caller's casing variant the devices manager would have
stripped, supervision and other senders keep the peer inbox; without a usable inbox they are refused with nothing typed.
The tmux row lists `send` and `terminal` with or without an inbox and has no inbox-only read-only reason. It does not
advertise `plugin_input`: the pane, not the plugin, carries the owner's message. A readable dialog on the pane supplies
its attention reason as in #7900. Sessions with no tmux pane keep the plugin and inbox behavior below.

### Owner input through the Persona plugin

`integrations/claude-code-plugin/` (its `README.md` is the contract), `server/internal/node/ccplugin.go`, `server/internal/devices/owner_input.go`.
The inbox can never carry the owner's go-ahead: Claude Code tells the model a peer message is not the user's and cannot
grant consent, so "yes, merge it" from the phone was refused (#5965). Claude Code's plugin API has a supported way in:
`$.prompt.submit({ text, asUser: true })` submits text as the person's own words, without the "The <plugin> plugin sent a
message" frame. A user-scope plugin installed from a terminal also loads in the desktop app's local sessions. For an
outside Claude with no tmux pane, Persona's plugin (`persona`) uses it for the owner's own messages; Persona Link
installs and updates it.

- **Measured** (2026-11-16, Windows 11; Claude Code 2.1.291, 2.1.299 and 2.1.308, the last the desktop app's own; a
  throwaway `claude -p --input-format stream-json` session per run in an empty directory whose `CLAUDE.md` allowed a
  file to be created only on the person's own approval). The same "I approve: please create APPROVED.txt now.": through
  the real peer inbox, wrapped in `prompts/agents/peer_message`, it was refused ("came from another session, not the
  person's own approval"); submitted by a plugin without `asUser` (read as "The spikeplug plugin sent a message: …") it
  was refused; submitted with `asUser` the file was created ("the user explicitly approves in their own message"), on
  all three versions. In bypass permissions mode (2.1.308), where the inbox holds Persona's message for approval at
  the computer, the `asUser` prompt was not held: queued at 05:02:22.8, it started its turn at 23.3, as soon as the
  turn before it ended. The transcript line is a plain user line: `{"type":"user","message":{"role":"user","content":
  "<text>"},"origin":{"kind":"plugin","name":"<plugin>","asUser":true},"promptSource":"system"}`, no `isMeta`, after a
  `queue-operation` enqueue with the text. A prompt submitted while a turn runs waits in Claude Code's queue and starts
  its own turn once the session rests (it is never folded into the running turn), and the submit resolves as that turn
  starts. A plugin's `$.http.fetch` gives up after 30 s; over a Unix socket on Windows its first connection attempts can
  fail while the listener is new, so the plugin retries. The process at the socket's other end is the claude itself
  (the pid of its `sessions/<pid>.json`), on Windows read with `SIO_AF_UNIX_GETPEERPID`.
- **Transport and who may take a message**: the plugin talks HTTP to the node socket (`node.sock`, the one hooks use;
  the node tells HTTP from its JSON requests by the first line): `POST /claude-plugin/v1/poll` (held up to 25 s) and
  `/ack`. The node answers only the claude that has the session open: the socket's peer pid (`SO_PEERCRED`,
  `LOCAL_PEERPID`, `SIO_AF_UNIX_GETPEERPID`) must be the pid whose `sessions/<pid>.json` names the session the request
  names; anything else gets 403, and where the system cannot say, nothing is handed over. Nothing that arrives on the
  socket becomes a message: only `session.send` makes one. The boundary is the node socket's own (0600, the same user;
  `docs/integrations/script-cli.md` "Security and privacy"): a process of that user could already type into the user's terminals.
  Polls from a claude the node does not deliver to this way (an engine pane's, the assistant's own, an SDK's) are told
  to come back in 5 minutes. `{"kind":"claude-plugin.status"}` on the same socket lists the sessions whose plugin polls
  (for Persona Link's installer and diagnostics).
- **Which messages**: only the owner's own. The hub marks a message typed in the owner's app (`captureHumanSubmission`:
  a full-role device token, not a link or answer-only token, not the master token, not a member hub), and the devices
  manager turns that mark into `owner_input: true` on `session.send` (`ownerInputParams`), dropping any `owner_input`
  a caller put in the params. Messages from the assistant, extensions, channels (the Lark and WeChat bots), relays
  between conversations and the auth relay have no mark and keep the inbox, as do non-owner Stop requests and the
  answers to a question the session asked (both at priority `now`), and slash commands (still refused, `peer_command`).
- **When**: when the plugin advertises `busy_input` (0.2.0+), owner messages go through the plugin at rest, while a
  turn runs, and while the session waits on a dialog other than a question (a permission prompt, a plan to approve):
  `$.prompt.submit` writes `queue-operation: enqueue` immediately, without blocking the poll loop or being held for
  computer approval in `bypassPermissions` / `auto` mode, and the prompt runs as the person's words when the turn ends
  (`session.send` reports `queued: true`, and `waits_on_computer: true` behind a dialog). A session waiting on an
  `AskUserQuestion` still takes the peer inbox at priority `now` so the question ends instead of blocking the queue.
  Older plugins without `busy_input` receive owner messages only at rest; while the session works or waits on a dialog
  the message falls back to the inbox with `relay_reason: "plugin_outdated"`, and the web app says to reopen the
  session on the computer once (below). `relay_reason: "busy"` is left for a status the plugin does not deliver in.
- **Measured** (2026-11-22, Windows 11, Claude Code 2.1.329, throwaway `claude -p --input-format stream-json` sessions
  with a throwaway plugin from `--plugin-dir`): in `bypassPermissions` mode with a turn running a long command, two
  `asUser` submits wrote their `queue-operation` enqueues within 2 ms, were not held, waited through every tool
  boundary of that turn and ran together as one user turn once it ended; the submit promise settled only when that turn
  started. With `--permission-prompt-tool stdio` and a permission request left unanswered (`status: "waiting"`,
  `waitingFor: "permission prompt"`), an `asUser` submit was enqueued at once and ran when the turn ended. A
  `claude plugin update` of an installed plugin answers "Restart to apply changes": a running session kept the old
  plugin's code on its next turn, and a plugin's own `$.command.run({ command: "reload-plugins" })` is refused in an SDK
  session such as the desktop app's ("/reload-plugins isn't available over a remote connection"). So a session keeps
  the plugin version it started with until it is opened again.
- **Why the inbox is not used for the owner's words where the plugin can take them**: the inbox's outcomes reach only a
  sender with an inbox of its own (receipts go to its `from`), which the node is not. A message the inbox read can be
  held (the transcript then has Claude Code's "Held peer message" notice, `peer_held`), or refused or dropped with no
  trace in the transcript (Claude Code 2.1.330's ingress guard drops for a rate limit, a repeated body, a full queue or
  a hop loop, and its receive gate refuses when cross-session messaging is switched off). One 2026-11-21 case: a desktop
  session in bypass mode working through a 20-minute turn took twelve owner messages through the inbox in a row (each
  `queue-operation` enqueue, absorbed at a tool boundary) and then wrote nothing at all for the next, with no hold
  notice: `peer_unconfirmed`, and the message never arrived. Its plugin was 0.1.0, so it had no other way in.
- **Owner Stop**: when the plugin advertises `interrupt` (0.2.0+), an owner-attested `session.interrupt` cancels the
  running main-loop turn via `$.turn.abort({ turnId })` (falling back to the peer inbox when no plugin turn is active or
  for non-owner interrupts), and `capabilities.interrupt` stays true while a turn runs and the plugin is reachable.
- **Session settings (`settings`, 0.2.0+)**: when the plugin advertises `settings` on `/poll`, `session.model` and
  `session.modes` (model / effort) hand session-scoped overrides over `/poll` and wait for `/ack`; the plugin applies
  them in memory on main-loop `turn.step` calls without modifying global Claude settings files.
- **Delivery and fallback**: the node hands the message to the session's waiting poll (`deadline_ms` 5 s ahead; the
  plugin submits nothing later) and counts it in once the transcript shows the plugin's prompt or its queue entry, or
  the plugin says it submitted it (its submit resolves as the turn starts). No poll takes it within 3 s, the plugin says
  it did not submit it, or nothing shows within 10 s: the message goes through the inbox instead, as above.
  For 3 minutes after the node starts, a session whose claude can have the plugin (it is installed in that claude's
  config directory) and has not polled yet gets up to 10 s for its first poll before the message is relayed as
  `no_plugin`: a node that restarted (an update) knows no plugin until each polls again, a few seconds later.
  The 250 ms settle window only frees the plugin's poll loop for interrupts and more prompts; it sends no final ack.
  The submit continuation reports the actual outcome later, so a delayed rejection or `drop` cannot be preceded by a
  timer-generated success. While it is pending, the transcript's queue/prompt line can confirm it as above.
  `session.send` answers `delivered_as: "user_input"` or `"relayed"` with `relay_reason` (`no_plugin`, `busy`,
  `plugin_outdated`, `plugin_unavailable`, `plugin_refused`, `plugin_unconfirmed`), only for messages that carried
  `owner_input` to such a session; a relayed message that ends in an error (`peer_unconfirmed`, `peer_held`) carries
  both fields in the error's data as well. A relayed message meets the inbox's own outcomes too: one a bypass-mode
  session holds is `peer_held`, as before. Pictures go as paths in `<attached_images>`, as through the inbox.
- **What the apps show**: a no-pane session's row says `capabilities.plugin_input: true` while its plugin polls. The message
  shows in the chat as the user's own line (the transcript's plain user line); one relayed shows from "Persona" as
  before. The web app says so when an owner's message to a session with the plugin was relayed (`lib-outbox.relayed_busy`,
  `lib-outbox.relayed_plugin`): the Agent may not take it as their approval. Nothing is said when it went in as their
  input, nor for a session without the plugin (it never had another way). A session whose plugin is from before busy
  delivery gets its own words (`lib-outbox.relayed_old_plugin` when the inbox took it,
  `lib-outbox.peer_unconfirmed_old_plugin` when it did not): reopen the session on the computer once and its messages
  go in as the person's input from then on. iOS and Android show the chat line as it is and ignore the new fields for
  now.
- **The plugin** submits text, owner turn interrupts and session settings: no command, no file but its own manifest,
  nothing written. It refuses a slash command, an empty or overlong text and a message past its deadline, tells the node
  why (`ack`), and keeps quiet (the debug log only) while no node answers, backing off up to a minute.
- **Managed settings can keep the classic hooks away**: on a computer whose device management installs a Claude Code
  `managed-settings.json`, the built-in `cc-plugin-sec-default@builtin` guard can seat itself in the `prepend` tier and
  pass every `classic.*` event over user-tier plugins to the `append` tier. Persona cannot and does not work around that
  organization policy. After a main-loop turn uses tools but no `classic.PreToolUse` reaches the plugin, the plugin adds
  `classic_passed_over: true` to `/poll`; the node stops authorizing plugin permissions for that session, refuses a
  direct `/permission` call with `passthrough`, withdraws any active plugin permission card, and does not set
  `capabilities.approve` from the plugin alone. A blocked row says the request must be approved on the computer or the
  session started from Persona in a terminal engine (herdr, Orca, and the rest) to approve from the phone, in `en`,
  `zh-Hans` and `ja`. Question cards (`clq_`) are unchanged because they never depended on the classic hook. `turn.*`,
  `session.start` and command hooks below the guard are unaffected. The closest upstream reports are
  [anthropics/claude-code#105457](https://github.com/anthropics/claude-code/issues/105457),
  [anthropics/claude-code#111404](https://github.com/anthropics/claude-code/issues/111404) and
  [anthropics/claude-code#109411](https://github.com/anthropics/claude-code/issues/109411). This deliberately does not
  use `tool.check` to lift permission prompts.
- **Not covered**: a desktop session the node opens to send to (`sendThroughDesktopApp`) has usually not polled yet, so
  its first message goes through the inbox. A claude with no inbox does not use the plugin either: there would be no
  fallback. Public sharing omits the plugin's lines, as any line without the human origin (`transcript.shareHuman`).
  The plugin API is early access and may change; an owner Stop uses `$.turn.abort` when a plugin turn is active and
  otherwise falls back to the priority-`now` inbox stop described below. How the desktop app draws the plugin's prompt
  was not checked.

### Stop and answers from the phone (#5961)

`server/internal/node/claudebg.go` (`interruptOutside`, `sendOutside`). Measured with Claude Code 2.1.321, the desktop app's own, on macOS (2026-11-12/13). The inbox's priority `now` is Claude Code's own "send now": its queue aborts the running turn for a command of priority now, and the message runs at once. It also ends a question or a permission prompt the session waits on: their tool call ends "Tool permission request aborted".

- **Stop**: `session.interrupt` on such a session first uses the plugin's `$.turn.abort` for an owner-attested request
  when the plugin advertises `interrupt` and has an active main-loop turn. Otherwise it sends `prompts/agents/peer_stop`
  (the user pressed Stop: stop, start nothing else, say in one sentence where you stopped, in the conversation's
  language) through the inbox at priority `now`, confirmed as a message is.
  - Measured: a reply being written was cut mid-way, and the one-line answer came 2.6 s after Stop. On a permission prompt, the prompt was declined and the answer came 2.7 s after.
  - `capabilities.interrupt` is true while the session is busy or waiting.
  - At rest nothing is sent and the call succeeds: a Stop from a list a moment old.
  - A claude without an inbox is refused (`peer_unsupported`).
  - A claude in a tmux pane gets Esc in its pane as before.
  - The transcript reader shows the stop's line as `transcript.StoppedFromPhone` from "Persona", which the hub words in the reader's language (`hub-transcript_text.stopped_from_phone`), not as the words sent to the model. That line starts the turn the answer is in.
- **Answering its question**: while the session waits on a question it asked, a message goes at priority `now`. Queued at `next`, it would wait behind the question until someone answered it on the computer; at `now` the question ends unanswered and the model takes the message as the answer (measured: answered 1.5 s later).
  - "Waits on a question": its status file says `waiting`, and its transcript's latest call is AskUserQuestion. The send reads the transcript as it is at that moment, not as the list read it up to `metaMinInterval` before.
  - Its row's `attention_reason` is `proto.AttentionClaudeAnswer` (the hub words it as "answer it on its card, or reply to answer"): the question is also a card, below.
  - On a permission prompt, a plan to approve or another dialog, a message still goes at `next` and waits for the computer. At `now` it would decline what only the computer should approve. The row keeps `proto.AttentionClaudeComputer`.
  - A claude in a tmux pane is the exception: the phone reads and answers the dialog on the pane's screen, so its row's `attention_reason` is that dialog's first line (read from the pane, as for a resting claude), not `proto.AttentionClaudeComputer`. While no dialog is read from the pane (the first read comes after `paneDialogEvery`, or the screen shows one the reader does not recognize), the row keeps the computer-only sentence.
- **The queue**: a phone message queued behind a running turn (a `queue-operation` with the envelope) shows the user's words, from Persona, without what Persona told the model (`prompts.PeerMessageBody`).

### Question cards outside Persona's terminals

`server/internal/node/claudeoutside_ask.go`. No hook of Persona's runs in such a claude, so its AskUserQuestion made no card: the apps showed only the row's line that a reply answers it, and the questions and their options (often several questions at once) were nowhere to be seen.

- **When**: the claude's status file says `waiting`, its transcript's latest tool call is an AskUserQuestion with no `tool_result` yet, it runs in no tmux pane (that dialog is read off the pane's screen), and the node can use its inbox (`capabilities.send`). A claude whose inbox the node cannot use gets no card: nothing could deliver the answer, and its row says to answer on the computer (`proto.AttentionClaudeComputer`).
- **The card**: `kind: "question"` with every question of the call (`question`, `header`, `multi_select`, `options` with labels and descriptions) read from the transcript's `tool_use` input, `tool_name: "AskUserQuestion"`, `agent: "claude"`, id `clq_` + a hash of the session and the call. No `can_cancel`: a Skip would have nothing to send. A call with two alike-worded questions gets no card (answers are keyed by the question's text); a reply answers it as before. The row says `capabilities.approve: true` while the card is up, so the apps let it be answered.
- **How it reaches the hub**: in the same list as the Codex cards (`codex.interactions`, `node/codexask.go` `syncCodexCards`), so the hub keeps it as it keeps those: announced once, ended when the node stops listing it, a hand-back (`dismiss`) keeping it off the phone while the claude still asks it.
- **The answer**: `codex.answer` with a `clq_` id goes to `outsideAnswer`. The picks (a multi-select's labels joined, an "Other" answer as typed) become `prompts/agents/answer` ("my answers to your questions", one line per question in the card's order; the assistant's answers say so, with why), sent through the inbox like any message. The session waits on its own question, so it goes at priority `now`: Claude Code ends the question (its tool call ends "Tool permission request aborted") and the model takes the message as the answers. The card is held off the list for a moment while the transcript catches up. A card whose question is no longer open (answered at the computer, the turn stopped) is refused as `prompt_changed` with nothing sent; a send the session did not take reports what the inbox said (`peer_unconfirmed` and the rest, above) and the hub shows the card again.
- **What the model is told**: `prompts/agents/peer_message` asks it to put a choice to the user with AskUserQuestion as usual (#8558). Every session that text reaches has an inbox the node could use, which is what a card needs, and a claude in a tmux pane is answered on its screen instead. A call with two alike-worded questions still gets no card; a reply answers it. Before, the prompt asked for numbered options in plain text and no dialog (#5887), which models often ignored.
- **Not covered**: the session's own dialog on the computer is not changed: it disappears only when the answer ends it. In bypass permissions mode the desktop app holds Persona's message for approval at the computer (above), so the answer waits there as any message does.

### The desktop app's sidebar (#3304)

The desktop app keeps an index of its Claude Code sessions, one JSON per session: `~/Library/Application Support/Claude/claude-code-sessions/<…>/<…>/local_<id>.json` on macOS (`Claude-3p` for the build on a third-party provider). On Windows, search `Claude`, `Claude-3p` and `Claude-Data` under local and roaming AppData, including Store layouts under `Packages/Claude_*/LocalCache/{Local,Roaming}`. Redirected `LOCALAPPDATA` and `APPDATA` are used only when scanning the current user's home. The Windows Store build measured on November 15, 2026 uses `%LOCALAPPDATA%\Claude` rather than its package folder; the additional layouts cover other installations. Each entry names the transcript the session runs (`cliSessionId`), whether the session is archived (`isArchived`), and the transcripts it ran before (`priorCliSessionIds`: it went on in a new one). Its sidebar lists each unarchived session once, a side session nested under its parent.

Existing saved environments are read from a dedicated metadata mirror (`node/claude_desktop_envs.go`): account-scoped presets provide an ID, name and local root folders. Identity includes the desktop data profile and account. Only local absolute folders are accepted; WSL, SSH and cloud targets are excluded. Additional roots appear as separate workspace rows under the same environment identity, including environments with no sessions. A session's `startedFromEnvironmentId` selects that saved name and identity exactly; a missing or unmatched reference falls back to the entry's `originCwd`, then `cwd`, after physical-path and Git-root normalization (`node/desktop_workspaces.go`). Live and offline sessions use the same rule. Windows path comparison normalizes case and separators.

The offline list follows the sidebar (`node/claudedesktop.go`), before a row is counted:
- it leaves out the transcripts of archived sessions (on the Mac measured, every finished /btw question was an archived session) and their earlier transcripts;
- it leaves out an unarchived session's earlier transcript once the list has shown the one it runs now (newest first, so normally the current one comes first; one written after the current one goes when the node puts its whole list together, "One row per session" below). When the current one is not listed, the earlier one stays: at worst a session shows twice, never not at all;
- it leaves out a session deleted in the app (#8314): Delete removes the entry and leaves the transcript, so a transcript whose entrypoint is the desktop app's (`claude-desktop…`) that no entry names, now or as an earlier transcript, is a deleted session's (`claudeDesktopIndex.gone`). Only when the index has a readable entry (no index, or another layout: nothing is left out), never for another client's transcript (the CLI's, an editor's), and not for a transcript written in the last 2 minutes (`desktopEntryGrace`: the app may not have written a new session's entry yet). Delete also writes `deleted_<uuid>` markers beside the entries, one for the session's own id and one for each transcript it ran, each holding the time of the delete in ms (measured 2026-11-15 on Claude-3p: 51 of 57 deletes wrote more than one). A transcript a marker names leaves the list at once, without those 2 minutes, after a restart too (`deletedMarkers`); a delete whose markers name no transcript still waits them out, and a transcript an entry names is listed whatever a marker says. On one Mac (2026-11-15) 26 of the 53 desktop rows were such sessions, among them every session another session had started and deleted once its work was done.

It also lists the transcript every unarchived session of the index runs, however long it has been quiet and however little it says yet, as the sidebar does. Top-level sessions are counted on their own, recent or not: the newest 1000 (a guard against a runaway index, far above a real sidebar; it was 100, and a sidebar of 200 sessions lost the ones quiet for a week, #15522), beyond the usual 7 days, taking none of the 80 places of the other sessions. A session drawn under another row (a side session, a fork, a scheduled task's earlier run: the index gives it a parent, below) is counted apart from those 1000: the newest 300 of them (`maxDesktopFolded`, #15279). Before, folded and top-level sessions shared one count, and a daily scheduled task's runs pushed the top-level sessions out. One whose transcript has no title or directory yet (opened in the app with nothing said, or just gone on in a new transcript) shows the entry's `title` and `cwd`. Another platform's runs (`agentfilters.go`) stay out even when the index names them; a program's one-shot run (`claude -p`) does not: a session New Agent starts in the app is written that way and says only `sdk-cli` until the app ends a turn in it. Otherwise only what an entry says is acted on: another client's transcript no entry names hides nothing, and an entry that cannot be read (or is over 4 MB) keeps every session id it mentions in its first 64 KB (`mentionedIDs`), none of which is taken for deleted. A kept session and Persona's own forks are listed as before, and running sessions are untouched (their questions and permission prompts must stay reachable). A session archived in the app and later resumed elsewhere under the same transcript leaves the offline list once it stops, as the sidebar does; unarchiving it in the app lists it again. Unarchived side sessions and /btw questions are rows of their own here, drawn under the session that started them (below). Discovery reads the session index and dedicated environment metadata, never the app's credential-bearing config; session-index files are parsed again only when changed. macOS's and Windows' layouts are known (`desktopDataDirs`); before, the index was read on macOS only, so on Windows archived sessions stayed listed and side sessions were unrelated rows. Tests: `node/claudedesktop_test.go`, `node/claude_desktop_envs_test.go`, `node/desktop_workspaces_test.go`.

**One row per session** (#14394). One session can have several entries: the app keeps copies of its `local_<id>.json` under its other account and organization folders, each with the `cliSessionId` and `isArchived` it had when it was written (measured 2026-11-23 on one Mac: 5 of 6 sessions had 4 or 5 copies). A copy written before the session went on in a new transcript still names the old one, unarchived. Read entry by entry, that listed the old transcript as a session of its own, beside the new one, with the same title and project; a session archived in the app stayed listed while a copy still said unarchived; and which transcript a message went to could follow either copy. The index now reads each session once, by its `sessionId` (`desktopSessions`): its transcript and archived flag are those of the copy whose `cliSessionId` no copy names as an earlier one, the most recently written of those. Every other transcript its copies name is one it ran before: a stale copy's `cliSessionId`, `priorCliSessionIds`, and `lastAssistantCliSessionId` when it differs (until the new transcript has an answer, it names the one the session went on from). A transcript some session runs now is that session's, whatever another one ran before. When the node puts its list together it folds those earlier transcripts into the row of the one the session runs now, live or at rest (`foldDesktopEarlier`): once that row is listed, whatever order the transcripts were written in, and at rest also for good once only archived sessions ran them, as the offline rules above say (a claude of the app still running one is not judged by the archive); before, the live list folded nothing. Left as they are: a row in a pane or of another source (a terminal's claude that took up an earlier transcript runs a continuation of its own), a kept session, Persona's branches, a row Persona is starting, and one waiting on the user, whose question or permission prompt stays reachable until it is answered or the claude stops. The row of the current transcript names the earlier ones no row lists in `aliases` (newest first, at most 128), so the manager shows no archive record kept under an old id (`docs/protocol/protocol.md` "Agent session archives"). An old id is not moved to the current row anywhere else: a link or an open chat naming it shows that transcript, which stays on disk, with no row. A message that still reaches the node for it (one held from before, the assistant's tools) goes on in the session (`desktopWentOn`): it is sent as one to the transcript the session runs now, to its claude's inbox or by opening the session in the app, with the settings queued for the old id, never with `claude --resume` of the old transcript in a terminal beside the app's session. An archived session's old transcripts and one a claude has open are handled as before. Tests: `node/claudedesktop_copies_test.go`, `node/claudedesktop_wenton_test.go`.

**Side sessions under the session that started them** (#5935). A session another one started (a side chat, a /btw question, a session spawned for a task) names that one in `spawnedFrom.sessionId`, its `local_<id>`. A fork made with the app's Fork names the session it was forked from in `forkedFromSessionId` alone (beside `forkedAtMessageUuid`, where it was forked); a session split off with `start_session` writes both, naming the same session (#9376). The app's sidebar nests a session under `spawnedFrom.sessionId`, else `forkedFromSessionId`, and so does the list: the node maps that id to the parent session's current transcript and, when that session is unarchived and its transcript is a row of the same list, sets the side session's `parent_agent_id` to it (`linkSideSessions`, with the parents read beside the offline scan, every 20 seconds). The row stays a session of its own (no `parent_session_id`, no subagent); the manager folds it under its parent's topmost listed row as a side session (`folded_as: "side"`, #7944), and where the hub advertises `sessions_side_fold` the clients show it in that parent's chat rather than as a row (`docs/protocol/protocol.md` "Session hierarchy"). An entry with neither id (older apps wrote `spawnedFrom` as `{"btw":true,"sideSession":true}` only; with a `forkedFromSessionId` beside it, that one counts), whose ids are of another shape, whose parent is archived, not in the index or not listed, or whose parents loop, links nothing. Neither does one the user moved to the top of the sidebar (Detach to top level): the app writes `"lineageDetached": true` there and keeps the old `spawnedFrom`, so an entry with that flag is read as having no parent, whichever field names it and whoever detached it (`lineageDetachedBy` is not read; #8522). Its own side sessions and forks still nest under it. A fork is a side session here in every way: where the hub advertises `sessions_side_fold` it is reached from its parent's chat, not listed as a row. Tests: `node/claudedesktop_side_test.go`.

**A scheduled task's runs under its newest run** (#8314). Each run of a scheduled task the app runs is a session of its own in the index, naming the task in `scheduledTaskId` (measured 2026-11-15: a daily task left one unarchived session a day). The app's sidebar lists them under the task in a section of their own, apart from the projects; the list drew each as a row of its project. The node now links a task's unarchived runs as it links side sessions: the newest (`createdAt`, the larger transcript id on a tie) heads the task, and every other run names it in `parent_agent_id` (`readClaudeDesktopIndex`), so a client that folds side sessions shows one row per task. A run some session started keeps that session as its parent; a task with one run links nothing.

**Sessions in their project** (#8521). The app's sidebar groups its sessions by the folder each was started in (`originCwd`), a session in a worktree the app made (`<repo>/.claude/worktrees/<name>`, its `cwd`) under the repository it was made from. The node gives each of the app's sessions a space the same way (`placeClaudeDesktop`), the running ones (`claude_desktop`) and the transcripts it ran that nothing runs now (agent surface `desktop`, no source, no pane): the repository the session works in, a linked worktree's main checkout (its `repo_root`, read off the worktree's `.git` file with the list's other Git places, no git run), and, until that is read or for a folder that is no repository, the folder the app started it in; the home folder (or one above it) is no project, as in the apps' lists by app. `space_kind` is `claude_desktop`, `space_id` is `codex-project:<root>` (the Codex App's space for the same repository, so both apps' sessions of a repository are one project on that computer), `space_name` the root's folder name. In a folder a terminal workspace is rooted in (the herdr workspace Persona opened there for an Agent, say), they are in that workspace's space instead, with the Agents there (`docs/protocol/agent-states.md` ("One folder, one space (#13533)")). Before, these sessions had no space: every client's list by project put them under "No project", whatever repository they worked in. A session a host placed keeps its place.

### Unread when a turn ends unseen (#6477)

A desktop app session's finished turn is Unread on the phone, and its finished push follows the hub's usual rules, as for a Codex App chat or a Claude in Tern (`node/claudedesktop_unread.go`).
Before, its status file's `idle` was listed `idle`, which the hub takes as seen: no Unread, no push.

- **What the app keeps:** no read state on disk. Its index records when it last brought a session into view (`lastFocusedAt`) and when a turn of it last began or ended (`lastTurnEdgeAt`).
  - Comparing the two alone would call the session someone is watching unread: a session shown the whole time is not brought into view again when its turn ends.
  - The app's own rule for someone attending a session has three parts: its window is focused, the session is shown, and the person did something in the last 300 s.
- **The rule:** a resting session (status `idle` or `shell`) is listed `done` while its last turn edge is after it was last seen. It counts as seen when either:
  - the app brought it into view after the turn edge (`lastFocusedAt`), or
  - the node found someone attending it. That means the desktop app is the application in front (`lsappinfo front`), the person did something within 5 minutes (the HID system's idle time, `ioreg -c IOHIDSystem`), and the session is among those the app brought into view last (within 5 s of the latest: a split view shows two at once).
  The check runs only for a resting session whose turn is not yet seen, at most every 2 s. If either command fails, the node counts the session as seen, so no session becomes Unread and no push goes out on a guess.
- **Kept:** when the node found a session attended goes to `<state dir>/desktop-seen.json`, so a restart does not make a watched session's last turn Unread. Only sessions whose last turn edge is after `lastFocusedAt` are kept.
- **Seen on the phone or the web** is the hub's, as for any session. A turn that begins afterwards is judged anew when it ends.
- **Limits:** foreground-window and input-idle detection is implemented on macOS. Windows reads the desktop index too, but its attendance fallback treats a session among the most recently shown as attended; other sessions still use their focus and turn timestamps. Windows unread and completion-push behavior remains unverified. A session shown in a split pane counts as shown only while it was brought into view last, or within 5 s of the session that was. After the user brings another session into the other pane, a turn ending in this one while the user watches is Unread, and pushes after the hub's delay.
- Tests: `node/claudedesktop_unread_test.go`.

### Sending to a desktop session nothing runs (#4300)

`node/claudedesktop_open.go`. A message to a session the sidebar lists (an unarchived entry of the index) goes back to the app when no claude runs it. Before, it was resumed with `claude --resume` in a terminal tab, so the session ran outside the app, and on a machine without a terminal engine it was refused.

- **Opening it:** the node opens the session at the app's own link, `claude://claude.ai/epitaxy/<sessionId>`, with `/usr/bin/open -g` on macOS or the registered `claude://` handler through `ShellExecute` on Windows. The link uses the entry's `sessionId` (`local_<uuid>`), and only in that form. One app may keep several index directories and handles their links.
- **Observed on macOS:** the app starts that session's claude with its peer inbox without coming to the front or changing what its window shows. This takes about 3 seconds on a Mac (Claude Code 2.1.321, 2026-11-11). Windows foreground behavior remains unverified.
- **Sending:** once that claude runs and its inbox socket is there (up to 30 seconds), the message goes to the inbox as above, and the turn shows in the app.
  - If the app goes on in a new transcript as it opens the session, the node follows the one the entry names.
  - A claude seen without an inbox is given 3 seconds to name one.
  - In bypass permissions mode the message is held there like any message to the inbox (`crossSessionInbound`).
- **Refused, with nothing sent:**
  - Another claude has the session open (a terminal's without an inbox, a program's): `open_elsewhere`, as before, never a second claude.
  - The app does not open it in time: `desktop_open_failed`.
  - A slash command: `peer_command`. The inbox runs none, and resuming it in a terminal would take the session out of the app. `/exit` is handled as before.
  - The app's claude has no inbox (an older Claude Code): `peer_unsupported`, the session left open in the app.
- **The old way:** when the link cannot be opened at all (no app handles it), nothing was started, and the session is resumed as before. So is any session the index does not list.
- **macOS only** (`claudeDesktopOpens`): on Windows the index is read for the list alone; the app is never asked to open a session, and a send is handled as it was before the index was read there (a session nothing runs is a stopped agent).
- **Launcher and model:** the app resumes the session with its own settings. A launcher or model picked on the phone for resuming does not apply to it.
- Tests: `node/claudedesktop_open_test.go`.

### New sessions in the desktop app

The `claude-desktop` launcher is offered when the desktop session index and a usable Claude CLI are present. Windows support extends the upstream launcher from PR #5528, merged on November 13, 2026. It needs no terminal engine and runs on the desktop app's provider. Explicit model and effort choices are applied through Persona's plugin, while import preserves the supported permission-mode choice described below. Plan mode is refused, and terminal-only controls are hidden in **New agent**.

`launchers.list` identifies it with `source: "claude_desktop"` and `agent: "claude"`; a login-shell launcher with the same name takes precedence. The implementation is in `node/claudedesktop_new.go` (#5480). Run Agent Here and switching a running terminal session to this launcher are refused; a known desktop session can use the import recovery below.

Model configuration › Agents can run on it, so a person's New Agent opens in the app without picking it; computers without the app and the starts Persona makes itself use another launcher (`docs/assistant/model-config.md`, "Claude's desktop app as Agents' launcher (#13117)").

For a directory-based start, the node writes a new transcript with the local-only command `claude -p --session-id <new id> "/rename <title>"`, imports it with `claude://resume?session=<id>`, waits for the desktop index, then sends the first message through the owning session's peer inbox. The seed command makes no model request. A supplied folder must exist or be a new final child of an existing folder; leaving it blank uses the desktop temporary-session folder. Workflow board keeps its project title in Persona and uses the folder for session grouping. Importing a session does not create a native saved environment with that title or synchronize a native sidebar project name.

The start's permission mode is stamped on the transcript's user lines (`stampDesktopMode`, #9350). The app imports the mode of the last user line that names one, else its settings' default. It lowers imported `bypassPermissions` to `acceptEdits` but keeps `auto` unless device policy withholds it, so a start with no mode or `bypassPermissions` uses `auto`; `default`, `acceptEdits` and `auto` are kept as asked (`desktopStartMode`). Claude Code's local seed command writes no mode itself, whatever its command-line option. If stamping fails, import uses the app's own choice and the node logs the failure.

Explicit model and effort choices are validated and queued for Persona's Claude Code plugin, then applied when the imported session loads. With neither selected the app keeps its defaults. The launcher model catalog uses the configured model source rather than a locked, empty list; later model and effort controls depend on the loaded session's plugin capabilities. The session continues on the app's provider.

The app's new-session links only fill its composer, so they cannot replace this import route. The seed process removes inherited Agent variables and `CLAUDE_CONFIG_DIR` so the transcript is written where the app reads it; the local rename command needs no CLI model sign-in. The title uses the first line of the first message or `name_hint`, spaces folded and limited to 40 characters, falling back to the folder name. The node waits up to 15 seconds for the imported index entry. Without a folder, it uses `临时会话` beside the largest group of existing session folders, falling back to `~/Documents` or home. Relative paths, files and missing parents are refused. A first-message slash command (`peer_command`) and `secret_ticket` (`secret_env_agent`) are also refused before seeding.

iOS, the Mac app that shares its New Agent, and the web send no folder when none is picked for this launcher, browse from the folder holding the app's projects, and offer a new folder there. Android currently requires a picked folder. See `docs/apple/ios.md` ("New Agent in Claude's desktop app") and `docs/web/web.md`.

Existing named environments can be discovered and bound, but the verified import protocol cannot guarantee that a new session joins a selected environment. A `desktop_project_id` beginning with `claude-environment:` is therefore refused with `claude_desktop_project_unsupported` before any transcript seed, import or message delivery, including during import recovery. Clear the selection to start by directory instead. Directory-based `claude-project:` selections remain supported when the chosen folder is within that directory; a mismatch is refused with `claude_desktop_project_mismatch`.

Native saved-environment creation and renaming belong to the desktop renderer's store; the available main-process mirror does not provide a supported create/rename interface. Native sidebar project creation is deferred and disabled in the board, and `workspace.create` with `desktop_app: "claude_desktop"` returns `desktop_project_sync_unavailable` before any directory or native metadata creation. Create the environment in Claude, then bind it in Persona. Ordinary Persona project creation remains available. Persona does not write the underlying JSON to simulate a native change: the app's cached state can overwrite such edits. Reading existing saved-environment metadata supplies native names, but does not synchronize Persona titles back to the native sidebar.

Creation acknowledges success only after import and any supplied first message finish. If a transcript exists but setup or delivery fails, `desktop_start_incomplete` returns `created: true`, the existing session and its first-message delivery state. A retry with content sends to that session. For an empty conversation, retry `session.start` with `launcher: "claude-desktop"`, that `resume_session_id`, `prompt: ""` and `async: false`: it validates the known session, retries import and waits for the index without seeding another transcript or starting a model turn. Archived or replaced sessions are refused. See the [new-session protocol](../protocol/protocol.md#new-session).

Windows launch/import and inbox behavior have automated and mock-protocol coverage; a complete new-session or message-delivery run in the real Windows app remains unverified. Existing inbox approval and unsupported-command limits still apply. See [Windows support](../operations/windows.md#desktop-projects-and-new-sessions) for the shared validation scope.

Upstream acceptance on November 12, 2026 used Claude.app 5.1231.2 with a third-party provider and Claude Code 2.1.311 on macOS: the start answered after 3.4 seconds, the first message was taken 2 seconds later, and the reply arrived 6 seconds after the start. That result predates the synchronous delivery and recovery changes here and does not validate the Windows path. Tests: `node/claudedesktop_new_test.go`.

## Installing the Persona plugin

Sessions Persona has no terminal for (the desktop app's Code sessions, other terminals) take the owner's messages as typed input
through the Persona plugin for Claude Code (`integrations/claude-code-plugin`). The resident node carries the plugin of its own build,
writes it to `~/.persona/claude-code-plugin` (the plugin's own marketplace, `persona`) and installs it at the user scope with Claude
Code's own CLI (`claude plugin marketplace add`, `claude plugin install persona@persona --scope user`, `claude plugin update`):
automatically on macOS and Windows, never against a choice the user made in Claude Code (turned off or removed there), and never into a
config directory Claude Code has not created. A user-scope plugin loads in the desktop app's local Code sessions too; inside the app, where
`/plugin` does not run, **+ › Plugins** shows it. New sessions load it; open ones keep what they loaded. Commands, defaults, opt-out and
status: [link.md](../operations/link.md) "Claude Code plugin".

## Blocking configuration directories (#676)

Platform sessions sharing a configuration directory with personal sessions (cannot separate directories) are filtered by that platform's extension using in-session markers; see `docs/extensions/extensions.md` (“Agent filtering”).

Some computers run automation scripts launching Claude with a separate `CLAUDE_CONFIG_DIR`, creating a session every few minutes. Node's 30-second environment scan discovers and reports these directories; hub stores them in settings `claude_dirs/<machine id>`, filling Agents with offline automated sessions. Deleting records does not help: the next scan restores them. Do Not Disturb (`/api/v1/mutes`) works per space / session, not a constantly growing set without spaces. Therefore **block** configuration directories per machine:

- Rules in hub settings: `claude_dirs_blocked/<machine id>`, a string array. Each is a path or `filepath.Match` glob (`~/.automation/*`), starting with `/` or `~/` (`~` is that computer's home). Match directory itself or its ancestor; blocking `~/.automation` also blocks descendants. Shared hub / node function: `proto.ClaudeDirBlocked`.
- Node (`node/claudedirs.go`): `node.configure` includes `claude_dirs_blocked`. `claudeConfigDirs()` skips matching directories, comparing original and resolved paths. It resolves each distinct expanded path spelling once per pass, even when many retained sessions share it. When a path contains a symlink followed by `..`, session and transcript discovery follows the link before looking up the parent. Live session readers reuse a quiet session file’s parsed contents while its identity, size and modification time match, and recheck the process on every pass. Every directory lookup (offline sessions, transcript by ID, usage, memory, settings page) uses it, so blocked directories are not read. Scanned directories remain in memory for immediate unblocking, but **are not reported**; unblocking reports previously withheld directories once. Node's default directory (`$CLAUDE_CONFIG_DIR` or `~/.claude`) is never blocked; Persona-started sessions live there.
- Hub (`devices/claudedirs.go`): remove blocked directories from `claude_dirs` sent to node (hub.json + remembered); do not store blocked `claude.dirs` reports (old nodes still report them). Rule changes remove blocked stored records, reconfigure online nodes, and fetch `sessions.list`; broadcasts update the app. Offline nodes receive configure on next connection.
- Hides only; **does not delete files**. Unblocking returns sessions under the original rules (last 7 days, at most 80 offline sessions).
- Limits: running Claude in herdr remains listed even with its config directory blocked—that is a terminal in use. Old nodes do not understand `claude_dirs_blocked`; hub can only avoid storing / sending directories, while scans still list sessions. Full effect needs updated node. Existing knowledge archives are unchanged.
- APIs: `GET /api/v1/machines/{mid}/claude-dirs` (node `claude.dirs.list`: each directory, source, session count, running count, blocking rule; offline / old node falls back to hub records, `live: false`); `PUT /api/v1/machines/{mid}/claude-dirs/blocked {"blocked": [...]}` replaces all rules and returns new state.
- Not the same as hidden Agent folders, the parallel per-computer list that matches a session's working directory for every runtime and only hides it, collection untouched (`docs/protocol/protocol.md`, "Agent directory visibility"). Both share the pattern syntax and matcher (`proto.DirPatternMatchedBy`).
- UI: Settings › Claude Code (iOS `ClaudeDirsSection`, web `settings/ClaudeDirs.tsx`), Blocked Configuration Directories. One row per directory with Block / Unblock. Glob-blocked rows say “Blocked by Rule …”; unblocking deletes the rule and restores all directories it covers. Manual rule entry below. Assistant uses `persona_settings` `agents.blocked_claude_dirs` get / add / remove, with phone confirmation before changes.
- Why no Block This Directory on individual Agents list sessions: Session lacks config-directory data (would need `proto.Session` and every client changed); users usually block the source of a whole batch, clearer in directory settings with counts / running status. A later session-menu entry can reuse the API.

## Hooks (injected only into Persona-started sessions)

At startup, node writes the same hooks twice, using its own binary's `hook` mode (`node/hooksplugin.go`):

- **Plugin** `~/.persona/claude-hooks/` (`.claude-plugin/plugin.json` + `hooks/hooks.json`, plugin name `persona-hooks`): local Claude Code ≥ 2.1 (same check as `agent-plugin`, `node/agentplugin.go`) gets `--plugin-dir`, loaded only for this session. Multiple `--plugin-dir` values compose with the launcher's own `--settings` / `--plugin-dir`, whereas Claude Code accepts only the last `--settings`. Launcher aliases / functions often keep gateway, key, model in their own settings; Persona's additional file replaced it and Claude requested login (#1411). Therefore Persona passes settings only for its own managed launchers (gateway environment only, `docs/integrations/launchers.md`).
- **Settings file** `~/.persona/claude-settings.json`: older Claude Code, unreadable version, or plugin-write failure gets `--settings`, merging alias-provided settings.
- **Live replies plugin** `~/.persona/claude-live-replies/` (plugin name `persona-live-replies`, only `MessageDisplay`): written beside `claude-hooks` whatever the setting says; an Agent's start (and its herdr resume command) adds its `--plugin-dir` only while the computer's **Settings › Claude Code › Live replies** is on, and only when the hooks go in a plugin. See "Live replies (MessageDisplay)".

Tested 2026-11-04 with Claude Code 2.1.318: plugin hooks behave like settings hooks. SessionStart, UserPromptSubmit, Stop trigger; PreToolUse deny and PermissionRequest allow both work in interactive and `-p` modes.

| Event | matcher | Purpose |
| --- | --- | --- |
| PreToolUse | `AskUserQuestion` | Let Claude show its terminal dialog (duplicate question texts are still denied) |
| PreToolUse, on a Mac | `mcp__.*cua.*__.*`; Agents also `Bash` with `--cua-only` | An Agent's own Cua Driver never operates System Settings or macOS's permission prompts: denied at once with the reason, no card (`server/internal/node/cua_gate.go`, #4349; `../extensions/computer-use.md` "c) Per-App approval"). The Agents' Bash entry goes to the node only for commands mentioning cua-driver; any other returns at once (`nodeclient.CuaOnlyFlag`). The assistant's Bash hook (the Feishu gate) takes them to the same check |
| PermissionRequest | `*` | AskUserQuestion → phone card while the terminal can also answer; ExitPlanMode → plan approval; others → tool authorization |
| PostToolUse | `AskUserQuestion` | A terminal answer withdraws the waiting phone card |
| Notification / Stop / UserPromptSubmit / SessionStart | — | Report only, record pane; hub sends completion push from session state changes, not Stop |
| Stop, a turn that did work and has no closeout | `decision: block` once, with the closeout form | Only with the `closeout` triage switch on; never twice in a row (`../assistant/agent-work.md` "Closeout") |
| SessionStart, in an engine pane | `hookSpecificOutput.additionalContext` with the closeout form | Only with the `closeout` triage switch on: the manager is asked once per session and the hook waits at most 1.5 seconds for it; never an assistant conversation, a subagent or a report-only session (`../assistant/agent-work.md` "Closeout") |
| MessageDisplay | — | The Agents' live replies plugin only (not the hooks plugins, not the settings files), loaded while the setting is on: pass the reply's lines to node, exec form (`command` + `args`, no shell), `async`, timeout 1, prints nothing; see "Live replies (MessageDisplay)" |

Test results:

- **`AskUserQuestion` on both ends** (#2382, measured on 2.1.319 in an isolated pty with logging hooks): PreToolUse gives no decision and Claude draws its dialog. About 60 ms later PermissionRequest arrives for the same call **without `tool_use_id`**; its hook can keep waiting for the phone without hiding that dialog. A phone answer returns `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":{...original input, "answers":{"<original question>":"<label>"}}}}}` → model receives `Your questions have been answered: "Which color?"="Blue"`. Join multiple labels with `", "`; custom text is a direct string.
- **Editable Lark send/reply text**: an eligible assistant Bash PreToolUse card carries `editable_text`. A plain Allow with `edited_text` returns `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"…","updatedInput":{...original input,"command":"<rebuilt command>"}}}`. The rebuilt command changes only the top-level JSON `text`; when the text changes, no grant is minted for the original call before the same gate reclassifies against current rules and grants the edited call once. Quotes, shell substitutions, newlines, Unicode and large JSON numbers stay data. Local `assistant_environment` runtimes (e.g. Antigravity) and the node's MCP command-gate tool (`assistantGateTool`) support the same editing flow through `gateProxy` body rewriting, while Codex and other Bash runtimes never receive this editing capability, and Deny notes continue through the ordinary `permissionDecision: "deny"` reason (or MCP tool error result).
- **Terminal answer cleanup**: answering in the terminal while PermissionRequest still waits completes the turn but does not end that hook. PostToolUse then carries the original `tool_use_id`, the original input plus `answers`, and the result. Node carries PreToolUse's id into PermissionRequest, removes `answers` to match the original session / Agent / tool / input, and completes every overlapping waiter of that id. A completion that arrives first is kept for that id through the hook / reconnect lifetime; a late receipt for an older identical question cannot cancel a newer one. The hook attempt remembers this pairing across reconnects: a resumed PermissionRequest reuses its own id and never consumes the next question's pending id, even when the two requests overlap or identical PermissionRequests arrive out of order. If the Node that saw PreToolUse was replaced, an unambiguous recent transcript tool_use / tool_result restores the pairing; multiple possible calls hand that ambiguous generation to the terminal and withdraw its restored cards until each PostToolUse arrives, instead of borrowing an id or waiting for a completion that may never come. Standby replay applies the same PreToolUse / PermissionRequest / PostToolUse lifecycle at the spool timestamps, including events whose stale hook.event is dropped, so a terminal answer taken while the manager was away stays answered. A PostToolUse after a phone answer does not suppress the next question, whose id differs. Real Claude probe: the terminal showed the question during the PermissionRequest wait, accepted the hook's answer, then emitted PostToolUse with the same PreToolUse `tool_use_id`.
- **Queued messages** (#6088, measured on 2.1.319 in an isolated session; the send-now hint measured again on 2.1.311 for #2467, on 2.1.321 for #4537 and on 2.1.326 for #9628): a message typed while Claude works waits in its queue (`queue-operation enqueue`) and goes into the running turn at the next tool boundary ("The user sent a new message while you were working"), so Persona's queue already steers at a safe point. Under the prompt Claude shows it with a send-now hint naming the key: 2.1.319 put "ctrl+x ctrl+s to send now" after the message on its line. That key, during a command, moves the command to the background (it keeps running and reports back as a background task) and delivers the message at once; while a reply is being written, the request is asked again with the message added. Esc instead interrupts the command ("The user doesn't want to proceed with this tool use"), then sends the queue. Persona's "Send now" presses the key the hint names (`sendNowOffer` in `server/internal/node/sendnow_hint.go`; `session.queue.send_now`, `docs/protocol/protocol.md`).
  Headless (`claude -p --input-format stream-json`, a Persona chat; measured on 2.1.322 for #4342): the transcript's `queue-operation enqueue` carries no content, so the queue can't be read from it. A user line with a `uuid` gets `command_lifecycle` lines on stdout with that `command_uuid` (`queued`, `started`, `cancelled`, `completed`; one absorbed mid-turn is `started`). The control request `cancel_async_message {message_uuid}` answers `{cancelled}` (false once it went in), and `interrupt {send_now: true}` answers `{send_now: "stopped", still_queued: [...]}` and starts the waiting messages at once (`server/internal/node/assistant_queue.go`).
  - **2.1.311** (#2467, macOS, a herdr pane, a throwaway session in an empty directory): the hint is its own dim line under the queued message, read off the screen as `❯ <the message>` then `  ctrl+enter to send now`. 2.1.311 binds both `ctrl+x ctrl+s` and `ctrl+enter` to send-now (`chat:sendNow`) and its hint names ctrl+enter only where the terminal reports extended keys and is not inside tmux or screen (`$TMUX` / `$STY`); the same binary started with `STY` set showed `ctrl+x ctrl+s to send now` on that line instead. herdr's key names `ctrl+enter` and `ctrl+x ctrl+s` each sent the queue (the hint went within a second, and the transcript got "Command was moved to the background (ID: …) so that a message that arrived while it was running can reach you" and `queue-operation remove`), and so did the CSI u bytes `ESC [13;5u` (`ctrl+enter` for Orca's raw writes); a plain Enter on the empty prompt left the queue as it was. A hint Persona doesn't know is refused as `send_now_unrecognized` with nothing pressed, not as a Claude Code to update.
  - **2.1.321** (#4537, Linux, a herdr pane and tmux inside one, a throwaway session with `"tui": "fullscreen"`; fixtures `server/internal/node/testdata/claude-2.1.321-send-now-*.txt`): the hint is unchanged, `ctrl+enter to send now` in the herdr pane and `ctrl+x ctrl+s to send now` in tmux, each on its own line under the message, and each key sent the queue (`queue-operation remove` with `reason: "absorbed_mid_turn"`). The bundled code builds the text from the binding (`${keys} to send now`, lower case, `click to send now` when no key is bound), and 2.1.311, 2.1.318, 2.1.319 and 2.1.321 pick the key the same way. The node reads the hint by that shape rather than as fixed text: keys, an optional separator, `send now`. Keys a terminal can't send (cmd, fn, a click) are quoted and not pressed. In the fullscreen renderer the queued message and its hint scroll with the transcript: scrolled up (PageUp, the wheel), the screen shows only `Jump to bottom (ctrl+End) ↓` (on macOS `(click)` or `: fn+↓ to scroll`; `N new messages` once more arrived) and `❯ Press up to edit queued messages`. ctrl+End, Claude Code's default `scroll:bottom` in 2.1.311 and 2.1.321 alike, brings the end and the hint back, so the node presses it (the key the pill names, else ctrl+End) before looking again. herdr 0.9 has no key name for End, Home or the page keys, so the node writes their xterm bytes (`ESC [1;5F` for ctrl+End) with `pane.send_text`.
  - **2.1.326** (#9628, macOS, a separate headless herdr session, a throwaway hook-free config with `"tui": "fullscreen"`; fixture `server/internal/node/testdata/claude-2.1.326-send-now-herdr.txt`): the hint under the queued message carries a label, `  ⎿  Queued · ctrl+enter to send now`, where `<prefix>Queued · <keys> to send now` fits the width, and is the bare `  <keys> to send now` as before where it doesn't (one line, never wrapped). The bundled code still builds the keys from the binding; 2.1.322 to 2.1.325 have no label. The node takes the keys after a ` · ` as well as after a gap, whatever the label says. ctrl+enter sent the queue: the foreground command moved to the background, `queue-operation remove`, and the reply to the queued message came while the command still ran.
- **`Notification` is late, and cancellable** (measured Claude Code 2.1.2xx, re-check on the installed version): `permission_prompt` arrives about 6 s after the dialog is already on screen, and if the dialog is answered first the notification is cancelled and never arrives. `idle_prompt` comes about 60 s after the input box goes idle. Both are reporting only: a card must never wait for them, and "needs you" cannot be derived from `Notification` alone. State comes from the session (transcript, status line, screen); the notification is a hint with a delay measured in seconds.
- **`MessageDisplay`** (undocumented) carries a reply's lines as Claude shows them, before the transcript has them: measured and used for the Agents Persona starts, see "Live replies (MessageDisplay)" below.
- **Claude's own safety checks ignore a hook's allow** (#3752, measured on 2.1.322 in a pty with an empty settings directory and a throwaway hook):
  a Bash command such as `cd <elsewhere> && rm -rf ./* ./.[!.]*` is asked about even in bypass mode ("Dangerous rm operation on
  statically-unresolvable target …", "⚠ Claude Code will automatically block this request in 1:59, to avoid holding up an unattended
  session", 1. Yes / 2. No). Claude runs the PermissionRequest hooks for it, so the phone gets a permission card, but an `allow` (with or
  without `updatedPermissions`) leaves the dialog on screen and the countdown going; a `deny` closes it ("Denied by PermissionRequest hook").
  Node therefore follows the pane after every allow it hands a hook; a dialog still there lists the Agent as blocked and the phone gets the
  dialog itself as a screen card (`docs/protocol/agent-states.md`, "An allow Claude did not take"). The screen card's `rev` leaves the
  countdown out (#571), so the answer holds while it ticks.
- **ExitPlanMode uses PermissionRequest, not PreToolUse** (PreToolUse matcher does not trigger in bypass mode).
  - Approval: `decision: {behavior: "allow", updatedInput: <original input>, updatedPermissions: [{type: "setMode", mode: "bypassPermissions", destination: "session"}]}`. **Plain `{behavior: "allow"}` does not work; TUI still asks “Would you like to proceed?”**
  - **The mode after the plan (#3784)**: a session started in plan mode records the mode it goes on in once its plan is approved (`plan_then` on `session.start`, kept in the node's session state): what a writable start with no mode of its own would run in (the assistant's default permission mode, else the computer's Claude Code `permissions.defaultMode`, else Ask every step), filled in by the manager for the apps' New Agent, `task_plan`, `agent_start` and dispatched tasks. The plan interaction carries the recorded mode as `plan_then`; the card shows that one following mode, and the manager and node both prefer it over a mode sent by an older client, so the answer's history and Claude's `setMode` agree (Allow All when none was recorded and the answer names no mode, as before). The terminal's own dialog may decide the plan while the card still waits, so the node watches the footer from the moment the card is raised until the card is answered, for at most 10 minutes (a plan approved there with "manually approve" is followed at once by a permission dialog that hides the footer until it is answered): once the footer shows a mode other than plan, the plan was approved there, and the node presses the session over to the recorded mode when the dialog picked another (`server/internal/node/planthen.go`). A plan sent back stays in plan mode and is left alone. When the recorded mode is Allow All the session is started with `--allow-dangerously-skip-permissions` beside `--permission-mode plan`, so that mode is in its shift+tab cycle: the hook's `setMode`, the correction above and a later `…/modes` call can all reach it. The same watch runs for every plan card, with or without a recorded mode: the plan dialog hides the footer, so once the footer is back the terminal decided the plan (approved or sent back) and the card that still waited is withdrawn as `dismissed` (decided on the computer), where a hook that merely went away is `cancelled`. So a plan's history says where it was decided: `resolved` with the user's answer on a card, `dismissed` with `answered_by: "terminal"` when the terminal decided it (approved or sent back), and `answered_by: "assistant"` when a supervising conversation answered.
  - Return for changes: `{behavior: "deny", message: "…revision feedback"}`; message becomes an is_error tool_result, and the model revises and resubmits.
- The user's launchers all include `--dangerously-skip-permissions`, so ordinary tools do not trigger PermissionRequest; without it, every tool call goes to the phone.
- Plugin / settings hooks are read only at startup; changes require restarting Claude. Hook command itself executes anew each time.
- Waiting more than 30 minutes (`hook_timeout_minutes`) or unable to connect to node → no output, normal TUI dialog. Phone can also choose Hand to Terminal on the Computer. If node disappears while waiting (hub / node redeployment), hook reconnects to the next node, preserving the phone card (`docs/protocol/protocol.md`, “Interactions survive restarts”). Killed hook (user Esc on computer) → node detects socket closure → tells hub to revoke interaction.
- **Another PermissionRequest hook answered first** (#2648): Claude Code runs all PermissionRequest hooks concurrently; first allow releases the operation, but **other hooks are not terminated**, running to their own timeouts (tested 2.1.308). Persona hook still waits for phone, leaving a stale card. The answering hook can notify node.sock (`~/.persona/node.sock`, used by `personad hook`) with one line `{"kind": "hook.decided", "input": <original Claude Code PermissionRequest input>}`, receiving `{"ok": true}`. Node identifies invocation by session, sub-Agent, tool, input: withhold an unsent card or revoke an existing one (`node/hookdecided.go`).
  Each `hook.decided` covers one invocation (#4646): earliest waiting matching card; if none, first matching request within 2 minutes, deleted after use. Later identical calls still go to phone. Once a hook on that computer has reported this since node startup, tool permission cards wait 1 second before dispatch to avoid push-then-immediate-revoke. Other computers retain no delay. The claude-bypass-allow extension (automatically allows Bash safety confirmations in bypass mode) uses this.

### Self-check: `personad node doctor`

Hooks, MCP, and skill-plugin files are generated at node startup; damage gives no warning (hook cannot connect to node and falls back to TUI; see the deleted `node.sock` entry in `docs/lessons/server.md`).

The generated `mcp.json` retains both built-in compatibility namespaces: ordinary Agent starts and resumes share this
file, and generation precedes the hub handshake. Doctor checks and repairs that same configuration. The
`persona_contracts_v1` optimization applies to per-process assistant configurations instead ("Headless assistant"
below; [rolling-upgrade-compatibility.md](../protocol/rolling-upgrade-compatibility.md), "MCP namespace / tools").

`personad node doctor [--json] [--fix]` performs read-only checks on this computer (`node/doctor.go`). It prints one line per check in the terminal's language (`i18n.Terminal`), with the report's order and layout unchanged; the zh-Hans labels remain `正常` / `注意` / `有问题` (“OK” / “Attention” / “Problem”) and correspond to JSON `ok` / `warn` / `fail`. Any fail yields a nonzero exit code (hub doctor has no `--json` and does not use exit status):

| Check | Content | Status |
| --- | --- | --- |
| `node.sock` | File exists and `{"kind":"ping"}` receives a response through real `serveSock` | Missing / unreachable: fail |
| `runtime.sock` | File exists and connects | Missing: warn (ignore if runtimes unused); unreachable: fail |
| `claude-settings.json`, `claude-hooks` (with `claude-live-replies/`), `assistant-settings.json`, `assistant-hooks`, `mcp.json`, `agent-plugin` | Byte-for-byte compare with **what this binary would generate now**: current / stale (different or plugin files missing, identified in items) / missing | stale: warn; missing hooks / settings / MCP: fail; missing `agent-plugin`: warn (only persona-office skill absent) |
| `command-paths` | Hook / MCP commands in those files (first word parsed following `shellQuote` syntax) point to existing executable programs | Unusable: fail |
| `claude-version` | Local Claude version ≥ `pluginMinVersion`, supporting `--plugin-dir` | Unreadable / too old: warn (`--settings` fallback still works); read nothing although the shell found claude: warn with state `unanswered`, which readiness treats as unknown rather than missing |
| `herdr` | Herdr API socket connects | Unreachable: warn; engine selected by hub on connection, not stored locally; ignore for other engines |
| `global-permission-hooks` | Count and commands, truncated to 80 characters, of synchronous non-async `PermissionRequest` hooks in user's `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude`). A hook whose program (first word, `~/` / `$HOME/` expanded, symlinks followed) is a file of a package the node installed (`<persona dir>/node-plugins/<id>/<version>/`, with the node's `.persona-node` mark and the version's `.persona-digest` record and manifest) is that extension's, named by its manifest (#11973); a path under `node-plugins/` alone is not enough | Another tool's hook present: warn; other approval hooks block requests alongside phone approvals. Only installed extensions' hooks: ok, naming them; **inspect only, never modify** |
| `agent-sandbox` | Whether the agent sandbox is on, whether this machine can run it, each kind of start's last status (`docs/server/agent-sandbox.md`) | On but unusable / skipped: warn |
| `launchd-process-type` (Mac only) | The resident node's installed LaunchAgent in `~/Library/LaunchAgents` has `ProcessType=Interactive`, and launchd runs it that way (`launchctl print`: `spawn type = interactive`). When both supported definitions exist, inspect the one launchd has loaded (docs/server/server.md "Scheduling and privacy on the Mac") | Missing / other value / loaded from an older plist: warn; no agent: ok |
| `path-access` (not on Windows) | Reads what a claude start reads (Claude Code's directory, `settings.json`, `settings.local.json`, `CLAUDE.md`, the symlinks in `skills`, `commands`, `agents`; each working directory and its `.claude/` ones) in a child that is killed after a 3 s stall (a whole run: at most 15 s); asked of the running node when there is one | Blocked in Claude Code's directory: fail; only in a working directory: warn |

- Comparison and `--fix` share `generatedFiles`; startup `writeClaudeConfig` and fix use `writeGeneratedConfig`, so current means what node restart would write. `agent-plugin` comparison lives in `assistantskills.PluginIssues`; `WritePlugin` now skips only when no differences are reported (previous stamp-only check never repaired deleted files if the stamp remained).
- Fix rewrites only Persona-generated files, only when stale / missing, and reports fixed. On a Mac, the command line's `--fix` also writes `ProcessType=Interactive` into the resident node's LaunchAgent and reloads it (the node restarts; the checks after it wait up to 15 s for the node's sockets to answer again); `node.doctor.fix` and a doctor running under that agent leave it alone and say so (docs/server/server.md "Scheduling and privacy on the Mac"). Does not touch global user settings or rebuild node sockets; restart node for that. **Use the binary actually running node**: generated files contain the current binary path; another binary's fix redirects hooks to itself.
- JSON: `{"ok", "dir", "checks": [{"name", "status", "state", "detail", "hint", "items"}], "fixed": []}`. Fields always present, lists never null, check names and order fixed (`TestDoctorJSONIsStable`). `personad node doctor --json` is machine output and keeps the original zh-Hans human fields byte for byte under every terminal language.
- Web machine page “What This Computer Can Do” (#2429) receives the same report shape via `node.doctor` / `node.doctor.fix` (`node/doctor_rpc.go`), full-role devices only. The RPC's optional `locale` words the human fields for a current page; without it the report remains canonical zh-Hans for old hubs, old readers and readiness. The hub forwards only an explicit `Accept-Language`, and an old node's report passes through unchanged. Capability grouping rules: `docs/server/machines.md` (“Doctor: what this computer can do”).
- Hub doctor covers hub configuration / network, not these files. Node checks live in node doctor, not hub doctor.
- Tests run in temporary directories (`node/doctor_test.go`); manual fix uses temporary HOME, not real `~/.persona`.

## Live replies (MessageDisplay)

On the phone and the web, an Agent's reply used to appear only once Claude wrote it to the transcript, often seconds after it was on the terminal; a reply that ends in an auto-approved tool call is written only once the tool is done. Claude Code's undocumented `MessageDisplay` hook carries the reply's lines as Claude shows them. The Claude sessions Persona starts register it through a plugin of its own, `claude-live-replies` (#1309), while the computer's setting **Settings › Claude Code › Live replies** is on (the default); the user's own sessions, the global report-only hooks and the headless assistant (which streams its own replies) never do. Claude reads plugins when it starts, so a change of the setting reaches the sessions started after it; a session already running keeps what it started with until Claude restarts (herdr's restore included: node gives herdr the resume command again with the plugin added or dropped; a pane's resume reports take turns and each says the pane's newest command, so turning it off and on again quickly cannot leave herdr the older one).

### Measured (Claude Code 2.1.319, isolated pty runs with a per-run `--settings`; the event is also in 2.1.318)

- **Input**: `session_id`, `transcript_path`, `cwd`, `prompt_id`, `hook_event_name: "MessageDisplay"`, `turn_id`, `message_id`, `index` (int), `final` (bool), `delta` (string). No `permission_mode`, `effort` or `agent_type`; no `agent_id` in a main conversation (subagents not measured).
  ```json
  {"session_id":"<id>","transcript_path":"<home>/.claude/projects/<project>/<id>.jsonl","cwd":"<work-dir>","prompt_id":"<uuid>","hook_event_name":"MessageDisplay","turn_id":"<uuid>","message_id":"<uuid>","index":0,"final":false,"delta":"First line of the reply.\n"}
  ```
- **Increments, not the whole text**: `delta` holds only the lines completed since the last batch. A non-final delta is whole lines ending in `\n`; the final one may end mid-line (the reply's last line arrives only with it), and is empty when the message ends on a newline (from the binary's schema, not seen). The deltas of one `message_id` joined in `index` order equal the transcript's text block exactly (9 of 9 messages). Only text: thinking and tool calls send nothing.
- **`index` is the batch number**, not a content block: 0, 1, 2… within the message, exactly one batch with `final: true`. (An earlier note said it was the content block index; that was wrong.)
- **Ids**: `message_id` is a random UUID per message on screen, the same for all its batches, never the transcript's `msg_…` nor a line's `uuid`. `turn_id` is new each turn, shared by its messages, and appears nowhere in the transcript. Only `prompt_id` matches: it is the turn's user line `promptId`, and `UserPromptSubmit` and Stop carry it too (assistant lines have none; they reach it through `parentUuid`).
- **Messages**: each API reply is a message. The words before a tool call and those after its result are two messages, the second with a new `message_id` and `index` from 0, the same `turn_id`. The first one's last line comes with its final batch only once the tool call's arguments are streamed; nothing comes while the tool runs.
- **Pace**: a batch only when a line is completed, at least 100 ms apart (the binary: at most 10 a second); the final batch is not held back. Long lines came about one per batch, 0.55–0.75 s apart, at the model's pace; 80 short lines came in 14 batches of 1–16 lines over 1.7 s.
- **Concurrency**: at most 3 hooks run at once; when 3 are busy, new lines wait and go out as one batch. Hooks finish out of order, so batches arrive out of order.
- **The terminal waits for the hook.** `MessageDisplay` hooks run in the foreground even with `async: true` (the debug log: "Detected async hook but forceSyncExecution is true"), and once any is registered the TUI shows a reply batch by batch instead of token by token, each batch only after its hooks are done (a 2 s hook delayed every line 2 s; a 12 s hook ran its full 12 s with a 30 s timeout). A hook's `hookSpecificOutput.displayContent` replaces the lines on screen (not the stored message). So the hook must pass the lines on and exit at once, printing nothing.
- **Ahead of the transcript**: up to 6.6 s for a reply that ends in a tool call (its text is written with the tool's result), 0.4–1.6 s for a plain reply's first lines. But the transcript can be first: before the final batch, and for a message that arrives in one piece even before the first batch. Line `timestamp`s are 0.14–3.34 s off from when the line is written.
- **Stop is not last**: with a slow hook a Stop came before the final batch; a batch still running when Claude exits is dropped.
- **Not every turn ends with a Stop** (from the binary and earlier measurements, not measured with this hook): Esc (and Persona's stop) fires no Stop, the transcript gets `[Request interrupted by user]`; a turn a model error ended fires `StopFailure` ("Fires instead of Stop when an API error … ended the turn", fire-and-forget) and the transcript gets the error as the answer (`API Error: …`) or a given-up `api_error` line.
- **`-p`**: each API message comes once, `index` 0 and `final: true`, the whole message (several text blocks joined), 0.14–0.42 s before the transcript.
- Not measured: after compaction, subagents (`agent_id`), an empty final delta, plugin registration (`--plugin-dir`; plugin hooks behaved like settings hooks before), the default 10 s timeout cutting a hook, an interrupt or a model error while a message is on screen.
- Older Claude Code versions without the event: 2.1.319 drops an unknown hook event with a warning and keeps the others; how much older versions accept is unverified, as for Elicitation. The settings file (Claude Code older than 2.1) does not register it.

### How it is used

- **Hook** (`server/internal/nodeclient/hook.go`): for a `MessageDisplay` input, `personad hook` sends `{"kind":"hook.display","input":…}` on `node.sock`, waits at most 150 ms for `{"ok":true}` and prints nothing. The node answers before it does anything with the lines (within a millisecond), so a hook that gave up after writing them still had them taken. The registration is `async: true`, timeout 1 s (seconds; Claude Code then shows the lines as they are), in exec form so that no shell starts for each batch (below).
- **What the terminal pays**: once the hook is registered, a Persona-started session's TUI shows each reply a batch of whole lines at a time instead of token by token (the batch ends at a line's end, at most 10 a second), and each batch is drawn only after one `personad hook` run, whether or not anyone has the session open. The user decided (2026-11-07) to ship it on by default with a switch to turn it off.
  - **How long a run takes** (wall time from exec to exit for one MessageDisplay input, 100 runs each, medians with p90, Apple silicon Mac, Go 1.27, 2026-11-07): `personad hook` 28.0 ms (p90 30.6) without a node socket and 28.6 ms (p90 31.0) with a fake node that answers at once, before; 12.0 ms (p90 13.8) and 13.0 ms (p90 14.2) after. `persona-node hook` 25.8 / 25.5 ms before, 12.2 / 11.4 ms after. A Go program that only reads its stdin takes 7.5 ms on the same machine, a Python hook 90–260 ms. Almost all of the difference was package initialization of the server it links (15–18 ms and 13–16 MB allocated before, which also set off four garbage collections; 3–4 ms, 1–1.6 MB and none after): the i18n catalogs (`extension.UISlots` and `assistant.silentTurnError` called `i18n.T` in package variables, so the lazy catalogs were parsed at every start anyway), every prompt template (`prompts`), about 440 package-level regexps (now `internal/lazyre`, compiled on first use; its guard test keeps new ones out) and the built-in model presets. What is left is the runtime and third-party packages' init (goldmark, the tailnet stack).
  - **The shell in front of it**: those numbers are the program alone (exec to exit). A hook whose `command` is a command line (shell form) runs as `/bin/sh -c "<command>"`; on the same Mac that adds 6–8 ms to every batch (2026-11-08, 100 runs each, a fake node answering: `personad hook` 12.0–12.7 ms alone, 19.2–20.2 ms through `/bin/sh`, p90 13.3–14.4 / 22.7–24.1; `persona-node hook` 11.0–11.5 / 18.0–18.9 ms; the stdin-only program 5.5 / 11.4 ms). So the MessageDisplay hook is in exec form, `{"command": "<personad>", "args": ["hook", "--persona-home", "<node directory>"]}`: Claude Code starts the program directly (checked on 2.1.319 in a `-p` run with a probe plugin: the exec-form hook got `[<program>, "hook"]` with the caller's `SHLVL` and a path with a space in it worked, the shell-form one ran under a shell, `SHLVL` one higher). Every Claude Code with the event takes exec form (the darwin-arm64 builds: 2.1.110 has neither, 2.1.150 exec form only, 2.1.200, 2.1.250, 2.1.300, 2.1.318 and 2.1.319 both). Persona's other hooks stay in shell form: the settings file is also for a Claude Code older than 2.1, and they do not hold up the screen. They name the node's directory with `PERSONA_HOME=<dir>` in front of the program; exec form has no shell to take that and a command hook has no `env` field (2.1.319's schema), so this one gets it as an argument, `--persona-home` (`nodeclient.HomeFlag`). A batch on a Mac therefore waits about 12–13 ms for the hook; a separate tiny hook binary would get to about 6–8 ms, but it would need its own install and update path on every platform, so it was not made.
  - **The setting** is per computer, kept by the manager that owns it in the hub's settings table (Postgres, key `claude_live_replies/<machine id>`, on when unset): `GET` / `PUT /api/v1/machines/{mid}/claude-live-replies` `{"on"}` (`docs/protocol/protocol.md`), sent to the node in `node.configure {"claude_live_replies"}` when it connects and at once when it changes. A node that is never told (an older hub) keeps it on. The web has it in Settings › Claude Code (with "Change all online machines" like the other rows), the Apple apps in Settings › Claude Code Settings › Live Replies (with Apply to All Online Machines; `LiveRepliesModel`, `docs/apple/ios.md` "An agent's live reply"); a hub without the route (404) shows no switch in either. Both say the text comes sooner on the web and the iPhone and Mac apps, and what the terminal pays; an iPhone or Mac app older than #1309's Apple part does not draw an Agent's live text.
- **Node** (`server/internal/node/livereply.go`): per session, the batches are put in `index` order and taken once; a batch of a message that ended, or of a turn a Stop ended (by `turn_id`), is dropped. Only while the hub watches the session, the node sends `session.delta {session_id, text}`: the message's text so far, minus what the transcript has, otherwise as it came. The check runs after each batch and right after each push of new items under the same per-session lock, so the `""` reaches the apps right after the items that replace the live copy.
  - **Which words replace it** (`unlanded`): the transcript's text items of the exchange under way (after the last prompt or wake) are split by API message (a run of the agent's lines; a tool's result, a queued message, a compaction come between two). The newest message whose words begin like the live text is its own; its blocks are taken off the live text's start, an item that begins with the rest of it means all of it is in. A block counts only where a word ends (`好的` is not the start of `好的，测试已通过`). Two messages of a turn can begin alike ("I'll update the names." before and after a tool): the node keeps the turn's message before (its text from the batches), and words that begin like both are that one's until a later message has them too, since the one before lands first. A cut item (pages and pushes carry 24 KB of a long text) holds its whole block, `full_size` bytes; what goes past it is still shown. Words of a later message after its own mean the message is over.
  - **When it goes**: a message whose final batch came and that the transcript has whole is done at once. Otherwise it belongs to its turn: when the transcript shows the turn over after the message's first batch came (the exchange opened after it by a prompt or a wake, `turn_duration`, an interrupt, a model error that ended the turn) it goes, which covers the turns that end without a Stop. Line timestamps are compared with when the first batch came, so the end of the turn before, read only after the next prompt's first batch, does not count. It also goes at the next prompt, 2–4 s after a Stop, when the session's claude is gone, and after 30 minutes without a batch. `StopFailure` is not registered: the transcript says the same, and the hub would get an event it does not know.
  - **Nobody watching**: the hub is told `""` (it would hand stale text to the next page), but the message stays: a page that opens the session while Claude still shows it gets it back, once the watch's first read of the transcript has checked it with the rules above.
- **Hub**: unchanged; it forwards `session.delta` as the `delta` push to the session's subscribers and keeps the latest for a page that subscribes mid-message (`docs/protocol/protocol.md`).
- **Clients**: the live text is sent as it came (only what the transcript already has is taken off its start), so a client can match it to the item that replaces it by its start. The web draws `delta` for any chat with a live exchange (`web/src/lib/live-text.ts`, `docs/web/web.md`): an Agent's against the agent's latest message only (its words at the end of the transcript, with nothing after them), plus a whole copy of the message before come late; only the newest items touch it (an older page or an opened fold never does), and the live work row stays above the latest-text slot through the handoff to the saved reply. The main chat keeps its rule: its reply's item ends it. The iPhone, iPad and Mac apps draw an Agent's the same way (`Shared/LiveText.swift`, a port of `live-text.ts` with its tests case by case; `ChatStore.agentStreamed`), under the newest items as the main chat draws its live text; a change that comes while the chat's rows are being rebuilt waits for them, so the words leave the live copy in the same pass their row comes, and it is dropped when the chat's subscription ends or starts again, since the `""` that ended it meanwhile went to no one (`docs/apple/ios.md` "An agent's live reply"); the watch does not. Android draws any chat's `delta` as a row matched to the transcript by its start (`ChatRealtimeState`); not checked with these sessions.
- **Not verified yet** (acceptance items of #1309 still open): a side-by-side run on a real session through node, hub and web (text before the transcript line); the plugin registration (`--plugin-dir`) firing the hook; a session after compaction; an interrupt or a model error with a message on screen. The tests drive the node with recorded input shapes and the web with the fake hub (`MOCK_AGENT_LIVE=1`, `web/e2e/agent-live.mjs`).

## Global report-only hooks (optional, off by default)

Only Persona-started Claude sessions have its hooks (above). User-started herdr sessions rely on screen / transcript inference, fragile for TUI cards, multiselect, previews.
`machines[].claude_global_hooks: true` in `hub.json` makes that node add report-only hooks to **default config directory** `settings.json` (`$CLAUDE_CONFIG_DIR` or `~/.claude`), so other sessions report turns and questions. Omitted means off; false / removal deletes Persona entries on next node connection. Manual `hub.json` changes require hub restart and take effect at node reconnect through `node.configure`, like botmux / herdr_autostart.

**Why not install Persona's existing hooks globally**: global settings commonly already contain synchronous `PermissionRequest` hooks from other tools (a terminal-control hook, Orca). Another blocking hook stacks approvals: the first blocks subsequent hooks and the terminal dialog. Persona's question / authorization hook waits for phone (timeout 86400); globally, users at the computer cannot answer in terminal until phone response or timeout. Same reason `codex.md` does not use Codex `PermissionRequest` hooks.

> **To re-test: a blocking `PermissionRequest` hook may not lock the terminal dialog.** Seen on Claude Code 2.1.221: the person at the computer could still answer, and a late `allow` from the hook still applied afterwards. Measured again on 2.1.319 for a handed-back `AskUserQuestion` (the bullet above): the dialog is already on the terminal and that waiting hook does not block it, although the turn goes on. If a blocking hook does not lock the dialog on the version in use, the "users cannot answer in the terminal" reason above is weaker than written. What still holds without it: a hook waiting on the phone leaves a card nobody can answer, and its late answer can arrive after the terminal's, producing two outcomes for one call. Re-measure on the installed version before relying on "the terminal is blocked"; keeping global hooks asynchronous stands on the duplicate-card problem alone.

Therefore this global group:

- **Report only**: SessionStart, `UserPromptSubmit`, Stop, Notification, plus `PreToolUse` / PostToolUse for `AskUserQuestion` / `ExitPlanMode` (card appearance / answer signals). Every entry `async: true`, `timeout: 10`; **no** `PermissionRequest`, Elicitation, or phone-waiting PreToolUse.
- **No answers**: phone answers still use screen cards and keys (“Screen cards for multiselect questions”).
- **Hook process**: command `<personad> hook --persona-global` (`runHook`). With no herdr / Orca pane variable (empty `HookPane`), or absent `node.sock`, **exit immediately without output**, no socket connection / wait. Connected waits at most 3 seconds; output always empty. Like plugin hooks, carries owning Claude PID / start time to protect counters from PID reuse.
- **No duplicate reports**: Persona-started sessions already load persona-hooks. Node identifies session ID: `n.state` sessions (started, resumed, forked by Persona) and assistant sessions are excluded, including turn counting (`takesReport`, `node/hooks.go`). An environment marker would require modifying launcher commands with assignment before user shell functions, differing in fish / zsh, so not used.
- **No pane binding**: global hooks never call bindPane. Tmux servers (botmux) inherit `HERDR_PANE_ID` into later Claude processes; binding would steal the pane from its actual Claude (see Session IDs and transcripts). Node finds session / pane associations itself.
- **Not treated as Persona hooks by hub**: node `hook.event` includes `report_only: true`; hub does not add session to hooked / hasHooks. Otherwise screen cards wait 1.5 seconds for a hook card that will never arrive. `hook_event` broadcasts still refresh session run state (`../protocol/protocol.md`), and Stop `stop_sig` remains deduplicated.
- **Still drive turn counting**: these are the only signals for user-started sessions; `UserPromptSubmit` / Stop enter node turn counters. State rules: `../protocol/agent-states.md` (“Hook turn signals”).

**`settings.json` write rules** (`node/globalhooks.go`):

- Add / remove only **marked entries**: command ends in `hook --persona-global`, unlike other tools. User hooks sharing a matcher group remain; remove only ours.
- Idempotent: desired content already present → **no file write**, backup, or formatting change. Changed Persona binary path replaces old entries without duplication.
- Preserve other keys, other hooks, order. JSON re-encoding follows existing indentation (first indented line, otherwise two spaces); inline arrays expand. Do not escape `&` / `<`. Preserve permissions; follow dotfile symlinks to real files without removing links.
- Back up as `settings.json.persona-bak-<UTC time>` before actual changes. Nonobject JSON, nonobject hooks, or nonlist event → leave untouched, log only.
- Do not create absent config directory (Claude not installed). Manage only default directory, not other profiles selected by `CLAUDE_CONFIG_DIR`; those belong to launchers.
- Tiny race window with Claude Code writing settings simultaneously: temporary-file + rename prevents partial files, but can lose the other's latest write.

**Why not `CLAUDE_CODE_PLUGIN_DIRS`** (2026-11-04, tested Claude Code 2.1.318): this variable **really exists**. Not in `claude --help`, but binary reads it. Absolute-path list separated by platform PATH delimiter (`path.delimiter`, `:` on Linux / macOS); `~` and relative paths skipped with warning. Merged with `--plugin-dir`, deduplicated. Error text says set in environment or settings env block, so `settings.json` env works too.
Not chosen because it is **one variable**: another terminal-control tool already puts its plugin path in global env.CLAUDE_CODE_PLUGIN_DIRS. Persona would need to alter another tool's value, likely overwritten on reinstall; event-based hooks compose without replacement.
Whether shell export is overridden by `settings.json` env was not tested. Without this conflict, users can add a Persona-generated plugin directory themselves.

**Not verified**: actual Claude Code triggering of these entries (other local tools already use async settings hooks, so field valid, but these entries have not run in real sessions); separate `AskUserQuestion` / `ExitPlanMode` matcher groups rather than `|` follow other tools; Windows command quoting.

## MCP elicitation hooks

Read-only local verification on 2026-10-31: `claude --version` returned **`2.1.306` (Claude Code)**, matching the installed `package.json` version. This is the actual version used for this check; distinguish it from versions recorded earlier elsewhere in this document.

The local native executable contains input / output schemas for Elicitation and `ElicitationResult`, `executeElicitationHooks` / `executeElicitationResultHooks`, and parseElicitationHookOutput. Inputs match hooks by MCP server name. Elicitation runs before the terminal dialog; `ElicitationResult` before returning the answer. This version already has integration entry points; development need not wait for an upgrade. Earliest supported older version is unverified.

Verification: run `claude --version`, resolve the installation directory from `command -v claude`, read package metadata and those executable strings. No installed package, Claude configuration, or real session was modified; no model request was sent. Formats below come from local schemas and input construction, **not yet verified by a real MCP server triggering hooks**.

Elicitation stdin:

```json
{
  "hook_event_name": "Elicitation",
  "session_id": "<session id>",
  "transcript_path": "<transcript path>",
  "cwd": "<project path>",
  "permission_mode": "default",
  "mcp_server_name": "example-mcp",
  "message": "Please choose",
  "mode": "form",
  "requested_schema": {
    "type": "object",
    "properties": {"choice": {"type": "string", "enum": ["yes", "no"]}},
    "required": ["choice"]
  }
}
```

Mode may be omitted, defaulting to form. URL mode replaces `requested_schema` with `"mode":"url"`, `"url":"https://example.com/confirm"`, and `"elicitation_id":"request_example"`. MCP `requestedSchema` / `elicitationId` become `requested_schema` / `elicitation_id` in hook input. Common fields may also include `agent_id`, `agent_type`, prompt_id.

Hook exits 0 and writes stdout:

```json
{"hookSpecificOutput":{"hookEventName":"Elicitation","action":"accept","content":{"choice":"yes"}}}
```

Deny / cancel use `"action":"decline"` / `"action":"cancel"` without content. URL acceptance also has no content. `ElicitationResult` input includes `mcp_server_name`, optional `elicitation_id` / mode, action, optional content; output uses the same hookEventName. Persona registers only pre-action Elicitation, avoiding asking already answered requests again. Claude may restrict hook answers for particular servers; final behavior belongs to Claude.

Persona integration contract:

- Node-generated ordinary-session and headless-assistant configs both register Elicitation, matcher `*`. Running Claude must restart to load new registration.
- Node places original schema / URL in `Interaction.elicitation`, forwarding MCP server name and sub-Agent source. Existing optional `tool_name` / `tool_use_id` stay, forwarded only if supplied upstream. This local event input construction **does not supply tool name, tool-call ID, or config source**; do not guess from the last call or server name. Other tool-permission hooks' `mcp_server:{name,source}` can supply config source.
- Hub sends new types only to connections declaring elicitation in `client_hello.features`, never before handshake; first declaration replays still-pending requests. No compatible connection and no phone registered to be pushed one ([protocol.md](../protocol/protocol.md) "MCP elicitation", #2433) → immediately return dismiss; hook outputs nothing, Claude uses normal terminal flow. With such a phone the hook waits like a permission card's, and the terminal shows nothing for the request until it is answered or handed back.
- Phone accept / decline / cancel pass through unchanged. Dismiss hands back to terminal, distinct from MCP cancel. Timeouts and node / hub interruptions reuse existing terminal fallback, reconnect, and interaction recovery. Request IDs distinguish hook-process attempts so repeated questions from one tool remain separate.
- The iOS app declares it too (#2322: both modes, the link opening in its own link card; `docs/apple/ios.md` "Information architecture"); the Mac and visionOS apps don't.
- Android declares capability and receives both modes; this worktree's `ElicitationFields.kt` still supports only form submission. URL-open-then-continue gating belongs to the UI workstream; this workstream did not change feature/chat. Real MCP round-trips, completed URL authorization, and older iOS device acceptance remain untested.

## Screen cards for multiselect questions (2026-11-02, tested 2.1.316)

User-started Claude via launchers, such as `--settings <profile>/settings.json`, does not load Persona hooks (injected only into Persona-started sessions above). AskUserQuestion appears in terminal, leaving only a screen card on phone. Previously screen cards missed multiselect: `[ ] 甲` (“A”) was treated as option text, number keys merely checked one option, and Submit was unrecognized, making submission impossible.
Do not install hooks into user-owned config directories by default (optional report-only hooks above cannot answer either). Instead, screen cards now recognize and answer multiselect.

Multiselect example (`server/internal/screen/testdata/claude-2.1.316-*`, recorded in isolated herdr, text replaced with placeholders; 环境 = environment, 平台 = platform, 手机 = phone, 手表 = watch):

```
←  ☒ 环境  ☐ 平台  ✔ Submit  →          Tab bar: one tab per question; ☒ answered; current tab indicated only by color
示例二：哪些平台要一起发？                 Example 2: which platforms should ship together?
❯ 1. [✔] 手机                            [ ] / [✔] are check states, not text
  2. [ ] 手表
  3. [ ] Type something                    Custom answer: entered text displays and auto-checks; clearing unchecks
     Next                                 Submit for the only or final question
──────
  4. Chat about this
```

- Number keys (or Enter) toggle checks without moving the cursor. **On the custom-answer row, numbers become text; on Submit, numbers do nothing**—move back onto an option first.
- Down from the last option reaches Submit, then Chat about this. Enter on Submit, or Right, opens Review your answers: each answer, Ready to submit your answers?, `1. Submit answers` / `2. Cancel`; unanswered questions add `⚠ You have not answered all questions`.
- Answers join options in original order with `, `, custom answer last.
- Sending multiple keys together loses keys (`send-keys 1 3` checks only one): send one key, wait for redraw.
- Single-choice among multiple questions: no checkboxes, `Type something.` has a period, number selects and advances. Revisiting an answered question shows ` ✔` after selected option.
- One single-choice question has no tab bar, with ` ☐ 环境` (“environment”) at top. Custom text replaces `Type something.`; Enter submits.

Node answers (`node/dialogmulti.go`) by rereading screen at each step and computing exactly one key: toggle mismatched choices by number (first move Up to an option if cursor is on custom answer / Submit), then clear custom answer (`ctrl+e`, `ctrl+u`), type text, move to Submit / Next and Enter. On review, submit only if all questions answered and confirm review closes.
Respect existing computer selections and mid-list / Submit cursor positions. Multiple-question single-choice still uses numbers; final review is submitted too.

## Questions with previews (2026-11-04, tested 2.1.303)

When AskUserQuestion options include preview, options are on the left and a right-hand box shows the current option preview. Footer: “Enter to select · ↑/↓ to navigate · n to add notes · Tab to switch questions”. No Type something; Chat about this has no number.
Here **numbers move the cursor but do not select**; Enter is required. Number-only answering marked the card answered while the question remained on computer (2026-11-04: phone answer to second question had no effect).

- Screen parser (`server/internal/screen`) recognizes the right-side option box (`┌` `│` `└`) as a preview question. Box contents and Notes: press n to add notes are excluded from labels / descriptions. Previously whole rows became labels; preview changes with cursor caused `prompt_changed` on movement. Each option now uses number + Enter.
- On final question, submit the review as usual (`submitReview` in multiselect section above).
- Deploy hub and node together after parser changes: otherwise preview-question rev differs and `prompt_changed` persists. Nonpreview dialog rev is unchanged.

## Sending messages, interrupting, and keys

- Send: `agent prompt <pane> <text>` (atomic paste + Enter, respecting bracketed paste). While Claude works, input enters its queue.
- Images: save under `~/.persona/uploads/<date>/`, append `<attached_images>` tags with one path per line; tags distinguish them from user text. App shows square thumbnails above text.
  Claude Code's paste turns a line that is only an image path, to a readable image file, into an `[Image #n]` (the model sees the picture) and drops the line,
  so before Claude the node gives each path in the block that ends the message a second line `path:<path>`, which stays text: the agent knows where the file is (`keepImagePaths`).
  No space after the colon: Claude Code before 2.1.316 cuts a pasted line at every space before `/` and attached the picture of `path: <path>` a second time (#4361); without the space it drops the line instead and names the picture's source path to the model itself.
  Blocks a client wrote (Android's composer uploads photos first) get it too. Apps take only lines starting with `/` or `~` as photos.
- Interrupt: `pane send-keys <pane> esc`.
- Any TUI dialog: phone Terminal / screen interaction → `pane send-keys` (`up`/`down`/`enter`/`esc`/`tab`/`shift+tab`/`ctrl+c`/numbers), pane send-text.

## Live screen

For subscribed working / blocked sessions, node runs `pane read --source visible --lines 60` every 400 ms, pushing only changes. App parses:
- Spinner (`✻ Cogitating… (12s · ↓ 3.2k tokens · esc to interrupt)`) → live status bar.
- Lines above composer → output-in-progress preview.
- Numbered options (`❯ 1. Yes` / `2. No`) → dialog buttons.

## Headless assistant (Chat tab)

Assistant does not enter herdr: node directly launches `zsh -ic 'CLAUDE_CONFIG_DIR=<selected profile> <launcher> "$@"' -p --input-format stream-json --output-format stream-json --verbose --include-partial-messages --session-id|--resume <id> --plugin-dir ~/.persona/assistant-hooks --mcp-config …` (`node/assistant_start.go`; hooks are plugins as above, plus a Lark Bash gate; older Claude gets `--settings ~/.persona/assistant-settings.json`):

- Claude CLI starts use the conversation's recorded config directory, including settings/model/compaction reads. Unrecorded old sessions use their actual transcript profile, then the old environment-based fallback. Only new sessions get the private profile default; the service-level environment remains a legacy default. Explicit selection, authentication implications and downgrade preparation: [assistant.md](../assistant/assistant.md), "Claude assistant profile compatibility (#3345)".
- A newly created (or still empty, after an interrupted first start) profile receives only the already installed, stably tagged `agent-bypass-allow` `PermissionRequest` entries found in the node's known Claude configuration directories (#7636). This opt-in is node-wide for the first process: a tagged entry in any known source can supply the seed. If no source supplies one, an unreadable default directory or `~/.claude` remains fail-closed, while an unreadable unrelated profile or session directory is logged and skipped rather than blocking every new profile. A per-node seed lock prevents a concurrent start of the same launcher from running before that write completes. A failed seed write aborts that start, so a later start retries before ordinary transcript state can make the profile ineligible. This closes the first-start window before the extension's periodic profile reconciliation. Mixed matcher groups lose their unrelated entries only in the copy; no credentials, modes, other hooks or other settings are copied, no source file changes, and no source tag means no file. Nonempty existing profiles are never seeded or rewritten. The command keeps the extension as the sole policy evaluator; Persona's Bash `PreToolUse` Lark/user-identity write confirmation still runs first and is not an auto-allow hook.
- Every assistant, guest and meeting-room conversation starts on this command. The node ignores a `runtime` param an older hub still sends (`"agentsdk"`), and a conversation an older node ran on the Agent SDK continues here with `--resume <id>` in the profile its state recorded: the same conversation id, its history kept. One the SDK started on the plain `claude` launcher keeps running the claude executable, as the SDK did (`command claude`), not a `claude` alias or shell function that may set a profile of its own; the first entrypoint on its transcript (`sdk-ts`) tells it apart, as the hub never kept a runtime ([rolling-upgrade-compatibility.md](../protocol/rolling-upgrade-compatibility.md), "Assistant conversations on the CLI only (#9799)").
- Non-guest assistant starts default to `--permission-mode bypassPermissions` when neither the manager nor the conversation selects a mode. Explicit selections remain authoritative on resumed and scheduled turns. The command gate honors bypass without a second tool approval; irreversible actions still require `ask_user`. See `docs/assistant/assistant.md` ("Assistant permission defaults").
- One process supports consecutive turns through stdin, one `{"type":"user","message":{…}}` per line, images as base64 image blocks.
- `stream_event` `content_block_delta/text_delta` supplies word-by-word output → WS delta; result ends the turn. Structured messages still read from transcript.
- **Main and supervisor model policy**: `is_main` and `follows_setting` conversations retain their configured model. Node does not retry quota failures on another model; the existing manager reset wait / durable 429 scheduler handles continuation. Continuations resolve the configured model again, and ordinary sends discard old fallback holds and markers without an availability probe. A tool-search refusal may still restart once on the same model. This does not change Claude Code's own refusal-fallback setting or gateway routing; those remain separate policies.
- **Bounded transient recovery for workers** (#3204, `node/assistant_recovery.go`): a turn ending on an upstream safeguards refusal, 429 / throttling, or a 5xx stays working while node resumes the unchanged request once on the same model (`prompts/assistant/model_retry`, a `<persona-context>` block, not another user bubble). If that attempt also fails, node resumes on the next concrete entry in the launcher's own ordered `/model` list, going on from the top after its last entry; each model gets one same-model retry. Where the launcher declares its models' context windows (a Codex launcher: its catalog as `codex debug models` renders it, `context_window × effective_context_window_percent`, read only when a chain picks a model and kept 10 minutes; Claude Code's lists declare none), an entry whose window is smaller than 1.25 × the conversation's last request (the transcript's context tokens) is skipped, and entries whose window is at least the asked-for model's come first (#10277): a 194k-token conversation once moved from a 1M model to the next entry, a 131k one, and compacted before every step (`assistant_fallback_window.go`). The `default` placeholder, models already tried, and a locked launcher's entries are not fallbacks. The named Agent presets in ModelProfile.Tiers are not this queue.
  The same rule applies to headless Codex's `turn/completed`. A hub/request 400 or any other 4xx but 429, authentication or model-route error, subscription usage limit (including Claude's textual quota errors), over-context refusal, or user interruption is never replayed. The structured kind wins over the words: Claude's failed turn is classified from its transcript (a rejected quota, or Claude Code's notice that it goes on by itself after the reset, is a usage limit; authentication and model routes are the user's), Codex's from `codexErrorInfo`, and only a safeguards refusal is replayed whatever its kind (#3495). Model changes use the ordinary stop-and-resume path, require an explicitly observed empty background-task inventory, and never kill running or not-yet-known background work to make a switch. Claude Code reports that inventory only when it changes, never at start, so before a switch node sends `initialize` (twice: the node sends none of its own, and only a repeated one is answered with the current set, empty included; 2.1.311). The old process stops only after the new start is checked, so a refused switch keeps it; if no eligible model can continue, the original error becomes final. Only then can the manager's scheduled-run watcher record `failed`. The transcript and session retain the model that actually answered, including a fallback.
  The session stays working throughout process replacement, including after the replacement is registered and before its message is delivered. A resident-node handover waits for the active recovery send, then carries the remaining budget and any queued failure to the new owner, a turn's first failure in the gap included; late results follow that owner. The new owner resumes a carried failure only once its session state has loaded; without the state, or when it cannot go on, the failure is final. A user's message cancels a recovery, but gives back the turn's busy only while the recovery still holds it: a delivered recovery turn is answering, and the message waits for it as for any other turn. A refusal sticks to a conversation (the refused turn stays in its transcript), and the main conversation and a scheduled task's ask for the configured model with every message, so a fallback that answered would last one turn (#3788). After a model switch answered, messages asking for the model it moved away from stay on that fallback for 30 minutes, or until the conversation compacts; any other model picked ends the hold, and a resident-node handover carries it. A conversation refused again within two hours of its last fallback skips the same-model retry. A Claude safeguards refusal always skips it: Claude Code has already continued once on the same model ("safeguards stopped the response above · continuing once with that noted"), so a resend would be the third try (#4283). A model the chain switched to that the gateway does not have ("There's an issue with the selected model", `model_not_found`) was the recovery's own pick: it is spent and the chain goes on to the next model; the same failure on the model the user asked for stays theirs to fix. Such a model is also remembered for its launcher for 6 hours, across conversations and resident handovers, so the next chain does not offer it again (#5234). The same failure on a model that answered this conversation within the last hour is the gateway's passing failure, not the user's choice: it is replayed on that model once, then follows the chain (#5234). A usage limit is told by the transcript first: a failed turn written there is a usage limit only by its record (a rejected quota, or Claude Code's notice that it goes on by itself), so a gateway's "upstream account quota usage limit has been reached" 429 is recovered; the words decide only when nothing was written. A current process exiting without a result closes its recovery instead of leaving a permanent working placeholder.
- **Model-choice provenance** (#10909): a switched chain persists the original launcher, effort, effort-presence and execution fingerprint beside `From`; recovery's own writes to the session's current model cannot replace them. A marker written before this identity existed uses the pre-request session only when its model is still `From`; fallback-mutated metadata is not mistaken for the original choice. Exhaustion preserves the identity within its chain, while a new chain captures its own. Cold and live records classify launcher and full-execution changes alike, and an exact old request expires naturally even after the two-hour sticky window.
  A new explicit choice does not end the old hold or marker until its input is actually delivered, so preflight rejection leaves the old chain intact. Each busy process also retains its own chain when a newer request cancels or replaces the conversation-map entry: an explicit pick gets its first same-model retry, while an older `from` or `to` turn keeps its original used and retry budgets, including when newer input joins the busy process. Completing or exhausting the old chain releases that per-process ownership before a queued turn can inherit it. Returning, reset and probe return rules stay unchanged, and releasing unavailable-model evidence is scoped to the recovery's launcher.
- Hooks trigger normally, but **AskUserQuestion is unavailable in -p mode** (No such tool available … nobody in this session can answer it; PreToolUse cannot run). Assistant uses MCP `ask_user` (`docs/assistant/assistant.md`).
- Per-process inline MCP JSON (`--mcp-config '{…}'`), carrying `PERSONA_ASSISTANT=1` and PERSONA_SESSION_ID. `personad mcp` marks the Persona tools the manager did not defer `_meta: {"anthropic/alwaysLoad": true}` for assistant processes, so they load up front while the rarely used ones (all but `coreTools`, `assistant/tool_loading.go`; none while `assistant.load_all_tools` is on) and other MCP servers keep normal ToolSearch deferral (#5838, #7317); `ToolSearch` is in the built-in `--tools` list, which filters it too, so those deferred tools can still be loaded (#5839); the process carries `ENABLE_TOOL_SEARCH=true` on Anthropic's own API, which lifts Claude Code's hold-off for a conversation that ran without ToolSearch, `false` behind a gateway, where `true` would lift Claude Code's own rule that keeps tool search off there, and nothing where the start cannot tell ([Tool search behind a gateway](#tool-search-behind-a-gateway-10365)); `--append-system-prompt` is hub's live snapshot.
- New assistant and guest sessions register only the `persona` MCP namespace when the hub advertises `persona_contracts_v1`; old or unknown hubs and resumed sessions keep both compatibility namespaces. The allow list uses the same selection. Historical tool-name readers continue accepting both names ([rolling-upgrade-compatibility.md](../protocol/rolling-upgrade-compatibility.md), "MCP namespace / tools").
- Spare: Claude waits on stdin for first message, so can launch ahead; new chats use its session ID.
- Launcher change kills process and resumes with new launcher. Idle 20-minute processes are reclaimed; next message resumes automatically.
- Interrupt: no stdin interrupt in -p; terminate process, resume next message.
- With the assistant's `claude_interactive` switch on (#10058, off by default) the same command runs as interactive Claude Code through `personad assistant-tui`, which speaks this stream-json to the node and falls back to it visibly when the interactive start fails: `docs/assistant/assistant.md` ("Interactive Claude Code (#10058)").
- **A round of the background supervisor** (#7315, `node/assistant_round.go`, [unified-entry.md](../assistant/unified-entry.md) "Each round starts clean"): the idle process stops and the round starts as `--resume <id> --resume-session-at <uuid>`, the uuid being the last `end_turn` assistant line of the conversation's first turn (`roundAnchor`), with `--tools Bash,Read,Write,Skill,ToolSearch`, `--strict-mcp-config` and only Persona's MCP server (`PERSONA_TOOL_SET=supervise`). `--resume-session-at` is a print-mode flag Claude Code does not document. Measured on 2.1.322 against a local mock of the model API (`TestAssistantRoundLive`, build tag `roundlive`): the session id and the file stay the same and the round is appended there; the model's request holds the messages up to that line and the round only; a later round does not see an earlier one; a plain `--resume` afterwards goes on from the latest round; an anchor that is not an ancestor of the latest leaf ends the process at once ("No message found with message.uuid of …"), and a compaction makes the first turn no ancestor. Anchoring at the first user message instead merges the start message into the round's own prompt, so the anchor is the end of that turn. The node uses the flag only from 2.1.322 (the version of the running process, else of the transcript's last line, else the machine's), only after finding the anchor on the latest chain, and starts the process once more without it when it does not start. A round's process counts as started only at its `system/init`: a refused anchor prints a `result` line (`error_during_execution`, `num_turns: 0`) on stdout before it exits, which is neither a start nor a turn. With `ToolSearch` left out of `--tools`, Claude Code 2.1.322 sends a `DeferredToolPlaceholder` tool, whatever the conversation started with; with it in and nothing deferred, the request lists only the four other built-ins. The tools stay what the process started with, so the node keeps that set on the process (`assistantProc.toolSet`) and compares it with what each send needs (`supervise_round`: the round's; anything else: every tool): an idle round's process is restarted as a plain `--resume <id>` with the assistant's `--tools`, its extension servers and no `--strict-mcp-config` or `PERSONA_TOOL_SET`, and leaves round state, so it is compacted when idle again. It is replaced only once it settled: not working and silent for `assistantSettle` (1 s), checked and stopped in one step (`stopSettled`). Until then its row says working, so the manager keeps supervision messages for it queued. A message that needs every tool and comes anyway while it runs a turn waits for that turn up to 30 s (`awaitRoundTurn`) and then goes into a plain resume with every tool, a Stop meanwhile ending the round at once and the message going in once that process exited (`stopAwaited`); past 30 s, or with only background tasks running, it goes into the process as Claude Code queues it, runs once on the round's tools, and has the process let go once it settled (`roundSettled`). Work is a turn, background tasks, a compaction (the user's `/compact` sets no busy flag) and a message Claude Code queued (`command_lifecycle` `queued`) or has not reported on yet: measured on 2.1.322, every message with a `uuid`, also one to an idle process, gets `queued` within a millisecond of its write and then `started` (a line without one, such as the node's `/compact`, gets none), so the node remembers a message before writing it and counts it until a lifecycle line says what became of it (only on a process that has printed one). Work is also a turn that has only just started: measured on 2.1.322, a queued message's `started` comes 3 ms after the previous turn's `result` and its first `message_start` seconds later, and every turn begins with a `system/init` (the one Claude Code starts on its own for a finished background task's notification comes 7 ms after a `result`, 26 ms after `background_tasks_changed` emptied the list). So `started` and every `init` after the first mark the process busy until the turn's `result`, and the second of silence, counted from when the line reader is done with a line (a line can wait in the pipe while the reader handles a slow one, a `result`'s transcript refresh), covers the gap before such an `init` (`TestRoundProcessGetsEveryToolForTheNextMessage`, `TestRoundProcessWorkingKeepsSupervisionQueued`, `TestAStartedTurnIsNotCut`, `TestRoundProcessIsNotReplacedWhileTheUserCompacts`, `TestAMessageThatJoinsARoundProcessRetiresIt`, `TestAMessageWaitsForTheRoundsTurnAndGetsEveryTool`, `TestStopWhileAMessageWaitsForTheRound`, `TestAMessageWaitsForAStoppedRoundToExit`, `TestLaterInitIsATurn`, `TestAMessageClaudeCodeHasNotSpokenOfHoldsTheProcess`, `TestALineNotReadYetKeepsTheProcess`).
- First user content in headless transcript is a block array; without ai-title, first sentence becomes title.

## Background tasks (trackBackground in `transcript/background.go`, tested 2026-10-25)

- Start: Bash `run_in_background:true` result is `Command running in background with ID: <id>. Output is being written to: <path>.`; async Agent result `Async agent launched … agentId: <id>`; timed-out foreground commands become `…moved to the background (ID: <id>)`.
- End: `<task-notification><task-id>…</task-id><status>completed|failed|killed</status><summary>…</summary></task-notification>`, in queue-operation enqueue (**not user input, never in queue list**), `queued_command` attachment (commandMode=task-notification, absorbed mid-turn), or idle user message. `TaskStop` / `KillShell` also end tasks.
- Notification forms (about 100,000 records in 2.1.3xx corpus): `Background command "<description or entire command>" completed (exit code N)` / failed with exit code N / was stopped. Monitor sends one `Monitor event: "<description>"` per event (content in `<event>`, task-id only), then `Monitor "<description>" stream ended` (tool-use-id, output-file, final lines in `<event>`). Sub-Agent: `Agent "<description>" finished` / `failed: …`, result in `<result>`. Monitor start tool_result: `Monitor started (task <id>, timeout …ms)`, letting app map task-id-only events to the call.
- **Claude Code itself entity-encodes `>` `<` `&` in summary / event / result** (`cat &gt; f &lt;&lt;'EOF'`); not double encoding by us. Parse with html.UnescapeString. Other corpus notices, such as `local-command-stdout`, have no entities. Background commands without description put the entire command (dozens of heredoc lines) in summary. Node parses `Item.task` (`transcript/tasknote.go`), retaining a one-line label (“Background Command Completed · <first command line>”) and full command in `task.detail` for tap-to-view. Idle user-message notifications, preceded by `system/turn_duration` ending the prior turn, carry `wake: true`. App does not split turns here: choose the longest closing passage across sections before / after wake as the answer, fold others into work row / Background Task Completed drawer (`docs/apple/ios.md`, 2026-10-26).
- Fast session metadata reads only last 384 KB of transcript; a few MB-sized base64 screenshot rows in a long turn push start rows out. Node incrementally scans each transcript (`bgScan`: full file first, appended bytes thereafter, parsing only relevant-keyword rows under 64 KB) into Session.background. Tasks older than 24 hours are treated as gone (Claude restart loses background tasks).

## /loop (tested 2026-10-25)

- Dynamic loop: after each turn, Claude calls `ScheduleWakeup {delaySeconds, prompt:"/loop …", reason}`, result `Next wakeup scheduled for 11:57:00 (in 1859s)`. At due time harness injects prompt as a `/loop …` user message. `{stop:true}` ends it.
- Fixed interval: `CronCreate {cron, prompt, recurring}` / CronDelete. `recurring:false` or fixed day / month expressions (`33 17 21 10 *`) are one-time reminders, not loops.
- Node parses alongside incremental background scanning. `Session.loop` supplies prompt, interval, next wakeup. If 15 minutes past due with no new ScheduleWakeup, treat loop as ended with Claude exit.
- Stop: cannot externally cancel scheduled wakeup; phone Stop Loop sends Agent a message to call `ScheduleWakeup(stop)` / `CronDelete` (`AppModel.stopLoop`).
- api-request bodies also contain tool names in definitions; count only assistant tool_use.

## Renaming (tested 2026-10-25)

- `/rename <name>` writes `{"type":"custom-title","customTitle":"<name>","sessionId":…}` to transcript. Name shows on divider above composer and in `claude --resume` list. custom-title takes priority over ai-title; later ai-title cannot overwrite.
- Herdr: `herdr workspace rename <workspace_id> <label>`, `herdr tab rename <tab_id> <label>`.
- Phone uses hub `…/rename` API. Space names key pins / hidden items; hub migrates those on rename.

## Sub-Agents (2026-10-25, tested 2.1.310)

- Files: `~/.claude/projects/<cwd>/<session>/subagents/agent-<id>.jsonl` (each row `isSidechain:true`, `agentId`, same format as main transcript) + `agent-<id>.meta.json`: `{agentType, description, toolUseId, requestShape:"background"|…, finished}`.
- Nesting (2.1 metadata reviewed 2026-11-04): children launched by sub-Agents live in the same subagents directory. Metadata adds `parentAgentId` (launching sub-Agent ID, absent for main-session children), `spawnDepth` (1 = main session), `isFork` (true for `agentType: "fork"`), `launchedWithDefaults`, `requestNonInteractive`, model.
- Node session ID `<session>@<agent id>` opens child file (`transcript.NewSubagentFile`, preserving sidechain rows), reusing items / watch / pagination. `Session.subagents` lists children: unfinished and written within 20 minutes means running; finished over a day ago omitted, independently per child (omitted parent does not omit children). Background list no longer contains Agents, only shells. Metadata uses only `agentType`, description, `toolUseId`, `requestShape`, finished, `parentAgentId` → `parent_id`, `isFork` → fork. Clients build hierarchy by `parent_id`, not spawnDepth.

## Cross-session messages (tested 2026-10-25)

- Another Claude session sends “Another Claude session sent a message:” + `<cross-session-message from="uds:…" from-name="<name>" from-mode="…">body</cross-session-message>` + English explanation.
  - To an idle session: before 2.1.299, a plain user message. Since then, a queue-operation enqueue / dequeue pair, then a **hidden** user line (`isMeta: true`, `promptSource: "system"`, `turnOrigin: "peer"`, `origin: {kind: "peer", from, name, body, msg_id, …}`) that starts a turn.
  - To a working session: a queue-operation with the same text, then, absorbed, a `queued_command` attachment with the same `origin`.
  - A subagent handing back its report has a peer origin too (`handback: true`), on a hidden line its task notification points at: it stays hidden.
- Parse as a user item with from: the sender's name (`origin.name`, else `from-name`, else 「另一个会话」 `transcript.FromSession`, which the hub localizes) and the body (`origin.body`, else the tagged text). The hidden line's item opens the turn, so the session shows as working while it answers (#6021); it is no prompt of the user's (`LastPromptAt`, the session title).
- Queue text `【来自 名字】正文` (“[From name] body”); phone shows a left-side gray “From name” card, Markdown body like replies, not the user's own message.


## Special XML in transcripts (local and hub scan, 2026-10-26)

| Tag | Handling |
| --- | --- |
| `<command-name>` / `<command-message>` / `<command-args>` | Show as /command arguments |
| `<local-command-stdout>` / `-stderr` | Notice row |
| `<local-command-caveat>` | Model-facing explanation; remove |
| `<task-notification>` | Background-task completion notice (`docs/apple/ios.md`, wakeup) |
| `<cross-session-message from-name=…>` | Message from another session |
| `<teammate-message teammate_id=…>` | Agent-team teammate report, displayed From teammate_id. Hub converts Agent-team JSON notifications to Markdown (`transcript/teammate.go`): `idle_notification` is bold summary, result report body, or error-stop reason; `task_assignment` is Assigned Task #ID: Title + description; shutdown request / approval / denial and plan submission / approval / return each one line. Unknown types / non-JSON remain unchanged |
| `<bash-input>` → `<bash-stdout>` / `<bash-stderr>` | Command run directly with ! in Claude Code: user row ! command, output as Bash tool beneath |
| `<system-reminder>` | Remove |
| `<attached_images>` | Phone image paths, app thumbnails |
| `<persona-context>` | App reference context, displayed as reference card |

Do not push Completed when a background-task wakeup turn ends: node records whether latest turn began with a wake (`Meta.LastTurnWake` → `Session.woke_by_task`); hub `notifyFinished` skips it. Session still becomes Unread, just no iOS notification (requested 2026-10-26).

## Suggested next message (composer hint)

After a turn, Claude Code may draw a dim (SGR 2) next-message suggestion in an empty composer: Tab accepts, Enter sends. **Not in transcript**, only screen: text immediately after ❯ and ESC[2m in `pane read --format ansi` (first character inverse while cursor there). User drafts are not dim; new-session Try "…" is dim but not a suggestion.

- Parser: `ClaudeInput(...).Suggestion` in `server/internal/screen`, multiline joined with spaces; fixture claude-2.1.312-suggestion-171.*.
- Node (`node/suggestion.go`): on Claude working / blocked → idle / done, read visible screen at 0 / 2 / 5 / 10 seconds (2.1.312 suggestions usually appear within seconds). Stop once found, store `proto.Session.Suggestion`; stop if draft exists. Clear on working again or Persona send / key input. No extra high-frequency screen reads.
- Short replies such as “ok” may lack suggestions; replies outlining next steps usually have one (tested “add all three to the README”).

## Refusal fallback notices and unavailable models (#2203)

Claude Code can write `system/model_refusal_fallback` after an upstream refusal, switching from `originalModel` to
`fallbackModel`. With `scope: "session"`, later turns can keep using that fallback even though the launcher and purpose
still name the configured model. A subsequent `model_not_found` / model-route 404 can therefore be a session fallback
failure rather than a configuration error.

- The transcript parser emits every `system/model_refusal_*` record as an ordinary notice, including unknown subtypes.
  `Item.model_refusal` keeps the subtype, original and fallback models, original category, scope, explanation and raw JSON
  record. The hub adds an English, Simplified Chinese or Japanese explanation to the notice without translating or
  replacing the source values. Existing clients display its text; it may appear inside folded work.
- `Session.model_fallback` carries the active observed fallback. Session scope survives later turns; other scopes end
  at the next turn. A real reply from another model or a confirmed `/model` change clears the evidence. Synthetic
  error replies do not count as model changes. The incremental background scan retains this state even when the
  fallback record has moved outside the quick metadata tail.
- A model-route failure while that fallback is active gets `turn_error_code: "model_fallback_unavailable"` and
  `turn_error_kind: "user"`. The original error stays intact. Readiness and work triage explain the refusal → fallback →
  unavailable-model chain, recommending a new conversation or `/model` in a Claude Code terminal. Readiness does not
  point at model settings or block creating a new conversation because of this session-only problem. Automatic work
  continuation and scheduled-run retries do not resend this failed turn.
- Clients (web #4487, iOS #4489, Android #4488) show the refusal as a line of its own after the exchange and a card above
  the composer while the fallback is active. In a Persona conversation, which is for everyday people, both say one plain
  sentence naming the model by its short name ("The upstream refused this request, so it switched to Sonnet 5.5"); the
  models as recorded, the category, the explanation, the refusal's own words and the error since sit under a closed
  Details (#5202, web and iOS). Agents conversations show everything.
- No configuration is required. Claude Code's own session-scoped fallback and the headless assistant's bounded recovery in [Headless assistant](#headless-assistant-chat-tab) are separate mechanisms. The latter handles a final safeguards / 429 / 5xx turn error without rewriting the request; it never replays this section's `model_fallback_unavailable` 404 and does not change Claude Code's own fallback policy. A gateway-shaped managed provider whose model list lacks one of Claude Code's fallback models turns that fallback off at launch instead ([Refusal fallback behind a gateway](#refusal-fallback-behind-a-gateway-7319)), so there the refusal itself ends the turn and the recovery takes it. This section's fallback rows still come from every start that keeps the switch: Anthropic's own API and a gateway whose list has every fallback model, where the fallback model exists, and plain `claude`, shell launchers, providers from before #2455, providers that declare no model list and providers that turn the switch back on, where a gateway without that model still answers 404.

Client follow-up remains necessary for dedicated cards, an always-visible notice, an actual-model/Fell Back marker,
explicit recovery buttons, and suppressing the old error cards' Retry/Continue or model-settings buttons. Clients may
ignore the new structured fields today. Apple also needs a `ChatCache` version bump when adopting the changed item
structure; this server change does not modify Swift. The broader no-fallback refusal policy below remains a proposal.

## Safety blocks and automatic fallback (broader proposal; narrow headless recovery implemented in #3204)

The user asked whether safety blocks and model downgrades are visible in the app. The limited server behavior above is implemented; the broader design in this section remains a proposal, **not implemented**. Items marked Decision Pending require the user's choice; implement the PR split below afterward. The bounded same-request recovery in #3204 is the one exception to this proposal's no-automatic-Continue baseline, not an implementation of its broader policy.

**Principle**: report what happened honestly and never bypass or reduce safeguards: no rewriting user messages or automatic rephrasing retries, and no decisions by the task manager's judging model. The implemented recovery only resumes the unchanged request under the explicit bounds above. Every other blocked turn remains the user's decision, using rules only, never model triage.

### Baseline before #3204

Claude Code (2.1.318 tested, see issue) has two outcomes when safeguards block a message:

| Outcome | Transcript | Current Persona |
| --- | --- | --- |
| Automatic fallback (default) | `system/model_refusal_fallback` row: original model, new model, category, scope; `scope: "session"` makes all later session turns use new model | `transcript.go` system branch recognizes only some subtypes; `default: return nil` drops it. Later assistant rows carry new model, `Meta.Model` → `Session.model` silently changes, and composer model button (`SessionModes.modelName`, web `launcher-models.ts` `modelName`, #2200 / #2201) displays it without explanation |
| Block without fallback | `system/model_refusal_no_fallback` row plus synthetic reply with `stop_reason: "refusal"`, model `<synthetic>`, “`API Error:` …safeguards flagged this message…” | System row dropped; synthetic reply treated as ordinary assistant text, `entry.Message` ignores stop_reason. `API Error:` prefix causes #1516 model-service-error handling below |

Blocked-without-fallback wording currently follows the model-service-error path everywhere:

- Chat: iOS `Views/Chat/APIErrorCard.swift`, web `web/src/lib/api-errors.ts`, Android `core/domain/.../lines/ApiErrors.kt` recognize `API Error:` prefix. Unknown status code becomes Model Request Failed; transient not false offers Retry, sending `继续` (“Continue”, `ChatView.swift`). Unchanged context likely gets blocked again.
- Manager: `assistant/fleet.go` `turnError` uses `assistantwire.CutOff` / `APIErrorMarkers` to classify last sentence as TurnError. Push (`hub/machine.go`) is “⚠️ Title” + “Interrupted by a Model Service Error: <original text>”.
- Tasks: receipt `state: "interrupted"`, summary Interrupted by a Model Service Error (`assistant/tasks_follow.go`, `assistantwire.InterruptedSummary`); Continue card sends `继续` (“Continue”, `continueTask` in `tasks_start.go`); main-chat `prompts/assistant/task_notice.md` says interrupted by model service error.
- Task-manager triage (`assistant/agents_work_turns.go` `triageRules`): `transientError` → `WaitTransient`; Auto Continue **automatically sends `继续` (“Continue”)**, at most maxRetries. Otherwise Model Service Error, Needs Your Attention card. Whether refusal text contains `transientError` keywords (upstream, network, `api_error`…) is unverified. **If so, blocked messages are currently automatically retried**; the first PR must prevent this (Verification Pending below).
- Scheduled tasks / follow-ups (`assistant/schedule.go` `watchScheduledRun`): `lastTurnAPIError` matches final sentence by `APIErrorMarkers` prefixes. Synthetic reply beginning API Error **always** matches, regardless of keywords. Run recorded failed, push Scheduled Task Failed: <original>, **same prompt rerun after 3 minutes** (`docs/assistant/assistant.md`, “Failure and retry”). This automatic resend definitely exists and must also be prevented in first PR.
- To-do task cards: web `web/src/lib/todos.ts` `interruptedAgent` / `interruption()` (`views/assistant/Tasks.tsx`), Android `Tasks.kt` `Interruption.of` and `Models.kt` `Session.interrupted` derive Interrupted from session.turn_error. Unrecognized `parseAPIError` gives Model Request Failed; `canContinue` true offers Continue. Clients currently ignore turn_error_kind.

Local Claude Code 2.1.310 binary lacks `model_refusal_*` / `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK` strings; local and `CLAUDE_CONFIG_DIR` transcripts lack both system rows. Field tables below come from the issue description; **verify names against sanitized real excerpts** before implementing parsing.

### Parsing (node: `server/internal/transcript`)

- Add `model_refusal_*` fields to entry (original / new model, category, scope, using verified real keys); add `stop_reason` to Message.
- `system/model_refusal_fallback` → `KindNotice` with new refusal structure: `{"outcome": "fallback", "from": "<original model>", "to": "<new model>", "category": "<original category>", "scope": "session" | <other original value>, "raw": "<original row content, possibly empty>"}`. Text remains a Chinese sentence (Client wording below), allowing old apps ignoring refusal to show ordinary notice.
- `system/model_refusal_no_fallback` → same notice, `outcome: "blocked"`. Following synthetic `stop_reason: "refusal"` reply produces text item with `refusal: {"outcome": "blocked", …}`, merged with preceding notice into one card, not API error card. **Exclude from `Meta.LastText` as an answer**; record `Meta.TurnError` = original text, `Meta.TurnErrorKind = "refusal"` (new `proto.Session.TurnErrorKind` value alongside transient / user). Detect by `stop_reason` and system row, not English safeguards wording, which can change.
- Synthetic text **removes initial `API Error:` prefix**, preserving full original in refusal.raw. iOS `APIErrorCard`, web `api-errors.ts`, Android `ApiErrors.kt` use that prefix; removing it in node means **old clients also display plain text without Retry**, no update required. Hub `CutOff` / `lastTurnAPIError` also stop treating it as API error. Old clients cannot identify a refusal card, only an English explanation, better than a Retry that gets blocked again.
- Clearing `TurnError`: Claude's `Meta.TurnError` comes from its own error reply (`isApiErrorMessage`, "Session IDs and transcripts" above) and is already cleared on the next turn start or nonsynthetic assistant reply; a refusal reply most likely sets it today with no kind, read off its text. New `TurnErrorKind = "refusal"` must keep that clearing: **clear `TurnError` / `TurnErrorKind` on next real user message or nonsynthetic assistant reply**. Otherwise refusal row still in fast-meta tail makes successful rephrased turns push Blocked, retain triage cards, and leave tasks Interrupted.
- Unknown model_refusal-prefixed subtypes or missing model / category: always show notice with original content, or “Claude's Safety Checks Handled This Message (Persona Cannot Identify the Details)” if absent; leave `refusal.outcome` empty. Unknown categories display raw value; cyber → Cybersecurity, others initially raw.
- Session: add `proto.Session` `model_fallback` `{from, to, category, scope, at}` only when latest fallback has `scope: "session"` and latest assistant still uses to. User `/model` switch removes it. Track in incremental scanning with `bgScan`, without extra file reads.
- Item-structure change requires iOS `ChatCache` directory version bump (currently `chats-v8`; use the latest version on `main` at implementation), per docs/lessons/transcript.md. Update hub golden and `../protocol/protocol.md` Item / Session field tables together.

### Client wording

Use the same copy on iOS / web / Android. Do not put wording in hub Go: notice text is old-client fallback; new clients render refusal fields themselves.

| Location | Automatic fallback | Blocked without fallback |
| --- | --- | --- |
| Chat | Single caution notice, not error red: “Claude's safety checks blocked this message; answering with <new model> (category: <category>)”. Scope session adds “This session will use <new model> from now on” | Caution card, not danger or Error: “Blocked by Claude's safety checks; no answer”, subtitle Category: <category>, collapsed original text and time. **No Retry**. Explanation: “Sending the same content again will probably be blocked; edit it before sending” |
| Composer model button | Actual new model using #2200 / #2201 friendly names, small Fell Back badge (new named design-system token / component, no hardcoding); menu adds Switch Back to <original model> | Unchanged |
| Switch back | Herdr Claude sends `/model <original model alias>` (existing `SessionModes.swift` path, `PromptItems.claudeModelID`). No option for read-only `claude_bg` or headless assistant (assistant model from hub config, next turn launches per config, #1562; verify whether fallback survives `--resume`) | — |
| Push | No separate push; answer and completion push remain normal | “⛔ <title>” + “Blocked by Claude's safety checks; no answer (<category>)”; no original message content |
| Task receipt | Normal receipt / completion rules; append “N turns were blocked by safety checks and answered with <new model>” | `state: "interrupted"` + new `cause: "refusal"`, summary Blocked by Claude's Safety Checks. New clients offer Open Chat, not Continue. No new state: old web `receiptState` maps unknown states to done |
| Main-chat task notice (`task_notice.md`) | Unchanged | Blocked by Claude's Safety Checks (Waiting for User Decision); prompt explicitly forbids rephrasing / resending for user |
| To-do task card | Unchanged | Web `todos.ts` interruption / `todoStatus`, Android `Tasks.kt` Interruption / `Session.interrupted` check `turn_error_kind == "refusal"` first: Blocked by Claude's Safety Checks, **no Continue**, Open Chat |
| Scheduled tasks / follow-ups | Unchanged | `watchScheduledRun` detects refusal-ended turn by `Item.refusal` / `TurnErrorKind == "refusal"`, not text prefix; record blocked, **no retry**. Push Scheduled Task Blocked: <title> + Blocked by Claude's Safety Checks; No Answer (<category>), no original text |
| Task-manager triage | Unaffected | `triageRules` checks `TurnErrorKind == "refusal"` **before** `transientError`: `WaitUser` card Blocked by Claude's Safety Checks: <category>, I'll Handle It (opens chat) / Leave It for Now. No `WaitTransient`, automatic Continue, or judging-model call |

Old clients show notice's Chinese text. Synthetic text without `API Error:` prefix (Parsing above) becomes ordinary text without Retry.
Old web / Android task cards ignore `turn_error_kind` and still show Interrupted + Continue; this needs client updates.
Hub `continueTask` does not reject cause refusal: the user's explicit Continue is their decision, but new clients offer no such button.

### Default policy (decided behind a gateway, pending elsewhere)

Claude Code 2.1.322 reads `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK` from `process.env` each time it checks, and it copies
each settings source's `env` (`--settings` and the user's `settings.json` included) into `process.env`, so a value in
either place turns the fallback off. Persona sets it in both places, so a version that reads only the process
environment sees it too.

#7319 settled the default for gateway-shaped managed providers whose Base URL is not Anthropic's own API and whose model
list lacks one of Claude Code's fallback models: they launch with the fallback off (option B, limited to these
providers), and `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=` (empty) in the provider's environment turns it back on
([Refusal fallback behind a gateway](#refusal-fallback-behind-a-gateway-7319)). Every other start keeps Claude Code's
default, and the options below are still open for those:

- **A. Keep Claude Code's default automatic fallback, only make it visible.** No launch changes; answers continue, but users may not know a weaker model is now used for the session—the Fell Back badge addresses this.
- **B. Disable automatic fallback for all Persona-started Claude.** Block stops work; user chooses rephrase, model change, or abandonment. With increasing refusals, stops and stalled tasks become noticeably more frequent.
- **C. Setting (recommended, decision pending)**: Settings › Claude Code, When Safety Checks Block a Message: Answer with Another Model (default, A) / Stop and Wait for Me (B). Store in hub settings, distribute with node.configure. One shared option for main chat and herdr Agents; split later if needed.

Variable injection when disabled:

- Herdr pane: prepend `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=1 ` to pane run command and herdr `resume_argv` (`claudeCommandLine` in `node/herdrresume.go`). Most launchers are shell functions; env cannot run functions. Assignment + function forwards the variable to launched Claude in zsh / bash; fish differs, as in report-only hooks above. Verify each.
- Managed launchers: `managedEnv` in `node/managed.go`, like `CLAUDE_CODE_DISABLE_ARTIFACT`; personad launch carries it. Done for gateway-shaped providers not on Anthropic's own API whose model list lacks a fallback model, in both the environment and the settings env ([Refusal fallback behind a gateway](#refusal-fallback-behind-a-gateway-7319)).
- Headless assistant: `cmd.Env` in node/assistant_start.go.
- User-started computer sessions are untouched, display only. Setting changes affect only subsequently started / resumed processes; explain in UI.

### Multi-Agent collaboration

- An Agent blocked without fallback stops; task says Blocked by Safety Checks, **not completed or model-service error**. Other Agents on the task keep running, `WaitAgent` dependents keep waiting. Task manager sends no Continue, does not reassign work or rephrase it for another Agent. Main chat receives notice; user decides.
- Automatic fallback does not interrupt flow; record in chat and receipt. `scope: "session"` adds Fell Back to model name in Agents list.
- Main chat itself blocked: same card; scheduled-task / follow-up turn (`docs/assistant/assistant.md`) recorded blocked, not retried; next scheduled time runs normally.

### Compatibility and verification pending

- New system rows, partly internal fields, may be renamed. Use fields only when available; otherwise display original text, without error or dropped row.
- **Verification pending**, before implementation with sanitized real excerpts in `server/internal/transcript/testdata`, text replaced by placeholders:
  1. Actual keys for both system rows, scope / category values; full synthetic-reply shape (always `stop_reason: "refusal"`? always `<synthetic>` model?).
  2. Does refusal text match `transientError` and cause current automatic Continue?
  3. Does `--resume` retain fallback model? Does `/model` restoration cause immediate refusal / fallback next turn?
  4. Behavior with custom `ANTHROPIC_BASE_URL` gateways, API keys, Bedrock / Vertex launchers; whether target model exists at gateway (otherwise ordinary API error).
  5. Earliest `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK` version; assignment + function forwarding in zsh / bash / fish.
  6. Corresponding headless stream-json events (structured content currently read from transcript; events could display earlier).
- Codex, an internal Codex variant, DSH, pi unaffected; their future refusals can use the same refusal structure.

### Implementation PR split (after decision)

1. Server: transcript parser / fixtures, proto `Item.refusal`, `Session.model_fallback`, `TurnErrorKind: "refusal"`; hub push, task-receipt cause, `triageRules` (prevent automatic Continue first), `watchScheduledRun` no-retry + wording, `TurnError` clearing, `task_notice.md` + golden; update `../protocol/protocol.md` and this section to current behavior.
2. iOS / Mac: fallback notices, refusal cards, Fell Back / Switch Back model controls, `ChatCache` version bump; dark / light screenshots.
3. Web: same (separate refusal card outside `api-errors.ts`, `feed.ts` receipts, `todos.ts` task cards read `turn_error_kind`).
4. Android: same, including `Tasks.kt` / `Models.kt` task cards; three styles × dark / light screenshots.
5. Default policy per decision: setting, `node.configure`, three variable-injection locations.

### Acceptance

- Parsing tests for sanitized excerpts of both outcomes; unknown subtype shows original text; normal answer following refusal clears `TurnError`, tested.
- Blocked scheduled tasks / follow-ups are recorded blocked, never retried, tested; task cards say Blocked by Safety Checks without Continue.
- No-fallback block: refusal card, no Retry; push, receipt, main-chat notice, triage all say Blocked by Safety Checks, not Model Service Error; manager sends no automatic messages, tested.
- Automatic fallback: chat notice; `scope: "session"` gives model button Fell Back, Switch Back sends /model.
- iOS, web, Android dark / light screenshots inspected; item-structure change includes `ChatCache` directory version bump.

## Full access by a restart

Claude Code has Allow All (`bypassPermissions`) in its Shift+Tab cycle only when it was started with
`--dangerously-skip-permissions`, `--allow-dangerously-skip-permissions` or `--permission-mode bypassPermissions`
(`bypass_available`, #1379). For any other terminal Claude Code, picking it (the composer's permission picker, `/permissions`,
#6476) does not fail. The node restarts the session in it (`server/internal/node/mode_restart.go`):

- **When**: once the turn is over. A busy agent (working, waiting for the user, still starting, or with background work a
  quit would end, the launcher switch's `switchBusy`) keeps its turn, and no running tool call is interrupted. The session
  list starts the restart as soon as the row is idle, the way it runs a queued ultracode switch. A draft in the box or a
  dialog in front puts it off (retried after 3 s, doubling up to a minute), so nothing is typed over someone at the computer.
- **How**: as a launcher switch restarts an agent. It presses Ctrl+C until only the shell is left, then types the
  launcher's command with `--resume <id> --permission-mode bypassPermissions` in the same terminal, on the launcher, model
  and effort the session runs on, in the conversation's folder. The conversation goes on in the same transcript. Meanwhile
  the row is listed as starting: messages and the user's Resume wait for it, and mode, model and key changes are refused.
  A start that does not come up is followed by one in the mode the agent had (`switch_failed`), or the agent is left
  stopped (`switch_failed_stopped`).
- **What the apps see**: `bypass_pending: true` on the row while the restart waits, with `permission_mode` still the mode
  it runs in. Picking any other mode first drops the restart (picking the mode it still runs in withdraws it: recorded as
  called off, not as a change of mode, #11089), and so does a pick older than two hours (`modeRestartMaxAge`).
- **Clients**: the web (#6476), iOS and Android (#6513) offer the pick wherever their mode menu is, with the note
  "Restarts the session to apply it once this turn ends" under it and in its confirmation, and `/permissions` in the
  composer's "/" list opening that menu with all five modes. While `bypass_pending` is set, their mode buttons keep the mode
  the agent runs in and say the session restarts when the turn ends (iOS: `SessionModes.bypassRestarts` / `bypassNote`,
  `docs/apple/ios.md` "Effort and permission modes"; Android: `docs/android/android.md`).
- **For which process** (#10296): the pick belongs to the claude process it was made for, its pid and start as the hooks
  report them (else its `sessions/<pid>.json`). Once that process has exited, or the session runs in another one (a
  resume at the computer or Persona's Resume), the pick is dropped: a later claude of the same conversation was never
  chosen for Full access and is never restarted into it. Where Persona cannot tell the process, the pick is refused.
- **The record** (#10297): each pick and what became of its restart (done, failed, dropped and why) is kept by the
  machine's manager (`docs/protocol/protocol.md` "Session mode changes").
- **Where**: only a Claude Code Agent in a pane of a terminal host that can say what runs there and start an agent in it
  (herdr), on a launcher Persona can tell, in a folder it can tell. Runtimes, Antigravity, Codex and sessions outside
  Persona's terminals keep the refusal. The restart is kept in the node's memory (shared by a resident node's Nodes), so
  a node restart forgets it rather than restarting the agent later without the user knowing.
