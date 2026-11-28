import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import test from 'node:test';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.ArkTS') return { url: 'worktree:arkts', shortCircuit: true };
    if (specifier.endsWith('platform/HubTransport')) return { url: 'worktree:transport', shortCircuit: true };
    if (specifier === '../core/SessionNaming' && context.parentURL?.endsWith('.ets'))
      return { url: new URL(specifier + '.ts', context.parentURL).href, shortCircuit: true };
    if (specifier.startsWith('.') && context.parentURL?.endsWith('.ets'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'worktree:arkts') return { format: 'module', shortCircuit: true, source:
      "import { randomUUID } from 'node:crypto'; export const util = { generateRandomUUID:randomUUID, Base64Helper:class {decodeSync(b){return Buffer.from(b,'base64')}}, TextDecoder:{create:()=>({decodeToString:b=>new TextDecoder().decode(b)})} };" };
    if (url === 'worktree:transport') return { format: 'module', shortCircuit: true, source:
      'export class HttpFailure extends Error { constructor(status,code,resultUnknown=false,resultPending=false) {super(code);Object.assign(this,{status,code,resultUnknown,resultPending});} }' };
    if (!url.endsWith('.ets')) return next(url, context);
    const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*|@Trace\s+/g, '');
    return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
  }
});
const { WorktreeActions, validWorktreeBranch, checkoutPath } = await import('../entry/src/main/ets/models/WorktreeActions.ets');
const { ProjectCatalog, ProjectWorktree } = await import('../entry/src/main/ets/models/ProjectCatalog.ets');
const { ResourceRow, Wire } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure } = await import('worktree:transport');

function fixture() {
  const queries = [], writes = [], blocked = [], finished = [], replayed = [], reads = [], views = new Map();
  const preview = { workspace_id: 'child', kind: 'herdr', path: '/qa/child', branch: 'qa/child', preview_token: 'this-preview', dirty: 0 };
  const removed = { workspace_id: 'child', state: 'removed', removed: true, branch_deleted: true };
  const behavior = { dirty: 0, unknown: false, renameReceipt: { ok: true }, features:['client_ops'], version:'0.9.1',
    workspaces:[{machine_id:'Studio/1',source:'herdr',source_id:'child',cwd:'/qa/child',title:'Child',branch:'main',online:true,available:true}],
    operation:'missing', receiptBody:undefined, replayError:undefined };
  const repo = {
    mutations: { blocked: () => blocked.filter(r=>r.state==='pending'||r.state==='unknown'), record: id => blocked.find(r=>r.id===id),
      async acknowledgeRemovedRename(record,viewScope,oldKey,raw,currentKey,admit) {
        if (!admit() || views.get(oldKey)!==raw || views.get(currentKey))return false;
        record.state='checked';finished.push([record.id,'checked']);return true;
      },
      acknowledge: async (id,admit) => {
        const record=blocked.find(r=>r.id===id);
        if (record?.state==='unknown' && !record.requiresReceipt && !record.confirmedPending && (!admit || admit())) await repo.mutations.finish(record,'checked');
      }, finish: async (record, state, admit) => {
      if (!admit || admit()) {record.state=state;finished.push([record.id,state]);}} },
    transport: { async request(path, method, json, id) {
      const body = JSON.parse(json);
      if (path.endsWith('/create')) {
        replayed.push({path,method,json,id});if(behavior.waitReplay) await behavior.waitReplay;if(behavior.replayError)throw behavior.replayError;
        return JSON.stringify(behavior.replayAnswer||{workspace_id:'new-id',branch:body.branch||'automatic',path:body.path||'/qa/automatic'});
      }
      queries.push({ path, method, body });
      if (path.endsWith('/prepare')) return JSON.stringify({ root: '/repo', path: '/qa', branch: 'main', automatic_name: true });
      return JSON.stringify(body.reconcile ? removed : { ...preview, dirty: behavior.dirty });
    }},
    async call(path, method, body, id, admit, _, acknowledged) {
      if (!method || method==='GET') {
        reads.push(path);
        if(path==='/api/v1/health')return behavior.publicHealth?{ok:true,hub_id:'hub-id'}:{ok:true,version:behavior.version,features:behavior.features,hub_id:'hub-id'};
        if(path==='/api/v1/workspaces')return {workspaces:behavior.workspaces,errors:behavior.errors||[],...behavior.catalogue};
        if(path.startsWith('/api/v1/ops/')) {
          if(behavior.operation==='missing')throw new HttpFailure(404,'no such operation');
          return {id:behavior.receiptId||path.split('/').at(-1),kind:behavior.kind||'worktree_create',state:behavior.operation,
            target:{path:behavior.receiptPath||'/api/v1/machines/Studio/1/worktree/create'},
            result:behavior.receiptBody?{status:200,body:Buffer.from(JSON.stringify(behavior.receiptBody)).toString('base64')}:{}};
        }
        throw Error('unexpected read '+path);
      }
      assert.equal(admit(), true); writes.push({ path, method, body, id });
      const result = path.endsWith('/delete') ? removed : path.endsWith('/rename') ? behavior.renameReceipt :
        behavior.createReply || { workspace_id: 'new-id', path: body.path || '/qa/automatic', branch: body.branch || 'automatic', parent_workspace_id: 'child' };
      if (behavior.unknown || !acknowledged(result)) { blocked.push({ id,path,method,state:'unknown',createdAt:new Date().toISOString() }); throw new HttpFailure(0, 'result_unknown', true); }
      return result;
    }
  };
  const owner = { connected: true, epoch: 3, name: 'home', profile:{hubId:'hub-id'},features:['client_ops'],connectionGeneration() { return this.epoch; },
    verifiedServerVersion(feature){return this.features.includes(feature)?behavior.helloVersion||'':'';},
    async savedRenameTargets(group,manager,machine,source,id) {
      const root=this.name+':worktree-request:', prefix=JSON.stringify([this.name,group,manager,machine,source]).slice(0,-1)+',';
      return [...views].filter(([k,raw])=>k.startsWith(root+prefix) && k.endsWith(',"rename"]') && raw.includes(id)).map(([k])=>k.slice(root.length));
    },
    captureView(key) {
      const scoped = this.name + ':' + key;
      return {storageKey:scoped,valid: () => true, read: async () => views.get(scoped) || '', write: async value => { views.set(scoped,value); },
        clearIfUnchanged: async expected => { if (views.get(scoped) === expected) views.delete(scoped); }};
    },
    scope() { return this.name; }, owns(epoch, scope) { return this.name === scope && this.epoch === epoch; }, repo: () => repo };
  const root = { ...owner, name: 'root', viewFor: async () => owner,
    directoryOwnerFor: row => row.group === 'home' && row.manager === 'default' ? owner : undefined };
  const tree = Object.assign(new ProjectWorktree(), { source: 'herdr', sourceId: 'child', machine: 'Studio/1', machineTitle: 'Studio',
    group: 'home', manager: 'default', cwd: '/qa/child', title: 'Child', online: true, startAllowed: true });
  return { form: new WorktreeActions(), root, owner, tree, behavior, writes, queries, finished, blocked, replayed, reads, views, cleanupResult: removed };
}

test('typed branch follows the prepared directory, while an explicitly edited directory survives typing', async () => {
  const { form, root, tree, writes } = fixture();
  await form.open(root, tree, 'create');
  assert.equal(form.canSubmit, false);
  form.setBranch('q'); form.setPath('/qa/q'); form.setBranch('qa/Mobile');
  assert.equal(form.path, '/qa/qa-mobile');
  form.setPath('/custom'); form.setBranch('qa/Other'); assert.equal(form.path, '/custom');
  form.setPath(''); assert.equal(form.path, '/qa/qa-other');
  const result = await form.submit();
  assert.equal(result.id, 'new-id'); assert.equal(result.parent, 'child');
  assert.deepEqual(writes[0].body, { workspace_id: 'child', kind: 'herdr', branch: 'qa/Other', path: '/qa/qa-other' });
  assert.equal(writes[0].path, '/api/v1/machines/Studio%2F1/worktree/create');
  assert.equal(writes[0].id.length, 36);
  assert.equal(await form.submit(), undefined);
  assert.equal(writes.length, 1);
  for (const name of ['../bad', 'a.lock', '@', '-x', 'a//b', 'foo@{one', 'a b', 'a~1', 'a:1']) assert.equal(validWorktreeBranch(name), false);
  assert.equal(checkoutPath('C:\\qa', 'fix/One'), 'C:\\qa\\fix-one');
});

