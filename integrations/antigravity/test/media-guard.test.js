import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFile, execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { decide, inspectImage, installMediaGuard } from '../lib/media-guard.js'

const SCRIPT = fileURLToPath(new URL('../media-guard.mjs', import.meta.url))
// A baseline JPEG: SOI, a quantization table, a frame header, SOS, two bytes of image data, EOI.
const segment = (marker, body) => Buffer.concat([Buffer.from([0xff, marker]), Buffer.from([0, body.length + 2]), body])
const jpeg = Buffer.concat([Buffer.from([0xff, 0xd8]), segment(0xdb, Buffer.alloc(65)), segment(0xc0, Buffer.alloc(9)),
  segment(0xda, Buffer.alloc(8)), Buffer.from([0x12, 0xff, 0x00, 0x34]), Buffer.from([0xff, 0xd9])])
const chunk = (type, data = Buffer.alloc(0)) => Buffer.concat([Buffer.from([0, 0, 0, data.length]), Buffer.from(type), data, Buffer.alloc(4)])
const png = Buffer.concat([Buffer.from('89504e470d0a1a0a', 'hex'), chunk('IHDR', Buffer.alloc(13)), chunk('IDAT', Buffer.alloc(6)), chunk('IEND')])

test('complete pictures and other files pass; cut or damaged pictures are named', () => {
  assert.deepEqual(inspectImage(jpeg), { ok: true, mime: 'image/jpeg' })
  assert.equal(inspectImage(Buffer.concat([jpeg, Buffer.from('vendor trailer')])).ok, true)
  assert.deepEqual(inspectImage(jpeg.subarray(0, jpeg.length - 3)), { ok: false, mime: 'image/jpeg', reason: 'truncated', scan: true })
  assert.deepEqual(inspectImage(jpeg.subarray(0, 40)), { ok: false, mime: 'image/jpeg', reason: 'truncated' })
  assert.deepEqual(inspectImage(png), { ok: true, mime: 'image/png' })
  assert.deepEqual(inspectImage(png.subarray(0, png.length - 12)), { ok: false, mime: 'image/png', reason: 'truncated' })
  assert.equal(inspectImage(Buffer.from('GIF89a......;')).ok, true)
  assert.equal(inspectImage(Buffer.from('GIF89a......')).ok, false)
  const webp = Buffer.concat([Buffer.from('RIFF'), Buffer.from([12, 0, 0, 0]), Buffer.from('WEBPVP8 '), Buffer.alloc(4)])
  assert.equal(inspectImage(webp).ok, true)
  assert.equal(inspectImage(webp.subarray(0, 14)).ok, false)
  assert.deepEqual(inspectImage(Buffer.from('plain text')), { ok: true })
})

test('read_file of a truncated JPEG is refused with a reason and a readable copy; the file is unchanged', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const cut = jpeg.subarray(0, jpeg.length - 2)
  const file = join(dir, 'shot.jpg')
  writeFileSync(file, cut)
  const state = join(dir, 'state')
  const answer = decide({ toolCall: { name: 'read_file', args: { Path: file } } }, state)
  assert.equal(answer.decision, 'deny')
  assert.match(answer.reason, /cut short \(truncated\)/)
  assert.match(answer.reason, /kept unchanged/)
  const copy = /readable as a separate copy: (\S+)$/.exec(answer.reason)?.[1]
  assert.ok(copy && copy.startsWith(state))
  assert.deepEqual(readFileSync(copy), Buffer.concat([cut, Buffer.from([0xff, 0xd9])]))
  assert.deepEqual(inspectImage(readFileSync(copy)), { ok: true, mime: 'image/jpeg' })
  assert.deepEqual(decide({ toolCall: { name: 'read_file', args: { Path: copy } } }, state), { decision: 'ask' })
  assert.deepEqual(readFileSync(file), cut)
  // A cut header has no image data to keep: refused without a copy.
  writeFileSync(file, jpeg.subarray(0, 40))
  const header = decide({ toolCall: { args: { Path: file } } }, state)
  assert.equal(header.decision, 'deny')
  assert.doesNotMatch(header.reason, /separate copy/)
})

test('truncated JPEG is still denied when the optional partial copy cannot be written', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const cut = jpeg.subarray(0, jpeg.length - 2)
  const file = join(dir, 'shot.jpg')
  writeFileSync(file, cut)
  // A regular file where the state directory would live makes mkdirSync fail with EEXIST/ENOTDIR on every OS.
  const blockedState = join(dir, 'not-a-dir')
  writeFileSync(blockedState, 'blocker')
  const answer = decide({ toolCall: { name: 'read_file', args: { Path: file } } }, blockedState)
  assert.equal(answer.decision, 'deny')
  assert.match(answer.reason, /cut short \(truncated\)/)
  assert.doesNotMatch(answer.reason, /separate copy/)
  assert.deepEqual(readFileSync(file), cut)
})

