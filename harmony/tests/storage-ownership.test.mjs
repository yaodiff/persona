import test from 'node:test';
import assert from 'node:assert/strict';
import { isHubStateKey } from '../entry/src/main/ets/core/StorageOwnership.ts';

test('forgetting a Hub matches every manager cache and operation without prefix collisions',()=>{
 for(const hub of ['hub-a','hub:["quoted"]\\name'])for(const manager of ['personal','work']){
  const scope=JSON.stringify([hub,manager]),target=JSON.stringify([scope,'session','machine','session']);
  for(const key of ['snapshot:'+scope,'outbox:'+scope,'outbox:'+scope+':op-id','mutations:'+scope+':id','view:'+scope+':navigation','xiaoyi:'+scope+':a2a-contexts','xiaoyi:'+scope+':push-outbox','draft:'+target,'transcript:'+target,'attachments:'+JSON.stringify([hub,manager,'machine','session'])]){
   assert.equal(isHubStateKey(key,hub),true,key);assert.equal(isHubStateKey(key,hub+'-other'),false,key);
  }
 }
});
test('global preferences, profile registry and other Hubs are never included in tenant cache removal',()=>{
 for(const key of ['profiles','preferences','snapshot:'+JSON.stringify(['hub-ab','personal']),'view:'+JSON.stringify(['other','hub-a'])+':navigation','arbitrary:hub-a'])assert.equal(isHubStateKey(key,'hub-a'),false,key);
 assert.equal(isHubStateKey('snapshot:["hub-a","personal"]',''),false);
});
