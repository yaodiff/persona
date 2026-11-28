import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {registerHooks,stripTypeScriptTypes} from 'node:module';

registerHooks({
  resolve(spec,ctx,next) {
    if(ctx.parentURL?.endsWith('.ets') && spec.startsWith('.')) return {url:new URL(spec+'.ets',ctx.parentURL).href,shortCircuit:true};
    return next(spec,ctx);
  },
  load(url,ctx,next) {
    if(!url.endsWith('.ets')) return next(url,ctx);
    const code=readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,'');
    return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(code)};
  }
});
const {PhoneMeetingOptions,meetingCaptureHeight,meetingDefaultTitle,meetingPlaceholderVp}=await import('../entry/src/main/ets/models/PhoneMeetingOptions.ets');
const {MeetingHubAudio,MeetingHubChoice}=await import('../entry/src/main/ets/models/MeetingHubAudio.ets');
const source=readFileSync(new URL('../entry/src/main/ets/components/KnowledgeWorkspacePage.ets',import.meta.url),'utf8');
const recording=readFileSync(new URL('../entry/src/main/ets/components/KnowledgeRecordingView.ets',import.meta.url),'utf8');
const strings=new Map(JSON.parse(readFileSync(new URL('../entry/src/main/resources/en_US/element/string.json',import.meta.url))).string.map(s=>[s.name,s.value]));
const tr=key=>strings.get(key) || key;
const methods=source.slice(source.indexOf('  private resetMeetingMode()'),source.indexOf('  @Local meetingTitle:'))+
  source.slice(source.indexOf('  private requestRecordingStart()'),source.indexOf('  private async readMeetingCalendar('));
const NativeStart=new Function('MeetingHubChoice','meetingAudioNetwork','tr','nativeMeetingServices',
  'return '+stripTypeScriptTypes('class Start {'+methods+'}'));
const settle=()=>new Promise(resolve=>setImmediate(resolve));

function fixture(){
  const network={wifiOnly:true},started=[],dialogs=[],toasts=[],state={generation:1,scope:'hub/personal',service:'Isolated transcription',recordingAllowed:true};
  const page=new (NativeStart(MeetingHubChoice,network,tr,{setRecordingEvent(){}}))();
  const owner={profile:{id:'test',manager:'personal',hubId:'hub',baseUrl:'https://fixture.invalid',pin:'synthetic'},
    connected:true,demo:false,features:['meeting_conversation','meeting_live_view','meeting_transcription'],
    isRecordingModuleAllowed:()=>state.recordingAllowed,
    scope:()=>state.scope,connectionGeneration:()=>state.generation,owns:(g,s)=>g===state.generation && s===state.scope};
  Object.assign(page,{model:owner,recordingScope:()=>state.scope,meetingKind:'meeting',meetingTitle:'Synthetic weekly',
    privateMeeting:false,selectedCalendarEvent:'',roomRole:()=>'off',pageAvailable:true,visible:true,confirmVersion:0,selectedAudioOwner:'',
    meetingNetworkReady:true,meetingSpeech:{offered:true,ready:true,mode:'hub',extensionRef:'isolated/asr',serviceName:()=>state.service,retentionDays:14},
    options:()=>({selectedInput:-1}),defaultRecordingTitle:()=>'Synthetic draft',
    getUIContext:()=>({showAlertDialog:x=>dialogs.push(x),getPromptAction:()=>({showToast:x=>toasts.push(x)})}),loadRecordings:async()=>true,
    recording:{canStart:()=>true,start:async(...args)=>{started.push(args);return true;}}});
  return {page,state,network,started,dialogs,toasts,owner};
}

