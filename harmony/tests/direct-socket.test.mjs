import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { registerHooks, stripTypeScriptTypes } from 'node:module';

const kits = {
  '@kit.AbilityKit': ['bundleManager'], '@kit.BasicServicesKit': ['deviceInfo'],
  '@kit.LocalizationKit': ['i18n'], '@kit.PerformanceAnalysisKit': ['hilog'],
  '@kit.CryptoArchitectureKit': ['cryptoFramework'], '@kit.DeviceCertificateKit': ['cert'],
  '@kit.NetworkKit': ['connection', 'socket'], '@kit.RemoteCommunicationKit': ['rcp'], '@kit.ArkTS': ['url', 'util']
};
registerHooks({
  resolve(specifier, context, next) {
    if (Object.hasOwn(kits, specifier)) return { url: 'socket-kit:' + specifier, shortCircuit: true };
    if (context.parentURL?.endsWith('/HubTransport.ets') && specifier.startsWith('../core/'))
      return { url: new URL(specifier + '.ts', context.parentURL).href, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.startsWith('socket-kit:')) return { format: 'module', shortCircuit: true,
      source: kits[url.slice(11)].map(name => `export const ${name} = globalThis.socketTestKit.${name};`).join('\n') };
    if (url.endsWith('.ets')) return { format: 'module', shortCircuit: true,
      source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8'), { mode: 'transform' }) };
    return next(url, context);
  }
});

// Stop at the peer certificate. No fake upgrade or credentials can make a dial look successful.
let records, lookup, refused, dials, tlsCalls, closed, writes;
class FakeSocket {
  on() {}
  off() {}
  async close() { closed.add(this); }
  async send() { writes++; }
}
function route(address) {
  const candidates = address.address === 'hub.test' ? records : [address];
  const candidate = candidates.find(candidate => candidate.family === address.family);
  if (!candidate || refused.has(candidate.address)) throw Error('unreachable_address');
  return candidate;
}
class TCP extends FakeSocket {
  async connect({ address }) { dials.push(address); this.peer = route(address); }
}
class TLS extends FakeSocket {
  constructor(tcp) { super(); this.tcp = tcp; }
  async bind(address) { this.local = address; }
  async connect(options) {
    tlsCalls.push(options);
    if (!options.proxy && !this.tcp) {
      assert.equal(this.local.family, options.address.family);
      route(options.address);
    }
    if (this.tcp) assert.equal(this.tcp.peer.family, options.address.family);
  }
  async getRemoteCertificate() { return { encodingFormat: -1, data: new Uint8Array() }; }
}
globalThis.socketTestKit = {
  rcp: {}, i18n: {getSystemLanguage: () => 'en-US'},
  util: {TextEncoder: class { encodeInto(value) { return new TextEncoder().encode(value); } },
    TextDecoder: {create: (encoding, options) => ({decodeToString: bytes => new TextDecoder(encoding,options).decode(bytes)})}},
  hilog: { error() {} }, url: { URL: { parseURL: input => new URL(input) } },
  cert: { EncodingFormat: { FORMAT_DER: 0, FORMAT_PEM: 1 } },
  connection: { getAddressesByName: name => lookup(name) },
  socket: { constructTCPSocketInstance: () => new TCP(), constructTLSSocketInstance: tcp => new TLS(tcp),
    Protocol: { TLSv13: 'TLSv1.3', TLSv12: 'TLSv1.2' }, ProxyTypes: { SOCKS5: 1 } }
};
const { EndpointConfig, HubTransport } = await import('../entry/src/main/ets/platform/HubTransport.ets');

test('held Agent HTTP 202 and client hello use the actual transport contract', async () => {
  const requests = [], envelopes = [], rcp = globalThis.socketTestKit.rcp;
  let reply, status = 202;
  rcp.Request = class { constructor(target,method,headers) { Object.assign(this,{target,method,headers}); } };
  rcp.createSession = () => ({close(){}, async fetch(request) {
    requests.push(request); request.destination(new TextEncoder().encode(JSON.stringify(reply)).buffer);
    return {statusCode:status, headers:{}};
  }});
  const client = new HubTransport(new EndpointConfig('https://desk.test/','fixture','','','owner'));
  client.clientMetadata = () => ({platform:'harmony',version:'1',build:'1',os:{}});
  client.send = message => {envelopes.push(JSON.parse(message));return true;};
  assert.ok(client.hello(['elicitation']));
  assert.deepEqual(envelopes[0].features, ['elicitation','agent_message_hold']);
  const root = '/api/v1/sessions/my%20laptop/chat%2Fone', path = root + '/messages';
  reply = {delivered:'later',undelivered:{id:'own-id',state:'waiting',computer:'Desk'}};
  assert.deepEqual(JSON.parse(await client.request(path,'POST','{}','own-id')), reply);
  assert.equal(requests.at(-1).target,'https://desk.test'+path);
  assert.equal(requests.at(-1).headers['X-Persona-Features'],'agent_message_hold');
  assert.equal(requests.at(-1).headers['X-Persona-Manager'],'owner');
  for(const [response,pending] of [[{state:'unknown'},false],[{state:'pending'},true],[{},true],
    [{...reply,undelivered:{...reply.undelivered,id:'foreign'}},true]]) {
    reply = response;
    await assert.rejects(client.request(path,'POST','{}','own-id'), error => error.status===202 && error.resultUnknown && error.resultPending===pending);
  }
  reply = {ok:true}; status = 200;
  const route = root + '/held/op%2F1/retry';
  await client.request(route,'POST','{}','action'); assert.equal(requests.at(-1).target,'https://desk.test'+route);
  assert.equal(requests.at(-1).headers['X-Persona-Features'],undefined);
  await assert.rejects(client.request('/api/v1/files/a%2Fb','GET'), /invalid_path/);
});
test('chat sends and photo uploads use their own outgoing limits and keep the normal response caps', async () => {
  const client = transport(), mib = 1024 * 1024;
  const message = '/api/v1/sessions/desk/chat/messages';
  const upload = '/api/v1/machines/desk/files?name=original.jpg&dest=attachment';
  const calls = [];
  client.requestData = async (path, method, body, type, responseLimit) => {
    calls.push({path, responseLimit});
    return {data: new ArrayBuffer(0), contentType: type};
  };

  const large = 'a'.repeat(4 * mib) + '界';
  await client.request(message, 'POST', large, 'message', 4 * mib + 3);
  assert.deepEqual(calls.pop(), {path: message, responseLimit: 4 * mib});
  await assert.rejects(client.request(message, 'POST', large, 'message', 4 * mib + 2), /request_too_large/);
  await assert.rejects(client.request('/api/v1/todos', 'POST', large, 'todo', 32 * mib), /request_too_large/);

  const sessions = '/api/v1/sessions?limit=50';
  await client.request(sessions, 'GET');
  assert.deepEqual(calls.pop(), {path: sessions, responseLimit: 64 * mib});
  const todos = '/api/v1/todos';
  await client.request(todos, 'GET');
  assert.deepEqual(calls.pop(), {path: todos, responseLimit: 4 * mib});

  const original = new ArrayBuffer(64 * mib);
  await client.requestBytes(upload, 'POST', original, 'application/octet-stream', 'photo', 64 * mib);
  assert.deepEqual(calls.pop(), {path: upload, responseLimit: 32 * mib});
  await assert.rejects(client.requestBytes(upload, 'POST', original, 'application/octet-stream', 'photo', original.byteLength - 1), /request_too_large/);
  await assert.rejects(client.requestBytes(upload, 'POST', original), /request_too_large/);
  assert.deepEqual(calls, []);
});

test('only an explicit old-hub refusal of the attachment destination permits a temp-file retry', async () => {
  const client = transport(), rcp = globalThis.socketTestKit.rcp;
  client.clientMetadata = () => ({platform: 'harmony', version: '1', build: '1', os: {}});
  const path = '/api/v1/machines/desk/files?name=image.png&dest=attachment';
  let body, status = 400;
  rcp.Request = class {};
  rcp.createSession = () => ({close() {}, async fetch(request) {
    request.destination(new TextEncoder().encode(JSON.stringify(body)).buffer);
    return {statusCode: status, headers: {}};
  }});
  const post = (destination = path) => client.requestBytes(destination, 'POST', new ArrayBuffer(8));
  const refused = error => error.code === 'unknown_upload_destination' && !error.resultUnknown;
  for (body of [{error: 'unknown dest'}, {code: 'bad_request', error: 'Bad request', detail: 'unknown dest'}])
    await assert.rejects(post(), refused);
  await assert.rejects(post(path.replace('attachment', 'health')), error => error.code === 'bad_request');
  body = {code: 'bad_request', error: 'invalid image'};
  await assert.rejects(post(), error => error.code === 'bad_request');
  body = {code: 'bad_request', error: 'unknown dest', state: 'unknown'};
  await assert.rejects(post(), error => error.resultUnknown);
  status = 500;
  await assert.rejects(post(), error => error.code === 'bad_request' && error.resultUnknown);
});

const v4 = { address: '192.0.2.20', family: 1 }, v6 = { address: '2001:db8::20', family: 2 };
function reset(answers) {
  records = answers; refused = new Set(); dials = []; tlsCalls = []; closed = new Set(); writes = 0;
  lookup = async name => { assert.equal(name, 'hub.test'); return records; };
}
function transport(host = 'hub.test', proxy) {
  return new HubTransport(new EndpointConfig('https://' + host + ':8443/', '', 'fixture', 'A'.repeat(43), '', [], proxy));
}
async function rejectsPin(client) {
  await assert.rejects(client.connect(() => {}), /hub_pin_mismatch/);
  assert.equal(writes, 0);
}

test('an AAAA-only domain reaches pin verification with its original TLS hostname', async () => {
  reset([v6]);
  await rejectsPin(transport());
  assert.deepEqual(tlsCalls.map(call => call.address), [{ address: 'hub.test', port: 8443, family: 2 }]);
});
test('an unreachable family can fall back before TLS and pin failure ends the dial', async () => {
  reset([v6, v4]); refused.add(v6.address);
  await rejectsPin(transport());
  assert.deepEqual(dials.map(peer => peer.family), [2, 1]);
  assert.equal(closed.size, 2);
  assert.equal(tlsCalls.at(-1).address.address, 'hub.test');
  assert.equal(tlsCalls.at(-1).address.family, 1);
  reset([v6, v4]);
  await rejectsPin(transport());
  assert.equal(tlsCalls.length, 1);
  assert.equal(tlsCalls[0].address.family, 2);
});
test('numeric peers and the SSH proxy do not start a direct Hub DNS lookup', async () => {
  reset([]); lookup = async () => { throw Error('unexpected_lookup'); };
  await rejectsPin(transport('[::1]')); await rejectsPin(transport('127.0.0.1'));
  const proxy = { host: '127.0.0.1', port: 1234, username: 'fixture', password: 'fixture', isCurrent: () => true };
  await rejectsPin(transport('hub.test', proxy));
  assert.equal(tlsCalls[2].proxy.address.address, proxy.host);
  assert.equal(tlsCalls[2].address.address, 'hub.test');
});
test('a second connect or a retired DNS lookup cannot open a socket', async () => {
  reset([v6]);
  const pending = Promise.withResolvers(), client = transport();
  lookup = () => pending.promise;
  const first = client.connect(() => {});
  const error = assert.rejects(first, /connection_closed/);
  await assert.rejects(client.connect(() => {}), /already_connecting/);
  client.close(); await error;
  pending.resolve(records); await Promise.resolve();
  assert.equal(tlsCalls.length, 0); assert.equal(dials.length, 0); assert.equal(writes, 0);
});

test('only the authenticated operation lookup recognizes the Hub missing-receipt envelope', async () => {
  const rcp = globalThis.socketTestKit.rcp;
  let body = {code: 'not_found', error: 'no such operation'}, status = 404;
  rcp.Request = class { constructor(target, method, headers) { Object.assign(this, {target, method, headers}); } };
  rcp.createSession = () => ({close() {}, async fetch(request) {
    request.destination(new TextEncoder().encode(JSON.stringify(body)).buffer);
    return {statusCode: status, headers: {}};
  }});
  const transport = new HubTransport(new EndpointConfig('https://desk.test/', 'fixture', '', '', 'owner'));
  transport.clientMetadata = () => ({platform: 'harmony', version: '1', build: '1', os: {}});
  const path = '/api/v1/ops/0d9ce811-24fe-4f6e-ae77-000000000001';
  const code = async (route, error, method = 'GET') => assert.rejects(
    transport.request(route, method), failure => failure.status === status && failure.code === error);
  await code(path, 'no_such_operation');
  await code('/api/v1/workspaces', 'not_found');
  await code('/api/v1/ops/bad-id', 'not_found');
  await code(path, 'not_found', 'POST');
  status = 403; await code(path, 'not_found');
  status = 404; body = {code: 'not_found', error: 'no such route'}; await code(path, 'not_found');
});

function sessionFrame(sequence) {
  const bytes = new TextEncoder().encode(JSON.stringify({type:'session',sequence}));
  return Uint8Array.of(0x81, bytes.length, ...bytes);
}

test('buffered Hub events preserve order across a partial read, including a reentrant reader', () => {
  const client = new HubTransport(new EndpointConfig('https://fixture.invalid', 'fixture', '', '', 'assistant'));
  const append = bytes => { client.rx.append(bytes, 1024 * 1024); };
  const seen = [];
  client.messageCallback = raw => {
    const {sequence} = JSON.parse(raw);
    seen.push(sequence);
    if (sequence === 2) {
      append(sessionFrame(4));
      client.processFrames();
    }
  };
  const next = sessionFrame(2);
  append(Uint8Array.from([...sessionFrame(1), ...next.slice(0, 7)]));
  client.processFrames();
  assert.deepEqual(seen, [1]);
  assert.equal(client.rx.length, 7);
  assert.equal(client.rx.view().byteLength, 7);
  append(Uint8Array.from([...next.slice(7), ...sessionFrame(3)]));
  client.processFrames();
  assert.deepEqual(seen, [1, 2, 3, 4]);
  assert.equal(client.rx.length, 0);
  assert.equal(client.rx.view().byteLength, 0);
});

test('when an event closes its transport the remaining buffer stays with that old connection', () => {
  const config = new EndpointConfig('https://fixture.invalid', 'fixture', '', '', 'assistant');
  const old = new HubTransport(config), next = new HubTransport(config), seen = [];
  next.messageCallback = raw => seen.push(JSON.parse(raw).sequence);
  old.messageCallback = raw => {
    seen.push(JSON.parse(raw).sequence);
    old.close();
    next.rx.append(sessionFrame(3), 1024 * 1024);
    next.processFrames();
  };
  old.rx.append(Uint8Array.from([...sessionFrame(1), ...sessionFrame(2)]), 1024 * 1024);
  old.processFrames();
  assert.deepEqual(seen, [1, 3]);
  assert.equal(old.closed, true);
  assert.equal(old.rx.length, 0);
});

test('fragmented text is checked as one Hub hello before any following event is delivered', () => {
  const stream = new HubTransport(new EndpointConfig('https://fixture.invalid', 'fixture', '', '', 'assistant'));
  const seen = [], states = [];
  stream.messageCallback = raw => seen.push(JSON.parse(raw));
  stream.stateCallback = state => states.push(state);
  const hello = {type:'hello',hub_id:'fixture',label:'已就绪'};
  const body = new TextEncoder().encode(JSON.stringify(hello));
  const cut = body.indexOf(0xe5) + 1;
  assert.ok(cut > 1 && cut < body.length);
  stream.rx.append(Uint8Array.of(0x01, cut, ...body.subarray(0,cut)), 1024 * 1024);
  stream.processFrames();
  assert.deepEqual(seen, []);
  stream.rx.append(Uint8Array.of(0x8a,0, 0x80,body.length-cut,...body.subarray(cut), ...sessionFrame(1)), 1024 * 1024);
  stream.processFrames();
  assert.deepEqual(seen, [hello,{type:'session',sequence:1}]);

  const changed = new TextEncoder().encode(JSON.stringify({type:'hello',hub_id:'other'}));
  stream.rx.append(Uint8Array.of(0x81,changed.length,...changed,...sessionFrame(2)), 1024 * 1024);
  stream.processFrames();
  assert.deepEqual(seen, [hello,{type:'session',sequence:1}]);
  assert.deepEqual(states, ['offline']);
  assert.equal(stream.rx.length, 0);
});

test('reading a normal batch does not recopy the unconsumed suffix after each message', t => {
  const stream = new HubTransport(new EndpointConfig('https://fixture.invalid', 'fixture', '', '', 'assistant'));
  const order = Array.from({length:12}, (_, i) => i);
  stream.rx.append(Uint8Array.from(order.flatMap(i=>Array.from(sessionFrame(i)))), 1024 * 1024);
  const total = stream.rx.length, originalSlice = Uint8Array.prototype.slice, seen = [];
  let copiedSuffix = 0;
  t.mock.method(Uint8Array.prototype, 'slice', function(...args) {
    const copy = originalSlice.apply(this,args);
    if (args.length === 1 && args[0] > 0) copiedSuffix += copy.length;
    return copy;
  });
  stream.messageCallback = raw => seen.push(JSON.parse(raw).sequence);
  stream.processFrames();
  assert.deepEqual(seen, order);
  assert.ok(copiedSuffix <= total, 'unconsumed data was copied repeatedly');
  assert.equal(stream.rx.length, 0);
});

test('reserved Health routes keep the paired credentials and use only the Hub’s default manager',async()=>{
  const requests=[],rcp=globalThis.socketTestKit.rcp, uuid='ed0fe4c5-f936-4419-b24f-b95cee97dc9d',base='/api/v1/assistant/health/intake';
  rcp.Request=class {constructor(target,method,headers){Object.assign(this,{target,method,headers});}};
  rcp.createSession=()=>({close(){},async fetch(request){requests.push(request);request.destination(new TextEncoder().encode('{}').buffer);return {statusCode:200,headers:{}};}});
  const client=new HubTransport(new EndpointConfig('https://test.invalid/','fixture','synthetic-health','','manager-a'));
  client.clientMetadata=()=>({platform:'harmony',version:'1',build:'1',os:{}});
  for(const [path,verb,defaultOnly] of [[base+'?space=health','POST',true],[base+'?space=health','GET',true],[base+'/ui?space=health','POST',true],[base+'/'+uuid+'?space=health','DELETE',true],[base+'/ui?space=work','POST',false],[base+'/unrelated?space=health','DELETE',false],['/api/v1/assistant?space=health','GET',false]]){
    await client.request(path,verb,verb==='POST'?'{}':'');
    const h=requests.at(-1).headers;assert.equal(h.Authorization,'Bearer synthetic-health');assert.equal(h['X-Persona-Manager'],defaultOnly?undefined:'manager-a');
  }
});
