// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, HubTimeoutError, setPairedToken } from "../../../lib/api";
import { bindScopedAssistant, ownedAssistant } from "../../../lib/scoped-assistant-api";
import { ResourceConfiguration, resourceConfigurations } from "./resourceConfiguration";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

const snapshot = (revision: string, value = 0, other = 0) => ({ id: "example", revision, value, other });
type Remote = ReturnType<typeof snapshot>;

function setup(manager = "assistant") {
  const read = vi.fn<() => Promise<Remote>>();
  const resources = resourceConfigurations((id, client) => new ResourceConfiguration<Remote, number>(id, client, { read }));
  const open = (owner: string, kind = "test") => resources.get("example", bindScopedAssistant(ownedAssistant(owner), { kind, id: "example" }));
  return { read, open, resource: open(manager) };
}

beforeEach(() => setPairedToken(""));

describe("resource configuration state", () => {
  it("ignores an older read after a newer answer or a confirmed save", async () => {
    const { read, resource } = setup();
    const late = deferred<Remote>();
    read.mockReturnValueOnce(late.promise).mockResolvedValueOnce(snapshot("two", 2));
    const old = resource.refresh();
    await resource.refresh();
    late.resolve(snapshot("one", 1));
    await old;
    expect(resource.snapshot().remote).toEqual(snapshot("two", 2));

    const beforeWrite = deferred<Remote>();
    read.mockReturnValueOnce(beforeWrite.promise).mockResolvedValueOnce(snapshot("three", 3));
    const refresh = resource.refresh();
    const save = resource.save({ label: "config", prepare: () => ({ send: async () => snapshot("three", 3) }) });
    await save;
    beforeWrite.resolve(snapshot("two", 2));
    await refresh;
    expect(resource.snapshot().remote).toEqual(snapshot("three", 3));
  });

  it("coalesces a repeated save and sends queued writes against the confirmed revision", async () => {
    const { read, resource } = setup();
    read.mockResolvedValueOnce(snapshot("one"));
    await resource.refresh();
    const first = deferred<Remote>();
    const send = vi.fn(() => first.promise);
    const save = { label: "config", key: "1", prepare: () => ({ send }) };
    const saved = resource.save(save);
    expect(resource.save(save)).toBe(saved);
    const revisions: string[] = [];
    const queued = resource.save({
      label: "config", key: "2",
      prepare: (remote) => {
        revisions.push(remote.revision);
        return { send: async () => snapshot("three", 2) };
      },
    });
    expect(send).toHaveBeenCalledTimes(1);
    expect(revisions).toEqual([]);
    expect(resource.snapshot()).toMatchObject({ saving: "config", queued: 1 });
    first.resolve(snapshot("two", 1));
    await Promise.all([saved, queued]);
    expect(revisions).toEqual(["two"]);
    expect(resource.snapshot()).toMatchObject({ remote: snapshot("three", 2), saving: null, queued: 0 });
  });

  it("keeps a pushed snapshot field over an older detail read or write reply", async () => {
    const { read, resource } = setup();
    const inFlight = deferred<Remote>();
    read.mockReturnValueOnce(inFlight.promise);
    const reading = resource.refresh();
    resource.patchRemote((remote) => ({ ...remote, other: 1 }));
    inFlight.resolve(snapshot("read"));
    await reading;
    expect(resource.snapshot().remote).toEqual(snapshot("read", 0, 1));

    const put = deferred<Remote>();
    const saving = resource.save({ label: "config", prepare: () => ({ send: () => put.promise }) });
    resource.patchRemote((remote) => ({ ...remote, other: 2 }));
    put.resolve(snapshot("write", 1, 1));
    await saving;
    expect(resource.snapshot().remote).toEqual(snapshot("write", 1, 2));
  });

  it("keeps a refused draft and refreshes a server revision conflict without replaying it", async () => {
    const { read, resource } = setup();
    read.mockResolvedValueOnce(snapshot("one")).mockResolvedValueOnce(snapshot("other", 3, 4));
    await resource.refresh();
    resource.edit(() => 9);
    const failure = new ApiError(409, "Settings changed", { code: "configuration_conflict" });
    const send = vi.fn().mockRejectedValue(failure);
    await expect(resource.save({ label: "config", prepare: () => ({ send }) })).rejects.toBe(failure);
    expect(resource.snapshot()).toMatchObject({
      remote: snapshot("other", 3, 4), draft: 9, saveError: { label: "config", error: failure },
    });
    expect(send).toHaveBeenCalledTimes(1);
  });

  it.each([true, false])("compares a timed-out write with its own resource (matches=%s)", async (matches) => {
    const { read, resource } = setup();
    const fresh = snapshot("other", matches ? 9 : 2, 4);
    read.mockResolvedValueOnce(snapshot("one")).mockResolvedValueOnce(fresh);
    await resource.refresh();
    resource.edit(() => 9);
    const failure = new HubTimeoutError();
    const saving = resource.save({
      label: "config",
      prepare: () => ({
        send: async () => { throw failure; },
        matches: (remote) => remote.value === 9,
        acknowledge: () => undefined,
      }),
    });
    if (matches) await expect(saving).resolves.toEqual(fresh);
    else await expect(saving).rejects.toBe(failure);
    expect(read).toHaveBeenCalledTimes(2);
    expect(resource.snapshot().remote).toEqual(fresh);
    expect(resource.snapshot().draft).toBe(matches ? undefined : 9);
  });

  it("checks an unreadable write receipt against a fresh resource snapshot", async () => {
    const { read, resource } = setup();
    const confirmed = snapshot("confirmed", 9);
    read.mockResolvedValueOnce(snapshot("one")).mockResolvedValueOnce(confirmed);
    await resource.refresh();
    const saved = resource.save({
      label: "config",
      prepare: () => ({ send: async () => undefined, matches: (remote) => remote.value === 9 }),
    });
    await expect(saved).resolves.toEqual(confirmed);
    expect(read).toHaveBeenCalledTimes(2);
  });

  it("keeps a newer edit and reconciles a missing comparison on reconnect", async () => {
    const { read, resource } = setup();
    read.mockResolvedValueOnce(snapshot("one")).mockRejectedValueOnce(new HubTimeoutError());
    await resource.refresh();
    resource.edit(() => 1);
    const saving = resource.save({
      label: "config",
      prepare: () => ({
        send: async () => { throw new HubTimeoutError(); },
        matches: (remote) => remote.value === 1,
        acknowledge: (draft) => draft === 1 ? undefined : draft,
      }),
    });
    resource.edit(() => 2);
    await expect(saving).rejects.toBeInstanceOf(HubTimeoutError);
    expect(resource.snapshot()).toMatchObject({ unconfirmed: true, draft: 2 });
    read.mockResolvedValueOnce(snapshot("two", 1));
    await resource.refresh();
    expect(resource.snapshot()).toMatchObject({ unconfirmed: false, remote: snapshot("two", 1), draft: 2, saveError: null });
  });

  it("isolates managers and resource kinds and retires active and queued work on credential replacement", async () => {
    const { read, resource, open } = setup();
    const work = open("work");
    const otherKind = open("assistant", "other");
    read.mockResolvedValueOnce(snapshot("one")).mockResolvedValueOnce(snapshot("work", 2));
    await resource.refresh();
    await work.refresh();
    resource.edit(() => 9);
    expect(open("assistant")).toBe(resource);
    expect(work.snapshot()).toMatchObject({ remote: snapshot("work", 2), draft: undefined });
    expect(otherKind.snapshot().remote).toBeNull();

    const delayed = deferred<Remote>();
    const first = resource.save({ label: "config", prepare: () => ({ send: () => delayed.promise }) });
    const put = vi.fn().mockResolvedValue(snapshot("queued", 3));
    const second = resource.save({ label: "config", prepare: () => ({ send: put }) });
    const stopped = Promise.allSettled([first, second]);
    const workRead = deferred<Remote>();
    read.mockReturnValueOnce(workRead.promise);
    const stale = work.refresh();
    setPairedToken(""); // Pairing with the same token still creates a new owner.
    const next = open("assistant");
    expect(next).not.toBe(resource);
    expect(resource.snapshot()).toMatchObject({ remote: null, draft: undefined, saving: null, queued: 0 });
    read.mockResolvedValueOnce(snapshot("new login", 5));
    await next.refresh();
    delayed.resolve(snapshot("late write", 9));
    workRead.resolve(snapshot("late read", 6));
    await stale;
    expect(await stopped).toMatchObject([
      { status: "rejected", reason: { name: "AbortError" } },
      { status: "rejected", reason: { name: "AbortError" } },
    ]);
    expect(put).not.toHaveBeenCalled();
    expect(next.snapshot()).toMatchObject({ remote: snapshot("new login", 5), draft: undefined });
    expect(work.snapshot().remote).toBeNull();
  });
});
