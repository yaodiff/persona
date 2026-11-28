import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { randomUUID } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { isHubStateKey } from '../entry/src/main/ets/core/StorageOwnership.ts';
globalThis.__attachmentOwnsKey = isHubStateKey;

globalThis.__attachmentUUID = randomUUID;
globalThis.__attachmentRuntime = {
  db: new Map(), files: new Map(), directories: new Set(), descriptors: new Map(), nextDescriptor: 1,
  pickedFiles: [], photoUris: [], photoAssets: new Map(), imageMime: 'image/jpeg', imageWidth: 3200,
  imageHeight: 2400, packBytes: 1024, filePickerGate: undefined, putGate: undefined, putAckGate: undefined,
};

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/Compatibility.ets') && specifier === '../core/ChatForwardWire')
      return {url:new URL('../core/ChatForwardWire.ts',context.parentURL).href,shortCircuit:true};
    if(specifier==='../platform/HubTransport')return {url:'persona-test:transport',shortCircuit:true};
    if (specifier.endsWith('/core/StorageOwnership') || specifier.endsWith('/core/AttachmentTypes')) return {url:new URL(specifier+'.ts',context.parentURL).href,shortCircuit:true};
    if (specifier === '@kit.AbilityKit') return { url: 'persona-test:ability', shortCircuit: true };
    if (specifier === '@kit.ArkTS') return { url: 'persona-test:arkts', shortCircuit: true };
    if (specifier === '@kit.ArkData') return { url: 'persona-test:arkdata', shortCircuit: true };
    if (specifier === '@kit.CoreFileKit') return { url: 'persona-test:corefile', shortCircuit: true };
    if (specifier === '@kit.ImageKit') return { url: 'persona-test:image', shortCircuit: true };
    if (specifier === '@kit.PerformanceAnalysisKit') return { url: 'persona-test:log', shortCircuit: true };
    if (specifier === '@kit.MediaLibraryKit') return { url: 'persona-test:media', shortCircuit: true };
    if (specifier.endsWith('/platform/LocalStore')) return { url: 'persona-test:local', shortCircuit: true };
    if (context.parentURL?.endsWith('.ets') && specifier.startsWith('.'))
      return { url: new URL(specifier + '.ets', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if(url==='persona-test:transport')return {format:'module',shortCircuit:true,source:`
      export class EndpointConfig {}
      export class HttpFailure extends Error{constructor(status,code,resultUnknown=false,resultPending=false){super(code);Object.assign(this,{status,code,resultUnknown,resultPending});}}
      export class HubTransport{async request(...args){return globalThis.__attachmentRequest(...args);}async requestBytes(...args){return {data:new globalThis.TextEncoder().encode(await globalThis.__attachmentRequest(...args)).buffer};}}
    `};
    if (url === 'persona-test:ability') return { format: 'module', source: 'export const common={}; export const wantConstant={Flags:{FLAG_AUTH_READ_URI_PERMISSION:1}};', shortCircuit: true };
    if (url === 'persona-test:log') return { format: 'module', source: 'export const hilog={debug(){}};', shortCircuit: true };
    if (url === 'persona-test:arkts') return { format: 'module', source: `
      export const util={
        generateRandomUUID:()=>globalThis.__attachmentUUID(),
        Base64Helper:class{encodeToStringSync(bytes){return Buffer.from(bytes).toString('base64')}},
        TextEncoder:{create:()=>({encodeInto:(value)=>new globalThis.TextEncoder().encode(value)})},
        TextDecoder:{create:()=>({decodeToString:(value)=>new globalThis.TextDecoder('utf-8',{fatal:true}).decode(value)})}
      };`, shortCircuit: true };
    if (url === 'persona-test:arkdata') return { format: 'module', source: `
      export const dataSharePredicates={DataSharePredicates:class{constructor(){this.values={}} equalTo(key,value){this.values[key]=value;return this}}};`, shortCircuit: true };
    if (url === 'persona-test:corefile') return { format: 'module', source: `
      const runtime=()=>globalThis.__attachmentRuntime;
      const descriptors=()=>runtime().descriptors;
      export const fileIo={
        OpenMode:{READ_ONLY:0,WRITE_ONLY:1,CREATE:64,TRUNC:512},
        statSync(file){const path=typeof file==='string'?file:descriptors().get(file)?.path;if(runtime().directories.has(path))return{size:0,isDirectory:true};const bytes=runtime().files.get(path);if(!bytes)throw Error('missing');return{size:runtime().reportedSizes?.get(path)??bytes.byteLength}},
        mkdirSync(path,recursive){runtime().directories.add(path)},
        openSync(path,flags){const id=runtime().nextDescriptor++;if((flags&3)===0){const bytes=runtime().files.get(path);if(!bytes)throw Error('missing');descriptors().set(id,{path,source:bytes,position:0,write:false})}else{runtime().files.set(path,new Uint8Array(0));descriptors().set(id,{path,position:0,write:true})}return{fd:id,name:path,path:path}},
        readSync(id,target,options={}){const d=descriptors().get(id);const start=d.position+(options.offset??0);const length=Math.min(options.length??target.byteLength,d.source.byteLength-start);new Uint8Array(target,0,length).set(d.source.subarray(start,start+length));d.position+=length;return length},
        writeSync(id,data,options={}){const d=descriptors().get(id);const part=new Uint8Array(data,0,options.length??data.byteLength);const start=d.position+(options.offset??0);const old=runtime().files.get(d.path);const next=new Uint8Array(Math.max(old.byteLength,start+part.byteLength));next.set(old);next.set(part,start);runtime().files.set(d.path,next);d.position=start+part.byteLength;return part.byteLength},
        closeSync(file){const id=typeof file==='number'?file:file.fd;descriptors().delete(id)}, unlinkSync(path){runtime().files.delete(path)}
      };
      export const fileUri={
        getUriFromPath(path){return 'file://sandbox'+path},
        FileUri:class{constructor(uri){this.uri=uri}get name(){const path=this.uri.split('?')[0];return decodeURIComponent(path.substring(path.lastIndexOf('/')+1))}}
      };
      export const picker={
        DocumentSelectOptions:class{},
        DocumentViewPicker:class{constructor(context){}async select(options){const files=runtime().pickedFiles.splice(0);const gate=runtime().filePickerGate;runtime().filePickerGate=undefined;if(gate)await gate.promise;return files}}
      };`, shortCircuit: true };
    if (url === 'persona-test:image') return { format: 'module', source: `
      const runtime=()=>globalThis.__attachmentRuntime;
      export const image={
        createImageSource(uri){const path=uri.replace('file://sandbox','');return{
          async getImageInfo(){return{mimeType:runtime().imageMime,size:{width:runtime().imageWidth,height:runtime().imageHeight}}},
          async createPixelMap(options){return{options,async release(){}}},async release(){}
        }},
        createImagePacker(){return{async packToData(pixels,options){
          runtime().packed.push({...options,...pixels.options.desiredSize});
          return new Uint8Array(runtime().packBytes).buffer;
        },async release(){}}}
      };`, shortCircuit: true };
    if (url === 'persona-test:media') return { format: 'module', source: `
      const runtime=()=>globalThis.__attachmentRuntime;
      export const photoAccessHelper={
        PhotoType:{IMAGE:1,VIDEO:2},PhotoViewMIMETypes:{IMAGE_TYPE:'image/*'},
        PhotoSelectOptions:class{},
        PhotoViewPicker:class{async select(options){runtime().photoPickLimit=options.maxSelectNumber;return{photoUris:runtime().photoUris.splice(0)}}},
        getPhotoAccessHelper(context){return{async release(){},async getAssets(options){const asset=runtime().photoAssets.get(options.predicates.values.uri);return{getCount:()=>asset?1:0,getFirstObject:async()=>asset,close(){}}}}}
      };`, shortCircuit: true };
    if (url === 'persona-test:local') return { format: 'module', source: `
      export class LocalStore{
        constructor(name){}
        revision(){return globalThis.__attachmentRuntime.epoch||0;} accepts(key,epoch){return epoch===(globalThis.__attachmentRuntime.epoch||0);}
        async open(context){if(globalThis.__attachmentRuntime.openFailure)throw Error('open failed');}
        async get(key){const runtime=globalThis.__attachmentRuntime;const gate=runtime.getGate;runtime.getGate=undefined;if(gate)await gate.promise;return runtime.db.get(key)||''}
        async put(key,value){const runtime=globalThis.__attachmentRuntime;const gate=runtime.putGate;runtime.putGate=undefined;if(gate)await gate.promise;runtime.db.set(key,value);const ackGate=runtime.putAckGate;runtime.putAckGate=undefined;if(ackGate){ackGate.entered();await ackGate.promise}}
        async remove(key){globalThis.__attachmentRuntime.db.delete(key)}
        async keys(){return [...globalThis.__attachmentRuntime.db.keys()]}
        async purgeHub(hub){const db=globalThis.__attachmentRuntime.db;for(const key of db.keys())if(globalThis.__attachmentOwnsKey(key,hub))db.delete(key)}
        allowHub(){} async close(){}
      }`, shortCircuit: true };
    if (url.endsWith('.ets')) {
      const source = readFileSync(new URL(url), 'utf8').replace(/@ObservedV2\s*/g, '').replace(/@Trace\s+/g, '');
      return { format: 'module', source: stripTypeScriptTypes(source), shortCircuit: true };
    }
    return next(url, context);
  },
});

