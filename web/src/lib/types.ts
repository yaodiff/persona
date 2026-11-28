import type { AgentPhase, AgentView } from "./agent-view-wire";
import type { UsageCard } from "./tokens";
import type { RuleSuggestion } from "./rule-suggestion";
// The hub's wire types (docs/protocol/protocol.md). Only the fields the web client reads.

/** Frozen Session.state and the host-state filters on older paging endpoints. */
export type AgentState = NonNullable<Session["state"]>;
export type StatusMarkState = AgentPhase | AgentState;

/** An engine as the machine's node found it (docs/integrations/engines.md): `note` says why one that is there cannot be used.
 *  `state` (Orca's row only, #17037; proto.EngineState) tells installed-and-stopped from not there; read it through
 *  engines.ts `engineState`, which turns a state this client does not know into "unknown". */
export interface DetectedEngine {
  id: string;
  name: string;
  available: boolean;
  version?: string;
  note?: string;
  state?: string;
}

export interface Machine {
  id: string;
  /** The manager it belongs to (docs/protocol/protocol.md "Per-manager routing"); none: the default one. */
  manager?: string;
  name: string;
  online: boolean;
  error?: string;
  hostname?: string;
  os?: string;
  claude_version?: string;
  herdr_version?: string;
  node_version?: string;
  node_mismatch?: boolean;
  deploying?: boolean;
  can_deploy?: boolean;
  deploy_reason?: string;
  deploy_phase?: "waiting_idle" | "transferring" | "draining" | "restarting" | "confirming";
  deploy_error?: string;
  /** Where the owning assistant obtained the program for this deployment, when known. */
  deploy_source?: "hub_package" | "local_build";
  system?: { name?: string; model?: string; cpu?: string; cores?: number; memory?: number };
  /** The terminal engine its sessions come from (herdr when absent); `engines`: what its node found, empty while offline. */
  engine?: string;
  engines?: DetectedEngine[];
  /** The hosts enabled side by side and the default one (#2576, docs/integrations/engines.md "Host sets"); absent from older hubs. */
  engine_hosts?: string[];
  engine_default?: string;
  /** What to preselect when the machine is paired: what its node found available, else herdr (only while online). */
  engine_suggested?: { hosts: string[]; default: string };
  /** The node is too old for sets and runs `engine` alone until it is updated. */
  engine_sets_unsupported?: boolean;
  /** "Start new Agents as Orca chats" (#17650): the switch, on or off, present only while Orca is in its engine set (where
   *  it applies); absent elsewhere and from older hubs, which then offer no switch. A value this client does not know reads
   *  as absent (lib/orca-chat.ts orcaChatSwitch). Changed with PUT …/orca-chat-starts {"on"}. */
  orca_chat_starts?: OrcaChatStarts;
  kind?: string;
  arch?: string;
  home?: string;
  last_seen?: string;
  /** Interconnected hubs (docs/archive/operations/plan-hub-mesh.md 2.6): the hub that owns this computer, and the way there. */
  hub_id?: string;
  hub_name?: string;
  machine_uid?: string;
  route?: { via: string; hops: number; rtt_ms?: number };
  /** Operating it through Cua Driver (the hub's extension cua, docs/extensions/computer-use.md): only while the extension is on and it is a Mac. */
  computer?: {
    enabled: boolean; installed?: boolean; daemon?: boolean; ready?: boolean; accessibility?: string; screen_recording?: string;
    desktop?: string; screen_sharing?: boolean; note?: string;
  };
  /** ego lite (the hub's extension ego, docs/plans/plan-ego-browser.md): only while the extension is on and it is a Mac. */
  browser?: import("./browser").MachineBrowser;
  /** iPhone control (the hub's extension iphone, docs/extensions/iphone.md): only while the extension is on and the bridge runs on its system (macOS, Linux). */
  iphone?: import("./iphone").MachineIPhone;
  /** feature machine_trust (docs/protocol/protocol.md "Computer trust"): "pending" while the user has not said it is theirs; omitted = confirmed. */
  trust?: "pending";
  /** What `error` is about, for words of our own: node_service_down / link_offline / never_connected / node_outdated. */
  error_code?: string;
  /** Switching this ssh computer to Persona Link (#10662): set by a manager that can, for its ssh computers and for an
   *  hour after a switch ended; absent from older hubs, which then offer nothing. */
  link_switch?: MachineLinkSwitch;
}

/** `Machine.orca_chat_starts` (#17650, `proto.OrcaChatStarts`): a closed set; a later value reads as absent. */
export type OrcaChatStarts = "on" | "off";

/** Where switching an ssh computer to Persona Link stands (`proto.MachineLinkSwitch`): `available`, a switch can start
 *  now, else `reason` says why not; `phase`, the current or last switch's ("" none), and a failed one's `error`, with
 *  `rollback_unconfirmed` when the computer could not be shown to be back on SSH. Reasons and errors are codes the
 *  apps put in their own words (lib/link-switch.ts). */
export interface MachineLinkSwitch {
  available: boolean;
  reason?: string;
  phase?: "" | "checking" | "installing" | "connecting" | "rolling_back" | "succeeded" | "failed";
  error?: string;
  rollback_unconfirmed?: boolean;
  /** when `phase` last changed (the hub's clock) */
  at?: string;
}

/** The hubs of this hub's interconnected group, from the signed-in hello (docs/archive/operations/plan-hub-mesh.md 2.6). */
export interface MeshHub {
  hub_id: string;
  name: string;
  online?: boolean;
  self?: boolean;
  endpoints?: string[];
}

/** A plan or task list a folded call wrote (the hub keeps it with the fold, transcript.foldTaskSnapshots): `replace:
 *  "todo"` is a TodoWrite's whole list. */
export interface FoldTaskSnapshot {
  replace?: string;
  entries: { id: string; subject?: string; description?: string; status?: string }[];
  source_item_id: string;
  source_tool_use_id: string;
  source_seq: number;
}

export interface Subagent {
  id: string;
  session_id: string;
  type?: string;
  description?: string;
  running?: boolean;
  tool_use_id?: string;
  /** The subagent that launched it; absent when the session did. One not in the list: shown at the top level. */
  parent_id?: string;
  /** A fork of its launcher's conversation. */
  fork?: boolean;
  background?: boolean;
  started_at?: string;
  updated_at?: string;
  last_text?: string;
  /** Tool calls it made (newer nodes, #5602). */
  steps?: number;
  /** Its latest tool call: what it is doing now while it runs (newer nodes). */
  last_tool?: { id?: string; name: string; summary: string };
  /** The model it ran on, from its own transcript (newer nodes, #5030); never the model its call asked for. */
  model?: string;
  /** Its latest reply's context plus output tokens, as a workflow agent's `tokens` (newer nodes, #5030). */
  tokens?: number;
}

/** Session.model_pick (#5033): model absent is the launcher's default, effort absent the one it runs with. */
export interface ModelPick {
  launcher: string;
  model?: string;
  effort?: string;
  /** The four dimensions' runtime and channel (lib/execution.ts); the hub projects them from the launcher, and back. */
  runtime?: string;
  channel?: string;
}

/** Session.pending_switch (#5035). `agent` is set on a switch to the other engine (feature `engine_switch`, #5054):
 *  "claude" or "codex", the engine the next message moves the conversation to. */
export interface PendingSwitch {
  launcher: string;
  model?: string;
  effort?: string;
  agent?: string;
  at?: string;
}

/** One earlier stretch of a conversation that moved between Claude Code and Codex (Session.engine_history, #5054): the
 *  session that ran it, its engine ("claude" / "codex"), and when the conversation was switched away from it. */
