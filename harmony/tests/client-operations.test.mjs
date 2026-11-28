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
    if (specifier === '@kit.ArkTS') return {url:'client-boundary:'+specifier,shortCircuit:true};
    if (context.parentURL?.endsWith('/ClientModel.ets')) {
      if (['../core/BackgroundActivity', '../core/ChatSendFeedback', '../core/DictationLanguage', './Wire', './AgentAttention', './AssistantView', './DemoRepository', './NavigationContext', './ChatDraftStore', './HubRepository', './MutationStore', './Compatibility', './InteractionDecision', '../core/ClientLocale', '../core/Domain', '../core/HubModules', '../core/NextSuggestions', '../core/ScopePolicy', '../core/StorageOwnership', '../core/MessageContent', '../core/TranscriptCache', '../core/XiaoYiActions'].includes(specifier)) {
        const base = new URL(specifier, context.parentURL);
        return {url: base.href + (existsSync(new URL(base.href + '.ts')) ? '.ts' : '.ets'), shortCircuit: true};
      }
      return {url: 'client-boundary:' + specifier, shortCircuit: true};
    }
    if (context.parentURL?.endsWith('/AssistantView.ets')) {
      if (specifier === '@kit.ArkTS') return {url:'client-boundary:'+specifier,shortCircuit:true};
      if (specifier.startsWith('.')) { const base=new URL(specifier,context.parentURL); return {url:base.href+(existsSync(new URL(base.href+'.ts'))?'.ts':'.ets'),shortCircuit:true}; }
    }
    if (context.parentURL?.endsWith('/DemoRepository.ets') || context.parentURL?.endsWith('/DemoTransport.ets') || context.parentURL?.endsWith('/HubRepository.ets') || context.parentURL?.endsWith('/MutationStore.ets')) {
      if (['./Wire','./HubRepository','./InteractionDecision','./MutationStore','./DemoTransport'].includes(specifier)) return {url:new URL(specifier+'.ets',context.parentURL).href,shortCircuit:true};
      if (specifier==='../core/StorageOwnership') return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
      return {url:'client-boundary:'+specifier,shortCircuit:true};
    }
    if (context.parentURL?.endsWith('/InboxWorkspace.ets') || context.parentURL?.endsWith('/TodoWorkspace.ets') || context.parentURL?.endsWith('/TodoHubView.ets') || context.parentURL?.endsWith('/TodoPhoneSections.ets') || context.parentURL?.endsWith('/PhoneTodoDetailEditor.ets') ||
      context.parentURL?.endsWith('/MessageImage.ets') || context.parentURL?.endsWith('/RemoteDocument.ets')) {
      if (specifier === '@kit.ArkTS' || specifier === '../platform/HubTransport') return {url:'client-boundary:'+specifier,shortCircuit:true};
      if (specifier.startsWith('.')) return {url:new URL(specifier.endsWith('.ets') || specifier.endsWith('.ts') ? specifier : specifier + '.ets',context.parentURL).href,shortCircuit:true};
    }
    if((context.parentURL?.endsWith('/NavigationContext.ets')||context.parentURL?.endsWith('/ChatDraftStore.ets')||context.parentURL?.endsWith('/Compatibility.ets'))&&specifier==='./Wire')return {url:new URL('./Wire.ets',context.parentURL).href,shortCircuit:true};
    if(context.parentURL?.endsWith('/QuestionWorkspace.ets') && specifier==='./Wire')return {url:new URL('./Wire.ets',context.parentURL).href,shortCircuit:true};
    if(context.parentURL?.endsWith('/AgentAttention.ets') || context.parentURL?.endsWith('/InteractionDecision.ets')&&specifier.startsWith('.'))return {url:new URL(specifier+'.ets',context.parentURL).href,shortCircuit:true};
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('client-boundary:')) return {format: 'module', shortCircuit: true, source: `
      export class HttpFailure extends Error { constructor(status=0,codeOrUnknown=false,unknownOrPending=false,pending=false) { super('synthetic failure'); this.status=status; this.resultUnknown=typeof codeOrUnknown==='string'?unknownOrPending:codeOrUnknown; this.resultPending=typeof codeOrUnknown==='string'?pending:unknownOrPending; } }
      export class HubTransport {async request(...args){return globalThis.clientTestRequest(...args);} close(){} } export class EndpointConfig {constructor(baseUrl='',hubId='',token='',pin='',managerId='',alternateUrls=[]){Object.assign(this,{baseUrl,hubId,token,pin,managerId,alternateUrls});}} export class CredentialVault {}
      export class LocalStore { data=new Map(); revision(){return 0;} accepts(){return true;} async put(k,v) {this.data.set(k,v);} async get(k) { return this.data.get(k)||''; } async remove(k){this.data.delete(k);} allowHub() {} async purgeHub(hub) {for(const key of this.data.keys())if(globalThis.clientIsHubStateKey(key,hub))this.data.delete(key);} }
      export class HubRepository {} export class TranscriptModel {} export class SessionWorkspace {}
      export class SshTunnel { initialize(){} stop(){} async bind(client){this.client=client;} async acquire(){if(globalThis.clientSshAcquire)return globalThis.clientSshAcquire(this.client);return undefined;} async forget(){} }
      export class KnowledgeRecording { static async forgetProfile(){} }
      export const dictationPreference = {choice:"auto",last:"",load(){},remember(){}};
      export const deviceLook = {async forgetHub(){}};
      export class Pairing {static async reachable(primary,...args){return globalThis.clientPairingAddress ? globalThis.clientPairingAddress(primary,...args) : primary;}} export class PairingTarget {} export class MutationRecord {} export class MutationStore { static forgetHub() {} }
      export class ChatAttachments { static allowHub() {} static async forgetHub() {} static suppressStoredSnapshot(...args) {globalThis.clientAttachmentSuppressed?.push(args);} static async commitStoredSnapshot(...args) {globalThis.clientAttachmentCommits?.push(args);if(globalThis.clientAttachmentCommitGate)await globalThis.clientAttachmentCommitGate();} }
      export function clearImagePreviews() {}
      export const common = {};
      export const url = {URL:{parseURL:value=>new URL(value)}};
      export const util = {TextEncoder:class {static create(){return new this();}encodeInto(value){return new TextEncoder().encode(value);}},generateRandomUUID:()=> 'synthetic-operation-'+(++globalThis.clientTestUUID)};
      export const tr = key => key; export const Strings = {refresh(){}}; export const i18n = {getSystemLanguage(){return 'zh-CN';},System:{setAppPreferredLanguage(value){globalThis.clientLanguageChanges?.push(value);}}};
      export const segment = encodeURIComponent;
      export const withSourceContext = text => text;
      export const messageDisplay = text => text;
      export const nativeNotifications = {async clear(){},features(){return ['elicitation'];}};
      export const nativeLiveView = {async initialize(){},async receiveFleetEvent(){},async syncFromModel(){},async stopNow(){},setForeground(){}};
    `};
    if (url.endsWith('.ets')) return {format:'module', shortCircuit:true, source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};
    return next(url, context);
  }
});
const {isHubStateKey}=await import('../entry/src/main/ets/core/StorageOwnership.ts');globalThis.clientIsHubStateKey=isHubStateKey;
const contextReference=JSON.stringify({kind:'todo',id:'source',title:'Source'});
globalThis.clientTestUUID=0;
const { ClientModel } = await import('../entry/src/main/ets/models/ClientModel.ets');
const { HubProfile, ResourceRow, Decision, ChatMessage } = await import('../entry/src/main/ets/models/Wire.ets');
const { HttpFailure } = await import('client-boundary:../platform/HubTransport');
const { InboxWorkspace } = await import('../entry/src/main/ets/models/InboxWorkspace.ets');
const { TodoWorkspace } = await import('../entry/src/main/ets/models/TodoWorkspace.ets');

const {clientLocale,setClientLanguage}=await import('../entry/src/main/ets/core/ClientLocale.ts');
function fixture() {
  setClientLanguage('default');
  const model = new ClientModel();
  model.profile = new HubProfile('synthetic-hub'); model.profile.manager='personal';
  model.selected = new ResourceRow('chat-one','Chat','session'); model.selected.machine='machine-one'; model.selected.session='chat-one';
  model.decisionReadyTarget = model.target();
  model.connected = true; model.canSend=()=>true; model.canApprove=()=>true; model.canInterrupt=()=>true;
  model.draft='exact draft '; model.sourceContext=contextReference;
  const writes=[];
  model.repository={async send(...args) { writes.push(args); }, async resolve() { return {}; }, async stop() {}};
  model.loadChat=async()=>{};
  return {model,writes};
}
test('an accepted hub hello selects picture limits by the destination session agent', () => {
  const model = new ClientModel();
  model.profile.hubId = 'hub-a';
  model.refresh = async () => {};
  const hello = (limits, hub = 'hub-a') => model.event(JSON.stringify({type: 'hello', hub_id: hub, image_limits: limits}));
  const values = row => {
    const limits = model.imageLimits(row);
    return [limits.requestBytes, limits.imageBytes, limits.maxSide, limits.perMessage];
  };
  const standard = [33554432, 3750000, 1600, 20];
  const row = ResourceRow.from({id: 'one', machine_id: 'machine', agent: 'codex'}, 'session');
  assert.deepEqual(values(row), standard);
  hello({request_bytes: 7340032, image_bytes: 100000, max_side: 1024, per_message: 7,
    agents: {codex: {per_message: 2, max_side: 600}, claude: {per_message: 4}, pi: 'invalid'}});
  assert.deepEqual(values(row), [standard[0], standard[1], 600, 2]);
  assert.deepEqual(values(), [standard[0], standard[1], standard[2], 4]);
  model.sessions = [ResourceRow.from({id: 'one', machine_id: 'machine', agent: 'pi', launcher: 'codex'}, 'session')];
  assert.deepEqual(values(row), [7340032, 100000, 1024, 7]);
  // An assistant conversation names its agent under `launcher`, not `agent`.
  const conversation = ResourceRow.from({id: 'conv', machine_id: 'machine', launcher: 'codex'}, 'conversation');
  assert.deepEqual(values(conversation), [standard[0], standard[1], 600, 2]);
  hello({per_message: 13}, 'another-hub');
  assert.deepEqual(values(row), [7340032, 100000, 1024, 7]);
  hello({request_bytes: 'wrong', image_bytes: -1, max_side: 0, per_message: 1e30});
  assert.deepEqual(values(row), standard);
  hello(undefined);
  assert.deepEqual(values(row), standard);
});

test('whole and partial quotes retain their source and other draft references across the chat wire',async()=>{
  const {model}=fixture();model.features=['persona_contracts_v1'];
  const message=new ChatMessage('visible-answer','assistant','Use **the cache** and `max-age`.');
  message.itemId='answer-source';message.ownerTarget=model.target();model.messages=[message];model.draft='Existing words';
  const original={kind:'todo',id:'task',title:'Existing task'};model.sourceContext=JSON.stringify(original);
  let writes=0;model.persistDraft=async()=>{writes++;};
  assert.equal(await model.quoteMessageContext(message,'Claude','the cache and max-age'),true);
  const refs=JSON.parse(model.sourceContext).contexts;
  assert.deepEqual(refs[0],original);assert.deepEqual(refs[1],{kind:'quote',title:'Claude',url:'',msg:'answer-source',text:'the cache and max-age',part:'selection'});
  assert.deepEqual(model.draftMessageQuotes(),[refs[1]]);assert.equal(model.otherContextTitle(),'Existing task');
  assert.equal(model.draft,'Existing words');assert.equal(writes,1);
  assert.ok(model.prepareMessageText().includes('<persona-context kind="quote" title="Claude" url="" msg="answer-source" part="selection">\nthe cache and max-age\n</persona-context>'));
  for(const sample of ['the cache and max-age','other words'])await model.quoteMessageContext(message,'Claude',sample);
  assert.equal(writes,1);
  await model.quoteMessageContext(message,'Claude');assert.equal(writes,2);assert.equal(model.draftMessageQuotes().length,2);
  await model.quoteMessageContext(message,'Claude','max-age');assert.equal(writes,3);assert.equal(model.draftMessageQuotes().length,3);
  model.removeMessageQuote(refs[1]);assert.equal(model.draftMessageQuotes().length,2);assert.equal(model.otherContextTitle(),'Existing task');
  const after=writes;model.messages=[];await model.quoteMessageContext(message,'Claude','cache');assert.equal(writes,after);
  model.messages=[message];message.ownerTarget='another chat';await model.quoteMessageContext(message,'Claude','cache');assert.equal(writes,after);
});


test('only an identity-matched hello on the current live scope and connection generation supplies a feature version',()=>{
 const {model}=fixture();
 model.profile.hubId='verified-id';
 model.repository.transport={hello(){}};
 model.repository.close=()=>{};model.reconcileWrites=async()=>{};model.refresh=async()=>{};model.flushNotification=async()=>{};
 const send=fields=>model.event(JSON.stringify({type:'hello',hub_id:'verified-id',server_version:'0.9.1',features:['client_ops'],...fields}));
 send({});assert.equal(model.verifiedServerVersion('client_ops'),'0.9.1');assert.equal(model.verifiedServerVersion('missing'),'');
 model.generation++;assert.equal(model.verifiedServerVersion('client_ops'),'');
 send({});model.profile.manager='work';assert.equal(model.verifiedServerVersion('client_ops'),'');
 send({});model.profile.hubId='another';assert.equal(model.verifiedServerVersion('client_ops'),'');
 model.profile.hubId='verified-id';send({hub_id:'foreign'});assert.equal(model.verifiedServerVersion('client_ops'),'');
 send({hub_id:''});assert.equal(model.verifiedServerVersion('client_ops'),'');
 send({server_version:''});assert.equal(model.verifiedServerVersion('client_ops'),'');
 send({features:[]});assert.equal(model.verifiedServerVersion('client_ops'),'');
 send({});model.connected=false;assert.equal(model.verifiedServerVersion('client_ops'),'');
});
function decision(model,id='request-one') { const value=new Decision(); value.id=id; value.kind='permission'; value.raw=JSON.stringify({id}); model.decision=value; return value; }
function deferred() { let resolve,reject; const promise=new Promise((yes,no)=>{resolve=yes;reject=no;}); return {promise,resolve,reject}; }

test('send feedback belongs to the current input and distinguishes acceptance, transcript echo and actual reply', async () => {
  const {model} = fixture(), received = deferred(), response = deferred();
  model.sourceContext = ''; model.draft = 'same request text';
  model.repository.send = async (_row, _text, uuid) => { received.resolve(uuid); await response.promise; return {ok:true}; };
  const turn = model.send();
  assert.equal(model.sendFeedbackPhase(), 'sending');
  const id = await received.promise, target = model.target(), original = model.selected;
  const post = (seq,role,kind,text,id) => ({id:'item-'+seq,seq,role,kind,text,client_msg_id:id});
  const rows = [post(40,'user','text','same request text','another-device'),post(41,'assistant','text','older answer')];
  model.publishTranscript(rows,false);
  assert.equal(model.sendFeedbackPhase(), 'sending');
  rows.push(post(42,'user','text','same request text',id));
  model.publishTranscript(rows,false);
  assert.equal(model.sendFeedbackPhase(), 'sending', 'a transcript echo alone cannot accept this request');
  model.selected = new ResourceRow('other','Other','session');
  response.resolve(); assert.equal(await turn,true); assert.equal(model.sendFeedbackPhase(),'');
  model.selected = original; assert.equal(model.target(),target); assert.equal(model.sendFeedbackPhase(),'processing');
  rows.push(post(43,'assistant','thinking','thinking'),post(44,'assistant','tool_use','tool'));
  model.publishTranscript(rows,false); assert.equal(model.sendFeedbackPhase(),'processing');
  rows.push(post(45,'assistant','text','answer'));
  model.publishTranscript(rows,false); assert.equal(model.sendFeedbackPhase(),'');
  model.publishTranscript(rows,false); await model.restoreLocalDelivery(); assert.equal(model.sendFeedbackPhase(),'');
});

test('rejected, held and stopped sends keep their native recovery paths and release only the matching activity', async () => {
  const {model} = fixture(); model.sourceContext = '';
  model.repository.send = async () => { throw new Error('offline'); };
  assert.equal(await model.send(), false); assert.equal(model.sendFeedbackPhase(),'');
  const rejected = model.messages.find(m=>m.localOperation); assert.ok(rejected);
  assert.equal(model.draft,'exact draft ');
  const {model:heldModel} = heldFixture();
  heldModel.draft='for offline machine';
  heldModel.repository.send=async (_row,text,id)=>({delivered:'later',undelivered:{id,text,state:'waiting',ts:'2026-01-01T00:00:00Z'}});
  assert.equal(await heldModel.send(),true); assert.equal(heldModel.sendFeedbackPhase(),'');
  const {model:live} = fixture(); live.sourceContext='';
  await live.send(); assert.equal(live.sendFeedbackPhase(),'processing');
  live.selected.state='working';
  const stop=deferred(), entered=deferred();
  live.repository.stop=async()=>{entered.resolve();await stop.promise;};
  const stopping=live.stop();await entered.promise;
  live.draft='newer request'; await live.send(); stop.resolve(); await stopping;
  assert.equal(live.sendFeedbackPhase(),'processing','the prior stop cannot finish a newer local attempt');
  await live.stop(); assert.equal(live.sendFeedbackPhase(),'');
});

