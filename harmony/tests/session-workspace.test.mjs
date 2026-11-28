import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/SessionWorkspace.ets') || context.parentURL?.endsWith('/FileTransfer.ets')) {
      if (specifier === '@kit.ArkTS') return { url: 'persona-session:arkts', shortCircuit: true };
      if (specifier === './ClientModel') return { url: 'persona-session:client', shortCircuit: true };
      if (specifier === './HubRepository') return { url: 'persona-session:repository', shortCircuit: true };
      if (specifier === '../platform/HubTransport') return { url: 'persona-session:transport', shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-session:arkts') return { format: 'module', source: 'export const util={generateRandomUUID:()=>crypto.randomUUID()};', shortCircuit: true };
    if (url === 'persona-session:client') return { format: 'module', source: 'export class ClientModel {}', shortCircuit: true };
    if (url === 'persona-session:repository') return { format: 'module', source: 'export const segment = encodeURIComponent;', shortCircuit: true };
    if (url === 'persona-session:transport') return { format: 'module', source: `
      export class HubTransport {}
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

const { SessionWorkspace } = await import('../entry/src/main/ets/models/SessionWorkspace.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure } = await import('persona-session:transport');

function clone(value) { return structuredClone(value); }
function waitForTurn() { return new Promise((resolve) => setTimeout(resolve, 0)); }

function session(id = 's1', fields = {}) {
  return ResourceRow.from({ id, session_id: id, machine_id: 'm1', manager: 'work', cwd: '/repo', workspace_id: 'w1', ...fields }, 'session');
}

function machine(fields = {}) {
  return ResourceRow.from({ id: 'm1', manager: 'work', name: 'MacBook', online: true, trust: '', ...fields }, 'machine');
}

function fixture(options = {}) {
  const state = { scope: 'hub:work', generation: 3, calls: [], attempts: [], uploads: [], blocked: [], beforeDispatch: undefined,
    handle: options.handle, features: options.features || ['file_edit', 'upload_dir', 'file_download', 'git_write', 'terminal_control', 'preview'] };
  const repository = {
    mutations: { blocked: () => state.blocked },
    transport: {
      sent: [],
      send(value) { this.sent.push(value); },
      stream(path) {
        const child = { path, closed: false, sent: [], callbacks: undefined,
          async connect(text, status, binary) { this.callbacks = { text, status, binary }; status('ready'); },
          send(value) { this.sent.push(value); return true; }, close() { this.closed = true; } };
        state.previewStream = child; return child;
      }
    },
    async call(path, method = 'GET', body = {}, _op = '', admit) {
      const call = { path, method, body: clone(body) };
      state.attempts.push(call);
      if (state.beforeDispatch) { const before = state.beforeDispatch; state.beforeDispatch = undefined; await before(call); }
      if (admit && !admit()) throw new HttpFailure(0, 'request_updated');
      state.calls.push(call);
      try {
        if (state.handle) {
          const result = await state.handle(call, state);
          if (result !== undefined) return clone(result);
        }
      } catch (error) {
        if (error.resultUnknown || error.resultPending) state.blocked.push({ path, method });
        throw error;
      }
      if (path.includes('/fs?')) return { path: '/repo', entries: [] };
      if (path.includes('/git/status')) return { is_repo: true, branch: 'main', upstream: 'origin/main', files: [], stashes: [] };
      if (path.includes('/git/branches')) return { branches: [{ name: 'main', current: true }, { name: 'topic', current: false }] };
      if (path.includes('/screen?')) return { text: 'terminal screen' };
      if (path.includes('/preview/servers')) return { servers: [{ name: 'web', port: 5173, url: 'http://localhost:5173/', command: ['npm', 'run', 'dev'], cwd: '/repo', listening: true }] };
      if (path.includes('/files/trash')) return { trashed: [{ path: '/repo/a.txt', trash_id: 'opaque-trash-id' }] };
      return { ok: true };
    },
    async upload(path, bytes, admit) {
      const attempt = { path, bytes };
      state.attempts.push(attempt);
      if (state.beforeDispatch) { const before = state.beforeDispatch; state.beforeDispatch = undefined; await before(attempt); }
      if (admit && !admit()) throw new HttpFailure(0, 'request_updated');
      state.uploads.push(attempt);
      return { path: '/repo/upload.bin' };
    }
  };
  const client = {
    selected: options.selected || session('s1', options.session || {}),
    machines: options.machines || [machine(options.machine || {})],
    profile: { manager: 'work' }, connected: options.connected !== false, demo: false,
    features: state.features,
    scope: () => state.scope,
    connectionGeneration: () => state.generation,
    owns: (generation, scope) => generation === state.generation && scope === state.scope,
    target(row = client.selected) { return JSON.stringify([state.scope, row.key()]); },
    repo: () => repository,
    viewRevision: () => 0,
    captureView: () => ({epoch:0,valid:()=>true,read:async()=>'',write:async()=>{}}),
    loadView: async () => '',
    saveView: async () => {},
    observeEvents: () => () => {},
    machineName: () => 'MacBook'
  };
  const workspace = new SessionWorkspace();
  workspace.bind(client); workspace.show();
  return { state, client, repository, workspace };
}

test('an unsupported terminal keeps its page subscription until the polling page leaves', async () => {
  const { state, repository, workspace } = fixture({ features: ['terminal_stream', 'terminal_control'] });
  const sent = [];
  repository.transport = { send: (raw) => sent.push(JSON.parse(raw)) };
  try {
    workspace.onConnected();
    workspace.selectTab(2);
    assert.equal(sent.filter((msg) => msg.type === 'terminal_subscribe').length, 1);
    workspace.receiveTerminal({ error: 'unsupported stream', unsupported: true });
    await waitForTurn();
    assert.ok(state.calls.some((call) => call.path.includes('/screen?')));
    assert.equal(sent.filter((msg) => msg.type === 'terminal_unsubscribe').length, 0);
    workspace.hide();
    assert.equal(sent.filter((msg) => msg.type === 'terminal_unsubscribe').length, 1);
    workspace.hide();
    assert.equal(sent.filter((msg) => msg.type === 'terminal_unsubscribe').length, 1);
  } finally {
    workspace.hide();
  }
});

test('file changes use the registered rename, move, trash and restore routes with exact bodies', async () => {
  const { state, workspace } = fixture();
  await waitForTurn();
  assert.equal(workspace.canEditFiles(), true);

  assert.equal(await workspace.renameFile('/repo/a.txt', 'renamed.txt'), true);
  assert.deepEqual(state.calls.find((call) => call.method === 'POST'), {
    path: '/api/v1/machines/m1/files/rename', method: 'POST', body: { path: '/repo/a.txt', cwd: '/repo', name: 'renamed.txt' }
  });
  assert.equal(await workspace.moveFiles(['/repo/renamed.txt'], '/repo/archive', 'keep'), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/files/move')).body,
    { paths: ['/repo/renamed.txt'], cwd: '/repo', dir: '/repo/archive', conflict: 'keep' });
  assert.equal(await workspace.trashFiles(['/repo/renamed.txt']), true);
  assert.deepEqual(workspace.trashUndo.map((item) => [item.path, item.trashId]), [['/repo/a.txt', 'opaque-trash-id']]);
  assert.equal(await workspace.restoreTrash(), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/files/restore')).body, { trash_ids: ['opaque-trash-id'] });
  workspace.hide();
});

test('file conflict is recoverable, while an uncertain write blocks further file edits', async () => {
  let uncertain = false;
  const { state, workspace } = fixture({ handle(call) {
    if (call.method === 'POST' && call.path.endsWith('/files/move')) throw new HttpFailure(409, 'exists');
    if (uncertain && call.method === 'POST' && call.path.endsWith('/files/rename')) throw new HttpFailure(0, 'result_unknown', true);
    return undefined;
  } });
  await waitForTurn();
  assert.equal(await workspace.moveFiles(['/repo/a.txt'], '/repo/target'), false);
  assert.equal(workspace.fileWriteState, 'conflict');
  uncertain = true;
  assert.equal(await workspace.renameFile('/repo/a.txt', 'next.txt'), false);
  assert.equal(workspace.fileWriteState, 'unknown');
  assert.equal(workspace.canEditFiles(), false);
  workspace.hide();
});

test('a target switch between journal admission and dispatch prevents stale file and upload writes', async () => {
  const { state, client, workspace } = fixture();
  await waitForTurn();
  state.beforeDispatch = async () => { client.selected = session('s2'); };
  assert.equal(await workspace.renameFile('/repo/a.txt', 'stale.txt'), false);
  assert.equal(state.calls.some((call) => call.path.endsWith('/files/rename')), false);
  client.selected = session('s1');
  state.generation++;
  workspace.bind(client); workspace.show();
  await waitForTurn();
  state.beforeDispatch = async () => { client.selected = session('s2'); };
  await assert.rejects(workspace.uploadFile('upload.bin', new Uint8Array([1]).buffer), /request_updated/);
  assert.equal(state.uploads.length, 0);
  workspace.hide();
});

test('git writes use registered bodies and stay acknowledged when the follow-up status read fails', async () => {
  let failStatus = false;
  const { state, workspace } = fixture({ handle(call) {
    if (failStatus && call.method === 'GET' && call.path.includes('/git/status')) throw new Error('status_offline');
    return undefined;
  } });
  await waitForTurn();
  workspace.selectTab(1); await workspace.loadGit();
  assert.equal(workspace.canWriteGit(), true);

  const file = Object.assign({ path: '/repo/a.txt' }, { staged: false, unstaged: true, untracked: false });
  assert.equal(await workspace.stageGitFile(file), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/stage')).body, { cwd: '/repo', paths: ['/repo/a.txt'] });
  assert.equal(await workspace.commitGit('ship it'), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/commit')).body, { cwd: '/repo', message: 'ship it' });
  assert.equal(await workspace.pushGit(true), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/push')).body, { cwd: '/repo', set_upstream: true });
  assert.equal(await workspace.switchGit('topic'), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/switch')).body, { cwd: '/repo', branch: 'topic' });
  assert.equal(await workspace.stashGit('push', '', 'save point', true), true);
  assert.deepEqual(state.calls.find((call) => call.path.endsWith('/git/stash')).body,
    { cwd: '/repo', op: 'push', message: 'save point', untracked: true });
  failStatus = true;
  assert.equal(await workspace.commitGit('ack is durable'), true);
  await waitForTurn();
  assert.equal(workspace.gitWriteState, 'acknowledged');
  workspace.hide();
});

test('unknown git and terminal key outcomes remain blocked without a replay', async () => {
  const { state, workspace } = fixture({ handle(call) {
    if (call.method === 'POST' && (call.path.endsWith('/git/commit') || call.path.endsWith('/keys')))
      throw new HttpFailure(0, 'result_unknown', true);
    return undefined;
  } });
  await waitForTurn();
  workspace.selectTab(1); await workspace.loadGit();
  assert.equal(await workspace.commitGit('may have committed'), false);
  assert.equal(workspace.gitWriteState, 'unknown');
  assert.equal(workspace.canWriteGit(), false);

  workspace.selectTab(3); await waitForTurn();
  assert.equal(workspace.screenControlRequested, false);
  assert.equal(workspace.enableScreenControl(), true);
  assert.equal(await workspace.sendScreenKeys(['ctrl+c'], 'typed text', workspace.screenRevision), false);
  assert.equal(workspace.screenControlRequested, false);
  assert.equal(state.calls.filter((call) => call.path.endsWith('/keys')).length, 1);
  assert.equal(await workspace.sendScreenKeys(['ctrl+c'], 'typed text', workspace.screenRevision), false);
  assert.equal(state.calls.filter((call) => call.path.endsWith('/keys')).length, 1);
  workspace.hide();
});

test('workspace mutations are disabled for an unconfirmed, offline, or read-only session', async () => {
  const pending = fixture({ machine: { trust: 'pending' } });
  await waitForTurn();
  assert.equal(pending.workspace.canEditFiles(), false);
  assert.equal(pending.workspace.canWriteGit(), false);
  pending.workspace.hide();

  const offline = fixture({ machine: { online: false } });
  await waitForTurn();
  assert.equal(offline.workspace.canEditFiles(), false);
  offline.workspace.hide();

  const readOnly = fixture({ session: { read_only: true } });
  await waitForTurn();
  assert.equal(readOnly.workspace.canEditFiles(), false);
  readOnly.workspace.hide();
});

test('the terminal screen is read-only by default and sends keys only after explicit enablement', async () => {
  const { state, workspace } = fixture();
  await waitForTurn();
  workspace.selectTab(3); await waitForTurn();
  assert.equal(workspace.screenText, 'terminal screen');
  assert.equal(workspace.screenControlRequested, false);
  assert.equal(workspace.canControlScreen(), true);
  assert.equal(workspace.enableScreenControl(), true);
  const revision = workspace.screenRevision;
  assert.equal(await workspace.sendScreenKeys(['enter'], 'pwd', revision), true);
  const keys = state.calls.find((call) => call.path.endsWith('/keys'));
  assert.deepEqual(keys.body, { keys: ['enter'], text: 'pwd' });
  assert.equal(await workspace.sendScreenKeys(['not-a-key'], '', revision), false);
  workspace.hide();
});

test('dev preview opens the authorized native stream without putting the Hub token in its URL', async () => {
  const { state, workspace } = fixture();
  await waitForTurn();
  workspace.selectTab(4); await waitForTurn();
  assert.equal(await workspace.openPreview(5173, '/guide', 'web'), true);
  assert.equal(state.previewStream.path, '/api/v1/machines/m1/preview/connect?port=5173&path=%2Fguide&w=390&h=844&dpr=1&mobile=1');
  assert.equal(state.previewStream.path.includes('token'), false);
  workspace.hide();
});

test('file transfer follows late capabilities of the captured connection and immediately revokes them',async()=>{
  const {client,state,workspace}=fixture({features:[]});
  const entry={name:'sample.txt',path:'/repo/sample.txt',directory:false,symlink:false,size:20,ignored:false};
  try {
    assert.equal(workspace.canSendFile(entry),false);
    client.features=['file_transfer'];
    assert.equal(workspace.canSendFile(entry),true);
    client.features=[];assert.equal(workspace.canSendFile(entry),false);
    client.features=['file_transfer'];state.generation++;
    assert.equal(workspace.canSendFile(entry),false);
  } finally {workspace.hide();}
});

const {folderCrumbs,parentFolder,indexWorkspaceFiles,findWorkspaceFiles,patchLines}=await import('../entry/src/main/ets/models/WorkspaceBrowser.ets');

test('each reachable breadcrumb keeps its own path, including the session directory when browsing deeper',()=>{
  const root='/Users/me',session=root+'/code/weather';
  const flags=(list)=>list.map(c=>[c.path,c.home,c.current,c.session]);
  assert.deepEqual(flags(folderCrumbs(session,root,session)),[
    [root,true,false,false],[root+'/code',false,false,false],[session,false,true,true]
  ]);
  const sub=folderCrumbs(session+'/Sources',root,session+'/');
  assert.deepEqual(flags(sub).slice(-2),[[session,false,false,true],[session+'/Sources',false,true,false]]);
  assert.equal(folderCrumbs('/Users/meal/code',root,session).some(c=>c.home||c.session),false);
  assert.deepEqual(flags(folderCrumbs('C:\\Users\\me\\repo\\src','C:\\Users\\me','c:\\Users\\me\\Repo'))[1],['C:\\Users\\me\\repo',false,false,true]);
  assert.deepEqual(flags(folderCrumbs('\\\\host\\share\\code','','\\\\host\\share'))[0],['\\\\host\\share',false,false,true]);
  assert.equal(folderCrumbs('/etc','',session)[0].home,false);
});

test('a measured breadcrumb keeps the current folder in view without moving a manually scrolled path',()=>{
  const source=readFileSync(new URL('../entry/src/main/ets/components/SessionTools.ets',import.meta.url),'utf8');
  const method=source.match(/^  private placeCurrentFolder\([\s\S]+?^  \}/m)[0];
  class Frame { constructor(run){this.run=run;} }
  const Page=new Function('FolderLayoutFrame','ScrollAlign','return '+stripTypeScriptTypes('class Page {'+method+'}'))(Frame,{END:'end'});
  const page=new Page(),frames=[],calls=[];let current=true;
  Object.assign(page,{filePathIdentity:'scope/weather',pathCrumbs:[{},{},{}],workspace:{isCurrent:()=>current},
    folderScroller:{scrollToIndex:(...args)=>calls.push(args)},getUIContext:()=>({postFrameCallback:f=>frames.push(f)})});
  const layout=(owner,width,height=44)=>page.placeCurrentFolder(owner,{width,height});
  layout('scope/weather',0);assert.equal(frames.length,0);
  layout('scope/weather',214);layout('scope/weather',214);assert.equal(frames.length,1);
  frames.shift().run();assert.deepEqual(calls,[[2,false,'end']]);
  layout('scope/weather',214);assert.equal(frames.length,0,'reading back toward home is not interrupted');
  layout('scope/weather',186,53.3);
  page.filePathIdentity='scope/another';layout('scope/another',186,53.3);
  frames.shift().run();assert.equal(calls.length,1,'a retired path cannot scroll its sibling');
  frames.shift().run();assert.equal(calls.length,2);
  layout('scope/another',180,53.3);current=false;frames.shift().run();assert.equal(calls.length,2);
  assert.match(source,/listDirection\(Axis.Horizontal\)\.stackFromEnd\(true\)\.alignListItem\(ListItemAlign.Center\)/);
});

test('folder history traverses above the session folder, clears stale rows and invalidates forward on a new branch',async()=>{
  const {workspace,state}=fixture();
  await waitForTurn();
  state.handle=async call=>{
    if (call.path.includes('/fs?')) {
      const path=new URL(call.path,'https://test.invalid').searchParams.get('path');
      return {path,entries:[{name:'source',dir:true}]};
    }
  };
  await workspace.openPath('/repo/Sources');
  assert.equal(workspace.currentPath,'/repo/Sources');
  await workspace.openPath('/repo/Sources/App');
  await workspace.historyFolder(false);assert.equal(workspace.currentPath,'/repo/Sources');
  await workspace.historyFolder(true);assert.equal(workspace.currentPath,'/repo/Sources/App');
  await workspace.historyFolder(false);await workspace.openPath('/repo/Tests');assert.deepEqual(workspace.folderForward,[]);
  await workspace.openPath('/repo');workspace.openParent();await waitForTurn();assert.equal(workspace.currentPath,'/');
  assert.equal(parentFolder('/'),'');
  assert.deepEqual(folderCrumbs('/Users/me/code/weather','/Users/me').map(x=>[x.title,x.path]),
    [['~','/Users/me'],['code','/Users/me/code'],['weather','/Users/me/code/weather']]);
  assert.deepEqual(folderCrumbs('C:\\Users\\me\\repo','C:\\Users\\me').map(x=>x.path),['C:\\Users\\me','C:\\Users\\me\\repo']);
  assert.equal(parentFolder('C:\\one'),'C:\\');
  assert.equal(parentFolder('\\\\host\\share\\one'),'\\\\host\\share');
  assert.equal(parentFolder('\\\\host\\share'),'');
  assert.ok(state.calls.every(call=>call.method==='GET'));
  workspace.hide();
});

test('recursive file search preserves relative scope, derives folders, respects hidden files, and drops a stale response after navigation',async()=>{
  const {workspace,state}=fixture();await waitForTurn();let resolve;
  state.handle=async call=>{
    if (call.path.includes('/fs/find')) return new Promise(r=>{ resolve=r; });
    if (call.path.includes('/fs?')) return {path:new URL(call.path,'https://test.invalid').searchParams.get('path'),entries:[]};
  };
  const read=workspace.loadFileIndex();await waitForTurn();await workspace.openPath('/repo/other');
  resolve({files:['wrong-owner.txt']});await read;
  assert.equal(workspace.findRoot,'');assert.deepEqual(workspace.findNames,[]);
  const names=['Sources/a.swift','Sources/deep/b.swift','.hidden/file.swift','../bad.swift','/absolute.swift','C:\\unsafe.swift','Sources/a.swift','readme','readme.archive'];
  const idx=indexWorkspaceFiles(names,false);
  assert.deepEqual(findWorkspaceFiles(idx,'deep').map(x=>[x.relative,x.directory]),[['Sources/deep',true],['Sources/deep/b.swift',false]]);
  assert.deepEqual(findWorkspaceFiles(idx,'swift').map(x=>x.relative),['Sources/a.swift','Sources/deep/b.swift']);
  assert.deepEqual(findWorkspaceFiles(indexWorkspaceFiles(names,true),'.hidden').map(x=>x.relative),['.hidden','.hidden/file.swift']);
  assert.equal(findWorkspaceFiles(idx,'readme',1)[0].relative,'readme');workspace.hide();
});

test('a status refresh retains the commit page only for the same history, and commit inspection drops the previous selection',async()=>{
  const {workspace,state}=fixture();
  const sha=n=>n.toString(16).padStart(40,'0');
  let base=1, release, oldShow;
  state.handle=async call=>{
    if (call.path.includes('/git/status')) return {is_repo:true,branch:'main',commits:Array.from({length:30},(_,i)=>({hash:sha(base+i),subject:'row '+(base+i)})),files:[]};
    if (call.path.includes('/git/log')) { assert.equal(new URL(call.path,'https://test.invalid').searchParams.get('skip'),'30');return new Promise(r=>{release=r}); }
    if (call.path.includes('/git/show')) {
      const id=new URL(call.path,'https://test.invalid').searchParams.get('hash');if(id===sha(1))return new Promise(r=>{oldShow=r});
      return {commit:{hash:id,subject:'selected'},files:[],diff:'selected diff'};
    }
  };
  workspace.selectTab(1);await waitForTurn();assert.equal(workspace.gitCommits.length,30);assert.equal(workspace.gitMoreCommits,true);
  const old=workspace.showGitCommit(sha(1));await waitForTurn();await workspace.showGitCommit(sha(2));
  oldShow({commit:{hash:sha(1)},files:[],diff:'wrong diff'});await old;assert.equal(workspace.gitShow.diff,'selected diff');
  const page=workspace.olderGitCommits();await waitForTurn();release({commits:[{hash:sha(30)},{hash:sha(31)}],has_more:false});await page;
  assert.equal(workspace.gitCommits.length,31);assert.equal(workspace.gitMoreCommits,false);
  await workspace.loadGit();assert.equal(workspace.gitCommits.length,31);
  base=5;await workspace.loadGit();assert.equal(workspace.gitCommits.length,30);assert.equal(workspace.gitMoreCommits,true);
  const stale=workspace.olderGitCommits();await waitForTurn();base=70;await workspace.loadGit();
  release({commits:[{hash:sha(888)}],has_more:false});await stale;assert.equal(workspace.gitCommits.length,30);
  assert.equal(workspace.gitCommits[0].hash,sha(70));assert.ok(state.calls.every(call=>call.method==='GET'));workspace.hide();
});

test('switching between staged and working-tree patches cannot display the wrong half, and line numbers restart at each hunk',async()=>{
  const {workspace,state}=fixture();const pending=[];
  state.handle=async call=>{
    if(call.path.includes('/git/status'))return {is_repo:true,branch:'main',files:[{path:'Sources/App.swift',index:'M',worktree:'M'}],commits:[]};
    if(call.path.includes('/git/diff?'))return new Promise(r=>pending.push({staged:new URL(call.path,'https://test.invalid').searchParams.get('staged'),resolve:r}));
  };
  workspace.selectTab(1);await waitForTurn();
  const a=workspace.openGitDiff(workspace.gitFiles[0],false);await waitForTurn();
  const b=workspace.openGitDiff(workspace.gitFiles[0],true);await waitForTurn();
  pending[1].resolve({diff:'staged patch'});await b;pending[0].resolve({diff:'stale working patch'});await a;
  assert.deepEqual(pending.map(x=>x.staged),['0','1']);assert.equal(workspace.gitDiff,'staged patch');
  const c=workspace.loadGitDiff();await waitForTurn();workspace.closeGitDiff();pending[2].resolve({diff:'closed'});await c;
  assert.equal(workspace.gitDiff,'');assert.equal(workspace.gitDiffBusy,false);
  const patch='--- a/a\n+++ b/a\n@@ -8,2 +12,2 @@\n-removed\n+added\n both\n@@ -22 +90 @@\n next';
  assert.deepEqual(patchLines(patch).map(v=>[v.kind,v.oldLine,v.newLine]),[
    ['meta','',''],['meta','',''],['hunk','',''],['removed','8',''],['added','','12'],['context','9','13'],['hunk','',''],['context','22','90']]);
  assert.ok(state.calls.every(call=>call.method==='GET'));workspace.hide();
});

test('folder changes replace names, symlink targets and modification times as one listing even when the previous folder returns late',async()=>{
  const {workspace,state}=fixture();
  await waitForTurn();let finishOld;
  state.handle=async call=>{
    if (!call.path.includes('/fs?')) return;
    const path=new URL(call.path,'https://fixture.invalid').searchParams.get('path');
    if (path==='/repo/old') return new Promise(resolve=>{finishOld=resolve});
    return {path,entries:[{name:'name with space.md',dir:false,mtime:'2026-11-20T06:41:00Z',symlink:true,link_target:'../README.md',link_broken:false,size:12}]};
  };
  const old=workspace.openPath('/repo/old');await waitForTurn();assert.deepEqual(workspace.entries,[]);
  await workspace.openPath('/repo/new');finishOld({path:'/repo/old',entries:[{name:'stale.md',mtime:'2023-01-01T00:00:00Z'}]});await old;
  assert.equal(workspace.currentPath,'/repo/new');
  assert.deepEqual(workspace.entries.map(f=>[f.name,f.path,f.mtime,f.linkTarget,f.linkBroken]),[
    ['name with space.md','/repo/new/name with space.md','2026-11-20T06:41:00Z','../README.md',false]]);
  const {workspaceAgeMinutes}=await import('../entry/src/main/ets/models/WorkspaceBrowser.ets');
  const now=Date.parse('2026-11-20T06:43:30Z');assert.equal(workspaceAgeMinutes(workspace.entries[0].mtime,now),2);
  assert.equal(workspaceAgeMinutes('0001-01-01T00:00:00Z',now),-1);assert.equal(workspaceAgeMinutes('not-a-date',now),-1);
  const index=indexWorkspaceFiles(['one\\two.cs','\\\\server\\share\\no.cs'],false,true);
  assert.deepEqual(findWorkspaceFiles(index,'two').map(h=>h.relative),['one/two.cs']);
  assert.deepEqual(findWorkspaceFiles(indexWorkspaceFiles(['literal\\backslash.txt'],false),'literal').map(h=>h.relative),['literal\\backslash.txt']);
  workspace.hide();
});

test('commit file navigation selects the matching immutable patch, including missing, deleted and renamed paths', async () => {
  const {commitFilePatch} = await import('../entry/src/main/ets/models/WorkspaceBrowser.ets');
  const added = 'diff --git a/first file b/first file\n--- /dev/null\n+++ b/first file\n@@ -0,0 +1 @@\n+first';
  const deleted = 'diff --git a/deleted b/deleted\n--- a/deleted\n+++ /dev/null\n@@ -1 +0,0 @@\n-old';
  const renamed = 'diff --git a/old b/renamed b/prefix\nsimilarity index 100%\nrename from old\nrename to renamed b/prefix';
  const encoded = String.raw`diff --git "a/\303\251" "b/\303\251"
--- "a/\303\251"
+++ "b/\303\251"
@@ -1 +1 @@
-old
+nouveau`;
  const patch = [added,deleted,renamed,encoded].join('\n');
  assert.equal(commitFilePatch(patch,'first file'),added);
  assert.equal(commitFilePatch(patch,'deleted'),deleted);
  assert.equal(commitFilePatch(patch,'renamed b/prefix'),renamed);
  assert.equal(commitFilePatch(patch,'é'),encoded);
  assert.equal(commitFilePatch(patch,'prefix'),'');
  assert.equal(commitFilePatch(patch,'missing'),'');
  assert.equal(patchLines(added, true)[0].kind,'hunk');
  assert.equal(patchLines('Binary files differ', true)[0].text,'Binary files differ');
  assert.equal(patchLines(added)[0].kind,'meta');
});

test('file and folder marks use the first changed descendant in this repository and expire with its owner', async () => {
  const files = [
    {path: 'Sources/Sub/App.swift', index: '.', worktree: 'M'},
    {path: 'Sources/Other.swift', index: 'A', worktree: '.'},
    {path: 'Readme.md', index: '?', worktree: '?'}
  ];
  const {workspace, state} = fixture({handle: ({path}) => path.includes('/git/status') ? {is_repo:true, root:'/repo', files} : undefined});
  await waitForTurn();
  assert.equal(workspace.fileGitMark({path:'/repo/Sources'}), 'M');
  assert.equal(workspace.fileGitMark({path:'/repo/Sources/Sub'}), 'M');
  assert.equal(workspace.fileGitMark({path:'/repo/Sources/Other.swift'}), 'A');
  assert.equal(workspace.fileGitMark({path:'/repo/Readme.md'}), 'U');
  assert.equal(workspace.fileGitMark({path:'/repo/Source'}), '');
  const pending = Promise.withResolvers();
  state.handle = ({path}) => path.includes('/git/status') ? pending.promise : undefined;
  const refresh = workspace.loadFileGitMarks();
  workspace.selectTab(1);
  pending.resolve({is_repo:true, root:'/repo', files:[{path:'Sources',index:'D',worktree:'.'}]});
  await refresh;
  assert.equal(workspace.fileGitMark({path:'/repo/Sources'}), 'M');
  workspace.hide();
});

test('a branch pull request is owner-scoped, cached by pushed head and creatable only from a confirmed pushed branch', async () => {
  let branch = 'feature', ahead = 1, currentPR = null;
  const {workspace, state} = fixture({handle: (call) => {
    if (call.path.includes('/git/status')) return {is_repo:true, root:'/repo', branch, upstream:'origin/'+branch, ahead, head:{hash:branch}, files:[], commits:[]};
    if (call.path.includes('/git/pr?')) return {branch, pr:currentPR};
    if (call.path.endsWith('/git/pr') && call.method==='POST') {
      currentPR={number:7, title:call.body.title}; return {branch,pr:currentPR};
    }
  }});
  workspace.selectTab(1);
  await waitForTurn();
  assert.equal(workspace.branchPushedForPr(),false);
  assert.equal(await workspace.createPullRequest(branch,'Reviewed','Body','main',true),false);
  await workspace.loadGit();
  assert.equal(state.calls.filter(c=>c.path.includes('/git/pr?')).length,1);
  ahead=0;
  await workspace.loadGit();
  await waitForTurn();
  assert.equal(workspace.branchPushedForPr(),true);
  assert.equal(await workspace.createPullRequest('other','Reviewed','Body','main',true),false);
  assert.equal(await workspace.createPullRequest(branch,'  Reviewed  ','Body','main',true),true);
  await waitForTurn();
  assert.equal(workspace.gitPrAnswer.pr.number,7);
  assert.equal(await workspace.createPullRequest(branch,'Duplicate','Body','main',true),false);
  const writes=state.calls.filter(c=>c.method==='POST' && c.path.endsWith('/git/pr'));
  assert.equal(writes.length,1);
  assert.deepEqual(writes[0].body,{cwd:'/repo',title:'Reviewed',body:'Body',base:'main',draft:true});
  const delayed=Promise.withResolvers();
  state.handle=({path})=> path.includes('/git/pr?') ? delayed.promise : undefined;
  const refresh=workspace.loadGitPullRequest(true);
  workspace.selectTab(0);
  delayed.resolve({branch,pr:{number:42}});
  await refresh;
  assert.equal(workspace.gitPrAnswer.pr.number,7);
  workspace.hide();
});

test('the Git footer stages every reported path once and unstages only when all changes were already staged',async()=>{
  let files=[{path:'a',index:'M',worktree:'.'},{path:'b',index:'M',worktree:'M'}];
  const status=()=>({is_repo:true,root:'/repo',branch:'main',files,commits:[]});
  const {workspace,state}=fixture({handle(call){
    if (call.method==='POST' && call.path.endsWith('/git/stage')) {
      files=files.map(file=>({...file,index:call.body.unstage ? '.' : 'M',worktree:call.body.unstage ? 'M' : '.'}));
      return {status:status()};
    }
    if (call.path.includes('/git/status')) return status();
  }});
  workspace.selectTab(1); await waitForTurn();
  assert.equal(workspace.allFilesStaged(),false);
  assert.equal(await workspace.toggleAllGitFiles(),true); await waitForTurn();
  assert.equal(workspace.allFilesStaged(),true);
  assert.equal(await workspace.toggleAllGitFiles(),true);
  assert.deepEqual(state.calls.filter(c=>c.method==='POST').map(c=>c.body),[
    {cwd:'/repo',paths:['a','b']},{cwd:'/repo',paths:['a','b'],unstage:true}]);
  workspace.hide();
});

test('pull uses the tracked checkout, installs the returned status, and works on Hubs predating git_write',async()=>{
  let behind=2;
  const status=()=>({is_repo:true,root:'/repo',branch:'main',upstream:'origin/main',behind,files:[],commits:[{hash:behind?'old':'new'}]});
  const {workspace,state}=fixture({features:[],handle:call=>{
    if(call.path.includes('/git/status')) return status();
    if(call.path.endsWith('/git/pull')){
      assert.equal(call.method,'POST');assert.deepEqual(call.body,{cwd:'/repo'});
      const updated=behind>0;behind=0;return {updated,status:status()};
    }
  }});
  workspace.selectTab(1);await waitForTurn();
  assert.equal(workspace.canWriteGit(),false);assert.equal(workspace.canPullGit(),true);
  assert.equal(await workspace.pullGit(),true);
  assert.equal(workspace.gitPullState,'updated');assert.equal(workspace.gitStatus.behind,0);assert.equal(workspace.gitCommits[0].hash,'new');
  assert.equal(await workspace.pullGit(),true);assert.equal(workspace.gitPullState,'current');
  assert.equal(state.calls.filter(c=>c.path.endsWith('/git/pull')).length,2);
});

test('pull rejects missing and deleted upstreams, read-only sessions and a branch changed before dispatch',async()=>{
  const {workspace,state}=fixture();
  workspace.selectTab(1);await waitForTurn();
  const tracked={is_repo:true,root:'/repo',branch:'main',upstream:'origin/main',files:[],commits:[]};
  for(const change of [{upstream:''},{gone:true},{branch:''}]){
    workspace.gitStatus={...tracked,...change};assert.equal(await workspace.pullGit(),false);
  }
  workspace.gitStatus=tracked;
  const readOnly=fixture({session:{read_only:true}});
  readOnly.workspace.selectTab(1);await waitForTurn();readOnly.workspace.gitStatus=tracked;
  assert.equal(await readOnly.workspace.pullGit(),false);
  state.beforeDispatch=()=>{workspace.gitStatus={...tracked,branch:'another',upstream:'origin/another'};};
  assert.equal(await workspace.pullGit(),false);
  assert.equal(state.calls.filter(c=>c.path.endsWith('/git/pull')).length,0);
});

test('pull reports a refused dirty checkout and cannot apply an in-flight reply to another branch',async()=>{
  const tracked={is_repo:true,root:'/repo',branch:'main',upstream:'origin/main',files:[],commits:[]};
  let pending,first=true;
  const {workspace,state}=fixture({handle:call=>{
    if(call.path.includes('/git/status')) return tracked;
    if(call.path.endsWith('/git/pull')){
      if(first){first=false;throw new HttpFailure(409,'dirty_worktree');}
      return new Promise(resolve=>{pending=resolve;});
    }
  }});
  workspace.selectTab(1);await waitForTurn();assert.equal(await workspace.pullGit(),false);assert.equal(workspace.gitWriteState,'failed');
  assert.equal(workspace.hasGitPullIssue(),true);
  const attempt=workspace.pullGit();await waitForTurn();assert.equal(workspace.gitPullState,'pulling');
  assert.equal(await workspace.pullGit(),false);
  assert.equal(workspace.gitPullState,'pulling');
  workspace.gitStatus={...tracked,branch:'other',upstream:'origin/other'};
  pending({status:tracked,updated:true});await attempt;
  assert.equal(workspace.gitStatus.branch,'other');assert.equal(workspace.gitPullState,'');
  assert.equal(state.calls.filter(c=>c.path.endsWith('/git/pull')).length,2);
});

test('pull rechecks dirty files on the original checkout before dispatching a write',async()=>{
  let files=[{path:'owned.txt',index:'.',worktree:'M'}];
  const status=()=>({is_repo:true,root:'/repo',branch:'main',upstream:'origin/main',files,commits:[]});
  const {workspace,state}=fixture({handle:call=>{
    if(call.path.includes('/git/status')) return status();
    if(call.path.endsWith('/git/pull')) return {updated:false,status:status()};
  }});
  workspace.selectTab(1);await waitForTurn();
  assert.equal(await workspace.pullGit(),false);
  assert.equal(workspace.gitPullState,'failed');
  assert.equal(workspace.gitWriteError,'session_git_pull_uncommitted_changes');
  assert.equal(state.calls.filter(c=>c.path.endsWith('/git/pull')).length,0);
  files=[];assert.equal(await workspace.pullGit(),true);
  assert.equal(workspace.gitPullState,'current');
  assert.equal(workspace.hasGitPullIssue(),false);
  assert.equal(state.calls.filter(c=>c.path.endsWith('/git/pull')).length,1);
});

test('branch feedback distinguishes a definite refusal from an unconfirmed pull',async()=>{
  let branch='main';
  const status=()=>({is_repo:true,root:'/repo',branch,upstream:'origin/'+branch,files:[],commits:[]});
  let denied=true;
  const {workspace,state}=fixture({handle:call=>{
    if (call.path.includes('/git/status')) return status();
    if (call.path.endsWith('/git/pull')) {
      if (denied) throw new HttpFailure(409,'not_fast_forward');
      throw new HttpFailure(0,'result_unknown',true);
    }
  }});
  workspace.selectTab(1);await waitForTurn();
  assert.equal(await workspace.pullGit(),false);
  assert.equal(workspace.gitPullState,'failed');
  assert.equal(workspace.gitWriteError,'session_git_pull_not_fast_forward');
  assert.equal(workspace.canPullGit(),true);
  branch='release';await workspace.loadGit();
  assert.equal(workspace.gitPullState,'');
  assert.equal(workspace.gitWriteError,'');
  denied=false;assert.equal(await workspace.pullGit(),false);
  assert.equal(workspace.gitPullState,'unknown');
  assert.equal(workspace.gitWriteState,'unknown');
  assert.equal(workspace.canPullGit(),false);
  assert.equal(await workspace.pullGit(),false);
  assert.equal(state.calls.filter(call=>call.path.endsWith('/git/pull')).length,2);
});
