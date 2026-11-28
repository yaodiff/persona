import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '../platform/HubTransport') return { url: 'wp:transport', shortCircuit: true };
    if (specifier === '../platform/LocalStore') return { url: 'wp:local', shortCircuit: true };
    if (specifier === './MutationStore') return { url: 'wp:mutation', shortCircuit: true };
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/TodoWorkspace.ets')) {
      return { url: 'wp:client', shortCircuit: true };
    }
    if (specifier === './HubRepository' && context.parentURL?.endsWith('/TodoWorkspace.ets')) {
      return { url: 'wp:repo', shortCircuit: true };
    }
    if (specifier === '@kit.ArkTS') return { url: 'wp:arkts', shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.')) {
      return { url: new URL(specifier.endsWith('.ets') ? specifier : specifier + '.ets', context.parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'wp:transport') {
      return {
        format: 'module',
        source: `
          export class EndpointConfig { constructor(baseUrl, hubId, token, pin, managerId = '') { Object.assign(this, { baseUrl, hubId, token, pin, managerId }); } }
          export class HttpFailure extends Error { constructor(status, code, resultUnknown = false, resultPending = false) { super(code || 'failed'); this.status = status; this.code = code; this.resultUnknown = resultUnknown; this.resultPending = resultPending; } }
          export class HubTransport { constructor(config) { this.config = config; } close() {} }
        `,
        shortCircuit: true
      };
    }
    if (url === 'wp:local') return { format: 'module', source: 'export class LocalStore {}', shortCircuit: true };
    if (url === 'wp:mutation') return { format: 'module', source: 'export class MutationRecord {} export class MutationStore {}', shortCircuit: true };
    if (url === 'wp:client') return { format: 'module', source: 'export class ClientModel {}', shortCircuit: true };
    if (url === 'wp:repo') return { format: 'module', source: 'export function segment(v) { return encodeURIComponent(v); } export class HubRepository {}', shortCircuit: true };
    if (url === 'wp:arkts') return { format: 'module', source: 'export const util = { TextDecoder: { create: () => ({ decodeToString: () => "" }) } };', shortCircuit: true };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const {
  TokenFormat,
  buildTokenHeatmap,
  cacheHitRatePercent,
  heatLevels,
  parseUsageDaysResponse,
  parseUsageHoursResponse,
  parseUsageProjectDetailResponse,
  parseUsageProjectsResponse,
  rankProjectsForRange,
  rankUsageParts,
  sumLastDaysTokens
} = await import('../entry/src/main/ets/core/TokenUsage.ts');

const { rowSummary, detailLines } = await import('../entry/src/main/ets/core/TodoWaitText.ts');
const {
  computePersonalAgentEntry,
  filterPersonalAgentSessions,
  parsePersonalAgentSessionsPage,
  parsePersonalAgentSources,
  personalAgentSessionPath,
  shouldShowPersonalAgentsHeading
} = await import('../entry/src/main/ets/core/PersonalAgents.ts');
const { isKnownDebateStatus, parseMinutesDebates } = await import('../entry/src/main/ets/core/MinutesDebates.ts');
const {
  kbInboxLine,
  kbInboxTier,
  kbMessageRowKey,
  parseChatInboxPage,
  parseChatInboxPieces,
  parseInboxKBChat
} = await import('../entry/src/main/ets/core/KBChatInbox.ts');
const { isHubPageHidden } = await import('../entry/src/main/ets/core/HubModules.ts');
const { buildQuickCommandGroups } = await import('../entry/src/main/ets/core/QuickCommandPalette.ts');

const { TodoWorkspace, TodoWorkspaceItem } = await import('../entry/src/main/ets/models/TodoWorkspace.ets');
const { DemoRepository } = await import('../entry/src/main/ets/models/DemoRepository.ets');

test('TokenFormat.count matches iOS TokenUsage.swift formatting and rollover rules', () => {
  assert.equal(TokenFormat.count(0), '0');
  assert.equal(TokenFormat.count(999), '999');
  assert.equal(TokenFormat.count(1250), '1.25K');
  assert.equal(TokenFormat.count(999500), '1M');
  assert.equal(TokenFormat.count(2960000), '2.96M');
  assert.equal(TokenFormat.count(178000000), '178M');
  assert.equal(TokenFormat.count(999500000), '1B');
  assert.equal(TokenFormat.count(3200000000), '3.2B');
  assert.equal(TokenFormat.count(-1500), '-1.5K');
});

test('TokenUsage heatmap builds 12x7 grid with 5-step buckets, hit rate, and project ranking', () => {
  assert.deepEqual(heatLevels([0, 10, 20, 30, 40]), [0, 1, 2, 3, 4]);
  assert.deepEqual(heatLevels([0, 0, 0]), [0, 0, 0]);
  assert.deepEqual(heatLevels([100, 100, 100000]), [3, 3, 4]);

  const rawDays = [];
  const startMs = Date.UTC(2026, 8, 5); // 84 days ending 2026-11-27 (Friday)
  for (let i = 0; i < 84; i++) {
    const d = new Date(startMs + i * 86400000);
    const iso = d.toISOString().slice(0, 10);
    rawDays.push({
      date: iso,
      in: i === 0 ? 0 : 1000 * (i + 1),
      cache_write: i === 0 ? 0 : 500,
      cache_read: i === 0 ? 0 : 8000 * (i + 1),
      out: i === 0 ? 0 : 300 * (i + 1),
      req: i === 0 ? 0 : i
    });
  }
  const parsedDays = parseUsageDaysResponse({ usage: { zone: 'UTC', stored: true, days: rawDays } });
  assert.equal(parsedDays.days.length, 84);
  const grid = buildTokenHeatmap(parsedDays.days, 12);
  assert.equal(grid.weeks, 12);
  assert.equal(grid.columns.length, 12);
  for (const col of grid.columns) {
    assert.equal(col.length, 7);
  }
  // 2026-11-27 is Friday -> Monday=0..Friday=4; Sat/Sun in last col are future (-1)
  assert.deepEqual(grid.todayCell, { column: 11, row: 4 });
  assert.equal(grid.columns[11][4].isToday, true);
  assert.equal(grid.columns[11][4].level, 4);
  assert.equal(grid.columns[11][5].level, -1);
  assert.equal(grid.columns[11][6].level, -1);

  const last7 = sumLastDaysTokens(parsedDays.days, 7);
  assert.ok(cacheHitRatePercent(last7) > 85);

  const parsedProjects = parseUsageProjectsResponse({
    usage: {
      zone: 'UTC',
      stored: true,
      ranges: [
        {
          days: 1,
          total: { in: 100, cache_write: 0, cache_read: 900, out: 100, req: 2 },
          projects: [
            { key: 'b', kind: 'folder', group: 'other', name: 'Docs', machines: 1, share: 0.25, tokens: { in: 25, cache_write: 0, cache_read: 225, out: 25, req: 1 } },
            { key: 'a', kind: 'repo', group: 'development', name: 'org/app', host: 'github.com', machines: 2, share: 0.75, tokens: { in: 75, cache_write: 0, cache_read: 675, out: 75, req: 1 } }
          ]
        },
        { days: 3, total: { in: 10, out: 10 }, projects: [] },
        { days: 7, total: { in: 500, out: 500 }, projects: [] }
      ]
    }
  });
  const ranked = rankProjectsForRange(parsedProjects, 'today');
  assert.equal(ranked.length, 2);
  assert.equal(ranked[0].project.key, 'a');
  assert.equal(ranked[0].progressFraction, 1);
  assert.ok(Math.abs(ranked[1].progressFraction - 275 / 825) < 1e-6);

  const hours = parseUsageHoursResponse({
    usage: {
      zone: 'UTC',
      stored: true,
      hours: [{ t: 1700000000, date: '2026-11-27', hour: 10, in: 100, out: 50 }],
      machines: [{ id: 'm1', name: 'Studio', in: 100, out: 50 }],
      agents: [{ agent: 'claude', in: 100, out: 50 }],
      total: { in: 100, out: 50 }
    }
  });
  assert.equal(hours.machines[0].name, 'Studio');
  assert.equal(hours.total.total, 150);

  const detail = parseUsageProjectDetailResponse({
    usage: {
      zone: 'UTC',
      stored: true,
      project: { key: 'a', kind: 'repo', group: 'development', name: 'org/app', tokens: { in: 100, out: 50 } },
      ranges: [
        {
          days: 7,
          total: { in: 100, cache_read: 300, out: 50, req: 4 },
          share: 0.5,
          cache_hit: 0.75,
          machines: [{ key: 'm1', name: 'Mac', tokens: { in: 100, out: 50 } }],
          models: [{ key: 'claude-sonnet-4-6', tokens: { in: 100, out: 50 } }]
        }
      ],
      days: [{ date: '2026-11-27', tokens: { in: 100, out: 50 } }],
      hours: []
    }
  });
  assert.ok(detail);
  assert.equal(rankUsageParts(detail.ranges[0].models)[0].progressFraction, 1);
});

test('TodoWaitText handles all wait kinds, PR blockers, overdue highlight, and shared fixtures', () => {
  const nowMs = new Date(2026, 10, 27, 10, 0, 0).getTime();
  const fixture = JSON.parse(readFileSync(new URL('../../spec/fixtures/todo-wait/todo_wait.json', import.meta.url), 'utf8'));
  for (const c of fixture.cases) {
    if (!c.expected.wait) continue;
    const summary = rowSummary(c.expected.wait, nowMs);
    assert.ok(summary.lead.length > 0, `${c.name}: non-empty lead`);
    const detail = detailLines(c.expected.wait, nowMs);
    assert.ok(detail.kindTitle.length > 0, `${c.name}: non-empty kindTitle`);
  }

  const pr = rowSummary({
    kind: 'pr_merge',
    waiting_on: 'PR #88',
    pr_blockers: ['review_required', 'checks_failing'],
    until: new Date(2026, 10, 27, 8, 0, 0).toISOString()
  }, nowMs);
  assert.equal(pr.lead, 'PR #88\u00A0· Needs review, Checks failing');
  assert.equal(pr.isDue, true);
  assert.match(pr.timing, /^Check-in due/);

  const limit = rowSummary({
    kind: 'usage_limit',
    waiting_on: 'Worker',
    wake_at: new Date(2026, 10, 27, 12, 0, 0).toISOString()
  }, nowMs);
  assert.equal(limit.lead, 'Worker reached its usage limit');
  assert.equal(limit.timing, 'Back 12:00');

  const offline = detailLines({ kind: 'computer_offline', waiting_on: 'Mac Mini' }, nowMs);
  assert.equal(offline.kindTitle, 'Computer offline');

  const sameIso = new Date(2026, 10, 27, 9, 0, 0).toISOString();
  const report = detailLines({ kind: 'scheduled_report', wake_at: sameIso, until: sameIso }, nowMs);
  assert.equal(report.kindTitle, 'Scheduled report');
  assert.equal(report.untilText, undefined, 'deduplicates identical wake_at and until');

  const parked = detailLines({
    kind: 'parked',
    reason: 'Deferred',
    url: 'https://example.com/msg/1',
    contact: { channel: 'email', sender: 'Persona', url: 'https://example.com/msg/1', request: 'Ping?' }
  }, nowMs);
  assert.equal(parked.kindTitle, 'Parked');
  assert.equal(parked.sourceURL, undefined, 'deduplicates sourceURL matching contact.url');
  assert.equal(parked.contact.url, 'https://example.com/msg/1');
});

test('TodoWorkspace decodes wait payloads and surfaces matching Done to-dos under a distinct Done section during search', () => {
  const ws = new TodoWorkspace();
  const openItem = new TodoWorkspaceItem({
    id: 't1',
    title: 'Ship release notes',
    project: 'proj-1',
    due_date: '2026-11-27',
    created_at: '2026-11-20T09:00:00Z',
    updated_at: '2026-11-27T09:00:00Z',
    wait: {
      kind: 'person_reply',
      waiting_on: 'Alex',
      reason: 'Confirm release notes',
      until: '2026-11-27T08:00:00Z'
    }
  }, 'work');
  const doneItem = new TodoWorkspaceItem({
    id: 't2',
    title: 'Archive release notes draft',
    project: 'proj-1',
    done_at: '2026-11-18T10:00:00Z',
    created_at: '2026-11-10T09:00:00Z',
    updated_at: '2026-11-18T10:00:00Z'
  }, 'work');
  assert.equal(openItem.wait?.kind, 'person_reply');
  assert.equal(openItem.wait?.waiting_on, 'Alex');

  ws.items = [openItem, doneItem];
  ws.setDueFilter('today');
  ws.setGroupBy('project');
  ws.setSearch('release notes');

  const sections = ws.sections(Date.parse('2026-11-27T10:00:00Z'), 1);
  assert.deepEqual(sections.map((s) => s.key), ['proj-1', 'done']);
  assert.equal(sections[1].rows[0].lead.id, 't2');
});

test('PersonalAgents parsing and group heading visibility gating match #16793 (0eba2caef)', () => {
  const sources = parsePersonalAgentSources({
    sources: [
      { source: 'lark', enabled: true, accounts: [{ accountId: 'acc1', provider: 'grok_bot', sessionCount: 2 }] },
      { source: 'old', enabled: true, setup: 'no_accounts', accounts: [] },
      { source: 'off', enabled: false, accounts: [{ accountId: 'acc2', provider: 'muse' }] }
    ]
  });
  const page = parsePersonalAgentSessionsPage({
    total: 5,
    hasMore: true,
    sessions: [
      { id: 'lark:s1', source: 'lark', sessionKey: 's1', provider: 'grok_bot', title: 'Research Bot', canSend: true, unreadCount: 2, rankTimeMs: 1700000000000 },
      { id: 'off:s2', source: 'off', sessionKey: 's2', provider: 'muse', title: 'Disabled Bot', canSend: false, unreadCount: 0, rankTimeMs: 1690000000000 }
    ]
  });

  const entry = computePersonalAgentEntry(sources, page);
  assert.deepEqual(entry, { available: true, count: 5 });

  assert.equal(shouldShowPersonalAgentsHeading('all', '', 5), true);
  assert.equal(shouldShowPersonalAgentsHeading('needsYou', '', 5), false);
  assert.equal(shouldShowPersonalAgentsHeading('all', 'research', 5), false);
  assert.equal(shouldShowPersonalAgentsHeading('all', '', 0), false);

  assert.equal(filterPersonalAgentSessions(page.sessions, 'research').length, 1);
  assert.equal(personalAgentSessionPath('lark:agent/1:main'), '/api/v1/agents/sessions/lark%3Aagent%2F1%3Amain');
});

test('MinutesDebates leniently parses valid debates and drops malformed entries without throwing', () => {
  assert.deepEqual(parseMinutesDebates(undefined), []);
  assert.deepEqual(parseMinutesDebates(null), []);
  assert.deepEqual(parseMinutesDebates('bad'), []);
  assert.deepEqual(parseMinutesDebates({}), []);

  const parsed = parseMinutesDebates([
    null,
    'invalid',
    { question: '   ', positions: [{ stance: 'A', at: 1 }, { stance: 'B', at: 2 }] },
    { question: 'Too few positions', positions: [{ stance: 'Only one', at: 10 }] },
    {
      question: 'Ship together?',
      topic: 'Scope',
      status: 'settled',
      at: 100,
      positions: [
        { stance: 'Ship together', at: 102, who: 'Mina' },
        { stance: 'Split release', at: 130, who: 'Alex' },
        { stance: '' }
      ],
      outcome: { text: 'Ship together', at: 180, decision: 0 }
    },
    {
      question: 'Future status',
      status: 'escalated',
      at: 200,
      positions: [
        { stance: 'Option A', at: 205 },
        { stance: 'Option B', at: 220 }
      ],
      outcome: { text: 'Should be ignored because not settled', at: 250 }
    }
  ], 1);

  assert.equal(parsed.length, 2);
  assert.equal(parsed[0].positions.length, 2);
  assert.equal(parsed[0].outcome?.decision, 0);
  assert.equal(isKnownDebateStatus(parsed[0].status), true);
  assert.equal(parsed[1].outcome, undefined);
  assert.equal(isKnownDebateStatus(parsed[1].status), false);
});

test('KBChatInbox matches chat recent lines to Inbox items and gates visibility when hub hides pages', () => {
  const recent = [
    '## 2026-11-11',
    '- 08:15 Mina Example: Can someone review the checklist?',
    '  With extra detail on line two',
    '- 08:20 Demo user: Checking now.'
  ].join('\n');

  const rawInbox = [
    {
      id: 'inbox-open',
      title: 'Release room',
      tier: 'action',
      status: 'action',
      summary: 'Review checklist',
      messages: [
        { day: '2026-11-11', line: '- 08:15 Mina Example: Can someone review the checklist?\n  With extra detail on line two' }
      ]
    },
    {
      id: 'inbox-done',
      title: 'Resolved note',
      tier: 'fyi',
      done_at: '2026-11-11T09:00:00Z'
    }
  ];

  const page = parseChatInboxPage('chats', recent, rawInbox);
  assert.ok(page);
  assert.equal(page.open.length, 1);
  assert.equal(page.done.length, 1);
  assert.equal(kbInboxTier(page.open[0]), 'action');
  assert.equal(kbInboxLine(page.open[0]), 'Review checklist');
  assert.ok(page.pieces && page.pieces.length >= 2);
  const linked = page.pieces.find((p) => p.item !== null);
  assert.equal(linked?.item?.id, 'inbox-open');

  assert.equal(parseChatInboxPage('docs', recent, rawInbox), null);
  assert.equal(parseChatInboxPieces(recent, []), null);
  assert.ok(kbMessageRowKey('2026-11-11', '- 08:15 Mina: Hi\n  there').includes('Hi\nthere'));

  assert.deepEqual(parseInboxKBChat({ id: 'demo-chat-notes', path: 'chats' }), { id: 'demo-chat-notes', path: 'chats' });
  assert.equal(parseInboxKBChat({ id: '' }), null);

  assert.equal(isHubPageHidden('inbox', JSON.stringify({ hidden: ['inbox'] }), false), true);
  assert.equal(isHubPageHidden('knowledge', JSON.stringify({ pages: { hidden: ['knowledge'] } }), false), true);
  assert.equal(isHubPageHidden('inbox', JSON.stringify({ hidden: ['inbox'] }), true), false);
});

test('QuickCommandPalette includes action:open_token_usage, gates knowledgeVisible, and UI files use normalized tokens with zero hex literals', () => {
  const groups = buildQuickCommandGroups('token', []);
  const actions = groups.find((g) => g.category === 'actions');
  assert.ok(actions && actions.entries.some((e) => e.id === 'action:open_token_usage'));
  const kbVisible = buildQuickCommandGroups('knowledge', [], { knowledgeVisible: true }).find((g) => g.category === 'workspaces');
  assert.ok(kbVisible && kbVisible.entries.some((e) => e.id === 'workspace:knowledge'));
  const kbHidden = buildQuickCommandGroups('knowledge', [], { knowledgeVisible: false }).find((g) => g.category === 'workspaces');
  assert.ok(!kbHidden || !kbHidden.entries.some((e) => e.id === 'workspace:knowledge'));

  const etsFiles = [
    '../entry/src/main/ets/components/TokenUsageSheet.ets',
    '../entry/src/main/ets/components/PersonalAgentsSheet.ets',
    '../entry/src/main/ets/components/TodoWorkspacePage.ets',
    '../entry/src/main/ets/components/FoldInspectorPanel.ets'
  ];
  for (const rel of etsFiles) {
    const src = readFileSync(new URL(rel, import.meta.url), 'utf8');
    const hexMatches = src.match(/#[0-9A-Fa-f]{6,8}\b/g) || [];
    assert.deepEqual(hexMatches, [], `${rel} has hardcoded hex literals: ${hexMatches.join(', ')}`);
  }

  const agentList = readFileSync(new URL('../entry/src/main/ets/components/AgentListPage.ets', import.meta.url), 'utf8');
  assert.match(agentList, /shouldShowPersonalAgentsHeading\(/);
  assert.match(agentList, /id\('agents\.personal'\)/);

  const kbPage = readFileSync(new URL('../entry/src/main/ets/components/KnowledgeWorkspacePage.ets', import.meta.url), 'utf8');
  assert.match(kbPage, /parseMinutesDebates\(/);
  assert.match(kbPage, /parseChatInboxPage\(/);
  assert.match(kbPage, /id\('knowledge\.minutes\.debates'\)/);
  assert.match(kbPage, /id\('knowledge\.inbox\.section'\)/);

  const inboxPage = readFileSync(new URL('../entry/src/main/ets/components/InboxWorkspacePage.ets', import.meta.url), 'utf8');
  assert.match(inboxPage, /parseInboxKBChat\(/);
  assert.match(inboxPage, /id\('inbox\.detail\.kb_chat'\)/);
});

test('DemoRepository supports all new offline parity routes and payloads', async () => {
  const repo = new DemoRepository();
  const days = parseUsageDaysResponse(await repo.call('/api/v1/usage/days?days=84'));
  assert.equal(days.days.length, 84);
  const hours = parseUsageHoursResponse(await repo.call('/api/v1/usage/hours?days=7'));
  assert.ok(hours.machines.length >= 1);
  const projects = parseUsageProjectsResponse(await repo.call('/api/v1/usage/projects'));
  assert.equal(projects.ranges.length, 3);
  const detail = parseUsageProjectDetailResponse(await repo.call('/api/v1/usage/projects/detail?key=' + encodeURIComponent(projects.ranges[2].projects[0].key)));
  assert.ok(detail && detail.ranges.length === 3);

  const agentsSources = parsePersonalAgentSources(await repo.call('/api/v1/agents'));
  assert.equal(agentsSources.length, 0);
  const agentsSessions = parsePersonalAgentSessionsPage(await repo.call('/api/v1/agents/sessions'));
  assert.equal(agentsSessions.sessions.length, 0);

  const todos = await repo.todos();
  assert.equal(todos.length, 3);
  const docsTodo = todos.find((row) => row.id === 'demo-todo-docs');
  assert.equal(docsTodo?.data()?.wait?.kind, 'pr_merge');

  const rec = await repo.call('/api/v1/kb/recordings/demo-recording-notes');
  assert.equal(parseMinutesDebates(rec.detail.minutes.debates).length, 2);

  const chat = await repo.call('/api/v1/kb/chats/demo-chat-notes');
  assert.ok(parseChatInboxPage('chats', chat.detail.recent, chat.inbox));
  const inboxItem = await repo.call('/api/v1/inbox/demo-inbox-lark');
  assert.deepEqual(parseInboxKBChat(inboxItem.kb_chat), { id: 'demo-chat-notes', path: 'chats' });
});
