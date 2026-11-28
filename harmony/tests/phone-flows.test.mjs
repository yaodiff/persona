import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import test from 'node:test';

registerHooks({
  resolve(spec, ctx, next) {
    if ((spec === '../core/PhoneSearchExcerpt' || spec === '../core/SessionNaming') && ctx.parentURL?.endsWith('.ets'))
      return {url:new URL(spec+'.ts',ctx.parentURL).href,shortCircuit:true};
    if (spec.startsWith('.') && ctx.parentURL?.endsWith('.ets'))
      return {url:new URL(spec+'.ets',ctx.parentURL).href,shortCircuit:true};
    return next(spec,ctx);
  },
  load(url,ctx,next) {
    if (!url.endsWith('.ets')) return next(url,ctx);
    const src=readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*|@Trace\s+/g,'');
    return {format:'module',source:stripTypeScriptTypes(src),shortCircuit:true};
  }
});
const {phonePlaceAtRoot,phonePanIntent,finishesPhonePan}=await import('../entry/src/main/ets/models/PhoneNavigation.ets');
const {PhoneSearchIndex,PhoneChatMatch,mergeChatMatches,localChatMatches}=await import('../entry/src/main/ets/models/PhoneChatSearch.ets');
const {phoneSearchExcerpt,searchHitRanges}=await import('../entry/src/main/ets/core/PhoneSearchExcerpt.ts');

test('sidebar search keeps the actual UTF-16 hits visible and bold across the phone type sizes',()=>{
  const sentence='顺便把 PR #42 的 review 意见处理掉',ranges=searchHitRanges([[4,6],[13,19],['wrong',2],[1],[2.8,4]]);
  const excerpt=(scale)=>phoneSearchExcerpt(sentence,ranges,scale);
  assert.equal(excerpt(1).map(s=>s.text).join(''),sentence);
  assert.equal(excerpt(3.2).map(s=>s.text).join(''),'…把 PR #42 的 review 意见处理掉');
  assert.equal(excerpt(3.2).filter(s=>s.matched)[0].text,'PR');
  const unicode='abc👩‍💻e\u0301🧑‍🚀XY rest';
  const at=unicode.indexOf('XY');
  assert.deepEqual(phoneSearchExcerpt(unicode,[[at,at+2]],3.2),[
    {text:'…e\u0301🧑‍🚀',matched:false},{text:'XY',matched:true},{text:' rest',matched:false}]);
  assert.deepEqual(phoneSearchExcerpt(unicode,[[4,5],[90,41]],3.2),[{text:unicode,matched:false}]);
  assert.deepEqual(phoneSearchExcerpt('first abcd',[[6,9],[7,10]],1),[{text:'first ',matched:false},{text:'abcd',matched:true}]);
  assert.deepEqual(phoneSearchExcerpt(sentence,[],3.2),[{text:sentence,matched:false}]);
});
const {Tokens}=await import('../entry/src/main/ets/design/Tokens.ets');

