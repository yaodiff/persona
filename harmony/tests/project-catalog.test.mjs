import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import test from 'node:test';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('.ets') && specifier === '../core/SessionNaming') {
      return { url: new URL(specifier + '.ts', context.parentURL).href, shortCircuit: true };
    }
    if (context.parentURL?.endsWith('.ets') && ['./Wire','./ProjectCatalog','./SessionSpace','./AgentRepositoryPlace','./InteractionDecision'].includes(specifier)) {
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (!url.endsWith('.ets')) return next(url, context);
    const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
    return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
  }
});
const { ProjectAgent, ProjectCatalog, ProjectWorktree, ProjectBrowserState, projectDirectory } = await import('../entry/src/main/ets/models/ProjectCatalog.ets');
const { projectOutline, projectHosts, projectSearchAgents, worktreeSpaceCards } = await import('../entry/src/main/ets/models/ProjectOutline.ets');
const { ResourceRow } = await import('../entry/src/main/ets/models/Wire.ets');
const { SessionSpace, sessionSpace, worktreeForSessionSpace } = await import('../entry/src/main/ets/models/SessionSpace.ets');
const { AgentSessionInfo,agentHeaderWords,sessionInfoPath } = await import('../entry/src/main/ets/models/AgentSessionInfo.ets');

test('legacy sessions keep their native brand mark without inventing a runtime or overriding an explicit provider', () => {
  for (const [fields, mark, runtime] of [
    [{}, 'claude', ''], [{agent:''}, 'claude', ''], [{source_kind:'codex_history'}, 'codex', ''],
    [{source_kind:'codex_app_server', agent:'claude'}, 'codex', 'Claude'],
    [{agent_type:'codex'}, 'codex', 'Codex'], [{agent:'paseo', agent_label:'Acme'}, 'acme', 'Acme']
  ]) {
    const record={session_id:'layout-1', machine_id:'layout', ...fields};
    const row=new ProjectAgent(ResourceRow.from(record,'session'),record);
    assert.equal(row.markRuntime,mark);
    assert.equal(row.runtime,runtime);
  }
});

test('the Agent title prioritizes active state and worktree and the info sheet retains the separate slot and machine',()=>{
  const tr=key=>({phone_todo_section_needs:'需要你',agent_group_idle:'已看'}[key] || key);
  const needs=ResourceRow.from({session_id:'heic',machine_id:'studio',state:'needs_you',slot_index:1},'session');
  const words=agentHeaderWords(needs,'Mac Studio','photo-sync',true,tr);
  assert.deepEqual([words.full,words.short,words.location],['需要你 · Mac Studio · photo-sync','需要你 · photo-sync','Mac Studio › photo-sync › #1']);
  const quiet=ResourceRow.from({session_id:'pr',machine_id:'book',state:'idle',slot_name:'review',slot_index:2},'session');
  const pr=agentHeaderWords(quiet,'MacBook','weather',true,tr);
  assert.deepEqual([pr.full,pr.short,pr.status,pr.location],['MacBook · weather','weather','已看','MacBook › weather › review']);
  assert.equal(sessionInfoPath('/Users/me/code/weather','/Users/me'),'~/code/weather');
  assert.equal(sessionInfoPath('/data00/home/agent/repo',''),'~/repo');
});

test('session info reads only its owning Hub and session and cannot fill a dismissed sheet or later session',async()=>{
  const a=ResourceRow.from({session_id:'a / test',machine_id:'one/two',manager:'m'},'session');a.group='g';
  const b=ResourceRow.from({session_id:'b',machine_id:'other',manager:'m'},'session');b.group='g';
  const requests=[];let gate=Promise.withResolvers();
  const client={selected:a,profile:{id:'g',manager:'m'},connected:true,generation:1,scope:()=>'g/m',
    target(){return this.selected.key();},connectionGeneration(){return this.generation;},owns(g,scope){return this.generation===g && scope===this.scope();},
    repo(){return{call:async(path,method,body,op,allowed)=>{requests.push({path,method,body,allowed});return gate.promise;}};}};
  const sheet=new AgentSessionInfo();const first=sheet.open(client);
  assert.equal(requests[0].path,'/api/v1/sessions/one%2Ftwo/a%20%2F%20test/info');assert.equal(requests[0].method,'GET');
  sheet.close();gate.resolve({session_id:a.id,version:'ignored'});await first;assert.deepEqual(sheet.detail,{});
  client.selected=b;gate=Promise.withResolvers();const second=sheet.open(client);client.generation++;
  assert.equal(requests[1].allowed(),false);gate.resolve({session_id:'b',version:'old generation'});await second;assert.deepEqual(sheet.detail,{});
  gate=Promise.withResolvers();const third=sheet.open(client);gate.resolve({session_id:'b',model:'live'});await third;
  assert.equal(sheet.detail.model,'live');client.selected=a;assert.equal(sheet.owns(client),false);
  gate=Promise.withResolvers();const mismatched=sheet.open(client);gate.resolve({session_id:'b',version:'wrong'});await mismatched;
  assert.equal(sheet.error,'request_updated');assert.deepEqual(sheet.detail,{});
});
function row(kind, value, group = 'home', manager = 'work') {
  const item = ResourceRow.from(value, kind);
  item.group = group;
  item.manager = manager;
  return item;
}
function workspace(id, extra = {}) {
  return row('worktree', { machine_id: 'mac', source: 'orca', source_id: id, title: id, cwd: '/repo/' + id, ...extra });
}
function session(id, extra = {}) {
  return row('session', { session_id: id, machine_id: 'mac', cwd: '/repo/feature', title: id, ...extra });
}
const machine = row('machine', { id: 'mac', name: 'Studio', online: true });

test('model labels keep their type size and only tighten tracking for a near-fitting identifier', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const method=source.slice(source.indexOf('  private spaceModelTracking('),source.indexOf('  @Builder spaceModel('));
  const env={windowWidthPx:1206};
  const Page=new Function('nativeEnvironment','PhoneStyle','return '+stripTypeScriptTypes('class Page {'+method+'}'))(env,{page:16});
  const widths=new Map([['provider-small',240],['provider-model-two',474],['provider-model-far-too-long',700]]);
  const observed=[];
  const page=Object.assign(new Page(),{narrowOutline:()=>false,spaceCardScale:()=>1,spaceCaptionScale:()=>1.5,
    getUIContext:()=>({px2vp:px=>px/3,vp2px:vp=>vp*3,getMeasureUtils:()=>({measureText:params=>{
      observed.push(params.fontSize);
      return widths.get(params.textContent)+(parseFloat(params.letterSpacing||'0')*3*params.textContent.length);
    }})})});
  assert.equal(page.spaceModelTracking('provider-small'),'0vp');
  const adjustment=parseFloat(page.spaceModelTracking('provider-model-two'));
  assert.ok(adjustment<0 && adjustment>=-.36);
  assert.equal(page.spaceModelTracking('provider-model-far-too-long'),'0vp');
  assert.ok(observed.every(font=>font==='18vp'));
  page.narrowOutline=()=>true;assert.equal(page.spaceModelTracking('provider-model-two'),'0vp');
});