test('both capture buttons use the same single-meeting admission and selecting a cloud service opens no recorder',async()=>{
  assert.match(source,/onStart:\(\):void => this\.requestRecordingStart\(\)/);
  assert.match(source,/else this\.requestRecordingStart\(\);\s*\}\)\.id\('recordings\.capture'\)/);
  assert.match(recording,/onClick\(\(\) => this\.onStart\(\)\)\.id\('knowledge\.recording\.start'\)/);
  const {page,started,dialogs}=fixture();
  assert.equal(page.pickedHub(),false,'a server default never consents to sending audio');
  page.chooseMode(true);assert.equal(page.pickedHub(),true);assert.deepEqual(started,[]);assert.deepEqual(dialogs,[]);
  page.requestRecordingStart();assert.equal(dialogs.length,1);assert.match(dialogs[0].message,/Isolated transcription/);assert.match(dialogs[0].message,/14 days/);
  dialogs[0].primaryButton.action();dialogs[0].secondaryButton.action();await settle();assert.deepEqual(started,[]);
  page.requestRecordingStart();dialogs[1].secondaryButton.action();dialogs[1].secondaryButton.action();await settle();
  assert.equal(started.length,1);assert.equal(started[0][0],'meeting');assert.equal(started[0][2],'off');
  const choice=started[0][4];assert.ok(choice instanceof MeetingHubChoice);assert.ok(choice.matches(page.model));
  assert.equal(choice.serviceRef,'isolated/asr');assert.equal(choice.serviceName,'Isolated transcription');assert.equal(choice.wifiOnly,true);
  assert.equal(page.pickedHub(),false,'the same choice may not carry into another meeting');
  page.requestRecordingStart();await settle();assert.equal(started.at(-1)[4],undefined);assert.equal(dialogs.length,2);
});

test('requestRecordingStart refuses cleanly when the Hub recording module is off (#15978)',async()=>{
  const {page,state,started,dialogs,toasts}=fixture();
  state.recordingAllowed=false;
  page.requestRecordingStart();
  await settle();
  assert.deepEqual(started,[]);
  assert.deepEqual(dialogs,[]);
  assert.equal(toasts.length,1);
  assert.equal(toasts[0].message,tr('xiaoyi_recording_module_off'));
});

test('an old, unready, offline, unsupported or uninitialized route and a note capture remain on this device',async()=>{
  for(const change of [f=>f.page.meetingSpeech.ready=false,f=>f.page.meetingSpeech.offered=false,f=>f.owner.connected=false,
    f=>f.page.meetingNetworkReady=false,f=>f.page.setMeetingKind('note'),f=>f.owner.features.pop(),f=>f.owner.features.shift(),f=>f.owner.features.splice(1,1)]) {
    const f=fixture();f.page.chooseMode(true);change(f);f.page.chooseMode(true);assert.equal(f.page.pickedHub(),false);
    f.page.requestRecordingStart();await settle();assert.equal(f.dialogs.length,0);assert.equal(f.started.length,1);assert.equal(f.started[0][4],undefined);
  }
});

test('approval cannot outlive its connection, service identity, local network preference, visibility or the selected meeting',async()=>{
  for(const change of [f=>f.state.generation++,f=>f.state.scope='another',f=>f.state.service='Different',f=>f.page.meetingSpeech.extensionRef='new/asr',
    f=>f.page.meetingSpeech.ready=false,f=>f.network.wifiOnly=false,f=>f.page.visible=false,f=>f.page.setMeetingKind('note'),
    f=>f.page.meetingTitle='Different',f=>f.page.privateMeeting=true,f=>f.owner.profile.pin='different',f=>f.page.resetMeetingMode()]){
    const f=fixture();f.page.chooseMode(true);f.page.requestRecordingStart();change(f);f.dialogs[0].secondaryButton.action();await settle();
    assert.deepEqual(f.started,[]);
  }
  const f=fixture();let resume;f.page.loadRecordings=()=>new Promise(done=>resume=done);
  f.page.chooseMode(true);f.page.requestRecordingStart();f.dialogs[0].secondaryButton.action();
  f.state.service='Different';resume(true);await settle();assert.deepEqual(f.started,[]);assert.equal(f.page.recording.error,'request_updated');
});

