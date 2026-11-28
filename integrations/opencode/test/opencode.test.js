import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createOpencodeHost } from '../lib/opencode.js'
import { registerA2A } from '../lib/a2a.js'

const T0 = Date.now() - 60_000

/** opencode.db with the two tables the runtime reads (the columns it reads, as opencode creates them). */
function fakeDb({ busy = true } = {}) {
  const db = new DatabaseSync(':memory:')
  db.exec(`create table session_v2 (id text primary key, parent_id text, directory text not null, title text, model text,
    revert text, time_created integer not null, time_updated integer not null, time_idle integer,${busy ? ' time_busy integer,' : ''} time_archived integer);
    create table session_message (id text primary key, session_id text not null, type text not null, seq integer not null,
    time_created integer not null, time_updated integer not null, data text not null);`)
  const api = {
    db,
    session(id, o = {}) {
      db.prepare('insert into session_v2 (id, parent_id, directory, title, model, revert, time_created, time_updated, time_archived) values (?,?,?,?,?,?,?,?,?)')
        .run(id, o.parent ?? null, o.directory ?? '/w/app', o.title ?? null, o.model ?? null, o.revert ?? null, o.created ?? T0, o.updated ?? Date.now(), o.archived ?? null)
    },
    message(session, seq, type, data, updated = Date.now()) {
      const id = `msg_${session}_${seq}`
      db.prepare('insert or replace into session_message (id, session_id, type, seq, time_created, time_updated, data) values (?,?,?,?,?,?,?)')
        .run(id, session, type, seq, T0 + seq, updated, JSON.stringify({ time: { created: T0 + seq, ...(data.time ?? {}) }, ...data, time2: undefined }))
    },
    user: (s, seq, text) => api.message(s, seq, 'user', { text }),
    assistant: (s, seq, text, extra = {}) => api.message(s, seq, 'assistant', { content: text ? [{ type: 'text', text }] : [], finish: 'stop', time: { completed: T0 + seq + 1 },
      model: { id: 'model-x', providerID: 'prov' }, tokens: { input: 1000, output: 10, reasoning: 0, cache: { read: 3000, write: 0 } }, ...extra }),
    openDb: async () => ({ all: (sql, ...p) => db.prepare(sql).all(...p), close() {} }),
  }
  return api
}

/** The experimental media_blob tables, with the keys and indexes opencode's migration creates. */
function fakeBlobDb() {
  const d = fakeDb()
  d.db.exec(`create table blob_ref (
    session_id text not null, message_id text not null, hash text not null, seq integer not null,
    constraint blob_ref_pk primary key (session_id, message_id, hash),
    constraint fk_blob_ref_session_id_session_v2_id_fk foreign key (session_id) references session_v2(id) on delete cascade,
    constraint fk_blob_ref_hash_blob_hash_fk foreign key (hash) references blob(hash) on delete cascade);
    create table blob (hash text primary key, mime text not null, bytes integer not null,
      width integer, height integer, data blob not null, time_created integer not null);
    create index blob_ref_hash_idx on blob_ref(hash);
    create index blob_ref_session_seq_idx on blob_ref(session_id, seq);`)
  d.blob = (hash, data, { mime = 'image/png', bytes = data.byteLength } = {}) => {
    d.db.prepare('insert into blob (hash, mime, bytes, width, height, data, time_created) values (?,?,?,null,null,?,?)')
      .run(hash, mime, bytes, data, T0)
  }
  d.ref = (session, message, seq, hash) => {
    d.db.prepare('insert into blob_ref (session_id, message_id, hash, seq) values (?,?,?,?)').run(session, message, hash, seq)
  }
  return d
}

const hashOf = (data) => createHash('sha256').update(data).digest('hex')

async function imageNotFound(n, session, item) {
  await assert.rejects(n.peer.call('session.image', { session_id: session, item_id: item }), (err) => err.code === 'not_found')
}

/** The plugin context of one location: the session / permission API as calls it records, and an event stream the test feeds. */
function fakeCtx(directory = '/w/app') {
  const queue = []
  let wake
  const ctx = {
    location: { directory },
    calls: [],
    session: {},
    permission: {},
    model: { list: async () => [{ id: 'model-x', providerID: 'prov', limit: { context: 200000 } }] },
    event: {
      async *subscribe({ signal }) {
        while (!signal?.aborted) {
          if (queue.length) { yield queue.shift(); continue }
          await new Promise((r) => { wake = r; signal?.addEventListener('abort', r, { once: true }) })
        }
      },
    },
    emit(type, data, id = `evt_${Math.random()}`) { queue.push({ id, type, data }); wake?.() },
  }
  for (const [ns, names] of [['session', ['prompt', 'interrupt', 'update']], ['permission', ['reply']]]) {
    for (const name of names) ctx[ns][name] = async (args) => { ctx.calls.push([`${ns}.${name}`, args]); return {} }
  }
  return ctx
}

async function setup(t, opts = {}) {
  const n = await startFakeNode(t)
  const d = opts.db ?? fakeDb()
  const host = createOpencodeHost({ openDb: d.openDb, pid: 4242, log: opts.log, now: opts.now }, { socketPath: n.path, retryMs: 20, pushDelayMs: 5, answerFirst: opts.answerFirst, listDays: opts.listDays })
  t.after(() => host.stop())
  return { n, d, host }
}

test('A2A tools in OpenCode use the executing session and its cancellation signal', async (t) => {
  const { n, d, host } = await setup(t)
  d.session('ses_a2a')
  const ctx = fakeCtx()
  host.register(ctx)
  const tools = []
  ctx.tool = { transform: async (define) => define({ add: (tool) => tools.push(tool) }) }
  await registerA2A(ctx, host.runtime)
  assert.deepEqual(tools.map((tool) => tool.name), ['a2a_sessions', 'a2a_read', 'a2a_get', 'a2a_send', 'a2a_report', 'a2a_cancel'])
  await n.ready
  await until('OpenCode A2A session listed', () => n.last('runtime.sessions')?.sessions.some(s => s.session_id === 'ses_a2a'))
  const report = tools.find((tool) => tool.name === 'a2a_report')
  const args = { task_id: 'task', message_id: 'report', state: 'completed', text: 'Test result' }
  const stop = new AbortController()
  assert.deepEqual(await report.execute(args, { sessionID: 'ses_a2a', signal: stop.signal }), { content: 'a2a_report ran' })
  assert.deepEqual(n.toolCalls, [{ name: 'a2a_report', args, session_id: 'ses_a2a' }])
  n.callTool = () => ({ is_error: true, content: [{ type: 'text', text: 'refused' }] })
  await assert.rejects(report.execute(args, { sessionID: 'ses_a2a', signal: stop.signal }), /refused/)
})

