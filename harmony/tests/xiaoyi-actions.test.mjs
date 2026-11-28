import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import {
  XIAOYI_OUTBOX_MAX_ITEMS,
  XIAOYI_OUTBOX_TTL_MS,
  buildApprovalPayload,
  buildPushDedupKey,
  buildPushItem,
  enqueueOutboxItem,
  extractLatestAssistantAnswer,
  extractPublishedModules,
  formatStatusSummary,
  formatXiaoYiError,
  isRecordingModuleAllowed,
  parseStoredHubModules,
  pruneOutboxItems,
  recordingModuleRefusalText,
  selectLiveSession,
  selectMachine,
  selectPendingApprovals,
  serializeStoredHubModules,
  takeOutboxBatch
} from '../entry/src/main/ets/core/XiaoYiActions.ts';

function session(overrides = {}) {
  return {
    id: 's1',
    machineId: 'm1',
    title: 'Build feature',
    status: 'working',
    state: 'working',
    updatedAt: '2026-11-26T06:00:00Z',
    assistant: false,
    quiet: false,
    muted: false,
    spaceName: 'persona',
    unreadCount: 0,
    canSend: true,
    ...overrides
  };
}

function interaction(overrides = {}) {
  return {
    id: 'i1',
    machineId: 'm1',
    sessionId: 's1',
    kind: 'permission',
    status: 'open',
    summary: 'Run tests',
    toolName: 'Bash',
    createdAt: '2026-11-26T06:00:00Z',
    delegating: false,
    muted: false,
    quiet: false,
    planThen: '',
    hasRequiredFields: false,
    ...overrides
  };
}

test('selectPendingApprovals skips quiet/muted/delegated approvals and orders oldest-first', () => {
  const sessions = [
    session({ id: 's1', machineId: 'm1', spaceName: 'active' }),
    session({ id: 's-quiet', machineId: 'm1', quiet: true, spaceName: 'active' }),
    session({ id: 's-muted-space', machineId: 'm1', spaceName: 'noisy' }),
    session({ id: 's-allowed', machineId: 'm1', spaceName: 'noisy' })
  ];
  const mutes = { spaces: ['m1/noisy'], sessions: ['m1/s-explicit'], allowed: ['m1/s-allowed'] };
  const interactions = [
    interaction({ id: 'newer', sessionId: 's1', createdAt: '2026-11-26T06:05:00Z' }),
    interaction({ id: 'oldest', sessionId: 's-allowed', createdAt: '2026-11-26T06:01:00Z' }),
    interaction({ id: 'quiet-card', sessionId: 's-quiet', createdAt: '2026-11-26T05:59:00Z' }),
    interaction({ id: 'muted-space-card', sessionId: 's-muted-space', createdAt: '2026-11-26T05:58:00Z' }),
    interaction({ id: 'delegated-perm', sessionId: 's1', delegating: true, kind: 'permission', createdAt: '2026-11-26T05:57:00Z' }),
    interaction({ id: 'delegated-question', sessionId: 's1', delegating: true, kind: 'question', createdAt: '2026-11-26T06:02:00Z' }),
    interaction({ id: 'closed-card', sessionId: 's1', status: 'resolved', createdAt: '2026-11-26T05:50:00Z' })
  ];
  const ordered = selectPendingApprovals(interactions, sessions, mutes);
  assert.deepEqual(ordered.map((i) => i.id), ['oldest', 'delegated-question', 'newer']);
});

test('buildApprovalPayload permits safe permission/plan approvals and refuses bypassPermissions, secrets, and questions', () => {
  const perm = buildApprovalPayload(interaction({ kind: 'permission' }));
  assert.equal(perm.kind, 'resolve');
  assert.deepEqual(perm.body, { allow: true });

  const secretPerm = buildApprovalPayload(interaction({ kind: 'permission', toolName: 'secret_run' }));
  assert.equal(secretPerm.kind, 'needs_screen');

  const requiredFieldPerm = buildApprovalPayload(interaction({ kind: 'permission', hasRequiredFields: true }));
  assert.equal(requiredFieldPerm.kind, 'needs_screen');

  const plan = buildApprovalPayload(interaction({ kind: 'plan', planThen: '' }));
  assert.equal(plan.kind, 'resolve');
  assert.deepEqual(plan.body, { approve: true, mode: 'acceptEdits' });

  const bypassPlan = buildApprovalPayload(interaction({ kind: 'plan', planThen: 'bypassPermissions' }));
  assert.equal(bypassPlan.kind, 'needs_screen');

  const question = buildApprovalPayload(interaction({ kind: 'question' }));
  assert.equal(question.kind, 'needs_screen');

  const screen = buildApprovalPayload(interaction({ kind: 'screen' }));
  assert.equal(screen.kind, 'needs_screen');
});