const { ChatAttachments } = await import('../entry/src/main/ets/models/ChatAttachments.ets');
const { ResourceRow, ImageLimits } = await import('../entry/src/main/ets/models/Wire.ets');

function resetRuntime() {
  const runtime = globalThis.__attachmentRuntime;
  runtime.db.clear(); runtime.files.clear(); runtime.directories.clear(); runtime.descriptors.clear();
  runtime.nextDescriptor = 1; runtime.pickedFiles = []; runtime.photoUris = []; runtime.photoAssets.clear();
  runtime.imageMime = 'image/jpeg'; runtime.imageWidth = 3200; runtime.imageHeight = 2400; runtime.packBytes = 1024;
  runtime.packed = [];
  runtime.filePickerGate = undefined; runtime.putGate = undefined; runtime.putAckGate = undefined; runtime.epoch = 0;
}

function deferred() {
  let resolve;
  const promise = new Promise((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

function context() { return { filesDir: '/app/files' }; }

function row(id = 'session-a', machine = 'machine-a') {
  const result = new ResourceRow(id, 'Session', 'session');
  result.manager = 'manager-a'; result.machine = machine; result.session = id;
  return result;
}

function model(targetRow, repository) {
  const value = {
    features: [],
    profile: { id: 'hub-a', hubId: 'hub-a', manager: 'manager-a', baseUrl: 'https://primary.invalid/', pin: 'same-pin' }, selected: targetRow, epoch: 1, pairingEpoch: 0,
    pairingRevision() { return this.pairingEpoch; },
    scope() { return JSON.stringify([this.profile.id, this.profile.manager]); },
    target(target = this.selected) { return JSON.stringify([this.scope(), target.kind, target.machine, target.id]); },
    connectionGeneration() { return this.epoch; },
    owns(generation, scope) { return generation === this.epoch && scope === this.scope(); },
    canSend() { return true; },
    imageLimits() { return new ImageLimits(); },
    repo() { return repository; },
  };
  return value;
}

function repository(upload = async () => ({ path: '/tmp/remote.bin', name: 'remote.bin', size: 4 })) {
  const value = { uploads: [], async upload(path, bytes) { this.uploads.push({ path, bytes }); return upload(path, bytes); } };
  return value;
}

test('a route retired during the upload journal sends no file bytes and keeps the local file retryable',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,{HubRepository}=await import('../entry/src/main/ets/models/HubRepository.ets');
 const {LocalStore}=await import('persona-test:local'),repo=new HubRepository({hubId:'hub-a',managerId:'manager-a'},new LocalStore('journal'));
 const session=row(),client=model(session,repo),attachments=new ChatAttachments(),gate=deferred(),entered=deferred();
 let allowed=true,posts=0;const previous=globalThis.__attachmentRequest;
 globalThis.__attachmentRequest=async()=>{posts++;return JSON.stringify({path:'/tmp/notes.txt',name:'notes.txt'});};
 runtime.files.set('document://picker/notes.txt',new Uint8Array([1,2]));runtime.pickedFiles=['document://picker/notes.txt'];
 try{
  await attachments.open(client,context(),session);await attachments.chooseFiles();const path=attachments.attachments[0].localPath;
  const begin=repo.mutations.begin.bind(repo.mutations);repo.mutations.begin=async(...args)=>{const record=await begin(...args);entered.resolve();await gate.promise;return record;};
  const preparing=attachments.prepareSend('Read',()=>allowed);await entered.promise;allowed=false;await attachments.close();gate.resolve();
  await assert.rejects(preparing,/request_updated|upload_failed|target_unavailable|scope_changed/);assert.equal(posts,0);
  await attachments.open(client,context(),session);
  assert.equal(attachments.attachments.length,1);assert.equal(runtime.files.has(path),true);assert.equal(attachments.attachments[0].status,'failed');
  allowed=true;await attachments.prepareSend('Read',()=>allowed);assert.equal(posts,1);
 }finally{globalThis.__attachmentRequest=previous;await attachments.close();}
});

test('a dispatched upload still saves its known receipt when the route retires before its response',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,gate=deferred(),entered=deferred(),session=row();
 let allowed=true;const repo=repository(async()=>{entered.resolve();return gate.promise;}),client=model(session,repo),attachments=new ChatAttachments();
 runtime.files.set('document://picker/notes.txt',new Uint8Array([1]));runtime.pickedFiles=['document://picker/notes.txt'];
 try{
  await attachments.open(client,context(),session);await attachments.chooseFiles();
  const preparing=attachments.prepareSend('Read',()=>allowed);await entered.promise;allowed=false;await attachments.close();
  client.selected=row('session-b');await attachments.open(client,context(),client.selected);
  gate.resolve({path:'/tmp/notes.txt',name:'notes.txt'});
  await assert.rejects(preparing,/scope_changed|target_unavailable/);assert.equal(repo.uploads.length,1);assert.equal(attachments.attachments.length,0);
  await attachments.close();client.selected=session;await attachments.open(client,context(),session);
  assert.equal(attachments.attachments[0].status,'ready');assert.equal(attachments.attachments[0].remotePath,'/tmp/notes.txt');
  allowed=true;await attachments.prepareSend('Read',()=>allowed);assert.equal(repo.uploads.length,1,'explicit retry reuses the confirmed receipt');
 }finally{await attachments.close();}
});
test('a late upload receipt cannot resurrect a forgotten attachment draft or its files',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,gate=deferred(),entered=deferred(),session=row();
 const repo=repository(async()=>{entered.resolve();return gate.promise;}),client=model(session,repo),attachments=new ChatAttachments();
 runtime.files.set('document://picker/notes.txt',new Uint8Array([1]));runtime.pickedFiles=['document://picker/notes.txt'];
 await attachments.open(client,context(),session);await attachments.chooseFiles();const path=attachments.attachments[0].localPath;
 const preparing=attachments.prepareSend('Read');await entered.promise;await attachments.close();
 runtime.epoch=(runtime.epoch||0)+1;await ChatAttachments.forgetHub(context(),'hub-a');
 gate.resolve({path:'/tmp/notes.txt',name:'notes.txt'});await assert.rejects(preparing,/scope_changed/);
 await attachments.open(client,context(),session);assert.equal(attachments.attachments.length,0);assert.equal(runtime.files.has(path),false);await attachments.close();
});

