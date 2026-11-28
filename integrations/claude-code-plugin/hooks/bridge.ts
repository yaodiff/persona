// Protocol 1 between this plugin and the local Persona node (README.md): where the node's socket is, what a poll's
// answer holds, which messages or turn interrupts may go in as the person's own, session-scoped model / effort
// overrides, and permission / question approval bridging. Pure functions; register.ts does the I/O.

/** The base of the node's endpoints; the host name is not used (the request goes over the socket). */
export const BASE = 'http://persona/claude-plugin/v1'

/** Optional protocol capabilities this plugin version serves on /poll. */
export const CAPABILITIES = ['busy_input', 'interrupt', 'settings', 'permissions'] as const

/** How long deliver waits before releasing the poll loop while the real submit result continues asynchronously. */
export const SUBMIT_QUEUE_SETTLE_MS = 250

/** How long the node may hold a poll or permission wait: under the 30 s after which Claude Code's fetch gives up. */
export const POLL_WAIT_MS = 25_000
export const PERMISSION_WAIT_MS = 25_000

/** Short grace window giving fast local command hooks (such as bypass-allow) a chance to decide before opening a card. */
export const PERMISSION_GRACE_MS = 500

/** Maximum total time one permission request keeps renewing before falling back to passthrough. */
export const PERMISSION_MAX_WAIT_MS = 24 * 60 * 60 * 1_000

/** Retry spacing on transient /permission errors; initial errors fail fast, while already-registered cards reconnect up to 2 minutes (matching nodeclient.ResumeWait). */
export const PERMISSION_RETRY_MS = 1_000
export const PERMISSION_MAX_ERRORS = 3
export const PERMISSION_RECONNECT_MS = 2 * 60 * 1_000

/** Maximum serialized permission request body sent to the node (matches node's 4 MiB /permission cap). */
export const PERMISSION_BODY_MOST = 4 * 1024 * 1024

/** Backoff while no node answers: from the first to the most, doubling. */
export const BACKOFF_FIRST_MS = 2_000
export const BACKOFF_MOST_MS = 60_000

/** The longest text submitted, in UTF-16 units; the node never sends more (its inbox line holds 1 MiB). */
export const TEXT_MOST = 200_000

const ID = /^[A-Za-z0-9_-]{1,128}$/
const MODEL = /^[A-Za-z0-9._:/[\]-]{1,200}$/
const PRE_TOOL_META = new Set([
  'tool',
  'tool_name',
  'tool_use_id',
  'tool_input',
  'hook_event_name',
  'session_id',
  'transcript_path',
  'cwd',
  'permission_mode',
  'prompt_id',
  'agent_id',
  'agent_type',
  'scratchpad_dir',
  'effort',
])

export type EffortChoice = 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'auto'

const EFFORTS: ReadonlySet<string> = new Set<EffortChoice>(['low', 'medium', 'high', 'xhigh', 'max', 'auto'])

export function validModel(model: unknown): model is string {
  return typeof model === 'string' && MODEL.test(model) && !model.startsWith('-')
}

export function isEffortChoice(effort: unknown): effort is EffortChoice {
  return typeof effort === 'string' && EFFORTS.has(effort)
}

/**
 * The node's socket, as the node resolves it: `$PERSONA_HOME/node.sock` when that is set (`~`, `~/…` and a relative
 * path are taken from the home directory), else `<home>/.persona/node.sock`. `undefined` without a home directory.
 */
export function socketPath(home: string | undefined, personaHome: string | undefined): string | undefined {
  if (!home) return undefined
  const sep = /^[A-Za-z]:[\\/]/.test(home) || home.includes('\\') ? '\\' : '/'
  const join = (a: string, b: string) => (a.endsWith('/') || a.endsWith('\\') ? a + b : a + sep + b)
  let root = join(home, '.persona')
  const p = personaHome?.trim()
  if (p) {
    if (p === '~') root = home
    else if (p.startsWith('~/') || p.startsWith('~\\')) root = join(home, p.slice(2))
    else if (p.startsWith('/') || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith('\\\\')) root = p
    else root = join(home, p)
  }
  return join(root, 'node.sock')
}