test('space card headers reserve the status slot and omit the optional runtime before truncating the tab', () => {
  const src=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const methods=src.slice(src.indexOf('  private spaceSlotTitle('),src.indexOf('  @Builder spaceSlot('));
  const environment={windowWidthPx:1206,fontScale:1},style={page:16},tokens={interfaceScaleFor:()=>1.5};
  const Page=new Function('nativeEnvironment','PhoneStyle','Tokens','FontWeight',
    'return '+stripTypeScriptTypes('class Page {'+methods+'}'))(environment,style,tokens,{Medium:500});
  const page=Object.assign(new Page(),{narrowOutline:()=>false,spaceCardScale:()=>1,getUIContext:()=>({
    px2vp:px=>px/3,getMeasureUtils:()=>({measureText:({textContent,fontSize})=>3*parseFloat(fontSize)*textContent.length*.58})
  })});
  const session={tabTitle:'widget',tab:'',runtime:'Claude',state:'working'};
  assert.ok(page.fitsRuntimeChip(session,''));
  environment.fontScale=3.2;
  assert.equal(page.fitsRuntimeChip(session,''),false);
  assert.equal(page.fitsRuntimeChip(session,'Claude 1'),false);
  assert.equal(page.spaceSlotTitle(session,'Claude 1'),'widget · Claude 1');
  environment.fontScale=1;
  assert.equal(page.fitsRuntimeChip({...session,tabTitle:'An exceptionally long terminal tab label'},''),false);
  page.narrowOutline=()=>true;
  assert.equal(page.fitsRuntimeChip(session,''),false);
  const grid=src.slice(src.indexOf('  @Builder spaceSlot('),src.indexOf('  @Builder spaceBar('));
  assert.match(grid,/\.alignItems\(ItemAlign\.Stretch\)\.id\('projects\.session_grid'\)/);
  assert.match(grid,/GridCol\(\{span:1\}\)\s*\{[\s\S]+?\.backgroundColor\(Colors\.surface\)[\s\S]+?\.id\('projects\.session'\)/);
  assert.match(grid,/\.layoutWeight\(1\)\.clip\(true\)\s*this\.spaceStatus\(agent\)/);
});

test('space cards fit their actual bordered grid columns and retain the natural style at 402 points or in one column', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const methods=source.slice(source.indexOf('  private narrowOutline()'),source.indexOf('  private treeInset('));
  const environment={windowWidthPx:1280,fontScale:1};
  const Page=new Function('nativeEnvironment','PhoneStyle','return '+stripTypeScriptTypes('class Page {'+methods+'}'))(environment,{page:16});
  const page=new Page();
  let density=3.5;
  page.getUIContext=()=>({px2vp:px=>px/density,vp2px:vp=>vp*density});
  const scale=page.spaceCardScale(),columnPx=Math.floor((1280-(32+12)*density)/2);
  const textWidthPx=columnPx-2*(Math.ceil(1.5*density)+Math.ceil(12*scale*density));
  assert.ok(scale>=.88 && scale<1);
  assert.ok(textWidthPx>=Math.ceil(150.34*scale*density),'A naturally full 150-point CJK headline fits without wrapping by one character');
  environment.fontScale=3.2;assert.equal(page.spaceCardScale(),scale);assert.equal(page.narrowOutline(),false);
  density=3;environment.windowWidthPx=1206;assert.equal(page.spaceCardScale(),1);
  environment.windowWidthPx=900;assert.equal(page.narrowOutline(),true);assert.equal(page.spaceCardScale(),1);
});

test('opening a terminal space resolves its owning checkout even when the two IDs differ', () => {
  const selected = session('selected', {space_kind: 'herdr', space_id: 'w1'});
  const space = sessionSpace(selected, [selected], [], 'home', 'work');
  const tree = (checkout, id, main, group = 'home') => ({
    key: JSON.stringify([group, 'work', 'mac', 'session-directory', checkout]), group, manager: 'work', machine: 'mac', main,
    agents: [{ row: {id}, spaceKind: 'herdr', spaceId: 'w1' }]
  });
  const primary = tree('/repo', 'lead', true);
  const branch = tree('/repo/review', 'selected', false);
  const elsewhere = tree('/repo', 'selected', true, 'elsewhere');
  assert.notEqual(space.key, primary.key);
  assert.equal(worktreeForSessionSpace(space, [{trees: [elsewhere]}, {trees: [branch, primary]}]), primary);
  assert.equal(worktreeForSessionSpace(space, [{trees: [branch]}]), branch);
  assert.equal(worktreeForSessionSpace(space, [{trees: [elsewhere]}]), undefined);
});

test('repository scroll anchors keep the same worktree when the summary scrolls with the list', () => {
  const source = readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets', import.meta.url), 'utf8');
  const methods = source.slice(source.indexOf('  private route()'), source.indexOf('  private search('));
  const Outline = new Function('ScrollAlign', 'return ' + stripTypeScriptTypes('class Outline {' + methods + '}'))({ START: 'start' });
  const positions = new Map();
  const moves = [];
  const view = Object.assign(new Outline(), {
    page: 'project', target: 'weather', lines: [{ key: 'main' }, { key: 'release' }],
    catalog: { browser: { anchor: key => positions.get(key), remember: (key, value) => positions.set(key, value) } },
    scroller: { scrollToIndex: index => moves.push(index) },
  });
  view.restorePosition();
  view.rememberPosition(0);
  assert.equal(positions.get('project/weather'), '');
  view.rememberPosition(1);
  assert.equal(positions.get('project/weather'), 'main');
  view.rememberPosition(2);
  assert.equal(positions.get('project/weather'), 'release');
  view.page = 'projects';
  view.target = '';
  view.restorePosition();
  view.rememberPosition(1);
  assert.equal(positions.get('projects/'), 'release');
  view.page = 'project';
  view.target = 'weather';
  view.restorePosition();
  assert.deepEqual(moves, [2]);
  view.page = 'projects';
  view.target = '';
  view.restorePosition();
  assert.deepEqual(moves, [2, 1]);
});

test('a mounted worktree card opens the newest scoped row and rejects cards removed or shown under another checkout',()=>{
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const method=source.slice(source.indexOf('  private openSpaceCard('),source.indexOf('  @Builder spaceStatus('));
  const Page=new Function('return '+stripTypeScriptTypes('class Page {'+method+'}'))();
  const live=session('same'),foreign=session('same');foreign.group='elsewhere';
  const current={row:live};let agents=[current];const opened=[];
  const page=Object.assign(new Page(),{page:'project_tree',target:'checkout',chooseAgent:agent=>opened.push(agent)});
  Object.defineProperty(page,'detailSessions',{get:()=>agents.map(agent=>({agent}))});
  page.openSpaceCard('checkout',foreign.key());page.openSpaceCard('another',live.key());assert.deepEqual(opened,[]);
  page.openSpaceCard('checkout',live.key());assert.deepEqual(opened,[current]);
  agents=[];page.openSpaceCard('checkout',live.key());assert.deepEqual(opened,[current]);
});

function sibling(id,slot,extra={},group='home',manager='work') {
  return row('session',{session_id:id,title:id,machine_id:'mac',cwd:'/repo',status:'idle',state:'idle',space_id:'w1',
    space_kind:'herdr',space_name:'weather',slot_id:'tab'+slot,slot_index:slot,slot_name:String(slot),...extra},group,manager);
}

test('the space strip follows native tab and pane order, keeps the current session in its pane and excludes foreign lookalikes',()=>{
  const current=sibling('review',3,{slot_name:'review',pane_id:'w1:p3',updated_at:'2026-11-15T00:00:00Z'});
  const other=sibling('after-clear',3,{pane_id:'w1:p3',updated_at:'2026-11-16T00:00:00Z'});
  const valid=[current,sibling('widget',1,{slot_name:'widget'}),sibling('tests',2,{slot_name:'tests'}),sibling('release',4)];
  const rejected=[other,sibling('wrong-machine',1,{machine_id:'other'}),sibling('other-hub',1,{},'elsewhere'),
    sibling('other-manager',1,{},'home','personal'),sibling('other-space',1,{space_id:'w2'}),sibling('other-host',1,{space_kind:'orca'}),
    sibling('stopped',1,{status:'offline'}),sibling('hidden',1,{assistant:true})];
  const result=sessionSpace(current,[...rejected,...valid],[],'home','work');
  assert.equal(result.title,'weather');assert.equal(result.current,'review');
  assert.deepEqual(result.tabs.map(t=>t.id),['widget','tests','review','release']);
  assert.equal(result.tabs.at(-1).label,'#4');
  const duplicates=sessionSpace(current,[current,sibling('c',5,{pane_id:'w1:pC',pane_index:2,agent:'codex'}),
    sibling('b',5,{pane_id:'w1:pB',pane_index:1,agent:'codex'}),sibling('a',5,{pane_id:'w1:pA',pane_index:0,agent:'claude'})],[],'home','work').tabs.slice(1);
  assert.deepEqual(duplicates.map(t=>[t.label,t.starter]),[['#5 · Claude',true],['#5 · Codex 1',false],['#5 · Codex 2',false]]);
});

test('a chat without a host space can show its registered project and never invents tabs from matching paths',()=>{
  const first=session('one'),second=session('two');
  const projects=projectDirectory([workspace('feature')],[first,second],[machine]);
  const result=sessionSpace(first,[first,second],projects,'home','work');
  assert.equal(result.title,projects[0].title);assert.deepEqual(result.tabs,[]);
  assert.equal(sessionSpace(first,[first,second],projects,'other','work').title,'');
  assert.deepEqual(sessionSpace(first,[],projects,'home','work').tabs,[]);
});