export interface EngineLeg {
  session_id: string;
  agent: string;
  until: string;
}

export interface SessionCapabilities {
  takeover?: { running: boolean; can_resume: boolean; reason?: string };
  read?: boolean;
  send?: boolean;
  interrupt?: boolean;
  queue_edit?: boolean;
  queue_send_now?: boolean;
  approve?: boolean;
  terminal?: boolean;
  attach?: boolean;
  /** Whether the runtime explicitly supports session model changes. */
  model?: boolean;
  /** Closing the agent independently of terminal access. Older nodes retain terminal-only closing. */
  close?: boolean;
  /** Renaming it without a terminal (a runtime's session, #6294). Absent: the app's own rule (lib/format.ts sessionCanRename). */
  rename?: boolean;
  /** The permission mode and effort chips can change it (session.modes, #1843): the node's word, absent from older nodes. */
  modes?: boolean;
  /** The permission modes session.modes takes for an agent that is not Claude Code (#8261): its own approval
   *  (`default`) and, where it has one, Full access. With two or more the composer switches between them (lib/session-actions.ts
   *  `listedModes`); absent from older nodes, and where `modes` is true. */
  permission_modes?: string[];
  /** Returning to before a user message, with optional native file restoration. */
  rewind?: boolean;
  rewind_code?: boolean;
  /** A Claude outside Persona's terminals with Persona's plugin: your own messages go in as your input while it rests
   *  (docs/integrations/claude-integration.md "Owner input through the Persona plugin"). Absent from older nodes. */
  plugin_input?: boolean;
}

/** A session's work state (docs/assistant/agent-work.md): working, blocked on a named precondition, or done. */
export interface WorkState {
  phase: "working" | "blocked" | "done" | "";
  /** End of this turn, unlike state_since which changes when a reply is read or the hub restarts. */
  turn_at?: string;
  /** What a blocked one waits on: user (a decision), agent, external (a deploy, CI, a time), transient (a model error). */
  on?: "user" | "agent" | "external" | "transient";
  /** One line for the lists: what it waits for, or what it concluded. */
  line?: string;
  wait?: string;
  then?: string;
  /** When the hub acts next on its own: "Continue" (transient) or a wake (external, agent). */
  wake_at?: string;
  /** When a subscription usage limit that stopped the turn resets (docs/assistant/agent-work.md "Usage limits"). */
  resets_at?: string;
  attempts?: number;
  max?: number;
  agent?: { machine_id: string; session_id: string; label?: string };
  link?: { url: string; expires_at?: string };
  artifacts?: { kind: string; ref: string }[];
  /** What the agent said of its own work as the turn ended (lib/closeout.ts). A turn the hub could not call finished
   *  (the closeout says merged and it is not) carries it with an empty `phase`. */
  closeout?: Closeout;
  interaction_id?: string;
  /** The hub's triage found the turn done before anybody looked at it, less than three hours ago: listed as Just
   *  finished (lib/agent-groups.ts justFinished, docs/protocol/agent-states.md "Just finished", #9751). Older hubs
   *  never send it. */
  just_finished?: boolean;
}

/** An agent's closeout (docs/assistant/agent-work.md "Closeout"): its own claim of where the work stands. `merged` and
 *  `deployed` are "yes" or "no", absent when not said; `checked` is what the hub found when it looked itself, absent
 *  where it could not tell. Session lists leave out `did` and `verified`. */
export interface Closeout {
  status: "done" | "not_done" | "blocked" | (string & {});
  /** Who or what a blocked turn waits for. */
  waiting?: string;
  did?: string[];
  merged?: string;
  deployed?: string;
  verified?: string;
  missing?: string;
  checked?: { merged?: string; deployed?: string };
}

/** Settings › Session triage (GET / PUT /agents/triage): all off until the user turns them on. */
export interface TriageSwitches {
  enabled: boolean;
  auto_continue: boolean;
  wake: boolean;
  decide: boolean;
  /** Detect repeated tool calls during a turn (off when absent). */
  tool_loop?: boolean;
  /** Ask for a closeout (#3612): hold an agent's stop once when a turn that did work ends without one (off when absent). */
  closeout?: boolean;
  /** Ask afterwards (#3616): one status question to an unsupervised agent whose turn did work and ended without a closeout (off when absent). */
  closeout_ask?: boolean;
  /** Remind When Seen, Unanswered (#5096): remind once when an agent asked the user something and it stays unanswered after they saw
   *  it. Rule-based, no tokens, so it is on when absent and does not depend on `enabled`. */
  reply_reminder?: boolean;
  /** How long after the user saw it the reminder goes out, in minutes (the hub's default when absent). */
  reply_reminder_minutes?: number;
}

/** A port an agent's processes listen on that answers HTTP (docs/operations/ports.md): its dev server. */
export interface Port {
  port: number;
  addr?: string;
  pid?: number;
  process?: string;
  title?: string;
}

