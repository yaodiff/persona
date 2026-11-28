import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import test from 'node:test';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('./'))
      return { url:new URL(specifier+'.ets',context.parentURL).href, shortCircuit:true };
    return next(specifier,context);
  },
  load(url, context, next) {
    if (!url.endsWith('.ets')) return next(url,context);
    const source=readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,'');
    return {format:'module',source:stripTypeScriptTypes(source),shortCircuit:true};
  }
});
const { AgentDecisionCard, AgentDecisionQueue }=await import('../entry/src/main/ets/models/AgentDecisionQueue.ets');
const { interactionResolution, recommendedOptionTitle }=await import('../entry/src/main/ets/models/InteractionDecision.ets');
const { ResourceRow, Wire }=await import('../entry/src/main/ets/models/Wire.ets');

function pair(id) {
  const row=ResourceRow.from({id:'s',session_id:'s',manager:'assistant',machine_id:'machine'},'session');
  row.group=id;
  const state={writes:[],calls:0,blocked:false,canApprove:true,rows:[{id:'ask',session_id:'s',machine_id:'machine',
    kind:'question',questions:[{question:'Which color?',options:[{label:'Blue'},{label:'Green'}]}]}]};
  const repo={async decisions(){ state.calls++; return structuredClone(state.rows); },
    async resolve(request,action,values,op,admit){
      assert.equal(admit(),true); state.writes.push({body:interactionResolution(request,action,values),op}); state.rows=[];
    }};
  const owner={connected:true,profile:{manager:'assistant',id},connectionGeneration(){return 1;},scope(){return id;},
    owns(epoch,scope){return epoch===1&&id===scope;},repo(){return repo;},canApproveSession(){return state.canApprove;},
    operationBlocked(){return state.blocked;}};
  return {row,state,repo,owner};
}
function rootFor(pairs) {
  return {connectionGeneration:()=>1,scope:()=>'root',owns:(n,s)=>n===1 && s==='root',directoryOwners:()=>pairs.map(p=>p.owner),
    directoryOwnerFor:row=>pairs.find(p=>p.row.group===row.group)?.owner};
}

test('identical request IDs on two paired Hubs resolve only on the displayed session owner',async()=>{
  const a=pair('one'), b=pair('two'), root=rootFor([a,b]), queue=new AgentDecisionQueue();
  await queue.reload(root);
  const card=queue.forSession(root,a.row)[0];
  assert.deepEqual(card.quickOptions(),['Blue','Green']);
  await queue.answer(root,b.row,card,'Blue','bad',()=>true);
  assert.equal(b.state.writes.length,0);
  await Promise.all([queue.answer(root,a.row,card,'Blue','one',()=>true),queue.answer(root,a.row,card,'Green','two',()=>true)]);
  assert.deepEqual(a.state.writes,[{body:{answers:{'Which color?':'Blue'}},op:'one'}]);
  assert.equal(card.request.status,'resolved');
  assert.equal(b.state.rows.length,1);
});

test('a supervisor-held approval never has quick actions, including when ownership changes just before a tap',async()=>{
  const p=pair('one'), other=pair('other'), root=rootFor([p,other]), queue=new AgentDecisionQueue();
  const approval={id:'approval',machine_id:'machine',session_id:'s',kind:'permission',tool_name:'Read',tool_input:{file_path:'README.md'}};
  const question={...p.state.rows[0],delegating:true};
  p.state.rows=[{interaction:{...approval,delegating:true}},question];
  await queue.reload(root);
  assert.deepEqual(queue.forSession(root,p.row).map(card=>card.request.id),['ask']);
  assert.deepEqual(queue.cards.filter(card=>card.supervisorHeld).map(card=>card.request.id),['approval']);
  assert.deepEqual(queue.cards.find(card=>card.supervisorHeld).quickOptions(),[]);
  assert.deepEqual(queue.forSession(root,p.row)[0].quickOptions(),['Blue','Green']);
  p.state.rows=[{...approval,delegating:false}];
  await queue.reload(root);
  const visible=queue.forSession(root,p.row)[0];
  assert.deepEqual(visible.quickOptions(),['allow','deny']);
  p.state.rows=[{...approval,delegating:true}];
  await queue.answer(root,p.row,visible,'allow','changed',()=>true);
  assert.equal(p.state.writes.length,0);
  assert.equal(queue.forSession(root,p.row).length,0);
  assert.equal(queue.forSession(root,other.row).length,1);
  p.state.rows=[{...approval,delegate:{reason:'Owner input requested'}}];
  await queue.reload(root);
  const escalated=queue.forSession(root,p.row)[0];
  await queue.answer(root,p.row,escalated,'allow','escalated',()=>true);
  assert.deepEqual(p.state.writes,[{body:{allow:true},op:'escalated'}]);
  assert.deepEqual(other.state.writes,[]);
});

