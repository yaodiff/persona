// @vitest-environment jsdom
// The health page's reports card (docs/assistant/health.md "Checkup reports") after the hub comes back: a list that could
// not be read while it was away is read again on the next hello, as the page's conversations are, and its error goes.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HealthReport } from "./reports";

type Request = { path: string; resolve: (value: unknown) => void; reject: (error: Error) => void };
const hub = vi.hoisted(() => ({ requests: [] as Request[], hello: new Set<() => void>() }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => new Promise((resolve, reject) => hub.requests.push({ path, resolve, reject })),
}));
vi.mock("../../lib/store", async (orig) => {
  const real = await orig<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), assistant: { machine_id: "mac" } } as ReturnType<typeof real.getState>),
    onHubEvent: (type: string, l: () => void) => {
      if (type !== "hello") return () => {};
      hub.hello.add(l);
      return () => void hub.hello.delete(l);
    },
  };
});

import { HealthReports } from "./HealthReports";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const path = "/machines/mac/health/reports";
const reads = () => hub.requests.filter((r) => r.path === path);
const report: HealthReport = { name: "blood.pdf", title: "Blood test", path: "reports/blood.pdf", size: 2048, modified_at: "2026-11-01T09:00:00Z" };
const card = () => host.querySelector('[data-testid="health-reports"]')?.textContent ?? "";

beforeEach(() => {
  hub.requests.length = 0;
  hub.hello.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("the reports card", () => {
  it("reads the list again on the next hello after it failed, and drops the error", async () => {
    await act(async () => root.render(<HealthReports version={0} />));
    expect(reads()).toHaveLength(1);
    await act(async () => reads()[0].reject(new TypeError("Failed to fetch")));
    expect(card()).toContain("没读到");

    await act(async () => hub.hello.forEach((l) => l()));
    expect(reads()).toHaveLength(2);
    await act(async () => reads()[1].resolve({ reports: [report] }));
    expect(card()).not.toContain("没读到");
    expect(card()).toContain("Blood test");
  });

  it("reads it again on a hello after a good read too, so a report added or deleted elsewhere shows", async () => {
    await act(async () => root.render(<HealthReports version={0} />));
    await act(async () => reads()[0].resolve({ reports: [] }));
    expect(card()).toContain("还没有");
    await act(async () => hub.hello.forEach((l) => l()));
    await act(async () => reads()[1].resolve({ reports: [report] }));
    expect(card()).toContain("Blood test");
  });
});
