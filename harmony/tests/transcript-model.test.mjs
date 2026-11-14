import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync, existsSync } from 'node:fs';

registerHooks({
  resolve(specifier, context, next) {
    if (specifier.startsWith('@kit.')) return {url:'client-boundary:'+specifier,shortCircuit:true};
    if (context.parentURL?.includes('/entry/src/main/ets/') && specifier.startsWith('.')) {
      const base = new URL(specifier + (specifier.endsWith('.ts') || specifier.endsWith('.ets') ? '' : '.ets'), context.parentURL);
      const url = existsSync(base) ? base : new URL(base.href.replace(/\.ets$/, '.ts'));
      return {url: url.href, shortCircuit: true};
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('client-boundary:')) return {format:'module',shortCircuit:true,source:`
      export const common = {};
      export const bundleManager = {getBundleInfoForSelf(){return {name:'com.example.persona'};}};
      export const cert = {};
      export const cryptoFramework = {};
      export const deviceInfo = {};
      export const hilog = {debug(){},info(){},warn(){},error(){}};
      export const i18n = {getSystemLanguage(){return 'zh-CN';}};
      export const rcp = {};
      export const socket = {};
      export const relationalStore = {};
      export const util = {TextEncoder:class {static create(){return new this();}encodeInto(value){return new TextEncoder().encode(value);}},generateRandomUUID:()=> 'synthetic-id'};
      export const tr = key => key;
      export const url = {URL:{parseURL:value=>new URL(value)}};
      export const segment = encodeURIComponent;
      export const withSourceContext = text => text;
      export const messageDisplay = text => text;
      export const nativeNotifications = {async clear(){},features(){return ['elicitation'];}};
    `};
    if (url.endsWith('.ets')) return {format:'module', shortCircuit:true, source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace(/@ObservedV2\s*/g,'').replace(/@Trace\s+/g,'').replace(/TranscriptItem, TranscriptBatch, TranscriptAction, TranscriptSnapshot/, '').replace(/, TranscriptBatch, TranscriptSnapshot/, ''))};
    return next(url, context);
  },
});

const {TranscriptModel} = await import('../entry/src/main/ets/models/TranscriptModel.ets');

function fixture() {
  const calls = [];
  const sent = [];
  const gate = {};
  gate.promise = new Promise((resolve, reject) => { gate.resolve = resolve; gate.reject = reject; });
  const repository = {
    async call(path) {
      calls.push(path);
      if (path.includes('after_seq=')) {
        const after = Number(new URLSearchParams(path.split('?')[1]).get('after_seq'));
        return {items:[{id:`${after + 1}#0`,seq:after + 1,role:'assistant',kind:'text',text:'filled'}],history:'h',from_seq:after,to_seq:after + 1};
      }
      return gate.promise;
    },
    transport: {send(text) { sent.push(JSON.parse(text)); }},
  };
  const model = new TranscriptModel({machine:'machine-one',session:'session-one',id:'session-one'}, repository,
    {async get(){return JSON.stringify({items:[{id:'1#0',seq:1,role:'assistant',kind:'text',text:'cached'}],covered:1,history:'h',hasMore:false,loadedOnce:true});},async put(){return true;}}, 'test', () => {}, error => { throw error; });
  return {model,calls,sent,gate};
}

test('a gap heard during a pending latest page is fetched after it completes', async () => {
  const {model,calls,gate} = fixture();
  model.reducer.restore({items:[{id:'1#0',seq:1,role:'assistant',kind:'text',text:'cached'}],covered:1,history:'h',hasMore:false,loadedOnce:true});
  const initial = model.latest();
  model.receive({type:'items',machine_id:'machine-one',session_id:'session-one',items:[{id:'4#0',seq:4,role:'assistant',kind:'text',text:'later'}],history:'h',from_seq:3,to_seq:4});
  gate.resolve({items:[{id:'1#0',seq:1,role:'assistant',kind:'text',text:'first'}],history:'h',to_seq:1,has_more:false});
  await initial;
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(calls.some(path => path.includes('after_seq=')));
});

test('a failed latest page releases the fill gate for a later gap', async () => {
  const {model,calls,gate} = fixture();
  model.reducer.restore({items:[{id:'1#0',seq:1,role:'assistant',kind:'text',text:'cached'}],covered:1,history:'h',hasMore:false,loadedOnce:true});
  const initial = model.latest();
  gate.reject(new Error('synthetic offline'));
  await assert.rejects(initial);
  model.receive({type:'items',machine_id:'machine-one',session_id:'session-one',items:[{id:'4#0',seq:4,role:'assistant',kind:'text',text:'later'}],history:'h',from_seq:3,to_seq:4});
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(calls.some(path => path.includes('after_seq=')));
});