test('cold chat restores only an accepted outbox operation for its owner without resending', async () => {
  const {model} = fixture(); model.sourceContext='';
  await model.send();
  const cold=fixture().model; cold.local=model.local;
  let writes=0; cold.repository.send=async()=>{writes++;};
  cold.profile.manager='other-manager';
  await cold.restoreLocalDelivery(); assert.equal(cold.sendFeedbackPhase(),'');
  cold.profile.manager='personal'; await cold.restoreLocalDelivery();
  assert.equal(cold.sendFeedbackPhase(),'processing'); assert.equal(writes,0);
  cold.selected.state='working'; await cold.stop();
  const reopened=fixture().model; reopened.local=model.local; await reopened.restoreLocalDelivery();
  assert.equal(reopened.sendFeedbackPhase(),'');
  const {ChatDraftStore,ChatOutboxEntry}=await import('../entry/src/main/ets/models/ChatDraftStore.ets');
  const legacy=new ChatOutboxEntry();legacy.id='old-done';legacy.target=reopened.target();legacy.at=Date.now();legacy.state='done';
  await new ChatDraftStore(reopened.local).stage(reopened.scope(),legacy);
  await reopened.restoreLocalDelivery();assert.equal(reopened.sendFeedbackPhase(),'');
});

test('the accepted window starts on the receipt and fresh terminal snapshots alone can retire it', async () => {
  const {ChatSendFeedback}=await import('../entry/src/main/ets/core/ChatSendFeedback.ts');
  const activity=()=>new ChatSendFeedback('local','machine','session',10,1000,'idle','old failure','yesterday');
  const first=activity();assert.equal(first.visible(1000000),'sending');
  first.observeSession('offline','','');assert.equal(first.visible(1000000),'sending');
  first.accept(1000000);first.accept(1001000);assert.equal(first.visible(1059999),'processing');assert.equal(first.visible(1060000),'');
  const fresh=activity();fresh.accept(1200);fresh.observeSession('idle','old failure','yesterday');assert.equal(fresh.visible(1300),'processing');
  fresh.observeSession('working','','');fresh.observeSession('idle','','');assert.equal(fresh.visible(1400),'processing');
  fresh.observeItems([{id:'own',role:'user',kind:'text',seq:11,client_msg_id:'LOCAL'}]);
  fresh.observeSession('working','','');fresh.observeSession('idle','','');assert.equal(fresh.visible(1500),'');
  fresh.accept(1600);assert.equal(fresh.visible(1700),'');
  const failed=activity();failed.accept(1200);failed.observeSession('idle','old failure','today');assert.equal(failed.visible(1500),'');
  const blocked=activity();blocked.accept(1200);blocked.observeSession('blocked','','');assert.equal(blocked.visible(1500),'');
});

test('the Beautiful work header starts only after the accepted send has its own visible transcript echo', async () => {
  const {model}=fixture(),entered=deferred(),reply=deferred();model.sourceContext='';
  model.repository.send=async (_row,_text,id)=>{entered.resolve(id);await reply.promise;return {ok:true};};
  const turn=model.send(),id=await entered.promise;assert.equal(model.sendFeedbackWorkHeader(),false);
  reply.resolve();await turn;assert.equal(model.sendFeedbackPhase(),'processing');assert.equal(model.sendFeedbackWorkHeader(),false);
  const echo={id:'same-text',seq:4,role:'user',kind:'text',text:model.draft,client_msg_id:'some-other-send'};
  model.publishTranscript([echo],false);assert.equal(model.sendFeedbackWorkHeader(),false);
  model.publishTranscript([{...echo,client_msg_id:id}],false);assert.equal(model.sendFeedbackWorkHeader(),true);
  model.publishTranscript([{...echo,client_msg_id:id},{id:'answer',seq:5,role:'assistant',kind:'text',text:'done'}],false);
  assert.equal(model.sendFeedbackWorkHeader(),false);
});

test('a follow-up during Agent approvals sends once to the active session and leaves every request unanswered', async () => {
  const { model, writes } = fixture();
  delete model.canSend;
  model.canCompose = () => true;
  model.sourceContext = '';
  model.selected.raw = JSON.stringify({ status: 'blocked' });
  const request = id => ({ id, kind: 'permission', machine_id: model.selected.machine, session_id: model.selected.session });
  const raw = [request('approve-build'), request('approve-format')];
  model.repository.resolve = () => { throw new Error('A note must never resolve a permission'); };
  model.decisionRefreshSequence = 1;
  model.replacePendingDecisions(raw, model.selected, model.target(), model.connectionGeneration(), model.scope(), 1);
  assert.equal(model.agentQueuesMessages(), true);
  assert.equal(model.decisionBlocksMessage(), false);
  model.draft = 'Please keep both originals';
  assert.equal(await model.send(), true);
  assert.deepEqual(writes.map(([row, text]) => [row.machine, row.session, text]), [['machine-one', 'chat-one', 'Please keep both originals']]);
  assert.deepEqual(model.pendingDecisions.map(r => [r.id, r.status]), [['approve-build', 'open'], ['approve-format', 'open']]);
  model.draft = 'A later draft';
  model.selected = new ResourceRow('other', 'Other', 'session');
  assert.equal(model.decisionBlocksMessage(), true);
  assert.equal(await model.send(), false);
  assert.equal(writes.length, 1);
});

test('the global Agent badge keeps the newest owned snapshot across out-of-order reads and assistant switches', async () => {
  const {model} = fixture();
  const old = deferred(), replaced = deferred();
  const card = id => ({machine_id: 'test-mac', interaction: {id, kind: 'question'}});
  const muted = () => false;
  model.repository.decisions = () => old.promise;
  const oldRead = model.readInteractionSnapshot();
  model.repository.decisions = async () => [card('current-a'), card('current-b')];
  await model.readInteractionSnapshot();
  old.resolve([card('late')]);
  await oldRead;
  assert.equal(model.phoneAgentBadge(muted), 2);

  model.repository.decisions = () => replaced.promise;
  const replacedRead = model.readInteractionSnapshot();
  model.profile.manager = 'new-assistant';
  replaced.resolve([card('previous-owner')]);
  await replacedRead;
  assert.equal(model.phoneAgentBadge(muted), 0);
  model.repository.decisions = async () => [card('my-request')];
  await model.readInteractionSnapshot();
  assert.equal(model.phoneAgentBadge(muted), 1);
  model.connected = false;
  assert.equal(model.phoneAgentBadge(muted), 0);
});

test('directory traffic reaches observers without redispatch to phone tools', () => {
  const {model}=fixture();
  const observed=[],commands=[];
  const off=model.observeEvents((raw,type)=>observed.push([type,raw]));
  model.setDeviceEvents(raw=>commands.push(raw));
  const directory=JSON.stringify({type:'catalog_preview',sessions:[{session_id:'sample'}]});
  const tool=JSON.stringify({type:'tool_call',call_id:'one',name:'phone_device_info',args:{}});
  model.event(directory);
  model.event(tool);
  off();
  model.event(directory);
  assert.deepEqual(observed,[['catalog_preview',directory],['tool_call',tool]]);
  assert.deepEqual(commands,[tool]);
});

test('the phone drawer reads current non-quiet open to-dos before opening the list and coalesces refreshes', async () => {
  const {model} = fixture();
  const reads = [], repo = {todos() { const result = deferred(); reads.push(result); return result.promise; }};
  const row = (id, extra={}) => ResourceRow.from({id,manager:'personal',...extra},'todo');
  const refresh = () => model.refreshPhoneList('todo', repo, model.generation, model.scope(), false);
  const first = refresh(), concurrent = refresh();
  assert.equal(reads.length, 1);
  reads[0].resolve([row('open'),row('quiet',{quiet:true}),row('closed',{done_at:'2026-11-01'}),row('other',{manager:'other'})]);
  await Promise.all([first,concurrent]);
  assert.equal(model.phoneTodoBadge(), 1);
  await refresh();
  assert.equal(reads.length, 1);
  model.todoRead.change++;
  const delayed = refresh();
  model.todoRead.change++;
  const latest = refresh();
  reads[2].resolve([row('open'),row('another')]);
  await latest;
  reads[1].resolve([]);
  await delayed;
  assert.equal(model.phoneTodoBadge(), 2);
  model.profile.manager = 'other';
  assert.equal(model.phoneTodoBadge(), 0);
});

function heldFixture() {
  const {model} = fixture();
  delete model.canSend;
  model.features = ['agent_message_hold'];
  model.selected.manager = 'personal';
  model.selected.machine = 'my laptop';
  model.selected.session = model.selected.id = 'chat/one';
  model.sessions = [model.selected];
  model.decisionReadyTarget = model.target();
  model.machines = [ResourceRow.from({id:'my laptop',name:'Desk',online:false}, 'machine')];
  const held = (id, state = 'waiting', error = 'Will send when Desk is back.', extra = {}) =>
    ({id, state, error, text:'Original words', computer:'Desk', at:'2026-11-01T12:00:00Z', ...extra});
  const update = (entries, extra = {}) => {
    model.selected.raw = JSON.stringify({undelivered:entries, ...extra});
    model.publishMessages();
  };
  return {model, held, update};
}

