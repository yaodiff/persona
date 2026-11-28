import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildXiaoYiRouteUri,
  createXiaoYiRoute,
  formatSharedDraftText,
  parseXiaoYiUri,
  parseXiaoYiWant
} from '../entry/src/main/ets/core/XiaoYiRoutes.ts';

test('parseXiaoYiUri parses all supported persona://open destinations and rejects unknown hosts or destinations', () => {
  for (const dest of ['assistant', 'agents', 'todos', 'knowledge', 'health', 'inbox', 'recording', 'dictate', 'settings_xiaoyi', 'share']) {
    const parsed = parseXiaoYiUri(`persona://open?dest=${dest}`);
    assert.ok(parsed, dest);
    assert.equal(parsed.dest, dest);
  }
  assert.equal(parseXiaoYiUri('persona://open?dest=todo')?.dest, 'todos');
  assert.equal(parseXiaoYiUri('persona://open?dest=recordings')?.dest, 'recording');
  assert.equal(parseXiaoYiUri('persona://open?dest=xiaoyi')?.dest, 'settings_xiaoyi');
  assert.equal(parseXiaoYiUri('persona://pair?code=123'), undefined);
  assert.equal(parseXiaoYiUri('persona://password-login?state=abc'), undefined);
  assert.equal(parseXiaoYiUri('https://example.com/open?dest=assistant'), undefined);
  assert.equal(parseXiaoYiUri('persona://open?dest=unknown_dest'), undefined);
});

test('session destination requires machine and session and preserves profile and manager', () => {
  assert.equal(parseXiaoYiUri('persona://open?dest=session&machine=m1'), undefined);
  assert.equal(parseXiaoYiUri('persona://open?dest=session&session=s1'), undefined);
  const full = parseXiaoYiUri('persona://open?dest=session&profile=hub-a&manager=work&machine=m1&session=s1');
  assert.ok(full);
  assert.equal(full.profile, 'hub-a');
  assert.equal(full.manager, 'work');
  assert.equal(full.machine, 'm1');
  assert.equal(full.session, 's1');
});

test('parseXiaoYiUri enforces URI length, control-char rejection, URL scheme allowlist, and text bounds', () => {
  assert.equal(parseXiaoYiUri('persona://open?dest=assistant&text=' + 'a'.repeat(17000)), undefined);
  assert.equal(parseXiaoYiUri('persona://open?dest=assistant\n'), undefined);
  assert.equal(parseXiaoYiUri('persona://open?dest=session&machine=m1%00bad&session=s1'), undefined);
  assert.equal(parseXiaoYiUri('persona://open?dest=assistant&text=%E0%A4%A'), undefined);

  const share = parseXiaoYiUri('persona://open?dest=share&title=Note&text=Hello&url=javascript%3Aalert(1)');
  assert.ok(share);
  assert.equal(share.url, '');
  assert.equal(share.title, 'Note');
  assert.equal(share.text, 'Hello');

  const safeShare = parseXiaoYiUri('persona://open?dest=share&url=https%3A%2F%2Fexample.com%2Fdoc&autoStart=1');
  assert.ok(safeShare);
  assert.equal(safeShare.url, 'https://example.com/doc');
  assert.equal(safeShare.autoStart, true);
});

test('buildXiaoYiRouteUri round-trips cleanly and parseXiaoYiWant reads Want parameters', () => {
  const route = createXiaoYiRoute('session');
  route.profile = 'hub-1';
  route.manager = 'personal';
  route.machine = 'workstation';
  route.session = 'sess-42';
  const uri = buildXiaoYiRouteUri(route);
  const roundTripped = parseXiaoYiUri(uri);
  assert.deepEqual(roundTripped, route);

  const fromParam = parseXiaoYiWant('', { personaDest: 'recording', autoStart: true });
  assert.ok(fromParam);
  assert.equal(fromParam.dest, 'recording');
  assert.equal(fromParam.autoStart, true);

  const fromParamUri = parseXiaoYiWant('', { personaXiaoYiUri: uri });
  assert.deepEqual(fromParamUri, route);
});

test('formatSharedDraftText deduplicates title/url cleanly without auto-sending', () => {
  assert.equal(formatSharedDraftText('Article', 'Summary here', 'https://example.com/a'), 'Article\nSummary here\nhttps://example.com/a');
  assert.equal(formatSharedDraftText('https://example.com/a', 'Check https://example.com/a', 'https://example.com/a'), 'Check https://example.com/a');
  assert.equal(formatSharedDraftText('Only', '', 'file:///etc/passwd'), 'Only');
});
