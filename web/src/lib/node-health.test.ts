import { describe, expect, it } from "vitest";
import {
  classifyConnection,
  deriveFeatures,
  doctorUnavailable,
  FEATURES,
  fixableChecks,
  fixResult,
  infoChecks,
  summarize,
  type DoctorCheck,
  type DoctorReport,
} from "./node-health";
import type { Machine } from "./types";

// The checks a healthy node reports, in its order (server/internal/node/doctor_rpc_test.go keeps the names in step).
const NAMES = [
  "node.sock",
  "runtime.sock",
  "claude-settings.json",
  "claude-hooks",
  "assistant-settings.json",
  "assistant-hooks",
  "mcp.json",
  "agent-plugin",
  "command-paths",
  "claude-version",
  "herdr",
  "global-permission-hooks",
];

function report(over: Record<string, Partial<DoctorCheck>> = {}): DoctorReport {
  const checks = NAMES.map((name) => ({ name, status: "ok", state: "", detail: `${name} fine`, hint: "", items: [], ...over[name] }) as DoctorCheck);
  return { ok: !checks.some((c) => c.status === "fail"), checks, fixed: [] };
}

const state = (r: DoctorReport, id: string, ctx = {}) => deriveFeatures(r, ctx).find((f) => f.id === id)?.state;