export interface Session {
  /** Explicit references to a deleted Assistant conversation return a scrubbed tombstone. */
  deleted?: boolean;
  /** The engine host it lives in (herdr, herdr-core, orca, paseo…), and the other hosts it is live in at once (#2576). */
  host?: string;
  also_open_in?: string[];
  /** Node-verified physical working directory; the original cwd stays available for existing session flows. */
  workspace_cwd?: string;
  /** The main checkout of the repository cwd belongs to: the node reads it off git (#1805), Paseo names its own (#2840).
   *  "By repo" groups by it. */
  repo_root?: string;
  /** The top of the git working tree cwd is in (repo_root for the main checkout) and its branch, from the node (#1805). */
  worktree?: string;
  branch?: string;
  machine_id: string;
  session_id: string;
  /** The manager it belongs to (docs/protocol/protocol.md "Per-manager routing"); none: the default one. */
  manager?: string;
  title: string;
  cwd: string;
  status: string;
  starting?: boolean;
  /** A Codex opened with no first message: it runs in its terminal but has no thread yet, so this row's id is the
   *  computer's, not the thread's. Its first message (text only) goes in here or in the terminal; then the row goes and
   *  the thread's comes, in the same pane (docs/integrations/codex.md "Starting without a first message"). */
  awaiting_first_message?: boolean;
  start_error?: string;
  /** The failed start's coded refusal (launcher_missing, claude_missing…) and its details; start_error is the hub's sentence for it. */
  start_error_code?: string;
  start_error_args?: Record<string, string>;
  state?: Extract<AgentPhase, "needs_you" | "working" | "idle"> | "unread" | "offline" | "unknown";
  view?: AgentView;
  /** Local seen-outbox overlay until the manager acknowledges the read. */
  locallySeen?: boolean;
  state_since?: string;
  agent?: string;
  agent_name?: string;
  source_kind?: string;
  source_thread_id?: string;
  source_instance_id?: string;
  parent_thread_id?: string;
  read_only_reason?: string;
  capabilities?: SessionCapabilities;
  attached?: boolean;
  /** "Start a new session" (a branch of it, #2137) works for it (a Claude Code conversation, node/fork.go); older nodes never say so. */
  can_fork?: boolean;
  /** What to call an agent the app does not know (a runtime's own name, "DSH"; docs/integrations/runtimes.md). */
  agent_label?: string;
  /** The client the agent runs in: "desktop" (its desktop app) or "cli" (its command line); absent when unknown (#6390). */
  agent_surface?: string;
  launcher?: string;
  /** A Persona chat's own pick (feature `assistant_model_pick`, #5033): the launcher, model and effort its next messages
   *  run on, kept on the hub for every device. Absent: it follows the default (the main chat) or keeps what it runs
   *  (docs/assistant/model-config.md "A conversation's own model"). */
  model_pick?: ModelPick;
  /** A switch of this running Claude Code Agent to another launcher (feature `launcher_switch`, #5035) that its next
   *  message carries out: the launcher, the model and effort picked (absent: the launcher's default, the effort it runs
   *  with now). `launcher` above stays the one it runs on until then (docs/protocol/protocol.md "Switching a running Agent
   *  to another provider"). */
  pending_switch?: PendingSwitch;
  model?: string;
  permission_mode?: string;
  effort?: string;
  /** This Claude Code may switch to Allow All while it runs: it was started with a flag that allows it (#1379). */
  bypass_available?: boolean;
  /** Full access was picked while this Claude Code cannot reach it with shift+tab: the node restarts the session in it
   *  once its turn is over, resuming the same conversation (#6476, node/mode_restart.go); `permission_mode` is the mode
   *  it still runs in until then. */
  bypass_pending?: boolean;
  /** On a botmux session whose sends go through the untrusted external-event trigger (botmux does not advertise
   *  ownerActor, or no BotMux Owner Union ID is set), the composer shows a one-line notice. */
  botmux_external_trigger?: boolean;
  /** Why, when `botmux_external_trigger` is set: `upgrade` (botmux before 3.59.23) or `owner_unset` (Settings › Agents
   *  has no BotMux Owner Union ID). Absent from an older node: the upgrade notice. */
  botmux_external_trigger_reason?: "upgrade" | "owner_unset";
  /** What the model saw in the latest reply and the model's window, in tokens (#2430); omitted while the node cannot tell. */
  context_tokens?: number;
  context_restart_recommended?: boolean;
  context_window?: number;
  /** Structured context window occupancy split with exact/estimated honesty marker (proto.ContextBreakdown, #12542). */
  context_breakdown?: ContextBreakdown;
  /** The levels `effort` may be set to when they are not Claude Code's: a Codex thread driven through its app-server (only its effort changes; docs/protocol/protocol.md). */
  efforts?: string[];
  /** Native Codex speed. Absent while the current setting is unknown. */
  service_tier?: string;
  service_tiers?: { id: string; name: string; description?: string }[];
  /** Claude Code's session-only ultracode switch as the node last saw it (docs/protocol/protocol.md "Claude Code ultracode"):
   *  absent is unknown, not off. */
  ultracode?: boolean;
  /** Why the switch can't be used, was refused or could not be confirmed (the node's words, in Chinese). */
  ultracode_note?: string;
  /** A switch request waits for the turn to end ("Applies next turn"); not the requested value. */
  ultracode_pending?: boolean;
  /** It was on before the agent resumed, and the resumed process has it off: the chat offers to turn it back on. */
  ultracode_lost_at?: string;
  assistant?: boolean;
  /** The assistant's space it runs in: "health" for the health space's (docs/assistant/health.md). */
  space?: string;
  started_by_persona?: boolean;
  space_id?: string;
  space_name?: string;
  space_index?: number;
  /** The space this one is nested under in its host, same machine and kind (Orca: a child worktree's parent, #2073). */
  parent_space_id?: string;
  slot_id?: string;
  slot_name?: string;
  slot_index?: number;
  /** The pane's 0-based place in its tab by layout, top left first (#2320): 0 is the pane the others were split from,
   *  the tab's "Lead" when an agent runs there. Absent when the host does not say (older nodes, hosts without layouts). */
  pane_index?: number;
  /** The host's pane, tab and workspace ids (herdr: bare "w9:p1"; other hosts "<kind>:<id>"). Workspace ids change
   *  across a herdr restart: never key anything stored by them (pins and folds go by machine/space name). */
  pane_id?: string;
  tab_id?: string;
  workspace_id?: string;
  /** Which host the space belongs to ("herdr", "orca", a runtime id…): space ids are unique only within one. */
  space_kind?: string;
  updated_at?: string;
  seen_at?: string;
  last_text?: string;
  /** The question its last reply put to the user (#5096), when its closing words ask something. */
  asks?: string;
  /** It stopped (unread or seen) on a question to the user nobody has answered yet (#5096): Awaiting Reply once seen. */
  awaits_reply?: boolean;
  /** Its latest step when that is a tool call no text came after ("running tests", "reading tasks.go"), and when it started. */
  activity?: string;
  activity_at?: string;
  /** Its latest turn ended in a model API error (the hub sets it while it is stopped, #1516): its task is interrupted. */
  turn_error?: string;
  /** What the agent's record says of turn_error: "transient" (worth a "Continue"), "user" (the user must act),
   *  or "usage_limit" (a subscription limit, until it resets). Or the hub's own word on a task Agent's turn with no
   *  output (#10187): "no_output" or "stopped" (Persona's Stop interrupted it), turn_error then being its sentence. */
  turn_error_kind?: string;
  /** What is wrong, when the hub can tell from turn_error (docs/assistant/onboarding.md 5.2): not_logged_in / auth_failed /
   *  model_route / rate_limited / model_fallback_unavailable (the model Claude Code fell back to after a refusal can't
   *  be used: no "Continue"). */
  turn_error_code?: string;
  /** The refusal fallback still active in this session, as Claude Code recorded it (#2289). */
  model_fallback?: ModelRefusal;
  /** "Back to the configured model" was asked and no newer reply has come yet (#2528): the next start asks for `to`. Not
   *  proof the configured model runs. */
  model_restore?: ModelRestore;
  /** When turn_error_kind is "usage_limit": when that subscription limit resets. Absent when it is not known. */
  turn_error_resets_at?: string;
  /** The CLI continues by itself once the usage limit resets. */
  turn_error_resumes?: boolean;
  /** Where it stands in its work (docs/assistant/agent-work.md): set by the hub once it triaged a stopped turn. */
  work?: WorkState;
  pending_interactions?: number;
  queued?: string[];
  /** In a Persona chat, the last this many of `queued` came from other conversations: the assistant delivers them when
   *  the turn ends, and the strip's actions leave them alone (#4342). */
  queued_relayed?: number;
  /** In a Persona chat, the owner's messages that could not go in and that the assistant keeps and sends again by
   *  itself, oldest first (#10194, lib/undelivered.ts); in an Agent's chat, the ones held while its computer was away
   *  (#10661, `computer` set on each). Drawn as the owner's bubbles marked not delivered. */
  undelivered?: UndeliveredMessage[];
  subagents?: Subagent[];
  loop?: LoopInfo;
  /** The goal it works toward until it holds (Claude Code's /goal, a Codex thread goal; docs/integrations/agent-goal.md). */
  goal?: GoalInfo;
  /** Its own task list (Claude Code's TodoWrite / TaskCreate): how far along it is (docs/integrations/agent-goal.md "Plan"). */
  plan?: PlanInfo;
  background?: BackgroundTask[];
  /** Recent shell / Monitor outcomes from the last day; absent on older hubs and omitted from list summaries. */
  background_ended?: BackgroundTask[];
  /** Bounded list summaries omit task arrays; these scalars are authoritative when present. */
  waiting?: boolean;
  background_count?: number;
  running_subagents?: number;
  running_workflows?: number;
  /** Independent shell/Monitor count; unlike background_count, excludes workflows and subagents. */
  background_tasks_count?: number;
  /** Newest running workflow, compact metadata only (no agents, phases or tools). */
  workflow_progress?: Workflow;
  parent_session_id?: string;
  /** Parent session's machine ID when a subagent runs on another computer (empty = same machine). */
  parent_machine_id?: string;
  root_session_id?: string;
  is_subagent?: boolean;
  /** The listed session this child folds under (an Agent another Agent started: a Codex subagent, an Agent a Paseo Agent
   *  created, or an A2A subagent), set by the hub's manager (docs/protocol/protocol.md "Session hierarchy", #5120): the
   *  web never lists or counts it on its own, and draws it under that session. */
  folded_under?: string;
  /** Machine ID of `folded_under` when folded across computers (empty = same machine). */
  folded_under_machine_id?: string;
  /** A2A task ID that launched this subagent, if any. */
  a2a_task_id?: string;
  /** How this subagent session was created (`"a2a" | "dispatch" | "spawned" | "runtime"`). */
  subagent_origin?: string;
  /** The session on the same machine this one is drawn under while staying a session of its own (#5935): a Claude
   *  desktop app's side chat, /btw question or spawned session names the one it was started from. Unlike `folded_under`
   *  it folds nothing away: the row keeps its state and counts and only moves under its parent, a level in
   *  (lib/side-sessions.ts). Set by the node while both are listed; older hubs leave it out. */
  parent_agent_id?: string;
  /** How it folds under `folded_under`: `"worker"` for an Agent of a host tab whose lead (its first pane) is
   *  `folded_under` (#5180, a Claude Code agent team's teammate): it is in the lead's Team panel, not a card in its chat.
   *  `"side"` for a side session (#7944, `parent_agent_id`, with `sessions_side_fold`): it is in its topmost parent's
   *  Side sessions panel (lib/side-fold.ts). */
  folded_as?: "worker" | "side";
  /** The side session a folded row belongs to when it folds through one and is not itself the side (#7944: a side's own
   *  Codex children or workers): its card and child card are in that side's chat, not `folded_under`'s. */
  folded_side?: string;
  /** On a session other sessions fold under: how many, and how many of them work or need the user (by their own state). */
  children?: SessionChildren;
  /** When the session began, where the node knows it (child sessions at least). */
  started_at?: string;
  /** The conversation's earlier stretches, oldest first, when it moved between Claude Code and Codex (feature
   *  `engine_switch`, #5054): only the session running it now has them, and the sessions they name are not listed on
   *  their own (one conversation, one row); their transcripts stay readable (docs/protocol/protocol.md "Switching a
   *  running Agent to another engine"). A session can be named more than once, each time for another stretch. */
  engine_history?: EngineLeg[];
  /** Claude Code workflow runs it launched in the last day, newest first (#2935); older hubs leave it out. */
  workflows?: Workflow[];
  /** Working only because of background work: its turn is over, workflows / background commands / subagents run on
   *  (#2935). Older hubs leave it out. */
  background_working?: boolean;
  /** Working only because of background commands that only wait (sleep, a check loop, gh … --watch; #6045): shown
   *  as "Waiting in the background", lighter, and not counted as Working. An older hub never sends it. */
  background_waiting?: boolean;
  /** Working on background commands that have used no CPU and written no output since this time (#13744; a known
   *  worker, set once that has lasted ten minutes, omitted otherwise). The state is still Working: a hint, not a state. */
  background_stalled_since?: string;
  rev?: number;
  suggestion?: string;
  /** The suggestion names a command, a path, a link, a secret-looking string or a risky action (#4644): it is filled
   *  in, never sent with one tap. An older hub never sends it. */
  suggestion_risky?: boolean;
  /** An assistant conversation moved out of the list (docs/assistant/assistant.md "Managing its own conversations"), or
   *  an archived agent (docs/protocol/protocol.md "Agent session archives": only the Archived list has those). */
  archived?: boolean;
  /** Manager-owned visibility policy; recording and direct session access remain available. */
  hidden_by_directory?: boolean;
  /** The user-defined labels of an assistant conversation (docs/assistant/assistant.md "Conversation tags"), newest
   *  write first's provenance aside; omitted when it carries none. An older hub never sends it. */
  tags?: string[];
  /** The HTTP ports its processes listen on (docs/operations/ports.md): closing it would stop those servers, and they are
   *  the dev servers a preview offers (docs/web/preview.md). */
  ports?: Port[];
  /** Who opened an assistant conversation (docs/protocol/protocol.md "Origin of assistant chats"): user, channel, inbox, task, spawned,
   *  lark_agent, extension, or one this page doesn't know; none is the user's (older hubs never say). */
  origin?: string;
  /** What it was opened for: the inbox item, the task, the conversation, the channel, the extension. */
  origin_ref?: string;
  /** The extension whose token opened it, by id (docs/protocol/protocol.md "Origin of assistant chats"): noted when it
   *  opened, never backfilled; absent for any other and on older hubs. */
  opened_by?: string;
  /** The channels messages came into it from, newest first (docs/extensions/channels.md "Source markers"). */
  channels?: string[];
  todo?: { id: string; title: string };
  /** Display-only completed association; never an active task or a reason to suppress notifications. */
  completed_todo?: { id: string; title: string; cancelled?: boolean };
  /** Works (or worked) for a quiet task (#1731): not in the global badge; the main conversation tells about it. */
  quiet?: boolean;
  /** Why it waits for the user, in its host's words (a botmux attention's reason); only while it needs you (docs/integrations/engines.md "botmux"). */
  attention_reason?: string;
  /** The host stopped its turn on an error it will not retry by itself (a botmux notice), until a message goes to it:
   *  shown over the composer with "Retry" (iOS FailureNote). */
  failure_reason?: string;
  /** Where it lives outside Persona (a botmux session's Feishu topic): "Open in Lark". */
  external_url?: string;
  /** An assistant conversation's runtime as it runs ("agentsdk"; absent: the CLI, #2455). */
  runtime?: string;
  /** Why an assistant conversation asked to run on the Agent SDK runs on the CLI instead. */
  runtime_fallback?: string;
  /** The last compaction the user asked for in an assistant conversation (POST …/compact, #1727). */
  compaction?: Compaction;
}

