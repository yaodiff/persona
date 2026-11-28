import assert from 'node:assert/strict'
import { test } from 'node:test'
import { usageAccountFromQuota } from '../lib/usage.js'

const sampledAt = new Date('2026-11-20T23:00:00Z')

test('native quota buckets become sorted 5h and weekly usage windows', () => {
  const account = usageAccountFromQuota({ response: {
    buckets: [{ bucketId: 'gemini-weekly', window: '604800s', remainingFraction: 0.25, resetTime: '2026-11-27T23:00:00Z' }],
    groups: [{ displayName: 'Third party', buckets: [
      { bucketId: '3p-5h', window: '18000s', remainingFraction: 0.75, resetTime: '2026-11-21T04:00:00Z' },
      { bucketId: '3p-flash-5h', window: '18000s', modelId: 'gemini-flash', remainingFraction: 0.5, resetTime: '2026-11-21T04:00:00Z' },
    ] }],
  } }, sampledAt)
  assert.deepEqual(account, {
    provider: 'agy', name: 'Antigravity', sampled_at: '2026-11-20T23:00:00.000Z',
    windows: [
      { label: '5h', window_minutes: 300, used_percent: 25, resets_at: '2026-11-21T04:00:00.000Z', model: 'Third party' },
      { label: '5h', window_minutes: 300, used_percent: 50, resets_at: '2026-11-21T04:00:00.000Z', model: 'gemini-flash' },
      { label: '7d', window_minutes: 10080, used_percent: 75, resets_at: '2026-11-27T23:00:00.000Z' },
    ],
  })
})

test('bucket ids supply legacy window lengths and fractions are clamped', () => {
  const account = usageAccountFromQuota({ response: { buckets: [
    { bucketId: '3p-5h', remainingFraction: 1.2 },
    { bucketId: 'gemini-weekly', remainingFraction: -0.2 },
  ] } }, sampledAt)
  assert.deepEqual(account.windows, [
    { label: '5h', window_minutes: 300, used_percent: 0 },
    { label: '7d', window_minutes: 10080, used_percent: 100 },
  ])
})

test('agy 1.5.14 quota groups: named windows give the lengths, group names the models', () => {
  // The shape agy 1.5.14 answers RetrieveUserQuotaSummary with: groups only, windows named "5h" and "weekly", no modelId.
  const account = usageAccountFromQuota({ response: { groups: [
    { displayName: 'Gemini Models', buckets: [
      { bucketId: 'gemini-weekly', window: 'weekly', remainingFraction: 0.75, resetTime: '2026-11-27T09:00:00Z' },
      { bucketId: 'gemini-5h', window: '5h', remainingFraction: 0.5, resetTime: '2026-11-21T02:00:00Z' },
    ] },
    { displayName: 'Claude and GPT models', buckets: [
      { bucketId: '3p-weekly', window: 'weekly', remainingFraction: 1, resetTime: '2026-11-27T06:00:00Z' },
      { bucketId: '3p-5h', window: '5h', remainingFraction: 1, resetTime: '2026-11-21T03:00:00Z' },
    ] },
  ] } }, sampledAt)
  assert.deepEqual(account.windows, [
    { label: '5h', window_minutes: 300, used_percent: 50, resets_at: '2026-11-21T02:00:00.000Z', model: 'Gemini Models' },
    { label: '5h', window_minutes: 300, used_percent: 0, resets_at: '2026-11-21T03:00:00.000Z', model: 'Claude and GPT models' },
    { label: '7d', window_minutes: 10080, used_percent: 25, resets_at: '2026-11-27T09:00:00.000Z', model: 'Gemini Models' },
    { label: '7d', window_minutes: 10080, used_percent: 0, resets_at: '2026-11-27T06:00:00.000Z', model: 'Claude and GPT models' },
  ])
})

test('a window agy names counts even when its bucket id does not name it', () => {
  const lengthOf = (value, bucketId = 'quota-a') => usageAccountFromQuota({ response: { buckets: [
    { bucketId, window: value, remainingFraction: 0.5 },
  ] } }, sampledAt).windows.map((w) => [w.label, w.window_minutes])
  assert.deepEqual(lengthOf('5h'), [['5h', 300]])
  assert.deepEqual(lengthOf('weekly'), [['7d', 10080]])
  assert.deepEqual(lengthOf(' Weekly '), [['7d', 10080]])
  assert.deepEqual(lengthOf('daily'), [['1d', 1440]])
  assert.deepEqual(lengthOf('30m'), [['30m', 30]])
  assert.deepEqual(lengthOf('7d'), [['7d', 10080]])
  assert.deepEqual(lengthOf('1w'), [['7d', 10080]])
  assert.deepEqual(lengthOf('18000s'), [['5h', 300]])
  for (const value of ['monthly', 'constructor', '0h', '5 h', '90s', '99999999999999999999h', '', 5, undefined]) {
    assert.deepEqual(lengthOf(value), [], String(value))
  }
  assert.deepEqual(lengthOf('monthly', '3p-5h'), [['5h', 300]]) // a name it does not read still leaves the bucket id
})