test('picked files stay local until prepareSend and use only the server receipt path', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime;
  const picked = 'document://picker/plan with spaces.txt';
  runtime.files.set(picked, new Uint8Array([1, 2, 3, 4])); runtime.pickedFiles = [picked];
  const repo = repository(async () => ({ path: '/tmp/agent uploads/plan.txt', name: 'plan-20261111.txt', size: 4 }));
  const attachments = new ChatAttachments(); const session = row();
  await attachments.open(model(session, repo), context(), session);
  await attachments.chooseFiles();
  assert.equal(repo.uploads.length, 0);
  assert.equal(attachments.attachments.length, 1);
  const selected = attachments.attachments[0];
  assert.equal(runtime.files.has(selected.localPath), true);
  assert.equal(selected.name, 'plan with spaces.txt');

  const prepared = await attachments.prepareSend('Please read this');
  assert.equal(repo.uploads.length, 1);
  assert.equal(repo.uploads[0].path, '/api/v1/machines/machine-a/files?name=plan%20with%20spaces.txt&dest=terminal');
  assert.equal(prepared.text, "Please read this '/tmp/agent uploads/plan.txt'");
  assert.deepEqual(prepared.images, []);
  assert.equal(prepared.repository, repo);
  await attachments.committed(prepared.snapshot);
  assert.equal(attachments.attachments.length, 0);
  assert.equal(runtime.files.has(selected.localPath), false);
});

test('attachment drafts survive close and reopen and remain isolated by session target', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime;
  const picked = 'document://picker/one.txt'; runtime.files.set(picked, new Uint8Array([1])); runtime.pickedFiles = [picked];
  const repo = repository(); const first = row('session-a'); const client = model(first, repo);
  const attachments = new ChatAttachments();
  await attachments.open(client, context(), first); await attachments.chooseFiles();
  const savedId = attachments.attachments[0].id; await attachments.close();
  const other = row('session-b'); client.selected = other;
  await attachments.open(client, context(), other);
  assert.deepEqual(attachments.attachments, []);
  await attachments.close();
  client.selected = first;
  const restored = new ChatAttachments(); await restored.open(client, context(), first);
  assert.equal(restored.attachments.length, 1);
  assert.equal(restored.attachments[0].id, savedId);
  assert.equal(runtime.files.has(restored.attachments[0].localPath), true);
});

test('an unknown upload remains blocked after reopening and does not upload twice', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime;
  const picked = 'document://picker/lost.txt'; runtime.files.set(picked, new Uint8Array([7, 8])); runtime.pickedFiles = [picked];
  const repo = repository(async () => { const error = new Error('reply lost'); error.resultUnknown = true; throw error; });
  const session = row(); const client = model(session, repo); const first = new ChatAttachments();
  await first.open(client, context(), session); await first.chooseFiles();
  await assert.rejects(first.prepareSend('Read'), /chat_attachment_upload_unknown/);
  assert.equal(first.attachments[0].status, 'unknown');
  await first.close();
  const restored = new ChatAttachments(); await restored.open(client, context(), session);
  assert.equal(restored.attachments[0].status, 'unknown');
  await assert.rejects(restored.prepareSend('Read again'), /chat_attachment_upload_unknown/);
  assert.equal(repo.uploads.length, 1);
});

test('photos resolve picker URIs through getAssets, validate decoded MIME, and send inline base64', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime;
  const photoUri = 'file://media/Photo/3/photo.jpg';
  runtime.files.set(photoUri, new Uint8Array([255, 216, 255, 0])); runtime.photoUris = [photoUri];
  runtime.photoAssets.set(photoUri, { uri: photoUri, photoType: 1, displayName: 'looks.png' });
  const repo = repository(); const session = row(); const attachments = new ChatAttachments();
  await attachments.open(model(session, repo), context(), session); await attachments.choosePhotos();
  assert.equal(attachments.attachments.length, 1);
  assert.equal(attachments.attachments[0].mime, 'image/jpeg');
  assert.equal(repo.uploads.length, 0);
  const prepared = await attachments.prepareSend('See photo');
  assert.equal(prepared.images.length, 1);
  assert.equal(prepared.images[0].mime, 'image/jpeg');
  assert.equal(Buffer.from(prepared.images[0].data, 'base64').byteLength, runtime.packBytes);
  assert.equal(repo.uploads.length, 0);
});

test('unsupported decoded image media is reported instead of guessed from the file name', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime;
  const photoUri = 'file://media/Photo/4/looks.jpg';
  runtime.files.set(photoUri, new Uint8Array([1, 2])); runtime.photoUris = [photoUri];
  runtime.photoAssets.set(photoUri, { uri: photoUri, photoType: 1, displayName: 'looks.jpg' });
  runtime.imageMime = 'image/heic';
  const session = row(); const attachments = new ChatAttachments();
  await attachments.open(model(session, repository()), context(), session); await attachments.choosePhotos();
  assert.deepEqual(attachments.attachments, []);
  assert.equal(attachments.errorKey, 'chat_attachment_image_unsupported');
  assert.equal([...runtime.files.keys()].some((path) => path.startsWith('/app/files/chat-attachments/')), false);
});

test('committing one captured revision preserves a later selection', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime;
  const first = 'document://picker/first.txt'; const second = 'document://picker/second.txt';
  runtime.files.set(first, new Uint8Array([1])); runtime.pickedFiles = [first];
  const repo = repository(); const session = row(); const attachments = new ChatAttachments();
  await attachments.open(model(session, repo), context(), session); await attachments.chooseFiles();
  const prepared = await attachments.prepareSend('Read files');
  runtime.files.set(second, new Uint8Array([2])); runtime.pickedFiles = [second]; await attachments.chooseFiles();
  const secondId = attachments.attachments[1].id;
  await attachments.committed(prepared.snapshot);
  assert.deepEqual(attachments.attachments.map((item) => item.id), [secondId]);
});