test('automatic creation has no stale path, renaming only sends its display name and requires host acknowledgement', async () => {
  const { form, root, tree, writes, behavior } = fixture();
  await form.open(root, tree, 'create'); form.automatic = true; await form.submit();
  assert.deepEqual(writes[0].body, { workspace_id: 'child', kind: 'herdr', auto_name: true });
  await form.open(root, tree, 'rename'); form.name = '  Mobile review  '; await form.submit();
  assert.deepEqual(writes[1].body, { workspace_id: 'child', kind: 'herdr', name: 'Mobile review' });
  behavior.renameReceipt = {};
  await form.open(root, tree, 'rename'); await form.submit();
  assert.equal(form.unknown, true); assert.equal(form.canSubmit, false);
});

test('dirty preview and cancel preserve the checkout, and a clean preview retains its branch by default', async () => {
  const { form, root, tree, behavior, writes, queries } = fixture();
  behavior.dirty = 1; await form.open(root, tree, 'clean');
  assert.equal(form.canSubmit, false); assert.equal(await form.submit(), undefined);
  form.force = true; assert.equal(form.canSubmit, true); form.close();
  assert.equal(writes.length, 0);
  behavior.dirty = 0; await form.open(root, tree, 'clean'); assert.equal(form.force, false);
  assert.deepEqual(queries[1].body, { workspace_id:'child', kind:'herdr', preview:true });
  await form.submit(); assert.equal(writes[0].body.delete_branch, false); assert.equal(writes[0].body.preview_token, 'this-preview');
});

test('a lost cleanup reply is reconciled without issuing a second mutation or leaking its token across owners', async () => {
  const { form, root, tree, behavior, writes, queries, finished } = fixture();
  await form.open(root, tree, 'clean'); form.deleteBranch = true; behavior.unknown = true;
  assert.equal(await form.submit(), undefined); assert.equal(form.unknown, true);
  assert.equal(await form.submit(), undefined);
  const result = await form.reconcile();
  assert.equal(result.action, 'clean'); assert.equal(writes.length, 1); assert.equal(writes[0].body.delete_branch, true);
  assert.equal(queries[1].body.reconcile, true);
  assert.deepEqual(finished, [[writes[0].id,'done']]);
  await form.open(root, tree, 'clean'); root.epoch++; assert.equal(await form.submit(), undefined);
  assert.equal(form.error, 'tree_preview_changed'); assert.equal(writes.length, 1);
});

test('cleanup reads the confirmed branch option across a lost reply and a reopened form', async () => {
  for (const requested of [true, false]) for (const deleted of [undefined, false, true]) for (const reopen of [false, true]) {
    const { form, root, tree, behavior, writes, queries, finished, cleanupResult } = fixture();
    behavior.unknown = true;
    await form.open(root, tree, 'clean');
    form.deleteBranch = requested;
    assert.equal(await form.submit(), undefined);
    assert.equal(form.unknown, true);
    if (deleted === undefined) delete cleanupResult.branch_deleted;
    else cleanupResult.branch_deleted = deleted;

    const recovery = reopen ? new WorktreeActions() : form;
    if (reopen) {
      form.close();
      await recovery.open(root, tree, 'clean');
    }
    assert.equal(recovery.deleteBranch, requested);
    recovery.deleteBranch = !requested; // A late native control update cannot change the dispatched payload.
    const result = await recovery.reconcile();
    assert.equal(result.branchRetained, requested && deleted !== true, JSON.stringify({ requested, deleted, reopen }));
    assert.equal(writes.length, 1);
    assert.equal(writes[0].body.delete_branch, requested);
    assert.deepEqual(queries.at(-1).body, { workspace_id: 'child', kind: 'herdr', preview_token: 'this-preview', reconcile: true });
    assert.deepEqual(finished, [[writes[0].id, 'done']]);
  }
});

test('a switch update while cleanup is in flight cannot change its result notice', async () => {
  const { form, root, tree, owner, writes, cleanupResult } = fixture();
  delete cleanupResult.branch_deleted;
  const sent = Promise.withResolvers(), reply = Promise.withResolvers();
  const repo = owner.repo(), call = repo.call.bind(repo);
  repo.call = async (...args) => {
    const result = await call(...args);
    sent.resolve();
    await reply.promise;
    return result;
  };
  await form.open(root, tree, 'clean');
  form.deleteBranch = true;
  const pending = form.submit();
  await sent.promise;
  assert.equal(form.busy, true);
  form.deleteBranch = false;
  reply.resolve();
  assert.equal((await pending).branchRetained, true);
  assert.equal(writes.length, 1);
  assert.equal(writes[0].body.delete_branch, true);
});

test('offline machines, missing permission and superseded clean previews report distinct recoveries', async () => {
  const {form,root,owner,tree,queries,writes} = fixture();
  root.connected = false;
  await form.open(root,tree,'clean'); assert.equal(form.error,'offline');
  root.connected = true; tree.online = false;
  await form.open(root,tree,'clean'); assert.equal(form.error,'offline');
  tree.online = true; tree.startAllowed = false;
  await form.open(root,tree,'clean'); assert.equal(form.error,'permission_denied');
  assert.equal(queries.length,0);
  tree.startAllowed = true;
  await form.open(root,tree,'clean'); assert.equal(form.canSubmit,true);
  owner.connected = false;
  await form.submit(); assert.equal(form.error,'offline'); assert.equal(form.canSubmit,false);
  owner.connected = true;
  const entered = Promise.withResolvers(), reply = Promise.withResolvers();
  owner.repo().transport.request = async () => { entered.resolve(); return reply.promise; };
  const preparing = form.open(root,tree,'clean');
  await entered.promise; owner.epoch++; reply.resolve('{}'); await preparing;
  assert.equal(form.error,'tree_preview_changed'); assert.equal(form.canSubmit,false);
  owner.repo().transport.request = async () => { throw new HttpFailure(403,'forbidden'); };
  await form.open(root,tree,'clean'); assert.equal(form.error,'permission_denied');
  owner.repo().transport.request = async () => { owner.connected = false; throw new HttpFailure(0,'transport_failed_closed'); };
  await form.open(root,tree,'clean'); assert.equal(form.error,'offline');
  assert.equal(writes.length,0);
});

test('an invalidated pre-dispatch snapshot removes only its own bytes and a new preview can proceed', async () => {
  for (const replace of [false,true]) {
    const {form,root,owner,tree,writes,views} = fixture();
    const prepared = Promise.withResolvers(), resume = Promise.withResolvers();
    const capture = owner.captureView.bind(owner);
    owner.captureView = key => {
      const lease = capture(key);
      return {...lease, write: async value => {
        await lease.write(value);
        if (value) { prepared.resolve(value); await resume.promise; }
      }};
    };
    await form.open(root,tree,'clean');
    const submitting = form.submit();
    const snapshot = await prepared.promise;
    const [key] = views.keys();
    const replacement = JSON.stringify({...JSON.parse(snapshot),id:'00000000-0000-4000-8000-000000000002'});
    if (replace) { form.close(); views.set(key,replacement); }
    else owner.epoch++;
    resume.resolve(); await submitting;
    assert.equal(writes.length,0);
    assert.equal(views.get(key),replace ? replacement : undefined);
    await form.open(root,tree,'clean');
    assert.equal(form.unknown,replace);
    assert.equal(form.canSubmit,!replace);
  }
});

test('journal admission failure clears a proven unsent operation and preserves an uncertain receipt', async () => {
  for (const uncertain of [false,true]) {
    const {form,root,owner,tree,views,writes} = fixture();
    owner.repo().call = async (_path,_method,_body,_id,admit) => {
      owner.connected = false;
      assert.equal(admit(),false);
      throw new HttpFailure(0,uncertain ? 'receipt_storage_failed' : 'request_updated',uncertain);
    };
    await form.open(root,tree,'clean');
    await form.submit();
    assert.equal(writes.length,0);
    assert.equal(views.size,uncertain ? 1 : 0);
    owner.connected = true;
    await form.open(root,tree,'clean');
    assert.equal(form.unknown,uncertain);
  }
});

