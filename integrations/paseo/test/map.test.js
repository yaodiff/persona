import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  AGENT_NOTICE, AUTO_ACCEPT_ROOTS, CANCELED_NOTE, ERROR_LEAD, ERROR_TEXT, FROM_SESSION, SCHEDULE_NOTICE, agentRoot, agentRow, archivedUnderOwnId, asksForTool,
  autoAcceptOf, codexThreadOf, backgroundOf, defaultProvider, effortFor, exitNote, interactionOf, launchersOf, linkedWorktree, modeBeforePlan,
  parentAgentId, paseoMode, permissionModesOf, promptKey, providerRoots, responseOf, rewindOf, rowStatus, projectSpaces, sameKindProvider, sessionIdOf,
  slotNameOf, spaceIdOf, startedByPersona, strayRecord, timelineItems, toolCall, transcriptOf, workspaceRow, paseoCheckouts,
} from '../lib/map.js'
import { agentSnapshot, autoAcceptFeature, entry, workspaceDescriptor } from './fake-paseo.js'

const CLAUDE_SID = 'aaaaaaaa-0000-4000-8000-000000000001'

// Providers as Paseo's config has them: built-ins, and custom ones extending them (a chain too).
const CONFIG = {
  claude: { enabled: false },
  'my-claude': { extends: 'claude', label: 'My Claude' },
  'my-claude2': { extends: 'my-claude' },
  'my-codex': { extends: 'codex', label: 'My Codex' },
  grok: { extends: 'acp', label: 'Grok' },
}

test('a custom provider is the built-in it extends, through a chain; an agent records its own', () => {
  const roots = providerRoots(CONFIG, [{ provider: 'pi' }])
  assert.equal(roots['my-claude'], 'claude')
  assert.equal(roots['my-claude2'], 'claude')
  assert.equal(roots['my-codex'], 'codex')
  assert.equal(roots.grok, 'acp')
  assert.equal(roots.pi, 'pi')
  assert.equal(providerRoots({ a: { extends: 'b' }, b: { extends: 'a' } }).a, 'a', 'a loop ends')
  assert.equal(agentRoot({ provider: 'odd', persistence: { metadata: { provider: 'claude' } } }, roots), 'claude')
  assert.equal(agentRoot({ provider: 'my-claude2' }, roots), 'claude')
})

test("an agent's id: Persona's label, else Claude's session for Claude Code, else paseo-<agent id>", () => {
  const roots = providerRoots(CONFIG)
  const claude = agentSnapshot({ provider: 'my-claude', persistence: { provider: 'my-claude', sessionId: CLAUDE_SID, metadata: { provider: 'claude' } } })
  assert.equal(sessionIdOf(claude, roots), CLAUDE_SID)
  assert.equal(transcriptOf(claude, roots), CLAUDE_SID)
  const fresh = agentSnapshot({ provider: 'my-claude', runtimeInfo: { sessionId: CLAUDE_SID } })
  assert.equal(sessionIdOf(fresh, roots), CLAUDE_SID, 'the session its runtime starts with')
  assert.equal(transcriptOf(fresh, roots), undefined, 'no transcript before it is persisted')
  assert.equal(sessionIdOf(agentSnapshot({ provider: 'my-claude' }), roots), undefined, 'none yet: not listed')
  const labeled = agentSnapshot({ provider: 'my-claude', labels: { 'persona.session': 'paseo-x1' }, persistence: { sessionId: CLAUDE_SID } })
  assert.equal(sessionIdOf(labeled, roots), 'paseo-x1')
  assert.equal(transcriptOf(labeled, roots), CLAUDE_SID)
  const codex = agentSnapshot({ id: 'c0ffee', provider: 'my-codex', persistence: { sessionId: '0199-thread' } })
  assert.equal(sessionIdOf(codex, roots), 'paseo-c0ffee')
  assert.equal(transcriptOf(codex, roots), undefined, 'only Claude Code has a transcript the node reads')
  // A Codex agent names the thread its conversation is: the node lists it once, not again from its rollout (#4495).
  const THREAD = '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0001'
  assert.equal(codexThreadOf(codex, roots), undefined, 'no thread id yet (Codex still starting): nothing named')
  const threaded = agentSnapshot({ id: 'c0ffee', provider: 'my-codex', persistence: { sessionId: THREAD, metadata: { provider: 'codex', threadId: THREAD } } })
  assert.equal(codexThreadOf(threaded, roots), THREAD)
  assert.equal(codexThreadOf(agentSnapshot({ provider: 'my-codex', runtimeInfo: { sessionId: THREAD } }), roots), THREAD, 'not persisted yet: the one its runtime runs')
  assert.equal(codexThreadOf(claude, roots), undefined, 'an agent of Claude Code has a transcript, no thread')
  assert.deepEqual([agentRow(threaded, { roots }).codex_thread, agentRow(codex, { roots }).codex_thread, agentRow(claude, { roots }).codex_thread],
    [THREAD, undefined, undefined])
  assert.equal(sessionIdOf(agentSnapshot({ provider: 'grok', labels: { 'persona.session': '../bad' } }), roots).startsWith('paseo-'), true, 'a bad label is not an id')
})

test('status: needing you while a request waits; working; done after a turn; 未运行 when closed', () => {
  assert.equal(rowStatus({ status: 'running', pendingPermissions: [{ id: 'p' }] }), 'blocked')
  assert.equal(rowStatus({ status: 'running' }), 'working')
  assert.equal(rowStatus({ status: 'initializing' }), 'working')
  assert.equal(rowStatus({ status: 'idle', lastUserMessageAt: '2026-01-01T00:00:00Z' }), 'done')
  assert.equal(rowStatus({ status: 'idle', lastUserMessageAt: null }), 'idle')
  assert.equal(rowStatus({ status: 'closed' }), 'offline')
  assert.equal(rowStatus({ status: 'error' }), 'done')
  // Paseo's own seen state (#2831): waiting for you to look at it, or already seen in Paseo.
  const ran = '2026-01-01T00:00:00Z'
  assert.equal(rowStatus({ status: 'idle', lastUserMessageAt: ran, requiresAttention: true, attentionReason: 'finished' }), 'done')
  assert.equal(rowStatus({ status: 'idle', lastUserMessageAt: ran, requiresAttention: false }), 'idle', 'seen in Paseo')
  assert.equal(rowStatus({ status: 'error', requiresAttention: false }), 'idle')
  assert.equal(rowStatus({ status: 'error', requiresAttention: true, attentionReason: 'error' }), 'done')
  assert.equal(rowStatus({ status: 'running', requiresAttention: true }), 'working', 'running first')
  assert.equal(rowStatus({ status: 'idle', requiresAttention: true, pendingPermissions: [{ id: 'p' }] }), 'blocked')
})

test('a closed agent Paseo still marks for you to look at stays done: Paseo letting its process go is no "Not running" (#8133)', () => {
  // Paseo unloads an idle agent after a while (closed) and keeps its attention mark, which only looking at it clears.
  assert.equal(rowStatus({ status: 'closed', requiresAttention: true, attentionReason: 'finished' }), 'done')
  assert.equal(rowStatus({ status: 'closed', requiresAttention: true, attentionReason: 'error' }), 'done')
  assert.equal(rowStatus({ status: 'closed', requiresAttention: false }), 'offline', 'looked at: not running')
  assert.equal(rowStatus({ status: 'closed' }), 'offline', 'a Paseo without the flag')
  assert.equal(rowStatus({ status: 'closed', requiresAttention: true }, { quiet: true }), 'offline', "another agent's subtask is never waited on (#4494)")
  assert.equal(rowStatus({ status: 'closed', requiresAttention: true }, { fleet: true }), 'idle', 'the agents it created still run: their reports wake it')
  assert.equal(rowStatus({ status: 'closed', requiresAttention: true, pendingPermissions: [{ id: 'p' }] }), 'blocked')
  const roots = providerRoots(CONFIG)
  const closed = agentSnapshot({ provider: 'grok', cwd: '/w/x', status: 'closed', requiresAttention: true, updatedAt: '2026-11-04T10:00:00.000Z' })
  assert.deepEqual([agentRow(closed, { roots }).status, agentRow(closed, { roots }).updated_at], ['done', '2026-11-04T10:00:00.000Z'],
    'Unread, from when it finished')
  assert.equal(agentRow({ ...closed, archivedAt: '2026-11-04T11:00:00.000Z' }, { roots, archived: true }).status, 'offline', 'archived: not running, whatever its mark')
})