test('one runtime per process lists the recent root sessions of the database', async (t) => {
  const d = fakeDb()
  d.session('ses_a', { title: 'Fix the parser', model: '{"id":"model-x"}' })
  d.user('ses_a', 4, 'fix the parser')
  d.assistant('ses_a', 5, 'Fixed it.\nSecond line')
  d.session('ses_new', { title: null, directory: '/w/lib' })
  d.user('ses_new', 2, 'what is in this repo?')
  d.session('ses_child', { parent: 'ses_a' })
  d.session('ses_archived', { archived: Date.now() })
  d.session('ses_ancient', { updated: Date.now() - 30 * 86400_000 })
  const { n, host } = await setup(t, { db: d })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  assert.equal(n.hello.runtime, 'opencode-4242')
  assert.equal(n.hello.agent, 'opencode')
  await until('listed', () => n.last('runtime.sessions')?.sessions.length === 2)
  const rows = Object.fromEntries(n.last('runtime.sessions').sessions.map((s) => [s.session_id, s]))
  assert.deepEqual(Object.keys(rows).sort(), ['ses_a', 'ses_new'])
  const a = rows.ses_a
  assert.deepEqual([a.title, a.cwd, a.status, a.last_text, a.model, a.rev, a.space_kind], ['Fix the parser', '/w/app', 'done', 'Fixed it. Second line', 'model-x', 5, 'opencode'])
  assert.equal(a.context_tokens, 4000)
  await until('window', async () => (await n.peer.call('session.items', { session_id: 'ses_a', turns: 1 })) && n.last('runtime.sessions').sessions.find((s) => s.session_id === 'ses_a').context_window === 200000)
  assert.equal(rows.ses_new.title, 'what is in this repo?', 'no title yet: the first message')
  assert.equal(rows.ses_new.status, 'idle')
  assert.equal(rows.ses_new.space_name, 'lib')
})

test('pages the conversation by turns, back and forward; a message still being written is not part of it', async (t) => {
  const d = fakeDb()
  d.session('ses_p')
  d.user('ses_p', 4, 'one')
  d.assistant('ses_p', 5, 'a1')
  d.user('ses_p', 9, 'two')
  d.message('ses_p', 10, 'assistant', { content: [{ type: 'text', text: 'a2' }, { type: 'tool', id: 'call_1', name: 'shell', state: { status: 'completed', input: { command: 'ls' }, content: [{ type: 'text', text: 'x' }] } }], finish: 'tool-calls', time: { completed: T0 + 99 } })
  d.message('ses_p', 14, 'assistant', { content: [{ type: 'text', text: 'writing…' }], time: {} }) // no completed: still running
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  const p = await n.peer.call('session.items', { session_id: 'ses_p', turns: 1 })
  assert.deepEqual(p.items.map((i) => i.id), ['9#0', '10#0', '10#2', '10#3'])
  assert.equal(p.to_seq, 10, 'the unfinished message is not there yet')
  assert.equal(p.has_more, true)
  assert.match(p.history, /^opencode:ses_p/)
  const before = await n.peer.call('session.items', { session_id: 'ses_p', before_seq: 9, limit: 10 })
  assert.deepEqual(before.items.map((i) => i.text), ['one', 'a1'])
  assert.equal(before.has_more, false)
  const after = await n.peer.call('session.items', { session_id: 'ses_p', after_seq: 5 })
  assert.deepEqual(after.items.map((i) => i.id), ['9#0', '10#0', '10#2', '10#3'])
  assert.equal(after.from_seq, 5)
  const full = await n.peer.call('session.item', { session_id: 'ses_p', item_id: '10#3' })
  assert.equal(full.text, 'x')
  assert.equal(await n.peer.call('session.cursor', { session_id: 'ses_p' }).catch(() => 10), 10)
})

test('catching up past more messages than one read takes: the page ends where the read did, and says more comes', async (t) => {
  const d = fakeDb()
  d.session('ses_long')
  // 300 plain turns: a user message, a reply and the idle marker (no item) each: 900 rows, 600 items.
  let seq = 0
  for (let i = 0; i < 300; i++) {
    d.user('ses_long', ++seq, `q${i}`)
    d.assistant('ses_long', ++seq, `a${i}`)
    d.message('ses_long', ++seq, 'idle', { outcome: 'completed' })
  }
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  const seen = []
  let after = 0
  for (let i = 0; i < 10; i++) {
    const p = await n.peer.call('session.items', { session_id: 'ses_long', after_seq: after })
    seen.push(...p.items.map((it) => it.seq))
    assert.ok(p.items.every((it) => it.seq <= p.to_seq), 'nothing past to_seq')
    after = p.to_seq
    if (!p.more_after) break
  }
  assert.equal(after, 900)
  assert.equal(seen.length, 600, 'every message once: none skipped between pages')
  assert.equal(new Set(seen).size, 600)
})

test('sends into the session (once per client_msg_id), interrupts and renames through the plugin API of the session\'s location', async (t) => {
  const d = fakeDb()
  d.session('ses_c', { directory: '/w/lib' })
  const { n, host } = await setup(t, { db: d })
  const app = fakeCtx('/w/app')
  const lib = fakeCtx('/w/lib')
  host.register(app)
  host.register(lib)
  await n.ready
  await n.peer.call('session.send', { session_id: 'ses_c', text: 'go on', client_msg_id: 'm1' })
  await n.peer.call('session.send', { session_id: 'ses_c', text: 'go on', client_msg_id: 'm1' }) // a retry
  assert.deepEqual(lib.calls, [['session.prompt', { sessionID: 'ses_c', text: 'go on' }]])
  assert.equal(app.calls.length, 0, 'the location the session lives in')
  await n.peer.call('session.send', { session_id: 'ses_c', text: 'look', images: [{ mime: 'image/png', data: 'AAAA' }], client_msg_id: 'm2' })
  assert.deepEqual(lib.calls.at(-1)[1].files, [{ uri: 'data:image/png;base64,AAAA', name: 'image-1.png' }])
  await n.peer.call('session.interrupt', { session_id: 'ses_c' })
  await n.peer.call('session.rename', { session_id: 'ses_c', title: 'Build fix' })
  assert.deepEqual(lib.calls.slice(-2), [['session.interrupt', { sessionID: 'ses_c' }], ['session.update', { sessionID: 'ses_c', title: 'Build fix' }]])
  await assert.rejects(n.peer.call('session.send', { session_id: 'ses_c', text: '   ', client_msg_id: 'm3' }), /消息是空的/)
})

test('a blob-backed FileRef image loads its exact bytes as standard base64 with the FileRef MIME', async (t) => {
  const d = fakeBlobDb()
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64')
  const hash = hashOf(png)
  d.session('ses_blob')
  d.message('ses_blob', 4, 'user', { text: 'look', files: [{ mime: 'image/png', source: { type: 'inline' }, name: 'shot.png', uri: `media:${hash}` }] })
  d.blob(hash, png, { mime: 'image/jpeg' }) // the FileRef, not a conflicting blob row, describes the attachment
  d.ref('ses_blob', 'msg_ses_blob_4', 4, hash)

  // Both sqlite drivers return views. Exercise a Buffer and a Uint8Array with non-zero offsets and trailing bytes.
  const openDb = d.openDb
  let blobReads = 0
  d.openDb = async () => {
    const handle = await openDb()
    return {
      close: handle.close,
      all(sql, ...params) {
        const result = handle.all(sql, ...params)
        if (!/\bfrom\s+blob\b/i.test(sql)) return result
        blobReads++
        return result.map((row) => {
          const raw = Buffer.from(row.data.buffer, row.data.byteOffset, row.data.byteLength)
          const padded = Buffer.alloc(raw.length + 8, 0xa5)
          raw.copy(padded, 4)
          const view = padded.subarray(4, 4 + raw.length)
          return { ...row, data: blobReads % 2 ? view : new Uint8Array(view.buffer, view.byteOffset, view.byteLength) }
        })
      },
    }
  }
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready

  const page = await n.peer.call('session.items', { session_id: 'ses_blob', limit: 10 })
  const item = page.items.find((it) => it.kind === 'image')
  assert.equal(item.id, '4#1')
  assert.deepEqual(item.image, { mime: 'image/png', data: '', lazy: true })
  const expected = { mime: 'image/png', data: png.toString('base64') }
  assert.deepEqual(await n.peer.call('session.image', { session_id: 'ses_blob', item_id: item.id }), expected)
  assert.deepEqual(await n.peer.call('session.image', { session_id: 'ses_blob', item_id: item.id }), expected)
  assert.equal(blobReads, 2)
})

