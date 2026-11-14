import { mainConversationMachine } from "./feed-cache";
import { describe, expect, it } from "vitest";
import { mainRuntimeDetails, mainRuntimeSummary, type MainExecution } from "./main-runtime";

const report: MainExecution = { session_id: "main", machine_id: "routing-node", source: "native", state: "running", harness: "codex", location: { host: "execution-host" } };

describe("main conversation runtime", () => {
  it("says only the main chat's name and plain state on the Assistant screen, no engine or place (audit item 80)", () => {
    expect(mainRuntimeSummary("main", report)).toBe("主对话 · 运行中");
    expect(mainRuntimeSummary("main", { ...report, location: { host: "execution-host", container: "example-container" } })).toBe("主对话 · 运行中");
    expect(mainRuntimeSummary("main", { ...report, source: "runtime", proxy: "Relay", location: undefined })).toBe("主对话 · 运行中");
    expect(mainRuntimeSummary("main", { ...report, state: "offline" })).toBe("主对话 · 离线");
    expect(mainRuntimeSummary("main", { ...report, state: "stopped" })).toBe("主对话 · 已停止");
    expect(mainRuntimeSummary("main", { ...report, state: "starting" })).toBe("主对话 · 启动中…");
    for (const state of ["running", "offline", "stopped"] as const) {
      const line = mainRuntimeSummary("main", { ...report, state, harness: "claude", location: { host: "execution-host", container: "example-container" } });
      expect(line).not.toMatch(/Claude|Codex|execution-host|example-container|容器/);
    }
  });

  it("says nothing when nothing is known: no report, another conversation's, or an unknown state (audit item 80)", () => {
    expect(mainRuntimeSummary("main", { session_id: "main", machine_id: "routing-node", state: "unknown" })).toBe("");
    expect(mainRuntimeSummary("main", { ...report, state: "unknown" })).toBe("");
    expect(mainRuntimeSummary("another", report)).toBe("");
    expect(mainRuntimeSummary("main", undefined)).toBe("");
  });

  it("separates no main from startup", () => {
    expect(mainRuntimeSummary(undefined)).toBe("主对话还没开始");
    expect(mainRuntimeSummary(undefined, undefined, true)).toBe("主对话 · 启动中…");
  });

  it("keeps where and with what it runs for Settings, from the execution side, never the routing node", () => {
    expect(mainRuntimeDetails("main", report)).toEqual({ place: "execution-host", runtime: "Codex" });
    expect(mainRuntimeDetails("main", { ...report, harness: "claude", location: { host: "execution-host", container: "example-container" } }))
      .toEqual({ place: "容器 example-container", runtime: "Claude Code" });
    // a proxy is the way in, not a location; a missing location is left out, not said to be unreported
    expect(mainRuntimeDetails("main", { ...report, source: "runtime", proxy: "Relay", location: undefined })).toEqual({ place: "经 Relay 接入", runtime: "Codex" });
    expect(mainRuntimeDetails("main", { ...report, location: undefined })).toEqual({ place: undefined, runtime: "Codex" });
    expect(mainRuntimeDetails("main", { ...report, state: "offline" }).place).toBe("上次运行于 execution-host");
    expect(mainRuntimeDetails("main", { ...report, harness: "other", harness_label: "Example Runtime" }).runtime).toBe("Example Runtime");
    expect(mainRuntimeDetails("another", report)).toEqual({});
    expect(mainRuntimeDetails(undefined, report)).toEqual({});
    expect(mainRuntimeDetails("main", undefined)).toEqual({});
  });

  it("preserves the main route when the configured device changes", () => {
    expect(mainConversationMachine({ main: "main", main_machine_id: "old", machine_id: "new", conversations: [] })).toBe("old");
    expect(mainConversationMachine({ main: "main", machine_id: "legacy", conversations: [] })).toBe("legacy");
  });
});
