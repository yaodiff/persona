import { afterEach, describe, expect, it, vi } from "vitest";

function tabStorage() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value); },
  };
}

describe("web client instance", () => {
  afterEach(() => {
    vi.resetModules();
    vi.unstubAllGlobals();
  });

  it("is stable within a document", async () => {
    const { clientInstance } = await import("./device-id");
    const first = clientInstance;
    expect(first).toBeTruthy();
    expect(clientInstance).toBe(first);
  });

  it("does not reuse a duplicated tab's copied sessionStorage", async () => {
    const copiedStorage = tabStorage();
    vi.stubGlobal("sessionStorage", copiedStorage);
    const firstDocument = await import("./device-id");
    const first = firstDocument.clientInstance;
    // A duplicated tab starts with a copy of its opener's sessionStorage, including the old implementation's key.
    copiedStorage.setItem("persona.clientInstance", first);

    vi.resetModules();
    const duplicatedDocument = await import("./device-id");
    const second = duplicatedDocument.clientInstance;
    expect(second).toBeTruthy();
    expect(second).not.toBe(first);
    expect(duplicatedDocument.clientInstance).toBe(second);
  });

  it("works when storage is unavailable", async () => {
    vi.stubGlobal("sessionStorage", {
      getItem: () => { throw new Error("storage unavailable"); },
      setItem: () => { throw new Error("storage unavailable"); },
    });
    const { clientInstance } = await import("./device-id");
    expect(clientInstance).toBeTruthy();
    expect(clientInstance).toBe(clientInstance);
  });
});
