import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { EventEmitter } from 'node:events'
import { envValue, stateDir, prepareStateDir, isAssistantDir } from '../lib/identity.js'
import { LEGACY_LISTEN_PATH, defaultListenPathMatches, runtimeStorePath, wirePackageName, SNAPSHOT_RE } from '../lib/compat.js'
import { PACKAGES, packageHash, readShipped, verifyPackage, selfPackage } from '../lib/package.js'
import { upgrade } from '../lib/listen.js'
import { alwaysAllowed, lastHost } from '../../pi/lib/map.js'
import { LEGACY_RELOAD, piConfigPath, piHostKind, sharedPiGlobals } from '../../pi/lib/compat.js'
import { mcpServers, daemonBinary } from '../../antigravity/lib/compat.js'
import { cleanUserText } from '../../antigravity/lib/map.js'

function temp(t) {
  const p = mkdtempSync(join(tmpdir(), 'persona-compat-'))
  t.after(() => rmSync(p, { recursive: true, force: true }))
  return p
}

test('compat environment and existing root do not create an empty store', (t) => {
  assert.equal(envValue('PERSONA_HOME', { JARVIS_HOME: 'old' }), 'old')
  assert.equal(envValue('PERSONA_HOME', { PERSONA_HOME: 'new', JARVIS_HOME: 'old' }), 'new')
  assert.equal(envValue('PERSONA_HOME', { PERSONA_HOME: '', JARVIS_HOME: 'old' }), '')
  const home = temp(t), legacy = join(home, '.jarvis')
  mkdirSync(legacy)
  assert.equal(stateDir(home, {}), legacy)
  assert.equal(prepareStateDir(home, {}), legacy)
  assert.equal(isAssistantDir(join(legacy, 'assistant'), home, {}), true)
  const store = join(legacy, 'runtime-packages', 'jarvis-pi-runtime')
  mkdirSync(store, { recursive: true })
  assert.equal(runtimeStorePath(legacy, 'persona-pi-runtime'), store)
  mkdirSync(join(home, '.persona'))
  assert.throws(() => stateDir(home, {}), /independent state directories/)
  assert.equal(stateDir(home, { PERSONA_HOME: legacy }), legacy)
})

test('compat websocket selects only a protocol actually offered and still checks token', () => {
  for (const protocol of ['persona-runtime.v1', 'jarvis-runtime.v1']) {
    const req = { method: 'GET', headers: { authorization: 'Bearer secret', upgrade: 'websocket', 'sec-websocket-key': 'test', 'sec-websocket-version': '13', 'sec-websocket-protocol': protocol } }
    const socket = new EventEmitter()
    let output = ''
    socket.write = (s) => { output += s }
    socket.end = (s) => { output += s }
    socket.destroy = () => {}
    assert.ok(upgrade(req, socket, Buffer.alloc(0), { token: 'secret' }))
    assert.ok(output.includes(`Sec-WebSocket-Protocol: ${protocol}\r\n`))
    output = ''
    assert.equal(upgrade(req, socket, Buffer.alloc(0), { token: 'wrong' }), null)
    assert.ok(output.startsWith('HTTP/1.1 401'))
  }
  assert.equal(defaultListenPathMatches(LEGACY_LISTEN_PATH, '/persona/runtime', '/persona/runtime'), true)
  assert.equal(defaultListenPathMatches(LEGACY_LISTEN_PATH, '/custom', '/persona/runtime'), false)
})

test('compat package names use identical files and hashes and keep exact name verification', () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '../..')
  for (const canonical of ['persona-dsh-runtime', 'persona-pi-runtime', 'persona-paseo-runtime', 'persona-agent-sdk-runtime']) {
    const legacy = wirePackageName(canonical)
    assert.equal(legacy, canonical.replace('persona-', 'jarvis-'))
    assert.deepEqual(PACKAGES[legacy], PACKAGES[canonical])
    assert.equal(packageHash(readShipped(root, PACKAGES[legacy])), packageHash(readShipped(root, PACKAGES[canonical])))
  }
  assert.equal(selfPackage(new URL('../../pi/index.js', import.meta.url).href, 'persona-pi-runtime').name, 'jarvis-pi-runtime')
  const files = [{path:'pi/index.js', data:Buffer.from('safe')}]
  const pkg = {name:'jarvis-pi-runtime', hash:packageHash(files), files:files.map((f)=>({path:f.path,data:f.data.toString('base64')}))}
  assert.ok(verifyPackage(pkg, 'jarvis-pi-runtime'))
  assert.throws(() => verifyPackage(pkg, 'persona-dsh-runtime'), /not persona-dsh-runtime/)
  assert.throws(() => verifyPackage({...pkg, hash:'bad'}, 'jarvis-pi-runtime'), /hash/)
})

