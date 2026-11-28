// Real Web app acceptance with an invented Hub transport shared by two browser
// profiles. No live botmux, model, node or external service is contacted.
// node web/e2e/botmux-display.mjs <loopback origin> <artifact directory>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { chromium } from "playwright-core";
const [address, artifacts] = process.argv.slice(2);
const origin = new URL(address);
assert.ok(["127.0.0.1", "localhost"].includes(origin.hostname));
const output = resolve(artifacts); await mkdir(output, { recursive: true });
const executablePath = process.env.PERSONA_TEST_CHROME_PATH;
const browser = await chromium.launch(executablePath ? { executablePath } : { channel: "chrome" });
const result = { status: "running", profiles: [] };
const deadline = setTimeout(() => void browser.close(), 180000);
const machine = { id: "example", name: "Example computer", online: true, os: "linux" };
const rows = [
 { machine_id: "example", session_id: "example-build", title: "Build the example project", state: "working", status: "working", pane_id: "herdr:p1", cwd: "/example", updated_at: "2026-01-01T12:00:00Z" },
 { machine_id: "example", session_id: "example-lark", title: "Review the example notes", state: "needs_you", status: "needs_you", pane_id: "botmux:example", cwd: "/example", updated_at: "2026-01-01T11:00:00Z" },
];
const counts = (selected) => {
 const count = (key) => selected.reduce((o,r) => ({...o, [key(r)]: (o[key(r)] ?? 0)+1}), {});
 return { total: selected.length, state:count(r=>r.state), machine:count(r=>r.machine_id), group:{},project:count(r=>r.cwd), todo:{"":selected.length},pane_source:count(r=>r.pane_id.startsWith("botmux:")?"feishu":"terminal"),background_waiting:0,revision:"example1" };
};
try {
 for (const paged of [false,true]) for (const width of [1280,390]) for (const theme of ["light","dark"]) for (const size of ["standard","large"]) {
  const name = `${paged?"paged":"complete"}-${width}-${theme}-${size}`;
  let saved, fail = false, release; let hold; const writes = [], reads = [], errors = [];
  const context = await browser.newContext({viewport:{width,height:900},colorScheme:theme,locale:"en-US",reducedMotion:"reduce",hasTouch:width<500,isMobile:width<500});
  const sockets=[];let primarySocket;
  async function configure(ctx){
  await ctx.addInitScript(({theme,size})=>{localStorage.setItem("persona.token","example");localStorage.setItem("persona.language","en");localStorage.setItem("persona.theme",theme);localStorage.setItem("persona.text-size",size);localStorage.setItem("persona.agentGrouping","state");}, {theme,size});
  await ctx.route(/^https?:\/\/(?!127\.0\.0\.1|localhost)/,route=>route.abort());
  await ctx.route("**/api/v1/**",async route=>{
   const req=route.request(),u=new URL(req.url()),p=u.pathname.replace("/api/v1",""); const q=u.searchParams;
   let data={dirs:[],blocked:[],home:"/example",settings:[],themes:[],colors:[],devices:[],hubs:[],channels:[],extensions:[],sources:[],accounts:[],connectors:[],presets:[],launchers:[]};let status=200;
   if(p.startsWith("/model-config/purposes/")){data={purpose:{id:"translate",name:"Translation",effective:{}},providers:[]};}
   else if(p==="/agents/display-settings") {
    if(req.method()==="PUT") {const body=req.postDataJSON();writes.push(body);if(hold)await hold;if(fail){status=503;data={error:"Example save failed"};}else {if(!body.only_if_unset||saved===undefined)saved=body.show_botmux;}}
    if(status===200)data={show_botmux:saved??true,configured:saved!==undefined};
   }else if(p==="/sessions"){
    reads.push(u.search);let selected=rows.filter(r=>(!q.has("exclude_pane_source")||!r.pane_id.startsWith("botmux:"))&&(!q.has("pane_source")||r.pane_id.startsWith("botmux:"))&&(!q.has("state")||r.state===q.get("state")));
    const c=counts(selected);data=q.has("counts")?c:{sessions:q.has("since")?[]:selected,remove:[],has_more:false,revision:"example1",total:selected.length,...(q.has("with_counts")?{counts:c}:{})};
   }else if(p==="/machines")data={machines:[machine]};
   else if(p==="/mutes")data={spaces:[],sessions:[]};
   else if(p==="/pins")data={spaces:[],sessions:[],projects:[]};
   else if(p==="/todos")data={todos:[]};
   else if(p==="/assistant")data={machine_id:"example",conversations:[],tasks:[],memory:[]};
   else if(p==="/interactions")data={interactions:[]};
   else if(p==="/managers")data={default:"assistant",managers:[{id:"assistant",name:"Persona",online:true,builtin:true}]};
   else if(p==="/workspace-board/settings")data={enabled:false,can_write:true};
   await route.fulfill({status,contentType:"application/json",body:JSON.stringify(data)});
  });
  await ctx.routeWebSocket("**/api/v1/ws**",ws=>{sockets.push(ws);if(ctx===context)primarySocket=ws;ws.send(JSON.stringify({type:"hello",hub_name:"Example hub",server_version:"example",features:paged?["sessions_list","sessions_lookup","sessions_agents_only","sessions_quick_filters"]:[]}));ws.onMessage(m=>{if(String(m).includes("app_ping"))ws.send(JSON.stringify({type:"pong"}));});});
  }
  await configure(context);
  const page=await context.newPage();page.on("pageerror",e=>errors.push(String(e)));page.on("console",m=>{if(m.type()==="error")console.error(m.text());});
  await page.goto(new URL("/settings?tab=agents#botmux-display",origin).href);
  const toggle=page.getByRole("switch",{name:"Show botmux sessions in Agents",exact:true});await toggle.waitFor();
  assert.equal(await toggle.getAttribute("aria-checked"),"true");
  await toggle.scrollIntoViewIfNeeded();await page.screenshot({path:`${output}/${name}-settings-on.png`});
  // Observe feedback independently from the mocked completion.
  hold=new Promise(resolve=>{release=resolve;});
  await toggle.evaluate(el=>{el.addEventListener("click",()=>{window.__botmuxStart=performance.now();new MutationObserver((_,obs)=>{if(document.body.textContent.includes("Saving…")){window.__botmuxFeedback=performance.now()-window.__botmuxStart;obs.disconnect();}}).observe(document.body,{childList:true,subtree:true});},{once:true});});
  await toggle.click();
  await page.getByRole("status").filter({hasText:"Saving…"}).waitFor();
  const feedbackMs=await page.evaluate(()=>window.__botmuxFeedback);
  assert.ok(feedbackMs<1000);assert.ok(await toggle.isDisabled());
  release();hold=undefined;await page.waitForFunction(()=>document.querySelector('[data-settings-row="botmux-display"] [role="switch"]')?.getAttribute("aria-checked")==="false");
  const completedMs=await page.evaluate(()=>performance.now()-window.__botmuxStart);
  assert.equal(saved,false);assert.equal(writes.length,1);
  await page.screenshot({path:`${output}/${name}-settings-off.png`});
  // Failed save preserves the acknowledged off value and is recoverable.
  fail=true;await toggle.click();await page.getByText(/Example save failed/).waitFor();assert.equal(await toggle.getAttribute("aria-checked"),"false");fail=false;
  await page.getByRole("button",{name:"Retry",exact:true}).click();await page.getByText(/Example save failed/).waitFor({state:"hidden"});
  await page.waitForFunction(()=>document.querySelector('[data-settings-row="botmux-display"] [role="switch"]')?.getAttribute("aria-checked")==="true");
  assert.equal(saved,true);
  assert.deepEqual(writes.at(-1),{show_botmux:true});
  await toggle.click();
  await page.waitForFunction(()=>document.querySelector('[data-settings-row="botmux-display"] [role="switch"]')?.getAttribute("aria-checked")==="false");
  // A second page rereads the shared value; its next list refresh picks up a change.
  const otherContext=await browser.newContext({viewport:{width:1280,height:900},locale:"en-US"});await configure(otherContext);const other=await otherContext.newPage();other.on("pageerror",e=>errors.push(String(e)));other.on("console",m=>{if(m.type()==="error")console.error(m.text());});await other.goto(new URL("/settings?tab=agents#botmux-display",origin).href);
  const second=other.getByRole("switch",{name:"Show botmux sessions in Agents",exact:true});await second.waitFor();assert.equal(await second.getAttribute("aria-checked"),"false");
  await otherContext.close();
  if(width<500){await page.goto(new URL("/settings",origin).href);await page.getByRole("button",{name:"Sidebar",exact:true}).click();}
  const list=page.locator("[data-sidebar-agents]");await list.waitFor();
  await page.waitForFunction(()=>!document.querySelector('[data-sidebar-agents]')?.textContent?.includes("Review the example notes"));
  assert.equal(await list.getByRole("radio",{name:/Lark group/}).count(),0);
  const chip=list.locator("button[data-botmux-mode]");
  await list.locator('button[data-botmux-mode="hide"]').waitFor();
  await list.scrollIntoViewIfNeeded();
  await page.screenshot({path:`${output}/${name}-sidebar-off.png`});
  // Another browser changed the value: the next actual list refresh follows it.
  saved=true;
  if(paged){primarySocket.send(JSON.stringify({type:"sessions_list"}));}
  else{primarySocket.send(JSON.stringify({type:"sessions",machine_id:"example",sessions:rows}));}
  await page.getByRole("radio",{name:/Lark group/}).waitFor();
  assert.deepEqual(writes,[{show_botmux:false},{show_botmux:true},{show_botmux:true},{show_botmux:false}]);
  await chip.click();
  await list.locator('button[data-botmux-mode="all"]').waitFor();
  await chip.click();
  await list.locator('button[data-botmux-mode="hide"]').waitFor();
  assert.equal(saved,false);
  await chip.click();
  await list.locator('button[data-botmux-mode="decode"]').waitFor();
  await page.getByRole("radio",{name:/Lark group/}).waitFor();
  assert.deepEqual(writes.slice(4),[{show_botmux:false},{show_botmux:true}]);
  await page.screenshot({path:`${output}/${name}-sidebar-decoded.png`});

  if(paged)assert.ok(reads.some(q=>q.includes("exclude_pane_source=feishu")));
  assert.deepEqual(errors,[]);
  result.profiles.push({name,feedbackMs,completedMs,writes:writes.length,excluded:paged?reads.filter(q=>q.includes("exclude_pane_source=feishu")).length:"local",errors});console.log(JSON.stringify(result.profiles.at(-1)));
  await context.close();
 }
 result.status="pass";
}catch(error){result.status="fail";result.error=String(error);console.error(error);for(const ctx of browser.contexts())for(const p of ctx.pages()){await p.screenshot({path:`${output}/failure.png`}).catch(()=>{});await writeFile(`${output}/failure.txt`,await p.locator("body").innerText());}}
finally{clearTimeout(deadline);await writeFile(`${output}/results.json`,JSON.stringify(result,null,2));await browser.close();}
assert.equal(result.status,"pass");
