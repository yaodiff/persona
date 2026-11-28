import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

const stubs = new Map([
  ['craft-stub:transport', `
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
  ['craft-stub:local-store', 'export class LocalStore {}'],
  ['craft-stub:mutation-store', 'export class MutationRecord {} export class MutationStore { static shared() { throw new Error("demo_mutation_store_must_not_be_used"); } }'],
  ['craft-stub:arkts', 'export const util = { generateRandomUUID: () => crypto.randomUUID(), TextDecoder: { create: () => ({ decodeToString: () => "" }) } };'],
  ['craft-stub:client', 'export class ClientModel {}']
]);

registerHooks({
  resolve(specifier, context, next) {
    if (stubs.has(specifier)) return { url: specifier, shortCircuit: true };
    if (specifier === '../platform/HubTransport') return { url: 'craft-stub:transport', shortCircuit: true };
    if (specifier === '../platform/LocalStore') return { url: 'craft-stub:local-store', shortCircuit: true };
    if (specifier === './MutationStore') return { url: 'craft-stub:mutation-store', shortCircuit: true };
    if (specifier === '@kit.ArkTS') return { url: 'craft-stub:arkts', shortCircuit: true };
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/TodoWorkspace.ets')) {
      return { url: 'craft-stub:client', shortCircuit: true };
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

const { extractComposerQueue } = await import('../entry/src/main/ets/core/ComposerQueue.ts');
const { extractTurnChangesFromRaw, extractWorkStepsFromRaw } = await import('../entry/src/main/ets/core/WorkPresentation.ts');
const { projectToolTurn } = await import('../entry/src/main/ets/core/Domain.ts');
const { parseMarkdown } = await import('../entry/src/main/ets/core/Markdown.ts');
const { DemoRepository } = await import('../entry/src/main/ets/models/DemoRepository.ets');
const { assistantTrayRows, TrayRequest } = await import('../entry/src/main/ets/models/AssistantTrays.ets');

const read = (rel) => readFileSync(new URL('../entry/src/main/ets/' + rel, import.meta.url), 'utf8');
const readResource = (locale) => JSON.parse(readFileSync(new URL('../entry/src/main/resources/' + locale + '/element/string.json', import.meta.url), 'utf8'));

test('extractComposerQueue aligns with Hub queued_relayed, capabilities.queue_send_now, Claude-in-pane, and malformed inputs', () => {
  // Empty / malformed inputs
  for (const bad of [undefined, null, {}, { queued: 'not-an-array' }, { queued: [null, 42, '   ', { text: '  ' }] }]) {
    assert.deepEqual(extractComposerQueue(bad), {
      items: [],
      ownCount: 0,
      relayedCount: 0,
      preview: '',
      steersQueue: false
    });
  }

  // Real Hub wire: queued_relayed marks trailing N items as relayed; Claude in a terminal pane steersQueue
  const claudePane = extractComposerQueue({
    queued: ['  Include Mate XT 2\n  tri-fold notes  ', '', 'Double-check zh_CN strings', 'Relayed from Release room'],
    queued_relayed: 1,
    pane_id: 'herdr-pane-1',
    agent: 'claude',
    capabilities: { send: true }
  });
  assert.equal(claudePane.items.length, 3);
  assert.equal(claudePane.ownCount, 2);
  assert.equal(claudePane.relayedCount, 1);
  assert.equal(claudePane.items[0].relayed, false);
  assert.equal(claudePane.items[1].relayed, false);
  assert.equal(claudePane.items[2].relayed, true);
  assert.equal(claudePane.preview, 'Include Mate XT 2 tri-fold notes');
  assert.equal(claudePane.steersQueue, true);

  // Clamping queued_relayed > items.length and negative queued_relayed
  const allRelayed = extractComposerQueue({
    queued: ['Relayed A', 'Relayed B'],
    queued_relayed: 99,
    assistant: true,
    capabilities: { send: true }
  });
  assert.equal(allRelayed.ownCount, 0);
  assert.equal(allRelayed.relayedCount, 2);
  assert.equal(allRelayed.items.every((item) => item.relayed), true);
  assert.equal(allRelayed.steersQueue, true);

  const clampedNeg = extractComposerQueue({
    queued: ['Own A'],
    queued_relayed: -3,
    assistant: true,
    capabilities: { send: false }
  });
  assert.equal(clampedNeg.ownCount, 1);
  assert.equal(clampedNeg.relayedCount, 0);
  assert.equal(clampedNeg.steersQueue, false);

  // Codex without queue_send_now cannot steer queue; with capabilities.queue_send_now it can
  const codexDefault = extractComposerQueue({
    queued: ['Check diffs'],
    agent: 'codex',
    pane_id: 'herdr-pane-2',
    capabilities: { send: true }
  });
  assert.equal(codexDefault.steersQueue, false);

  const codexCap = extractComposerQueue({
    queued: ['Check diffs'],
    agent: 'codex',
    source_kind: 'live',
    capabilities: { send: true, queue_send_now: true }
  });
  assert.equal(codexCap.steersQueue, true);

  // Claude outside terminal (source_kind set, no queue_send_now) cannot steer queue
  const claudeDesktop = extractComposerQueue({
    queued: ['Wait'],
    agent: 'claude',
    source_kind: 'claude_desktop',
    pane_id: 'pane-1',
    capabilities: { send: true }
  });
  assert.equal(claudeDesktop.steersQueue, false);

  // Colliding fallback & duplicate explicit IDs always produce unique item IDs
  const uniqueCheck = extractComposerQueue({
    queued: [
      { id: 'idx-1', body: 'First' },
      'Second',
      { id: 'dup', text: 'Third' },
      { id: 'dup', text: 'Fourth' }
    ]
  });
  const ids = uniqueCheck.items.map((item) => item.id);
  assert.equal(new Set(ids).size, 4);
  assert.equal(ids[0], 'idx-1');
});

test('DemoRepository exposes live main counts (Needs You 1, In Progress 1), wire-aligned changelog queue, work steps, code block, interrupt, and 409 nothing_queued', async () => {
  const repo = new DemoRepository();
  const roster = repo.roster();

  // 1. demo-main status bar counts via assistantTrayRows
  const feed = await repo.call('/api/v1/assistant/conversations/demo-main/feed?paginate=1&manager=demo');
  assert.ok(Array.isArray(feed.task_cards));
  const todosResp = await repo.call('/api/v1/todos?manager=demo');
  const interactionsResp = await repo.call('/api/v1/interactions');
  const requests = interactionsResp.interactions.map((item) => {
    const inner = item.interaction || {};
    return new TrayRequest(item.machine_id, item.session_id, item.id, inner.plan || '', 0, false, false);
  });
  const trayRows = assistantTrayRows(
    todosResp.todos,
    roster.sessions,
    feed.items || [],
    feed.plans || {},
    requests,
    'demo',
    'demo',
    'demo-main',
    'demo-main',
    Date.parse('2026-11-27T09:05:00Z')
  );
  assert.equal(trayRows.filter((row) => row.bucket === 'needs').length, 1);
  assert.equal(trayRows.filter((row) => row.bucket === 'progress').length, 1);

  // 2. demo-agent-changelog real Hub wire fields & queue state
  const changelog = roster.sessions.find((row) => row.id === 'demo-agent-changelog');
  assert.ok(changelog);
  const rawChangelog = changelog.data();
  assert.equal(rawChangelog.queued_relayed, 0);
  assert.equal(rawChangelog.capabilities?.queue_send_now, true);
  const queue = extractComposerQueue(rawChangelog);
  assert.deepEqual(queue.items.map((item) => item.text), [
    'Include Mate XT 2 tri-fold notes',
    'Double-check zh_CN strings'
  ]);
  assert.equal(queue.ownCount, 2);
  assert.equal(queue.relayedCount, 0);
  assert.equal(queue.steersQueue, true);

  // 3. demo-agent-changelog transcript has work steps, changed files, and fenced TypeScript code block
  const changelogItems = repo.historyItems(changelog);
  const turn = projectToolTurn(changelogItems[0].id, changelogItems, false);
  assert.equal(turn.summaryVisible, true);
  assert.ok(turn.toolCount >= 3);
  assert.ok(turn.thoughtCount >= 1);
  const steps = extractWorkStepsFromRaw(JSON.stringify(changelogItems), false);
  assert.ok(steps.length >= 4);
  const changes = extractTurnChangesFromRaw(JSON.stringify(changelogItems));
  assert.ok(changes && changes.files.length >= 1);
  const blocks = parseMarkdown(turn.visibleAssistantText?.text || '');
  assert.ok(blocks.some((b) => b.kind === 'code' && b.language === 'typescript'));

  // 4. Interrupt transitions demo-agent-changelog to idle/interrupted with updated last_text
  await repo.call('/api/v1/sessions/demo-mac/demo-agent-changelog/interrupt', 'POST', {});
  const afterInterrupt = (await repo.sessions()).find((row) => row.id === 'demo-agent-changelog');
  assert.equal(afterInterrupt.state, 'idle');
  assert.equal(afterInterrupt.data().status, 'interrupted');
  assert.equal(afterInterrupt.data().last_text, 'Interrupted in local demo.');

  // 5. Send now dequeues queued items cleanly into transcript, and empty queue returns 409 nothing_queued
  await repo.call('/api/v1/sessions/demo-mac/demo-agent-changelog/queue/send-now', 'POST', {}, '', undefined, false, undefined, true);
  const afterSendNow = (await repo.sessions()).find((row) => row.id === 'demo-agent-changelog');
  assert.equal(extractComposerQueue(afterSendNow.data()).items.length, 0);
  const transcriptAfterSendNow = repo.historyItems(afterSendNow);
  assert.ok(transcriptAfterSendNow.some((item) => item.text === 'Include Mate XT 2 tri-fold notes'));
  assert.ok(transcriptAfterSendNow.some((item) => item.text === 'Double-check zh_CN strings'));
  await assert.rejects(
    repo.call('/api/v1/sessions/demo-mac/demo-agent-changelog/queue/send-now', 'POST', {}),
    { status: 409, code: 'nothing_queued' }
  );

  // 6. Posting a message also dequeues remaining queued items cleanly
  repo.seed();
  await repo.call('/api/v1/sessions/demo-mac/demo-agent-changelog/messages', 'POST', {
    text: 'Ship the changelog draft.',
    client_msg_id: 'demo-msg-dequeue'
  });
  const afterPost = (await repo.sessions()).find((row) => row.id === 'demo-agent-changelog');
  assert.equal(extractComposerQueue(afterPost.data()).items.length, 0);
  assert.ok(repo.historyItems(afterPost).some((item) => item.text === 'Ship the changelog draft.'));
});

test('ComposerTopStrip, Conversation, MarkdownText, WorkStepsCard, and localized strings satisfy Stage 1B craft and lifecycle hygiene', () => {
  const strip = read('components/ComposerTopStrip.ets');
  const conversation = read('components/Conversation.ets');
  const markdown = read('components/MarkdownText.ets');
  const workSteps = read('components/WorkStepsCard.ets');

  assert.ok(strip.includes('@ComponentV2'));
  assert.ok(strip.includes("id('composer.queue.strip')"));
  assert.ok(strip.includes("id('composer.queue.toggle')"));
  assert.ok(strip.includes("id('composer.queue.send_now')"));
  assert.ok(strip.includes('this.queue.steersQueue && this.queue.ownCount > 0'));
  assert.ok(strip.includes("id('composer.queue.list')"));
  assert.ok(strip.includes('Colors.divider'));
  assert.ok(strip.includes('Motion.state'));
  assert.ok(strip.includes('Haptics.selection()'));
  assert.ok(strip.includes("toastCenter.show(tr('copied'))"));
  assert.ok(strip.includes("tr('composer_queue_relayed')"));

  // Conversation: syncFeed active in demo, sessions+conversations lookup, target-scoped send-now busy, emptyRequest & admit & reportError
  const syncFeedMatch = conversation.match(/private syncFeed\(\): void \{[\s\S]*?\n  \}/);
  assert.ok(syncFeedMatch);
  assert.doesNotMatch(syncFeedMatch[0], /this\.model\.demo/);
  assert.ok(conversation.includes('ComposerTopStrip'));
  assert.ok(conversation.includes('extractComposerQueue'));
  assert.ok(conversation.includes('this.model.conversations'));
  assert.ok(conversation.includes('this.model.sessions'));
  assert.ok(conversation.includes('queueSendNowTarget'));
  assert.ok(conversation.includes('/queue/send-now'));
  assert.ok(conversation.includes('owner.reportError(error as Error)'));
  assert.ok(conversation.includes('toastCenter.error(tr('));

  // MarkdownText: sync pasteboard + disposed guard + wrappedCodeKeys cleanup/pruning
  assert.ok(markdown.includes('wrappedCodeKeys'));
  assert.ok(markdown.includes('setDataSync('));
  assert.ok(markdown.includes('this.disposed'));
  assert.ok(markdown.includes('this.wrappedCodeKeys = []'));
  assert.ok(markdown.includes("tr(this.isCodeWrapped(blockKey) ? 'code_unwrap_lines' : 'code_wrap_lines')"));
  assert.ok(markdown.includes("id('markdown.code.wrap')"));
  assert.ok(markdown.includes("id('markdown.code.copy')"));
  assert.ok(markdown.includes('Haptics.selection()'));
  assert.ok(markdown.includes("toastCenter.show(tr('copied'))"));
  assert.ok(markdown.includes("tr('copied')"));

  assert.ok(workSteps.includes('Motion.state'));
  assert.ok(workSteps.includes("id('chat.work.timeline.count')"));
  assert.ok(workSteps.includes('Haptics.selection()'));
  assert.ok(workSteps.includes("toastCenter.show(tr('copied'))"));

  const requiredKeys = [
    'composer_queued_count',
    'composer_queue_send_now',
    'composer_queue_relayed',
    'code_wrap_lines',
    'code_unwrap_lines',
    'copied'
  ];
  for (const locale of ['base', 'en_US', 'zh_CN', 'ja_JP']) {
    const names = new Set(readResource(locale).string.map((entry) => entry.name));
    for (const key of requiredKeys) {
      assert.ok(names.has(key), `${locale} missing string key: ${key}`);
    }
  }
});