test('a row: its space, the agent the apps know, model, modes and transcript', () => {
  const roots = providerRoots(CONFIG)
  const a = agentSnapshot({
    provider: 'my-claude', cwd: '/w/proj', workspaceId: 'wks_9', title: '  修登录  ', model: 'claude-x[1m]', currentModeId: 'acceptEdits',
    effectiveThinkingOptionId: 'max', persistence: { sessionId: CLAUDE_SID, metadata: { provider: 'claude' } }, updatedAt: '2026-11-04T10:00:00.000Z',
    createdAt: '2026-11-04T09:58:00.000Z',
  })
  const row = agentRow(a, { roots, project: { projectKey: 'prj', projectName: 'proj', workspaceName: '登录修复' } })
  assert.deepEqual(row, {
    session_id: CLAUDE_SID, title: '修登录', cwd: '/w/proj', status: 'idle', updated_at: '2026-11-04T10:00:00.000Z',
    rev: Date.parse('2026-11-04T10:00:00.000Z'), space_id: 'prj', space_name: 'proj', slot_name: '登录修复', workspace_id: 'wks_9',
    agent: 'claude', launcher: 'my-claude', started_at: '2026-11-04T09:58:00.000Z',
    model: 'claude-x[1m]', permission_mode: 'acceptEdits', effort: 'max', transcript: CLAUDE_SID,
    capabilities: { rewind: false, rewind_code: false },
  })
  const g = agentRow(agentSnapshot({ id: 'g1', provider: 'grok', cwd: '/w/x', currentModeId: 'yolo', status: 'error', lastError: 'boom' }),
    { roots, labels: { grok: 'Grok · Official' }, lastText: 'ignored' })
  assert.equal(g.agent, 'paseo')
  assert.equal(g.agent_label, 'Grok · Official')
  assert.equal(g.title, 'x')
  assert.equal(g.permission_mode, undefined, "a mode Persona does not have is not shown")
  assert.equal(g.last_text, '出错了：boom')
  assert.equal(agentRow(agentSnapshot({ provider: 'grok', archivedAt: '2026-01-01T00:00:00Z' }), { roots }), undefined)
})

test("a rewind's reach is the provider's, and a prompt compares as the user typed it", () => {
  assert.deepEqual(rewindOf(agentSnapshot({ capabilities: { supportsRewindConversation: true, supportsRewindFiles: true, supportsRewindBoth: true } })),
    { conversation: true, code: true }, 'Claude Code')
  assert.deepEqual(rewindOf(agentSnapshot({ capabilities: { supportsRewindConversation: true } })), { conversation: true, code: false }, 'Codex, pi, omp')
  assert.deepEqual(rewindOf(agentSnapshot({ capabilities: { supportsRewindBoth: true } })), { conversation: false, code: false }, 'OpenCode: both at once only')
  assert.deepEqual(rewindOf(agentSnapshot({ capabilities: {} })), { conversation: false, code: false }, 'a Paseo that does not say')
  assert.equal(promptKey('  fix  the\nbug '), 'fixthebug')
  assert.equal(promptKey('<persona-context kind="selection" title="a.ts">const x = 1</persona-context>\n fix it'), 'fixit')
  assert.equal(promptKey('<persona-context>\nnow: 10:00\n</persona-context>\nhi'), 'hi')
  for (const typed of ['[Request interrupted by user]', '<command-name>/clear</command-name>', '<local-command-stdout></local-command-stdout>', '', undefined]) {
    assert.equal(promptKey(typed), '', `not the user's own words: ${typed}`)
  }
})

test("an archived agent keeps its row when its id is not its transcript's: a session Persona started, not one of Paseo's own (#4771)", () => {
  const roots = providerRoots(CONFIG)
  const at = '2026-11-04T10:00:00.000Z'
  const claude = (fields) => agentSnapshot({ provider: 'my-claude', cwd: '/w/proj', title: 'T', status: 'closed', archivedAt: at, updatedAt: at,
    persistence: { provider: 'my-claude', sessionId: CLAUDE_SID, metadata: { provider: 'claude' } }, ...fields })
  const started = claude({ id: 'a1', labels: { 'persona.session': 'paseo-a1' } })
  assert.equal(archivedUnderOwnId(started, roots), true, 'started from Persona: paseo-<agent id>, its transcript another id')
  assert.equal(archivedUnderOwnId(claude({}), roots), false, "started in Paseo: its id is its transcript's, which the node lists")
  assert.equal(archivedUnderOwnId(claude({ labels: { 'persona.session': CLAUDE_SID } }), roots), false, 'taken over: the same id either way')
  assert.equal(archivedUnderOwnId({ ...started, archivedAt: null }, roots), false, 'not archived: a row as any other')
  assert.equal(archivedUnderOwnId(claude({ labels: { 'persona.session': 'paseo-a1', 'paseo.parent-agent-id': 'boss' } }), roots), false,
    "another agent created it: its creator's conversation says what it did")
  assert.equal(archivedUnderOwnId({ ...started, persistence: null }, roots), false, 'no transcript: nothing to read')
  assert.equal(archivedUnderOwnId(agentSnapshot({ id: 'c1', provider: 'my-codex', archivedAt: at, labels: { 'persona.session': 'paseo-c1' } }), roots),
    false, 'Codex without a known thread: no conversation to retain yet')
  // Its row: the session's own id, not running, its transcript the conversation.
  const row = agentRow(started, { roots, project: { projectKey: 'prj', projectName: 'proj' }, archived: true })
  assert.deepEqual([row.session_id, row.status, row.transcript, row.title, row.space_id, row.updated_at], ['paseo-a1', 'offline', CLAUDE_SID, 'T', 'prj', at])
  assert.equal(agentRow({ ...started, status: 'idle' }, { roots, archived: true }).status, 'offline', 'whatever its record says')
  assert.equal(agentRow(started, { roots }), undefined, 'without archived: no row, as before')
  // Paseo changes nothing of an agent it keeps archived (rewind, permission mode, effort, model): the row offers none of
  // them, whatever the record it was archived with says it can do (#8133).
  const able = { ...started, capabilities: { supportsRewindConversation: true, supportsRewindBoth: true } }
  assert.deepEqual(agentRow(able, { roots, archived: true, thinking: ['low', 'high'] }).capabilities,
    { rewind: false, rewind_code: false, modes: false, model: false })
  assert.deepEqual(agentRow({ ...able, archivedAt: null }, { roots }).capabilities,
    { rewind: true, rewind_code: true, a2a_wake_on_send: true },
    'an idle-retired agent can wake on send until it is archived')
  const grok = agentSnapshot({ id: 'g1', provider: 'grok', archivedAt: at, labels: { 'persona.session': 'paseo-g1' } })
  assert.equal(agentRow(grok, { roots, archived: true, thinking: ['low', 'high'] }).efforts, undefined, 'no effort to pick either')
})

test("a row that is not Claude Code's lists the efforts its model takes, among Persona's levels (#8133)", () => {
  const roots = providerRoots(CONFIG)
  const grok = agentSnapshot({ provider: 'grok', cwd: '/w/x' })
  assert.deepEqual(agentRow(grok, { roots, thinking: ['low', 'medium', 'high', 'ultra'] }).efforts, ['low', 'medium', 'high'])
  assert.deepEqual(agentRow(agentSnapshot({ provider: 'my-codex', cwd: '/w/x' }), { roots, thinking: ['xhigh', 'low'] }).efforts, ['low', 'xhigh'],
    'weakest first')
  assert.equal(agentRow(grok, { roots, thinking: ['ultra'] }).efforts, undefined, "none of Persona's levels: none listed")
  assert.equal(agentRow(grok, { roots }).efforts, undefined, 'Paseo has not said')
  const claude = agentSnapshot({ provider: 'my-claude', cwd: '/w/x', persistence: { sessionId: CLAUDE_SID, metadata: { provider: 'claude' } } })
  assert.equal(agentRow(claude, { roots, thinking: ['low', 'high'] }).efforts, undefined, "Claude Code's are the apps' own")
})

