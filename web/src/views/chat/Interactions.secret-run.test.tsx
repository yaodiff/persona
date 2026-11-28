// @vitest-environment jsdom
// The assistant's password card (`secret_run`, #1137, #1581) on the web: it shows the computer, the purpose and every
// command exactly as they will run, a password field, Decline and Run. Run sends the typed password once, inside the
// answer, and empties the field; Enter never sends; the password is never in storage, the store, the URL or a log, and
// the field is emptied on Decline, when the card changes and when it goes. No Allow, no Always allow, no hand back; the
// quick paths (a row's answers, a notification's Allow) never take it, and the one way out to the hub (answerCard) sends
// only a bare decline or an allow with the password alone. The hub is faked (apiOp).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown>; headers?: Record<string, string> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown>; headers?: Record<string, string> }) => {
    calls.push({ path, body: init.json, ...(init.headers ? { headers: init.headers } : {}) });
    return {};
  }),
}));
import { InteractionCard } from "./Interactions";
import { setToken } from "../../lib/api";
import { answerCard } from "../../lib/card-answer";
import { NOTE_MESSAGE, planClick } from "../../lib/notification-answer";
import { quickAnswers } from "../../lib/quick-answers";
import { applyCompleteSessions, getState } from "../../lib/store";
import { secretRunInput } from "../../lib/tool-detail";
import { initI18n } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll("button")].map((b) => b.textContent?.trim() || b.getAttribute("aria-label"));
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const field = () => host.querySelector("input") as HTMLInputElement;
const type = (value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field(), value);
    field().dispatchEvent(new Event("input", { bubbles: true }));
  });

const PW = "correct horse 7";
const REFUSED = "请在卡片上操作：输入密码后点击「运行」，或点击「拒绝」。";
const COMMANDS = [
  "sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/com.example.backup.plist",
  'sudo -S -k -p \'\' sh -c "echo  two  spaces > /tmp/x"',
];
const ENV_COMMANDS = [
  '/usr/bin/curl --fail --header "Authorization: Bearer $PERSONA_SECRET" https://example.test/one',
  '/usr/bin/env sh -c \'printf "%s" "$PERSONA_SECRET" | /usr/bin/base64\'',
];
const ENV_WARNINGS = [
  {
    lang: "en",
    warning: "Warning: the typed password goes directly to every command listed above via PERSONA_SECRET. Encoded output may evade redaction.",
    decline: "Decline",
    run: "Run",
  },
  {
    lang: "zh-Hans",
    warning: "警告：你输入的密码将通过 PERSONA_SECRET 直接传递给以上列出的每条命令，经过编码的输出可能绕过脱敏保护。",
    decline: "拒绝",
    run: "运行",
  },
  {
    lang: "ja",
    warning: "警告：入力したパスワードは PERSONA_SECRET を通じて上記の各コマンドに直接渡されます。エンコードされた出力はマスキングを回避する場合があります。",
    decline: "拒否",
    run: "実行",
  },
];
const card = (input: Record<string, unknown> = {}, extra: Partial<Interaction> = {}): Interaction => ({
  id: "ix_secret", machine_id: "m", session_id: "s", session_title: "Persona", kind: "permission", tool_name: "secret_run",
  tool_input: { machine: "macmini", machine_name: "Mac mini", purpose: "Install the launch daemon for the backup job", commands: COMMANDS, delivery: "stdin", ...input },
  summary: "Mac mini\nInstall the launch daemon for the backup job\n$ sudo -S -k -p '' true",
  fields: [{ key: "password", label: "Password", secret: true }],
  ...extra,
});

const session = (s: Partial<Session> = {}) => {
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s", ...s } as Session] }, interactions: [], answered: {} });
};
const render = (ix: Interaction) => act(() => root.render(<InteractionCard ix={ix} />));