function heldDeleteView(model, translate = key => key) {
  const source = readFileSync(new URL('../entry/src/main/ets/components/Conversation.ets',import.meta.url),'utf8');
  assert.match(source,/onClick\(\(\) => this\.confirmHeldDelete\(message\)\)\.id\('chat\.held\.cancel\.'/);
  const method = source.match(/^  private confirmHeldDelete\(message: ChatMessage\): void \{[\s\S]*?^  \}/m);
  assert.ok(method);
  const View = new Function('tr','return '+stripTypeScriptTypes('class HeldView { '+method[0]+' }'))(translate);
  const view = new View();
  view.model=model; view.sendCurrent=()=>true; view.revisionVisibilityRevision=1;
  view.getUIContext=()=>({showAlertDialog:dialog=>{view.dialog=dialog;}});
  return view;
}

test('held Agent native delete confirmation keeps the held message on dismissal and deletes only once on confirmation',async()=>{
  const {model,held,update}=heldFixture(), view=heldDeleteView(model), calls=[];
  const entry=held('op/1','failed','Might not have arrived. Retry checks the chat first.'); update([entry]);
  model.repository.call=async(path,method,_body,_id,admit)=>{ assert.ok(admit()); calls.push([path,method]); return {ok:true}; };
  model.refresh=async()=>{};
  const message=model.messages[0]; view.confirmHeldDelete(message);
  assert.equal(view.dialog.title,'agent_delete_held_title'); assert.equal(view.dialog.message,message.text);
  assert.equal(view.dialog.primaryButton.value,'cancel'); assert.equal(view.dialog.secondaryButton.value,'delete');
  assert.ok(view.dialog.primaryButton.defaultFocus);
  assert.equal(calls.length,0); await view.dialog.primaryButton.action();
  assert.deepEqual(model.selected.data().undelivered,[entry]); assert.equal(calls.length,0);
  view.confirmHeldDelete(model.messages[0]); await Promise.all([view.dialog.secondaryButton.action(),view.dialog.secondaryButton.action()]);
  assert.deepEqual(calls,[['/api/v1/sessions/my%20laptop/chat%2Fone/held/op%2F1','DELETE']]);
  assert.equal(model.messages.length,0);
});

test('held Agent native delete confirmation rechecks manager state, ownership and visible route',async()=>{
  for(const change of ['row','removed','reconnect','hidden-and-shown','route','read-only','model']) {
    const {model,held,update}=heldFixture(), view=heldDeleteView(model), entry=held('x');
    update([entry]); let deletes=0;
    model.repository.call=async()=>{deletes++;return {ok:true};};
    model.refresh=async()=>{};
    view.confirmHeldDelete(model.messages[0]);
    if(change==='row')update([held('x','failed','Not sent.')]);
    if(change==='removed')update([]);
    if(change==='reconnect')model.generation++;
    if(change==='hidden-and-shown')view.revisionVisibilityRevision++;
    if(change==='route')view.sendCurrent=()=>false;
    if(change==='read-only')update([entry],{read_only:true});
    if(change==='model')view.model=heldFixture().model;
    await view.dialog.secondaryButton.action(); assert.equal(deletes,0,change);
  }
});

for (const [field, changed] of Object.entries({
  text: 'Revised message', images: 2, computer: 'Other desk', at: '2026-11-01T12:00:00.000000002Z'
})) {
  test('held Agent open confirmation rejects a change only to ' + field, async () => {
    const {model, held, update} = heldFixture(), view = heldDeleteView(model), writes = [];
    const original = held('same', 'failed', 'Retry checks the chat first.', {images: 1, at: '2026-11-01T12:00:00.000000001Z'});
    model.queueRefresh = () => {};
    model.repository.call = async (_path, method) => { writes.push(method); return {ok: true}; };
    update([original]);
    const captured = model.messages[0];
    view.confirmHeldDelete(captured);
    update([{...original, [field]: changed}]);
    await view.dialog.secondaryButton.action();
    assert.deepEqual(writes, [], 'the old confirmation sent DELETE');
    await model.actOnHeld(captured, true);
    assert.deepEqual(writes, [], 'the old Retry sent POST');
    view.confirmHeldDelete(model.messages[0]);
    await view.dialog.secondaryButton.action();
    assert.deepEqual(writes, ['DELETE']);
  });
}

test('held Agent open confirmation describes image-only and mixed messages in the selected language', () => {
  for (const [locale, photos] of [['en_US','Photos'], ['zh_CN','照片'], ['ja_JP','写真']]) {
    const {model, held, update} = heldFixture();
    const file = new URL('../entry/src/main/resources/' + locale + '/element/string.json', import.meta.url);
    const strings = new Map(JSON.parse(readFileSync(file)).string.map(({name, value}) => [name, value]));
    const view = heldDeleteView(model, key => strings.get(key));
    for (const [text, expected] of [['', photos + ' · 2'], ['Keep these', 'Keep these\n' + photos + ' · 2']]) {
      update([held('pictures', 'waiting', 'Will send later.', {text, images: 2})]);
      view.confirmHeldDelete(model.messages[0]);
      assert.equal(view.dialog.message, expected, locale);
      assert.ok(view.dialog.primaryButton.defaultFocus);
    }
  }
});

test('held Agent accepted Retry survives content changes during the POST and on later reads', async () => {
  const {model, held} = heldFixture(), entered = deferred(), receipt = deferred(), posts = [];
  const original = held('same', 'failed', 'Retry checks the chat first.');
  let current = original;
  model.queueRefresh = () => {};
  model.repository.machines = async () => model.machines;
  model.repository.sessions = async () => [heldSession([current])];
  model.repository.call = async (path, method) => {
    if (method === 'POST') { posts.push(path); entered.resolve(); await receipt.promise; }
    return {ok: true};
  };
  await model.refresh();
  const retry = model.actOnHeld(model.messages[0], true);
  await entered.promise;
  current = {...original, text: 'Revised text'};
  await model.refresh();
  receipt.resolve(); await retry;
  assert.equal(model.heldRetryPending(model.messages[0]), true);
  current = {...current, images: 3, computer: 'Renamed desk', at: '2026-11-02T12:00:00Z'};
  await model.refresh();
  await model.actOnHeld(model.messages[0], true);
  assert.equal(posts.length, 1);
  assert.equal(model.canActOnHeld(model.messages[0], false), true);
  current = {...current, error: 'Retry sends it anyway.'};
  await model.refresh();
  assert.equal(model.canActOnHeld(model.messages[0], true), true);
});

test('held Agent rows show the exact manager line once and give way to the same transcript operation', () => {
  const {model, held, update} = heldFixture();
  const first = held('wire/one', 'waiting', 'Will check the chat when Desk is back.', {images:2});
  const second = held('two', 'failed', 'Not sent: Desk was away for 24 hours.');
  const third = held('three', 'failed', 'May have arrived. Check the chat first.');
  update([first, first, second, third, {...second,id:'persona',computer:''}]);
  assert.deepEqual(model.messages.map(m => [m.role,m.text,m.held?.line,m.localImageCount]),
    [first,second,third].map(h => ['user',h.text,h.error,h.images || 0]));
  model.publishTranscript([{id:'landed',role:'user',kind:'text',text:'Original words',client_msg_id:'two'}], false);
  assert.deepEqual(model.messages.map(m => m.held?.id || m.id), ['landed','wire/one','three']);
  update([]);
  assert.deepEqual(model.messages.map(m => m.id), ['landed']);
});

test('held Agent first receipt takes over the optimistic operation and retires its durable outbox copy', async () => {
  const {model, held, update} = heldFixture();
  let posted = 0;
  model.repository.send = async (_row, _text, id) => { posted++; return {delivered:'later',undelivered:held(id)}; };
  assert.equal(await model.send(), true);
  assert.equal(posted, 1);
  assert.deepEqual(model.messages.map(m => [m.text,m.held?.state,m.held?.line]), [['Original words','waiting','Will send when Desk is back.']]);
  assert.equal(model.draft, '');
  update([]);
  await model.restoreLocalDelivery();
  assert.deepEqual(model.messages, []);
  assert.equal(posted, 1);
});

test('held Agent late first receipt rechecks a cancellation already made on the other device', async () => {
  const {model, held} = heldFixture(), received = deferred(), mayReply = deferred();
  model.repository.send = async (_row, _text, id) => {
    received.resolve(); await mayReply.promise;
    return {delivered:'later', undelivered:held(id)};
  };
  model.repository.sessions = async () => [ResourceRow.from({machine_id:'my laptop', session_id:'chat/one', manager:'personal'}, 'session')];
  model.repository.machines = async () => model.machines;
  model.repository.call = async () => ({conversations:[]});
  const send = model.send(); await received.promise;
  await model.refresh(); // The latest list already omits the operation. The initial HTTP reply arrives later.
  mayReply.resolve(); assert.ok(await send);
  assert.equal(model.messages[0]?.held?.state,'waiting');
  for (let i = 0; i < 100 && model.messages.length; i++) await new Promise(done => setTimeout(done, 10));
  assert.deepEqual(model.messages, []);
  await model.restoreLocalDelivery(); assert.deepEqual(model.messages, []);
});

test('held Agent input distinguishes the offline computer, a stopped Agent and view-only sources', () => {
  const {model, update} = heldFixture();
  assert.equal(model.canSend(), true);
  assert.equal(model.agentInputNotice(), 'agent_computer_holds');
  model.features = [];
  assert.equal(model.canCompose(), false);
  assert.equal(model.agentInputNotice(), 'agent_computer_offline');
  model.connected = false;
  assert.equal(model.agentInputNotice(), '');
  assert.equal(model.canCompose(), true);
  assert.equal(model.canSend(), false);
  model.connected = true;
  model.features = ['agent_message_hold'];
  update([], {status:'interrupted', capabilities:{read:true,terminal:true}});
  assert.equal(model.canSend(), false);
  assert.equal(model.agentInputNotice(), 'agent_stopped');
  update([], {source_kind:'history',capabilities:{read:true}});
  assert.equal(model.canCompose(), false);
  assert.equal(model.agentInputNotice(), 'read_only');
  update([], {read_only:true});
  assert.equal(model.canSend(), false);
  model.selected.kind = 'conversation';
  update([]);
  assert.equal(model.agentInputNotice(), '');
  assert.equal(model.canCompose(), true);
});

test('held Agent actions revalidate the current state, suppress double taps and use the owning session', async () => {
  const {model,held,update} = heldFixture(), calls = [], gate = deferred();
  update([held('op/1'), held('op/2','failed','Could not confirm delivery.'), held('signed','failed','Sender signed out.',{delete_only:true})]);
  const [waiting, failed, signed] = model.messages;
  model.unresolvedWrites = [{path:'/api/v1/sessions/my%20laptop/chat%2Fone/held/op%2F2/retry'}];
  assert.equal(model.canActOnHeld(failed, true), false);
  assert.equal(model.canActOnHeld(failed, false), false);
  assert.equal(model.canActOnHeld(waiting, false), true);
  model.unresolvedWrites = [];
  model.repository.call = async (...args) => { calls.push(args); await gate.promise; return {ok:true}; };
  model.refresh = async () => {};
  await model.actOnHeld(waiting, true);
  await model.actOnHeld(signed, true);
  assert.equal(calls.length, 0);
  const first = model.actOnHeld(failed, true);
  await model.actOnHeld(failed, true);
  assert.equal(calls.length, 1);
  gate.resolve(); await first;
  assert.deepEqual(calls.map(c => c.slice(0,2)), [['/api/v1/sessions/my%20laptop/chat%2Fone/held/op%2F2/retry','POST']]);
  await model.actOnHeld(failed, true);
  assert.equal(calls.length, 1);
  await model.actOnHeld(signed, false);
  assert.deepEqual(calls.at(-1).slice(0,2), ['/api/v1/sessions/my%20laptop/chat%2Fone/held/signed','DELETE']);
  assert.ok(!model.messages.some(m => m.held?.id === 'signed'));
  update([], {read_only:true});
  await model.actOnHeld(waiting, false);
  assert.equal(calls.length, 2);
});

test('held Agent changes made on another device reach both chats, including a push during an older snapshot', async () => {
  const a = heldFixture(), b = heldFixture(), clients = [a.model,b.model];
  const row = entries => ResourceRow.from({machine_id:'my laptop',session_id:'chat/one',manager:'personal',undelivered:entries}, 'session');
  const first = a.held('cross','waiting','Waits behind the message above.');
  const expired = a.held('cross','failed','Not sent. Desk was away for 24 hours.');
  let records = [first];
  const reads = new Map();
  for (const model of clients) {
    reads.set(model, []);
    model.repository.sessions = async () => { const result = row(records); reads.get(model).push(result); return [result]; };
    model.repository.machines = async () => model.machines;
    model.repository.call = async () => ({conversations:[]});
    await model.refresh();
    assert.equal(model.messages[0]?.held?.line, first.error);
  }
  const stale = deferred(), entered = deferred(), readSessions = b.model.repository.sessions;
  b.model.repository.sessions = async () => {
    b.model.repository.sessions = readSessions;
    const previous = row(records); entered.resolve(); await stale.promise; return [previous];
  };
  const oldRefresh = b.model.refresh(); await entered.promise;
  records = [expired];
  const push = () => clients.forEach(model => model.event(JSON.stringify({type:'session',session:records.length ? row(records).data() : row([]).data()})));
  const eventually = async check => { for(let i=0;i<100 && !check();i++) await new Promise(done => setTimeout(done,10)); assert.ok(check()); };
  push(); await eventually(() => a.model.messages[0]?.held?.line === expired.error);
  stale.resolve(); await oldRefresh;
  await eventually(() => b.model.messages[0]?.held?.line === expired.error);
  records = []; push(); await eventually(() => clients.every(model => !model.messages.length));
  assert.ok(clients.every(model => reads.get(model).length >= 3));
});

test('held Agent retries are explicit again after the manager checks the chat and still cannot confirm', async () => {
  const {model, held, update} = heldFixture(), calls = [];
  const uncertain = held('cross','failed','May have arrived. Check the chat first.');
  const unconfirmed = held('cross','failed','Could not confirm it arrived. Retry to send it anyway, or delete it.');
  update([uncertain]); model.refresh = async () => {};
  model.repository.call = async (path, method, _body, _op, admission) => {
    assert.ok(admission()); calls.push([path,method]);
    if (calls.length === 1) update([unconfirmed]); // The authoritative push beats the earlier action reply.
    return {ok:true};
  };
  const stale = model.messages[0];
  await model.actOnHeld(stale, true);
  assert.equal(model.messages[0].held.line, unconfirmed.error);
  await model.actOnHeld(stale, true);
  assert.equal(calls.length, 1);
  await model.actOnHeld(model.messages[0], true);
  assert.equal(calls.length, 2);
  assert.ok(calls.every(([path,method]) => path.endsWith('/held/cross/retry') && method === 'POST'));
});

test('held Agent check-only acknowledgment preserves the manager reason through an older list and a failed refresh', async () => {
  const {model, held, update} = heldFixture(), posts = [], entered = deferred(), beforeAck = deferred();
  const uncertain = held('cross','failed','Might not have arrived. Retry checks the chat first.');
  const unconfirmed = held('cross','failed',"Couldn't confirm it arrived. Retry to send it anyway, or delete it.");
  const row = value => ResourceRow.from({machine_id:'my laptop',session_id:'chat/one',manager:'personal',undelivered:[value]}, 'session');
  update([uncertain]);
  model.repository.call = async (path, method) => { if (method === 'POST') { posts.push(path); return {ok:true}; } return {}; };
  model.repository.machines = async () => model.machines;
  model.repository.sessions = async () => { entered.resolve(); await beforeAck.promise; return [row(uncertain)]; };
  const oldRead = model.refresh(); await entered.promise;
  const original = model.messages[0];
  await model.actOnHeld(original, true);
  beforeAck.resolve(); await oldRead;
  let reads = 0;
  model.repository.sessions = async () => { reads++; throw new Error('unreachable'); };
  await model.refresh(); assert.ok(reads);
  assert.deepEqual([model.messages[0].held.state,model.messages[0].held.line], ['failed',uncertain.error]);
  assert.equal(model.selected.data().undelivered[0].error, uncertain.error);
  assert.equal(model.heldRetryPending(model.messages[0]),true);
  assert.equal(model.canActOnHeld(model.messages[0],true),false);
  assert.equal(model.canActOnHeld(model.messages[0],false),true);
  await model.actOnHeld(model.messages[0],true); assert.equal(posts.length,1);
  model.repository.sessions = async () => [row(unconfirmed)];
  await model.refresh();
  assert.equal(model.messages[0].held.line,unconfirmed.error);
  assert.equal(model.heldRetryPending(model.messages[0]),false);
  assert.equal(model.canActOnHeld(original,true),false);
  assert.equal(model.canActOnHeld(model.messages[0],true),true);
  assert.equal(posts.length,1);
  await model.actOnHeld(model.messages[0],true); assert.equal(posts.length,2);
});

function heldSession(entries, session = 'chat/one', manager = 'personal', extra = {}) {
  return ResourceRow.from({machine_id:'my laptop', session_id:session, manager, undelivered:entries, ...extra}, 'session');
}

test('held Agent successful stale snapshots cannot rearm the same check-only Retry', async () => {
  const {model, held} = heldFixture(), calls = [];
  const uncertain = held('a','failed','Might not have arrived. Retry checks the chat first.');
  const unconfirmed = {...uncertain, error:'Could not confirm it arrived. Retry to send anyway.'};
  const other = {...uncertain, id:'b'};
  let sessions = [heldSession([uncertain, other]), heldSession([uncertain], 'elsewhere')];
  model.queueRefresh = () => {};
  model.repository.sessions = async () => sessions;
  model.repository.machines = async () => model.machines;
  model.repository.call = async (path, method) => { if (method === 'POST') calls.push(path); return {ok:true}; };
  await model.refresh();
  for (const message of model.messages) await model.actOnHeld(message, true);
  const stale = model.messages[0];
  // The GET succeeds with a cached row. Another session even has the same operation ID.
  sessions = [heldSession([uncertain, {...other,error:'Retry can send this other message.'}]),
    heldSession([unconfirmed],'elsewhere')];
  await model.refresh();
  assert.equal(model.heldRetryPending(model.messages[0]), true);
  assert.equal(model.heldRetryPending(model.messages[1]), false);
  assert.equal(model.canActOnHeld(model.messages[0], false), true);
  await model.actOnHeld(stale, true);
  await model.actOnHeld(model.messages[0], true);
  assert.equal(calls.length, 2);
  // A complete snapshot obtained for a different manager cannot consume this manager's receipt.
  model.profile.manager = 'work';
  sessions = [heldSession([unconfirmed],'chat/one','work')];
  await model.refresh();
  assert.equal(model.heldRetryPending(model.messages[0]), false);
  model.profile.manager = 'personal';
  sessions = [heldSession([uncertain])];
  await model.refresh();
  assert.equal(model.heldRetryPending(model.messages[0]), true);
  sessions = [heldSession([unconfirmed])];
  await model.refresh();
  await model.actOnHeld(stale, true);
  assert.equal(calls.length, 2);
  await model.actOnHeld(model.messages[0], true);
  assert.equal(calls.length, 3);
  // The target session reports that this operation has left it.
  sessions = [heldSession([])]; await model.refresh();
  sessions = [heldSession([unconfirmed])]; await model.refresh();
  assert.equal(model.canActOnHeld(model.messages[0], true), true);
});

for (const action of ['retry','delete']) test('held Agent ' + action + ' receipt survives a whole computer missing from the Hub inventory', async () => {
  const {HubRepository} = await import('../entry/src/main/ets/models/HubRepository.ets');
  const {model,held} = heldFixture(), retry = action === 'retry', writes = [];
  const waiting = held('same','failed','Retry checks the chat first.');
  const row = {machine_id:'my laptop',session_id:'chat/one',manager:'personal',undelivered:[waiting]};
  const unrelated = {...row,machine_id:'other computer'};
  let rows = [row,unrelated], machines = ['my laptop','other computer'];
  const repo = new HubRepository({managerId:'personal',hubId:model.profile.id});
  repo.transport.request = async (path,method) => {
    if (method !== 'GET') { writes.push([method,path]); return JSON.stringify({ok:true}); }
    if (path === '/api/v1/sessions') return JSON.stringify({sessions:rows});
    if (path === '/api/v1/machines') return JSON.stringify({machines:machines.map(id=>({id,manager:'personal'}))});
    assert.equal(path,'/api/v1/assistant'); return JSON.stringify({conversations:[]});
  };
  model.repository = repo; model.queueRefresh = () => {};
  await model.refresh(); await model.actOnHeld(model.messages[0],retry);
  assert.equal(writes.length,1);
  // A successful GET describes another computer, which happens to reuse both session and operation IDs.
  rows = [unrelated]; machines = ['other computer']; await model.refresh();
  assert.equal(model.messages.length,0);
  const reopened = heldFixture().model; reopened.local = model.local; reopened.connected = false;
  await reopened.restoreSnapshot(reopened.connectionGeneration(),reopened.scope());
  reopened.repository = repo; reopened.queueRefresh = () => {}; reopened.connected = true;
  rows = [row,unrelated]; machines = ['my laptop','other computer']; await reopened.refresh();
  if (retry) {
    assert.equal(reopened.messages[0].held.line,waiting.error);
    assert.equal(reopened.canActOnHeld(reopened.messages[0],false),true);
    await reopened.actOnHeld(reopened.messages[0],true);
    assert.equal(writes.length,1,'the old check-only prompt cannot issue a second POST');
    rows = [{...row,undelivered:[{...waiting,error:'Could not confirm. Retry to send anyway.'}]}];
    await reopened.refresh(); await reopened.actOnHeld(reopened.messages[0],true); assert.equal(writes.length,2);
  } else {
    assert.deepEqual(reopened.messages,[],'the stale target row cannot bring back the confirmed deletion');
    assert.deepEqual(writes.map(([m])=>m),['DELETE']);
  }
  rows = [{...row,undelivered:[]}]; await reopened.refresh();
  const known = JSON.parse(await reopened.local.get('snapshot:'+reopened.scope()));
  assert.deepEqual(known.heldActions,[],'the target itself can acknowledge departure');
});

test('held Agent forgetting a pairing cleans only its pending receipts and snapshots', async () => {
  const {model,held,update} = heldFixture();
  const keeper = new HubProfile(model.profile.id+'-other'); keeper.manager = model.profile.manager;
  const forgotten = model.profile; model.profiles = [forgotten,keeper]; model.queueRefresh = () => {};
  model.repository.close = () => {}; model.repository.call = async () => ({ok:true});
  const scopes = [];
  for (const profile of [keeper,forgotten]) {
    model.profile = profile; scopes.push(model.scope());
    update([held('same','failed','Check first.')]); await model.actOnHeld(model.messages[0],true);
  }
  await model.forgetProfile(forgotten.id);
  assert.equal(await model.local.get('snapshot:'+scopes[1]),'');
  assert.ok(await model.local.get('snapshot:'+scopes[0]));
  assert.deepEqual(model.heldReceipts.map(receipt=>receipt.scope),[scopes[0]]);
  assert.deepEqual(Array.from(model.heldRevisions.keys()),[scopes[0]]);
});

test('held Agent confirmed receipt and deletion survive offline restart and an outstanding stale snapshot in their own scope', async () => {
  const {model, held} = heldFixture(), stale = deferred(), readStarted = deferred();
  const kept = held('keep'), deleted = held('remove'), incoming = [];
  model.queueRefresh = () => {};
  model.repository.sessions = async () => [heldSession([kept, deleted])];
  model.repository.machines = async () => model.machines;
  model.repository.call = async () => ({ok:true});
  model.repository.send = async (_row, _text, id) => { const receipt = held(id); incoming.push(receipt); return {delivered:'later',undelivered:receipt}; };
  await model.refresh();
  model.repository.sessions = async () => {
    const previous = [heldSession([kept, deleted])];
    readStarted.resolve(); await stale.promise; return previous;
  };
  const oldRead = model.refresh(); await readStarted.promise;
  const original = model.messages.find(m => m.held?.id === deleted.id);
  await model.actOnHeld(original, false);
  assert.ok(await model.send()); assert.equal(incoming.length,1);
  stale.resolve(); await oldRead;
  const restored = async (hub, manager) => {
    const other = heldFixture().model;
    other.local = model.local; other.profile = new HubProfile(hub); other.profile.manager = manager;
    other.selected = new ResourceRow('chat/one','Test','session'); other.selected.machine = 'my laptop';
    other.selected.session = 'chat/one'; other.sessions = []; other.connected = false;
    await other.restoreSnapshot(other.connectionGeneration(),other.scope());
    await other.restoreLocalDelivery(); other.publishMessages();
    return other.messages.map(m => [m.held?.id,m.held?.line]);
  };
  assert.deepEqual(await restored('synthetic-hub','personal'), [kept,incoming[0]].map(m=>[m.id,m.error]));
  assert.deepEqual(await restored('synthetic-hub','other'), []);
  assert.deepEqual(await restored('other-hub','personal'), []);
});

test('held Agent offline restart retains a check-only lock and a confirmed deletion through a cached Hub read', async () => {
  const {model,held} = heldFixture(), uncertain = held('check','failed','Retry checks the chat first.'), removed = held('gone');
  let list = [heldSession([uncertain,removed])], posts = 0;
  const connect = client => {
    client.queueRefresh = () => {};
    client.repository.sessions = async () => list;
    client.repository.machines = async () => client.machines;
    client.repository.call = async (_path,method) => { if (method === 'POST') posts++; return {ok:true}; };
  };
  connect(model); await model.refresh();
  await model.actOnHeld(model.messages[0],true);
  await model.actOnHeld(model.messages[1],false);
  const reopened = heldFixture().model; reopened.local = model.local; reopened.connected = false;
  await reopened.restoreSnapshot(reopened.connectionGeneration(),reopened.scope()); reopened.publishMessages();
  assert.deepEqual(reopened.messages.map(m=>m.held?.id),[uncertain.id]);
  assert.equal(reopened.heldRetryPending(reopened.messages[0]),true);
  connect(reopened); reopened.connected = true; await reopened.refresh();
  assert.deepEqual(reopened.messages.map(m=>m.held?.id),[uncertain.id]);
  await reopened.actOnHeld(reopened.messages[0],true); assert.equal(posts,1);
  list = [heldSession([{...uncertain,error:'Could not confirm. Retry to send anyway.'}])];
  await reopened.refresh();
  assert.equal(reopened.heldRetryPending(reopened.messages[0]),false);
  await reopened.actOnHeld(reopened.messages[0],true); assert.equal(posts,2);
  assert.ok(!JSON.stringify(await reopened.local.get('snapshot:'+reopened.scope())).includes(removed.id));
});

test('held Agent replacement pairing rejects a late response from the previous pairing', async () => {
  const {model,held} = heldFixture(), waiting = deferred(), entered = deferred();
  model.queueRefresh = () => {};
  model.repository.sessions = async () => [heldSession([])];
  model.repository.machines = async () => model.machines;
  model.repository.call = async () => ({ok:true});
  model.repository.send = async (_row,_text,id) => { entered.resolve(); await waiting.promise; return {delivered:'later',undelivered:held(id)}; };
  await model.refresh();
  const request = model.send(); await entered.promise;
  model.retirePairing(model.profile.id); ++model.generation;
  waiting.resolve(); await request;
  const saved = JSON.parse(await model.local.get('snapshot:'+model.scope()));
  assert.deepEqual(JSON.parse(saved.sessions[0].raw).undelivered,[]);
});

test('held Agent snapshots reject an incomplete sessions envelope without releasing the current row', async () => {
  const {HubRepository} = await import('../entry/src/main/ets/models/HubRepository.ets');
  const {model, held, update} = heldFixture();
  const repo = new HubRepository({managerId:'personal',hubId:model.profile.id});
  let envelope = {sessions:[{machine_id:'my laptop',session_id:'chat/one',manager:'personal',undelivered:[held('x','failed','Check first.')]}]};
  repo.call = async (path) => path === '/api/v1/sessions' ? envelope : {ok:true};
  repo.machines = async () => model.machines;
  model.queueRefresh = () => {}; model.repository = repo;
  update([]); await model.refresh(); await model.actOnHeld(model.messages[0],true);
  const target = {machine_id:'my laptop',session_id:'chat/one',manager:'personal'};
  for (const malformed of [{},{sessions:null},{sessions:{}},{sessions:[],has_more:true},
    {sessions:[null]},{sessions:[{...target,undelivered:{}}]},{sessions:[{...target,undelivered:[{}]}]}]) {
    envelope = malformed; await model.refresh();
    assert.equal(model.messages[0]?.held?.id,'x'); assert.equal(model.heldRetryPending(model.messages[0]),true);
  }
  envelope = {sessions:[]}; await model.refresh(); assert.equal(model.messages.length,0);
});

test('held Agent confirmed state waits for a stale disk write and stays in the dispatching manager after navigation', async () => {
  const {model, held} = heldFixture(), disk = deferred(), started = deferred(), reply = deferred(), posted = deferred();
  const queued = held('delete'), keeper = held('keep');
  model.queueRefresh = () => {};
  model.repository.sessions = async () => [heldSession([queued,keeper])];
  model.repository.machines = async () => model.machines;
  model.repository.call = async () => ({ok:true});
  await model.refresh();
  const scope = model.scope(), key = 'snapshot:' + scope, put = model.local.put.bind(model.local);
  let delay = true;
  model.local.put = async (k,v,...rest) => { if (k === key && delay) { delay = false; started.resolve(); await disk.promise; } await put(k,v,...rest); };
  const writing = model.refresh(); await started.promise;
  const removing = model.actOnHeld(model.messages[0],false);
  let wire;
  model.repository.send = async (_row,_text,id) => { wire = held(id); posted.resolve(); await reply.promise; return {delivered:'later',undelivered:wire}; };
  const sending = model.send(); await posted.promise;
  model.profile.manager = 'work'; model.generation++;
  disk.resolve(); await writing; await removing;
  reply.resolve(); assert.ok(await sending);
  const cached = JSON.parse(await model.local.get(key));
  assert.deepEqual(JSON.parse(cached.sessions[0].raw).undelivered.map(m=>m.id),[keeper.id,wire.id]);
  assert.equal(await model.local.get('snapshot:'+model.scope()),'');
});

test('held Agent retired detail receipts survive the replacement workspace view reading the same scope', async () => {
  const {model:owner,held} = heldFixture(), {model:detail,update} = heldFixture();
  detail.directoryParent = owner; detail.local = owner.local;
  const waiting = held('remove'), uncertain = held('check','failed','Retry checks first.');
  const snapshot = () => [heldSession([waiting,uncertain])];
  owner.queueRefresh = detail.queueRefresh = () => {};
  owner.repository.sessions = async () => snapshot();
  owner.repository.machines = async () => owner.machines;
  owner.repository.call = async () => ({ok:true});
  await owner.refresh(); update([waiting,uncertain]);
  const sent = deferred(), reply = deferred(), started = deferred(), old = deferred(); let actions = 0;
  detail.repository.call = async (_p,_m,_b,_i,admit) => {
    assert.ok(admit()); if (++actions === 2) sent.resolve(); await reply.promise; return {ok:true};
  };
  const deleted = detail.actOnHeld(detail.messages[0],false), checked = detail.actOnHeld(detail.messages[1],true);
  await sent.promise; detail.retired = true; detail.generation++;
  owner.repository.sessions = async () => { started.resolve(); await old.promise; return snapshot(); };
  const reading = owner.refresh(); await started.promise;
  reply.resolve(); await Promise.all([deleted,checked]); old.resolve(); await reading;
  const restored = heldFixture().model; restored.local = owner.local; restored.connected = false;
  await restored.restoreSnapshot(restored.connectionGeneration(),restored.scope()); restored.publishMessages();
  assert.deepEqual(restored.messages.map(m=>m.held?.id),[uncertain.id]);
  assert.equal(restored.heldRetryPending(restored.messages[0]),true);
  await owner.refresh();
  assert.deepEqual(owner.messages.map(m=>m.held?.id),[uncertain.id]);
  assert.equal(owner.canActOnHeld(owner.messages[0],true),false);
});

test('held Agent manager actions on an offline runtime placeholder are independent of composing a new message', async () => {
  const {model, held} = heldFixture(), calls = [];
  const waiting = held('queued'), failed = held('expired','failed','Not sent. Desk was away for 24 hours.');
  const signedOut = {...failed,id:'delete-only',delete_only:true};
  const archive = {source_kind:'runtime', agent:'dsh', state:'offline', status:'offline', capabilities:{read:true}};
  const place = heldSession([waiting, failed, signedOut], 'chat/one', 'personal', archive);
  model.selected = place; model.sessions = [place]; model.publishMessages();
  model.queueRefresh = () => {};
  model.repository.call = async (path, method, _body, _id, admit) => { assert.ok(admit()); calls.push([path,method]); return {ok:true}; };
  const [queued, expired, signed] = model.messages, view = heldDeleteView(model);
  assert.equal(model.canCompose(), false); assert.equal(model.canSend(), false);
  assert.equal(model.offersHeldAction(queued, true), false);
  assert.equal(model.offersHeldAction(signed, true), false);
  assert.equal(model.canActOnHeld(expired, true), true);
  assert.equal(model.canActOnHeld(queued, false), true);
  assert.equal(model.canActOnHeld(signed, false), true);
  await model.actOnHeld(expired, true);
  view.confirmHeldDelete(queued); await view.dialog.primaryButton.action();
  assert.equal(calls.length, 1); assert.ok(model.messages.some(m => m.held?.id === waiting.id));
  view.confirmHeldDelete(queued); await view.dialog.secondaryButton.action();
  assert.deepEqual(calls, [
    ['/api/v1/sessions/my%20laptop/chat%2Fone/held/expired/retry','POST'],
    ['/api/v1/sessions/my%20laptop/chat%2Fone/held/queued','DELETE'],
  ]);
  for (const override of [{read_only:true},{deleted:true},{source_kind:'identity_conflict'},{source_kind:'history'},
    {capabilities:{read:false}}, {manager:'other'}]) {
    const row = heldSession([failed], 'chat/one', 'personal', {...archive,...override});
    model.sessions = [row]; model.selected = row; model.publishMessages();
    assert.equal(model.offersHeldAction(model.messages[0], false), false, JSON.stringify(override));
  }
});

test('held Agent cold manager placeholder allows cancel while its runtime identity is unavailable', async () => {
  const {model, held} = heldFixture();
  const row = heldSession([held('cold')], 'chat/one', 'personal', {status:'offline',state:'offline'});
  model.sessions = [row]; model.selected = row; model.publishMessages();
  model.queueRefresh = () => {};
  let deletes = 0;
  model.repository.call = async (_path, method, _body, _id, admit) => {
    assert.equal(method,'DELETE'); assert.ok(admit()); deletes++; return {ok:true};
  };
  assert.equal(model.canCompose(), false);
  assert.equal(model.canActOnHeld(model.messages[0], false), true);
  const view = heldDeleteView(model); view.confirmHeldDelete(model.messages[0]); await view.dialog.secondaryButton.action();
  assert.equal(deletes, 1); assert.equal(model.messages.length, 0);
  assert.equal(model.canCompose(), false);
});

test('held Agent actions cannot cross a chat, manager, denied route or capability change', async () => {
  for (const change of ['chat','manager','route','read-only','stopped','disconnected']) {
    const {model,held,update} = heldFixture();
    update([held('x','failed','Not delivered.')]); const message = model.messages[0];
    let posts = 0, route = true;
    model.repository.call = async (_path, _method, _body, _id, admit) => {
      if (change === 'chat') model.selected = new ResourceRow('other','Other','session');
      if (change === 'manager') model.profile.manager = 'other';
      if (change === 'route') route = false;
      if (change === 'read-only') update([held('x','failed','Not delivered.')],{read_only:true});
      if (change === 'stopped') update([held('x','failed','Not delivered.')],{status:'offline',capabilities:{read:true,terminal:true}});
      if (change === 'disconnected') model.connected = false;
      if (!admit()) throw new Error('request_updated');
      posts++; return {ok:true};
    };
    model.refresh = async () => {};
    await model.actOnHeld(message, true, () => route);
    assert.equal(posts, 0, change);
  }
});

test('held Agent retry receipts still guard the owning row when its live connection or view closes after dispatch', async () => {
  for (const retirement of ['socket','chat']) {
    const {model,held,update} = heldFixture();
    update([held('x','failed','Might not have arrived. Retry checks the chat first.')]);
    const owner = model.selected, message = model.messages[0];
    let posts = 0;
    model.repository.call = async (_path,_method,_body,_operation,admit) => {
      assert.ok(admit()); posts++;
      if (retirement === 'socket') model.connected = false;
      else model.selected = new ResourceRow('elsewhere','Another chat','session');
      return {ok:true};
    };
    model.refresh = async () => {};
    await model.actOnHeld(message,true);
    model.connected = true; model.selected = owner; model.publishMessages();
    assert.equal(model.messages[0].held.line,message.held.line);
    assert.equal(model.canActOnHeld(model.messages[0],true),false,retirement);
    await model.actOnHeld(model.messages[0],true); assert.equal(posts,1);
  }
});


test('an acknowledged message stays done when refreshing its transcript fails',async()=>{
  const {model,writes}=fixture(); model.loadChat=async()=>{throw new Error('read failed');};
  assert.equal(await model.send(),true); assert.equal(writes.length,1);
  assert.equal(model.operationState,'done'); assert.equal(model.draft,''); assert.equal(model.sourceContext,'');
});
test('a failed dispatched send preserves the exact draft and records an uncertain result',async()=>{
  const {model}=fixture(); model.repository.send=async()=>{throw new HttpFailure(0,true);};
  assert.equal(await model.send(),false); assert.equal(model.operationState,'unknown');
  assert.equal(model.draft,'exact draft '); assert.equal(model.sourceContext,contextReference);
});
test('a late acknowledged send cannot clear a newly selected conversation draft',async()=>{
  const {model}=fixture(), wait=deferred(), admitted=deferred(); let dispatched;
  model.repository.send=async row=>{dispatched=row;admitted.resolve();await wait.promise;};
  const sending=model.send(); await admitted.promise; const original=model.selected;
  model.selected=new ResourceRow('chat-two','Other','session'); model.draft='new conversation draft'; model.sourceContext='new source'; model.operationState='';
  wait.resolve(); assert.equal(await sending,true); assert.equal(dispatched,original);
  assert.equal(model.draft,'new conversation draft'); assert.equal(model.sourceContext,'new source'); assert.equal(model.operationState,'');
});
test('an acknowledged approval cannot reopen after a follow-up read fails',async()=>{
  const {model}=fixture(), request=decision(model); let count=0;
  model.repository.resolve=async()=>{count++;return {};}; model.loadChat=async()=>{throw new Error('read failed');};
  await model.answer('approve',{},request); await model.answer('approve',{},request);
  assert.equal(count,1); assert.equal(request.status,'resolved'); assert.equal(model.operationState,'done');
});
test('an accepted pending approval stays disabled until a receipt is known',async()=>{
  const {model}=fixture(), request=decision(model); let count=0;
  model.repository.resolve=async()=>{count++;throw new HttpFailure(202,true,true);};
  await model.answer('approve',{},request); await model.answer('approve',{},request);
  assert.equal(count,1); assert.equal(request.status,'pending'); assert.equal(model.operationState,'pending');
});
test('a stale form cannot submit values to a replacement request',async()=>{
  const {model}=fixture(), previous=decision(model); decision(model,'request-two'); let count=0;
  model.repository.resolve=async()=>{count++;return {};};
  await model.answer('approve',{secret:'synthetic'},previous);
  assert.equal(count,0); assert.equal(model.error,'request_updated'); assert.equal(model.decision.status,'open');
});
test('a late approval or interrupt failure cannot overwrite another conversation state',async()=>{
  for (const action of ['answer','stop']) {
    const {model}=fixture(), request=decision(model), wait=deferred();
    if (action === 'stop') model.selected.state = 'working';
    model.repository.resolve=()=>wait.promise; model.repository.stop=()=>wait.promise;
    const result=action==='answer'?model.answer('approve',{},request):model.stop();
    model.selected=new ResourceRow('chat-two','Other','session'); model.error=''; model.operationState='';
    wait.reject(new HttpFailure(503,true)); await result;
    assert.equal(model.error,''); assert.equal(model.operationState,'');
  }
});
test('a delayed main-chat refresh cannot create a conversation on a newly selected owner',async()=>{
 const {model}=fixture(),wait=deferred();let count=0;model.refresh=()=>wait.promise;model.repository.newConversation=async()=>{count++;return new ResourceRow('new','','conversation');};
 const opening=model.openMain();model.profile=new HubProfile('different-hub');wait.resolve();
 await assert.rejects(opening,/request_updated/);assert.equal(count,0);
});
test('a newly created conversation cannot select itself after its owner is changed',async()=>{
 const {model}=fixture(),wait=deferred();let selected=0;model.repository.newConversation=()=>wait.promise;model.select=async()=>{selected++;};
 const opening=model.newConversation();model.profile=new HubProfile('different-hub');wait.resolve(new ResourceRow('new','','conversation'));
 await assert.rejects(opening,/request_updated/);assert.equal(selected,0);
});
test('forgetting the active Hub clears content and retires writes before removing its credential',async()=>{
 const {model}=fixture(),profile=model.profile,events=[],wait=deferred();model.profiles=[profile];model.messages=[{text:'synthetic private message'}];model.sessions=[model.selected];
 model.repository.close=()=>events.push('closed');model.local.purgeHub=async id=>{events.push('purge:'+id);await wait.promise;};model.vault={async remove(id){events.push('credential:'+id);}};
 const forgetting=model.forgetProfile(profile.id);
 assert.equal(model.profile.id,'');assert.equal(model.draft,'');assert.equal(model.messages.length,0);assert.equal(model.sessions.length,0);await new Promise(resolve=>setImmediate(resolve));assert.ok(events.includes('purge:'+profile.id));
 await assert.rejects(model.selectProfile(profile),/pending_action/);assert.ok(!events.some(event=>event.startsWith('credential:')));
 wait.resolve();await forgetting;assert.ok(events.includes('credential:'+profile.id));assert.deepEqual(model.profiles,[]);
});
test('an approval submits an open immutable request snapshot while its visible form stays pending',async()=>{
 const {model}=fixture(),request=decision(model),wait=deferred();let sent;
 model.repository.resolve=async captured=>{sent=captured;assert.equal(request.status,'pending');assert.equal(captured.status,'open');await wait.promise;return {};};
 const approving=model.answer('approve',{},request);assert.notEqual(sent,request);assert.equal(sent.raw,request.raw);wait.resolve();await approving;assert.equal(request.status,'resolved');
});
test('background closes its captured transport even if saving the draft fails',async()=>{
 const {model}=fixture();let closed=0;model.repository.close=()=>closed++;model.persistDraft=async()=>{throw new Error('storage failed');};
 await assert.rejects(model.background(),/storage failed/);assert.equal(closed,1);
});
test('a delayed background draft save cannot close a replacement foreground transport',async()=>{
 const {model}=fixture(),wait=deferred();let oldClosed=0,newClosed=0;model.repository.close=()=>oldClosed++;model.persistDraft=()=>wait.promise;
 const background=model.background();model.repository={close:()=>newClosed++};wait.resolve();await background;
 assert.equal(oldClosed,1);assert.equal(newClosed,0);
});
test('an earlier credential admission cannot restore a Hub after a later forget',async()=>{
 const {model}=fixture(),profile=model.profile,wait=deferred(),events=[];model.profiles=[profile];model.repository.close=()=>{};
 model.vault={async get(){return null;},async put(){events.push('put');await wait.promise;},async remove(){events.push('remove');}};
 const pairing=model.useEndpoint({hubId:profile.id,baseUrl:'https://example.test',token:'synthetic',pin:'test',managerId:'personal'},'Synthetic');
 for(let i=0;i<20&&!events.length;i++)await new Promise(resolve=>setTimeout(resolve,0));
 const forgetting=model.forgetProfile(profile.id);wait.resolve();await assert.rejects(pairing,/request_updated/);await forgetting;
 assert.deepEqual(events,['put','remove']);assert.equal(model.profile.id,'');assert.deepEqual(model.profiles,[]);
});
test('a redeem response returning after forget cannot write credentials or restore the profile',async()=>{
 const {model}=fixture(),profile=model.profile,wait=deferred();model.profiles=[profile];model.repository.close=()=>{};let admitted=0;model.vault={async get(){return null;},async remove(){},async put(){admitted++;}};
 const pairing=model.pair({redeem:()=>wait.promise},{hubId:profile.id,name:'Synthetic'});await model.forgetProfile(profile.id);
 wait.resolve({hubId:profile.id,baseUrl:'https://example.test',token:'synthetic',pin:'test',managerId:'personal'});await assert.rejects(pairing,/request_updated/);
 assert.equal(admitted,0);assert.equal(model.profile.id,'');assert.deepEqual(model.profiles,[]);
});
test('a warm notification tap remains queued while reconnecting and opens only after the catalog refresh',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.connected=false;const row=model.selected;model.sessions=[row];let attempts=0;
 model.selectProfile=async()=>{attempts++;};model.refresh=async()=>{};
 await model.openNotification({profile:profile.id,manager:'personal',machine:row.machine,session:row.session});assert.equal(attempts,1);assert.equal(model.notificationTarget,'');
 model.connected=true;await model.foregrounded();assert.equal(JSON.parse(model.notificationTarget).id,row.id);
});
test('a notification tap queued in background is invalidated when its Hub is forgotten',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.repository.close=()=>{};model.refresh=async()=>{};model.selectProfile=async()=>{};
 await model.background();await model.openNotification({profile:profile.id,manager:'personal',machine:'machine-one',session:'chat-one'});await model.forgetProfile(profile.id);
 model.connected=true;await model.foregrounded();assert.equal(model.notificationTarget,'');
});
test('notification cleanup failures cannot skip credential removal or profile deletion',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.repository.close=()=>{};let removed=0;model.vault={async remove(){removed++;}};
 const {nativeNotifications}=await import('client-boundary:../platform/NativeNotifications');const clear=nativeNotifications.clear;nativeNotifications.clear=async()=>{throw new Error('service unavailable');};
 try {await assert.rejects(model.forgetProfile(profile.id),/cleanup_incomplete/);assert.equal(removed,1);assert.deepEqual(model.profiles,[]);assert.equal(model.error,'cleanup_incomplete');}
 finally {nativeNotifications.clear=clear;}
});