test('stale preview cannot bind to a different sheet instance or act on the main checkout', async () => {
  const { form, root, owner, tree, queries } = fixture();
  let complete;
  owner.repo().transport.request = () => new Promise(resolve => { complete = resolve; });
  const opening = form.open(root, tree, 'clean'); while (!complete) await Promise.resolve();
  form.close(); complete(JSON.stringify({ workspace_id:'child', kind:'herdr', path:'/qa', dirty:0, preview_token:'stale' }));
  await opening; assert.equal(form.canSubmit,false);
  tree.main = true; await form.open(root, tree, 'clean'); assert.equal(form.error,'permission_denied');
  assert.equal(queries.length,0);
});

test('a lost create freezes UUID and exact bytes across reopening; one explicit retry does not admit a second operation', async()=>{
  const f=fixture();f.behavior.unknown=true;
  await f.form.open(f.root,f.tree,'create');f.form.setBranch('qa/Once');await f.form.submit();
  const first=f.writes[0], frozen=JSON.stringify(first.body);
  assert.equal(f.form.canSubmit,false);assert.equal(f.form.canRetrySame,true);
  f.form.close();assert.ok([...f.views.values()].some(value=>value.includes(first.id)));
  const other=new WorktreeActions();await other.open(f.root,{...f.tree,cwd:'/other',permits:()=>true},'create');
  assert.equal(other.canRetrySame,false);assert.equal(other.legacy,true);other.close();
  await f.form.open(f.root,f.tree,'create');assert.equal(f.form.branch,'qa/Once');assert.equal(f.form.canSubmit,false);
  f.form.setBranch('qa/changed');f.form.setPath('/new');assert.equal(f.form.path,first.body.path);
  await f.form.reconcile();assert.equal(f.replayed.length,0);
  const gate=Promise.withResolvers();f.behavior.waitReplay=gate.promise;
  const attempt=f.form.reconcile(true);await f.form.reconcile(true);await f.form.submit();
  while (!f.replayed.length) await Promise.resolve();gate.resolve();
  const result=await attempt;assert.equal(result.id,'new-id');
  assert.deepEqual(f.replayed,[{path:first.path,method:first.method,json:frozen,id:first.id}]);
  assert.deepEqual(f.finished,[[first.id,'done']]);assert.equal(f.writes.length,1);assert.equal(f.form.canSubmit,false);
});

test('cached receipts restore completion, pending and definitive failure distinctly',async()=>{
  for(const state of ['done','pending','failed','unknown']){
    const f=fixture();f.behavior.unknown=true;await f.form.open(f.root,f.tree,'create');
    f.form.automatic=true;await f.form.submit();f.behavior.operation=state;
    f.behavior.receiptBody=state==='done'?{workspace_id:'once',branch:'automatic',path:'/qa/automatic'}:{code:'refused'};
    const result=await f.form.reconcile();
    assert.equal(f.replayed.length,0);assert.equal(f.writes.length,1);
    if(state==='done'){assert.equal(result.id,'once');assert.equal(f.blocked[0].state,'done');}
    else if(state==='failed'){assert.equal(f.form.error,'refused');assert.equal(f.form.unknown,false);assert.equal(f.blocked[0].state,'failed');}
    else {assert.equal(f.form.error,state==='pending'?'pending':'result_unknown');assert.equal(f.form.canSubmit,false);}
  }
});

test('an old build, absent feature, invalid target or expired receipt cannot authorize another POST',async()=>{
  for(const kind of ['old','feature','mismatched','expired']){
    const f=fixture();
    if(kind==='old')f.behavior.version='dev';
    if(kind==='feature')f.owner.features=[];
    f.behavior.unknown=true;await f.form.open(f.root,f.tree,'create');f.form.automatic=true;await f.form.submit();
    if(kind==='expired')f.form.requestTime=Date.now()-31*86400000;
    if(kind==='mismatched'){f.behavior.operation='unknown';f.behavior.receiptPath='/api/v1/machines/Another/worktree/create';}
    await f.form.reconcile(true);assert.equal(f.replayed.length,0);assert.equal(f.form.canSubmit,false);
  }
  const f=fixture();f.behavior.version='dev';f.behavior.unknown=true;
  await f.form.open(f.root,f.tree,'create');f.form.automatic=true;await f.form.submit();
  assert.equal(f.form.canRetrySame,false);f.behavior.operation='pending';await f.form.reconcile();
  assert.equal(f.form.canRetrySame,true);
});

test('manual verification of a legacy create releases only its missing record and demands a separate fresh submit',async()=>{
  const f=fixture();
  const old={id:'00000000-0000-4000-8000-000000000001',path:'/api/v1/machines/Studio%2F1/worktree/create',
    method:'POST',state:'unknown',createdAt:new Date(Date.now()-300000).toISOString()};
  const unrelated={...old,id:'00000000-0000-4000-8000-000000000002',path:'/api/v1/machines/Other/worktree/create'};
  f.blocked.push(old,unrelated);
  await f.form.open(f.root,f.tree,'create');assert.equal(f.form.legacy,true);assert.equal(f.form.canRetrySame,false);
  await f.form.reconcile(true);assert.equal(f.form.releaseReady,false);
  f.form.setBranch('qa/lost');await f.form.reconcile(true);assert.equal(f.form.releaseReady,true);assert.equal(f.form.canSubmit,false);
  f.behavior.workspaces.push({...f.behavior.workspaces[0],source_id:'different',branch:'qa/lost'});
  await f.form.reconcile(true);assert.equal(f.form.releaseReady,false);assert.equal(old.state,'unknown');
  f.behavior.workspaces.pop();await f.form.reconcile();await f.form.reconcile(true);
  assert.equal(old.state,'failed');assert.equal(unrelated.state,'unknown');assert.equal(f.form.canSubmit,true);assert.equal(f.replayed.length,0);
  assert.equal(f.writes.length,0);await f.form.submit();assert.equal(f.writes.length,1);assert.notEqual(f.writes[0].id,old.id);
});

test('uncertain rename checks the same live host source, cwd and title without resending the name',async()=>{
  const f=fixture();f.behavior.unknown=true;await f.form.open(f.root,f.tree,'rename');f.form.name='New title';await f.form.submit();
  f.form.close();await f.form.open(f.root,f.tree,'rename');assert.equal(f.form.name,'New title');
  const row=f.behavior.workspaces[0];row.title='New title';
  for(const [key,wrong] of [['machine_id','other'],['source','orca'],['source_id','other'],['cwd','/other'],['online',false],['title','Old']]){
    const original=row[key];row[key]=wrong;await f.form.reconcile(true);row[key]=original;assert.equal(f.form.unknown,true);
  }
  const result=await f.form.reconcile();assert.equal(result.action,'rename');assert.equal(f.writes.length,1);assert.equal(f.replayed.length,0);
});

async function lostRename({legacy=false,age=65000}={}) {
  const f=fixture();
  f.behavior.unknown=true;
  await f.form.open(f.root,f.tree,'rename'); f.form.name='Requested'; await f.form.submit();
  const [record]=f.blocked, [key]=f.views.keys(), data=JSON.parse(f.views.get(key));
  data.at=Date.now()-age; record.createdAt=new Date(data.at).toISOString();
  record.requiresReceipt=false; record.confirmedPending=false;
  if (legacy) delete data.originalName;
  const snapshot=JSON.stringify(data); f.views.set(key,snapshot);
  f.form.close(); await f.form.open(f.root,f.tree,'rename');
  return {...f,record,key,snapshot,row:f.behavior.workspaces[0]};
}

test('keeping a verified name settles one rename locally and a separate save gets a new UUID',async()=>{
  for (const legacy of [false,true]) {
    const f=await lostRename({legacy});
    assert.equal(f.form.renameOriginal,legacy?'':'Child');
    const unrelated={...f.record,id:'00000000-0000-4000-8000-000000000003',path:'/api/v1/machines/Other/worktree/rename'};
    f.blocked.push(unrelated);
    await f.form.reconcile(true); assert.equal(f.record.state,'unknown'); assert.equal(f.form.renameCurrent,'');
    await f.form.reconcile(); assert.equal(f.form.renameCurrent,'Child'); assert.equal(f.form.name,'Requested');
    assert.equal(f.form.canSubmit,false); assert.equal(f.reads.filter(p=>p.endsWith('/workspaces')).length,2);
    await f.form.reconcile(true);
    assert.equal(f.record.state,'checked'); assert.equal(unrelated.state,'unknown');
    assert.equal(f.views.has(f.key),false); assert.equal(f.form.unknown,false);
    assert.equal(f.form.name,'Child'); assert.equal(f.form.canSubmit,false);
    await f.form.submit(); assert.equal(f.writes.length,1); assert.equal(f.replayed.length,0);
    f.behavior.unknown=false; f.form.name='New decision';
    await f.form.submit(); assert.equal(f.writes.length,2);
    assert.equal(f.writes[1].body.name,'New decision'); assert.notEqual(f.writes[1].id,f.record.id);
  }
});