test('a legacy inline image keeps its existing session.image behavior', async (t) => {
  const d = fakeBlobDb()
  const data = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 0xfe, 0x80]).toString('base64')
  d.session('ses_inline')
  d.message('ses_inline', 3, 'user', { files: [{ mime: 'image/png', name: 'legacy.png', data }] })
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  assert.deepEqual(await n.peer.call('session.image', { session_id: 'ses_inline', item_id: '3#1' }), { mime: 'image/png', data })
})

test('malformed, percent-encoded, uppercase and arbitrary image URIs are not fetched as blobs', async (t) => {
  const d = fakeBlobDb()
  const hash = 'a'.repeat(64)
  const uris = [
    'media:not-a-hash',
    `media:${'a'.repeat(63)}`,
    `media:${'A'.repeat(64)}`,
    `media:%61${'a'.repeat(63)}`,
    `media:${hash}%0A`,
    `media:${hash}?download=1`,
    'https://example.test/image.png',
    'file:///tmp/image.png',
  ]
  d.session('ses_uri')
  d.message('ses_uri', 4, 'user', { files: uris.map((uri) => ({ mime: 'image/png', name: 'bad.png', uri })) })
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  for (let i = 0; i < uris.length; i++) await imageNotFound(n, 'ses_uri', `4#${i + 1}`)
})

test('a media hash cannot load through a missing, foreign or stale blob_ref', async (t) => {
  const d = fakeBlobDb()
  d.session('ses_guard')
  d.session('ses_other')
  const cases = [
    { seq: 10, name: 'missing blob' },
    { seq: 11, name: 'orphan hash without blob_ref' },
    { seq: 12, name: 'cross-session ref' },
    { seq: 13, name: 'cross-message ref' },
    { seq: 14, name: 'wrong message seq' },
    { seq: 15, itemSeq: 16, name: 'rewritten message seq' },
  ]
  for (const c of cases) {
    const raw = Buffer.from(c.name)
    const hash = hashOf(raw)
    const message = `msg_ses_guard_${c.seq}`
    d.message('ses_guard', c.seq, 'user', { files: [{ mime: 'image/png', name: 'guarded.png', uri: `media:${hash}` }] })
    if (c.name !== 'missing blob') d.blob(hash, raw)
    switch (c.name) {
      case 'missing blob':
        // A database written with foreign keys off can retain this dangling ref; the reader must still reject it.
        d.db.exec('pragma foreign_keys = off')
        d.ref('ses_guard', message, c.seq, hash)
        d.db.exec('pragma foreign_keys = on')
        break
      case 'cross-session ref':
        d.message('ses_other', c.seq, 'user', { text: 'other session' })
        d.ref('ses_other', `msg_ses_other_${c.seq}`, c.seq, hash)
        break
      case 'cross-message ref':
        d.message('ses_guard', 90, 'user', { text: 'other message' })
        d.ref('ses_guard', 'msg_ses_guard_90', 90, hash)
        break
      case 'wrong message seq':
        d.ref('ses_guard', message, c.seq + 1, hash)
        break
      case 'rewritten message seq':
        d.ref('ses_guard', message, c.seq, hash)
        d.db.prepare('update session_message set seq = ? where id = ?').run(c.itemSeq, message)
        break
    }
  }
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  for (const c of cases) await imageNotFound(n, 'ses_guard', `${c.itemSeq ?? c.seq}#1`)
})

test('an oversized blob is rejected by actual raw length before base64 allocation', async (t) => {
  const d = fakeBlobDb()
  // This alone would need the RPC's whole 64 MiB line as base64, before the JSON envelope. Lie in blob.bytes to
  // prove the loader checks the actual BLOB view, not upstream's metadata or an encoded-size check after allocation.
  const raw = Buffer.alloc(48 * 1024 * 1024, 0x69)
  const hash = hashOf(raw)
  d.session('ses_oversize')
  d.message('ses_oversize', 4, 'user', { files: [{ mime: 'image/png', name: 'large.png', uri: `media:${hash}` }] })
  d.blob(hash, raw, { bytes: 1 })
  d.ref('ses_oversize', 'msg_ses_oversize_4', 4, hash)
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready

  const toString = Buffer.prototype.toString
  Buffer.prototype.toString = function (encoding, ...args) {
    if (encoding === 'base64') throw new Error('base64 was allocated before the raw-size check')
    return toString.call(this, encoding, ...args)
  }
  try {
    await imageNotFound(n, 'ses_oversize', '4#1')
  } finally {
    Buffer.prototype.toString = toString
  }
})

