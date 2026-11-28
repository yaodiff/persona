import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

const stubs = new Map([
  ['parity-stub:transport', `
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
  ['parity-stub:local-store', 'export class LocalStore {}'],
  ['parity-stub:mutation-store', 'export class MutationRecord {} export class MutationStore { static shared() { throw new Error("demo_mutation_store_must_not_be_used"); } }'],
  ['parity-stub:arkts', 'export const util = { generateRandomUUID: () => crypto.randomUUID(), TextDecoder: { create: () => ({ decodeToString: () => "" }) } };'],
  ['parity-stub:client', 'export class ClientModel {}']
]);

registerHooks({
  resolve(specifier, context, next) {
    if (stubs.has(specifier)) return { url: specifier, shortCircuit: true };
    if (specifier === '../platform/HubTransport') return { url: 'parity-stub:transport', shortCircuit: true };
    if (specifier === '../platform/LocalStore') return { url: 'parity-stub:local-store', shortCircuit: true };
    if (specifier === './MutationStore') return { url: 'parity-stub:mutation-store', shortCircuit: true };
    if (specifier === '@kit.ArkTS') return { url: 'parity-stub:arkts', shortCircuit: true };
    if (specifier === './ClientModel' && context.parentURL?.endsWith('/TodoWorkspace.ets')) {
      return { url: 'parity-stub:client', shortCircuit: true };
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
  canMutateQueueIndex,
  extractComposerQueue,
  mergeQueuedTakeBackDraft,
  queueKeepSwapped,
  queueKeepWithout
} = await import('../entry/src/main/ets/core/ComposerQueue.ts');
const {
  boundStarredInput,
  buildStarredIndexMap,
  filterStarredSnapshots,
  isMessageStarred,
  isPositionalItemId,
  isVerifiedStarSource,
  parseStarredIndexResponse,
  parseStarredListResponse,
  STARRED_TEXT_MAX_SCALARS,
  starredRefId
} = await import('../entry/src/main/ets/core/StarredMessages.ts');
const {
  exportChatToMarkdown,
  truncateExportText
} = await import('../entry/src/main/ets/core/ChatExport.ts');
const { buildQuickCommandGroups } = await import('../entry/src/main/ets/core/QuickCommandPalette.ts');
const { DemoRepository } = await import('../entry/src/main/ets/models/DemoRepository.ets');

const readEts = (rel) => readFileSync(new URL('../entry/src/main/ets/' + rel, import.meta.url), 'utf8');
const readStrings = (locale) => JSON.parse(readFileSync(new URL('../entry/src/main/resources/' + locale + '/element/string.json', import.meta.url), 'utf8'));

test('ComposerQueue delete/reorder/relayed guardrails and draft merge align with iOS QueueSteerTests', () => {
  assert.deepEqual(queueKeepWithout(3, 0), [1, 2]);
  assert.deepEqual(queueKeepWithout(3, 1), [0, 2]);
  assert.deepEqual(queueKeepWithout(1, 0), []);
  assert.deepEqual(queueKeepWithout(2, 2), [0, 1]);
  assert.deepEqual(queueKeepWithout(0, 0), []);

  assert.deepEqual(queueKeepSwapped(3, 1, -1), [1, 0, 2]);
  assert.deepEqual(queueKeepSwapped(3, 1, 1), [0, 2, 1]);
  assert.deepEqual(queueKeepSwapped(3, 0, -1), []);
  assert.deepEqual(queueKeepSwapped(3, 2, 1), []);
  assert.deepEqual(queueKeepSwapped(3, 5, -1), []);
  assert.deepEqual(queueKeepSwapped(1, 0, 1), []);

  assert.equal(canMutateQueueIndex(2, 0), true);
  assert.equal(canMutateQueueIndex(2, 1), true);
  assert.equal(canMutateQueueIndex(2, 2), false);
  assert.equal(canMutateQueueIndex(2, -1), false);
  assert.equal(canMutateQueueIndex(0, 0), false);

  assert.equal(mergeQueuedTakeBackDraft('', 'Pulled item'), 'Pulled item');
  assert.equal(mergeQueuedTakeBackDraft('Existing draft', 'Pulled item'), 'Existing draft\nPulled item');
  assert.equal(mergeQueuedTakeBackDraft('Existing draft', '   '), 'Existing draft');

  const relayState = extractComposerQueue({
    queued: [
      'Own note',
      '来自「日报」（不唤醒）：Check metrics',
      '来自「Release room」：Please review'
    ],
    queued_relayed: 2,
    assistant: true,
    capabilities: { send: true }
  });
  assert.equal(relayState.ownCount, 1);
  assert.equal(relayState.relayedCount, 2);
  assert.equal(relayState.items[0].noWake, false);
  assert.equal(relayState.items[1].relayed, true);
  assert.equal(relayState.items[1].sender, '日报');
  assert.equal(relayState.items[1].noWake, true);
  assert.equal(relayState.items[1].text, 'Check metrics');
  assert.equal(relayState.items[2].relayed, true);
  assert.equal(relayState.items[2].sender, 'Release room');
  assert.equal(relayState.items[2].noWake, false);
  assert.equal(relayState.items[2].text, 'Please review');
});

test('StarredMessages index lookup, positional fingerprint verification, bounding, and multi-term search match Hub/iOS', () => {
  assert.equal(isPositionalItemId('line12#0'), true);
  assert.equal(isPositionalItemId('demo-item-1'), false);

  const validPos = {
    machine_id: 'm1',
    session_id: 's1',
    item_id: 'line4#0',
    conversation: 'agent',
    source_fingerprint: '0123456789abcdef0123456789abcdef'
  };
  const invalidPos = {
    machine_id: 'm1',
    session_id: 's1',
    item_id: 'line4#0',
    conversation: 'agent'
  };
  assert.equal(isVerifiedStarSource(validPos), true);
  assert.equal(isVerifiedStarSource(invalidPos), false);

  const parsedIndex = parseStarredIndexResponse({
    can_write: true,
    refs: [
      { id: 'sm1', ...validPos },
      { id: 'sm2', ...invalidPos },
      { id: 'sm3', machine_id: 'm1', session_id: 's2', item_id: 'uuid#0', conversation: 'assistant' }
    ]
  });
  assert.equal(parsedIndex.canWrite, true);
  assert.equal(parsedIndex.refs.length, 3);

  const indexMap = buildStarredIndexMap(parsedIndex.refs);
  assert.equal(isMessageStarred(indexMap, validPos), true);
  assert.equal(starredRefId(indexMap, validPos), 'sm1');
  assert.equal(isMessageStarred(indexMap, invalidPos), false);
  assert.equal(isMessageStarred(indexMap, {
    machine_id: 'm1',
    session_id: 's2',
    item_id: 'uuid#0',
    conversation: 'assistant'
  }), true);

  const parsedList = parseStarredListResponse({
    can_write: true,
    stars: [
      {
        id: 'sm1',
        machine_id: 'm1',
        session_id: 's1',
        item_id: 'uuid-1#0',
        conversation: 'agent',
        role: 'assistant',
        title: 'Release Checklist',
        speaker: 'Claude',
        text: 'All 42 host suites passed.'
      },
      {
        id: 'sm2',
        machine_id: 'm1',
        session_id: 's2',
        item_id: 'uuid-2#0',
        conversation: 'assistant',
        role: 'user',
        title: 'Daily Planning',
        speaker: '',
        text: 'Draft the HarmonyOS parity roadmap.'
      }
    ]
  });
  assert.equal(parsedList.stars.length, 2);
  assert.equal(filterStarredSnapshots(parsedList.stars, '').length, 2);
  assert.equal(filterStarredSnapshots(parsedList.stars, 'claude 42').length, 1);
  assert.equal(filterStarredSnapshots(parsedList.stars, 'me roadmap', 'Me', 'Assistant')[0].id, 'sm2');
  assert.equal(filterStarredSnapshots(parsedList.stars, 'nonexistent').length, 0);

  const longText = 'a'.repeat(STARRED_TEXT_MAX_SCALARS + 25);
  const bounded = boundStarredInput({
    id: 'ignore-me',
    starred_at: '2026-11-27T00:00:00Z',
    machine_id: 'm1',
    session_id: 's1',
    item_id: 'uuid-1#0',
    conversation: 'assistant',
    role: 'assistant',
    title: 'Title',
    speaker: 'Persona',
    text: longText
  });
  assert.equal(bounded.id, undefined);
  assert.equal(bounded.starred_at, undefined);
  assert.equal(Array.from(bounded.text).length, STARRED_TEXT_MAX_SCALARS);
  assert.equal(bounded.omitted_chars, 25);
});

test('ChatExport formats Markdown headers, code blocks, timestamps, work steps, and truncation notices cleanly', () => {
  const sampleMessages = [
    { id: 'u1', role: 'user', text: 'Check the release gate.', at: Date.UTC(2026, 10, 27, 9, 0) },
    { id: 'w1', role: 'work', text: '', work: 'Read /demo/ReleaseDesk/release.ts', toolCount: 1, thoughtCount: 1, at: Date.UTC(2026, 10, 27, 9, 1) },
    { id: 'a1', role: 'assistant', text: 'Done:\n\n```ts\nexport const ok = true;\n```', at: Date.UTC(2026, 10, 27, 9, 2) }
  ];

  const withoutWork = exportChatToMarkdown(sampleMessages, {
    title: 'Release Audit',
    machineName: 'My Mac',
    modelName: 'claude-sonnet-4-6',
    userSpeaker: 'Me',
    assistantSpeaker: 'Claude',
    includeTimestamps: true,
    includeWorkSteps: false
  });
  assert.equal(withoutWork.messageCount, 2);
  assert.equal(withoutWork.workStepCount, 0);
  assert.match(withoutWork.markdown, /^# Release Audit/);
  assert.match(withoutWork.markdown, /> My Mac · claude-sonnet-4-6/);
  assert.match(withoutWork.markdown, /## Me · 2026-11-27 09:00 UTC/);
  assert.match(withoutWork.markdown, /```ts\nexport const ok = true;\n```/);
  assert.equal(withoutWork.markdown.includes('Read /demo/ReleaseDesk/release.ts'), false);

  const withWorkNoTime = exportChatToMarkdown(sampleMessages, {
    title: 'Release Audit',
    workLabel: 'Work steps',
    includeTimestamps: false,
    includeWorkSteps: true
  });
  assert.equal(withWorkNoTime.workStepCount, 1);
  assert.match(withWorkNoTime.markdown, /### Work steps\n\nRead \/demo\/ReleaseDesk\/release\.ts/);
  assert.equal(withWorkNoTime.markdown.includes('2026-11-27'), false);

  const clipped = truncateExportText('```ts\nconst x = 1234567890;\n```', 18, '{omitted} chars cut');
  assert.equal(clipped.truncated, true);
  assert.match(clipped.text, /```[\s\S]*_.*chars cut_/);
});

