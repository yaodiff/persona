import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync, existsSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/ClientModel.ets') && specifier === '../core/PhoneInterfaceStyle')
      return {url:new URL('../core/PhoneInterfaceStyle.ts',context.parentURL).href,shortCircuit:true};
    if ((context.parentURL?.endsWith('/Compatibility.ets') || context.parentURL?.endsWith('/ClientModel.ets')) && specifier === '../core/ChatForwardWire')
      return {url:new URL('../core/ChatForwardWire.ts',context.parentURL).href,shortCircuit:true};
    if (context.parentURL?.endsWith('/ClientModel.ets') && specifier === '../core/Markdown')
      return {url:new URL('../core/Markdown.ts',context.parentURL).href,shortCircuit:true};
    if (specifier === '@kit.ArkTS') return { url: 'persona-question-test:arkts', shortCircuit: true };
    if (context.parentURL?.endsWith('/ClientModel.ets')) {
      if (['../core/BackgroundActivity', '../core/ChatSendFeedback', '../core/DictationLanguage', './Wire', './AgentAttention', './InteractionDecision', './ChatDraftStore', '../core/Domain', '../core/HubModules', '../core/NextSuggestions', '../core/XiaoYiActions'].includes(specifier)) {
        const base = new URL(specifier, context.parentURL);
        return { url: base.href + (existsSync(new URL(base.href + '.ts')) ? '.ts' : '.ets'), shortCircuit: true };
      }
      return { url: 'persona-question-test:' + specifier, shortCircuit: true };
    }
    if (context.parentURL?.endsWith('/AgentAttention.ets') || context.parentURL?.endsWith('/InteractionDecision.ets') || context.parentURL?.endsWith('/ChatDraftStore.ets')) {
      if (specifier.startsWith('.')) return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    }
    if (context.parentURL?.endsWith('/Domain.ts') && specifier.startsWith('.'))
      return { url: new URL(specifier.endsWith('.ts') ? specifier : specifier + '.ts', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'persona-question-test:arkts') return {
      format: 'module', shortCircuit: true,
      source: 'export const url={URL:{parseURL:value=>new URL(value)}}; export const util={TextEncoder:class{encodeInto(value){return new TextEncoder().encode(value);}},generateRandomUUID:()=>"persona-question-operation"};'
    };
    if (url.startsWith('persona-question-test:')) return {
      format: 'module', shortCircuit: true,
      source: `
        export class HttpFailure extends Error { constructor(status=0, codeOrUnknown=false, unknownOrPending=false, pending=false) { super('synthetic failure'); this.status=status; this.resultUnknown=typeof codeOrUnknown==='string'?unknownOrPending:codeOrUnknown; this.resultPending=typeof codeOrUnknown==='string'?pending:unknownOrPending; } }
        export class AssistantViewModel { static forgetProfile(){} }
        export class SshTunnel { initialize(){} stop(){} async bind(){} async acquire(){} async forget(){} }
        export class KnowledgeRecording { static async forgetProfile(){} }
        export const deviceLook={async forgetHub(){}};
        export function clientLocale(){return 'default';} export function initializeClientLanguage(){} export function setClientLanguage(){}
        export const i18n={System:{setAppPreferredLanguage(){} }};
        export class DemoRepository {}
        export class NavigationContext { static rowFrom(value){return JSON.parse(value);} }
        export function utf8Bytes(value){return new TextEncoder().encode(value).length;}
        export function clearImagePreviews(){}
        export function parseMessageContent(text){return {text,images:[],files:[]};}
        export class ChatAttachments { static allowHub(){} static async forgetHub(){} }
        export const nativeNotifications={async initialize(){},async clear(){},features(){return [];},receive(){}};
        export const nativeLiveView={async initialize(){},async receiveFleetEvent(){},async syncFromModel(){},async stopNow(){},setForeground(){}};
        export function resolveManagerScope(value){return value;}
        export class SessionWorkspace {}
        export class HubTransport { async request(){return '{}';} close(){} }
        export class EndpointConfig {}
        export class CredentialVault {}
        export const dictationPreference = {choice:"auto",last:"",load(){},remember(){}};
      export class LocalStore { data=new Map(); revision(){return 0;} accepts(){return true;} async put(key,value){this.data.set(key,value);} async get(key){return this.data.get(key)||'';} async remove(key){this.data.delete(key);} allowHub(){} async purgeHub(){} async open(){} async close(){} }
        export class HubRepository {}
        export function segment(value){return encodeURIComponent(value);}
        export const tr=key=>key; export const Strings={refresh(){}};
        export function withSourceContext(text){return text;} export function messageDisplay(text){return text;}
        export class MutationRecord { id=''; state=''; path=''; requiresReceipt=false; }
        export class MutationStore { static forgetHub(){} }
        export class TranscriptModel { async open(){} async latest(){} async older(){} close(){} }
        export class Pairing { static async reachable(primary){return primary;} }
        export class PairingTarget {}
        export function scopeKey(value){return JSON.stringify(value);}
        export function sessionCapabilities(){return {canRead:true,canSend:true,canApprove:true,canInterrupt:true,canTerminal:true,canAttach:true};}
        export function projectToolTurn(){return {};}
        export class TranscriptReducer { items=[]; restore(){} }
        export const common={};
      `
    };
    if (url.endsWith('.ets')) return {
      format: 'module', shortCircuit: true,
      source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, ''))
    };
    return next(url, context);
  }
});

