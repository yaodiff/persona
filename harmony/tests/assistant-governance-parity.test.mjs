import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

const stubs = new Map([
  ['gov-stub:transport', `
    export class EndpointConfig {
      constructor(baseUrl, hubId, token, pin, managerId = '') {
        Object.assign(this, { baseUrl, hubId, token, pin, managerId });
      }
    }
    export class HttpFailure extends Error {
      constructor(status, code, resultUnknown = false, resultPending = false) {
        super(code || 'request_failed'); this.status = status; this.code = code;
        this.resultUnknown = resultUnknown; this.resultPending = resultPending;
      }
    }
    export class HubTransport {
      constructor(config) { this.config = config; }
      async request() { throw new Error('transport_must_not_be_used'); }
      async requestBytes() { throw new Error('transport_must_not_be_used'); }
      send() { throw new Error('transport_must_not_be_used'); }
      close() {}
    }
  `],
  ['gov-stub:local-store', 'export class LocalStore {}'],
  ['gov-stub:mutation-store', 'export class MutationRecord {} export class MutationStore { static shared() { throw new Error("demo_mutation_store_must_not_be_used"); } }'],
  ['gov-stub:arkts', 'export const util = { generateRandomUUID: () => crypto.randomUUID(), TextDecoder: { create: () => ({ decodeToString: () => "" }) } };']
]);