test('close invalidates an old picker and preserves a newer open', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime;
  const firstUri = 'document://picker/first.txt';
  runtime.files.set(firstUri, new Uint8Array([1])); runtime.pickedFiles = [firstUri];
  const repo = repository(); const sessionA = row('session-a'); const client = model(sessionA, repo);
  const attachments = new ChatAttachments();
  await attachments.open(client, context(), sessionA); await attachments.chooseFiles();

  const staleUri = 'document://picker/late.txt';
  runtime.files.set(staleUri, new Uint8Array([2])); runtime.pickedFiles = [staleUri];
  const pickerGate = deferred(); runtime.filePickerGate = pickerGate;
  const stalePicker = attachments.chooseFiles();
  const closing = attachments.close();
  assert.deepEqual(attachments.attachments, []);
  assert.equal(attachments.busy, false);

  const sessionB = row('session-b'); client.selected = sessionB;
  await attachments.open(client, context(), sessionB);
  const newerUri = 'document://picker/newer.txt';
  runtime.files.set(newerUri, new Uint8Array([3])); runtime.pickedFiles = [newerUri];
  await attachments.chooseFiles();
  const newerId = attachments.attachments[0].id;

  pickerGate.resolve();
  await stalePicker;
  assert.deepEqual(attachments.attachments.map((item) => item.id), [newerId]);
  assert.equal([...runtime.files.keys()].filter((path) => path.startsWith('/app/files/chat-attachments/')).length, 2);

  await closing;
  assert.deepEqual(attachments.attachments.map((item) => item.id), [newerId]);
  assert.equal(attachments.busy, false);
});

test('an older open cannot replace a newer open after a delayed database read', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime;
  const firstUri = 'document://picker/first.txt';
  runtime.files.set(firstUri, new Uint8Array([1])); runtime.pickedFiles = [firstUri];
  const repo = repository(); const sessionA = row('session-a'); const client = model(sessionA, repo);
  const attachments = new ChatAttachments();
  await attachments.open(client, context(), sessionA); await attachments.chooseFiles();

  const saveGate = deferred(); runtime.getGate = saveGate;
  const sessionB = row('session-b'); client.selected = sessionB;
  const olderOpen = attachments.open(client, context(), sessionB);
  assert.deepEqual(attachments.attachments, []);
  for(let i=0;i<30&&runtime.getGate;i++)await new Promise(resolve=>setTimeout(resolve,0));
  assert.equal(runtime.getGate,undefined);

  const sessionC = row('session-c'); client.selected = sessionC;
  await attachments.open(client, context(), sessionC);
  const newerUri = 'document://picker/newer.txt';
  runtime.files.set(newerUri, new Uint8Array([3])); runtime.pickedFiles = [newerUri];
  await attachments.chooseFiles();
  const newerId = attachments.attachments[0].id;

  saveGate.resolve();
  await olderOpen;
  assert.deepEqual(attachments.attachments.map((item) => item.id), [newerId]);
  assert.match((await attachments.prepareSend('Read')).target, /session-c/);
});

test('a native picker result survives a same-owner reconnect without uploading before send',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,gate=deferred(),session=row(),repo=repository(),client=model(session,repo),attachments=new ChatAttachments();
 const uri='document://picker/reconnected.txt';runtime.files.set(uri,new Uint8Array([1,2,3]));runtime.pickedFiles=[uri];runtime.filePickerGate=gate;
 await attachments.open(client,context(),session);const picking=attachments.chooseFiles();
 client.epoch++;gate.resolve();await picking;
 assert.equal(attachments.attachments.length,1);assert.equal(attachments.attachments[0].name,'reconnected.txt');assert.equal(repo.uploads.length,0);
 const prepared=await attachments.prepareSend('Read');assert.equal(prepared.generation,client.epoch);assert.equal(repo.uploads.length,1);
});
test('a reconnect during upload cannot dispatch a message or silently repeat the upload',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,gate=deferred(),session=row(),repo=repository(()=>gate.promise),client=model(session,repo),attachments=new ChatAttachments();
 const uri='document://picker/uncertain.txt';runtime.files.set(uri,new Uint8Array([1]));runtime.pickedFiles=[uri];
 await attachments.open(client,context(),session);await attachments.chooseFiles();const preparing=attachments.prepareSend('Read');
 for(let i=0;i<30&&!repo.uploads.length;i++)await new Promise(resolve=>setTimeout(resolve,0));
 assert.equal(repo.uploads.length,1);client.epoch++;gate.resolve({path:'/tmp/uncertain.txt',name:'uncertain.txt'});
 await assert.rejects(preparing,/scope_changed/);await attachments.close();await attachments.open(client,context(),session);
 assert.equal(attachments.attachments[0].status,'unknown');await assert.rejects(attachments.prepareSend('Read'),/upload_unknown/);assert.equal(repo.uploads.length,1);
});

test('forgetting a Hub deletes only its sandbox attachment copies and leaves another Hub intact',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),repo=repository(),client=model(session,repo),attachments=new ChatAttachments();
 runtime.files.set('document://picker/first.txt',new Uint8Array([1]));runtime.pickedFiles=['document://picker/first.txt'];
 await attachments.open(client,context(),session);await attachments.chooseFiles();const firstPath=attachments.attachments[0].localPath;await attachments.close();
 client.profile={id:'hub-b',hubId:'hub-b',manager:'manager-a'};
 runtime.files.set('document://picker/second.txt',new Uint8Array([2]));runtime.pickedFiles=['document://picker/second.txt'];
 await attachments.open(client,context(),session);await attachments.chooseFiles();const secondPath=attachments.attachments[0].localPath;await attachments.close();
 await ChatAttachments.forgetHub(context(),'hub-a');
 assert.equal(runtime.files.has(firstPath),false);assert.equal(runtime.files.has(secondPath),true);
 assert.ok([...runtime.db.keys()].every(key=>!isHubStateKey(key,'hub-a')));
 assert.ok([...runtime.db.keys()].some(key=>isHubStateKey(key,'hub-b')));
});

async function twoFiles() {
 resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),repo=repository(),client=model(session,repo),attachments=new ChatAttachments();
 for(const name of ['first.txt','second.txt'])runtime.files.set('document://picker/'+name,new Uint8Array([1,2,3]));
 runtime.pickedFiles=['document://picker/first.txt','document://picker/second.txt'];await attachments.open(client,context(),session);await attachments.chooseFiles();
 return {runtime,session,repo,client,attachments};
}
async function savedGate(runtime) {for(let i=0;i<40&&runtime.putGate;i++)await new Promise(resolve=>setTimeout(resolve,0));assert.equal(runtime.putGate,undefined);}

test('closing during a saved removal cannot restore the removed attachment',async()=>{
 const {runtime,session,client,attachments}=await twoFiles(),[first,second]=attachments.attachments,gate=deferred();runtime.putGate=gate;
 const removing=attachments.remove(first.id);await savedGate(runtime);await attachments.close();gate.resolve();await removing;await attachments.open(client,context(),session);
 assert.deepEqual(attachments.attachments.map(item=>item.id),[second.id]);assert.equal(runtime.files.has(first.localPath),false);assert.equal(runtime.files.has(second.localPath),true);
});
test('closing during an acknowledged revision cleanup cannot restore already sent attachments',async()=>{
 const {runtime,session,client,attachments}=await twoFiles(),prepared=await attachments.prepareSend('Read'),gate=deferred();
 // Leave one later unsent item so the cleanup persists a nonempty draft.
 runtime.files.set('document://picker/later.txt',new Uint8Array([4]));runtime.pickedFiles=['document://picker/later.txt'];await attachments.chooseFiles();const later=attachments.attachments.at(-1);
 runtime.putGate=gate;const committing=attachments.committed(prepared.snapshot);await savedGate(runtime);await attachments.close();gate.resolve();await committing;await attachments.open(client,context(),session);
 assert.deepEqual(attachments.attachments.map(item=>item.id),[later.id]);assert.equal(runtime.files.has(later.localPath),true);
});
test('a picker saved before closing keeps its persisted sandbox copy recoverable',async()=>{
 const {runtime,session,client,attachments}=await twoFiles(),gate=deferred();runtime.files.set('document://picker/later.txt',new Uint8Array([4]));runtime.pickedFiles=['document://picker/later.txt'];runtime.putGate=gate;
 const picking=attachments.chooseFiles();await savedGate(runtime);await attachments.close();gate.resolve();await picking;await attachments.open(client,context(),session);
 assert.equal(attachments.attachments.length,3);for(const item of attachments.attachments)assert.equal(runtime.files.has(item.localPath),true);
});
test('rapid removals and Send cannot overwrite a removal already awaiting storage',async()=>{
 const {runtime,attachments}=await twoFiles(),[first,second]=attachments.attachments,gate=deferred();runtime.putGate=gate;
 const removing=attachments.remove(first.id);assert.equal(attachments.busy,true);await attachments.remove(second.id);await assert.rejects(attachments.prepareSend('Read'),/target_unavailable/);
 await savedGate(runtime);gate.resolve();await removing;assert.deepEqual(attachments.attachments.map(item=>item.id),[second.id]);assert.equal(runtime.files.has(second.localPath),true);
 await attachments.remove(second.id);assert.deepEqual(attachments.attachments,[]);
});