test("rows of agents Persona started or took over say so; an agent made in Paseo's app does not, nor one whose id the bridge only kept (#8130)", () => {
  const roots = providerRoots(CONFIG)
  const claude = (labels) => agentSnapshot({ provider: 'my-claude', cwd: '/w/proj', labels,
    persistence: { provider: 'my-claude', sessionId: CLAUDE_SID, metadata: { provider: 'claude' } } })
  const started = claude({ 'persona.session': 'bbbbbbbb-0000-4000-8000-000000000042' })
  const takenOver = claude({ 'persona.session': CLAUDE_SID })
  const own = claude({})
  const kept = claude({ 'persona.session': 'cccccccc-0000-4000-8000-000000000003', 'persona.kept': '1' })
  assert.deepEqual([started, takenOver, own, kept].map(startedByPersona), [true, true, false, false])
  assert.deepEqual([started, takenOver, own, kept].map((a) => agentRow(a, { roots }).started_by_persona), [true, true, undefined, undefined])
  assert.equal(startedByPersona(claude({ 'persona.session': '../x' })), false, 'not a session id the node takes')
})

test('an archived Codex conversation keeps a Persona-owned id, without changing Paseo-originated agents (#5129)', () => {
  const roots = providerRoots({ ...CONFIG, 'codex-alias': { extends: 'my-codex' } })
  const thread = '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0003'
  const codex = agentSnapshot({ id: 'cx1', provider: 'codex-alias', archivedAt: '2026-11-04T10:00:00.000Z',
    labels: { 'persona.session': 'paseo-cx1' },
    persistence: { sessionId: thread, metadata: { provider: 'codex', threadId: thread } } })
  assert.equal(archivedUnderOwnId(codex, roots), true)
  const row = agentRow(codex, { roots, archived: true })
  assert.deepEqual([row.session_id, row.status, row.codex_thread, row.transcript], ['paseo-cx1', 'offline', thread, undefined])
  assert.equal(archivedUnderOwnId({ ...codex, labels: {} }, roots), false, 'Paseo-originated agents keep their current archive behavior')
  assert.equal(archivedUnderOwnId({ ...codex, labels: { 'persona.session': '../invalid' } }, roots), false, 'a rejected label is not Persona ownership')
  assert.equal(archivedUnderOwnId({ ...codex, labels: { 'persona.session': thread } }, roots), false, 'no different id to preserve')
  assert.equal(archivedUnderOwnId({ ...codex, archivedAt: null }, roots), false)
  assert.equal(archivedUnderOwnId({ ...codex, labels: { ...codex.labels, 'paseo.parent-agent-id': 'boss' } }, roots), false)
  assert.equal(archivedUnderOwnId({ ...codex, persistence: null }, roots), false, 'no known Codex thread yet')
  assert.equal(archivedUnderOwnId({ ...codex, persistence: null, runtimeInfo: { sessionId: thread } }, roots), true, 'a known runtime thread also identifies the conversation')
})

test("a delegated agent is its creator's subtask: its parent, no waiting to be looked at; the unfinished ones are the creator's background work", () => {
  const roots = providerRoots(CONFIG)
  const child = (o = {}) => agentSnapshot({ id: 'kid-1', provider: 'my-codex', cwd: '/w/proj', title: '评审 E5（GPT）', createdAt: '2026-11-04T09:00:00.000Z',
    labels: { 'paseo.parent-agent-id': ' boss-1 ', 'paseo.parent-server-id': 'srv_1' }, ...o })
  assert.equal(parentAgentId(child()), 'boss-1')
  assert.deepEqual([parentAgentId(agentSnapshot()), parentAgentId({ labels: { 'paseo.parent-agent-id': '  ' } }), parentAgentId(undefined)], [undefined, undefined, undefined])
  // Paseo sets the flag on a delegated agent too, but shows it on no workspace, and nobody opens it there: left to
  // the flag it would stay "done, not looked at" for good. The bridge says which agents are quiet that way.
  const ran = '2026-11-04T09:30:00.000Z'
  const quiet = { quiet: true }
  assert.equal(rowStatus(child({ status: 'idle', lastUserMessageAt: ran, requiresAttention: true, attentionReason: 'finished' }), quiet), 'idle')
  assert.equal(rowStatus(child({ status: 'error', requiresAttention: true, attentionReason: 'error' }), quiet), 'idle')
  assert.equal(rowStatus(child({ status: 'idle', lastUserMessageAt: ran }), quiet), 'idle', 'a Paseo without the flag too')
  assert.deepEqual([rowStatus(child({ status: 'running' }), quiet), rowStatus(child({ status: 'closed', requiresAttention: true }), quiet),
    rowStatus(child({ status: 'idle', pendingPermissions: [{ id: 'p' }] }), quiet)], ['working', 'offline', 'blocked'], 'what it does now is still said')
  assert.equal(rowStatus(child({ status: 'idle', lastUserMessageAt: ran, requiresAttention: true })), 'done',
    "not for its label alone: one created from another workspace is that workspace's own, looked at like any agent")

  const row = agentRow(child({ status: 'running' }), { roots, parent: CLAUDE_SID, quiet: true })
  assert.deepEqual([row.is_subagent, row.parent_session_id, row.status], [true, CLAUDE_SID, 'working'])
  assert.equal(row.started_at, '2026-11-04T09:00:00.000Z', 'when it was created: its card counts its running time from it')
  assert.equal(agentRow(child({ createdAt: undefined }), { roots }).started_at, undefined, 'never a time it was not created at')
  assert.equal(agentRow(child({ status: 'idle', requiresAttention: true }), { roots, parent: CLAUDE_SID, quiet: true }).status, 'idle')
  const own = agentRow(child({ status: 'running' }), { roots })
  assert.deepEqual([own.is_subagent, own.parent_session_id], [undefined, undefined], 'its creator is not listed beside it: a row of its own')

  const boss = agentSnapshot({ id: 'boss-1', provider: 'my-claude', cwd: '/w/proj', status: 'idle', requiresAttention: true,
    persistence: { sessionId: CLAUDE_SID, metadata: { provider: 'claude' } } })
  const led = agentRow(boss, { roots, fleet: [child({ status: 'running' }), child({ id: 'kid-2', title: '  ', status: 'running' })] })
  assert.deepEqual(led.background, [
    { id: 'paseo-kid-1', kind: 'agent', description: '评审 E5（GPT）', started_at: '2026-11-04T09:00:00.000Z' },
    { id: 'paseo-kid-2', kind: 'agent', started_at: '2026-11-04T09:00:00.000Z' },
  ])
  assert.equal(led.status, 'done', "its own turn ended; the hub counts it as working while they run")
  assert.equal(agentRow(boss, { roots, fleet: [] }).background, undefined)
  // Paseo let its process go while they still run: their reports wake it, so it is not "Offline" (which a list of
  // what runs leaves out, with them under it). With none left, it is done while Paseo still marks it for you to look
  // at (#8133), and "Offline" once it does not.
  const let_go = { ...boss, status: 'closed' }
  assert.deepEqual([agentRow(let_go, { roots, fleet: [child({ status: 'running' })] }).status, agentRow(let_go, { roots }).status,
    rowStatus(let_go, { fleet: true }), rowStatus(let_go), rowStatus({ ...let_go, requiresAttention: false })], ['idle', 'done', 'idle', 'done', 'offline'])
  assert.equal(backgroundOf({ title: 'no id' }), undefined)
})

test("a Codex row: Paseo's full-access is Persona's bypassPermissions, the other Codex modes are not reported", () => {
  const roots = providerRoots(CONFIG)
  const row = (currentModeId) => agentRow(agentSnapshot({ provider: 'my-codex', cwd: '/w/x', currentModeId }), { roots })
  assert.equal(row('full-access').permission_mode, 'bypassPermissions')
  assert.equal(row('auto').permission_mode, undefined)
  assert.equal(row('auto-review').permission_mode, undefined)
  assert.deepEqual([paseoMode('codex', 'bypassPermissions'), paseoMode('codex', 'auto'), paseoMode('claude', 'bypassPermissions')],
    ['full-access', undefined, 'bypassPermissions'])
  assert.equal(paseoMode('acp', 'bypassPermissions'), undefined)
})

