import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

globalThis.__demoTransportCalls = 0;

const stubs = new Map([
  ['demo-stub:transport', `
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
      async request() { globalThis.__demoTransportCalls++; throw new Error('transport_must_not_be_used'); }
      async requestBytes() { globalThis.__demoTransportCalls++; throw new Error('transport_must_not_be_used'); }
      send() { globalThis.__demoTransportCalls++; throw new Error('transport_must_not_be_used'); }
      close() { globalThis.__demoTransportCalls++; }
    }
  `],
  ['demo-stub:local-store', 'export class LocalStore {}'],
  ['demo-stub:mutation-store', 'export class MutationRecord {} export class MutationStore { static shared() { throw new Error("demo_mutation_store_must_not_be_used"); } }'],
  ['demo-stub:arkts', 'export const util = { TextDecoder: { create: () => ({ decodeToString: () => "" }) } };']
]);

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/Compatibility.ets') && specifier === '../core/ChatForwardWire')
      return {url:new URL('../core/ChatForwardWire.ts',context.parentURL).href,shortCircuit:true};
    if (specifier === '../platform/HubTransport') return { url: 'demo-stub:transport', shortCircuit: true };
    if (specifier === '../platform/LocalStore') return { url: 'demo-stub:local-store', shortCircuit: true };
    if (specifier === './MutationStore') return { url: 'demo-stub:mutation-store', shortCircuit: true };
    if (specifier === '@kit.ArkTS') return { url: 'demo-stub:arkts', shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier.endsWith('.ets') ? specifier : specifier + '.ets', context.parentURL).href, shortCircuit: true };
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

