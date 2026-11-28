import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
const state={ready:true,foregrounded:0,background:0,activated:0,recordingStops:0,dictationStops:0,lookUnbinds:0,localeAnnouncements:0,language:'default',notifications:[],xiaoYiRoutes:[],query:Promise.resolve(),init:Promise.resolve()};
globalThis.__personaAbilityTest=state;
registerHooks({
 resolve(specifier,context,next){
  if(context.parentURL?.endsWith('/EntryAbility.ets')){
   if(specifier==='../core/WireCompatibility')return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
   if(specifier==='../core/XiaoYiRoutes')return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
   if(specifier==='../models/Wire')return {url:new URL(specifier+'.ets',context.parentURL).href,shortCircuit:true};
   return {url:'ability-test:'+specifier,shortCircuit:true};
  }
  return next(specifier,context);
 },
 load(url,context,next){
  if(url.startsWith('ability-test:'))return {format:'module',shortCircuit:true,source:`const s=globalThis.__personaAbilityTest;
   export const phoneTextSize={attach(){},refresh(scale){s.fontScale=scale;},detach(){}};
   export const phonePasswordLogin={receive(uri){if(!s.expectPasswordReturn || uri!==s.expectPasswordReturn)return false;s.receivedPasswordReturn=uri;return true;}};
   export const AbilityConstant={},ConfigurationConstant={ColorMode:{COLOR_MODE_LIGHT:1,COLOR_MODE_DARK:2,COLOR_MODE_NOT_SET:0}},Want={},common={};
   export class UIAbility {context={config:{fontSizeScale:1},resourceManager:{},getApplicationContext:()=>({setColorMode(){}})}}
   export const window={};export const Strings={bind(){},refresh(){}};
   export const XiaoYiRuntimeContext={bind(){s.contextBound=(s.contextBound||0)+1;}};
   export const IntentSuggestionsService={async clearAll(){s.suggestionsCleared=(s.suggestionsCleared||0)+1;},scheduleStatusShare(ctx,p,w){s.scheduledSuggestions=s.scheduledSuggestions||[];s.scheduledSuggestions.push({ctx,p,w});},resetSchedule(){s.suggestionsScheduleReset=(s.suggestionsScheduleReset||0)+1;}};
   export const ShareReceiver={isShareWant(w){return w?.action==='ohos.want.action.sendData';},async parseWant(w){const t=w?.parameters?.text||'';return t?{hasContent:true,title:'',text:t,url:'',draftText:t,route:{dest:'share',profile:'',manager:'',machine:'',session:'',autoStart:false,text:t,title:'',url:''}}:{hasContent:false,title:'',text:'',url:'',draftText:''};}};
   export const nativeMeetingServices={async initialize(){s.meetingRefreshes=(s.meetingRefreshes||0)+1},setRecordingEvent(){},
     receive(parameters){if(parameters.personaMeetingType==='persona.meeting.reminder' && parameters.occurrence)s.meetingOpens=(s.meetingOpens||0)+1}};
   export const NOTIFICATION_ACTION='persona.notification.OPEN';export const nativeNotifications={refresh:()=>s.query};
   export const LIVE_VIEW_ACTION='persona.liveview.OPEN';export const nativeLiveView={async stopNow(){}};
   export const deviceFeatures={async background(){},async foreground(){}};
   export const NativeDictation={cancelActive(){s.dictationStops++;}};
   export const KnowledgeRecording={async backgroundAll(){s.recordingStops++;}};
   export const deviceLook={async initialize(){},async bind(owner){if(owner===undefined)s.lookUnbinds++;}};
   export const nativeEnvironment={update(){},bindWindow(){},unbindWindow(){}};
   export const ClientModel={registerForgetHook(fn){s.forgetHook=fn;},registerStatusHook(fn){s.statusHook=fn;}};
   export const client={get ready(){return s.ready;},set ready(value){s.ready=value;},appearance:0,get language(){return s.language;},announceLanguage(){s.localeAnnouncements++;},error:'',initialize:()=>s.init,activateForeground(){s.activated++;},async foregrounded(){s.foregrounded++;},async background(){s.background++;},async shutdown(){s.background++;},async openNotification(target){s.notifications.push(target);},openXiaoYiRoute(route){s.xiaoYiRoutes.push(route);}};
  `};
  if(url.endsWith('.ets')||url.endsWith('.ts'))return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,''))};
  return next(url,context);
 }
});
const {default:EntryAbility}=await import('../entry/src/main/ets/entryability/EntryAbility.ets');
const turn=()=>new Promise(resolve=>setTimeout(resolve,0));
function reset(){Object.assign(state,{ready:true,foregrounded:0,background:0,activated:0,recordingStops:0,dictationStops:0,lookUnbinds:0,localeAnnouncements:0,language:'default',notifications:[],xiaoYiRoutes:[],scheduledSuggestions:[],suggestionsScheduleReset:0,query:Promise.resolve(),init:Promise.resolve()});}
test('a foreground permission query cannot reopen a connection after background or destroy',async()=>{
 for(const stop of ['onBackground','onDestroy']){
  reset();let release;state.query=new Promise(resolve=>release=resolve);const ability=new EntryAbility();ability.onForeground();ability[stop]();release();await turn();
  assert.equal(state.foregrounded,0);assert.equal(state.background,1);
  assert.equal(state.recordingStops,1);assert.equal(state.dictationStops,1);assert.equal(state.lookUnbinds,1);
 }
});
test('a notification arriving during cold initialization is retained and dispatched once ready',async()=>{
 reset();state.ready=false;let release;state.init=new Promise(resolve=>release=resolve);const ability=new EntryAbility();ability.onCreate({},{});
 const target={profile:'hub',manager:'personal',machine:'machine',session:'session'};ability.onNewWant({action:'persona.notification.OPEN',parameters:target},{});assert.equal(state.notifications.length,0);
 state.ready=true;release();await turn();assert.deepEqual(state.notifications,[target]);
});