test("Allow all is Paseo's Auto Accept where that is the approval (Grok); off, or not listed, says no mode", () => {
  const roots = providerRoots(CONFIG)
  assert.ok(AUTO_ACCEPT_ROOTS.has(roots.grok), 'Grok extends acp')
  const row = (fields) => agentRow(agentSnapshot({ provider: 'grok', cwd: '/w/x', ...fields }), { roots })
  assert.equal(row({ features: [autoAcceptFeature(true)] }).permission_mode, 'bypassPermissions')
  assert.equal(row({ features: [autoAcceptFeature(false)] }).permission_mode, undefined, "off: Grok's own settings may still approve everything")
  assert.equal(row({ status: 'closed' }).permission_mode, undefined, 'not loaded: Paseo lists no switch')
  assert.deepEqual([autoAcceptOf({ features: [autoAcceptFeature(true)] }), autoAcceptOf({ features: [autoAcceptFeature()] }), autoAcceptOf({ features: [] })],
    [true, false, undefined])
})

test("a provider that is not Claude Code's takes its own approval, and Allow all where Paseo has one: its rows and launchers say so (#8261)", () => {
  const both = ['default', 'bypassPermissions']
  assert.deepEqual([permissionModesOf('acp'), permissionModesOf('opencode'), permissionModesOf('codex'), permissionModesOf('pi'), permissionModesOf('pi', true),
    permissionModesOf('claude')], [both, both, both, ['default'], both, undefined])
  const roots = providerRoots(CONFIG, [{ provider: 'pi' }])
  const row = (provider, fields) => agentRow(agentSnapshot({ provider, cwd: '/w/x', ...fields }), { roots })
  assert.deepEqual(row('grok').capabilities.permission_modes, both, "Grok's Auto Accept, though Paseo has not loaded it")
  assert.deepEqual(row('my-codex').capabilities.permission_modes, both, "Codex's full-access")
  assert.deepEqual(row('pi').capabilities.permission_modes, ['default'], 'no Allow all: its own alone, nothing to switch')
  assert.deepEqual(row('pi', { features: [autoAcceptFeature(false)] }).capabilities.permission_modes, both, 'a switch Paseo lists for it')
  assert.equal(row('my-claude', { persistence: { sessionId: CLAUDE_SID, metadata: { provider: 'claude' } } }).capabilities.permission_modes, undefined,
    "Claude Code's five are the apps' own")
  const archived = agentSnapshot({ id: 'g1', provider: 'grok', archivedAt: '2026-11-04T10:00:00.000Z', labels: { 'persona.session': 'paseo-g1' } })
  assert.equal(agentRow(archived, { roots, archived: true }).capabilities.permission_modes, undefined, 'archived: Paseo changes none of it')
})

test("spaces: a project's checkout and its worktrees are one space; a workspace names where an agent is, and is no slot", () => {
  // The placement's projectKey is Paseo's project record id.
  const at = (dir, o = {}) => ({ cwd: dir, isGit: true, currentBranch: o.branch ?? null, remoteUrl: null, worktreeRoot: dir,
    isPaseoOwnedWorktree: o.paseo ?? false, mainRepoRoot: o.main ?? null })
  const main = { projectKey: 'prj_app', projectName: 'app', workspaceName: '主线', checkout: at('/w/app') }
  const wt = { projectKey: 'prj_app', projectName: 'app', workspaceName: '修登录', checkout: at('/wt/brave-fox', { paseo: true, main: '/w/app' }) }
  // Paseo handed the worktree's directory alone (#2802): a record of its own, named after the folder.
  const stray = { projectKey: 'prj_stray', projectName: 'brave-fox', workspaceName: '  ',
    checkout: at('/wt/brave-fox', { paseo: true, main: '/w/app', branch: 'fix-login' }) }
  const gitWt = { projectKey: 'prj_app', projectName: 'app', workspaceName: null, checkout: at('/w/app-hotfix', { main: '/w/app' }) }
  const folder = { projectKey: 'prj_notes', projectName: 'notes', workspaceName: '周报', checkout: { cwd: '/w/notes', isGit: false } }
  assert.deepEqual([main, wt, stray, gitWt, folder].map(linkedWorktree), [false, true, true, true, false])
  assert.deepEqual([main, wt, stray, gitWt, folder].map(strayRecord), [false, false, true, false, false])
  assert.equal(spaceIdOf(wt), 'prj_app')
  assert.equal(spaceIdOf(undefined), undefined)
  assert.deepEqual([slotNameOf(wt, '/wt/brave-fox'), slotNameOf(stray, '/wt/brave-fox'), slotNameOf(gitWt, '/w/app-hotfix')],
    ['修登录', 'fix-login', 'app-hotfix'], "the workspace's title, else its branch, else its folder")

  const s = projectSpaces([main, wt, stray, gitWt, folder].map((project) => ({ project })))
  assert.deepEqual([main, wt, stray, gitWt, folder, undefined].map(s.idOf), ['prj_app', 'prj_app', 'prj_app', 'prj_app', 'prj_notes', undefined],
    "the stray record goes into its repository's project")
  assert.deepEqual(Object.fromEntries(s.names), { prj_app: 'app', prj_notes: 'notes' })
  const alone = projectSpaces([{ project: stray }])
  assert.deepEqual([alone.idOf(stray), Object.fromEntries(alone.names)], ['prj_stray', { prj_stray: 'app' }],
    "its repository's project unknown: a space of its own, named after the repository's folder")
  // The main checkout reached through a symbolic link: compared as the bridge resolves it.
  const linked = { ...main, checkout: at('/home/me/app') }
  const viaLink = projectSpaces([linked, stray].map((project) => ({ project })), (p) => p.replace('/home/me/', '/w/'))
  assert.equal(viaLink.idOf(stray), 'prj_app')

  const roots = providerRoots(CONFIG)
  const row = (workspaceId, project) => agentRow(agentSnapshot({ provider: 'grok', cwd: project.checkout.cwd, workspaceId }),
    { roots, project, spaceId: s.idOf(project), spaceName: s.names.get(s.idOf(project)) })
  const rows = [row('wks_main', main), row('wks_wt', wt), row('wks_stray', stray)]
  assert.deepEqual(rows.map((r) => [r.space_id, r.space_name, r.slot_id, r.slot_name]), [
    ['prj_app', 'app', undefined, '主线'], ['prj_app', 'app', undefined, '修登录'], ['prj_app', 'app', undefined, 'fix-login'],
  ], "no slot id: the apps draw the agents of one slot as one tab's panes (#4493)")
  assert.deepEqual(rows.map((r) => r.repo_root), ['/w/app', '/w/app', '/w/app'], "按仓库: the repository's main checkout (#2840)")
  assert.equal(row('wks_notes', folder).repo_root, undefined, 'not a git repository: the path says enough')
  const physical = agentRow(agentSnapshot({ provider: 'grok', cwd: main.checkout.cwd, workspaceId: 'wks_main' }),
    { roots, project: main, canon: (path) => `/data${path}` })
  assert.equal(physical.repo_root, '/data/w/app', 'by its physical directory, as the bridge resolves it (#4492)')
  const bare = agentRow(agentSnapshot({ provider: 'grok', cwd: '/w/x', workspaceId: 'wks_x' }), { roots })
  assert.deepEqual([bare.space_id, bare.space_name, bare.slot_id], ['wks_x', 'x', undefined], 'no placement: its workspace, as before')
})