const { DemoRepository } = await import('../entry/src/main/ets/models/DemoRepository.ets');
const { messageDisplay } = await import('../entry/src/main/ets/models/Compatibility.ets');
const { parseMessageContent } = await import('../entry/src/main/ets/core/MessageContent.ts');
const { parseStarredIndexResponse, parseStarredListResponse } = await import('../entry/src/main/ets/core/StarredMessages.ts');
const { loadMessageImage } = await import('../entry/src/main/ets/models/MessageImage.ets');
const { loadRemoteDocument } = await import('../entry/src/main/ets/models/RemoteDocument.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

test('rendered demo image notes and remote documents fail closed before any transport call', async () => {
  globalThis.__demoTransportCalls = 0;
  const repo = new DemoRepository();
  const display = messageDisplay('<attached_images>\n/demo/photo.png\n</attached_images>');
  const content = parseMessageContent(display.text);
  assert.deepEqual(content.images, ['/demo/photo.png']);
  const chat = new ResourceRow('demo-agent-review', 'Review release checks', 'session');
  chat.manager = 'demo'; chat.machine = 'demo-mac'; chat.session = chat.id;
  const model = { profile: { manager: 'demo' }, repo: () => repo, connectionGeneration: () => 1,
    scope: () => 'demo-scope', target: () => 'demo-target', selected: chat, owns: () => true };
  await assert.rejects(loadMessageImage(model, 'demo-item-photo', content.images[0]), { code: 'demo_unsupported' });

  const document = new ResourceRow('demo-file', 'notes.md', 'file');
  document.manager = 'demo'; document.machine = 'demo-mac'; document.path = '/demo/ReleaseDesk/notes.md';
  await assert.rejects(loadRemoteDocument(model, document, () => true), { code: 'demo_unsupported' });
  await assert.rejects(repo.transport.request('/api/v1/assistant'), { code: 'demo_unsupported' });
  await assert.rejects(repo.transport.requestBytes('/api/v1/machines/demo-mac/file/download'), { code: 'demo_unsupported' });
  await assert.rejects(repo.transport.connect(() => {}), { code: 'demo_unsupported' });
  assert.throws(() => repo.transport.stream('/api/v1/ws'), { code: 'demo_unsupported' });
  assert.equal(repo.transport.hello([]), false);
  assert.equal(repo.transport.send('{}'), false);
  assert.equal(repo.transport.sendBinary(new ArrayBuffer(1)), false);
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('seed is isolated, synthetic, and ready for inherited HubRepository list methods', async () => {
  const repo = new DemoRepository();
  const roster = repo.roster();
  assert.equal(repo.transport.config.baseUrl, 'http://127.0.0.1:1/');
  assert.equal(repo.transport.config.hubId, 'demo');
  assert.equal(repo.transport.config.token, '');
  assert.equal(repo.transport.config.pin, '');
  assert.equal(repo.transport.config.managerId, 'demo');
  assert.equal(roster.machines.length, 1);
  assert.equal(roster.machines[0].id, 'demo-mac');
  assert.equal(roster.machines[0].data().trust, 'personal');
  assert.equal(roster.machines[0].data().online, true);
  assert.equal(roster.mainId, 'demo-main');
  assert.equal((await repo.sessions()).length, 3);
  assert.equal((await repo.machines()).length, 1);
  assert.equal((await repo.todos()).length, 3);

  roster.sessions[0].raw = '{}';
  assert.equal(repo.roster().sessions[0].data().title, 'Review release checks');
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('Persona main/new conversations keep independent mutable local history', async () => {
  const repo = new DemoRepository();
  const main = repo.roster().conversations.find((row) => row.id === repo.mainId);
  const mainHistory = repo.history(main);
  assert.match(mainHistory[0].text, /local Persona demo/);

  const created = await repo.newConversation(false, 'new-conversation-op');
  assert.equal(created.kind, 'conversation');
  assert.notEqual(created.id, main.id);
  const afterSend = repo.sendLocal(created, 'Keep this note local.');
  assert.equal(afterSend.at(-2).role, 'user');
  assert.equal(afterSend.at(-2).text, 'Keep this note local.');
  assert.match(afterSend.at(-1).text, /No message was sent/);
  assert.deepEqual(repo.history(main).map((item) => item.text), mainHistory.map((item) => item.text));
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('todo create, edit, complete, reopen, and deletion return real-shaped envelopes', async () => {
  const repo = new DemoRepository();
  const created = await repo.call('/api/v1/todos?manager=demo', 'POST', { title: 'Review the demo', notes: 'First draft' });
  const id = created.todo.id;
  assert.match(id, /^demo-todo-/);

  const edited = await repo.call('/api/v1/todos/' + id + '?manager=demo', 'PATCH', { title: 'Review the local demo', notes: 'Updated draft' });
  assert.equal(edited.todo.title, 'Review the local demo');
  assert.equal(edited.todo.notes, 'Updated draft');
  const done = await repo.call('/api/v1/todos/' + id + '?manager=demo', 'PATCH', { done: true });
  assert.equal(done.todo.status, 'done');
  assert.ok(done.todo.done_at);
  const reopened = await repo.call('/api/v1/todos/' + id + '?manager=demo', 'PATCH', { done: false });
  assert.equal(reopened.todo.status, 'not_started');
  assert.equal(Object.hasOwn(reopened.todo, 'done_at'), false);
  assert.ok(reopened.todo.reopened_at);
  assert.ok((await repo.todos()).some((row) => row.id === id));
  await repo.call('/api/v1/todos/' + id + '?manager=demo', 'DELETE');
  assert.equal((await repo.todos()).some((row) => row.id === id), false);
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('the local to-do detail applies and clears the same plan fields without reaching a transport',async()=>{
  const repo=new DemoRepository();
  const available=await repo.call('/api/v1/todos?manager=demo','GET');const id=available.todos[0].id;
  const path='/api/v1/todos/'+id+'?manager=demo';
  let r=await repo.call(path,'PATCH',{due_date:'2026-11-25',remind_at:'2026-11-24T10:00:00Z',priority:'high',project:'demo-project-release'});
  assert.equal(r.todo.due_date,'2026-11-25');assert.equal(r.todo.priority,'high');assert.equal(r.todo.project,'demo-project-release');
  r=await repo.call(path,'PATCH',{due_at:null,remind_at:null,priority:'',project:''});
  assert.ok(!r.todo.due_at&&!r.todo.due_date&&!r.todo.remind_at&&!r.todo.project);assert.equal(r.todo.priority,'');
  await assert.rejects(repo.call(path,'PATCH',{project:'missing-project'}));assert.equal(globalThis.__demoTransportCalls,0);
});

test('knowledge detail, tags, search, and related moments mutate only sample entries', async () => {
  const repo = new DemoRepository();
  const projects = await repo.knowledge('projects');
  const project = projects[0];
  const detailPath = '/api/v1/kb/projects/' + encodeURIComponent(project.id);
  const before = await repo.call(detailPath);
  assert.equal(before.detail.id, project.id);
  assert.ok(Array.isArray(before.matches));

  await repo.call(detailPath, 'PUT', { tags: ['demo', 'reviewed'], importance: 'P1', relevance: 90, status: 'paused' });
  const after = await repo.call(detailPath);
  assert.deepEqual(after.detail.meta.tags, ['demo', 'reviewed']);
  assert.equal(after.detail.meta.importance, 'P1');
  assert.equal(after.detail.meta.status, 'paused');

  const results = await repo.call('/api/v1/kb-search?q=checklist&results=1');
  assert.ok(results.results.some((result) => result.entry.id === 'demo-doc-release'));
  const hits = await repo.knowledge('projects', 'release');
  assert.ok(hits.length > 0);
  assert.match((await repo.call('/api/v1/kb/moments/demo-moment-release')).markdown, /sample release checklist/);
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('Inbox list, source context, read receipts, confirmed reply routes, and handoff stay synthetic', async () => {
  const repo = new DemoRepository();
  const rows = await repo.inbox();
  const lark = rows.find((row) => row.id === 'demo-inbox-lark');
  const mail = rows.find((row) => row.id === 'demo-inbox-mail');
  assert.ok(lark && mail);
  assert.ok((await repo.resource(lark)).id === lark.id);

  const sources = await repo.call('/api/v1/messaging?manager=demo');
  assert.equal(sources.sources[0].id, 'lark');
  const account = await repo.call('/api/v1/messaging/lark/accounts?manager=demo');
  assert.equal(account.default, 'demo-lark-account');
  const status = await repo.call('/api/v1/messaging/lark/status?manager=demo&account=demo-lark-account');
  assert.equal(status.signed_in, true);
  const messages = await repo.call('/api/v1/messaging/lark/conversations/demo-lark-room/messages?manager=demo&account=demo-lark-account');
  assert.equal(messages.messages.length, 2);
  assert.equal(messages.messages[0].id, 'demo-lark-message-1');
  const thread = await repo.call('/api/v1/messaging/lark/messages/demo-lark-message-1/thread?manager=demo&account=demo-lark-account');
  assert.equal(thread.root.id, 'demo-lark-message-1');
  assert.equal(thread.messages.length, 2);
  await repo.call('/api/v1/messaging/lark/conversations/demo-lark-room/read?manager=demo&account=demo-lark-account', 'POST', {
    position: '1001', message_ids: ['demo-lark-message-1']
  });

  const replied = await repo.reply(lark, 'I will review the sample checklist.', 'demo-lark-reply');
  assert.equal(replied.id, lark.id);
  assert.match(replied.data().summary, /Demo reply/);
  const processed = await repo.processInbox(lark);
  assert.equal(processed.isDone(), true);
  const reopened = await repo.processInbox(processed);
  assert.equal(reopened.isDone(), false);

  const mailTarget = mail.data().reply_target;
  const mailReply = await repo.reply(mail, 'I will check the sample notes.', 'demo-mail-reply');
  assert.equal(mailReply.id, mail.id);
  assert.match(mailReply.data().summary, /Demo reply/);
  assert.equal(mailTarget.uid, 42);

  const handoff = await repo.call('/api/v1/inbox/' + lark.id + '/handoff?manager=demo', 'POST', {
    text: 'Review the checklist', client_id: 'demo-handoff-op'
  }, 'demo-handoff-op');
  assert.equal(handoff.item.id, lark.id);
  assert.equal(handoff.session.machine_id, 'demo-mac');
  assert.ok(repo.history({ kind: 'conversation', id: handoff.session.session_id, session: handoff.session.session_id, machine: handoff.session.machine_id, data: () => ({}) }).length > 0);
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('generic demo thread replies validate their parent and replay only the matching client id', async () => {
  const repo = new DemoRepository();
  const path = '/api/v1/messaging/lark/conversations/demo-lark-room/messages?manager=demo&account=demo-lark-account';
  const body = { client_id: 'demo-thread-reply', expect_user: 'demo-user', text: 'I reviewed the sample plan.', reply_to: 'demo-lark-message-1', thread: true };
  const accepted = await repo.call(path, 'POST', body, body.client_id);
  assert.equal(accepted.state, 'done');
  const threadPath = '/api/v1/messaging/lark/messages/demo-lark-message-1/thread?account=demo-lark-account';
  let thread = await repo.call(threadPath);
  assert.equal(thread.messages.length, 3);
  assert.equal(thread.messages[2].thread_root, 'demo-lark-message-1');
  assert.equal(thread.messages[2].reply_to, 'demo-lark-message-1');

  assert.equal((await repo.call(path, 'POST', body, body.client_id)).state, 'done');
  thread = await repo.call(threadPath);
  assert.equal(thread.messages.length, 3);
  await assert.rejects(repo.call(path, 'POST', { ...body, text: 'Different content.' }, body.client_id), { code: 'demo_client_id_conflict' });
  await assert.rejects(repo.call(path, 'POST', { ...body, client_id: 'bad-thread', reply_to: 'missing-message' }, 'bad-thread'), { code: 'reply_target_changed' });
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('agent roster mutations and todo linking use only admitted local state', async () => {
  const repo = new DemoRepository();
  const roster = repo.roster(), machine = roster.machines[0], todo = roster.todos[0];
  const created = await repo.createAgent('Prepare a sample report', machine, '/demo/ReleaseDesk', 'codex', 'demo-create-agent', todo.id, undefined, '', '', '', '', '  Sample report  ');
  assert.equal(created.data().slot_name, 'Sample report');
  assert.equal(created.manager, 'demo');
  assert.equal(created.machine, machine.id);
  const linked = await repo.call('/api/v1/todos/' + todo.id + '/agents/' + machine.id + '/' + created.id + '?manager=demo', 'PUT');
  assert.equal(linked.todo.agents.length, 2);
  assert.ok(linked.todo.agents.some((agent) => agent.session_id === 'demo-agent-review'));
  assert.ok(linked.todo.agents.some((agent) => agent.session_id === created.id));

  await repo.call('/api/v1/sessions/' + machine.id + '/' + created.id + '/rename', 'POST', { title: 'Renamed demo task' });
  let session = (await repo.sessions()).find((row) => row.id === created.id);
  assert.equal(session.title, 'Renamed demo task');
  await repo.call('/api/v1/sessions/' + machine.id + '/demo-agent-notes/seen', 'POST');
  session = (await repo.sessions()).find((row) => row.id === 'demo-agent-notes');
  assert.equal(session.state, 'idle');

  const beforeHistory = repo.history(created).map((item) => item.text);
  await assert.rejects(repo.send(created, 'This must not be stored.', 'blocked-op', '', [], () => false), { code: 'request_updated' });
  assert.deepEqual(repo.history(created).map((item) => item.text), beforeHistory);
  await repo.call('/api/v1/sessions/' + machine.id + '/' + created.id + '/close', 'POST');
  assert.equal((await repo.sessions()).find((row) => row.id === created.id).state, 'done');
  await repo.call('/api/v1/sessions/' + machine.id + '/' + created.id, 'DELETE');
  assert.equal((await repo.sessions()).some((row) => row.id === created.id), false);
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('unsupported actions and rejected admission do not mutate or touch transport', async () => {
  const repo = new DemoRepository();
  const row = repo.roster().sessions[1], before = repo.history(row).map((item) => item.text);
  await assert.rejects(repo.call('/api/v1/sessions/' + row.machine + '/' + row.id + '/messages', 'POST', {
    text: 'This must not be stored.', client_msg_id: 'blocked-op'
  }, 'blocked-op', () => false), { code: 'request_updated' });
  assert.deepEqual(repo.history(row).map((item) => item.text), before);

  const initialTodos = await repo.todos();
  await assert.rejects(repo.call('/api/v1/todos/' + initialTodos[0].id + '?manager=demo', 'PATCH', { title: 'Changed', notes: 'x'.repeat(21000) }), { code: 'demo_text_too_large' });
  assert.equal((await repo.todos())[0].title, initialTodos[0].title);
  await assert.rejects(repo.call('/api/v1/mail/send?manager=demo', 'POST', { text: 'No real send.' }), { code: 'demo_unsupported' });
  await assert.rejects(repo.call('/api/v1/extensions?manager=demo'), { code: 'demo_unsupported' });
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('demo manager permissions, new conversations and the independent AI twin stay local and separate',async()=>{
 const repo=new DemoRepository(),conf='/api/v1/assistant?manager=demo',twin='/api/v1/model-config/purposes/inbox.agent?manager=demo';
 const initial=(await repo.call(conf)).conversations;
 assert.equal((await repo.call(conf)).default_permission_mode,'default');
 await repo.call(conf,'PUT',{default_permission_mode:'acceptEdits'});
 const created=await repo.call('/api/v1/assistant/conversations?manager=demo','POST',{});
 assert.equal(created.session.permission_mode,'acceptEdits');
 const specific=await repo.call('/api/v1/assistant/conversations?manager=demo','POST',{permission_mode:'plan'});
 assert.equal(specific.session.permission_mode,'plan');
 assert.deepEqual((await repo.call(conf)).conversations.slice(0,initial.length),initial);
 const purposes=(await repo.call('/api/v1/model-config?manager=demo')).purposes;
 assert.ok(Array.isArray(purposes.find(p=>p.id==='agent.new').uses));
 const before=(await repo.call(twin)).purpose;assert.equal(before.effective.permission_mode,'default');
 const changed=(await repo.call(twin,'PATCH',{revision:before.revision,permission_mode:'auto',model:'other'})).purpose;
 assert.equal(changed.override.permission_mode,'auto');
 await assert.rejects(repo.call(twin,'PATCH',{revision:before.revision,permission_mode:'default'}),{status:409});
 const clean=(await repo.call(twin,'PATCH',{revision:changed.revision,permission_mode:null})).purpose;
 assert.deepEqual(clean.override,{model:'other'});assert.equal(clean.effective.permission_mode,'default');
 await assert.rejects(repo.call(twin.replace('manager=demo','manager=other')),{status:404});
 assert.equal((await repo.call(conf)).default_permission_mode,'acceptEdits');
 repo.seed();assert.equal((await repo.call(conf)).default_permission_mode,'default');assert.deepEqual((await repo.call(twin)).purpose.override,{});
 assert.equal(globalThis.__demoTransportCalls,0);
});

test('read-only demo preview routes serve synthetic files, git diff, screen, metrics, and usage offline', async () => {
  globalThis.__demoTransportCalls = 0;
  const repo = new DemoRepository();
  const fs = await repo.call('/api/v1/machines/demo-mac/fs?path=%2Fdemo%2FReleaseDesk&cwd=%2Fdemo%2FReleaseDesk');
  assert.equal(fs.path, '/demo/ReleaseDesk');
  assert.ok(fs.entries.some((entry) => entry.name === 'CHANGELOG.md'));

  const file = await repo.call('/api/v1/machines/demo-mac/file?path=%2Fdemo%2FReleaseDesk%2FCHANGELOG.md&cwd=%2Fdemo%2FReleaseDesk');
  assert.equal(file.name, 'CHANGELOG.md');
  assert.match(file.text, /Highlights/);

  const git = await repo.call('/api/v1/machines/demo-mac/git/status?cwd=%2Fdemo%2FReleaseDesk');
  assert.equal(git.is_repo, true);
  assert.ok(git.files.length >= 2);
  const diff = await repo.call('/api/v1/machines/demo-mac/git/diff?cwd=%2Fdemo%2FReleaseDesk&path=src%2Frelease.ts&staged=0');
  assert.match(diff.diff, /verifyReleaseGate/);

  const screen = await repo.call('/api/v1/sessions/demo-mac/demo-agent-review/screen?lines=60&source=visible');
  assert.match(screen.text, /42 passed/);

  const metrics = await repo.call('/api/v1/metrics');
  assert.equal(metrics.machines[0].machine_id, 'demo-mac');
  assert.equal(metrics.machines[0].system.cores, 12);
  const detail = await repo.call('/api/v1/machines/demo-mac/metrics?range=1h');
  assert.ok(detail.metrics.points.length >= 24);

  const usage = await repo.call('/api/v1/usage/windows');
  assert.equal(usage.accounts.length, 2);
  assert.equal(globalThis.__demoTransportCalls, 0);
});

test('seeded demo starred fixtures parse cleanly via StarredMessages list and index parsers', async () => {
  globalThis.__demoTransportCalls = 0;
  const repo = new DemoRepository();
  const listRaw = await repo.call('/api/v1/assistant/starred');
  const indexRaw = await repo.call('/api/v1/assistant/starred?index=1');
  const list = parseStarredListResponse(listRaw);
  const index = parseStarredIndexResponse(indexRaw);

  assert.equal(list.canWrite, true);
  assert.equal(index.canWrite, true);
  assert.ok(list.stars.length >= 2);
  assert.ok(index.refs.length >= 2);

  for (const id of ['demo-star-1', 'demo-star-2']) {
    const star = list.stars.find((entry) => entry.id === id);
    assert.ok(star, `missing ${id}`);
    assert.equal(star.role, 'assistant');
    assert.ok(star.title.trim().length > 0);
    assert.ok(star.speaker.trim().length > 0);
    assert.ok(star.text.trim().length > 0);
    assert.ok(index.refs.some((ref) => ref.id === id));
  }
  assert.equal(globalThis.__demoTransportCalls, 0);
});