test('parallel media-guard hooks on the same truncated JPEG all deny without temp-file races', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const cut = jpeg.subarray(0, jpeg.length - 2)
  const file = join(dir, 'shot.jpg')
  writeFileSync(file, cut)
  const state = join(dir, 'state')
  const payload = JSON.stringify({ toolCall: { name: 'read_file', args: { Path: file } } })
  const runAsync = () => new Promise((resolve, reject) => {
    const child = execFile(process.execPath, [SCRIPT, '--state', state], (err, stdout) => {
      if (err) reject(err)
      else resolve(JSON.parse(stdout.toString()))
    })
    child.stdin.end(payload)
  })
  const answers = await Promise.all(Array.from({ length: 6 }, runAsync))
  for (const answer of answers) {
    assert.equal(answer.decision, 'deny')
    assert.match(answer.reason, /cut short \(truncated\)/)
  }
  const files = readdirSync(state)
  assert.equal(files.filter((name) => name.endsWith('.tmp')).length, 0)
  assert.equal(files.length, 1)
  assert.ok(files[0].endsWith('.partial.jpg'))
  assert.deepEqual(readFileSync(join(state, files[0])), Buffer.concat([cut, Buffer.from([0xff, 0xd9])]))
})

test('truncated JPEG stays denied for non-string state, missing --state value, directory collision, and self-heals bad partial copies', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const cut = jpeg.subarray(0, jpeg.length - 2)
  const wantCopy = Buffer.concat([cut, Buffer.from([0xff, 0xd9])])
  const file = join(dir, 'shot.jpg')
  writeFileSync(file, cut)
  const payload = { toolCall: { name: 'read_file', args: { Path: file } } }
  const copyName = `${createHash('sha256').update(cut).digest('hex').slice(0, 32)}.partial.jpg`

  // (a) Non-string truthy state (as parseArgs({ strict: false }) yields for bare `--state`) must deny, never fail open.
  const boolState = decide(payload, true)
  assert.equal(boolState.decision, 'deny')
  assert.doesNotMatch(boolState.reason, /separate copy/)
  const cliBareFlag = JSON.parse(execFileSync(process.execPath, [SCRIPT, '--state'], {
    input: JSON.stringify(payload),
  }).toString())
  assert.equal(cliBareFlag.decision, 'deny')
  assert.doesNotMatch(cliBareFlag.reason, /separate copy/)

  // (b) Pre-existing directory at the partial copy path must deny without claiming a readable copy or leaving .tmp files.
  const dirState = join(dir, 'dir-collision')
  mkdirSync(join(dirState, copyName), { recursive: true })
  const dirCollision = decide(payload, dirState)
  assert.equal(dirCollision.decision, 'deny')
  assert.doesNotMatch(dirCollision.reason, /separate copy/)
  assert.equal(readdirSync(dirState).filter((name) => name.endsWith('.tmp')).length, 0)

  // (c) Pre-existing 0-byte or truncated partial copy is self-healed to valid cut + EOI bytes.
  const healState = join(dir, 'heal-state')
  mkdirSync(healState, { recursive: true })
  const copyPath = join(healState, copyName)
  for (const stale of [Buffer.alloc(0), cut.subarray(0, 10)]) {
    writeFileSync(copyPath, stale)
    const healed = decide(payload, healState)
    assert.equal(healed.decision, 'deny')
    assert.equal(/readable as a separate copy: (\S+)$/.exec(healed.reason)?.[1], copyPath)
    assert.deepEqual(readFileSync(copyPath), wantCopy)
    assert.equal(readdirSync(healState).filter((name) => name.endsWith('.tmp')).length, 0)
  }
})

test('everything else gets no opinion, and the script never fails closed', (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  writeFileSync(join(dir, 'ok.jpg'), jpeg)
  for (const payload of [undefined, {}, { toolCall: { args: {} } }, { toolCall: { args: { Path: join(dir, 'missing.jpg') } } },
    { toolCall: { args: { Path: join(dir, 'ok.jpg') } } }, { toolCall: { args: { Path: dir } } }]) {
    assert.deepEqual(decide(payload, join(dir, 'state')), { decision: 'ask' })
  }
  const run = (input) => JSON.parse(execFileSync(process.execPath, [SCRIPT, '--state', join(dir, 'state')], { input }).toString())
  assert.deepEqual(run('not json'), { decision: 'ask' })
  writeFileSync(join(dir, 'cut.png'), png.subarray(0, 20))
  assert.equal(run(JSON.stringify({ toolCall: { name: 'read_file', args: { Path: join(dir, 'cut.png') } } })).decision, 'deny')
})