test('a third-party name is displayed and preserved; changes during review or before a later save require a new decision',async()=>{
  const f=await lostRename();
  await f.form.reconcile(); f.row.title=' Changed by someone else ';
  await f.form.reconcile(true); assert.equal(f.form.renameCurrent,''); assert.equal(f.record.state,'unknown');
  await f.form.reconcile(); assert.equal(f.form.renameCurrent,f.row.title);
  assert.equal(f.form.renameOriginal,'Child'); assert.equal(f.form.name,'Requested');
  await f.form.reconcile(true); assert.equal(f.form.name,f.row.title); assert.equal(f.form.unknown,false); assert.equal(f.form.canSubmit,false);
  f.form.name='A new request'; f.row.title='Changed again'; f.behavior.unknown=false;
  await f.form.submit(); assert.equal(f.writes.length,1); assert.equal(f.form.name,'Changed again'); assert.equal(f.form.canSubmit,false);
  f.form.name='My final choice'; await f.form.submit(); assert.equal(f.writes.length,2);
});

test('a rename release requires the exact aged, non-pending local UUID and one complete live owner-qualified workspace',async()=>{
  const conditions=[
    f=>{f.record.createdAt=new Date().toISOString();}, f=>{f.record.createdAt='invalid';},
    f=>{f.record.requiresReceipt=true;}, f=>{f.record.confirmedPending=true;}, f=>{f.record.state='pending';},
    f=>{f.record.id='00000000-0000-4000-8000-000000000004';}, f=>{f.record.path+='/different';},
    f=>{f.record.method='GET';}, f=>{f.row.machine_id='other';}, f=>{f.row.source_id='wrong';},
    f=>{f.blocked.push({...f.record,id:'00000000-0000-4000-8000-000000000006'});},
    f=>{f.row.source='orca';}, f=>{f.row.cwd='/other';}, f=>{f.row.manager_id='foreign';},
    f=>{f.row.online=false;}, f=>{f.row.available=false;},
    f=>{f.behavior.workspaces.push({...f.row});}, f=>{f.behavior.errors=[{machine_id:f.row.machine_id}];},
    f=>{const repo=f.owner.repo(), original=repo.call;repo.call=(path,...rest)=>path==='/api/v1/workspaces'
      ? Promise.resolve({workspaces:[f.row],has_more:true}):original.call(repo,path,...rest);},
    f=>{f.views.set(f.key,JSON.stringify({...JSON.parse(f.snapshot),id:'replacement'}));}
  ];
  for (const invalidate of conditions) {
    const f=await lostRename(); invalidate(f); const state=f.record.state;
    await f.form.reconcile(); await f.form.reconcile(true);
    assert.equal(f.form.renameCurrent,''); assert.equal(f.form.unknown,true);
    assert.equal(f.record.state,state); assert.equal(f.writes.length,1); assert.equal(f.replayed.length,0);
  }
});

test('replacing the reviewed journal record between readbacks requires checking it again',async()=>{
  const f=await lostRename(); await f.form.reconcile();
  f.blocked[0]={...f.record};
  await f.form.reconcile(true);
  assert.equal(f.form.renameCurrent,''); assert.equal(f.blocked[0].state,'unknown');
  assert.equal(f.form.unknown,true); assert.equal(f.writes.length,1);
});

test('journal and snapshot failures can resume; cleanup never erases a replacement request',async()=>{
  for (const fault of ['journal','snapshot','replacement','after-clear']) {
    const f=await lostRename(), journal=f.owner.repo().mutations, view=f.form.view;
    const acknowledge=journal.acknowledge,clear=view.clearIfUnchanged;
    const replacement=JSON.stringify({...JSON.parse(f.snapshot),id:'00000000-0000-4000-8000-000000000005'});
    await f.form.reconcile();
    if (fault==='journal') journal.acknowledge=async()=>{throw Error('storage_unavailable');};
    else if (fault==='after-clear') view.clearIfUnchanged=async value=>{
      await clear(value);
      const read=view.read; view.read=async()=>{view.read=read;throw Error('storage_unavailable');};
    };
    else view.clearIfUnchanged=async value=>{
      if(fault==='snapshot') throw Error('storage_unavailable');
      f.views.set(f.key,replacement); await clear(value);
    };
    await f.form.reconcile(true);
    assert.equal(f.form.unknown,true); assert.equal(f.writes.length,1);
    assert.equal(f.record.state,fault==='journal'?'unknown':'checked');
    assert.equal(f.views.get(f.key),fault==='replacement'?replacement:fault==='after-clear'?undefined:f.snapshot);
    journal.acknowledge=acknowledge;
    view.clearIfUnchanged=clear;
    if (fault!=='after-clear') { f.form.close(); await f.form.open(f.root,f.tree,'rename'); }
    await f.form.reconcile(); await f.form.reconcile(true);
    assert.equal(f.form.unknown,fault==='replacement'); assert.equal(f.writes.length,1);
  }
});

test('retiring an owner during the second read cannot settle its rename or mutate a new form',async()=>{
  const f=await lostRename(); await f.form.reconcile();
  const gate=Promise.withResolvers(), entered=Promise.withResolvers(), repo=f.owner.repo();
  repo.call=async()=>{entered.resolve();return gate.promise;};
  const pending=f.form.reconcile(true);
  await entered.promise; f.owner.epoch++; f.form.close();
  gate.resolve({workspaces:[f.row]}); await pending;
  assert.equal(f.record.state,'unknown'); assert.equal(f.views.get(f.key),f.snapshot); assert.equal(f.writes.length,1);
});

test('an in-flight retry cannot update or unlock a replacement connection',async()=>{
  const f=fixture();f.behavior.unknown=true;await f.form.open(f.root,f.tree,'create');f.form.automatic=true;await f.form.submit();
  const original=f.writes[0].id, gate=Promise.withResolvers();f.behavior.waitReplay=gate.promise;
  const pending=f.form.reconcile(true);while(!f.replayed.length)await Promise.resolve();
  f.owner.epoch++;f.form.close();gate.resolve();assert.equal(await pending,undefined);
  assert.deepEqual(f.finished,[]);assert.equal(f.blocked[0].id,original);assert.equal(f.form.canSubmit,false);
});

test('a legacy check cannot unlock a young or expired ID, a partial catalogue, a generic 404, or an unverified Hub',async()=>{
  for(const which of ['young','expired','partial','no-source','no-version','wrong-hub','wrong-404']){
    const f=fixture(), age=which==='young'?30000:which==='expired'?32*86400000:300000;
    const record={id:'00000000-0000-4000-8000-000000000003',path:'/api/v1/machines/Studio%2F1/worktree/create',
      method:'POST',state:'unknown',createdAt:new Date(Date.now()-age).toISOString()};
    f.blocked.push(record);
    if(which==='partial')f.behavior.errors=[{machine_id:'Studio/1',error:'unavailable'}];
    if(which==='no-source')f.behavior.workspaces=[];
    if(which==='no-version')f.behavior.version='0.0.1';
    if(which==='wrong-hub')f.owner.profile.hubId='replacement';
    if(which==='wrong-404'){
      const repo=f.owner.repo(),original=repo.call.bind(repo);
      repo.call=(path,...rest)=>path.includes('/ops/')?Promise.reject(new HttpFailure(404,'http_error')):original(path,...rest);
    }
    await f.form.open(f.root,f.tree,'create');f.form.setBranch('qa/lost');await f.form.reconcile();await f.form.reconcile(true);
    assert.equal(f.form.releaseReady,false,which);assert.equal(f.form.canSubmit,false,which);
    assert.equal(record.state,'unknown',which);assert.equal(f.replayed.length,0,which);
  }
});