/** Structured context window occupancy split with exact/estimated honesty marker (proto.ContextBreakdown, #12542).
 *  Omitted categories are unavailable; explicit 0 means genuinely zero. */
export interface ContextBreakdown {
  source: "exact" | "estimated";
  system_and_rules?: number;
  tools_and_mcp?: number;
  conversation?: number;
  tool_outputs?: number;
  free?: number;
  auto_compact_threshold?: number;
}

/** A compaction the user asked for (proto.Compaction): running, done or failed (`error` says why). */
export interface Compaction {
  state: "running" | "done" | "failed";
  error?: string;
  started_at: string;
  finished_at?: string;
}

/** Where the context was compacted (proto.Compact): "manual" (/compact) or "auto", the size before it. `notes`: the
 *  notices of the compaction folded into it here (lib/compaction.ts), never from the hub. */
export interface CompactInfo {
  trigger?: string;
  pre_tokens?: number;
  notes?: string[];
}

/** A goal the agent keeps working toward until it holds (proto.GoalInfo, docs/integrations/agent-goal.md). */
export interface GoalInfo {
  objective: string;
  /** active / paused / blocked / usage_limited / budget_limited / complete (Claude Code's are only active or complete). */
  status: string;
  /** Claude Code's latest check of the condition: why it holds, or what is still missing. */
  reason?: string;
  tokens_used?: number;
  token_budget?: number;
  seconds?: number;
  updated_at?: string;
}

/** An agent's own task list, counted (proto.PlanInfo): done of total items, and the one in progress. */
export interface PlanInfo {
  done: number;
  total: number;
  /** The item in progress (its activeForm), one line; absent when none is. */
  current?: string;
  at?: string;
}