test('a late ACK updates a reopened instance and preserves its later unsent selection',async()=>{
 const {runtime,session,client,attachments:first}=await twoFiles(),prepared=await first.prepareSend('Original send');await first.close();
 const second=new ChatAttachments();await second.open(client,context(),session);assert.equal(second.attachments.length,2);
 runtime.files.set('document://picker/later.txt',new Uint8Array([4]));runtime.pickedFiles=['document://picker/later.txt'];await second.chooseFiles();const later=second.attachments.at(-1);
 await first.committed(prepared.snapshot);
 assert.deepEqual(second.attachments.map(item=>item.id),[later.id]);
 const next=await second.prepareSend('New draft');assert.deepEqual(next.snapshot.attachments.map(item=>item.id),[later.id]);assert.equal(next.text.includes('first.txt'),false);assert.equal(next.text.includes('second.txt'),false);
});
test('oversized selected documents are rejected before sandbox copying or upload',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),repo=repository(),attachments=new ChatAttachments(),uri='document://picker/too-large.txt';
 runtime.files.set(uri,new Uint8Array([1]));runtime.reportedSizes=new Map([[uri,32*1024*1024+1]]);runtime.pickedFiles=[uri];
 await attachments.open(model(session,repo),context(),session);await attachments.chooseFiles();
 assert.equal(attachments.errorKey,'chat_attachment_file_too_large');assert.deepEqual(attachments.attachments,[]);assert.equal(repo.uploads.length,0);runtime.reportedSizes=undefined;
});
async function pickedPictures(byteArrays, limits = {}, repo = repository(), space = '') {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime, session = row(), attachments = new ChatAttachments();
  if (space) session.raw = JSON.stringify({ space });
  const client = model(session, repo);
  client.imageLimits = () => Object.assign(new ImageLimits(), limits);
  runtime.imageMime = 'image/png';
  for (const [index, bytes] of byteArrays.entries()) {
    const name = 'image-' + index + '.png', uri = 'photo://picker/' + name;
    runtime.files.set(uri, Uint8Array.from(bytes));
    runtime.photoUris.push(uri);
    runtime.photoAssets.set(uri, { uri, displayName: name, photoType: 1 });
  }
  await attachments.open(client, context(), session);
  await attachments.choosePhotos();
  assert.equal(attachments.attachments.length, byteArrays.length);
  return { runtime, client, session, repo, attachments };
}

test('a resized photo follows the hub long side and JPEG quality; its untouched original is uploaded if it cannot fit', async () => {
  const { runtime, client, session, repo, attachments } = await pickedPictures([[1, 2, 3]], { maxSide: 1440, imageBytes: 10 });
  const prepared = await attachments.prepareSend('Read');
  assert.deepEqual(runtime.packed.map(p => [p.format, p.quality, p.width, p.height]), [
    ['image/jpeg', 75, 1440, 1080], ['image/jpeg', 60, 1440, 1080], ['image/jpeg', 60, 1280, 960],
  ]);
  assert.equal(prepared.images.length, 0);
  assert.equal(prepared.overflowNoticeKey, 'chat_attachment_photos_size_as_files');
  assert.match(prepared.text, /remote\.bin/);
  assert.match(repo.uploads[0].path, /files\?name=image-0\.png&dest=attachment$/);
  assert.deepEqual([...new Uint8Array(repo.uploads[0].bytes)], [1, 2, 3]);
  await attachments.close();
  // An acknowledged upload stays a file after restoring the draft under a different limit.
  client.imageLimits = () => new ImageLimits();
  await attachments.open(client, context(), session);
  assert.equal((await attachments.prepareSend('Retry')).overflowNoticeKey, prepared.overflowNoticeKey);
  assert.equal(repo.uploads.length, 1);
  assert.equal(runtime.packed.length, 3);
  await attachments.close();
});

test('base64 padding, the one MiB reserve and the photo count decide which originals become files', async () => {
  const { runtime, repo, attachments } = await pickedPictures([[1], [2, 3, 4, 5], [6], [7]],
    { requestBytes: 1024 * 1024 + 8, perMessage: 2 }, repository(), 'health');
  runtime.imageWidth = 120; runtime.imageHeight = 80;
  const prepared = await attachments.prepareSend('Read');
  assert.deepEqual(prepared.images.map(p => [p.mime, p.data]), [['image/png', 'AQ=='], ['image/png', 'Bg==']]);
  assert.equal(runtime.packed.length, 0);
  assert.deepEqual(repo.uploads.map(u => [...new Uint8Array(u.bytes)]), [[2, 3, 4, 5], [7]]);
  assert.ok(repo.uploads.every(u => u.path.endsWith('&dest=health')));
  assert.equal(prepared.overflowNoticeKey, 'chat_attachment_photos_count_size_as_files');
  await attachments.close();
});

test('a ready count-overflow photo keeps that cause on the next prepare instead of claiming size', async () => {
  const { runtime, repo, attachments, client, session } = await pickedPictures([[1], [2]], { perMessage: 1 });
  runtime.imageWidth = 120; runtime.imageHeight = 80;
  const first = await attachments.prepareSend('Read');
  assert.equal(first.overflowNoticeKey, 'chat_attachment_photos_count_as_files');
  assert.equal(repo.uploads.length, 1);
  assert.equal(attachments.attachments.find(item => item.status === 'ready')?.overflowReason, 'count');
  await attachments.close();
  await attachments.open(client, context(), session);
  const second = await attachments.prepareSend('Retry');
  // The ready photo was uploaded for perMessage, not bytes: a retry must not say the message was too large.
  assert.equal(second.overflowNoticeKey, 'chat_attachment_photos_count_as_files');
  assert.equal(repo.uploads.length, 1);
  await attachments.close();
});

test('an unsupported photo is rejected even when its original would overflow the request', async () => {
  const { runtime, repo, attachments } = await pickedPictures([[1, 2, 3]], { requestBytes: 1024 * 1024 });
  runtime.imageMime = 'image/heic';
  await assert.rejects(attachments.prepareSend('Read'), /chat_attachment_image_unsupported/);
  assert.equal(repo.uploads.length, 0);
  await attachments.close();
});