test("launchers: one per usable provider, with the agent it starts, its models' Persona efforts, and whether its approval is Auto Accept", () => {
  const entries = [
    { provider: 'claude', enabled: false, status: 'unavailable', models: [] },
    { provider: 'my-claude', enabled: true, status: 'ready', label: 'My Claude', models: [
      { id: 'fable[1m]', label: 'Fable (1M)', isDefault: true, thinkingOptions: [{ id: 'low' }, { id: 'medium' }, { id: 'high' }, { id: 'max' }], defaultThinkingOptionId: 'max' },
      { id: 'haiku', label: 'Haiku', thinkingOptions: [], isSelectable: true },
      { id: 'hidden', label: 'Hidden', isSelectable: false },
    ] },
    { provider: 'my-codex', enabled: true, status: 'loading', label: 'My Codex', models: [] },
    { provider: 'grok', enabled: true, status: 'ready', label: 'Grok', models: [{ id: 'g', label: 'G', thinkingOptions: [{ id: 'xhigh' }, { id: 'ultra' }], defaultThinkingOptionId: 'ultra' }] },
    { provider: 'bad.name', enabled: true, status: 'ready', models: [] },
    { provider: 'opencode', enabled: true, status: 'ready', label: 'OpenCode', models: [] },
    { provider: 'pi', enabled: true, status: 'ready', label: 'Pi', models: [] },
  ]
  const roots = providerRoots(CONFIG, entries)
  // Grok (over ACP) and OpenCode approve through Paseo's Auto Accept switch (#8141); Claude Code and Codex have modes,
  // pi neither. Each is named as in Paseo, and one that is not Claude Code's says the permission modes a start takes:
  // Allow all with Auto Accept or Codex's full-access, else its own alone (#8261).
  const both = ['default', 'bypassPermissions']
  assert.deepEqual(launchersOf(entries, roots), [
    { name: 'my-claude', agent: 'claude', model: 'Fable (1M)', display_name: 'My Claude', models: [
      { id: 'fable[1m]', name: 'Fable (1M)', default: true, efforts: ['low', 'medium', 'high', 'max'], default_effort: 'max' },
      { id: 'haiku', name: 'Haiku' },
    ] },
    // Paseo creates a Codex agent with no first message: the form need not ask for one (#8140).
    { name: 'my-codex', agent: 'codex', model: 'My Codex', models: [], prompt_optional: true, display_name: 'My Codex', permission_modes: both },
    { name: 'grok', agent: 'paseo', model: 'G', models: [{ id: 'g', name: 'G', efforts: ['xhigh'] }], auto_accept: true, display_name: 'Grok',
      permission_modes: both },
    { name: 'opencode', agent: 'paseo', model: 'OpenCode', models: [], auto_accept: true, display_name: 'OpenCode', permission_modes: both },
    { name: 'pi', agent: 'paseo', model: 'Pi', models: [], display_name: 'Pi', permission_modes: ['default'] },
  ])
  assert.equal(launchersOf([{ provider: 'pi', enabled: true, status: 'ready', label: 'pi' }], { pi: 'pi' })[0].display_name, undefined,
    'a label that is its id names nothing more')
  assert.equal(defaultProvider(entries, roots), 'my-claude', "Claude Code's own is off: the first running Claude Code")
  assert.equal(defaultProvider([{ provider: 'claude', enabled: true, status: 'ready' }, ...entries], roots), 'claude')
})

test('a launcher Paseo lacks: its provider of the same kind, its own built-in first, never another kind (#8140)', () => {
  const roots = { claude: 'claude', 'my-claude': 'claude', 'cc-b': 'claude', codex: 'codex', 'my-codex': 'codex', grok: 'acp' }
  const launchers = ['my-claude', 'cc-b', 'my-codex', 'grok'].map((name) => ({ name }))
  assert.equal(sameKindProvider(launchers, roots, 'claude'), 'my-claude', 'the first of its kind, Paseo default first')
  assert.equal(sameKindProvider([...launchers, { name: 'claude' }], roots, 'claude'), 'claude', "Paseo's built-in Claude Code, when it is on")
  assert.equal(sameKindProvider(launchers, roots, 'codex'), 'my-codex')
  assert.equal(sameKindProvider(launchers, roots, 'claude', (p) => p !== 'my-claude'), 'cc-b', 'only those it accepts (plan mode)')
  for (const kind of ['traex', 'agy', '', undefined]) assert.equal(sameKindProvider(launchers, roots, kind), undefined, String(kind))
})

test("an effort the model does not offer: the nearest lower one it offers, else its lowest; none of Persona's: none (#8140)", () => {
  const four = ['low', 'medium', 'high', 'max']
  assert.equal(effortFor('xhigh', four), 'high')
  assert.equal(effortFor('high', four), 'high')
  assert.equal(effortFor('medium', ['low', 'high', 'turbo']), 'low')
  assert.equal(effortFor('low', ['high', 'max']), 'high', 'none lower: the lowest it offers')
  assert.equal(effortFor('high', ['turbo', 'minimal']), undefined, "no level of Persona's: the model's own default")
})

test("tool calls as the apps draw them: Claude Code's names and arguments", () => {
  assert.deepEqual(toolCall('x', { type: 'shell', command: 'ls -la', cwd: '/w' }), { name: 'Bash', input: { command: 'ls -la', cwd: '/w' } })
  assert.deepEqual(toolCall('x', { type: 'edit', filePath: '/a.go', oldString: 'a', newString: 'b' }), { name: 'Edit', input: { file_path: '/a.go', old_string: 'a', new_string: 'b' } })
  assert.deepEqual(toolCall('x', { type: 'search', query: '*.go', toolName: 'glob' }), { name: 'Glob', input: { pattern: '*.go' } })
  assert.deepEqual(toolCall('x', { type: 'search', query: 'paseo', toolName: 'web_search' }), { name: 'WebSearch', input: { query: 'paseo' } })
  assert.deepEqual(toolCall('mcp__x__y', { type: 'unknown', input: { a: 1 }, output: null }), { name: 'mcp__x__y', input: { a: 1 } })
  assert.deepEqual(toolCall('read', { type: 'read', filePath: '/r', offset: 3 }), { name: 'Read', input: { file_path: '/r', offset: 3 } })
})

test("Grok's own tools, named in its ACP input (variant), as the apps' tools, and their results as text", () => {
  const call = (name, input, output, status = 'completed') => ({ type: 'tool_call', callId: 'c1', name, status, error: null, detail: { type: 'unknown', input, output } })
  const drawn = (item) => {
    const [use, result] = timelineItems([entry(1, item, 2)], { maxSeq: 2 }).items
    return [use.tool_name, use.tool_input, result?.text]
  }
  assert.deepEqual(drawn(call('other', { variant: 'ListDir', target_directory: '/w/app' },
    { type: 'ListDir', Content: { content: '- /w/app/\n  - main.go', absolute_root_path: '/w/app' } })), ['LS', { path: '/w/app' }, '- /w/app/\n  - main.go'])
  assert.equal(drawn(call('List `/w/app`', { variant: 'ListDir', target_directory: '/w/app' }, null))[0], 'LS', 'named after the ACP title: still the variant')
  assert.deepEqual(drawn(call('other', { variant: 'Monitor', command: 'bash watch.sh', description: 'Watch the deploy', timeout_ms: 600000, persistent: false },
    { type: 'Monitor', taskId: 't1', timeoutMs: 600000, persistent: false })),
  ['Monitor', { command: 'bash watch.sh', description: 'Watch the deploy', timeout_ms: 600000, persistent: false }, 'taskId: t1\ntimeoutMs: 600000\npersistent: false'])
  assert.deepEqual(drawn(call('other', { variant: 'TaskOutput', task_ids: ['t1'], timeout_ms: null },
    { type: 'TaskOutput', Result: { task_id: 't1', command: '[monitor] Watch the deploy', status: 'failed', exit_code: 1, output: 'FAILED: connection closed' } })),
  ['TaskOutput', { task_id: 't1' }, 'FAILED: connection closed\n（退出码 1）'])
  assert.equal(drawn(call('other', { variant: 'TaskOutput', task_ids: ['t2'] }, null, 'canceled'))[2], '（已取消）')
  assert.deepEqual(drawn(call('other', { variant: 'KillTask', task_id: 't1' },
    { type: 'KillTask', Result: { task_id: 't1', outcome: 'already_exited', message: 'Task had already completed' } })),
  ['TaskStop', { task_id: 't1' }, 'Task had already completed'])
  assert.deepEqual(drawn(call('paseo__list_schedules', { variant: 'UseTool', tool_name: 'paseo__list_schedules', tool_input: { limit: 5 } },
    { type: 'MCP', tool_name: 'list_schedules', server_name: 'paseo', output: { OkayOutput: 'schedules_count=0' } })),
  ['mcp__paseo__list_schedules', { limit: 5 }, 'schedules_count=0'])
  // Any other one under its own name; a result with no text of its own, its fields.
  assert.deepEqual(drawn(call('other', { variant: 'Sleep', seconds: 5 }, { type: 'Sleep', Result: { slept: 5 } })), ['Sleep', { seconds: 5 }, 'slept: 5'])
  // Its permission card says the tool and what it is about.
  const card = interactionOf('s1', { id: 'p1', provider: 'grok', name: 'other', kind: 'tool', title: 'List directory', input: { variant: 'ListDir', target_directory: '/w/app' } })
  assert.deepEqual([card.tool_name, card.tool_input, card.summary, card.reason], ['LS', { path: '/w/app' }, '/w/app', 'List directory'])
})