test('switching an agent tab replaces only the owning route and a stale tap cannot navigate',async()=>{
  const source=readFileSync(new URL('../entry/src/main/ets/pages/Index.ets',import.meta.url),'utf8');
  const code=source.slice(source.indexOf('  @Computed get phoneSessionSpace()'),source.indexOf('  private phoneLargeTitle()')).replace('@Computed ','');
  const {Wire}=await import('../entry/src/main/ets/models/Wire.ets');
  const Shell=new Function('SessionSpace','sessionSpace','Wire','return '+stripTypeScriptTypes('class Shell {'+code+'}'))(SessionSpace,sessionSpace,Wire);
  const current=sibling('review',3),next=sibling('tests',2),foreign=sibling('outside',2,{},'other');
  const names=['project_tree','session'],params=['weather','original'];let saved=0;
  const v=Object.assign(new Shell(),{model:{section:4},phoneTop:'session',phoneParam:'original',activeChild:'original',chatReady:true,
    targetReady:()=>v.chatReady,childKey:(_name,param)=>param,projectCatalog:{projects:[],browser:{}},
    detailModel:{connected:true,profile:{id:'home',manager:'work'},selected:current,sessions:[current,next,foreign]},
    stack:{size:()=>2,getAllPathName:()=>names,getParamByIndex:i=>params[i],replacePath:spec=>{params[1]=spec.param;}},
    routeRow:()=>current,routeTarget:(row,root)=>ResourceRow.from({...row.data(),navigation_root:root},'session'),
    savePath:()=>saved++});
  const key=v.phoneSessionSpace.key;
  v.switchSessionTab('wrong','review','tests');v.switchSessionTab(key,'else','tests');
  v.switchSessionTab(key,'review','outside');v.switchSessionTab(key,'review','review');
  assert.equal(saved,0);
  v.switchSessionTab(key,'review','tests');
  assert.equal(saved,1);assert.equal(params[0],'weather');
  const route=JSON.parse(params[1]);assert.equal(route.id,'tests');assert.equal(route.group,'home');assert.equal(route.manager,'work');
  assert.equal(JSON.parse(route.raw).navigation_root,false);
  v.switchSessionTab(key,'review','tests');assert.equal(saved,1);
});