test('failed erasure retains a durable cleanup identity that retries after restart without reconnecting',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.repository.close=()=>{};let attempts=0;model.vault={async remove(){attempts++;throw Error('asset unavailable');}};
 await assert.rejects(model.forgetProfile(profile.id),/cleanup_incomplete/);assert.deepEqual(model.profiles,[]);assert.equal(model.pendingCleanupCount,1);assert.deepEqual(JSON.parse(await model.local.get('hub-cleanup')),[[profile.id,profile.id]]);
 const restarted=new ClientModel();restarted.local=model.local;let erased=0;restarted.vault={async remove(id){assert.equal(id,profile.id);erased++;}};await restarted.restoreCleanup();
 await assert.rejects(restarted.selectProfile(profile),/pending_action/);await assert.rejects(restarted.useEndpoint({hubId:profile.id},'again'),/pending_action/);await restarted.retryCleanup();assert.equal(erased,1);assert.equal(attempts,1);assert.equal(restarted.pendingCleanupCount,0);assert.deepEqual(JSON.parse(await restarted.local.get('hub-cleanup')),[]);
});

test('a cleanup journal failure retires the active context before any physical erasure',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.repository.close=()=>{};let deleted=0,purged=0;model.vault={async remove(){deleted++;}};model.local.purgeHub=async()=>{purged++;};const put=model.local.put.bind(model.local);model.local.put=async(key,value)=>{if(key==='hub-cleanup')throw Error('disk full');return put(key,value);};
 await assert.rejects(model.forgetProfile(profile.id),/cleanup_incomplete/);assert.equal(model.connected,false);assert.equal(model.profile.id,'');assert.equal(deleted,0);assert.equal(purged,0);assert.equal(model.profiles[0].id,profile.id);await assert.rejects(model.selectProfile(profile),/pending_action/);
 model.local.put=put;await model.retryCleanup();assert.equal(deleted,1);assert.equal(purged,1);assert.equal(model.pendingCleanupCount,0);
});