test('a watched session gets each finished message once, as a continuous batch; its live text as deltas; the state follows the turn', async (t) => {
  const d = fakeDb()
  d.session('ses_w')
  d.user('ses_w', 4, 'one')
  d.assistant('ses_w', 5, 'a1')
  const { n, host } = await setup(t, { db: d })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await n.peer.call('session.watch', { session_id: 'ses_w', since_seq: 0 })
  ctx.emit('session.inbox.enqueued', { sessionID: 'ses_w', inboxID: 'msg_x', item: { type: 'user', payload: { text: 'two' } } })
  await until('working', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_w')?.status === 'working')
  ctx.emit('session.text.delta', { sessionID: 'ses_w', assistantMessageID: 'msg_a', ordinal: 0, delta: 'Hel' })
  ctx.emit('session.text.delta', { sessionID: 'ses_w', assistantMessageID: 'msg_a', ordinal: 0, delta: 'lo' })
  await until('live text', () => n.all('session.delta').some((x) => x.text === 'Hello'))
  d.user('ses_w', 9, 'two')
  d.assistant('ses_w', 10, 'Hello there')
  ctx.emit('session.step.ended', { sessionID: 'ses_w', assistantMessageID: 'msg_a', finish: 'stop' })
  ctx.emit('session.execution.succeeded', { sessionID: 'ses_w' })
  await until('pushed', () => n.all('session.items').flatMap((b) => b.items).some((i) => i.text === 'Hello there'))
  const batches = n.all('session.items')
  assert.deepEqual(batches.slice(1).filter((b, i) => b.from_seq !== batches[i].to_seq), [])
  assert.equal(batches.at(-1).to_seq, 10)
  assert.equal(n.all('session.items').flatMap((b) => b.items).filter((i) => i.text === 'Hello there').length, 1)
  await until('done again', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_w')?.status === 'done')
  assert.equal(n.last('runtime.sessions').sessions.find((s) => s.session_id === 'ses_w').rev, 10)
  assert.ok(n.all('session.delta').at(-1).text === '', 'the live text ends with the turn')
})

test('a shell that runs past STALE_MS still publishes its same-seq result', async (t) => {
  let now = Date.now()
  const d = fakeDb()
  d.session('ses_long_shell')
  const { n, host } = await setup(t, { db: d, now: () => now })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await n.peer.call('session.watch', { session_id: 'ses_long_shell', since_seq: 0 })
  await host.commit('ses_long_shell')
  await host.onEvent(ctx, { type: 'session.execution.started', data: { sessionID: 'ses_long_shell' } })

  d.message('ses_long_shell', 5, 'shell', { command: 'sleep 61; echo done', status: 'running', time: { created: now } }, now)
  await host.commit('ses_long_shell')
  assert.equal(host.sessions.get('ses_long_shell').lastSeq, 0, 'running shell is not final at start')

  now += 61_000 // the running turn's normal poll calls the same commit path
  await host.commit('ses_long_shell')

  d.message('ses_long_shell', 5, 'shell', { command: 'sleep 61; echo done', status: 'completed', exit: 0,
    output: { output: 'done' }, time: { created: now - 61_000, completed: now } }, now)
  await host.commit('ses_long_shell')
  await until('completed shell result pushed', () => n.all('session.items').flatMap((b) => b.items).some((i) => i.kind === 'tool_result' && i.text === 'done'), 500)
})

test('what a watched session writes is pushed cut to the page limits, as its pages are', async (t) => {
  const d = fakeDb()
  d.session('ses_big')
  d.user('ses_big', 4, 'one')
  d.assistant('ses_big', 5, 'a1')
  const { n, host } = await setup(t, { db: d })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await n.peer.call('session.watch', { session_id: 'ses_big', since_seq: 0 })
  ctx.emit('session.execution.started', { sessionID: 'ses_big' }) // the conversation so far is the baseline
  await until('working', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_big')?.status === 'working')
  const output = 'line of output\n'.repeat(2000) // 30 KB
  d.message('ses_big', 8, 'assistant', { content: [{ type: 'tool', id: 'call_1', name: 'shell', state: { status: 'completed', input: { command: 'cat big.log' }, content: [{ type: 'text', text: output }] } }],
    finish: 'tool-calls', time: { created: T0 + 8, completed: T0 + 9 } })
  ctx.emit('session.tool.success', { sessionID: 'ses_big' })
  const pushed = await until('pushed', () => n.all('session.items').flatMap((b) => b.items).find((i) => i.kind === 'tool_result'))
  const paged = (await n.peer.call('session.items', { session_id: 'ses_big', limit: 2 })).items.find((i) => i.kind === 'tool_result')
  assert.equal(pushed.full_size, Buffer.byteLength(output), 'cut, with the size of the whole')
  assert.ok(Buffer.byteLength(pushed.text) < 5 * 1024, 'at most the 4 KB a page carries')
  assert.deepEqual(pushed, paged, 'the push and the page say the same')
})

test('every location delivers the same events: each is handled once; the stream moves to another location when its reader goes away', async (t) => {
  const d = fakeDb()
  d.session('ses_e')
  d.user('ses_e', 4, 'one')
  const { n, host } = await setup(t, { db: d })
  const a = fakeCtx('/w/app')
  const b = fakeCtx('/w/lib')
  const offA = host.register(a)
  host.register(b)
  await n.ready
  a.emit('session.execution.started', { sessionID: 'ses_e' }, 'evt_1')
  b.emit('session.execution.started', { sessionID: 'ses_e' }, 'evt_1')
  await until('working', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_e')?.status === 'working')
  assert.equal(host.sessions.get('ses_e').running, true)
  offA() // the primary goes: b reads the stream now
  b.emit('session.execution.succeeded', { sessionID: 'ses_e' }, 'evt_2')
  await until('idle again', () => host.sessions.get('ses_e').running === false)
})

/** A session the test runs a turn on, and its row as last listed. */
async function missedEnd(t, dbOpts) {
  const d = fakeDb(dbOpts)
  d.session('ses_m')
  d.user('ses_m', 4, 'one')
  const { n, host } = await setup(t, { db: d })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  const row = () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_m')
  const set = (cols) => d.db.prepare(`update session_v2 set ${Object.keys(cols).map((k) => `${k} = ?`).join(', ')} where id = ?`).run(...Object.values(cols), 'ses_m')
  let renames = 0
  const relist = async () => {
    const title = `r${++renames}`
    ctx.emit('session.renamed', { sessionID: 'ses_m', title })
    await until(`listed as ${title}`, () => row()?.title === title)
  }
  ctx.emit('session.execution.started', { sessionID: 'ses_m' })
  await until('working', () => row()?.status === 'working')
  return { d, n, host, ctx, row, set, relist }
}

test('a turn that ended while the event stream was down: time_idle after time_busy ends it, even after a rename', async (t) => {
  const { d, host, ctx, row, set, relist } = await missedEnd(t)
  const start = Date.now()
  // The previous turn's stamps: idle before this turn's start, still working.
  set({ time_busy: T0, time_idle: T0 + 1, time_updated: T0 + 1 })
  await relist()
  assert.equal(row().status, 'working')
  // This turn started in opencode and is not over: busy after idle.
  set({ time_busy: start, time_idle: T0 + 1 })
  await relist()
  assert.equal(row().status, 'working')
  // It ends while the stream is down; then a title generated alongside it renames the session (time_updated moves on).
  d.assistant('ses_m', 5, 'done it')
  set({ time_idle: start + 5, time_updated: start + 800 })
  await relist()
  assert.equal(row().status, 'done', 'a rename after the end does not keep it working')
  assert.equal(host.sessions.get('ses_m').running, false)
  // The end event arrives late after all: nothing changes, nothing breaks.
  ctx.emit('session.execution.succeeded', { sessionID: 'ses_m' })
  await relist()
  assert.equal(row().status, 'done')
  // The next turn starts: working again, not ended by the old idle stamp.
  ctx.emit('session.execution.started', { sessionID: 'ses_m' })
  await until('working again', () => row()?.status === 'working')
  await relist()
  assert.equal(row().status, 'working')
})

test('an opencode without time_busy: the list still reads, and the events alone say when a turn ends', async (t) => {
  const { d, ctx, row, set, relist } = await missedEnd(t, { busy: false })
  d.assistant('ses_m', 5, 'done it')
  set({ time_idle: Date.now() + 5, time_updated: Date.now() + 5 })
  await relist()
  assert.equal(row().status, 'working', 'no start stamp to compare with: left to the events')
  ctx.emit('session.execution.succeeded', { sessionID: 'ses_m' })
  await until('done', () => row()?.status === 'done')
})

test('a permission: a card on the phone while the TUI dialog waits; allow, always, deny with a reason; an answer in the TUI withdraws it', async (t) => {
  const d = fakeDb()
  d.session('ses_perm')
  d.user('ses_perm', 4, 'build it')
  const { n, host } = await setup(t, { db: d, answerFirst: 'phone' })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 'ses_perm'))
  const ask = (id, extra = {}) => ctx.emit('permission.asked', { id, sessionID: 'ses_perm', action: 'shell', resources: ['rm -rf build'], save: ['rm *'], source: { type: 'tool', id: `call_${id}` }, ...extra })

  n.resolve = () => ({ allow: true })
  ask('per_1')
  await until('replied once', () => ctx.calls.some((c) => c[0] === 'permission.reply'))
  assert.deepEqual(n.asked[0].tool_name, 'Bash')
  assert.equal(n.asked[0].summary, 'rm -rf build')
  assert.equal(n.asked[0].tool_input.command, 'rm -rf build')
  assert.equal(n.asked[0].always_label, '总是允许 rm *')
  assert.deepEqual(ctx.calls.at(-1), ['permission.reply', { sessionID: 'ses_perm', requestID: 'per_1', decision: 'once' }])

  n.resolve = () => ({ allow: true, always: true })
  ask('per_2')
  await until('always', () => ctx.calls.filter((c) => c[0] === 'permission.reply').length === 2)
  assert.equal(ctx.calls.at(-1)[1].decision, 'always')

  n.resolve = () => ({ allow: false, message: 'not that one' })
  ask('per_3')
  await until('rejected', () => ctx.calls.filter((c) => c[0] === 'permission.reply').length === 3)
  assert.deepEqual(ctx.calls.at(-1)[1], { sessionID: 'ses_perm', requestID: 'per_3', decision: 'reject', message: 'not that one' })

  n.resolve = () => ({ dismiss: true }) // handed back to the TUI
  ask('per_4')
  await until('asked', () => n.asked.length === 4)
  await new Promise((r) => setTimeout(r, 100))
  assert.equal(ctx.calls.filter((c) => c[0] === 'permission.reply').length, 3, 'nothing answered for a dismissed card')
})

test('a permission asked by a subagent is the root session\'s; while it waits the row says "needs you"; the TUI answering clears it', async (t) => {
  const d = fakeDb()
  d.session('ses_root')
  d.user('ses_root', 4, 'go')
  d.session('ses_sub', { parent: 'ses_root' })
  const { n, host } = await setup(t, { db: d, answerFirst: 'local' })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 'ses_root'))
  ctx.emit('permission.asked', { id: 'per_9', sessionID: 'ses_sub', action: 'edit', resources: ['/w/a.js'] })
  await until('blocked', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_root')?.status === 'blocked')
  assert.equal(n.asked.length, 0, 'the TUI is first: no card')
  assert.equal(n.last('runtime.sessions').sessions.some((s) => s.session_id === 'ses_sub'), false)
  ctx.emit('permission.replied', { sessionID: 'ses_sub', requestID: 'per_9', reply: 'once' })
  await until('clear', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 'ses_root')?.status !== 'blocked')
})