test('the picker, privacy statements and status labels resolve in all four shipped resource configurations',()=>{
  for(const locale of ['base','en_US','zh_CN','ja_JP']){
    const words=new Map(JSON.parse(readFileSync(new URL('../entry/src/main/resources/'+locale+'/element/string.json',import.meta.url))).string.map(s=>[s.name,s.value]));
    for(const phase of ['negotiating','connecting','live','reconnecting','waiting_network','fallback','finishing','final','failed','revoked','previous'])
      assert.ok(words.has('meeting_audio_'+phase),locale+' '+phase);
    for(const key of ['meeting_cloud_service','meeting_start_privacy_cloud','meeting_start_privacy_cloud_days'])
      assert.ok(words.get(key).includes('{service}'),locale+' '+key);
    for(const key of ['meeting_start_privacy_cloud_days','meeting_start_privacy_local_days','meeting_room_privacy_local_days'])assert.ok(words.get(key).includes('{days}'));
    assert.ok(words.get('meeting_room_text_shared'));
    assert.ok(words.has('meeting_configured_service'));
  }
});

test('inviting Persona explains where the text goes and keeps the actual audio destination in the start confirmation',()=>{
  const {page,dialogs}=fixture();
  page.roomRole=()=>'listener';
  assert.equal(page.modePrivacy(false),tr('meeting_room_privacy_local_days').replace('{days}','14'));
  page.chooseMode(true);
  page.requestRecordingStart();
  const cloud=tr('meeting_start_privacy_cloud_days').replace('{service}','Isolated transcription').replace('{days}','14');
  assert.equal(dialogs[0].message,cloud+' '+tr('meeting_room_text_shared'));
  page.meetingSpeech.retentionDays=0;
  assert.equal(page.modePrivacy(false),tr('phone_meeting_live_note'));
  assert.equal(page.modePrivacy(true),tr('meeting_start_privacy_cloud').replace('{service}','Isolated transcription')+' '+tr('meeting_room_text_shared'));
  page.roomRole=()=>'off';
  assert.equal(page.modePrivacy(false),tr('phone_record_audio_local'));
});

test('the meeting shows one identified transcript, choosing the device or hub that actually produced each line',()=>{
  const methods=recording.slice(recording.indexOf('  private hasHubTranscription('),recording.indexOf('  @Builder hubTranscription('));
  const View=new Function('return '+stripTypeScriptTypes('class SourceView {'+methods+'}'))();
  const view=new View(),audio=new MeetingHubAudio();
  view.model={hubAudio:audio,room:{latest:'Phone sentence'},entry:{roomRole:'listener',askedTranscription:'hub'}};
  audio.phase='live';audio.latestLine='Hub sentence';audio.caption='Temporary hub words';
  assert.equal(view.currentLine(),'Hub sentence');assert.equal(view.currentCaption(),'Temporary hub words');assert.equal(view.phoneLines(),false);
  audio.problem='no_interim';assert.equal(view.currentLine(),'Hub sentence');assert.equal(view.currentCaption(),'');
  audio.problem='upstream_unavailable';assert.equal(view.currentLine(),'Phone sentence');assert.equal(view.currentCaption(),'');
  audio.problem='';assert.equal(view.currentCaption(),'Temporary hub words');
  view.model.entry.roomRole='off';assert.equal(view.currentLine(),'Phone sentence');assert.equal(view.currentCaption(),'Temporary hub words');
  view.model.entry.roomRole='participant';
  for(const phase of ['waiting_network','reconnecting','fallback','failed','revoked']){
    audio.phase=phase;assert.equal(view.currentLine(),'Phone sentence');assert.equal(view.currentCaption(),'');
  }
  audio.phase='final';assert.equal(view.currentLine(),'Phone sentence');assert.equal(view.currentCaption(),'');
  assert.match(recording,/!this\.hasHubTranscription\(\) && this\.model\.room\.latest/);
  assert.match(recording,/this\.model\.room\.active && this\.model\.entry\?\.roomRole!=='off'/);
});