test('retrying another Hub cleanup does not stop the active Hub SSH lease or connection',async()=>{
 const {model}=fixture(),active=model.profile;model.profiles=[active];let stopped=0,closed=0;model.ssh.stop=()=>{stopped++;};model.repository.close=()=>{closed++;};model.vault={async remove(){}};model.cleanupProfiles.set('forgotten-hub','forgotten-identity');
 await model.retryCleanup();assert.equal(stopped,0);assert.equal(closed,0);assert.equal(model.profile,active);assert.equal(model.connected,true);assert.equal(model.pendingCleanupCount,0);
});

test('language commits reannounce capabilities on all live owners and a failed commit does not',async()=>{
 const {model}=fixture(),child=new ClientModel(),hello=[];model.views.set('child',child);child.connected=true;child.detached=true;model.repository.transport={hello:features=>hello.push(['root',features])};child.repository={transport:{hello:features=>hello.push(['child',features])}};
 await model.setLanguage('ja-JP');assert.deepEqual(hello.map(row=>row[0]),['root','child']);assert.equal(child.language,'ja-JP');hello.length=0;model.local.put=async()=>{throw Error('disk full');};await model.setLanguage('en-US');assert.deepEqual(hello,[]);assert.equal(child.language,'ja-JP');
});

test('follow-system retains this launch language and rollback restores it until cold start',async()=>{
 const {model}=fixture();globalThis.clientLanguageChanges=[];await model.setLanguage('en-US');await model.setLanguage('default');assert.equal(model.language,'default');assert.equal(clientLocale('zh-CN'),'en');
 model.local.put=async()=>{throw Error('disk full');};await model.setLanguage('ja-JP');assert.equal(model.language,'default');assert.equal(clientLocale('zh-CN'),'en');assert.deepEqual(globalThis.clientLanguageChanges,['en-US','default','ja-JP','en-US','default']);delete globalThis.clientLanguageChanges;
 setClientLanguage('default');assert.equal(clientLocale('zh-CN'),'zh-Hans','a new cold launch can apply the saved system preference');
});
test('prepared attachment sends use their captured payload exactly once and clear only the matching draft',async()=>{
 const {model,writes}=fixture(),images=[{mime:'image/png',data:'synthetic-base64'}],prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'prepared context and file path',images};
 assert.equal(await model.sendPrepared(prepared,model.draft,model.sourceContext),true);assert.equal(writes.length,1);assert.equal(writes[0][1],prepared.text);assert.deepEqual(writes[0][4],images);assert.equal(model.draft,'');
});
test('a prepared send is refused after owner, transport, or source draft changes',async()=>{
 for(const change of ['owner','repository','draft']){
  const {model,writes}=fixture(),draft=model.draft,context=model.sourceContext,prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'prepared',images:[]};
  if(change==='owner')model.profile=new HubProfile('another-hub');else if(change==='repository')model.repository={send:async()=>writes.push('wrong')};else model.draft='changed';
  assert.equal(await model.sendPrepared(prepared,draft,context),false);assert.equal(writes.length,0);
 }
});
test('a Persona notification keeps its conversation destination when the same runtime session is also in the fleet',async()=>{
 const {model}=fixture(),profile=model.profile;model.profiles=[profile];model.sessions=[model.selected];const conversation=new ResourceRow(model.selected.id,'Persona','conversation');conversation.machine=model.selected.machine;conversation.session=model.selected.session;model.conversations=[conversation];model.refresh=async()=>{};
 await model.openNotification({profile:profile.id,manager:'personal',machine:conversation.machine,session:conversation.session});assert.equal(JSON.parse(model.notificationTarget).kind,'conversation');
});
test('an owner detail context uses isolated transports and drafts without persisting the workspace manager',async()=>{
 const {model}=fixture(),row=new ResourceRow('owned-chat','Other manager','conversation');row.manager='other';let registryWrites=0;model.local.put=async key=>{if(key==='profiles')registryWrites++;};
 const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;await this.saveProfiles();};
 try {
  const view=await model.viewFor(row);assert.notEqual(view,model);assert.equal(view.profile.manager,'other');assert.equal(model.profile.manager,'personal');assert.equal(view.local,model.local);assert.equal(view.detached,true);assert.equal(registryWrites,0);
  view.draft='other-manager draft';assert.equal(model.draft,'exact draft ');assert.equal(await model.viewFor(row),view);
 } finally {ClientModel.prototype.selectProfile=select;}
});
test('a pending owner context cannot be returned after the workspace changes',async()=>{
 const {model}=fixture(),row=new ResourceRow('owned-chat','','conversation'),wait=deferred();row.manager='other';const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;await wait.promise;this.connected=true;};
 try {const opening=model.viewFor(row);model.profile=new HubProfile('different');wait.resolve();await assert.rejects(opening,/request_updated/);}
 finally {ClientModel.prototype.selectProfile=select;model.retireViews();}
});
test('forgetting a Hub retires its detail contexts before purging their data',async()=>{
 const {model}=fixture(),row=new ResourceRow('owned-chat','','conversation');row.manager='other';const select=ClientModel.prototype.selectProfile;model.profiles=[model.profile];model.repository.close=()=>{};
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;};
 try {const view=await model.viewFor(row);let closed=0;view.repository={close(){closed++;}};const old=view.connectionGeneration(),scope=view.scope();await model.forgetProfile(model.profile.id);assert.equal(view.owns(old,scope),false);assert.equal(closed,1);assert.equal(model.views.size,0);ClientModel.prototype.selectProfile=select;await assert.rejects(view.selectProfile(view.profile),/pending_action/);}
 finally {ClientModel.prototype.selectProfile=select;}
});
test('resource handoff keeps its owner context and does not change the workspace manager',async()=>{
 const {model}=fixture(),row=new ResourceRow('todo-other','Other todo','todo');row.manager='other';const view=new ClientModel(),chat=new ResourceRow('owner-main','Owner main','conversation');view.profile=new HubProfile('synthetic-hub');view.profile.manager='other';view.selected=chat;view.openMain=async()=>chat;view.persistDraft=async()=>{};model.viewFor=async()=>view;
 assert.equal(await model.handoff(row),chat);assert.equal(model.profile.manager,'personal');assert.equal(model.sourceContext,contextReference);assert.equal(JSON.parse(view.sourceContext).contexts[0].manager,'other');
});
test('main chat selection that is cancelled by an owner switch cannot complete navigation',async()=>{
 const {model}=fixture(),wait=deferred(),main=new ResourceRow('main','Main','conversation');main.manager='personal';model.mainId=main.id;model.conversations=[main];model.refresh=async()=>{};model.select=async()=>{await wait.promise;};
 const opening=model.openMain();model.profile=new HubProfile('new-owner');wait.resolve();await assert.rejects(opening,/request_updated/);
});
test('handoff cannot append source context after its main selection is replaced',async()=>{
 for(const change of ['scope','target']){
  const {model}=fixture(),wait=deferred(),main=new ResourceRow('main','Main','conversation'),source=new ResourceRow('todo','Todo','todo');source.manager='personal';main.manager='personal';model.selected=main;model.openMain=()=>wait.promise;let persisted=0;model.persistDraft=async()=>{persisted++;};
  const handing=model.handoff(source);await new Promise(resolve=>setImmediate(resolve));
  if(change==='scope')model.profile=new HubProfile('other-owner');else model.selected=new ResourceRow('another-chat','','conversation');model.sourceContext='new source';model.draft='new draft';wait.resolve(main);
  await assert.rejects(handing,/request_updated/);assert.equal(model.sourceContext,'new source');assert.equal(model.draft,'new draft');assert.equal(persisted,0);
 }
});
test('overlapping foreground restores do not reuse a manager connection task from an earlier root generation',async()=>{
 const {model}=fixture(),target=new ResourceRow('owned','','conversation');target.manager='other';const gates=[deferred(),deferred()];let calls=0;const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;await gates[calls++].promise;this.connected=true;};
 try {
  const previous=model.viewFor(target);model.generation++;const latest=model.viewFor(target);assert.equal(calls,2);
  gates[0].resolve();await assert.rejects(previous,/request_updated/);gates[1].resolve();const view=await latest;assert.equal(view.profile.manager,'other');assert.equal(view.connected,true);assert.equal(model.profile.manager,'personal');
 } finally {ClientModel.prototype.selectProfile=select;model.retireViews();}
});
test('an ACK clears its persisted draft even after leaving the destination, while another draft stays intact',async()=>{
 const {model}=fixture(),gate=deferred(),admitted=deferred(),original=model.selected;model.repository.send=async()=>{admitted.resolve();await gate.promise;};const target=model.target();
 const sending=model.send();await admitted.promise;model.selected=new ResourceRow('other','Other','session');model.selected.manager='personal';model.updateDraft('Other draft');await model.persistDraft();gate.resolve();assert.equal(await sending,true);
 assert.equal(model.draft,'Other draft');assert.equal(JSON.parse(await model.local.get('draft:'+target)).text,'');await model.select(original);assert.equal(model.draft,'');
});
test('deleting and retyping identical words while a send is pending creates a new draft lineage',async()=>{
 const {model}=fixture(),gate=deferred(),admitted=deferred(),text=model.draft;model.repository.send=async()=>{admitted.resolve();await gate.promise;};const sending=model.send();await admitted.promise;const sentRevision=model.draftRevision;
 model.updateDraft('');model.updateDraft(text);const newer=model.draftRevision;assert.notEqual(newer,sentRevision);await model.persistDraft();gate.resolve();assert.equal(await sending,true);
 assert.equal(model.draft,text);assert.equal(model.draftRevision,newer);assert.equal(JSON.parse(await model.local.get('draft:'+model.target())).revision,newer);model.cancelDraftTimer();
});
test('an uncertain message blocks only its own destination and a different chat can send',async()=>{
 const {model}=fixture(),original=model.selected;model.repository.send=async()=>{throw new HttpFailure(202,true,true);};assert.equal(await model.send(),false);assert.equal(model.operationState,'pending');
 const other=new ResourceRow('other','Other','session');other.manager='personal';other.machine='machine-one';other.session=other.id;await model.select(other);assert.equal(model.operationState,'');model.updateDraft('Other message');model.repository.send=async()=>{};assert.equal(await model.send(),true);
 await model.select(original);assert.equal(model.operationState,'pending');assert.equal(await model.send(),false);
});
test('optimistic user content is visible before ACK and only the exact server echo replaces it',async()=>{
 const {model}=fixture(),gate=deferred(),admitted=deferred();let op,text;model.repository.send=async(row,body,id)=>{text=body;op=id;admitted.resolve();await gate.promise;};
 const sending=model.send();await admitted.promise;assert.ok(model.messages.some(m=>m.id==='local:'+op&&m.deliveryState==='pending'));gate.resolve();assert.equal(await sending,true);
 model.publishTranscript([{id:'unrelated',role:'user',kind:'text',text,seq:1,client_msg_id:'another-op'}],false);assert.ok(model.messages.some(m=>m.id==='local:'+op));
 model.publishTranscript([{id:'server-echo',role:'user',kind:'text',text,seq:2,client_msg_id:op}],false);assert.equal(model.messages.filter(m=>m.role==='user').length,1);assert.equal(model.messages[0].id,'server-echo');assert.equal(model.messages.some(m=>m.id==='local:'+op),false);
 await new Promise(resolve=>setImmediate(resolve));
});
test('changing destination during local outbox admission prevents dispatch to either destination',async()=>{
 const {model,writes}=fixture(),gate=deferred(),reading=deferred(),get=model.local.get.bind(model.local);model.local.get=async key=>{if(key.startsWith('outbox:')){reading.resolve();await gate.promise;}return get(key);};
 const sending=model.send();await reading.promise;model.selected=new ResourceRow('new','New','session');model.updateDraft('New draft');gate.resolve();assert.equal(await sending,false);assert.equal(writes.length,0);assert.equal(model.draft,'New draft');model.cancelDraftTimer();
});
test('a crash between local staging and transport admission restores an explicit uncertain result without replay',async()=>{
 const {model,writes}=fixture(),scope=model.scope(),target=model.target(),{ChatDraftStore,ChatOutboxEntry}=await import('../entry/src/main/ets/models/ChatDraftStore.ets'),store=new ChatDraftStore(model.local),entry=new ChatOutboxEntry();entry.id='crash-before-admission';entry.target=target;entry.text='Unsent synthetic text';entry.draft=entry.text;entry.revision='crash-draft';await store.stage(scope,entry);
 await model.select(model.selected);assert.equal(model.operationState,'unknown');assert.equal(writes.length,0);assert.equal(model.canCheckLocalMessage(entry.id),true);await model.acknowledgeWrite(entry.id);assert.equal(model.operationState,'failed');
 model.updateDraft('Another draft');await model.restoreMessageDraft(entry.id);assert.equal(model.draft,entry.draft);assert.equal(writes.length,0);model.cancelDraftTimer();
});
test('a persisted outbox receipt can clear its original draft even when no operation journal entry survived',async()=>{
 const {model,writes}=fixture(),scope=model.scope(),target=model.target(),{ChatDraftStore,ChatOutboxEntry,ChatDraft}=await import('../entry/src/main/ets/models/ChatDraftStore.ets'),store=new ChatDraftStore(model.local),entry=new ChatOutboxEntry(),draft=new ChatDraft();entry.id='receipt-only';entry.target=target;entry.revision='sent-version';entry.text='Sent synthetic text';entry.draft=entry.text;draft.text=entry.text;draft.revision=entry.revision;await store.save(target,draft);await store.stage(scope,entry);
 model.features=['client_ops'];model.repository.call=async path=>{assert.equal(path,'/api/v1/ops/'+entry.id);return{state:'done'};};const restoredRow=model.selected;model.selected=new ResourceRow();model.draft='';model.sourceContext='';await model.select(restoredRow);assert.equal(model.operationState,'unknown');await model.reconcileWrites();assert.equal(model.operationState,'done');assert.equal(model.draft,'');assert.equal(writes.length,0);assert.equal((await store.read(target)).text,'');
});
test('plain text and attachment composers enforce the same long-message bound before persistence or dispatch',async()=>{
 const {model,writes}=fixture(),old=model.draft;model.updateDraft('x'.repeat(100001));assert.equal(model.draft,old);assert.equal(model.error,'chat_attachment_text_too_large');model.draft='x'.repeat(100001);assert.equal(await model.send(),false);assert.equal(writes.length,0);
});
test('an exact photo-message echo before HTTP timeout proves delivery and returns a successful attachment ACK',async()=>{
 const {model}=fixture(),gate=deferred(),admitted=deferred();model.draft='';model.sourceContext='';let op;model.repository.send=async(row,text,id)=>{op=id;admitted.resolve();await gate.promise;};
 const prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'',images:[{mime:'image/jpeg',data:'synthetic'}]};const sending=model.sendPrepared(prepared,'','');await admitted.promise;
 model.publishTranscript([{id:'photo-echo',role:'user',kind:'text',text:'<attached_images>\n/tmp/photo.jpg\n</attached_images>',seq:1,client_msg_id:op}],false);gate.reject(new HttpFailure(0,true));assert.equal(await sending,true);assert.equal(model.draft,'');assert.ok(!['unknown','pending'].includes(model.operationState));assert.equal(model.messages.some(m=>m.id==='local:'+op),false);
});
test('a photo-only message stays visible locally after ACK when transcript refresh fails',async()=>{
 const {model}=fixture();model.draft='';model.sourceContext='';model.loadChat=async()=>{throw new Error('read failed');};
 const prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'',images:[{mime:'image/jpeg',data:'one'},{mime:'image/png',data:'two'}]};assert.equal(await model.sendPrepared(prepared,'',''),true);assert.equal(model.messages.length,1);assert.equal(model.messages[0].localImageCount,2);assert.equal(model.messages[0].deliveryState,'done');
});
test('read-only metadata received while the local message is being staged revokes dispatch',async()=>{
 const {model,writes}=fixture(),gate=deferred(),entered=deferred(),get=model.local.get.bind(model.local);model.canSend=()=>ClientModel.prototype.canSend.call(model);model.selected.raw=JSON.stringify({source_kind:'live',capabilities:{read:true,send:true}});assert.equal(model.canSend(),true);
 model.local.get=async key=>{if(key.startsWith('outbox:')){entered.resolve();await gate.promise;}return get(key);};const sending=model.send();await entered.promise;model.selected.raw=JSON.stringify({source_kind:'live',read_only:true,capabilities:{read:true,send:false}});gate.resolve();assert.equal(await sending,false);assert.equal(writes.length,0);assert.equal(model.operationState,'failed');model.selected.raw=JSON.stringify({source_kind:'live',capabilities:{read:true,send:true}});assert.equal(await model.send(),true);assert.equal(writes.length,1);
});
test('composer limits count UTF-8 Chinese and emoji plus source-context bytes',async()=>{
 for(const text of ['界'.repeat(40000),'🌍'.repeat(30000)]){const {model,writes}=fixture();model.draft=text;assert.equal(await model.send(),false);assert.equal(writes.length,0);}
 const {model,writes}=fixture();model.draft='界'.repeat(33300);model.sourceContext=JSON.stringify({kind:'todo',title:'Source',id:'x'.repeat(400)});assert.equal(await model.send(),false);assert.equal(model.error,'chat_attachment_request_too_large');assert.equal(writes.length,0);
});

