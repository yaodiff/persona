import test from 'node:test'
import assert from 'node:assert/strict'
import { extractShellGates, matchShellGate } from '../lib/shell-gates.js'

const gates = extractShellGates([
  { name: 'mail_send', shell: ['send-mail', 'scripts/mail.py'] },
])

test('matchShellGate detects POSIX shell -c option ordering, clusters, standalone +, and zsh options', () => {
  const bypassed = [
    "sh -c -- 'send\"\"-mail --to alice'",
    "bash -lc -- 'send\"\"-mail --to alice'",
    "sh -c - 'send\"\"-mail --to alice'",
    "sh -c -e 'send\"\"-mail --to alice'",
    "bash -c -l 'send\"\"-mail --to alice'",
    "sh -c -u -o pipefail 'send\"\"-mail --to alice'",
    "sh -o errexit -c 'send\"\"-mail --to alice'",
    "bash -o pipefail -c 'send\"\"-mail --to alice'",
    "sh +e -c 'send\"\"-mail --to alice'",
    "sh +o noglob -c 'send\"\"-mail --to alice'",
    "bash --rcfile /dev/null -c 'send\"\"-mail --to alice'",
    "bash --init-file /tmp/rc --norc -c 'send\"\"-mail --to alice'",
    "bash -O extglob -c 'send\"\"-mail --to alice'",
    "bash +O extglob -c 'send\"\"-mail --to alice'",
    "sh -co errexit 'send\"\"-mail --to alice'",
    "sh -oc errexit 'send\"\"-mail --to alice'",
    "bash -Oc extglob 'send\"\"-mail --to alice'",
    "bash -oo errexit noglob -c 'send\"\"-mail --to alice'",
    "bash -c -oo errexit noglob 'send\"\"-mail --to alice'",
    "bash -c -OO extglob nullglob 'send\"\"-mail --to alice'",
    "ksh -oc 'send\"\"-mail --to alice'",
    "ksh -o -c 'send\"\"-mail --to alice'",
    "ksh +o -c 'send\"\"-mail --to alice'",
    "ksh -o c 'send\"\"-mail --to alice'",
    "ksh +o c 'send\"\"-mail --to alice'",
    "ksh +oc 'send\"\"-mail --to alice'",
    "ksh -o - 'send\"\"-mail --to alice'",
    "ksh +o - 'send\"\"-mail --to alice'",
    "ksh -o - -c 'send\"\"-mail --to alice'",
    "ksh -o - -- 'send\"\"-mail --to alice'",
    "ksh -o -- 'send\"\"-mail --to alice'",
    "ksh -co -o noglob 'send\"\"-mail --to alice'",
    "ksh -co -c 'send\"\"-mail --to alice'",
    "ksh -oerrexit -o noglob -oc 'send\"\"-mail --to alice'",
    "ksh 'send\"\"-mail --to alice'",
    "ksh -o noglob 'send\"\"-mail --to alice'",
    "ksh -onoclobber 'send\"\"-mail --to alice'",
    "ksh -- 'send\"\"-mail --to alice'",
    "ksh -s +s 'send\"\"-mail --to alice'",
    "ksh -s +s -- 'send\"\"-mail --to alice'",
    "ksh -o noglob -s +s 'send\"\"-mail --to alice'",
    "sh -oerrexit -c 'send\"\"-mail --to alice'",
    "zsh -O -c 'send\"\"-mail --to alice'",
    "zsh -c -O 'send\"\"-mail --to alice'",
    "zsh -Oc 'send\"\"-mail --to alice'",
    "zsh -0 -c 'send\"\"-mail --to alice'",
    "zsh -c -0 'send\"\"-mail --to alice'",
    "zsh --emulate csh -c 'send\"\"-mail --to alice'",
    "sh + -c 'send\"\"-mail --to alice'",
    "bash + -c 'send\"\"-mail --to alice'",
    "dash + -c 'send\"\"-mail --to alice'",
    "sh -c + 'send\"\"-mail --to alice'",
    "zsh -c + 'send\"\"-mail --to alice'",
    "ksh -c + 'send\"\"-mail --to alice'",
    "env FOO=1 /bin/sh -o errexit -c -- 'send\"\"-mail --to alice'",
    "bash -c 'echo ok' && sh -c -- 'send\"\"-mail --to alice'",
  ]
  for (const cmd of bypassed) {
    assert.equal(matchShellGate(cmd, gates)?.name, 'mail_send', `expected gate hit for: ${cmd}`)
  }
})

test('matchShellGate detects unquoted command-substitution and line-continuation word/token joining', () => {
  const bypassed = [
    'send$()-mail --to alice',
    'send$(true)-mail --to alice',
    'send$(:)-mail --to alice',
    'send``-mail --to alice',
    'send`true`-mail --to alice',
    'send\\\n-mail --to alice',
    '"send\\\n-mail" --to alice',
    'send$\\\n(true)-mail --to alice',
    '"send$\\\n(true)-mail" --to alice',
    'echo "$\\\n(send""-mail)"',
    "$\\\n'send\\x2dmail' --to alice",
    "sh -c $\\\n'send\\x2dmail'",
    'echo $($\\\n\'send\\x2dmail\')',
    'sh -c $(true) \'send""-mail --to alice\'',
  ]
  for (const cmd of bypassed) {
    assert.equal(matchShellGate(cmd, gates)?.name, 'mail_send', `expected gate hit for: ${cmd}`)
  }
})

test('matchShellGate preserves non-matching shell invocations without false positives', () => {
  const safe = [
    "sh -c 'echo ok'",
    "bash -o pipefail -c -- 'echo ok'",
    "sh -- -c 'send\"\"-mail --to alice'",
    "sh - -c 'send\"\"-mail --to alice'",
    "sh script.sh -c 'send\"\"-mail --to alice'",
    "sh -o c 'send\"\"-mail --to alice'",
    "ksh -o -- -c 'send\"\"-mail --to alice'",
    "ksh script.sh -c 'send\"\"-mail --to alice'",
    "ksh -o noglob script.sh",
    "ksh -onoclobber script.sh",
    "ksh -s 'send\"\"-mail --to alice'",
    "ksh +s -s 'send\"\"-mail --to alice'",
    "'send\\\n-mail' --to alice",
    'send\\\r\n-mail --to alice',
    'send\\\\\n-mail --to alice',
    'sh -c --',
    'sh -o',
    'echo my-send-mail-helper',
    'bash -c "echo my-send-mail-helper"',
    'ls -la',
  ]
  for (const cmd of safe) {
    assert.equal(matchShellGate(cmd, gates), undefined, `unexpected gate hit for: ${cmd}`)
  }
})