test("a request for an MCP tool, whichever provider asks: Claude Code's name, an ACP call, OpenCode's name, Codex's approval (#8141)", () => {
  const own = 'mcp__persona__report_to_supervisor'
  // Claude Code names it.
  assert.equal(asksForTool({ provider: 'claude', name: own, kind: 'tool' }, own), true)
  assert.equal(asksForTool({ provider: 'claude', name: 'mcp__persona__phone_notify', kind: 'tool' }, own), false)
  // Grok over ACP: Paseo names the request after the ACP kind; its input (in the detail Paseo maps) names the tool.
  const grok = (tool, at = 'detail') => {
    const input = { variant: 'UseTool', tool_name: tool, tool_input: { kind: 'done', text: 'x' } }
    return { provider: 'acp', name: 'other', kind: 'tool', title: 'Use tool', ...(at === 'detail' ? { detail: { type: 'unknown', input, output: null } } : { input }) }
  }
  assert.equal(asksForTool(grok('persona__report_to_supervisor'), own), true)
  assert.equal(asksForTool(grok('persona__report_to_supervisor', 'input'), own), true)
  assert.equal(asksForTool(grok('persona__phone_notify'), own), false, 'another tool of the same server')
  assert.equal(asksForTool(grok('other__report_to_supervisor'), own), false, "another server's tool of the same name")
  assert.equal(asksForTool({ provider: 'acp', name: 'execute', kind: 'tool', detail: { type: 'shell', command: 'ls' } }, own), false)
  // OpenCode's name for an MCP tool is <server>_<tool>, its permission key.
  assert.equal(asksForTool({ provider: 'opencode', name: 'persona_report_to_supervisor', kind: 'tool' }, own), true)
  assert.equal(asksForTool({ provider: 'opencode', name: 'persona_phone_notify', kind: 'tool' }, own), false)
  assert.equal(asksForTool({ provider: 'opencode', name: 'bash', kind: 'tool' }, own), false)
  // Codex asks for an MCP tool as an approval of its server, the tool named in the message.
  const codex = (server, message) => ({ provider: 'codex', name: 'CodexMcpElicitation', kind: 'tool', title: `MCP approval: ${server}`,
    description: message, input: { mode: 'form', requestedSchema: null, url: null }, metadata: { threadId: 't1', turnId: null, serverName: server, elicitationId: null } })
  assert.equal(asksForTool(codex('persona', 'Allow the persona MCP server to run tool "report_to_supervisor"?'), own), true)
  assert.equal(asksForTool(codex('persona', 'Allow the persona MCP server to run tool "phone_notify"?'), own), false)
  assert.equal(asksForTool(codex('other', 'Allow the other MCP server to run tool "report_to_supervisor"?'), own), false)
  assert.equal(asksForTool(codex('persona', 'Something else entirely'), own), false, 'a message that names no tool')
  assert.equal(asksForTool({ provider: 'codex', name: 'CodexBash', kind: 'tool', metadata: { serverName: 'persona' },
    description: 'Allow the persona MCP server to run tool "report_to_supervisor"?' }, own), false, "Codex's other requests")
  // A name that is no MCP tool's matches nothing.
  assert.equal(asksForTool({ provider: 'claude', name: 'Bash', kind: 'tool' }, 'Bash'), false)
})

test("the bridge's own words are the Chinese the hub rewrites in the reader's language; a compaction is the node's divider (#8133)", () => {
  // The hub matches them word for word (server/internal/transcript's Runtime* constants): change both sides together.
  assert.deepEqual([ERROR_TEXT, ERROR_LEAD, CANCELED_NOTE, exitNote(2)], ['出错了', '出错了：', '（已取消）', '（退出码 2）'])
  const items = timelineItems([
    entry(1, { type: 'error', message: 'Provider crashed' }),
    entry(2, { type: 'error', message: '' }),
    entry(3, { type: 'compaction', status: 'completed', trigger: 'auto', preTokens: 180000 }),
    entry(4, { type: 'compaction', status: 'completed' }),
  ], { maxSeq: 4 }).items
  assert.deepEqual(items.map((i) => [i.kind, i.text, i.compact]), [
    ['notice', '出错了：Provider crashed', undefined], ['notice', '出错了', undefined],
    ['notice', '上下文已压缩', { trigger: 'auto', pre_tokens: 180000 }], ['notice', '上下文已压缩', {}],
  ], "an error with nothing said is the bare word; a compaction is the node's notice, a divider the apps word themselves")
  const roots = providerRoots(CONFIG)
  assert.equal(agentRow(agentSnapshot({ provider: 'grok', status: 'error', lastError: 'boom' }), { roots }).last_text, '出错了：boom')
})

test("what another agent hands this one is a message from another session, a subagent's task note or a notice, as from a transcript (#8220)", () => {
  // The node's words for the same lines read from a transcript (server/internal/transcript/paseo.go): change both sides together.
  assert.deepEqual([FROM_SESSION, AGENT_NOTICE, SCHEDULE_NOTICE], ['另一个会话',
    { completed: '子 Agent 完成', failed: '子 Agent 失败', stopped: '子 Agent 已停止', waiting: '子 Agent 等待批准' }, '定时任务到点'])
  const child = '00000000-0000-4000-8000-00000000c41d'
  // Paseo's projection of an envelope (agent-messages/envelope.ts projectAgentMessage), and of a bare one (`heartbeat`).
  const said = (callId, event, text, sender = { id: child, title: 'Ticket 01' }) => ({ type: 'tool_call', callId, name: 'agent_message', status: 'completed',
    error: null, agentMessage: { event, sender, text }, detail: { type: 'plain_text', text, icon: 'bot' } })
  const bare = (text) => ({ type: 'tool_call', callId: 'paseo-system:abc', name: 'heartbeat', status: 'completed', error: null,
    detail: { type: 'plain_text', text, icon: 'sparkles' } })
  const request = JSON.stringify({ agentId: child, requestId: 'permission-1', request: { id: 'permission-1', name: 'Bash', kind: 'tool',
    input: { command: 'make smoke', description: 'Run the smoke test' } } }, null, 2)
  const items = timelineItems([
    entry(1, said('paseo-agent-message:m1', 'message', 'Ticket 01 is done, please review.')),
    entry(2, said('paseo-agent-message:m2', 'message', 'Found it.', { id: child })),
    entry(3, said('paseo-agent-message:n1', 'finished', `Agent ${child} (Ticket 01) finished.\n\nServer: srv_test\n\n<agent-response>\nAll checks pass.\n` +
      '[truncated 99 chars; use get_agent_activity for the full response]\n</agent-response>')),
    entry(4, said('paseo-agent-message:n2', 'permission-required', `Agent ${child} (Ticket 01) needs permission.\n\nServer: srv_test\n\n` +
      'Respond with `respond_to_permission` using the `agentId` and `requestId` below.\n\n<permission-request>\n' + request + '\n</permission-request>')),
    entry(5, said('paseo-agent-message:n3', 'errored', `Agent ${child} (${child}) was canceled.`, { id: child, title: child })),
    entry(6, bare('Schedule "nightly review" fired (id=s1, run=r1).\nReview the day.')),
    entry(7, bare(`Agent ${child} (Ticket 02) errored.\n\n<agent-response>\nNo database.\n</agent-response>`)),
    entry(8, bare('Something Paseo has not sent yet.\nMore of it.')),
    entry(9, said('paseo-agent-message:m4', 'message', '  ')),
  ], { maxSeq: 9 }).items
  assert.deepEqual(items.map((i) => [i.id, i.role, i.kind, i.text ?? i.tool_name, i.from, i.task]), [
    ['1#0', 'user', 'text', 'Ticket 01 is done, please review.', 'Ticket 01', undefined],
    ['2#0', 'user', 'text', 'Found it.', '另一个会话', undefined],
    ['3#0', 'system', 'notice', '子 Agent 完成 · Ticket 01', undefined, { kind: 'agent', status: 'completed', title: 'Ticket 01', detail: 'All checks pass.…' }],
    ['4#0', 'system', 'notice', '子 Agent 等待批准 · Ticket 01', undefined, { kind: 'agent', status: 'waiting', title: 'Ticket 01', detail: 'Bash: Run the smoke test' }],
    ['5#0', 'system', 'notice', '子 Agent 已停止', undefined, { kind: 'agent', status: 'stopped', title: '' }],
    ['6#0', 'system', 'notice', '定时任务到点：nightly review', undefined, undefined],
    ['7#0', 'system', 'notice', '子 Agent 失败 · Ticket 02', undefined, { kind: 'agent', status: 'failed', title: 'Ticket 02', detail: 'No database.' }],
    ['8#0', 'system', 'notice', 'Something Paseo has not sent yet.', undefined, undefined], // a bare envelope of another kind
  ], 'an empty message shows nothing')
  assert.ok(items.every((i) => !i.wake && !i.queued), 'the timeline does not say whether one started a run')
})

