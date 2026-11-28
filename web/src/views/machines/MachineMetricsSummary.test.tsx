// @vitest-environment jsdom
// A computer's card says load the way the chip, the machine page and the apps do (#12519): load per core with one
// decimal under CPU ("Load 1.4 per core"), the raw load average only while the core count is unknown.
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import type { Machine, MachineMetrics } from "../../lib/types";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const { MachineMetricsSummary, loadLine } = await import("./MachineMetricsSummary");

const GB = 1024 ** 3;
const studio: Machine = { id: "studio", name: "Studio", online: true, system: { cores: 18, memory: 128 * GB } };
const metrics: MachineMetrics = { machine_id: "studio", online: true, points: [], latest: { t: 1, cpu: 41, load: 25.4, mem_used: 74 * GB, mem_total: 128 * GB } };

describe("the card's load line", () => {
  // switching languages loads their catalogs, which takes a few seconds on a busy machine
  afterEach(async () => {
    await initI18n("zh-Hans");
  }, 30_000);

  it("is load per core with one decimal, the raw value only without a core count", async () => {
    expect(loadLine(25.4, 18)).toBe("每核负载 1.4");
    expect(loadLine(25.4, undefined)).toBe("负载 25.4");
    expect(loadLine(undefined, 18)).toBe("负载 —");
    await initI18n("en");
    expect(loadLine(25.4, 18)).toBe("Load 1.4 per core");
    await initI18n("ja");
    expect(loadLine(25.4, 18)).toBe("負荷 コアあたり 1.4");
  }, 30_000);

  it("shows under CPU, from the machine's cores or the metrics' system", () => {
    const host = document.createElement("div");
    host.innerHTML = renderToStaticMarkup(<MachineMetricsSummary machine={studio} metrics={metrics} />);
    expect(host.textContent).toContain("每核负载 1.4");
    expect(host.textContent).not.toContain("负载 25.4");
    host.innerHTML = renderToStaticMarkup(<MachineMetricsSummary machine={{ ...studio, system: undefined }} metrics={{ ...metrics, system: { cores: 18 } as MachineMetrics["system"] }} />);
    expect(host.textContent).toContain("每核负载 1.4");
  });
});
