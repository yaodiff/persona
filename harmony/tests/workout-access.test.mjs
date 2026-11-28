import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {registerHooks,stripTypeScriptTypes} from 'node:module';

registerHooks({
  resolve(spec,ctx,next){
    if(spec==='@kit.HealthServiceKit')return {url:'test-health:scope',shortCircuit:true};
    return next(spec,ctx);
  },
  load(url,ctx,next){
    if(url==='test-health:scope')return {
      format:'module',
      shortCircuit:true,
      source:`export const healthStore={
        get exerciseSequenceHelper(){return globalThis.__healthHelper;},
        init:(context)=>globalThis.__initHealthStore(context),
        getAuthorizations:(request)=>globalThis.__readWorkoutScope(request)
      };`
    };
    if(url.endsWith('/PhoneWorkoutAccess.ets'))return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8'))};
    return next(url,ctx);
  }
});
const {workoutAccess,WorkoutAccessResult}=await import('../entry/src/main/ets/platform/PhoneWorkoutAccess.ets');

test('PhoneWorkoutAccess uses a static @kit.HealthServiceKit import so ets-loader rewrites it to @hms.health.store',()=>{
  const source=readFileSync(new URL('../entry/src/main/ets/platform/PhoneWorkoutAccess.ets',import.meta.url),'utf8');
  assert.match(source,/^\s*import\s+\{\s*healthStore\s*\}\s+from\s+['"]@kit\.HealthServiceKit['"]/m);
  assert.doesNotMatch(source,/import\s*\(\s*['"]@kit\.HealthServiceKit['"]\s*\)/);
});

test('workout access checks only the read authorization, distinguishing device, scope and service refusal',async()=>{
  let offered=false,calls=0,response={readDataTypes:[],writeDataTypes:[]};
  globalThis.__healthHelper={DATA_TYPE:{id:101}};
  globalThis.__initHealthStore=async()=>{};
  globalThis.canIUse=cap=>cap==='SystemCapability.Health.HealthStore'&&offered;
  globalThis.__readWorkoutScope=request=>{calls++;assert.deepEqual(request,{readDataTypes:[{id:101}],writeDataTypes:[]});if(response instanceof Error)throw response;return response;};
  assert.equal((await workoutAccess()).state,'unavailable');assert.equal(calls,0);
  offered=true;assert.equal((await workoutAccess()).state,'not_granted');
  response={readDataTypes:[{id:99},{id:101}],writeDataTypes:[]};assert.equal((await workoutAccess()).state,'granted');
  response=Object.assign(Error('private platform detail'),{code:201});const failed=await workoutAccess();
  assert.deepEqual({...failed},{state:'unknown',code:201});assert.equal(calls,3);
});

test('workout access initializes healthStore with host context before reading and retries init after failure',async()=>{
  const order=[];
  let failInit=true;
  globalThis.__healthHelper={DATA_TYPE:{id:101}};
  globalThis.canIUse=cap=>cap==='SystemCapability.Health.HealthStore';
  globalThis.__initHealthStore=async context=>{
    order.push(['init',context.tag]);
    if(failInit)throw Object.assign(Error('init failed'),{code:401});
  };
  globalThis.__readWorkoutScope=async ()=>{
    order.push(['read']);
    return {readDataTypes:[{id:101}],writeDataTypes:[]};
  };
  const ctxA={tag:'ability-a'};
  const first=await workoutAccess(ctxA);
  assert.deepEqual({...first},{state:'unknown',code:401});
  assert.deepEqual(order,[['init','ability-a']]);

  failInit=false;
  const second=await workoutAccess(ctxA);
  assert.deepEqual({...second},{state:'granted',code:0});
  const third=await workoutAccess(ctxA);
  assert.deepEqual({...third},{state:'granted',code:0});
  assert.deepEqual(order,[['init','ability-a'],['init','ability-a'],['read'],['read']]);

  const ctxB={tag:'ability-b'};
  const fourth=await workoutAccess(ctxB);
  assert.deepEqual({...fourth},{state:'granted',code:0});
  assert.deepEqual(order,[['init','ability-a'],['init','ability-a'],['read'],['read'],['init','ability-b'],['read']]);
});

test('workout access never throws on missing data types, malformed responses, or null/primitive rejections',async()=>{
  globalThis.canIUse=cap=>cap==='SystemCapability.Health.HealthStore';
  globalThis.__initHealthStore=async()=>{};

  globalThis.__healthHelper=undefined;
  assert.deepEqual({...await workoutAccess()},{state:'unknown',code:0});

  globalThis.__healthHelper={DATA_TYPE:{id:101}};
  globalThis.__readWorkoutScope=async()=>null;
  assert.deepEqual({...await workoutAccess()},{state:'unknown',code:0});

  globalThis.__readWorkoutScope=async()=>({readDataTypes:null});
  assert.deepEqual({...await workoutAccess()},{state:'unknown',code:0});

  globalThis.__readWorkoutScope=async()=>({readDataTypes:[null,{id:101}]});
  assert.deepEqual({...await workoutAccess()},{state:'granted',code:0});

  for(const thrown of [undefined,null,'native-string-failure',42,{code:NaN},{code:'201'}]){
    globalThis.__readWorkoutScope=async()=>{throw thrown;};
    assert.deepEqual({...await workoutAccess()},{state:'unknown',code:0});
  }
});

test('PhoneSettingsDevicePages health sheet load passes host context and contains UIContext/workoutAccess failures',async()=>{
  const source=readFileSync(new URL('../entry/src/main/ets/components/PhoneSettingsDevicePages.ets',import.meta.url),'utf8');
  const slice=(startMarker,endMarker)=>{
    const s=source.indexOf(startMarker);assert.ok(s>=0,startMarker);
    const e=source.indexOf(endMarker,s);assert.ok(e>s,endMarker);
    return source.slice(s,e);
  };
  const body=slice('  private hostContext():','  private bytes(');
  const calls=[];
  let failContext=false,failAccess=false;
  const Host=new Function('workoutAccess','WorkoutAccessResult',stripTypeScriptTypes(
    'class Host { active=true; page="phone_health_reports"; healthRevision=0; workoutState=""; workoutCode=0; storageError=false; ' +
    'screenScroll={load(){}}; getUIContext(){ if(this._failContext) throw new Error("detached"); return { getHostContext:()=>this._ctx }; } ' +
    body + ' }'
  ) + ';return Host;')(async ctx=>{ calls.push(ctx); if(failAccess) throw undefined; return new WorkoutAccessResult('granted',0); },WorkoutAccessResult);

  const page=new Host();
  page._ctx={tag:'host-1'};
  await page.load();
  assert.equal(page.workoutState,'granted');
  assert.equal(page.workoutCode,0);
  assert.deepEqual(calls,[page._ctx]);

  page._failContext=true;
  await page.load();
  assert.equal(page.workoutState,'granted');
  assert.equal(calls.at(-1),undefined);

  failAccess=true;
  await page.load();
  assert.equal(page.workoutState,'unknown');
  assert.equal(page.workoutCode,0);

  assert.match(source,/aboutToAppear\(\):void\s*\{\s*this\.active=true;this\.load\(\)\.catch\(\(\)\s*=>\s*\{\}\);/);
  assert.match(source,/@Monitor\('page'\)\s*changed\(\):void\s*\{\s*this\.load\(\)\.catch\(\(\)\s*=>\s*\{\}\);/);
  assert.match(source,/'settings\.health\.check',\(\)=>\{this\.load\(\)\.catch\(\(\)\s*=>\s*\{\}\);\}/);
});

test('HealthToday custom dialog open and dismiss guard detached UIContext and closeCustomDialog rejections',async()=>{
  const source=readFileSync(new URL('../entry/src/main/ets/components/HealthToday.ets',import.meta.url),'utf8');
  const dismissBody=source.slice(source.indexOf('  private dismiss():'),source.indexOf('  private metric('));
  const askBody=source.slice(source.indexOf('  private async ask('),source.indexOf('  private submit('));
  const DialogHost=new Function('Haptics','DialogAlignment',stripTypeScriptTypes(
    'class DialogHost { detail=true; detailClient={}; dialogEpoch=0; amount="10"; amountOwner="owner"; amountDialog=-1; mounted=true; ' +
    'kind="water"; model={demo:false}; intake={busy:false,ownerKey:"owner",currentClient:()=>({id:"c1"})}; amountEditor(){} ' +
    'getUIContext(){ if(this._failUi) throw new Error("detached"); return { getPromptAction:()=>this._prompt }; } ' +
    dismissBody + '\n' + askBody + ' }'
  ) + ';return DialogHost;')({tap(){}},{Center:0});

  const host=new DialogHost();
  host._failUi=true;
  await host.ask('water');
  assert.equal(host.amountDialog,-1,'synchronous getUIContext failure must reset amountDialog instead of leaving it wedged at -2');

  host._failUi=false;
  let closeCalls=0;
  host._prompt={
    openCustomDialog:async()=>{ host.mounted=false; return 7; },
    closeCustomDialog:()=>{ closeCalls++; throw new Error('already closed'); }
  };
  await host.ask('water');
  assert.equal(closeCalls,1);
  assert.equal(host.amountDialog,-1);

  host.amountDialog=9;
  host._prompt={ closeCustomDialog:()=>{ throw new Error('already closed'); } };
  host.dismiss();
  assert.equal(host.amountDialog,-1);

  host.amountDialog=11;
  host._failUi=true;
  host.dismiss();
  assert.equal(host.amountDialog,-1);
});
