// @vitest-environment jsdom
// NodeHealth summary rows (#16844): broken/partial features show both what failed (`finding`) and the recovery hint
// without requiring the user to expand "Show all checks", while ready/off/fixable rows keep a single line.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  const kept = new Map<string, string>();
  const g = globalThis as unknown as { localStorage: Pick<Storage, "getItem" | "setItem" | "removeItem"> };
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});
import { NodeHealth } from "./NodeHealth";
import type { DoctorReport } from "../../lib/node-health";
import type { Machine } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

const machine: Machine = { id: "mac-1", name: "mac-1", online: true };

let report: DoctorReport;
let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};

beforeEach(() => {
  report = {
    ok: false,
    checks: [
      { name: "node.sock", status: "ok", state: "", detail: "已应答", hint: "", items: [] },
      { name: "runtime.sock", status: "warn", state: "", detail: "不存在", hint: "没有 node 在监听", items: [] },
      { name: "claude-settings.json", status: "ok", state: "current", detail: "一致", hint: "", items: [] },
      { name: "claude-hooks", status: "ok", state: "current", detail: "一致", hint: "", items: [] },
      { name: "assistant-settings.json", status: "ok", state: "current", detail: "一致", hint: "", items: [] },
      { name: "assistant-hooks", status: "ok", state: "current", detail: "一致", hint: "", items: [] },
      { name: "mcp.json", status: "warn", state: "stale", detail: "过期：1 处不同", hint: "重启 node", items: [] },
      { name: "agent-plugin", status: "ok", state: "current", detail: "一致", hint: "", items: [] },
      { name: "command-paths", status: "ok", state: "", detail: "都可执行", hint: "", items: [] },
      { name: "claude-version", status: "ok", state: "", detail: "2.1.0", hint: "", items: [] },
      { name: "herdr", status: "ok", state: "", detail: "监听中", hint: "", items: [] },
      {
        name: "global-permission-hooks",
        status: "warn",
        state: "",
        detail: "1 个同步的 PermissionRequest 钩子（example-hook）",
        hint: "Persona 不会改 /home/me/.claude/settings.json，需要时自己检查",
        items: ["example-hook"],
      },
      {
        name: "path-access",
        status: "fail",
        state: "",
        detail: "读不了 /home/me/.claude/skills/loop → /home/me/.agents/skills/loop（too many levels of symbolic links）：每个 Claude 启动时会失败或卡住",
        hint: "这个软链接形成了循环指向；删除失效的软链接，或将它重新指向真实的目录",
        items: ["读不了：/home/me/.claude/skills/loop → /home/me/.agents/skills/loop"],
      },
    ],
    fixed: [],
  };
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json(report))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("NodeHealth", () => {
  it("shows both what failed and the hint on broken Other checks without expanding all checks", async () => {
    act(() => root.render(<NodeHealth machine={machine} heading={<h2>Health</h2>} />));
    await flush();

    // Collapsed by default: all checks list is not rendered yet.
    expect(host.querySelector("#checks-mac-1")).toBeNull();

    const otherRow = host.querySelector<HTMLLIElement>('li[data-feature="other"]');
    expect(otherRow).not.toBeNull();
    expect(otherRow!.textContent).toContain("too many levels of symbolic links");
    expect(otherRow!.textContent).toContain("循环指向");
    expect(otherRow!.querySelectorAll("p").length).toBe(3); // title + finding + hint

    // Ready, off, and fixable stale rows keep a single description/note line (2 <p> elements: title + description/note).
    expect(host.querySelectorAll('li[data-feature="approvals"] p').length).toBe(2);
    expect(host.querySelectorAll('li[data-feature="runtimes"] p').length).toBe(2);
    expect(host.querySelectorAll('li[data-feature="mcp"] p').length).toBe(2);

    // Info row shows detail and the settings file path in its hint.
    const infoRow = host.querySelector<HTMLLIElement>('li[data-info="global-permission-hooks"]');
    expect(infoRow?.textContent).toContain("/home/me/.claude/settings.json");
  });
});
