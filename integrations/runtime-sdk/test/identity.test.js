import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readdirSync, rmSync, symlinkSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { envValue, stateDir, socketDir, prepareStateDir, prepareSocketDirectory, isAssistantDir } from '../lib/identity.js'

test('only the Persona environment is read, including an explicitly empty value', () => {
  assert.equal(envValue('PERSONA_HOME', { PERSONA_HOME: 'current', PREVIOUS_HOME: 'ignored' }), 'current')
  assert.equal(envValue('PERSONA_HOME', { PREVIOUS_HOME: 'ignored' }), undefined)
  assert.equal(envValue('PERSONA_HOME', { PERSONA_HOME: '', PREVIOUS_HOME: 'ignored' }), '')
  assert.equal(envValue('PERSONA_PERSONA_ID', { PERSONA_PERSONA_ID: 'correct', PERSONA_ID: 'wrong' }), 'correct')
})

for (const kind of ['fresh', 'existing', 'linked', 'dangling']) {
  test(`single state directory: ${kind}`, (t) => {
    const home = mkdtempSync(join(tmpdir(), 'persona-identity-'))
    t.after(() => rmSync(home, { recursive: true, force: true }))
    const root = join(home, '.persona')
    if (kind === 'existing') mkdirSync(root)
    if (kind === 'linked') { mkdirSync(join(home, 'external')); symlinkSync('external', root) }
    if (kind === 'dangling') symlinkSync('missing', root)
    const before = readdirSync(home)
    assert.equal(stateDir(home, {}), root)
    assert.equal(socketDir(home, {}), root)
    assert.deepEqual(readdirSync(home), before)
    if (kind === 'dangling') assert.throws(() => prepareStateDir(home, {}), /unavailable/)
    else {
      assert.equal(prepareStateDir(home, {}), root)
      assert.equal(prepareStateDir(home, {}), root)
      assert.deepEqual(readdirSync(home).sort(), kind === 'linked' ? ['.persona', 'external'] : ['.persona'])
    }
    assert.equal(stateDir(home, { PERSONA_HOME: '~/profile' }), join(home, 'profile'))
    assert.equal(isAssistantDir(join(home, '.persona', 'assistant'), home, {}), true)
    assert.equal(isAssistantDir(join(home, '.persona', 'assistant-m-0a1b'), home, {}), true)
    assert.equal(isAssistantDir(join(home, '.persona', 'health'), home, {}), true)
    assert.equal(isAssistantDir(join(home, '.persona', 'health-m-0a1b'), home, {}), true)
    assert.equal(isAssistantDir(join(home, '.persona', 'tracks', 'ops'), home, {}), true)
    assert.equal(isAssistantDir(join(home, '.persona', 'tracks-m-0a1b', 'ops-2'), home, {}), true)
    assert.equal(isAssistantDir(join(home, 'profile', 'assistant'), home, { PERSONA_HOME: '~/profile' }), true)
    assert.equal(isAssistantDir(join(home, 'profile', 'tracks', 'ops'), home, { PERSONA_HOME: '~/profile' }), true)
    assert.equal(isAssistantDir(join(home, '.persona', 'tracks', 'health'), home, {}), false)
    assert.equal(isAssistantDir(join(home, '.persona', 'tracks', 'main'), home, {}), false)
    assert.equal(isAssistantDir(join(home, '.persona', 'tracks', 'a'), home, {}), false)
    assert.equal(isAssistantDir(join(home, '.persona', 'tracks', 'Bad'), home, {}), false)
    assert.equal(isAssistantDir(join(home, 'project', 'assistant'), home, {}), false)
    assert.equal(isAssistantDir(join(home, 'project', 'tracks', 'ops'), home, {}), false)
    assert.equal(prepareSocketDirectory(join(home, 'sockets', 'node.sock'), home, {}), join(home, 'sockets'))
  })
}