/** The home directory: `USERPROFILE` on Windows (what the node's Go reads there), else `HOME`. */
export function homeDir(userProfile: string | undefined, home: string | undefined): string | undefined {
  if (userProfile && /^[A-Za-z]:[\\/]/.test(userProfile)) return userProfile
  return home || undefined
}

/** One message the node handed over: its id, the text, and the time after which it must not be submitted. */
export type Message = { id: string; text: unknown; deadlineMs: number }

/** An owner Stop handed over by the node to cancel the running main-loop turn via $.turn.abort. */
export type InterruptRequest = { id: string; deadlineMs: number }

/** The running main-loop turn tracked from turn.start / turn.step until turn.complete. */
export type ActiveTurn = { session: string; turnId: string }

/**
 * Whether a main-loop turn's tool calls reach the `classic.PreToolUse` hook. Claude Code's managed-settings guard can
 * pass every `classic.*` event over a user-tier plugin; once that is seen, it stays true for the session.
 */
export type ClassicHookObservation = {
  toolUses: number
  preToolUses: number
  passedOver: boolean
}

export function createClassicHookObservation(): ClassicHookObservation {
  return { toolUses: 0, preToolUses: 0, passedOver: false }
}

/** Counts main-loop tools from a completed `turn.step`; subagent steps do not prove the main-loop hook path. */
export function noteTurnStepToolUses(
  observation: ClassicHookObservation,
  agentId: string | undefined,
  step: unknown,
): ClassicHookObservation {
  if (agentId !== undefined || typeof step !== 'object' || step === null) return observation
  const uses = (step as { toolUses?: unknown }).toolUses
  if (!Array.isArray(uses)) return observation
  return { ...observation, toolUses: observation.toolUses + uses.length }
}

/** Counts a `classic.PreToolUse` event that reached the plugin for the main agent. */
export function noteClassicPreToolUse(observation: ClassicHookObservation, raw: unknown): ClassicHookObservation {
  if (!isRecord(raw)) return observation
  if (typeof raw.agent_id === 'string' && raw.agent_id !== '') return observation
  return { ...observation, preToolUses: observation.preToolUses + 1 }
}

/**
 * Finishes one main-loop turn. A turn with tool calls and no `classic.PreToolUse` means Claude Code passed the classic
 * hook chain over this plugin, so later polls report it and the node stops authorizing permission bridging.
 */
export function finishClassicTurn(observation: ClassicHookObservation): ClassicHookObservation {
  return {
    toolUses: 0,
    preToolUses: 0,
    passedOver: observation.passedOver || (observation.toolUses > 0 && observation.preToolUses === 0),
  }
}

/** A session-only model / effort switch handed over by the node. */
export type SettingsUpdate = { id: string; model?: string; effort?: EffortChoice }

/** In-memory session-only model / effort override applied on main-loop `turn.step` calls. */
export type SessionOverride = {
  session: string
  model?: string
  baseModel?: string
  effort?: EffortChoice
}

/** What a poll came to. */
export type PollAnswer =
  | {
      kind: 'messages'
      messages: Message[]
      interrupt?: InterruptRequest
      settings?: SettingsUpdate
      permissions: boolean
      retryMs: number
    }
  | { kind: 'refused'; retryMs: number } // the node does not take this process for this session
  | { kind: 'unavailable' } // no node, an old node, or no answer

/** Decision object returned to Claude Code from `classic.PermissionRequest`. */
export type PermissionDecision =
  | { behavior: 'allow'; updatedInput?: Record<string, unknown>; updatedPermissions?: unknown[] }
  | { behavior: 'deny'; message: string; interrupt?: boolean }

/** Outcome of one `POST /claude-plugin/v1/permission` call. */
export type PermissionAnswer =
  | { action: 'renew' }
  | { action: 'decide'; decision: PermissionDecision }
  | { action: 'passthrough' }
  | { action: 'refused' }
  | { action: 'unavailable' }

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function readInterrupt(raw: unknown): InterruptRequest | undefined {
  if (!isRecord(raw)) return undefined
  const { id, deadline_ms: deadline } = raw
  if (typeof id !== 'string' || !ID.test(id) || typeof deadline !== 'number') return undefined
  return { id, deadlineMs: deadline }
}