test('a restored offline detail adopts live permissions without replacing session rows, and old menus recheck their scoped checkout', () => {
  const source = readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets', import.meta.url), 'utf8');
  const phoneBar=source.slice(source.indexOf('  @Builder spaceBar('),source.indexOf('  build()',source.indexOf('  @Builder spaceBar(')));
  assert.match(phoneBar,/ForEach\(this\.menuEntries\(this\.target\),[\s\S]*this\.more\(current\)[\s\S]*this\.headerIdentity\(current\)/);
  const methods = ['menuTarget','menuEntries','headerIdentity','startFromTree','openEditor'].map(name => {
    const start = source.indexOf('  private ' + name + '(');
    assert.notEqual(start, -1, name);
    const end = source.indexOf('\n  }', start);
    return source.slice(start, end + 4);
  });
  const Page = new Function('return ' + stripTypeScriptTypes('class Page {\n' + methods.join('\n') + '\n}'))();
  const catalog = new ProjectCatalog();
  const update = (online, cwd, agents = []) => {
    const current = workspace('shared', { online: true, available: true, cwd });
    if (!online) current.state = 'offline';
    const other = workspace('shared', { online:true, available:true, cwd });
    other.manager = 'different-owner';
    catalog.projects = projectDirectory([current,other], agents, [machine]);
    return catalog.projects.flatMap(p => p.trees).find(t => t.manager === 'work' && t.sourceId === 'shared');
  };
  const cached = update(false, '/repo/one');
  const opened = [], starts = [];
  const ui = Object.assign(new Page(), { catalog, page:'project_tree', target:cached.key, model:{},
    onStart: tree => starts.push(tree), editor:{busy:false, open: (_model,tree,action) => opened.push([tree,action])} });
  const oldKey = ui.headerIdentity(cached);
  assert.equal(ui.menuEntries(cached.key)[0].online, false);
  const online = update(true, '/repo/one');
  assert.notEqual(cached, online);
  assert.notEqual(ui.headerIdentity(online), oldKey);
  assert.equal(ui.menuEntries(cached.key)[0],online);
  ui.openEditor(cached,'create');
  assert.deepEqual(opened, [[online,'create']]);
  ui.startFromTree(cached.key);
  assert.deepEqual(starts,[online]);
  const changedAgents = update(true, '/repo/one', [session('added', { cwd:'/repo/one', workspace_id:'shared', space_kind:'orca' })]);
  assert.equal(ui.headerIdentity(changedAgents), ui.headerIdentity(online), 'activity alone does not recreate the header or menu');
  update(false, '/repo/one');
  ui.openEditor(online,'rename');
  update(true, '/repo/moved');
  ui.openEditor(online,'clean');
  assert.equal(opened.length,1,'the original menu cannot rename offline or remove a relocated checkout');
  ui.target = catalog.projects.flatMap(p => p.trees).find(t => t.manager === 'different-owner').key;
  assert.deepEqual(ui.menuEntries(cached.key),[]);
  ui.startFromTree(cached.key);
  assert.equal(starts.length,1,'the same host workspace id in another owner is a different route');
});
test('empty worktrees, independent roots and nested host worktrees retain stable identities', () => {
  const catalog = [
    workspace('main', { project_id: 'a', project_name: 'Repo', is_main: true }),
    workspace('feature', { project_id: 'b', parent_source_id: 'main' }),
    workspace('deep', { parent_source_id: 'feature' }),
    workspace('other', { project_id: 'a', project_name: 'Repo' }),
    workspace('other-engine', { source: 'paseo', project_id: 'a', project_name: 'Another repo' })
  ];
  const result = projectDirectory(catalog, [session('agent', { workspace_id: 'deep', space_kind: 'orca' })], [machine]);
  assert.equal(result.length, 2);
  const project = result.find(item => item.title === 'Repo');
  assert.deepEqual(project.roots.map(tree => tree.title), ['main', 'other']);
  assert.equal(project.sessionCount, 1);
  const child = project.roots[0].children[0].children[0];
  assert.equal(child.breadcrumb, 'main › feature › deep');
  assert.equal(child.sessions[0].id, 'agent');
  assert.equal(project.roots[1].canStart, true);
});
test('explicit engine IDs and ambiguous directories never capture a different workspace', () => {
  const a = workspace('one', { cwd: '/repo', project_id: 'repo' });
  const b = workspace('two', { cwd: '/repo', project_id: 'repo' });
  const other = workspace('one', { cwd: '/else', project_id: 'private' });
  other.group = 'another';
  const result = projectDirectory([a, b, other], [
    session('explicit', { workspace_id: 'one', space_kind: 'orca', cwd: '/else' }),
    session('ambiguous', { cwd: '/repo' }),
    session('foreign-engine', { workspace_id: 'one', space_kind: 'paseo', cwd: '/repo' })
  ], [machine]);
  const trees = result.flatMap(p => p.trees);
  assert.deepEqual(trees.find(t => t.group === 'home' && t.sourceId === 'one').sessions.map(s => s.id), ['explicit']);
  assert.equal(trees.find(t => t.group === 'another').sessions.length, 0);
  assert.deepEqual(trees.filter(t => !t.source).flatMap(t => t.sessions.map(s => s.id)).sort(), ['ambiguous', 'foreign-engine']);
});
test('older session directories group by repository and keep offline inventory visible', () => {
  const dir = workspace('dir', { source: 'session-directory', cwd: '/repo/feature', available: false });
  const projects = projectDirectory([dir], [session('s', { repo_root: '/repo', space_kind: 'codex_app' })], [machine]);
  assert.equal(projects.length, 1);
  assert.equal(projects[0].title, 'repo');
  assert.equal(projects[0].trees[0].canStart, false);
  assert.equal(projects[0].trees[0].sessions.length, 1);
});
test('cycles in host parent IDs leave every worktree reachable without a loop', () => {
  const result = projectDirectory([workspace('a', {parent_source_id: 'b'}), workspace('b', {parent_source_id: 'a'})], [], [machine]);
  assert.equal(result.length, 1);
  assert.equal(result[0].roots.length, 1);
  assert.equal(result[0].roots[0].children[0].children.length, 0);
});
test('reconnect retains an unchanged directory projection and incorporates in-place edits, authority and row order', async () => {
  const tree=workspace('root', {cwd:'/repo',is_main:true});
  const makeSession=(id,title)=>session(id,{title,workspace_id:'root',space_kind:'orca',status:'working'});
  const first=makeSession('s1','Original'),second=makeSession('s2','Second');
  const model={
    connected:true,sessionsReady:true,profile:{id:'home',manager:'work'},scope:()=>'home/work',
    directorySessions:[first,second],directoryMachines:[machine],directoryUnavailable:0,
    directoryOwners(){return [this]},owns:()=>true,connectionGeneration:()=>1,refreshDirectory:async()=>{},
    loadView:async()=>'',saveView:async()=>{},repo:()=>({call:async()=>({workspaces:[tree.data()]})})
  };
  const catalog=new ProjectCatalog();
  await catalog.refresh(model);
  const initial=catalog.projects;
  model.directorySessions=[makeSession('s1','Original'),makeSession('s2','Second')];
  await catalog.refresh(model);
  assert.equal(catalog.projects,initial,'refetched equivalent metadata should keep the existing observable tree');
  model.directorySessions[0].title='Changed on reconnect';
  catalog.syncRows(model);
  assert.notEqual(catalog.projects,initial);
  assert.equal(catalog.projects[0].trees[0].agents.find(a=>a.row.id==='s1').row.title,'Changed on reconnect');
  const beforeOrder=catalog.projects;
  model.directorySessions.reverse();
  catalog.syncRows(model);
  assert.notEqual(catalog.projects,beforeOrder);
  const beforeOffline=catalog.projects;
  model.directoryMachines=[row('machine',{id:'mac',name:'Studio',online:false})];
  catalog.syncRows(model);
  assert.notEqual(catalog.projects,beforeOffline);
  assert.equal(catalog.projects[0].trees[0].startAllowed,false);
  catalog.clear();
  assert.equal(catalog.projects.length,0);
  catalog.syncRows({...model,scope:()=>'other/manager'});
  assert.equal(catalog.projects.length,0);
});

test('a closed project search defers locale folding and reuses each folded Agent search key',()=>{
  const item=session('needle',{title:'İSTANBUL / 大模型 - É',status:'working'});
  const lower=String.prototype.toLocaleLowerCase;
  let agentFolds=0;
  String.prototype.toLocaleLowerCase=function(...args) {
    if(String(this).includes('İSTANBUL'))agentFolds++;
    return Reflect.apply(lower,this,args);
  };
  try{
    const projects=projectDirectory([], [item], [machine]);
    projectOutline(projects,new ProjectBrowserState());
    assert.equal(agentFolds,0);
    assert.deepEqual(projectSearchAgents(projects[0],'大模型').map(a=>a.row.id),['needle']);
    assert.deepEqual(projectSearchAgents(projects[0],'É').map(a=>a.row.id),['needle']);
    assert.equal(agentFolds,1);
  } finally {String.prototype.toLocaleLowerCase=lower;}
});

test('a stale manager reply cannot repopulate a different account', async () => {
  let complete;
  const owner = {
    profile: {id: 'home', manager: 'work'}, connected: true,
    scope: () => 'home/work', connectionGeneration: () => 1, owns: () => true,
    repo: () => ({call: () => new Promise(resolve => { complete = resolve; })})
  };
  const model = { ...owner, directoryMachines: [machine], directorySessions: [], directoryUnavailable: 0,
    refreshDirectory: async () => {}, directoryOwners: () => [owner] };
  const store = new ProjectCatalog();
  const request = store.refresh(model);
  await new Promise(resolve => setImmediate(resolve));
  store.clear();
  complete({workspaces: [workspace('old').data()]});
  await request;
  assert.deepEqual(store.projects, []);
  assert.equal(store.loading, false);
});
test('a failed read keeps last confirmed workspaces and exposes partial availability', async () => {
  let valid = true;
  const owner = {
    profile: {id: 'home', manager: 'work'}, connected: true, scope: () => 'home/work',
    connectionGeneration: () => 1, owns: () => true, repo: () => ({call: async () => {
      if (!valid) throw new Error('offline');
      return {workspaces: [workspace('confirmed').data()]};
    }})
  };
  const model = {...owner, directoryMachines: [machine], directorySessions: [], directoryUnavailable: 0,
    refreshDirectory: async () => {}, directoryOwners: () => [owner]};
  const store = new ProjectCatalog();
  await store.refresh(model);
  valid = false;
  await store.refresh(model);
  assert.equal(store.partial, true);
  assert.equal(store.projects[0].trees[0].sourceId, 'confirmed');
});

test('cold restoration shows saved scoped sessions offline, then accepts an authoritative empty session list', async () => {
  const tree = workspace('root', {cwd: '/repo'});
  const saved = JSON.stringify({
    workspaces: [tree.data()],
    sessions: [session('saved', {cwd: '/repo', workspace_id: 'root', space_kind: 'orca'}).data()],
    machines: [machine.data()]
  });
  const model = {
    connected: false, sessionsReady: false, profile: {id: 'home', manager: 'work'},
    scope: () => 'home/work', connectionGeneration: () => 1, owns: () => true,
    directorySessions: [], directoryMachines: [], directoryUnavailable: 0,
    directoryOwners() { return [this]; },
    refreshDirectory: async () => {}, loadView: async () => saved, saveView: async () => {},
    repo: () => ({call: async () => ({workspaces: [tree.data()]})})
  };
  const catalog = new ProjectCatalog();
  await catalog.refresh(model);
  assert.equal(catalog.projects[0].trees[0].sessions[0].state, 'offline');
  assert.equal(catalog.projects[0].trees[0].canStart, false);
  model.connected = true;
  model.sessionsReady = true;
  model.directoryMachines = [machine];
  await catalog.refresh(model);
  assert.equal(catalog.projects[0].trees[0].sessions.length, 0);
  assert.equal(catalog.projects[0].trees[0].canStart, true);
});

test('a worktree remains browsable while its computer is missing, offline or awaiting trust', () => {
  const tree = workspace('checkout', {cwd: '/repo'});
  for (const machines of [[], [row('machine', {id: 'mac', online: false})], [row('machine', {id: 'mac', online: true, trust: 'pending'})]]) {
    const project = projectDirectory([tree], [], machines)[0];
    assert.equal(project.trees.length, 1);
    assert.equal(project.trees[0].canStart, false);
  }
});

test('loose Git rows use the node checkout, and two repositories claiming its path never share row or route identities', () => {
  const rows = [
    session('project-scope',{cwd:'/workspace/repo/src',worktree:'/workspace/repo',repo_root:'/workspace'}),
    session('main',{cwd:'/workspace/repo/src',worktree:'/workspace/repo',repo_root:'/workspace/repo'})
  ];
  const project = input => projectDirectory([],input,[machine]).map(p => [p.title,p.roots[0].key,p.roots[0].main,p.roots[0].sessions.map(s => s.id)]);
  assert.deepEqual(project(rows),project(rows.toReversed()));
  const result = project(rows);
  assert.equal(result.length,2);
  assert.notEqual(result[0][1],result[1][1]);
  assert.equal(result.find(([title]) => title === 'repo')[2],true);
  assert.deepEqual(result.find(([title]) => title === 'workspace')[3],['project-scope']);
});

test('the phone outline puts busy agents directly below a lone main checkout and unfolds only matching search ancestors', () => {
  const make = (id,state,minute) => session(id,{cwd:'/repo/code',repo_root:'/repo',state,title:id,model:'model-'+id,updated_at:'2026-11-20T12:'+minute+':00Z'});
  const rows = [make('idle','idle','55'),make('older','working','01'),make('answer','needs_you','12'),make('newer','working','34')];
  const projects = projectDirectory([],rows,[machine]), state = new ProjectBrowserState();
  const visible = () => projectOutline(projects,state);
  assert.deepEqual(visible().map(l => l.kind),['project','session','session','session','idle-agents']);
  assert.deepEqual(visible().filter(l => l.agent).map(l => l.agent.row.id),['answer','newer','older']);
  assert.ok(visible().filter(l => l.agent).every(line => line.level === 1));
  state.toggle(visible()[0].key,true);
  assert.deepEqual(visible().map(l => l.kind),['project']);
  state.query = 'model-idle';
  assert.deepEqual(visible().map(l => l.kind),['project','session']);
  assert.equal(visible()[1].agent.row.id,'idle');
  state.query = '';
  assert.deepEqual(visible().map(l => l.kind),['project']);
});

test('nested empty workspaces keep their own controls and a search walks only matching children', () => {
  const projects = projectDirectory([workspace('main',{cwd:'/repo',is_main:true}),workspace('child',{parent_source_id:'main'}),
    workspace('grandchild',{parent_source_id:'child'}),workspace('sibling',{parent_source_id:'main'})],
    [session('wanted',{state:'working',title:'Fix the parser',workspace_id:'grandchild',space_kind:'orca'})],[machine]);
  const state = new ProjectBrowserState(), visible = () => projectOutline(projects,state);
  assert.deepEqual(visible().map(l => l.kind),['project','tree','children']);
  const disclosure = visible()[2]; state.toggle(disclosure.key,false);
  assert.ok(visible().some(l => l.tree?.sourceId === 'sibling'));
  state.query = 'parser';
  assert.deepEqual(visible().filter(l => l.kind==='tree').map(l => l.tree.sourceId),['main','child','grandchild']);
  const found = visible().at(-1);
  assert.equal(found.agent.row.id,'wanted');
  assert.equal(found.level, visible().find(line => line.kind === 'tree' && line.tree === found.tree).level);
  assert.ok(!visible().find(l => l.tree?.sourceId === 'sibling'));
});

test('machine filters and tree folding distinguish matching names across owners and follow host order', () => {
  const other = row('machine',{id:'other',name:'Studio',online:true},'another','work');
  const otherAgent = row('session',{id:'o',machine_id:'other',cwd:'/same',state:'working'},'another','work');
  const projects = projectDirectory([],[session('one',{cwd:'/same',state:'working'}),otherAgent],[other,machine]);
  const hosts = projectHosts(projects);
  assert.equal(hosts.length,2); assert.equal(hosts[0].name,hosts[1].name); assert.notEqual(hosts[0].key,hosts[1].key);
  assert.equal(projects[0].roots[0].group,'another');
  const state=new ProjectBrowserState(); state.machine=hosts[1].key;
  assert.deepEqual(projectOutline(projects,state).filter(l => l.agent).map(l => l.agent.row.id),['one']);
  state.toggle('project/'+projects[1].key,true);state.clear();
  assert.equal(state.machine,'');assert.equal(state.preferences.length,0);
});

test('large projects expose a reachable Show All without clipping downstream worktrees and locale numeric naming is stable', () => {
  const sessions = Array.from({length:29},(_,i) => session('agent-'+i,{cwd:'/repo/a',state:'working',repo_root:'/repo'}));
  const known = [workspace('10',{project_id:'group',project_name:'Z',title:'app10'}),workspace('2',{project_id:'group',project_name:'Z',title:'app2'})];
  const list = projectDirectory(known,sessions,[machine]), state = new ProjectBrowserState();
  const quiet = list.find(p => p.title==='Z');assert.deepEqual(quiet.roots.map(t => t.title),['app2','app10']);
  const project = list.find(p => p.title==='repo');
  let lines = projectOutline(list,state);
  assert.equal(lines.filter(l => l.kind==='session').length,20);
  assert.equal(lines.find(l => l.kind==='more').count,29);
  state.showAll(project.roots[0].key);lines=projectOutline(list,state);
  assert.equal(lines.filter(l => l.kind==='session').length,29);assert.ok(!lines.find(l=>l.kind==='more'));
});

test('a host tab groups real pane sessions in pane order, keeps its space cards available when folded, and reveals a searched child', () => {
  const pane = (id, overrides) => session(id, {
    cwd:'/repo/weather', space_kind:'herdr', space_id:'w1', space_name:'weather', slot_id:'w1:t4', slot_index:4,
    slot_name:'release', agent:'claude', ...overrides
  });
  const rows = [
    pane('child-z',{ pane_id:'w1:p2E', pane_index:2, state:'needs_you', title:'Review deployment' }),
    pane('first',{ pane_id:'w1:pW', pane_index:0, state:'idle', title:'Write notes' }),
    pane('child-a',{ pane_id:'w1:pX', pane_index:1, state:'working', title:'Check changelog' }),
    pane('single',{ slot_id:'w1:t8', slot_index:8, state:'working', title:'Another release' }),
    pane('no-slot',{ slot_id:null, state:'working', title:'Loose terminal' })
  ];
  const projects=projectDirectory([],rows,[machine]), state=new ProjectBrowserState(), tree=projects[0].roots[0];
  const visible=()=>projectOutline(projects,state), detail=()=>worktreeSpaceCards(projects,tree);
  const group=visible().find(line=>line.kind==='tab');
  assert.equal(group.tabTitle,'release');assert.equal(group.count,3);assert.equal(group.tabLead.row.id,'child-z');
  const members=visible().filter(line=>line.paneName);
  assert.deepEqual(members.map(line=>[line.agent.row.id,line.paneName,line.starter]),[
    ['first','Claude',true],['child-a','Claude 1',false],['child-z','Claude 2',false]
  ]);
  assert.equal(members[1].level,members[0].level+1);
  assert.deepEqual(visible().filter(line=>line.kind==='session').map(line=>line.agent.row.id).sort(),rows.map(row=>row.id).sort());
  state.toggle(group.key,true);
  assert.deepEqual(visible().filter(line=>line.kind==='session').map(line=>line.agent.row.id).sort(),['no-slot','single']);
  assert.deepEqual(detail().filter(line=>line.paneName).map(line=>line.agent.row.id), ['first','child-a','child-z']);
  state.query='deployment';
  const hit=visible().filter(line=>line.kind==='session');assert.equal(hit.length,1);
  assert.equal(hit[0].agent.row,rows[0]);assert.equal(hit[0].paneName,'Claude 2');assert.equal(visible().find(line=>line.kind==='tab').open,true);
  state.query='';assert.equal(visible().find(line=>line.kind==='tab').open,false);
});

test('the space grid collects live members across Git checkouts and rejects other hosts, owners and ambiguous local spaces', () => {
  const card=(id,path,space='s',extra={},group='home',manager='work')=>row('session',{session_id:id,title:id,machine_id:'mac',
    cwd:path,state:'working',repo_root:'/repo',worktree:path,space_id:space,space_kind:'herdr',slot_id:'t'+id,...extra},group,manager);
  const rows=[
    card('last','/repo', 's',{state:'idle',slot_index:9}),card('first','/repo','s',{state:'needs_you',slot_index:6,pending_interactions:2}),
    card('child','/repo/widget','s',{slot_index:5}),card('unread','/other','s',{state:'unread',slot_index:5}),
    card('other-space','/repo/fix','else'),card('other-engine','/other-engine','s',{space_kind:'orca'}),
    card('other-host','/repo','s',{machine_id:'second'}),card('other-hub','/repo','s',{},'other'),card('other-manager','/repo','s',{},'home','other'),
    card('offline','/repo/widget','s',{state:'offline'}),card('archived','/repo/widget','s',{archived:true})
  ];
  const grid=rows=>{
    const projects=projectDirectory([],rows,[machine]),owner=projects.flatMap(p=>p.trees).find(t=>t.agents.some(a=>a.row.id==='first'));
    return worktreeSpaceCards(projects,owner).map(line=>line.agent);
  };
  assert.deepEqual(grid(rows).map(a=>a.row.id),['first','unread','child','last']);
  assert.equal(grid(rows)[0].pending,2);
  assert.deepEqual(grid([...rows,card('unrelated-on-same-path','/repo','r')]).map(a=>a.row.id),['first','unrelated-on-same-path','last']);
});

test('project quick filters retain matching panes and disclose repositories inline without changing saved browsing folds', () => {
  const make=(id,state,extra={})=>session(id,{cwd:'/repo/weather',repo_root:'/repo/weather',title:'weather '+id,state,...extra});
  const muted=make('muted-work','working'), projects=projectDirectory([],[
    muted,make('need','needs_you'),make('read','unread'),make('active','working'),
    make('watch','working',{background_waiting:true}),make('archived','unread',{archived:true})
  ],[machine]);
  const browser=new ProjectBrowserState();browser.query='weather';
  const query=kind=>projectOutline(projects,browser,'','',kind,a=>a.row.id==='muted-work'||a.row.id==='need');
  const ids=kind=>query(kind).filter(l=>l.kind==='session').map(l=>l.agent.row.id);
  assert.deepEqual(ids('working'),['active']);assert.deepEqual(ids('agent_group_unread'),['read']);
  assert.deepEqual(ids('needs_you'),['need']);assert.deepEqual(ids('agent_group_background'),['watch']);
  const repo=query('working').find(l=>l.kind==='project');
  browser.toggleSearch(repo.key);assert.deepEqual(ids('working'),[]);assert.ok(query('working').find(l=>l.key===repo.key));
  assert.equal(browser.open(repo.key,true),true);browser.resetSearchFolds();assert.deepEqual(ids('working'),['active']);
});

test('the machine’s first repository precedes an unnamed project’s named Orca workspace and quiet Herdr checkouts stay reachable below them', () => {
  const collection=workspace('orca:collection',{title:'Workspace collection',cwd:'/code',source:'orca'});
  const notes=workspace('notes',{title:'notes',cwd:'/code/notes',source:'herdr'});
  const weather=session('weather-agent',{title:'Weather',state:'working',cwd:'/code/weather',repo_root:'/code/weather',
    todo:{id:'todo-weather',title:'Widget follow-up'}});
  const plain=session('standalone',{title:'Other',state:'idle',cwd:'/code/notes',space_kind:'herdr',workspace_id:'notes'});
  const projects=projectDirectory([collection,notes],[weather,plain],[machine]), browser=new ProjectBrowserState();
  const lines=projectOutline(projects,browser);
  assert.deepEqual(lines.filter(line=>line.kind==='project').map(line=>line.project.title),['weather','Workspace collection','notes']);
  assert.ok(lines.findIndex(line=>line.kind==='idle-projects')<lines.findIndex(line=>line.kind==='project' && line.project.title==='notes'));
  assert.equal(lines.find(line=>line.agent?.row.id==='weather-agent').agent.todoTitle,'Widget follow-up');
  browser.query='notes';assert.ok(projectOutline(projects,browser).some(line=>line.kind==='tree' && line.tree.sourceId==='notes'));
});

test('only Orca and Paseo catalog entries appear without a session in the root Agent tree', () => {
  const catalog = ['herdr', 'orca', 'paseo'].map(source => workspace(source, { source, cwd: '/code/' + source }));
  const browser = new ProjectBrowserState();
  const projects = projectDirectory(catalog, [], [machine]);
  const titles = (lines) => lines.filter(line => line.kind === 'project').map(line => line.project.title).sort();
  assert.deepEqual(titles(projectOutline(projects, browser)), ['orca', 'paseo']);
  const empty = projects.find(project => project.title === 'herdr');
  assert.ok(projectOutline(projects, browser, empty.key).some(line => line.tree?.sourceId === 'herdr'));
  assert.ok(titles(projectOutline(projects, browser, '', empty.hostKey)).includes('herdr'));
  browser.query = 'herdr';
  assert.deepEqual(titles(projectOutline(projects, browser)), ['herdr']);
  browser.query = '';
  const linked = session('attached', { cwd: '/code/herdr', workspace_id: 'herdr', space_kind: 'herdr' });
  assert.deepEqual(titles(projectOutline(projectDirectory(catalog, [linked], [machine]), browser)), ['herdr', 'orca', 'paseo']);
});

test('typing a repository name or a distinct Agent title narrows the status counts to the same Agent rows', () => {
  const rows=[session('one',{title:'a special issue',cwd:'/repo/weather',state:'needs_you'}),
    session('two',{title:'General',cwd:'/repo/weather',state:'unread'}),
    session('three',{title:'Outside',cwd:'/repo/other',state:'working'})];
  const projects=projectDirectory([],rows,[machine]);
  const matches=q=>projects.flatMap(p=>projectSearchAgents(p,q)).map(a=>a.row.id).sort();
  assert.deepEqual(matches('weather'),['one','two']);assert.deepEqual(matches('special'),['one']);
  assert.deepEqual(matches('never-matches'),[]);assert.deepEqual(matches(''),['one','three','two']);
});

test('repository Agent metadata keeps the to-do visible and drops the model before cutting the place name', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectAgentPlaceLine.ets',import.meta.url),'utf8');
  const getter=source.match(/get fit\(\): string \{[\s\S]+?^  \}/m)[0];
  const tokens={interfaceFontVp:(points,scale)=>Math.min(points*scale,points+6)+'vp',interfaceScaleFor:points=>(points+6)/points};
  const Component=new Function('Tokens','tr','return '+stripTypeScriptTypes('class Line {'+getter+'}'))(tokens,key=>key);
  const line=Object.assign(new Component(),{place:'tests',modelName:'claude-sonnet-5-5',showsSource:true,todoTitle:'task',fontScale:1});
  const widths={terminal:26,tests:29,'claude-sonnet-5-5':105,opus:27,task:103,'Claude 1':51};
  line.getUIContext=()=>({px2vp:v=>v,getMeasureUtils:()=>({measureText:({textContent,fontSize})=>
    widths[textContent]*parseFloat(fontSize)/(textContent==='claude-sonnet-5-5'||textContent==='opus'?12:13)})});
  line.availableWidth=360; assert.equal(line.fit,'full');
  line.availableWidth=260; assert.equal(line.fit,'todo-icon');
  line.fontScale=3.2; line.availableWidth=242; assert.equal(line.fit,'place');
  line.modelName='opus'; line.place='Claude 1'; assert.equal(line.fit,'todo-icon');
  line.availableWidth=113; assert.equal(line.fit,'place');
  line.availableWidth=360; line.todoTitle=''; assert.equal(line.fit,'full');
});