test('QuickCommandPalette exposes opt-in starred, archived, and export actions across locales', () => {
  const groups = buildQuickCommandGroups('', [], {
    showStarred: true,
    showArchived: true,
    showExportChat: true
  });
  const actionIds = groups.find((g) => g.category === 'actions').entries.map((e) => e.id);
  assert.ok(actionIds.includes('action:open_starred'));
  assert.ok(actionIds.includes('action:open_archived'));
  assert.ok(actionIds.includes('action:export_chat'));

  const starQuery = buildQuickCommandGroups('星标', [], { showStarred: true, showArchived: true, showExportChat: true });
  assert.deepEqual(starQuery.find((g) => g.category === 'actions').entries.map((e) => e.id), ['action:open_starred']);
});

test('DemoRepository supports /queue edit+reorder, /assistant/starred CRUD, /archive+/unarchive, and /fork', async () => {
  const repo = new DemoRepository();
  const roster = repo.roster();
  assert.ok(roster.features.includes('starred_messages'));

  // 1. Queue reorder & delete preserving queue semantics
  const changelogBefore = (await repo.sessions()).find((r) => r.id === 'demo-agent-changelog');
  assert.deepEqual(changelogBefore.data().queued, ['Include Mate XT 2 tri-fold notes', 'Double-check zh_CN strings']);
  const movedResp = await repo.call('/api/v1/sessions/demo-mac/demo-agent-changelog/queue', 'POST', { keep: [1, 0] });
  assert.deepEqual(movedResp.queue, ['Include Mate XT 2 tri-fold notes', 'Double-check zh_CN strings']);
  const changelogMoved = (await repo.sessions()).find((r) => r.id === 'demo-agent-changelog');
  assert.deepEqual(changelogMoved.data().queued, ['Double-check zh_CN strings', 'Include Mate XT 2 tri-fold notes']);

  const deletedResp = await repo.call('/api/v1/sessions/demo-mac/demo-agent-changelog/queue', 'POST', { keep: [1] });
  assert.deepEqual(deletedResp.queue, ['Double-check zh_CN strings', 'Include Mate XT 2 tri-fold notes']);
  const changelogAfterDelete = (await repo.sessions()).find((r) => r.id === 'demo-agent-changelog');
  assert.deepEqual(changelogAfterDelete.data().queued, ['Include Mate XT 2 tri-fold notes']);

  await assert.rejects(
    repo.call('/api/v1/sessions/demo-mac/demo-agent-changelog/queue', 'POST', { keep: [5] }),
    { status: 400, code: 'demo_invalid_input' }
  );

  // 2. Starred messages CRUD
  const indexBefore = await repo.call('/api/v1/assistant/starred?index=1');
  assert.equal(indexBefore.can_write, true);
  assert.ok(indexBefore.refs.length >= 2);

  const listBefore = await repo.call('/api/v1/assistant/starred');
  assert.equal(listBefore.stars.length, indexBefore.refs.length);

  const added = await repo.call('/api/v1/assistant/starred', 'POST', {
    machine_id: 'demo-mac',
    session_id: 'demo-agent-notes',
    item_id: 'demo-item-99',
    conversation: 'agent',
    role: 'assistant',
    title: 'Organize project notes',
    speaker: 'Claude',
    text: 'Architecture summary'
  });
  assert.ok(added.star?.id);
  const removed = await repo.call('/api/v1/assistant/starred/' + encodeURIComponent(added.star.id), 'DELETE', {});
  assert.equal(removed.removed?.id, added.star.id);

  // 3. Archive / unarchive conversations
  const assistantData = await repo.call('/api/v1/assistant');
  const archivedConv = assistantData.conversations.find((c) => c.id === 'demo-archived-notes');
  assert.equal(archivedConv?.archived, true);
  await repo.call('/api/v1/assistant/conversations/demo-archived-notes/unarchive', 'POST', {});
  const afterUnarchive = (await repo.call('/api/v1/assistant')).conversations.find((c) => c.id === 'demo-archived-notes');
  assert.equal(afterUnarchive?.archived, false);
  await repo.call('/api/v1/assistant/conversations/demo-archived-notes/archive', 'POST', {});
  const afterReArchive = (await repo.call('/api/v1/assistant')).conversations.find((c) => c.id === 'demo-archived-notes');
  assert.equal(afterReArchive?.archived, true);
  await assert.rejects(
    repo.call('/api/v1/assistant/conversations/demo-main/archive', 'POST', {}),
    { status: 400, code: 'cannot_archive_main' }
  );

  // 4. Session / conversation fork
  const forked = await repo.call('/api/v1/sessions/demo-mac/demo-agent-review/fork', 'POST', {});
  assert.ok(forked.session_id && forked.session_id !== 'demo-agent-review');
  const forkedSession = (await repo.sessions()).find((r) => r.id === forked.session_id);
  assert.ok(forkedSession);
  assert.ok(repo.historyItems(forkedSession).length >= 2);
  await assert.rejects(
    repo.call('/api/v1/sessions/demo-mac/demo-main/fork', 'POST', {}),
    { status: 400, code: 'fork_unsupported' }
  );
});