test('a session opencode does not have is not_found, so the node does not take another runtime\'s session for this one', async (t) => {
  const d = fakeDb()
  d.session('ses_a')
  d.user('ses_a', 4, 'hello')
  const { n, host } = await setup(t, { db: d })
  host.register(fakeCtx())
  await n.ready
  // The node asks every connected runtime session.items {limit: 1} about a session nobody listed, and gives it to
  // the first that answers (runtimeFor, handOver): an empty page here would take a DSH or pi session for opencode.
  await assert.rejects(n.peer.call('session.items', { session_id: 'session-0b1c', limit: 1 }), (err) => err.code === 'not_found')
  const p = await n.peer.call('session.items', { session_id: 'ses_a', limit: 1 })
  assert.deepEqual(p.items.map((i) => i.id), ['4#0'])
})

test('without the database the runtime still answers: an empty list, no pages, sends and the rest through the plugin', async (t) => {
  const n = await startFakeNode(t)
  const host = createOpencodeHost({ openDb: async () => { throw new Error('no such file') }, pid: 4243 }, { socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => host.stop())
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await until('empty list', () => n.last('runtime.sessions')?.sessions.length === 0)
  const p = await n.peer.call('session.items', { session_id: 'ses_none', turns: 1 })
  assert.deepEqual(p.items, [])
})

test('a deleted session: its card leaves the phone, its polling stops, and it is not brought back', async (t) => {
  const d = fakeDb()
  d.session('ses_del')
  d.user('ses_del', 4, 'go')
  const { n, host } = await setup(t, { db: d, answerFirst: 'phone' })
  const ctx = fakeCtx()
  host.register(ctx)
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 'ses_del'))
  await n.peer.call('session.watch', { session_id: 'ses_del', since_seq: 0 })
  n.resolve = () => new Promise(() => {}) // the phone does not answer
  ctx.emit('session.execution.started', { sessionID: 'ses_del' })
  ctx.emit('permission.asked', { id: 'per_d', sessionID: 'ses_del', action: 'shell', resources: ['ls'] })
  await until('the card', () => n.asked.length === 1)
  assert.equal(host.runtime.busy(), true)

  ctx.emit('session.deleted', { sessionID: 'ses_del' })
  await until('the card withdrawn', () => n.all('interaction.cancel').some((c) => c.id === n.asked[0].id))
  assert.equal(host.runtime.busy(), false)
  await new Promise((r) => setTimeout(r, 2500)) // past a poll
  assert.equal(host.sessions.has('ses_del'), false, 'not brought back by its polling')
})

test('hello advertises launcher, models, start, and assistant without supervision_policy; launchers.models normalizes catalog', async (t) => {
  const n = await startFakeNode(t)
  const d = fakeDb()
  const host = createOpencodeHost(
    { openDb: d.openDb, pid: 4250, home: '/home/u' },
    { socketPath: n.path, retryMs: 20, pushDelayMs: 5, model: 'opencode-go/claude-sonnet-4-6' },
  )
  t.after(() => host.stop())
  const ctx = fakeCtx('/w/app')
  ctx.model.list = async () => [
    { id: 'claude-sonnet-4-6', providerID: 'opencode-go', name: 'Claude Sonnet 4.6', limit: { context: 200000 }, variants: ['low', 'high'] },
    { id: 'gpt-5', providerID: 'openai', name: 'GPT-5', limit: { context: 128000 } },
  ]
  host.register(ctx)
  await n.ready

  assert.equal(n.hello.launcher, 'opencode')
  assert.equal(n.hello.caps.start, true)
  assert.equal(n.hello.caps.models, true)
  assert.equal(n.hello.caps.assistant, true)
  assert.equal(n.hello.caps.model, true)
  assert.ok(!n.hello.caps.supervision_policy, 'must not declare supervision_policy (#11220)')

  const catalogRes = await n.peer.call('launchers.models', { refresh: true })
  assert.equal(catalogRes.agent, 'opencode')
  assert.deepEqual(catalogRes.models.map((m) => m.id), ['opencode-go/claude-sonnet-4-6', 'openai/gpt-5'])
  assert.equal(catalogRes.models[0].default, true)
  assert.deepEqual(catalogRes.models[0].efforts, ['low', 'high'])
  await until('launchers updated', () => n.last('runtime.launchers')?.launchers?.[0]?.models?.length === 2)
})