test('the full inferred-worktree row folds, while a host workspace or search result opens', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const method=source.match(/^  private treeRowFolds\(line:[\s\S]+?^  \}/m)[0];
  const Page=new Function('return '+stripTypeScriptTypes('class Page {'+method+'}'))();
  const page=new Page();
  const inferred={count:3,tree:{source:'',children:[]}};
  page.filteringText=()=>false;
  assert.equal(page.treeRowFolds(inferred),true);
  assert.equal(page.treeRowFolds({count:0,tree:{source:'',children:[inferred]}}),true);
  assert.equal(page.treeRowFolds({count:3,tree:{source:'herdr',children:[]}}),false);
  page.filteringText=()=>true;
  assert.equal(page.treeRowFolds(inferred),false);
});

test('a mounted repository header reads the current fold on every tap of its original callback', () => {
  const body=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8')
    .match(/^  private toggle\(line:[\s\S]+?^  \}/m)[0];
  const Page=new Function('return '+stripTypeScriptTypes('class Page {'+body+'}'))(),state=new ProjectBrowserState();
  const projects=projectDirectory([],[session('one',{state:'working'})],[machine]),received=[];
  const page=Object.assign(new Page(),{model:{},catalog:{toggleBrowserFold(_m,key,open){received.push(open);state.toggle(key,open)}}});
  Object.defineProperty(page,'lines',{get:()=>projectOutline(projects,state)});
  const first=page.lines.find(row=>row.kind==='project');const click=()=>page.toggle(first);
  assert.equal(first.open,true);click();assert.equal(page.lines.find(l=>l.key===first.key).open,false);
  click();assert.equal(page.lines.find(l=>l.key===first.key).open,true);assert.deepEqual(received,[true,false]);
});