test('New sheets have zero hardcoded hex literals, P2 cleanup applied, and trilingual strings complete', () => {
  const zeroHexFiles = [
    'components/StarredMessagesSheet.ets',
    'components/ChatExportSheet.ets',
    'components/ArchivedConversationsSheet.ets',
    'components/ComposerTopStrip.ets',
    'design/NormalizedPrimitives.ets'
  ];
  for (const file of zeroHexFiles) {
    const src = readEts(file);
    const matches = src.match(/#[0-9A-Fa-f]{6,8}\b/g) || [];
    assert.deepEqual(matches, [], `${file} contains hardcoded hex literals: ${matches.join(', ')}`);
  }

  // Wave 1 P2 cleanup check
  const primitives = readEts('design/NormalizedPrimitives.ets');
  const headerBlock = primitives.slice(primitives.indexOf('export struct PersonaSheetHeader'), primitives.indexOf('export struct PersonaStatusPill'));
  assert.equal(headerBlock.includes('private env:'), false, 'PersonaSheetHeader should not retain unused env field');

  // Menu ordering check: export_chat and fork_continue_new_chat placed before stop in Index.ets
  const indexSrc = readEts('pages/Index.ets');
  const showActionsBlock = indexSrc.slice(indexSrc.indexOf('private showChatActions('), indexSrc.indexOf('private syncConversationRows('));
  assert.ok(showActionsBlock.indexOf("tr('export_chat')") < showActionsBlock.indexOf("tr('stop')"));
  assert.ok(showActionsBlock.indexOf("tr('fork_continue_new_chat')") < showActionsBlock.indexOf("tr('stop')"));

  // Trilingual completeness for all new keys
  const requiredKeys = [
    'composer_queue_edit', 'composer_queue_move_up', 'composer_queue_move_down',
    'composer_queue_taken_back', 'composer_queue_deleted',
    'composer_queue_arrives_turn_end', 'composer_queue_arrives_next',
    'starred_messages', 'star_message', 'unstar_message', 'starred_badge',
    'starred_saved_toast', 'starred_removed_toast', 'starred_search_placeholder',
    'starred_empty_title', 'starred_empty_message', 'starred_unavailable_title',
    'starred_unavailable_message', 'starred_load_failed', 'starred_search_empty',
    'starred_open_original', 'starred_original_unavailable', 'starred_original_check_failed',
    'starred_omitted_chars',
    'export_chat', 'export_include_timestamps', 'export_include_work_steps',
    'export_preview_count', 'export_truncated_badge', 'export_truncated_notice',
    'export_copy_markdown', 'export_save_share', 'export_copied_toast',
    'archived_chats', 'unarchive', 'unarchived_toast',
    'archived_search_placeholder', 'archived_empty_title', 'archived_empty_message',
    'archived_search_empty',
    'fork_continue_new_chat', 'fork_chat_done_toast', 'undo', 'assistant'
  ];

  for (const locale of ['base', 'zh_CN', 'ja_JP']) {
    const map = new Map(readStrings(locale).string.map((e) => [e.name, e.value]));
    for (const key of requiredKeys) {
      assert.ok(map.has(key) && String(map.get(key)).trim().length > 0, `${locale} missing string key: ${key}`);
    }
  }
});
