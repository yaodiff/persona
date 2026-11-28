import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkTS') return { url: 'persona-admin:arkts', shortCircuit: true };
    if (specifier === '../platform/HubTransport') return { url: 'persona-admin:transport', shortCircuit: true };
    if (context.parentURL?.endsWith('AdminWorkspace.ets') && specifier === './Wire')
      return { url: new URL('./Wire.ets', context.parentURL).href, shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-admin:arkts') return { format: 'module', source: 'export const util = {};', shortCircuit: true };
    if (url === 'persona-admin:transport') return { format: 'module', source: `
      export class HttpFailure extends Error {
        constructor(status, code, resultUnknown = false, resultPending = false) {
          super(code); this.status = status; this.code = code; this.resultUnknown = resultUnknown; this.resultPending = resultPending;
        }
      }`, shortCircuit: true };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  }
});

const { AdminManager, AdminWorkspace } = await import('../entry/src/main/ets/models/AdminWorkspace.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure } = await import('persona-admin:transport');

function machine(id, manager = 'work', fields = {}) {
  const row = new ResourceRow(id, id, 'machine');
  row.manager = manager;
  row.raw = JSON.stringify({ id, name: id, manager, online: true, trust: 'personal', ...fields });
  return row;
}

function fixture(options = {}) {
  const shared = { calls: [], views: new Map(), viewValues: new Map(), failRosterAfterDelete: false, blocked: new Set() };
  const makeClient = (manager, state = {}) => {
    const client = {
      profile: { id: 'profile', manager }, connected: true, features: ['builtin_presets'], machines: state.machines || [], generation: 5,
      scope() { return 'profile:' + manager; }, connectionGeneration() { return this.generation; },
      owns(generation, scope) { return this.connected && generation === this.generation && scope === this.scope(); },
      operationBlocked(path) { return shared.blocked.has(manager + ':' + path); },
      repo() { return { call: async (path, method = 'GET', body = {}, op = '', admit) => {
        if (method !== 'GET' && admit && !admit()) throw new HttpFailure(0, 'request_updated');
        const call = { manager, path, method, body: structuredClone(body), op };
        shared.calls.push(call);
        if (state.onCall) {
          try {
            const result = await state.onCall(call, shared);
            if (result !== undefined) return structuredClone(result);
          } catch (failure) {
            if (failure.resultUnknown || failure.resultPending) shared.blocked.add(manager + ':' + path);
            throw failure;
          }
        }
        if (path === '/api/v1/assistant' && method === 'GET') return structuredClone(state.assistant || {
          machine_id: 'machine-a', launcher: 'claude', timezone: 'Asia/Shanghai', notes: 'old', main_tasks_quiet: true,
          agent_tasks: false, inbox_signals: false, interject_background: false, kb_topics: true,
          idle_close_hours: 12, archive_closed_days: 3, recording_days: 14, minutes_days: 0
        });
        if (path === '/api/v1/assistant/memory') return { files: [] };
        if (path === '/api/v1/managers' && method === 'GET') {
          if (shared.failRosterAfterDelete) throw new HttpFailure(503, 'offline');
          return { default: 'default', managers: [{ id: 'work', name: 'Work', online: false, default: false, builtin: false }] };
        }
        if (path === '/api/v1/machines' && method === 'GET') return { machines: state.machineList || [] };
        return { ok: true };
      } }; },
      async viewFor(row) {
        if (!row.manager || row.manager === this.profile.manager) return this;
        const key = row.manager;
        if (!shared.views.has(key)) shared.views.set(key, makeClient(key, options.children?.[key] || {}));
        return shared.views.get(key);
      },
      async loadView(key) { return shared.viewValues.get(this.scope() + ':' + key) || ''; },
      viewRevision(key) { return shared.viewValues.get(this.scope() + ':epoch:' + key) || 0; },
      async saveView(key, value, expectedEpoch) {
        const epochKey = this.scope() + ':epoch:' + key;
        if ((shared.viewValues.get(epochKey) || 0) !== expectedEpoch) throw new Error('request_updated');
        shared.viewValues.set(this.scope() + ':' + key, value); shared.viewValues.set(epochKey, expectedEpoch + 1);
      }
    };
    return client;
  };
  const root = makeClient(options.manager || 'work', options.root || {});
  if (options.machines) root.machines = options.machines;
  return { root, shared };
}

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

test('the assistant Recent setting changes only its supported Hub field and retains an unsaved profile draft', async () => {
  const server={recent_task_chats:false,notes:'Original',leaders:['direction']};
  const env=fixture({root:{onCall:async call=>{
    if (call.path!=='/api/v1/assistant') return undefined;
    if (call.method==='PUT') Object.assign(server,call.body);
    return server;
  }}});
  const applied=[];env.root.acceptSidebarConfig=data=>applied.push(data.recent_task_chats);
  const workspace=new AdminWorkspace();await workspace.open(env.root,'assistant',true);
  workspace.setForm('notes','My unsaved note');
  const ticket=workspace.capture('/api/v1/assistant');
  assert.equal(await workspace.setRecentTaskChats(ticket,true),true);
  assert.deepEqual(env.shared.calls.filter(call=>call.method==='PUT').map(call=>call.body),[{recent_task_chats:true}]);
  assert.equal(workspace.formText('notes'),'My unsaved note');assert.equal(workspace.draftDirty,true);assert.deepEqual(applied,[true]);
  workspace.hide();assert.equal(await workspace.setRecentTaskChats(ticket,false),false);
  const legacy=fixture(),old=new AdminWorkspace();await old.open(legacy.root,'assistant',true);
  assert.equal(await old.setRecentTaskChats(old.capture('/api/v1/assistant'),true),false);
  assert.deepEqual(legacy.shared.calls.filter(call=>call.method==='PUT'),[]);
});

test('All Chats and Assistant Profile share the acknowledged Hub preference, even after the options sheet is dismissed',async()=>{
  const gate=Promise.withResolvers(),store={recent_task_chats:false};
  const env=fixture({root:{onCall:async call=>{
    if (call.path!=='/api/v1/assistant') return undefined;
    if (call.method==='PUT') { await gate.promise; Object.assign(store,call.body); }
    return {...store};
  }}});
  const echoes=[];env.root.acceptSidebarConfig=data=>echoes.push(data.recent_task_chats);
  const menu=new AdminWorkspace(),path='/api/v1/assistant';
  await menu.open(env.root,'conversation_options',true);
  assert.equal(menu.showsConversationOptions(env.root),true);
  const ticket=menu.capture(path),writing=menu.setRecentTaskChats(ticket,true);
  assert.equal(menu.assistantData.recent_task_chats,true);
  assert.equal(await menu.setRecentTaskChats(ticket,false),false);
  menu.hide(); assert.equal(menu.showsConversationOptions(env.root),false);
  gate.resolve();assert.equal(await writing,true);assert.deepEqual(echoes,[true]);
  const profile=new AdminWorkspace();await profile.open(env.root,'assistant',true);
  assert.equal(profile.assistantData.recent_task_chats,true);
  assert.equal(await profile.setRecentTaskChats(profile.capture(path),false),true);
  await menu.open(env.root,'conversation_options',true);assert.equal(menu.assistantData.recent_task_chats,false);
  assert.deepEqual(env.shared.calls.filter(call=>call.method==='PUT').map(call=>call.body),[{recent_task_chats:true},{recent_task_chats:false}]);
});

test('All Chats reverts a refused switch and will not write from an old menu, unauthenticated client or older Hub',async()=>{
  const gate=Promise.withResolvers();const env=fixture({root:{onCall:async call=>{
    if (call.path!=='/api/v1/assistant') return undefined;
    if (call.method==='PUT') {await gate.promise;throw Error('forbidden');}
    return {recent_task_chats:false};
  }}});
  const menu=new AdminWorkspace(),path='/api/v1/assistant';
  await menu.open(env.root,'conversation_options',true);
  const ticket=menu.capture(path);const write=menu.setRecentTaskChats(ticket,true);
  assert.equal(menu.assistantData.recent_task_chats,true);gate.resolve();assert.equal(await write,false);
  assert.equal(menu.assistantData.recent_task_chats,false);assert.equal(menu.writeState,'failed');
  env.root.demo=true;assert.equal(await menu.setRecentTaskChats(ticket,true),false);
  env.root.demo=false;env.root.connected=false;assert.equal(await menu.setRecentTaskChats(ticket,true),false);
  env.root.connected=true;env.root.generation++;assert.equal(await menu.setRecentTaskChats(ticket,true),false);
  assert.equal(env.shared.calls.filter(call=>call.method==='PUT').length,1);
  const legacy=fixture();await menu.open(legacy.root,'conversation_options',true);
  assert.equal(await menu.setRecentTaskChats(menu.capture(path),true),false);
  assert.deepEqual(legacy.shared.calls.filter(call=>call.method==='PUT'),[]);
  const original=fixture({root:{onCall:async()=>({recent_task_chats:false})}}),replacement=fixture({root:{onCall:async()=>({recent_task_chats:false})}});
  await menu.open(original.root,'conversation_options',true);const formerMenu=menu.capture(path);
  await menu.open(replacement.root,'conversation_options',true);
  assert.equal(await menu.setRecentTaskChats(formerMenu,true),false);
  assert.deepEqual(replacement.shared.calls.filter(call=>call.method==='PUT'),[]);
});

test('assistant profile writes use the documented path and changed fields only', async () => {
  const env = fixture(), workspace = new AdminWorkspace();
  await workspace.open(env.root, 'assistant', true);
  workspace.setForm('notes', 'A newer note');
  assert.equal(await workspace.saveAssistant(workspace.capture('/api/v1/assistant')), true);
  const write = env.shared.calls.find((call) => call.method === 'PUT');
  assert.deepEqual({ path: write.path, method: write.method, body: write.body }, {
    path: '/api/v1/assistant', method: 'PUT', body: { notes: 'A newer note' }
  });
});

test('hidden and changed-generation snapshots cannot start a write', async () => {
  const env = fixture(), workspace = new AdminWorkspace();
  await workspace.open(env.root, 'assistant', true);
  workspace.setForm('notes', 'unsaved');
  const hidden = workspace.capture('/api/v1/assistant');
  workspace.hide();
  assert.equal(await workspace.saveAssistant(hidden), false);
  assert.equal(env.shared.calls.some((call) => call.method !== 'GET'), false);

  await workspace.open(env.root, 'assistant', true);
  const stale = workspace.capture('/api/v1/assistant');
  env.root.generation++;
  assert.equal(await workspace.saveAssistant(stale), false);
  assert.equal(env.shared.calls.some((call) => call.method !== 'GET'), false);
});

test('profile, manager scope, and selected-machine changes revoke stale write snapshots', async () => {
  const scopeEnv = fixture(), scopeWorkspace = new AdminWorkspace();
  await scopeWorkspace.open(scopeEnv.root, 'assistant', true);
  scopeWorkspace.setForm('notes', 'stale scope draft');
  const staleScope = scopeWorkspace.capture('/api/v1/assistant');
  scopeEnv.root.profile.manager = 'changed-manager';
  assert.equal(await scopeWorkspace.saveAssistant(staleScope), false);
  assert.equal(scopeEnv.shared.calls.some((call) => call.method !== 'GET'), false);

  const profileEnv = fixture(), profileWorkspace = new AdminWorkspace();
  await profileWorkspace.open(profileEnv.root, 'assistant', true);
  profileWorkspace.setForm('notes', 'stale profile draft');
  const staleProfile = profileWorkspace.capture('/api/v1/assistant');
  profileEnv.root.profile.id = 'changed-profile';
  assert.equal(await profileWorkspace.saveAssistant(staleProfile), false);
  assert.equal(profileEnv.shared.calls.some((call) => call.method !== 'GET'), false);

  const machineA = machine('machine-a'), machineB = machine('machine-b');
  const machineEnv = fixture({ machines: [machineA, machineB] }), machineWorkspace = new AdminWorkspace();
  await machineWorkspace.open(machineEnv.root, 'engine', true);
  const staleMachine = machineWorkspace.capture('/api/v1/machines/machine-a/engine');
  machineWorkspace.selectMachine('machine-b');
  assert.equal(await machineWorkspace.saveEngineSelection(staleMachine), false);
  assert.equal(machineEnv.shared.calls.some((call) => call.method !== 'GET'), false);
});

test('unknown writes stay blocked and are never replayed automatically', async () => {
  const env = fixture({ root: { onCall: async (call) => {
    if (call.method === 'PUT' && call.path === '/api/v1/assistant') throw new HttpFailure(0, 'result_unknown', true);
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'assistant', true);
  workspace.setForm('notes', 'uncertain');
  const snapshot = workspace.capture('/api/v1/assistant');
  assert.equal(await workspace.saveAssistant(snapshot), false);
  assert.equal(workspace.writeState, 'result_unknown');
  assert.equal(await workspace.saveAssistant(snapshot), false);
  assert.equal(env.shared.calls.filter((call) => call.method === 'PUT').length, 1);
});

test('engine writes use the selected machine manager and exact host-set payload', async () => {
  const row = machine('node-a', 'other', { engine: 'herdr', engine_hosts: ['herdr'], engine_default: 'herdr',
    engines: [{ id: 'herdr', name: 'herdr', available: true }, { id: 'orca', name: 'Orca', available: true }] });
  const env = fixture({ machines: [row], children: { other: { machineList: [row.data()] } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'engine', true);
  workspace.selectEngineHost('orca', true);
  workspace.selectEngineDefault('orca');
  const snapshot = workspace.capture('/api/v1/machines/node-a/engine');
  assert.equal(await workspace.saveEngineSelection(snapshot), true);
  const write = env.shared.calls.find((call) => call.method === 'PUT');
  assert.deepEqual({ manager: write.manager, path: write.path, body: write.body }, {
    manager: 'other', path: '/api/v1/machines/node-a/engine', body: { hosts: ['herdr', 'orca'], default: 'orca' }
  });
});

test('an acknowledged manager deletion remains done when the refresh fails', async () => {
  const env = fixture({ root: { onCall: async (call, shared) => {
    if (call.method === 'DELETE' && call.path === '/api/v1/managers/work') { shared.failRosterAfterDelete = true; return { ok: true }; }
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'managers', true);
  const snapshot = workspace.capture('/api/v1/managers/work');
  assert.equal(await workspace.deleteManager(snapshot, 'work'), true);
  assert.equal(workspace.writeState, 'done');
  assert.equal(workspace.refreshFailed, true);
  assert.equal(env.shared.calls.filter((call) => call.method === 'DELETE').length, 1);
});

test('resource refreshes cannot retarget an open confirmation', async () => {
  let files = [{ name: 'first.md', content: 'first' }, { name: 'next.md', content: 'next' }];
  let roster = [
    { id: 'work', name: 'Work', online: false, default: false, builtin: false },
    { id: 'offline', name: 'Offline', online: false, default: false, builtin: false }
  ];
  const env = fixture({ root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/assistant/memory') return { files };
    if (call.method === 'GET' && call.path === '/api/v1/managers') return { default: 'work', managers: roster };
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'memory', true);
  const memoryConfirmation = workspace.capture('/api/v1/assistant/memory/first.md');
  files = [{ name: 'next.md', content: 'next' }];
  await workspace.refresh();
  assert.equal(workspace.selectedMemory, 'next.md');
  assert.equal(await workspace.deleteMemory(memoryConfirmation, 'first.md'), false);
  assert.equal(env.shared.calls.some((call) => call.method === 'DELETE'), false);

  await workspace.open(env.root, 'managers', true);
  workspace.selectManager('offline');
  const managerConfirmation = workspace.capture('/api/v1/managers/offline');
  roster = [{ id: 'work', name: 'Work', online: false, default: true, builtin: false }];
  await workspace.refresh();
  assert.equal(workspace.selectedManager, 'work');
  assert.equal(await workspace.renameManager(managerConfirmation, 'offline', 'Wrong target'), false);
  assert.equal(env.shared.calls.some((call) => call.method === 'PATCH'), false);

  const page = readFileSync(new URL('../entry/src/main/ets/components/AdminWorkspacePage.ets', import.meta.url), 'utf8');
  assert.match(page, /admin_confirmation_target/);
  assert.doesNotMatch(page, /JSON\.stringify\(body\)|method \+ ' ' \+ path/);
});

test('launcher edits preserve unedited configuration and use the supported wire kind', async () => {
  const provider = {
    name: 'provider', display_name: 'Provider', kind: 'launcher', api: 'messages', agent: 'codex', base_url: 'https://models.example',
    model: 'main-model', small_model: 'small-model', subagent_model: 'subagent-model', custom_model: 'custom-model',
    custom_model_name: 'Custom', custom_model_description: 'Custom description', args: ['--safe-flag'],
    models: [{ id: 'main-model', name: 'Main' }], opus_model: 'main-model', sonnet_model: 'main-model', haiku_model: 'small-model',
    fable_model: '', subagent_force: true, proxy: 'http://proxy.example', no_proxy: 'localhost', timeout_ms: 30000,
    compact_window: 1000, max_context: 2000, effort: 'high'
  };
  const env = fixture({ root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/launchers') return { launchers: [{ name: 'provider', display_name: 'Provider', kind: 'launcher' }] };
    if (call.method === 'GET' && call.path === '/api/v1/launchers/provider') return { launcher: provider };
    if (call.method === 'PUT' && call.path === '/api/v1/launchers/provider') return { launcher: { name: 'provider' } };
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'launchers', true);
  workspace.beginLauncher(false);
  workspace.setForm('display_name', 'Renamed for people');
  const snapshot = workspace.capture('/api/v1/launchers/provider');
  assert.equal(await workspace.saveLauncher(snapshot), true);
  const body = env.shared.calls.find((call) => call.method === 'PUT').body;
  assert.equal(body.display_name, 'Renamed for people');
  assert.equal(body.kind, 'launcher');
  assert.equal(body.agent, 'codex');
  assert.equal(body.api, 'messages');
  assert.deepEqual(body.args, ['--safe-flag']);
  assert.equal(body.small_model, 'small-model');
  assert.equal(body.subagent_model, 'subagent-model');
  assert.equal(body.custom_model, 'custom-model');
  assert.deepEqual(body.models, provider.models);
  assert.equal(body.proxy, provider.proxy);
  assert.equal(Object.hasOwn(body, 'api_key'), false);

  const newEnv = fixture({ root: { onCall: async (call) => {
    if (call.method === 'POST' && call.path === '/api/v1/launchers') {
      if (!call.body.base_url || !call.body.model) throw new HttpFailure(400, 'bad_request');
      return { launcher: { name: 'new-provider' } };
    }
  } } });
  const newWorkspace = new AdminWorkspace();
  await newWorkspace.open(newEnv.root, 'launchers', true);
  newWorkspace.beginLauncher(true);
  newWorkspace.setForm('name', 'new-provider');
  newWorkspace.setForm('base_url', 'https://gateway.example');
  newWorkspace.setForm('model', 'model');
  const newSnapshot = newWorkspace.capture('/api/v1/launchers');
  assert.equal(await newWorkspace.saveLauncher(newSnapshot), true);
  const createBody = newEnv.shared.calls.find((call) => call.method === 'POST').body;
  assert.equal(createBody.kind, 'launcher');
  assert.notEqual(createBody.kind, 'agent');
});

test('launcher ACK preserves a newer form draft and replacement key', async () => {
  const writeGate = deferred(), writeStarted = deferred();
  const env = fixture({ root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/launchers') return { launchers: [{ name: 'provider', display_name: 'Provider', kind: 'launcher' }] };
    if (call.method === 'GET' && call.path === '/api/v1/launchers/provider') return { launcher: {
      name: 'provider', display_name: 'Provider', kind: 'launcher', api: 'messages', agent: 'claude', model: 'model'
    } };
    if (call.method === 'PUT' && call.path === '/api/v1/launchers/provider') { writeStarted.resolve(); return writeGate.promise; }
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'launchers', true);
  workspace.beginLauncher(false);
  workspace.setForm('display_name', 'Submitted name');
  workspace.setLauncherSecretDraft('submitted-key');
  const pendingSave = workspace.saveLauncher(workspace.capture('/api/v1/launchers/provider'));
  await writeStarted.promise;

  workspace.setForm('display_name', 'Newer name');
  workspace.setLauncherSecretDraft('newer-key');
  writeGate.resolve({ launcher: { name: 'provider' } });
  assert.equal(await pendingSave, true);
  await workspace.draftQueue;
  assert.equal(workspace.writeState, 'done');
  assert.equal(workspace.launcherEditor, true);
  assert.equal(workspace.draftDirty, true);
  assert.equal(workspace.formText('display_name'), 'Newer name');
  assert.equal(workspace.launcherSecretDraft, 'newer-key');
  const persisted = JSON.parse(env.shared.viewValues.get('profile:work:admin-workspace:launchers'));
  assert.equal(persisted.dirty, true);
  assert.equal(JSON.parse(persisted.draft).display_name, 'Newer name');
  assert.equal(env.shared.calls.find((call) => call.method === 'PUT').body.api_key, 'submitted-key');
});

test('replacement refreshes and stale writes release only their own busy state', async () => {
  const machineA = machine('machine-a'), machineB = machine('machine-b');
  const firstRead = deferred(), thirdRead = deferred(), firstStarted = deferred(), thirdStarted = deferred();
  let machineReads = 0;
  const env = fixture({ machines: [machineA, machineB], root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/machines') {
      machineReads++;
      if (machineReads === 1) { firstStarted.resolve(); return firstRead.promise; }
      if (machineReads === 3) { thirdStarted.resolve(); return thirdRead.promise; }
      return { machines: [machineA.data(), machineB.data()] };
    }
  } } });
  const workspace = new AdminWorkspace();
  const initialOpen = workspace.open(env.root, 'engine', true);
  await firstStarted.promise;
  workspace.selectMachine('machine-b');
  await workspace.refresh();
  assert.equal(workspace.machineData.id, 'machine-b');
  const newestRefresh = workspace.refresh();
  await thirdStarted.promise;
  firstRead.resolve({ machines: [machineA.data()] });
  await initialOpen;
  assert.equal(workspace.loading, true);
  assert.equal(workspace.machineData.id, 'machine-b');
  thirdRead.resolve({ machines: [machineB.data()] });
  await newestRefresh;
  assert.equal(workspace.loading, false);

  const writeGate = deferred(), writeStarted = deferred();
  const launcherEnv = fixture({ root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/launchers') return { launchers: [
      { name: 'first', display_name: 'First', kind: 'launcher' }, { name: 'second', display_name: 'Second', kind: 'launcher' }
    ] };
    if (call.method === 'GET' && call.path === '/api/v1/launchers/first') return { launcher: { name: 'first', display_name: 'First', kind: 'launcher', agent: 'claude', model: 'm' } };
    if (call.method === 'GET' && call.path === '/api/v1/launchers/second') return { launcher: { name: 'second', display_name: 'Second', kind: 'launcher', agent: 'claude', model: 'm' } };
    if (call.method === 'PUT' && call.path === '/api/v1/launchers/first') { writeStarted.resolve(); return writeGate.promise; }
  } } });
  const launcherWorkspace = new AdminWorkspace();
  await launcherWorkspace.open(launcherEnv.root, 'launchers', true);
  launcherWorkspace.beginLauncher(false);
  launcherWorkspace.setForm('display_name', 'First update');
  const pendingSave = launcherWorkspace.saveLauncher(launcherWorkspace.capture('/api/v1/launchers/first'));
  await writeStarted.promise;
  launcherWorkspace.selectLauncher('second');
  await launcherWorkspace.refresh();
  assert.equal(launcherWorkspace.saving, true);
  assert.equal(launcherWorkspace.loading, false);
  writeGate.resolve({ launcher: { name: 'first' } });
  await pendingSave;
  assert.equal(launcherWorkspace.saving, false);
});

test('reopening after reconnect refreshes the current generation and fences late assistant ACKs', async () => {
  const oldWrite = deferred(), oldWriteStarted = deferred();
  let reads = 0, writes = 0;
  const env = fixture({ root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/assistant') {
      reads++;
      return { machine_id: 'machine-a', launcher: 'claude', timezone: 'UTC', notes: reads === 1 ? 'old remote' : 'fresh remote' };
    }
    if (call.method === 'PUT' && call.path === '/api/v1/assistant') {
      writes++;
      if (writes === 1) { oldWriteStarted.resolve(); return oldWrite.promise; }
      return { machine_id: 'machine-a', launcher: 'claude', timezone: 'UTC', notes: 'current ACK' };
    }
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'assistant', true);
  workspace.setForm('notes', 'old generation draft');
  const oldSnapshot = workspace.capture('/api/v1/assistant');
  const oldSave = workspace.saveAssistant(oldSnapshot);
  await oldWriteStarted.promise;

  env.root.generation++;
  await workspace.open(env.root, 'assistant', true);
  assert.equal(workspace.formText('notes'), 'old generation draft');
  const currentSnapshot = workspace.capture('/api/v1/assistant');
  assert.equal(workspace.admits(currentSnapshot), true);
  workspace.setForm('notes', 'current generation draft');
  assert.equal(await workspace.saveAssistant(workspace.capture('/api/v1/assistant')), true);
  workspace.setForm('notes', 'later unsaved draft');

  oldWrite.resolve({ machine_id: 'machine-a', launcher: 'claude', timezone: 'UTC', notes: 'old ACK' });
  assert.equal(await oldSave, true);
  assert.equal(workspace.formText('notes'), 'later unsaved draft');
  assert.equal(workspace.draftDirty, true);
  assert.equal(workspace.assistantData.notes, 'current ACK');
  assert.equal(workspace.writeState, 'done');
  assert.equal(reads, 2);
  assert.equal(writes, 2);
});

test('launcher import previews are bound to the machine that produced them', async () => {
  const machineA = machine('machine-a'), machineB = machine('machine-b');
  const env = fixture({ machines: [machineA, machineB], root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/launchers') return { launchers: [] };
    if (call.method === 'GET' && call.path === '/api/v1/launchers/import?machine=machine-a') return { providers: [{ name: 'provider-a', has_key: true }] };
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'launchers', true);
  const path = '/api/v1/launchers/import?machine=machine-a';
  await workspace.loadImporter(workspace.capture(path), 'machine-a');
  assert.equal(workspace.importerMachine, 'machine-a');
  assert.deepEqual(workspace.importerSelection, ['provider-a']);
  const staleImport = workspace.capture('/api/v1/launchers/import');
  workspace.selectMachine('machine-b');
  assert.equal(workspace.importerMachine, '');
  assert.deepEqual(workspace.importerSelection, []);
  assert.equal(await workspace.importLaunchers(staleImport, 'machine-a', ['provider-a']), false);
  assert.equal(env.shared.calls.some((call) => call.method === 'POST' && call.path === '/api/v1/launchers/import'), false);
});

test('Hub update status uses state enums and busy session objects', () => {
  const workspace = new AdminWorkspace();
  workspace.hubUpdate = { state: 'downloading', busy: [
    { machine_id: 'node-a', machine: 'Studio Mac', session_id: 'opaque', title: 'Design review' },
    { machine_id: 'node-b', machine: 'Build host', session_id: '', title: '' }
  ] };
  assert.equal(workspace.hubUpdateWorking(), true);
  assert.deepEqual(workspace.hubBusyLabels(), ['Studio Mac · Design review', 'Build host']);
  workspace.hubUpdate = { state: 'available', busy: [] };
  assert.equal(workspace.hubUpdateWorking(), false);

  const page = readFileSync(new URL('../entry/src/main/ets/components/AdminWorkspacePage.ets', import.meta.url), 'utf8');
  assert.match(page, /this\.workspace\.hubUpdateWorking\(\)/);
  assert.match(page, /Toggle\(\{ type: ToggleType\.Switch, isOn: Wire\.bool\(this\.workspace\.hubUpdate, 'auto'\) \}\)\.enabled\(this\.writeAllowed\(\) && !this\.workspace\.hubUpdateWorking\(\)\)/);
  assert.match(page, /Toggle\(\{ type: ToggleType\.Switch, isOn: Wire\.bool\(this\.workspace\.hubUpdate, 'auto_update_nodes'\) \}\)\.enabled\(this\.writeAllowed\(\) && !this\.workspace\.hubUpdateWorking\(\)\)/);
  assert.doesNotMatch(page, /Wire\.bool\(status, 'working'\)|Wire\.strings\(status, 'busy'\)/);
});

test('manager install commands decode the API object and expose only available choices', () => {
  const workspace = new AdminWorkspace();
  workspace.installCode = { commands: {
    service: 'curl -fsSL https://hub.example/i/123 | sh', join: 'personad manager join https://hub.example 123 --install', docker: ''
  } };
  assert.deepEqual(workspace.managerInstallCommandKinds(), ['service', 'join']);
  assert.equal(workspace.managerInstallCommand('join'), 'personad manager join https://hub.example 123 --install');
  assert.equal(workspace.managerInstallCommand('docker'), '');
});

test('empty ordinary-memory lists can create validated Markdown files through the existing route', async () => {
  const files = [];
  const env = fixture({ root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/assistant/memory') return { files };
    if (call.method === 'PUT' && call.path.startsWith('/api/v1/assistant/memory/')) {
      const name = decodeURIComponent(call.path.slice('/api/v1/assistant/memory/'.length));
      files.push({ name, content: call.body.content });
      return { ok: true };
    }
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'memory', true);
  assert.equal(workspace.memoryRows.length, 0);

  workspace.setNewMemoryName('../bad.md');
  workspace.setNewMemoryContent('invalid path must not be sent');
  assert.equal(await workspace.createMemory(workspace.capture('/api/v1/assistant/memory/..%2Fbad.md')), false);
  assert.equal(workspace.error, 'admin_memory_invalid_name');
  assert.equal(env.shared.calls.some((call) => call.method === 'PUT'), false);

  workspace.setNewMemoryName('project-note.md');
  workspace.setNewMemoryContent('Keep this as ordinary memory.');
  const path = '/api/v1/assistant/memory/project-note.md';
  assert.equal(workspace.canCreateMemory(), true);
  assert.equal(await workspace.createMemory(workspace.capture(path)), true);
  const write = env.shared.calls.find((call) => call.method === 'PUT');
  assert.deepEqual({ path: write.path, method: write.method, body: write.body }, {
    path, method: 'PUT', body: { content: 'Keep this as ordinary memory.' }
  });
  assert.equal(workspace.memoryRows.some((row) => row.name === 'project-note.md'), true);
});

test('manager removal permits offline pending identities and honors server-owned blockers', async () => {
  let roster = [
    { id: 'work', name: 'Work', online: true, default: true, builtin: true },
    { id: 'offline', name: 'Offline', online: false, installing: 'canceling', default: false, builtin: false }
  ];
  const env = fixture({ root: { onCall: async (call) => {
    if (call.method === 'GET' && call.path === '/api/v1/managers') return { default: 'work', managers: roster };
    if (call.method === 'DELETE' && call.path === '/api/v1/managers/offline') {
      roster = [roster[0]];
      return { ok: true };
    }
  } } });
  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'managers', true);
  const offline = workspace.managers.find((manager) => manager.id === 'offline');
  const builtin = workspace.managers.find((manager) => manager.id === 'work');
  assert.equal(workspace.managerCanDelete(offline), true);
  assert.equal(workspace.managerCanDelete(builtin), false);
  workspace.selectManager('offline');
  const snapshot = workspace.capture('/api/v1/managers/offline');
  assert.equal(await workspace.deleteManager(snapshot, 'offline'), true);
  assert.equal(env.shared.calls.some((call) => call.method === 'DELETE' && call.path === '/api/v1/managers/offline'), true);
});

test('native admin routes use the parent title and one parent refresh action', () => {
  const page = readFileSync(new URL('../entry/src/main/ets/components/AdminWorkspacePage.ets', import.meta.url), 'utf8');
  for (const key of ['admin_identity', 'admin_memory', 'admin_managers', 'admin_launchers', 'admin_machine_engine', 'admin_node_update', 'admin_hub_update']) assert.equal(page.includes("this.title('" + key + "')"), false);
  assert.equal(page.includes("Button(tr('refresh'))"), false);
  assert.match(page, /@Monitor\('refreshRevision'\)/);
  assert.match(page, /admin\.launcher\.edit\.base_url/);
});


test('a pre-save read cannot overwrite an acknowledged assistant update', async () => {
  const gate = deferred(), started = deferred(); let delay = false;
  const env = fixture({ root: { onCall: async (call) => {
    if (call.path === '/api/v1/assistant' && call.method === 'GET' && delay) { started.resolve(); return gate.promise; }
    if (call.path === '/api/v1/assistant' && call.method === 'PUT') return { notes: call.body.notes, timezone: 'Asia/Shanghai' };
  } } });
  const workspace = new AdminWorkspace(); await workspace.open(env.root, 'assistant', true);
  workspace.setForm('notes', 'New saved notes'); delay = true;
  const read = workspace.refresh(); await started.promise;
  assert.equal(await workspace.saveAssistant(workspace.capture('/api/v1/assistant')), true);
  gate.resolve({ notes: 'Old remote notes', timezone: 'Asia/Shanghai' }); await read;
  assert.equal(workspace.formText('notes'), 'New saved notes');
  assert.equal(workspace.assistantData.notes, 'New saved notes');
});

test('creation ACK rebases newer launcher edits onto the acknowledged provider', async () => {
  const gate = deferred(), started = deferred();
  const env = fixture({ root: { onCall: async (call) => {
    if (call.path === '/api/v1/launchers' && call.method === 'POST') { started.resolve(); return gate.promise; }
    if (call.path === '/api/v1/launchers/new-provider' && call.method === 'PUT') return { launcher: { name: 'new-provider' } };
    if (call.path === '/api/v1/launchers' && call.method === 'GET') return { launchers: [{ name: 'new-provider', kind: 'launcher' }] };
  } } });
  const workspace = new AdminWorkspace(); await workspace.open(env.root, 'launchers', true);
  workspace.beginLauncher(true); workspace.setForm('name', 'new-provider');
  workspace.setForm('base_url', 'https://gateway.example'); workspace.setForm('model', 'model');
  const save = workspace.saveLauncher(workspace.capture('/api/v1/launchers')); await started.promise;
  workspace.setForm('display_name', 'Newer display'); workspace.setLauncherSecretDraft('newer-secret');
  gate.resolve({ launcher: { name: 'new-provider' } }); assert.equal(await save, true);
  assert.equal(workspace.launcherNew, false); assert.equal(workspace.launcherEditor, true);
  assert.equal(workspace.formText('display_name'), 'Newer display'); assert.equal(workspace.launcherSecretDraft, 'newer-secret');
  assert.equal(Object.hasOwn(workspace.launcherDetail, 'api_key'), false);
  assert.equal(await workspace.saveLauncher(workspace.capture('/api/v1/launchers/new-provider')), true);
  assert.equal(env.shared.calls.filter((call) => call.method === 'POST').length, 1);
  const update = env.shared.calls.find((call) => call.method === 'PUT');
  assert.equal(update.path, '/api/v1/launchers/new-provider'); assert.equal(update.body.display_name, 'Newer display');
  assert.equal(update.body.api_key, 'newer-secret');
});


test('assistant ACK updates the baseline while preserving an undo typed during save', async () => {
  const gate = deferred(), started = deferred(); let writes = 0;
  const env = fixture({ root: { assistant: { notes: 'A' }, onCall: async (call) => {
    if (call.path === '/api/v1/assistant' && call.method === 'PUT') {
      if (++writes === 1) { started.resolve(); return gate.promise; }
      return { notes: call.body.notes };
    }
  } } });
  const workspace = new AdminWorkspace(); await workspace.open(env.root, 'assistant', true);
  workspace.setForm('notes', 'B'); const save = workspace.saveAssistant(workspace.capture('/api/v1/assistant'));
  await started.promise; workspace.setForm('notes', 'A'); gate.resolve({ notes: 'B' }); await save;
  assert.equal(workspace.assistantData.notes, 'B'); assert.equal(workspace.formText('notes'), 'A'); assert.equal(workspace.draftDirty, true);
  assert.equal(await workspace.saveAssistant(workspace.capture('/api/v1/assistant')), true);
  assert.equal(writes, 2); assert.equal(env.shared.calls.filter((call) => call.method === 'PUT')[1].body.notes, 'A');
});


test('new launcher controls use the actual default harness rather than previous provider detail', async () => {
  const page = readFileSync(new URL('../entry/src/main/ets/components/AdminWorkspacePage.ets', import.meta.url), 'utf8');
  const method = page.match(/private launcherAgent\(\): string \{([^}]+)\}/)[1];
  const effectiveAgent = Function('Wire', 'return function() {' + method + '}')({ text: (row, key, fallback) => row[key] || fallback });
  assert.equal(effectiveAgent.call({ workspace: { launcherNew: true, launcherDetail: { agent: 'codex' } } }), 'claude');
  assert.equal(effectiveAgent.call({ workspace: { launcherNew: false, launcherDetail: { agent: 'codex' } } }), 'codex');
});

test('assistant Next-Message Suggestions switch writes directly via AdminWorkspace, syncs ClientModel, and rolls back on error', async () => {
  const store = { next_suggestions: false, notes: 'Keep' };
  let failPut = false;
  const env = fixture({ root: { onCall: async (call) => {
    if (call.path !== '/api/v1/assistant') return undefined;
    if (call.method === 'PUT') {
      if (failPut) throw new HttpFailure(500, 'server_error');
      Object.assign(store, call.body);
    }
    return { ...store };
  } } });
  const synced = [];
  env.root.syncNextSuggestions = (data) => synced.push(data.next_suggestions);

  const workspace = new AdminWorkspace();
  await workspace.open(env.root, 'assistant', true);
  assert.deepEqual(synced, [false]);
  workspace.setForm('notes', 'Unsaved draft');
  const ticket = workspace.capture('/api/v1/assistant');
  assert.equal(await workspace.setNextSuggestions(ticket, true), true);
  assert.deepEqual(env.shared.calls.filter((c) => c.method === 'PUT').map((c) => c.body), [{ next_suggestions: true }]);
  assert.equal(workspace.assistantData.next_suggestions, true);
  assert.equal(workspace.formText('notes'), 'Unsaved draft');
  assert.deepEqual(synced, [false, true]);

  failPut = true;
  assert.equal(await workspace.setNextSuggestions(workspace.capture('/api/v1/assistant'), false), false);
  assert.equal(workspace.assistantData.next_suggestions, true);

  const legacy = fixture();
  const old = new AdminWorkspace();
  await old.open(legacy.root, 'assistant', true);
  assert.equal(await old.setNextSuggestions(old.capture('/api/v1/assistant'), true), false);
  assert.deepEqual(legacy.shared.calls.filter((c) => c.method === 'PUT'), []);
});