test('a machine link owns its scope and ignores the root search and another Hub’s saved machine filter', () => {
  const a=session('A',{cwd:'/repo/weather',state:'working'}),other=row('session',{session_id:'B',machine_id:'mac',cwd:'/other',state:'working'},'other');
  const projects=projectDirectory([],[a,other],[machine]),state=new ProjectBrowserState();
  const selected=projects.find(p=>p.trees.some(t=>t.agents.some(a=>a.row.id==='A')));
  const foreign=projects.find(p=>p!==selected);state.query='cannot-match';state.machine=foreign.hostKey;
  assert.deepEqual(projectOutline(projects,state,'',selected.hostKey).filter(l=>l.kind==='session').map(l=>l.agent.row.id),['A']);
  assert.equal(state.query,'cannot-match');assert.equal(state.machine,foreign.hostKey);
  assert.deepEqual(projectOutline(projects,state,'','missing-host'),[]);
});

test('tab identity is scoped to checkouts, hosts, managers and source spaces, and two sessions sharing a pane identifier stay accessible', () => {
  const rows=[];
  const pair=(scope,extra,group='home',manager='work')=>{
    for(const suffix of ['a','b']) rows.push(row('session',{
      session_id:scope+suffix,cwd:'/r',machine_id:'mac',space_kind:'herdr',space_id:'shared',
      slot_id:'s4',slot_name:'release',pane_id:'w1:p4',state:'working',agent:'claude',...extra
    }, group,manager));
  };
  pair('root',{});pair('other-tab',{slot_id:'s8'});pair('other-place',{cwd:'/x'});pair('other-space',{space_id:'w2'});
  pair('other-host',{machine_id:'other'});pair('other-hub',{},'second');pair('other-manager',{},'home','personal');
  const projects=projectDirectory([],rows,[machine]),state=new ProjectBrowserState();
  const view=()=>projectOutline(projects,state), groups=view().filter(line=>line.kind==='tab');
  assert.equal(groups.length,7);assert.equal(new Set(groups.map(line=>line.key)).size,7);
  assert.ok(groups.every(line=>line.count===2 && line.tabTitle==='release'));
  const visible=view().filter(line=>line.kind==='session');
  assert.equal(new Set(visible.map(line=>line.agent.row.key())).size,14);
  const target=groups.find(line=>line.tabLead.row.id==='roota');
  assert.deepEqual(visible.filter(line=>line.agent.row.id.startsWith('root')).map(line=>line.paneName),['Claude 1','Claude 2']);
  state.toggle(target.key,true);assert.equal(view().filter(line=>line.kind==='session').length,12);
});