describe("deriveFeatures", () => {
  it("is all ready for a healthy node", () => {
    const f = deriveFeatures(report());
    expect(f.map((x) => x.id)).toEqual(["approvals", "status", "terminal", "mcp", "skills", "runtimes"]);
    expect(f.every((x) => x.state === "ready" && x.finding === "" && x.note === "")).toBe(true);
    expect(summarize(f)).toEqual({ attention: 0, title: "一切正常", off: "" });
  });

  it("covers every check the node reports", () => {
    const covered = new Set(FEATURES.flatMap((f) => f.checks));
    expect(NAMES.filter((n) => !covered.has(n))).toEqual(["global-permission-hooks"]);
  });

  it("takes the worst state among a feature's checks", () => {
    const r = report({
      "node.sock": { status: "fail", detail: "不存在", hint: "确认 hub 已连上这台电脑" },
      "mcp.json": { status: "warn", state: "stale", detail: "过期" },
    });
    // node.sock is under approvals, status and mcp: all three break; the note is the worst check's hint.
    expect(state(r, "approvals")).toBe("broken");
    expect(state(r, "status")).toBe("broken");
    expect(state(r, "mcp")).toBe("broken");
    expect(deriveFeatures(r).find((f) => f.id === "approvals")?.note).toBe("确认 hub 已连上这台电脑");
    // a generated file that is stale or missing points at "Fix", not at a command line
    const stale = report({ "mcp.json": { status: "warn", state: "stale", hint: "重启 node 会重写；或运行 personad node doctor --fix" } });
    expect(deriveFeatures(stale).find((f) => f.id === "mcp")?.note).toContain("修复");
    expect(state(r, "skills")).toBe("ready");
  });

  it("calls a stale generated file 部分可用 and a missing critical one 有问题", () => {
    const stale = report({ "claude-hooks": { status: "warn", state: "stale", detail: "过期：2 处不同", hint: "重启 node 会重写" } });
    expect(state(stale, "approvals")).toBe("partial");
    expect(state(stale, "mcp")).toBe("ready");
    const missing = report({ "mcp.json": { status: "fail", state: "missing", detail: "不存在" } });
    expect(state(missing, "mcp")).toBe("broken");
    expect(state(missing, "approvals")).toBe("ready");
    expect(summarize(deriveFeatures(stale))).toMatchObject({ attention: 1, title: "1 项功能需要处理" });
  });

  it("turns a warning on something optional into 未启用, a failure into 有问题", () => {
    const r = report({ "runtime.sock": { status: "warn", detail: "不存在" } });
    expect(state(r, "runtimes")).toBe("off");
    expect(summarize(deriveFeatures(r))).toEqual({ attention: 0, title: "一切正常", off: "未配置：运行时接入" });
    expect(state(report({ "runtime.sock": { status: "fail", detail: "连不上" } }), "runtimes")).toBe("broken");
  });

  it("needs herdr only when it is the computer's engine", () => {
    const r = report({ herdr: { status: "warn", detail: "连不上 herdr 的 socket" } });
    expect(state(r, "terminal", { engine: "herdr" })).toBe("broken");
    expect(state(r, "terminal", { engine: "herdr-both" })).toBe("broken");
    expect(state(r, "terminal", {})).toBe("broken"); // never chosen: herdr
    // An Orca computer stands on Orca's line, not herdr's: without one (an older node) the feature is left out (#17037).
    expect(state(r, "terminal", { engine: "orca" })).toBeUndefined();
    expect(state(r, "terminal", { engine: "paseo" })).toBe("off");
    expect(summarize(deriveFeatures(r, { engine: "paseo" })).off).toBe("未配置：终端与引擎");
  });

  it("counts Orca as the engine where the computer's set enables it (#17037)", () => {
    const withOrca = (status: "ok" | "warn") => {
      const r = report({ herdr: { status: "warn", detail: "连不上 herdr 的 socket" } });
      r.checks.push({ name: "orca", status, detail: "Orca", hint: status === "warn" ? "打开 Orca" : "" });
      return r;
    };
    expect(state(withOrca("ok"), "terminal", { hosts: ["orca"] })).toBe("ready"); // herdr's socket does not matter
    expect(state(withOrca("warn"), "terminal", { hosts: ["orca"] })).toBe("broken");
    expect(deriveFeatures(withOrca("warn"), { hosts: ["orca"] }).find((f) => f.id === "terminal")?.note).toBe("打开 Orca");
    expect(state(withOrca("ok"), "terminal", { hosts: ["herdr", "orca"] })).toBe("broken"); // herdr in the set too
    expect(state(withOrca("warn"), "terminal", { hosts: ["herdr-core"] })).toBe("off"); // neither: herdr's line, not needed
    expect(deriveFeatures(withOrca("warn"), { hosts: ["herdr"] }).map((f) => f.id)).not.toContain("other"); // never "Other checks"
    expect(summarize(deriveFeatures(withOrca("ok"), { hosts: ["orca"] })).off).toBe(""); // the engine is not "off"
  });

  it("leaves a feature out when an older node does not report its checks, and shows an unknown bad check", () => {
    const old: DoctorReport = { ok: true, checks: report().checks.filter((c) => c.name !== "agent-plugin" && c.name !== "claude-version") };
    expect(deriveFeatures(old).map((f) => f.id)).not.toContain("skills");
    const next = report();
    next.checks.push({ name: "disk-space", status: "warn", detail: "剩 200 MB", hint: "清理磁盘" });
    const other = deriveFeatures(next).find((f) => f.id === "other");
    expect(other).toMatchObject({ state: "partial", finding: "剩 200 MB", note: "清理磁盘" });
    // a broken check without a separate hint keeps finding empty so the detail is not repeated twice
    const detailOnly = report();
    detailOnly.checks.push({ name: "path-access", status: "fail", detail: "读不了 /tmp/link", hint: "" });
    expect(deriveFeatures(detailOnly).find((f) => f.id === "other")).toMatchObject({ state: "broken", finding: "", note: "读不了 /tmp/link" });
    // stale / missing generated files and optional "off" features keep finding empty
    const staleAndOff = report({
      "mcp.json": { status: "warn", state: "stale", detail: "过期", hint: "重启 node" },
      "runtime.sock": { status: "warn", detail: "不存在", hint: "没有 node 在监听" },
    });
    expect(deriveFeatures(staleAndOff).find((f) => f.id === "mcp")?.finding).toBe("");
    expect(deriveFeatures(staleAndOff).find((f) => f.id === "runtimes")?.finding).toBe("");
    // a fine unknown check hides nothing and adds nothing
    const fine = report();
    fine.checks.push({ name: "disk-space", status: "ok", detail: "" });
    expect(deriveFeatures(fine).some((f) => f.id === "other")).toBe(false);
  });

  it("keeps the user's own settings out of the features and in an information row", () => {
    const r = report({ "global-permission-hooks": { status: "warn", detail: "1 个同步的 PermissionRequest 钩子（x）", hint: "可能互相影响" } });
    expect(deriveFeatures(r).every((f) => f.state === "ready")).toBe(true);
    expect(infoChecks(r).map((c) => c.name)).toEqual(["global-permission-hooks"]);
    expect(infoChecks(report())).toEqual([]);
  });
});