test('the title uses the mobile date and minutes for the real locale and only its placeholder takes the compact text size',()=>{
  const time=new Date(2026,10,22,21,9);
  const zh=meetingDefaultTitle('meeting',time,'zh-CN','会议','笔记');
  assert.match(zh,/会议 · 2026年11月22日 21:09/);
  assert.match(meetingDefaultTitle('note',time,'en-US','Meeting','Note'),/Note · Nov 22, 2026 9:09 PM/);
  assert.match(meetingDefaultTitle('meeting',time,'ja-JP','会議','ノート'),/^会議 · .*2026.*11.*22.*21:09/);
  const title=source.slice(source.indexOf('TextInput({text:this.meetingTitle'),source.indexOf(".id('recordings.title')"));
  assert.match(title,/placeholderFont\(\{size:this.placeholderSize\(\)/);
  assert.match(title,/onAreaChange[^\n]*recordingTitleWidth=Number\(next.width\)/);
  assert.equal(meetingPlaceholderVp(326,375,23),'19.2vp');
  assert.equal(meetingPlaceholderVp(380,335,23),'23vp');
  assert.ok(parseFloat(meetingPlaceholderVp(258,375,23))*375/23<=246);
  assert.doesNotMatch(title,/\.maxFontScale/);
});

test('the meeting start action scales its real height and gives a short form room to scroll',()=>{
  assert.equal(meetingCaptureHeight(1,700),58);
  assert.ok(Math.abs(meetingCaptureHeight(3.2,700)-71.8)<.02);
  assert.equal(meetingCaptureHeight(3.2,143),55);
  assert.equal(meetingCaptureHeight(3.2,115),44);
  assert.ok(meetingCaptureHeight(1.2,700)>58);
});

test('an idle meeting menu only offers the offline language and physical inputs reported for its own visible draft',async()=>{
  const form=new PhoneMeetingOptions(),old={},next={},marks=[];
  let active=true,resolve,inputs=[{id:11,bluetooth:false,name:''},{id:21,bluetooth:true,name:'Synthetic headset'}];
  form.bind(old,true,true,false,()=>active,value=>marks.push(value),()=>new Promise(done=>resolve=done),()=>inputs);
  form.togglePrivate();form.togglePrivate();assert.deepEqual(marks,[true,false]);
  form.selectInput(21);assert.equal(form.selectedInput,21);inputs=[];await form.prepare();assert.equal(form.selectedInput,-1);
  form.clear(old);form.bind(next,true,false,false,()=>active,()=>assert.fail(),async()=>['zh-CN'],()=>[]);
  resolve(['en-US','ja-JP']);await settle();assert.equal(form.languageAvailable,true);assert.deepEqual(form.inputs,[]);
  form.togglePrivate();assert.equal(form.marked,false);
  active=false;form.selectInput(21);assert.equal(form.selectedInput,-1);
  form.clear(next);assert.equal(form.available,false);assert.equal(form.languageAvailable,false);
});

test('the native recording header uses interface body type while its full profile remains a reading surface',()=>{
  const detail=source.slice(source.indexOf('  @Builder recordingDetail()'),source.indexOf(".id('knowledge.recording.detail')"));
  assert.match(detail,/Text\(this\.workspace\.selected\.detail\)\.fontSize\(Tokens\.body\)\s*\.maxFontScale\(Tokens\.interfaceScaleFor\(Tokens\.body\)\)/);
  assert.match(detail,/MarkdownText\(\{text:Wire\.text\(this\.workspace\.detail,'summary'\)/);
  assert.match(detail,/\.id\('knowledge.recording.intro'\)/);
});


test('the recording detail keeps its actual path explanation and its minutes entry alongside the source-bound reading profile',()=>{
  const block=source.slice(source.indexOf('  @Builder recordingDetail()'),source.indexOf(".id('knowledge.recording.detail')"));
  assert.match(block,/if \(Wire\.text\(this\.workspace\.detail,'path'\)\) \{[\s\S]*?recordingFootnote\('recording_detail_folder_access'\)/);
  assert.match(block,/MarkdownText\(\{text:Wire\.text\(this\.workspace\.detail,'summary'\),recapReading:true/);
  assert.match(block,/this\.recordingToolsButton\(\)/);
  assert.match(source.slice(source.indexOf('  @Builder recordingToolsButton()'),source.indexOf('  @Builder recordingToolContents()')),/\.id\('knowledge\.recording\.tools'\)/);
  const messages=['base','en_US','ja_JP','zh_CN'].map(locale=>JSON.parse(readFileSync(new URL('../entry/src/main/resources/'+locale+'/element/recording_detail.json',import.meta.url),'utf8')).string);
  for (const entries of messages) assert.ok(entries.find(item=>item.name==='recording_detail_folder_access')?.value.length>16);
});

test('the recording facts sheet returns to its own entry and permits an explicit dirty close without a write',()=>{
  const body=source.slice(source.indexOf('  @Builder recordingFactsEditor()'),source.indexOf('  private recordingMetric('));
  assert.match(body,/onClick\(\(\) => this\.facts\.close\(\)\)\.id\('knowledge\.facts\.cancel'\)/);
  assert.match(body,/onClick\(\(\) => this\.submitFacts\(\)\)\.id\('knowledge\.facts\.save'\)/);
  assert.match(source,/\.bindSheet\(this\.facts\.open,this\.recordingFactsEditor\(\)/);
  assert.match(source,/onWillDismiss:\(action:DismissSheetAction\) => \{ if \(!this\.workspace\.busy && !this\.factsChanged\(\)\)/);
});

test('deleting a confirmed recording fact dismisses immediately and only its owner can undo the successful write',async()=>{
  const body=source.slice(source.indexOf('  private async deleteFacts()'),source.indexOf('  @Builder recordingUndoNotice()'));
  const timers=[],Actions=new Function('setTimeout',stripTypeScriptTypes('class RecordingActions { '+body+' }')+'; return RecordingActions;')((fn,duration)=>timers.push({fn,duration}));
  const actions=new Actions(),writes=[];actions.removedFactsNotice=0;
  let owner='meeting A',finish;
  actions.factsOwner=()=>owner;
  actions.facts={baseline:'initial fact',open:true,close(){this.open=false;}};
  actions.workspace={busy:false,saveFacts:(value,expected)=>{
    writes.push([value,expected]);return new Promise(resolve=>{finish=resolve;});
  }};
  const first=actions.deleteFacts();assert.equal(actions.facts.open,false);
  owner='meeting B';finish(true);await first;assert.equal(actions.removedFacts,undefined);
  actions.facts.open=true;actions.facts.text='   ';owner='meeting A';
  const second=actions.submitFacts();finish(true);await second;
  assert.equal(actions.removedFacts,'initial fact');
  owner='meeting B';await actions.undoFacts();assert.equal(writes.length,2);
  owner='meeting A';const undo=actions.undoFacts();finish(true);await undo;
  assert.deepEqual(writes,[['','initial fact'],['','initial fact'],['initial fact','']]);
  assert.equal(actions.removedFacts,'');
  actions.facts.open=true;const next=actions.deleteFacts();finish(true);await next;
  assert.deepEqual(timers.map(timer=>timer.duration),[5000,5000]);
  timers[0].fn();assert.equal(actions.removedFacts,'initial fact');
  timers[1].fn();assert.equal(actions.removedFacts,'');assert.equal(writes.length,4);
  assert.match(source,/Stack\(\{alignContent:Alignment\.Top\}\) \{\s*this\.recordingDetailScroll\(\)\s*this\.recordingUndoNotice\(\)/);
});

test('the phone KB detail ends after the folder and opens its original tools through the tag sheet',()=>{
 const section=(from,to)=>source.slice(source.indexOf(from),source.indexOf(to,source.indexOf(from)+from.length));
 const detail=section('  @Builder recordingDetailScroll()', '  @Builder detailContent(');
 const sheet=section('  @Builder metadataForm()','  @Builder editForm()');
 const tools=section('  @Builder recordingToolContents()', '  @Builder recordingDetail()');
 assert.match(detail,/if \(!this\.phoneRecordingDetail\)\s*\{\s*this\.recordingToolsButton\(\)\s*if \(this\.recordingTools\) this\.recordingToolContents\(\)/);
 assert.match(sheet,/if \(this\.phoneRecordingDetail && this\.recordingTools\)/);
 assert.match(sheet,/this\.editForm\(\)\s*if \(this\.phoneRecordingDetail\) this\.recordingToolsButton\(\)/);
 assert.match(sheet,/onClick\(\(\) => this\.recordingTools=false\)/);
 assert.doesNotMatch(sheet,/reset|saveMetadata|deleteRecording|beginEdit\(\)|editOpen\s*=/);
 assert.match(detail,/dismiss\.reason===DismissReason\.PRESS_BACK\) this\.recordingTools=false/);
 assert.match(tools,/this\.recordingSection\(/);assert.match(tools,/this\.relatedEntries\(/);
 assert.match(tools,/this\.onOpenResource\(item\)/);assert.match(tools,/this\.workspace\.canDeleteRecording\(\)\)\.onClick\(\(\) => this\.confirmDeleteRecording\(\)/);
 const shell=readFileSync(new URL('../entry/src/main/ets/pages/Index.ets',import.meta.url),'utf8');
 assert.match(shell,/phoneRecordingDetail:name==='knowledge' && !this\.wide\(\)/);
});

test('imported KB recordings expose source notes only for the actual linked meeting with a supported URL',async()=>{
 const {Wire}=await import('../entry/src/main/ets/models/Wire.ets');
 const g=source.slice(source.indexOf('  @Computed get importedRecordingNotes()'),source.indexOf('  private recordingToolsTitle()')).replace('@Computed ','');
 const Card=new Function('Wire','return '+stripTypeScriptTypes('class Card {'+g+'}'))(Wire);
 const p=new Card();p.workspace={detail:{}};
 const ref={kind:'meetings',id:'real',extra:' https://example.test/notes ',source:'larkcli'};
 for(const detail of [{},{meta:{imported_meeting:{id:'real'}}},{meta:{imported_meeting:{id:'other'}},links:[ref]},
   {meta:{imported_meeting:{id:'real'}},links:[{...ref,kind:'docs'}]},{meta:{imported_meeting:{id:'real'}},links:[{...ref,extra:''}]},
   {meta:{imported_meeting:{id:'real'}},links:[{...ref,extra:'file:///not-a-web-link'}]}]){
   p.workspace.detail=detail;assert.equal(p.importedRecordingNotes,undefined);
 }
 p.workspace.detail={meta:{imported_meeting:{id:'real'}},links:[{...ref,id:'unrelated'},ref]};
 assert.equal(p.importedRecordingNotes,ref);
});

test('the recording tool sheet keeps the explicit deletion dialog and original confirmed owner',()=>{
 const s=source.slice(source.indexOf('  private confirmDeleteRecording()'),source.indexOf('  private confirmDispatch()'));
 const Card=new Function('tr','return '+stripTypeScriptTypes('class Card {'+s+'}'))(key=>key);const p=new Card();
 let owner='first',reconnect=0,dialog,count=0,allowed=true;
 p.workspace={canDeleteRecording:()=>allowed,selected:{key:()=>owner},deleteRecording:async()=>{count++;return true;}};
 p.model={connectionGeneration:()=>reconnect,scope:()=> 'same-group',owns:(gen,scope)=>gen===reconnect && scope==='same-group'};
 p.getUIContext=()=>({showAlertDialog:d=>{dialog=d;}});
 p.confirmDeleteRecording();assert.equal(dialog.title,'knowledge_forget_recording');assert.equal(dialog.message,'knowledge_forget_recording_note');
 assert.equal(count,0);dialog.primaryButton.action();assert.equal(count,0);
 owner='second';dialog.secondaryButton.action();assert.equal(count,0);
 p.confirmDeleteRecording();reconnect++;dialog.secondaryButton.action();assert.equal(count,0);
 p.confirmDeleteRecording();dialog.secondaryButton.action();assert.equal(count,1);
 dialog=undefined;allowed=false;p.confirmDeleteRecording();assert.equal(dialog,undefined);assert.equal(count,1);
});