test('one mounted tab header keeps its arrow, spoken action and handler in sync through repeated taps and a search round trip', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const functions=['tabOpen','toggleTab','tabDescription'].map(name=>
    source.match(new RegExp('^  private '+name+'\\([\\s\\S]+?^  \\}', 'm'))[0]);
  const browser=source.match(/^  private get outlineBrowser\([\s\S]+?^  \}/m)[0];
  const getter=source.match(/^  @Computed get tabRows\([\s\S]+?^  \}/m)[0].replace('@Computed ','');
  const query=source.match(/^  private filteringText\([^\n]+$/m)[0];
  const Page=new Function('tr','return '+stripTypeScriptTypes('class Page {\n'+[browser,getter,query,...functions].join('\n')+'\n}'))(
    key=>key==='project_agent_count'?'{count} agents':key);
  const state=new ProjectBrowserState(), received=[];
  const pane=id=>session(id,{cwd:'/repo',state:'working',space_kind:'herdr',space_id:'w1',slot_id:'w1:t4',slot_name:'release',agent:'claude'});
  let projects=projectDirectory([],[pane('one'),pane('two')],[machine]);
  const page=Object.assign(new Page(),{page:'projects',model:{},catalog:{browser:state,toggleBrowserFold(model,key,open){
    assert.equal(model,page.model);received.push(open);state.toggle(key,open);
  }}});
  Object.defineProperty(page,'lines',{get:()=>projectOutline(projects,state)});
  Object.defineProperty(page,'detailSessions',{get:()=>worktreeSpaceCards(projects,projects[0].roots[0])});
  const original=page.lines.find(row=>row.kind==='tab'), key=original.key;
  // Hold on to this one callback as ArkUI does for the reusable native row. Rebuilding it on each tap hides the bug.
  const click=()=>page.toggleTab(key);
  const verify=(open,panes)=>{
    assert.equal(page.tabOpen(key),open);
    assert.equal(page.lines.filter(row=>row.kind==='session').length,panes);
    assert.ok(page.tabDescription(key).endsWith(open?'agent_collapse':'agent_expand'));
  };
  verify(true,2);
  for (let i=0;i<2;i++){click();verify(false,0);click();verify(true,2);}
  assert.equal(original.open,true);
  click();verify(false,0);
  state.query='release';assert.equal(page.tabOpen(key),true);assert.equal(page.lines.filter(row=>row.kind==='session').length,2);
  click();assert.equal(received.length,5);assert.ok(!page.tabDescription(key).includes('agent_collapse'));
  state.query='';verify(false,0);click();verify(true,2);
  page.page='project_tree';click();assert.equal(page.detailSessions.length,2);
  page.page='projects';projects=[];click();assert.equal(received.length,6);assert.equal(page.tabDescription(key),'');
  const builder=source.slice(source.indexOf('  @Builder tabLine('),source.indexOf('  @Builder counts('));
  assert.equal((builder.match(/\.id\('projects\.tab'\)/g)||[]).length,1);
  assert.match(builder,/onClick\(\(\) => this\.toggleTab\(key\)\)/);
  assert.match(builder,/rotate\(\{angle:this\.tabOpen\(key\)/);
});

test('Agents uses only its admitted inventory and exposes registered workspaces of known, admitted owners', () => {
  const parent=workspace('main',{is_main:true}), child=workspace('feature',{parent_source_id:'main'}),
    idle=workspace('demo',{title:'Demo release',source:'paseo',pinned_at:'2026-02-02T09:00:00Z'}),
    other=workspace('not-registered',{source:'herdr'}), hidden=workspace('private-preview');
  hidden.group='other';
  const duplicate=session('pinned',{state:'offline',space_kind:'paseo',workspace_id:'demo'});
  const foreign=session('foreign',{state:'offline',space_kind:'paseo',workspace_id:'demo'});foreign.group='other';
  const active=session('body-match',{state:'working',workspace_id:'feature',space_kind:'orca'});
  const archived=session('archived',{state:'working',archived:true}), offline=session('other-offline',{state:'offline'});
  const model={scope:()=>'home',directoryMachines:[machine]};
  const catalog=new ProjectCatalog(), raw=[parent,child,idle,other,hidden];
  catalog.ownerScope='home';catalog.snapshots=new Map([['home',raw]]);
  catalog.projects=projectDirectory(raw,[active,archived,offline,duplicate,foreign],[machine]);
  const canonical=catalog.projects.flatMap(p=>p.trees.map(t=>t.key));
  const visible=(query,empty,admission=()=>true,rows=[active,archived,offline,duplicate,foreign])=>
    catalog.agentDirectory(model,rows,empty,query,admission);
  const trees=projects=>projects.flatMap(p=>p.trees);
  const agents=projects=>trees(projects).flatMap(t=>t.agents.map(a=>a.row.key()));
  assert.deepEqual(agents(visible('',true)).sort(),[active.key(),duplicate.key()].sort());
  assert.deepEqual(trees(visible('',true)).map(t=>t.sourceId).sort(),['main','feature','demo'].sort());
  assert.deepEqual(trees(visible('release',true,()=>true,[])).map(t=>t.sourceId),['demo']);
  assert.deepEqual(visible('nonexistent',true,()=>true,[]),[]);
  assert.deepEqual(visible('',true,(g,m,id)=>g==='home' && m==='other' && id==='mac'),[]);
  const filtered=visible('from transcript',false,()=>true,[active]);
  assert.deepEqual(trees(filtered).map(t=>t.sourceId).sort(),['main','feature'].sort());
  const state=new ProjectBrowserState();state.query='from transcript';
  assert.deepEqual(projectOutline(filtered,state,'','','',()=>false,true,true)
    .filter(l=>l.kind==='session').map(l=>l.agent.row.key()),[active.key()]);
  assert.deepEqual(catalog.projects.flatMap(p=>p.trees.map(t=>t.key)),canonical);
  model.scope=()=>'switched';assert.deepEqual(visible('',true,()=>true,[]),[]);
});

test('the newly selected empty terminal checkout appears on its original repository list only when the live owner and filters admit it',()=>{
  const parent=workspace('parent',{source:'herdr',title:'login-form'});
  const child=workspace('w3',{source:'herdr',title:'qa/mini-login-form',branch:'qa/mini-login-form',
    cwd:'/checkouts/qa-mini-login-form',parent_source_id:'parent'});
  const raw=[parent,child], catalog=new ProjectCatalog(), model={scope:()=>'home',directoryMachines:[machine]};
  catalog.ownerScope='home';catalog.snapshots.set('home',raw);catalog.projects=projectDirectory(raw,[],[machine]);
  const tree=catalog.projects.flatMap(p=>p.trees).find(t=>t.sourceId==='w3');
  let live=true;
  const receipt=Object.assign(new ProjectWorktree(),tree,{receiptCurrent:()=>live});
  const listed=(query,empty,admit=()=>true)=>catalog.agentDirectory(model,[],empty,query,admit).flatMap(p=>p.trees.map(t=>t.sourceId));
  assert.deepEqual(listed('login-form',true),[]);
  catalog.selectCreated(receipt);catalog.agentBrowser.selectedTree=tree.key;
  assert.deepEqual(listed('login-form',true).sort(),['parent','w3']);
  assert.deepEqual(listed('no match',true),[]);assert.deepEqual(listed('login-form',false),[]);
  assert.deepEqual(listed('',true,()=>false),[]);
  live=false;assert.deepEqual(listed('login-form',true),[]);
  live=true;tree.cwd='/different checkout';assert.deepEqual(listed('login-form',true),[]);
});

test('Agents repository routes stay on the owning stack and refuse stale targets', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/pages/Index.ets',import.meta.url),'utf8');
  const method=source.match(/^  private openAgentRepositoryRoute\([\s\S]+?^  \}/m)[0];
  const Page=new Function('return '+stripTypeScriptTypes('class Page {'+method+'}'))();
  const calls=[], tree={key:'workspace',canStart:true};
  const page=Object.assign(new Page(),{model:{section:1},projectCatalog:{tree:k=>k==='workspace'?tree:undefined,
    projects:[{key:'repository',hostKey:'the-owner'}]},pushPlace:(name,key)=>calls.push([name,key]),prepareAgent:(_,t)=>calls.push(['start',t.key])});
  for (const [name,key] of [['project','repository'],['project_tree','workspace'],['start','workspace'],['machine_projects','the-owner'],
    ['project_tree','repository'],['project','workspace'],['machine_projects','other-owner']]) page.openAgentRepositoryRoute(name,key);
  page.model.section=4;page.openAgentRepositoryRoute('project','repository');
  assert.deepEqual(calls,[['project','repository'],['project_tree','workspace'],['start','workspace'],['machine_projects','the-owner']]);
});

test('an open worktree form adopts its same live name and retries only a fresh read on that checkout', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const methods=source.slice(source.indexOf('  private currentEditorTree('),source.indexOf('  @Builder editorSheet('))
    .replace(/@Monitor\([^)]*\) /g,'');
  const Page=new Function('return '+stripTypeScriptTypes('class Page {'+methods+'}'))();
  const old={key:'owner/tree',cwd:'/work/branch',title:'old'}, fresh={...old,title:'new'};
  let canonical=fresh;const calls=[];
  const editor={action:'clean',title:'old',locationTitle:'old',open:(model,target,action)=>calls.push([target,action])};
  const page=Object.assign(new Page(),{editedTree:old,menuTarget:k=>k===canonical.key?canonical:undefined,editorShown:true,editor});
  page.editorLocationChanged();assert.equal(editor.locationTitle,'new');assert.equal(editor.title,'old');
  editor.unknown=true;page.retryEditor();assert.deepEqual(calls,[]);
  editor.unknown=false;page.retryEditor();assert.deepEqual(calls,[[fresh,'clean']]);
  canonical={...fresh,cwd:'/different/checkpoint'};page.retryEditor();assert.equal(editor.error,'request_updated');assert.equal(calls.length,1);
});