const { ClientModel } = await import('../entry/src/main/ets/models/ClientModel.ets');
const { Decision, HubProfile, ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');

function interaction(id, kind, toolName, options = {}) {
  return {
    id, machine_id: options.machine || 'machine-one', session_id: options.session || 'chat-one',
    manager: options.manager || 'personal', status: options.status || 'open', kind, tool_name: toolName,
    revision: options.revision || '', resumable: options.resumable || false, wait: options.wait,
    prompt: options.prompt || id, questions: kind === 'question' ? [{ question: options.prompt || id, options: [{ label: 'A' }, { label: 'B' }] }] : []
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function fixture(kind = 'conversation') {
  const model = new ClientModel();
  model.profile = new HubProfile('synthetic-hub');
  model.profile.manager = 'personal';
  model.selected = new ResourceRow('chat-one', 'Chat', kind);
  model.selected.machine = 'machine-one';
  model.selected.session = 'chat-one';
  model.selected.manager = 'personal';
  model.connected = true;
  model.repository = {
    interactions: [],
    sent: [],
    resolved: [],
    async decisions() { return this.interactions; },
    async send(...args) { this.sent.push(args); },
    async resolve(request) { this.resolved.push(request.id); return {}; },
    close() {}
  };
  model.loadChat = async () => {};
  return model;
}

test('Persona question recognition requires a conversation and the exact ask_user question tool', async () => {
  const model = fixture();
  model.repository.interactions = [
    interaction('ask', 'question', 'ask_user'),
    interaction('agent-question', 'question', 'AskUserQuestion'),
    interaction('unknown-question', 'question', 'future_tool'),
    interaction('permission', 'permission', 'ask_user'),
    interaction('plan', 'plan', 'ask_user')
  ];

  await model.refreshDecisions();

  assert.deepEqual(model.personaQuestions.map((request) => request.id), ['ask']);
  assert.equal(model.decision.id, 'agent-question', 'the earliest blocking request wins even when a Persona question came first');
  model.selected.kind = 'session';
  await model.refreshDecisions();
  assert.equal(model.personaQuestions.length, 0);
  assert.equal(model.decision.id, 'ask', 'ask_user remains blocking in Agent sessions');
});

test('refresh loads every request for only the selected session and preserves the first blocker across list order', async () => {
  const model = fixture();
  const question = interaction('ask', 'question', 'ask_user');
  const blocker = interaction('permission', 'permission', 'Bash');
  const otherSession = interaction('other-session', 'question', 'ask_user', { session: 'chat-two' });
  const otherMachine = interaction('other-machine', 'plan', '', { machine: 'machine-two' });
  const otherManager = interaction('other-manager', 'question', 'ask_user', { manager: 'secondary' });
  model.repository.interactions = [question, blocker, otherSession, otherMachine, otherManager];

  await model.refreshDecisions();
  assert.deepEqual(model.pendingDecisions.map((request) => request.id), ['ask', 'permission']);
  assert.equal(model.decision.id, 'permission');

  model.repository.interactions = [blocker, question, otherSession, otherMachine];
  await model.refreshDecisions();
  assert.deepEqual(model.pendingDecisions.map((request) => request.id), ['permission', 'ask']);
  assert.equal(model.decision.id, 'permission');
});

test('a failed interaction refresh retains the currently displayed requests', async () => {
  const model = fixture();
  model.repository.interactions = [interaction('ask', 'question', 'ask_user')];
  await model.refreshDecisions();
  const current = model.pendingDecisions[0];
  model.repository.decisions = async () => { throw new Error('temporary read failure'); };

  await assert.rejects(model.refreshDecisions(), /temporary read failure/);
  assert.equal(model.pendingDecisions[0], current);
  assert.equal(model.personaQuestions[0], current);
  model.repository.decisions = async () => [];
  await model.refreshDecisions();
  assert.deepEqual(model.pendingDecisions, [], 'a successful empty refresh removes questions from the active collection');
  assert.equal(model.decision, undefined);
});

test('recovery keeps chat send and answer gated until a successful decision read', async () => {
  const model = fixture();
  const blocker = interaction('permission', 'permission', 'Bash');
  model.repository.interactions = [blocker];
  await model.refreshDecisions();
  const previousBlocker = model.decision;
  assert.equal(model.canApprove(), true);

  // Offline/recovery invalidation clears cards so the existing composer remains mounted.
  model.clearPendingDecisions();
  model.draft = 'A recoverable draft';
  const delayed = deferred();
  model.repository.decisions = () => delayed.promise;
  const recovering = model.refreshDecisions();

  assert.equal(model.decision, undefined);
  assert.equal(model.canCompose(), true, 'the composer stays available for editing');
  assert.equal(model.canSend(), false, 'sending waits for this conversation owner to be rechecked');
  assert.equal(model.canApprove(), false, 'a stale form cannot answer during recovery');
  assert.equal(await model.send(), false);
  await model.answer('deny', {}, previousBlocker);
  assert.deepEqual(model.repository.sent, []);
  assert.deepEqual(model.repository.resolved, []);
  assert.equal(model.draft, 'A recoverable draft');

  delayed.reject(new Error('temporary recovery failure'));
  await assert.rejects(recovering, /temporary recovery failure/);
  assert.equal(model.decision, undefined);
  assert.equal(model.canSend(), false, 'a failed read cannot release the recovery gate');
  assert.equal(model.canApprove(), false);

  model.repository.decisions = async () => [];
  await model.refreshDecisions();
  assert.equal(model.decision, undefined, 'a successful empty read confirms that no blocker remains');
  assert.equal(model.canSend(), true);
  assert.equal(model.canApprove(), true);
  assert.equal(await model.send(), true);
  assert.equal(model.repository.sent.length, 1);
});

test('a Persona question stays in the current collection while an ordinary message uses the message outbox', async () => {
  const model = fixture();
  model.repository.interactions = [interaction('wait-false', 'question', 'ask_user', { wait: false })];
  model.selected.raw = JSON.stringify({ is_main: false });
  await model.refreshDecisions();
  model.draft = 'A follow-up message';

  assert.equal(model.decision, undefined);
  assert.equal(model.canCompose(), true);
  assert.equal(await model.send(), true);
  assert.equal(model.repository.sent.length, 1);
  assert.equal(model.repository.resolved.length, 0);
  assert.equal(model.pendingDecisions[0].id, 'wait-false');
  assert.equal(model.pendingDecisions[0].status, 'open');
  assert.equal(model.draft, '');
  assert.ok(model.local.data.has('outbox:' + model.scope()), 'the message went through the existing outbox');
});

test('several ask_user cards resolve independently, reject duplicates, and allow chat while a resolve is pending', async () => {
  const model = fixture();
  model.repository.interactions = [
    interaction('ask-one', 'question', 'ask_user', { resumable: true }),
    interaction('ask-two', 'question', 'ask_user')
  ];
  await model.refreshDecisions();
  const [first, second] = model.personaQuestions;
  const flights = new Map();
  model.repository.resolve = (request) => {
    model.repository.resolved.push(request.id);
    const flight = deferred();
    flights.set(request.id, flight);
    return flight.promise;
  };

  const firstCardSnapshot = Object.assign(new Decision(), first);
  const resolvingFirst = model.answer('A', {}, firstCardSnapshot);
  assert.equal(first.status, 'pending');
  await model.answer('A', {}, firstCardSnapshot);
  assert.deepEqual(model.repository.resolved, ['ask-one'], 'a duplicate submission is ignored');
  const resolvingSecond = model.answer('B', {}, second);
  assert.equal(second.status, 'pending');

  model.draft = 'Still available to chat';
  assert.equal(await model.send(), true);
  assert.equal(model.repository.sent.length, 1);

  flights.get('ask-two').resolve({});
  await resolvingSecond;
  assert.equal(second.status, 'resolved');
  assert.equal(first.status, 'pending', 'the faster response does not change another card');
  flights.get('ask-one').resolve({});
  await resolvingFirst;
  assert.equal(first.status, 'resolved');
});

test('stale request data and a form from a previous conversation cannot answer a replacement card', async () => {
  const model = fixture();
  model.repository.interactions = [interaction('same-id', 'question', 'ask_user', { prompt: 'Old question', revision: 'old' })];
  await model.refreshDecisions();
  const stale = model.personaQuestions[0];
  model.repository.interactions = [interaction('same-id', 'question', 'ask_user', { prompt: 'Updated question', revision: 'new' })];
  await model.refreshDecisions();
  await model.answer('A', {}, stale);
  assert.deepEqual(model.repository.resolved, []);
  assert.equal(model.error, 'request_updated');
  assert.equal(model.personaQuestions[0].status, 'open');

  const previousConversationCard = model.personaQuestions[0];
  const next = new ResourceRow('chat-two', 'Other', 'conversation');
  next.machine = 'machine-one'; next.session = 'chat-two'; next.manager = 'personal';
  await model.select(next);
  await model.answer('A', {}, previousConversationCard);
  assert.deepEqual(model.repository.resolved, []);
});