function readSettings(raw: unknown): SettingsUpdate | undefined {
  if (!isRecord(raw)) return undefined
  const { id, model, effort } = raw
  if (typeof id !== 'string' || !ID.test(id)) return undefined
  if (model !== undefined && model !== '' && !validModel(model)) return undefined
  if (effort !== undefined && effort !== '' && !isEffortChoice(effort)) return undefined
  const m = typeof model === 'string' && model !== '' ? model : undefined
  const ef = isEffortChoice(effort) ? effort : undefined
  if (m === undefined && ef === undefined) return undefined
  return { id, ...(m !== undefined ? { model: m } : {}), ...(ef !== undefined ? { effort: ef } : {}) }
}

/** Reads a poll's answer: status and body as `$.http.fetch` resolved them. */
export function readPoll(status: number, body: string): PollAnswer {
  if (status === 403) return { kind: 'refused', retryMs: retryOf(parse(body), BACKOFF_MOST_MS) }
  if (status !== 200) return { kind: 'unavailable' }
  const answer = parse(body)
  if (answer === undefined) return { kind: 'unavailable' }
  const list = Array.isArray(answer.messages) ? answer.messages : []
  const messages: Message[] = []
  for (const m of list) {
    if (!isRecord(m)) continue
    const { id, text, deadline_ms: deadline } = m
    if (typeof id !== 'string' || !ID.test(id) || typeof deadline !== 'number') continue
    messages.push({ id, text, deadlineMs: deadline })
  }
  const interrupt = readInterrupt(answer.interrupt)
  const settings = readSettings(answer.settings)
  return {
    kind: 'messages',
    messages,
    ...(interrupt !== undefined ? { interrupt } : {}),
    ...(settings !== undefined ? { settings } : {}),
    permissions: answer.permissions === true,
    retryMs: retryOf(answer, 0),
  }
}

/** Parses and validates a `PermissionDecision` object. */
export function readPermissionDecision(raw: unknown): PermissionDecision | undefined {
  if (!isRecord(raw)) return undefined
  if (raw.behavior === 'allow') {
    const out: { behavior: 'allow'; updatedInput?: Record<string, unknown>; updatedPermissions?: unknown[] } = {
      behavior: 'allow',
    }
    if (isRecord(raw.updatedInput)) out.updatedInput = raw.updatedInput
    if (Array.isArray(raw.updatedPermissions)) out.updatedPermissions = raw.updatedPermissions
    return out
  }
  if (raw.behavior === 'deny' && typeof raw.message === 'string') {
    const out: { behavior: 'deny'; message: string; interrupt?: boolean } = {
      behavior: 'deny',
      message: raw.message,
    }
    if (typeof raw.interrupt === 'boolean') out.interrupt = raw.interrupt
    return out
  }
  return undefined
}

/** Reads a `POST /claude-plugin/v1/permission` answer. */
export function readPermissionAnswer(status: number, body: string): PermissionAnswer {
  if (status === 403) return { action: 'refused' }
  if (status === 404 || status === 400 || status === 413) return { action: 'passthrough' }
  if (status !== 200) return { action: 'unavailable' }
  const answer = parse(body)
  if (answer === undefined) return { action: 'unavailable' }
  if (answer.action === 'renew') return { action: 'renew' }
  if (answer.action === 'passthrough') return { action: 'passthrough' }
  if (answer.action === 'decide') {
    const decision = readPermissionDecision(answer.decision)
    return decision !== undefined ? { action: 'decide', decision } : { action: 'passthrough' }
  }
  return { action: 'passthrough' }
}

/** Reports whether a `classic.PermissionRequest` hook result carries an explicit allow/deny decision. */
export function hasPermissionDecision(value: unknown): boolean {
  return isRecord(value) && readPermissionDecision(value.decision) !== undefined
}

