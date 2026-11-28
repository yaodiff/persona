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

Persona is tested with Paseo 0.15.0 and newer (#8134). An older Paseo still works, but has known problems (in 0.14, a message sent while a turn starts cancels that turn),
and the engine's note under Settings › Machines asks to update it.

## Pinned workspaces

Paseo's **Pin chat / Pin to top** pins a whole workspace. Persona's Agents workspace tree reads that choice automatically
on web, desktop, Apple and Android: pinned workspaces come first, newest pin first, and show the existing pin mark.
Their repositories move ahead of unpinned repositories so the workspace stays easy to find. Empty pinned workspaces
remain visible; closed, unarchived conversations in a pinned workspace remain available even after Paseo unloads them.
Explicit search and state filters still apply, and manually folded groups stay folded.

Pin or unpin in Paseo. Persona reflects the change when its workspace catalog refreshes (or when the Agents page is
opened again), including after reconnecting. Each pin belongs to that exact computer and workspace; another workspace
in the same project or directory stays independent. Persona's own session/project pins and Assistant directions remain
separate. Reading the catalog never changes either app's preferences.

The bridge carries Paseo's `pinnedAt` as optional `pinned_at` (an RFC3339 timestamp). Clearing it or an older daemon
omitting it means unpinned. The existing catalog snapshot preserves it while its computer is unreachable; a successful
refresh replaces it, including removals. There is no new setting or model dependency.

## Which sessions appear

- **Listed Agents**: all unarchived Paseo Agents, up to 300. Running Agents, those waiting for you, and Agents in natively pinned workspaces are always included.
  An Agent visible in Paseo's own App is also visible in Persona; archived Agents are omitted. Your own Agents come first:
  Agents another Agent created (below) fill what is left of the 300, and one of those that runs or waits for you is listed with
  every Agent between it and yours, whatever the count.
- **Archived Agents** (#4521): an archived Agent of yours that ran Claude Code stays in Persona as its transcript, under Not running (the last 7 days), and a message to it has
  Paseo take it over again. Paseo starts Claude Code's command line itself, so these transcripts read like a script's one-shot `claude -p` runs, which node
  leaves out; the bridge therefore names the Claude sessions of the Agents archived in the last 8 days to node (`unlisted_transcripts`): an Agent that leaves Paseo's list, once Paseo
  says it was archived, and after connecting the ones Paseo's directory still has (read newest first, archived Agents included). Until that read succeeds the
  bridge names none, not an empty list, so node keeps what it was told before; a read that fails is tried again every 2 minutes (#8129).
  Not named: an Agent deleted in Paseo (it stays gone), an Agent another Agent created (its creator's chat says what it did), Paseo's own helper runs, which have no Agent,
  and an Agent that keeps its own ID (next). The transcript is listed under its Claude session ID, the ID such an Agent had while it was listed.
  - Named, it is node's from then on (#8131): node reads it, and a message has Paseo take it over (`import_agent`, "Take over") with that Agent's own provider, directory and
    workspace, so Paseo restores that Agent rather than making a second one beside it. Node used to send every call about it to the bridge, which no longer had it, until
    node restarted and forgot that Paseo had listed it.
  - An Agent of another provider has no transcript to stay as. Archived or deleted in Paseo and opened from an old link, it answers Paseo's not-found
    ("Paseo has no such session"), which the apps show as such, rather than the computer failing to read it (#8131).
- **Archived Agents that keep their own ID** (#4771, #5129): an Agent whose session ID is not its Claude session's (one created or taken over from Persona, `paseo-<agent id>`
  below, or one whose ID the bridge kept through a fork, #6709) is not named that way: listed as its transcript, it would come back under another ID, and everything Persona keeps under its own (a to-do's link, a pin, an archive
  mark, its read state, its sharing owner) would be left behind. The bridge keeps listing it itself instead, as a row not running under the same ID that names its
  transcript (`transcript`), so its content comes from the transcript as for any Claude Agent and node lists no second row for it. These rows come after the 300: the latest
  80 archived in the last 7 days, and those the phone touched lately. They are read from Paseo's archive at connect, so a restarted bridge lists them again once it has read
  it (a read that fails, again every 2 minutes); while the bridge is away they stay, not running, as every row of Claude Code does ("Finding Paseo").
  Codex Agents started from Persona keep their ID in the same way: a valid `persona.session` label and a known Codex thread retain an offline row with
  `codex_thread`, so node does not list that thread again as history. These rows share the same archive limits; distinct threads stay distinct, and a live
  Agent naming a thread takes precedence over an archived row for that thread. Codex Agents started in Paseo itself keep their existing archive behavior.
  Resuming a retained Codex Agent requires its original provider to remain enabled; it never imports that thread through a different provider.
  - A message to one goes to the Agent: Paseo takes it back from the archive first, keeping its labels, and it goes on under the same ID. `session.start` with `resume` set
    to that ID does the same through `import_agent` (see "Take over").
  - Past those limits node still keeps its record of an Agent Persona started (launcher, folder and `engine_runtime`, written when Paseo answers the start and kept by the hub, #10941): an
    extension's resume of it, which the hub hands to the computer once it is not listed, is checked against that folder and goes on through `import_agent` as
    the owner's does (`docs/extensions/extensions.md` "Resuming a chat"). An Agent started in Paseo's App, or before that record, has none: only the owner resumes it then.
  - While it is archived, interrupting and closing it do nothing, and its permission mode, effort and model cannot be changed, nor can it be rewound (Paseo changes them only on an Agent it has loaded).
    Its row offers none of them (#8133): `capabilities` says `rewind`, `rewind_code`, `modes` and `model` false whatever the record it was archived with
    could do, and it lists no `efforts`; node narrows the row's capabilities by them (`docs/integrations/runtimes.md`).
    A rename goes to Paseo's record, which tells no list of it: the row takes the name from the bridge, and from Paseo's archive when the bridge reads it again (every 2 minutes, below).
  - Deleted in Paseo, it leaves the list as below. The bridge uses the existing `transcript` / `codex_thread` fields, without a new protocol feature.
- **Deleted Agents** (#4629): an Agent deleted in Paseo (its App, `paseo delete`) leaves Persona too, and an archived one's Claude session is named no more (or its row leaves). Paseo 0.15
  tells every client of a deletion, archived Agents included (the list's removal, which does not say why: #8129), so the bridge asks Paseo for the Agent
  (`fetch_agent_request`: a deleted one is `agent: null`) and a deleted Agent is gone at once. Should a removal be missed (Paseo 0.14 told a deletion only to the client that
  asked for it), it also asks 1 s and 5 s after an Agent that ran is closed, and every 2 minutes for the Agents its directory no longer has (the directory read again, then one
  question for each Agent missing from it).
- **Session IDs**:
  - An Agent running Claude Code (including custom providers with `extends: "claude"`) uses its Claude session ID, the same ID as that chat in herdr or "Not running."
    An Agent created by Paseo itself has no session ID before its first turn, so it is not listed yet.
  - Other Agents use `paseo-<agent id>`.
  - Agents created or taken over through Persona store their ID in the `persona.session` label, which stays unchanged thereafter,
    even if the Claude session changes (`/clear`). A new Agent's ID is the one the hub named for the start (`session_id`: the
    manager names the sessions it starts) when it is valid and no other session has it, else `paseo-<agent id>` (#8130). Their
    rows say `started_by_persona` (not an Agent whose ID the bridge only kept, `persona.kept` below): the manager finds a start
    whose answer it lost by the named ID, or among these rows by launcher and folder, also after the node restarted in the
    middle of the start, rather than starting a second Agent.
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
    - **The IDs a conversation went by** (#8131). Before #6709 such a conversation was listed under its transcript's ID, and under
      each fork's after it, and Persona's archive (the user's, or closed sessions archiving themselves) kept a record under each.
      A row under an ID of its own (a kept ID, or `paseo-<agent id>`) names, for the manager, its transcript and the sessions that
      one was forked from back to its own ID (`aliases`, node reads them from each transcript's first record). Node lists none of
      them beside it, and the Archived list shows no record under one while that row is listed: the row is the conversation.
  - **Two Agents on one Claude session** (#8131): Paseo moving a conversation to another provider, or importing it again, makes
    a new Agent on the same Claude session beside the old one. The newer one takes the ID of the oldest Agent with that session
    (its label, its kept ID or its Claude session), listed or archived, and is kept on its `persona.session` label with
    `persona.kept` as above. So the conversation stays one row under the ID Persona knows it by, and a message goes to the
    newer Agent, on the provider it was moved to, unless the older one is at work (running, or waiting for an answer), which
    gets it then: no second process writes the transcript. Archived, the one archived last is the row, as for any two
    archived Agents under one ID.
- **Content**:
  - **Agents running Claude Code**: content is read directly from the Claude transcript on that computer, exactly as for herdr sessions: tool cards, sub-Agents, background tasks,
    compaction, and "what it is doing" are all available. The same transcript does not produce an extra "Not running" row. If the transcript has not been written yet (the first turn has just started), or node does not recognize its configuration directory,
    content uses Paseo's timeline until the transcript becomes available, then switches automatically. So does an old Agent whose transcript Claude Code's own cleanup removed.
    A message the phone sent meanwhile keeps its `client_msg_id` on its line in the transcript (#8358; docs/protocol/protocol.md "Matching pending messages").
  - **Other Agents**: content comes from Paseo's timeline, converted to the App's rendering format. Commands, file reads / writes, diffs, search, web access, sub-Agents, and to-dos use Claude Code tool names.
  - **An Agent Paseo let go is read only while its chat is open** (#8132). Reading the timeline of an Agent that is `closed` (Paseo 0.15 lets one go
    after an hour idle) or archived has Paseo load it first, which starts its provider's process again: every timeline request does, and Paseo has no read
    that leaves the Agent stored. The bridge reads such an Agent only for the watch an App sends when it opens the chat (every App does; the web and
    Android read their first page just before it, which is refused, and the watch's backlog then brings the page: both keep the chat loading until it
    comes and say the refusal only if nothing came in 45 s, Android also the hub's error for a backlog it could not read, #8321), or for a send, a
    rewind or a mode change. Any other read (the manager's after a turn, an App's background sync of its recent chats, which pulls them whenever their
    `rev` moves) is answered from the bridge's last whole read of the Agent while that is still all of the conversation, and otherwise refused with
    `not_loaded`, asking Paseo nothing. A chat left open reads nothing more once Paseo lets its Agent go; the next turn, a message's, is read as it runs.
  - **What other Agents send an Agent** (#8220): Paseo hands an Agent another Agent's message, a child's notice (finished, errored, waiting for a
    permission, closed) and a schedule's run as user input in a `<paseo-system>` envelope: XML-escaped, several delivered together separated by blank
    lines. A schedule's run comes in a bare `<paseo-system>`, as the notices did before Paseo versioned the envelope. Node reads them from the Claude
    transcript and the Codex rollout (`server/internal/transcript/paseo.go`); the bridge from Paseo's timeline, which projects them into
    `agent_message` and `heartbeat` tool calls. Each is drawn with what every App already has for Claude Code's cross-session messages and
    subagent notices, never as the user's words:
    - Another Agent's message is a message from another session: `from` is the sender's title (the "another session" stand-in when Paseo knows
      none), the text its words unescaped.
    - A child's notice is a subagent's task note (`task`, kind `agent`, no `task_id`): finished → `completed`, errored → `failed`, a cancel or a
      close → `stopped`, a permission → `waiting`. Its detail is what the permission asks for and the child's last answer; Paseo's server line,
      the request's JSON and its instructions to the model are not shown. Its line (pushes, older Apps) is the node's subagent heading, or
      `子 Agent 等待批准` for a permission, which the hub gives in the reader's language.
    - A schedule's run is a notice: `定时任务到点：` and the schedule's name, or its prompt's first line when it has none.
    - From a transcript, a notice that started a turn is a wake, as a background task's notice is (its turn pushes "done" only as the end of
      held work), and a message absorbed mid-turn is queued. The timeline does not say which, so the bridge marks neither.
    - A bare envelope of another kind is a notice of its first line: Paseo shows any bare envelope as its own, and quotes one a person sends as a
      `user-message`. Text around envelopes, extra space, and an envelope Paseo's own parser would not take stay the user's words, as Paseo leaves them.
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
  | `closed`: not running, process exited (Paseo 0.15 also lets an Agent go after an hour idle) | Not running (Paseo reloads it when a message is sent, or when its chat is opened: "Content"). While Agents it created have not finished: Working (background), since their reports wake it |
  | `closed` while still marked for attention (Paseo lets an idle Agent's process go after a while and keeps its mark) | Unread, from when it finished, as while it was loaded (#8133): Seen once viewed in Persona, which clears the mark in Paseo too (below), and Not running once Paseo no longer marks it. An Agent another Agent created (below): Not running |

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
    `host` `paseo`. A computer whose Paseo engine is not enabled, or that is offline, lists none. A Paseo workspace picked in a Git
    repository still offers "In a new worktree" (#8856; iOS as New Worktree…, Android and web as the switch under it): Paseo is asked
    where its worktree goes (`worktree/status` with `kind` `paseo`) and makes it (`worktree/add`, the same), and the start goes into the
    workspace Paseo made for it (the `workspace_id` `worktree/add` answers), not the one picked. An Orca workspace's worktrees go through
    Orca's own New Worktree.
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
  - **Creation keeps project identity** (#9726, #14844): an explicitly selected workspace supplies its own project ID without
    re-resolving its folder. With a directory alone, the bridge reads the active projects and compares physical paths for the
    directory, Git checkout and main checkout. A main-checkout project wins over legacy projects rooted at the linked worktree
    or its subdirectories. Duplicate active records at the selected physical root (symlink spellings of one directory) resolve
    independent of registry order: the record registered at the physical path itself, then the one with more workspaces, then
    the lowest project ID; matches at different non-main roots are `project_ambiguous` for new creation. When no project matches,
    the bridge hands Paseo the physical path rather than the symlink spelling, so the new project is registered at its canonical
    root. Names and remote URLs are
    never used to merge independent clones. A failed or malformed project list stops new creation. Only a successful list with no
    match allows Paseo to allocate a project. Project discovery is best effort only for an implicit start that already has a
    worktree workspace: on failure or ambiguity it falls back to the original owner-less workspace lookup, and rethrows the
    original error only when that lookup finds nothing.
    A direct Agent start that needs a new workspace uses the same resolver: since `agent.create.request` has no project ID, the bridge
    creates a directory workspace with that ID first, then starts the Agent in it. An exact directory workspace already owned by
    the selected project is reused; with a resolved project, an implicit start never chooses a legacy workspace owned by another
    project. This stays inside the existing pending-start flow; retries reuse its creation key. If the Agent fails later, the directory workspace remains
    available for retry.
    Existing groups and sessions are preserved: Paseo 0.16's public workspace commands/protocol have no move or reparent operation.
    The resolver repairs subsequent implicit selection; it does not migrate previously duplicated project groups or workspaces.
  - **Terminals for a Paseo Agent's folder**: a Paseo workspace has no terminal, so Open Terminal Here and a dev server's Start for one of its
    Agents open in the terminal engine's workspace that holds the folder, else in a new workspace there, never in one that only happens to be first;
    the web and iOS offer Start only for sessions with a terminal. For that, Open Terminal Here sends the Agent's Paseo workspace with the folder
    (the web did not, and its tab went to the first workspace, #8135).
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
- **Native background work** (#9320, Paseo 0.16): provider shell commands and monitors can outlive an Agent's foreground turn.
  The bridge follows `agent.background_work.update` and reads `agent.background_work.list.request` when the daemon advertises
  `agentBackgroundWork`. The session reports these jobs through Persona's existing `background` list, keeping the daemon's task IDs,
  command, description and start time. A positive summary without readable details keeps the session Working until the inventory is
  known. Completed jobs disappear on the next update; reconnects and periodic reads reconcile missed updates without opening the chat
  or loading a dormant provider. A failed read preserves known work. Older daemons receive no background-work requests.
  - A Claude task reported in both its local transcript and Paseo counts once; the transcript keeps its richer local details.
  - Background work in a child keeps its creator active. Worktree deletion is refused while native or delegated work remains,
    both at preview and confirmation. Per-task stopping remains in Paseo's own interface.

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
  - The creator's row lists the ones that have not finished (running, waiting for an answer, running native background jobs, or waiting for Agents of their own) as background work (`background`, kind `agent`),
    so it reads Working (background), and its own "done" push waits until they have reported back.
  - The hub's fleet counts (the Live Activity, Android's notification and widget) and Android's Agents list count your own Agents; one another Agent created counts while it
    needs you.
- **Repository** (#2840): Agents in Git repositories carry `repo_root` (the main repository directory for a worktree, or the checkout itself for a main checkout), as its physical
  directory (symbolic links resolved, #4492), the way node names a session's working tree. "By repository" on all three clients uses it to group
  Paseo worktrees under the main repository. Paseo worktrees live at `~/.paseo/worktrees/<project hash>/<name>`; the path alone does not identify the repository.
  On all three clients, "By repository" names a Paseo worktree by its workspace's title (the `slot_name` its Agents share) instead of its branch; the main checkout keeps its name (#4497; iOS #4768).
- **Streaming**: while the phone is viewing a session, the bridge subscribes to that Agent's timeline and shows text as it is written. Unviewed sessions have no bridge subscription. If a session changes Agents (the original closes and another resumes it), the bridge subscribes to the new one. Non-Claude sessions use the new history to make the phone refetch the whole page.
  A session Paseo takes over while the phone has it open (a message to a Claude session not running) is watched in the bridge from then on: node asks as soon as the takeover answers, so the live text of that first turn shows without opening the chat again (#8130). A session Paseo is still creating or importing (below) is watched once its Agent is there.
  When the connection to Paseo drops, the text being written ends: Paseo tells a new connection neither what was written meanwhile nor that a turn ended, so after the
  reconnect the text starts again with what comes next (#8129).

## Capabilities

| Capability | Paseo | Notes |
| --- | --- | --- |
| List sessions and show state | ✅ | Pushed immediately when Paseo's catalog changes |
| Send messages and images | ✅ | While an Agent is working, messages steer the current turn (Paseo's `steer`, like typing while Claude Code works); resending the same message delivers it only once. A message Paseo refuses after the bridge's 25-second wait (the phone was told it went) is written to the log. A message to a session Paseo is still creating or importing waits for its Agent ("Create", "Take over") |
| Interrupt | ✅ | |
| Permission, question, and plan cards | ✅ | Both the phone and Paseo App can answer; the first answer wins. Answering in Paseo removes the phone's card. Tapping "Return to computer" on the phone leaves the answer to the Paseo App. A card stays on the phone across link drops and goes up again after its 30 minutes while Paseo still waits (Mapping, "Cards") |
| Create sessions from the phone | ✅ | Each available Paseo provider is a launcher, with its models, reasoning effort, and permission modes, named as in Paseo (`display_name`, the provider's label, #8261); one that is not Claude Code's says the modes a start takes (`permission_modes`, as its rows do, below), so New Agent offers it "Launcher default" (no mode sent) and, with Auto Accept or full-access, Allow all, and only the efforts its model lists (the web, Android and iOS read the list, and never send such a start the mode or effort remembered from a Claude Code start; on a computer older than the list, iOS's New Agent offers a provider that is neither Claude Code nor Codex the same from its agent type, #8144); where a start goes to Paseo they are the only launchers offered, and a launcher Paseo does not have runs on its provider of the same kind or is refused ("A launcher Paseo does not have" below, #8140). A Codex provider starts without a first message too (`prompt_optional`) (permission modes: Claude Code's five; for providers on Codex, `bypassPermissions` starts Paseo's `full-access`; for providers whose approval is Paseo's Auto Accept switch (`auto_accept`: the ones on ACP, such as Grok, and OpenCode, #7758), `bypassPermissions` starts it with Auto Accept on, and their launchers say `auto_accept: true` (#8141, "Agents Persona starts" below); the others leave Paseo's default). A read-only start (`plan`, #3238) runs only on a provider running Claude Code, in Paseo's `plan` mode: a Claude Code launcher Paseo lacks runs on one of its Claude Code providers, a non-Claude one is refused (`readonly_unsupported`), and an agent Paseo reports in another mode is archived and refused. Workspace selection: use the workspace explicitly named in the form if it exists in Paseo; otherwise, if the directory is exactly a worktree, use that worktree's workspace (prefer one under the main-repository project, then the most recent), matching Paseo's own directory-based lookup. For a main checkout or ordinary directory, Paseo creates a new workspace under that project |
| Resume a "Not running" Claude session | ✅ | Paseo imports the chat without changing its session ID. If Paseo already has it, reuse it, restoring it first if archived, with that Agent's own provider (#8131). An archived Agent that keeps its own ID (above) is restored under that ID. Reject if another claude process outside Paseo is using it, to prevent two processes writing at once |
| Rename | ✅ | Changes the Agent's title in Paseo. iOS, Android and web show Rename on a Paseo session from its row's `capabilities.rename` (#6294; not on an Agent another Agent started), with the session's name only (no tab). A title given in Paseo (a rename in its App, the title another Agent created it with) stays: Persona's automatic naming replaces only a title Paseo made from the first prompt (#8133). Paseo 0.15.4 records nothing of where a title came from, so node counts a Claude Code session's title as its first prompt (`title_source: "prompt"`) only when it is exactly what Paseo makes of that prompt (the first line with words, white space collapsed, at most 60 characters; a prompt that opens with one of Persona's context blocks is titled after the block's first line), or the folder name the bridge stands in with for an untitled Agent; any other title has no known provenance, which nothing replaces |
| Close | ✅ | Archives the Agent in Paseo, stopping it and removing it from Paseo's list. An Agent of Claude Code stays under Not running (above); any other leaves Persona's list. iOS, Android and web show Close from the row's `capabilities.close` (#6294) |
| Change permission mode and reasoning effort | ✅ | Claude Code's five modes; on Codex, `bypassPermissions` switches to Paseo's `full-access` and `default` back to its provider's own default mode (the `defaultModeId` Paseo lists for it, `auto`, or `auto-review` where its reviewer is on; else the agent's first mode short of full access, #8261), and a Codex agent in `full-access` reports `bypassPermissions`; on a provider whose approval is Auto Accept (Grok has no permission modes at all), `bypassPermissions` and `default` turn Auto Accept on and off (`set_agent_feature`, #7758), and an agent with it on reports `bypassPermissions`. One with it off reports no mode, since the agent's own settings may still approve everything (Grok's always-approve). Paseo lists no features for an Agent it has not loaded (closed, after Paseo restarts, a change to its stored record), though the switch stays on: the row keeps the state the bridge last saw (#8133); a bridge started while Paseo has not loaded the Agent sees none until Paseo loads it, and a switch has Paseo load it first. One rule on every client for the providers that are not Claude Code's (#8261): their row lists the modes it takes (`capabilities.permission_modes`: `["default","bypassPermissions"]` with Auto Accept or full-access, `["default"]` for the others, none once archived), and the apps offer a two-state switch where it lists two, "Launcher default" (not "Ask": the provider's own settings may approve everything) and Allow all, which asks first. `capabilities.modes` stays false on these rows, since the Claude Code chips offer modes the bridge refuses, so an app older than the list shows nothing there. Reasoning effort includes only the levels supported by the model: the row of an Agent that is not Claude Code's lists its model's levels (`efforts`, the thinking options Paseo lists for it that are Persona's), so iOS offers the effort alone there as web and Android do (#8133) |
| Change model | ✅ | The model panel's choice goes to Paseo (`set_agent_model`, before an effort chosen with it, which must be the new model's): Claude Code takes it at once, Codex at its next turn. Only a model Paseo lists for the agent's provider (or one of its aliases); not for an archived agent |
| Switch a running Agent to another launcher, or between Claude Code and Codex | ❌ | The node refuses it for every runtime's session (`switch_unsupported`): the web's model panel lists the other providers off with why, and switches only the session's own provider's model (#8142) |
| Rewind | ✅ | Back to before one of your messages (`agent.rewind`), found in Paseo's timeline by its text, its occurrence and how many of your messages came after it: a conversation that moved meanwhile is refused, nothing changed. Claude Code can put the files back too; Codex, pi and omp rewind the conversation only; other providers none. Each row says which (`capabilities.rewind` / `rewind_code`). Claude Code forks its session to rewind: an agent listed under its Claude session id first keeps that id on the `persona.session` label (with `persona.kept`, "Session IDs"), so it stays the same session in Persona. Refused while it works or waits for you, and for an archived agent. A message whose turn was interrupted before Claude Code took it in cannot be a target until Paseo reloads the agent (Paseo: "Cannot rewind before the provider acknowledges the submitted prompt") |
| Test a launcher (Settings › Models) | ✅ | One real prompt through the provider, with the provider's own login (`caps.check`): a probe agent in a background Paseo workspace for the node's empty check directory, in plan mode where the provider has it, without Persona's tools, never listed. Afterwards the probe is deleted, the workspace archived, and the project Paseo made for that directory removed (one Paseo already had stays) |
| Models of a launcher | ✅ | Asked from Paseo when a picker opens (`list_provider_models`); refresh has Paseo look again first (`refresh_providers_snapshot`) |
| Workspaces | ✅ | Paseo's checkouts, worktrees and folders are the computer's workspaces, Agents in them or not; New Agent starts in the one chosen ("Spaces and workspaces" above) |
| New worktree | ✅ (web; iOS, Android and web from New Agent's workspace) | Paseo makes it in its worktrees folder, as a workspace of its own (`workspace.create.request`), after node's git checks; a new Agent's own worktree too, removed through Paseo when the Agent is closed and you ask |
| Rename workspaces and spaces, delete worktrees | ✅ (web) | A workspace's title and a project's name are set in Paseo; Delete worktree, only for one Paseo made, after a preview the user confirms and never while one of its Agents is at work, has Paseo archive it (folder removed, branch kept). Closing a workspace while keeping its folder stays in Paseo's App |
| Branch to new chat (fork) | ✅ for Claude Code | The node copies the Agent's Claude transcript up to the reply chosen, under a new Claude session id named "分支 · <title>" (#6298). The branch is a session not running; its first message has Paseo take it over (`import_agent`, as in "Take over"), so it goes on in Paseo under that id while the original is untouched. It goes on with the original's provider: the node keeps the launcher with the branch and names it in the takeover (#8130), not whichever provider ran last, maybe another account's. Not while a turn is being written, and not for other providers (Paseo's own `agent.fork_context` only builds a text attachment of the history) |
| Keystrokes, screen reading, live terminal, phone takeover | ❌ | Paseo has no terminal |
| Agent MCP tools | ✅ for new sessions | A new Agent gets whatever tools the hub gives an agent on that computer (see "Create"). Personal machine: the phone tools (`phone_*`), the connectors' agent tools (such as `mail_*`), the agent tools (`report_to_supervisor`, to-do decisions) and, when enabled, the dispatch tools (`dispatch_*`). Work machine: only the agent tools, no dispatch. A new Claude Code Agent has its own Persona tools preapproved (Paseo's `toolPolicy`), so it never asks for them; an Agent of any other provider that Persona started has its asks for them answered by the bridge ("Cards", #8141). Taken-over sessions and Agents Paseo created itself are not given them (what they already have stays): Paseo's `import_agent` takes no config |

**Agents Persona starts** (#8141, the owner's decision of 2026-11-14): the assistant's `agent_start` and its planned tasks (the plan
card, the start, retries) run their Agents with Allow all wherever a Paseo provider can: in `bypassPermissions` on a provider running
Claude Code, as everywhere, and with Auto Accept on for a provider whose approval is that switch (its launcher says `auto_accept`),
read-only tasks too, held to reading by their first message as Claude Code's are
(`docs/assistant/todos.md` "Read-only planned tasks"). Such an Agent would otherwise stop for every tool, `LS` and
`report_to_supervisor` included, on a card the assistant never answers. Paseo's Codex providers start as Codex does in a terminal:
in the task's settled mode (`docs/assistant/todos.md` "Permission of a planned task"), so in `full-access` only when that mode is
`bypassPermissions`, and a read-only one keeps `plan`, which only a provider running Claude Code holds here. A provider with neither
modes nor the switch (pi) keeps its own approval: nothing Persona sends reaches it. An Agent a person starts from New Agent keeps
the mode they chose, and Persona's own tools never ask in any Agent Persona started ("Cards").

## A launcher Paseo does not have

Paseo runs every Agent with a provider of its own. Where a start goes to Paseo (its engine host is Paseo: the computer's
default, or the one a start names), the owner's rule of 2026-11-14 applies (#8140). Before it, a launcher Paseo lacked ran on
Paseo's default provider with that provider's own model and effort (#6192): a Codex launcher started Claude Code, a gateway's
launcher ran on another account, and a provider added in Persona never applied its gateway or key.

- **List only what can run.** New Agent lists Paseo's providers, each with its own models and efforts, in Paseo's order (its
  default first), and what starts elsewhere whatever the host (another runtime's launchers, Claude's desktop app, the Codex
  App). The login shell's launchers, the providers added in Persona and plain `claude` are not offered: Paseo would run one of
  its own in their place. The node answers `launchers.list` for that host (`host_runtime`), and the manager adds nothing to it
  (`docs/protocol/protocol.md`, `launchers.list`). The main chat's menu still lists the computer's own launchers: the assistant
  runs them on the command line, whatever the engine. Paseo's providers are not among them (the Paseo runtime does not declare
  the assistant): the menu shows each as unavailable, "Can start Agents … but can't run assistant chats". Where an assistant
  runtime on the computer declares it runs the assistant for one (the Grok companion's `--assistant-for grok`,
  `integrations/grok/README.md`), that entry names it instead ("For assistant chats, choose Grok"); the user picks it.
- **A name both have is Paseo's provider there.** On such a computer a launcher of the login shell's (or one added in Persona)
  named like a Paseo provider means that provider: its models, its Test in Settings › Models, its start options and the
  start. In a terminal engine the shell's launcher of that name runs, as before.
- **Substitute openly, within the same kind.** A preset, a task, Model configuration › Agents or a stale list that names a
  launcher Paseo does not have (or a provider of Paseo's that is switched off or failing) gets one of Paseo's providers of the
  same kind of agent: its built-in provider of that kind (`claude`, `codex`) when it is on, else the first of that kind in
  Paseo's order. Persona chooses it and says so where the start was asked for: a task's why and plan card (the manager picks
  it when the card is made, or at dispatch), `agent_start`'s confirmation and answer, and New Agent's note (`launcher_note`,
  which the web shows), e.g. "Paseo on <computer> has no “codex”, so this runs on “my-codex”, an agent of the same kind". The
  model the start named stays only when that provider offers it; otherwise it runs its own default model, and the note says
  so. The effort goes with it.
- **Refuse when there is none of that kind.** The start is refused with the reason (the manager names Paseo's providers there;
  the bridge's code is `launcher_kind_missing`), and nothing runs in its place: never another kind of agent, never Paseo's
  default, never another account unsaid. A read-only (`plan`) start also needs a provider of the kind that holds Paseo's plan
  mode, else `readonly_unsupported`.
- **Lower an unsupported effort and say so.** An effort the model does not offer goes down to the nearest lower one it
  offers (`xhigh` → `high`; with none lower, the lowest it offers; with no level of Persona's at all, the model's default).
  The session's effort is the one it runs at, and the note says so. An effort for a model Paseo lists no levels for passes as
  asked.
- **How.** The node tells the bridge the kind of agent a launcher it does not have starts (`agent_type` on `session.start`,
  since the bridge declares `caps.same_kind`; from a node that does not say, the bridge reads the kind from the name as the
  node does: `pi`, `cursor`, `agy`, `codex`, `traex` and `omp` by name, any other Claude Code); the bridge picks the provider and the effort, and its answer says what it ran
  in place of what was asked (`instead`), which the node passes on with the session. The hub turns it into the app's note,
  the manager into the task's; launcher health and a picked model's use are recorded under the launcher that ran.
- **A computer with a terminal engine and Paseo**: the start's host decides. In the terminal the login shell's launchers run
  as before; in Paseo, its providers alone, as above.

## How the bridge runs

- **Bridge code**: bundled in personad as runtime package `persona-paseo-runtime` (`internal/runtimepkg`, generated from `integrations/` at build time).
  When the engine switches to Paseo, node writes it beside `runtime.sock` at `runtime-packages/persona-paseo-runtime/<hash>/`, then runs:
  `node bridge.mjs --socket <runtime.sock> --paseo-home <Paseo home> --mcp-config <mcp.json beside runtime.sock> --allow-tools <the Agent's own Persona tools>`.
- **Which Node.js**: the first version 22 or newer, in this order:
  1. `PERSONA_NODEJS`
  2. The one used by the Paseo daemon (the process in `paseo.pid`; see "Finding Paseo" for when it is trusted)
  3. `node` on PATH
  4. Common Homebrew, `/usr/local`, volta, and nvm installation locations
  5. On a Mac, Paseo's desktop App: `/Applications/Paseo.app/Contents/MacOS/Paseo`, then the same under `~/Applications`, run with `ELECTRON_RUN_AS_NODE=1`.
     So a Mac with only the App installed finds it before Paseo runs, or when the daemon's process cannot be read.

  The node's log names the one chosen at each start: `paseo bridge: Node.js 22.20.0, the Paseo daemon's: <path>` (where it was found: `PERSONA_NODEJS`, `the Paseo daemon's`, `PATH`, `installed` or `Paseo's app`).
- **Exit and shutdown**:
  - After the bridge exits, node runs it again with exponential backoff starting at 1 second, capped at 1 minute. After 5 minutes of stable operation, the interval resets to 1 second.
  - When switching away from this engine, node stops it: SIGTERM to its process group, then SIGKILL after 3 seconds.
  - Node keeps the bridge's stdin open. If node suddenly disappears (is killed), stdin closes and the bridge exits. This avoids leaving an old bridge holding the runtime ID and blocking the new node's bridge.
- **Logs**: bridge output is written line by line to node's log, prefixed with `paseo bridge:`. Node also keeps the bridge's last 50 lines for the engine's note.
- **node.info**: Paseo appears available in the engine list when the bridge is connected, with Paseo's version; a Paseo older than 0.15.0 gets a `note` asking to update it.
  It also appears available if the daemon responds even though the bridge has not connected, and `note` (displayed below the engine option in the App) says why (#8134):
  - The bridge could not start (no Node.js 22, say), or it exited: the error it printed last, such as Node.js's `SyntaxError: …` or the bridge's `uncaught: Error: …`
    (up to three lines of its message, without the stack), rather than its exit status. While the next run starts, the note keeps that reason.
  - The bridge has run for 30 seconds without connecting: the last line it printed, such as Paseo refusing its connection.
  - Node writes the note in Chinese from the hub's `node-engine` catalog, and the hub words it in each reader's language (`docs/protocol/protocol.md` "Engines").
    Like the rest of the engine list, it is what node reported when it last connected to the hub: `node.info` is asked once per connection (`docs/integrations/engines.md`).

## Finding Paseo

- **Address**: use only the address in `paseo.pid` under Paseo home (`PASEO_HOME`, default `~/.paseo`), just as Paseo's own CLI does. Never fall back to a default port,
  since another home's daemon might be listening there.
  - No `paseo.pid`: Paseo is not running.
  - `paseo.pid` exists but has no address yet: Paseo is starting.
  - Retry later in either case.
- **Its Node.js** (#8134): the bridge runs on the daemon's own Node.js (the process `pid` in `paseo.pid` names) only while the daemon answers at that file's address, and only when that
  process's executable is a Node.js (by its name: `node`, `nodejs…`, `node-…`) or an Electron app (its framework in the Mac app bundle the file is in, or `resources/app.asar` beside it elsewhere).
  A `paseo.pid` left behind by a Paseo that crashed, or from before a reboot, names a process that may now be any program of yours, and finding Node.js runs what it finds with `-e …`.
  - The executable is the kernel's: `/proc/<pid>/exe` on Linux, the executable path of `KERN_PROCARGS2` on macOS (`node/update_inventory_argv_darwin.go`).
    Never the process's name: Paseo's processes rename themselves ("Paseo Supervisor"), which is also what `ps -o comm=` shows.
  - Otherwise node goes on down the list ("How the bridge runs"); a Mac with only the Paseo App finds the App's binary.
- **Version**: Persona is tested with Paseo 0.15.0 and newer. The version comes from Paseo's answer to the bridge's hello (`server_info`); below 0.15.0 the engine still works, and its note asks to update Paseo.
- **Password**: if Paseo has a password, log in using `local-credential` in its home (as Paseo's CLI does). With no password, Paseo skips the check.
- **Declared connection capabilities**: the bridge connects as a `cli` client and declares `owned_subscriptions`, receiving only replies to its own requests and messages from its own subscriptions, not broadcasts.
  Its hello names no app version: Paseo reads one as a version of its own app and decides what a client gets by it (#8129).
  It subscribes to four things:
  - Agent catalog
  - Workspaces (#6295)
  - Pending requests and provider changes
  - Timeline of the Agent being viewed
- **Keepalive**: the bridge pings every 10 seconds and calls the connection dead after 25 seconds with nothing from Paseo, counting only the time it was awake to hear:
  after the computer slept (or the bridge was held up), it pings and waits for the answer first (#8129). Its log says who ended a connection: the bridge
  (`the bridge closed the connection (<code>)`: no answer to its pings, none to its hello, its setup failed), Paseo (`Paseo closed the connection (<code>)`),
  or nobody (`the connection to Paseo dropped (1006)`, as when Paseo is killed).
- **Paseo restart (for example, an upgrade)**: the bridge reconnects every second; measured recovery is about 1.5 seconds. Sessions remain in the list during this time.
  - At each connect the bridge reads Paseo's Agents and workspaces a page at a time beside their subscriptions. What a subscription says of an Agent or a workspace
    while the pages are read (a turn that ended, one made, archived or deleted) wins over a page's copy, which can be older (#8129): Paseo sends what changed while it
    built the first page right after that page, and the end of a turn leaves an Agent's `updatedAt` as it was, so the copies cannot be told apart by time.
  - Every 2 minutes the directory is read again: an Agent whose copy differs from Paseo's is taken as Paseo has it then (an update lost), unless Paseo said something
    of that Agent while the pages were read. A page lists no features of an Agent Paseo has not loaded, so the features last seen stand in for the comparison too
    (otherwise every read would take such an Agent again).
- **Paseo stopped for over 30 seconds**: the bridge leaves node, and the engine shows as not detected. Claude Code sessions stay listed under their IDs as "Not running"
  (#8131), the archived ones' too, read from their transcripts, until the bridge is back or for as long as the list of sessions not running goes back (7 days); the same
  after switching the computer to another engine. Sending a message, renaming or closing one then reports that Paseo is not connected; nothing is sent. Agents of other
  providers leave the list until Paseo is back. A node that restarts while Paseo is down lists none of them until Paseo is back: what the bridge listed is in node's memory
  only, and Paseo's transcripts read as a script's one-shot runs.

## Mapping

- **Providers → agents** (#8142): an agent of Claude Code is `agent: "claude"`, one of Codex `"codex"`; every other
  provider (Grok, Kimi, Cursor, TRAE, pi, OpenCode…) runs under Paseo's own id, `agent: "paseo"`, with its provider as
  `launcher` and the provider's label in Paseo as `agent_label`. `paseo` names the engine, never an agent: the apps call
  and draw such a row by its provider. The web (`lib/agent-kind.ts` `providerAgent`) takes the provider as its agent:
  the mark the web has for it (Cursor, TRAE, Pi, OpenCode, Gemini, by the provider's id or its label), else its
  label's letter; its label as its name on cards, pushes, the model pill and panel, subagent cards and the composer's
  source line; one Agents filter entry and one model-panel engine per provider; one Harness choice per provider in the
  work-session form. The web offers nothing the node refuses on a runtime's session: no other launcher or engine in
  the model panel, no "Copy resume command", and its queued messages are only shown (no Send now, Edit, Delete or
  reorder: `session.queue.*` is refused). Its launcher carries the label as `display_name`, so every client's pickers
  name it the same way (#8261). Session info of such an agent shows no counts: Paseo sends no usage for it, and the node
  says so (`usage_unknown`) rather than the apps showing "0 tokens, 0 turns".
- **timeline → items**: seq uses Paseo timeline's seq. Content is assigned to seq as follows:
  - Messages, thinking, notifications: the row where they start.
  - Tool calls: the call uses its start row; the result uses its completion row.
  - Text being written is delivered only as streaming text, becoming an item once complete.
  - If Paseo rewrites history (rewind), `history` changes and the App refetches the whole page.
  - The bridge's own words are Chinese, as node's own lines are, and the hub rewrites them in the reader's language (#8133, `docs/protocol/i18n.md`
    "Follow-up work"): an error's notice and the last words of an Agent that stopped on one (`出错了：` and the cause, `出错了` alone when Paseo says
    nothing more), a failed command's exit code after its output, and a call cancelled before it gave anything back. A compaction is node's own
    notice with `compact`, which every App draws as its divider in its own language.
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
    - Approval selects Paseo's `implement`, then switches to the mode selected on the phone. Paseo's `implement_resume`, offered when the agent was in bypass or auto
      before it planned, goes back to that earlier mode, whatever the phone chose: it is used only when the phone chose that same mode (its label names it, #8128).
    - Rejection sends the feedback as the denial reason.
  - One of the Agent's own Persona tools (`--allow-tools`: `report_to_supervisor`, the to-do decision tools, a leader's `task_plan` / `task_review`) asked in an Agent Persona started (it carries the `persona.session` label) → allowed by the bridge once, with no card, no rule kept and no mode changed (#4966, `docs/integrations/claude-integration.md` "Persona tools Agents call without a card"). An agent made in Paseo's own app (also one whose ID the bridge kept, `persona.kept`), and every other tool, still ask.
    Whatever the provider (#8141), each asking in its own words: Claude Code by the tool's name (`mcp__<server>__<tool>`); an ACP agent by the call its card shows (Grok names the tool in its input, `UseTool`); OpenCode by its own name for the tool, `<server>_<tool>`; Codex in an approval of the bridge's server whose message names the tool (Codex 0.167: `Allow the <server> MCP server to run tool "<tool>"?`). A request that names the tool no such way still asks.
    Claude Code Agents created since #6293 do not ask at all: they are created with these tools preapproved (below); this answer covers the Agents created before, and every other provider's.
  - Everything else → permission card:
    - If Claude Code provides a suggested rule, the card offers "Always allow," stating which rule will be added and where it applies.
    - Denial includes the reason you entered.
  - Card IDs are derived from Paseo request IDs: after a node restart the next bridge asks with the same ID, and the hub hangs that on the card the phone already shows.
    A card put up again (below) has an ID of its own.
  - A card says whose it is by the session's row: Claude Code's is Claude's card, any other the card of the agent the row
    names, by the row's label. The node takes the row's `agent_label` whenever it has one, so a Grok agent's push reads
    "Grok needs your permission", not "Paseo…" (#8142).
    iOS (#8144) takes a card's own label. Where a card says only "Paseo" (an older node's fallback), it takes the
    session row's label, else the row's launcher (Paseo's provider id); the row's mark, its share card and its child card
    name it the same way. Android (#8143) names no one on such a card ("Waiting for your approval").
  - Plan requests Claude Code gives Paseo contain no plan body (`ExitPlanMode` input is empty). The bridge reads the body from that tool call in the timeline.
  - **Across link drops** (#8128): while Paseo waits on a request, its card is on the phone, whatever happened to the links in between ("each pending request has one
    home", `docs/design/DESIGN_SYSTEM.md`).
    - **The hub link drops** (the computer sleeps, its network changes): the card stays on the phone. It carries the bridge's own ID, so the hub keeps it as `resumable`
      (Codex, ACP, OpenCode and pi cards too, which have no `toolUseId`), and the node answers the bridge `node_gone`, not a dismiss. The bridge asks the next hub
      with the same card for 2 minutes from the drop, however long the card was up before, and an answer given meanwhile reaches Paseo. A request that comes up
      while there is no hub goes to the phone once the hub is back. If the hub was away longer than it keeps a card (3 minutes), it ended the card: its answer for
      it comes at once, and the bridge puts up a new card.
    - **The bridge's link to Paseo drops**: an answer given meanwhile waits in the bridge and goes to Paseo when it is back, if Paseo still asks; the card does not go
      up a second time. A card stays pending until Paseo confirms its answer (`agent_permission_resolved`), so an update of the agent in between does not show it again.
    - **Nobody answers for 30 minutes**: the card's time on the phone runs out, but Paseo keeps waiting, so the bridge puts up a new card (and the phone is notified
      again) while Paseo still lists the request. DSH instead keeps the phone's card when its own timed question runs out first and hands a late answer back to its
      agent (`docs/integrations/runtimes.md` ("Interactions")); here the phone's card is the one with a time limit, so it is the one renewed.
    - **"Return to computer"** is the only dismissal that leaves a request to Paseo's app: one made while the card is up, the hub there since it went up and before
      its 30 minutes are over. A dismissal that comes at once is the hub's answer for a card it had ended (a person takes longer), so the bridge puts up a new card,
      except for a card it has just made, which was never on the hub. A hand-back holds until the request is answered.
- **Create**: use Paseo's `agent.create`. The bridge chooses the Agent ID and includes the form's selected model, permission mode (Allow all on an Auto Accept provider as `featureValues.auto_accept`, "Capabilities"), and reasoning effort (one the model offers: "A launcher Paseo does not have"). The first message is `initialPrompt`; with none, Paseo creates the Agent idle.
  - The apps start a provider with no first message and send it once the start is answered, as an ordinary message that can carry photos and files (iOS and the web; Android since #8143): such a start has no `initialPrompt`. A Codex provider too, since its launcher is `prompt_optional` (#8140).
  - Creating from a workspace places it in that Paseo workspace, Agents in it or not; one Paseo no longer has is refused (above).
  - **Answered before Paseo is done** (#8130): if Paseo does not finish creation within 25 seconds (a provider slow to start, Paseo
    restarting), the start answers with the row the session will have, `starting`, and goes on in the background for up to 10
    minutes. That row is named as the listed one will be: a provider the apps do not know carries its label (`agent_label`), so
    the apps never call it "Paseo" while it starts (#8142). The bridge lists that row until Paseo has the Agent, whose own row then
    takes over under the same ID; its chat is empty meanwhile rather than an error. A message sent to it meanwhile waits for the
    Agent (answered as queued when that is after the call's wait), and so does a watch, which then follows the Agent. When Paseo
    refuses after all (no Agent, or a read-only start that came up in another mode, which is archived), the row turns not running
    with `start_error` ("Didn't start", with Paseo's reason) for 10 minutes, as the node's own failed starts do, and the log says
    why. A message to it is refused, and closing it removes the row.
  - **A dropped connection makes no second Agent** (#8130): every creation request carries an `idempotencyKey`,
    `persona:<session id>:<attempt>` (the creation again without tools, below, is another attempt). Paseo 0.15's creation service
    keeps a request by its key across connections: asked again with the same request, it answers with the Agent it made, or waits
    for the one it is still making (another request under the same key is refused, `agent_request_key_conflict`). When the
    connection drops before Paseo answers, the bridge waits until Paseo is back and its directory read, takes the Agent the
    directory lists under the session's ID if there is one, and asks again otherwise; within the 25 seconds the start answers as
    usual, later as above. Before, the phone was told the start failed while Paseo made the Agent, and a retry made a second one.
  - **Agent MCP tools** (#3655): the bridge reads the node's `mcp.json` (the one terminal Agents load) at each creation and puts its stdio servers (`personad mcp`) into the request's `config.mcpServers`.
    The tool list is the hub's, as for terminal Agents: it decides per computer (personal machine: phone, connector and agent tools; work machine: agent tools only), and the bridge adds no filter.
    Each server's environment gets `PERSONA_SESSION_ID` set to that Agent's own `persona.session` label (the ID the hub named, else `paseo-<uuid>` from the first Paseo agent ID of that start), not the provider's session ID, so `report_to_supervisor` and the other
    tools resolve the same task link as for terminal Agents. Paseo keeps `mcpServers` when it reloads the Agent. If `mcp.json` is missing or has no stdio server, the Agent is created without the tools and the bridge logs why.
    Only new Agents are given them: taken-over sessions (`import_agent`) and Agents created in Paseo itself are left as they are.
    An A2A ticket creates the additional `persona_a2a` server from the current `persona` configuration, or from the legacy MCP namespace when an older generated `mcp.json` still has only that key. This is a read-only compatibility fallback; no new legacy server is generated.
  - **Own tools preapproved** (#6293): on a provider running Claude Code, the request also carries `config.toolPolicy.preapproved`: the Agent's own Persona tools (`--allow-tools`) of the servers it is given, as `{kind: "mcp", server, tool}`.
    Paseo adds them to Claude Code's allowed tools, so they run without a permission request. Not on Codex (Paseo would limit that server to the listed tools), nor OpenCode (Paseo 0.15 takes a policy there, but then leaves that agent's Auto Accept off), nor other providers (Paseo refuses the policy there): the bridge answers their asks for these tools (above).
    A provider Paseo says cannot preapprove gets the Agent again, once, with the tools and without the policy (the bridge then answers those asks, as above).
    A provider that takes no MCP servers makes Paseo 0.13 refuse the creation (`Provider '<name>' does not support MCP servers`). The bridge then creates the Agent again, once, without `mcpServers`,
    as before this feature, and logs it; no other creation error is retried. The retry simply uses a new Paseo agent ID and idempotency key, but keeps the same Persona session ID (`persona.session` label, `PERSONA_SESSION_ID`) as the first request,
    so the row the phone already got stays the Agent's. Paseo keeps a record of each creation request under `creations/` in its home; the refused request's record stays (failed, no Agent registered), and the bridge does not clean it up.
- **Take over**: use Paseo's `import_agent`, with the provider the takeover names when it runs Claude Code (the launcher the session ran
  on, which node keeps: a branch's is its original's provider, #8130), else the provider used by the most recent Agent running Claude Code on that computer.
  Set the `persona.session` label to the original session ID; archived Paseo Agents are restored. Paseo finds an archived Agent's record by its provider and Claude session,
  and writes the request's labels over its own: an archived Agent that keeps its own ID is imported with its own provider, Claude session and directory, and that ID as the
  label, so it comes back as itself, under the same ID (#4771). So is an archived Agent whose Claude session the bridge named to node (#8131): with the provider of the most
  recent Agent, Paseo made a second Agent on that session beside the archived one.
  - **An import that takes long** (#8130): node's call does not cut the import off. When Paseo takes longer than 25 seconds (a long
    conversation, a provider slow to start), the takeover answers with the session's row, `starting`, its transcript still the
    conversation, and goes on in the background (up to 10 minutes). The message that follows (node sends it right after the
    takeover) waits for the import and goes to the Agent once Paseo has it; a second takeover meanwhile is the same import. One
    Paseo fails after the wait leaves the row not running with `start_error`, and the message that waited is not sent; the next
    message takes the session over again, and once the row is gone it is a session not running again (node does not take a session
    a runtime listed only while starting it for one of the runtime's). An import
    asked again after a dropped connection takes the Agent Paseo's directory has for the session by then (Paseo imports a session
    once, and refuses the next import of it). Should the bridge leave node meanwhile, the session is kept as not running, as Paseo's
    other Claude conversations are, not as starting. Before, a 25-second limit failed the takeover while Paseo went on, and the message was lost.

- **Rich answers** (#8855): Paseo tells the agents it runs to draw tables, charts, stats, progress, checklists, steps
  and choices as ```` ```paseo-ui ```` blocks of one JSON object. The hub writes each complete, readable block as a
  ```` ```persona-view ```` of the same parts on its way out (`server/internal/hub/card_paseo_ui.go`, beside the ring
  downgrade in `cardCompatItems`), so every app draws it in any chat with no release; the transcript keeps the block.
  Past the apps' three cards a reply, or before one of the reply's own `persona-view` blocks, a block becomes its
  plain-text fallback; an unreadable or unclosed one stays code, as Paseo shows it. Buttons are drawn as the choices to
  reply with: card actions answer only in the assistant chat. While a reply streams, its block shows as code until the
  finished item arrives.

## Out of scope and known limitations

- No terminal: no keystrokes, screen reading, live terminal view, or phone takeover. The App hides these entry points based on session capabilities.
- Persona's MCP tools (phone, connector and agent tools) reach only Agents created through Persona; taken-over sessions and Agents created in Paseo itself are not given them (what they already have stays; see "Create").
- New Agent's list follows the computer's default engine (#8140): the apps ask for the launchers once per computer. On a computer with a
  terminal engine and Paseo, picking the other one in "Start in" keeps the default's list; a launcher the chosen engine does not have then
  goes by "A launcher Paseo does not have" (the same kind, said, or a refusal) in Paseo, and as before in the terminal. The hub's
  `GET /api/v1/machines/{mid}/launchers?host=` already answers per host for an app that asks again when the host changes.
- On a computer whose engine is Paseo, a name the login shell and Paseo both have is Paseo's provider in an Agent picker's models and Test.
  Assistant pickers send `purpose=assistant` for model discovery and Test, which use the shell launcher or the runtime that actually runs
  assistant conversations. Older clients without that parameter still receive the Agent engine's models and Test result.
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
- The bridge keeps "Return to computer" in memory: after it restarts (a node update restarts it), a request handed back before goes to the phone once more,
  since the hub's answer for the old card, at once, reads like that for a card it ended ("Cards" above). A card the next bridge takes up again after a node
  restart ends at the first bridge's 30 minutes, which the next one does not know: if nobody answers until then, that end counts as a hand-back.
- Paseo's own plugins, scheduled tasks, and hub execution are not integrated.

## Verification

- Unit tests: `cd integrations/paseo && npm test`. Use a fake Paseo daemon (`test/fake-paseo.js`) and the SDK's fake node to test lists, content,
  streaming, sending, cards, creation, takeover, mode changes, workspaces and worktrees, Paseo restart, and disconnection. The fake loads an Agent it let
  go when its timeline is read, as Paseo does, and keeps which (`loads`). It holds a new subscription's updates while it builds the first page and sends
  them right after it, as Paseo 0.15 does, so a test can change things while a page is read (`duringPage`) and see what a reconnect leaves (#8129).
- End to end: first `cd server && go generate ./internal/runtimepkg && go build -o /tmp/personad ./cmd/personad`, then run
  `PERSONAD=/tmp/personad CLAUDE_BIN=<a Claude Code executable> [PASEO_BIN=<a Paseo executable>] node integrations/paseo/e2e/run.mjs`. There are 65 checks;
  without `PASEO_BIN`, `paseo` must be installed locally. Its temporary directories go under `$TMPDIR` when it is set. A check waits, with a
  limit, for the state it asserts (the list the node pushes once the bridge is gone, a row naming its forks), rather than reading the
  last list once: node pushes its list a moment after what changed it (#8358).
  - Start a real Paseo daemon with its own home and an unused port. The provider uses `extends: "claude"`; the model is a scripted
    Anthropic API in `e2e/model.mjs`.
  - Start a real `personad node --stdio`, with the SDK test tools acting as hub. After setting the engine to Paseo, node runs the bridge itself.
  - Test from creation through closing (the session stays listed under its ID, offering no rewind, mode or model change while archived), a message that continues it,
    archiving it again across a restart of the bridge (meanwhile still listed and read, a message refused as Paseo away, #8131), and resuming it.
  - The launcher test and refreshed models through Paseo (nothing of the probe left), a model switch, a rewind of that session, and a rewind of an Agent started in Paseo (its Claude session forked, its session in Persona the same).
  - What a start in Paseo can run (#8140): the list for Paseo's host is its providers alone, and a launcher Paseo lacks (Claude Code's built-in provider, off there) runs on its provider of the same kind, an effort its model lacks lowered, both said on the answer.
    That start has no first message: the one sent to it next, before Claude Code has written a transcript, keeps its `client_msg_id` on its line once the session reads the transcript (#8358).
  - An Agent started in Paseo and archived there: its transcript a session not running, read by node, and a message has Paseo take that same Agent back, with no node
    restart in between (#8131).
  - Paseo forking an Agent started there by itself, as its App does to go on after an API error: seen by the bridge (the ID pinned as kept) and while the bridge was not running (the same ID from the line of its transcripts), each row naming its fork in `aliases`.
  - The engine switched away at the end: Paseo's Claude Code sessions stay listed, not running, readable, and a message is refused as Paseo away (#8131).
  - A branch of the session (fork), continued in Paseo under its own id, its live text shown while it was open on the phone (#8130).
  - Paseo's workspaces (#6295): a workspace with no Agents in node's catalog as its main checkout, New worktree in Paseo's worktrees folder (prepare; create: the new branch at
    the workspace's HEAD, a Paseo worktree), the new worktree in the catalog at once, and a session started in it, placed in that workspace.
  - Renaming and deleting them (#6297): the worktree's title and its project's name, set in Paseo and said by the catalog and the session's row, and the
    worktree deleted after its preview (refused while its Agent is at work): Paseo no longer lists it, its folder is gone, its branch stays.
  - A new Agent's own worktree made by Paseo (`worktree.add`): refused for removal while the Agent runs, Persona's once it is closed, and
    removed through Paseo with its branch.
  - A start under the session id the hub named (#8130): that id in Paseo, and a row saying Persona started it. Paseo's creation asked
    again with the same idempotency key gives the same agent and no second one, and another request under that key is refused.
  - Use only temporary directories, writing nothing to the active local Paseo or `~/.claude`. Node still reads the config directory each of the machine's
    other running claudes names (`CLAUDE_CONFIG_DIR`), as every node does (`server/internal/node/claudedirs.go`), so its list can hold their transcripts
    too; the checks pick out the run's own sessions, by ID, folder or engine.
  - Add `KEEP=1` to retain temporary directories, or `DEBUG=1` to print the bridge lines from node's log.
- How the web names and draws providers (#8142): the mock hub's `MOCK_PASEO_PROVIDERS=1` (`scripts/mock/mock_hub.py`, on the
  Paseo computer of `MOCK_PASEO=1`) adds two of Paseo's providers the apps have no id for, Grok waiting on an approval and
  Cursor idle, with their launchers and model lists on the server: the sidebar's marks, the pill and the model panel, the
  Inbox card and the Agents filter show "Grok" and "Cursor", never "paseo". Both list the modes they take (#8261): Grok
  on its own approval with its model's two efforts, Cursor in Full access; their launchers say the same, and Session info
  shows no counts for them.
- Codex archive lifecycle: with the same generated `personad` binary, run `PERSONAD=/tmp/personad CODEX_BIN=<a Codex executable> PASEO_BIN=<a Paseo executable> node integrations/paseo/e2e/codex.mjs`.
  It uses a real temporary Paseo daemon and Codex with a scripted local Responses API, isolated homes and synthetic data. It checks that archiving,
  reconnecting and continuing preserve the Agent and thread identity and leave only one conversation row, and that a read of the archived Agent no chat
  asked for starts no Codex while opening its chat reads it (#8132). It never uses the active Paseo daemon or a real model account. Its temporary
  directories go under `$TMPDIR` when it is set.
- Native background regression: add `PASEO_BACKGROUND_E2E=1` to the Codex command above with Paseo 0.16 or newer and a Codex
  app-server that supports background terminals. A scripted tool starts a real shell command that waits for a fixture file;
  the test checks its task ID and command after the foreground turn ends, then releases it and checks that the row clears,
  without watching the chat. All processes, files and model traffic stay in the isolated fixture.
- Tested versions: on 2026-11-16, Paseo 0.16.0 passed all 65 Claude Code checks and all 20 Codex checks including the native
  background regression. The background regression fails on the pre-fix bridge because the native task never reaches Persona.
  On 2026-11-14 the end-to-end check passed all 61 checks on Paseo 0.14.0 and on 0.15.4
  (Claude Code 2.1.294).
- Local testing (2026-11-04, Paseo 0.14.0, Claude Code 2.1.294):
  - All 29 end-to-end checks passed (35 on 2026-11-11, with the archived sessions that keep their ID).
  - Read-only validation against a real active Paseo: the bridge connected to a fake node, allowing only read requests. Every available provider became a launcher; all Claude Code sessions pointed to their own transcripts,
    most of which were found locally (the missing ones were in configuration directories unknown to node, so their content used the timeline). No write requests were sent.

## Troubleshooting

- **Engine shows "Not detected"**: Paseo is not running, or `paseo.pid` is not in the home node expects. For a custom home, set `PASEO_HOME` for personad.
- **Engine is available, but the note says "Paseo is running, but Persona can’t connect to it: …"**: the bridge cannot start, or it exited; the rest is the reason, or the error the bridge printed
  last. Commonly a missing Node.js 22 (`no Node.js found (set PERSONA_NODEJS)`, or `no usable Node.js (…)` naming each one tried and why): set `PERSONA_NODEJS` to a node executable version 22 or newer.
- **The note says "Paseo is running, but Persona still hasn’t connected to it: …"**: the bridge has run for 30 seconds without connecting. The rest is the last line it printed, such as
  `paseo: Paseo refused the connection (…)` (Paseo did not accept the bridge's hello, or the `local-credential` of a Paseo with a password) or `persona: this personad node lacks …` (update personad).
- **The note says "Update Paseo: …"**: the Paseo there is older than 0.15.0. It still works, with the problems fixed since; update the Paseo App or CLI and restart its daemon.
- **Which Node.js runs the bridge**: the line `paseo bridge: Node.js …` in node's log at each start says which, and where it was found.
- **A note that seems out of date**: the engine list is what node reported when it last connected to the hub, so a bridge that started failing later shows its note after node reconnects.
- **Sessions appear, but content uses Paseo's format rather than Claude's transcript**: node did not find the transcript, for example because the provider uses a different
  `CLAUDE_CONFIG_DIR`. Add that directory to the machine's `machines[].claude_config_dirs` in hub.json (`docs/operations/config.md`).
- **Detailed logs**: look for lines beginning with `paseo bridge:` in node's log.