test('an outdated request, read-only session, or uncertain mutation cannot be answered from the list',async()=>{
  const p=pair('one'), root=rootFor([p]), queue=new AgentDecisionQueue();
  await queue.reload(root);
  const initial=queue.forSession(root,p.row)[0];
  p.state.canApprove=false;
  assert.equal(queue.enabled(root,p.row,initial),false);
  p.state.canApprove=true; p.state.blocked=true;
  assert.equal(queue.enabled(root,p.row,initial),false);
  p.state.blocked=false; p.state.rows[0].questions[0].question='Different prompt';
  await queue.answer(root,p.row,initial,'Blue','stale',()=>true);
  assert.equal(p.state.writes.length,0);
  assert.equal(queue.error,'request_updated');
  const current=queue.forSession(root,p.row)[0];
  await queue.answer(root,p.row,current,'missing','bad',()=>true);
  assert.equal(p.state.writes.length,0);
  queue.clear();
  assert.equal(queue.enabled(root,p.row,current),false);
});

test('single-owner forms skip unrelated Hubs on open, submit, and refresh',async()=>{
  const a=pair('one'), b=pair('two'), root=rootFor([a,b]), queue=new AgentDecisionQueue();
  await queue.reload(root,a.owner);
  const card=queue.forSession(root,a.row)[0];
  await queue.answerForm(root,a.row,card,'answer',{'Which color?':'Green'},'only',()=>true);
  assert.equal(a.state.writes[0].body.answers['Which color?'],'Green');
  assert.equal(b.state.calls,0);
  assert.equal(queue.forSession(root,b.row).length,0);
});

test('navigation or owner changes while validating a response prevent the mutation',async()=>{
  const p=pair('one'),root=rootFor([p]),queue=new AgentDecisionQueue();
  await queue.reload(root);
  const card=queue.forSession(root,p.row)[0], finish=Promise.withResolvers();
  p.repo.decisions=()=>finish.promise;
  const answer=queue.answer(root,p.row,card,'Blue','stale',()=>true);
  queue.clear(); finish.resolve(p.state.rows);
  await answer;
  assert.equal(p.state.writes.length,0);
});

test('each supervising task displays the shared Agent question once, while one answer clears every occurrence',async()=>{
  const {owner,row,state}=pair('one'), other=pair('two');
  Object.assign(owner,{directoryOwners:()=>[owner,other.owner],directoryOwnerFor:agent=>agent.group==='one' ? owner : other.owner,
    sessions:[other.row,row]});
  const requests=new AgentDecisionQueue(); await requests.reload(owner,owner);
  const source=readFileSync(new URL('../entry/src/main/ets/components/AssistantCards.ets',import.meta.url),'utf8');
  const question=source.match(/^class TaskQuestionRow \{[\s\S]*?^\}/m)[0];
  const getter=source.match(/  @Computed (get taskQuestions\(\)[\s\S]*?^  \})/m)[1];
  const Panel=new Function('Wire',stripTypeScriptTypes(question+'\nclass Panel {'+getter+'}')+'\nreturn Panel;')(Wire);
  const linked={machine_id:'machine',session_id:'s'};
  const todo=(id,day,agents,extra={})=>({id,title:id,origin:'chat',created_at:'2026-06-'+day+'T08:00:00Z',agents,...extra});
  const feed={owner:'assistant',conversationId:'chat',todosReady:true,todos:[
    todo('older','01',[linked]),todo('newer','02',[linked,linked]),todo('elsewhere','03',[linked],{origin:'another'}),
    todo('released','03',[{...linked,released_at:'2026-06-02'}]),todo('foreign','03',[linked],{manager:'foreign'}),
    todo('missing','03',[{...linked,session_id:'gone'}])]};
  const panel=Object.assign(new Panel(),{model:owner,feed,requests,active:true,dockOnly:true});
  assert.deepEqual(panel.taskQuestions.map(q=>q.todoId),['newer','older']);
  assert.ok(panel.taskQuestions.every(q=>q.agent===row));
  const card=panel.taskQuestions[0].card;
  await requests.answerForm(owner,row,card,'answer',{'Which color?':'Green'},'once',()=>true);
  assert.equal(state.writes.length,1);assert.deepEqual(panel.taskQuestions,[]);
});

