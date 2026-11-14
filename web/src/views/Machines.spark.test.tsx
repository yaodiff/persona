// @vitest-environment jsdom
// Machines: every card draws its own CPU sparkline. Each one must own its gradient id (no duplicate ids on a page with
// several computers, and no fill borrowed from another card) and be announced as one image with its label.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { MetricPoint } from "../lib/types";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const { Spark } = await import("./Machines");

const points = (base: number): MetricPoint[] => [0, 1, 2, 3].map((i) => ({ t: 1000 + i * 60, cpu: base + i * 5 }) as MetricPoint);

describe("Spark", () => {
  it("gives each instance its own gradient id and a role=img svg", () => {
    const html = renderToStaticMarkup(
      <div>
        <Spark points={points(10)} />
        <Spark points={points(40)} />
      </div>,
    );
    const host = document.createElement("div");
    host.innerHTML = html;
    const svgs = [...host.querySelectorAll("svg")];
    expect(svgs).toHaveLength(2);
    const ids = svgs.map((svg) => svg.querySelector("linearGradient")?.id ?? "");
    expect(ids.every(Boolean)).toBe(true);
    expect(new Set(ids).size).toBe(2);
    for (const [i, svg] of svgs.entries()) {
      expect(svg.getAttribute("role")).toBe("img");
      expect(svg.getAttribute("aria-label")).toBeTruthy();
      const fills = [...svg.querySelectorAll("polygon")].map((p) => p.getAttribute("fill"));
      expect(fills.length).toBeGreaterThan(0);
      for (const f of fills) expect(f).toBe(`url(#${ids[i]})`);
    }
    // every id on the page is unique
    const all = [...host.querySelectorAll("[id]")].map((el) => el.id);
    expect(new Set(all).size).toBe(all.length);
  });
});

const { MachineMetricsSummary } = await import("./machines/MachineMetricsSummary");
const now = Math.floor(Date.now() / 1000);
const memoryTotal = 8 * 1024 ** 3;
const fixture = {
  machine_id: "mac", online: true,
  latest: { t: now, cpu: 4.5, load: 0, mem_used: memoryTotal / 2, mem_total: memoryTotal, net_rx: 0, net_tx: 1024 },
  points: [
    { t: now - 60, cpu: 1, mem: memoryTotal / 4, rx: 0, tx: 1024 },
    { t: now, cpu: 2, mem: memoryTotal / 2, rx: 0, tx: 2048 },
  ],
};
// the metrics' cells: the grid's children (the grid sits in the container its columns are measured against)
const cells = (host: HTMLElement) => host.querySelector("[data-testid=machine-metrics-summary]")!.children;
function summary(metrics: import("../lib/types").MachineMetrics = fixture, machine: import("../lib/types").Machine = { id: "mac", name: "Mac", online: true }) {
  const host = document.createElement("div");
  host.innerHTML = renderToStaticMarkup(<MachineMetricsSummary machine={machine} metrics={metrics} />);
  return host;
}

describe("machine metric summaries", () => {
  it("shows CPU load, memory percentage, bytes and separate network rates including zero", () => {
    const host = summary();
    expect(host.textContent).toContain("4.5%");
    expect(host.textContent).toContain("负载 0.00");
    expect(host.textContent).toContain("50%");
    expect(host.textContent).toContain("4.00 / 8.00 GB");
    expect(host.textContent).toContain("↓ 0 B/s");
    expect(host.textContent).toContain("↑ 1.00 KB/s");
    const memoryLine = host.querySelectorAll("svg")[1].querySelector("polyline")!;
    const ys = memoryLine.getAttribute("points")!.split(" ").map((p) => Number(p.split(",")[1]));
    // 25% and 50%, on the same 0–100 scale as CPU (not raw byte values).
    expect(ys).toEqual([29, 20]);
    const network = host.querySelectorAll("svg")[2];
    const lines = [...network.querySelectorAll("polyline")];
    expect(lines.map((line) => line.getAttribute("stroke"))).toEqual(["var(--orange)", "var(--ai)"]);
    expect(lines[0].getAttribute("points")!.split(" ").map((p) => p.split(",")[1])).toEqual(["38.0", "38.0"]);
    expect(network.querySelector("polygon")).toBeNull();
  });

  it.each([undefined, 0])("falls back from unknown latest memory total (%s) to system memory", (mem_total) => {
    const metrics = { ...fixture, latest: { ...fixture.latest, mem_total }, system: { memory: memoryTotal } };
    expect(summary(metrics).textContent).toContain("50%");
    expect(summary({ ...metrics, system: undefined }, { id: "mac", name: "Mac", online: true, system: { memory: memoryTotal } }).textContent).toContain("50%");
  });

  it("does not turn unknown memory capacity or network measurements into zero", () => {
    const host = summary({ ...fixture, latest: { ...fixture.latest, mem_total: undefined, net_rx: undefined, net_tx: undefined } });
    const cell = cells(host);
    expect(cell[1].textContent).toContain("—");
    expect(cell[1].textContent).not.toContain("0%");
    expect(cell[1].querySelector("svg")).toBeNull();
    expect(cell[2].textContent).toContain("↓ —");
    expect(cell[2].textContent).toContain("↑ —");
  });

  it("preserves 0% memory as a measured value", () => {
    const host = summary({ ...fixture, latest: { ...fixture.latest, mem_used: 0 } });
    expect(cells(host)[1].textContent).toContain("0%");
    expect(cells(host)[1].textContent).toContain("0 B / 8.00 GB");
  });

  it.each(["machine", "metrics"])("hides cached live values when %s says offline", (source) => {
    const host = summary({ ...fixture, online: source !== "metrics" }, { id: "mac", name: "Mac", online: source !== "machine" });
    expect(host.innerHTML).toBe("");
  });

  it("breaks both network lines at gaps and only the missing series at a missing measurement", () => {
    const points = Array.from({ length: 8 }, (_, i) => ({ t: now - (7 - i) * 10, cpu: 1, mem: 0, rx: i, tx: i, ...(i === 2 ? { gap: true } : {}), ...(i === 5 ? { rx: undefined } : {}) }));
    const host = summary({ ...fixture, points });
    const network = host.querySelectorAll("svg")[2];
    const download = network.querySelector('[data-series="0"]')!;
    const upload = network.querySelector('[data-series="1"]')!;
    expect(download.querySelectorAll("polyline")).toHaveLength(3);
    expect(upload.querySelectorAll("polyline")).toHaveLength(2);
  });

  it("draws a zero-valued download even when the upload series is entirely missing", () => {
    const host = summary({ ...fixture, points: fixture.points.map((p) => ({ ...p, tx: undefined })) });
    const network = host.querySelectorAll("svg")[2];
    expect(network.querySelectorAll("polyline")).toHaveLength(1);
    expect(network.querySelector("polyline")?.getAttribute("stroke")).toBe("var(--orange)");
  });

  it("retains optional token values and their last-hour history", () => {
    const host = summary({ ...fixture, usage: { today: { in: 1000 }, per_minute: 25, recent: [0, 15, 25] } });
    expect(cells(host)).toHaveLength(4);
    expect(host.querySelectorAll("svg")).toHaveLength(4);
    expect(host.textContent).toContain("25/分");
    expect(host.textContent).toContain("今天 1K");
  });
});