test('session.start creates an Agent session with model, prompt, images, and modes switching; validates errors', async (t) => {
  const n = await startFakeNode(t)
  const d = fakeDb()
  const host = createOpencodeHost({ openDb: d.openDb, pid: 4251, home: '/home/u' }, { socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => host.stop())

  const ctx = fakeCtx('/w/app')
  ctx.model.list = async () => [
    { id: 'claude-sonnet-4-6', providerID: 'opencode-go', name: 'Claude Sonnet 4.6', limit: { context: 200000 } },
    { id: 'gpt-5', providerID: 'openai', name: 'GPT-5', limit: { context: 128000 } },
  ]
  ctx.session.create = async (args) => {
    ctx.calls.push(['session.create', args])
    return { id: 'ses_created_1', title: 'New session', model: args.model }
  }
  ctx.session.switchModel = async (args) => {
    ctx.calls.push(['session.switchModel', args])
    return {}
  }
  host.register(ctx)
  await n.ready

  await assert.rejects(n.peer.call('session.start', { cwd: '' }), (err) => err.code === 'bad_request')
  await assert.rejects(
    n.peer.call('session.start', { cwd: '/w/app', supervision_instructions: 'secret supervisor rule' }),
    (err) => err.code === 'assistant_supervision_runtime',
  )

  const started = await n.peer.call('session.start', {
    cwd: '/w/app',
    model: 'opencode-go/claude-sonnet-4-6',
    prompt: 'check the repo',
    images: [{ mime: 'image/png', data: 'QUFB' }],
    client_msg_id: 'cm_start_1',
  })
  assert.equal(started.session.session_id, 'ses_created_1')
  assert.equal(started.session.started_by_persona, true)
  assert.equal(started.session.launcher, 'opencode')
  assert.equal(started.session.model, 'opencode-go/claude-sonnet-4-6')
  assert.equal(started.session.status, 'working')

  assert.deepEqual(ctx.calls[0], ['session.create', {
    location: { directory: '/w/app' },
    model: { providerID: 'opencode-go', id: 'claude-sonnet-4-6' },
  }])
  assert.equal(ctx.calls[1][0], 'session.prompt')
  assert.equal(ctx.calls[1][1].sessionID, 'ses_created_1')
  assert.equal(ctx.calls[1][1].text, 'check the repo')
  assert.deepEqual(ctx.calls[1][1].files, [{ uri: 'data:image/png;base64,QUFB', name: 'image-1.png' }])

  // Session appears in runtime.sessions even before DB flush.
  await until('started listed', () => n.last('runtime.sessions')?.sessions?.some((s) => s.session_id === 'ses_created_1'))

  // Switch model via session.modes.
  await n.peer.call('session.modes', { session_id: 'ses_created_1', model: 'openai/gpt-5' })
  assert.deepEqual(ctx.calls.at(-1), ['session.switchModel', {
    sessionID: 'ses_created_1',
    model: { providerID: 'openai', id: 'gpt-5' },
  }])

  // Missing session.create fails cleanly with unsupported.
  const ctxNoCreate = fakeCtx('/w/other')
  host.register(ctxNoCreate)
  await assert.rejects(
    n.peer.call('session.start', { cwd: '/w/other', prompt: 'hi' }),
    (err) => err.code === 'unsupported',
  )
})

test('assistant mode registers loopback MCP, hides context snapshots, enforces memory_dir, plan, command gates, and rejects supervision_instructions', async (t) => {
  const n = await startFakeNode(t, {
    tools: [
      { name: 'todo_list', description: 'List todos', input_schema: { type: 'object', properties: {} } },
      { name: 'mail_send', description: 'Send mail', input_schema: { type: 'object', properties: {} }, shell: ['send-mail'] },
    ],
  })
  const d = fakeDb()
  const openDb = d.openDb
  let inventoryUnavailable = false
  d.openDb = async () => {
    const handle = await openDb()
    return { ...handle, all(sql, ...params) {
      if (inventoryUnavailable && sql.startsWith('select id, directory')) throw new Error('inventory unavailable')
      return handle.all(sql, ...params)
    } }
  }
  const host = createOpencodeHost(
    { openDb: d.openDb, pid: 4252, home: '/home/u' },
    { socketPath: n.path, retryMs: 20, pushDelayMs: 5, answerFirst: 'local' },
  )
  t.after(() => host.stop())

  const mcpAdded = []
  const ctx = fakeCtx('/home/u/.persona/assistant')
  ctx.session.create = async (args) => {
    ctx.calls.push(['session.create', args])
    return { id: 'ses_asst_v2', title: '' }
  }
  ctx.mcp = {
    add: async (args) => {
      mcpAdded.push(args)
      return {}
    },
  }
  host.register(ctx)
  await n.ready

  const started = await n.peer.call('session.start', {
    cwd: '/home/u/.persona/assistant',
    assistant: true,
    mode: 'default',
  })
  assert.equal(started.session.session_id, 'ses_asst_v2')
  assert.equal(mcpAdded.length, 1)
  assert.equal(mcpAdded[0].name, 'persona')
  assert.equal(mcpAdded[0].config.type, 'remote')

  // Verify the registered loopback MCP endpoint serves assistant tools and forwards tool calls to fake node.
  const toolsRes = await fetch(mcpAdded[0].config.url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...mcpAdded[0].config.headers },
    body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list', params: {} }),
  }).then((r) => r.json())
  assert.deepEqual(toolsRes.result.tools.map((x) => x.name), ['todo_list', 'mail_send'])

  const callRes = await fetch(mcpAdded[0].config.url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...mcpAdded[0].config.headers },
    body: JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'todo_list', arguments: {} } }),
  }).then((r) => r.json())
  assert.equal(callRes.result.content[0].text, 'todo_list ran')
  assert.equal(n.toolCalls[0].assistant, true)
  assert.equal(n.toolCalls[0].session_id, 'ses_asst_v2')

  const callAssistantTool = async () => fetch(mcpAdded[0].config.url, {
    method: 'POST', headers: { 'Content-Type': 'application/json', ...mcpAdded[0].config.headers },
    body: JSON.stringify({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'todo_list', arguments: {} } }),
  }).then((response) => response.json())
  // A native/TUI conversation unknown to Persona can use the same directory
  // server too. It must not inherit the sole registered Assistant's identity.
  d.session('ses_native_other', { directory: '/home/u/.persona/assistant' })
  assert.equal((await callAssistantTool()).result.isError, true)
  d.db.prepare('delete from session_v2 where id = ?').run('ses_native_other')
  inventoryUnavailable = true
  assert.equal((await callAssistantTool()).result.isError, true)
  assert.equal(n.toolCalls.length, 1, 'ambiguous or unavailable inventory executes no hub tools')
  inventoryUnavailable = false

  // Send with context snapshot + memory_dir; reject supervision_instructions.
  await assert.rejects(
    n.peer.call('session.send', { session_id: 'ses_asst_v2', text: 'hi', assistant: true, supervision_instructions: 'do not inject' }),
    (err) => err.code === 'assistant_supervision_runtime',
  )
  await n.peer.call('session.send', {
    session_id: 'ses_asst_v2',
    text: '查下我的待办',
    context: '当前时间：2026-11-20',
    assistant: true,
    memory_dir: '/home/u/.persona/assistant/memory',
    client_msg_id: 'cm_a1',
  })
  const sentPrompt = ctx.calls.find((c) => c[0] === 'session.prompt')[1].text
  assert.match(sentPrompt, /^<persona-context>\n当前时间：2026-11-20\n<\/persona-context>\n\n查下我的待办$/)

  // When persisted in DB, the attribute-less snapshot is stripped from session.items and title.
  d.session('ses_asst_v2', { directory: '/home/u/.persona/assistant', title: null })
  d.user('ses_asst_v2', 1, sentPrompt)
  const itemsPage = await n.peer.call('session.items', { session_id: 'ses_asst_v2', limit: 10 })
  assert.equal(itemsPage.items[0].text, '查下我的待办')

  // Memory write inside memory_dir auto-allows without asking phone.
  ctx.emit('permission.asked', { id: 'p_mem', sessionID: 'ses_asst_v2', action: 'write', resources: ['/home/u/.persona/assistant/memory/notes.md'] })
  await until('memory auto-allowed', () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === 'p_mem'))
  assert.equal(ctx.calls.at(-1)[1].decision, 'once')
  assert.equal(n.asked.length, 0)

  // Command gate is rejected even under bypassPermissions.
  await n.peer.call('session.modes', { session_id: 'ses_asst_v2', permission_mode: 'bypassPermissions' })
  ctx.emit('permission.asked', { id: 'p_gate', sessionID: 'ses_asst_v2', action: 'shell', resources: ['send-mail --to bob'] })
  await until('gate rejected', () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === 'p_gate'))
  assert.equal(ctx.calls.at(-1)[1].decision, 'reject')

  // Normal shell under bypassPermissions auto-allows.
  ctx.emit('permission.asked', { id: 'p_ok', sessionID: 'ses_asst_v2', action: 'shell', resources: ['ls -la'] })
  await until('bypass allowed', () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === 'p_ok'))
  assert.equal(ctx.calls.at(-1)[1].decision, 'once')

  // Plan mode rejects writes/shell.
  await n.peer.call('session.modes', { session_id: 'ses_asst_v2', permission_mode: 'plan' })
  ctx.emit('permission.asked', { id: 'p_plan', sessionID: 'ses_asst_v2', action: 'edit', resources: ['/home/u/.persona/assistant/memory/notes.md'] })
  await until('plan rejected', () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === 'p_plan'))
  assert.equal(ctx.calls.at(-1)[1].decision, 'reject')

  // Back to default mode: assistant permissions go to phone even when answerFirst is 'local'.
  await n.peer.call('session.modes', { session_id: 'ses_asst_v2', permission_mode: 'default' })
  n.resolve = () => ({ allow: true })
  ctx.emit('permission.asked', { id: 'p_phone', sessionID: 'ses_asst_v2', action: 'shell', resources: ['uname -a'] })
  await until('phone asked', () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === 'p_phone'))
  assert.equal(n.asked.length, 1)
  assert.equal(ctx.calls.at(-1)[1].decision, 'once')
})