test('selectMachine obeys explicit fuzzy match without silent switching, then remembered, then first online trusted', () => {
  const machines = [
    { id: 'm-pending', name: 'Untrusted', online: true, trusted: false, manager: '' },
    { id: 'm-off', name: 'Office Mac', online: false, trusted: true, manager: '' },
    { id: 'm-home', name: 'Home Studio', online: true, trusted: true, manager: '' },
    { id: 'm-work', name: 'Work Laptop', online: true, trusted: true, manager: '' }
  ];

  const offlinePick = selectMachine(machines, 'Office');
  assert.equal(offlinePick.ok, false);
  assert.equal(offlinePick.errorCode, 'xiaoyi_machine_offline');
  assert.equal(offlinePick.target, 'Office Mac');

  const missingPick = selectMachine(machines, 'NonExistent');
  assert.equal(missingPick.ok, false);
  assert.equal(missingPick.errorCode, 'xiaoyi_machine_not_found');

  const explicit = selectMachine(machines, 'laptop');
  assert.equal(explicit.ok, true);
  assert.equal(explicit.machine?.id, 'm-work');

  const remembered = selectMachine(machines, '', 'm-work');
  assert.equal(remembered.ok, true);
  assert.equal(remembered.machine?.id, 'm-work');

  const fallback = selectMachine(machines, '', 'm-off');
  assert.equal(fallback.ok, true);
  assert.equal(fallback.machine?.id, 'm-home');

  const none = selectMachine([machines[0], machines[1]]);
  assert.equal(none.ok, false);
  assert.equal(none.errorCode, 'xiaoyi_no_online_machine');
});

test('selectLiveSession picks fuzzy match or newest sendable live non-assistant session', () => {
  const sessions = [
    session({ id: 'assistant-1', assistant: true, updatedAt: '2026-11-26T09:00:00Z' }),
    session({ id: 's-readonly', title: 'Read only newest', canSend: false, updatedAt: '2026-11-26T08:30:00Z' }),
    session({ id: 's-offline', status: 'offline', state: 'offline', updatedAt: '2026-11-26T08:00:00Z' }),
    session({ id: 's-old', title: 'Docs cleanup', updatedAt: '2026-11-26T06:00:00Z' }),
    session({ id: 's-new', title: 'XiaoYi integration', updatedAt: '2026-11-26T07:00:00Z' })
  ];
  const latest = selectLiveSession(sessions, 'm1');
  assert.equal(latest.ok, true);
  assert.equal(latest.session?.id, 's-new');

  const named = selectLiveSession(sessions, 'm1', 'docs');
  assert.equal(named.ok, true);
  assert.equal(named.session?.id, 's-old');

  const missing = selectLiveSession(sessions, 'm1', 'missing');
  assert.equal(missing.ok, false);
  assert.equal(missing.errorCode, 'xiaoyi_no_live_session');
});

test('formatStatusSummary renders crisp localized output in en, zh-Hans, and ja', () => {
  const s = [session({ id: 's1', state: 'working' }), session({ id: 's2', state: 'idle', status: 'idle' })];
  const p = [interaction({ id: 'i1', summary: 'Approve unit tests' })];
  const en = formatStatusSummary(s, p, 2, 'en');
  assert.equal(en.workingCount, 1);
  assert.equal(en.pendingCount, 1);
  assert.ok(en.headline.includes('1 working'));
  assert.ok(en.headline.includes('1 need you'));
  assert.ok(en.detail.includes('Approve unit tests'));

  const zh = formatStatusSummary(s, p, 0, 'zh-Hans');
  assert.ok(zh.headline.includes('1 个任务进行中'));
  assert.ok(zh.detail.includes('Approve unit tests'));

  const ja = formatStatusSummary([], [], 0, 'ja');
  assert.equal(ja.workingCount, 0);
  assert.ok(ja.headline.length > 0);
});

