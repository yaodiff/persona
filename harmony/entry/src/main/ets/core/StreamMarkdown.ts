import { MarkdownBlock, parseMarkdown } from './Markdown';

export interface StreamChunkSplit {
  done: string[];
  tail: string;
}

export interface KeyedMarkdownBlock {
  key: string;
  block: MarkdownBlock;
}

const CODE_LANGUAGE_BADGES: Record<string, string> = {
  'ts': 'TYPESCRIPT',
  'typescript': 'TYPESCRIPT',
  'tsx': 'TSX',
  'js': 'JAVASCRIPT',
  'javascript': 'JAVASCRIPT',
  'jsx': 'JSX',
  'py': 'PYTHON',
  'python': 'PYTHON',
  'sh': 'BASH',
  'bash': 'BASH',
  'zsh': 'BASH',
  'shell': 'BASH',
  'swift': 'SWIFT',
  'ets': 'ARKTS',
  'arkts': 'ARKTS',
  'rs': 'RUST',
  'rust': 'RUST',
  'go': 'GO',
  'golang': 'GO',
  'json': 'JSON',
  'yml': 'YAML',
  'yaml': 'YAML',
  'md': 'MARKDOWN',
  'markdown': 'MARKDOWN',
  'sql': 'SQL',
  'html': 'HTML',
  'css': 'CSS',
  'scss': 'SCSS',
  'cpp': 'C++',
  'c++': 'C++',
  'cxx': 'C++',
  'c': 'C',
  'h': 'C',
  'kt': 'KOTLIN',
  'kotlin': 'KOTLIN',
  'java': 'JAVA',
  'rb': 'RUBY',
  'ruby': 'RUBY',
  'php': 'PHP',
  'diff': 'DIFF',
  'patch': 'DIFF',
  'xml': 'XML',
  'toml': 'TOML',
  'docker': 'DOCKER',
  'dockerfile': 'DOCKER'
};

export function formatCodeLanguageBadge(language: string): string {
  const trimmed = (language || '').trim();
  if (!trimmed) return 'CODE';
  const normalized = trimmed.toLowerCase();
  const mapped = CODE_LANGUAGE_BADGES[normalized];
  return mapped ? mapped : trimmed.toUpperCase();
}

export function trimCodeForCopy(text: string): string {
  let end = (text || '').length;
  while (end > 0 && (text.charCodeAt(end - 1) === 10 || text.charCodeAt(end - 1) === 13)) {
    end--;
  }
  return end === (text || '').length ? (text || '') : text.slice(0, end);
}

export function countCodeLines(text: string): number {
  const cleaned = trimCodeForCopy(text);
  if (!cleaned) return 0;
  let count = 1;
  for (let i = 0; i < cleaned.length; i++) {
    if (cleaned.charCodeAt(i) === 10) count++;
  }
  return count;
}

export function formatCodeLineCount(lineCount: number): string {
  if (lineCount <= 0) return '';
  return lineCount === 1 ? '1 line' : String(lineCount) + ' lines';
}

class FenceState {
  character: string;
  length: number;
  constructor(character: string, length: number) {
    this.character = character;
    this.length = length;
  }
}

function openFence(trimmed: string): FenceState | undefined {
  if (trimmed.length < 3) return undefined;
  const ch = trimmed[0];
  if (ch !== '`' && ch !== '~') return undefined;
  let count = 0;
  while (count < trimmed.length && trimmed[count] === ch) count++;
  if (count < 3) return undefined;
  const info = trimmed.slice(count);
  if (ch === '`' && info.includes('`')) return undefined;
  return new FenceState(ch, count);
}

function closesFence(trimmed: string, fence: FenceState): boolean {
  if (trimmed.length < fence.length || trimmed[0] !== fence.character) return false;
  let count = 0;
  while (count < trimmed.length && trimmed[count] === fence.character) count++;
  return count >= fence.length && trimmed.slice(count).trim().length === 0;
}

/**
 * Mirrors iOS PersonaKit `StreamChunks.split` (apple/Packages/PersonaKit/Sources/PersonaKit/StreamChunks.swift):
 * splits a streaming reply at definite block boundaries (a blank line outside a code fence followed by an
 * unindented line). Finished chunks stay immutable so only the active tail re-parses during streaming.
 */