/** A /loop: Claude re-runs a prompt on its own, scheduling each wakeup ("wakeup") or by cron ("cron"). */
export interface LoopInfo {
  kind: string;
  prompt?: string;
  reason?: string;
  interval_seconds?: number;
  next_at?: string;
  cron?: string;
  ticks?: number;
}

/** A command or subagent running in the background until it reports back. */
export interface BackgroundTask {
  id: string;
  kind: string;
  description?: string;
  command?: string;
  output_file?: string;
  started_at?: string;
  /** A retained outcome: completed / failed / killed / stopped; absent on older hubs and while running. */
  status?: string;
  ended_at?: string;
  exit_code?: number;
  /** A Monitor: when it stops by itself. */
  expires_at?: string;
  /** A workflow: its run in Session.workflows. */
  run_id?: string;
}

/** A Claude Code workflow run (the Workflow tool, proto.Workflow; docs/protocol/protocol.md "workflows"). */
export interface Workflow {
  task_id: string;
  run_id?: string;
  name?: string;
  summary?: string;
  /** running | completed | failed | killed | stalled; anything else reads as completed. */
  status: string;
  running: boolean;
  phases?: string[];
  phase?: string;
  /** Phase's 1-based place in phases; 0 or absent: not known. */
  phase_index?: number;
  phase_count?: number;
  /** Per-phase counts over all its agents (not only the 60 listed), in `phases` order then undeclared ones; title ""
   *  is agents whose phase is not known. Older hubs leave it out. */
  phase_stats?: WorkflowPhaseStats[];
  /** The Workflow call that launched it: its card goes in the chat in that call's place (#3434). */
  tool_use_id?: string;
  agents_started?: number;
  agents_done?: number;
  agents_running?: number;
  /** Agents that failed (counted apart from done); hubs before #5511 count them as done and leave this out. */
  agents_failed?: number;
  /** Sums over all its agents: each agent's latest usage (not cumulative spend), and its tool calls. */
  tokens?: number;
  calls?: number;
  started_at?: string;
  ended_at?: string;
  updated_at?: string;
  error?: string;
  agents?: WorkflowAgent[];
}

/** One phase's counts (proto.WorkflowPhaseStats); `failed` only from hubs with #5511. */
export interface WorkflowPhaseStats {
  title: string;
  started: number;
  done: number;
  running: number;
  failed?: number;
}

/** One agent of a workflow run; its conversation reads like a subagent's (session_id "<parent>@<id>"). */
export interface WorkflowAgent {
  id: string;
  session_id: string;
  label?: string;
  phase?: string;
  running: boolean;
  /** running | done | stopped (it had not come back when the run ended) | failed (#5511); older hubs leave it out. */
  status?: string;
  started_at?: string;
  ended_at?: string;
  updated_at?: string;
  last_text?: string;
  model?: string;
  /** Its latest tool call, kept after its last words. */
  latest_tool?: { name: string; summary?: string };
  tokens?: number;
  calls?: number;
}

/** What a background task's notice says (proto.TaskNote). */
export interface TaskNote {
  kind: string;
  status: string;
  title?: string;
  task_id?: string;
  tool_use_id?: string;
  exit_code?: number;
  output_file?: string;
  detail?: string;
}

/** A picture folded into a run of work (proto.Fold.images): the turn-image strip shows it without opening the fold. */
export interface FoldImage {
  /** The image item: its bytes from /sessions/{mid}/{sid}/items/{item_id}/image. Missing: only a path (file/thumb). */
  item_id?: string;
  tool_use_id: string;
  tool: string;
  source: "read" | "screenshot" | "browser" | "mcp" | "tool";
  path?: string;
  machine?: string;
  mime?: string;
  size?: number;
  width?: number;
  height?: number;
}

/** A file a folded run's successful edits changed (proto.FoldChange): its lines added and removed as the chat counts an
 *  edit's diff; `exact` false when an edit's text was clipped for good. A fold from a node that counts them always has
 *  the list (empty when nothing was edited); one without it leaves the run's edits unknown. */
export interface FoldChange {
  path: string;
  add: number;
  del: number;
  exact: boolean;
}

/** A web page a folded run used (docs/protocol/protocol.md "Folded work", #9132): one it opened or a search result. */
export interface FoldSource {
  url: string;
  title?: string;
}

/** An out-of-band declarative view attached by a tool call in a folded run (#14730); never sent to model runtimes. */
export interface FoldToolView {
  item_id?: string;
  tool_use_id?: string;
  tool?: string;
  view: Record<string, unknown>;
}

/** What model requests used (proto.TokenUsage): tokens by kind, `in` without the cache; `cache_write_1h` is the part of
 *  `cache_write` written for an hour; `fast`: Claude Code's fast speed. Entries with the same `id` (and model) are one
 *  report, counted once. */
export interface TokenUsage {
  id?: string;
  model?: string;
  in?: number;
  out?: number;
  cache_read?: number;
  cache_write?: number;
  cache_write_1h?: number;
  fast?: boolean;
}

/** proto.Botmux: a BotMux-wrapped item's own words and the facts shown as tags. */
export interface Botmux {
  kind: "message" | "trigger" | "reply";
  text: string;
  sender?: string;
  sender_type?: string;
  quoted?: boolean;
  topic_context?: boolean;
  thread?: boolean;
  attachments?: number;
  attachment_meta?: { kind: string; name?: string; mime_type?: string }[];
  mentions?: string[];
  source?: string;
  final?: boolean;
  voice?: boolean;
  truncated?: boolean;
}

export interface Item {
  id: string;
  seq: number;
  ts?: string;
  role: "user" | "assistant" | "system";
  kind: "text" | "thinking" | "tool_use" | "tool_result" | "notice" | "image" | "fold" | "recap" | "visualization";
  text?: string;
  tool_use_id?: string;
  tool_name?: string;
  tool_input?: Record<string, unknown>;
  is_error?: boolean;
  /** The runtime's own outcome, when the node recorded one (docs/protocol/protocol.md "Tool outcomes"). */
  outcome?: { status?: string; exit_code?: number };
  full_size?: number;
  /** Opaque identity of the source record when an item ID names only a position. */
  source_fingerprint?: string;
  queued?: boolean;
  wake?: boolean;
  narration?: boolean;
  codex_heartbeat_quiet?: boolean;
  task?: TaskNote;
  fold?: { items: number; tools: number; thoughts: number; until?: string; through_seq: number; through_id?: string; images?: FoldImage[]; tasks?: FoldTaskSnapshot[]; latest_thinking?: Item; supervision_user?: boolean; changes?: FoldChange[] | null; usage?: TokenUsage[]; sources?: FoldSource[]; botmux?: Botmux[]; tool_views?: FoldToolView[] };
  /** Out-of-band declarative UI views returned by a tool call (#14730); never sent to model runtimes. */
  tool_views?: unknown[];
  image?: { mime: string; data?: string; lazy?: boolean; size?: number; width?: number; height?: number };
  /** A Codex visualize marker (kind "visualization"): a self-contained HTML fragment on the node, fetched through the
   *  session visualization route. `mode` is "wide" or absent; the bytes are never carried here. */
  visualization?: { path: string; mode?: string; title?: string };
  /** Sent by another Claude session (cross-session messaging), not typed by you: its name. */
  from?: string;
  /** What a BotMux-wrapped message or `botmux send` call says without BotMux's blocks (docs/protocol/protocol.md
   *  "BotMux messages"); `text` stays the original. Absent from older nodes. */
  botmux?: Botmux;
  /** The client_msg_id of the message this came from, as the hub tied it (every item of it: the text, its images, a
   *  queued copy, or the notice it became); older hubs never send it (lib/echo.ts). */
  client_msg_id?: string;
  /** This notice is where the context was compacted (Claude Code's compact_boundary). */
  compact?: CompactInfo;
  /** A notice for a Claude Code model_refusal_* record (#2289), kept as recorded. */
  model_refusal?: ModelRefusal;
  /** The model that wrote this assistant item, as its transcript records it (proto.Item.Model, #5034); absent when the
   *  transcript does not say and from older nodes. Drawn only as a divider where the replies' model changes. */
  model?: string;
  /** The tokens of the model requests this item stands for, one entry per model and speed (proto.Item.Usage, #5426):
   *  a Claude Code request's on its first item, a Codex turn's total on its turn_duration item, a finished subagent's on
   *  its result or notice. Absent from older nodes. lib/turn-usage.ts sums an exchange's into its usage. */
  usage?: TokenUsage[];
  /** Observed time-to-first-token in milliseconds during live streaming execution (proto.Item.TTFTMS, #12542).
   *  Absent when not observed (e.g. historical replays); 0 is a valid observed value. */
  ttft_ms?: number;
  /** A message Persona typed in for the model (a task's notice, a scheduled run…), not by you (lib/injected.ts). */
  injected?: Injected;
  /** Display provenance, independent of the model-facing role. Unknown kinds remain non-user sources. */
  source?: MessageSource;
  /** Client-only promotion evidence transferred from a removed Fold.supervision_user during an overlapping merge. */
  supervisionUser?: boolean;
}