test('an exact echo after leaving and reopening the chat settles the original repository call despite its late timeout',async()=>{
 const {model}=fixture(),{HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets'),repo=new HubRepository({managerId:'personal',hubId:model.profile.id},model.local),gate=deferred(),entered=deferred(),original=model.selected;model.repository=repo;model.draft='';model.sourceContext='';let op;
 globalThis.clientTestRequest=async(path,method,body,id)=>{op=id;entered.resolve();return gate.promise;};
 const prepared={repository:repo,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:original,text:'',images:[{mime:'image/jpeg',data:'synthetic'}]},sending=model.sendPrepared(prepared,'','');await entered.promise;
 const other=new ResourceRow('other','Other','session');other.manager='personal';await model.select(other);await model.select(original);
 model.publishTranscript([{id:'navigated-photo-echo',role:'user',kind:'text',text:'<attached_images>\n/tmp/photo.jpg\n</attached_images>',seq:1,client_msg_id:op}],false);
 await new Promise(resolve=>setImmediate(resolve));gate.reject(new HttpFailure(0,true));assert.equal(await sending,true);assert.ok(!['unknown','pending'].includes(model.operationState));assert.equal(repo.mutations.blocked().length,0);assert.equal(model.messages.some(m=>m.id==='local:'+op),false);
});
test('Checked obtains a fresh server receipt and cannot release a completion lost from an older local unknown row',async()=>{
 const {model}=fixture(),{HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets'),repo=new HubRepository({managerId:'personal',hubId:model.profile.id},model.local);model.repository=repo;const record=await repo.mutations.begin('/messages','POST','body');await repo.mutations.finish(record,'unknown');model.unresolvedWrites=repo.mutations.blocked();model.features=['client_ops'];let lookups=0;
 globalThis.clientTestRequest=async path=>{assert.equal(path,'/api/v1/ops/'+record.id);lookups++;return '{"state":"done"}';};await model.acknowledgeWrite(record.id);assert.equal(lookups,1);assert.equal(record.state,'done');assert.equal(repo.mutations.blocked().length,0);
});
test('a queued echo completion is observed after the journal barrier before a late transport error propagates',async()=>{
 const {model}=fixture(),{HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets'),repo=new HubRepository({managerId:'personal',hubId:model.profile.id},model.local),network=deferred(),requested=deferred(),held=deferred(),queued=deferred(),original=model.selected;model.repository=repo;model.draft='';model.sourceContext='';let op;
 globalThis.clientTestRequest=async(path,method,body,id)=>{op=id;requested.resolve();return network.promise;};const prepared={repository:repo,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:original,text:'',images:[{mime:'image/png',data:'synthetic'}]},sending=model.sendPrepared(prepared,'','');await requested.promise;
 const other=new ResourceRow('other','Other','session');other.manager='personal';await model.select(other);await model.select(original);
 const put=model.local.put.bind(model.local),scope=model.scope();model.local.put=async(key,value,...rest)=>{if(key==='mutations:'+scope&&JSON.parse(value).ids.length>1){queued.resolve();await held.promise;}return put(key,value,...rest);};
 const occupying=repo.mutations.begin('/unrelated','POST','');await queued.promise;
 model.publishTranscript([{id:'queued-photo-echo',role:'user',kind:'text',text:'<attached_images>\n/tmp/photo.png\n</attached_images>',seq:1,client_msg_id:op}],false);network.reject(new HttpFailure(0,true));held.resolve();await occupying;assert.equal(await sending,true);assert.ok(!['unknown','pending'].includes(model.operationState));assert.equal(repo.mutations.blocked().some(r=>r.id===op),false);
});
test('client_ops missing receipt can release only a user-checked local-only staged message without replay',async()=>{
 const {model,writes}=fixture(),scope=model.scope(),{ChatDraftStore,ChatOutboxEntry}=await import('../entry/src/main/ets/models/ChatDraftStore.ets'),entry=new ChatOutboxEntry();entry.id='local-only-404';entry.target=model.target();entry.text='Local draft';entry.draft=entry.text;entry.revision='local-only-version';await new ChatDraftStore(model.local).stage(scope,entry);model.features=['client_ops'];model.repository.call=async()=>{throw new HttpFailure(404,'not_found',false);};await model.select(model.selected);assert.equal(model.canCheckLocalMessage(entry.id),true);await model.acknowledgeWrite(entry.id);assert.equal(model.operationState,'failed');assert.equal(writes.length,0);await model.restoreMessageDraft(entry.id);assert.equal(model.draft,'Local draft');assert.equal(writes.length,0);
});


test('an exact echo after a timed-out photo send clears the captured attachment selection after reopening',async()=>{
 const {model}=fixture();model.context={filesDir:'/app/files'};globalThis.clientAttachmentCommits=[];
 model.repository.send=async()=>{throw new HttpFailure(0,true);};model.draft='';model.sourceContext='';
 const snapshot={target:JSON.stringify(['synthetic-hub','personal','machine-one','chat-one']),selectionRevision:1,attachments:[{id:'image-one',revision:1}]};
 const prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'',images:[{mime:'image/png',data:'synthetic'}],snapshot};
 assert.equal(await model.sendPrepared(prepared,'',''),false);assert.equal(globalThis.clientAttachmentCommits.length,0);
 const entry=model.localMessages[0];model.localMessages=[];await model.restoreLocalDelivery();
 model.projectMessages=()=>[];model.publishTranscript([{id:'server-user',role:'user',client_msg_id:entry.id}],false);
 for(let i=0;i<20;i++)await new Promise(resolve=>setImmediate(resolve));
 assert.equal(globalThis.clientAttachmentCommits.length,1);assert.equal(globalThis.clientAttachmentCommits[0][1],JSON.stringify(snapshot));
 assert.equal((await model.local.get('outbox:'+model.scope())).includes(entry.id),false);assert.equal(model.draft,'');
 delete globalThis.clientAttachmentCommits;
});


test('confirmed photo delivery suppresses its selection before failed draft cleanup or preview-store initialization',async()=>{
 const {model}=fixture();model.context={filesDir:'/app/files'};globalThis.clientAttachmentSuppressed=[];
 model.draft='';model.sourceContext='';const snapshot={target:JSON.stringify(['synthetic-hub','personal','machine-one','chat-one']),selectionRevision:1,attachments:[{id:'image-one',revision:1}]};
 const prepared={repository:model.repository,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'',images:[{mime:'image/png',data:'synthetic'}],snapshot};
 const oldPut=model.local.put.bind(model.local);let admitted=false;
 model.local.put=async(k,v)=>{if(admitted&&k.startsWith('draft:'))throw Error('disk full');return oldPut(k,v);};model.repository.send=async()=>{admitted=true;};
 assert.equal(await model.sendPrepared(prepared,'',''),true);assert.equal(globalThis.clientAttachmentSuppressed.length,1);assert.equal(globalThis.clientAttachmentSuppressed[0][0],JSON.stringify(snapshot));assert.equal(model.error,'cache_unavailable');delete globalThis.clientAttachmentSuppressed;
});


test('restoring multiple delivered selections cannot enter a replacement pairing after the first awaits cleanup',async()=>{
 const {model}=fixture(),scope=model.scope(),{ChatDraftStore,ChatOutboxEntry}=await import('../entry/src/main/ets/models/ChatDraftStore.ets'),store=new ChatDraftStore(model.local),entered=deferred(),release=deferred();let epoch=0;
 model.context={filesDir:'/app/files'};model.local.revision=()=>epoch;model.local.accepts=(key,captured)=>captured===epoch;globalThis.clientAttachmentSuppressed=[];globalThis.clientAttachmentCommits=[];
 for(const id of ['first','second']){const entry=new ChatOutboxEntry();entry.id=id;entry.target=model.target();entry.attachmentSnapshot=JSON.stringify({target:JSON.stringify(['synthetic-hub','personal','machine-one','chat-one']),attachments:[{id,revision:1}]});await store.stage(scope,entry);await store.settle(scope,id,'done');}
 globalThis.clientAttachmentCommitGate=async()=>{entered.resolve();await release.promise;};
 const restoring=model.restoreLocalDelivery();await entered.promise;epoch++;model.local.data.clear();model.repository={async send(){}};model.draft='Fresh pairing draft';model.draftRevision='fresh';model.operationState='fresh';release.resolve();await restoring;
 assert.equal(globalThis.clientAttachmentSuppressed.length,1);assert.equal(globalThis.clientAttachmentCommits.length,1);assert.equal(model.draft,'Fresh pairing draft');assert.equal(model.operationState,'fresh');
 delete globalThis.clientAttachmentSuppressed;delete globalThis.clientAttachmentCommits;delete globalThis.clientAttachmentCommitGate;
});


test('dedicated resource destinations do not issue a generic knowledge or unsupported todo detail read',async()=>{
 for(const kind of ['todo','knowledge','inbox','file','todo_source','todo_thread']){
  const {model}=fixture();let calls=0;model.repository.resource=async()=>{calls++;throw Error('wrong endpoint');};
  const row=new ResourceRow('resource','Resource',kind);row.manager=model.profile.manager;await model.select(row);assert.equal(calls,0);assert.equal(model.error,'');assert.equal(model.selected.kind,kind);
 }
});

test('identical manager and session ids in two paired Hubs retain independent detail contexts',async()=>{
 const {model}=fixture(),other=new HubProfile('second-hub','Second Hub','https://second.invalid');other.manager='personal';model.profiles=[model.profile,other];
 const row=new ResourceRow('same-chat','','session');row.group=other.id;row.manager='personal';row.machine='same-machine';
 const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;};
 try {const view=await model.viewFor(row);assert.notEqual(view,model);assert.equal(view.profile.id,'second-hub');assert.equal(view.profile.baseUrl,other.baseUrl);assert.equal(model.profile.id,'synthetic-hub');assert.equal(model.directoryOwnerFor(row),view);
  const own=new ResourceRow(row.id,'','session');own.group=model.profile.id;own.manager=row.manager;own.machine=row.machine;assert.notEqual(row.key(),own.key());
  await assert.rejects(model.select(row),/request_updated/);
 }finally{ClientModel.prototype.selectProfile=select;model.retireViews();}
});
test('directory hydration visits every paired Hub and manager without changing the chosen workspace',async()=>{
 const {model}=fixture(),chosen=model.profile,other=new HubProfile('second-hub','Second Hub');other.manager='personal';model.profiles=[chosen,other];model.foreground=true;
 const calls=[],make=(profile,manager)=>{const view=new ClientModel();view.profile=new HubProfile(profile.id,profile.name);view.profile.manager=manager;view.connected=true;view.managers=manager==='personal'?[new ResourceRow('personal'),new ResourceRow('work')]:[];view.refresh=async()=>{};return view;};
 const ownManagers=[new ResourceRow('personal'),new ResourceRow('work')];model.managers=ownManagers;model.refresh=async()=>{};
 model.viewFor=async row=>{calls.push([row.group,row.manager]);return row.group===chosen.id&&row.manager==='personal'?model:make(model.profiles.find(p=>p.id===row.group),row.manager);};
 await model.refreshDirectory();assert.deepEqual(calls,[[chosen.id,'personal'],[chosen.id,'work'],[other.id,'personal'],[other.id,'work']]);assert.equal(model.profile,chosen);assert.equal(model.directoryReady,true);assert.equal(model.directoryLoading,false);
});
test('a superseded directory pass cannot publish completion into a new workspace',async()=>{
 const {model}=fixture(),gate=deferred();model.profiles=[model.profile];model.viewFor=()=>gate.promise;
 const old=model.refreshDirectory();model.generation++;model.directoryReady=false;gate.resolve(model);await old;assert.equal(model.directoryReady,false);assert.equal(model.directoryTask,undefined);
});

test('aggregate serialization retains needs-you, working and unread session states',()=>{const {model}=fixture();model.profiles=[model.profile];model.sessions=['needs_you','working','unread'].map(state=>{const row=new ResourceRow(state,state,'session');row.state=state;row.manager=model.profile.manager;row.machine='same-machine';return row;});model.publishDirectory();assert.deepEqual(model.directorySessions.map(row=>row.state),['needs_you','working','unread']);assert.ok(model.directorySessions.every(row=>row.group===model.profile.id));});

test('the local experience uses one mutable repository across chat, todo and Agent flows without transport',async()=>{const model=new ClientModel();model.ready=true;model.foreground=true;let network=0;globalThis.clientTestRequest=async()=>{network++;throw Error('demo must stay local');};model.useDemo();assert.equal(model.profile.manager,'demo');assert.equal(model.repo(),model.demoRepository);const main=await model.openMain();assert.equal(model.canSend(),true);model.updateDraft('Synthetic local message');assert.equal(await model.send(),true);assert.ok(model.messages.some(message=>message.role==='user'&&message.text==='Synthetic local message'));const other=await model.newConversation();assert.notEqual(other.id,main.id);assert.equal(model.messages.some(message=>message.text==='Synthetic local message'),false);await model.select(main);assert.ok(model.messages.some(message=>message.text==='Synthetic local message'));const todo=await model.repo().call('/api/v1/todos','POST',{title:'Local task',notes:'Synthetic'});await model.listResources('todo');assert.ok(model.resourceRows.some(row=>row.id===todo.todo.id));const agent=await model.createAgent('Local Agent',model.machines[0],'/demo/ReleaseDesk','',todo.todo.id);assert.ok(model.sessions.some(row=>row.id===agent.id));assert.equal(agent.group,'demo');assert.equal(network,0);model.cancelDraftTimer();});

test('application language uses the native API and serializes device preferences with appearance changes',async()=>{const {model}=fixture(),gate=deferred(),entered=deferred();globalThis.clientLanguageChanges=[];const put=model.local.put.bind(model.local);let first=true;model.local.put=async(key,value)=>{if(key==='preferences'&&first){first=false;entered.resolve();await gate.promise;}return put(key,value);};const appearance=model.setAppearance(2,'brief',false);await entered.promise;const language=model.setLanguage('ja-JP');assert.deepEqual(globalThis.clientLanguageChanges,[]);gate.resolve();await appearance;await language;const saved=JSON.parse(await model.local.get('preferences'));assert.equal(saved.appearance,2);assert.equal(saved.style,'brief');assert.equal(saved.alerts,false);assert.equal(saved.language,'ja-JP');assert.equal(model.language,'ja-JP');assert.deepEqual(globalThis.clientLanguageChanges,['ja-JP']);assert.equal(model.languageChanging,false);await model.setLanguage('not-supported');assert.deepEqual(globalThis.clientLanguageChanges,['ja-JP']);delete globalThis.clientLanguageChanges;});

test('queued appearance, style and notification edits merge only their changed fields',async()=>{const {model}=fixture(),gate=deferred(),entered=deferred(),put=model.local.put.bind(model.local);let first=true;model.local.put=async(key,value)=>{if(key==='preferences'&&first){first=false;entered.resolve();await gate.promise;}return put(key,value);};const mode=model.setAppearance(2);await entered.promise;const style=model.setStyle('brief'),alerts=model.setAlerts(false);gate.resolve();await Promise.all([mode,style,alerts]);const saved=JSON.parse(await model.local.get('preferences'));assert.equal(saved.appearance,2);assert.equal(saved.style,'brief');assert.equal(saved.alerts,false);});
test('a fallback profiles write returning after owner change or background cannot send a bearer probe',async()=>{
 for(const cancel of ['owner','background']){const model=new ClientModel(),profile=new HubProfile('fallback-hub','','https://primary.invalid/');profile.hubId=profile.id;profile.pin='A'.repeat(43);profile.manager='personal';profile.addresses=['https://secondary.invalid/'];model.profile=profile;model.profiles=[profile];model.foreground=true;model.vault={async get(){return'synthetic-device-token';}};const gate=deferred(),entered=deferred(),put=model.local.put.bind(model.local);let writes=0,requests=0;model.local.put=async(key,value)=>{if(key==='profiles'&&++writes===2){entered.resolve();await gate.promise;}return put(key,value);};globalThis.clientPairingAddress=async()=> 'https://secondary.invalid/';globalThis.clientTestRequest=async()=>{requests++;return'{}';};const connecting=model.selectProfile(profile,true);await entered.promise;if(cancel==='owner'){model.generation++;model.profile=new HubProfile('new-context');}else model.foreground=false;gate.resolve();await connecting;assert.equal(requests,0);delete globalThis.clientPairingAddress;}
});
test('pairing redemption cannot commit credentials after its initiating context is replaced',async()=>{const {model}=fixture(),gate=deferred(),entered=deferred();model.foreground=true;let puts=0,selected=0;model.vault={async get(){return null;},async put(){puts++;}};model.useEndpoint=async()=>{selected++;};const target={hubId:'paired-a',name:'A'},pairing={async redeem(_target,_name,admit){entered.resolve();await gate.promise;return{hubId:'paired-a',token:'synthetic-issued'};}};const old=model.pair(pairing,target);await entered.promise;model.generation++;model.profile=new HubProfile('chosen-c');gate.resolve();await assert.rejects(old,/request_updated/);assert.equal(puts,0);assert.equal(selected,0);assert.equal(model.profile.id,'chosen-c');});
test('cancelled credential admission restores the earlier token without selecting the new Hub',async()=>{const {model}=fixture(),gate=deferred(),entered=deferred();let stored='previous-token',allowed=true,selected=0;model.vault={async get(){return stored;},async put(_id,value){stored=value;if(value==='new-token'){entered.resolve();await gate.promise;}},async remove(){stored=null;}};model.selectProfile=async()=>{selected++;};const saving=model.useEndpoint({hubId:'target-hub',token:'new-token',baseUrl:'https://target.invalid/',pin:'',managerId:''},'Target',()=>allowed);await entered.promise;allowed=false;gate.resolve();await assert.rejects(saving,/request_updated/);assert.equal(stored,'previous-token');assert.equal(selected,0);});

test('demo answer and stop acknowledgements refresh the selected roster and retain its draft',async()=>{
 const model=new ClientModel();model.useDemo();const review=model.sessions.find(row=>row.id==='demo-agent-review');assert.ok(review);await model.select(review);
 assert.equal(model.decision?.id,'demo-plan-review');model.draft='Keep this draft';model.sourceContext='Keep this source';
 model.loadChat=async()=>{throw new Error('follow-up read failed');};await model.answer('approve',{},model.decision);
 assert.equal(model.operationState,'done');assert.equal(model.decision.status,'resolved');assert.equal(model.sessions.find(row=>row.id===review.id).state,'working');
 assert.equal(model.selected.state,'working');assert.equal(model.draft,'Keep this draft');assert.equal(model.sourceContext,'Keep this source');
 await model.stop();assert.equal(model.sessions.find(row=>row.id===review.id).state,'idle');assert.equal(model.selected.state,'idle');
 assert.equal(model.sessions.find(row=>row.id===review.id).data().status,'interrupted');assert.equal(model.draft,'Keep this draft');assert.equal(model.sourceContext,'Keep this source');
});

test('the demo todo workspace projects its waiting agent and opens the linked plan',async()=>{
 const model=new ClientModel();model.useDemo();const workspace=new TodoWorkspace();await workspace.open(model);
 const waiting=workspace.items.find(todo=>todo.id==='demo-todo-release');assert.ok(waiting);assert.equal(workspace.state(waiting),'needs_you');
 await workspace.openRow(waiting);
 const agent=waiting.activeAgents()[0];assert.ok(agent);assert.equal(agent.sessionId,'demo-agent-review');
 const session=workspace.agentSession(agent);assert.equal(session.id,'demo-agent-review');assert.equal(session.machine,'demo-mac');
 await model.select(session);assert.equal(model.decision?.id,'demo-plan-review');assert.equal(model.decision?.status,'open');
});

test('InboxWorkspace sends a reply into the opened demo thread with its validated root',async()=>{
 const model=new ClientModel();model.useDemo();const workspace=new InboxWorkspace();await workspace.open(model);
 const row=workspace.items.find(item=>item.id==='demo-inbox-lark');assert.ok(row);await workspace.openRow(row);
 assert.equal(workspace.isGeneric(),true);const root=workspace.feedMessages.find(message=>message.id==='demo-lark-message-1');assert.ok(root);
 await workspace.openThread(root);assert.equal(workspace.threadRoot,root.id);assert.equal(workspace.threadReplyTo,root.id);
 assert.equal(await workspace.sendConversation('I reviewed the sample plan.',root.id,true),true);
 const reply=workspace.feedMessages.find(message=>message.text==='I reviewed the sample plan.');assert.ok(reply);
 assert.equal(reply.data().thread_root,root.id);assert.equal(reply.data().reply_to,root.id);assert.equal(workspace.threadRoot,root.id);
});


test('assistant cards capture the actual Client machine identity and forget before durable cleanup',async()=>{
 const {AssistantViewModel}=await import('../entry/src/main/ets/models/AssistantView.ets');
 const {model}=fixture(),profile=model.profile,wait=deferred();model.selected.kind='conversation';model.selected.group=profile.id;model.selected.manager='personal';model.features=['assistant_views'];model.profiles=[profile];model.repository.close=()=>{};model.local.purgeHub=async()=>wait.promise;model.vault={async remove(){}};
 const key='view_real_client_owner',envelope={id:key,conversation:model.selected.id,version:1,view:{fallback:'Synthetic private card',sections:[{items:[{type:'button',key:'choose',title:'Choose',action:{id:'choose',reply:'Choose'}}]}]}};
 const feed={owner:'personal',conversationId:model.selected.id,views:{[key]:envelope},viewActions:{},viewBlocks:[]};
 const card=AssistantViewModel.acquire(model,feed,{id:'position',kind:'view',ref:key},key,true);
 assert.equal(card.canInteract(),true);assert.equal(card.fallbackText,'Synthetic private card');card.hide();
 const forgetting=model.forgetProfile(profile.id);assert.equal(card.active,false);assert.equal(card.fallbackText,'');assert.deepEqual(card.view,{});assert.equal(card.canRetryOriginal,false);
 wait.resolve();await forgetting;
});

test('SSH metadata follows the paired Hub across canonical manager selection and stays separate from manager views',async()=>{
 const {model}=fixture();await model.saveView('ssh-tunnel','{"enabled":true}');await model.saveView('filters','personal');
 model.profile.manager='work';assert.equal(await model.loadView('ssh-tunnel'),'{"enabled":true}');assert.equal(await model.loadView('filters'),'');
 model.profile=new HubProfile('different-hub');assert.equal(await model.loadView('ssh-tunnel'),'');
});

test('detached-owner reconnect reconstructs its profile while retaining the shared pairing lease',async()=>{
 const {model}=fixture(),row=new ResourceRow('secondary','','conversation');row.manager='work';
 const select=ClientModel.prototype.selectProfile;
 ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;};
 try {
  const first=await model.viewFor(row),profile=first.profile,revision=first.pairingRevision();first.connected=false;
  const again=await model.viewFor(row);assert.equal(first,again);assert.notEqual(again.profile,profile);assert.equal(again.pairingRevision(),revision);
  model.retirePairing(model.profile.id);assert.notEqual(again.pairingRevision(),revision);
 }finally{ClientModel.prototype.selectProfile=select;model.retireViews();}
});