test('a failed image pack does not fall through to a file upload', async () => {
  const { runtime, repo, attachments } = await pickedPictures([[1, 2, 3]], { maxSide: 1280 });
  runtime.packBytes = 0;
  await assert.rejects(attachments.prepareSend('Read'), /chat_attachment_image_unsupported/);
  assert.equal(repo.uploads.length, 0);
  await attachments.close();
});

test('a photo upload with an uncertain result is never repeated as a file or inline after reopening', async () => {
  const repo = repository(async () => { throw Object.assign(Error('timed out'), { resultUnknown: true }); });
  const { attachments, client, session } = await pickedPictures([[1], [2]], { perMessage: 1 }, repo);
  await assert.rejects(attachments.prepareSend('First'), /chat_attachment_upload_unknown/);
  await attachments.close();
  client.imageLimits = () => new ImageLimits();
  await attachments.open(client, context(), session);
  await assert.rejects(attachments.prepareSend('Retry'), /chat_attachment_upload_unknown/);
  assert.equal(repo.uploads.length, 1);
  await attachments.close();
});

test('an older hub receives a photo through its legacy upload only after a definite refusal of the new destination', async () => {
  const { HttpFailure } = await import('persona-test:transport');
  for (const features of [[], ['some_older_feature']]) {
    const repo = repository(async path => {
      if (path.endsWith('&dest=attachment')) throw new HttpFailure(400, 'unknown_upload_destination');
      assert.ok(!path.includes('&dest='));
      return { path: '/tmp/original.png', name: 'original.png' };
    });
    const { attachments, client } = await pickedPictures([[1]], { requestBytes: 1024 * 1024 }, repo);
    client.features = features;
    const prepared = await attachments.prepareSend('Describe');
    assert.equal(prepared.text, 'Describe /tmp/original.png');
    assert.equal(repo.uploads.length, features.length ? 1 : 2);
    await attachments.prepareSend('Retry');
    assert.equal(repo.uploads.length, features.length ? 1 : 2);
    await attachments.close();
  }
});

test('photo uploads retain their destination on other errors, uncertain refusals and health conversations', async () => {
  const { HttpFailure } = await import('persona-test:transport');
  for (const [space, error] of [
    ['health', new HttpFailure(400, 'unknown_upload_destination')],
    ['', new HttpFailure(400, 'bad_request')],
    ['', new HttpFailure(400, 'unknown_upload_destination', true)],
  ]) {
    const repo = repository(async () => { throw error; });
    const { attachments } = await pickedPictures([[1]], { requestBytes: 1024 * 1024 }, repo, space);
    await assert.rejects(attachments.prepareSend('Describe'));
    assert.equal(repo.uploads.length, 1);
    assert.ok(repo.uploads[0].path.endsWith('&dest=' + (space || 'attachment')));
    assert.equal(attachments.attachments.length, 1);
    await attachments.close();
  }
});
test('photo requests apply the UTF-8 text limit to Chinese, emoji and serialized source context',async()=>{
 const {withSourceContext}=await import('../entry/src/main/ets/models/Compatibility.ets');
 for(const text of ['界'.repeat(40000),'🌍'.repeat(30000),withSourceContext('界'.repeat(33300),JSON.stringify({kind:'todo',id:'x'.repeat(400),title:'Source'}),[])]){
  resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),repo=repository(),attachments=new ChatAttachments(),uri='photo://picker/bounded.png';runtime.files.set(uri,new Uint8Array([1,2,3]));runtime.photoUris=[uri];runtime.photoAssets.set(uri,{uri,displayName:'bounded.png',photoType:1});await attachments.open(model(session,repo),context(),session);await attachments.choosePhotos();
  await assert.rejects(attachments.prepareSend(text),/chat_attachment_request_too_large/);assert.equal(repo.uploads.length,0);await attachments.close();
 }
});
test('uploaded file paths count toward the same text bound and a shorter retry reuses the acknowledged upload',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),repo=repository(),attachments=new ChatAttachments(),uri='document://picker/report.txt';runtime.files.set(uri,new Uint8Array([1,2,3]));runtime.pickedFiles=[uri];await attachments.open(model(session,repo),context(),session);await attachments.chooseFiles();
 await assert.rejects(attachments.prepareSend('x'.repeat(99999)),/chat_attachment_request_too_large/);assert.equal(repo.uploads.length,1);assert.equal(attachments.attachments[0].status,'ready');assert.match((await attachments.prepareSend('Read')).text,/remote.bin/);assert.equal(repo.uploads.length,1);
});


test('an acknowledged stored selection cleans a closed draft while retaining newer unsent files',async()=>{
 const {runtime,session,client,attachments}=await twoFiles(),prepared=await attachments.prepareSend('Send these');await attachments.close();
 const reopened=new ChatAttachments();await reopened.open(client,context(),session);
 runtime.files.set('document://picker/new.txt',new Uint8Array([7]));runtime.pickedFiles=['document://picker/new.txt'];await reopened.chooseFiles();
 const newest=reopened.attachments.at(-1),owned=newest.localPath;
 await ChatAttachments.commitStoredSnapshot(context(),JSON.stringify(prepared.snapshot),client.target());
 assert.deepEqual(reopened.attachments.map(item=>item.id),[newest.id]);assert.equal(runtime.files.has(owned),true);assert.equal(reopened.busy,false);
 await reopened.close();const final=new ChatAttachments();await final.open(client,context(),session);assert.deepEqual(final.attachments.map(item=>item.id),[newest.id]);await final.close();
});

test('known-delivered attachments stay hidden and sending blocks until durable cleanup succeeds',async()=>{
 const {runtime,session,client,attachments}=await twoFiles(),prepared=await attachments.prepareSend('Send these');
 const oldRemove=attachments.storage.remove;attachments.storage.remove=async()=>{throw Error('disk full');};
 assert.equal(await attachments.committed(prepared.snapshot),false);assert.equal(attachments.attachments.length,0);assert.equal(attachments.busy,true);
 attachments.storage.remove=oldRemove;assert.equal(await attachments.committed(prepared.snapshot),true);assert.equal(attachments.busy,false);
 await attachments.close();await attachments.open(client,context(),session);assert.equal(attachments.attachments.length,0);await attachments.close();
});


test('a fresh attachment database open failure still suppresses known-delivered photos before any I/O',async()=>{
 const {runtime,session,client,attachments}=await twoFiles(),prepared=await attachments.prepareSend('Send these');runtime.openFailure=true;
 await assert.rejects(ChatAttachments.commitStoredSnapshot(context(),JSON.stringify(prepared.snapshot),client.target()),/open failed/);
 assert.equal(attachments.attachments.length,0);assert.equal(attachments.busy,true);assert.equal(attachments.errorKey,'chat_attachment_storage_unavailable');runtime.openFailure=false;
 await ChatAttachments.commitStoredSnapshot(context(),JSON.stringify(prepared.snapshot),client.target());assert.equal(attachments.busy,false);await attachments.close();
});

test('Health conversation files use the health destination and preserve the returned report path',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row();session.raw=JSON.stringify({space:'health'});const repo=repository(async()=>({path:'/health/reports/report.pdf',name:'report.pdf'})),client=model(session,repo),attachments=new ChatAttachments();
 runtime.files.set('document://picker/report.pdf',new Uint8Array([1,2]));runtime.pickedFiles=['document://picker/report.pdf'];await attachments.open(client,context(),session);await attachments.chooseFiles();const prepared=await attachments.prepareSend('Review');
 assert.equal(repo.uploads[0].path,'/api/v1/machines/machine-a/files?name=report.pdf&dest=health');assert.equal(prepared.text,'Review /health/reports/report.pdf');await attachments.close();
});

