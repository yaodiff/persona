import test from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';

registerHooks({
  load(url, context, next) {
    if (url.endsWith('/ToastCenter.ets')) {
      const source = readFileSync(new URL(url), 'utf8')
        .replace(/@ObservedV2\s*/g, '')
        .replace(/@Trace\s+/g, '');
      return { format: 'module', shortCircuit: true, source: stripTypeScriptTypes(source) };
    }
    return next(url, context);
  }
});

const { ToastCenter } = await import('../entry/src/main/ets/models/ToastCenter.ets');

test('ToastCenter manages typed toasts, replaces active toast cleanly, and supports explicit dismiss', () => {
  const center = new ToastCenter();
  assert.equal(center.current, undefined);

  center.success('Copied');
  assert.equal(center.current?.kind, 'success');
  assert.equal(center.current?.message, 'Copied');
  const firstId = center.current?.id;

  let ran = 0;
  center.error('Network failed', 'Retry', () => { ran++; });
  assert.equal(center.current?.kind, 'error');
  assert.notEqual(center.current?.id, firstId);
  assert.equal(center.current?.actionTitle, 'Retry');
  center.current?.onAction?.();
  assert.equal(ran, 1);

  center.dismiss();
  assert.equal(center.current, undefined);

  // Empty/whitespace strings are ignored
  center.info('   ');
  assert.equal(center.current, undefined);
});