test('the plugin is idempotent, outlives its source generation and asks only if its installed script is gone', { skip: process.platform === 'win32' && 'no /bin/sh' }, (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const agyDir = join(home, 'antigravity-cli')
  mkdirSync(agyDir)
  mkdirSync(join(home, 'config'))
  writeFileSync(join(home, 'config', 'package.json'), '{"type":"commonjs"}\n')
  const sourceRoot = join(home, 'source-generation')
  const source = join(sourceRoot, 'media-guard.mjs')
  mkdirSync(join(sourceRoot, 'lib'), { recursive: true })
  writeFileSync(source, readFileSync(SCRIPT))
  writeFileSync(join(sourceRoot, 'lib', 'media-guard.js'), readFileSync(new URL('../lib/media-guard.js', import.meta.url)))
  const dir = installMediaGuard({ agyDir, script: source })
  assert.equal(dir, join(home, 'config', 'plugins', 'persona-media-guard'))
  assert.equal(JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8')).type, 'module', 'the installed .js helper has an explicit ESM scope even under a CommonJS parent')
  const hooks = JSON.parse(readFileSync(join(dir, 'hooks.json'), 'utf8'))['persona-media-guard'].PreToolUse[0]
  assert.equal(hooks.matcher, 'read_file|view_file')
  const sh = (command, input) => JSON.parse(execFileSync('/bin/sh', ['-c', command], { input }).toString())
  writeFileSync(join(home, 'cut.png'), png.subarray(0, 20))
  const payload = JSON.stringify({ toolCall: { name: 'read_file', args: { Path: join(home, 'cut.png') } } })
  assert.equal(sh(hooks.hooks[0].command, payload).decision, 'deny')
  assert.equal(installMediaGuard({ agyDir, script: source }), dir)
  rmSync(sourceRoot, { recursive: true, force: true })
  assert.equal(sh(hooks.hooks[0].command, payload).decision, 'deny', 'the installed copy does not depend on its source generation')
  rmSync(join(dir, 'media-guard.mjs'))
  assert.deepEqual(sh(hooks.hooks[0].command, payload), { decision: 'ask' })
  assert.ok(existsSync(join(dir, 'plugin.json')))
})

// agy runs a hook through cmd.exe on Windows, which has no /bin/sh: the command there is cmd's own, with each path quoted.
test('on Windows the hook is a cmd.exe command', (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-media-guard-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const agyDir = join(home, 'antigravity-cli')
  mkdirSync(agyDir)
  const node = String.raw`C:\Program Files (x86)\nodejs\node.exe`
  const script = SCRIPT
  const dir = installMediaGuard({ agyDir, script, node, platform: 'win32' })
  const command = JSON.parse(readFileSync(join(dir, 'hooks.json'), 'utf8'))['persona-media-guard'].PreToolUse[0].hooks[0].command
  const ask = 'echo {"decision":"ask"}'
  const installed = join(dir, 'media-guard.mjs')
  const dependency = join(dir, 'lib', 'media-guard.js')
  assert.equal(command, `if exist "${node}" (if exist "${installed}" (if exist "${dependency}" ("${node}" "${installed}" --state "${join(agyDir, 'persona-media-guard')}") else (${ask})) else (${ask})) else (${ask})`)
})

test('cmd.exe runs the Windows hook and answers "ask" once its script or Node.js is gone', { skip: process.platform !== 'win32' && 'needs cmd.exe' }, (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy media guard ('))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const agyDir = join(home, 'antigravity-cli')
  mkdirSync(agyDir)
  const hook = (script, node) => JSON.parse(readFileSync(join(installMediaGuard({ agyDir, script, node }), 'hooks.json'), 'utf8'))['persona-media-guard'].PreToolUse[0].hooks[0].command
  // As written, and wrapped in the quotes cmd /c strips: both are how a caller may pass it.
  const cmd = (command, input) => [command, `"${command}"`].map((line) =>
    JSON.parse(execFileSync('cmd.exe', ['/d', '/c', line], { input, windowsVerbatimArguments: true }).toString()))
  writeFileSync(join(home, 'cut.png'), png.subarray(0, 20))
  const payload = JSON.stringify({ toolCall: { name: 'read_file', args: { Path: join(home, 'cut.png') } } })
  for (const answer of cmd(hook(SCRIPT), payload)) assert.equal(answer.decision, 'deny')
  const installed = join(home, 'config', 'plugins', 'persona-media-guard', 'media-guard.mjs')
  const missingScript = hook(SCRIPT)
  rmSync(installed)
  for (const answer of cmd(missingScript, payload)) assert.deepEqual(answer, { decision: 'ask' })
  // Node.js moved (an upgrade, a reinstall): cmd would say "is not recognized" and print no JSON.
  for (const answer of cmd(hook(SCRIPT, join(home, 'old node (x86)', 'node.exe')), payload)) assert.deepEqual(answer, { decision: 'ask' })
})
