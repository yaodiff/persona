import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

const screen = { foldable: false, region: undefined, failure: false, foldStatus: 0, foldDisplayMode: 0, density: 3.2, listeners: new Map() };
globalThis.__personaNativeEnvironment = screen;
registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@kit.AbilityKit') return { url: 'native-environment:ability', shortCircuit: true };
    if (specifier === '@kit.AccessibilityKit') return {url:'native-environment:accessibility',shortCircuit:true};
    if (specifier === '@kit.ArkUI') return { url: 'native-environment:display', shortCircuit: true };
    if (specifier.endsWith('/core/FoldPosture')) return { url: new URL('../core/FoldPosture.ts', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === 'native-environment:ability') return {format:'module',shortCircuit:true,source:'export const ConfigurationConstant={ColorMode:{COLOR_MODE_DARK:0,COLOR_MODE_LIGHT:1}};'};
    if (url === 'native-environment:accessibility') return {format:'module',shortCircuit:true,source:'export const accessibility={isAnimationReduceEnabledSync:()=>globalThis.__personaNativeEnvironment.readMotion()};'};
    if (url === 'native-environment:display') return { format: 'module', shortCircuit: true, source: `
      const s = globalThis.__personaNativeEnvironment;
      export const window = {WindowStatusType:{FULL_SCREEN:1},AvoidAreaType:{TYPE_NAVIGATION_INDICATOR:3,TYPE_SYSTEM:0,TYPE_KEYBOARD:5}};
      export const display = {
        isFoldable: () => s.foldable,
        getFoldStatus: () => s.foldStatus,
        getFoldDisplayMode: () => s.foldDisplayMode,
        getDefaultDisplaySync: () => ({ densityPixels: s.density }),
        on: (name, cb) => s.listeners.set(name, cb),
        off: (name, cb) => { if (s.listeners.get(name) === cb) s.listeners.delete(name); },
        getCurrentFoldCreaseRegion() {
          if (s.failure) throw new Error('display unavailable'); return s.region;
        }
      };
    ` };
    if (url.endsWith('/NativeEnvironment.ets')) return { format: 'module', shortCircuit: true,
      source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '')) };
    return next(url, context);
  }
});
const { NativeEnvironment } = await import('../entry/src/main/ets/platform/NativeEnvironment.ets');

test('ordinary native layout survives absent, invalid or unavailable fold geometry', () => {
  const environment = new NativeEnvironment();
  screen.foldable = true;
  for (const region of [undefined, { creaseRects: [] }, { creaseRects: [{ left: 0, top: 100, width: 0, height: 10 }] },
    { creaseRects: [{ left: 0, top: NaN, width: 100, height: 10 }] }]) {
    screen.region = region; environment.update({}); assert.equal(environment.nativeHoverAvailable, false);
  }
  screen.region = { creaseRects: [{ left: 0, top: 100, width: 100, height: 10 }] };
  environment.update({ fontSizeScale: 3.2 }); assert.equal(environment.nativeHoverAvailable, true);
  environment.update({}); assert.equal(environment.fontScale, 3.2);
  screen.failure = true; environment.update({}); assert.equal(environment.nativeHoverAvailable, false);
  screen.failure = false; screen.foldable = false; environment.update({}); assert.equal(environment.nativeHoverAvailable, false);
});


test('window geometry and native navigation avoidance update together and listeners retire',()=>{
 const environment=new NativeEnvironment(),handlers=new Map();let rect={top:40,height:1000},bottom=60;
 const nativeWindow={getWindowProperties:()=>({windowRect:rect}),getWindowAvoidArea:()=>({bottomRect:{height:bottom}}),on:(name,callback)=>handlers.set(name,callback),off:(name,callback)=>{assert.equal(handlers.get(name),callback);handlers.delete(name);}};
 environment.bindWindow(nativeWindow);assert.equal(environment.safeBottomPx,980);
 rect={top:40,height:1200};handlers.get('windowSizeChange')({width:600,height:1200});assert.equal(environment.safeBottomPx,1180);
 bottom=80;handlers.get('avoidAreaChange')({type:3,area:{}});assert.equal(environment.safeBottomPx,1160);
 rect={top:140,height:1200};handlers.get('windowRectChange')({rect});assert.equal(environment.safeBottomPx,1260);
 environment.unbindWindow();assert.equal(handlers.size,0);
});


