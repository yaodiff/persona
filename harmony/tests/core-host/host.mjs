// Shared host plumbing for the generated ArkTS codec and the real core's replay helper. These tests are host evidence
// only: a missing prerequisite fails, never skips. See native/app-core/crates/persona-core-ohos/examples/host_replay.rs
// for the helper's framing and command protocol.
import { after } from 'node:test';
import assert from 'node:assert/strict';
import nodeModule from 'node:module';
import { spawn } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';

if (typeof nodeModule.registerHooks !== 'function' || typeof nodeModule.stripTypeScriptTypes !== 'function') {
  throw new Error('The ArkTS core tests load the generated codec with module.registerHooks and ' +
    'module.stripTypeScriptTypes, which need Node.js 22.15 or newer (23.5 or newer on the 23 line). This is ' + process.version + '.');
}
export const helperPath = process.env.PERSONA_CORE_HOST_REPLAY;
if (!helperPath) {
  throw new Error('PERSONA_CORE_HOST_REPLAY is not set. Build the helper with `cargo build --locked -p persona-core-ohos ' +
    '--example host_replay` in native/app-core and set the variable to target/debug/examples/host_replay there.');
}
if (!existsSync(helperPath)) {
  throw new Error('PERSONA_CORE_HOST_REPLAY names a file that does not exist: ' + helperPath);
}

const wireUrl = new URL('../../entry/src/main/ets/core/rustcore/Wire.ts', import.meta.url);
export const wireSource = readFileSync(wireUrl, 'utf8');
nodeModule.registerHooks({
  load(url, context, next) {
    if (url === wireUrl.href) return {format: 'module', shortCircuit: true, source: nodeModule.stripTypeScriptTypes(wireSource)};
    return next(url, context);
  },
});
export const wire = await import(wireUrl.href);

// The helper's frames: a u32 little-endian length, a tag byte, then the fields.
const MAX_FRAME = 16 * 1024 * 1024;
export const Command = {config: 1, create: 2, dispatch: 3, flush: 4, drain: 5, close: 6, reencode: 7, samples: 8, transport: 9, sort: 10};
export const Record = {ok: 1, error: 2, changed: 3, effect: 4, sample: 5, end: 6};
export const PayloadKind = {latest: 1, newer: 2, reloaded: 3, older: 4, range: 5, frame: 6};
const WireType = {Action: 1, ChatEvent: 2, ChatPatch: 3, ChatSnapshot: 4, Config: 5, Effect: 6, ListOp: 7, Request: 8, WireBatch: 9, Payload: 10};

export function frame(tag, ...fields) {
  const body = Buffer.concat([Buffer.of(tag), ...fields]);
  const prefix = Buffer.alloc(4);
  prefix.writeUInt32LE(body.length);
  return Buffer.concat([prefix, body]);
}
export function handleField(handle) {
  const field = Buffer.alloc(8);
  field.writeBigInt64LE(BigInt(handle));
  return field;
}
export function hex(bytes) {
  return Buffer.from(bytes).toString('hex');
}