/** A Claude Code model_refusal_* record (server proto.ModelRefusal) as the hub gives it: its known fields, values as
 *  recorded (unknown ones included), and `text`, the refusal's own words. Never the record's line (#2528). */
export interface ModelRefusal {
  subtype: string;
  original_model?: string;
  fallback_model?: string;
  category?: string;
  scope?: string;
  explanation?: string;
  text?: string;
  // The node's own bounded recovery projects these onto the session's fallback shape (#7824). A runtime-written
  // record never sets them, so an older client that does not know them keeps its old behavior.
  /** When the configured model is available again (RFC3339); absent when the upstream gave no time. */
  recovery_reset?: string;
  /** Every model the recovery chain could try failed; fallback_model is empty in that case. */
  recovery_exhausted?: boolean;
  /** The configured model answered a probe; the next request runs on it again. */
  recovery_returning?: boolean;
  /** Every model tried, in order, joined by the arrow sequence the node writes. */
  recovery_tried?: string;
}

/** A pending "back to the configured model" (server proto.ModelRestore): "next_start" — the idle headless process was
 *  closed (or none ran), so the next start asks for `to`. */
export interface ModelRestore {
  state: "next_start" | (string & {});
  to?: string;
  at: string;
}

/** Message provenance supplied by the assistant, not a group-chat member or a model role. */
export interface MessageSource {
  kind: "conversation" | "patrol" | "followup" | "supervision" | (string & {});
  conversation_id?: string;
  title?: string;
}

/** What a message Persona wrote for the model is (docs/protocol/protocol.md "Messages injected by Persona"). */
export interface Injected {
  kind: "task_notice" | "followup" | "system" | (string & {});
  /** task_notice: done | needs | interrupted | failed; followup: once | daily | interval | wake; system: lost_tasks | supervise */
  state?: string;
  /** Supervision without a level is legacy triage; other injections remain informational. */
  level?: "info" | "triage" | "user";
  title?: string;
  machine?: string;
  todo_id?: string;
  /** The one line to show. */
  line: string;
  /** followup once | daily | interval: what the task asks. */
  detail?: string;
}

export interface ItemsPage {
  items: Item[];
  has_more?: boolean;
  more_after?: boolean;
  more?: boolean;
  history?: string;
  to_seq?: number;
  from_seq?: number;
}

export interface Question {
  question: string;
  header?: string;
  recommend?: string;
  multi_select?: boolean;
  /** Missing or null for a question asked in free text (Codex request_user_input; a Go nil slice, #1573). */
  options?: { label: string; description?: string; action?: "defer" | (string & {}) }[] | null;
}

export interface DialogOption {
  key: string;
  label: string;
  detail?: string;
  kind?: string;
  /** Ticked at the computer (a multi-select box), or an answered question's pick. */
  checked?: boolean;
  /** What is typed into the free-text field (kind "type_something"). */
  text?: string;
}

/** A terminal dialog as the hub reads it. A multi-select question (multi) is sent whole by its submit row ("Submit",
 *  or "Next" when another question follows); steps are the questions of its tab bar, review the tab that submits them. */
export interface ScreenDialog {
  header?: string;
  question?: string;
  detail?: string;
  options?: DialogOption[];
  rev?: string;
  multi?: boolean;
  submit?: string;
  steps?: { label: string; done: boolean }[];
  review?: boolean;
  incomplete?: boolean;
  /** A shell command's permission prompt (#5266): the command, the agent's words for it, the boxed warning as Claude
   *  Code words it and what it is about (risk "delete"), the countdown sentence, what it does when it runs out
   *  (deadline_action "deny" / "allow") and when (deadline_at, RFC 3339, the hub's clock). A hub before #5265 sends none. */
  command?: string;
  description?: string;
  warning?: string;
  risk?: string;
  deadline?: string;
  deadline_action?: string;
  deadline_at?: string;
}

/** What an MCP server asks for: a form (`requested_schema`, a JSON Schema subset) or a link to open (`url`). */
export interface Elicitation {
  mode: "form" | "url" | (string & {});
  message?: string;
  requested_schema?: Record<string, unknown>;
  url?: string;
  elicitation_id?: string;
}

/** Session.children: the sessions folded under one, counted by their own state (docs/protocol/protocol.md "Session hierarchy"). */
export interface SessionChildren {
  total: number;
  working?: number;
  needs_you?: number;
  /** How many of them are its tab's workers (`folded_as: "worker"`, #5180): a lead with workers has a Team panel. */
  workers?: number;
  /** Of them, its side sessions (`folded_as: "side"`, #7944), and how many of those work, need the user or are unread,
   *  by their own state; absent when 0. Counted in the keys above too. */
  sides?: number;
  sides_working?: number;
  sides_needs_you?: number;
  sides_unread?: number;
}

export const SESSION_REVIEW_FEATURE = "session_review";
/** The hub feature that says it has git/review (lib/session-review.ts). Here, not there, so the chat's check for it
 *  keeps session-review.ts off the first screen. */
export const REVIEW_FEATURE = "git_review";
export const SESSION_REVIEW_TOOL = "persona_session_review";

export function isSessionReview(ix: Pick<Interaction, "kind" | "tool_name">): boolean {
  return ix.kind === "question" && ix.tool_name === SESSION_REVIEW_TOOL;
}