test('freeze covers manual vs automatic naming and in-place retargets; the body from a different checkout is never used',async()=>{
  const f=fixture();f.behavior.unknown=true;await f.form.open(f.root,f.tree,'create');f.form.setBranch('qa/once');await f.form.submit();
  f.form.automatic=true;assert.equal(f.form.canRetrySame,false);await f.form.reconcile(true);assert.equal(f.replayed.length,0);
  f.form.automatic=false;f.tree.cwd='/other';assert.equal(f.form.canRetrySame,false);await f.form.reconcile(true);
  assert.equal(f.replayed.length,0);assert.equal(f.blocked[0].state,'unknown');
});

test('public health cannot prove support alone; the verified WS build supports safely checking the original create',async()=>{
  for(const hello of ['','0.0.8','0.9.1']){
    const f=fixture();f.behavior.publicHealth=true;f.behavior.helloVersion=hello;f.behavior.unknown=true;
    await f.form.open(f.root,f.tree,'create');f.form.setBranch('qa/hello');await f.form.submit();
    assert.equal(f.form.canRetrySame,hello==='0.9.1');assert.equal(f.replayed.length,0);
    if(hello==='0.9.1')assert.ok(!f.reads.includes('/api/v1/health'),'verified connection metadata needs no public version field');
  }
  const f=fixture();f.behavior.publicHealth=true;f.behavior.helloVersion='0.9.1';f.owner.features=[];f.behavior.unknown=true;
  await f.form.open(f.root,f.tree,'create');f.form.setBranch('qa/hello');await f.form.submit();
  assert.equal(f.form.canRetrySame,false);
});

test('checking a legacy create retries a dropped status and host read once without a replay',async()=>{
  const f=fixture();f.behavior.publicHealth=true;f.behavior.helloVersion='0.9.1';
  f.blocked.push({id:'00000000-0000-4000-8000-000000000003',path:'/api/v1/machines/Studio%2F1/worktree/create',
    method:'POST',state:'unknown',createdAt:new Date(Date.now()-300000).toISOString()});
  const repo=f.owner.repo(),base=repo.call.bind(repo),calls=new Map();
  repo.call=(path,...rest)=>{
    const count=(calls.get(path)||0)+1;calls.set(path,count);
    if(count===1&&(path.includes('/ops/')||path.endsWith('/workspaces')))throw new HttpFailure(0,'transport_failed_42');
    return base(path,...rest);
  };
  await f.form.open(f.root,f.tree,'create');f.form.setBranch('qa/delayed');await f.form.reconcile();
  assert.equal(calls.get('/api/v1/ops/'+f.blocked[0].id),2);assert.equal(calls.get('/api/v1/workspaces'),2);
  assert.equal(f.form.releaseReady,true);assert.equal(f.replayed.length,0);assert.equal(f.writes.length,0);
});

test('a connection change during read backoff cancels the second attempt before it reaches transport',async()=>{
  const f=fixture();f.behavior.unknown=true;await f.form.open(f.root,f.tree,'create');f.form.automatic=true;await f.form.submit();
  const repo=f.owner.repo(),base=repo.call.bind(repo),first=Promise.withResolvers();let count=0;
  repo.call=(path,...rest)=>{
    if(path.includes('/ops/')){count++;first.resolve();throw new HttpFailure(0,'transport_failed_9');}
    return base(path,...rest);
  };
  const check=f.form.reconcile();await first.promise;f.owner.epoch++;await check;
  assert.equal(count,1);assert.equal(f.replayed.length,0);assert.equal(f.form.canCheck,false);
});


test('read-only cleanup reconciliation reports the matching workspace state and keeps the original request unresolved', async () => {
  const f = fixture();
  f.behavior.unknown = true;
  await f.form.open(f.root,f.tree,'clean');
  await f.form.submit();
  const request = f.writes[0];
  for (const [workspace,state,removed,expected] of [
    ['child','present',false,'tree_cleanup_present'], ['child','removing',false,'tree_cleanup_removing'],
    ['child','unknown',false,'result_unknown'], ['other','present',false,'result_unknown'],
    ['child','present',undefined,'result_unknown'], ['child','present',true,'result_unknown']
  ]) {
    Object.assign(f.cleanupResult,{workspace_id:workspace,state,removed});
    assert.equal(await f.form.reconcile(true),undefined);
    assert.equal(f.form.error,expected);
    assert.equal(f.form.unknown,true);
    assert.equal(f.form.canSubmit,false);
    assert.equal(f.form.canRetrySame,false);
    assert.deepEqual(f.queries.at(-1).body,{workspace_id:'child',kind:'herdr',preview_token:'this-preview',reconcile:true});
  }
  assert.deepEqual(f.writes,[request]);
  assert.deepEqual(f.finished,[]);
});

async function unresolvedHerdrCleanup() {
  const f=fixture();
  f.behavior.dirty=1;
  f.behavior.unknown=true;
  await f.form.open(f.root,f.tree,'clean');
  f.form.force=true;
  f.form.deleteBranch=true;
  await f.form.submit();
  assert.equal(f.form.unknown,true);
  Object.assign(f.cleanupResult,{workspace_id:'child',state:'present',removed:false,can_revoke_preview:true});
  const request=f.owner.repo().transport.request;
  f.owner.repo().transport.request=async (...args)=>{
    const response=JSON.parse(await request(...args));
    if(JSON.parse(args[2]).preview)response.preview_token=f.behavior.newToken??'new-preview';
    return JSON.stringify(response);
  };
  return f;
}

test('a surrendered Herdr token opens a fresh dirty preview with both destructive options reset',async()=>{
  for(const reopen of [false,true]) for(const missingJournal of [false,true]) {
    const f=await unresolvedHerdrCleanup();
    if(missingJournal)f.blocked.length=0; // The draft persisted before the mutation journal began.
    if(reopen) { f.form.close();f.form=new WorktreeActions();await f.form.open(f.root,f.tree,'clean'); }
    await f.form.reconcile();
    assert.equal(f.form.cleanupRepreviewAvailable,true);
    assert.equal(f.queries.at(-1).body.revoke_preview,undefined);
    assert.equal(f.form.canSubmit,false);
    Object.assign(f.cleanupResult,{state:'preview_revoked',revoked_preview_token:'this-preview'});
    assert.equal(await f.form.reconcile(true),undefined);
    assert.deepEqual(f.queries.at(-2).body,{kind:'herdr',workspace_id:'child',preview_token:'this-preview',reconcile:true,revoke_preview:true});
    assert.deepEqual(f.queries.at(-1).body,{kind:'herdr',workspace_id:'child',preview:true});
    assert.equal(f.views.size,0);assert.equal(f.form.unknown,false);
    assert.equal(f.form.ready,true);assert.equal(f.form.dirty,1);assert.equal(f.form.force,false);assert.equal(f.form.deleteBranch,false);
    assert.equal(f.form.canSubmit,false);assert.equal(f.writes.length,1);
    assert.deepEqual(f.finished,missingJournal?[]:[[f.writes[0].id,'failed']]);
    f.form.force=true;assert.equal(f.form.canSubmit,true);await f.form.submit();
    assert.equal(f.writes[1].body.preview_token,'new-preview');assert.equal(f.writes[1].body.delete_branch,false);
    assert.notEqual(f.writes[1].id,f.writes[0].id);
  }
});

test('a dispatched, missing, mismatched or legacy observation never releases the original cleanup',async()=>{
  for(const reply of [
    {state:'present'}, {state:'removing'}, {state:'unknown'}, {state:'preview_revoked'},
    {state:'preview_revoked',revoked_preview_token:'other'}, {workspace_id:'other',state:'preview_revoked',revoked_preview_token:'this-preview'},
    {state:'preview_revoked',revoked_preview_token:'this-preview',removed:undefined}
  ]) {
    const f=await unresolvedHerdrCleanup();await f.form.reconcile();
    Object.assign(f.cleanupResult,{can_revoke_preview:undefined},reply);
    await f.form.reconcile(true);
    assert.equal(f.queries.at(-1).body.revoke_preview,true);
    assert.equal(f.form.cleanupRepreviewAvailable,false);assert.equal(f.form.unknown,true);assert.equal(f.form.canSubmit,false);
    assert.equal(f.writes.length,1);assert.equal(f.finished.length,0);assert.equal(f.blocked[0].state,'unknown');
  }
});

