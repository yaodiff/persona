import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks } from 'node:module';

registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL?.endsWith('/StreamMarkdown.ts') && specifier === './Markdown') {
      return { url: new URL('./Markdown.ts', context.parentURL).href, shortCircuit: true };
    }
    return next(specifier, context);
  }
});

const {
  IncrementalMarkdownCache,
  fastTextHash,
  splitStreamChunks
} = await import('../entry/src/main/ets/core/StreamMarkdown.ts');
const { markdownVisibleText } = await import('../entry/src/main/ets/core/Markdown.ts');

test('splitStreamChunks mirrors iOS StreamChunks: cuts on blank lines outside fences followed by unindented text', () => {
  const prose = '第一段已完成。\n\n第二段也在输出中';
  assert.deepEqual(splitStreamChunks(prose), {
    done: ['第一段已完成。'],
    tail: '第二段也在输出中'
  });

  // Blank line inside a code fence must NOT split the fence prematurely.
  const fenced = 'Intro\n\n```ts\nconst a = 1;\n\nconst b = 2;\n```\n\nAfter fence';
  assert.deepEqual(splitStreamChunks(fenced), {
    done: ['Intro', '```ts\nconst a = 1;\n\nconst b = 2;\n```'],
    tail: 'After fence'
  });

  // Indented continuation after a blank line stays with its list item.
  const indented = '- First item\n\n  continuation line\n\nNext paragraph';
  assert.deepEqual(splitStreamChunks(indented), {
    done: ['- First item\n\n  continuation line'],
    tail: 'Next paragraph'
  });

  // Unclosed fence while streaming stays intact in tail.
  const openFence = 'Done header\n\n```python\ndef f():\n\n    return 1';
  assert.deepEqual(splitStreamChunks(openFence), {
    done: ['Done header'],
    tail: '```python\ndef f():\n\n    return 1'
  });
});

test('IncrementalMarkdownCache avoids re-parsing completed chunks across streaming deltas and keeps stable keys', () => {
  const cache = new IncrementalMarkdownCache();

  const step1 = '# 报告标题\n\n第一段已完成内容。\n\n正在流式';
  const entries1 = cache.update(step1);
  const initialParses = cache.parseCount;
  assert.equal(entries1.length, 3);
  const headingKey = entries1[0].key;
  const paraKey = entries1[1].key;
  const headingBlock = entries1[0].block;
  const paraBlock = entries1[1].block;

  // Append characters to the tail: only the tail parses (+1), done blocks reuse exact object & key!
  const step2 = step1 + '输出更多文字';
  const entries2 = cache.update(step2);
  assert.equal(cache.parseCount, initialParses + 1);
  assert.equal(entries2[0].key, headingKey);
  assert.equal(entries2[1].key, paraKey);
  assert.equal(entries2[0].block, headingBlock);
  assert.equal(entries2[1].block, paraBlock);
  assert.notEqual(entries2[2].key, entries1[2].key);
  assert.ok(markdownVisibleText(entries2.map((e) => e.block)).includes('正在流式输出更多文字'));

  // Identical text update does zero parses.
  const entriesRepeat = cache.update(step2);
  assert.equal(cache.parseCount, initialParses + 1);
  assert.equal(entriesRepeat, entries2);

  // Starting a new paragraph promotes the prior tail into a stable chunk without re-parsing earlier chunks.
  const step3 = step2 + '\n\n第四块开始';
  const entries3 = cache.update(step3);
  assert.equal(entries3[0].block, headingBlock);
  assert.equal(entries3[1].block, paraBlock);
  assert.equal(entries3.length, 4);
});

test('fastTextHash differentiates content without JSON.stringify in MarkdownText', () => {
  assert.notEqual(fastTextHash('hello'), fastTextHash('hello!'));
  assert.equal(fastTextHash('相同文本'), fastTextHash('相同文本'));

  const source = readFileSync(new URL('../entry/src/main/ets/components/MarkdownText.ets', import.meta.url), 'utf8');
  assert.ok(source.includes('IncrementalMarkdownCache'));
  assert.ok(source.includes('StreamTailCaret'));
  assert.doesNotMatch(source, /JSON\.stringify\(block\)/);
});