test('an Agent question preview names the actual provider, including an assistant tool and launcher-backed multi-provider engines',()=>{
  const {owner}=pair('p');
  function shown(interaction,session={}) {
    const card=new AgentDecisionCard(owner,'assistant','test','s',{id:'ask',kind:'question',...interaction});
    return card.questionAgent(ResourceRow.from({id:'s',...session},'session'));
  }
  assert.equal(shown({}, {agent:'claude',launcher:'claude',agent_label:'claude'}),'Claude');
  assert.equal(shown({agent:'codex'},{agent:'claude'}),'Codex');
  assert.equal(shown({agent:'traex'}),'TRAE CLI');
  assert.equal(shown({agent:'paseo',agent_label:'Paseo'},{agent:'paseo',agent_label:'Paseo',launcher:'Grok'}),'Grok');
  assert.equal(shown({agent:'paseo',agent_label:'Kimi'},{agent:'paseo',launcher:'Other'}),'Kimi');
  assert.equal(shown({}, {agent:'CaseSensitiveRuntime'}),'CaseSensitiveRuntime');
  assert.equal(shown({tool_name:'ask_user',agent:'codex'}),'Persona');
  assert.equal(shown({},{}),'');
});

test('the three-line Agents preview uses the interface text size while a question in its full form keeps the reading size',()=>{
  const page=readFileSync(new URL('../entry/src/main/ets/components/AgentListPage.ets',import.meta.url),'utf8');
  const summary=page.slice(page.indexOf('if (card.preview) Text('),page.indexOf('if (card.quickOptions().length) this.decisionChoices'));
  assert.match(summary,/Text\(this\.decisionPreview\(card\)\)\.fontSize\(17\)\.maxFontScale\(Tokens\.interfaceFontMaxScale\)/);
  assert.match(summary,/\.maxLines\(3\)/);
  assert.match(page,/card\.questionAgent\(binding\.bound\.value\)/);
  assert.match(page,/onClick\(\(\) => this\.openRow\(binding\.bound\.value\)\)\.id\('agents.preview.'/);
  const detail=readFileSync(new URL('../entry/src/main/ets/components/InteractionDecisionForm.ets',import.meta.url),'utf8');
  assert.ok(detail.split('\n').some(line=>line.includes('Text(field.title).fontSize(Tokens.body)') && !line.includes('maxFontScale')));
});

const { DecisionPager, orderedDecisions } = await import('../entry/src/main/ets/models/DecisionPager.ets');
const { interactionSkipReason } = await import('../entry/src/main/ets/models/InteractionDecision.ets');

test('the assistant pages its own requests before unique supervised requests and follows an insert or resolution', () => {
  const item = (id,kind='question',status='open') => ({id,kind,status,raw:JSON.stringify({id,kind})});
  const own=item('own'), plan=item('plan','plan'), last=item('last');
  const items=orderedDecisions([own],[plan,own,last,plan,item('retired','question','resolved')]);
  assert.deepEqual(items,[own,plan,last]);
  const pager=new DecisionPager();pager.bind('first');
  const ids=items.map(x=>x.id);pager.reconcile(ids);assert.equal(pager.current,'own');
  pager.select(ids,1);pager.reconcile(['new',...ids]);assert.equal(pager.current,'plan');
  pager.reconcile(['new','own','last']);assert.equal(pager.current,'last');
  pager.select(['new','own','last'],3);assert.equal(pager.current,'last');
  pager.fold(['new','own','last']);assert.equal(pager.arrived(['own','fresh']),1);
  pager.reconcile(['fresh']);assert.equal(pager.collapsed(['fresh']),false);
  assert.deepEqual(pager.dots(['1','2','3','4','5','6','7','8','9']),[0,1,2,3,4,5,6]);
});

test('paging retains only the immutable question draft in the same conversation', () => {
  const pager=new DecisionPager();pager.bind('A');
  const q={id:'question',raw:'v1',kind:'question'};const first=pager.draft(q);
  first.save({answer:'draft'},{choices:['blue']},'A');pager.fold(['question']);pager.show();
  assert.deepEqual(pager.draft(q).text,{answer:'draft'});assert.deepEqual(pager.draft(q).picks,{choices:['blue']});
  assert.deepEqual(pager.draft({...q,raw:'v2'}).text,{});
  pager.bind('B');assert.notEqual(pager.draft(q),first);assert.equal(pager.draft({...q,kind:'plan'}),undefined);
});

test('skip explains unsendable plans, permissions and terminal requests without manufacturing a response', () => {
  const q=(kind,raw={})=>({kind,raw:JSON.stringify(raw)});
  assert.equal(interactionSkipReason(q('plan')),'decision_skip_plan');assert.equal(interactionSkipReason(q('permission')),'decision_skip_permission');
  assert.equal(interactionSkipReason(q('screen')),'decision_skip_terminal');assert.equal(interactionSkipReason(q('question',{can_cancel:true})),'');
});

const { decisionSourceItem } = await import('../entry/src/main/ets/models/InteractionHistory.ets');

test('view in chat lands on the words before the named tool, with a bounded latest-question fallback', () => {
  const items=[
    {id:'start',role:'user',kind:'text',text:'original'},
    {id:'a',role:'assistant',kind:'text',text:'first question'},
    {id:'call-1',kind:'tool_use',tool_name:'ask_user',tool_use_id:'one'},
    {id:'result',role:'user',kind:'text',text:'machine result',tool_use_id:'one'},
    {id:'next',role:'user',kind:'text',text:'next turn'},
    {id:'call-2',kind:'tool_use',tool_name:'request_user_input',tool_use_id:'two'}
  ];
  assert.equal(decisionSourceItem(items,'one'),'a');
  assert.equal(decisionSourceItem(items,'two'),'next');
  assert.equal(decisionSourceItem(items,''),'next');
  assert.equal(decisionSourceItem(items,'missing'),'');
  assert.equal(decisionSourceItem([...items,...Array.from({length:400},(_,i)=>({id:String(i),kind:'status'}))],'one'),'');
});

test('the optional project search readback retains sessionless cards and their generation; a cleared page cannot receive the old snapshot', async () => {
  const a=pair('one'), b=pair('two'), root=rootFor([a,b]), queue=new AgentDecisionQueue();
  a.state.rows.push({id:'without-session',machine_id:'machine',kind:'permission',status:'open'});
  const results=[];
  await queue.reload(root,undefined,value=>results.push(value));
  assert.equal(results.length,1);
  const local=results[0].find(part=>part.owner===a.owner);
  assert.deepEqual([local.scope,local.generation,local.records.length],['one',1,2]);
  assert.equal(queue.forSession(root,a.row).length,1);
  const gate=Promise.withResolvers(); a.repo.decisions=()=>gate.promise;
  const change=queue.reload(root,a.owner,value=>results.push(value)); queue.clear(); gate.resolve(a.state.rows);
  await change;
  assert.equal(results.length,1); assert.deepEqual(a.state.writes,[]); assert.deepEqual(b.state.writes,[]);
});


test('recommended quick answers show a localized tag and submit the original option once', async () => {
  const p=pair('chosen'), root=rootFor([p]), queue=new AgentDecisionQueue();
  const rawLabel='Swift Testing (Recommended)';
  p.state.rows[0].questions=[{question:'Test framework?',options:[{label:rawLabel},{label:'XCTest'}]}];
  await queue.reload(root);
  const card=queue.forSession(root,p.row)[0];
  assert.deepEqual(card.quickOptions(),[rawLabel,'XCTest']);
  for(const label of ['Swift Testing (Recommended)','Swift Testing（推荐）','Swift Testing(推荐)'])
    assert.equal(recommendedOptionTitle(label),'Swift Testing');
  assert.equal(recommendedOptionTitle('XCTest'),undefined);
  assert.equal(recommendedOptionTitle('Recommended options'),undefined);
  await queue.answer(root,p.row,card,rawLabel,'select-framework',()=>true);
  assert.deepEqual(p.state.writes,[{body:{answers:{'Test framework?':rawLabel}},op:'select-framework'}]);
});

test('permissions use the requesting agent and long question lists still use their full answer form', () => {
  const p=pair('chosen');
  const card=data=>new AgentDecisionCard(p.owner,'assistant','machine','s',data);
  assert.equal(card({id:'claude',kind:'permission',agent:'claude'}).namedPermissionAgent,false);
  assert.equal(card({id:'legacy',kind:'permission'}).namedPermissionAgent,false);
  assert.equal(card({id:'other',kind:'permission',agent:'codex'}).namedPermissionAgent,true);
  const labels=['one','two','three','four','five'];
  const question=card({id:'question',kind:'question',questions:[{question:'Pick one',options:labels.map(label=>({label}))}]});
  assert.deepEqual(question.quickOptions(),[]);
  assert.deepEqual(question.request.fields[0].choices,labels);
});