test('native color mode follows resolved configuration without resetting for unrelated updates',()=>{
 const environment=new NativeEnvironment();environment.update({colorMode:0});assert.equal(environment.dark,true);environment.update({fontSizeScale:2});assert.equal(environment.dark,true);environment.update({colorMode:1});assert.equal(environment.dark,false);
});

test('popup geometry uses native window width and the window-relative top avoidance',()=>{const e=new NativeEnvironment(),handlers=new Map();let rect={top:140,width:1260,height:2720};const win={getWindowProperties:()=>({windowRect:rect}),getWindowAvoidArea:type=>type===0?{topRect:{height:117}}:{bottomRect:{height:84}},on:(key,fn)=>handlers.set(key,fn),off:()=>{}};e.bindWindow(win);assert.equal(e.windowWidthPx,1260);assert.equal(e.safeTopPx,117);rect={top:240,width:2584,height:1828};handlers.get('windowRectChange')({rect});assert.equal(e.windowWidthPx,2584);assert.equal(e.safeTopPx,117);e.unbindWindow();});


test('keyboard visibility follows its window and a retired listener cannot resize the replacement',()=>{
 const env=new NativeEnvironment();
 function fakeWindow(initial){const events=new Map();return {events,getWindowProperties:()=>({windowRect:{top:0,width:1260,height:2720}}),getWindowAvoidArea:type=>({bottomRect:{height:type===5?initial:0}}),on:(name,fn)=>events.set(name,fn),off:(name,fn)=>{assert.equal(events.get(name),fn);events.delete(name);}};}
 const first=fakeWindow(0);env.bindWindow(first);const stale=first.events.get('keyboardHeightChange');
 stale(900);assert.equal(env.keyboardHeightPx,900);stale(0);assert.equal(env.keyboardHeightPx,0);
 const second=fakeWindow(700);env.bindWindow(second);assert.equal(first.events.size,0);assert.equal(env.keyboardHeightPx,700);
 stale(1200);assert.equal(env.keyboardHeightPx,700);const retired=second.events.get('keyboardHeightChange');env.bindWindow(second);retired(1200);assert.equal(env.keyboardHeightPx,700);second.events.get('keyboardHeightChange')(-10);assert.equal(env.keyboardHeightPx,0);
 const removed=second.events.get('keyboardHeightChange');env.unbindWindow();assert.equal(env.keyboardHeightPx,0);assert.equal(env.keyboardChanged,undefined);removed(900);assert.equal(env.keyboardHeightPx,0);assert.equal(second.events.size,0);
});

test('each transition reads the current system Reduce Motion preference', () => {
  const environment = new NativeEnvironment();
  for (const enabled of [false, true, false]) {
    screen.readMotion = () => enabled;
    assert.equal(environment.reduceMotion, enabled);
  }
  screen.readMotion = () => { throw new Error('Accessibility service unavailable'); };
  assert.equal(environment.reduceMotion, false);
});


test('phone form sheets follow the attached portrait window and retain native sizing in other windows', () => {
  const env = new NativeEnvironment();
  const handlers = new Map();
  let width = 1206, height = 2622, inset = 117, status = 1;
  const owner = {
    getWindowProperties: () => ({ windowRect: { width, height, top: 0 } }),
    getWindowStatus: () => status,
    getWindowAvoidArea: type => type === 0 ? { topRect: { height: inset } } : { bottomRect: { height: 0 } },
    on: (key, fn) => handlers.set(key, fn), off: key => handlers.delete(key)
  };
  const source = readFileSync(new URL('../entry/src/main/ets/pages/Index.ets', import.meta.url), 'utf8');
  const first = source.indexOf('  private compactFormHeight()');
  const last = source.indexOf('  private compactNavigationInset()', first);
  const end = source.indexOf('\n  }', last);
  assert.ok(first >= 0 && last > first && end > last);
  const declaration = source.slice(first, end + 4);
  const Form = new Function('SheetSize', 'Tokens', 'return '+stripTypeScriptTypes('class Form {'+declaration+'}'))({ LARGE:'native' }, { compactStatusInset:62 });
  const form = Object.assign(new Form(), { environment:env, model:{profile:{id:'paired'}}, getUIContext:() => ({ px2vp:px => px/3 }) });
  env.bindWindow(owner); assert.equal(form.compactFormHeight(), 812); assert.equal(form.compactNavigationInset(), 23);
  inset = 270; handlers.get('avoidAreaChange')({}); assert.equal(form.compactFormHeight(), 776); assert.equal(form.compactNavigationInset(), 0);
  status = 2; handlers.get('windowRectChange')({}); assert.equal(form.compactFormHeight(), 'native');
  status = 1; [width,height] = [height,width]; handlers.get('windowSizeChange')({}); assert.equal(form.compactFormHeight(), 'native');
  [width,height] = [1206,2622]; handlers.get('windowSizeChange')({}); env.nativeHoverAvailable = true; assert.equal(form.compactFormHeight(), 'native');
  env.nativeHoverAvailable=false; inset=0; handlers.get('avoidAreaChange')({}); assert.equal(form.compactNavigationInset(), 0);
  env.unbindWindow(); assert.equal(env.windowHeightPx, 0); assert.equal(env.isFullScreen, false);
});