/** Whether `needle` is anywhere in `value`: strings, arrays, objects, Maps and Sets, however deep. */
function holds(value: unknown, needle: string, seen = new WeakSet<object>()): boolean {
  if (typeof value === "string") return value.includes(needle);
  if (!value || typeof value !== "object") return false;
  if (seen.has(value)) return false;
  seen.add(value);
  if (value instanceof Map) return [...value.entries()].some(([k, v]) => holds(k, needle, seen) || holds(v, needle, seen));
  if (value instanceof Set) return [...value].some((v) => holds(v, needle, seen));
  return Object.entries(value).some(([k, v]) => k.includes(needle) || holds(v, needle, seen));
}

beforeEach(() => {
  calls.length = 0;
  session();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("secret_run 的 delivery 解析", () => {
  it("只有明确的 env 才选择环境变量，缺省和其他值都保持 stdin，命令逐字保留", () => {
    const raw = { machine: "macmini", machine_name: " Mac mini ", purpose: " Use the token ", commands: ENV_COMMANDS };
    const parsed = { machine: "Mac mini", purpose: "Use the token", commands: ENV_COMMANDS };
    expect(secretRunInput({ ...raw, delivery: "env" })).toEqual({ ...parsed, delivery: "env" });
    for (const delivery of [undefined, "stdin", "ENV", "env ", "other", true]) {
      expect(secretRunInput({ ...raw, delivery }), JSON.stringify(delivery)).toEqual({ ...parsed, delivery: "stdin" });
    }
  });
});

describe("密码卡片（secret_run）", () => {
  it("写明电脑、用途和逐字的命令，不指向别的设备", () => {
    render(card());
    expect(host.textContent).toContain("用你的密码在 Mac mini 上运行");
    expect(host.textContent).toContain("Install the launch daemon for the backup job");
    expect([...host.querySelectorAll("[data-secret-run-command]")].map((e) => e.textContent)).toEqual(COMMANDS);
    expect(host.textContent).not.toContain("iPhone");
    // the commands are text, never something to edit: the password field is the card's one input
    expect(host.querySelectorAll("input, textarea, [contenteditable]").length).toBe(1);
  });

  it("有密码框、拒绝和运行；没有允许、总是允许和交还电脑", () => {
    render(card());
    expect(buttons()).toEqual(["拒绝", "运行"]);
    const input = field();
    expect(input.type).toBe("password");
    expect(input.placeholder).toBe("Password");
    expect(input.getAttribute("autocomplete")).toBe("one-time-code");
    expect(input.getAttribute("spellcheck")).toBe("false");
    expect(input.getAttribute("autocapitalize")).toBe("off");
    expect(input.getAttribute("autocorrect")).toBe("off");
    // no form to submit, no name a password manager files it under
    expect(input.closest("form")).toBeNull();
    expect(input.hasAttribute("name")).toBe(false);
  });

  it("没填密码时运行点不了，填了才能点，删空又点不了", async () => {
    render(card());
    expect(button("运行")!.disabled).toBe(true);
    await act(async () => button("运行")!.click());
    expect(calls).toEqual([]);
    type(PW);
    expect(button("运行")!.disabled).toBe(false);
    type("");
    expect(button("运行")!.disabled).toBe(true);
    // the hub refuses a password of spaces alone
    type("   ");
    expect(button("运行")!.disabled).toBe(true);
  });

  it("tool_input 没有命令时用 summary 代替；连 summary 也没有时运行点不了", () => {
    render(card({ commands: undefined }));
    expect(host.querySelector("[data-secret-run-summary]")!.textContent).toBe("Mac mini\nInstall the launch daemon for the backup job\n$ sudo -S -k -p '' true");
    type(PW);
    expect(button("运行")!.disabled).toBe(false);
    render(card({ commands: undefined }, { id: "ix_secret_2", summary: undefined }));
    type(PW);
    expect(button("运行")!.disabled).toBe(true);
  });

  it("运行只发一次 allow:true 和填的密码，发出时密码框就清空", async () => {
    render(card());
    type(PW);
    await act(async () => button("运行")!.click());
    expect(field().value).toBe("");
    // a second click finds the field empty: nothing more goes out
    await act(async () => button("运行")!.click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/ix_secret/resolve", body: { allow: true, values: { password: PW } } }]);
    expect(field().value).toBe("");
  });

  it("在密码框里按 Enter 不会发出，只把焦点移开", async () => {
    render(card());
    type(PW);
    await act(async () => field().focus());
    expect(document.activeElement).toBe(field());
    await act(async () => {
      field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", bubbles: true, cancelable: true }));
    });
    await flush();
    expect(calls).toEqual([]);
    expect(document.activeElement).not.toBe(field());
    expect(field().value).toBe(PW);
  });

  it("拒绝只发 allow:false，已填的密码清空", async () => {
    render(card());
    type(PW);
    await act(async () => button("拒绝")!.click());
    expect(field().value).toBe("");
    await flush();
    expect(calls).toEqual([{ path: "/interactions/ix_secret/resolve", body: { allow: false } }]);
  });

  it("换成另一张卡时，上一张填的密码不留下", () => {
    render(card());
    type(PW);
    render(card({}, { id: "ix_secret_2" }));
    expect(field().value).toBe("");
    expect(button("运行")!.disabled).toBe(true);
  });

  it("卡片离开页面时，密码框里的值也清空", () => {
    render(card());
    type(PW);
    const input = field();
    act(() => root.render(<></>));
    expect(input.isConnected).toBe(false);
    expect(input.value).toBe("");
  });

  it("整个流程不写 localStorage / sessionStorage，密码不进 store、地址和日志", async () => {
    const writes = vi.spyOn(Storage.prototype, "setItem");
    const logs = (["log", "info", "warn", "error", "debug"] as const).map((m) => vi.spyOn(console, m));
    render(card());
    type(PW);
    await act(async () => button("运行")!.click());
    await flush();
    expect(calls).toHaveLength(1);
    expect(writes).not.toHaveBeenCalled();
    expect(holds(getState(), PW)).toBe(false);
    // the card just answered keeps which field was filled in, not what was typed (as the hub's history keeps it)
    expect(getState().answered.ix_secret?.resolution).toEqual({ allow: true, values: { password: "<redacted>" } });
    expect(location.href).not.toContain(PW);
    expect(logs.some((l) => holds(l.mock.calls, PW))).toBe(false);
    expect(host.innerHTML).not.toContain(PW);
  });

  it("没有电脑名时用机器 id", () => {
    render(card({ machine_name: undefined }));
    expect(host.textContent).toContain("用你的密码在 macmini 上运行");
  });

  it("答复出口只放行两种：不带别的内容的拒绝，和只带密码的允许", async () => {
    const refused: Record<string, unknown>[] = [
      { allow: true },
      { allow: true, values: {} },
      { allow: true, values: { password: "" } },
      { allow: true, values: { password: "  " } },
      { allow: true, values: { password: 7 } },
      { allow: true, values: ["x"] },
      { allow: true, values: null },
      { allow: true, values: { password: "x", other: "y" } },
      { allow: true, values: { pass: "x" } },
      { allow: true, values: { password: "x" }, always: true },
      { allow: true, values: { password: "x" }, message: "x" },
      { allow: false, message: "x" },
      { allow: false, values: { password: "x" } },
      { dismiss: true },
      {},
    ];
    for (const body of refused) expect(await answerCard(card(), body), JSON.stringify(body)).toEqual({ ok: false, error: REFUSED });
    expect(calls).toEqual([]);
    expect(await answerCard(card(), { allow: false })).toEqual({ ok: true, already: false });
    expect(await answerCard(card(), { allow: true, values: { password: "x" } })).toEqual({ ok: true, already: false });
    expect(calls).toEqual([
      { path: "/interactions/ix_secret/resolve", body: { allow: false } },
      { path: "/interactions/ix_secret/resolve", body: { allow: true, values: { password: "x" } } },
    ]);
  });

  it("不能审批的会话：密码框、拒绝和运行都用不了", async () => {
    session({ source_kind: "runtime", capabilities: { approve: false } } as Partial<Session>);
    await act(async () => {
      root.render(<InteractionCard ix={card()} />);
      await vi.dynamicImportSettled();
    });
    await flush();
    expect(host.querySelector("fieldset")!.disabled).toBe(true);
    expect(button("拒绝")!.matches(":disabled")).toBe(true);
    expect(button("运行")!.matches(":disabled")).toBe(true);
    expect(field().matches(":disabled")).toBe(true);
  });

  it("快捷路径不接这张卡：列表行没有一键答复，通知上的允许不发出（没带 fields 也一样）", () => {
    for (const ix of [card(), card({}, { fields: [] })]) {
      expect(quickAnswers(ix)).toBeNull();
      const click = (action: "allow" | "deny") => planClick({ type: NOTE_MESSAGE, ix: "ix_secret", action, until: Date.now() + 60_000 }, [ix], Date.now());
      expect(click("allow")).toBeUndefined();
      expect(click("deny")).toBeUndefined();
    }
  });

  it.each(ENV_WARNINGS)("env 审批在 $lang 明确警告每条命令都会收到 PERSONA_SECRET，命令仍逐字可见", async ({ lang, warning, decline, run }) => {
    await initI18n(lang);
    try {
      await act(async () => {
        root.render(<InteractionCard ix={card({ delivery: "env", commands: ENV_COMMANDS })} />);
        await vi.dynamicImportSettled();
      });
      expect(host.querySelector('[role="alert"]')?.textContent).toBe(warning);
      expect([...host.querySelectorAll("[data-secret-run-command]")].map((e) => e.textContent)).toEqual(ENV_COMMANDS);
      expect(buttons()).toEqual([decline, run]);
      expect(host.querySelectorAll("input").length).toBe(1);
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("缺省和 stdin 都没有环境变量警告，保留原来的密码说明", () => {
    for (const delivery of [undefined, "stdin", "ENV"]) {
      render(card({ delivery }));
      expect(host.querySelector('[role="alert"]')).toBeNull();
      expect(host.textContent).not.toContain("PERSONA_SECRET");
      expect(host.textContent).not.toContain("经过编码的输出");
      expect(host.textContent).toContain("仅本次使用，不会保存，助手也无法查看。");
    }
  });

  it("env 运行仍只发密码并立即清空；新一次运行要重填，密码不进存储、store、地址或日志", async () => {
    const writes = vi.spyOn(Storage.prototype, "setItem");
    const logs = (["log", "info", "warn", "error", "debug"] as const).map((m) => vi.spyOn(console, m));
    render(card({ delivery: "env", commands: ENV_COMMANDS }));
    type(PW);
    await act(async () => button("运行")!.click());
    expect(field().value).toBe("");
    await act(async () => button("运行")!.click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/ix_secret/resolve", body: { allow: true, values: { password: PW } }, headers: { "X-Persona-Features": "secret_run_env" } }]);
    expect(getState().answered.ix_secret?.resolution).toEqual({ allow: true, values: { password: "<redacted>" } });

    render(card({ delivery: "env", commands: ENV_COMMANDS }, { id: "ix_secret_env_fresh" }));
    expect(field().value).toBe("");
    expect(button("运行")!.disabled).toBe(true);
    expect(calls).toHaveLength(1);
    expect(writes).not.toHaveBeenCalled();
    expect(holds(getState(), PW)).toBe(false);
    expect(location.href).not.toContain(PW);
    expect(logs.some((l) => holds(l.mock.calls, PW))).toBe(false);
    expect(host.innerHTML).not.toContain(PW);
  });

  it("env 不能由快捷答复、通知、Always、其他答复形状或键盘批准", async () => {
    const ix = card({ delivery: "env", commands: ENV_COMMANDS }, { always_label: "Always allow these commands" });
    for (const candidate of [ix, card({ delivery: "env", commands: ENV_COMMANDS }, { fields: [] })]) {
      expect(quickAnswers(candidate)).toBeNull();
      const click = (action: "allow" | "deny") => planClick({ type: NOTE_MESSAGE, ix: candidate.id, action, until: Date.now() + 60_000 }, [candidate], Date.now());
      expect(click("allow")).toBeUndefined();
      expect(click("deny")).toBeUndefined();
    }
    const refused: Record<string, unknown>[] = [
      { allow: true },
      { allow: true, always: true },
      { allow: true, values: { password: PW }, always: true },
      { allow: true, values: { password: PW }, remembered: true },
      { allow: true, values: { password: PW }, delivery: "env" },
      { allow: true, values: { password: PW, PERSONA_SECRET: PW } },
      { dismiss: true },
    ];
    for (const body of refused) expect(await answerCard(ix, body), JSON.stringify(body)).toEqual({ ok: false, error: REFUSED });
    expect(calls).toEqual([]);

    render(ix);
    expect(buttons()).toEqual(["拒绝", "运行"]);
    type(PW);
    await act(async () => field().focus());
    await act(async () => {
      field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", bubbles: true, cancelable: true }));
    });
    const surface = host.querySelector<HTMLElement>("[data-card-keys]")!;
    await act(async () => surface.focus());
    await act(async () => {
      surface.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", bubbles: true, cancelable: true }));
      surface.dispatchEvent(new KeyboardEvent("keydown", { key: "1", code: "Digit1", bubbles: true, cancelable: true }));
    });
    await flush();
    expect(calls).toEqual([]);
    expect(field().value).toBe(PW);
  });

  it("env 的直接批准必须来自显示警告的卡片，并只在该请求声明 secret_run_env", async () => {
    const ix = card({ delivery: "env", commands: ENV_COMMANDS });
    expect(await answerCard(ix, { allow: true, values: { password: PW } })).toEqual({ ok: false, error: REFUSED });
    expect(calls).toEqual([]);
    expect(await answerCard(ix, { allow: true, values: { password: PW } }, { handoffCard: true })).toEqual({ ok: true, already: false });
    expect(calls).toEqual([{
      path: "/interactions/ix_secret/resolve",
      body: { allow: true, values: { password: PW } },
      headers: { "X-Persona-Features": "secret_run_env" },
    }]);
  });

  it("reports owner card seen through authenticated api() and retries after non-2xx failure on reconnect", async () => {
    const prevConn = getState().conn;
    setToken("synthetic-owner-tok");
    const seenReqs: { url: string; method?: string; auth: string | null }[] = [];
    const responses: Response[] = [
      new Response(JSON.stringify({ error: "temporary failure" }), { status: 503, headers: { "Content-Type": "application/json" } }),
      new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } }),
      new Response(JSON.stringify({ error: "forbidden" }), { status: 403, headers: { "Content-Type": "application/json" } }),
    ];
    const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      seenReqs.push({ url: String(input), method: init?.method, auth: headers.get("Authorization") });
      return responses.shift() ?? new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", mockFetch);
    try {
      // Offline: defers /seen until connection recovers.
      Object.assign(getState(), { conn: "offline" });
      render(card({}, { id: "ix_secret_seen_1", seen_ttl_seconds: 600 }));
      await flush();
      expect(seenReqs).toEqual([]);

      // Connecting: first POST fails with 503 -> carries Bearer auth and clears sent cache so reconnect retries.
      act(() => { applyCompleteSessions([getState().sessions.m[0]], [], { conn: "connecting" }); });
      await flush();
      expect(seenReqs).toEqual([
        { url: "/api/v1/interactions/ix_secret_seen_1/seen", method: "POST", auth: "Bearer synthetic-owner-tok" },
      ]);

      // Hub reconnects (conn -> online via store subscription): retries automatically without remounting and succeeds.
      act(() => { applyCompleteSessions([getState().sessions.m[0]], [], { conn: "online" }); });
      await flush();
      expect(seenReqs).toHaveLength(2);
      expect(seenReqs[1]).toEqual({
        url: "/api/v1/interactions/ix_secret_seen_1/seen", method: "POST", auth: "Bearer synthetic-owner-tok",
      });

      // Re-rendering after success does NOT send a duplicate /seen.
      render(card({}, { id: "ix_secret_seen_1", seen_ttl_seconds: 600 }));
      await flush();
      expect(seenReqs).toHaveLength(2);

      // Permanent 403 failure does NOT spam retries on subsequent reconnects.
      render(card({}, { id: "ix_secret_seen_403", seen_ttl_seconds: 600 }));
      await flush();
      expect(seenReqs).toHaveLength(3);
      act(() => { applyCompleteSessions([getState().sessions.m[0]], [], { conn: "connecting" }); });
      act(() => { applyCompleteSessions([getState().sessions.m[0]], [], { conn: "online" }); });
      await flush();
      expect(seenReqs).toHaveLength(3);

      // Already-seen card (seen_at set) does NOT send /seen.
      render(card({}, { id: "ix_secret_seen_already", seen_ttl_seconds: 600, seen_at: "2026-11-27T00:00:00Z" }));
      await flush();
      expect(seenReqs).toHaveLength(3);

      // Supervisor-held card (delegating: true) does NOT send /seen until released (delegating: false).
      render(card({}, { id: "ix_secret_held", seen_ttl_seconds: 600, delegating: true }));
      await flush();
      expect(seenReqs).toHaveLength(3);
      render(card({}, { id: "ix_secret_held", seen_ttl_seconds: 600, delegating: false }));
      await flush();
      expect(seenReqs).toHaveLength(4);
      expect(seenReqs[3]).toEqual({
        url: "/api/v1/interactions/ix_secret_held/seen", method: "POST", auth: "Bearer synthetic-owner-tok",
      });

      // Generic secret-field permission card with seen_ttl_seconds also reports /seen via api().
      render({
        id: "ix_generic_secret_field", machine_id: "m", session_id: "s", kind: "permission",
        tool_name: "custom_secret_tool", summary: "Provide token",
        fields: [{ key: "token", label: "Token", secret: true }], seen_ttl_seconds: 600,
      });
      await vi.dynamicImportSettled();
      await flush();
      expect(seenReqs).toHaveLength(5);
      expect(seenReqs[4]).toEqual({
        url: "/api/v1/interactions/ix_generic_secret_field/seen", method: "POST", auth: "Bearer synthetic-owner-tok",
      });
    } finally {
      Object.assign(getState(), { conn: prevConn });
      vi.unstubAllGlobals();
      setToken("");
    }
  });

  it("defers /seen while the browser tab is hidden and sends once when it becomes visible", async () => {
    const prevConn = getState().conn;
    const prevVisibility = Object.getOwnPropertyDescriptor(document, "visibilityState");
    let vis: DocumentVisibilityState = "hidden";
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => vis });
    setToken("synthetic-vis-tok");
    const seenReqs: { url: string; method?: string; auth: string | null }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      seenReqs.push({ url: String(input), method: init?.method, auth: headers.get("Authorization") });
      return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    try {
      Object.assign(getState(), { conn: "online" });
      render(card({}, { id: "ix_secret_hidden_tab", seen_ttl_seconds: 600 }));
      await flush();
      expect(seenReqs).toEqual([]);

      // Visibility event while still hidden does nothing.
      act(() => { document.dispatchEvent(new Event("visibilitychange")); });
      await flush();
      expect(seenReqs).toEqual([]);

      // Switching tab to visible sends exactly one authenticated /seen request.
      vis = "visible";
      act(() => { document.dispatchEvent(new Event("visibilitychange")); });
      await flush();
      expect(seenReqs).toEqual([
        { url: "/api/v1/interactions/ix_secret_hidden_tab/seen", method: "POST", auth: "Bearer synthetic-vis-tok" },
      ]);

      // Subsequent visibility transitions do not resend.
      vis = "hidden";
      act(() => { document.dispatchEvent(new Event("visibilitychange")); });
      vis = "visible";
      act(() => { document.dispatchEvent(new Event("visibilitychange")); });
      await flush();
      expect(seenReqs).toHaveLength(1);
    } finally {
      if (prevVisibility) Object.defineProperty(document, "visibilityState", prevVisibility);
      else delete (document as { visibilityState?: DocumentVisibilityState }).visibilityState;
      Object.assign(getState(), { conn: prevConn });
      vi.unstubAllGlobals();
      setToken("");
    }
  });
});