test('plugin refuses ambiguous assistant ownership and never retargets a cached MCP token; permissions and modes stay enforced', async (t) => {
  const n = await startFakeNode(t, {
    tools: [
      { name: 'todo_list', description: 'List todos', input_schema: { type: 'object', properties: {} } },
      { name: 'mail_send', description: 'Send mail', input_schema: { type: 'object', properties: {} }, shell: ['send-mail'] },
    ],
  })
  const d = fakeDb()
  const host = createOpencodeHost(
    { openDb: d.openDb, pid: 4253, home: '/home/u' },
    { socketPath: n.path, retryMs: 20, pushDelayMs: 5, answerFirst: 'local' },
  )
  t.after(() => host.stop())

  const mcpAdded = []
  let createSeq = 0
  const ctx = fakeCtx('/home/u/.persona/assistant')
  ctx.model = {
    list: async () => [
      { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', variants: { low: {}, high: {} } },
    ],
  }
  ctx.session.create = async (args) => {
    ctx.calls.push(['session.create', args])
    createSeq++
    return { id: `ses_asst_multi_${createSeq}`, title: '' }
  }
  ctx.session.switchModel = async (args) => {
    ctx.calls.push(['session.switchModel', args])
  }
  ctx.mcp = {
    add: async (args) => {
      mcpAdded.push(args)
      return {}
    },
  }
  host.register(ctx)
  await n.ready
  assert.equal(n.hello.caps.readonly, true)

  // Start two Assistant chats sharing ~/.persona/assistant.
  const s1 = await n.peer.call('session.start', {
    cwd: '/home/u/.persona/assistant',
    assistant: true,
    model: 'opencode-go/claude-sonnet-4-6',
  })
  const s2 = await n.peer.call('session.start', {
    cwd: '/home/u/.persona/assistant',
    assistant: true,
    model: 'opencode-go/claude-sonnet-4-6',
  })
  assert.equal(s1.session.session_id, 'ses_asst_multi_1')
  assert.equal(s2.session.session_id, 'ses_asst_multi_2')
  assert.equal(mcpAdded.length, 1, 'directory route is wired once')

  // A directory-scoped token cannot distinguish the two native sessions.
  const call1 = await fetch(mcpAdded[0].config.url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...mcpAdded[0].config.headers },
    body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'todo_list', arguments: {} } }),
  }).then((r) => r.json())
  assert.equal(call1.result.isError, true)
  assert.match(call1.result.content[0].text, /ambiguous/)
  assert.equal(n.toolCalls.length, 0)

  // A new message does not establish the identity of a later MCP call.
  await n.peer.call('session.send', {
    session_id: 'ses_asst_multi_1',
    text: 'back to s1',
    assistant: true,
    memory_dir: '/home/u/.persona/assistant/memory',
    client_msg_id: 'cm_m1',
  })
  await fetch(mcpAdded[0].config.url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...mcpAdded[0].config.headers },
    body: JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'todo_list', arguments: {} } }),
  }).then((r) => r.json())
  assert.equal(n.toolCalls.length, 0)

  // Relative AGENTS.md resolves against cwd (~/.persona/assistant), NOT memory_dir, so it must NOT auto-allow!
  n.resolve = () => ({ allow: false, message: 'denied' })
  ctx.emit('permission.asked', { id: 'p_rel_agents', sessionID: 'ses_asst_multi_1', action: 'write', resources: ['AGENTS.md'] })
  await until('relative AGENTS.md asked phone', () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === 'p_rel_agents'))
  assert.equal(n.asked.length, 1, 'relative AGENTS.md must not auto-allow as memory write')
  assert.equal(ctx.calls.at(-1)[1].decision, 'reject')

  // Shell syntax bypass (`send-mail; echo ok` and `"send-mail"`) rejected even under bypassPermissions.
  await n.peer.call('session.modes', { session_id: 'ses_asst_multi_1', permission_mode: 'bypassPermissions' })
  ctx.emit('permission.asked', { id: 'p_gate_semi', sessionID: 'ses_asst_multi_1', action: 'shell', resources: ['send-mail; echo ok'] })
  await until('semicolon gate rejected', () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === 'p_gate_semi'))
  assert.equal(ctx.calls.at(-1)[1].decision, 'reject')

  // acceptEdits auto-allows write/edit without asking phone.
  await n.peer.call('session.modes', { session_id: 'ses_asst_multi_1', permission_mode: 'acceptEdits' })
  ctx.emit('permission.asked', { id: 'p_accept_edit', sessionID: 'ses_asst_multi_1', action: 'edit', resources: ['/w/foo.js'] })
  await until('acceptEdits auto-allowed', () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === 'p_accept_edit'))
  assert.equal(ctx.calls.at(-1)[1].decision, 'once')
  assert.equal(n.asked.length, 1)

  // Effort-only modes change calls switchModel with the current session model and variant.
  await n.peer.call('session.modes', { session_id: 'ses_asst_multi_1', effort: 'high' })
  const sw = ctx.calls.filter((c) => c[0] === 'session.switchModel').at(-1)
  assert.deepEqual(sw[1].model, { providerID: 'opencode-go', id: 'claude-sonnet-4-6', variant: 'high' })

  // Effort-only modes on a DB-only/restarted session (no in-memory s.model) resolves model from DB and calls switchModel.
  d.session('ses_db_only_effort', {
    directory: '/w/app',
    model: JSON.stringify({ providerID: 'opencode-go', id: 'claude-sonnet-4-6' }),
  })
  await n.peer.call('session.modes', { session_id: 'ses_db_only_effort', effort: 'low' })
  const swDb = ctx.calls.filter((c) => c[0] === 'session.switchModel').at(-1)
  assert.equal(swDb[1].sessionID, 'ses_db_only_effort')
  assert.deepEqual(swDb[1].model, { providerID: 'opencode-go', id: 'claude-sonnet-4-6', variant: 'low' })

  // Effort-only modes on a session with no model in memory or DB fails closed with bad_request (never silently records effort).
  d.session('ses_no_model_effort', { directory: '/w/app', model: null })
  await assert.rejects(
    n.peer.call('session.modes', { session_id: 'ses_no_model_effort', effort: 'high' }),
    (err) => err.code === 'bad_request',
  )
  assert.equal(host.sessions.get('ses_no_model_effort')?.effort, undefined)

  // Forgetting the owner revokes its old URL; it never gains the sibling's identity.
  ctx.emit('session.deleted', { sessionID: 'ses_asst_multi_1' })
  await until('s1 forgotten', () => !host.assistants.has('ses_asst_multi_1'))
  await until('old owner token revoked', async () => (await fetch(mcpAdded[0].config.url, {
    method: 'POST', headers: { 'Content-Type': 'application/json', ...mcpAdded[0].config.headers },
    body: JSON.stringify({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'todo_list', arguments: {} } }),
  })).status === 404)
  assert.equal(n.toolCalls.length, 0)
  await n.peer.call('session.send', { session_id: 'ses_asst_multi_2', text: 'continue', assistant: true, client_msg_id: 'new-route' })
  assert.equal(mcpAdded.length, 2)
  const siblingCall = await fetch(mcpAdded[1].config.url, {
    method: 'POST', headers: { 'Content-Type': 'application/json', ...mcpAdded[1].config.headers },
    body: JSON.stringify({ jsonrpc: '2.0', id: 4, method: 'tools/call', params: { name: 'todo_list', arguments: {} } }),
  }).then((r) => r.json())
  assert.equal(siblingCall.result.isError, false)
  assert.equal(n.toolCalls.at(-1).session_id, 'ses_asst_multi_2')

  // Deleting the final surviving assistant session releases the MCP route (404).
  ctx.emit('session.deleted', { sessionID: 'ses_asst_multi_2' })
  await until('mcp route released', async () => {
    const r = await fetch(mcpAdded[1].config.url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...mcpAdded[1].config.headers },
      body: JSON.stringify({ jsonrpc: '2.0', id: 4, method: 'ping' }),
    })
    return r.status === 404
  })
})