export interface Interaction {
  id: string;
  /** Authoritative interaction for a display-only copy; aggregates count only cards without this field. */
  mirror_of?: string;
  /** The manager it belongs to (docs/protocol/protocol.md "Per-manager routing"); none: the default one. */
  manager?: string;
  machine_id: string;
  session_id: string;
  session_title?: string;
  cwd?: string;
  kind: "question" | "plan" | "permission" | "screen" | "elicitation";
  created_at?: string;
  expires_at?: string;
  seen_at?: string;
  seen_ttl_seconds?: number;
  tool_name?: string;
  /** The transcript call that raised this card, when its runtime supplies one. */
  tool_use_id?: string;
  questions?: Question[];
  /** The hub accepts a bare cancellation for this question. */
  can_cancel?: boolean;
  plan?: string;
  /** The permission mode a plan-mode session goes on in once this plan is approved (#3784). */
  plan_then?: string;
  tool_input?: Record<string, unknown>;
  /** A trusted connector gate marked this as an outward write on behalf of the owner. */
  outward_write?: boolean;
  /** The outgoing text the server lets the user edit before Allow, only for a Claude Code Lark send/reply card. */
  editable_text?: string;
  /** Script asks opt into Markdown for their summary or question; absent and unknown values stay plain (#10857). */
  format?: "markdown" | (string & {});
  summary?: string;
  /** A finished independent session review's finding count, full report, and hidden reviewer (#1808). */
  review_count?: number;
  review_report?: string;
  reviewer_session_id?: string;
  screen?: string;
  secret?: boolean;
  dialog?: ScreenDialog;
  /** Who asks when it is not Claude Code: "codex", "traex", or an agent runtime's agent ("dsh"; docs/integrations/runtimes.md). */
  agent?: string;
  /** Display label supplied by the source (for example a script request). */
  agent_label?: string;
  /** A manager-owned exact operation or batch. Its reply control supplies the interaction id explicitly; ordinary
   *  quote replies never acquire approval authority. The phrase list is a display snapshot, not an authorization. */
  approval_batch_id?: string;
  approval_item_id?: string;
  owner_proposal?: boolean;
  no_expiry?: boolean;
  owner_approval_phrases?: string[];
  /** What "always allow" allows here; another agent's card offers it only with this. */
  always_label?: string;
  /** Permission scopes remembered by the hub for this conversation. */
  always_keys?: string[];
  /** Whether answering "Always allow" is kept, as the manager says (#3590): false hides the choice, true offers it;
   *  absent (an older hub, an Agents session's card) the card's own rule (offersAlways). */
  always_honored?: boolean;
  /** The tool by its name for people, from the manager: its own tools ("iPhone control" for iphone_do) and Persona's
   *  MCP tools on a node's card ("Report to supervisor · Done", #4966); absent on other cards. */
  tool_title?: string;
  /** The tool alone where tool_title also says what this call does ("Report to supervisor"): what Always covers. */
  tool_name_title?: string;
  /** Why another agent asks for the permission, in its own words (a Codex and compatible variants approval read off the terminal). */
  reason?: string;
  /** An MCP form or link (kind elicitation, docs/protocol/protocol.md "MCP elicitation"): the server, where it is configured, and
   *  the subagent that asks, when the hook says. */
  mcp_server_name?: string;
  mcp_config_source?: string;
  agent_id?: string;
  agent_type?: string;
  /** The session the card's session folds under (Session.folded_under, #5120): the card's home is that session's page. */
  folded_under?: string;
  /** Machine ID of `folded_under` when the card's session folds under a parent on another computer (empty = same machine). */
  folded_under_machine_id?: string;
  /** Session.folded_as of the card's session (#5180): `"worker"` when a lead's worker asks, `"side"` when a side
   *  session does (#7944). */
  folded_as?: "worker" | "side";
  /** Session.folded_side of the card's session (#7944): the side session whose chat the card is answered in. */
  folded_side?: string;
  elicitation?: Elicitation;
  /** Whether this assistant-owned operation can take a non-final owner question. */
  can_clarify?: boolean;
  /** True when the card has no interactive computer terminal or UI to hand back to (headless assistant, command gates,
   *  manager-owned cards, headless ACP companions like AGY, Grok, and builtin OpenCode); absent/false keeps "Leave to computer". */
  no_hand_back?: boolean;
  /** What a permission card needs filled in before it is approved (a password): such a card is never approved in one tap. */
  fields?: { key: string; label: string; secret?: boolean }[];
  /** The assistant is deciding whether it may answer this question for the user (docs/assistant/assistant.md "Assistant
   *  answers questions first"): nothing was pushed yet, and an answer given here wins. The same card comes again without it. */
  delegating?: boolean;
  /** Hub push-grading urgency ("P0" | "P1" | "P2"): absent defaults to P0. */
  push_level?: "P0" | "P1" | "P2" | (string & {});
  /** What the assistant filled in when it left the question to the user. */
  delegate?: Delegate;
  /** A rule the hub suggests on a question a supervisor passed up, once the owner gave the same answer three times
   *  (#4929, lib/rule-suggestion.ts); the card comes again without it once it was answered. Absent otherwise. */
  suggestion?: RuleSuggestion;
}

/** The assistant's context on a question it left to the user (Interaction.delegate). */
export interface Delegate {
  what?: string;
  why_now?: string;
  /** The answer it recommends: an option's label when the question has options. */
  recommend?: string;
  /** What not taking the recommendation costs. */
  cost?: string;
  /** Why the user has to decide. */
  reason?: string;
  deadline?: string;
}

export interface Launcher {
  name: string;
  /** A verified separate runtime channel, independent of ordinary Assistant support. */
  supervision_policy?: boolean;
  assistant?: boolean;
  /** A Persona-managed provider's name for people, any language (#5029), or a runtime's name for a provider it runs
   *  (Paseo's label, "Grok", #8261); absent for an rc alias or from an older hub. Show it through lib/provider-names.ts. */
  display_name?: string;
  model?: string;
  /** What it starts when not Claude Code ("codex"); agent_type: what the hub's machine config says it runs. */
  agent?: string;
  agent_type?: string;
  /** A Codex launcher the computer starts with no first message too; absent from an older computer, whose Codex
   *  starts need one. */
  prompt_optional?: boolean;
  /** The permission modes a start on a runtime's provider of another agent takes (#8261, Paseo's): its own approval
   *  (`default`, sent as no mode) and, where it has one, Full access. Such a launcher's start also takes only the efforts
   *  its model lists (lib/launcher-models.ts `effortRule`). Absent: Claude Code's five, or none. */
  permission_modes?: string[];
  /** A Codex launcher the computer starts in a shell pane the user picked (Run agent here), keeping that pane when the
   *  start fails (#5504); absent from an older computer, where Run agent here offers Claude Code only. */
  run_here?: boolean;
  /** Where it starts the session when not in a terminal: "claude_desktop" (the launcher `claude-desktop`, Claude's desktop
   *  app, #5480) or "codex_app" (`codex-app`, the Codex App, #5726); lib/desktop-start.ts. Absent for every other one. */
  source?: string;
  /** How its last check or start went on the computer the list is for (GET /machines/{mid}/launchers,
   *  docs/server/launcher-health.md); absent: never checked, a failure too old to count, or an older hub. */
  health?: LauncherHealth;
  /** A Persona-managed provider pi on this computer can use too (docs/integrations/pi.md "Persona's model providers");
   *  absent for every other launcher and from an older hub. */
  pi?: boolean;
}

/** A launcher's health record on one computer (#1643). */
export interface LauncherHealth {
  /** ok | failing | repairing | blocked */
  state: string;
  /** The failure's class: not_in_terminal, auth, model_route, cli_outdated, timeout, other. */
  kind?: string;
  /** The redacted raw text of the failure. */
  error?: string;
  checked_at?: string;
}

export type TodosV2AutoSeen = "off" | "subtasks" | "all_autonomous";
export type TodosV2PendingReviewSLAHours = 0 | 12 | 24 | 48;
export type TodosV2CanWaitSLAHours = 0 | 24 | 48 | 72;
export type TodosV2DigestWindowMinutes = 0 | 30 | 60 | 120;