test('project search counts scoped pending sessions once across the search; terminal and unread admit only their real query matches', async () => {
  const {projectSearchResult}=await import('../entry/src/main/ets/models/ProjectSearch.ets');
  const owns=(id)=>({profile:{id,manager:'assistant'},owns:(generation,scope)=>generation===1 && scope===id});
  const local=owns('local'), other=owns('other'), removed=owns('removed');
  const root={directoryOwners:()=>[local,other]};
  const row=(name,state,owner='local',extra={})=>{
    const result=ResourceRow.from({session_id:name,machine_id:'mac',manager:'assistant',state,...extra},'session');
    result.group=owner;return result;
  };
  const wait=row('parent','needs_you'), outside=row('outside','idle'), cross=row('outside','idle','other'),
    done=row('done','unread'), masked=row('mask','needs_you','local',{hidden_by_directory:true}),
    persona=row('persona','needs_you','local',{assistant:true});
  const rows=[wait,outside,cross,done,masked,persona];
  const ask=(id,session_id,extra={})=>({id,session_id,machine_id:'mac',...extra});
  const snapshot=(owner,records,generation=1)=>({owner,scope:owner.profile.id,generation,records});
  const pending=[snapshot(local,[ask('1','parent'),ask('2','child',{folded_under:'parent'}),ask('3','outside'),
    ask('4','outside'),ask('5','mask'),ask('6','persona'),ask('7','stale',{status:'resolved'}),
    ask('8','unlisted'),ask('9',''),ask('9',''),ask('10','hidden',{manager:'other'})]),
    snapshot(other,[ask('11','outside')]),snapshot(local,[ask('old-owner-generation','old')],0),
    snapshot(removed,[ask('lost-owner','lost')])];
  const search=(quick,machine,filtered)=>projectSearchResult(root,rows,{queryMatches:[wait.key(),done.key()],
    filtered:filtered.map(r=>r.key()),filter_counts:{needsYou:1,unread:1,working:0,terminal:1}},
    pending,machine,quick,row=>row.key());
  const general=search('all','',[wait,done]);
  assert.deepEqual(general.counts,{needsYou:5,unread:1,working:0,terminal:1});
  assert.deepEqual(general.rows,new Set([wait.key(),done.key()]));
  const localMachine=JSON.stringify(['local','assistant','mac']);
  const needs=search('needsYou',localMachine,[wait]);
  assert.equal(needs.counts.needsYou,4);
  assert.deepEqual(needs.rows,new Set([wait.key(),outside.key()]));
  assert.deepEqual(search('terminal',localMachine,[done]).rows,new Set([done.key()]));
  assert.deepEqual(search('unread',localMachine,[done]).rows,new Set([done.key()]));
  pending.splice(0,pending.length,snapshot(local,[
    {interaction:ask('supervised','child',{folded_under:'parent',kind:'screen',delegating:true})},
    ask('outside-agent','',{kind:'plan',delegating:true})
  ]));
  assert.equal(search('needsYou',localMachine,[wait]).counts.needsYou,0);
  assert.deepEqual(search('needsYou',localMachine,[wait]).rows,new Set());
  pending[0].records.push(ask('mine','parent',{kind:'question',delegating:true}));
  assert.deepEqual(search('needsYou',localMachine,[wait]).rows,new Set([wait.key()]));

});

test('the repository tree keeps matching workspace paths with no sessions and projects only the selected source rows', () => {
  const weather=workspace('weather',{title:'weather',cwd:'/repo/weather',is_main:true});
  const terminal=session('herdr',{cwd:'/repo/weather',space_kind:'orca',workspace_id:'weather',pane_id:'w1:p1'});
  const native=session('native',{cwd:'/repo/weather',space_kind:'orca',workspace_id:'weather',pane_id:'orca:p2'});
  const state=new ProjectBrowserState();state.query='weather';
  const projects=projectDirectory([weather],[terminal,native],[machine]);
  const shown=(quick,keys)=>projectOutline(projects,state,'','',quick,()=>false,false,false,new Set(keys));
  assert.ok(shown('' ,[]).some(line=>line.kind==='tree' && line.tree.sourceId==='weather'));
  assert.ok(!shown('',[]).some(line=>line.kind==='session'));
  const selected=shown('terminal',[terminal.key()]);
  assert.deepEqual(selected.filter(line=>line.kind==='session').map(line=>line.agent.row.key()),[terminal.key()]);
  assert.equal(shown('terminal',[]).length,0);
  assert.equal(state.query,'weather');
});

test('active project search follows the same fixed navigation field and separate scalable filter controls as compact Agents', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const search=source.split('@Builder searchAndFilter()')[1].split('@Builder projectMenu(')[0];
  assert.match(search,/\.height\(this\.compact \? 44 :/);
  assert.match(search,/\.margin\(this\.compact \? 0 :/);
  assert.match(search,/Row\(\{space:this\.searchMode \? 11 : 8\}\)/);
  assert.match(search,/\.width\(44\)\.height\(44\).*\n[^\n]*\.accessibilityText\(tr\('cancel'\)\)/);
  assert.match(search,/Column\(\{space:this\.searchMode \? 20 : 6\}\)/);
  assert.match(search,/\.textFont\(\{size:this\.searchTextSize\(\)\}\)\.placeholderFont\(\{size:this\.searchTextSize\(\)/);
  assert.match(source,/this\.compact \? String\(PhoneStyle\.formSize\(17, 22\.2, nativeEnvironment\.fontScale\) \* Math\.min\(1, nativeEnvironment\.fontScale\)\) \+ 'vp' : '16fp'/);
  assert.match(source,/compactSearchIcon\(\): string \{\n\s+return String\(PhoneStyle\.formSize\(20, 27\.33, nativeEnvironment\.fontScale\)\)/);
  assert.match(search,/\.searchIcon\(this\.compact \? \{size:this\.compactSearchIcon\(\)/);
  assert.match(search,/CancelButtonStyle\.INPUT,icon:\{\s*size:PhoneStyle\.formSize\(24,32,nativeEnvironment\.fontScale\),color:\$r\('app\.color\.persona_switcher_ink'\),src:\$r\('app\.media\.phone_search_clear'\)/);
  assert.match(source,/quickHeight\(\):number \{ return PhoneStyle\.formSize\(44,57\.7,/);
  assert.match(search,/\.align\(Alignment\.Start\)\.scrollable\(ScrollDirection\.Horizontal\)/);
});

test('the compact project quick bar opens the real grouping menu and restores a filtered project search after returning', () => {
  const source=readFileSync(new URL('../entry/src/main/ets/components/ProjectsPage.ets',import.meta.url),'utf8');
  const bar=source.slice(source.indexOf('@Builder searchAndFilter()'),source.indexOf('@Builder projectMenu('));
  assert.match(bar,/Divider\(\)\.vertical\(true\)[^\n]*\n\s*this\.searchGrouping\(\)/);
  const menu=source.slice(source.indexOf('@Builder searchGroupingMenu()'),source.indexOf('private quickHeight()'));
  assert.match(menu,/this\.searchGroups\.groupingOptions\(\)/);
  assert.match(menu,/this\.selectSearchGrouping\(key\).*projects\.grouping\./);
  assert.match(menu,/this\.machineMenu\(\).*projects\.machine/);
  assert.match(menu,/\.bindMenu\(this\.searchGroupingMenu\)\.id\('projects\.grouping'\)/);
  const lifecycle=source.match(/^  aboutToAppear\(\): void \{[\s\S]+?^  \}/m)[0];
  const Page=new Function('return '+stripTypeScriptTypes('class Page {'+lifecycle+'}'))();
  const catalog=new ProjectCatalog(),calls=[];
  Object.assign(catalog.browser,{searchActive:true,query:'',searchQuick:'unread',searchGrouping:'machine',machine:'owner-scoped'});
  const page=Object.assign(new Page(),{page:'projects',compact:true,catalog,onSearchMode:active=>calls.push(active),
    watchSearchOwners(){calls.push('observe')},prepareSearchGroups(){calls.push('restore')}});
  page.aboutToAppear();
  assert.deepEqual(calls,[true,'observe','restore']);assert.equal(page.quick,'unread');
  assert.deepEqual([catalog.browser.query,catalog.browser.searchGrouping,catalog.browser.machine],['','machine','owner-scoped']);
  catalog.browser.clear();assert.deepEqual([catalog.browser.searchActive,catalog.browser.searchQuick,catalog.browser.searchGrouping,catalog.browser.machine],[false,'','','']);
});
