// Real Web settings with an owned synthetic mock hub; no live credentials or shell session are required.
import { chromium } from 'playwright-core';
import assert from 'node:assert/strict';
import { mkdirSync,writeFileSync } from 'node:fs';
const [origin,out]=process.argv.slice(2);
assert(/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin??'')&&out,'Provide an owned loopback Vite origin and artifact directory');
mkdirSync(out,{recursive:true});
const watchdog=setTimeout(()=>{console.error('TIMEOUT font preferences (240s)');process.exit(124)},240000);
const browser=await chromium.launch(process.env.CHROMIUM_PATH ? {executablePath:process.env.CHROMIUM_PATH} : {channel:'chrome'});
const results=[];const errors=[];
try {
for(const width of [1280,390]) for(const theme of ['light','dark']) for(const size of ['standard','large']) {
 const tag=`${width}-${theme}-${size}`;
 const context=await browser.newContext({viewport:{width,height:1000},colorScheme:theme,reducedMotion:'reduce',serviceWorkers:'block'});
 await context.addInitScript(({theme,size})=>{
  localStorage.setItem('persona.token','mock');localStorage.setItem('persona.language','en');localStorage.setItem('persona.theme',theme);localStorage.setItem('persona.text-size',size);
  window.fontPaint=[];window.fontFeedback=[];
  addEventListener('change',event=>{if(!['Interface font','Code font'].includes(event.target.getAttribute?.('aria-label')))return;const start=performance.now();requestAnimationFrame(()=>window.fontFeedback.push({control:event.target.getAttribute('aria-label'),ms:performance.now()-start}));},true);
  new MutationObserver(()=>{const style=document.getElementById('persona-font');if(style&&!window.fontPaint.length)window.fontPaint.push({time:performance.now(),css:style.textContent,rootChildren:document.getElementById('root')?.childElementCount??0});}).observe(document,{subtree:true,childList:true});
 },{theme,size});
 await context.route('**/*',r=>new URL(r.request().url()).origin===origin?r.continue():r.abort());
 const page=await context.newPage();page.on('pageerror',e=>errors.push(String(e)));page.setDefaultTimeout(15000);
 const fonts=[];page.on('request',r=>{if(r.resourceType()==='font')fonts.push(r.url());});
 await page.goto(`${origin}/settings?tab=general`);
 const sans=page.getByRole('combobox',{name:'Interface font',exact:true});const mono=page.getByRole('combobox',{name:'Code font',exact:true});await sans.waitFor();
 assert.equal(await page.locator('#persona-font').count(),0);
 const group=page.locator('[data-settings-group="appearance"]');await group.scrollIntoViewIfNeeded();
 await page.screenshot({path:`${out}/${tag}-default.png`});
 await page.evaluate(()=>document.fonts.ready);const before=fonts.length;
 await sans.selectOption('georgia');await mono.selectOption('menlo');
 await page.waitForFunction(()=>getComputedStyle(document.body).fontFamily.startsWith('Georgia'));
 const sample=await page.locator('[data-settings-row="font"] .prose-j').evaluate(el=>({
  sans:getComputedStyle(el).fontFamily,mono:getComputedStyle(el.querySelector('code')).fontFamily
 }));assert(sample.mono.includes('Menlo'));assert(sample.sans.includes('Georgia'));
 const second=await context.newPage();await second.goto(`${origin}/settings?tab=general`);await second.getByRole('combobox',{name:'Interface font',exact:true}).waitFor();
 await sans.selectOption('custom');const custom=page.getByRole('textbox',{name:'Interface font: custom family',exact:true});await custom.fill('Arial');await page.getByRole('button',{name:'Apply',exact:true}).click();
 await second.waitForFunction(()=>getComputedStyle(document.body).fontFamily.startsWith('Arial'));
 assert.equal(await second.getByRole('textbox',{name:'Interface font: custom family',exact:true}).inputValue(),'Arial');
 await page.evaluate(()=>document.fonts.ready);
 await page.locator('[data-settings-row="font"]').scrollIntoViewIfNeeded();
 await page.screenshot({path:`${out}/${tag}-custom.png`});
 const cdp=await context.newCDPSession(page);await cdp.send('DOM.enable');await cdp.send('CSS.enable');
 const {root:domRoot}=await cdp.send('DOM.getDocument');const {nodeId}=await cdp.send('DOM.querySelector',{nodeId:domRoot.nodeId,selector:'[data-settings-row="font"] .prose-j p'});
 const platform=await cdp.send('CSS.getPlatformFontsForNode',{nodeId});
 const sync=await page.evaluate(()=>{document.documentElement.style.setProperty('--font-sans','Verdana, sans-serif');return getComputedStyle(document.body).fontFamily;});
 await page.waitForFunction(()=>getComputedStyle(document.body).fontFamily.startsWith('Arial'));
 assert.equal(fonts.length,before,'changing local fonts adds no font request');
 if(width===1280&&theme==='light'&&size==='standard') {
  const terminals=await page.evaluate(async()=>{
   await import('/src/views/session/terminal/xterm.ts');const {TerminalFeed}=await import('/src/views/session/terminal/feed.ts');const {FullFeed}=await import('/src/views/session/terminal/full.ts');
   const host=()=>{const d=document.createElement('div');d.className='font-mono';d.style.cssText='width:640px;height:320px;position:absolute;top:0';document.body.append(d);return d;};
   const first=host(),feed=new TerminalFeed('mac','font-test');await feed.mount(first,first);
   const one=feed.snapshot().fontFamily;const oneSize=feed.snapshot().fontSize;feed.dispose();first.remove();
   const fullHost=host(),full=new FullFeed('mac','font-test');await full.mount(fullHost);if(!full.term)throw Error(JSON.stringify(full.snapshot()));const two=full.term.options.fontFamily;const twoSize=full.term.options.fontSize;
   const {setFontChoice}=await import('/src/ui/font-preference.ts');setFontChoice('mono',{preset:'consolas'});
   const next=host();full.claim({},next,next,'font-test');await document.fonts.ready;await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
   const retained=full.term.options.fontFamily;const retainedSize=full.term.options.fontSize;full.dispose();fullHost.remove();next.remove();
   const againHost=host(),again=new TerminalFeed('mac','font-test');await again.mount(againHost,againHost);const reopened=again.snapshot().fontFamily;again.dispose();againHost.remove();
   setFontChoice('mono',{preset:'menlo'});return {one,two,retained,reopened,oneSize,twoSize,retainedSize};
  });assert(/^(?:"PersonaTermCJK", )?Menlo,/.test(terminals.one.replaceAll('"Menlo"','Menlo')));assert(/^(?:"PersonaTermCJK", )?Menlo,/.test(terminals.two.replaceAll('"Menlo"','Menlo')));assert(/^(?:"PersonaTermCJK", )?Consolas,/.test(terminals.retained.replaceAll('"Consolas"','Consolas')));assert(/^(?:"PersonaTermCJK", )?Consolas,/.test(terminals.reopened.replaceAll('"Consolas"','Consolas')));assert.equal(terminals.twoSize,terminals.retainedSize);results.push({terminals});
 }
 await page.reload();await sans.waitFor();
 const paint=await page.evaluate(()=>({first:window.fontPaint[0],paint:performance.getEntriesByType('paint').map(x=>({name:x.name,time:x.startTime})),family:getComputedStyle(document.body).fontFamily}));
 assert(paint.first&&paint.first.rootChildren===0);assert(paint.family.startsWith('Arial'));if(paint.paint.length)assert(paint.first.time<=paint.paint[0].time);
 await sans.selectOption('default');await second.waitForFunction(()=>!document.getElementById('persona-font')?.textContent.includes('--font-sans:'));
 await mono.selectOption('default');await second.waitForFunction(()=>!document.getElementById('persona-font'));
 assert.equal(await page.evaluate(()=>JSON.parse(localStorage.getItem('persona.font')).browser),true);
 results.push({tag,sample,paint,platform,feedback:await page.evaluate(()=>window.fontFeedback),fontRequestsAddedBeforeReload:0});await context.close();console.log('PASS',tag);
}
assert.deepEqual(errors,[]);writeFileSync(`${out}/results.json`,JSON.stringify(results,null,2));
} finally {await browser.close();clearTimeout(watchdog);}