/** Deterministic JSON key with sorted object keys so flat PreToolUse and nested PermissionRequest inputs match. */
export function canonicalJson(value: unknown): string {
  const seen = new Set<unknown>()
  const encode = (v: unknown): string => {
    if (v === null || typeof v !== 'object') return JSON.stringify(v) ?? 'null'
    if (seen.has(v)) return '"[Circular]"'
    seen.add(v)
    try {
      if (Array.isArray(v)) return `[${v.map(encode).join(',')}]`
      const rec = v as Record<string, unknown>
      const keys = Object.keys(rec).sort()
      const parts: string[] = []
      for (const k of keys) {
        if (rec[k] === undefined) continue
        parts.push(`${JSON.stringify(k)}:${encode(rec[k])}`)
      }
      return `{${parts.join(',')}}`
    } finally {
      seen.delete(v)
    }
  }
  return encode(value)
}

/** Tool call lookup key isolated by `agentId`, shared by PreToolUse pairing and PostToolUse fallback matching. */
export function toolCallKey(toolName: string, toolInput: unknown, stripAnswers = false, agentId = ''): string {
  let input: unknown = isRecord(toolInput) ? toolInput : {}
  if (stripAnswers && isRecord(input) && 'answers' in input) {
    const copy: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(input)) {
      if (k !== 'answers') copy[k] = v
    }
    input = copy
  }
  return `${agentId}\0${toolName}\0${canonicalJson(input)}`
}

/** Extracts `(agentId, toolName, toolUseId, toolInput)` from `classic.PreToolUse` (flat on 2.1.330, or nested). */
export function extractPreToolUse(
  raw: unknown,
): { agentId: string; toolName: string; toolUseId: string; toolInput: Record<string, unknown> } | undefined {
  if (!isRecord(raw)) return undefined
  const toolName =
    typeof raw.tool === 'string' && raw.tool !== ''
      ? raw.tool
      : typeof raw.tool_name === 'string' && raw.tool_name !== ''
        ? raw.tool_name
        : ''
  const toolUseId = typeof raw.tool_use_id === 'string' ? raw.tool_use_id : ''
  if (toolName === '' || toolUseId === '') return undefined
  const agentId = typeof raw.agent_id === 'string' ? raw.agent_id : ''
  if (isRecord(raw.tool_input)) {
    return { agentId, toolName, toolUseId, toolInput: raw.tool_input }
  }
  const toolInput: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(raw)) {
    if (!PRE_TOOL_META.has(k) && v !== undefined) toolInput[k] = v
  }
  return { agentId, toolName, toolUseId, toolInput }
}

const CACHE_MOST = 128

/**
 * Bounded cache pairing `classic.PreToolUse` `tool_use_id`s with subsequent `classic.PermissionRequest` events.
 * Calls are isolated by `agent_id`. When two or more uncompleted identical calls overlap for the same
 * `(agent_id, tool, input)`, that key is marked ambiguous and `takePreTool` leaves `tool_use_id` empty rather than
 * guessing (preventing a completion for call 1 from withdrawing call 2's card).
 */
export type PreToolCache = {
  pending: Map<string, string[]>
  ambiguous: Map<string, Set<string>>
  order: { key: string; id: string }[]
  completed: Set<string>
  completedOrder: string[]
}

export function createPreToolCache(): PreToolCache {
  return {
    pending: new Map(),
    ambiguous: new Map(),
    order: [],
    completed: new Set(),
    completedOrder: [],
  }
}

export function recordPreTool(cache: PreToolCache, raw: unknown): void {
  const info = extractPreToolUse(raw)
  if (info === undefined || cache.completed.has(info.toolUseId)) return
  const key = toolCallKey(info.toolName, info.toolInput, false, info.agentId)
  const amb = cache.ambiguous.get(key)
  if (amb !== undefined) {
    amb.add(info.toolUseId)
    return
  }
  let ids = cache.pending.get(key)
  if (ids !== undefined) {
    // Drop already-completed entries before checking for concurrent ambiguity.
    ids = ids.filter(id => !cache.completed.has(id))
    if (ids.length === 0) cache.pending.delete(key)
    else cache.pending.set(key, ids)
  }
  if (ids !== undefined && ids.length > 0) {
    if (ids.includes(info.toolUseId)) return
    // Multiple concurrent uncompleted calls share the exact same (agent_id, tool, input): mark ambiguous.
    const set = new Set<string>(ids)
    set.add(info.toolUseId)
    cache.ambiguous.set(key, set)
    cache.pending.delete(key)
    for (const id of ids) removeOrder(cache, id)
    return
  }
  cache.pending.set(key, [info.toolUseId])
  cache.order.push({ key, id: info.toolUseId })
  while (cache.order.length > CACHE_MOST) {
    const oldest = cache.order.shift()
    if (oldest !== undefined) removePending(cache, oldest.key, oldest.id)
  }
}