// A JSON value's source text as it appears in its fixture, alongside the parsed value. This small parser is only for
// the checked-in JSON fixture documents; it records slices, never re-serialises a value for transport decoding.
export function parseJsonSource(text) {
  let offset = 0;
  const space = () => { while (/[ \u000a\u000d\u0009]/.test(text[offset] ?? '')) offset++; };
  function value() {
    space();
    const start = offset;
    const char = text[offset];
    if (char === '{') {
      offset++;
      const object = {};
      space();
      if (text[offset] === '}') { offset++; return {raw: text.slice(start, offset), value: object}; }
      for (;;) {
        space();
        if (text[offset] !== '"') throw new Error('an object key is not a string at ' + offset);
        const key = value().value;
        space();
        if (text[offset++] !== ':') throw new Error('an object key has no colon at ' + offset);
        const member = value();
        Object.defineProperty(object, key, {value: member.value, enumerable: true, configurable: true, writable: true});
        const sources = object.__sources;
        if (sources === undefined) Object.defineProperty(object, '__sources', {value: {[key]: member}});
        else sources[key] = member;
        space();
        if (text[offset] === '}') { offset++; return {raw: text.slice(start, offset), value: object}; }
        if (text[offset++] !== ',') throw new Error('an object has no comma at ' + offset);
      }
    }
    if (char === '[') {
      offset++;
      const array = [];
      const sources = [];
      space();
      if (text[offset] === ']') { offset++; return {raw: text.slice(start, offset), value: array, elements: sources}; }
      for (;;) {
        const member = value();
        array.push(member.value);
        sources.push(member);
        space();
        if (text[offset] === ']') { offset++; return {raw: text.slice(start, offset), value: array, elements: sources}; }
        if (text[offset++] !== ',') throw new Error('an array has no comma at ' + offset);
      }
    }
    if (char === '"') {
      offset++;
      for (;;) {
        if (offset >= text.length) throw new Error('a string is not closed');
        if (text[offset] === '"') {
          offset++;
          const raw = text.slice(start, offset);
          return {raw, value: JSON.parse(raw)};
        }
        if (text[offset] === '\\') {
          offset++;
          if (!/["\\/bfnrtu]/.test(text[offset] ?? '')) throw new Error('an invalid string escape at ' + offset);
          if (text[offset] === 'u') {
            if (!/^[0-9a-fA-F]{4}$/.test(text.slice(offset + 1, offset + 5))) throw new Error('an invalid Unicode escape at ' + offset);
            offset += 4;
          }
        }
        if ((text.charCodeAt(offset) ?? 0) < 0x20) throw new Error('a control character in a string at ' + offset);
        offset++;
      }
    }
    if (text.startsWith('true', offset)) offset += 4;
    else if (text.startsWith('false', offset)) offset += 5;
    else if (text.startsWith('null', offset)) offset += 4;
    else {
      const number = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(text.slice(offset));
      if (number === null) throw new Error('not a JSON value at ' + offset);
      offset += number[0].length;
    }
    const raw = text.slice(start, offset);
    return {raw, value: JSON.parse(raw)};
  }
  const root = value();
  space();
  if (offset !== text.length) throw new Error('bytes follow the JSON document at ' + offset);
  return root;
}

export function rawItems(fixtureCase) {
  const source = fixtureCase.inputSource.value.items;
  if (source === null || source === undefined) return [];
  if (!Array.isArray(source)) throw new Error(fixtureCase.name + ': items is not an array');
  return fixtureCase.inputSource.value.__sources.items.elements.map((item) => item.raw);
}

export function pointer(object, path) {
  if (path === '') return object;
  let value = object;
  for (const part of path.slice(1).split('/').map((part) => part.replaceAll('~1', '/').replaceAll('~0', '~'))) {
    if (value === null || typeof value !== 'object' || !Object.hasOwn(value, part)) return undefined;
    value = value[part];
  }
  return value;
}

// Prefixes a failure with where it happened, keeping the assertion's own diff.
export function at(label, check) {
  try {
    return check();
  } catch (error) {
    error.message = label + ': ' + error.message;
    throw error;
  }
}

export class HostCore {
  #child;
  #pending = Buffer.alloc(0);
  #records = [];
  #wake = null;
  #failure = null;
  #exit = null;
  #stderr = '';
  #callbacks = [];
  #samples = new Map();

  constructor(path) {
    this.#child = spawn(path, [], {stdio: ['pipe', 'pipe', 'pipe']});
    this.#child.stdout.on('data', (chunk) => this.#received(chunk));
    this.#child.stderr.on('data', (chunk) => { this.#stderr += chunk; });
    // A helper that has gone away is reported by its exit, with its stderr, not by the write that noticed.
    this.#child.stdin.on('error', () => {});
    this.#child.on('error', (error) => { this.#failure ??= error; this.#notify(); });
    this.#child.on('close', (code, signal) => { this.#exit = {code, signal}; this.#notify(); });
  }

  #notify() {
    const wake = this.#wake;
    this.#wake = null;
    if (wake) wake();
  }

  #received(chunk) {
    this.#pending = Buffer.concat([this.#pending, chunk]);
    while (this.#failure === null && this.#pending.length >= 4) {
      const length = this.#pending.readUInt32LE(0);
      if (length === 0 || length > MAX_FRAME) {
        this.#failure = new Error('the helper wrote a frame of ' + length + ' bytes');
        break;
      }
      if (this.#pending.length < 4 + length) break;
      this.#records.push(this.#pending.subarray(4, 4 + length));
      this.#pending = this.#pending.subarray(4 + length);
    }
    this.#notify();
  }

  async #record() {
    for (;;) {
      if (this.#failure !== null) throw this.#failure;
      const record = this.#records.shift();
      if (record !== undefined) return record;
      if (this.#exit !== null) {
        throw new Error('the helper exited (' + (this.#exit.signal ?? this.#exit.code) + ') before answering: ' + this.#stderr.trim());
      }
      await new Promise((resolve) => { this.#wake = resolve; });
    }
  }

  // Sends one command and reads up to its result, keeping the callback and sample records written before it.
  async #call(tag, ...fields) {
    this.#child.stdin.write(frame(tag, ...fields));
    for (;;) {
      const record = await this.#record();
      const body = record.subarray(1);
      switch (record[0]) {
        case Record.ok: return new Uint8Array(body);
        case Record.error: throw new Error('the core refused: ' + body.toString('utf8'));
        case Record.changed:
          assert.equal(body.length, 16, 'a changed record is a handle and a version');
          this.#callbacks.push({kind: 'changed', handle: Number(body.readBigInt64LE(0)), version: body.readBigUInt64LE(8)});
          break;
        case Record.effect:
          this.#callbacks.push({kind: 'effect', handle: Number(body.readBigInt64LE(0)), bytes: new Uint8Array(body.subarray(8))});
          break;
        case Record.sample: {
          const name = body.subarray(1, 1 + body[0]).toString('utf8');
          assert.ok(!this.#samples.has(name), 'the helper names each sample once: ' + name);
          this.#samples.set(name, new Uint8Array(body.subarray(1 + body[0])));
          break;
        }
        default: throw new Error('the helper wrote an unknown record: ' + record[0]);
      }
    }
  }

  async coreConfig() { return this.#call(Command.config); }
  async coreCreate(config) {
    const handle = Number(Buffer.from(await this.#call(Command.create, config)).readBigInt64LE(0));
    assert.ok(Number.isSafeInteger(handle) && handle > 0, 'a handle is a positive integer');
    return handle;
  }
  async coreDispatch(handle, action) { return this.#flag(await this.#call(Command.dispatch, handleField(handle), action)); }
  async coreFlush(handle, timeoutMillis) {
    const timeout = Buffer.alloc(4);
    timeout.writeUInt32LE(timeoutMillis);
    return this.#flag(await this.#call(Command.flush, handleField(handle), timeout));
  }
  async coreDrain(handle, chat) { return this.#call(Command.drain, handleField(handle), Buffer.from(chat, 'utf8')); }
  async coreClose(handle) { assert.equal((await this.#call(Command.close, handleField(handle))).length, 0); }

  // Rust's encoding of the value Rust decoded from `bytes`.
  async reencode(type, bytes) { return this.#call(Command.reencode, Buffer.of(WireType[type]), bytes); }
  // The helper's Rust-built values, by name, as Rust encoded them.
  async samples() {
    await this.#call(Command.samples);
    return this.#samples;
  }
  async transport(kind, body) {
    const value = await this.#call(Command.transport, Buffer.of(kind), Buffer.from(body, 'utf8'));
    return JSON.parse(Buffer.from(value).toString('utf8'));
  }
  async sort(input) {
    const value = await this.#call(Command.sort, Buffer.from(JSON.stringify(input), 'utf8'));
    return JSON.parse(Buffer.from(value).toString('utf8'));
  }

  #flag(value) {
    assert.ok(value.length === 1 && value[0] <= 1, 'a flag is one byte, 0 or 1');
    return value[0] === 1;
  }

  // The callbacks since the last call, in order. Every one must belong to `handle`.
  takeCallbacks(handle) {
    const callbacks = this.#callbacks;
    this.#callbacks = [];
    for (const callback of callbacks) assert.equal(callback.handle, handle, 'a callback for another handle');
    return callbacks;
  }

  // Ends the script: the helper writes its `end` record and exits 0, with nothing left unread.
  async end() {
    this.#child.stdin.end();
    assert.equal((await this.#record())[0], Record.end, 'the helper ends with its end record');
    while (this.#exit === null && this.#failure === null) await new Promise((resolve) => { this.#wake = resolve; });
    if (this.#failure !== null) throw this.#failure;
    assert.deepEqual(this.#exit, {code: 0, signal: null}, this.#stderr.trim());
    assert.equal(this.#records.length + this.#pending.length + this.#callbacks.length, 0, 'nothing follows the end record');
  }

  kill() { this.#child.kill('SIGKILL'); }
}

const cores = [];
export function startCore() {
  const core = new HostCore(helperPath);
  cores.push(core);
  return core;
}
after(() => { for (const core of cores) core.kill(); });

export function requestObject(request) {
  switch (request.kind) {
    case 'fetch_after': return {kind: request.kind, after_seq: request.after_seq};
    case 'fetch_latest': return {kind: request.kind, fold_work: request.fold_work};
    case 'subscribe': return request.since_seq === null ? {kind: request.kind, fold_work: request.fold_work}
      : {kind: request.kind, fold_work: request.fold_work, since_seq: request.since_seq};
    case 'fetch_older': return {kind: request.kind, before_seq: request.before_seq, fold_work: request.fold_work};
    case 'fetch_range': return {kind: request.kind, from_seq: request.from_seq, through_seq: request.through_seq};
    default: throw new Error('unknown request: ' + request.kind);
  }
}

// One open chat on one handle: dispatches ArkTS-encoded actions, and after each flush decodes what the core sent
// back, applying the patch's operations to the list a client would hold.
export class ReplayedChat {
  list = [];
  #version = 0n;

  constructor(core, handle, chat, label) {
    this.core = core;
    this.handle = handle;
    this.chat = chat;
    this.label = label;
  }

  async dispatch(action) {
    const bytes = wire.encodeAction(action);
    // Rust reads these bytes as an Action and writes the same bytes back: nothing was dropped or left over.
    assert.equal(hex(await this.core.reencode('Action', bytes)), hex(bytes), this.label + ': Rust re-encodes the action differently');
    assert.equal(await this.core.coreDispatch(this.handle, bytes), true, this.label + ': dispatch');
  }

  // Returns the drained patch and the requests the core made since the last settle.
  async settle() {
    assert.equal(await this.core.coreFlush(this.handle, 5000), true, this.label + ': the worker did not finish');
    const requests = [];
    for (const callback of this.core.takeCallbacks(this.handle)) {
      if (callback.kind === 'changed') {
        assert.ok(callback.version > this.#version, this.label + ': wake-up versions grow');
        this.#version = callback.version;
        continue;
      }
      const effect = at(this.label + ' effect', () => wire.decodeEffect(callback.bytes));
      if (effect.kind === 'rejected') throw new Error(this.label + ': rejected: ' + effect.reason);
      assert.equal(effect.chat, this.chat, this.label + ': the effect names the chat');
      requests.push(requestObject(effect.request));
    }
    const bytes = await this.core.coreDrain(this.handle, this.chat);
    const patch = at(this.label + ' patch', () => wire.decodeChatPatch(bytes));
    // The wake-up's version reaches a client as a plain number and the patch's inside its bytes: they are one value.
    assert.equal(BigInt(patch.version), this.#version, this.label + ': the patch carries the last wake-up\'s version');
    for (const op of patch.ops) {
      if (op.kind === 'remove') this.list.splice(op.index, 1);
      else if (op.kind === 'insert') this.list.splice(op.index, 0, op.json);
      else this.list[op.index] = op.json;
    }
    return {patch, requests};
  }
}
