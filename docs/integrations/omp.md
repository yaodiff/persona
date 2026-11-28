# omp (Oh My Pi)

omp (Oh My Pi) is a pi fork with the same extension API, session files and conversation model. Persona reads it as a pi-family
agent (`docs/integrations/pi.md`). In a herdr pane with the Persona extension installed, omp appears in Agents with its state and
chat (text, thinking, tool calls and results, images) and token-by-token output. From the phone you can send messages, interrupt,
rename, start a new omp with a first message, answer its `ask_user` questions and approve the tools you list.

Verified with omp 18.18.x and herdr 0.9.5. The chat, the state and the controls need a herdr engine: the extension names only the herdr
pane it runs in (`herdrPaneOf` in `integrations/pi/lib/pi.js`). Orca lists an omp terminal too (its process scan knows omp), as a
terminal-only row, and New Agent starts omp in an Orca tab; a stopped omp resumes only in herdr.

Added in steps (issue #4148).

In New Agent on web or Android, select the computer's `omp` launcher in herdr or Orca. Enter a model ID such as
`opus` or `openai/gpt-5.2` to use it for this session. You can enter an ID even when the model list is empty.
Leaving the model at Default lets omp read `modelRoles.default` from its own configuration.
An older Persona node offers the launcher default. Upgrade the node on that computer to enter a model ID.
iOS and iPadOS leave the model to omp's own configuration, as described under "Starting from the apps".

## Scope

| Works from the phone | Not supported |
| --- | --- |
| Start `omp` in a new herdr or Orca tab with a first message and an optional model | Setting thinking effort, permission mode or ultracode on that start |
| List running omp sessions and their state, and the stopped ones of the last 7 days | Chat, state and controls in engines other than herdr (Orca lists an omp terminal as a terminal-only row and starts one), and Windows |
| Read the chat, including live text | A web client for omp (the equivalent of pi-web) |
| Send messages, interrupt, rename | Sessions under an `omp --profile` directory |
| Answer `ask_user` questions and `approve` cards | Automatic updates of the Persona extension |

Persona never installs or edits herdr's omp integration and never writes into omp's agent directory. Only the extension below is
yours to install.

## Installation

On the computer running omp (in the hub's machine list, with a node that can run omp):

1. Install the Persona extension in omp from this checkout:

   ```sh
   omp install <Persona>/integrations/omp
   ```

   `omp install` links a local directory instead of copying it, so the extension follows the checkout. To update it, update the
   checkout and restart omp; there is no automatic update. Install only this package into omp: the pi extension in the same omp
   would report the same sessions under pi's identity, and the node keeps one owner per session.
2. herdr 0.9.5 recognizes `omp` panes by itself. herdr's own omp integration (`herdr integration install omp`) is optional: it
   reports the session file of the pane, which gives the node the session id sooner.
3. Restart omp. Its sessions reach the node from then on; no companion process is needed.

### Settings

Optional, in `<agent dir>/persona.json`. That is `~/.omp/agent/persona.json` by default, or `$PI_CODING_AGENT_DIR/persona.json`
when omp keeps its agent directory elsewhere (omp reads the same variable):

```json
{
  "approve": ["bash", "write"],
  "askUser": true,
  "answerFirst": "auto"
}
```

| Field | Default | Description |
| --- | --- | --- |
| `approve` | `[]` | Tools whose calls ask for approval on the phone before they run, such as `["bash", "write"]`. omp has no permission dialog of its own, so nothing is blocked by default |
| `askUser` | `true` | Gives the model an `ask_user` tool (a question, optionally with choices), answered on the phone or in omp's terminal |
| `answerFirst` | `auto` | Who gets approvals and questions first. `auto`: the phone if it drove this session within the last 6 hours, otherwise local first. `phone` and `local` fix one side |
| `socket` | `~/.persona/runtime.sock` | The node's runtime socket, when it is not the default. A full path; a leading `~` is not expanded |

## How a session is identified

The node takes an omp session's id from the first source that has it:

1. **herdr's omp integration**, which reports the session file it wrote for the pane (`agent_session {kind: "path"}`). The id is
   the session id in that file.
2. **The Persona extension in that omp process**, which lists the session with the pane it runs in (`pane_id`, from
   `HERDR_PANE_ID`). The node joins that runtime row and herdr's pane into one session row.
3. **A fallback id**, `omp-<pane>` (for example `omp-w1-p2`), when neither has answered 8 seconds after herdr reports the pane.
   The row is usable at once. When the real id arrives, the node aliases the fallback id to it, so an open chat keeps working
   and the list keeps one row. The pane, its saved state and the launch herdr restores move to the real id with it, so a
   later id that goes missing again leaves the row under the session it runs. Closing the pane drops the alias.

Without the extension a running omp is still listed, with its terminal only. The chat explains that the extension is missing
(`omp install <Persona>/integrations/omp`), and a send is refused with `omp_no_extension`.

## Stopped sessions

When an omp exits, its extension's row goes with it. The node lists omp's recent stopped sessions from omp's own session files,
`<agent dir>/sessions/<encoded cwd>/<time>_<uuid>.jsonl`:

- **What is read.** Only the head of each file (8 KiB): the session line (id, working directory, title) and the title line. A
  row carries the title, the directory and the file's modification time, never the conversation.
- **Which files.** Modified within the last 7 days, newest first, at most 80. The same session in two directories is listed once,
  from its newest file.
- **How often.** A list within 15 seconds of the last scan reuses it. A file is read again only when its modification time or size
  changes. A scan that takes longer than 3 seconds keeps the previous list but remembers the files it read, so the next scans
  go on from there until one finishes.
- **Live rows win.** A running session's own row hides its file. A running omp the node knows only by its fallback id (no
  extension, no herdr omp integration) may be writing any file of its directory, so it hides that directory's stopped sessions
  rather than offer one that is still running. A pane whose session id was reported and then went missing again (its extension
  reconnecting) still hides the session it last reported; a pane that switches to another session counts as new from then on.
  Directories are compared as physical paths, so a pane opened through a symlink counts. A pi pane never hides them: pi writes
  its own agent directory, not omp's.

A stopped row shows a notice in place of its chat (`hub-transcript_text.omp_stopped`).

## Resuming a stopped session

A message to a stopped row resumes it (`server/internal/node/ompresume.go`). The node opens a new herdr tab in the session's
directory, runs `omp --resume <session file>` there, waits until the extension lists the session again, and sends the message
to it. A `session.start` with `resume_session_id` does the same, with its prompt as the message, sent with the start's
images and `client_msg_id` (so the same message sent again under that id is not delivered twice). A session whose
extension listed it and then left with the omp is resumed the same way: a runtime that is gone does not keep it.

- **Which file.** Only one the node found itself: from the scan of omp's session files, or the one herdr's omp integration
  reported while the session ran. The client never names it. The file must be a `.jsonl` file under `<agent dir>/sessions`,
  still exist and hold the session's id; one with an apostrophe in its path is refused, because herdr's resume command could
  not say it. Without a usable file the send is refused with `omp_resume_unknown` and no tab opens.
- **Refused before a tab.** A directory that is gone (`not_running`), an Orca default engine (`orca_resume`), no terminal engine
  running, and a `session.start` for a session a connected runtime or another tool owns (`runtime_session`,
  `open_elsewhere`).
- **One resume at a time.** Two sends or starts at once, or a send racing a start, start one omp: each takes the session's
  recovery lock, so the second waits for the first resume and goes to the omp it started.
- **Never a second omp.** A session whose file a live pane may still be writing is not a stopped one (see "Live rows win"), so
  it is never resumed.
- **Other calls.** A stopped session has no terminal: rename, interrupt, modes, queue edits and close are refused with
  `omp_stopped`.
- **herdr's restore.** When herdr restores a pane Persona started, an omp gets `omp --resume <file>` from its session's own file,
  and no command at all when the node knows none (never `omp resume <pane>`).

The hub puts the refusals into the reader's language (`rpc-errors.omp_resume_unknown`, `rpc-errors.omp_stopped`).

## Starting from the apps

Only the launcher named `omp` starts omp natively (the node lists it with agent `omp`), and a native start sets nothing omp's
own config decides unless a client explicitly selects a supported model override:

- **iOS and iPadOS New Agent.** With an omp launcher picked (`Launcher.isOmp` or the native `Launcher.startsOmp`), Permissions &
  Thinking is not shown (no permission mode, thinking effort or ultracode), no model is sent, and the first message goes with the
  start, as a Codex's does: the node types it into omp's command and answers with omp's session. A message sent after the start would
  reach an omp whose extension has not connected yet and be refused, so the first message is never sent that way. omp takes no
  attachments: there is no add menu, and a photo or file picked before holds Start Chat back ("omp takes no attachments") until it
  is removed.
- **Pushes.** An omp card's push is titled with omp's name: the extension names its cards `omp`, and the hub uses a runtime
  card's own label (`server/internal/hub/push_payload.go`).

## Differences from pi

- **No pi-web and no companion process.** omp has no web client; its sessions live in its own process.
- **Its own agent directory.** `~/.omp/agent`, or `$PI_CODING_AGENT_DIR` when set, whatever its name: the node lists stopped
  sessions from the same directory omp and the extension use. pi's `~/.pi/agent` is not read unless that variable names it.
- **Its own runtime identity and slot.** The runtime is `omp-<pid>`, the apps' name and agent are `omp`, and card ids start with
  `omp`. The extension keeps its host in `__personaOmp` (`integrations/pi` uses `__personaPi`), so an omp and a pi loaded in the
  same process do not collide.
- **Its own resume command.** `omp --resume <session file>`, where pi uses `pi --approve --session`.
- **Its own word for a turn that is over.** omp's pi compatibility layer dispatches the loop's `agent_end` but no
  `agent_settled`, pi's event for a run fully over (its automatic retry, compaction and queued continuation included), so the
  host takes `agent_end` as the turn's end (`settleEvents` in `integrations/omp/lib/host.js`). Without it every session would
  list as working forever after its first turn.

Everything else is pi's code under omp's identity: the extension API, the conversation mapping, approval and question handling,
live text, and the host's record of where a session last ran (`integrations/pi/lib/pi.js`).

## Token usage

The node meters omp's token usage from its session files (`$PI_CODING_AGENT_DIR` or `~/.omp/agent`) like pi's: each assistant message's usage and its `model_usage`
entries (calls outside the conversation), orchestration tokens included, under the agent `omp` (`docs/server/machines.md`, "Token usage").

## Known limitations

- Only omp's default agent directory is read. Sessions started with `omp --profile <name>` live elsewhere.
- A stopped session is resumed only in herdr: an Orca default engine refuses it.
- Only the launcher named exactly `omp` starts omp. A wrapper script or alias around omp is refused as an omp start.
- A new `session.start` with a model passes `--model` and the selected ID to omp. The same choice is available to `agent_start`, `task_plan` and Agent presets. The override lasts for one session and leaves `~/.omp/agent/config.yml` untouched.
- A resume uses the saved session. Asking for a model override on resume, or for a permission mode, thinking effort or ultracode on an omp start, is refused before a tab opens.
- `omp -p`, `--print` and `--mode json` do not register with Persona: a one-shot run has nothing to drive from the phone.

## Validation

- `cd integrations/omp && npm test`: agent directory and `persona.json` parsing, the omp identity (`agent`, `name`, runtime
  `omp-<pid>`), the `ask_user` and one-shot rules, and the slot shared with pi in one process.
- `cd integrations/pi && npm test`: the configurable host identity.
- Node, with fake herdr and fake runtimes: `server/internal/node/omp_row_test.go` covers the family's rows, the missing extension
  and the fallback row; `server/internal/node/omp_alias_test.go` covers the wait for a session id and the alias;
  `server/internal/node/ompdisk_test.go` covers the stopped sessions: the scan and its cache, bad lines, the cap, the offline row
  and its refused send, a slow scan going on where it stopped, and a live omp pane hiding its directory (also through a symlink,
  and after its session id went missing again) while a pi pane does not; `server/internal/node/ompresume_test.go` covers the
  resume from disk, two sends or two starts at once, a start racing a send, a send or start after the extension left, a
  start's images and `client_msg_id`, the refusals, a resume that never comes up and herdr's restore command; `server/internal/node/omp_guards_test.go`
  covers a live pane's file, a start for a runtime's session, the fallback row moving to its session, and the calls a stopped
  session refuses. `server/internal/node/omp_start_model_test.go` checks the new session's recorded model and the arguments
  the terminal receives for default, short and provider-qualified model IDs.
- End-to-end node: `cd server && go test ./internal/node -run '^TestOmpEndToEnd' -count=1` drives `session.start` through
  fake herdr, joins the extension's row, sends through the node, exits to the on-disk row and resumes the exact session file
  into a replacement runtime. The refusal cases cover a missing extension, a session file deleted after the scan and an
  existing file outside the trusted sessions root.
- Opt-in live smoke: `./scripts/test/test-omp.sh` needs herdr, omp, Go and Python 3, builds `TestOmpLiveAttachesToHerdrAndRuntime`
  with the `omplive` tag and checks that a real omp with only Persona's extension joins its isolated herdr pane. It uses a fresh
  HOME, config and persona directory and a named disposable herdr session, clears inherited agent-directory and herdr-config
  overrides, sends no message (so makes no model request), and cleans up its own processes and files. A missing tool prints `RESULT skip` and exits 0; success and failure print `RESULT ok`
  or `RESULT fail`.
- iOS: `apple/PersonaTests/OmpNewAgentTests.swift` covers telling an omp launcher, no modes or efforts and no attachments;
  against `MOCK_OMP=1 PORT=<dedicated port> python3 scripts/mock/mock_hub.py` (which lists an `omp` launcher and refuses an omp
  start that is async or sets those options, as the node does), `PersonaUITests/NewAgentAttachmentsTests/testOmpStartsWithItsFirstMessageAndNoOptions`
  picks omp in New Agent and checks the start's body.
- Hub: `TestRuntimePushTitle` (`server/internal/hub/runtime_test.go`) covers an omp card's push title.