export function splitStreamChunks(source: string): StreamChunkSplit {
  const normalized = source.replace(/\r\n?/g, '\n');
  if (!normalized) return { done: [], tail: '' };
  const lines = normalized.split('\n');
  const done: string[] = [];
  let start = 0;
  let fence: FenceState | undefined;
  for (let i = 0; i < lines.length; i++) {
    const trimmed = lines[i].trim();
    if (fence) {
      if (closesFence(trimmed, fence)) fence = undefined;
      continue;
    }
    const opened = openFence(trimmed);
    if (opened) {
      fence = opened;
      continue;
    }
    if (trimmed.length > 0 || i <= start || i + 1 >= lines.length) continue;
    const next = lines[i + 1];
    if (!next || next[0] === ' ' || next[0] === '\t') continue;
    done.push(lines.slice(start, i).join('\n'));
    start = i + 1;
  }
  return { done: done, tail: lines.slice(start).join('\n') };
}

/** Fast deterministic FNV-1a 32-bit digest for stable, JSON.stringify-free block keys. */
export function fastTextHash(value: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < value.length; i++) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(36) + ':' + String(value.length);
}

class CachedChunk {
  source: string;
  entries: KeyedMarkdownBlock[];
  constructor(source: string, entries: KeyedMarkdownBlock[]) {
    this.source = source;
    this.entries = entries;
  }
}

function keyedBlocks(prefix: string, blocks: MarkdownBlock[]): KeyedMarkdownBlock[] {
  const result: KeyedMarkdownBlock[] = [];
  for (let i = 0; i < blocks.length; i++) {
    const block = blocks[i];
    const body = block.source || block.text || '';
    result.push({ key: prefix + '.' + String(i) + '.' + block.kind + '.' + fastTextHash(body), block: block });
  }
  return result;
}

/**
 * Incremental Markdown block cache for streaming and static messages.
 * Reuses parsed blocks and keys for every unchanged completed chunk (`done[i]`) and parses only the active tail.
 */
export class IncrementalMarkdownCache {
  private lastText: string = '';
  private doneChunks: CachedChunk[] = [];
  private tailSource: string = '';
  private tailEntries: KeyedMarkdownBlock[] = [];
  private flattened: KeyedMarkdownBlock[] = [];
  private parseCountValue: number = 0;

  get parseCount(): number { return this.parseCountValue; }

  update(text: string): KeyedMarkdownBlock[] {
    if (text === this.lastText && (this.flattened.length > 0 || !text)) return this.flattened;
    this.lastText = text;
    if (!text) {
      this.doneChunks = [];
      this.tailSource = '';
      this.tailEntries = [];
      this.flattened = [];
      return this.flattened;
    }
    const split = splitStreamChunks(text);
    const nextDone: CachedChunk[] = [];
    for (let i = 0; i < split.done.length; i++) {
      const chunk = split.done[i];
      const existing = i < this.doneChunks.length ? this.doneChunks[i] : undefined;
      if (existing && existing.source === chunk) {
        nextDone.push(existing);
      } else {
        this.parseCountValue++;
        const blocks = parseMarkdown(chunk);
        nextDone.push(new CachedChunk(chunk, keyedBlocks('c' + String(i), blocks)));
      }
    }
    this.doneChunks = nextDone;

    if (split.tail !== this.tailSource) {
      this.tailSource = split.tail;
      if (split.tail) {
        this.parseCountValue++;
        this.tailEntries = keyedBlocks('t' + String(nextDone.length), parseMarkdown(split.tail));
      } else {
        this.tailEntries = [];
      }
    }

    const merged: KeyedMarkdownBlock[] = [];
    for (const chunk of this.doneChunks) {
      for (const entry of chunk.entries) merged.push(entry);
    }
    for (const entry of this.tailEntries) merged.push(entry);
    this.flattened = merged;
    return this.flattened;
  }

  reset(): void {
    this.lastText = '';
    this.doneChunks = [];
    this.tailSource = '';
    this.tailEntries = [];
    this.flattened = [];
  }
}