registerHooks({
  resolve(specifier, context, next) {
    if (stubs.has(specifier)) return { url: specifier, shortCircuit: true };
    if (specifier === '../platform/HubTransport') return { url: 'gov-stub:transport', shortCircuit: true };
    if (specifier === '../platform/LocalStore') return { url: 'gov-stub:local-store', shortCircuit: true };
    if (specifier === './MutationStore') return { url: 'gov-stub:mutation-store', shortCircuit: true };
    if (specifier === '@kit.ArkTS') return { url: 'gov-stub:arkts', shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      return { url: new URL(specifier.endsWith('.ets') ? specifier : specifier + '.ets', context.parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (stubs.has(url)) return { format: 'module', source: stubs.get(url), shortCircuit: true };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const {
  alwaysGrantsPath,
  filterOutRevokedStandingGrants,
  normalizeStandingSource,
  parseAlwaysGrantsResponse,
  parseAlwaysRevokeResponse,
  parseStandingAuthorizationsResponse,
  parseSuggestedRuleCount,
  revokeAlwaysGrantPath,
  revokeStandingAuthorizationPath,
  standingActionLabelKey,
  standingAuthorizationsPath,
  standingConditionLabelKey,
  standingFolderName
} = await import('../entry/src/main/ets/core/StandingAuth.ts');

const {
  AssistantSearchDebouncer,
  assistantSearchLead,
  assistantSearchPath,
  assistantSearchSnippetParts,
  localAgentSearchMatches,
  localAssistantChatMatches,
  mergeAssistantSearchRows,
  parseAssistantSearchAnswer
} = await import('../entry/src/main/ets/core/AssistantSearch.ts');

const {
  AUTO_CONVERSATION_SOURCES,
  filterAutoConversations,
  partitionAutoConversations,
  sourceOfAutoConversation
} = await import('../entry/src/main/ets/core/AutoConversations.ts');

const {
  autoRenameSessionKey,
  autoRenameSessionPath,
  canAutoRenameSession,
  pendingAutoRenameTitle,
  shouldApplyAutoRenameTitle
} = await import('../entry/src/main/ets/core/SessionNaming.ts');

const { buildQuickCommandGroups } = await import('../entry/src/main/ets/core/QuickCommandPalette.ts');
const { DemoRepository } = await import('../entry/src/main/ets/models/DemoRepository.ets');

const readEts = (rel) => readFileSync(new URL('../entry/src/main/ets/' + rel, import.meta.url), 'utf8');
const readStrings = (locale) => JSON.parse(readFileSync(new URL('../entry/src/main/resources/' + locale + '/element/string.json', import.meta.url), 'utf8'));

test('StandingAuth parses grants, formats paths/sources, and filters revoking/revoked entries', () => {
  const parsed = parseStandingAuthorizationsResponse({
    revision: 7,
    authorizations: [
      { id: '', machine_id: 'mac', directory: '/ignored', actions: ['merge'] },
      {
        id: 'sa_1',
        machine_id: 'mac-1',
        directory: '/workspace/weather',
        actions: ['merge', 'push'],
        conditions: ['ci_green', 'no_conflicts'],
        quote: 'Go ahead when green.',
        source: 'Main conversation',
        granted_at: '2026-11-20T09:00:00Z',
        reversible: true
      },
      { id: 'sa_1', machine_id: 'dup' }
    ]
  });
  assert.equal(parsed.revision, 7);
  assert.equal(parsed.authorizations.length, 1);
  assert.equal(standingFolderName(parsed.authorizations[0].directory), 'weather');
  assert.equal(standingActionLabelKey('merge'), 'standing_action_merge');
  assert.equal(standingActionLabelKey('custom'), '');
  assert.equal(standingConditionLabelKey('ci_green'), 'standing_condition_ci_green');
  assert.equal(standingConditionLabelKey('owner_note'), '');

  assert.equal(parseSuggestedRuleCount('Suggested rule: the same answer 4 times in 14 days, confirmed with Always.'), 4);
  assert.equal(parseSuggestedRuleCount('建议的规则：14 天内 5 次相同的回答，点「总是」确认。'), 5);
  assert.equal(parseSuggestedRuleCount('提案されたルール：14 日間に同じ回答が 6 回あり、「常に」で確認されました。'), 6);
  assert.equal(normalizeStandingSource('主对话').kind, 'main_chat');
  assert.equal(normalizeStandingSource('Settings').kind, 'settings');

  assert.equal(standingAuthorizationsPath(), '/api/v1/assistant/standing-authorizations');
  assert.equal(revokeStandingAuthorizationPath('sa/1'), '/api/v1/assistant/standing-authorizations/sa%2F1');
  assert.equal(alwaysGrantsPath('conv/1'), '/api/v1/assistant/conversations/conv%2F1/always');
  assert.equal(revokeAlwaysGrantPath('conv/1', 'hash#1'), '/api/v1/assistant/conversations/conv%2F1/always/hash%231');

  const always = parseAlwaysGrantsResponse({
    grants: [
      { key_hash: '', tool_name: 'Bad' },
      { key_hash: 'abc123', tool_name: 'Bash', created_at: '2026-11-27T08:00:00Z' }
    ]
  });
  assert.equal(always.length, 1);
  assert.equal(always[0].tool_name, 'Bash');
  assert.deepEqual(parseAlwaysRevokeResponse({ ok: true, restart_required: true }), { ok: true, restart_required: true });
  assert.deepEqual(parseAlwaysRevokeResponse({ ok: true }), { ok: true, restart_required: false });

  const remaining = filterOutRevokedStandingGrants(
    [{ id: 'a' }, { id: 'b' }, { id: 'c' }],
    ['a'],
    ['c']
  );
  assert.deepEqual(remaining.map((g) => g.id), ['b']);
});

test('AssistantSearch merges remote hits over local matches, clips UTF-16 snippets, debounces, and falls back on 404', async () => {
  const known = [
    { id: 'c1', session_id: 'c1', machine_id: 'm1', title: 'Alpha', last_text: 'local hit', updated_at: '2026-11-27T08:00:00Z' },
    { id: 'c2', session_id: 'c2', machine_id: 'm1', title: 'Beta', last_text: 'other', updated_at: '2026-11-27T09:00:00Z' },
    { id: 'c3', session_id: 'c3', machine_id: 'm1', title: 'Archived', archived: true, updated_at: '2026-11-26T09:00:00Z' }
  ];
  const local = localAssistantChatMatches(known, 'alpha');
  assert.deepEqual(local.map((s) => s.id), ['c1']);

  const answer = parseAssistantSearchAnswer({
    partial: true,
    results: [
      {
        session_id: 'c2',
        machine_id: 'm1',
        title: 'Beta',
        title_match: false,
        hits: [{ item_id: 'item-9', seq: 9, role: 'assistant', snippet: 'See the alpha checklist', ranges: [[8, 13]] }]
      },
      {
        session_id: 'c1',
        machine_id: 'm1',
        title: 'Alpha',
        title_match: true,
        hits: [{ item_id: 'item-1', seq: 1, role: 'user', snippet: 'ignored because title_match', ranges: [[0, 7]] }]
      },
      {
        session_id: 'unknown',
        machine_id: 'm1',
        title: 'Unknown',
        hits: []
      }
    ]
  });
  const merged = mergeAssistantSearchRows(local, answer, known);
  assert.equal(merged.length, 2);
  assert.equal(merged[0].session.id, 'c2');
  assert.equal(merged[0].hit?.item_id, 'item-9');
  assert.equal(merged[1].session.id, 'c1');
  assert.equal(merged[1].hit, undefined);

  assert.equal(assistantSearchPath('hello world'), '/api/v1/assistant/search?q=hello%20world&limit=30');
  assert.equal(assistantSearchLead(1), 8);
  assert.equal(assistantSearchLead(1.2), 5);
  assert.equal(assistantSearchLead(1.5), 2);
  const parts = assistantSearchSnippetParts('0123456789012 alpha tail', [[14, 19]], 1);
  assert.ok(parts.some((p) => p.matched && p.text === 'alpha'));

  const agents = localAgentSearchMatches([
    { id: 'a1', session_id: 'a1', machine_id: 'm1', title: 'Build fix', branch: 'main', assistant: false, updated_at: '2026-11-27T08:00:00Z' },
    { id: 'a2', session_id: 'a2', machine_id: 'm1', title: 'Subagent', is_subagent: true, assistant: false },
    { id: 'a3', session_id: 'a3', machine_id: 'm1', title: 'Assistant row', assistant: true },
    { id: 'a4', session_id: 'a4', machine_id: 'm1', title: 'Old build', archived: true }
  ], 'build');
  assert.deepEqual(agents.map((a) => a.id), ['a1']);

  const debouncer = new AssistantSearchDebouncer(15);
  let calls = 0;
  let lastResult = null;
  debouncer.schedule({
    query: 'first',
    scope: 'chats',
    connected: true,
    supportsRemote: true,
    hubKey: 'hub1|m',
    fetcher: async () => { calls++; return { results: [], partial: false }; },
    onResult: (q, ans) => { lastResult = { q, ans }; }
  });
  debouncer.schedule({
    query: 'second',
    scope: 'chats',
    connected: true,
    supportsRemote: true,
    hubKey: 'hub1|m',
    fetcher: async () => { calls++; return { results: [], partial: false }; },
    onResult: (q, ans) => { lastResult = { q, ans }; }
  });
  await new Promise((r) => setTimeout(r, 40));
  assert.equal(calls, 1);
  assert.equal(lastResult?.q, 'second');

  // Sequence-guarded stale in-flight response cancellation:
  let resolveInflight = null;
  const inflightPromise = new Promise((resolve) => { resolveInflight = resolve; });
  debouncer.schedule({
    query: 'slow',
    scope: 'chats',
    connected: true,
    supportsRemote: true,
    hubKey: 'hub2|m',
    fetcher: async () => inflightPromise,
    onResult: (q, ans) => { lastResult = { q, ans }; }
  });
  await new Promise((r) => setTimeout(r, 25));
  debouncer.schedule({
    query: 'fast',
    scope: 'chats',
    connected: true,
    supportsRemote: true,
    hubKey: 'hub2|m',
    fetcher: async () => ({ results: [{ session_id: 'c1', machine_id: 'm1', title: 'Alpha', hits: [] }], partial: false }),
    onResult: (q, ans) => { lastResult = { q, ans }; }
  });
  await new Promise((r) => setTimeout(r, 40));
  assert.equal(lastResult?.q, 'fast');
  resolveInflight({ results: [], partial: true });
  await Promise.resolve();
  assert.equal(lastResult?.q, 'fast');

  // 404 marks hub unsupported and falls back cleanly.
  debouncer.schedule({
    query: 'third',
    scope: 'chats',
    connected: true,
    supportsRemote: true,
    hubKey: 'hub404|m',
    fetcher: async () => { const err = new Error('not_found'); err.status = 404; throw err; },
    onResult: (q, ans, failed) => { lastResult = { q, ans, failed }; }
  });
  await new Promise((r) => setTimeout(r, 40));
  assert.equal(debouncer.isUnsupported('hub404|m'), true);
  assert.equal(lastResult?.failed, false);
  const scheduledAfter404 = debouncer.schedule({
    query: 'fourth',
    scope: 'chats',
    connected: true,
    supportsRemote: true,
    hubKey: 'hub404|m',
    fetcher: async () => { throw new Error('must_not_call'); },
    onResult: () => {}
  });
  assert.equal(scheduledAfter404, false);
  debouncer.cancel();
});

test('AutoConversations partitions Mine, Automatic, and per-source groups newest-first', () => {
  assert.deepEqual([...AUTO_CONVERSATION_SOURCES], ['inbox', 'task', 'lark_agent', 'spawned', 'extension', 'other']);
  assert.equal(sourceOfAutoConversation({ id: 'u1', origin: '' }), undefined);
  assert.equal(sourceOfAutoConversation({ id: 'u2', origin: 'user' }), undefined);
  assert.equal(sourceOfAutoConversation({ id: 'u3', origin: 'channel' }), undefined);
  assert.equal(sourceOfAutoConversation({ id: 't1', origin: 'user' }, ['t1']), 'task');
  assert.equal(sourceOfAutoConversation({ id: 'o1', origin: 'custom_bot' }), 'other');

  const partition = partitionAutoConversations([
    { id: 'main', main: true, origin: 'user', updated_at: '2026-11-27T12:00:00Z' },
    { id: 'archived', archived: true, origin: 'inbox', updated_at: '2026-11-27T11:00:00Z' },
    { id: 'mine-older', origin: 'user', updated_at: '2026-11-27T08:00:00Z' },
    { id: 'mine-newer', origin: 'channel', updated_at: '2026-11-27T10:00:00Z' },
    { id: 'inbox-1', origin: 'inbox', state: 'needs_you', updated_at: '2026-11-27T09:30:00Z' },
    { id: 'task-1', origin: 'task', state: 'working', updated_at: '2026-11-27T09:00:00Z' },
    { id: 'lark-1', origin: 'lark_agent', state: 'unread', updated_at: '2026-11-27T07:00:00Z' }
  ], { mainId: 'main', hubSays: true });

  assert.deepEqual(partition.all.map((s) => s.id), ['mine-newer', 'inbox-1', 'task-1', 'mine-older', 'lark-1']);
  assert.deepEqual(partition.mine.map((s) => s.id), ['mine-newer', 'mine-older']);
  assert.deepEqual(partition.auto.map((s) => s.id), ['inbox-1', 'task-1', 'lark-1']);
  assert.deepEqual(partition.groups.map((g) => g.source), ['inbox', 'task', 'lark_agent']);
  assert.equal(partition.needsYou, 1);
  assert.equal(partition.working, true);
  assert.equal(partition.unread, 1);
  assert.deepEqual(filterAutoConversations(partition, 'inbox').map((s) => s.id), ['inbox-1']);
  assert.deepEqual(filterAutoConversations(partition, 'automatic').map((s) => s.id), ['inbox-1', 'task-1', 'lark-1']);
});

test('AutoRename checks coding Agent eligibility, guards stale responses, and respects manual renames', () => {
  assert.equal(canAutoRenameSession({
    kind: 'session', machineId: 'm1', sessionId: 's1', assistant: false,
    connected: true, isDemo: false, features: ['agent_autoname']
  }), true);
  assert.equal(canAutoRenameSession({
    kind: 'session', machineId: 'm1', sessionId: 's1', assistant: false,
    connected: true, isDemo: true, features: []
  }), true);
  assert.equal(canAutoRenameSession({
    kind: 'conversation', machineId: 'm1', sessionId: 'c1', assistant: false,
    connected: true, isDemo: true, features: ['agent_autoname']
  }), false);
  assert.equal(canAutoRenameSession({
    kind: 'session', machineId: 'm1', sessionId: 'c1', assistant: true,
    connected: true, isDemo: true, features: ['agent_autoname']
  }), false);
  assert.equal(canAutoRenameSession({
    kind: 'session', machineId: 'm1', sessionId: 's1', assistant: false,
    connected: true, isDemo: false, features: []
  }), false);

  assert.equal(autoRenameSessionPath('mac/1', 'sess 2'), '/api/v1/sessions/mac%2F1/sess%202/autoname');
  const key = autoRenameSessionKey('scope', 1, 'm1', 's1');
  assert.equal(pendingAutoRenameTitle([key], key, 'Renaming…'), 'Renaming…');
  assert.equal(pendingAutoRenameTitle([], key, 'Renaming…'), '');
  assert.equal(shouldApplyAutoRenameTitle('Manual Title', 'Old Title', 'manual', 2, 1), false);
  assert.equal(shouldApplyAutoRenameTitle('Old Title', 'Old Title', 'auto', 1, 1), true);
});

test('QuickCommandPalette, UI components, and trilingual resources wire all Wave 6 parity features cleanly', () => {
  const groups = buildQuickCommandGroups('', [], {
    showStandingAuth: true,
    showFullSearch: true,
    showAllChats: true
  });
  const actionIds = groups.find((g) => g.category === 'actions').entries.map((e) => e.id);
  assert.ok(actionIds.includes('action:open_standing_auth'));
  assert.ok(actionIds.includes('action:open_full_search'));
  assert.ok(actionIds.includes('action:open_all_chats'));

  const standingSheet = readEts('components/StandingAuthSheet.ets');
  assert.match(standingSheet, /PersonaSheetHeader\(/);
  assert.match(standingSheet, /closeIdentity:\s*'standing\.auth\.close'/);
  assert.match(standingSheet, /Tokens\.controlBar/);
  assert.match(standingSheet, /showAlertDialog/);

  const alwaysSection = readEts('components/AlwaysGrantsSection.ets');
  assert.match(alwaysSection, /always\.restart\.banner/);
  assert.match(alwaysSection, /Tokens\.controlBar/);
  assert.match(alwaysSection, /loadGrants\(\)[\s\S]*this\.grants = undefined;\s*this\.revokingHash = '';\s*this\.pendingConfirmKey = '';/);
  assert.match(alwaysSection, /this\.pendingConfirmKey !== confirmKey/);

  const searchSheet = readEts('components/AssistantSearchSheet.ets');
  assert.match(searchSheet, /PersonaSheetHeader\(/);
  assert.match(searchSheet, /closeIdentity:\s*'assistant\.search\.close'/);
  assert.match(searchSheet, /assistant\.search\.scope\.chats/);
  assert.match(searchSheet, /assistant\.search\.scope\.agents/);

  const autoSheet = readEts('components/AutoConversationsSheet.ets');
  assert.match(autoSheet, /PersonaSheetHeader\(/);
  assert.match(autoSheet, /closeIdentity:\s*'auto\.chats\.close'/);
  assert.match(autoSheet, /auto\.chats\.filter\./);
  assert.match(autoSheet, /knownTaskIds/);
  assert.match(autoSheet, /if \(this\.canMutate\(item\.row\)\) \{[\s\S]*\.bindContextMenu/);

  const standingSheetSrc = readEts('components/StandingAuthSheet.ets');
  assert.match(standingSheetSrc, /@Monitor\('model\.conversations'\)/);

  const archivedSheet = readEts('components/ArchivedConversationsSheet.ets');
  assert.match(archivedSheet, /archived\.tab\.chats/);
  assert.match(archivedSheet, /archived\.tab\.agents/);
  assert.match(archivedSheet, /\/api\/v1\/sessions\/archived/);
  assert.match(archivedSheet, /\/unarchive/);

  const agentList = readEts('components/AgentListPage.ets');
  assert.match(agentList, /agents\.menu\.autoname/);
  assert.match(agentList, /isAutoRenamingRow/);
  assert.match(agentList, /AgentDirectoryRowContent\(\{[\s\S]*renaming:/);

  const rowContent = readEts('components/AgentDirectoryRowContent.ets');
  assert.match(rowContent, /@Param renaming: boolean = false;/);
  assert.match(rowContent, /if \(this\.renaming\) return tr\('agent_renaming'\);/);

  const indexSrc = readEts('pages/Index.ets');
  assert.match(indexSrc, /action:open_standing_auth/);
  assert.match(indexSrc, /action:open_full_search/);
  assert.match(indexSrc, /action:open_all_chats/);
  assert.match(indexSrc, /chat\.actions\.autoname/);

  const requiredKeys = [
    'standing_auth_title', 'standing_tab_standing', 'standing_tab_session',
    'standing_revoke_confirm_title', 'always_grants_heading', 'always_grants_restart_needed',
    'assistant_full_search_title', 'assistant_search_scope_chats',
    'archived_agents_empty_title', 'agent_auto_rename', 'agent_renaming', 'assistant_session'
  ];
  for (const locale of ['base', 'en_US', 'zh_CN', 'ja_JP']) {
    const map = new Map(readStrings(locale).string.map((item) => [item.name, item.value]));
    for (const k of requiredKeys) {
      assert.ok(map.get(k), `${locale} missing ${k}`);
    }
  }
});

test('DemoRepository serves standing authorizations, always grants, assistant search, auto chats, archived agents, and autoname', async () => {
  const repo = new DemoRepository();
  const roster = repo.roster();
  assert.ok(roster.features.includes('assistant_search'));
  assert.ok(roster.features.includes('conversation_origin'));
  assert.ok(roster.features.includes('agent_autoname'));

  // 1. Standing authorizations & live DELETE
  const standingBefore = parseStandingAuthorizationsResponse(await repo.call('/api/v1/assistant/standing-authorizations'));
  assert.equal(standingBefore.authorizations.length, 2);
  const delStanding = await repo.call('/api/v1/assistant/standing-authorizations/' + standingBefore.authorizations[0].id, 'DELETE', {});
  assert.equal(delStanding.ok, true);
  assert.equal(delStanding.revision, standingBefore.revision + 1);
  const standingAfter = parseStandingAuthorizationsResponse(await repo.call('/api/v1/assistant/standing-authorizations'));
  assert.equal(standingAfter.authorizations.length, 1);

  // 2. Session always-allow grants & live DELETE
  const grantsBefore = parseAlwaysGrantsResponse(await repo.call('/api/v1/assistant/conversations/demo-main/always'));
  assert.equal(grantsBefore.length, 1);
  assert.equal(grantsBefore[0].tool_name, 'Bash');
  const delGrant = parseAlwaysRevokeResponse(await repo.call('/api/v1/assistant/conversations/demo-main/always/' + grantsBefore[0].key_hash, 'DELETE', {}));
  assert.equal(delGrant.ok, true);
  assert.equal(delGrant.restart_required, false);
  assert.equal(parseAlwaysGrantsResponse(await repo.call('/api/v1/assistant/conversations/demo-main/always')).length, 0);

  // 3. Assistant full search across Demo conversations & transcripts
  const searchAns = parseAssistantSearchAnswer(await repo.call('/api/v1/assistant/search?q=release&limit=30'));
  assert.ok(searchAns.results.some((r) => r.session_id === 'demo-recent-review'));
  assert.ok(searchAns.results.some((r) => r.hits.some((h) => h.ranges.length > 0)));

  // 4. AutoConversations over Demo conversations + top-level leaders Pin/Unpin persistence
  const demoConvs = roster.conversations.map((r) => ({
    id: r.id,
    session_id: r.session || r.id,
    machine_id: r.machine,
    title: r.title,
    origin: r.data().origin,
    kind: r.data().kind,
    automatic: r.data().automatic,
    main: r.data().main,
    archived: r.data().archived,
    updated_at: r.data().updated_at
  }));
  const demoPartition = partitionAutoConversations(demoConvs, { mainId: roster.mainId, hubSays: true });
  assert.ok(demoPartition.mine.some((c) => c.id === 'demo-recent-review'));
  assert.ok(demoPartition.auto.some((c) => c.id === 'demo-auto-summary'));

  const pinned = await repo.call('/api/v1/assistant/directions/demo-recent-review/make', 'POST', {});
  assert.deepEqual(pinned.leaders, ['demo-recent-review']);
  const assistantAfterPin = await repo.call('/api/v1/assistant');
  assert.deepEqual(assistantAfterPin.leaders, ['demo-recent-review']);
  assert.deepEqual(repo.roster().leaders, ['demo-recent-review']);
  const unpinned = await repo.call('/api/v1/assistant/directions/demo-recent-review/end', 'POST', {});
  assert.deepEqual(unpinned.leaders, []);
  const assistantAfterUnpin = await repo.call('/api/v1/assistant');
  assert.deepEqual(assistantAfterUnpin.leaders, []);
  const pinsPut = await repo.call('/api/v1/pins', 'PUT', { spaces: [], sessions: ['demo-mac/demo-agent-review'] });
  assert.deepEqual(pinsPut.sessions, ['demo-mac/demo-agent-review']);

  // 5. Archived agent sessions & unarchive
  const archivedAgents = await repo.call('/api/v1/sessions/archived');
  assert.ok(archivedAgents.sessions.some((s) => s.session_id === 'demo-agent-archived'));
  const unarchived = await repo.call('/api/v1/sessions/demo-mac/demo-agent-archived/unarchive', 'POST', {});
  assert.equal(unarchived.ok, true);
  const archivedAfter = await repo.call('/api/v1/sessions/archived');
  assert.equal(archivedAfter.sessions.some((s) => s.session_id === 'demo-agent-archived'), false);
  assert.ok((await repo.sessions()).some((s) => s.id === 'demo-agent-archived'));

  // 6. Agent Auto Rename & assistant refusal
  const renamed = await repo.call('/api/v1/sessions/demo-mac/demo-agent-review/autoname', 'POST', {});
  assert.equal(renamed.ok, true);
  assert.equal(renamed.changed, true);
  const updatedReview = (await repo.sessions()).find((s) => s.id === 'demo-agent-review');
  assert.equal(updatedReview.title, renamed.title);
  await assert.rejects(
    repo.call('/api/v1/sessions/demo-mac/demo-main/autoname', 'POST', {}),
    { status: 400, code: 'assistant_session' }
  );
});