export function takePreTool(
  cache: PreToolCache,
  toolName: string,
  toolInput: unknown,
  agentId = '',
  hasActiveSameKey = false,
): { id: string; ambiguous: boolean } {
  const key = toolCallKey(toolName, toolInput, false, agentId)
  if (cache.ambiguous.has(key)) return { id: '', ambiguous: true }
  const ids = cache.pending.get(key)
  if (ids === undefined || ids.length === 0) {
    return { id: '', ambiguous: hasActiveSameKey }
  }
  const live = ids.filter(id => !cache.completed.has(id))
  cache.pending.delete(key)
  for (const id of ids) removeOrder(cache, id)
  if (live.length === 0) return { id: '', ambiguous: hasActiveSameKey }
  if (live.length > 1 || hasActiveSameKey) {
    cache.ambiguous.set(key, new Set(live))
    return { id: '', ambiguous: true }
  }
  return { id: live[0] ?? '', ambiguous: false }
}

export function markToolCompleted(cache: PreToolCache, toolUseId: string): void {
  if (toolUseId === '') return
  if (!cache.completed.has(toolUseId)) {
    cache.completed.add(toolUseId)
    cache.completedOrder.push(toolUseId)
    while (cache.completedOrder.length > CACHE_MOST) {
      const oldest = cache.completedOrder.shift()
      if (oldest !== undefined) cache.completed.delete(oldest)
    }
  }
  for (const [key, ids] of cache.pending) {
    if (ids.includes(toolUseId)) removePending(cache, key, toolUseId)
  }
  for (const [key, set] of cache.ambiguous) {
    if (set.delete(toolUseId) && set.size === 0) {
      cache.ambiguous.delete(key)
    }
  }
  removeOrder(cache, toolUseId)
}

/** Clears pending and ambiguous PreToolUse state for an agent when its turn completes. */
export function clearAgentPreTools(cache: PreToolCache, agentId: string): void {
  const prefix = `${agentId}\0`
  for (const key of [...cache.pending.keys()]) {
    if (key.startsWith(prefix)) cache.pending.delete(key)
  }
  for (const key of [...cache.ambiguous.keys()]) {
    if (key.startsWith(prefix)) cache.ambiguous.delete(key)
  }
  cache.order = cache.order.filter(item => !item.key.startsWith(prefix))
}

export function isToolCompleted(cache: PreToolCache, toolUseId: string): boolean {
  return toolUseId !== '' && cache.completed.has(toolUseId)
}

function removePending(cache: PreToolCache, key: string, id: string): void {
  const ids = cache.pending.get(key)
  if (ids === undefined) return
  const idx = ids.indexOf(id)
  if (idx >= 0) ids.splice(idx, 1)
  if (ids.length === 0) cache.pending.delete(key)
}

function removeOrder(cache: PreToolCache, id: string): void {
  const idx = cache.order.findIndex(item => item.id === id)
  if (idx >= 0) cache.order.splice(idx, 1)
}

