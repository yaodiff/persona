# Paseo engine

[Paseo](https://paseo.sh) is a multi-Agent manager: a persistent daemon plus desktop, phone, and web apps. It runs Claude Code, Codex, and various ACP Agents
through their SDKs, without terminals, and has its own workspaces (projects and worktrees). In Persona, Paseo sits alongside herdr, Herdr App, and Orca
as a computer's **engine** (`docs/integrations/engines.md`). When a computer uses Paseo, Agents shows that computer's Paseo Agents; creating chats from the phone,
resuming work, and answering cards all happen in Paseo.

The integration differs from the other three: herdr / Herdr App / Orca are terminal-based, and node reads their panes. Paseo has no terminal; node connects through a **bridge**.
The bridge is a runtime (`docs/integrations/runtimes.md`), speaking Paseo's WebSocket protocol on one side and Persona's runtime protocol on the other. Node runs it itself; no separate installation is needed.

```
 Phone / web / Mac ── hub ── node ──runtime.sock── bridge (run by node, integrations/paseo)──WebSocket── Paseo daemon ── Claude Code / Codex / ACP…
                              └── reads transcripts in ~/.claude (Agents running Claude Code)
```

## How to use it

1. Run Paseo on the computer: open the Paseo desktop App, or run `paseo daemon start`.
2. In the App, open Settings › Machines, select the computer, and change "Engine" to **Paseo**. You can also choose it at the engine step during pairing.
3. Wait a few seconds; the Paseo Agents appear in Agents.

Nothing else needs to be installed. The bridge requires Node.js 22 or newer. Node uses Paseo's own copy: the node used by the CLI version, or the desktop App's binary,
run with `ELECTRON_RUN_AS_NODE=1`. A plain `node` always runs the bridge without that variable, even when Node itself was started from an Electron host (such as an editor's extension host) and inherited it.

## Which sessions appear

- **Listed Agents**: all unarchived Paseo Agents, up to 300. Running Agents and those waiting for you are always included.
  An Agent visible in Paseo's own App is also visible in Persona; archived Agents are omitted. Your own Agents come first:
  Agents another Agent created (below) fill what is left of the 300, and one of those that runs or waits for you is listed with
  every Agent between it and yours, whatever the count.
- **Archived Agents** (#4521): an archived Agent of yours that ran Claude Code stays in Persona as its transcript, under Not running (the last 7 days), and a message to it has
  Paseo take it over again. Paseo starts Claude Code's command line itself, so these transcripts read like a script's one-shot `claude -p` runs, which node
  leaves out; the bridge therefore names the Claude sessions of the Agents archived in the last 8 days to node (`unlisted_transcripts`): an Agent that leaves Paseo's list, once Paseo
  says it was archived, and after connecting the ones Paseo's directory still has (read newest first, archived Agents included).
  Not named: an Agent deleted in Paseo (it stays gone), an Agent another Agent created (its creator's chat says what it did), Paseo's own helper runs, which have no Agent,
  and an Agent that keeps its own ID (next). The transcript is listed under its Claude session ID, the ID such an Agent had while it was listed.
- **Archived Agents that keep their own ID** (#4771, #5129): an Agent whose session ID is not its Claude session's (one created or taken over from Persona, `paseo-<agent id>`
  below, or one whose ID the bridge kept through a fork, #6709) is not named that way: listed as its transcript, it would come back under another ID, and everything Persona keeps under its own (a to-do's link, a pin, an archive
  mark, its read state, its sharing owner) would be left behind. The bridge keeps listing it itself instead, as a row not running under the same ID that names its
  transcript (`transcript`), so its content comes from the transcript as for any Claude Agent and node lists no second row for it. These rows come after the 300: the latest
  80 archived in the last 7 days, and those the phone touched lately. They are read from Paseo's archive at connect, so a restarted bridge lists them again once it has read
  it, and they leave the list with the bridge, as the other rows do.
  Codex Agents started from Persona keep their ID in the same way: a valid `persona.session` label and a known Codex thread retain an offline row with
  `codex_thread`, so node does not list that thread again as history. These rows share the same archive limits; distinct threads stay distinct, and a live
  Agent naming a thread takes precedence over an archived row for that thread. Codex Agents started in Paseo itself keep their existing archive behavior.
  Resuming a retained Codex Agent requires its original provider to remain enabled; it never imports that thread through a different provider.
  - A message to one goes to the Agent: Paseo takes it back from the archive first, keeping its labels, and it goes on under the same ID. `session.start` with `resume` set
    to that ID does the same through `import_agent` (see "Take over").
  - While it is archived, interrupting and closing it do nothing, and its permission mode and effort cannot be changed (Paseo changes them only on an Agent it has loaded).
    A rename goes to Paseo's record, which tells no list of it: the row takes the name from the bridge, and from Paseo's archive when the bridge reads it again (every 2 minutes, below).
  - Deleted in Paseo, it leaves the list as below. The bridge uses the existing `transcript` / `codex_thread` fields, without a new protocol feature.
- **Deleted Agents** (#4629): an Agent deleted in Paseo (its App, `paseo delete`) leaves Persona too, and an archived one's Claude session is named no more (or its row leaves). Paseo tells a
  deletion only to the client that asked for it: the bridge sees an Agent that ran since Paseo started go `closed`, and of any other nothing. So it asks Paseo for the Agent
  (`fetch_agent_request`: a deleted one is `agent: null`): 1 s and 5 s after an Agent that ran is closed, and every 2 minutes for the Agents its directory no longer has (the
  directory read again, then one question for each Agent missing from it). A deleted Agent is gone within seconds when it had run, within 2 minutes otherwise.
- **Session IDs**:
  - An Agent running Claude Code (including custom providers with `extends: "claude"`) uses its Claude session ID, the same ID as that chat in herdr or "Not running."
    An Agent created by Paseo itself has no session ID before its first turn, so it is not listed yet.
  - Other Agents use `paseo-<agent id>`.
  - Agents created or taken over through Persona store their ID in the `persona.session` label, which stays unchanged thereafter,
    even if the Claude session changes (`/clear`).
  - **Paseo moving an Agent to another Claude session** (#6709). Paseo forks an Agent's Claude Code session whenever it rewinds it.
    That includes going on after an API error in Paseo's App, because Paseo drops the failed turn first. The fork is a new transcript
    beside the old one, and every record it copied carries `forkedFrom: {sessionId, messageUuid}`. An Agent started in Paseo's App
    keeps the ID it was listed under, so the chat stays open and its cards, read state and links stay with it:
    - **The bridge sees the move**: Paseo updates the Agent with a new Claude session. The bridge keeps the old ID and, at that
      update, writes it to the Agent's `persona.session` label together with `persona.kept: "1"`. The row names the fork as its
      `transcript`, so the node shows the conversation from there. The fork's ID gets no row.
    - **The bridge does not see it** (it was not running): the ID is the first session of the line the Agent's transcript was
      forked from, found by reading the first record of each transcript back through the same project folder. This applies only
      to an Agent that began that conversation itself, meaning its first record is at most a minute older than the Agent. The walk
      never enters a session another Paseo Agent has, listed or archived. An Agent imported, or handed over from another Agent,
      keeps its current ID as before.
      - A first transcript that is gone still names the line, because its ID is in the fork's records.
      - A transcript the bridge cannot read leaves the current ID. The bridge looks in `$CLAUDE_CONFIG_DIR`, in `~/.claude` and in
        any `CLAUDE_CONFIG_DIR` a Paseo provider sets.
      - The label is written at the Agent's next change in Paseo, never while the directory is being read.
      - A branch Persona made ("Branch to new chat" below) copies the records of the conversation as they are, so its first
        record can still name the original's earlier session. It is imported with its own ID on its label, so no line is
        ever read for it. A copy imported in Paseo's App without a label is not followed into a session the original
        still has either.
    - `persona.kept` (also set before a rewind from Persona, "Capabilities") means the bridge kept the ID. Such an Agent is not one
      Persona started, so Persona's own tools still ask for permission in it ("Cards"). Once archived, it stays a row under that ID
      (above) and none of its transcripts is named to the node.
    - Codex and other providers' Agents are `paseo-<agent id>` whatever their thread, so nothing needs to be kept for them.
- **Content**:
  - **Agents running Claude Code**: content is read directly from the Claude transcript on that computer, exactly as for herdr sessions: tool cards, sub-Agents, background tasks,
    compaction, and "what it is doing" are all available. The same transcript does not produce an extra "Not running" row. If the transcript has not been written yet (the first turn has just started), or node does not recognize its configuration directory,
    content uses Paseo's timeline until the transcript becomes available, then switches automatically.
  - **Other Agents**: content comes from Paseo's timeline, converted to the App's rendering format. Commands, file reads / writes, diffs, search, web access, sub-Agents, and to-dos use Claude Code tool names.
- **Agents running Codex** (#4495): the row names the Codex thread the Agent runs (`codex_thread`). Node finds that thread's rollout on its own as well; named, the chat is
  listed once, as the Paseo Agent, not a second time as a read-only Codex history row, and the thread's own Codex sub-Agents hang off the Paseo Agent's row.
  When another program takes the thread up while the Paseo Agent is not running (the Codex App runs it, a shared app-server has it loaded), that program's row is listed too,
  with the thread's sub-Agents: a message from the phone goes where the thread is open.
- **State**:

  | In Paseo | In Persona |
  | --- | --- |
  | Pending request (permission, question, plan) | Needs you |
  | `running`, `initializing` | Working |
  | `idle` / `error`, marked for attention by Paseo (`requiresAttention`: finished / errored and not yet viewed in Paseo) | Unread (Seen once viewed in Persona); the final message for an error is its cause |
  | `idle` / `error`, viewed in Paseo | Seen |
  | `idle` / `error` on an Agent another Agent created in its own workspace (below) | Seen: its creator was told, nobody else has to look |
  | Older Paseo (no `requiresAttention`): `idle` after a turn, `error` | Persona tracks Unread / Seen itself |
  | `idle`: has not run yet | Idle |
  | `closed`: not running, process exited | Not running (Paseo reloads it when a message is sent). While Agents it created have not finished: Working (background), since their reports wake it |

  Read state goes both ways: viewed in Paseo means Seen in Persona (#2831), and seen in Persona clears Paseo's own mark (#6299): when you see a session Paseo flags
  (Unread here), the manager tells the node (`session.seen`), and the bridge clears `requiresAttention` in Paseo. A loaded agent's with `clear_agent_attention`; one Paseo
  has not loaded (Not running) through its workspace (`workspace.clear_attention`), and only when no other agent of that workspace is flagged, since
  `clear_agent_attention` would start a stored agent. One waiting for an answer stays flagged until it is answered.

- **Spaces and workspaces** (#2784, #2802, #4493): the space is Paseo's project; the workspace names where in it an Agent is.
  - **A space is a Paseo project**: a repository's main checkout and its worktrees form one group in the App, named after the project.
    The space ID is Paseo's project-record ID: the `projectKey` field in the location information Paseo gives the Agent contains this ID (not the project's own key).
  - **Separate worktree project records are merged into the main repository**: when only a worktree directory is given to Paseo, it creates a separate project record for it
    (named after the worktree directory). These records are grouped with the main repository's project by its path (`checkout.mainRepoRoot`, compared after resolving symlinks).
    If Persona does not yet know the main repository's project (it has no Agents), the record forms its own group, named after the main repository directory.
  - **A Paseo workspace is the Agent's place label, not a slot**: a workspace is a worktree, or a task in the same directory. Its name goes in `slot_name`:
    the workspace title; if absent, the branch name; if that is absent too, the directory name. The Apps show it beside the Agent ("project › workspace").
    There is no `slot_id`: a slot is one tab, and the Apps draw the Agents that share one as that tab's panes: one row for all of them, each named
    by its Agent rather than its title, and one that stopped running shown as replaced by another there. A workspace's Agents are separate conversations,
    as they are separate tabs in Paseo, so each is a row of its own under the project.
  - Agents without project information from Paseo keep the existing behavior: their workspace is their space, with no workspace name.
  - **Paseo's workspaces are the computer's workspaces** (#6295), while Paseo is one of the computer's enabled engines (a bridge still connected after
    the computer moved to another engine makes, lists and deletes none): the bridge follows Paseo's list of workspaces as it follows its Agents (one
    subscription, `fetch_workspaces_request`) and tells node the whole list when what it lists changes (`runtime.workspaces`,
    `docs/integrations/runtimes.md` ("Workspaces")). Node lists them in the workspace catalog with source `paseo`, Agents in them or not: a folder
    opened in Paseo's App, a worktree whose Agents were all archived. A row's title is the workspace's name, `project_id` its space (the Paseo project
    above), `is_main` true for a main checkout, and a folder outside a repository is no Git workspace. Workspaces being archived and Paseo's background
    ones (the launcher test's, below) are not listed. An Agent's row names its workspace (`workspace_id`), so the clients place it in that row rather
    than by its project or folder.
  - **Starting in a workspace**: New Agent from a Paseo workspace's row starts the Agent in that workspace, Agents in it or not. A workspace Paseo no
    longer has (a stale row) is refused (`workspace_missing`) and nothing starts; the session does not go to another workspace.
    New Agent itself (#7259; iOS, web and Android) lists the chosen computer's Paseo and Orca workspaces by project, main checkout, then
    worktrees, then folders, so one can be picked instead of typing or browsing a folder; the start sends its id as `workspace_id` with
    `host` `paseo`. A computer whose Paseo engine is not enabled, or that is offline, lists none.
  - **New worktree** (#6295; on the web, the ⋯ of a Paseo Git workspace in By repository and the Agents space menu): Paseo makes the worktree, as a
    workspace of its own (`workspace.create.request`), in its worktrees folder: `<worktrees root>/<project hash>/<name>`, the root being Paseo's
    `worktrees.root` setting or `<Paseo home>/worktrees`. The dialog shows that folder (beside a worktree Paseo already made of the repository, else
    derived from the repository's path as Paseo derives it); only the worktree's own folder name can be chosen, and node takes it as Paseo names a
    folder (its `slugify`: lowercase, `[a-z0-9]` runs joined by single hyphens, at most 50 characters), so the checks are on the folder Paseo uses and
    the path made is the one shown. Persona's rules still hold, checked by node with git first: an existing branch is checked out as it is and refused
    while another worktree has it, a new branch starts at the source's HEAD, a folder that exists is refused (an empty one too: Paseo would make
    `<name>-1` beside it), a name with nothing left is refused, nothing is forced. Automatic naming leaves the branch and the folder to Paseo. No
    Agent is created with it (Paseo would rename a new branch after a first Agent's prompt); the dialog's next step is Start Agent there, since a
    Paseo workspace has no terminal to open. A new Agent's own worktree (#6115) and an assistant task's worktree on a computer whose engine is Paseo
    are made the same way: the clients send the engine the Agent opens in (`kind`), and the `-2`, `-3`… of a name too long for Paseo's folders
    shorten the name, not the number. Closing a Paseo Agent offers to remove its own worktree as for herdr (#6297): `worktree/status` calls it
    Persona's when it is the folder of a worktree workspace Paseo made, and `worktree/remove` archives that workspace through Paseo with Delete
    worktree's guards (below).
  - **Terminals for a Paseo Agent's folder**: a Paseo workspace has no terminal, so Open Terminal Here and a dev server's Start for one of its
    Agents open in the terminal engine's workspace that holds the folder, else in a new workspace there, never in one that only happens to be first;
    the web and iOS offer Start only for sessions with a terminal.
  - **Rename and delete** (#6297; on the web, a Paseo row's ⋯ in By repository, and Rename space in the Agents space menu): Rename sets the
    workspace's title in Paseo (`workspace.title.set.request`); its branch, folder and Agents keep their names. Rename space renames the Paseo
    project (`project.rename.request`, its custom name), by the project's id or by one of its workspaces' (Android names a space that way),
    and pins, mutes and to-do links follow the new name. Paseo sends its workspaces again after either but not its Agents' placements, so the
    bridge brings those up to date itself and the rows say the new name at once. Delete worktree is offered on a worktree row and goes as for
    herdr and Orca: a preview of what goes (its uncommitted changes, its branch), a confirmation bound to it (and to the Agents in the
    workspace, which are archived with it), then Paseo archives the workspace (`archive_workspace_request`), which removes a worktree it made
    from the disk, forced, and keeps its branch; "Also delete the local branch if merged" uses Git's safe delete afterwards. Only a worktree Paseo made
    is deleted (its catalog row says `owned`, and only such a row offers Delete): a main checkout, a folder and a worktree made outside Paseo are
    refused at the preview (Paseo would only archive those), as are a row whose folder is gone, one git no longer knows and a folder inside a
    worktree, each with its own reason. Since Paseo removes the folder whatever is in it, neither the preview nor the confirmation goes on while
    one of the workspace's Agents is at work (starting, working, waiting for you) or an Agent of another engine runs in the folder.
- **Agents created by another Agent** (#4494): with Paseo's `create_agent` an Agent starts others (an orchestrator and its reviewers). Paseo marks them with the label
  `paseo.parent-agent-id`, shows them under their creator, notifies nobody when they finish (their creator is told what they did) and archives them with their creator. In Persona:
  - Such an Agent's row names its creator's session (`is_subagent`, `parent_session_id`) while the creator is listed in the same workspace, however many Agents created one another
    in a line; an App that folds subtasks (Android, iOS) lists it under the creator's row. One created from another workspace is that workspace's own Agent, as in Paseo's sidebar: an ordinary row,
    Unread when it stops. Agents that created one another in a ring, and one whose creator is gone, are rows of their own too.
  - While it waits for you (a permission, a question, a plan) it is a row of its own: node lists it without its parent, so every App that folds subtasks by its parent shows it
    and counts it under Needs you, whatever it folds; node names the creator in `asking_parent_session_id` instead, and the Apps that show it as a card in its creator's chat
    keep it there (`docs/protocol/protocol.md` ("Session hierarchy")). Once answered it is under its creator again.
  - Its row says when it was created (`started_at`, Paseo's `createdAt`): the card counts its running time from it.
  - It is never Unread and sends no "done" push: once stopped it is Seen. Paseo sets `requiresAttention` on it as on any Agent, but shows that on no workspace, and nobody opens
    it there to clear it. It can be opened and sent to like any Agent.
  - The creator's row lists the ones that have not finished (running, waiting for an answer, or waiting for Agents of their own) as background work (`background`, kind `agent`),
    so it reads Working (background), and its own "done" push waits until they have reported back.
  - The hub's fleet counts (the Live Activity, Android's notification and widget) and Android's Agents list count your own Agents; one another Agent created counts while it
    needs you.
- **Repository** (#2840): Agents in Git repositories carry `repo_root` (the main repository directory for a worktree, or the checkout itself for a main checkout), as its physical
  directory (symbolic links resolved, #4492), the way node names a session's working tree. "By repository" on all three clients uses it to group
  Paseo worktrees under the main repository. Paseo worktrees live at `~/.paseo/worktrees/<project hash>/<name>`; the path alone does not identify the repository.
  On all three clients, "By repository" names a Paseo worktree by its workspace's title (the `slot_name` its Agents share) instead of its branch; the main checkout keeps its name (#4497; iOS #4768).
- **Streaming**: while the phone is viewing a session, the bridge subscribes to that Agent's timeline and shows text as it is written. Unviewed sessions have no bridge subscription. If a session changes Agents (the original closes and another resumes it), the bridge subscribes to the new one. Non-Claude sessions use the new history to make the phone refetch the whole page.

## Capabilities

| Capability | Paseo | Notes |
| --- | --- | --- |
| List sessions and show state | ✅ | Pushed immediately when Paseo's catalog changes |
| Send messages and images | ✅ | While an Agent is working, messages steer the current turn (Paseo's `steer`, like typing while Claude Code works); resending the same message delivers it only once |
| Interrupt | ✅ | |
| Permission, question, and plan cards | ✅ | Both the phone and Paseo App can answer; the first answer wins. Answering in Paseo removes the phone's card. Tapping "Return to computer" on the phone leaves the answer to the Paseo App |
| Create sessions from the phone | ✅ | Each available Paseo provider is a launcher, with its models, reasoning effort, and permission modes (permission modes: Claude Code's five; for providers on Codex, `bypassPermissions` starts Paseo's `full-access`; for providers whose approval is Paseo's Auto Accept switch (`auto_accept`: the ones on ACP, such as Grok, and OpenCode, #7758), `bypassPermissions` starts it with Auto Accept on; the others leave Paseo's default). A read-only start (`plan`, #3238) runs only on a provider running Claude Code, in Paseo's `plan` mode: a launcher Paseo lacks falls back to such a provider, a non-Claude one is refused (`readonly_unsupported`), and an agent Paseo reports in another mode is archived and refused. Workspace selection: use the workspace explicitly named in the form if it exists in Paseo; otherwise, if the directory is exactly a worktree, use that worktree's workspace (prefer one under the main-repository project, then the most recent), matching Paseo's own directory-based lookup. For a main checkout or ordinary directory, Paseo creates a new workspace under that project |
| Resume a "Not running" Claude session | ✅ | Paseo imports the chat without changing its session ID. If Paseo already has it, reuse it, restoring it first if archived. An archived Agent that keeps its own ID (above) is restored under that ID. Reject if another claude process outside Paseo is using it, to prevent two processes writing at once |
| Rename | ✅ | Changes the Agent's title in Paseo. iOS, Android and web show Rename on a Paseo session from its row's `capabilities.rename` (#6294; not on an Agent another Agent started), with the session's name only (no tab) |
| Close | ✅ | Archives the Agent in Paseo, stopping it and removing it from Paseo's list. An Agent of Claude Code stays under Not running (above); any other leaves Persona's list. iOS, Android and web show Close from the row's `capabilities.close` (#6294) |
| Change permission mode and reasoning effort | ✅ | Claude Code's five modes; on Codex, `bypassPermissions` switches to Paseo's `full-access` and a Codex agent in `full-access` reports `bypassPermissions`; on a provider whose approval is Auto Accept (Grok has no permission modes at all), `bypassPermissions` and `default` turn Auto Accept on and off (`set_agent_feature`, #7758), and an agent with it on reports `bypassPermissions`. One with it off reports no mode, since the agent's own settings may still approve everything (Grok's always-approve). The apps keep the mode chips hidden on these rows: the chips offer Claude Code's modes, and node gives the modes capability to Claude Code rows only; reasoning effort includes only the levels supported by the model |
| Change model | ✅ | The model panel's choice goes to Paseo (`set_agent_model`, before an effort chosen with it, which must be the new model's): Claude Code takes it at once, Codex at its next turn. Only a model Paseo lists for the agent's provider (or one of its aliases); not for an archived agent |
| Rewind | ✅ | Back to before one of your messages (`agent.rewind`), found in Paseo's timeline by its text, its occurrence and how many of your messages came after it: a conversation that moved meanwhile is refused, nothing changed. Claude Code can put the files back too; Codex, pi and omp rewind the conversation only; other providers none. Each row says which (`capabilities.rewind` / `rewind_code`). Claude Code forks its session to rewind: an agent listed under its Claude session id first keeps that id on the `persona.session` label (with `persona.kept`, "Session IDs"), so it stays the same session in Persona. Refused while it works or waits for you, and for an archived agent. A message whose turn was interrupted before Claude Code took it in cannot be a target until Paseo reloads the agent (Paseo: "Cannot rewind before the provider acknowledges the submitted prompt") |
| Test a launcher (Settings › Models) | ✅ | One real prompt through the provider, with the provider's own login (`caps.check`): a probe agent in a background Paseo workspace for the node's empty check directory, in plan mode where the provider has it, without Persona's tools, never listed. Afterwards the probe is deleted, the workspace archived, and the project Paseo made for that directory removed (one Paseo already had stays) |
| Models of a launcher | ✅ | Asked from Paseo when a picker opens (`list_provider_models`); refresh has Paseo look again first (`refresh_providers_snapshot`) |
| Workspaces | ✅ | Paseo's checkouts, worktrees and folders are the computer's workspaces, Agents in them or not; New Agent starts in the one chosen ("Spaces and workspaces" above) |
| New worktree | ✅ (web; iOS from New Agent's workspace) | Paseo makes it in its worktrees folder, as a workspace of its own (`workspace.create.request`), after node's git checks; a new Agent's own worktree too, removed through Paseo when the Agent is closed and you ask |
| Rename workspaces and spaces, delete worktrees | ✅ (web) | A workspace's title and a project's name are set in Paseo; Delete worktree, only for one Paseo made, after a preview the user confirms and never while one of its Agents is at work, has Paseo archive it (folder removed, branch kept). Closing a workspace while keeping its folder stays in Paseo's App |
| Branch to new chat (fork) | ✅ for Claude Code | The node copies the Agent's Claude transcript up to the reply chosen, under a new Claude session id named "分支 · <title>" (#6298). The branch is a session not running; its first message has Paseo take it over (`import_agent`, as in "Take over"), so it goes on in Paseo under that id while the original is untouched. Not while a turn is being written, and not for other providers (Paseo's own `agent.fork_context` only builds a text attachment of the history) |
| Keystrokes, screen reading, live terminal, phone takeover | ❌ | Paseo has no terminal |
| Agent MCP tools | ✅ for new sessions | A new Agent gets whatever tools the hub gives an agent on that computer (see "Create"). Personal machine: the phone tools (`phone_*`), the connectors' agent tools (such as `mail_*`), the agent tools (`report_to_supervisor`, to-do decisions) and, when enabled, the dispatch tools (`dispatch_*`). Work machine: only the agent tools, no dispatch. A new Claude Code Agent has its own Persona tools preapproved (Paseo's `toolPolicy`), so it never asks for them. Taken-over sessions and Agents Paseo created itself are not given them (what they already have stays): Paseo's `import_agent` takes no config |

Login-shell launchers (those in `~/.zshrc`) still appear in the creation form. If selected, use the Paseo provider with the same name if one exists;
otherwise use Paseo's default provider (its built-in Claude Code provider if enabled, otherwise the first provider running Claude Code).
When a named launcher is replaced, the bridge leaves model and reasoning effort unset so the replacement provider uses its own
defaults. This includes a Persona-managed launcher or a resolved Agent preset: the manager can accept that launcher while the
Paseo engine has no enabled provider with the same name. An enabled Codex variant does not automatically stand in for `codex`;
the existing fallback still prefers a Claude provider. Model and effort stay with starts that keep their launcher, or that named
no launcher. Permission mode, workspace and the first message keep their existing behavior.

## How the bridge runs

- **Bridge code**: bundled in personad as runtime package `persona-paseo-runtime` (`internal/runtimepkg`, generated from `integrations/` at build time).
  When the engine switches to Paseo, node writes it beside `runtime.sock` at `runtime-packages/persona-paseo-runtime/<hash>/`, then runs:
  `node bridge.mjs --socket <runtime.sock> --paseo-home <Paseo home> --mcp-config <mcp.json beside runtime.sock> --allow-tools <the Agent's own Persona tools>`.
- **Which Node.js**: the first version 22 or newer, in this order:
  1. `PERSONA_NODEJS`
  2. The one used by the Paseo daemon (the process in `paseo.pid`)
  3. `node` on PATH
  4. Common Homebrew, `/usr/local`, volta, and nvm installation locations
- **Exit and shutdown**:
  - After the bridge exits, node runs it again with exponential backoff starting at 1 second, capped at 1 minute. After 5 minutes of stable operation, the interval resets to 1 second.
  - When switching away from this engine, node stops it: SIGTERM to its process group, then SIGKILL after 3 seconds.
  - Node keeps the bridge's stdin open. If node suddenly disappears (is killed), stdin closes and the bridge exits. This avoids leaving an old bridge holding the runtime ID and blocking the new node's bridge.
- **Logs**: bridge output is written line by line to node's log, prefixed with `paseo bridge:`.
- **node.info**: Paseo appears available in the engine list when the bridge is connected, with Paseo's version. It also appears available if the daemon responds even though the bridge has not connected;
  the bridge startup failure reason goes in `note` (displayed below the engine option in the App).

## Finding Paseo

- **Address**: use only the address in `paseo.pid` under Paseo home (`PASEO_HOME`, default `~/.paseo`), just as Paseo's own CLI does. Never fall back to a default port,
  since another home's daemon might be listening there.
  - No `paseo.pid`: Paseo is not running.
  - `paseo.pid` exists but has no address yet: Paseo is starting.
  - Retry later in either case.
- **Password**: if Paseo has a password, log in using `local-credential` in its home (as Paseo's CLI does). With no password, Paseo skips the check.
- **Declared connection capabilities**: the bridge connects as a `cli` client and declares `owned_subscriptions`, receiving only replies to its own requests and messages from its own subscriptions, not broadcasts.
  It subscribes to four things:
  - Agent catalog
  - Workspaces (#6295)
  - Pending requests and provider changes
  - Timeline of the Agent being viewed
- **Paseo restart (for example, an upgrade)**: the bridge reconnects every second; measured recovery is about 1.5 seconds. Sessions remain in the list during this time.
- **Paseo stopped for over 30 seconds**: the bridge leaves node, and the engine shows as not detected. Claude Code sessions fall back to "Not running," with transcripts preserved.
  Sending a message then reports that Paseo is not connected; nothing is sent.

## Mapping

- **timeline → items**: seq uses Paseo timeline's seq. Content is assigned to seq as follows:
  - Messages, thinking, notifications: the row where they start.
  - Tool calls: the call uses its start row; the result uses its completion row.
  - Text being written is delivered only as streaming text, becoming an item once complete.
  - If Paseo rewrites history (rewind), `history` changes and the App refetches the whole page.
- **Tool names**:

  | Paseo detail | App rendering |
  | --- | --- |
  | `shell` | `Bash` |
  | `read` | `Read` |
  | `edit` | `Edit` |
  | `write` | `Write` |
  | `search` | `Grep` / `Glob` / `WebSearch` |
  | `fetch` | `WebFetch` |
  | `sub_agent` | `Task` |
  | `plan` | `ExitPlanMode` |
  | `todo` | `TodoWrite` |
  | Grok's own tools (#7758): its ACP input names the tool in `variant`, and Paseo passes the call on as `unknown`, named `other` or after the ACP title | `ListDir` → `LS`, `Monitor` → `Monitor`, `TaskOutput` → `TaskOutput`, `KillTask` → `TaskStop`, `UseTool` → `mcp__<server>__<tool>` with the MCP tool's input; any other under its own name. The result is the listing, the background task's output (and its exit code), the message, or the MCP tool's text; otherwise its fields |
  | Other | Original name and unmodified parameters |

- **Cards**:
  - Paseo `question` → question card. Answers for Claude Code use the original question text as the key; other Agents use the `header` from Paseo's card.
  - `plan` → plan card:
    - Approval selects Paseo's `implement`, then switches to the mode selected on the phone. If the previous mode was bypass / auto, use `implement_resume` to return to it.
    - Rejection sends the feedback as the denial reason.
  - One of the Agent's own Persona tools (`--allow-tools`: `report_to_supervisor`, the to-do decision tools, a leader's `task_plan` / `task_review`) asked by Claude Code in an Agent Persona started (it carries the `persona.session` label) → allowed by the bridge once, with no card, no rule kept and no mode changed (#4966, `docs/integrations/claude-integration.md` "Persona tools Agents call without a card"). An agent made in Paseo's own app (also one whose ID the bridge kept, `persona.kept`), and every other tool, still ask.
    Claude Code Agents created since #6293 do not ask at all: they are created with these tools preapproved (below); this answer covers the Agents created before.
  - Everything else → permission card:
    - If Claude Code provides a suggested rule, the card offers "Always allow," stating which rule will be added and where it applies.
    - Denial includes the reason you entered.
  - Card IDs are derived from Paseo request IDs, reconnecting to the original card after a bridge restart.
  - Plan requests Claude Code gives Paseo contain no plan body (`ExitPlanMode` input is empty). The bridge reads the body from that tool call in the timeline.
- **Create**: use Paseo's `agent.create`. The bridge chooses the Agent ID and includes the form's selected model, permission mode (Allow all on an Auto Accept provider as `featureValues.auto_accept`, "Capabilities"), and reasoning effort. The first message is `initialPrompt`.
  - Creating from a workspace places it in that Paseo workspace, Agents in it or not; one Paseo no longer has is refused (above).
  - If Paseo does not finish creation within 25 seconds, return the session row first and keep waiting in the background.
  - **Agent MCP tools** (#3655): the bridge reads the node's `mcp.json` (the one terminal Agents load) at each creation and puts its stdio servers (`personad mcp`) into the request's `config.mcpServers`.
    The tool list is the hub's, as for terminal Agents: it decides per computer (personal machine: phone, connector and agent tools; work machine: agent tools only), and the bridge adds no filter.
    Each server's environment gets `PERSONA_SESSION_ID` set to that Agent's own `persona.session` label (`paseo-<uuid>`, from the first Paseo agent ID of that start), not the provider's session ID, so `report_to_supervisor` and the other
    tools resolve the same task link as for terminal Agents. Paseo keeps `mcpServers` when it reloads the Agent. If `mcp.json` is missing or has no stdio server, the Agent is created without the tools and the bridge logs why.
    Only new Agents are given them: taken-over sessions (`import_agent`) and Agents created in Paseo itself are left as they are.
  - **Own tools preapproved** (#6293): on a provider running Claude Code, the request also carries `config.toolPolicy.preapproved`: the Agent's own Persona tools (`--allow-tools`) of the servers it is given, as `{kind: "mcp", server, tool}`.
    Paseo adds them to Claude Code's allowed tools, so they run without a permission request. Not on Codex (Paseo would limit that server to the listed tools) nor other providers (Paseo refuses the policy there).
    A provider Paseo says cannot preapprove gets the Agent again, once, with the tools and without the policy (the bridge then answers those asks, as above).
    A provider that takes no MCP servers makes Paseo 0.13 refuse the creation (`Provider '<name>' does not support MCP servers`). The bridge then creates the Agent again, once, without `mcpServers`,
    as before this feature, and logs it; no other creation error is retried. The retry simply uses a new Paseo agent ID, but keeps the same Persona session ID (`persona.session` label, `PERSONA_SESSION_ID`) as the first request,
    so the row the phone already got stays the Agent's. Paseo keeps a record of each creation request under `creations/` in its home; the refused request's record stays (failed, no Agent registered), and the bridge does not clean it up.
- **Take over**: use Paseo's `import_agent`, choosing the provider used by the most recent Agent running Claude Code on that computer.
  Set the `persona.session` label to the original session ID; archived Paseo Agents are restored. Paseo finds an archived Agent's record by its provider and Claude session,
  and writes the request's labels over its own: an archived Agent that keeps its own ID is imported with its own provider, Claude session and directory, and that ID as the
  label, so it comes back as itself, under the same ID (#4771).

## Out of scope and known limitations

- No terminal: no keystrokes, screen reading, live terminal view, or phone takeover. The App hides these entry points based on session capabilities.
- Persona's MCP tools (phone, connector and agent tools) reach only Agents created through Persona; taken-over sessions and Agents created in Paseo itself are not given them (what they already have stays; see "Create").
- The MCP server's `command` is the node's own program as an absolute path, stored in the Agent's config and kept by Paseo across reloads. Updates and deploys replace that file in place, so it normally stays valid. If the node's program
  moves or is switched (for example `persona-link node switch`, or a reinstall elsewhere), Agents created earlier keep the old path and lose their Persona tools until they are created again; the bridge does not rewrite a stored config.
- Connect only to the local Paseo daemon. Paseo's relay and its other connected hosts are not integrated. Paseo on another computer uses that computer's own node.
- Closing a Paseo workspace (archiving it while keeping its folder) and deleting a worktree made outside Paseo are left to Paseo's App.
- Paseo answers an archive as done even when it could not remove the worktree's folder (it logs it): Persona's deletion then reads as not known, and the
  folder stays. The workspace is archived in Paseo all the same.
- Before Paseo has made a worktree of a repository, the folder New worktree shows is derived from the repository's path the way Paseo 0.14 derives it. Should a later Paseo
  derive it otherwise, the worktree is still Paseo's and the answer names the folder it is in; only the dialog's folder was off.
- New worktree, Rename and Delete worktree are on the web only: iOS and Android offer them for herdr and Orca workspaces. Android's Rename space renames a
  Paseo space too (by the session's workspace); the web's chat ⋯ offers Rename space for terminal spaces only.
- Images in Paseo's timeline are not displayed. Claude Code sessions are unaffected; their images come from the transcript.
- Paseo's own plugins, scheduled tasks, and hub execution are not integrated.

## Verification

- Unit tests: `cd integrations/paseo && npm test`. Use a fake Paseo daemon (`test/fake-paseo.js`) and the SDK's fake node to test lists, content,
  streaming, sending, cards, creation, takeover, mode changes, workspaces and worktrees, Paseo restart, and disconnection.
- End to end: first `cd server && go generate ./internal/runtimepkg && go build -o /tmp/personad ./cmd/personad`, then run
  `PERSONAD=/tmp/personad CLAUDE_BIN=<a Claude Code executable> node integrations/paseo/e2e/run.mjs`. There are 57 checks; `paseo` must be installed locally.
  Its temporary directories go under `$TMPDIR` when it is set.
  - Start a real Paseo daemon with its own home and an unused port. The provider uses `extends: "claude"`; the model is a scripted
    Anthropic API in `e2e/model.mjs`.
  - Start a real `personad node --stdio`, with the SDK test tools acting as hub. After setting the engine to Paseo, node runs the bridge itself.
  - Test from creation through closing (the session stays listed under its ID), a message that continues it, archiving it again across a restart of the bridge, and resuming it.
  - The launcher test and refreshed models through Paseo (nothing of the probe left), a model switch, a rewind of that session, and a rewind of an Agent started in Paseo (its Claude session forked, its session in Persona the same).
  - Paseo forking an Agent started there by itself, as its App does to go on after an API error: seen by the bridge (the ID pinned as kept) and while the bridge was not running (the same ID from the line of its transcripts).
  - A branch of the session (fork), continued in Paseo under its own id.
  - Paseo's workspaces (#6295): a workspace with no Agents in node's catalog as its main checkout, New worktree in Paseo's worktrees folder (prepare; create: the new branch at
    the workspace's HEAD, a Paseo worktree), the new worktree in the catalog at once, and a session started in it, placed in that workspace.
  - Renaming and deleting them (#6297): the worktree's title and its project's name, set in Paseo and said by the catalog and the session's row, and the
    worktree deleted after its preview (refused while its Agent is at work): Paseo no longer lists it, its folder is gone, its branch stays.
  - A new Agent's own worktree made by Paseo (`worktree.add`): refused for removal while the Agent runs, Persona's once it is closed, and
    removed through Paseo with its branch.
  - Use only temporary directories, without touching the active local Paseo or `~/.claude`.
  - Add `KEEP=1` to retain temporary directories, or `DEBUG=1` to print the bridge lines from node's log.
- Codex archive lifecycle: with the same generated `personad` binary, run `PERSONAD=/tmp/personad CODEX_BIN=<a Codex executable> PASEO_BIN=<a Paseo executable> node integrations/paseo/e2e/codex.mjs`.
  It uses a real temporary Paseo daemon and Codex with a scripted local Responses API, isolated homes and synthetic data. It checks that archiving,
  reconnecting and continuing preserve the Agent and thread identity and leave only one conversation row. It never uses the active Paseo daemon or a real model account.
- Local testing (2026-11-04, Paseo 0.14.0, Claude Code 2.1.294):
  - All 29 end-to-end checks passed (35 on 2026-11-11, with the archived sessions that keep their ID).
  - Read-only validation against a real active Paseo: the bridge connected to a fake node, allowing only read requests. Every available provider became a launcher; all Claude Code sessions pointed to their own transcripts,
    most of which were found locally (the missing ones were in configuration directories unknown to node, so their content used the timeline). No write requests were sent.

## Troubleshooting

- **Engine shows "Not detected"**: Paseo is not running, or `paseo.pid` is not in the home node expects. For a custom home, set `PASEO_HOME` for personad.
- **Engine is available, but the note says "Paseo is running, but Persona cannot connect to it: …"**: the bridge cannot start; the rest gives the reason, commonly a missing Node.js 22.
  Set `PERSONA_NODEJS` to a node executable version 22 or newer.
- **Sessions appear, but content uses Paseo's format rather than Claude's transcript**: node did not find the transcript, for example because the provider uses a different
  `CLAUDE_CONFIG_DIR`. Add that directory to the machine's `machines[].claude_config_dirs` in hub.json (`docs/operations/config.md`).
- **Detailed logs**: look for lines beginning with `paseo bridge:` in node's log.