describe("fix", () => {
  it("is offered for stale and missing generated files only", () => {
    expect(fixableChecks(report())).toEqual([]);
    const r = report({
      "claude-hooks": { status: "warn", state: "stale" },
      "mcp.json": { status: "fail", state: "missing" },
      "command-paths": { status: "fail", state: "" },
      "global-permission-hooks": { status: "warn" },
    });
    expect(fixableChecks(r).map((c) => c.name)).toEqual(["claude-hooks", "mcp.json"]);
  });

  it("says what it did", () => {
    expect(fixResult({ ok: true, checks: [], fixed: ["a", "b"] })).toEqual({ text: "已重新生成 2 个文件", tone: "ok" });
    expect(fixResult({ ok: true, checks: [] })).toEqual({ text: "没有需要重新生成的文件", tone: "ok" });
    expect(fixResult({ ok: false, checks: [{ name: "fix", status: "fail", detail: "重新生成失败：磁盘已满" }] })).toEqual({ text: "重新生成失败：磁盘已满", tone: "error" });
  });
});

describe("doctorUnavailable", () => {
  it("tells an old node, an old hub, a device that may not and an offline computer", () => {
    expect(doctorUnavailable({ status: 502, message: "unknown method node.doctor", isJson: true })).toBe("old-node");
    expect(doctorUnavailable({ status: 404, message: "404 page not found", isJson: false })).toBe("old-hub");
    expect(doctorUnavailable({ status: 404, message: "unknown machine", isJson: true })).toBeNull();
    expect(doctorUnavailable({ status: 403, message: "a computer's self-check needs a full device", isJson: true })).toBe("forbidden");
    expect(doctorUnavailable({ status: 503, message: "machine is offline", isJson: true })).toBe("offline");
    expect(doctorUnavailable({ status: 502, message: "timeout", isJson: true })).toBeNull();
    expect(doctorUnavailable({ message: "连不上 hub" })).toBeNull();
  });
});

describe("classifyConnection", () => {
  const off = (error: string, over: Partial<Machine> = {}): Machine => ({ id: "m", name: "m", online: false, error, ...over });

  it("reads the hub's error text", () => {
    const kind = (e: string, o: Partial<Machine> = {}) => classifyConnection(off(e, o))?.kind ?? null;
    expect(kind("me@host: Permission denied (publickey).")).toBe("auth");
    expect(kind("Host key verification failed.")).toBe("auth");
    expect(kind("ssh: connect to host x port 22: Connection refused")).toBe("unreachable");
    expect(kind("ssh: Could not resolve hostname x: Name or service not known")).toBe("unreachable");
    expect(kind("node exited")).toBe("unreachable");
    expect(kind("waiting for Persona Link", { kind: "link" })).toBe("unreachable");
    expect(kind("ssh: connect to host x port 22: Operation timed out")).toBe("timeout");
    expect(kind("context deadline exceeded")).toBe("timeout");
    expect(kind("unknown method node.info")).toBe("version");
  });

  it("puts a timeout that mentions the connection under timeout", () => {
    expect(classifyConnection(off("ssh: connect to host x port 22: Connection timed out"))?.kind).toBe("timeout");
  });

  it("guesses nothing from text it does not know, an online machine or no text", () => {
    expect(classifyConnection(off("something odd happened"))).toBeNull();
    expect(classifyConnection(off(""))).toBeNull();
    expect(classifyConnection({ ...off("Permission denied"), online: true })).toBeNull();
  });

  it("gives a next step that fits how the computer is reached", () => {
    expect(classifyConnection(off("Permission denied", { kind: "ssh" }))?.next).toContain("SSH");
    expect(classifyConnection(off("Permission denied", { kind: "link" }))?.next).toContain("Persona Link");
    expect(classifyConnection(off("connection refused", { kind: "link" }))?.next).toContain("Persona Link");
    expect(classifyConnection(off("version mismatch", { kind: "ssh" }))?.next).toContain("重新部署");
    expect(classifyConnection(off("version mismatch", { kind: "local" }))?.next).toContain("本地升级");
  });
});