test('botmux sentinel entries preserve the timeline preview and original items', () => {
  const marker = 'BOTMUX_NOTHING_TO_SEND'
  const mixed = 'Notes\n' + marker + '\nready'
  const entries = [entry(1, { type: 'assistant_message', text: mixed })]
  const row = (timeline) => agentRow(agentSnapshot({ provider: 'grok' }), { roots: providerRoots(CONFIG), lastText: timeline.lastText })
  assert.equal(row(timelineItems(entries)).last_text, 'Notes ready')
  entries.push(entry(2, { type: 'assistant_message', text: marker }))
  const timeline = timelineItems(entries)
  assert.equal(row(timeline).last_text, 'Notes ready')
  assert.deepEqual(timeline.items.map(i => i.text), [mixed, marker])
  assert.equal(row(timelineItems(entries.slice(1))).last_text, undefined)
})

test('a timeline as items: results where they finished, the text being written held back, echoes tagged', () => {
  const entries = [
    entry(1, { type: 'user_message', text: '看看测试', messageId: 'm1' }),
    entry(2, { type: 'reasoning', text: '先跑一下' }, 3),
    entry(4, { type: 'tool_call', callId: 'call_1', name: 'shell', status: 'completed', error: null, detail: { type: 'shell', command: 'npm test', output: 'ok', exitCode: 1 } }, 9),
    entry(5, { type: 'tool_call', callId: 'call_2', name: 'read', status: 'running', error: null, detail: { type: 'read', filePath: '/a' } }, 6),
    entry(7, { type: 'assistant_message', text: '测试过了' }, 8),
    entry(10, { type: 'assistant_message', text: '还在写' }, 12),
  ]
  const done = timelineItems(entries, { maxSeq: 12, running: false, echoes: new Map([['m1', 'cm-1']]) })
  assert.deepEqual(done.items.map((i) => [i.id, i.kind, i.text ?? i.tool_name]), [
    ['1#0', 'text', '看看测试'], ['2#0', 'thinking', '先跑一下'], ['4#0', 'tool_use', 'Bash'], ['5#0', 'tool_use', 'Read'],
    ['7#0', 'text', '测试过了'], ['9#0', 'tool_result', 'ok\n（退出码 1）'], ['10#0', 'text', '还在写'],
  ])
  assert.equal(done.items[0].client_msg_id, 'cm-1')
  assert.equal(done.items.find((i) => i.kind === 'tool_result').is_error, true)
  assert.equal(done.toSeq, 12)
  assert.equal(done.safeSeq, 4, 'a running tool can still finish: read again from before it')
  assert.equal(done.lastText, '还在写')

  const live = timelineItems(entries, { maxSeq: 12, running: true })
  assert.equal(live.toSeq, 9, 'the text being written is not final: it goes live, not as an item')
  assert.equal(live.items.at(-1).id, '9#0')
  assert.equal(live.safeSeq, 4)
  const quick = timelineItems([entry(3, { type: 'tool_call', callId: 'c', name: 'shell', status: 'failed', error: 'denied', detail: { type: 'shell', command: 'rm' } })], { maxSeq: 3 })
  assert.deepEqual(quick.items.map((i) => i.id), ['3#0', '3#1'], 'a call and its result in one row')
  assert.equal(quick.items[1].text, 'denied')
})

test('requests as cards: a question, a plan, a permission with what "always" adds', () => {
  const q = interactionOf('s1', { id: 'permission-1', provider: 'claude', name: 'AskUserQuestion', kind: 'question', metadata: { toolUseId: 'toolu_1' },
    input: { questions: [{ question: '用哪个库？', header: '库', multiSelect: false, options: [{ label: 'A', description: '快' }, 'B'] }] } })
  assert.equal(q.kind, 'question')
  assert.equal(q.tool_use_id, 'toolu_1')
  assert.match(q.id, /^paseo-[0-9a-f]{16}$/)
  assert.deepEqual(q.questions, [{ question: '用哪个库？', header: '库', multi_select: false, options: [{ label: 'A', description: '快' }, { label: 'B', description: '' }] }])
  const plan = interactionOf('s1', { id: 'permission-2', provider: 'claude', name: 'ExitPlanMode', kind: 'plan', input: { plan: 'x' }, metadata: { planText: '## 计划' } })
  assert.deepEqual([plan.kind, plan.plan], ['plan', '## 计划'])
  const perm = interactionOf('s1', { id: 'permission-3', provider: 'claude', name: 'Bash', kind: 'tool', input: { command: 'npm test' },
    suggestions: [{ type: 'addRules', rules: [{ toolName: 'Bash', ruleContent: 'npm test' }], behavior: 'allow', destination: 'localSettings' }] })
  assert.deepEqual([perm.kind, perm.tool_name, perm.tool_input.command, perm.summary, perm.always_label], ['permission', 'Bash', 'npm test', 'npm test', '这个项目不再问：Bash(npm test)'])
  const codex = interactionOf('s2', { id: 'p4', provider: 'codex', name: 'exec', kind: 'tool', title: 'Run a command', detail: { type: 'shell', command: 'make' } })
  assert.deepEqual([codex.tool_name, codex.tool_input.command, codex.summary, codex.reason, codex.always_label], ['Bash', 'make', 'make', 'Run a command', undefined],
    'the card says what will run; the generic title is the reason')
  const why = interactionOf('s2', { id: 'p5', provider: 'codex', name: 'exec', kind: 'tool', title: 'Run a command', description: 'build the app', detail: { type: 'shell', command: 'make' } })
  assert.deepEqual([why.summary, why.reason], ['make', 'build the app'])
  const bare = interactionOf('s2', { id: 'p6', provider: 'codex', name: 'mcp_thing', kind: 'tool', title: 'Use the thing', description: 'needs network' })
  assert.deepEqual([bare.summary, bare.reason], ['Use the thing', 'needs network'], 'nothing in the input: the title is the summary')
  assert.notEqual(interactionOf('s1', { id: 'permission-1', kind: 'tool' }).id, interactionOf('s2', { id: 'permission-1', kind: 'tool' }).id)
})

