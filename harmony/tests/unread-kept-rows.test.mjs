import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';

const coreHelper = process.env.PERSONA_CORE_HOST_REPLAY;
globalThis.unreadKeptCoreSort = (input) => {
  if (!coreHelper) throw new Error('PERSONA_CORE_HOST_REPLAY is required');
  const result = spawnSync(coreHelper, [], {
    input: (() => {
      const payload = Buffer.from(input);
      const frame = Buffer.alloc(5);
      frame.writeUInt32LE(payload.length + 1, 0);
      frame[4] = 10;
      return Buffer.concat([frame, payload]);
    })(),
    encoding: 'buffer',
    timeout: 10000,
    maxBuffer: 32 * 1024 * 1024
  });
  if (result.error || result.status !== 0) throw new Error('real core helper failed');
  const size = result.stdout.readUInt32LE(0);
  if (size < 1 || result.stdout[4] !== 1) throw new Error('real core rejected input');
  return result.stdout.subarray(5, 4 + size).toString('utf8');
};

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === 'libpersona_core_ohos.so') return { url: 'unread-kept-core:', shortCircuit: true };
    if (context.parentURL?.endsWith('/AgentWorkspace.ets') && specifier === './Wire') {
      return { url: new URL('./Wire.ets', context.parentURL).href, shortCircuit: true };
    }
    if (context.parentURL?.includes('/entry/src/main/ets/')) {
      const resolved = new URL(specifier, context.parentURL);
      if (['/Wire', '/AgentActivityGroups', '/AgentRepositoryPlace', '/MachineDashboard', '/InteractionDecision'].some((n) => resolved.pathname.endsWith(n))) {
        resolved.pathname += '.ets';
      } else if (!/\.[a-z]+$/i.test(resolved.pathname)) {
        resolved.pathname += '.ts';
      }
      return { url: resolved.href, shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'unread-kept-core:') {
      return { format: 'module', shortCircuit: true, source: 'export default { coreProjectSort(input) { return globalThis.unreadKeptCoreSort(input); } };' };
    }
    if (url.endsWith('.ets') || url.endsWith('/AgentListAdapter.ts') || url.endsWith('/AgentListProjection.ts')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { AgentWorkspace } = await import('../entry/src/main/ets/models/AgentWorkspace.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

function sessionRow(id, state, updatedAt) {
  return ResourceRow.from({
    session_id: id,
    machine_id: 'm1',
    manager: 'work',
    title: 'Agent ' + id,
    cwd: '/repo',
    repo_root: '/repo',
    pane_id: 'herdr:pane-' + id,
    worktree: '/repo',
    branch: 'main',
    status: state === 'unread' ? 'done' : state,
    state: state,
    updated_at: updatedAt
  }, 'session');
}

function makeFixture(sessions) {
  const calls = [];
  const machine = ResourceRow.from({ id: 'm1', manager: 'work', name: 'Studio', online: true, trust: 'personal' }, 'machine');
  const repo = {
    async call(path, method = 'GET', body = {}) {
      calls.push({ path, method, body });
      if (path === '/api/v1/pins') return { spaces: [], sessions: [] };
      if (path === '/api/v1/mutes') return { spaces: [], sessions: [], allowed: [] };
      return { ok: true };
    }
  };
  const client = {
    connected: true,
    demo: false,
    sessionsReady: true,
    profile: { id: 'hub', manager: 'work' },
    features: [],
    sessions,
    machines: [machine],
    selected: new ResourceRow(),
    repo() { return repo; },
    scope() { return 'hub:work'; },
    connectionGeneration() { return 1; },
    owns(g, s) { return g === 1 && s === 'hub:work'; },
    target(row) { return JSON.stringify(['hub:work', row.kind, row.machine, row.id]); },
    machineName() { return 'Studio'; },
    shortPath(p) { return p; },
    operationBlocked() { return false; },
    async loadView() { return ''; },
    async saveView() {},
    directoryOwners() { return [this]; },
    groupName() { return 'Hub'; },
    directoryOwnerFor() { return this; },
    async refreshDirectory() {},
    async refresh() {}
  };
  return { client, calls };
}

test('Unread quick filter keeps opened/seen rows in place until reselected, refreshed, or cleared', async () => {
  const u1 = sessionRow('u1', 'unread', '2026-11-26T05:00:00Z');
  const u2 = sessionRow('u2', 'unread', '2026-11-26T04:59:00Z');
  const w1 = sessionRow('w1', 'working', '2026-11-26T05:01:00Z');
  const { client } = makeFixture([u1, u2, w1]);
  const workspace = new AgentWorkspace();
  await workspace.open(client);

  workspace.setQuickFilter('unread');
  assert.deepEqual(workspace.visibleRows().map((r) => r.id), ['u1', 'u2']);
  assert.deepEqual(workspace.unreadShownRows.map((r) => r.id), ['u1', 'u2']);

  // Opening u1 makes it seen (idle) on the Hub, yet u1 stays kept in place so reading does not drop it.
  client.sessions = [sessionRow('u1', 'idle', '2026-11-26T05:00:00Z'), u2, w1];
  workspace.update(client);
  assert.deepEqual(workspace.visibleRows().map((r) => [r.id, r.state]), [['u2', 'unread'], ['u1', 'idle']]);
  assert.deepEqual(workspace.unreadShownRows.map((r) => r.id), ['u2']);

  // In space grouping, kept seen rows also retain their exact position in time order.
  workspace.setGroupingMode('space');
  client.sessions = [u1, u2, w1];
  workspace.update(client);
  workspace.setQuickFilter('unread');
  client.sessions = [sessionRow('u1', 'idle', '2026-11-26T05:00:00Z'), u2, w1];
  workspace.update(client);
  assert.deepEqual(workspace.visibleRows().map((r) => [r.id, r.state]), [['u1', 'idle'], ['u2', 'unread']]);

  // Reselecting the Unread chip recomputes and removes the already-seen row.
  workspace.setQuickFilter('unread');
  assert.deepEqual(workspace.visibleRows().map((r) => r.id), ['u2']);
});

test('markAllSeen marks every visible unread row seen and clears kept rows', async () => {
  const u1 = sessionRow('u1', 'unread', '2026-11-26T05:00:00Z');
  const u2 = sessionRow('u2', 'unread', '2026-11-26T04:59:00Z');
  const { client, calls } = makeFixture([u1, u2]);
  const workspace = new AgentWorkspace();
  await workspace.open(client);

  workspace.setQuickFilter('unread');
  calls.length = 0;
  const marked = await workspace.markAllSeen();
  assert.equal(marked, 2);
  assert.deepEqual(
    calls.filter((c) => c.path.endsWith('/seen')).map((c) => [c.method, c.path]),
    [['POST', '/api/v1/sessions/m1/u1/seen'], ['POST', '/api/v1/sessions/m1/u2/seen']]
  );
  assert.deepEqual(workspace.visibleRows(), []);
  assert.deepEqual(workspace.unreadShownRows, []);
});