test('empty, disabled, expired, amount-only and unknown buckets produce no window', () => {
  for (const response of [
    {},
    { buckets: [{ bucketId: '3p-5h', remainingFraction: 0.5, disabled: true }] },
    { buckets: [{ bucketId: '3p-5h', remainingAmount: 12 }] },
    { buckets: [{ bucketId: 'credits', remainingFraction: 0.5 }] },
    { buckets: [{ bucketId: '3p-5h', remainingFraction: 0.5, resetTime: '2026-11-20T22:59:59Z' }] },
  ]) {
    assert.deepEqual(usageAccountFromQuota({ response }, sampledAt).windows, [])
  }
  assert.throws(() => usageAccountFromQuota({}, sampledAt), TypeError)
})

test('malformed native bucket collections are rejected', () => {
  for (const response of [
    { buckets: null },
    { buckets: 'bad' },
    { groups: null },
    { groups: {} },
    { groups: [{ buckets: null }] },
    { groups: [{ buckets: 'bad' }] },
  ]) {
    assert.throws(() => usageAccountFromQuota({ response }, sampledAt), TypeError)
  }
})

test('userStatus populates signed-in account name and subscription plan with agy 1.5.18 precedence', () => {
  const quota = { response: { buckets: [{ bucketId: '3p-5h', window: '5h', remainingFraction: 0.75, resetTime: '2026-11-21T04:00:00Z' }] } }
  const full = usageAccountFromQuota(quota, sampledAt, {
    userStatus: {
      name: 'Alice Example',
      email: 'alice@example.com',
      planStatus: { planInfo: { teamsTier: 'TEAMS_TIER_PRO', planName: 'Pro' } },
      userTier: { id: 'g1-ultra-tier', name: 'Google AI Ultra' },
    },
  })
  assert.equal(full.name, 'alice@example.com')
  assert.equal(full.plan, 'Google AI Ultra')
  assert.match(full.account_key, /^[0-9a-f]{64}$/)

  const upperEmail = usageAccountFromQuota(quota, sampledAt, {
    userStatus: { email: '  ALICE@Example.COM \n' },
  })
  assert.equal(upperEmail.account_key, full.account_key)

  const displayNameFallback = usageAccountFromQuota(quota, sampledAt, {
    userStatus: {
      name: '  Bob\tExample\n',
      email: '   ',
      planStatus: { planInfo: { planName: 'Pro' } },
    },
  })
  assert.equal(displayNameFallback.name, 'Bob Example')
  assert.equal(displayNameFallback.plan, 'Pro')
  assert.equal('account_key' in displayNameFallback, false)

  const topPlanInfoFallback = usageAccountFromQuota(quota, sampledAt, {
    userStatus: { email: 'carol@example.com' },
    planInfo: { planName: 'Enterprise' },
  })
  assert.equal(topPlanInfoFallback.name, 'carol@example.com')
  assert.equal(topPlanInfoFallback.plan, 'Enterprise')
  assert.notEqual(topPlanInfoFallback.account_key, full.account_key)

  const tierIdFallback = usageAccountFromQuota(quota, sampledAt, {
    userStatus: { userTier: { id: 'g1-pro-tier' } },
  })
  assert.equal(tierIdFallback.name, 'Antigravity')
  assert.equal(tierIdFallback.plan, 'g1-pro-tier')

  const directUserStatus = usageAccountFromQuota(quota, sampledAt, {
    email: 'dana@example.com',
    userTier: { name: 'Google AI Pro' },
  })
  assert.equal(directUserStatus.name, 'dana@example.com')
  assert.equal(directUserStatus.plan, 'Google AI Pro')
})

test('missing, malformed, or oversized userStatus values fall back and respect 128-byte UTF-8 bounds', () => {
  const quota = { response: { buckets: [{ bucketId: '3p-5h', window: '5h', remainingFraction: 0.5 }] } }
  for (const status of [undefined, null, '', 42, [], {}, { userStatus: null }, { userStatus: { email: 123, name: ' \n\t ', userTier: { name: '  ' } } }]) {
    const account = usageAccountFromQuota(quota, sampledAt, status)
    assert.equal(account.name, 'Antigravity')
    assert.equal('plan' in account, false)
  }

  const longAccount = usageAccountFromQuota(quota, sampledAt, {
    userStatus: {
      email: `${'a'.repeat(140)}@example.com`,
      userTier: { name: `${'🚀'.repeat(40)} Ultra` },
    },
  })
  assert.ok(Buffer.byteLength(longAccount.name) <= 128)
  assert.equal(Buffer.byteLength(longAccount.name), 128)
  assert.ok(Buffer.byteLength(longAccount.plan) <= 128)
  assert.equal(Buffer.byteLength(longAccount.plan), 128)
  assert.equal(longAccount.plan, '🚀'.repeat(32))
})

test('epoch-zero resetTime on an untouched bucket omits resets_at instead of dropping the window', () => {
  const account = usageAccountFromQuota({ response: { groups: [
    { displayName: 'Claude and GPT models', buckets: [
      { bucketId: '3p-5h', window: '5h', remainingFraction: 1, resetTime: '1970-01-01T00:00:00Z' },
      { bucketId: '3p-weekly', window: 'weekly', remainingFraction: 1, resetTime: '1970-01-01T00:00:00Z' },
    ] },
  ] } }, sampledAt)
  assert.deepEqual(account.windows, [
    { label: '5h', window_minutes: 300, used_percent: 0, model: 'Claude and GPT models' },
    { label: '7d', window_minutes: 10080, used_percent: 0, model: 'Claude and GPT models' },
  ])
})
