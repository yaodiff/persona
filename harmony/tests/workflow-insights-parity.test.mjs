import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

const stubs = new Map([
  ['wf-stub:transport', `
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
  ['wf-stub:local-store', 'export class LocalStore {}'],
  ['wf-stub:mutation-store', 'export class MutationRecord {} export class MutationStore { static shared() { throw new Error("demo_mutation_store_must_not_be_used"); } }'],
  ['wf-stub:arkts', 'export const util = { generateRandomUUID: () => crypto.randomUUID(), TextDecoder: { create: () => ({ decodeToString: () => "" }) } };'],
  ['wf-stub:client', 'export class ClientModel {}']
]);

registerHooks({
  resolve(specifier, context, next) {
    if (stubs.has(specifier)) return { url: specifier, shortCircuit: true };
    if (specifier === '../platform/HubTransport') return { url: 'wf-stub:transport', shortCircuit: true };
    if (specifier === '../platform/LocalStore') return { url: 'wf-stub:local-store', shortCircuit: true };
    if (specifier === './MutationStore') return { url: 'wf-stub:mutation-store', shortCircuit: true };
    if (specifier === '@kit.ArkTS') return { url: 'wf-stub:arkts', shortCircuit: true };
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/TodoWorkspace.ets')) {
      return { url: 'wf-stub:client', shortCircuit: true };
    }
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
  buildAddTodoDecisionPayload,
  buildTodoStageProjection,
  currentTodoDecisions,
  listedTodoDecisions,
  parseTodoDecisionsResponse,
  resolveTodoStage,
  shownTodoDecisions,
  supersedeCandidatesFor
} = await import('../entry/src/main/ets/core/TodoDecisions.ts');

const {
  buildProjectActivityBars,
  buildRecentHourlyActivity,
  formatAgentDisplayName,
  formatLauncherDisplayName,
  parseUsageHoursResponse,
  parseUsageProjectDetailResponse,
  rankAgentShares,
  rankMachineShares,
  rankUsagePlaces
} = await import('../entry/src/main/ets/core/TokenUsage.ts');

const {
  buildNextSuggestionsPayload,
  canDirectSendSuggestion,
  extractSessionSuggestions,
  parseAssistantNextSuggestions,
  resolveAuthoritativeNextSuggestions
} = await import('../entry/src/main/ets/core/NextSuggestions.ts');

const {
  isRuleSuggestionOffered,
  parseRuleSuggestion,
  parseRuleSuggestionMade,
  ruleSuggestionAlwaysPath,
  ruleSuggestionMadeKind,
  ruleSuggestionNotNowPath,
  RuleSuggestionTracker
} = await import('../entry/src/main/ets/core/RuleSuggestions.ts');

const {
  buildTranscriptView,
  exportTranscriptText,
  filterTranscriptBookmarks,
  findTranscriptMatches,
  splitTranscriptMatchParts,
  stepTranscriptMatch
} = await import('../entry/src/main/ets/core/TranscriptSearch.ts');

const { isRecordingModuleAllowed } = await import('../entry/src/main/ets/core/XiaoYiActions.ts');
const { DemoRepository } = await import('../entry/src/main/ets/models/DemoRepository.ets');

const readEts = (rel) => readFileSync(new URL('../entry/src/main/ets/' + rel, import.meta.url), 'utf8');
const readStrings = (locale) => JSON.parse(readFileSync(new URL('../entry/src/main/resources/' + locale + '/element/string.json', import.meta.url), 'utf8'));

test('TodoStage projection enforces zero guessing and builds 6-step and done progressions', () => {
  assert.equal(resolveTodoStage(undefined), null);
  assert.equal(resolveTodoStage(''), null);
  assert.equal(resolveTodoStage('unknown_stage'), null);
  assert.equal(resolveTodoStage('review', 'general'), null);
  assert.equal(resolveTodoStage('review'), 'review');
  assert.equal(resolveTodoStage('develop'), 'develop');

  const openProj = buildTodoStageProjection('review', false);
  assert.equal(openProj.totalSteps, 6);
  assert.equal(openProj.currentNumber, 3);
  assert.equal(openProj.currentId, 'review');
  assert.deepEqual(
    openProj.steps.map((s) => [s.id, s.state]),
    [
      ['plan', 'passed'],
      ['develop', 'passed'],
      ['review', 'current'],
      ['merge', 'ahead'],
      ['deploy', 'ahead'],
      ['verify', 'ahead']
    ]
  );

  const doneProj = buildTodoStageProjection('develop', true);
  assert.equal(doneProj.totalSteps, 3);
  assert.equal(doneProj.currentNumber, 3);
  assert.equal(doneProj.currentId, 'done');
  assert.deepEqual(
    doneProj.steps.map((s) => [s.id, s.state]),
    [
      ['plan', 'passed'],
      ['develop', 'passed'],
      ['done', 'passed']
    ]
  );
});

test('TodoDecisions folding, revise/supersede logic, payload builder, and DemoRepository CRUD work end-to-end', async () => {
  const all = parseTodoDecisionsResponse({
    decisions: [
      { id: 'd1', todo_id: 't1', question: 'Q1', choice: 'A1', superseded_by: 'd2' },
      { id: 'd2', todo_id: 't1', question: 'Q1', choice: 'A2', supersedes_id: 'd1' },
      { id: 'd3', todo_id: 't1', question: 'Q2', choice: 'B1' },
      { id: 'd4', todo_id: 't1', question: 'Q3', choice: 'C1' },
      { id: 'd5', todo_id: 't1', question: 'Q4', choice: 'D1' },
      { id: 'd6', todo_id: 't1', question: 'Q5', choice: 'E1' },
      { id: 'd7', todo_id: 't1', question: 'Q6', choice: 'F1' }
    ]
  });

  const active = currentTodoDecisions(all);
  assert.deepEqual(active.map((d) => d.id), ['d2', 'd3', 'd4', 'd5', 'd6', 'd7']);
  assert.equal(shownTodoDecisions(listedTodoDecisions(all, false), 'first').length, 5);
  assert.equal(shownTodoDecisions(listedTodoDecisions(all, false), 'all').length, 6);
  assert.equal(shownTodoDecisions(listedTodoDecisions(all, false), 'folded').length, 0);
  assert.equal(listedTodoDecisions(all, true).length, 7);

  const cands = supersedeCandidatesFor(all[3], all);
  assert.deepEqual(cands.map((d) => d.id), ['d2', 'd3']);

  assert.equal(buildAddTodoDecisionPayload('   ', 'choice'), null);
  assert.equal(buildAddTodoDecisionPayload('question', '  '), null);
  assert.deepEqual(buildAddTodoDecisionPayload(' Q ', ' C ', '  ', ''), { question: 'Q', choice: 'C' });
  assert.deepEqual(buildAddTodoDecisionPayload('Q', 'C', 'Why', 'd1'), {
    question: 'Q',
    choice: 'C',
    reason: 'Why',
    supersedes_id: 'd1'
  });

  const repo = new DemoRepository();
  const todos = await repo.call('/api/v1/todos');
  const releaseTodo = todos.todos.find((t) => t.id === 'demo-todo-release');
  const docsTodo = todos.todos.find((t) => t.id === 'demo-todo-docs');
  assert.equal(releaseTodo.stage, 'review');
  assert.equal(docsTodo.stage, 'develop');

  const initialReleaseDecisions = await repo.call('/api/v1/todos/demo-todo-release/decisions?all=1');
  assert.equal(initialReleaseDecisions.decisions.length, 3);
  assert.equal(initialReleaseDecisions.decisions[0].superseded_by, 'demo-dec-release-2');

  const initialDocsDecisions = await repo.call('/api/v1/todos/demo-todo-docs/decisions?all=1');
  assert.equal(initialDocsDecisions.decisions.length, 1);

  const posted = await repo.call('/api/v1/todos/demo-todo-release/decisions', 'POST', {
    question: 'Require release notes?',
    choice: 'Yes, include Harmony notes',
    reason: 'Parity pack',
    supersedes_id: 'demo-dec-release-3'
  });
  assert.equal(posted.inserted, true);
  assert.equal(posted.decision.supersedes_id, 'demo-dec-release-3');

  const afterPost = await repo.call('/api/v1/todos/demo-todo-release/decisions?all=1');
  assert.equal(afterPost.decisions.length, 4);
  const oldDec = afterPost.decisions.find((d) => d.id === 'demo-dec-release-3');
  assert.equal(oldDec.superseded_by, posted.decision.id);

  await repo.call(`/api/v1/todos/demo-todo-release/decisions/${posted.decision.id}`, 'PATCH', { supersedes_id: '' });
  const afterUndo = await repo.call('/api/v1/todos/demo-todo-release/decisions?all=1');
  assert.equal(afterUndo.decisions.find((d) => d.id === 'demo-dec-release-3').superseded_by, '');
});

test('TokenUsage parses and ranks hourly activity, machine/agent shares, and full project detail in DemoRepository', async () => {
  const repo = new DemoRepository();
  const rawHours = await repo.call('/api/v1/usage/hours?days=7');
  const parsedHours = parseUsageHoursResponse(rawHours);
  assert.equal(parsedHours.hours.length, 24);
  assert.equal(parsedHours.machines.length, 2);
  assert.equal(parsedHours.agents.length, 2);

  const hourly = buildRecentHourlyActivity(parsedHours.hours, 24);
  assert.equal(hourly.bars.length, 24);
  assert.ok(hourly.peakId.length > 0);
  assert.ok(hourly.totalTokens > 0);
  assert.ok(hourly.bars.every((b) => b.fraction >= 0 && b.fraction <= 1));

  const machines = rankMachineShares(parsedHours.machines);
  const agents = rankAgentShares(parsedHours.agents);
  assert.equal(machines.length, 2);
  assert.equal(agents.length, 2);
  assert.equal(formatAgentDisplayName('claude', 'Not split'), 'Claude');
  assert.equal(formatAgentDisplayName('', 'Not split'), 'Not split');
  assert.equal(formatLauncherDisplayName('', 'Opened in a Terminal'), 'Opened in a Terminal');
  assert.equal(formatLauncherDisplayName('claude', 'Opened in a Terminal', { claude: 'Claude Code' }), 'Claude Code');

  const rawDetail = await repo.call('/api/v1/usage/projects/detail?key=git%3Aexample.test%2Fexample%2Freleasedesk');
  const detail = parseUsageProjectDetailResponse(rawDetail);
  assert.ok(detail);
  assert.equal(detail.ranges.length, 3);
  const b7 = detail.ranges[2];
  assert.equal(b7.machines.length, 2);
  assert.equal(b7.agents.length, 2);
  assert.equal(b7.models.length, 2);
  assert.equal(b7.launchers.length, 2);
  assert.equal(b7.places.length, 2);

  const places = rankUsagePlaces(b7.places);
  assert.equal(places.length, 2);
  assert.equal(places.some((p) => p.place.worktree === true), true);

  const todayBars = buildProjectActivityBars(detail, 'today');
  const weekBars = buildProjectActivityBars(detail, '7d');
  assert.equal(todayBars.bars.length, 24);
  assert.equal(weekBars.bars.length, 7);
});

test('NextSuggestions enforces risky direct-send gating and DemoRepository /api/v1/assistant toggle', async () => {
  assert.equal(parseAssistantNextSuggestions({ main: 'm1' }), null);
  assert.equal(parseAssistantNextSuggestions({ next_suggestions: true }), true);
  assert.equal(parseAssistantNextSuggestions({ next_suggestions: false }), false);
  assert.deepEqual(buildNextSuggestionsPayload(true), { next_suggestions: true });
  assert.equal(resolveAuthoritativeNextSuggestions({ next_suggestions: false }, true), false);
  assert.equal(resolveAuthoritativeNextSuggestions({}, true), true);

  const chips = extractSessionSuggestions({
    suggestions: [
      { text: 'Safe follow up', risky: false },
      { text: 'git push --force', risky: true }
    ]
  });
  assert.equal(chips.length, 2);
  assert.equal(canDirectSendSuggestion(chips[0]), true);
  assert.equal(canDirectSendSuggestion(chips[1]), false);

  const singleRisky = extractSessionSuggestions({
    suggestion: 'rm -rf build/',
    suggestion_risky: true
  });
  assert.equal(singleRisky.length, 1);
  assert.equal(canDirectSendSuggestion(singleRisky[0]), false);

  const repo = new DemoRepository();
  const initialAssistant = await repo.call('/api/v1/assistant');
  assert.equal(initialAssistant.next_suggestions, true);
  const mainConv = initialAssistant.conversations.find((c) => c.id === 'demo-main');
  const mainChips = extractSessionSuggestions(mainConv);
  assert.equal(mainChips.length, 3);
  assert.equal(mainChips.filter((c) => !c.risky).length, 2);
  assert.equal(mainChips.filter((c) => c.risky).length, 1);

  const disabled = await repo.call('/api/v1/assistant', 'PUT', { next_suggestions: false });
  assert.equal(disabled.next_suggestions, false);
  const mainConvOff = disabled.conversations.find((c) => c.id === 'demo-main');
  assert.equal(extractSessionSuggestions(mainConvOff).length, 0);
});

test('RuleSuggestions parses rules, enforces quiet window, and handles Always/Not Now in DemoRepository', async () => {
  const s = parseRuleSuggestion({
    id: 'rs-1',
    kind: 'task_trust',
    text: 'Always start ReleaseDesk tasks?',
    creates: 'tasks.trust',
    count: 3,
    scope: { machine_id: 'm1', machine: 'My Mac', directory: '/repo' }
  });
  assert.ok(s);
  assert.equal(isRuleSuggestionOffered(s), true);
  assert.equal(ruleSuggestionMadeKind(s), 'task_trust');
  assert.equal(ruleSuggestionAlwaysPath('a/b', 'mgr'), '/api/v1/assistant/rule-suggestions/a%2Fb/always?manager=mgr');
  assert.equal(ruleSuggestionNotNowPath('a/b'), '/api/v1/assistant/rule-suggestions/a%2Fb/not-now');

  const unknown = parseRuleSuggestion({ id: 'rs-x', text: 'Unknown', creates: 'future_kind' });
  assert.equal(isRuleSuggestionOffered(unknown), false);

  const tracker = new RuleSuggestionTracker();
  assert.equal(tracker.lineState(s, 'plan|p1', 'hub1', 1000).status, 'offered');
  tracker.markSending(s.id, 'hub1', 'always');
  assert.equal(tracker.lineState(s, 'plan|p1', 'hub1', 1000).status, 'sending');
  tracker.clearSending(s.id, 'hub1');
  tracker.markMade('plan|p1', 'hub1', s.id, 'Rule added');
  tracker.markSettled(s.id, 'hub1', 1000);
  assert.equal(tracker.lineState(s, 'plan|p1', 'hub1', 2000).status, 'made');
  assert.equal(tracker.lineState(s, 'plan|p2', 'hub1', 2000).status, 'hidden');

  const made = parseRuleSuggestionMade({ kind: 'task_trust', creates: 'tasks.trust' });
  assert.equal(made.creates, 'tasks.trust');

  const repo = new DemoRepository();
  const interactions = await repo.call('/api/v1/interactions');
  assert.equal(interactions.interactions[0].suggestion.id, 'demo-rule-suggest-1');
  const alwaysRes = await repo.call('/api/v1/assistant/rule-suggestions/demo-rule-suggest-1/always', 'POST', {});
  assert.equal(alwaysRes.creates, 'tasks.trust');
  await assert.rejects(
    () => repo.call('/api/v1/assistant/rule-suggestions/demo-rule-suggest-1/not-now', 'POST', {}),
    (err) => err.status === 404
  );
});

test('TranscriptSearch (#16509) handles search, cycling, bookmarks, expiration, and Demo recording previewability', async () => {
  const sampleDetail = {
    meta: {
      started_at: '2026-11-27T08:00:00Z',
      bookmarks: [{ at: 140, label: 'Ship parity pack' }]
    },
    segments: [
      { start: 10, speaker: 'Mina', text: 'Check Release checklist' },
      { start: 140, speaker: 'Alex', text: 'Ship parity pack together' },
      { start: 200, echo: true, speaker: 'Persona', text: 'Ignored echo' }
    ]
  };
  const view = buildTranscriptView(sampleDetail);
  assert.equal(view.segmentCount, 2);
  assert.equal(view.hasBookmarks, true);
  assert.equal(view.rows.length, 3);
  assert.equal(view.rows[1].id, 'recording-line-140');
  assert.equal(view.rows[2].id, 'recording-bookmark-140');

  const matches = findTranscriptMatches(view.rows, 'parity', 'Bookmark');
  assert.equal(matches.length, 2);
  const firstId = stepTranscriptMatch(matches, null, 1);
  const secondId = stepTranscriptMatch(matches, firstId, 1);
  const wrappedId = stepTranscriptMatch(matches, secondId, 1);
  assert.equal(firstId, 'recording-line-140');
  assert.equal(secondId, 'recording-bookmark-140');
  assert.equal(wrappedId, firstId);
  assert.equal(stepTranscriptMatch(matches, firstId, -1), secondId);

  const parts = splitTranscriptMatchParts('Ship Parity pack', 'parity');
  assert.deepEqual(parts, [
    { text: 'Ship ', hit: false },
    { text: 'Parity', hit: true },
    { text: ' pack', hit: false }
  ]);
  assert.equal(filterTranscriptBookmarks(view.rows).length, 1);
  assert.ok(exportTranscriptText(view.rows, 'Bookmark').includes('Bookmark · Ship parity pack'));

  // Expired speech filters spoken segments while preserving retained bookmarks
  const expiredView = buildTranscriptView({
    meta: {
      started_at: '2026-11-27T08:00:00Z',
      trimmed_at: '2026-12-15T00:00:00Z',
      bookmarks: [{ at: 140, label: 'Retained mark' }]
    },
    segments: sampleDetail.segments
  });
  assert.equal(expiredView.rows.length, 1);
  assert.equal(expiredView.rows[0].bookmark, true);
  assert.equal(expiredView.segmentCount, 0);

  // DemoRepository demo-recording-notes has both minutes.debates and retained segments
  const repo = new DemoRepository();
  const recDetail = await repo.call('/api/v1/kb/recordings/demo-recording-notes');
  assert.ok(recDetail.detail.minutes.debates.length >= 2);
  const demoView = buildTranscriptView(recDetail.detail);
  assert.ok(demoView.segmentCount >= 5);
  assert.equal(demoView.hasBookmarks, true);

  // #15985 fail-closed recording module gate remains intact for real hubs
  assert.equal(isRecordingModuleAllowed(['modules', 'module.voice'], null), false);
  assert.equal(isRecordingModuleAllowed(['modules', 'module.recording'], null), true);
  const clientSrc = readEts('models/ClientModel.ets');
  assert.match(clientSrc, /canPreviewDemoRecordings\(\):\s*boolean\s*\{\s*return this\.demo;\s*\}/);
  assert.match(clientSrc, /isRecordingModuleAllowed\(\):\s*boolean\s*\{\s*return isRecordingModuleAllowed\(this\.features,\s*this\.rememberedHubModules\);\s*\}/);
});

test('UI wiring, >=44vp touch targets, zero hex literals, and trilingual string keys are complete', () => {
  const todoPage = readEts('components/TodoWorkspacePage.ets');
  assert.match(todoPage, /TodoStagePill\(/);
  assert.match(todoPage, /TodoStageBar\(/);
  assert.match(todoPage, /TodoDecisionsSection\(/);

  const usageSheet = readEts('components/TokenUsageSheet.ets');
  assert.match(usageSheet, /buildRecentHourlyActivity\(/);
  assert.match(usageSheet, /rankUsagePlaces\(/);
  assert.match(usageSheet, /token_usage_by_agent/);
  assert.match(usageSheet, /token_usage_by_launcher/);
  assert.match(usageSheet, /\(bar: ActivityBarItem\) => bar\.id \+ ':' \+ String\(bar\.tokens\.total\) \+ ':' \+ \(\(pickedId \|\| summary\.peakId\) === bar\.id \? '1' : '0'\)/);

  const conv = readEts('components/Conversation.ets');
  assert.match(conv, /suggestionChipsBar\(/);
  assert.match(conv, /canDirectSendSuggestion\(/);

  const adminPage = readEts('components/AdminWorkspacePage.ets');
  assert.match(adminPage, /this\.workspace\.setNextSuggestions\(this\.workspace\.capture\('\/api\/v1\/assistant'\), on\)/);
  assert.match(adminPage, /if \(on !== Wire\.bool\(this\.workspace\.assistantData, 'next_suggestions'\)\)/);

  const cards = readEts('components/AssistantCards.ets');
  const decisionForm = readEts('components/InteractionDecisionForm.ets');
  assert.match(cards, /RuleSuggestionBar\(/);
  assert.match(decisionForm, /RuleSuggestionBar\(/);
  const agentCardSlice = decisionForm.slice(
    decisionForm.indexOf('@Builder agentDecisionCard()'),
    decisionForm.indexOf('@Builder responseFooter()')
  );
  assert.match(agentCardSlice, /RuleSuggestionBar\(/);

  const kbPage = readEts('components/KnowledgeWorkspacePage.ets');
  assert.match(kbPage, /canPreviewDemoRecordings\(/);
  assert.match(kbPage, /knowledge\.recording\.review_toggle/);
  assert.match(kbPage, /knowledge\.recording\.search/);
  assert.match(kbPage, /\(part: TranscriptMatchPart, partIdx: number\) => String\(partIdx\) \+ ':' \+ \(part\.hit \? '1' : '0'\) \+ ':' \+ part\.text/);

  const requiredKeys = [
    'todo_stage_plan', 'todo_stage_develop', 'todo_stage_review', 'todo_stage_merge',
    'todo_stage_deploy', 'todo_stage_verify', 'todo_stage_done', 'todo_stage_step_summary',
    'todo_decisions_title', 'todo_decision_revise', 'todo_decisions_full_history',
    'token_usage_recent_hourly', 'token_usage_by_agent', 'token_usage_by_launcher', 'token_usage_by_place',
    'next_suggestions_title', 'suggestion_use', 'suggestion_fill_in', 'suggestion_send_now',
    'rule_suggestion_always', 'rule_suggestion_not_now',
    'recording_bookmark', 'recording_transcript_review', 'recording_transcript_find', 'recording_transcript_copy'
  ];

  for (const loc of ['base', 'en_US', 'zh_CN', 'ja_JP']) {
    const map = new Map(readStrings(loc).string.map((entry) => [entry.name, entry.value]));
    for (const key of requiredKeys) {
      const val = map.get(key);
      assert.ok(typeof val === 'string' && val.trim().length > 0, `${loc} missing ${key}`);
    }
  }
});
