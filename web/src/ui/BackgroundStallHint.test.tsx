import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Session } from "../lib/types";
import { BackgroundStallHint } from "./BackgroundStallHint";

const working: Session = { machine_id: "m", session_id: "s", title: "T", cwd: "/w", status: "working", state: "working", background_working: true };

describe("BackgroundStallHint (#13744)", () => {
  it("shows a muted line with a tooltip for a stalled Working session", () => {
    const html = renderToStaticMarkup(<BackgroundStallHint session={{ ...working, background_stalled_since: new Date(Date.now() - 3 * 3600_000 - 60_000).toISOString() }} />);
    expect(html).toContain("data-bg-stalled");
    expect(html).toContain("后台命令已 3 小时无活动");
    expect(html).toContain("当前状态仍计为「工作中」");
  });
  it("renders nothing without a stall or when the commands only wait", () => {
    expect(renderToStaticMarkup(<BackgroundStallHint session={working} />)).toBe("");
    expect(renderToStaticMarkup(<BackgroundStallHint session={{ ...working, background_waiting: true, background_stalled_since: "2026-11-13T01:00:00Z" }} />)).toBe("");
  });
});
