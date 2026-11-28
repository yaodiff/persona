# Health space (#2691)

Health-related chats are split out of the assistant's main chat into a separate **Health** space: its own entry point, its own chat directory, its own memory.
The first version is modeled on ChatGPT Health and starts with the hub and the web (#2789); the iOS and Mac entry points then reuse the same APIs (#2901). Android has a minimal entry (#4329): its health chats and a new health chat page.

## Health items

Who it serves: everyday people choosing what Health tracks for them (#14711).
The Health module keeps one ordered list in the manager setting `health.items`, with a revision.
Its catalog contains water and protein (`persona`), Activity rings and trends (`apple_health`).
Clients provide the titles. The hub forwards GET and PUT requests from full-access devices;
watch answer tokens and extensions cannot change the list. Hello advertises `health_items` only when
the default manager names it. See `docs/protocol/protocol.md` ("Health items") for the wire contract.

Before a first save, reads compute defaults without saving: Activity rings and trends; water
and protein too if intake entries (including undone entries), an intake setting, or a pending
water extension import exists. A fresh owner gets only Activity rings and trends. Each save
replaces the entire ordered list at the revision read; stale writes return 409 and the current
state. Clients preserve items they do not display. Removing an item keeps its records; adding
it again makes them available. A successful save increments once and sends one `health_items` event, which also
refreshes the default manager's watch complications and widgets through the intake wake path.

Reads show recognized IDs once each, in stored order. If a newer hub saved IDs this version
does not recognize, reads omit them without changing the setting. PUT and `add_item` saves
keep those IDs at the end in their original order, including repeated unknown IDs. This lets
an older hub change known items without erasing newer ones. Unknown or duplicate IDs in a
PUT request are still rejected. An unknown-only saved list stays saved and reads as empty.

The Health chat includes recording guidance only for added water/protein items, while phone
health summaries and workouts remain available. Logging an absent item says to ask the owner
whether to add it. Only after `ask_user` consent does the chat call `health_intake` with
`action:add_item` and `item`, then records the entry. The item is appended after the other
recognized items, before any preserved unknown IDs. Adding an existing item is idempotent;
list and delete remain available. Without the module, the chat has no intake-item guidance.

Water reminders stop while water is absent. GET intake includes the ordered `items` so watch
complications and Log Water can ask the owner to add Water in Health. Intake HTTP writes and
imports still accept absent items for older clients; Apple Health sync follows all records
and undo markers regardless of the list.

On iPhone and iPad (#14715), the home follows this ordered list only when hello advertises
`health_items`; older hubs keep the existing home. Water and protein share Today: the section
occupies the earlier item's position, and its columns follow their order in the list. iPad omits
Activity rings; Vision Pro draws only Today. Other home sections stay in place. **Edit** opens
the existing grouped sheet with switches and native move handles. Saving replaces visible slots
while retaining hidden and unrecognized IDs in their relative order. A 409 adopts the returned
state and explains that another device changed it; other failures keep the draft. Removing an
item keeps records, and adding it back restores their display.

Before the first save, an owner with no added intake item sees one question for Water and Protein.
On supported iPhone and iPad hubs, the question also offers strength; an added, visible strength item suppresses it.
Hidden strength keeps the device's existing question and stays preserved when saving.
**Add** saves the selected items; **Not Now** saves the current list. Either accepted save stops
asking, including after relaunch. Complications, intake widgets and Log Water read `items` from
intake: absent water/protein asks to add it in Health, and Log Water refuses before writing.
A missing field from an older hub keeps both items available.

## Strength pages on Apple clients

With `health_strength` and an added `strength` item, iPhone and iPad show the server's `strength.home` card at its
stored position. The existing reserved `-health-intake` renderer opens the overview, exercise and draft editor pages;
the server supplies their titles and opaque state. Managed `health-strength-` cards use the ordinary in-app hub link
for Adjust. Save, Cancel and Close return to the conversation. Content-free `health_strength` events and authenticated
hello/reconnect reload the home card and open pages. Without the capability, the item is hidden and retained during
edits; Vision Pro continues to show only Today. Removing strength keeps its data and existing card actions.

## Tell Health on Apple clients

Tell Health is a background Shortcuts action with one text input on iPhone and Apple Watch. It uses the selected hub
and the watch's existing credential, requires `health_tell`, and posts only to the dedicated Health endpoint. A receipt
says **Sent to Health**, without claiming delivery or execution. Bounded retries reuse one invocation ID so a lost
receipt does not create another sentence; refusals and uncertain delivery remain visible. Health's help explains a
Strength Training Starts automation, the option to say it in Health instead, and system-controlled confirmation.
See [App Intents](../apple/intents.md) for the action and receipt behavior.

## Why split it out

Health information is more private than work information, and longer-lived: allergies, medications, checkup results need to stay useful once recorded, but they should not show up in the context of work chats,
nor be read by the main chat's tools or written into the knowledge base. Inside the main chat, memory, tools and the chat list are all shared and cannot be separated.
With a separate directory, Claude Code's per-directory auto memory, CLAUDE.md and chat list separate naturally.

## What we borrow from ChatGPT Health

| What ChatGPT Health does | What Persona v1 does | Later |
| --- | --- | --- |
| A separate Health entry in the sidebar (desktop) / menu (phone), opening an independent space ([1]) | A separate **Health** entry in the web sidebar, `/health`; **Health** in the iOS, Mac and Android assistant sidebar | Android: the rest of the Health home (memory, reports, sources) |
| Health home: recent data and trends, synced records, suggested questions, past health chats, connected accounts ([2]) | Health home: Today with water and protein, Activity rings on iPhone, trends on iPhone and iPad with Apple Health data, data sources, suggested questions, past health chats and health memory | More connected data sources |
| Health memory is stored separately and does not flow back into regular chats; users can view and delete it ([3]) | The Health space has its own directory and its auto memory belongs only to it; viewable and deletable on the web; the main chat's tools cannot see health chats | Give the Health space only health-related tools |
| Talking about health in a regular chat suggests moving to Health ([4]) | Not done | Suggest "Continue in Health" when the main chat touches on health |
| Connected data is not used for training, with extra encryption and isolation ([5]) | All data stays on your own hub and computers; `kb_update` from a health chat is rejected (blocked in code); not sending to other chats relies on chat tools only accepting the same space; push and `phone_notify` notifications carry no health content; handing to an Agent, sending email and Lark each need confirmation on the phone ("outbound") | More data sources: medical records, checkup report uploads, heart rate / HRV from recordings (#1950) |
| Does not replace a doctor's diagnosis and treatment | CLAUDE.md states: no diagnosis, no prescribing, no changing prescriptions; on danger signs, the first sentence tells the user to seek medical care | |

Sources:

1. https://www.foxnews.com/tech/chatgpt-health-promises-privacy-health-conversations
2. https://chat.openai.com/health
3. https://livemint.com/technology/tech-news/openai-launches-a-dedicated-experience-for-medical-questions-in-chatgpt-heres-how-it-works-11767866024939.html
4. https://www.thetechoutlook.com/new-release/software-apps/openai-introduces-a-dedicated-space-for-health-conversations-in-chatgpt-chatgpt-health/
5. https://openai.com/index/health-in-chatgpt/

## How directories and memory are isolated

- **Directory**: health chats run in `<data dir>/health` on the assistant's machine (default `~/.persona/health`), alongside the assistant's own `<data dir>/assistant`.
  When the hub asks node to prepare the directory (`assistant.prepare`), the relative path it passes is `.persona/health`.
  `identity.HealthDir`, `identity.SpaceHealth`, `identity.SpaceOf` are in `server/internal/identity/paths.go`; `IsAssistantDir` also holds for the health directory,
  so health chats, like assistant chats, are "assistant chats" (`assistant: true`) and use the same tools, authorization and questions.
- **CLAUDE.md**: the Health space has its own `server/internal/prompts/assistant/health_claude_md.md` (golden: `server/internal/assistant/testdata/prompts/health_claude_md.golden`):
  talk only about health; read system data with `phone_health_summary`; log Persona water and protein with `health_intake`, and other data such as weight and sleep with `phone_health_write_sample` (confirmed on the phone); do not write to the knowledge base or send health content to other chats; when the user wants to share with someone (doctor, coach, family), help send it, with the send confirmed on the phone;
  no diagnosis, no prescribing; on danger signs, tell the user to seek medical care first; when discussing numbers, attach a chart card by default ("Charts" below). Connector and plugin skills are installed the same way as in the assistant directory, but the instructions plugins give the assistant (`plugins.Instructions`) are not added to the health CLAUDE.md: those were written for the main chat's work scenarios, and v1 deliberately leaves them out.
- **Memory**: Claude Code's auto memory is per working directory (`~/.claude/projects/<encoded dir>/memory/`). Only health chats read and write the health directory's memory; it is not in the main chat's context, and vice versa.
- **Files** (#3282): the main chat's Claude can read files on the computer with Read / Bash; separate directories do not mean unreadable. So the main chat (not the Health space's assistant chat) starts with deny rules:
  Read and Edit may not touch the health directory or its Claude project directory (memory, transcripts), `server/internal/node/healthdeny.go`; the CLI gets them via `--disallowedTools`. The same rules cover the track directories the manager names on `assistant.prepare` (`deny_dirs`, capability `deny_dirs_v1`, #13177).
  Deny rules override every allow: even after tapping **Always Allow in Session** or switching to bypass mode, the files cannot be read (tested on Claude Code 2.1.318).
  **Always Allow in Session** for file tools in the main chat also only allows the directory of that file (`hooks.go` `fileRuleScope`), instead of opening the whole tool.
  **Not blocked**: Bash can build the path in any number of ways (`cd .. && cat he*`), so matching command text cannot catch all of them; blocking it fully needs Claude Code's sandbox or OS-level isolation. When the main chat runs on Codex, this rule does not exist yet either.
  The memory API with `?space=health` reads and writes exactly this memory.
- **node**: node recognizes the Claude project directory that corresponds to the health directory (`isAssistantProject` in `server/internal/node/sessions_offline.go`), and treats health chat transcripts as assistant chats, same as the assistant's.
  A health chat is listed in the health directory however its model `cd`s (into `reports/`, `/tmp`, anywhere), running and at rest (#2368): Claude Code writes the shell's directory after a `cd` into every transcript line, so the node goes by where the chat started (its start record, else the transcript's first `cwd`, else its project directory's name; `readQuickMetaSince`, `assistantHome`).
- **Which space**: a chat's space comes from the directory it started in; a `cd` into a subfolder or anywhere else does not move it. In the session list the hub sends, every health chat carries `space: "health"` (`proto.Session.Space`, filled from cwd in `server/internal/assistant/fleet_sessions.go`, `server/internal/assistant/fleet_routes.go` and `server/internal/hub/assistant_link.go`);
  the assistant's own chats do not have this field. To decide which space a chat belongs to, the assistant looks at `space` first and falls back to the directory (`spaceOf` in `server/internal/assistant/health.go`).
  These classify, so they take the directory itself only (`identity.SpaceOf`, exact). What hides or confirms (below) also counts a directory inside a health space (`identity.SpaceAt`, `identity.InSpace`): there, when in doubt, health is the safe side.
- **Registry and policy** (#13104): the health space is the built-in entry of the manager's track registry (`server/internal/assistant/track_registry.go`; the manager supplies it, the `assistant_tracks` table holds the others), with the health template, the health category as its default, and the manager's health directory.
  A track has a kind (a domain space is one kind) and its directory is data: the health space keeps `health`, a new track defaults to `tracks/<slug>` whatever its kind. Kinds, templates and the versioned list of information categories are checked in code, not in the table. Conversations still name their track in the wire field `space`.
  What hides, redacts or confirms never compares a track's name: it reads the track's `TrackPolicy` (lock screen and channels, extensions, knowledge-base writes, handing things out, the main chat's file tools).
  The main space has every switch off and the health space every one; another registered track's follow its default categories, and a slug the registry does not know has every switch on ("Using information by scene" below).
  The hub and the nodes cannot read the registry: a node learns the other tracks' directories from the manager (`deny_dirs`) and tells sessions in them by `space`, and both treat any track as the strictest. Once noted in a track other than the main space, a conversation stays there (`assistant_conversation_spaces`).

## Information categories (#13246)

Collecting is not limited by track: every assistant conversation is tagged with the kinds of personal information it carries, so a later step can decide each use by the information's categories and the scene it goes to instead of by the track it came from.

- **Categories** (`server/internal/assistant/track_registry.go`, versioned in code, never user-defined): `health`, `finance`, `relationships`, `emotion`, `legal_id`. No category means general. Credentials are no category: they are filtered out before anything is kept.
- **A conversation's categories** (`conversationCategories` in `server/internal/assistant/info_tag.go`): its track's default categories, a floor nobody takes away (the health space's is `health`), and what was tagged since, kept in the manager table `assistant_info_categories` by session id. They only grow: the model and agents only add, and only the user takes one away, which stays away (a `removed_by_user` row) until the user adds it back. When the categories cannot all be told (the store or the conversation's track cannot be read), uses outside the track are to take the conversation for sensitive.
- **Tagging after each turn**: once an assistant conversation's turn ended and stayed still, what the user said and the reply (the first 1,000 characters) go to the model use `info.tag`, a read-only use under Sorting (`inbox.triage`). Its prompt is `server/internal/prompts/assistant/info_tag.md`, in English whatever the owner's language, and the answer is category names only; the track's default categories are passed as the ones to look for especially. Health conversations are tagged too: tagging is collecting, and limits belong to how information is used, so the first 1,000 characters of every health turn go to the model Sorting is set up with, which need not be the health conversation's own. Answers are cached by text and model. A turn that cannot be read or tagged (no model set up, paused, timed out) leaves the categories as they were.
- The health space behaves as before: its default category is `health`, so every exit keeps its strict rule without asking a model ("Using information by scene" below).
- **Process environment** (`PERSONA_CATEGORIES`, `server/internal/node/categories_env.go`): next to `PERSONA_SPACE` and `PERSONA_TRACK_KIND`, every assistant process gets `PERSONA_CATEGORIES` as a sorted comma-separated list of category names (`""` for general, `health` in the health space, `emotion,finance` when tagged with both). The manager sends the directory's default categories on `assistant.prepare` (`categories`) and an existing conversation's accumulated categories on `assistant.send`, `session.send` (via `SendParams`), and `assistant.answer_again` (`categories`, or `categories_unknown` while a turn waits to be tagged, the store cannot be read, or the conversation's track cannot be placed). When the categories are not known (an older manager or node, a directory not prepared since the node started, `categories_unknown`, or an invalid token), `PERSONA_CATEGORIES` is absent from the environment so a command treats it as unknown (sensitive). Long-lived processes (headless Claude, `assistant-tui`, `codex app-server`) and warm spares record the `(categories, known)` state they started with: idle processes restart on change, active processes refuse stricter/unknown transitions with `busy` so work is not lost or delivered onto stale categories, stale spares are rejected and replaced on the next warm, and local runtimes with `caps.assistant_environment` receive per-session `PERSONA_CATEGORIES` (or omit it when unknown) on `session.send`, `session.modes`, and `session.fork`. Agents never inherit it from the assistant's shell.

## Using information by scene (#13246)

What a piece of information may do is decided where it is used, by one function, `usage` (`server/internal/assistant/usage.go`), from what it is (its categories, whether they are all known, the track it came from) and where it goes. It never reads a track's name or kind: a track takes part only through its default categories, which everything from it carries.

| # | Scene | General | Sensitive (any category, or not known) |
| --- | --- | --- | --- |
| 1 | Inside its own track | Used | Used |
| 2 | The main chat or another track | By relevance | Only when on topic, never in a standing context; the main chat's Read and Edit are denied the track's directory |
| 3 | Handed to an agent or a computer | By relevance | Confirmed on the phone each time; a task plan does not start on its own |
| 4 | Lock screen and pushes | As usual | Only that there is news |
| 5 | Sent to other people | As usual | Confirmed on the phone each time |
| 6 | Read by an extension | As usual | Hidden unless the extension is the user's own or was granted every category |
| 7 | Written to the shared knowledge base | As usual | Refused |

Information whose categories are not known yet (not tagged, tagging failed or timed out) takes the sensitive side outside its own track. `TrackPolicy` sums these rules up as the switches the call sites read until each asks `usage` for its own scene (`policyOf`); a track's policy is that of its default categories, so the health space keeps every switch on, the main space none, a slug the registry does not know every one, and only tracks with a sensitive default category have their directories denied the other conversations (`deny_dirs`). `TestUsageHealthOutbound` maps every row of the table under "Outbound" to its scene.

Which categories a use reads depends on the scene:

- **A text leaving Persona** (scenes 3, 4, 5 and 7: a push or `notify_user`, `phone_notify`, `urgent_call`, a to-do reminder, a scheduled task's failure notice, a decision's push, "all cards" and "all questions" on Lark and the plugin channels, `agent_send`, `agent_start`, `agent_switch`, an A2A message or task from an assistant conversation, a task plan, `kb_update`) carries the categories tagged of its own words, within 3 seconds (`infoTagExitTimeout`), and its track's default categories (`exitPolicy` in `server/internal/assistant/info_use.go`). Never its conversation's accumulated ones: the main chat would soon carry every category and have everything confirmed. A track sensitive by default asks no model. When the words cannot be tagged in time (a timeout, a paused model, an error), a text from the main space is taken for general; in any other track, the conversation's categories stand in when it was tagged and no turn of it waits to be tagged, otherwise the text is taken for sensitive. With no model set up for `info.tag`, a text carries its track's default categories, as before tagging.
- **A conversation as a whole** (scenes 6 and 2: an extension listing or reading it, a recall elsewhere) carries its accumulated categories (`conversationPolicy`); while a turn of it waits to be tagged they are not known. An extension reading the assistant's conversations (`assistant.conversations`, `assistant.messages`) is shown each by its categories, and an extension reading the assistant's entries (`assistant.entries.list`, `GET /api/v1/assistant/entries`) is shown each by its origin space's default categories, its own categories, and its source track's usage rules (#13548); its other reads in the manager (the feed, card history, archived agents, memory) go by the track's default categories and its grant. To-dos, and every control (starting a conversation in a track, taking a run onto a to-do), keep the track's rule whatever the extension's trust or grants: trust widens reads only. On the hub's own read routes (session lists, transcripts, cards), an extension marked `owner` reads every track (`Profile.ExtensionOwners`), while third-party extensions remain hidden by track for now.
- **Extension trust and grants** (scene 6, `server/internal/assistant/extension_trust.go`): an extension the user marked as their own (`trust: owner`, such as their memory extension) reads everything; any other is a third party's and reads sensitive information only of the categories the user granted it, each one (`assistant_extension_trust`, `assistant_extension_grants`). The extension manager names the reading extension (`assistant.extension_read`); a read that names none is a third party's with no grant. When an extension is removed, its trust and grants go with it (`assistant.extension_trust_forget`): installed again it starts as a third party's. With no grants given, every extension sees what it saw before, except that conversations of a track with no sensitive default category, and their memory, are no longer kept from it, and a main conversation tagged with a sensitive category is.
- **Only the user changes it** (`GET /api/v1/assistant/extension-trust`, `PUT /api/v1/assistant/extension-trust/{id}`, `GET` / `PUT /api/v1/assistant/conversations/{id}/categories`): a full-access client only; an extension's token or a scoped device gets 403, and agents have no tool for it. A track's own categories cannot be taken away (409); a category the user took away stays away until the user adds it back.
- A redacted push outside the health space says only that there is news (`assistant-health.sensitive_notice_*`), and a confirmation card says the content may be personal (`assistant-health.sensitive_outbound`); a track whose default categories carry health keeps the health space's words.
- What stays inside Persona is not a text leaving: to-do edits from a conversation (`todo_create`, `todo_update`, their repositories and workspaces) and the main chat's mirror to the owner's own Lark chat and plugin channels keep the track's rule.

## Memory by scene (#13532)

The memory ledger's entries (`assistant_entries`, `server/internal/assistant/entries.go`), and the memory extension's as a second source, reach a conversation, a task or a draft through one assembler (`server/internal/assistant/memory_assemble.go`), which picks entries by their scope and lets each through by `usage` for the scene it goes to. It reads no track's name or kind.

- **What usage is told of an entry** (`memoryEntryUses` in `server/internal/assistant/memory_assemble_entry.go`, the one place that reads an entry's space and categories): its track is the space of the conversation it names as its source (the space dimension of its scope until the entry type has one). An entry that states another track (`origin_space`) than its conversation's is not known; one that states none whose conversation cannot be told now is in no track and not known. `decision_record` sets `origin_space` itself, never from the model: the recording conversation's space when it is in one, else its source conversation's. Its categories are its own (`category`) and its track's default categories. Nothing tags entries themselves yet: an entry that carries no category takes the categories its conversation was tagged with, whenever they are kept, and is known only when that conversation was tagged and no turn of it waits to be tagged; otherwise it is used as usual in its own track and taken for sensitive everywhere else. With no model set up for `info.tag`, an entry carries its track's default categories and those its conversation was tagged with before. An entry naming an unknown category or a track the registry does not know is not known. An assembly reads the newest 2000 entries the scope selects (`NewestAssistantEntries`).
- **The memory extension's entries, a second source** (`memoryExtensionUses` and `memorySources` in the same file): a memory extension keeps its own entries, and from a version that sets them each carries the space it was first said in (`origin_space`, the slug, `""` for the main space) and its categories (`categories`). The assembler takes them beside the ledger's, each source spending its own budget (below, "Budgets"), every one personal, with ids prefixed `memory-extension:` so a to-do's brief tells them apart. From an extension that sets both fields, an entry's track is the space it states (it names no conversation the manager could check it against, as a ledger entry naming none keeps what it states), and its categories are its own plus its track's default categories, added by the manager; an unknown space or category makes it not known. Both fields are left out when empty, so a missing field means "the main space" or "no category" only for an extension that sets them; the source says so for the whole batch (`Tagged`), never per entry. From an older extension, or one whose version cannot be told, where an entry was said cannot be told: it is in no track and not known, so the strictest rule holds and no assembly carries it (not the main conversation's standing channel or recalls either). **The source** (#13586, `server/internal/assistant/memory_extension_source.go`): an extension that declares `memory_sources` answers its entries through `memory.entries`; the extensions' manager reads every such contribution that is on (`server/internal/extmgr/memory_sources.go`), each read is mapped (`memoryExtensionBatchOf`) and merged, and `Service.memoryExtension` keeps the result for a minute (10 seconds after a failed read; any extension change reads again). An extension that does not declare it still delivers rendered text (its `instructions`, `project_context` and `inbox_context` files and its own recall tool), filtered by its own rules (`Resident`, `Named`), which the manager does not filter; one that declares it has those outlets withheld, with its own MCP servers in the assistant and its data-directory skills (`docs/extensions/extensions.md` "Memory sources").
- **The read-only call a memory extension offers** (decided in #13532: grading by scene is the hub's alone, so it is not written again in each memory extension): `memory.entries` of the extension point `memory_sources` (`docs/extensions/extensions.md` "Memory sources", `schema/extension/memory.schema.json`) answers the entries one by one, most important first, each with `id`, `text`, `status`, `facet`, `origin_space`, `categories`, `standing` (the extension's always-in-context entries, its `Resident`; only these go to a standing channel), `rank` and `subjects` (the names whose mention brings it in, its `Named`; an entry that is not standing comes in only by relevance, in a recall, a hand-off or the Inbox), and per page `tagged`, `has_more` and `after`. `memoryExtensionBatchOf` (`server/internal/assistant/memory_extension_wire.go`) maps the pages to the assembler's input: only `active` entries that hold to the schema's rules, tagged only when every page says so, subjects as tags (so `entryRelevant` matches them), `standing` carried on the entry (only standing entries reach a standing channel: `memoryExtensionBatch.standingOnly`), and the most important last (standing first, then by rank, the extension's order kept among equals), since a budget is spent newest first. Registered and wired (#13586): once an extension declares `memory_sources`, the manager reads `memory.entries` into `Service.memoryExtension` and stops delivering that extension's text outlets (instructions, project context, Inbox context, its connectors' read tools in the assistant's conversations, its own MCP servers in the assistant, its data-directory skills) directly, so no entry reaches a scene around the filter; an extension that does not declare it keeps them unchanged. A call that fails, times out, answers over 1 MB or answers a page whose own fields break the schema leaves that assembly without the extension's entries; an entry that breaks a rule (an unknown `status` included) is dropped alone. It never falls back to the text.
- **A standing channel** (scene 1 and 2, `standingMemory`): a conversation's own track's entries, and personal entries from elsewhere that are general, newest first within the budgets below; never a sensitive entry from elsewhere. Of the memory extension, only its standing entries. A main-space conversation's standing channel is a section of its per-message snapshot (`withStandingMemory` in `server/internal/assistant/memory_extension_source.go`, `server/internal/prompts/assistant/snapshot_memory.md`), sent again only when it changes, with the memory extension's entries only and only on the owner's own assistant computer (not a guest's conversation, not a work computer); track conversations (spaces, health) take none, and their context is unchanged (a recall, `decisions_query`, is how they reach their own track's standing entries). The standing section never waits for a read of the memory sources: it takes what the source keeps for the current extensions, or none until the read it starts is done. The ledger's entries reach standing channels in the memory ledger's next step.
- **A recall** (scene 2): `decisions_query` returns the conversation's own track's entries, general entries from elsewhere, and sensitive entries from elsewhere only when the conversation's categories carry every one of theirs. The filter runs on the page the ledger read, so the cursor and `has_more` stay the ledger's. The memory extension's entries a recall lets through come beside them on the first page (`memory: [{id, text}]`, `recallExtensionMemory`), scanning the full paginated extension memory cache: with `tag`, all-term matches rank first and remaining budget (at most 3 KB) is backfilled by multi-term hit count, subject/facet relevance and entry rank. Main-chat turns also deterministically attach up to 1536 bytes of non-sensitive extension entries whose high-discriminability entity subjects are mentioned in the current turn (`mainEntityMemorySection`, excluding generic stopwords and standing duplicates). Leader sessions receive their bound project's extension memory and active direction/global user decisions on start, resume, and compaction recovery (`leader_memory.go`, `compact_recovery.go`).
- **A dispatched task's first message** (scene 3, `server/internal/assistant/memory_handoff.go`): entries for any executor that apply on the computer it starts on (personal ones only on the owner's own computer, none on a work computer), general ones when they share two terms (common English words aside) with the task's title and prompt, within the budgets below, as a context block (`server/internal/prompts/todo/memory_entries.md`). A sensitive entry goes only after a confirmation: the plan's words, tagged once when the plan is made, carried every one of its categories, so the plan waited for the owner's Start on its card (`PlanTask.memory_topic`), and the owner tapped it (`owner_started`). A leader's workers, `task_spawn`, a plan the hub started without asking or one whose words were general carry no sensitive entry. No hand-off asks a model. Once the start went through, the to-do's brief notes what went: a `memory` event in its log with the entry ids and the sensitive ones among them (`docs/protocol/protocol.md`); a failed start, or a fallback to another computer, notes nothing for the try that did not start. An Agent started outside a plan (one the assistant starts, a to-do handed to a new Agent) carries the memory extension's general entries its words bear on, never a sensitive one, only on the owner's own computer, with no brief note (`agentStartMemory`).
- **Budgets** (#15312, `server/internal/assistant/memory_assemble.go`): bytes of entry text, spent newest (most important) first; an entry that does not fit is left out and a smaller, older one may still go. The ledger's entries and the memory extension's each spend their own budget, so neither crowds the other out. Of the ledger, a standing channel takes at most 1536 bytes of its own track's entries and 1536 of personal ones from elsewhere, a hand-off 1536. Of the memory extension, a standing channel takes at most 4096 bytes of its standing entries, in whichever scene they are, or the budget the extension states on its `memory.entries` pages (`standing_bytes`, `docs/extensions/extensions.md` "Memory sources"), capped at 8192; a hand-off takes at most 1536 of its entries. The recall (3072) and the Inbox's (1024) bounds of the extension's entries are unchanged. Context length: the extension's standing section goes in every main-conversation message's snapshot and is sent again only when it changes, so the default adds at most 4 KB (about 1,000 to 1,400 tokens of CJK text, fewer for English) to that block and the cap at most 8 KB; the hand-off block grows by at most the extension's 1536 bytes beside the ledger's. The compaction recovery block (6 KiB) carries no memory entries and is unchanged.
- **The Inbox's helper and drafts** (scene 5, `inboxMemory`): general entries the message shares terms with, never a sensitive one. The memory extension's go to triage and drafts by this rule (`inboxMemoryContext`: at most 1 KB, as a background block under the extension's name); the Inbox carries no ledger entries yet, and takes this rule when it does.
- **Extensions** (scene 6): an extension the user marked as their own reads every track's memory files (`assistant.memory`, `memorySpaces` in `server/internal/assistant/memory_rpc.go`); a third party's reads a track's only with a grant for every default category of it. Reading ledger entries (`assistant.entries.list`, `GET /api/v1/assistant/entries`) still filters by the personal boundary only.

## Tracks and kinds

A track is of a kind (#13472): a config whose every field picks one of the options the manager defines (`server/internal/assistant/track_kinds.go`). A new option is a code change; a new combination of options is a new kind, config only. Nothing branches on a kind's name: every decision reads a field of the track's effective config, which is its kind's with what the track sets.

- **Built-in kinds** (the manager's, never rows): `orchestration` (the main chat's way: template `assistant`, the engineering context, leaders' supervision, the assistant's memory assembly, no capture, no channels) and `health` (the health space's headless policy: health template, health categories, track context and memory, no channels). The `space` kind is a declarative contribution of the optional `space-conversations` extension, not a built-in product feature: template `space`, user-written `SPACE.md`, track context and memory, no supervision, capture target, topic and single-chat channels, listed under spaces. Its tracks and their data remain in the same tables and directories when the extension is disabled or uninstalled; a kind with no enabled provider is left out and its conversations get the strictest policy. An extension-provided kind is read-only through the custom-kind API, as a built-in one is.
- **Extension installation on upgrade** (#14574): when the extension registry is ready, `extmgr` checks for existing non-health track records. If at least one exists and `space-conversations` is not installed, it downloads that exact extension from the verified, signed official marketplace and starts it. A fresh or unused installation is marked `not_needed` and receives no extension or Spaces navigation. An already installed extension, including a disabled one, always wins over migration; disabling or uninstalling it never causes a background reinstall. A failed download or start remains a retryable extension operation and is retried by the migration loop. The market's signature is never bypassed, including when a network read fails.
- **The user's kinds** are rows of `assistant_track_kinds`, their config a JSON object with `schema: 1`. A kind whose `schema` is newer than the manager, or whose config is not valid here, is listed as disabled (`disabled: schema` or `invalid`) and its tracks are left out (their conversations get the strictest policy) until the assistant is updated or the kind fixed. A kind asking for leaders' supervision gets the warning `supervision_leaders`: leaders review the main chat only for now.
- **Fields and options**: `template` (`assistant`, `space`, `health`), `user_instructions` (`space_md` or none), `context` (`engineering`, `track`, `none`), `default_categories` and `tag_focus` (information categories), `rhythms` (scheduled tasks a new track gets: title, five-field cron, optional IANA zone, prompt, `topic_policy` `new` or `continue`), `attention` (`push_cap` P0–P2, see below; `deep_default` `quick` or `deep`; `enter_mode` `none` or `immersive`, never switched to on its own; the attention modes themselves are in `docs/server/server.md` "Attention modes"), `supervision` (`leaders`, `routine`, `none`), `usage`, `assembly` (`assistant`, `track`), `capture_target`, `channels` (`topics`, `single`) and `sidebar` (group, order, icon token).
- **Usage only tightens**: each `usage` rule names a scene (`source_track`, `elsewhere`, `hand_off`, `lock_screen`, `others`, `extension`, `kb_write`), a category (or `*` for all, general and unknown information included) and a rule (`allow`, `relevant`, `minimal`, `redact`, `confirm`, `deny`). A rule looser than what `usage` itself gives that category in that scene is refused (400); for `*`, it is compared with the strictest category.
- **What a track sets** (`config_override`): `tag_focus`, `rhythms` (only when the track is created; later its scheduled tasks are changed as scheduled tasks), `attention`, `usage` (added to its kind's, so it only tightens further), `capture_target` and the sidebar's `icon`. Its template and default categories are its own columns, defaulting to its kind's. Anything else in the override is refused.
- **What the settings do** (each read from the track's effective config, never its kind's name; the built-in kinds and the health space set nothing that changes behavior):
  - `rhythms`: when a track is created (`POST /api/v1/assistant/tracks`), each rhythm becomes one scheduled task of the track (`Task.space` is its slug, a cron task, on or off as the rhythm says, `created_by: track`), all in one save (`server/internal/assistant/track_rhythms.go`). `topic_policy` `new` is a new conversation each run (`fresh_each_run`), `continue` one conversation continued. A rhythm's `tz` is not kept yet: the task runs in the user's time zone, as every scheduled task does. From then on they are ordinary scheduled tasks; a later change of the kind's rhythms reaches only tracks created after it.
  - `attention.push_cap`: a push from one of the track's conversations that is no card (`notify_user`, a turn's finished push) carries at most this level; a louder one is lowered to it, and a push without a level keeps none when what the hub derives for it (P1, P2 for a finished turn) is already that quiet. Cards (questions, approvals, a to-do's decision) keep their level (`server/internal/assistant/track_attention.go`). The manager settles the level before the hub gets the push; with no track capped below P0, nothing is looked up.
  - `tag_focus`: tagging a turn of the track's conversations, or a text leaving one, asks the model to look especially for these categories and the track's default categories (`InfoTag.Focus`; the health space's focus stays health).
  - `usage`: every use of the track's information (`usage`, `server/internal/assistant/usage.go`) applies the rule on top of the assistant's own and keeps the stricter. A `*` rule covers all of the track's information; a category's rule covers information carrying it, or whose categories are not known. The switches the call sites read stay on for anything stricter than their rule (`deny` on the lock screen still redacts).
  - The kind itself: every assistant process gets `PERSONA_TRACK_KIND` next to `PERSONA_SPACE`: `orchestration` in the assistant's own directory, `space` in the health space, a track's own kind in its directory. The manager names it on `assistant.prepare` (`track_kind`); a node from before, or a process resumed before its directory's first prepare since the node started, gives none. An Agent never takes it over from the assistant's terminal.
- **A track's own directory** (template `space`): `tracks/<slug>` in the state directory of the assistant's machine (`tracks-<id>/<slug>` for another manager), mode 0700, with `SPACE.md`, `inbox/` (what the user files into the track), `recordings/` and `.space/` (Persona's: `history/` keeps SPACE.md before Persona rewrites its state block). `SPACE.md` is written once from `server/internal/prompts/assistant/space_md.md` in the owner's language (a title, three empty sections and the state block between `<!-- persona:managed:state:begin -->` and `<!-- persona:managed:state:end -->`) and is the user's from then on. `CLAUDE.md` and `AGENTS.md` are the same file: the baseline `server/internal/prompts/assistant/space_claude_md.md` followed by the `SPACE.md` the node reads there (`assistant.prepare` with `space`, capability `track_spaces_v1`; `server/internal/node/assistant_space.go`). The node rewrites them only when they change, so editing `SPACE.md` reaches a running Claude conversation at its next message (a new process, `instructionsStale`) and a Codex conversation when the next one starts. The manager writes a new track's directory when it is created and checks every track's each minute with the assistant's and the health space's instructions (`refreshInstructions`); a computer whose node does not keep track directories gets none, and a conversation of the track is not started there. Only a track in its manager's tracks directory has one (`identity.TrackDirOf`), so every process tells its conversations from the path alone (`identity.IsAssistantDir`, `identity.SpaceOf`); an archived track keeps its directory, and the health space keeps its own directory and template.
- **Context with each message** (#13499, #17557): a conversation in a track other than the main space, the health space included, gets its own short `<persona-context>` instead of the main chat's snapshot (`server/internal/assistant/space_context.go`, `server/internal/prompts/assistant/space_context.md`): the time, the track's title and the owner's language, at most 600 characters, and nothing about Agents, computers, unread Lark chats, tasks or the knowledge base. The track is told as the manager noted the conversation, else as its computer lists it (a directory inside a track counts as the track), and before the first turn is noted or listed by the conversation directory the node sends on `assistant.context` (or, for older nodes without it, the manager's in-flight start space per computer, suppressing the main snapshot when concurrent starts span different spaces); the main conversation, leaders and the background supervisor are not looked up and keep the snapshot byte for byte. A kind whose `context` is `none` gets no context; `engineering` on a track other than the main space gets the short one for now. A track the registry does not know gets the short one, named by its slug.
- **Scheduled tasks in a track** (#13501, #17117): a task carries `space`, the track it runs in. One set up from a conversation takes that conversation's track (`schedule_routine`, `schedule_followup`); the owner may also put a task into a track, or take it out, with `space` on `POST` / `PUT /api/v1/assistant/tasks` (full credentials only, 403 otherwise; the assistant's own `""`, `health`, or a track with a directory here, 400 otherwise; a body without `space` keeps it). Moving a task leaves the conversation it came from and its own conversation behind. A task of a track never has a working directory of its own (400): it runs in the track's directory, its conversation is one of the track's (that is where its result is delivered), and what reaches the phone follows the track's policy (`server/internal/assistant/schedule_space.go`). `topic_policy` (`new` or `continue`, as a kind's `rhythms` name it) is the same choice as `fresh_each_run`: `new` starts a conversation for every run, `continue` keeps one; views carry `fresh_each_run`. A task may also carry an optional `delivery` anchor (`{channel: "feishu", chat_id, thread_id}`, validated on `POST`/`PUT /api/v1/assistant/tasks` and in `schedule_followup`/`schedule_routine`; omitting `delivery` on `PUT` keeps it, `null` or `{}` clears it; `server/internal/assistant/schedule_delivery.go`). When set, finished runs are posted back into that opted-in Feishu topic (`in_thread`, `no_detour`) and the topic is bound to the task's execution conversation so replies in the thread continue it directly (see `docs/extensions/bots.md` ("Topic group chats")).
- **Recordings in a track** (#13501): a recording upload with `space` (`PUT /api/v1/recordings/{id}`, `docs/protocol/protocol.md` "Recording sync") goes to that track instead of the knowledge base. The manager renders its transcript as `recordings/<day>-<id>.md` (front matter with the recording and the track's default categories, then one timed line per sentence) and has the assistant's machine write it there (`assistant.space_recording`, capability `track_recordings_v1`; `server/internal/node/assistant_space_recordings.go`); `assistant_space_recordings` keeps its revision and tombstone. Such a recording is never in the knowledge base (`kb_list`, `kb_search`, summaries, minutes): the track's conversations read the file. Once in a track a recording stays there, whatever a later upload names, and one the knowledge base holds stays in it (409 `recording_in_kb`); an all-day recording cannot go to a track (400). `recording_days` applies to it too: past it, or when deleted, its file is removed (later, when the computer was away) and a tombstone kept until the window ends (`server/internal/assistant/space_recordings.go`).
- **Reading and writing SPACE.md**: the apps read a track's `SPACE.md` with `GET /api/v1/assistant/tracks/{slug}/space-md` and the user saves it with `PUT` (`content` and the `base_digest` it was read at; a stale one is refused, so two edits never overwrite each other). The user's write may change anything but the blocks between `persona:managed` markers and the markers themselves, compared byte for byte. The track's own conversations, and only they (`spaceTools` in `server/internal/assistant/track_space_md.go`; the main chat's and the health space's tool lists are unchanged), get the MCP tool `space_state_update {text}`, which replaces the text of the state block alone, at most 2048 bytes; the model never edits that block itself. A conversation that talks with other people (the inbox's AI twin, a Feishu /agent) or that an extension opened for its own use (an outward reader) is refused; one the extension contributing the track's kind opened in that track (space-conversations, from the user's message on its page) is the space's own and writes it, the hub naming each contributed kind's extension (`server/internal/extmgr/track_kinds.go`, `spaceStateRefusal`). Both go to the node on the assistant's computer (`assistant.space_md`, capability `track_space_md_v1`, `server/internal/node/assistant_space_md.go`), which reads the file without following a link, keeps the file it replaces in `.space/history/` (the latest 20) and writes atomically. After the user's write the track's `CLAUDE.md` and `AGENTS.md` are written again at once; after the state's, at the next refresh.
- **Who changes them**: full-access clients can use the track and kind routes. An extension with the explicit `spaces` scope can list and create tracks, change a track and its `SPACE.md`, list a space's open conversations (`GET /api/v1/assistant/tracks/{slug}/conversations`, newest first, `?limit=` 1 to 50, default 20; 404 for a space hidden from extensions, and each conversation the extension's information policy hides left out, of `total` too; #15991), and read or change a visible conversation's categories. The legacy every-scope grant does not include it. An extension-provided or built-in kind is never changed through the custom-kind API. Every write names the revision it was made against (409 when another came first), and a kind change that would leave one of its tracks invalid is refused (409). Sensitive tracks remain hidden according to their extension policy, and `track-kinds` writes are not part of that scope.
- **In the apps**: no default navigation or creation surface for domain Spaces remains in Core. Installing and enabling `space-conversations` supplies the extension page and the `space` kind. Web opens its list, creation and SPACE.md pages through the extension; existing `/tracks/<slug>/<conversation>` links still open the ordinary conversation host, and `/tracks/new` and space-home links go to the extension when installed or to extension settings when not. iOS / iPadOS and Android show their existing native space surface only while that extension's navigation contribution is enabled. The health space keeps its own entry and pages and is not part of the extension's surface.

## Quick capture

What the user jots down in passing (the share sheet, the watch, Siri, the web; #13502) goes into a list of its own, never into a conversation: taking one in gives the main chat no turn and no tokens. The server part is in `server/internal/assistant/captures.go`, `capture_sort.go` and `capture_brief.go`; the apps' entry points come with the clients.

- **Taking one in**: `POST /api/v1/assistant/captures` with `{client_id, source, kind, text?, url?, space_hint?}` is one insert into the manager table `assistant_captures`, answered at once (202 `{id, status}`); the target is under 300 ms at p95, so nothing else runs before the answer. `source` is `share_ios`, `share_android`, `watch`, `siri`, `web`, `feishu` or `shortcut`; `kind` is `text`, `url` or `voice` (transcribed on the device); images and files come later. `client_id` is the device's own id for the capture, sent again with every retry: the same id is one capture (200 with `duplicate: true`), so an offline watch's queue delivers it once. `space_hint` names a track the user chose; it must take captures.
- **Which tracks take captures**: a track with a directory of its own (template `space`, not archived, not the health space) whose effective config has `capture_target` (the `space` kind's default; a track may turn it off).
- **Sorting** (a work loop in the manager, two at a time, woken by each new capture and every minute): one call of the model use `capture.sort` (under Sorting, `inbox.triage`, until it is given its own) sorts the capture into one of those tracks and tags its information categories. Its prompt is `server/internal/prompts/assistant/capture_sort.md`, English; the user message is JSON data: the note (its first 2,000 characters), its source, the tracks (slug, title, description) and the user's choice. The answer is a track or none, a confidence, an action (`keep`, `discuss`, `research`), a title and the categories. A capture the user put in a track is only tagged. Its categories are the model's and the track's default categories, kept as `assistant_info_categories` rows of kind `capture`; with none, a row that says it was tagged with none. With no model set up, captures stay `new`, listed; a call that fails is tried again, up to five times, then the capture is `failed`, still listed.
- **Filing**: only filing happens without the user, and only when the user chose the track or the confidence is at least 0.8. The node of the assistant's machine writes `inbox/<date>-<id>.md` in the track's directory (a short front matter with the id, time, source, kind and categories, the title, then what was captured), through `assistant.track_inbox_write` (capability `track_inbox_v1`, `server/internal/node/track_inbox.go`): the directory must be that track's own and a real folder, `inbox/` a real folder in it, the name a plain file name, and the file is written new and renamed into place, never through a link. While that computer is offline or its node cannot, the capture waits, `sorted`, and is filed once it can, without asking the model again. The others stay `sorted` for the user ("discuss" and "research" only ever run when the user picks them).
- **What the owner is told**: a filed capture is pushed quietly (P2): its title and track, or, when it carries a sensitive category or was not tagged, only "1 capture" (`assistant-captures.count`), no title, track or content. The morning and evening briefings (routines with facts) get a quick-capture section on the card and in the model's facts: how many wait to be sorted, those filed since the window began by title and track (five at most), and the sensitive ones only counted ("1 capture").
- **The list**: `GET /api/v1/assistant/captures[?status=]` (the newest 200) is the user's own view, scene 1: every capture with its text, sort, where it was filed and its categories. Both routes are for full-access clients only (403 otherwise).

## API

### Tell Health

Tell Health (#14714) is for people who want to speak or send a sentence to Health from their phone or watch.
`POST /api/v1/assistant/health/tell` stores `{client_id,text}` and immediately returns a receipt, before model work.
A retry with the same client ID keeps the first sentence. The receipt means received; delivery and the answer follow.

The first Tell Health message or strength workout report starts the local day's **training chat**. Later sentences,
the strength workout report and its plan stay in that chat. Saying that strength training has started asks Health
to review the last session and prepare a plan draft for confirmation. Only these two entry points select the
training chat; an ordinary Health conversation does not become it. The target is always Health, even if extra
request fields name a different space or chat. A gone, deleted or invalid chat is replaced. An offline computer waits
in receipt order; the original sentence stays in storage until delivery is confirmed. Each sentence's failed attempts
are at least 30 seconds apart, even when a new receipt wakes the worker; later sentences wait behind it.

Before continuing a chat, the sentence's destination is saved. An uncertain continuation retries only that original
computer and chat with the same once-only key, even if the training-chat pointer changes, a lookup fails or the
manager restarts. Only a definite refusal before delivery, including a deleted chat, permits a new Health chat.
The hub's lookup does not expose whether the current connection's session list is complete. A transient lookup
failure before a sentence is bound to a chat can therefore replace the pointer and split one day's training across
chats; it does not redirect a sentence already bound for continuation.

The day uses the owner's time zone. Sentences use receipt time; reports use workout start time. A workout begun
at 23:30 and reported at 00:40 continues the previous day's chat unless a sentence after midnight has already opened
the new day's chat. A sentence received at 00:10 starts the new day; if it has done so, the late previous-day report
opens a separate chat.
Only the latest day's chat pointer is retained; late older reports cannot take it over. Simultaneous first
sentences and reports can start separate chats. Shortcuts actions and automation setup are client features.

Replies use the Health space's fixed notification text, even when answered quickly. After 24 hours or five failed
attempts, a fixed notification says delivery could not be confirmed; at most one notice is sent per worker round for
newly failed sentences. It includes no sentence content and the stored originals are retained. Claude Code starts
can reuse a reserved conversation ID after a restart. Other launchers may choose their own IDs: a reported uncertain
start is abandoned, but a manager restart after the chat starts and before delivery is recorded can repeat the sentence
once. Tell Health remains available when the Health data module is absent or off, including the
`health_tell` capability; the strength report and plan guidance require the module and an added strength item.

For the exact protocol, see the "Assistant" table in `docs/protocol/protocol.md`.

| Endpoint | How the Health space uses it |
| --- | --- |
| `POST /api/v1/assistant/health/tell` | `{client_id,text}` → stored receipt, then today's training chat. Full or watch answer role; Health only. See "Tell Health" and `docs/protocol/protocol.md` for states and limits. |
| `POST /api/v1/assistant/conversations` | With `"space":"health"`, starts a new chat in the health directory (writing the health CLAUDE.md first). Combined with `"main":true` it returns 400 (the Health space has no main chat); an unknown `space` returns 400 |
| `GET /api/v1/assistant?space=health` | Lists the Health space's chats; `cwd` is the health directory. Without `space` it lists only the assistant's own chats, and health chats no longer appear. `?archived=1` works the same; an unknown `space` returns 400 |
| `GET /api/v1/assistant/memory?space=health` | The Health space's memory `{"files":[…],"dir"}` |
| `PUT / DELETE /api/v1/assistant/memory/{name}?space=health` | Edit or delete one memory in the Health space |
| `sessions` push, session list | Health chats carry `space: "health"` |

Once a chat is open it behaves like any other assistant chat: sending, archiving, renaming and compacting all use the existing endpoints (`/api/v1/assistant/conversations/{id}/…`, `/api/v1/sessions/{mid}/{sid}/…`) without `space`.

## Tool isolation

The `conversation*` tools in a chat only see chats in the same space (`server/internal/assistant/conversations.go`: `spaceConversation`, `ownSpaceConversation`, `selfSpace`):

- `conversations_list`: in a health chat it lists only health chats; in an assistant chat only assistant chats.
- `conversation_read`, `conversation_rename`, `conversation_archive`, `conversation_send`: a target in another space is treated as not found.
- `conversation_new`: a new chat started from a health chat stays in the Health space.
- Calls not made from a chat (no caller session id) count as the assistant's own space.

- `kb_update` from a health chat fails outright: health information does not go into the knowledge base (`assistant/tools.go` `runTool`). Knowledge base reads (`kb_search` etc.) work as usual.
- `health_intake` logs, lists and deletes only in a known Health conversation. `runTool` checks `agentCallerSpace` using the caller's machine and session, including Health subdirectories; main-space and unknown callers are refused before storage access. Health chats on another computer work too. `health_intake` and `health_strength` are listed only in Health conversations by both node `mcp.tools` and targeted hub `ToolsFor`, while the Health module is available. Discovery grants no access by itself. The first list before a session snapshot requires the node's attested `caller_cwd` (#14855); without directory evidence (older nodes, Windows or runtime-hosted sessions), the next discovery heartbeat, within 30 seconds after the snapshot arrives, adds the tools. The cursor includes the effective space, so an earlier list without Health tools cannot stay cached.
- Scheduled follow-ups and scheduled tasks created in a health chat (`schedule_followup`, `schedule_routine`) record their space (`Task.space`); when one fires and needs a new chat, it opens in the Health space.
  The space owns the task's working directory: a nonempty task `cwd` is rejected when saving and before dispatch,
  including an older stored override. Clear it to restore the canonical Health directory. Launcher, model, effort
  and execution computer remain configurable under their existing validation; the selected computer prepares this
  manager's Health instructions and directory. Every new Health conversation prepares that directory even when an
  internal caller supplies an already-resolved `cwd`. A follow-up continues its source chat only when its actual
  directory matches the freshly prepared Health directory, with the node enforcing that directory on resume.
  Missing or mismatched source context uses the existing safe replacement path. Normal-space tasks keep their custom directories.
- When the calling chat's space cannot be determined (the assistant's machine cannot list it for the moment), it is treated as the main space: it can only reach main-space chats, and health chats can never be listed or read. The worst case is a health chat temporarily seeing the main space's list; health content is never exposed elsewhere the other way round.
- `list_followups` lists only this space's scheduled tasks; editing with `schedule_routine` and an id, or deleting with `delete_routine` / `cancel_followup`, treats tasks in another space as not found.
- Extensions' skills and gated commands reach both spaces (there is no per-space contribution). Every assistant process carries its space in `PERSONA_SPACE` (`health` / `main`, #6003, `docs/extensions/extensions.md` "Assistant"), so a command meant for health data alone can refuse in the main space; a `cd` into the health directory does not change it. Its track's kind is in `PERSONA_TRACK_KIND` ("Tracks and kinds"), and its information categories are in `PERSONA_CATEGORIES` ("Information categories").
- All other tools (phone tools, reminders, mail, Lark, Agents…) exist in both spaces: health information can be sent to whoever the user wants; the rules are under "Outbound" below.

## Outbound (#3194)

The boundary is not "health information may not leave" but "it may not leave without the user knowing". Emailing checkup results to a doctor, or sending them over Lark to a fitness coach or family, is legitimate and still works.
In the health space the decision always depends on the chat's space (the trusted session directory), never on a model judging "does this sentence count as health": its default category makes every exit below strict without asking a model. Outside it the same exits judge the words leaving ("Using information by scene").

| Exit | In a health chat | Code |
| --- | --- | --- |
| Push: turn ended, `notify_user`, plan waiting for you to start, scheduled task failed | Only says "New in your health space. Open Persona to see it.", without the body, chat title or task name | `hub/machine.go` `doneAlert`, `assistant/tools.go`, `tasks.go`, `schedule.go` |
| Phone local notification: `phone_notify` | The hub replaces the title and body with the same localized notice as `notify_user` before forwarding to the phone; no health content reaches the notification | `assistant/fleet_tools.go` `mcpCall`, `assistant/health.go` `spaceNotice` |
| Push for question / authorization cards | The lock screen likewise only says **Health**; the card content is viewed in the App. A card is judged by its chat's directory: the node and then the manager place a card of an assistant chat in the chat's directory, not the hook's after a `cd` (`identity.CardCwd`, the manager's `placeCard` covers older nodes), and any directory inside a health space counts as health (`identity.InHealthSpace`, #2368) | `hub/push_payload.go` `interactionPayloadIn` (looks at `ix.Cwd`), `assistant/fleet_machines.go` `placeCard` |
| Lark bot, plugin channels | Forwarded `notify_user` only says there is a new message; health to-do reminders keep the generic notice without a specific to-do URL or ID in the card or a to-do reply binding (ordinary reminders retain their detail button, notes and reply context); answers in a scheduled task's chat are not mirrored; "all cards" and "all questions" exclude health chat cards (the chat the channel itself is driving works as usual) | `assistant/ask_view.go` (`ixSensitive`), `modules/lark/asks.go`, `feishu_push.go`, `channels_plugin.go` |
| `agent_send` / `agent_start` | Confirmed on the phone every time. `agent_send` accepts targets in the conversation’s own space. The card explains that the Agent keeps its copy after the health chat is deleted | `assistant/health.go` `confirmOutbound` |
| `task_plan` | Never starts automatically; always waits for the user to tap start on the plan card | `assistant/tasks.go` |
| `mail_send` and connector write operations | Confirmed on the phone every time; gate allow rules do not let it through (deny still rejects) | `connector.ConfirmEach`, `callConnector` in `extmgr/connectors.go` |
| Lark write operations (lark-cli, command gate) | Confirmed on the phone every time; allow rules do not let it through, and there is no **Always Allow in Session**; reads work as usual (deny still rejects). Both entrances hold it: the gated command (`larkCheck`) and the node's gate proxy, which a script can call without the command: a health process's proxy address lets a write through only with the one-time grant `larkCheck` leaves once the phone confirmed it. Both decide the space from a record made when the conversation's process is started, not from the directory a command runs in (a `cd` moves it); a hook directory that is itself a health space counts too, so a mix-up can only ask more | `node/larkgate.go` `larkCheck`, `node/gateproxy.go` `ServeHTTP` |

The health CLAUDE.md directs the model to use `notify_user` for notices. Both notification tools use the existing Health title and generic message in English, Simplified Chinese or Japanese.
Explicit reminders created with `phone_reminders_create` keep the user-requested text and require confirmation on the phone. Memory and chats within the same space need no confirmation.

Known limit: a gate proxy address does not know who calls it. A process in a health conversation could in principle learn the address of another conversation (another process of the same user has an environment that can be read), the same kind of gap as the Bash one under "How directories and memory are isolated": closing it needs the sandbox or OS-level isolation (see "Later").

## Entering Health (#4329)

The original rule from #4329 (implemented in #4385) resumed the latest health chat when one existed, falling back to the home when there was no history.
#13273 changes the entry to open the **Health home** directly. The home is the first screen for the daily overview (#13176), with previous chats one tap away, and opens without waiting for the chat list. Returning to the latest chat takes one extra tap.

- **Health**: opens the home whether or not there are previous chats. Its **Health Chats** list keeps open (not archived) chats newest first by last activity (`updated_at`), and tapping a row opens that chat.
  The list is `GET /api/v1/assistant?space=health` with the pushed sessions carrying `space: "health"` over it; no endpoint of its own. Loading the list does not hold up entry; on the web a failed list says why in its section.
- **First message**: the home's input box starts a new health chat when sent, as **New Chat** does in the main space; merely opening the home creates no chat.
- **New**: a health chat's header returns to the home. On the web a clock button next to it also lists past chats as a menu.

| Client | Where |
| --- | --- |
| Web | `/health` stays on the home; `/health/new` remains an alias for it. `HealthPage` / `Health` and the header buttons `HealthChatActions` are in `modules/health/Health.tsx`. With the module off, the same `/health…` and `/tracks/health…` addresses open the default-panel read-only archive for existing conversations, reports and memory. Tests: `entry.test.ts`, `Health.conversation.test.tsx`, `HealthReadOnly.test.tsx` |
| iOS (iPhone and iPad) | the sidebar's **Health** opens the home, with all open health conversations listed newest first. `AssistantView.enterHealth()` synchronously returns to the root and presents the home; the embedded Sidebar layout selects Health as its page root. Entry does not wait for the conversation list. **New Chat** in a health conversation still returns to the home (`HealthModel.newOpensHealth`); the first message in its composer starts a new health conversation. `-openAssistantPage health` / `-openShellPage health` still open the home. Tests: `HealthModelTests`, `HealthEntryUITests`, the Health upload case in `TreeSwapFoldTests` |
| Android | the assistant sidebar's **Health** opens the home immediately (`AssistantScreen.openHealth`); `HealthEntry.history` keeps its chat list ordered. See "Android entry point" below |

## Strength training

Who this serves: people keeping gym sets, including people who recognize a machine without knowing its name.
Adding **Strength training** enables writing through `health_strength`; it adds no setup to other Health items.
The model matches exercise names and aliases before creating an exercise, asks once how a new machine's weight is
counted, and never invents weights or reps. A machine photo is conversation input, not an exercise photo archive.
Both Health tools are listed only in known Health conversations, including a new conversation's first list when
the node supplies attested directory evidence (#14855). Execution independently enforces the same caller boundary.

`health_strength history` groups sets by owner-local day over `days` (default 30, range 1–366). With an exercise `id`,
it retains that exercise's full history in the range. Without `id`, it returns up to the 500 most recent sets across
all exercises. Use `history` to check an uncertain log before retrying it.

The manager stores four tables in Postgres:

| Table | Meaning |
| --- | --- |
| `health_strength_exercises` | Name, aliases, primary/secondary muscles, equipment, weight rule and seat/settings note. Another machine is another exercise. Deleted exercises retain a tombstone. |
| `health_strength_sets` | Confirmed exercise, kg, reps, warm-up flag, time and optional workout/draft. Idempotent IDs, undo tombstones and commit-ordered sequence numbers follow intake's rules. |
| `health_strength_drafts` | Unconfirmed sets or a day's plan, revision, status and the current conversation/card/version. Drafts never count as sets. |
| `health_strength_workouts` | Uploaded strength workout UUID, start/end and aggregate measurements. Other activities never enter this table. |

When a strength workout arrives, unassigned sets from 30 minutes before its start to three hours after its end join
it; existing assignments remain. Sets logged later use the latest matching strength window, so locker-room logging
can still count. Upload and logging both use the same inclusive boundaries and serialized transaction. Duplicate
uploads use the persisted window. Confirming a plan uses the latest-starting strength workout on its `plan_day`
in the owner's time zone, even if confirmation arrives hours later or the next day. Its sets use that workout's start
time and the usual window matching; with no workout on that day they use the confirmation time. Undo advances the
set's sequence; a repeated undo does not.

A multi-set interpretation or plan creates a managed conversation card: **Save**, **Don't save**, **Adjust**, or
**Done as planned** for a plan. Only Save confirms the sets, in one transaction with the card. A repeated click adds
nothing twice. These buttons are handled by Health without a model round or notification. Models cannot rewrite
the reserved `health-strength-` cards. Adjust opens the working copy; saving that page updates the draft and card,
not the set table. A stale edit reloads the newer draft; Cancel keeps the stored draft. If a card settles while its
editor is open, Save, stepper and add/remove actions redraw a read-only page and explain whether it was saved or
discarded. Below UI version 6 these edit actions retain the existing 400 response. Other invalid strength page
actions use strength-specific copy. A duplicate exercise name shows a toast and keeps the entered name for correction.
If an exercise on an open card was deleted, Save returns a definite 400 with guidance to discard or edit the draft;
no sets are written and Don't save remains available.
Deleting an exercise confirms its name and active set count, then tombstones it and undoes all its sets atomically.

The server draws the following pages with existing declarative components, under Health's shared reserved
`-health-intake` page id (`docs/protocol/protocol.md`, "Strength training"):

- Overview puts pending drafts first, followed by exercises with last/best, this week's sets per primary muscle
  (excluding warm-ups), the latest session and the latest open plan. Management exposes name, settings and weight rule.
- An exercise shows its seat note, weight rule, daily best weight or estimated one-rep max, and each set with its own
  weight and reps, including warm-up and drop sets. History ranges are 30/90/365 days; Undo only accepts a listed set.
- Adjust has kg steppers (2.5 per tap), reps steppers, an exercise choice, warm-up switch and add/remove controls for
  up to 40 sets. Below UI version 6 it displays a read-only list and an update hint.
- Home shows today's or the latest session, a bar chart of working sets over the last seven owner-local days
  including today (excluding warm-ups and undone sets, with zero bars on empty days), and up to three records in the last 30 days.
  A record requires an earlier non-warm-up set and exceeds its best weight or estimated one-rep max
  (`weight × (1 + reps / 30)`).

`health_strength` broadcasts contain no health data and make open pages refresh. Removing the item retains its data
and pages and permits existing cards to settle; new tool writes ask to add the item again. Workout reports fall back
to their existing behavior when strength is not added. Building or switching out Health removes the capability,
pages and tool handler, disables card buttons, and leaves stored data intact.

Strength reports assemble **when the task runs**, including sets logged after upload: this workout's sets grouped by
exercise, the previous day's sets before the workout, previous non-warm-up best weight/estimated max, explicit record
flags, the latest open plan for the workout's local day, measurements and the owner's language. A report record
requires a non-warm-up baseline before this workout. Plan sets carry exercise names (including deleted exercises),
weight, reps and warm-up flags, without draft timestamps or exercise UUIDs. They do not request
other Health data. With sets, the report lists and compares them; with a plan but no sets it asks `health_strength show`
to place the Done as planned card in the report's actual conversation. With neither it gives one line about logging
with a sentence or a machine photo. A failed assembly uses the stored prompt. Other activities retain the exact
existing report prompt. Three scenarios in English, Simplified Chinese and Japanese have caller-level goldens.

## Water and protein

Persona keeps water and protein entries in the default assistant manager's database (#13175).
The server half is the Health module (`server/internal/modules/health`, docs/architecture/core-and-modules.md
"Health"); a hub built or switched without it serves none of the routes below and announces no `health_intake`.
These are Persona's own records, counted independently of other apps' dietary samples below.
An entry has a UUID, kind, amount (ml or g), measurement time and optional food note. Recording
the same UUID again returns the original. Delete marks an entry as undone; it never revives on
a retry. Each add or first undo receives a new change number. Rows and undo markers are kept.
Today and calendar-day totals use the phone's last reported assistant time zone, including
23- and 25-hour days. Without one, intake resolves the machine's IANA zone from `TZ` or its
localtime link, falling back to `UTC` for both calculation and the returned identifier.

The hub forwards the manager's routes under `/api/v1/assistant/health/`:

| Route | Purpose |
| --- | --- |
| `GET intake` | Today's totals, goals and entries for both kinds, daily totals (7 days by default), and the time zone |
| `POST intake` | Record `{id,kind,amount?,at?,note?}`; water without an amount uses one configured cup |
| `DELETE intake/{id}` | Undo a record from any date |
| `GET intake/changes` | Current row states after a change number, including undo markers, in sequence order |
| `POST intake/ui` | Draw a detail page or handle its actions with the extension-page UI envelope |
| `POST water/reminder` | Handle a water notification's buttons and return a localized toast |
| `POST intake/import` | Import the legacy water extension and switch it off; full-access devices only |

Omit the selected manager header and `manager` query so the hub uses the default manager.
`space=health` may be sent for context, but the hub does not use it to select a manager.
Full-access devices may call
these routes; the watch's answer credential may only GET or POST `intake`. Extension,
scoped and read-only credentials cannot access the data. The manager owns measurements,
settings, pages and scheduling; the hub only authenticates, forwards and delivers pushes.
A content-free `health_intake` WebSocket event makes open apps reload. The hub wakes closed
iPhone widgets for intake changes at most once per 15 minutes, without changing Live Activity
content. Clients show these capabilities only when hello includes `health_intake`.

**Today on the Health home.** iPhone, iPad and the web show separate water and protein
rings with today's amount and goal. Water offers **Drank a Glass** (the configured cup)
and **Other Amount**; protein offers **Log Grams**. Logging updates the total locally at
once, then reconciles it with the hub. A successful log offers Undo; a failed write rolls
back and offers Retry with the same record UUID. A record already undone elsewhere stays
undone on retry. These totals come from Persona's intake records in the default manager,
not from Apple Health or other apps. Goals and the day boundary come from the same intake
response. Open pages reload after a `health_intake` event, a reconnect and a return to the
foreground, waiting for pending writes to settle.

Tapping either ring opens its detail page: a sheet with a close button on iPhone and iPad,
or a full page on the web using the existing extension-page route. iPhone also shows
Activity rings and health trends; iPad shows trends when Apple Health data can be read,
without Activity rings. The web shows neither Activity rings nor Apple Health trends.
Without the `health_intake` feature, iOS shows **Update your hub first** in Today and the
web omits the block without requesting intake data. Demo mode supplies sample intake
records and both detail pages through the in-app demo hub; page actions return sample
pages rather than saving detail-page edits.

**Details.** Water and Protein reuse the Subscriptions page's declarative renderer under
reserved extension id `-health-intake`, page `pages`. Initial state selects `kind:"water"`
or `kind:"protein"`. The pages show today's entries with Delete (protein includes what was
eaten), 7/30-day bar charts and how many dates met the current goal. Today's gauge and the
bar-chart series use the named category colors `blue` for water and `amber` for protein,
including after the goal is met. The existing UI-version fitting keeps the total, unit and
percentage when an older renderer cannot draw the gauge. Water's timezone note mentions
recording and reminders; protein's mentions recording only. Today's entries are
paged in groups of 40. Settings open from the page: water goal, cup size, reminders on/off,
start/end, interval, weekends, stop when done; protein goal. Changes are validated before
saving to `health.intake`. Invalid stored settings fall back to defaults and are logged, so
records and the settings form remain usable; a valid save repairs the stored setting. Defaults: water 2000 ml, cup 250 ml, reminders off; protein 60 g.
Water goal is 500–6000 ml, cup 50–1000 ml, interval 15–240 minutes; protein goal 10–300 g.
Start and end are exact `HH:mm`, with start before end. Each water entry is 1–3000 ml;
protein is 0.1–200 g and remains positive after rounding. Records support one decimal place. The water page and
notification text input use whole ml, as the water extension does.

**Reminders.** Water alone can remind, off by default. In the chosen time window it reminds
at the configured interval, optionally skips weekends and stops once the goal is met. Later
snoozes for 30 minutes; recording water from any entry point clears snooze and starts a new
interval. A restart catches up at most once within the allowed window. State lives in
`health.intake.water_reminder` (`last`, `snooze`, `reason`). The page explains the next reminder,
why reminders are off, and the last delivery status. It reads the hub's existing low-distraction
state: P1 reminders go to the briefing without ringing. The existing notification outbox
queues a reminder while the hub is disconnected. Quiet hours do not suppress these requested
reminders. The notification says “Time for some water” and today's progress; it carries no
chat content. Its fixed category is `HEALTH_WATER`; Drank a cup, Later and Record another amount
call the reminder endpoint. Button record IDs are stable UUIDv5 names, so repeated taps and
network retries record once, even after an entry was undone. Older clients show a plain alert.

Request and response fields, reserved page state, category action identifiers and examples
are in [protocol.md](../protocol/protocol.md) ("Health intake"). No language model is involved
in the pages, settings or scheduling, and none of these routes grants an extension access to
Health chats or memory.

### Moving from the water extension

A full-access device can request `POST /api/v1/assistant/health/intake/import` (#13636).
Nothing moves merely by opening Health. Before import, Today counts only Health's built-in
records; the extension continues to record and remind independently. GET intake includes
`water_extension:"pending"` while the extension is enabled, or installed without a receipt,
including one manually disabled before import. After a successful import it is omitted;
re-enabling the extension makes it pending again. An uninstalled extension never shows pending.

Import validates the complete extension file first, including old records. A corrupt file
returns its reason without changing records, settings, reminders or the source file. It then
commits the records in one transaction, switches the extension off using its normal dependent
checks, and reads the file again after shutdown to catch a drink recorded just before the
switch. UUIDv5 identities include the extension's storage key, record ID and UTC timestamp,
so a reset's repeated IDs remain distinct and retrying never duplicates or revives an undone
entry. A failed switch leaves Health settings and reminder timing unchanged; already imported
records remain and the same request can be retried.

After stopping the extension, import copies its saved water settings over built-in defaults.
The built-in reminder turns on only if both the extension and its reminder switch were on;
an untouched extension reminder switch means on. Last-reminder and snooze timing carry over,
with a later drink or existing Health reminder time retained to avoid an immediate reminder.
Protein uses its built-in default. The resulting settings, timing and migration receipt commit
together. The receipt is `settings` key `health.intake.water_import`, containing migration time,
newly imported count and extension version. The response returns that request's new-record
count, whether the extension is disabled, and the resulting settings. A completed import
retried while the extension remains off retains current Health settings and timing.

The extension remains installed with its data directory. **Migrate before uninstalling**:
uninstall deletes that directory. Afterwards, replace extension water widgets and watch-face
rings with the built-in ones; the old surfaces show their empty choose-an-extension state.
Rebuild shortcuts that used Run Extension Action with the built-in Log Water action. The
extension's Android water page disappears when it is off. The iPhone/iPad migration card
warns about these changes before the user requests import; web and Android do not offer the card.

On iPhone and iPad, a notice card above **Today** offers **Move into Health** only when the
intake response includes `water_extension:"pending"`. Demo mode and responses without that
field show no card. It explains that records and settings move into Health, the extension
is turned off, and its Android water page disappears. Nothing imports automatically.
Tapping the single-line button sends `POST /api/v1/assistant/health/intake/import?space=health`
through the default manager. Intake actions are disabled while it runs. Success refreshes
Today, removes the card and shows the imported count and extension-disabled confirmation,
with a reminder to replace widgets, watch faces and shortcuts. A failed import shows the
reason in a toast and keeps the card available for a safe retry.

The mock hub's intake domain starts with `water_extension:"pending"`. POST import adds two
synthetic drinks (200 ml and 250 ml) once, alongside its seeded intake records, and removes the
flag. `MOCK_WATER_EXTENSION=none` or `migrated` starts without it; `none` returns 404 on import,
while `migrated` returns zero new records. `MOCK_HEALTH_INTAKE=off` still disables the feature.
Both `/mock/reset` and `/mock/health-intake?reset=1` restore the seeded records, default settings
and initial migration state. This fixture contains no real records.

## Activity rings

On iPhone, the Health home shows an **Activity** section between Today and health trends
when HealthKit is available. It reads today's local Apple Health activity summary,
including Move, Exercise and Stand values and goals. Apple's Activity ring view draws the
rings on black with their standard colors. When both Exercise and Stand goals are absent, only Move is
shown; a paused summary is identified. The tile does not open another app.

The foreground **Allow Reading** button requests only the activity-summary read type when
it has not yet been requested. Loading the page checks whether a request is needed but
never opens the authorization sheet itself. The type is also part of the existing health
tool's read-permission set. Background readers do not request permission. When HealthKit
is unavailable the section is hidden. An empty result says there may be no data or reading
may not be allowed, and points to iOS Settings > Health > Data Access & Devices > Persona;
a query error says today's summary could not be read. An empty result is never treated as
proof that access was denied.

Activity rings are absent on iPad, Vision Pro and the web. Demo mode supplies intake data,
not a simulated activity summary: the iPhone tile follows the device's HealthKit availability
and authorization state. DEBUG screenshot examples can supply Allow Reading, unreadable,
Move-only and three-ring states without reading real health data.

## Dietary intake and resting energy

`health_intake` is authoritative for Persona's own water and protein records (see "Water and protein" above). Its `list` action returns every active record, with IDs, for 1–366 days including today (default 7), plus daily totals and goals; `delete` accepts a returned ID and repeated deletion succeeds. Each `log` generates a new server UUID, so an uncertain write must be checked with `list` before logging again. Explicit ml or protein grams can be logged directly; photos and other estimates first need an `ask_user` confirmation, with food and portion saved in the note. Repeatedly confirmed foods, or an explicit request to remember one, belong in Health memory; "the usual" still gets a confirmation card. Water reminders are configured on the water detail page.

When Apple Health synchronization is enabled, its protein total already includes the samples Persona wrote. Never add Persona's protein total to the Apple Health total: they overlap. Persona's own progress uses its intake records; the phone summary describes the system total from all available sources.

The Health space uses `phone_health_summary` for system health data, including dietary intake written by apps such as 薄荷健康. Its requested metrics are:

| Metric | Meaning | Unit | Daily aggregation |
| --- | --- | --- | --- |
| `dietary_energy` | Energy consumed | kcal | Sum |
| `protein` | Protein consumed | g | Sum |
| `carbs` | Carbohydrates consumed | g | Sum |
| `fat` | Total fat consumed | g | Sum |
| `resting_energy` | Resting energy burned | kcal | Sum |

Activity energy remains `active_energy` (kcal), separate from resting energy and intake. The summary contains daily totals, so meals and food details need their own source. Use the metrics advertised by the connected phone's tool schema.

For today's intake, call `phone_health_summary` with `{"metrics":["dietary_energy","protein","carbs","fat","resting_energy"],"days":1}`. Days start at the phone's local midnight; today's values end at the query time. Missing data or denied read access must not be presented as zero intake. The Health space's generated instructions include these names, units, day boundaries and limitations.

On iOS, `HealthTool` reads the corresponding HealthKit quantity types and requests their read permissions alongside the existing metrics. An existing installation may need authorization for these newly added categories. Open Persona, grant access, and reconnect it to the hub so the registered tool description and schema refresh (`docs/apple/phone-tools.md`, "Daily health summaries").

## Every health type and sample (#13740)

Data a band, ring, scale, glucose meter, ECG or hearing test saves to Apple Health or Health Connect is reachable sample by sample, with
the app and device that saved it, through `phone_health_types`, `phone_health_samples`, `phone_health_sources` and
`phone_health_request_access` (iPhone and Android), plus `phone_health_watch` on the iPhone. The audience is everyday users who ask
about the data they already keep, and Agents that analyse it for them; nothing in the Health page changes, and Apple Health stays named
where the app uses it (App Review 2.5.1, #13118).

- Start with `phone_health_types` to find the type name and whether Persona was asked for it; ask once with
  `phone_health_request_access` while Persona is open. A type without access answers its status, not an error, and empty results on an
  iPhone can still mean the user refused (HealthKit never tells).
- Read with `phone_health_samples`, keep its `anchor`, and pass it back later to get only what changed. Match samples by `id`.
- On an iPhone that may be locked when the question comes (an evening review, a scheduled task), watch the types first with
  `phone_health_watch`; locked answers then come from the phone's snapshot (`source: snapshot`, `as_of`), the last 7 days at most.
  A build without HealthKit background delivery refreshes those snapshots only when Persona opens or is woken while unlocked.
- Health data goes back only to the hub that asked and is not written to logs on the phone or the hub; it reaches the model that
  called the tool like every other tool result.

Details, anchors and platform differences: `docs/apple/phone-tools.md`, "Every health type, sample by sample".

## Web entry point

- A separate **Health** item in the sidebar opens the home at `/health` immediately ("Entering Health" above). `/health/new` remains an alias; a health chat's **New** goes to `/health`.
- Home (`/health`, also `/health/new`): a one-line privacy note (chats and memory stay in the Health space, data is on your own hub, not a substitute for a doctor), data sources (Apple Health / Health Connect, via the phone's `phone_health_summary` / `phone_health_write_sample`; Persona must be open on the phone and authorized),
  suggested questions (tap one to start a health chat with it), past health chats, health memory (view, delete).
- Tapping a chat opens `/health/<id>`, the same chat page as other assistant chats; its input box asks health questions as the home's does ("Ask a health question…", `ChatView`'s `placeholder`), not the main chat's line.
- Health chats no longer appear in the main chat's history list.


Verification image removed; see the associated PR.

Verification image removed; see the associated PR.

## iOS and Mac entry points (#2901)

- Entry on iPhone and iPad: the sidebar's **Health** opens the Health home immediately, with its open conversations listed newest first ("Entering Health" above). `AssistantView.enterHealth()` synchronously calls `backToRoot(); present(.health)`; the embedded Sidebar layout selects Health as its page root. Opening the home does not wait for the conversation list. **New Chat** in a health conversation still returns to the home, and the first message in its composer starts a new health conversation (`apple/Modules/Health/HealthView.swift`).
- Health page: a one-line note; **Works with Apple Health** (#13118), the Health app's icon and what is read and saved, which on an iPhone or iPad opens Settings › Connectors › This iPhone (App Review 2.5.1 wants HealthKit named Apple Health where it is used; the phone data row is **Apple Health** too); suggested questions (tap one to start a health chat with it), health chats (`GET /api/v1/assistant?space=health`, merged with pushed ones carrying `space: "health"`; swipe / right-click to archive),
  health memory (pushes the memory page `AssistantMemoryView(space: "health")`; view, edit and delete all carry `?space=health`), data sources, privacy. At the bottom is an input box; sending does `POST /api/v1/assistant/conversations` with `"space":"health"`, never set as the main chat.
- iOS input box (#3825): Persona's new conversation's composer in every style (`StarterComposer` in the shell's composer): + opens the attach panel on the iPhone (touch and hold, or the iPad's tap: the + menu), and the model button picks the launcher and one of its models (#11472; Health's, the default manager's, until one is picked), the effort and, a level down, the permission mode. The effort and mode are the ones Persona's new conversation picks (a health conversation already started with them). Files still go to `reports/`. The Mac keeps its input line (BeautifulUI: the prompt bar with its launcher menu).
- Health chats that are started or tapped open in the Persona page's chat, the same chat page as other assistant chats; go back to the Health page via **Health** in the sidebar.
- The sidebar's **Recent**, **Archived** and search do not list health chats (`AssistantLists` splits by `Session.assistantSpace`; health ones are in `AppModel.healthConversations`).
- Screenshot arguments: `-tab assistant -openAssistantPage health` (new shell: `-openShellPage health`); both still open the home. Tests: `PersonaTests/HealthModelTests`, `PersonaUITests/HealthEntryUITests`, and the Health upload case in `TreeSwapFoldTests`.

## Android entry point (#4329)

- Entry: **Health** below **Knowledge** in the assistant drawer (not in demo mode). It opens the Health home immediately, whether or not there are previous chats ("Entering Health" above; `AssistantScreen.kt` `openHealth`). The page loads its history in the background.
- Health home (the new health chat page): the hero line, the input box and the space's past chats (`HealthEntry.history`, newest first, tap to open). The first message starts the chat with `POST /api/v1/assistant/conversations?space=health` and `"space":"health"`, never `main`. The box takes no files (they would go to the main chat's machine, outside the Health space) and has no launcher picker.
- In a health chat the top bar's new chat button reads **New health chat** and opens that page. Health chats pushed live (`space: "health"`) stay out of the main chat lists.
- Requests carry `?space=health` on the URL, so the hub client sends no manager header: the space is the default manager's, as on the web.
- Tests: `HealthEntryTest`, `HealthEntryScreenTest` (feature/assistant), `InboxAssistantTransportTest.theHealthSpaceListsAndStartsItsChatsByName`, `HubManagerHeaderTest`, `SpaceConversationsTest`.

## Charts (#3140)

Health answers are mostly a set of numbers, and a chart is faster to read than text. In the second version the Health space attaches a `persona-card` **by default** when discussing numbers (the chart cards from #886; format and rendering in `sdk/go/wire/personacard.go`), and each client draws it as best it can:

| Content | Card | Example |
| --- | --- | --- |
| Per-day trends over the last 7 days (or longer): steps, sleep, weight, resting heart rate | `line` | Steps over the last 7 days |
| Calories per meal (comparing a few items, calories burned per workout) | `bar` | Calories per meal today |
| Daily calorie breakdown (how much each part takes of one total) | `ring` (#3069; a `stacked_bar` card draws one bar and reads poorly for one day) | Today's calories: breakfast / lunch / dinner / remaining |
| A single key metric | `stat` | Steps today |
| Details (each food, each item on a lab report) | Markdown table | |

- **Instructions**: in the Health space's CLAUDE.md (`server/internal/prompts/assistant/health_claude_md.md`, "讲数字默认配图表"): pick per the table above, put the conclusion and key numbers in the text with the card after it, at most three cards at a time, and do not fill days with no data with 0.
  `TestHealthClaudeMDTeachesCharts` checks that all four card types (`line`, `bar`, `ring`, `stat`) have examples, that none is a `stacked_bar`, and that every example is a card all clients can draw.
- **Web**: a health chat is the assistant's chat page (`ChatView`); code blocks are drawn as charts via `ui/AssistantCard.tsx` as before, with no separate implementation. What each client draws is governed by the cases in `schema/extension/examples/persona-card/` (web: `lib/assistant-view.test.ts`).
- **Lark**: cards cannot draw charts, and the JSON in a code block is not an answer either, so before sending, the bot rewrites each `persona-card` as a line or two of numbers (`stat` as one bold number, `line` / `bar` as a ▁▃█ sparkline plus the individual numbers, `stacked_bar` / `ring` as a small two-column table); see `docs/extensions/bots.md` ("Cards").

To see it: in the default mock hub (`python3 scripts/mock/mock_hub.py`, numbers are made up), the Health space's three chats "Sleep this week", "Step goal" and "What I ate today" all have charts.

Verification image removed; see the associated PR.

Verification image removed; see the associated PR.

## Checkup reports (#4546)

Checkup reports are usually PDFs dozens of pages long. If only the chat history remains after reading one, there is no way to compare across years, so the original and an item-by-item record both stay in the health directory.

- **Upload**: attach a PDF or another file in the input box of the web Health page (home and health chats); it goes to the assistant's machine via `POST /api/v1/machines/{mid}/files?dest=health`
  (`proto.UploadDestHealth`). node stores it in the health directory's `reports/` (`originalname-YYYYMMDD-HHMMSS.ext`, 0700 / 0600, never expires; the computer's manager's health space: `health-<id>/` for another manager than the built-in one, `docs/assistant/managers.md` ("Directories on a computer"). Reports uploaded before the computer moved to another manager stay in the previous manager's space there: the new owner does not list them, and they show again if the computer moves back), and the path is inserted into the input box and sent with the message.
  Images are still sent as images (Claude looks at them directly) and are not saved to disk. The main chat cannot read `reports/`: it is inside the health directory denied by #3282.
- **Record**: the health chat reads the original and writes a same-named `.md` next to it: report date (the exam date printed on the report), institution, type, a table of "Item | Result | Unit | Reference range | Abnormal",
  values copied as in the original and "illegible" where unreadable, ending with the doctor's conclusions and advice. Photos sent directly in the chat get a record the same way. The procedure is in the health CLAUDE.md (`server/internal/prompts/assistant/health_claude_md.md`, "数据").
- **Compare**: when asked about "the past few years" or "compared with last year", it reads all records under `reports/`, merges the same item ("triglycerides" and "TG") and compares by date, drawn as a `line` card; originals without a record get one first.
- **No approval every time**: the health chat's `--allowedTools` includes `Edit(//<health dir>/reports/**)` (`healthReportRules` in `node/healthdeny.go`), so writing records does not pop a confirmation on the phone;
  everywhere else in the health directory still asks according to the chat's permission mode. Claude Code matches only `Read` and `Edit` rules by path; `Edit(path)` covers every file-modifying tool, and `Write(path)` never matches,
  so tapping **Always Allow** on the phone for Write / MultiEdit / NotebookEdit also records `Edit(<dir>/**)`.
- Tested (Claude Code 2.1.318, two fake reports): both records were copied correctly item by item, the date was the exam date on the report, "TG" and "LDL-C" were matched with last year's "triglycerides" and "low-density lipoprotein", and the answer had three `line` cards.
- iOS / Mac (#4587): files picked via the Health page's input box (both interface styles) and via **Files** in a health chat's input box, as well as files dragged in on Mac, all carry `dest=health` (`FileUploads.start(…, dest:)`);
  photos are still sent as images, and videos still go to the temporary directory. A hub / node older than #4546 returns 400 / "unknown upload destination"; the App reports the upload failed, which goes away after updating the hub.
- **Entry point (#4656)**: with only the input box's `+`, it is not obvious you can upload, so the Health page has an explicit **Upload a Checkup Report**: on the web below the home input box, on iOS / Mac at the front of **Try Asking**.
  Tapping it picks a PDF or images (images are also uploaded as files, with the original kept in `reports/` as well). Once everything is uploaded, a health chat starts automatically (`modules/health/reports.ts`, `FileUploads.startAll`) with the paths in the first message;
  it still starts if you leave the page mid-upload. To test the whole path: iOS launch argument `-showReportUpload <file name>[,<file name>…]` (on the mock hub, look for `POST …/files?dest=health` and `POST /api/v1/assistant/conversations`); with `-showReportUploadMB <n>` each file is that big and stays on its way a while.
- **View and delete (#4998)**: **Checkup Reports** on the Health page lists reports in `reports/` (`server/internal/node/healthreports.go`): an original and its same-named `.md` record count as one report; record-only reports (photos sent in chat) count too.
  View the structured record, open the original or delete. Deletion is permanent and removes both original and record; **content already mentioned in chats and health memory is not deleted with them**, as the confirmation explains. Names must identify a single regular file in `reports/`; symlinks, directories and `../` are rejected.
  On the web it is on the home page (`modules/health/HealthReports.tsx`); iOS / Mac use a separate page (`HealthReportsView.swift`). An old node returns 409 `node_outdated`, which the page displays. APIs live in `server/internal/devices/healthreports.go`, forwarded by hub (`devicewire.Routes`).
  Screenshot arguments: `-openReportRecord YES` (open the first report's record directly), `-showReportDelete YES` (show delete confirmation directly), both starting from the reports page.
- Not yet: Android (its Health entry has no reports section and its box takes no files, #4329).

## Not in v1

- Packaging the diet-advice extension: another Agent is doing it.
- Suggesting moving to the Health space when the main chat touches on health.
- Migrating existing health content discussed in the main chat: not migrated; old memory stays in the assistant directory.

## Health trends on iOS (#3272)

- `apple/Modules/Health/HealthTrendsSection.swift`: **Health Trends**, steps (`bar`), sleep and weight (`line`; `stat` when there is only one day) over the last 7 / 30 days, drawn as `persona-card` with the same renderer as cards in health answers (`AssistantViewCard`).
- Data is read directly from Apple Health on the iPhone or iPad (`HealthTool.daily`, the same queries as `phone_health_summary`), not via the hub. The reusable section can offer **Allow Access** before permission has been requested, or explain how to allow reading when it has no data; the home hides these empty states as described below.
- On the Health home, trends follow Today and, on iPhone, Activity rings. iPhone and iPad show the section only when at least one metric has data; unavailable, unreadable or not-yet-authorized data leaves it hidden. Home reads do not request access. Vision Pro and the web do not include this section.
- Screenshots: `-showHealthTrends sample` (the simulator has no health data, so sample data is used), `empty`, `ask`, `YES` (reads the device).

## Later

- Suggest "Continue in Health" when the main chat touches on health.
- diet-advice push notifications go into the Health space.
- The main chat reading the health directory via Bash or via Codex: block it fully with a sandbox or OS-level isolation (after #3282).
- More data sources: medical records, checkup report upload on Android (web and iOS: see "Checkup reports" above), heart rate / HRV from recordings (#1950).

## How to verify

- Server (inside `server/`): `go test ./internal/assistant -run Health` (`server/internal/assistant/health_test.go`: starting health chats, separate lists, `main` and unknown `space` rejected, tools do not cross spaces, outbound, golden),
  `go test ./internal/hub -run Health` (push without body), `go test ./internal/connector -run ConfirmEach`, `go test ./internal/node -run LarkGateHealth`, `go test ./internal/identity` (health directory, `SpaceOf`, `IsAssistantDir`).
  After changing `health_claude_md.md`, update the golden and review the diff (`docs/assistant/prompts.md`). Charts: `go test ./internal/assistant -run HealthClaudeMDTeachesCharts`, `go test ./internal/modules/lark/feishubot -run PersonaCards`.
- Web: `cd web && npm test && npm run build`.
- iOS / Mac: `PersonaTests/HealthModelTests` (lists and memory carry `space`, merging pushes, sidebar does not list health chats, new chats carry `space`); build `Persona` and `PersonaMac`.
- iPhone dietary intake acceptance (real device; not covered by server or simulator tests):
  1. In 薄荷健康, enable writing dietary energy, protein, carbohydrates and total fat to Apple Health, record today's intake, and confirm the resulting entries in the Health app. Note the totals actually present there.
  2. Install/open the updated Persona app, enable Health under Settings › Connectors › This iPhone, and allow the new categories in the system Health authorization sheet. For a previously denied category, enable its read permission in Health's data-access settings. Keep Persona connected to the hub.
  3. In the Health space, request today's intake, or call `phone_health_summary` with `{"metrics":["dietary_energy","protein","carbs","fat","resting_energy"],"days":1}`. Verify that the hub's advertised metric enum includes all five names and the returned `date` is today in the phone's time zone.
  4. Compare the four dietary values against the entries written to Health (kcal / g, rounded to one decimal). If other sources also wrote entries, compare with the relevant Health totals. `resting_energy` should match available basal-energy data; it may be absent when no source recorded it. A missing value stays absent, rather than becoming zero.
  5. Add another food entry in 薄荷健康, wait until it is visible in Health, and query again. The day's total should include both entries; no previous-day entry should enter today's total. This check verifies the source app → Health → Persona → hub path.

## Syncing to Apple Health

On iPhone, **Settings › Health › Sync to Apple Health** is off by default. It is hidden in demo mode and on other
devices, and requires a hub with `health_intake`. Turning it on requests write access to water and protein, then
backfills all records, including imported history, at their original times. Turning it off stops sync and keeps
samples already written. This is one-way: Apple Health edits do not change Persona's intake records.

Each record writes one sample with its record UUID as `HKMetadataKeySyncIdentifier`, sync version 1, and
`HKMetadataKeyWasUserEntered`. Replaying history uses the same identifier and version. Undoing a record deletes the
sample with that identifier; HealthKit permits deletion only of samples Persona itself saved. Other apps' samples
are untouched. This opt-in sync is separate from the AI's `phone_health_write_sample` tool, which still requires
confirmation for each sample and never writes water or protein or deletes data.

The device stores a protected, backup-excluded cursor bound to the group and actual hub identity selected when sync was enabled. Another hub
does not inherit consent or progress; explicitly enabling sync there starts from zero. A missing cursor also starts
from zero. The client reads the full-device-only changes stream, including tombstones, in sequence order. It saves
progress only after each HealthKit operation succeeds and stops on the first write, delete, network or checkpoint
failure. The next trigger retries that entry. Settings shows the last completed sync, missing write permissions,
or the last failure. After allowing write access in system settings, the next trigger continues from that cursor.

Triggers are foreground entry, a successful local record or undo, `health_intake` broadcasts, and the existing hub
wake for widgets. The phone must be unlocked before each operation. No new background task or delivery entitlement
is added; a closed app without a widget catches up when next opened. Turning sync off does not remove past samples.

Simulator verification establishes storage behavior, not physical-device lock, iCloud or background delivery.
The iOS 27.2 simulator's Health app does not expose the nutrition views used for visual acceptance. As part of
rollout, the user verifies visible water/protein entries, replay and undo in the Health app on their physical iPhone
using the TestFlight build, before enabling sync on real records.

`HealthIntakeSyncTests` uses a fake writer for cursor, failure and hub-scope cases. The opt-in
`HealthIntakeHealthKitTests` integration suite (`TEST_RUNNER_PERSONA_HEALTH_LIVE=1`, after granting the system write
sheet on a disposable simulator) checks both types against the real HealthKit store: same-version replay stays at
one sample; deletion and repeated deletion of a missing identifier succeed and leave zero. With Xcode 27.2 and the
iOS 27.2 simulator these checks pass; repeat them on a physical iPhone before release.

## Automatic workout reports (iOS)

Settings → Health → Automatic workout reports is off by default. Enabling it requests
HealthKit workout, heart-rate and distance read access and records a consent timestamp.
Opening or foregrounding the app uploads completed workouts lasting at least ten minutes
whose start is after that timestamp. The device must reconnect to the same paired hub.

**Right after a workout (opt-in build, #2526).** A build made with config.json
`app.health_background_delivery: true` (`docs/operations/config.md`) is signed with the
HealthKit background delivery entitlement, and sets Info.plist
`PersonaHealthBackgroundDelivery`. The switch is off by default, because an App ID whose
provisioning profile doesn't allow the entitlement fails to sign, or a cloud-signed export
drops it silently. Turn it on only after a signed build shows the profile allows it.
`scripts/release/entitlements.py check` then reports a release that lacks it.

In such a build:

- `WorkoutReports.observe()` runs from `application(_:didFinishLaunchingWithOptions:)`,
  where HealthKit wants it for a background wake, and again when reports are turned on.
- It sets up an `HKObserverQuery` on workouts and `enableBackgroundDelivery(frequency:
  .immediate)`.
- A new workout wakes the app, even while Persona is closed. The app runs the same
  `refresh`, then calls HealthKit's completion whatever happened, since HealthKit retries
  a wake that never completes.
- Turning reports off stops the query and disables delivery.
- The Settings footer says which mode is active (`WorkoutReports.immediate`): right after
  a workout, or when Persona is opened. If HealthKit refuses delivery at run time, the
  footer falls back to the second.

Only such a build reads the reports' small state file at launch; other builds do as before.

Reports appear in the Health space using existing persona-card measurement cards,
a minute-average heart-rate chart when available, and a brief interpretation. Missing
measurements are omitted; route coordinates are not uploaded.

Since #2526's second step, a report also carries:

- the elevation gain;
- the heart rate's average and maximum;
- per-kilometre splits for runs, walks, hikes and rides, with pauses left out.

Strength training instead uses the execution-time assembly described in "Strength training" above, when that item
is added and its uploaded window is stored. Other activities retain the following comparison behavior.

The report then draws a pace chart from the splits, and compares the workout with the
user's recent ones of the same activity through `phone_workouts_recent`. When the tool
isn't available or finds nothing else, the comparison is skipped and not mentioned.
The Health conversation's instructions point later questions at `phone_workouts_recent`
and `phone_workout_detail` (`docs/apple/phone-tools.md`, "Workouts").

The hub validates the new fields like the others:

- the elevation is at most 20 km;
- the heart rate is in (0, 300] bpm, with the average no higher than the maximum;
- splits number kilometres 1, 2, … with positive seconds, at most 1000, summing to no
  more than the workout.

A report queued by an older app, without these fields, is still accepted.

The device atomically stores its HealthKit cursor with a bounded upload outbox.
Failed uploads are retried on the next foreground visit, except a report the hub
rejects as invalid (HTTP 400, 413 or 422): the same data gets the same answer, so
the device drops it and keeps going, and Settings → Health says some workouts were
skipped. Without that, one bad report would hold back every later workout. The hub
accepts end times up to a day past its own clock, so ordinary clock drift between
the phone and the hub is never a rejection. The assistant stores a
workout UUID receipt in PostgreSQL so retries, restarts, and deletion of an old task
do not recreate it. Turning the switch off clears the local pending uploads; already
accepted reports may still complete. Re-enabling starts a new consent window.