test('native photo editing grants read-only access and retains its original as a separate immutable attachment',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,uri='photo://original',asset={uri,displayName:'photo.jpg',photoType:1};runtime.files.set(uri,new Uint8Array([1,2,3]));runtime.photoAssets.set(uri,asset);runtime.photoUris=[uri];
 const session=row(),client=model(session,repository()),host=context(),attachments=new ChatAttachments();let requested;
 host.startAbilityByType=async(type,params,callback)=>{requested={type,params};runtime.files.set('file://edited',new Uint8Array([9,8]));callback.onResult({resultCode:0,want:{uri:'file://edited'}});};
 await attachments.open(client,host,session);await attachments.choosePhotos();const original=attachments.attachments[0],bytes=runtime.files.get(original.localPath).slice();await attachments.editPhoto(original.id);
 assert.equal(requested.type,'photoEditor');assert.equal(requested.params['ability.want.params.uriPermissionFlag'],1);assert.deepEqual(requested.params['ability.params.stream'],[original.previewUri]);assert.equal(attachments.attachments.length,2);assert.deepEqual(runtime.files.get(original.localPath),bytes);assert.notEqual(attachments.attachments[1].id,original.id);await attachments.close();
});

test('editing a received image appends a new owned attachment without changing existing draft images',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,uri='photo://draft',asset={uri,displayName:'draft.jpg',photoType:1};runtime.files.set(uri,new Uint8Array([1,2,3]));runtime.photoAssets.set(uri,asset);runtime.photoUris=[uri];
 const session=row(),client=model(session,repository()),host=context(),attachments=new ChatAttachments();let requested;
 host.startAbilityByType=async(type,params,callback)=>{requested={type,params};runtime.files.set('file://received-edited',new Uint8Array([9,8,7]));callback.onResult({resultCode:0,want:{uri:'file://received-edited'}});};
 await attachments.open(client,host,session);await attachments.choosePhotos();
 const original=attachments.attachments[0],originalPath=original.localPath,originalBytes=runtime.files.get(originalPath).slice();
 const received=new Uint8Array([4,5,6]).buffer;
 await attachments.editReceivedImage(received,'image/jpeg',client.target(),client.scope(),client.connectionGeneration(),'hub-a');
 assert.equal(requested.type,'photoEditor');assert.equal(requested.params['ability.want.params.uriPermissionFlag'],1);
 assert.notEqual(requested.params['ability.params.stream'][0],original.previewUri);
 assert.equal(attachments.attachments.length,2);assert.equal(attachments.attachments[0].id,original.id);
 assert.deepEqual(runtime.files.get(originalPath),originalBytes);
 const added=attachments.attachments[1];assert.notEqual(added.id,original.id);assert.equal(added.mime,'image/jpeg');
 assert.deepEqual(runtime.files.get(added.localPath),new Uint8Array([9,8,7]));await attachments.close();
});

test('received-image editor results from a changed target are discarded with owned staging files',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),client=model(session,repository()),host=context(),attachments=new ChatAttachments();let release,started;
 const entered=new Promise(resolve=>{started=resolve}),gate=new Promise(resolve=>{release=resolve;});
 host.startAbilityByType=async(_type,_params,callback)=>{started();await gate;runtime.files.set('file://stale-edit',new Uint8Array([9,8]));callback.onResult({resultCode:0,want:{uri:'file://stale-edit'}});};
 await attachments.open(client,host,session);const edit=attachments.editReceivedImage(new Uint8Array([1,2,3]).buffer,'image/jpeg',client.target(),client.scope(),client.connectionGeneration(),'hub-a');
 await entered;assert.equal(attachments.picking,true);client.selected=row('session-b');release();await edit;
 assert.equal(attachments.attachments.length,0);assert.equal([...runtime.files.keys()].some(path=>path.startsWith('/app/files/chat-attachments/')),false);
 await attachments.close();
});

test('a same-profile transport reconnect while the received-image editor is open keeps the owned edit',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),client=model(session,repository()),host=context(),attachments=new ChatAttachments();let release,started;
 const entered=new Promise(resolve=>{started=resolve}),gate=new Promise(resolve=>{release=resolve;});
 const uri='photo://existing-draft',asset={uri,displayName:'existing.jpg',photoType:1};runtime.files.set(uri,new Uint8Array([3,2,1]));runtime.photoAssets.set(uri,asset);runtime.photoUris=[uri];
 host.startAbilityByType=async(_type,_params,callback)=>{started();await gate;runtime.files.set('file://reconnected-edit',new Uint8Array([9,8,7]));client.epoch++;client.profile={...client.profile,baseUrl:'https://verified-alternate.invalid/'};callback.onResult({resultCode:0,want:{uri:'file://reconnected-edit'}});};
 await attachments.open(client,host,session);
 await attachments.choosePhotos();const original=attachments.attachments[0],originalBytes=runtime.files.get(original.localPath).slice();
 const edit=attachments.editReceivedImage(new Uint8Array([1,2,3]).buffer,'image/jpeg',client.target(),client.scope(),client.connectionGeneration(),'hub-a');
 await entered;assert.equal(attachments.picking,true);release();await edit;
 assert.equal(attachments.attachments.length,2);assert.equal(attachments.attachments[0].id,original.id);
 assert.deepEqual(runtime.files.get(original.localPath),originalBytes);assert.equal(attachments.attachments[1].mime,'image/jpeg');
 assert.deepEqual(runtime.files.get(attachments.attachments[1].localPath),new Uint8Array([9,8,7]));
 await attachments.close();
});

test('received-image editor rejects scope changes, Hub forgetting, and re-pairing while open',async()=>{
 for(const invalidation of ['scope','forget','repair']){
  resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),client=model(session,repository()),host=context(),attachments=new ChatAttachments();let release,started;
  const entered=new Promise(resolve=>{started=resolve}),gate=new Promise(resolve=>{release=resolve;});
  host.startAbilityByType=async(_type,_params,callback)=>{started();await gate;runtime.files.set('file://rejected-edit',new Uint8Array([9,8]));callback.onResult({resultCode:0,want:{uri:'file://rejected-edit'}});};
  await attachments.open(client,host,session);
  const edit=attachments.editReceivedImage(new Uint8Array([1,2,3]).buffer,'image/jpeg',client.target(),client.scope(),client.connectionGeneration(),'hub-a');
  await entered;
  if(invalidation==='scope')client.profile.manager='manager-b';
  if(invalidation==='forget')runtime.epoch++;
  if(invalidation==='repair'){client.profile={...client.profile};client.pairingEpoch++;}
  release();await edit;
  assert.equal(attachments.attachments.length,0,invalidation);
  assert.equal([...runtime.files.keys()].some(path=>path.startsWith('/app/files/chat-attachments/')),false,invalidation);
  await attachments.close();
 }
});

