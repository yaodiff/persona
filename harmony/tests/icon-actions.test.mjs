import test from 'node:test';
import fs from 'node:fs';
import vm from 'node:vm';
import assert from 'node:assert/strict';
import {fileURLToPath} from 'node:url';

const w=fileURLToPath(new URL('../entry/src/main/ets',import.meta.url));

test('action icons distinguish approvals, uncertain writes and the operation that is actually sending',()=>{
const inbox=fs.readFileSync(w+'/components/InboxWorkspacePage.ets','utf8');
function method(src,name){
 const hit=src.indexOf('private '+name+'(): string {');assert(hit>=0,name);const start=src.indexOf('{',hit);let level=0,quote='',escaped=false;
 for(let i=start;i<src.length;i++){const ch=src[i];if(quote){if(escaped)escaped=false;else if(ch==='\\')escaped=true;else if(ch===quote)quote='';continue;}if(ch==='"'||ch==="'"){quote=ch;continue;}if(ch==='{')level++;if(ch==='}'&&--level===0)return new Function(src.slice(start+1,i));}throw new Error(name);
}

const context=state=>({workspace:{writeState:'',attachmentBusy:false,handoffBusy:false,mailComposeState:'',mailComposeBusy:false,...state}});
const cases=[
 ['conversationSendKey',{},'send'],['conversationSendKey',{writeState:'sending'},'sending'],
 ['conversationSendKey',{writeState:'unknown'},'result_unknown'],['conversationSendKey',{writeState:'pending'},'pending'],
 ['conversationSendKey',{writeState:'sending',attachmentBusy:true},'send'],
 ['attachmentSendKey',{writeState:'sending',attachmentBusy:false},'inbox_send_attachment'],
 ['attachmentSendKey',{writeState:'sending',attachmentBusy:true},'sending'],
 ['mailSendKey',{mailComposeState:'sending',mailComposeBusy:false},'sending'],
 ['mailSendKey',{mailComposeState:'draft',mailComposeBusy:true,mailComposeReady:false},'loading'],
 ['mailSendKey',{mailComposeState:'draft',mailComposeBusy:true,mailComposeReady:true},'sending'],
 ['mailSendKey',{mailComposeState:'unknown'},'result_unknown'],['mailSendKey',{mailComposeState:'pending'},'pending'],
 ['handoffSendKey',{writeState:'sending',handoffBusy:false},'inbox_handoff_send'],
 ['handoffSendKey',{writeState:'sending',handoffBusy:true},'sending'],
 ['handoffSendKey',{writeState:'unknown'},'result_unknown']
];for(const [name,state,expected]of cases)assert.equal(method(inbox,name).call(context(state)),expected,name+JSON.stringify(state));
let icons=fs.readFileSync(w+'/design/IconActions.ets','utf8').replace(/^import .*\n/gm,'').replace('export class','class').replace(/: (?:string|boolean|ResourceColor|Resource)\b/g,'');
const IconActions=vm.runInNewContext(icons+'; IconActions',{$r:s=>s,Colors:{onAction:'onAction',danger:'danger',link:'link'}});
for(const [key,glyph] of [['assistant_feed_start','play_fill'],['send','arrow_up'],['assistant_detail_retry','arrow_clockwise'],['approve','checkmark_shield'],['deny','nosign'],['confirm','checkmark'],['inbox_send_attachment','doc_text_badge_arrow_up'],['result_unknown','exclamationmark_circle']])assert.equal(IconActions.symbol(key),'sys.symbol.'+glyph,key);
assert.equal(IconActions.busy('unknown'),false);assert.equal(IconActions.busy('result_unknown'),false);
const decision=fs.readFileSync(w+'/components/InteractionDecisionForm.ets','utf8');
for(const kind of ['permission','plan'])for(const selected of ['approve','deny',''])assert.equal(method(decision,'submitIconKey').call({request:{kind},selected}),selected||'confirm');
assert.equal(method(decision,'submitIconKey').call({request:{kind:'screen'},selected:'deny'}),'confirm');

});