/** Builds the JSON body for `POST /claude-plugin/v1/permission`. */
export function buildPermissionPayload(
  sessionId: string,
  reqId: string,
  toolUseId: string,
  raw: Record<string, unknown>,
  waitMs: number,
): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    session_id: sessionId,
    req_id: reqId,
    wait_ms: waitMs,
    tool_name: typeof raw.tool_name === 'string' ? raw.tool_name : '',
    tool_input: isRecord(raw.tool_input) ? raw.tool_input : {},
  }
  if (toolUseId !== '') payload.tool_use_id = toolUseId
  if (typeof raw.cwd === 'string' && raw.cwd !== '') payload.cwd = raw.cwd
  if (typeof raw.transcript_path === 'string' && raw.transcript_path !== '') {
    payload.transcript_path = raw.transcript_path
  }
  if (typeof raw.permission_mode === 'string' && raw.permission_mode !== '') {
    payload.permission_mode = raw.permission_mode
  }
  if (typeof raw.prompt_id === 'string' && raw.prompt_id !== '') payload.prompt_id = raw.prompt_id
  if (typeof raw.agent_id === 'string' && raw.agent_id !== '') payload.agent_id = raw.agent_id
  if (typeof raw.agent_type === 'string' && raw.agent_type !== '') payload.agent_type = raw.agent_type
  if (isRecord(raw.mcp_server)) payload.mcp_server = raw.mcp_server
  if (Array.isArray(raw.permission_suggestions) && raw.permission_suggestions.length > 0) {
    payload.permission_suggestions = raw.permission_suggestions
  }
  return payload
}

/** Merges a new settings update into the active session override. */
export function mergeOverride(
  prev: SessionOverride | undefined,
  session: string,
  update: SettingsUpdate,
  sessionModel: string | undefined,
): SessionOverride {
  const base = prev?.session === session ? prev : undefined
  const next: SessionOverride = { session }
  if (update.model !== undefined) {
    next.model = update.model
    if (sessionModel !== undefined) next.baseModel = sessionModel
  } else if (base?.model !== undefined) {
    next.model = base.model
    if (base.baseModel !== undefined) next.baseModel = base.baseModel
  }
  if (update.effort !== undefined) next.effort = update.effort
  else if (base?.effort !== undefined) next.effort = base.effort
  return next
}

/** Applies the active session override to a main-loop `turn.step` input, clearing it if the computer changed models. */
export function applyStepOverride<
  E extends { model: string; effort?: 'low' | 'medium' | 'high' | 'xhigh' | 'max' | number; agentId?: string },
>(
  step: E,
  override: SessionOverride | undefined,
  session: string,
  sessionModel: string | undefined,
): { step: E; nextOverride: SessionOverride | undefined } {
  if (override === undefined || step.agentId !== undefined) return { step, nextOverride: override }
  if (override.session !== session) return { step, nextOverride: undefined }
  if (
    override.model !== undefined &&
    override.baseModel !== undefined &&
    sessionModel !== undefined &&
    sessionModel !== override.baseModel
  ) {
    // The user picked a different model in the desktop / terminal UI after Persona's switch: let the newer pick win.
    return { step, nextOverride: undefined }
  }
  const nextStep: E = { ...step }
  if (override.model !== undefined) nextStep.model = override.model
  if (override.effort === 'auto') delete nextStep.effort
  else if (override.effort !== undefined) nextStep.effort = override.effort
  return { step: nextStep, nextOverride: override }
}

/** Why a message is not submitted, or `ok`. */
export type Verdict = 'ok' | 'invalid' | 'late' | 'command'

/**
 * Whether the message may go in as the person's words now (`now`, epoch ms): text that is a string with something in
 * it and not too long, before its deadline, and no slash command (the node sends none; a command would run, not be
 * read).
 */
export function verdict(m: Message, now: number): Verdict {
  if (typeof m.text !== 'string' || m.text.trim() === '' || m.text.length > TEXT_MOST) return 'invalid'
  if (now > m.deadlineMs) return 'late'
  if (m.text.trimStart().startsWith('/')) return 'command'
  return 'ok'
}

/** The wait after a failed poll: the first backoff, then twice the last, up to the most. */
export function nextBackoff(last: number): number {
  return last <= 0 ? BACKOFF_FIRST_MS : Math.min(last * 2, BACKOFF_MOST_MS)
}

function parse(body: string): Record<string, unknown> | undefined {
  try {
    const v: unknown = JSON.parse(body)
    return isRecord(v) ? v : undefined
  } catch {
    return undefined
  }
}

function retryOf(answer: Record<string, unknown> | undefined, fallback: number): number {
  const r = answer?.retry_ms
  return typeof r === 'number' && r >= 0 && r <= 3_600_000 ? r : fallback
}