test('matching system language reannounces live Hub locale after a configuration change',()=>{reset();const ability=new EntryAbility();ability.onConfigurationUpdate({});assert.equal(state.localeAnnouncements,1);state.language='ja-JP';ability.onConfigurationUpdate({});assert.equal(state.localeAnnouncements,1);});

test('a meeting reminder opens the recording page intent without starting a microphone or publishing a transcript',()=>{
 const a=new EntryAbility();state.meetingOpens=0;
 a.onNewWant({parameters:{personaMeetingType:'persona.meeting.reminder',occurrence:'synthetic-1'}},{});
 assert.equal(state.meetingOpens,1);
 a.onNewWant({parameters:{personaMeetingType:'not-a-reminder',occurrence:'synthetic-2'}},{});
 assert.equal(state.meetingOpens,1);
});

test('a matching system-browser return reaches the running password flow before pairing or navigation', () => {
 reset();
 const ability = new EntryAbility();
 const callback = 'persona://password-login?state=fixture';
 state.expectPasswordReturn = callback;
 try {
  ability.onNewWant({uri: callback}, {});
  assert.equal(state.receivedPasswordReturn, callback);
  assert.notEqual(state.pairingLink, callback);
 } finally {
  delete state.expectPasswordReturn;
  delete state.receivedPasswordReturn;
 }
});

test('a native text-size configuration is forwarded to the local display preference',()=>{reset();const ability=new EntryAbility();ability.onConfigurationUpdate({fontSizeScale:1.45});assert.equal(state.fontScale,1.45);});

test('a XiaoYi deep link arriving during cold initialization is retained and dispatched once ready', async () => {
 reset(); state.ready = false; let release; state.init = new Promise(resolve => release = resolve);
 const ability = new EntryAbility();
 ability.onCreate({ uri: 'persona://open?dest=session&profile=hub-1&manager=personal&machine=m1&session=s1' }, {});
 assert.equal(state.xiaoYiRoutes.length, 0);
 state.ready = true; release(); await turn();
 assert.equal(state.xiaoYiRoutes.length, 1);
 const parsed = JSON.parse(state.xiaoYiRoutes[0]);
 assert.equal(parsed.dest, 'session');
 assert.equal(parsed.machine, 'm1');
 assert.equal(parsed.session, 's1');
});

test('a pairing link still routes to pairing and never enters the XiaoYi deep-link queue', () => {
 reset();
 const ability = new EntryAbility();
 ability.onNewWant({ uri: 'persona://pair?host=example.com&code=123456' }, {});
 assert.equal(state.xiaoYiRoutes.length, 0);
});

test('an incoming system share Want survives cold onCreate -> onForeground transition without being dropped', async () => {
 reset();
 state.ready = false;
 let releaseInit;
 state.init = new Promise(resolve => releaseInit = resolve);
 const ability = new EntryAbility();
 ability.onCreate({ action: 'ohos.want.action.sendData', parameters: { text: 'Cold shared note' } }, {});
 ability.onForeground();
 await turn();
 assert.equal(state.xiaoYiRoutes.length, 0);
 state.ready = true;
 releaseInit();
 await turn();
 assert.equal(state.xiaoYiRoutes.length, 1);
 const parsed = JSON.parse(state.xiaoYiRoutes[0]);
 assert.equal(parsed.dest, 'share');
 assert.equal(parsed.text, 'Cold shared note');
});

test('an incoming system share Want stages a share draft route without auto-sending', async () => {
 reset();
 const ability = new EntryAbility();
 ability.onNewWant({ action: 'ohos.want.action.sendData', parameters: { text: 'Shared article excerpt' } }, {});
 await turn();
 assert.equal(state.xiaoYiRoutes.length, 1);
 const parsed = JSON.parse(state.xiaoYiRoutes[0]);
 assert.equal(parsed.dest, 'share');
 assert.equal(parsed.text, 'Shared article excerpt');
});

test('EntryAbility registers XiaoYi forget/status hooks onCreate and resets suggestion timers onDestroy', () => {
 reset();
 const ability = new EntryAbility();
 ability.onCreate({}, {});
 assert.equal(typeof state.forgetHook, 'function');
 assert.equal(typeof state.statusHook, 'function');
 const ctx = { tag: 'ctx' };
 state.statusHook(2, 1, ctx);
 assert.deepEqual(state.scheduledSuggestions, [{ ctx, p: 2, w: 1 }]);
 ability.onDestroy();
 assert.equal(state.suggestionsScheduleReset, 1);
});
