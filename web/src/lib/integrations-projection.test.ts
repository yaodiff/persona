// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setPairedToken } from "./api";
import { readIntegrationsCatalog, readIntegrationDetail, supportsIntegrationsProjection } from "./integrations-projection";
import { chooseManager, MANAGER_HEADER, resetManagerChoice } from "./manager-choice";
import { setRoster, type ManagerList } from "./managers";
import { bindScopedAssistant, ownedAssistant, verifyScopedAssistantRoster } from "./scoped-assistant-api";

const roster: ManagerList = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "Home", online: true, integrations_projection_v1: true },
    { id: "work", name: "Work", online: true },
  ],
};

function item(manager: string) {
  return { ref: { manager, kind: "extension", id: "sample" }, name: manager, origin: "installed_extension",
    capabilities: ["tools", "future_capability"], availability: { state: "available" } };
}

function detail(manager: string) {
  return { item: item(manager), sections: [], availability: [] };
}

const catalogClient = (manager: string) => bindScopedAssistant(ownedAssistant(manager), { kind: "integrations", id: "" });
const detailClient = (manager: string) => bindScopedAssistant(ownedAssistant(manager), { kind: "extension", id: "sample" });

beforeEach(() => {
  setPairedToken("test-credential");
  resetManagerChoice();
  setRoster(structuredClone(roster));
});

afterEach(() => {
  setRoster(null);
  resetManagerChoice();
  vi.unstubAllGlobals();
});

it("requires a positive announcement from the exact assistant", () => {
  expect(supportsIntegrationsProjection(roster, "assistant")).toBe(true);
  expect(supportsIntegrationsProjection(roster, "work")).toBe(false);
  expect(supportsIntegrationsProjection(null, "assistant")).toBe(false);
});

it("deduplicates reads for one ref and isolates equal ids on other assistants", async () => {
  chooseManager("work");
  const fetcher = vi.fn(async (_: string, init?: RequestInit) =>
    Response.json(detail(new Headers(init?.headers).get(MANAGER_HEADER)!)));
  vi.stubGlobal("fetch", fetcher);
  const [first, second, work] = await Promise.all([
    readIntegrationDetail(detailClient("assistant"), "extension", "sample"),
    readIntegrationDetail(detailClient("assistant"), "extension", "sample"),
    readIntegrationDetail(detailClient("work"), "extension", "sample"),
  ]);
  expect(fetcher).toHaveBeenCalledTimes(2);
  expect(first).toEqual(second);
  expect(first).not.toBe(second);
  expect(first.item.ref.manager).toBe("assistant");
  expect(work.item.ref.manager).toBe("work");
  expect(first.item.capabilities).toEqual(["tools"]);
});

it("uses the server counts and rejects a catalog from a different assistant", async () => {
  const counts = { all: 2, channels: 0, data: 1, tools: 1 };
  const fetcher = vi.fn()
    .mockResolvedValueOnce(Response.json({ items: [item("assistant")], counts, availability: [] }))
    .mockResolvedValueOnce(Response.json({ items: [item("work")], counts, availability: [] }));
  vi.stubGlobal("fetch", fetcher);
  await expect(readIntegrationsCatalog(catalogClient("assistant"))).resolves.toMatchObject({ counts });
  await expect(readIntegrationsCatalog(catalogClient("assistant"))).rejects.toThrow();
});

it("drops unsupported section kinds and rejects foreign section refs", async () => {
  const valid = { kind: "information_access", ref: item("assistant").ref, availability: { state: "available" }, edit: "settings" };
  const fetcher = vi.fn()
    .mockResolvedValueOnce(Response.json({ ...detail("assistant"), sections: [valid, { ...valid, kind: "future_action" }] }))
    .mockResolvedValueOnce(Response.json({ ...detail("assistant"), sections: [{ ...valid, ref: item("work").ref }] }));
  vi.stubGlobal("fetch", fetcher);
  const client = detailClient("assistant");
  await expect(readIntegrationDetail(client, "extension", "sample")).resolves.toMatchObject({ sections: [valid] });
  await expect(readIntegrationDetail(client, "extension", "sample")).rejects.toThrow();
});

it("retires pending reads and rechecks the roster when the same token is paired again", async () => {
  let finish!: (response: Response) => void;
  let pendingSignal: AbortSignal | null | undefined;
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/managers")) {
      return Response.json({ ...roster, managers: roster.managers.map((entry) => ({ ...entry, integrations_projection_v1: false })) });
    }
    if (!finish) {
      pendingSignal = init?.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    }
    return Response.json({ ...detail("assistant"), item: { ...item("assistant"), name: "New session" } });
  });
  vi.stubGlobal("fetch", fetcher);
  const previous = detailClient("assistant");
  const pending = readIntegrationDetail(previous, "extension", "sample");
  const rejected = expect(pending).rejects.toMatchObject({ name: "AbortError" });
  await vi.waitFor(() => expect(finish).toBeTypeOf("function"));

  setPairedToken("test-credential");
  expect(pendingSignal?.aborted).toBe(true);
  expect(supportsIntegrationsProjection(await verifyScopedAssistantRoster(), "assistant")).toBe(false);
  expect(detailClient("assistant").key).not.toBe(previous.key);
  await expect(readIntegrationDetail(detailClient("assistant"), "extension", "sample"))
    .resolves.toMatchObject({ item: { name: "New session" } });
  finish(Response.json(detail("assistant")));
  await rejected;
});