/** Additive user-configurable attention preferences under todos_v2 (#16098, #16111, #16433). */
export interface TodosV2AttentionPrefs {
  todos_v2_auto_seen?: TodosV2AutoSeen;
  todos_v2_pending_review_sla_hours?: TodosV2PendingReviewSLAHours;
  todos_v2_can_wait_sla_hours?: TodosV2CanWaitSLAHours;
  todos_v2_fold_children?: boolean;
  todos_v2_quiet_hours?: string;
  todos_v2_digest_window_minutes?: TodosV2DigestWindowMinutes;
}

export interface AssistantInfo {
  /** The manager that answered, including the default whose request carried no manager header. */
  manager?: string;
  machine_id: string;
  main_machine_id?: string;
  launcher?: string;
  cwd?: string;
  main?: string;
  timezone?: string;
  notes?: string;
  notes_effective?: string;
  /** The current directions' opaque ids, in hub order: a conversation-backed id indexes `conversations`; an external
   *  one has no Persona conversation and gets its target from the direction tree or `/assistant/leaders` (#8236). */
  leaders?: string[];
  /** "Fold background rounds" in effect (#4314, docs/assistant/assistant.md "Background rounds"); absent from an older hub, which means on. */
  main_background_folded?: boolean;
  /** Opt-in to fold routine relay replies and skip their finished notification in the main conversation. */
  fold_routine_relay_replies?: boolean;
  /** "Show scheduled task chats" in the sidebar's Recent (#8324, lib/recent-task-chats.ts): one value on the hub for all
   *  of the user's devices, off by default; absent from an older hub (no switch, and Recent leaves them out). */
  recent_task_chats?: boolean;
  /** Exact, one-shot owner replies and persistent proposal cards; off by default and absent from an older hub. */
  owner_reply_approval?: boolean;
  persistent_owner_action_cards?: boolean;
  owner_reply_approval_phrases?: string[];
  /** User-chosen ordinary conversation order (#12017), shared by every client; absent or empty means activity time. */
  conversation_order?: string[];
  /** Exact user-chosen tag filter entries (#12018); null or absent means the automatic all-in-use-tag row. */
  conversation_tag_filters?: string[] | null;
  /** The background supervisor's conversation (#4927, lib/background-supervisor.ts): its session id once the hub made
   *  it; absent before that and from an older hub. Read-only. */
  background_supervisor?: string;
  /** Unified experimental To-dos & Tasks Labs toggle (#14538, #14584); off by default and absent from older hubs. */
  todos_v2?: boolean;
  /** Auto-clear unseen badges for autonomously completed to-dos under todos_v2 (#16098, #16111); defaults to "all_autonomous". */
  todos_v2_auto_seen?: TodosV2AutoSeen;
  /** Quiet window in hours before pending_review escalates to needs_you under todos_v2 (#16098, #16111); defaults to 24. */
  todos_v2_pending_review_sla_hours?: TodosV2PendingReviewSLAHours;
  /** Deferral window in hours before non-urgent can_wait decisions escalate under todos_v2 (#16098, #16111); defaults to 48. */
  todos_v2_can_wait_sla_hours?: TodosV2CanWaitSLAHours;
  /** Fold non-urgent child tasks under top-level goals in board projection under todos_v2 (#16098, #16111); defaults to true. */
  todos_v2_fold_children?: boolean;
  /** Daily quiet-hours window ("HH:MM-HH:MM" 24h range with distinct start/end, or "" for off) under todos_v2 (#16433). */
  todos_v2_quiet_hours?: string;
  /** Digest batching window in minutes (0, 30, 60, or 120) for non-urgent notifications under todos_v2 (#16433). */
  todos_v2_digest_window_minutes?: TodosV2DigestWindowMinutes;
  /** Unified experimental Integrations & Extensions Labs toggle (#14802, #14805); off by default and absent from older hubs. */
  integrations_v2?: boolean;
  work_guide?: boolean;
  /** Additive receipts for Labs experiments. Older managers omit the field. */
  labs_seen?: string[];
  conversations: Session[];
}

export interface MetricPoint {
  t: number;
  cpu?: number;
  load?: number;
  mem?: number;
  /** Network history, bytes per second. */
  rx?: number;
  tx?: number;
  gap?: boolean;
}

export interface MachineMetrics {
  machine_id: string;
  online: boolean;
  system?: Machine["system"];
  latest?: { t: number; cpu?: number; load?: number; mem_used?: number; mem_total?: number; net_rx?: number; net_tx?: number; disk_used?: number; disk_total?: number; battery_percent?: number; battery_charging?: boolean; temp_c?: number };
  points: MetricPoint[];
  /** Its tokens (docs/server/machines.md "Token usage"): absent from an older hub, or when its node never reported usage. */
  usage?: UsageCard;
}

/** What a working Claude's status line says right now (hub `live` push, feature `session_live`; docs/protocol/protocol.md
 *  "Live status"): read off its pane about once a second while someone watches. Absent fields: the line did not say. */
export interface SessionLive {
  /** The spinner's verb without the ellipsis ("Frosting"). */
  verb?: string;
  /** Seconds this turn has run, as the line counts them. */
  elapsed_s?: number;
  /** Output tokens so far this turn (the line's "↓ 22.8k tokens"). */
  tokens_down?: number;
  /** It is thinking now ("thinking with … effort"). */
  thinking?: boolean;
  /** It just thought for this many seconds ("thought for 3s"). */
  thought_s?: number;
  /** The model request error Claude is retrying, without the countdown or attempt suffix. */
  retry_error?: string;
  /** Current model request attempt, as shown on screen. */
  retry_attempt?: number;
  /** Maximum number of attempts, as shown on screen. */
  retry_max?: number;
  /** When the node read it (RFC 3339). */
  at?: string;
}

/** The hub's `live` push: `live` null or absent means the turn is over (or it is no longer read). */
export interface LiveEvent {
  type: "live";
  machine_id: string;
  session_id: string;
  live?: SessionLive | null;
}

/** One of Session.undelivered: an owner's message kept after it could not go in (proto.UndeliveredMessage). */
export interface UndeliveredMessage {
  /** Its client_msg_id. */
  id: string;
  text: string;
  /** How many pictures it carries. */
  images?: number;
  at: string;
  /** waiting: sent again by itself; failed: it gave up (Retry / Delete). */
  state: "waiting" | "failed";
  attempts?: number;
  next_at?: string;
  error?: string;
  /** A failed message that can only be deleted. */
  delete_only?: boolean;
  /** Held for an Agent while its computer was away (#10661): that computer's name. `error` is then the whole line to
   *  show under the bubble, and Retry / Delete go to the session's held routes (lib/undelivered-actions.ts). */
  computer?: string;
  /** Set on an input of a logical conversation's delivery queue (docs/operations/logical-input-reconciliation.md
   *  "Delivery deadline"): why it has not gone in yet. `error` is still the server's line for it. */
  reason?: UndeliveredReason;
  /** When the current wait began (the dispatch, the queueing, or the expiry). */
  since?: string;
  /** awaiting_receipt: when it stops waiting for its receipt and expires. Nothing is sent twice. */
  deadline?: string;
  /** How many inputs wait before this one / after the one awaiting its receipt. */
  ahead?: number;
  behind?: number;
  /** The client_msg_id of the input a queued one waits behind. */
  blocked_by?: string;
}

/** Why a logical conversation's input has not gone in (proto.UndeliveredReason). receipt_expired is `failed`: it may
 *  have gone in, so the owner looks and picks Retry (sent once more, last in the queue) or Ignore; the rest wait. */
export type UndeliveredReason = "awaiting_receipt" | "behind_unconfirmed" | "switch_pending" | "queued" | "receipt_expired";