test("the phone's answers as Paseo's responses", () => {
  const question = { id: 'p', kind: 'question', input: { questions: [{ question: '用哪个库？', header: '库', options: [] }] } }
  assert.deepEqual(responseOf(question, { answers: { '用哪个库？': 'A' } }, 'claude'), { response: { behavior: 'allow', updatedInput: { answers: { '用哪个库？': 'A' } } } })
  assert.deepEqual(responseOf(question, { answers: { '用哪个库？': 'A' } }, 'codex'), { response: { behavior: 'allow', updatedInput: { answers: { 库: 'A' } } } }, "others take Paseo's card's key")
  // Paseo's implement_resume goes back to the mode the agent had before it planned (its label names it), whatever was
  // chosen on the phone (#8128): taken only when that is the phone's mode; otherwise implement, then the phone's mode.
  const modes = [{ id: 'default', label: 'Always Ask' }, { id: 'acceptEdits', label: 'Accept File Edits' }, { id: 'plan', label: 'Plan Mode' },
    { id: 'auto', label: 'Auto mode' }, { id: 'bypassPermissions', label: 'Bypass' }]
  const plan = { id: 'p', kind: 'plan', actions: [{ id: 'reject' }, { id: 'implement' }, { id: 'implement_resume', label: 'Implement with Auto mode' }] }
  assert.equal(modeBeforePlan(plan, modes), 'auto')
  assert.deepEqual(responseOf(plan, { approve: true, mode: 'auto' }, 'claude', modes), { response: { behavior: 'allow', selectedActionId: 'implement_resume' } })
  assert.deepEqual(responseOf(plan, { approve: true, mode: 'bypassPermissions' }, 'claude', modes),
    { response: { behavior: 'allow', selectedActionId: 'implement' }, setMode: 'bypassPermissions' }, 'auto before planning, Bypass chosen: not resumed into auto')
  const bypassed = { ...plan, actions: [{ id: 'reject' }, { id: 'implement' }, { id: 'implement_resume', label: 'Implement with Bypass' }] }
  assert.deepEqual(responseOf(bypassed, { approve: true, mode: 'bypassPermissions' }, 'claude', modes), { response: { behavior: 'allow', selectedActionId: 'implement_resume' } })
  assert.deepEqual(responseOf(bypassed, { approve: true, mode: 'auto' }, 'claude', modes),
    { response: { behavior: 'allow', selectedActionId: 'implement' }, setMode: 'auto' })
  assert.deepEqual(responseOf({ ...plan, actions: [{ id: 'reject' }, { id: 'implement' }, { id: 'implement_resume' }] }, { approve: true, mode: 'auto' }, 'claude', modes),
    { response: { behavior: 'allow', selectedActionId: 'implement' }, setMode: 'auto' }, 'a mode before planning the request does not name: implement, then switch')
  assert.deepEqual(responseOf(plan, { approve: true, mode: 'auto' }, 'claude'),
    { response: { behavior: 'allow', selectedActionId: 'implement' }, setMode: 'auto' }, 'no modes to read the label with: the same')
  assert.deepEqual(responseOf(plan, { approve: true, mode: 'acceptEdits' }, 'claude', modes), { response: { behavior: 'allow', selectedActionId: 'implement' } })
  assert.deepEqual(responseOf({ ...plan, actions: [{ id: 'reject' }, { id: 'implement' }] }, { approve: true, mode: 'bypassPermissions' }, 'claude'),
    { response: { behavior: 'allow', selectedActionId: 'implement' }, setMode: 'bypassPermissions' })
  assert.deepEqual(responseOf(plan, { approve: false, feedback: '先写测试' }, 'claude'),
    { response: { behavior: 'deny', message: '用户在手机上打回了这个计划。修改意见：先写测试', selectedActionId: 'reject' } })
  const tool = { id: 'p', kind: 'tool', suggestions: [{ type: 'addRules', rules: [{ toolName: 'Bash' }], behavior: 'allow', destination: 'session' }, { type: 'setMode', mode: 'x' }] }
  assert.deepEqual(responseOf(tool, { allow: true, always: true }), { response: { behavior: 'allow', updatedPermissions: [tool.suggestions[0]] } })
  assert.deepEqual(responseOf(tool, { allow: true }), { response: { behavior: 'allow' } })
  assert.deepEqual(responseOf(tool, { allow: false, message: '不要' }), { response: { behavior: 'deny', message: '不要' } })
  assert.equal(responseOf(tool, { dismiss: true }), undefined, "handed back: Paseo's app answers it")
})

test("Paseo's workspaces as the node's rows: kind, branch, the space its agents name, the repository, a worktree Paseo made", () => {
  const main = workspaceDescriptor({ id: 'wks_00000000000000c1', workspaceDirectory: '/w/app', projectId: 'prj_app', projectDisplayName: 'app' })
  assert.deepEqual(workspaceRow(main), { workspace_id: main.id, title: 'main', cwd: '/w/app', kind: 'checkout', branch: 'main', space_id: 'prj_app', space_name: 'app', repo_root: '/w/app' },
    "its placement's project is the space, as its agents' rows have it")
  const tree = workspaceDescriptor({ id: 'wks_00000000000000c2', workspaceDirectory: '/w/trees/fix', workspaceKind: 'worktree', name: 'Fix', worktreeSlug: 'fix',
    gitRuntime: { currentBranch: 'fix/login' }, project: { projectKey: 'prj_stray', projectName: 'fix', checkout: { cwd: '/w/trees/fix', isGit: true, worktreeRoot: '/w/trees/fix', mainRepoRoot: '/w/app', isPaseoOwnedWorktree: true } } })
  assert.deepEqual(workspaceRow(tree, { spaceId: 'prj_app', spaceName: 'app', canon: (p) => `/real${p}` }),
    { workspace_id: tree.id, title: 'Fix', cwd: '/w/trees/fix', kind: 'worktree', branch: 'fix/login', space_id: 'prj_app', space_name: 'app', repo_root: '/real/w/app', owned: true },
    "a stray record's worktree in its repository's space; the main checkout physical")
  const answer = { ...tree, project: undefined, projectId: 'prj_app', projectDisplayName: 'app', projectRootPath: '/w/app' }
  assert.deepEqual([workspaceRow(answer).space_id, workspaceRow(answer).repo_root, workspaceRow(answer).owned], ['prj_app', '/w/app', true],
    "Paseo's answer to workspace.create has no placement: its project and root")
  const folder = workspaceDescriptor({ id: 'wks_00000000000000c3', workspaceDirectory: '/w/notes', workspaceKind: 'directory', name: 'notes' })
  assert.deepEqual(workspaceRow(folder), { workspace_id: folder.id, title: 'notes', cwd: '/w/notes', kind: 'folder', space_id: 'prj_1', space_name: 'proj' })
  assert.equal(workspaceRow({ ...main, archivingAt: '2026-11-12T00:00:00Z' }), undefined, 'being archived')
  assert.equal(workspaceRow({ ...main, id: '../x' }), undefined)
  // Paseo's utils/worktree.js: sha256 of the main checkout, its first 8 bytes in base 36, 8 characters.
  assert.match(paseoCheckouts('/home/u/.paseo/worktrees', '/w/app'), /^\/home\/u\/\.paseo\/worktrees\/[0-9a-z]{8}$/)
  assert.equal(paseoCheckouts('/b', '/w/app'), paseoCheckouts('/b', '/w/app'))
  assert.notEqual(paseoCheckouts('/b', '/w/app'), paseoCheckouts('/b', '/w/app2'))
  const row = agentRow(agentSnapshot({ id: 'a1', provider: 'grok', workspaceId: 'wks_00000000000000c2', title: 'x' }), { roots: {} })
  assert.equal(row.workspace_id, 'wks_00000000000000c2', "an agent's row names its workspace")
})

test("native workspace pins carry a timestamp, independently of project and directory; old or unpinned workspaces omit it", () => {
  const descriptor = workspaceDescriptor({ id: 'wks_pin', workspaceDirectory: '/w/app', pinnedAt: '2026-11-16T15:00:00+02:00' })
  assert.equal(workspaceRow(descriptor).pinned_at, '2026-11-16T13:00:00.000Z')
  const sibling = { ...descriptor, id: 'wks_sibling', pinnedAt: null }
  assert.equal(workspaceRow(sibling).pinned_at, undefined, 'same folder and project are not the same workspace')
  for (const pinnedAt of [undefined, null, '', 'invalid', '2026-11-16', 1, true]) {
    assert.equal(workspaceRow({ ...descriptor, pinnedAt }).pinned_at, undefined)
  }
})

test('a retained archived conversation no longer belongs to a natively pinned workspace until resumed', () => {
  const agent = agentSnapshot({ provider: 'my-codex', workspaceId: 'wks_pinned', labels: { 'persona.session': 'saved-chat' }, archivedAt: '2026-11-16T12:00:00Z' })
  const history = agentRow(agent, { archived: true })
  assert.equal(history.session_id, 'saved-chat')
  assert.equal(history.status, 'offline')
  assert.equal(history.workspace_id, undefined)
  assert.equal(agentRow({ ...agent, archivedAt: null }).workspace_id, 'wks_pinned')
})