test('the in-chat recap preview and its date follow interface type while the full recap keeps reading type', () => {
  const source = readFileSync(new URL('../entry/src/main/ets/components/ConversationRecap.ets', import.meta.url), 'utf8');
  const marker = source.slice(source.indexOf('@Builder marker'), source.indexOf('@Builder strip'));
  const strip = source.slice(source.indexOf('@Builder strip'), source.indexOf('@Builder details'));
  const details = source.slice(source.indexOf('@Builder details'), source.indexOf('  build()'));
  assert.match(marker, /Text\('· ' \+ this\.relative\(items[^\n]+\n\s*\.fontSize\(Tokens.caption\)\.maxFontScale\(Tokens.interfaceScaleFor\(Tokens.caption\)\)/);
  assert.match(strip, /Text\(markdownVisibleText\(parseMarkdown\(item.text\)\)\)\.fontSize\(15\)\.maxFontScale\(Tokens.interfaceScaleFor\(15\)\)/);
  assert.match(strip, /\.lineHeight\(Tokens.interfaceFontVp\(19.5, nativeEnvironment.fontScale\)\)/);
  for (const icon of strip.matchAll(/SymbolGlyph[^\n]+/g)) {
    assert.match(icon[0], /\.fontSize\(Tokens.interfaceFontVp\(/);
  }
  assert.equal(Tokens.interfaceFontVp(19.5, 1), '19.5vp');
  assert.equal(Tokens.interfaceFontVp(19.5, 3.2), '25.5vp');
  assert.match(details, /MarkdownText\(\{ text: item.text, chatReply: true/);
});

test('native phone search sizes the entered text and placeholder together and preserves the iPhone interface limit at maximum type',()=>{
  for (const [scale,vp] of [[0.85,13.6],[1,16],[1.2,19.2],[1.5,22],[3.2,22]]) {
    assert.equal(Tokens.interfaceFontVp(16,scale),String(vp)+'vp');
  }
  const read=name=>readFileSync(new URL('../entry/src/main/ets/'+name+'.ets',import.meta.url),'utf8');
  const query=[read('components/PhoneConversationSearch'),read('components/SettingsDirectory')];
  assert.match(query[0],/TextInput\(\{text:this\.query,[^]*?\.fontSize\(Tokens\.interfaceFontVp\(16,nativeEnvironment\.fontScale\)\)\.placeholderFont\(\{size:Tokens\.interfaceFontVp\(16,nativeEnvironment\.fontScale\)\}\)/);
  assert.match(query[1],/TextInput\(\{text:this\.query,[^]*?\.fontSize\(this\.searchTextSize\(\)\)\.placeholderFont\(\{size:this\.searchTextSize\(\)\}\)/);
  assert.match(query[1],/this\.searchSize\(16\.5,22\)\*Math\.min\(this\.environment\.fontScale,1\)/);
  assert.match(query[0],/phone_search_conversation_count[^]*?\.fontSize\(13\)\.maxFontScale\(Tokens\.interfaceScaleFor\(13\)\)[^]*?id\('phone_search.count'\)/);
  const shell=read('pages/Index');
  const switcher=shell.slice(shell.indexOf('@Builder assistantMenuCommand'),shell.indexOf('@Builder drawerManagerMenu'));
  assert.match(switcher,/Text\(tr\(create \? 'phone_new_assistant' : 'phone_manage_assistants'\) \+ \(create \? '' : '…'\)\)\.fontSize\(Tokens\.interfaceFontVp\(17,this\.environment\.fontScale\)\)/);
  assert.match(switcher,/PhoneStyle\.sidebarMetric\(44,this\.environment\.fontScale\)/);
});
const {AgentLaunchChoices,builtinLauncherTitle,typedAgentDirectory,providerStartModes,launchModelName,recentLaunchDirectories,LaunchChoice,RecentAgentSetup,rememberAgentSetup,loadAgentSetups}=await import('../entry/src/main/ets/models/AgentLaunchChoices.ets');
const {ProjectWorktree}=await import('../entry/src/main/ets/models/ProjectCatalog.ets');
const {ResourceRow,Wire}=await import('../entry/src/main/ets/models/Wire.ets');

test('compact sidebar rows, glyphs and their hit targets share the interface scale', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/design/PhoneStyle.ets',import.meta.url),'utf8');
  const metric=source.match(/static sidebarMetric\([^]*?\{ (return [^}]+) \}/)[1];
  const measure=new Function('points','scale',metric);
  assert.equal(measure(44,1),44);
  assert.equal(measure(44,1.2),52.8);
  assert.ok(Math.abs(measure(44,3.2)-57.85)<0.01);
  assert.ok(Math.abs(measure(46,3.2)-60.5)<0.1);
  const shell=readFileSync(new URL('../entry/src/main/ets/pages/Index.ets',import.meta.url),'utf8');
  const places=shell.slice(shell.indexOf('@Builder spaceRow(kind:'),shell.indexOf('  private measureSpaces'));
  assert.match(places,/this\.spaceRow\('persona'\)\s+this\.drawerDivider\(\)/);
  assert.match(places,/this\.spaceRow\('devices'\)\s+if \(!this\.wide\(\)\) this\.drawerDivider\(\)/);
  assert.match(places,/SymbolGlyph\(this\.spaceSymbol\(kind\)\)[^\n]*this\.drawerMetric\(24\)/);
  assert.ok(places.includes('constraintSize({minHeight:this.drawerMetric(44)})'));
});

test('All Chats leaves the presenting drawer in place until a conversation is selected', () => {
  const shell=readFileSync(new URL('../entry/src/main/ets/pages/Index.ets',import.meta.url),'utf8');
  const show=shell.match(/private showAllChats\(agents:boolean\): void \{([^}]+)\}/)[1];
  const open=new Function('agents',show);
  for(const openedFromDrawer of [false,true]) {
    const host={spacesShown:openedFromDrawer,drawerSearching:true,chatSearchShown:false};
    open.call(host,false);
    assert.equal(host.spacesShown,openedFromDrawer);
    assert.ok(host.chatSearchShown&&!host.drawerSearching);
    open.call(host,true);
    assert.equal(host.spacesShown,false);
  }
  assert.match(shell,/private openPickedChat\([^}]+this\.spacesShown = false; this\.drawerSearching = false; this\.chatSearchShown = false/);
});

test('the sidebar and full list use the same source and state marks', () => {
  const read=p=>readFileSync(new URL('../entry/src/main/ets/'+p+'.ets',import.meta.url),'utf8');
  for(const path of ['components/PhoneAllChats','pages/Index']) {
    assert.ok(read(path).includes('AssistantChatTitle({title:sidebarConversationTitle(binding.value,tr)'));
    assert.ok(read(path).includes('AssistantChatStateMark({state:assistantChatState(binding.value)'));
  }
  const marks=read('components/AssistantChatMarks');
  assert.ok(marks.includes("this.state === 'needs_you' ? Colors.needsMark : Colors.unreadMark"));
  assert.ok(marks.includes("if (this.state === 'working') LoadingProgress()"));
  for(const [mode,values] of [['base',['#007AFF','#E8890C','#30A46C']],['dark',['#0A84FF','#FFB224','#4CC38A']]]) {
    const colors=JSON.parse(readFileSync(new URL('../entry/src/main/resources/'+mode+'/element/color.json',import.meta.url),'utf8')).color;
    assert.deepEqual(['needs','working','unread'].map(key=>colors.find(row=>row.name==='persona_'+key+'_mark').value),values);
  }
  assert.ok(marks.includes('Flex({direction:FlexDirection.Row') && marks.includes('flexShrink(1).constraintSize({minWidth:0})'));
  assert.ok(marks.includes(".flexShrink(0).accessibilityLevel('no').id(this.identity+'.source')"));
  assert.ok(marks.includes("if (this.source === 'lark_agent') Image($r('app.media.brand_lark'))"));
});

test('drawer roots and drill-downs leave always exactly one leading action',()=>{
  assert.equal(phonePlaceAtRoot(4,[],false),true);assert.equal(phonePlaceAtRoot(4,['project'],false),false);
  assert.equal(phonePlaceAtRoot(0,['conversation'],true),true);assert.equal(phonePlaceAtRoot(0,['conversation'],false),false);
  assert.equal(phonePlaceAtRoot(1,[],false),true);assert.equal(phonePlaceAtRoot(1,['session'],false),false);
  for(const [s,r] of [[2,'resources'],[3,'settings_root'],[5,'resources'],[6,'resources'],[7,'resources'],[8,'settings'],[9,'settings'],[10,'recordings']]){
    assert.equal(phonePlaceAtRoot(s,[r],false),true);assert.equal(phonePlaceAtRoot(s,[r,'other'],false),false);
  }
});
test('horizontal drawer and peer gestures leave pushed pages, vertical scroll and the composer alone',()=>{
  const pan=(section,root,open,x,y,dx,dy)=>phonePanIntent(section,root,open,x,y,390,840,dx,dy);
  assert.equal(pan(0,true,false,25,200,14,2),'open');assert.equal(pan(4,true,true,240,150,-15,2),'close');
  assert.equal(pan(0,true,false,280,200,-18,0),'agents');assert.equal(pan(1,true,false,130,210,18,0),'persona');
  assert.equal(pan(0,true,false,150,150,-18,0),'agents');
  assert.equal(pan(1,true,false,7,150,18,0),'persona');
  assert.equal(pan(0,true,false,200,160,18,14),'open');
  for(const args of [[1,false,false,7,80,18,0],[0,true,false,280,800,-18,0],[0,true,false,280,180,-18,20]])
    assert.equal(pan(...args),'');
  assert.equal(finishesPhonePan(31,800,390),true);assert.equal(finishesPhonePan(32,-900,390),false);
  assert.equal(finishesPhonePan(18,0,390),false);assert.equal(finishesPhonePan(-110,0,390),false);
  assert.equal(finishesPhonePan(-210,0,390),true);assert.equal(finishesPhonePan(-160,-240,390),true);
  assert.equal(finishesPhonePan(-240,425,390),false);
});
function makeOwner(call) {
  return {generation:1,connected:true,profile:{id:'home',manager:'agent'},features:['assistant_search'],conversations:[],
    scope(){return this.profile.id+':'+this.profile.manager},connectionGeneration(){return this.generation},
    owns(g,s){return g===this.generation&&s===this.scope()},repo(){return{call}}};
}
function gate(){let resolve,reject;const promise=new Promise((ok,fail)=>{resolve=ok;reject=fail});return {promise,resolve,reject}}
test('folder choices retain only resolved directories from the current machine and connection',async()=>{
  const pending=new Map();const owner=makeOwner(p=>{const g=gate();pending.set(p,g);return g.promise});
  const choices=new AgentLaunchChoices();const a=choices.folder(owner,'work/mac','/one'),b=choices.folder(owner,'work/mac','/two');
  const [p1,p2]=[...pending.keys()];assert.equal(p1,'/api/v1/machines/work%2Fmac/dirs?path=%2Fone');
  pending.get(p2).resolve({path:'/two',parent:'/',entries:[{name:'project',path:'/two/project',is_git:true}],recent:[{name:'actual',path:'/recent'}]});await b;
  pending.get(p1).resolve({path:'/one',entries:[]});await a;
  assert.equal(choices.path,'/two');assert.equal(choices.directories[0].detail,'Git');assert.equal(choices.recentDirectories[0].id,'/recent');
  const c=choices.folder(owner,'new','/unknown');owner.generation++;
  [...pending.values()].at(-1).resolve({path:'/old-scope'});await c;assert.equal(choices.path,'');
});
test('typed folders resolve under the confirmed machine directory, and a failed read leaves siblings available',async()=>{
  assert.equal(typedAgentDirectory(' src ','/Users/me/code/weather/'),'/Users/me/code/weather/src');
  assert.equal(typedAgentDirectory('one/two','/'),'/one/two');
  assert.equal(typedAgentDirectory('src',''),'~/src');
  for (const input of ['/srv/repo','~','~/code','C:\\work','\\\\server\\share','']) assert.equal(typedAgentDirectory(input,'/other'),input);
  assert.equal(typedAgentDirectory('  ','/work'),'');
  assert.equal(typedAgentDirectory('src','C:\\work'),'C:\\work\\src');
  const choices=new AgentLaunchChoices(),pending=[];
  const owner=makeOwner(url=>{const entry=gate();pending.push({path:new URL(url,'http://fixture').searchParams.get('path'),...entry});return entry.promise});
  const get=path=>choices.folder(owner,'mac',path), response=path=>({path,parent:'/Users/me/code',entries:[{name:'src',path:path+'/src'}]});
  let call=get('/Users/me/code/weather');pending.at(-1).resolve(response('/Users/me/code/weather'));await call;
  const denied=Object.assign(new Error('internal'),{status:502});
  call=get(typedAgentDirectory('nope',choices.path)); assert.equal(choices.path,'/Users/me/code/weather');
  pending.at(-1).reject(denied);await call;assert.equal(choices.error,'phone_directory_read_failed');assert.equal(choices.directories[0].id,'/Users/me/code/weather/src');
  const older=get('/slow');const stale=pending.at(-1);
  const newer=get('/fast');pending.at(-1).resolve(response('/fast'));await newer;stale.reject(denied);await older;
  assert.deepEqual([choices.path,choices.error],['/fast','']);
  call=get('/limited');pending.at(-1).reject(Object.assign(new Error('forbidden'),{status:403}));await call;
  assert.deepEqual([choices.path,choices.directories.length],['',0]);
});
test('the initial missing folder falls back once on its own machine and can never replace a newer machine or Hub',async()=>{
  const choices=new AgentLaunchChoices(),pending=[];
  const call=url=>{const entry=gate();pending.push({url,...entry});return entry.promise};
  const owner=makeOwner(call), other=makeOwner(call);
  const first=choices.folder(owner,'mac','/gone');
  pending.at(-1).reject(Object.assign(new Error('internal'),{status:502})); await Promise.resolve();
  assert.match(pending.at(-1).url,/mac\/dirs\?path=$/); const fallback=pending.at(-1);
  const switched=choices.folder(other,'studio','~');pending.at(-1).resolve({path:'/home/studio',entries:[]});await switched;
  fallback.resolve({path:'/home/mac',entries:[{name:'private',path:'/home/mac/private'}]});await first;
  assert.equal(choices.path,'/home/studio');assert.equal(choices.directories.length,0);
  const offline=choices.folder(owner,'mac','/offline');const calls=pending.length;pending.at(-1).reject(Object.assign(new Error('offline'),{status:0}));await offline;
  assert.equal(pending.length,calls);assert.equal(choices.path,'');
  const saved=choices.folder(owner,'mac','/gone');pending.at(-1).reject(Object.assign(new Error('not found'),{status:404}));await Promise.resolve();
  pending.at(-1).resolve({path:'/home/mac',entries:[{name:'work',path:'/home/mac/work'}]});await saved;
  assert.equal(choices.path,'/home/mac');assert.equal(choices.error,'phone_directory_read_failed');
  const late=choices.folder(owner,'mac','child');owner.generation++;pending.at(-1).resolve({path:'/old',entries:[]});await late;
  assert.equal(choices.path,'');assert.equal(choices.directories.length,0);
});
test('model aliases keep the host title while raw GPT model IDs preserve GPT- prefix', () => {
  assert.equal(launchModelName('gpt-5.5-codex','GPT-5.5-Codex'),'GPT-5.5 Codex');
  assert.equal(launchModelName('gpt-5.5-mini','Team review'),'Team review');
  assert.equal(launchModelName('gateway/glm-5.3','glm-5.3'),'glm-5.3');
  assert.equal(launchModelName('foo/gpt-5.6-fast',''),'GPT-5.6 Fast');
  assert.equal(launchModelName('gpt--mini',''),'gpt--mini');
  assert.equal(builtinLauncherTitle('claude'),'Claude Code');
  assert.equal(builtinLauncherTitle('codex'),'Codex');
  assert.equal(builtinLauncherTitle('claude-desktop'),'Claude Desktop');
  assert.equal(builtinLauncherTitle('codex-app'),'Codex App');
  assert.equal(builtinLauncherTitle('traex','TRAEX'),'TRAE CLI');
  assert.equal(builtinLauncherTitle('agy'),'Antigravity CLI');
  assert.equal(builtinLauncherTitle('claude','My Claude'),'My Claude');
});

test('available models, compatible efforts and launcher-supplied model locks follow the node',async()=>{
  const replies=[{agent:'codex',models:[{id:'m1',name:'Model one',default:true,efforts:['medium','high']},{id:'m2',efforts:['low']}]},
    {agent:'claude',locked:'configured by launcher',models:[]}];
  const paths=[];const owner=makeOwner(async p=>{paths.push(p);return replies.shift()});const c=new AgentLaunchChoices();
  await c.loadModels(owner,'Mac One','code/dev');assert.equal(paths[0],'/api/v1/machines/Mac%20One/launchers/code%2Fdev/models?purpose=agent.new');
  assert.equal(c.acceptsTypedModel,true);assert.deepEqual(c.effortNames,['medium','high']);c.updateEfforts('m2');assert.deepEqual(c.effortNames,['low']);
  await c.loadModels(owner,'Mac One','managed');assert.equal(c.acceptsTypedModel,false);assert.equal(c.locked,'configured by launcher');assert.deepEqual(c.effortNames,[]);
});
test('recent setups deduplicate exact workspace/launcher/model choices and never retain prompt text',()=>{
  const first=Object.assign(new RecentAgentSetup(),{machine:'owner+node',cwd:'/workspace',launcher:'test',model:'m',effort:'high',tree:'w'});
  const other=Object.assign(new RecentAgentSetup(),{machine:'owner+node',cwd:'/else'});
  assert.deepEqual(rememberAgentSetup([other,first],first),[first,other]);
  assert.deepEqual(loadAgentSetups('{"entries":[{"machine":"m","cwd":"/a","prompt":"private"},{"machine":""}]}').map(x=>[x.machine,x.cwd,x.prompt]),[['m','/a',undefined]]);
});

test('model detail follows the configured launcher, explicit preset or typed model and the older catalog default',async()=>{
  const word=key=>({phone_start_configured_model:'Model Settings: {model}',phone_start_typed_model_id:'Typed model ID: {id}'}[key]??key);
  const choices=new AgentLaunchChoices(), catalog={agent:'claude',models:[
    {id:'local-default',name:'Local',default:true,resolved:'launcher-model-5'},
    {id:'chosen',name:'Quality',resolved:'remote-model'},{id:'remembered',name:'remembered',custom:true}
  ]};
  await choices.loadModels(makeOwner(async()=>catalog),'mac','claude');
  const view=(id,setting,launcher={},explicit=false)=>choices.modelPresentation(id,setting,launcher,explicit,word);
  assert.deepEqual([view('','group/model').title,view('','group/model').detail],['phone_start_default_model','Model Settings: group/model']);
  assert.deepEqual([view('chosen','group/model').title,view('chosen','group/model').detail],['Quality','chosen → remote-model']);
  assert.equal(view('local-default','group/model').detail,'local-default → launcher-model-5');
  assert.equal(view('remembered','group/model').detail,'phone_start_typed_model');
  assert.match(view('gpt-5.5','group/model').detail,/Typed model ID: gpt-5.5/);
  assert.equal(view('','').detail,'launcher-model-5');
  assert.equal(view('','group/model',{name:'omp',source:'path',agent:'omp'},true).detail,'launcher-model-5');
  const offline=new AgentLaunchChoices();assert.equal(offline.modelPresentation('','',{},false,word).detail,'flow_use_default');
  choices.locked='locked by launcher';assert.equal(view('chosen','group/model').detail,'locked by launcher');
});

test('the model picker preserves the host default as one choice and shows the launcher identity and description',async()=>{
  const choices=new AgentLaunchChoices(),owner=makeOwner(async()=>({agent:'codex',models:[
    {id:'first',name:'First',default:true,efforts:['low']},
    {id:'case/second',name:'Second',description:'Use this for reviews',efforts:['high']}
  ]}));
  await choices.loadModels(owner,'mac','coding');
  assert.equal(choices.defaultModelName(),'First');assert.equal(choices.selectedModelId('first'),'');
  assert.deepEqual(choices.models.map(m=>[m.id,m.title,m.detail,m.description]),[
    ['case/second','Second','coding · case/second','Use this for reviews']]);
  choices.updateEfforts('case/second');assert.deepEqual(choices.effortNames,['high']);
});

// Run the real phone form methods under Node, as navigation-context does for the native navigator.
// Only persistence and post-selection presentation are outside this fixture's boundary.
const formSource = readFileSync(new URL('../entry/src/main/ets/pages/Index.ets', import.meta.url), 'utf8');
const formMethods = new Map();
for (const match of formSource.matchAll(/^  private (?:async )?(\w+)\(/gm)) {
  const lineEnd = formSource.indexOf('\n', match.index);
  const end = formSource.slice(match.index, lineEnd).trimEnd().endsWith('}') ? lineEnd : formSource.indexOf('\n  }', lineEnd) + 4;
  formMethods.set(match[1], formSource.slice(match.index, end));
}
const externalEffects = new Set(['saveAgentDraft', 'loadRecentSetups', 'closeAgentPanel', 'projectPlace', 'openLinkedResource', 'openPickedChat']);
const required = new Set(['pickerOptions', 'chooseAgentOption', 'selectedAgentTree', 'openAgentPicker', 'pickAgentMachine',
  'canStartAgent', 'browseAgentFolder', 'loadAgentLaunchers', 'loadInitialAgentDirectory', 'createAgent', 'agentModelHelp', 'launchRowTitle', 'launchRowDetail']);
for (const name of required) {
  const body = formMethods.get(name);
  assert.ok(body, name);
  for (const [, callee] of body.matchAll(/\bthis\.(\w+)\(/g)) {
    if (formMethods.has(callee) && !externalEffects.has(callee)) required.add(callee);
  }
}
const nativeFormDependencies = { Wire, LaunchChoice, RecentAgentSetup, rememberAgentSetup, recentLaunchDirectories, providerStartModes, builtinLauncherTitle,
  tr: key => ({ flow_start_model_terminal_hint:'{about} /model', phone_start_configured_model:'Model Settings: {model}',
    phone_start_typed_model_id:'Typed model ID: {id}' }[key] ?? key) };
const NativeAgentForm = new Function(...Object.keys(nativeFormDependencies), 'return ' + stripTypeScriptTypes(
  'class NativeAgentForm {\n' + [...required].map(name => formMethods.get(name)).join('\n') + '\n}'))(...Object.values(nativeFormDependencies));

test('Other Model keeps an unlisted ID on cancel and confirms one valid model to the current form after dismissal', async () => {
  const {modelIDProblem}=await import('../entry/src/main/ets/models/ModelID.ets');
  const word=key=>key==='flow_model_id_character' ? 'unsupported {character}' : key;
  for (const [id,expected] of [['','flow_model_id_empty'],['.bad','flow_model_id_start'],['a b','flow_model_id_space'],
    ['a\nb','flow_model_id_control'],['vendor/模型','unsupported 模'],['a'.repeat(257),'flow_model_id_length'],
    ['~gateway/latest',''],['vendor/code:beta@1[1m]',''],['a'.repeat(256),'']]) assert.equal(modelIDProblem(id,word),expected,id);
  const choices=new AgentLaunchChoices();
  await choices.loadModels(makeOwner(async()=>({agent:'codex',models:[{id:'native',name:'Native',aliases:['short']}]})),'mac','codex');
  assert.equal(choices.listsModel('short'),true); assert.equal(choices.listsModel('SHORT'),false);
  assert.deepEqual(choices.models[0].aliases,['short']); assert.equal(choices.hasCatalogModel(),true);
  const source=readFileSync(new URL('../entry/src/main/ets/components/AgentStartPicker.ets',import.meta.url),'utf8');
  const methods=['canTypeModel','openTypedModel','confirmTypedModel','finishTypedModel'].map(name=>{
    const start=source.indexOf('  private '+name+'('), line=source.indexOf('\n',start);
    assert.ok(start>=0,name);
    return source.slice(start,source.slice(start,line).trimEnd().endsWith('}') ? line : source.indexOf('\n  }',line)+4);
  });
  const Picker=new Function('modelIDProblem','tr','return '+stripTypeScriptTypes('class Picker {\n'+methods.join('\n')+'\n}'))(modelIDProblem,word);
  const used=[];
  const picker=Object.assign(new Picker(),{choices,kind:'model',manual:true,selected:'vendor/prior',choose:id=>used.push(id)});
  picker.openTypedModel(); assert.equal(picker.typedModel,'vendor/prior');
  picker.typedModel='a b';picker.confirmTypedModel(); assert.equal(picker.typingModel,true);assert.deepEqual(used,[]);
  picker.typingModel=false;picker.finishTypedModel();assert.deepEqual(used,[]);
  picker.openTypedModel();picker.typedModel='  vendor/new  ';picker.confirmTypedModel();assert.equal(picker.typingModel,false);assert.deepEqual(used,[]);
  picker.finishTypedModel();picker.finishTypedModel();assert.deepEqual(used,['vendor/new']);
  picker.selected='short';picker.openTypedModel();assert.equal(picker.typedModel,'');picker.typedModel='second';picker.confirmTypedModel();
  choices.locked='launcher supplies its own model';picker.finishTypedModel();assert.deepEqual(used,['vendor/new']);
});


test('the focused New Agent draft follows the settled keyboard, content and text-size layout, and never a closed form', () => {
  const callbacks=[], moves=[];
  const methods=['focusAgentDraft','agentFormWillScroll','revealFocusedAgentDraft','agentDraftAreaChanged'];
  const sources={DRAG:0,SCROLL_BAR:4,OTHER_USER_INPUT:3,SCROLLER:6};
  const Form=new Function('setTimeout','ScrollSource','return '+stripTypeScriptTypes(
    'class Form {\n'+methods.map(name=>formMethods.get(name)).join('\n')+'\n}'))(fn=>callbacks.push(fn),sources);
  const fields={'agent.task':[260,92],'agent.tab_name':[385,52]};
  let current=0,rendered=0,actionTop=460;
  const frames={
    'agent.fields':()=>[150,410],
    'agent.fields.content':()=>[150-rendered,1200],
    'agent.create':()=>[actionTop,60]
  };
  const context={px2vp:px=>px/3,getComponentUtils:()=>({getRectangleById(id){
    const [y,height]=frames[id] ? frames[id]() : [150+fields[id][0]-rendered,fields[id][1]];
    return {windowOffset:{y:y*3},size:{height:height*3}};
  }})};
  const form=Object.assign(new Form(),{showSheet:true,sheetKind:'newagent',agentPanel:'',agentFormRevision:1,
    agentFocusScrollOrigin:-1,agentFocusLayoutRequest:0,getUIContext:()=>context,agentFormScroll:{
      currentOffset:()=>({xOffset:0,yOffset:current}),
      scrollTo:({xOffset,yOffset})=>{ assert.equal(xOffset,0);current=yOffset;moves.push(yOffset); }
    }});
  const layout=()=>{while(callbacks.length) callbacks.shift()()};
  form.focusAgentDraft('agent.task');
  form.agentDraftAreaChanged({width:402,height:624},{width:402,height:410});
  assert.deepEqual(moves,[]);layout();assert.deepEqual(moves,[50]);
  form.revealFocusedAgentDraft();layout();
  assert.deepEqual(moves,[50],'duplicate callbacks on the same stale native rectangles must not add another scroll');
  current=rendered=100;
  form.revealFocusedAgentDraft();layout();
  assert.deepEqual(moves,[50,50],'correct a second native keyboard adjustment using the original offset');
  rendered=current;actionTop=435;
  form.revealFocusedAgentDraft();layout();assert.equal(current,75,'a smaller visible region can reveal more');
  form.agentDraftAreaChanged({width:402,height:410},{width:402,height:410});assert.equal(callbacks.length,0);
  rendered=current;actionTop=460;form.focusAgentDraft('agent.tab_name');layout();assert.equal(current,135);
  form.revealFocusedAgentDraft();form.agentFormWillScroll(sources.DRAG);
  current=rendered=170;layout();form.revealFocusedAgentDraft();layout();assert.equal(current,170,'keep a manual reading position');
  fields['agent.task']=[90,460];form.focusAgentDraft('agent.task');layout();
  assert.equal(current,170,'a multiline editor taller than the viewport can scroll its own caret');
  const prior=moves.length;
  form.revealFocusedAgentDraft(); form.agentFormRevision++;layout();assert.equal(moves.length,prior);
  form.agentPanel='folder';form.revealFocusedAgentDraft();assert.equal(callbacks.length,0);
  form.agentPanel='';form.focusedAgentField='';form.revealFocusedAgentDraft();assert.equal(callbacks.length,0);
  assert.match(formSource,/\.minLines\(3\)\.maxLines\(10,\{overflowMode:MaxLinesMode\.SCROLL\}\)/);
  const messageField=formSource.slice(formSource.indexOf('TextArea({text:this.newTask'),formSource.indexOf(".id('agent.task')"));
  assert.match(messageField,/\.constraintSize\(\{minHeight:this\.agentSize\(94\.33,115\.67\)\}\)/);
  assert.doesNotMatch(messageField,/\.height\(|maxHeight:/,'long drafts still expand and scroll on their own');
  assert.match(formSource,/\.fontSize\(Tokens\.interfaceFontVp\(17,this\.environment\.fontScale\)\)\.placeholderFont\(\{size:Tokens\.interfaceFontVp\(17,this\.environment\.fontScale\)\}\)/);
});

function clientForForm(group, manager, calls) {
  const owner = makeOwner(async path => {
    calls.push([group, manager, path]);
    if (path.includes('/dirs?')) return { path: '/chosen', parent: '/', entries: [] };
    if (path.includes('/models?')) return { agent: 'codex', models: [{ id: 'independent' }] };
    return { launchers: [{ name: 'codex' }] };
  });
  owner.profile = { id: group, manager };
  owner.operationBlocked = () => false;
  return owner;
}
function formMachine(owner, id) {
  const machine = ResourceRow.from({ id, name: id, online: true, manager: owner.profile.manager }, 'machine');
  machine.group = owner.profile.id;
  return machine;
}
function formTree(machine, id, source = 'orca') {
  return Object.assign(new ProjectWorktree(), {
    group: machine.group, manager: machine.manager, machine: machine.id, source, sourceId: id, online: true, startAllowed: true,
    key: JSON.stringify([machine.group, machine.manager, machine.id, source, id]), title: id, cwd: '/work/' + id
  });
}
function phoneForm(root, selectedMachine, machines, trees) {
  root.directoryMachines = machines;
  const form = Object.assign(new NativeAgentForm(), {
    chatForward: {newFor:()=>undefined,newAllows:()=>true,newPrompt:(_owner,note)=>note,afterNew:async()=>{}},
    model: root, agentModel: root, agentPanel: '', agentFormRevision: 1, machineIntent: 1, launcherRevision: 0,
    newTask: 'test', directory: '', launcher: 'codex', agentModelId: '', pickedAgentTree: '', launchListReady: false, blockedLaunchers: [],
    showSheet: true, sheetKind: 'newagent', switchingAgentMachine: false, creatingAgent: false,
    launchChoices: new AgentLaunchChoices(), projectCatalog: { loading: false, projects: [{ trees }], refresh() {} },
    saveAgentDraft() {}, loadRecentSetups() {}, closeAgentPanel() { this.agentPanel = ''; }
  });
  form.agentMachineKey = form.machineKey(selectedMachine);
  return form;
}

test('New Agent shows the row and picker default from the current machine purpose without choosing a start model',async()=>{
  const owner=clientForForm('A','work',[]), machine=formMachine(owner,'mac'), form=phoneForm(owner,machine,[machine],[]);
  Object.assign(form,{agentUsesModelSettings:true,defaultLauncherName:'claude',agentConfiguredModel:'model-chat',launcher:'',
    launcherData:[{name:'claude',agent:'claude'},{name:'codex',agent:'codex'}],agentPanel:'model'});
  await form.launchChoices.loadModels(makeOwner(async()=>({agent:'claude',models:[{id:'host',name:'Host choice',is_default:true},
    {id:'preset-id',name:'Preset choice'}]})),'mac','claude');
  assert.equal(form.launchRowTitle('model'),'phone_start_default_model');
  assert.equal(form.launchRowDetail('model'),'Model Settings: model-chat');
  assert.equal(form.pickerOptions()[0].detail,'claude · Model Settings: model-chat');
  assert.equal(form.agentModelId,'');
  form.launcher='codex';assert.equal(form.launchRowDetail('model'),'Host choice');
  form.agentPreset='explicit';form.agentModelId='preset-id';assert.equal(form.launchRowTitle('model'),'Preset choice');
  assert.equal(form.launchRowDetail('model'),'preset-id');
  form.agentModelId='';form.launcher='';form.agentUsesModelSettings=false;
  assert.equal(form.launchRowDetail('model'),'Host choice');assert.equal(form.agentModelId,'');
  form.agentPanel='';form.launchChoices.locked='model is fixed by launcher';form.openAgentPicker('model');
  assert.equal(form.agentPanel,'');assert.equal(form.launchRowDetail('model'),'model is fixed by launcher');
});

function startRepositoryClass() {
  const repoSource = readFileSync(new URL('../entry/src/main/ets/models/HubRepository.ets', import.meta.url), 'utf8');
  const create = repoSource.match(/^  async createAgent\([^]*?^  }/m)[0];
  return new Function('Wire','ResourceRow','return '+stripTypeScriptTypes('class StartRepository {\n'+create+'\n}'))(Wire,ResourceRow);
}

test('the start sheet and the session request agree on thinking effort for each launcher and model', async () => {
  const StartRepository = startRepositoryClass();
  const starts = [];
  const repository = Object.assign(new StartRepository(), {owner:'work', async call(path,method,data,op,allowed) {
    assert.equal(path,'/api/v1/sessions'); assert.equal(method,'POST'); assert.equal(allowed(),true);
    starts.push(data); return {session:{id:'started'}};
  }});
  const ordinary = {name:'codex',agent:'codex'};
  const alias = {name:'team',agent:'codex',permission_modes:[]};
  const provider = {name:'provider',source:'paseo',agent:'codex',permission_modes:['launcherDefault','bypassPermissions']};
  const nativeOmp = {name:'omp',source:'path',agent:'omp',permission_modes:['launcherDefault']};
  const registeredOmp = {...nativeOmp,source:'paseo'};
  const terminalTraex = {name:'traex',agent_type:'traex',source:'zshrc'};
  const claude = {name:'classic',agent:'claude'};
  const olderPaseo = {name:'grok',agent:'grok',source:'paseo'};
  for (const [launcher,model,choices,saved,expected] of [
    [ordinary,'picked',[], 'high',''], [alias,'picked',[], 'high',''],
    [provider,'picked',['medium','high'], 'high','high'], [provider,'picked',['medium','high'], 'max',''],
    [provider,'unlisted',[], 'high',''], [nativeOmp,'picked',[], 'high',''], [terminalTraex,'picked',[], 'high',''],
    [registeredOmp,'picked',['medium','high'], 'high','high'], [olderPaseo,'unlisted',[], 'high',''],
    [claude,'unlisted',['low','medium','high','xhigh','max'], 'high','high'],
    [claude,'no-efforts',[], 'high','']
  ]) {
    const owner=clientForForm('A','work',[]), machine=formMachine(owner,'mac');
    const form=phoneForm(owner,machine,[machine],[]);
    form.launcher=launcher.name; form.launcherData=[launcher]; form.agentEffort=saved; form.agentModelId=model;
    form.agentLauncherOptions=[launcher.name]; form.launchListReady=true; form.recentSetups=[];
    owner.repo=()=>({call:async()=>({agent:launcher.agent,models:[{id:'picked',efforts:['high','medium','unrecognized']},{id:'no-efforts'}]})});
    await form.launchChoices.loadModels(owner,'mac',launcher.name); form.updateAgentEfforts();
    form.openAgentPicker('effort');
    assert.equal(form.agentPanel,choices.length?'effort':'',launcher.name);
    if (choices.length) assert.deepEqual(form.pickerOptions().map(x=>x.id),['',...choices],launcher.name);
    form.closeAgentPanel(); form.directory='/work/login';
    form.agentTabName = launcher.name === 'codex' ? '  版本复核  ' : '  ';
    owner.createAgent=(prompt,machine,cwd,launcher,todo,admit,id,host,model,effort,tabName)=>
      repository.createAgent(prompt,machine,cwd,launcher,'test-operation',todo,admit,id,host,model,effort,tabName);
    owner.saveView=async()=>{}; form.openPickedChat=()=>{}; form.projectPlace=()=>false;
    await form.createAgent();
    assert.equal(form.startFailure??'',''); assert.equal(form.showSheet,false);
    const wire=starts.at(-1);
    assert.deepEqual([wire.machine_id,wire.cwd,wire.launcher,wire.model,wire.prompt],['mac','/work/login',launcher.name,model,'test']);
    assert.equal(wire.tab_name,launcher.name==='codex' ? '版本复核' : undefined);
    assert.equal(Object.hasOwn(wire,'effort'),!!expected,launcher.name);
    assert.equal(wire.effort??'',expected,launcher.name);
  }
});

test('explicit Start keeps a confirmed terminal or engine workspace bound to the exact host and never starts on opening',async()=>{
  const Repository=startRepositoryClass();
  for (const host of ['herdr','herdr-core','orca']) {
    const root=clientForForm('workspace-hub','work',[]), machine=formMachine(root,'one-machine'), child=formTree(machine,'child-from-host',host);
    child.receiptCurrent=()=>true;child.branch='qa/one';child.parent='root';root.machines=[machine];
    const form=phoneForm(root,machine,[machine],[]), posts=[];
    Object.assign(form,{agentWorktree:child,directory:child.cwd,launcherData:[{name:'codex',agent:'codex'}],
      agentLauncherOptions:['codex'],recentSetups:[],agentTabName:'',projectPlace:()=>true,openLinkedResource(){}});
    form.projectCatalog.selectedWorktree=key=>key===child.key?child:undefined;
    const repository=Object.assign(new Repository(),{owner:'work',async call(path,method,body,op,admit){
      assert.equal(admit(),true); posts.push({path,method,body}); return {session:{id:'session-new'}};
    }});
    root.createAgent=(prompt,m,cwd,launcher,todo,admit,id,kind,model,effort,name)=>
      repository.createAgent(prompt,m,cwd,launcher,'fixture',todo,admit,id,kind,model,effort,name);
    root.saveView=async()=>{};
    assert.equal(form.canStartAgent(),true);assert.deepEqual(posts,[]);
    await form.createAgent();
    assert.equal(form.startFailure??'','');assert.equal(posts.length,1,host);
    assert.deepEqual(posts[0],{path:'/api/v1/sessions',method:'POST',body:{
      machine_id:machine.id,cwd:child.cwd,launcher:'codex',prompt:'test',workspace_id:child.sourceId,host}});
    assert.equal(form.showSheet,false);
  }
});

test('the New Agent footer follows an available model purpose and still loads launchers on an older hub', async () => {
  const launcherPath='/api/v1/machines/mac/launchers?purpose=agent.new', purposePath='/api/v1/model-config/purposes/agent.new';
  for (const [purpose,machineDefault,launcher,label,hint] of [
    [{id:'agent.new',effective:{provider:'launcher.codex',model:'picked'}}, {}, 'codex','codex · picked','flow_start_model_settings_hint /model'],
    [undefined,{provider:'launcher.classic',model:'node-default'},'classic','classic · node-default','flow_start_machine_launchers_hint /model']
  ]) {
    const owner=clientForForm('A','work',[]),machine=formMachine(owner,'mac'),form=phoneForm(owner,machine,[machine],[]);
    const calls=[]; form.launcher='';
    owner.repo=()=>({call:async path=>{
      calls.push(path);
      if (path===purposePath) { if (!purpose) throw Error('404'); return {purpose}; }
      if (path===launcherPath) return {default:machineDefault,launchers:[{name:launcher,agent:launcher==='classic'?'claude':'codex'}]};
      return {agent:'codex',models:[{id:'picked',efforts:['high']}]};
    }});
    await form.loadAgentLaunchers();
    await new Promise(resolve=>setImmediate(resolve));
    assert.deepEqual(calls.slice(0,2),[launcherPath,purposePath]); assert.ok(calls[2].includes('/launchers/'+launcher+'/models?'));
    assert.equal(form.defaultLauncherName,launcher); assert.equal(form.machineDefaultLabel,label);
    assert.equal(form.launchListReady,true); assert.equal(form.agentModelHelp(),hint);
    assert.equal(form.launchRowDetail('model'),purpose ? 'Model Settings: picked' : 'flow_use_default');
    form.launcherData=[{name:launcher,source:'paseo',agent:'grok'}];
    assert.equal(form.agentModelHelp(),hint.replace(' /model',''));
  }
});

test('opening the phone form offers a recent directory and a promptless Claude start while an older Codex still needs a first message', () => {
  const owner=clientForForm('A','work',[]),machine=formMachine(owner,'laptop');
  const form=phoneForm(owner,machine,[machine],[]);
  form.newTask='';form.directory='/chosen';form.launchListReady=true;form.agentLauncherOptions=['claude','codex','codex-plus'];
  form.launcherData=[{name:'claude',agent:'claude'},{name:'codex',agent:'codex'},{name:'codex-plus',agent:'codex',prompt_optional:true}];
  form.launcher='';form.defaultLauncherName='claude';
  assert.equal(form.canStartAgent(),true);
  form.launcher='codex';assert.equal(form.canStartAgent(),false);
  form.launcher='codex-plus';assert.equal(form.canStartAgent(),true);
  const dirs=recentLaunchDirectories({recent:[{path:'/data/apps/weather',last_used:'2026-01-01T10:00:00Z'},{path:''}]});
  assert.deepEqual(dirs.map(d=>[d.id,d.title,d.description]),[['/data/apps/weather','weather','2026-01-01T10:00:00Z']]);
});

test('a delayed machine directory lookup cannot replace the new machine or a directory chosen while opening the form', async () => {
  const owner=clientForForm('A','work',[]),machine=formMachine(owner,'mac'),other=formMachine(owner,'server');
  const form=phoneForm(owner,machine,[machine,other],[]),pending=new Map();
  owner.repo=()=>({call:path=>{const reply=gate();pending.set(path,reply);return reply.promise}});
  const old=form.loadInitialAgentDirectory();
  form.machineIntent++;form.agentMachineKey=form.machineKey(other);
  const fresh=form.loadInitialAgentDirectory();
  pending.get('/api/v1/machines/server/dirs?path=').resolve({path:'/home',recent:[{path:'/home/weather'}]});
  await fresh;assert.equal(form.directory,'/home/weather');
  pending.get('/api/v1/machines/mac/dirs?path=').resolve({path:'/foreign',recent:[{path:'/foreign/app'}]});
  await old;assert.deepEqual(form.recentAgentDirectories.map(item=>item.id),['/home/weather']);
  assert.equal(form.directory,'/home/weather');
  const manual=form.loadInitialAgentDirectory();
  form.directory='/chosen';
  pending.get('/api/v1/machines/server/dirs?path=').resolve({path:'/home',recent:[{path:'/home/recent'}]});
  await manual;assert.equal(form.directory,'/chosen');
});

test('opening the workspace picker refreshes an occupied catalog and accepts only this machine’s bound worktrees', async () => {
  const owner = clientForForm('group-A', 'manager-A', []), foreign = clientForForm('group-B', 'manager-A', []);
  const machine = formMachine(owner, 'same-id'), otherMachine = formMachine(owner, 'second'), otherHub = formMachine(foreign, 'same-id');
  const inferred = formTree(machine, 'session-folder', ''), directory = formTree(machine, 'known-directory', 'session-directory');
  const herdr = formTree(machine, 'terminal', 'herdr'), registered = formTree(machine, 'login');
  const old = [inferred, directory, herdr, formTree(otherMachine, 'foreign-machine'), formTree(otherHub, 'foreign-group')];
  const form = phoneForm(owner, machine, [machine, otherMachine, otherHub], old);
  const release = gate(); let refreshes = 0;
  form.projectCatalog.refresh = async root => {
    assert.equal(root, owner);
    refreshes++;
    await release.promise;
    form.projectCatalog.projects = [{ trees: [...old, registered, Object.assign(formTree(machine,'offline'), { online:false })] }];
  };
  form.openAgentPicker('workspace');
  assert.equal(refreshes, 1);
  assert.deepEqual(form.pickerOptions().map(x => x.id), ['']);
  release.resolve(); await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(form.pickerOptions().map(x => x.id), ['', registered.key]);
  for (const wrong of [inferred, directory, herdr, old[3], old[4]]) {
    form.chooseAgentOption(wrong.key);
    assert.equal(form.pickedAgentTree, '');
  }
  form.chooseAgentOption(registered.key);
  assert.equal(form.selectedAgentTree(), registered);
  assert.equal(form.directory, registered.cwd);
  form.openAgentPicker('workspace');
  assert.equal(refreshes, 2, 'a previous successful registration does not suppress the next catalog read');
});

test('switching between matching machine IDs isolates owner reads and retries a failed selection', async () => {
  for (const [otherGroup, otherManager] of [['group-B','manager-A'], ['group-A','manager-B']]) {
    const calls = [], a = clientForForm('group-A','manager-A',calls), b = clientForForm(otherGroup,otherManager,calls);
    const localMachine = formMachine(a, 'same-node'), otherMachine = formMachine(b,'same-node');
    const local = formTree(localMachine,'left'), remote = formTree(otherMachine,'right','paseo');
    const form = phoneForm(a, localMachine, [localMachine, otherMachine], [local,remote]);
    const failure = gate(); let attempts = 0;
    a.viewFor = async target => {
      assert.equal(target, otherMachine);
      if (++attempts === 1) { await failure.promise; throw Error('disconnected'); }
      return b;
    };
    const selecting = form.pickAgentMachine(otherMachine);
    form.openAgentPicker('folder');
    form.openAgentPicker('model');
    assert.deepEqual(calls, [], 'the prior Hub cannot serve reads during a switch');
    failure.resolve(); await selecting;
    assert.equal(attempts, 1);
    // A form may roll the highlight back, or retain the failed machine so the user can retry it.
    if (form.chosenAgentMachine() === otherMachine) {
      assert.equal(form.canStartAgent(), false);
      assert.deepEqual(form.availableAgentTrees(), [], 'retained options cannot expose the preceding owner');
      form.openAgentPicker('workspace');
      assert.equal(form.pickerOptions().some(option => option.id === local.key), false);
      form.openAgentPicker('folder');
      form.openAgentPicker('model');
      form.agentPanel = 'folder';
      form.browseAgentFolder('/previously-visible-folder');
      assert.deepEqual(calls, [], 'a failed new owner must never read folders or models from the previous owner');
    }
    await form.pickAgentMachine(otherMachine);
    assert.equal(attempts, 2, 'tapping the same failed machine retries its owner lookup');
    assert.equal(form.chosenAgentMachine(), otherMachine);
    assert.equal(form.agentModel, b);
    form.openAgentPicker('workspace');
    assert.deepEqual(form.pickerOptions().map(option => option.id), ['',remote.key]);
    form.openAgentPicker('folder');
    form.openAgentPicker('launcher');
    form.chooseAgentOption('codex');
    form.openAgentPicker('model');
    assert.ok(calls.some(([group,manager,path]) => group === otherGroup && manager === otherManager && path.includes('/dirs?')));
    assert.ok(calls.some(([group,manager,path]) => group === otherGroup && manager === otherManager && path.includes('/codex/models?')));
    assert.ok(calls.every(([group,manager]) => group === otherGroup && manager === otherManager));
  }
});
test('matches local titles and previews immediately; remote message snippet and id enrich the existing chat',()=>{
  const a=ResourceRow.from({id:'a',title:'Release',machine_id:'mac',last_text:'Zoom quality',updated_at:'2026-11-20'},'conversation');
  const b=ResourceRow.from({id:'b',machine_id:'mac',title:'Archived',archived:true},'conversation');
  const list=localChatMatches([b,a],'release quality',false);assert.equal(list.length,1);assert.equal(list[0].row,a);
  const merged=mergeChatMatches(list,[new PhoneChatMatch(a,'This sentence matches','actual-item',[[5,13]])]);
  assert.equal(merged.length,1);assert.equal(merged[0].subtitle,'This sentence matches');assert.equal(merged[0].hitId,'actual-item');
  assert.deepEqual(merged[0].ranges,[[5,13]]);
});
test('full-text requests are debounced, and clearing, switching scope or hiding search discards late hits',async()=>{
  const calls=[];const owner=makeOwner(async p=>{const g=gate();calls.push([p,g]);return g.promise});
  const index=new PhoneSearchIndex();index.schedule(owner,'a',false);index.schedule(owner,'snow & rain',false);
  await new Promise(r=>setTimeout(r,250));assert.equal(calls.length,1);assert.match(calls[0][0],/q=snow%20%26%20rain/);
  index.close();calls[0][1].resolve({results:[{machine_id:'m',session_id:'old',title:'Old'}]});await new Promise(r=>setTimeout(r,5));assert.deepEqual(index.matches,[]);
  index.schedule(owner,'fresh',false);await new Promise(r=>setTimeout(r,230));owner.generation++;
  calls[1][1].resolve({results:[{machine_id:'m',session_id:'other'}]});await new Promise(r=>setTimeout(r,5));assert.deepEqual(index.matches,[]);
  index.close();index.schedule(owner,'agent',true);assert.equal(index.busy,false);index.close();
});

test('the assistant popover rechecks roster membership and opens an installation in its original hub without switching assistants', async () => {
  const {NavigationContext}=await import('../entry/src/main/ets/models/NavigationContext.ets');
  const Panel=new Function('NavigationContext','return '+stripTypeScriptTypes(
    'class Panel {'+formMethods.get('assistantMenuAction')+'}'))(NavigationContext);
  const calls=[], routes=[], normal=new ResourceRow('installed','Work','manager');
  const installing=ResourceRow.from({id:'pending',name:'Installing',installing:'2030-02-03T01:00:00Z'},'manager');
  const panel=new Panel();
  Object.assign(panel,{assistantMenuScope:'owner',spacesShown:true,drawerAcceptsTap:()=>true,drawerManagers:[normal,installing],
    model:{scope:()=> 'owner',selectListedManager:(id,scope)=>calls.push([id,scope])},
    stack:{pushPath:value=>routes.push(value)},savePath:()=>{},openSettings:value=>calls.push(value)});
  panel.assistantMenuAction('other','',normal);
  assert.deepEqual(calls,[]);assert.equal(routes.length,0);
  panel.assistantMenuAction('owner','',new ResourceRow('removed'));
  assert.equal(routes.length,0);assert.deepEqual(calls,[]);
  panel.assistantMenuScope='owner';panel.assistantMenuAction('owner','',new ResourceRow('pending'));
  assert.deepEqual(calls,[]);assert.equal(routes.length,1);
  assert.equal(NavigationContext.installingManagerFor(routes[0].param,'owner'),'pending');
  assert.equal(NavigationContext.installingManagerFor(routes[0].param,'other'),'');
  assert.equal(NavigationContext.installingManagerFor('managers','owner'),'');
  panel.assistantMenuScope='owner';panel.assistantMenuAction('owner','',normal);
  assert.deepEqual(calls,[['installed','owner']]);assert.equal(routes.length,1);
});

test('closing the compact drawer with a row under the finger keeps the current page', () => {
  const methods = ['drawerAcceptsTap', 'endPhonePan'].map(name => formMethods.get(name));
  const Drawer = new Function('finishesPhonePan', 'Haptics', 'return ' + stripTypeScriptTypes(
    'class NativeDrawer {\n' + methods.join('\n') + '\n}'))(finishesPhonePan, {selection() {}});
  const drawer = new Drawer();
  Object.assign(drawer, {wide: () => false, spacesShown: true, gestureKind: 'close',
    gestureScope: 'paired', gesturePlace: 4, phoneWidth: 360, drawerWidth: () => 295, drawerTapBlockedUntil: 0,
    model: {scope: () => 'paired', section: 4}});
  assert.equal(drawer.drawerAcceptsTap(), false);
  drawer.endPhonePan(-170, -620);
  assert.equal(drawer.spacesShown, false);
  assert.equal(drawer.drawerAcceptsTap(), false);
  // A late click still belongs to the completed drag, even if it reaches the retained row.
  drawer.spacesShown = true;
  assert.equal(drawer.drawerAcceptsTap(), false);
  drawer.drawerTapBlockedUntil = Date.now() - 1;
  assert.equal(drawer.drawerAcceptsTap(), true);
});

test('Reduce Motion switches a compact drawer directly and retires an older completion', () => {
  const methods = ['settlePhoneDrawer', 'movePhonePan'].map(name => formMethods.get(name));
  const motionStub = {
    drawer: (reduce, _velocity, _width, onFinish) => reduce ? {duration: 0, onFinish} : {duration: 220, curve: 'ease-out', onFinish}
  };
  const Drawer = new Function('Curve', 'Motion', 'return ' + stripTypeScriptTypes(
    'class NativeDrawer {\n' + methods.join('\n') + '\n}'))({EaseOut: 'ease-out'}, motionStub);
  const drawer = new Drawer();
  let completion;
  Object.assign(drawer, {
    drawerAnimation: 0, drawerMounted: true, spacesShown: false, drawerFraction: 1,
    environment: {reduceMotion: false}, gestureKind: 'open', drawerWidth: () => 300, drawerFrom: 0,
    getUIContext: () => ({animateTo(options, update) { completion = options.onFinish; update(); }})
  });
  drawer.settlePhoneDrawer(false);
  assert.equal(typeof completion, 'function');
  drawer.environment.reduceMotion = true;
  drawer.getUIContext = () => { throw new Error('unexpected native animation'); };
  drawer.movePhonePan(120);
  assert.equal(drawer.drawerFraction, 0);
  drawer.spacesShown = true;
  drawer.settlePhoneDrawer(true);
  completion();
  assert.equal(drawer.drawerMounted, true);
  assert.equal(drawer.drawerFraction, 1);
  drawer.spacesShown = false;
  drawer.settlePhoneDrawer(false);
  assert.equal(drawer.drawerMounted, false);
  assert.equal(drawer.drawerFraction, 0);
});

test('the repository only selects a row for its adjacent live detail, never for a popped phone chat', () => {
  const from = formSource.indexOf('get adjacentProjectItem()');
  const end = formSource.indexOf('@Builder projectPage(', from);
  assert.ok(from >= 0 && end > from);
  const Shell = new Function('return ' + stripTypeScriptTypes('class Shell {' + formSource.slice(from,end) + '}'))();
  let split = false, ready = true;
  const page = Object.assign(new Shell(), {wide:() => split, shownName:'session', shownParam:'route', activeChild:'session:route',
    childKey:(name,param) => name+':'+param, targetReady:() => ready, detailModel:{selected:{kind:'session',key:() => 'scoped-owner'}}});
  assert.equal(page.adjacentProjectItem, '');
  split = true;
  assert.equal(page.adjacentProjectItem, 'session:scoped-owner');
  ready = false; assert.equal(page.adjacentProjectItem, '');
  page.shownName = 'project_tree'; page.activeChild = 'project_tree:route';
  assert.equal(page.adjacentProjectItem, 'tree:route');
  page.activeChild = ''; assert.equal(page.adjacentProjectItem, '');
  split = false; page.shownName = 'projects'; page.activeChild = 'projects:route';
  assert.equal(page.adjacentProjectItem, '');
});

test('only a workspace opened over the same owned session inherits its phone header and sibling tabs', () => {
  const start = formSource.indexOf('get phoneToolParent()');
  const end = formSource.indexOf('private phonePageName()', start);
  assert.ok(start >= 0 && end > start);
  const Shell = new Function('return ' + stripTypeScriptTypes(
    'class PhoneShell {\n' + formSource.slice(start, end) + '\n}'))();
  const shell = new Shell();
  const row = (machine, manager, id) => {
    const value = new ResourceRow();
    value.kind = 'session'; value.group = 'hub'; value.machine = machine; value.manager = manager; value.id = id;
    return value;
  };
  const a = row('desktop','personal','same');
  const b = row('laptop','personal','same');
  const otherManager = row('desktop','shared','same');
  const routes = new Map([['chat',a],['opened-from-chat',a],['different-computer',b],['different-manager',otherManager]]);
  Object.assign(shell,{phoneTop:'tools',phonePath:['project','session','tools'],phoneParam:'opened-from-chat',
    stack:{getParamByIndex:index => index === 1 ? 'chat' : undefined},routeRow:param => routes.get(param)});
  assert.equal(shell.phoneToolParent,'chat');
  for (const param of ['different-computer','different-manager']) {
    shell.phoneParam = param;
    assert.equal(shell.phoneToolParent,'');
  }
  shell.phoneParam = 'opened-from-chat';
  shell.phonePath = ['project','tools'];
  assert.equal(shell.phoneToolParent,'');
});

test('the long-draft header belongs only to the originating active portrait-phone session', () => {
  const expr=formSource.match(/@Computed get floatingDraftHeader\(\): boolean \{\s*return ([\s\S]+?);\s*\}/)[1];
  const floating=new Function('return '+expr);
  const view={fullHeightPhone:()=>true,phoneTop:'session',longDraftIdentity:'photos',activeChild:'photos'};
  assert.equal(floating.call(view),true);
  view.activeChild='weather';assert.equal(floating.call(view),false);
  view.activeChild='photos';view.longDraftIdentity='';assert.equal(floating.call(view),false);
  view.longDraftIdentity='photos';view.fullHeightPhone=()=>false;assert.equal(floating.call(view),false);
});

test('a foreground appearance change recreates just the retained session header and sibling controls', () => {
  const header = formSource.slice(formSource.indexOf('  @Builder phoneHeader()'), formSource.indexOf('  @Builder compactPlaces()'));
  assert.match(header,/this\.phoneTop === 'session' \|\| this\.phoneToolParent/);
  assert.match(header,/ForEach\(\[this\.environment\.dark\],\s*\(dark:boolean\) => \{\s*WithTheme\(\{[^}]*colorMode:dark \? ThemeColorMode\.DARK : ThemeColorMode\.LIGHT[^}]*\}\) \{\s*this\.compactPlaces\(\)\s*\}\s*\},\s*\(dark:boolean\) => String\(dark\)\)/);
  // The Navigation owns Files/search/Git, the selected session, and its mounted conversation draft.
  const regular=formSource.match(/if \((!this\.wide\(\) && !this\.floatingProjectHeader[^\n]+)\) this\.phoneHeader\(\)\s*Navigation\(this\.stack\)/)[1];
  const floating=formSource.match(/@Computed get floatingProjectHeader\(\): boolean \{\s*return ([\s\S]+?);\s*\}/)[1];
  const row=new Function('return '+regular),overlay=new Function('return '+floating);
  const count=(top,search,wide=false,projects=true,merged=false)=>{
    const context={phoneTop:top,projectSearching:search,wide:()=>wide,projectPlace:()=>projects,mergedPhone:()=>merged,model:{profile:{id:'hub'}}};
    context.floatingProjectHeader=Boolean(overlay.call(context));
    return [Boolean(row.call(context)),context.floatingProjectHeader];
  };
  assert.deepEqual(count('',true),[false,false]);assert.deepEqual(count('project_tree',false),[false,false]);
  assert.deepEqual(count('session',true),[true,false]);assert.deepEqual(count('',false),[false,true]);
  assert.deepEqual(count('',false,false,true,true),[true,false]);
  assert.deepEqual(count('',true,false,false),[true,false]);assert.deepEqual(count('session',false,true),[false,false]);
  assert.match(formSource,/if \(this\.floatingProjectHeader \|\| this\.floatingDraftHeader\) [\s\S]{0,100}this\.phoneHeader\(\)/);
  const chrome = formSource.slice(formSource.indexOf('  @Builder compactPlaces()'), formSource.indexOf('  private parentIndexes()'));
  assert.match(chrome,/SessionSpaceTabs\(\{space:this\.phoneSessionSpace/);
});

test('the phone More button follows the restored session when Files or Git returns to its parent', () => {
  const header = formSource.slice(formSource.indexOf('  @Builder compactPlaces()'),formSource.indexOf('  private parentIndexes()'));
  const button = formSource.slice(formSource.indexOf('  @Builder compactSessionMore()'),formSource.indexOf('  @Builder destinationMenus('));
  assert.match(header,/this\.compactChatMenu\(this\.phoneTop\)[^\n]+\{\s*this\.compactSessionMore\(\)/);
  assert.match(button,/\.enabled\(this\.phoneChatActionsReady\)\.onClick\(\(\) => this\.showChatActions\(this\.phoneTop,this\.phoneParam\)\)/);
  assert.match(button,/\.bindMenu\(this\.chatTitleMenuVisible && this\.phoneChatActionsReady,/);
  const expression=formSource.match(/@Computed get phoneChatActionsReady\(\): boolean \{\s*return ([\s\S]+?);\s*\}/)[1];
  const ready=new Function('return '+expression);
  const shell={
    phoneTop:'tools',phoneParam:'git-A',activeChild:'tools/git-A',restored:'tools/git-A',
    compactChatMenu:n=>['session','conversation','tools'].includes(n),
    childKey:(name,param)=>name+'/'+param,
    targetReady(name,param){return this.restored===this.childKey(name,param);}
  };
  assert.equal(ready.call(shell),true);
  shell.phoneTop='session';shell.phoneParam='A';shell.activeChild='session/A';
  assert.equal(ready.call(shell),false);
  shell.restored='session/A';
  assert.equal(ready.call(shell),true);
  shell.phoneParam='B';assert.equal(ready.call(shell),false);
  shell.activeChild='session/B';shell.restored='session/B';assert.equal(ready.call(shell),true);
});

test('the compact meeting, knowledge and inbox header replaces its native button and actions with the active route owner',()=>{
  const shell=readFileSync(new URL('../entry/src/main/ets/pages/Index.ets',import.meta.url),'utf8');
  const source=shell.slice(shell.indexOf('  private rootModuleMenuKind()'),shell.indexOf('  @Builder rootModuleMenu()'))+
    shell.slice(shell.indexOf('  private openModuleMenu('),shell.indexOf('  @Builder phoneModuleMore('));
  const Nav=new Function('return '+stripTypeScriptTypes('class NativeMenu {'+source+'}'))();
  const n=new Nav();let generation=1,refreshes=0;
  Object.assign(n,{model:{section:0,scope:()=> 'fixture',connectionGeneration:()=>generation},meetingOptions:{available:true,prepare:()=>refreshes++},
    spacesShown:false,navigationIntent:1,phoneTop:'recordings',phoneParam:''});
  const old=n.menuOwner('recordings');n.openModuleMenu('recordings',old);assert.equal(refreshes,1);assert.ok(n.menuCurrent('recordings'));
  for(const [top,param,kind] of [['resources','knowledge','knowledge'],['knowledge_meetings','','knowledge'],['resources','inbox','inbox'],['resources','knowledge','knowledge'],['recordings','','recordings']]){
    const before=n.moduleMenuOwner;n.phoneTop=top;n.phoneParam=param;n.navigationIntent++;const active=n.menuOwner(kind);
    assert.notEqual(before,active);
    n.openModuleMenu('recordings',old);assert.notEqual(n.moduleMenuOwner,active);
    n.openModuleMenu(kind,active);assert.ok(n.menuCurrent(kind));n.closeModuleMenu(before);assert.equal(n.moduleMenuShown,true);
  }
  generation++;assert.equal(n.menuCurrent('recordings'),false);const next=n.menuOwner('recordings');n.openModuleMenu('recordings',next);
  n.closeModuleMenu(next);assert.equal(n.moduleMenuShown,false);
  assert.match(shell,/ForEach\(\[this\.rootModuleMenuKind\(\)\],\(kind:string\) => \{\s*this\.phoneModuleMore\(kind,this\.menuOwner\(kind\)\)\s*\},\(kind:string\) => this\.menuOwner\(kind\)\)/);
});

test('the phone start action belongs to the bottom of the scroll area, separate from the sheet close control',()=>{
  const start=formSource.slice(formSource.indexOf('  @Builder agentStartFooter()'),formSource.indexOf('  private async loadAgentLaunchers()'));
  const cancel=start.indexOf("id('agent.cancel')"),stack=start.indexOf('Stack({alignContent:Alignment.Bottom})'),scroll=start.indexOf('Scroll(this.agentFormScroll)');
  assert.ok(cancel>=0 && stack>cancel && scroll>stack);
  const inset=start.slice(start.indexOf('if (this.environment.keyboardHeightPx > 0)',stack),start.indexOf('this.agentStartFooter()',stack));
  assert.ok(inset.includes("Rect().width('100%').height(this.agentActionBottom()).fill(Colors.sheetBackground)"));
  assert.ok(inset.includes(".hitTestBehavior(HitTestMode.Block).accessibilityLevel('no').id('agent.keyboard.inset')"));
  assert.match(start,/\.height\('100%'\)\.justifyContent\(FlexAlign.End\)/);
  assert.match(start,/this\.agentStartFooter\(\)\s*\}\.width\('100%'\)\.layoutWeight\(1\)\.clip\(true\)/);
  assert.match(start,/bottom:this\.agentScrollClearance\(\)/);
  assert.match(start,/\.hitTestBehavior\(HitTestMode.Block\)\.enabled\(this\.canStartAgent\(\)\)/);
  assert.doesNotMatch(start,/\.overlay\(/);
  const Form=new Function('PhoneStyle','Tokens','return '+stripTypeScriptTypes('class Form{'+
    ['agentStartButtonHeight','agentActionBottom','agentScrollClearance'].map(key=>formMethods.get(key)).join('\n')+'}'))(
    {formSize:(regular,large,scale)=>scale===1?regular:large},{gap:8});
  const f=new Form();f.fullHeightPhone=()=>true;f.getUIContext=()=>({px2vp:n=>n/3});
  f.environment={keyboardHeightPx:0,safeBottomPx:2520,windowHeightPx:2622,fontScale:1};
  f.startFailure='';f.agentErrorHeight=0;
  assert.equal(f.agentScrollClearance(),14+7+60+42);
  f.environment.fontScale=3.2;f.environment.keyboardHeightPx=810;f.startFailure='maintenance';f.agentErrorHeight=47;
  assert.equal(f.agentScrollClearance(),14+7+69.3+16+8+47);
});

test('clearing a compact settings query keeps the field active and dismissing still closes the sheet',()=>{
  const source=readFileSync(new URL('../entry/src/main/ets/components/SettingsDirectory.ets',import.meta.url),'utf8');
  const clear=source.slice(source.indexOf('  @Builder clearSearchButton()'),source.indexOf('  build() {',source.indexOf('  @Builder clearSearchButton()')));
  assert.match(clear,/this\.query=''; this\.onQueryChange\(''\)/);
  assert.match(clear,/requestFocus\('settings.search'\)/);
  assert.doesNotMatch(clear,/onDismissSearch/);
  assert.match(source,/this\.field\.stopEditing\(\); this\.onDismissSearch\(\); \}\)\.id\('settings.close'\)/);
  assert.match(source,/style:this.showDismissSearch \? CancelButtonStyle.INVISIBLE : CancelButtonStyle.INPUT/);
});