test('a received-image file stays with its acknowledged draft when close and navigation race the storage reply',async()=>{
 resetRuntime();const runtime=globalThis.__attachmentRuntime,session=row(),client=model(session,repository()),host=context(),attachments=new ChatAttachments();let releaseAck,enteredAck;
 const entered=new Promise(resolve=>{enteredAck=resolve}),ack=new Promise(resolve=>{releaseAck=resolve;});
 runtime.putAckGate={entered:enteredAck,promise:ack};
 host.startAbilityByType=async(_type,_params,callback)=>{runtime.files.set('file://committed-edit',new Uint8Array([6,5,4]));callback.onResult({resultCode:0,want:{uri:'file://committed-edit'}});};
 await attachments.open(client,host,session);
 const originalTarget=client.target();
 const edit=attachments.editReceivedImage(new Uint8Array([1,2,3]).buffer,'image/jpeg',originalTarget,client.scope(),client.connectionGeneration(),'hub-a');
 await entered;
 const draftTarget=JSON.stringify([client.profile.hubId,session.manager,session.machine,session.session]);
 const raw=runtime.db.get('attachments:'+draftTarget);assert.ok(raw);
 const acknowledged=JSON.parse(raw).attachments[0];assert.equal(runtime.files.has(acknowledged.localPath),true);
 await attachments.close();
 const anotherTarget=row('session-b');client.selected=anotherTarget;const other=new ChatAttachments();await other.open(client,host,anotherTarget);
 releaseAck();await edit;
 assert.equal(other.attachments.length,0);assert.equal(runtime.files.has(acknowledged.localPath),true);
 await other.close();client.selected=session;
 const restored=new ChatAttachments();await restored.open(client,host,session);
 assert.equal(restored.attachments.length,1);assert.equal(restored.attachments[0].localPath,acknowledged.localPath);
 assert.equal(restored.attachments[0].mime,acknowledged.mime);assert.equal(runtime.files.has(acknowledged.localPath),true);
 await restored.close();
});

test('photo picker follows remaining slots, keeps file slots separate and can still pick an overflow photo', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime, session = row(), client = model(session, repository());
  const attachments = new ChatAttachments(), limits = new ImageLimits();
  client.imageLimits = () => limits;
  await attachments.open(client, context(), session);
  const document = 'document://picker/movie.mp4';
  runtime.files.set(document, new Uint8Array([1]));
  runtime.pickedFiles = [document];
  await attachments.chooseFiles();
  const photo = 'photo://picker/example.jpg';
  runtime.files.set(photo, new Uint8Array([1]));
  runtime.photoAssets.set(photo, { uri: photo, displayName: 'example.jpg', photoType: 1 });
  runtime.photoUris = Array(6).fill(photo);
  await attachments.choosePhotos();
  assert.equal(runtime.photoPickLimit, 20);
  assert.equal(attachments.attachments.length, 7);
  await attachments.choosePhotos();
  assert.equal(runtime.photoPickLimit, 14);
  limits.perMessage = 1000;
  await attachments.choosePhotos();
  assert.equal(runtime.photoPickLimit, 500);
  limits.perMessage = 3;
  runtime.photoUris = [photo];
  await attachments.choosePhotos();
  assert.equal(runtime.photoPickLimit, 1);
  assert.equal(attachments.attachments.length, 8);
  await attachments.close();
  await attachments.open(client, context(), session);
  assert.equal(attachments.attachments.length, 8, 'a smaller current limit must not invalidate a saved draft');
  await attachments.close();
});

test('an acknowledged large photo selection is removed from storage after reopening', async () => {
  resetRuntime();
  const runtime = globalThis.__attachmentRuntime, session = row(), client = model(session, repository());
  const limits = new ImageLimits();
  limits.perMessage = 80;
  client.imageLimits = () => limits;
  const attachments = new ChatAttachments(), photo = 'photo://picker/review.jpg';
  runtime.files.set(photo, new Uint8Array([1]));
  runtime.photoAssets.set(photo, { uri: photo, displayName: 'review.jpg', photoType: 1 });
  runtime.photoUris = Array(70).fill(photo);
  await attachments.open(client, context(), session);
  await attachments.choosePhotos();
  const prepared = await attachments.prepareSend('Review these');
  assert.equal(prepared.images.length, 70);
  const snapshot = JSON.stringify(prepared.snapshot);
  assert.ok(snapshot.length > 4096);
  await attachments.close();
  await ChatAttachments.commitStoredSnapshot(context(), snapshot, client.target());
  await attachments.open(client, context(), session);
  assert.deepEqual(attachments.attachments, []);
  await attachments.close();
});

test('leaving while the upload marker is being saved keeps an unsent overflow photo retryable', async () => {
  const { runtime, client, session, repo, attachments } = await pickedPictures([[1], [2]], { perMessage: 1 });
  let markSaved, acknowledge;
  const saved = new Promise(resolve => { markSaved = resolve; });
  const acknowledgement = new Promise(resolve => { acknowledge = resolve; });
  const set = runtime.db.set;
  runtime.db.set = function(key, value) {
    if (JSON.parse(value).attachments?.some(item => item.status === 'uploading')) {
      delete this.set;
      runtime.putAckGate = { entered: markSaved, promise: acknowledgement };
    }
    return set.call(this, key, value);
  };
  const preparing = attachments.prepareSend('Read these');
  await saved;
  client.selected = row('another-session');
  acknowledge();
  await assert.rejects(preparing);
  assert.equal(repo.uploads.length, 0);
  await attachments.close();

  client.selected = session;
  await attachments.open(client, context(), session);
  const retried = await attachments.prepareSend('Read these');
  assert.equal(retried.images.length, 1);
  assert.equal(repo.uploads.length, 1);
  assert.deepEqual([...new Uint8Array(repo.uploads[0].bytes)], [2]);
  await attachments.close();
});

test('two active editors preparing the same overflow photo upload its original once', async () => {
  const { client, session, repo, attachments } = await pickedPictures([[1], [2]], { perMessage: 1 });
  const other = new ChatAttachments();
  await other.open(client, context(), session);

  // Both editors have read the same draft before either can claim the overflow upload.
  let release;
  const bothReady = new Promise(resolve => { release = resolve; });
  let encoding = 0;
  for (const editor of [attachments, other]) {
    const encode = editor.encodeImage.bind(editor);
    editor.encodeImage = async (...args) => {
      if (++encoding === 2) release();
      await bothReady;
      return encode(...args);
    };
  }
  const results = await Promise.allSettled([attachments.prepareSend('Read'), other.prepareSend('Read')]);
  assert.equal(results.filter(result => result.status === 'fulfilled').length, 1);
  assert.equal(repo.uploads.length, 1);
  assert.deepEqual([...new Uint8Array(repo.uploads[0].bytes)], [2]);
  await attachments.close();
  await other.close();
});

test('a photo whose overflow upload was refused goes inline once the edited draft fits', async () => {
  const { HttpFailure } = await import('persona-test:transport');
  const repo = repository(async () => { throw new HttpFailure(400, 'bad_request'); });
  const { runtime, attachments, client, session } = await pickedPictures([[1], [2]], { perMessage: 1 }, repo);
  runtime.imageWidth = 120;
  runtime.imageHeight = 80;
  await assert.rejects(attachments.prepareSend('First send'), /bad_request/);
  assert.equal(attachments.attachments[1].status, 'failed');
  await attachments.close();

  await attachments.open(client, context(), session);
  await attachments.remove(attachments.attachments[0].id);
  const prepared = await attachments.prepareSend('Send the remaining photo');
  assert.deepEqual(prepared.images.map(photo => photo.data), ['Ag==']);
  assert.equal(prepared.overflowNoticeKey, '');
  assert.equal(repo.uploads.length, 1);
  await attachments.close();
  await attachments.open(client, context(), session);
  assert.equal(attachments.attachments[0].overflowReason, '');
  await attachments.close();
});