test('NativeEnvironment uses live densityPixels and fold listeners across Mate XT landscape, portrait, and floating windows', () => {
  const env = new NativeEnvironment();
  const handlers = new Map();
  let rect = { width: 3184, height: 2232, top: 0 };
  screen.foldable = true;
  screen.density = 3.0;
  screen.foldStatus = 11;
  screen.foldDisplayMode = 5;
  screen.region = { creaseRects: [{ left: 0, top: 100, width: 100, height: 10 }] };
  screen.failure = false;

  const nativeWindow = {
    getWindowProperties: () => ({ windowRect: rect }),
    getWindowStatus: () => 1,
    getWindowAvoidArea: () => ({ topRect: { height: 0 }, bottomRect: { height: 0 } }),
    on: (key, fn) => handlers.set(key, fn),
    off: (key) => handlers.delete(key)
  };
  env.bindWindow(nativeWindow);
  // 3184 / 3.0 = 1061.3vp -> trifold_unfolded
  assert.equal(env.foldPosture, 'trifold_unfolded');
  assert.equal(env.isTriFold, true);
  assert.equal(env.creaseTopPx, 100);
  assert.equal(env.creaseHeightPx, 10);
  assert.equal(screen.listeners.has('foldStatusChange'), true);
  assert.equal(screen.listeners.has('foldDisplayModeChange'), true);

  // Rotate to 3-screen portrait (2232 / 3.0 = 744vp) -> dual_unfolded, isTriFold=false
  rect = { width: 2232, height: 3184, top: 0 };
  handlers.get('windowSizeChange')(rect);
  assert.equal(env.foldPosture, 'dual_unfolded');
  assert.equal(env.isTriFold, false);

  // Resize to floating window (1260 / 3.0 = 420vp) while hardware still reports GLOBAL_FULL (5) -> cover, isTriFold=false
  rect = { width: 1260, height: 2400, top: 0 };
  handlers.get('windowSizeChange')(rect);
  assert.equal(env.foldPosture, 'cover');
  assert.equal(env.isTriFold, false);

  // Restore landscape wide and test foldStatusChange listener to Z-stand (21) -> trifold_angled
  rect = { width: 3184, height: 2232, top: 0 };
  handlers.get('windowSizeChange')(rect);
  screen.foldStatus = 21;
  screen.listeners.get('foldStatusChange')(21);
  assert.equal(env.foldPosture, 'trifold_angled');
  assert.equal(env.isTriFold, true);

  // MatePad 2880x1920 at density 2.0 -> 1440vp -> trifold_unfolded even when non-foldable
  screen.foldable = false;
  screen.foldStatus = 0;
  screen.foldDisplayMode = 0;
  screen.density = 2.0;
  rect = { width: 2880, height: 1920, top: 0 };
  env.update({});
  handlers.get('windowSizeChange')(rect);
  assert.equal(env.foldPosture, 'trifold_unfolded');
  assert.equal(env.creaseTopPx, 0);
  assert.equal(env.creaseHeightPx, 0);

  env.unbindWindow();
  assert.equal(screen.listeners.size, 0);
  screen.foldable = false;
  screen.density = 3.2;
  screen.foldStatus = 0;
  screen.foldDisplayMode = 0;
  screen.region = undefined;
});