test('enabled SSH startup failures stop Hub discovery before any direct probe or authenticated request',async()=>{
 const model=new ClientModel(),profile=new HubProfile('ssh-fail-hub','','https://target.invalid/');profile.hubId=profile.id;profile.pin='A'.repeat(43);profile.manager='work';
 model.profile=profile;model.profiles=[profile];model.foreground=true;model.vault={async get(){return 'synthetic';}};
 let probes=0,requests=0;globalThis.clientSshAcquire=async()=>{throw new Error('ssh_host_key_mismatch');};globalThis.clientPairingAddress=async()=>{probes++;return profile.baseUrl;};globalThis.clientTestRequest=async()=>{requests++;return '{}';};
 try {await model.selectProfile(profile,true);assert.equal(probes,0);assert.equal(requests,0);assert.equal(model.connected,false);}
 finally{delete globalThis.clientSshAcquire;delete globalThis.clientPairingAddress;model.stopReconnect();}
});

test('explicit SSH policies select the verified route in the configured order on a paired Hub',async()=>{
 for(const [policy,direct,ssh,expected] of [
  ['fallback',true,false,['direct','using-direct']],
  ['fallback',false,true,['direct','ssh','watch-direct']],
  ['always',true,true,['ssh']],
  ['always',true,false,['ssh','direct','using-direct']]
 ]){
  const model=new ClientModel(), profile=new HubProfile('policy-hub','','https://selected.invalid/');
  profile.hubId=profile.id;profile.pin='B'.repeat(43);profile.manager='work';
  model.profile=profile;model.profiles=[profile];model.foreground=true;model.vault={async get(){return 'synthetic';}};
  const calls=[];
  model.ssh.configuration={enabled:true,routePolicy:policy};model.ssh.watchDirect=()=>calls.push('watch-direct');
  model.ssh.usingDirectAddress=()=>calls.push('using-direct');
  globalThis.clientSshAcquire=async()=>{calls.push('ssh');if(!ssh)throw Error('ssh_connect_failed');return{isCurrent:()=>true};};
  globalThis.clientPairingAddress=async(address,...params)=>{calls.push('direct');assert.equal(params.at(-1),true);if(!direct)throw Error('unavailable');return address;};
  globalThis.clientTestRequest=async()=>{throw Error('end of routing probe');};
  try{await model.selectProfile(profile,true);assert.deepEqual(calls,expected);}
  finally{model.stopReconnect();delete globalThis.clientSshAcquire;delete globalThis.clientPairingAddress;delete globalThis.clientTestRequest;}
 }
});

test('Hub connection retirement closes the active owner and every retained manager while preserving other Hubs',async()=>{
 const {model}=fixture(),row=new ResourceRow('secondary','','conversation');row.manager='work';model.profiles=[model.profile];let rootClosed=0,secondaryClosed=0,foreignClosed=0;model.repository={close(){rootClosed++;}};
 const select=ClientModel.prototype.selectProfile;ClientModel.prototype.selectProfile=async function(profile){this.profile=profile;this.connected=true;};
 try{
  const secondary=await model.viewFor(row);secondary.repository={close(){secondaryClosed++;}};
  const foreign=new ClientModel();foreign.profile=new HubProfile('foreign');foreign.connected=true;foreign.repository={close(){foreignClosed++;}};model.views.set('foreign',foreign);
  const before=model.connectionGeneration();model.retireHubConnections(model.profile.id);assert.equal(rootClosed,1);assert.equal(secondaryClosed,1);assert.equal(foreignClosed,0);assert.equal(model.connected,false);assert.equal(secondary.retired,true);assert.equal(model.connectionGeneration()>before,true);assert.equal(model.views.has('foreign'),true);
 }finally{ClientModel.prototype.selectProfile=select;model.stopReconnect();model.retireViews();}
});

test('committed network settings replace an old reconnect timer even when no settings view remains',async()=>{
 const {model}=fixture();model.repository={close(){model.scheduleReconnect();}};model.connected=false;let attempts=0;
 model.selectProfile=async()=>{attempts++;model.connected=true;};model.scheduleReconnect();
 model.retireHubConnections(model.profile.id);
 try {await new Promise(resolve=>setTimeout(resolve,1200));assert.equal(attempts,1);assert.equal(model.connected,true);}
 finally{model.stopReconnect();}
});

test('slow credential admission keeps its generation while SSH offline callbacks request reconnection',async()=>{
 const {model}=fixture(),entered=deferred(),wait=deferred(),profile=model.profile;let selected=0,reads=0;
 model.foreground=true;model.ssh.stop=()=>model.scheduleReconnect();model.repository={close(){model.scheduleReconnect();}};
 model.vault={async get(){if(++reads===1){entered.resolve();await wait.promise;}return 'previous';},async put(){},async remove(){}};
 model.selectProfile=async function(next){selected++;this.profile=next;this.generation++;this.connected=true;};
 const pairing={async redeem(){return{hubId:profile.id,baseUrl:'https://replacement.invalid/',token:'new-synthetic',pin:'A'.repeat(43),managerId:profile.manager};}};
 const task=model.pair(pairing,{hubId:profile.id,name:'Replacement'});await entered.promise;const admitted=model.connectionGeneration();
 try {model.scheduleReconnect();await new Promise(resolve=>setTimeout(resolve,1200));assert.equal(model.connectionGeneration(),admitted);assert.equal(selected,0);wait.resolve();await task;assert.equal(selected,1);assert.equal(model.profile.baseUrl,'https://replacement.invalid/');}
 finally{wait.resolve();await task.catch(()=>{});model.stopReconnect();}
});

test('language publication waits for storage and a failed save restores the previous native preference',async()=>{
 const {model}=fixture();model.language='en-US';globalThis.clientLanguageChanges=[];const entered=deferred(),gate=deferred();model.local.put=async()=>{entered.resolve();await gate.promise;throw Error('storage unavailable');};
 const changing=model.setLanguage('ja-JP');await entered.promise;assert.equal(model.language,'en-US');gate.resolve();await changing;
 assert.equal(model.language,'en-US');assert.deepEqual(globalThis.clientLanguageChanges,['ja-JP','en-US']);assert.equal(model.error,'cache_unavailable');delete globalThis.clientLanguageChanges;
});

test('captured outcome storage keeps its original scope and is retired by that scope purge',async()=>{
  const {model}=fixture();const retired=new Set();model.local.accepts=key=>!retired.has(key);
  const old=model.captureView('synthetic-outcome');await old.write('pending');const oldKey=[...model.local.data.keys()][0];
  model.profile=new HubProfile('other-synthetic-hub');model.profile.manager='personal';
  const current=model.captureView('synthetic-outcome');await old.write('acknowledged');
  assert.equal(await old.read(),'acknowledged');assert.equal(await current.read(),'');
  retired.add(oldKey);model.local.data.delete(oldKey);assert.equal(old.valid(),false);
  await assert.rejects(old.write('late-result'),/request_updated/);await assert.rejects(old.read(),/request_updated/);
  await current.write('current-scope');assert.equal(await current.read(),'current-scope');assert.equal(model.local.data.has(oldKey),false);
});

test('a machine-directory notification requests fresh scoped facts instead of applying foreign payload rows',async(t)=>{
  t.mock.timers.enable({apis:['setTimeout']});
  const {model}=fixture(),scopes=[];
  model.machines=[new ResourceRow('owned','Original','machine')];
  model.refresh=async()=>{scopes.push(model.scope());};
  const owner=model.scope();
  model.event(JSON.stringify({type:'machines',manager:'another-owner',machines:[{id:'foreign',name:'Foreign'}]}));
  assert.equal(model.machines[0].id,'owned');
  t.mock.timers.tick(100);await Promise.resolve();
  assert.deepEqual(scopes,[owner]);assert.equal(model.machines[0].id,'owned');
});