test('compat pi markers, config and explicitly empty host selector', (t) => {
  assert.equal(alwaysAllowed([{type:'custom',customType:'jarvis-allow',data:{tool:'bash'}}], 'bash'), true)
  assert.equal(lastHost([{type:'custom',customType:'persona-host',data:{host:'terminal'}},{type:'custom',customType:'jarvis-host',data:{host:'pi-web'}}]), 'pi-web')
  assert.equal(piHostKind({JARVIS_PI_HOST:'web'}), 'web')
  assert.equal(piHostKind({PERSONA_PI_HOST:'',JARVIS_PI_HOST:'web'}), '')
  assert.equal(LEGACY_RELOAD, 'jarvis-reload')
  const dir = temp(t)
  writeFileSync(join(dir, 'jarvis.json'), '{}')
  assert.equal(piConfigPath(dir), join(dir, 'jarvis.json'))
  writeFileSync(join(dir, 'persona.json'), '{}')
  assert.equal(piConfigPath(dir), join(dir, 'persona.json'))
})

test('compat pi globals share takeover slots in either direction and preserve false', (t) => {
  const keys = ['__personaPi','__jarvisPi','__personaPiHost','__jarvisPiHost','__personaPiAskUser','__jarvisPiAskUser']
  const saved = keys.map((k)=>[k,Object.getOwnPropertyDescriptor(globalThis,k)])
  t.after(()=>{ for (const [k,desc] of saved) { delete globalThis[k]; if(desc) Object.defineProperty(globalThis,k,desc) } })
  for(const key of keys) delete globalThis[key]
  globalThis.__jarvisPi = { hash:'old' }
  globalThis.__jarvisPiAskUser = false
  sharedPiGlobals()
  assert.equal(globalThis.__personaPi, globalThis.__jarvisPi)
  assert.equal(globalThis.__personaPiAskUser, false)
  globalThis.__personaPi = {hash:'new'}
  assert.equal(globalThis.__jarvisPi.hash,'new')
  globalThis.__jarvisPi = {hash:'rollback'}
  assert.equal(globalThis.__personaPi.hash,'rollback')
  globalThis.__jarvisPiAskUser = true
  assert.equal(globalThis.__personaPiAskUser,true)
})

test('compat context only strips bare live snapshots and MCP aliases keep session scope', (t) => {
  for (const scheme of ['persona', 'jarvis']) {
    const snapshot = `<${scheme}-context>private snapshot</${scheme}-context>`
    assert.equal((snapshot+' hello').replace(SNAPSHOT_RE,''), 'hello')
    assert.equal(cleanUserText(snapshot+' hello'), 'hello')
    const quoted = `<${scheme}-context source="user">keep</${scheme}-context>`
    assert.equal(quoted.replace(SNAPSHOT_RE,''), quoted)
    // the view feedback after the snapshot is the model's too (#4335)
    assert.equal((snapshot+'<persona-view-errors>\nThe view v1 failed its check.\n</persona-view-errors>\nhello').replace(SNAPSHOT_RE,''), 'hello')
  }
  const servers = mcpServers('/bin/personad','session',true)
  assert.deepEqual(servers.map((s)=>s.name),['persona']) // agy: one server, its tool budget counts each (#7333)
  for(const server of servers) assert.deepEqual(server.env,[{name:'PERSONA_SESSION_ID',value:'session'},{name:'JARVIS_SESSION_ID',value:'session'},{name:'PERSONA_ASSISTANT',value:'1'},{name:'JARVIS_ASSISTANT',value:'1'}])
  const home = temp(t)
  const bin = join(home,'.jarvis','bin','jarvisd')
  mkdirSync(dirname(bin),{recursive:true}); writeFileSync(bin,'binary')
  assert.equal(daemonBinary(home,{}),bin)
})