test('buildPushDedupKey matches SHA-256 16-byte prefix and outbox enforces dedup, 7-day TTL, and 200 cap', () => {
  const ts = Date.parse('2026-11-26T06:15:32Z');
  const minute = Math.floor(ts / 60000);
  const expected = createHash('sha256')
    .update(['Mail', 'Hello', 'World', String(minute)].join('\u0000'))
    .digest('hex')
    .slice(0, 32);
  assert.equal(buildPushDedupKey('Mail', 'Hello', 'World', ts), expected);
  // Same minute => identical dedup key
  assert.equal(buildPushDedupKey('Mail', 'Hello', 'World', ts + 15000), expected);

  const item = buildPushItem('', 'First line\nSecond line', 'Mail', 'https://example.com/1', ts);
  assert.ok(item);
  assert.equal(item.title, 'First line');
  assert.equal(item.link, 'https://example.com/1');
  assert.equal(buildPushItem('', '', 'Mail'), undefined);

  const old = buildPushItem('Old', 'Body', 'App', '', ts - XIAOYI_OUTBOX_TTL_MS - 1000);
  assert.ok(old);
  let queue = [old];
  for (let i = 0; i < XIAOYI_OUTBOX_MAX_ITEMS + 15; i++) {
    const next = buildPushItem(`Item ${i}`, 'Body', 'App', '', ts + i * 60000);
    assert.ok(next);
    queue = enqueueOutboxItem(queue, next, ts + (XIAOYI_OUTBOX_MAX_ITEMS + 20) * 60000);
  }
  assert.equal(queue.length, XIAOYI_OUTBOX_MAX_ITEMS);
  assert.equal(pruneOutboxItems([old], ts).length, 0);
  assert.equal(takeOutboxBatch(queue).length, 20);
});

test('extractLatestAssistantAnswer skips non-assistant items and collects ordered deltas after afterSeq', () => {
  const entries = [
    { seq: 1, kind: 'text', role: 'assistant', text: 'Old answer' },
    { seq: 2, kind: 'text', role: 'user', text: 'New question' },
    { seq: 3, kind: 'thinking', role: 'assistant', text: 'Internal thought' },
    { seq: 4, kind: 'tool_use', role: 'assistant', text: 'search' },
    { seq: 5, kind: 'text', role: 'assistant', text: 'Part 1' },
    { seq: 6, kind: 'text', role: 'assistant', text: 'Part 2' }
  ];
  const res = extractLatestAssistantAnswer(entries, 2);
  assert.equal(res.hasReply, true);
  assert.equal(res.maxSeq, 6);
  assert.equal(res.text, 'Part 1\n\nPart 2');
  assert.deepEqual(res.segments, ['Part 1', 'Part 2']);
});

test('formatXiaoYiError maps known and fallback codes cleanly', () => {
  assert.equal(formatXiaoYiError('xiaoyi_unlock_required').key, 'xiaoyi_unlock_required');
  assert.equal(formatXiaoYiError('xiaoyi_recording_module_off').key, 'xiaoyi_recording_module_off');
  assert.equal(formatXiaoYiError('xiaoyi_ssh_open_app', 'hub.example').host, 'hub.example');
  assert.equal(formatXiaoYiError('offline').key, 'xiaoyi_offline');
  assert.equal(formatXiaoYiError('something_unexpected').key, 'xiaoyi_failed');
});

test('isRecordingModuleAllowed follows upstream HubModules (#15978 / #15985) across live, stored, and legacy hubs', () => {
  // Older hub without `modules` marker => allowed
  assert.equal(extractPublishedModules(['file_upload', 'recording_day']), null);
  assert.equal(isRecordingModuleAllowed(['file_upload', 'recording_day']), true);

  // Publishing hub with `module.recording` => allowed
  assert.deepEqual(extractPublishedModules(['modules', 'module.voice', 'module.recording']), ['module.recording', 'module.voice']);
  assert.equal(isRecordingModuleAllowed(['modules', 'module.recording']), true);

  // Publishing hub WITHOUT `module.recording` => refused (fail closed)
  assert.deepEqual(extractPublishedModules(['modules', 'module.voice']), ['module.voice']);
  assert.equal(isRecordingModuleAllowed(['modules', 'module.voice']), false);
  assert.equal(isRecordingModuleAllowed(['modules']), false);

  // Stored persistence round-trip preserves offline off-state (including empty module list)
  const storedOff = serializeStoredHubModules(extractPublishedModules(['modules']));
  assert.deepEqual(parseStoredHubModules(storedOff), []);
  assert.equal(isRecordingModuleAllowed([], parseStoredHubModules(storedOff)), false);

  const storedOn = serializeStoredHubModules(extractPublishedModules(['modules', 'module.recording']));
  assert.equal(isRecordingModuleAllowed([], parseStoredHubModules(storedOn)), true);

  const storedLegacy = serializeStoredHubModules(null);
  assert.equal(parseStoredHubModules(storedLegacy), null);
  assert.equal(isRecordingModuleAllowed([], parseStoredHubModules(storedLegacy)), true);

  // Live features override stale remembered off-state when hub re-enables recording
  assert.equal(isRecordingModuleAllowed(['modules', 'module.recording'], []), true);

  // Localized plain-language refusal copy matches iOS #15985
  assert.equal(recordingModuleRefusalText('en'), 'Meeting recording is off on this hub.');
  assert.equal(recordingModuleRefusalText('zh-CN'), '这台 hub 关闭了会议录音。');
  assert.equal(recordingModuleRefusalText('ja-JP'), 'このhubでは会議の録音がオフです。');
});