test('plugin assistant isMemoryWrite blocks multi-resource bypass, symlink escapes, namei divergence, and missing cwd fallback', async (t) => {
  const base = mkdtempSync(join(tmpdir(), 'persona-oc-plug-mem-'))
  t.after(() => rmSync(base, { recursive: true, force: true }))
  const asstDir = join(base, 'assistant')
  const memDir = join(asstDir, 'memory')
  const nestedMem = join(memDir, 'nested', 'deep')
  const outsideDir = join(base, 'outside')
  const outsideSub = join(outsideDir, 'sub')
  mkdirSync(nestedMem, { recursive: true })
  mkdirSync(outsideSub, { recursive: true })
  writeFileSync(join(outsideDir, 'secret.txt'), 'secret')
  const escapeFileLink = join(memDir, 'escape-file.md')
  const danglingLink = join(memDir, 'dangling.md')
  const escapeDirLink = join(memDir, 'out-dir')
  const deepLink = join(memDir, 'deep-link')
  const loopA = join(memDir, 'loop-a')
  const loopB = join(memDir, 'loop-b')
  symlinkSync(join(outsideDir, 'secret.txt'), escapeFileLink)
  symlinkSync(join(outsideDir, 'new-evil.txt'), danglingLink)
  symlinkSync(outsideSub, escapeDirLink)
  symlinkSync(nestedMem, deepLink)
  symlinkSync(loopB, loopA)
  symlinkSync(loopA, loopB)

  const n = await startFakeNode(t, { tools: [] })
  n.resolve = () => ({ allow: false })
  const d = fakeDb()
  const host = createOpencodeHost(
    { openDb: d.openDb, pid: 4254 },
    { socketPath: n.path, retryMs: 20, pushDelayMs: 5, answerFirst: 'local', home: base },
  )
  t.after(() => host.stop())

  const ctx = fakeCtx(asstDir)
  ctx.session.create = async () => ({ id: 'ses_mem_sec', title: '' })
  ctx.mcp = { add: async () => ({}) }
  host.register(ctx)
  await n.ready

  await n.peer.call('session.start', { cwd: asstDir, assistant: true })
  await n.peer.call('session.send', {
    session_id: 'ses_mem_sec',
    text: 'memory security check',
    assistant: true,
    mode: 'default',
    memory_dir: memDir,
    client_msg_id: 'cm_plug_mem_sec',
  })

  const askAndExpect = async (id, resources, wantDecision, wantAskedDelta, extra = {}) => {
    const before = n.asked.length
    ctx.emit('permission.asked', { id, sessionID: 'ses_mem_sec', action: 'write', resources, ...extra })
    await until(`reply ${id}`, () => ctx.calls.some((c) => c[0] === 'permission.reply' && c[1].requestID === id))
    const reply = ctx.calls.find((c) => c[0] === 'permission.reply' && c[1].requestID === id)[1]
    assert.equal(reply.decision, wantDecision, `${id}: expected ${wantDecision}`)
    assert.equal(n.asked.length, before + wantAskedDelta, `${id}: unexpected phone ask delta`)
  }

  const sparseResources = [join(memDir, 'ok.md')]
  sparseResources.length = 2
  for (const [id, resources, extra] of [
    ['multi_escape', [join(memDir, 'ok.md'), join(outsideDir, 'secret.txt')]],
    ['multi_empty', [join(memDir, 'ok.md'), '']],
    ['multi_null', [join(memDir, 'ok.md'), null]],
    ['sparse_res', sparseResources],
    ['empty_arr', []],
    ['sym_file', [escapeFileLink]],
    ['sym_dangling', [danglingLink]],
    ['sym_dir', [join(escapeDirLink, 'pwn.txt')]],
    ['namei_dotdot', [`${escapeDirLink}/../secret.txt`]],
    ['lex_dotdot', [`${deepLink}/../../SOUL.md`]],
    ['sym_loop', [loopA]],
    ['tilde', ['~/SOUL.md']],
    ['nul', [`${join(memDir, 'ok.md')}\0`]],
    ['mem_self', [memDir]],
    ['meta_filepath_out', [join(memDir, 'ok.md')], { metadata: { filepath: join(outsideDir, 'secret.txt') } }],
    ['meta_diff', [join(memDir, 'ok.md')], { metadata: { diff: '--- a/etc/hosts' } }],
    ['meta_non_obj', [join(memDir, 'ok.md')], { metadata: 'bad' }],
    ['patterns_out', [join(memDir, 'ok.md')], { patterns: [join(outsideDir, 'secret.txt')] }],
    ['files_out', [join(memDir, 'ok.md')], { files: [join(outsideDir, 'secret.txt')] }],
  ]) {
    await askAndExpect(id, resources, 'reject', 1, extra)
  }

  // Empty cwd must NOT fall back to memoryDir for relative paths.
  const asst = host.assistants.get('ses_mem_sec')
  const savedCwd = asst.cwd
  asst.cwd = ''
  try {
    await askAndExpect('empty_cwd', ['AGENTS.md'], 'reject', 1)
  } finally {
    asst.cwd = savedCwd
  }

  // Root filesystem memoryDir must fail closed.
  const savedMem = asst.memoryDir
  asst.memoryDir = '/'
  try {
    await askAndExpect('root_mem', ['/etc/hosts'], 'reject', 1)
  } finally {
    asst.memoryDir = savedMem
  }

  // Legitimate writes inside memoryDir still auto-allow without asking phone.
  for (const [id, resources, extra] of [
    ['ok_single', [join(memDir, 'notes.md')]],
    ['ok_multi', [join(memDir, 'a.md'), join(memDir, 'nested', 'b.md')]],
    ['ok_rel', ['memory/sub/new.md']],
    ['ok_dotdot_name', [join(memDir, '..note.md')]],
    ['ok_meta_patterns', [join(memDir, 'notes.md')], { patterns: [join(memDir, 'notes.md')], metadata: { filepath: join(memDir, 'notes.md') } }],
  ]) {
    await askAndExpect(id, resources, 'once', 0, extra)
  }
})