test('a lost revocation reply is recovered on a read-only check after reopening the original request',async()=>{
  const f=await unresolvedHerdrCleanup();await f.form.reconcile();
  const transport=f.owner.repo().transport,read=transport.request;
  transport.request=async (...args)=>{
    if(JSON.parse(args[2]).revoke_preview) {
      Object.assign(f.cleanupResult,{state:'preview_revoked',revoked_preview_token:'this-preview'});
      throw new HttpFailure(504,'lost_reply');
    }
    return read(...args);
  };
  await f.form.reconcile(true);
  assert.equal(f.form.unknown,true);assert.equal(f.finished.length,0);assert.equal(f.views.size,1);
  f.form.close();await f.form.open(f.root,f.tree,'clean');
  assert.equal(f.form.cleanupRepreviewAvailable,false);
  await f.form.reconcile();
  assert.equal(f.queries.at(-2).body.revoke_preview,undefined);assert.equal(f.queries.at(-1).body.preview,true);
  assert.equal(f.form.canCheck,false);assert.equal(f.form.force,false);assert.equal(f.writes.length,1);
});

test('repreview retains a replaced owner, journal or checkout snapshot and rejects a reused token',async()=>{
  for(const fault of ['snapshot-before','journal-before','snapshot-after','reconnect','new-token','journal-write']) {
    const f=await unresolvedHerdrCleanup();await f.form.reconcile();
    const key=[...f.views.keys()][0], replacement='a newer view snapshot';
    Object.assign(f.cleanupResult,{state:'preview_revoked',revoked_preview_token:'this-preview'});
    if(fault==='snapshot-before')f.views.set(key,replacement);
    if(fault==='journal-before')f.blocked.push({...f.blocked[0],id:'other-request'});
    if(fault==='new-token')f.behavior.newToken='this-preview';
    if(fault==='journal-write')f.owner.repo().mutations.finish=async()=>{throw new HttpFailure(0,'receipt_storage_failed',true)};
    if(fault==='snapshot-after'||fault==='reconnect') {
      const transport=f.owner.repo().transport,read=transport.request;
      transport.request=async (...args)=>{
        const result=await read(...args);
        if(JSON.parse(args[2]).revoke_preview) {
          if(fault==='snapshot-after')f.views.set(key,replacement);
          else f.owner.epoch++;
        }
        return result;
      };
    }
    await f.form.reconcile(true);
    assert.equal(f.writes.length,1);assert.equal(f.form.canSubmit,false);
    if(fault==='new-token') {
      assert.equal(f.form.ready,false);assert.equal(f.form.error,'tree_preview_changed');
      assert.deepEqual(f.finished,[[f.writes[0].id,'failed']]);
    } else {
      assert.equal(f.finished.length,0);assert.equal(f.form.unknown,true);assert.equal(f.views.size,1);
      if(fault.startsWith('snapshot-'))assert.equal(f.views.get(key),replacement);
      if(fault.endsWith('-before'))assert.equal(f.queries.at(-1).body.revoke_preview,undefined);
    }
  }
});

test('a verified Hub release or clean deploy names the first release with worktree receipts in its ancestry', async () => {
  const versions = [
    ['v0.1.0', true], ['v0.1.0-13-g123abcd-202611210631', true],
    ['v0.9.1-beta.2-1045-g6f14df7d1-202611221823', true], ['v0.9.1-rc1', true],
    ['v0.1.0-beta.3-42-g123abcd-202611221823', false], ['v0.0.9-12-g123abcd-202611221823', false],
    ['v0.9.1-beta.2-1045-g6f14df7d1-dirty-202611221823', false], ['dev-202611221823', false],
    ['0.9.1-custom', false], ['v0.9.1-42-gunknown-202611221823', false]
  ];
  for (const [version, supported] of versions) {
    const f = fixture(); f.behavior.helloVersion = version; f.behavior.unknown = true;
    await f.form.open(f.root, f.tree, 'create'); f.form.setBranch('qa/release'); await f.form.submit();
    assert.equal(f.form.canRetrySame, supported, version); assert.equal(f.replayed.length, 0);
  }
});

async function olderManualCreate() {
  const f = fixture();
  f.behavior.helloVersion = 'custom'; f.behavior.unknown = true;
  await f.form.open(f.root, f.tree, 'create'); f.form.setBranch('qa/original'); await f.form.submit();
  const [key] = f.views.keys(), saved = JSON.parse(f.views.get(key));
  assert.equal(saved.receipts, false);
  saved.at = Date.now() - 3 * 60000;
  f.views.set(key, JSON.stringify(saved));
  Object.assign(f.blocked[0], {createdAt: new Date(saved.at).toISOString(), confirmedPending: false, requiresReceipt: false});
  f.form.close();
  f.behavior.helloVersion = 'v0.9.1-beta.2-1045-g6f14df7d1-202611221823';
  await f.form.open(f.root, f.tree, 'create');
  return {...f, key, saved};
}

test('a saved manual create on a previously unrecognized Hub retries only with the original ID and bytes after two read-only checks', async () => {
  const f = await olderManualCreate();
  assert.equal(f.form.canRetrySame, false);
  await f.form.reconcile();
  assert.equal(f.form.canRetrySame, true); assert.equal(f.replayed.length, 0);
  const change = await f.form.reconcile(true);
  assert.equal(change.id, 'new-id'); assert.equal(f.writes.length, 1);
  assert.deepEqual(f.replayed, [{path:f.saved.path,method:'POST',json:f.saved.payload,id:f.saved.id}]);
  assert.equal(f.reads.filter(path => path === '/api/v1/workspaces').length, 2);
});

test('a fresh, pending, retained or unverified manual checkout cannot be replayed, including changes between checks', async () => {
  for (const reason of ['young','pending','receipt','directory','branch','partial','other-owner','manager','snapshot','unsupported','different-record']) {
    const f = await olderManualCreate();
    await f.form.reconcile(); assert.equal(f.form.canRetrySame, true);
    const record = f.blocked[0], source = f.behavior.workspaces[0];
    if (reason === 'young') record.createdAt = new Date().toISOString();
    if (reason === 'pending') record.confirmedPending = true;
    if (reason === 'receipt') record.requiresReceipt = true;
    if (reason === 'directory') f.behavior.workspaces.push({...source,source_id:'new',cwd:'/qa/qa-original'});
    if (reason === 'branch') f.behavior.workspaces.push({...source,source_id:'new',branch:'qa/original'});
    if (reason === 'partial') f.behavior.errors = [{machine_id:'Studio/1', error:'unavailable'}];
    if (reason === 'other-owner') source.source_id = 'other';
    if (reason === 'manager') source.manager_id = 'other';
    if (reason === 'snapshot') f.views.set(f.key, 'replaced');
    if (reason === 'unsupported') f.behavior.helloVersion = 'dev';
    if (reason === 'different-record') Object.assign(record,{id:'00000000-0000-4000-8000-000000000003'});
    await f.form.reconcile(true);
    assert.equal(f.replayed.length, 0, reason); assert.equal(f.form.canRetrySame, false, reason);
    assert.equal(f.finished.length, 0, reason);
  }
});

async function removedRenameFixture() {
  const f=await lostRename();
  const current=Object.assign(new ProjectWorktree(),f.tree,{sourceId:'different-checkout',cwd:'/qa/new',title:'New checkout'});
  const live={...f.row,source_id:current.sourceId,cwd:current.cwd,title:current.title,manager_id:current.manager};
  f.behavior.workspaces=[live];f.form.close();
  return {...f,current,live,reopen:()=>f.form.open(f.root,current,'rename')};
}

test('an explicitly reviewed removed worktree releases only its old rename and keeps the complete original request',async()=>{
  const f=await removedRenameFixture(),other={...f.record,id:'00000000-0000-4000-8000-000000000080',path:'/api/v1/machines/Elsewhere/worktree/rename'};
  f.blocked.push(other);await f.reopen();
  assert.equal(f.form.removedRenameDirectory,'/qa/child');assert.equal(f.form.canSubmit,false);
  assert.equal(f.record.state,'unknown');assert.equal(f.views.get(f.key),f.snapshot);assert.equal(f.writes.length,1);
  const readCount=f.reads.length;await f.form.reconcile(true);
  assert.equal(f.reads.slice(readCount).filter(p=>p==='/api/v1/workspaces').length,1);
  assert.equal(f.record.state,'checked');assert.equal(other.state,'unknown');assert.equal(f.views.get(f.key),f.snapshot);
  assert.deepEqual(f.finished,[[f.record.id,'checked']]);assert.equal(f.form.name,'New checkout');
  assert.equal(f.form.unknown,false);assert.equal(f.form.canSubmit,false);await f.form.submit();assert.equal(f.writes.length,1);
  f.behavior.unknown=false;f.form.name='A separate name';await f.form.submit();assert.equal(f.writes.length,2);
  assert.equal(f.writes[1].body.workspace_id,f.current.sourceId);assert.equal(f.writes[1].body.name,'A separate name');
  assert.notEqual(f.writes[1].id,f.record.id);assert.equal(f.replayed.length,0);assert.equal(f.views.get(f.key),f.snapshot);
});