test('every quick filter keeps its complete localized accessible label and applies in one tap on narrow or magnified screens',()=>{
 const source=fs.readFileSync(w+'/components/AgentListPage.ets','utf8');
 const filters=source.slice(source.indexOf('  @Builder filters()'),source.indexOf('  @Builder renameForm()'));
 const keys=source.match(/const QUICK_FILTERS: string\[\] = (\[[^\]]+\])/)[1];
 const labelSource=source.match(/const QUICK_LABELS: Record<string,string> = (\{[^;]+\});/)[1];
 const labels=vm.runInNewContext('('+labelSource+')');
 assert.deepEqual(Array.from(vm.runInNewContext(keys)),['all','needsYou','unread','working','backgroundWaiting','feishu','terminal']);
 assert.deepEqual({...labels},{all:'all',needsYou:'needs_you',unread:'agent_group_unread',working:'working',backgroundWaiting:'agent_group_background',feishu:'agent_source_lark_group',terminal:'terminal'});
 assert.match(source,/!known \|\| key === 'all' \|\| key === this\.workspace\.quickFilter \|\| Wire.number\(counts,key\)>0/);
 assert.match(filters,/ForEach\(this.shownQuickFilters,[\s\S]*Button\(\{ type: ButtonType.Capsule \}\)[\s\S]*Text\(tr\(QUICK_LABELS\[key\]\)/);
 assert.match(filters,/accessibilityText\(tr\(QUICK_LABELS\[key\]\)\)/);
 assert.match(filters,/accessibilityDescription\(key !== 'all' && this.workspace.filterCounts\[key\] !== undefined/);
 assert.match(filters,/accessibilitySelected\(this\.workspace\.quickFilter === key\)/);
 assert.match(source,/private quickHeight\(\): number \{ return PhoneStyle.formSize\(44,57.7,this.environment.fontScale\)/);
 assert.match(filters,/responseRegion\(\{x:0,y:0,width:'100%',height:this.quickHeight\(\)\}\)/);
 assert.match(filters,/onClick\([\s\S]*this\.workspace\.setQuickFilter\(key\)[\s\S]*\.id\('agents\.quick\.' \+ key\)/);
 assert.match(filters,/\.scrollable\(ScrollDirection\.Horizontal\)/);
 assert.ok(filters.indexOf('this.filterControl()')>filters.indexOf('.scrollable(ScrollDirection.Horizontal)'));
});

test('magnified project and Agent views keep symbols inside their slots and give titles and fields room to grow', () => {
 const read = name => fs.readFileSync(w + '/' + name + '.ets', 'utf8');
 const projects = read('components/ProjectsPage'), agents = read('components/AgentListPage');
 const picker = read('components/AgentStartPicker'), phone = read('design/PhoneStyle');
 const index = read('pages/Index');
 const launch = index.slice(index.indexOf('  @Builder agentOptionRow('), index.indexOf('  private async loadAgentLaunchers'));
 for (const source of [phone, projects, agents, picker, launch]) {
   assert.doesNotMatch(source, /SymbolGlyph\([^\n]*?\)\s*\.fontSize\(\d+\)/, 'a fixed symbol must use vp independent of system text scaling');
 }
 assert.match(projects, /this\.narrowOutline\(\)[\s\S]{0,120}Column\(\{space:3\}\)[\s\S]{0,110}Text\(line\.tree!\.title\)/);
 assert.match(projects, /GridRow\(\{columns:this\.narrowOutline\(\) \? 1 : 2/);
 assert.match(agents, /Text\(tr\(QUICK_LABELS\[key\]\)\)\.fontSize\(Tokens\.interfaceFontVp\(15,this\.environment\.fontScale\)\)/);
 assert.match(picker, /TextInput\(\{text:this\.address[^\n]+\.height\(PhoneStyle\.groupedRowHeight\(nativeEnvironment\.fontScale\)\)\.fontSize\(Tokens\.interfaceFontVp\(13,nativeEnvironment\.fontScale\)\)/);
 assert.match(launch, /width\(this\.agentSize\(144,181\.67\)\)\.height\(this\.machineCardHeight\(row\)\)/);
 assert.match(launch, /Wire\.text\(row\.data\(\),'hostname'\) \|\| tr\('online'\)/);
 assert.match(projects, /15\*this\.spaceCardScale\(\)\)\.maxFontScale\(Tokens\.interfaceScaleFor\(15\)\)\.fontWeight\(600\)/);
 assert.match(launch, /this\.machineChoice\(m\)[\s\S]{0,160}scrollable\(ScrollDirection\.Horizontal\)/);
 assert.match(projects, /px2vp\(width\) < 320/);
 assert.match(projects, /Text\(this\.displayAgentTitle\(agent\)[^\n]+maxFontScale\(Tokens\.interfaceScaleFor\(15\)\)/);
 assert.match(picker, /Text\(o.title\)\.fontSize\(17\)\.maxFontScale\(Tokens.interfaceScaleFor\(17\)\)/);
 assert.doesNotMatch(launch.match(/TextArea\(\{text:this.newTask[\s\S]*?id\('agent.task'\)/)[0],/maxFontScale/);
 assert.match(launch, /Text\(tr\('phone_new_chat_title'\)\)\.fontSize\(17\)\.maxFontScale\(Tokens\.chromeFontMaxScale\)\.maxLines\(2\)\.fontWeight\(600\)/);
});

test('magnified Inbox filters can grow and row icons keep their layout slots', () => {
  const source = fs.readFileSync(w + '/components/InboxWorkspacePage.ets', 'utf8');
  const filters = source.slice(source.indexOf('@Builder choiceBar()'), source.indexOf('private groupOpen('));
  const list = source.slice(source.indexOf('@Builder sectionHeading('), source.indexOf('@Builder header()'));

  assert.match(filters, /Text\(tr\(inboxTierLabel\(choice.key\)\)\)\.fontSize\(14\)\.maxFontScale\(Tokens.navigationTextMaxScale\)/);
  assert.match(filters, /height\(PhoneStyle\.sidebarMetric\(44,this.environment.fontScale\)\)/);
  assert.equal((filters.match(/responseRegion\(this\.chipTouchRegion\(\)\)/g) || []).length, 2);
  assert.match(filters, /const height = PhoneStyle\.sidebarMetric\(44, this.environment.fontScale\)/);
  assert.doesNotMatch(filters, /\.height\(34\)/);
  assert.match(filters, /\.scrollable\(ScrollDirection.Horizontal\)/);
  assert.doesNotMatch(list, /SymbolGlyph\([^\n]*?\)\.fontSize\(\d+\)/);
  assert.match(list, /Flex\(\{wrap:FlexWrap.Wrap,space:\{main:LengthMetrics.vp\(5\)/);
  assert.ok(list.indexOf('this.sourceBar()') < list.indexOf('this.inboxSearch(true)'));
  assert.match(filters, /this.workspace.sourceFilter=account \|\| clear \? 'all' : value/);
  assert.match(filters, /this.workspace.accountFilter=account && !clear \? value : 'all'/);
  assert.match(list, /minHeight:PhoneStyle\.sidebarMetric\(44,this.environment.fontScale\)/);
});

test('compact navigation keeps the back and close marks legible independently of the button frame', () => {
  const design = fs.readFileSync(w + '/design/PhoneStyle.ets', 'utf8');
  const glyphs = fs.readFileSync(w + '/design/PhoneOutlineIcon.ets', 'utf8');
  const shell = fs.readFileSync(w + '/pages/Index.ets', 'utf8');
  const pickers = fs.readFileSync(w + '/components/AgentStartPicker.ets', 'utf8');
  const projects = fs.readFileSync(w + '/components/ProjectsPage.ets', 'utf8');
  const drawings = [...glyphs.matchAll(/name:'(navigationBack|navigationClose)',width:([\d.]+),height:([\d.]+),pen:([\d.]+)/g)];
  assert.deepEqual(drawings.map(([, name, width, height]) => [name, Number(width), Number(height)]), [
    ['navigationBack', 11, 18.67], ['navigationClose', 17, 17],
  ]);
  assert.match(design, /iconScale:PhoneStyle\.formSize\(1,1\.25,this\.environment\.fontScale\)/);
  assert.match(design, /\.width\(this\.controlDiameter\)\.height\(this\.controlDiameter\)/);
  const phoneButtons = [...shell.matchAll(/PhoneIconButton\(\{([\s\S]*?)\}\)/g)].map(match => match[1]);
  for (const [identity, mark] of [['shell.back', 'navigationBack'], ['settings.back', 'navigationBack'], ['settings.close', 'navigationClose']]) {
    const callers = phoneButtons.filter(button => button.includes("identity:'" + identity + "'"));
    assert.equal(callers.length, 1, identity);
    assert.ok(callers[0].includes("symbol:'" + mark + "'"), identity);
  }
  for (const [source, mark, identity] of [[shell, 'navigationClose', 'agent.cancel'], [pickers, 'navigationBack', 'agent.picker.back'], [projects, 'navigationBack', 'shell.back']]) {
    const control = source.slice(source.lastIndexOf('Button({', source.indexOf(".id('" + identity + "')")), source.indexOf(".id('" + identity + "')"));
    assert.ok(control.includes("PhoneNavigationIcon({mark:'" + mark + "'})"), identity);
    assert.ok(control.includes('.width(PhoneStyle.bar).height(PhoneStyle.bar)'), identity);
  }
});