test('selecting a context only stages this conversation draft and sends no message',async()=>{
 const {model,writes}=fixture();model.canCompose=()=>true;model.persistDraft=async()=>{};
 const item=new ResourceRow('reference','Reference','knowledge');item.group=model.profile.id;item.manager=model.profile.manager;
 const target=model.target();assert.equal(await model.addDraftContext(item,target,model.connectionGeneration(),model.scope()),true);
 assert.equal(model.target(),target);assert.equal(writes.length,0);
 const references=JSON.parse(model.sourceContext).contexts;assert.equal(references.at(-1).id,'reference');assert.equal(references[0].id,'source');
});
test('a context from a different owner is not staged',async()=>{
 const {model}=fixture();model.canCompose=()=>true;const before=model.sourceContext;let writes=0;model.persistDraft=async()=>writes++;
 const item=new ResourceRow('reference','Reference','todo');item.manager='other';
 assert.equal(await model.addDraftContext(item,model.target(),model.connectionGeneration(),model.scope()),false);assert.equal(writes,0);assert.equal(model.sourceContext,before);
});
test('promoting a quoted message preserves its text and attachment metadata through the next send',async()=>{
 const {model,writes}=fixture();model.canCompose=()=>true;model.persistDraft=async()=>{};
 model.features=['persona_contracts_v1'];
 const legacy={kind:'message',title:'Quoted reply',item_id:'quoted-item',machine_id:'machine-one',session_id:'chat-one',manager:model.profile.manager,
  text:'Quoted details <tag> & meaning',images:[{id:'image-one',mime_type:'image/png',width:640,height:480}],
  files:[{name:'notes.txt',path:'/fixture/notes.txt',size:42}],metadata:{reference:'retained'}};
 model.sourceContext=JSON.stringify(legacy);
 const item=new ResourceRow('todo-reference','Todo','todo');item.manager=model.profile.manager;
 assert.equal(await model.addDraftContext(item,model.target(),model.connectionGeneration(),model.scope()),true);
 assert.deepEqual(JSON.parse(model.sourceContext).contexts[0],legacy);
 assert.equal(await model.send(),true);assert.equal(writes.length,1);
 const serialized=writes[0][1].match(/^<persona-context[^>]*>([\s\S]*?)<\/persona-context>/)[1];
 const sent=JSON.parse(serialized.replace(/&lt;/g,'<').replace(/&gt;/g,'>').replace(/&amp;/g,'&'));
 assert.deepEqual(sent,legacy);assert.match(writes[0][1],/todo-reference/);
});
test('a Knowledge handoff preserves a legacy message reference already in the main draft',async()=>{
 const {model,writes}=fixture();model.features=['persona_contracts_v1'];
 model.selected.kind='conversation';model.selected.manager=model.profile.manager;
 const legacy={kind:'message',title:'Quoted reply',text:'Keep the quoted text',images:[{id:'image-one'}],files:[{name:'notes.txt',size:42}]};
 model.sourceContext=JSON.stringify(legacy);model.persistDraft=async()=>{};
 model.viewFor=async()=>model;model.openMain=async()=>model.selected;
 const source=new ResourceRow('knowledge-one','Knowledge','knowledge');source.manager=model.profile.manager;
 await model.handoff(source);
 const contexts=JSON.parse(model.sourceContext).contexts;assert.deepEqual(contexts[0],legacy);assert.equal(contexts[1].id,source.id);
 assert.equal(await model.send(),true);assert.equal(writes.length,1);
 assert.match(writes[0][1],/Keep the quoted text/);assert.match(writes[0][1],/image-one/);assert.match(writes[0][1],/knowledge-one/);
});
test('route admission survives draft, outbox and mutation journals for plain and prepared messages',async()=>{
 const {HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets');
 for(const preparedSend of [false,true])for(const boundary of ['draft','outbox','mutation']){
  const {model}=fixture();let allowed=true,posts=0;const put=model.local.put.bind(model.local),beforeRequest=globalThis.clientTestRequest;
  const repo=new HubRepository({managerId:model.profile.manager,hubId:model.profile.id},model.local);model.repository=repo;
  model.local.put=async(key,value)=>{await put(key,value);if(boundary==='draft'&&key.startsWith('draft:')||boundary==='outbox'&&key.startsWith('outbox:'))allowed=false;};
  const entered=deferred(),gate=deferred(),begin=repo.mutations.begin.bind(repo.mutations);
  if(boundary==='mutation')repo.mutations.begin=async(...args)=>{const record=await begin(...args);entered.resolve();await gate.promise;return record;};
  globalThis.clientTestRequest=async(_path,method)=>{if(method==='POST')posts++;return '{}';};
  const draft=model.draft,context=model.sourceContext;
  const prepared={repository:repo,generation:model.connectionGeneration(),scope:model.scope(),modelTarget:model.target(),row:model.selected,text:'Prepared message',images:[]};
  try{
   const sending=preparedSend?model.sendPrepared(prepared,draft,context,()=>allowed):model.send(()=>allowed);
   if(boundary==='mutation'){await entered.promise;allowed=false;gate.resolve();}
   assert.equal(await sending,false);assert.equal(posts,0);assert.equal(model.draft,draft);assert.equal(model.sourceContext,context);
  }finally{globalThis.clientTestRequest=beforeRequest;}
 }
});

test('failed context persistence restores the old context without replacing a newer edit',async()=>{
 for(const newer of [false,true]){
  const {model}=fixture();model.canCompose=()=>true;const before=model.sourceContext,wait=deferred();model.persistDraft=()=>wait.promise;
  const item=new ResourceRow('reference','Reference','inbox');item.manager=model.profile.manager;
  const pending=model.addDraftContext(item,model.target(),model.connectionGeneration(),model.scope());
  if(newer){model.sourceContext='newer-context';model.draftRevision='newer-revision';}
  wait.reject(Error('disk unavailable'));assert.equal(await pending,false);assert.equal(model.sourceContext,newer?'newer-context':before);
 }
});


test('knowledge contexts retain their entry kind when IDs collide across categories',async()=>{
 const {model,writes}=fixture();model.canCompose=()=>true;model.persistDraft=async()=>{};model.sourceContext='';
 for(const path of ['projects','docs','docs']){const row=new ResourceRow('shared-id','Reference','knowledge');row.path=path;row.manager=model.profile.manager;assert.equal(await model.addDraftContext(row,model.target(),model.connectionGeneration(),model.scope()),true);}
 const refs=JSON.parse(model.sourceContext).contexts;assert.equal(refs.length,2);assert.deepEqual(refs.map(r=>r.entry_kind),['projects','docs']);assert.equal(writes.length,0);assert.match(model.prepareMessageText('Inspect'),/entry_kind/);
});


test('interrupt is active-turn only and its pending admission retires with the route',async()=>{
 const {model}=fixture();let posts=0;model.repository.stop=async()=>posts++;
 assert.equal(model.canStop(),false);await model.stop();assert.equal(posts,0);
 model.selected.state='working';assert.equal(model.canStop(),true);let visible=true;const gate=deferred(),entered=deferred();
 model.repository.stop=async(row,admit)=>{entered.resolve();await gate.promise;if(admit())posts++;};
 const stopping=model.stop(()=>visible);await entered.promise;assert.equal(model.stopPending(),true);assert.equal(model.canStop(),false);
 visible=false;gate.resolve();await stopping;assert.equal(posts,0);assert.equal(model.stopPending(),false);
});

test('question resolution rechecks journal-delayed scope, request, capability and active-sheet admission',async()=>{
 const {HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets');
 const {QuestionWorkspace}=await import('../entry/src/main/ets/models/QuestionWorkspace.ets');
 for(const retirement of ['none','same-content','sheet','native-dismiss','request','owner','target','capability','uncertain-refresh']){
  const {model}=fixture();delete model.canApprove;model.selected.kind='conversation';model.selected.manager='personal';model.selected.raw=JSON.stringify({source_kind:'native',capabilities:{read:true,send:true,approve:true}});model.decisionReadyTarget=model.target();
  const raw={id:'question-admission',kind:'question',tool_name:'ask_user',resumable:true,status:'open',manager:'personal',machine_id:'machine-one',session_id:'chat-one',can_cancel:true,questions:[{question:'Which?',options:[{label:'A'}]}]};
  model.decisionRefreshSequence=1;model.replacePendingDecisions([raw],model.selected,model.target(),model.connectionGeneration(),model.scope(),1);const request=model.personaQuestions[0];assert.ok(request);assert.equal(model.canApprove(),true);
  const repo=new HubRepository({managerId:'personal',hubId:model.profile.id},model.local);model.repository=repo;const entered=deferred(),gate=deferred(),begin=repo.mutations.begin.bind(repo.mutations);repo.mutations.begin=async(...args)=>{const record=await begin(...args);entered.resolve();await gate.promise;return record;};
  const workspace=new QuestionWorkspace();assert.equal(workspace.open(model,true,request),true);const sheet=workspace.beginPresentation(),admit=workspace.ticket(sheet);
  let posts=0,visible=true;const previous=globalThis.clientTestRequest;globalThis.clientTestRequest=async(_path,method)=>{if(method==='POST')posts++;return '{}';};
  try{
   const answering=model.answer('answer',{'Which?':'A'},request,()=>visible&&admit());await entered.promise;assert.equal(request.status,'pending');
   if(retirement==='sheet')visible=false;else if(retirement==='native-dismiss')workspace.dismiss(sheet,()=>{assert.equal(admit(),false);gate.resolve();});else if(retirement==='request')model.replacePendingDecisions([{...raw,questions:[{question:'Which?',options:[{label:'B'}]}]}],model.selected,model.target(),model.connectionGeneration(),model.scope(),1);else if(retirement==='same-content')model.replacePendingDecisions([raw],model.selected,model.target(),model.connectionGeneration(),model.scope(),1);else if(retirement==='owner')model.generation++;else if(retirement==='target')model.selected=new ResourceRow('other','Other','conversation');else if(retirement==='capability')model.selected.raw=JSON.stringify({source_kind:'native',capabilities:{approve:false}});
   if(retirement==='uncertain-refresh'){
    model.unresolvedWrites=[{path:'/api/v1/interactions/'+request.id+'/resolve'}];
    model.replacePendingDecisions([raw],model.selected,model.target(),model.connectionGeneration(),model.scope(),1);
    assert.equal(model.pendingDecisions[0],request);assert.equal(request.status,'unknown');
   }
   gate.resolve();await answering;assert.equal(posts,['none','same-content'].includes(retirement)?1:0,retirement);assert.equal(repo.mutations.blocked().length,0);if(['sheet','native-dismiss'].includes(retirement))assert.equal(request.status,'open');
   if(retirement==='uncertain-refresh')assert.equal(request.status,'unknown','a newer authoritative uncertainty must not reopen after admission fails');
  }finally{gate.resolve();globalThis.clientTestRequest=previous;}
 }
});

test('assistant selection rechecks the source scope and installed, visible roster at the time of the tap', async () => {
  const { model } = fixture();
  const calls = [];
  model.selectManager = async id => { calls.push(id); };
  const row = (id, data = {}) => ResourceRow.from({ id, ...data }, 'manager');
  model.managers = [row('ready'), row('hidden', { hidden: true }),
    row('installing', { installing: '2026-11-21T10:00:00Z' }), row('legacy', { installing: true })];
  const source = model.scope();
  for (const id of ['hidden', 'installing', 'legacy', 'missing', model.profile.manager]) {
    await model.selectListedManager(id, source);
  }
  await model.selectListedManager('ready', source + 'previous');
  assert.deepEqual(calls, []);
  await model.selectListedManager('ready', source);
  assert.deepEqual(calls, ['ready']);
});

test('cancelled pairing restores the prior complete credential and preserves a later replacement with the same device token', async () => {
  const {HubAccess} = await import('../entry/src/main/ets/core/HubAccess.ts');
  const origin='https://hub.invalid:443';
  for (const superseded of [false, true]) {
    const {model}=fixture(), profile=model.profile, wait=deferred(), entered=deferred();
    const previous=HubAccess.fromInput(origin,'client','previous');
    const candidate=HubAccess.fromInput(origin,'client','candidate');
    const newer=HubAccess.fromInput(origin,'client','newer');
    let current=previous, allowed=true;
    model.profiles=[profile]; model.repository.close=()=>{};
    model.vault={
      async get(_id,receive){receive?.(current);return 'same-device-token';},
      async put(_id,token,edge){
        assert.equal(token,'same-device-token'); current=edge;
        if(edge===candidate){entered.resolve();await wait.promise;}
      },
      async remove(){assert.fail('an active earlier pairing must remain stored');}
    };
    const changing=model.useEndpoint({hubId:profile.id,baseUrl:origin,token:'same-device-token',pin:'',managerId:'',access:candidate},'Hub',()=>allowed);
    await entered.promise;
    if(superseded)current=newer;
    allowed=false;wait.resolve();
    await assert.rejects(changing,/request_updated/);
    assert.equal(current,superseded?newer:previous);
  }
});

test('a failed device-token write restores pending SSH metadata on the verified Hub only',async()=>{
  const {model}=fixture();model.foreground=true;
  // The existing selected Hub belongs to a different pairing context.
  const base='https://new.synthetic.invalid/',id='verified-synthetic-ssh',pin='A'.repeat(43);
  const previousScope=model.scope(),settled=[];
  model.local.allowHub=()=>{};model.local.accepts=()=>true;
  model.vault={async get(){return null;},async put(){throw Error('synthetic device is locked');}};
  const route={async install(hub,address,spki,view){
    assert.equal(hub,id);assert.equal(address,base);assert.equal(spki,pin);
    await view.write('synthetic-pending-ssh');
    return async committed=>{settled.push(committed);if(!committed)await view.clearIfUnchanged('synthetic-pending-ssh');};
  }};
  await assert.rejects(model.useEndpoint({hubId:id,baseUrl:base,pin,token:'synthetic-new-device'},'Synthetic',()=>true,route),/device is locked/);
  assert.deepEqual(settled,[false]);assert.equal(model.scope(),previousScope);
  assert.ok([...model.local.data.values()].every(value=>value!=='synthetic-pending-ssh'));
});

test('forward recipients recheck the current same-Hub inventory, owner, read-only flag, and live machine before accepting a draft',()=>{
 const {model}=fixture();
 function destination(id,attrs={}){
  const r=ResourceRow.from({session_id:id,machine_id:'studio',title:id,state:'idle',...attrs},'session');
  r.manager='personal';r.group=model.profile.id;return r;
 }
 const target=destination('to'),computer=ResourceRow.from({id:'studio',name:'Studio',online:true},'machine');
 model.machines=[computer];model.sessions=[model.selected,target];
 assert.equal(model.canForwardTo(target),true);
 const cross=destination('to');cross.group='second-pair';assert.equal(model.canForwardTo(cross),false);
 const foreignOwner=destination('to');foreignOwner.manager='work';assert.equal(model.canForwardTo(foreignOwner),false);
 for(const disallowed of [{archived:true},{space:'health'},{deleted:true},{read_only:true},{source_kind:'new_source',capabilities:{read:true,send:false}},{capabilities:{send:false}}]){
  model.sessions=[destination('to',disallowed)];assert.equal(model.canForwardTo(target),false,JSON.stringify(disallowed));
 }
 model.sessions=[destination('to',{state:'offline',capabilities:{send:true}})];
 assert.equal(model.canForwardTo(target,false),false);assert.equal(model.canForwardTo(target,true),true);
 computer.raw=JSON.stringify({online:false});assert.equal(model.canForwardTo(target,true),false);
 model.sessions=[];assert.equal(model.canForwardTo(target),false);
});

test('an exact old rename lookup queries only the current owner, machine and source, and rejects a changed connection',async()=>{
  const {model}=fixture(), args=[],id='30000000-0000-4000-8000-000000000001',scope=model.scope(),group=model.profile.id;
  const target=JSON.stringify([scope,group,model.profile.manager,'Test %_ host','herdr','old','/fixture/checkout','rename']);
  const key=model.captureView('worktree-request:'+target).storageKey;
  model.local.keys=async(...input)=>{args.push(input);return [key,key.replace(',"rename"]',',"clean"]'),'view:somewhere-else:'+target];};
  assert.deepEqual(await model.savedRenameTargets(group,model.profile.manager,'Test %_ host','herdr',id),[target]);
  assert.deepEqual(args,[[ 'view:'+scope+':worktree-request:'+JSON.stringify([scope,group,model.profile.manager,'Test %_ host','herdr']).slice(0,-1)+',',id ]]);
  model.local.keys=async()=>{model.generation++;return [key];};
  await assert.rejects(model.savedRenameTargets(group,model.profile.manager,'Test %_ host','herdr',id),/request_updated/);
});

test('the composer can send a quote without an extra note and keeps attachments ahead of the context-only note', async () => {
  const {model,writes}=fixture();
  model.features=['persona_contracts_v1'];model.draft='   ';
  model.sourceContext=JSON.stringify({contexts:[{kind:'quote',title:'Claude',msg:'reply-42',part:'selection',text:'one selected line'}]});
  assert.equal(model.hasSendableContext(),true);
  const prepared=model.prepareMessageText();
  assert.match(prepared,/<persona-context kind="quote"[^>]*msg="reply-42" part="selection">\none selected line\n<\/persona-context>\n\nchat_context_only_note$/);
  assert.match(model.prepareMessageText('', 'chat_context_image_note'),/\n\nchat_context_image_note$/);
  assert.equal(await model.send(),true);
  assert.equal(writes.length,1);assert.equal(writes[0][1],prepared);
  for(const context of ['', '{}', '{"contexts":[]}', '{broken']) {
    model.sourceContext=context;model.draft='';assert.equal(model.hasSendableContext(),false);assert.equal(await model.send(),false);
  }
  assert.equal(writes.length,1);
});