test('a removed-rename review requires one scoped original, a complete catalogue, and a distinct live checkout',async()=>{
 const conditions=[
  f=>f.behavior.workspaces.push({...f.row,cwd:'/qa/moved'}),
  f=>f.behavior.workspaces.push({...f.row,source_id:'path-reused'}),
  f=>f.behavior.workspaces.push({...f.live,source_id:'second-at-current-path'}),
  f=>f.behavior.workspaces.push({...f.live,cwd:'/qa/duplicate-current'}),
  f=>{f.live.manager_id='different-manager'},f=>{f.live.online=false},f=>{f.live.available=false},f=>{f.live.title=''},
  f=>{f.behavior.workspaces=[{}]},f=>{f.behavior.catalogue={partial:true}},f=>{f.behavior.catalogue={truncated:true}},
  f=>{f.behavior.catalogue={complete:false}},f=>{f.behavior.catalogue={total:2}},f=>{f.behavior.catalogue={next_cursor:{}}},
  f=>{f.behavior.errors=[{machine_id:'other',error:'unspecified partial scan'}]},
  f=>{const old=f.owner.repo().call;f.owner.repo().call=async function(path,...rest){return path==='/api/v1/workspaces'?
    {workspaces:[f.live],has_more:true}:old.call(this,path,...rest)}},
  f=>{f.record.confirmedPending=true},f=>{f.record.requiresReceipt=true},f=>{f.record.state='pending'},
  f=>{f.record.createdAt=new Date().toISOString()},f=>{f.views.set(f.key,JSON.stringify({...JSON.parse(f.snapshot),at:Date.now()}))},
  f=>{f.record.payload='{"workspace_id":"other"}'},f=>{f.views.set(f.key,JSON.stringify({...JSON.parse(f.snapshot),path:'/api/v1/machines/Other/worktree/rename'}))},
  f=>{f.views.set(f.key,JSON.stringify({...JSON.parse(f.snapshot),id:'00000000-0000-4000-8000-0000000000ff'}))},
  f=>{f.blocked.push({...f.record,id:'00000000-0000-4000-8000-000000000082'})},
  f=>{f.current.manager='another'},f=>{f.current.group='other'},f=>{f.current.source='orca'},f=>{f.current.machine='Other';f.record.path='/api/v1/machines/Other/worktree/rename'},
  f=>{f.views.set(f.key+'-unrelated',f.snapshot);f.views.delete(f.key)},
  f=>{f.views.set(f.key.replace('/qa/child','/qa/elsewhere'),f.snapshot)},
  f=>{f.current.sourceId=f.row.source_id},f=>{f.current.cwd=f.row.cwd},f=>{f.current.startAllowed=false}
 ];
 for(const change of conditions){
  const f=await removedRenameFixture();change(f);const state=f.record.state,prior=f.views.get(f.key);
  await f.reopen();await f.form.reconcile(false);await f.form.reconcile(true);
  assert.equal(f.form.removedRenameDirectory,'',String(change));assert.equal(f.form.canSubmit,false,String(change));
  assert.equal(f.record.state,state,String(change));assert.equal(f.views.get(f.key),prior);assert.equal(f.writes.length,1);assert.equal(f.replayed.length,0);
 }
});

test('confirmation rereads both workspaces, the owner and both saved forms and cannot release a replacement record',async()=>{
 for(const change of [f=>{f.behavior.workspaces.push(f.row)}, f=>{f.live.title='Changed again'},
    f=>{f.views.set(f.key,JSON.stringify({...JSON.parse(f.snapshot),originalName:'Replacement'}))},
    f=>{f.views.set(f.form.view.storageKey,'new target state')},
    f=>{f.blocked[0]={...f.record}},f=>{f.record.requiresReceipt=true}, f=>{f.owner.epoch++},
    f=>{f.current.cwd='/qa/replaced'},f=>{f.current.online=false},
    f=>{f.blocked.push({...f.record,id:'00000000-0000-4000-8000-000000000084'})}]){
  const f=await removedRenameFixture();await f.reopen();assert.ok(f.form.removedRenameDirectory);
  change(f);await f.form.reconcile(true);
  assert.equal(f.record.state,'unknown',String(change));assert.equal(f.blocked[0].state,'unknown');
  assert.equal(f.writes.length,1);assert.equal(f.form.canSubmit,false);assert.equal(f.replayed.length,0);
 }
});

function nativeMethods(path, names, dependencies) {
  const text = readFileSync(new URL('../entry/src/main/ets/' + path,import.meta.url),'utf8');
  const methods = names.map(name => {
    const found = new RegExp('^  private (?:async )?' + name + '\\(','m').exec(text);
    assert.ok(found,name);
    const line = text.indexOf('\n',found.index);
    return text.slice(found.index,text.slice(found.index,line).trimEnd().endsWith('}') ? line : text.indexOf('\n  }',line)+4);
  });
  return new Function(...Object.keys(dependencies),'return '+stripTypeScriptTypes('class NativePage {'+methods.join('\n')+'}'))(...Object.values(dependencies));
}

const NativeProjects = nativeMethods('components/ProjectsPage.ets',['complete','editorDismissed','menuTarget'],{});
const NativeRoute = nativeMethods('pages/Index.ets',['worktreeChanged','prepareAgent','selectedAgentTree','agentDraftKey',
  'agentMachines','machineKey','agentMachineOnline','chosenAgentMachine'],{ResourceRow,Wire,tr:key=>key});
const nextFrame = () => new Promise(resolve=>setImmediate(resolve));

async function projectCreateUI(origin = 'project_tree', section = 4) {
  const f=fixture(), {root,owner,tree,form}=f;
  Object.assign(owner.profile,{id:tree.group,manager:tree.manager});
  const machine=ResourceRow.from({id:tree.machine,name:'Test machine',online:true},'machine');
  machine.group=tree.group; machine.manager=tree.manager;
  root.section=section; root.directoryMachines=[machine];owner.machines=[machine];
  root.reportError=error=>{throw error;}; owner.loadView=async()=>'';
  tree.key=JSON.stringify([tree.group,tree.manager,tree.machine,tree.source,tree.sourceId]);
  const catalog=new ProjectCatalog(), routes=origin==='project_tree' ? [{name:'project_tree',key:tree.key}] : [];
  catalog.projects=[{trees:[tree]}];
  const refreshed=Promise.withResolvers();catalog.saveParent=async()=>{};catalog.refresh=()=>refreshed.promise;
  const stack={size:()=>routes.length,getAllPathName:()=>routes.map(r=>r.name),getParamByIndex:i=>routes[i]?.key};
  const route=Object.assign(new NativeRoute(),{model:root,projectCatalog:catalog,stack,navigationIntent:0,agentFormRevision:0,
    machineIntent:0,showSheet:false,chatForward:{clearNew(){}},startWorktree:{close(){}},launchChoices:{clear(){}},
    loadRecentSetups(){},loadAgentLaunchers(){},pushPlace(name,key){routes.push({name,key});this.navigationIntent++;}});
  let settled, currentPage=true;
  const screen=Object.assign(new NativeProjects(),{editedTree:tree,editor:form,editorShown:true,
    onChanged:(tree,change)=>{settled=route.worktreeChanged(tree,change,origin,()=>currentPage);}});
  await form.open(root,tree,'create');form.automatic=true;
  return {...f,machine,catalog,routes,route,screen,leave:()=>{currentPage=false;},finish:async()=>{refreshed.resolve();await settled;}};
}

test('a direct host receipt presents New Agent only after the editor closes, before inventory catches up; cancel leaves the child selected',async()=>{
  const f=await projectCreateUI();
  await f.screen.complete();
  assert.equal(f.screen.editorShown,false);assert.equal(f.route.showSheet,false);
  assert.equal(f.routes.length,1);assert.equal(f.writes.length,1);
  f.screen.editorDismissed();await nextFrame();
  const preset=f.route.selectedAgentTree();
  assert.deepEqual([preset.sourceId,preset.source,preset.branch,preset.cwd,preset.parent],['new-id','herdr','automatic','/qa/automatic','child']);
  assert.equal(f.route.agentModel,f.owner);assert.equal(f.route.directory,preset.cwd);
  assert.deepEqual([f.route.sheetKind,f.route.showSheet,f.route.pickedAgentTree],['newagent',true,'']);
  assert.deepEqual(f.routes.at(-1),{name:'project_tree',key:preset.key});assert.equal(f.catalog.browser.selectedTree,preset.key);
  assert.equal(f.catalog.tree(preset.key),undefined,'the receipt does not add a catalog management target');
  f.screen.editorDismissed();assert.equal(f.routes.length,2,'native dismissal is consumed once');
  f.route.showSheet=false;await f.finish();
  assert.equal(f.routes.at(-1).key,preset.key);assert.equal(f.catalog.selectedWorktree(preset.key),preset);
  assert.deepEqual(f.writes.map(w=>w.path),['/api/v1/machines/Studio%2F1/worktree/create']);
  const other=Object.assign(new ProjectWorktree(),preset,{cwd:'/another/checkout'});
  f.catalog.projects=[{trees:[other]}];assert.equal(f.route.selectedAgentTree(),undefined,'same id and different path cannot be substituted');
  f.owner.epoch++;f.catalog.projects=[];assert.equal(f.catalog.selectedWorktree(preset.key),undefined);
});

test('a confirmed create returns to its child when the workspace listing omits the branch and parent', async () => {
  const f = await projectCreateUI();
  await f.screen.complete();
  f.screen.editorDismissed();
  await nextFrame();
  const receipt = f.route.selectedAgentTree();
  assert.equal(f.route.showSheet, true);
  const sparse = { machine_id: receipt.machine, source: receipt.source, source_id: receipt.sourceId, cwd: receipt.cwd };
  f.catalog.ownerScope = f.root.scope();
  f.root.directoryOwners = () => [f.owner];
  f.root.directorySessions = [];
  function publish(fields = {}) {
    f.catalog.snapshots.set(f.owner.scope(), [f.catalog.fromHost(f.owner, {...sparse, ...fields})]);
    f.catalog.syncRows(f.root);
    return f.catalog.tree(receipt.key);
  }
  const listed = publish();
  assert.equal(f.route.selectedAgentTree(), listed);
  f.route.showSheet = false;
  await f.finish();
  assert.deepEqual(f.routes.at(-1), {name:'project_tree', key:receipt.key});
  Object.assign(f.screen, {catalog:f.catalog, page:'project_tree', target:receipt.key});
  assert.ok(listed && listed !== receipt, 'native detail and actions must resolve the catalog object');
  assert.equal(f.catalog.selectedWorktree(receipt.key), listed);
  assert.equal(f.screen.menuTarget(receipt.key), listed);
  assert.equal(f.writes.length, 1);

  const offline = publish({online:false});
  assert.equal(f.catalog.selectedWorktree(receipt.key), offline);
  assert.equal(offline.canStart, false);
  assert.equal(offline.permits('rename'), false);
  f.catalog.createdParents.set(f.owner.scope(), [{machine:receipt.machine, source:receipt.source, id:receipt.sourceId,
    path:receipt.cwd, parent:receipt.parent}]);
  assert.equal(publish().parent, receipt.parent);
  for (const conflict of [{cwd:'/another/checkout'}, {branch:'another-branch'}, {branch:''},
    {parent_source_id:'another-parent'}, {parent_source_id:''}]) {
    publish(conflict);
    assert.equal(f.catalog.selectedWorktree(receipt.key), undefined, JSON.stringify(conflict));
    assert.equal(f.screen.menuTarget(receipt.key), undefined);
  }
  const agent = ResourceRow.from({session_id:'in-this-checkout', ...sparse, workspace_id:receipt.sourceId,
    space_kind:receipt.source, branch:'other-branch'}, 'session');
  agent.group=receipt.group; agent.manager=receipt.manager;
  f.root.directorySessions=[agent];
  publish();
  assert.equal(f.catalog.selectedWorktree(receipt.key), undefined, 'a known conflicting branch stays authoritative');
  agent.raw=JSON.stringify({...agent.data(),branch:receipt.branch});
  publish({branch:''});
  assert.equal(f.catalog.selectedWorktree(receipt.key), undefined, 'a session cannot replace an explicitly empty host branch');
  const confirmed = publish({branch:receipt.branch, parent_source_id:receipt.parent});
  assert.equal(f.catalog.selectedWorktree(receipt.key), confirmed);
  publish({machine_id:'other-host'});
  assert.equal(f.catalog.selectedWorktree(receipt.key), undefined);
  publish(); f.owner.epoch++;
  assert.equal(f.catalog.selectedWorktree(receipt.key), undefined);
});

test('cancel, uncertain creation, a different parent, offline machine, changed surface and retired owner cannot open a child form',async()=>{
  for (const kind of ['cancel','unknown','same id','other parent','offline','new machine trust','other page','retired owner','owner replacement','another sheet']) {
    const f=await projectCreateUI();
    if (kind==='cancel') f.screen.editorShown=false;
    else {
      if (kind==='unknown') f.behavior.unknown=true;
      if (kind==='same id'||kind==='other parent') f.behavior.createReply={workspace_id:kind==='same id'?'child':'new-id',
        branch:'automatic',path:'/qa/automatic',parent_workspace_id:kind==='other parent'?'different':'child'};
      await f.screen.complete();
    }
    if (kind==='offline') f.owner.connected=false;
    if (kind==='new machine trust') f.root.directoryMachines=[];
    if (kind==='other page') f.leave();
    if (kind==='retired owner') f.owner.epoch++;
    if (kind==='owner replacement') f.root.directoryOwnerFor=()=>({...f.owner});
    if (kind==='another sheet') f.route.showSheet=true;
    f.screen.editorDismissed();await f.finish();await nextFrame();
    assert.equal(f.route.sheetKind,undefined,kind);assert.equal(f.routes.length,1,kind);
    assert.equal(f.writes.filter(w=>w.path.endsWith('/sessions')).length,0,kind);
  }
});

test('a different navigation, directory identity or owner that wins the draft read never receives the late form',async()=>{
  for (const change of ['back','different branch','generation','offline machine','different form']) {
    const f=await projectCreateUI(), draft=Promise.withResolvers();
    f.owner.loadView=()=>draft.promise;await f.screen.complete();f.screen.editorDismissed();await nextFrame();
    const preset=f.catalog.createdSelection;assert.ok(preset);
    if (change==='back') { f.routes.pop();f.route.navigationIntent++; }
    if (change==='different branch') f.catalog.projects=[{trees:[Object.assign(new ProjectWorktree(),preset,{branch:'wrong'})]}];
    if (change==='generation') f.owner.epoch++;
    if (change==='offline machine') f.machine.state='offline';
    if (change==='different form') f.route.agentFormRevision++;
    draft.resolve('');await f.finish();await nextFrame();
    assert.equal(f.route.showSheet,false,change);
    assert.equal(f.writes.length,1);
  }
});


test('creating from either repository list presents the prefilled form over the existing search and returns to that list on Cancel',async()=>{
  for (const [origin,section] of [['agent_repositories',1],['projects',4],['project',1],['machine_projects',6]]) {
    const f=await projectCreateUI(origin,section);
    const browser=origin==='agent_repositories' ? f.catalog.agentBrowser : f.catalog.browser;
    browser.setSearch('login-form','all/work');browser.machine='selected-machine';browser.toggle('untouched-project',true);
    const folds=[...browser.preferences];browser.remember('origin','last-visible-row');
    await f.screen.complete();f.screen.editorDismissed();await nextFrame();
    const child=f.route.selectedAgentTree();assert.equal(child.sourceId,'new-id');
    assert.equal(f.route.showSheet,true);assert.equal(f.route.directory,child.cwd);
    assert.deepEqual(f.routes,[],'the repository row has never pushed a detail page');
    f.route.showSheet=false;await f.finish();
    assert.deepEqual(f.routes,[]);assert.equal(browser.selectedTree,child.key);
    assert.equal(browser.query,'login-form');assert.equal(browser.machine,'selected-machine');assert.deepEqual(browser.preferences,folds);
    assert.equal(browser.anchor('origin'),'last-visible-row');assert.equal(f.writes.length,1);
  }
});
