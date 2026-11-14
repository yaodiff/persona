import { describe, expect, it } from "vitest";
import {
  canChoose,
  setupCodeFromHash,
  withoutSetupCode,
  canRollBack,
  canUpgrade,
  extensionRunNote,
  extensionStatus,
  sourceProblem,
  uploadProblem,
  canRemove,
  centerName,
  countdown,
  groupDoctor,
  hubLabel,
  isLegacy,
  lines,
  linuxInstallCommand,
  machineStatus,
  memberNotice,
  narrowerChoices,
  needsMachines,
  needsRepair,
  parseConnectorLink,
  pathRows,
  peerStatus,
  probeLinkable,
  qrPath,
  REFUSED_TEXT,
  restartText,
  setupRequest,
  shellQuote,
  sortMeshHubs,
  uptime,
  validCidr,
  validHostPort,
  validMachineId,
  validProxy,
  viaLabel,
} from "./logic";
import type { AdminMachine, MeshView, Peer } from "./types";

describe("access narrowing", () => {
  it("only offers the current scope or a narrower one", () => {
    expect(narrowerChoices("any")).toEqual(["loopback", "lan", "cidrs", "any"]);
    expect(narrowerChoices("lan")).toEqual(["loopback", "lan"]);
    expect(narrowerChoices("cidrs")).toEqual(["loopback", "cidrs"]);
    expect(narrowerChoices("loopback")).toEqual(["loopback"]);
  });
  it("never widens", () => {
    expect(canChoose("lan", "any")).toBe(false);
    expect(canChoose("lan", "cidrs")).toBe(false);
    expect(canChoose("cidrs", "lan")).toBe(false);
    expect(canChoose("loopback", "lan")).toBe(false);
    expect(canChoose("any", "cidrs")).toBe(true);
  });
});

describe("validCidr", () => {
  it("accepts IPv4 and IPv6 prefixes", () => {
    for (const ok of ["192.0.2.0/24", "203.0.113.0/24", "0.0.0.0/0", "2001:db8::/32", "fd00::/8", " 198.51.100.7/32 "]) expect(validCidr(ok), ok).toBe(true);
  });
  it("rejects the rest", () => {
    for (const bad of ["192.0.2.0", "192.0.2.0/33", "300.0.0.0/8", "1.2.3/24", "2001:db8::/129", "2001::db8::/64", "lan", ""]) expect(validCidr(bad), bad).toBe(false);
  });
  it("splits a list on lines and commas", () => {
    expect(lines(" a/1 \n\n b/2, c/3 ")).toEqual(["a/1", "b/2", "c/3"]);
  });
});

describe("setupRequest", () => {
  it("sends the setup code only when the hub asks for one", () => {
    expect(setupRequest("hunter2hunter2", "abcd-efgh", false)).toEqual({ password: "hunter2hunter2" });
    expect(setupRequest("hunter2hunter2", " ABCD-EFGH ", true)).toEqual({ password: "hunter2hunter2", setup_code: "ABCD-EFGH" });
  });
});

describe("qrPath", () => {
  it("draws runs of dark modules, offset by the margin", () => {
    const { d, size } = qrPath(["110", "001"], 2);
    expect(size).toBe(7);
    expect(d).toBe("M2 2h2v1h-2zM4 3h1v1h-1z");
  });
  it("is empty for a blank code", () => {
    expect(qrPath(["000", "000", "000"]).d).toBe("");
  });
});

describe("time", () => {
  const now = Date.parse("2026-11-02T12:00:00Z");
  it("formats uptime", () => {
    expect(uptime("2026-11-02T11:59:30Z", now)).toBe("不到 1 分钟");
    expect(uptime("2026-11-02T11:48:00Z", now)).toBe("12 分钟");
    expect(uptime("2026-11-02T09:00:00Z", now)).toBe("3 小时");
    expect(uptime("2026-11-02T09:30:00Z", now)).toBe("2 小时 30 分钟");
    expect(uptime("2026-10-30T08:00:00Z", now)).toBe("3 天 4 小时");
    expect(uptime(undefined, now)).toBe("—");
  });
  it("counts down to expiry", () => {
    expect(countdown("2026-11-02T12:09:42Z", now)).toBe("9:42");
    expect(countdown("2026-11-02T12:00:05Z", now)).toBe("0:05");
    expect(countdown("2026-11-02T11:59:00Z", now)).toBe("");
  });
});

describe("labels", () => {
  it("says how many changes wait for a restart", () => {
    expect(restartText({ required: false, reasons: [] })).toBe("");
    expect(restartText({ required: true, reasons: ["a"] })).toBe("有改动要重启 hub 后生效");
    expect(restartText({ required: true, reasons: ["a", "b"] })).toBe("有 2 项改动重启 hub 后生效");
  });
  it("groups doctor items by level", () => {
    const g = groupDoctor([
      { level: "ok", item: "a", detail: "" },
      { level: "problem", item: "b", detail: "" },
      { level: "info", item: "c", detail: "" },
    ]);
    expect(g.problem.map((i) => i.item)).toEqual(["b"]);
    expect(g.ok.map((i) => i.item)).toEqual(["a"]);
    expect(g.info.map((i) => i.item)).toEqual(["c"]);
  });
  it("puts pending machines before their live state", () => {
    const m = (p: Partial<AdminMachine>): AdminMachine => ({ id: "x", name: "x", kind: "ssh", online: false, state: "active", ...p });
    expect(machineStatus(m({ state: "pending_add" })).label).toBe("重启后加入");
    expect(machineStatus(m({ online: true })).tone).toBe("green");
    expect(machineStatus(m({ error: "refused" })).tone).toBe("red");
    expect(machineStatus(m({})).label).toBe("离线");
  });
  it("shows a refused peer as offline, with why", () => {
    const p: Peer = { hub_id: "h", name: "n", state: "connected", machines: 3 };
    expect(peerStatus(p)).toEqual({ tone: "green", label: "已连接" });
    expect(peerStatus({ ...p, state: "refused" })).toEqual({ tone: "gray", label: "离线", error: REFUSED_TEXT });
    expect(peerStatus({ ...p, state: "refused", error: "401 from peer" }).error).toBe("401 from peer");
    expect(peerStatus({ ...p, state: "offline" }).error).toBeUndefined();
    expect(peerStatus({ ...p, state: "connecting" }).tone).toBe("orange");
  });
  it("links only a probed hub with TLS", () => {
    const probe = { hub_id: "h", name: "n", tls_port: 18662, fp: "abc" };
    expect(probeLinkable(probe)).toBe(true);
    expect(probeLinkable({ ...probe, tls_port: 0, fp: "" })).toBe(false);
    expect(probeLinkable({ ...probe, fp: "" })).toBe(false);
  });
  it("checks machine ids", () => {
    expect(validMachineId("laptop")).toBe(true);
    expect(validMachineId("mac-mini.2")).toBe(true);
    expect(validMachineId("Laptop")).toBe(false);
    expect(validMachineId("-x")).toBe(false);
    expect(validMachineId("a b")).toBe(false);
  });
});

const SELF = "self-hub-id-0000";
const OFFICE = "office-hub-id-1111";
const mesh = (p: Partial<MeshView> = {}): MeshView => ({
  role: "center",
  center: { hub_id: SELF, name: "家里", online: true },
  term: 2,
  hubs: [
    { hub_id: SELF, name: "家里", role: "center", online: true, self: true },
    { hub_id: OFFICE, name: "公司", role: "member", online: true, paired: true },
  ],
  ...p,
});
const machine = (p: Partial<AdminMachine>): AdminMachine => ({ id: "x", name: "x", kind: "link", online: true, state: "active", ...p });

describe("paths", () => {
  it("names the hub a path goes through", () => {
    expect(hubLabel(mesh(), SELF)).toBe("这台 hub");
    expect(hubLabel(mesh(), OFFICE)).toBe("公司");
    expect(hubLabel(mesh(), "abcdef0123456789")).toBe("abcdef01");
    expect(hubLabel(null, SELF, SELF)).toBe("这台 hub");
    expect(hubLabel(undefined, undefined)).toBe("");
  });
  it("puts the path in use first and keeps the hub's order for the rest", () => {
    const m = machine({
      paths: [
        { via: "link", hub_id: SELF, ok: false, error: "refused" },
        { via: "service", hub_id: SELF, ok: true, rtt_ms: 0 },
        { via: "hub", hub_id: OFFICE, id: "mbp", ok: true, rtt_ms: 57, active: true },
      ],
    });
    const rows = pathRows(m, mesh());
    expect(rows.map((r) => r.via)).toEqual(["hub", "link", "service"]);
    expect(rows[0]).toMatchObject({ label: "经成员 hub", hub: "公司", self: false, active: true, rtt: 57, ok: true });
    expect(rows[1]).toMatchObject({ label: "电脑来连", hub: "这台 hub", self: true, ok: false, error: "refused" });
    expect(rows[2].rtt).toBeUndefined(); // 0 is "not measured"
    expect(pathRows(machine({}), mesh())).toEqual([]);
  });
  it("hides a working path's stale error", () => {
    const rows = pathRows(machine({ paths: [{ via: "service", hub_id: SELF, ok: true, error: "old" }] }), mesh());
    expect(rows[0].error).toBeUndefined();
  });
  it("labels every way to a computer", () => {
    expect(["local", "service", "link", "ssh", "tailcat", "hub"].map(viaLabel)).toEqual(["本机", "hub 来连", "电脑来连", "ssh", "tailcat", "经成员 hub"]);
    expect(viaLabel("future")).toBe("future");
    expect(viaLabel(undefined)).toBe("");
  });
});

describe("computers", () => {
  it("never removes one only a member has", () => {
    expect(canRemove(machine({ kind: "hub" }))).toBe(false);
    expect(canRemove(machine({ kind: "service" }))).toBe(true);
    expect(canRemove(machine({ kind: "link", state: "pending_remove" }))).toBe(false);
  });
  it("marks ssh and tailcat as legacy", () => {
    expect(isLegacy(machine({ kind: "ssh" }))).toBe(true);
    expect(isLegacy(machine({ kind: "tailcat" }))).toBe(true);
    expect(isLegacy(machine({ kind: "service" }))).toBe(false);
  });
  it("offers pairing again when the connector no longer takes this hub", () => {
    const err = "连接器已经配对给了别的 hub，原来的凭据失效：在管理界面重新配对这台电脑";
    expect(needsRepair(machine({ kind: "service", error: err }))).toBe(true);
    expect(needsRepair(machine({ kind: "service", error: "dial tcp: connection refused" }))).toBe(false);
    expect(needsRepair(machine({ kind: "link", error: err }))).toBe(false);
  });
  it("asks for computers while there is nothing but the hub's own", () => {
    expect(needsMachines([])).toBe(true);
    expect(needsMachines([machine({ kind: "local" })])).toBe(true);
    expect(needsMachines([machine({ kind: "local" }), machine({ kind: "link", state: "pending_remove" })])).toBe(true);
    expect(needsMachines([machine({ kind: "local" }), machine({ kind: "service" })])).toBe(false);
    expect(needsMachines([machine({ kind: "hub" })])).toBe(false);
  });
});

describe("connector links", () => {
  it("reads what persona-link pair --listen prints", () => {
    expect(parseConnectorLink(" persona://connector?addr=192.0.2.30:18663&addr=%5B2001:db8::30%5D:18663&fp=abc_-1&code=ABCD-EFGH ")).toEqual({
      addrs: ["192.0.2.30:18663", "[2001:db8::30]:18663"],
      fp: "abc_-1",
      code: "ABCD-EFGH",
    });
    for (const scheme of ["persona"]) {
      expect(parseConnectorLink(`${scheme}://connector?addr=192.0.2.30:18663&fp=abc_-1&code=ABCD-EFGH`)).toEqual({
        addrs: ["192.0.2.30:18663"], fp: "abc_-1", code: "ABCD-EFGH",
      });
      expect(parseConnectorLink(`${scheme}://connector?addr=192.0.2.30:18663&code=ABCD-EFGH`)).toBeNull();
      expect(parseConnectorLink(`${scheme}://link?addr=192.0.2.30:18663&fp=f&code=c`)).toBeNull();
    }
    expect(parseConnectorLink("persona://connector/?fp=f&code=c")).toEqual({ addrs: [], fp: "f", code: "c" });
    expect(parseConnectorLink("persona://connector?addr=bad&fp=f&code=c")?.addrs).toEqual([]);
  });
  it("rejects anything else", () => {
    for (const bad of ["", "persona://link?url=x&fp=f&code=c", "https://connector?fp=f&code=c", "persona://connector?addr=192.0.2.30:18663&fp=f", "persona://connector?code=c"])
      expect(parseConnectorLink(bad), bad).toBeNull();
  });
  it("checks host:port and socks5 proxies like the hub", () => {
    for (const ok of ["192.0.2.30:18663", "workstation.example.com:443", "[2001:db8::30]:18663", "[fe80::1%en0]:1"]) expect(validHostPort(ok), ok).toBe(true);
    for (const bad of ["192.0.2.30", "192.0.2.30:0", "192.0.2.30:70000", "2001:db8::30:18663", "a/b:1", ":1", "https://x:1"]) expect(validHostPort(bad), bad).toBe(false);
    for (const ok of ["socks5://192.0.2.5:1080", "socks5h://proxy.example.com:1080", "SOCKS5://u:p@[2001:db8::5]:1080"]) expect(validProxy(ok), ok).toBe(true);
    for (const bad of ["http://192.0.2.5:3128", "socks5://192.0.2.5", "192.0.2.5:1080", "socks5://"]) expect(validProxy(bad), bad).toBe(false);
  });
  it("builds the Linux one-liner, quoted for the shell", () => {
    expect(shellQuote("it's")).toBe("'it'\\''s'");
    expect(linuxInstallCommand("persona://link?url=https%3A%2F%2Fhub.example.com&token=jp_x")).toBe("persona-link install --pair - <<< 'persona://link?url=https%3A%2F%2Fhub.example.com&token=jp_x'");
  });
});

describe("the group", () => {
  it("lists the center first, then this hub, then by name", () => {
    const hubs = sortMeshHubs([
      { hub_id: "b", name: "B", role: "member", online: true },
      { hub_id: "s", name: "Z", role: "member", online: true, self: true },
      { hub_id: "a", name: "A", role: "member", online: false },
      { hub_id: "c", name: "C", role: "center", online: true },
    ]);
    expect(hubs.map((h) => h.hub_id)).toEqual(["c", "s", "a", "b"]);
  });
  it("says nothing on the center", () => {
    expect(memberNotice(mesh())).toBeNull();
    expect(memberNotice(null)).toBeNull();
    expect(memberNotice(undefined)).toBeNull();
  });
  it("sends a member's admin to the center, and says when it is unreachable", () => {
    const m = mesh({ role: "member", center: { hub_id: OFFICE, name: "公司", online: true } });
    const n = memberNotice(m)!;
    expect(n.title).toBe("这台 hub 是组里的成员，组由中心「公司」管理");
    expect(n.detail).toContain("设备、互联等去中心的管理界面");
    expect(n.offline).toBe(false);
    expect(n.detail).not.toContain("连不上");
    const off = memberNotice({ ...m, center: { ...m.center, online: false } })!;
    expect(off.offline).toBe(true);
    expect(off.detail).toContain("中心现在连不上");
  });
  it("names the center even when the hub did not", () => {
    expect(centerName(mesh({ center: { hub_id: OFFICE, name: "", online: true } }))).toBe("公司");
    expect(centerName(mesh({ center: { hub_id: "abcdef0123", name: "", online: true }, hubs: [] }))).toBe("abcdef01");
  });
});

describe("extensions", () => {
  it("says each state", () => {
    expect(extensionStatus({ status: "running" })).toEqual({ tone: "green", label: "启动" });
    expect(extensionStatus({ status: "stopped" })).toEqual({ tone: "green", label: "启动" });
    expect(extensionStatus({ status: "disabled" })).toEqual({ tone: "gray", label: "停用" });
    expect([extensionRunNote({ status: "running" }), extensionRunNote({ status: "stopped" }), extensionRunNote({ status: "error" })]).toEqual([
      "正在运行", "现在没在运行，要用时会自己启动", "",
    ]);
    expect(extensionStatus({ status: "needs_setup" }).tone).toBe("orange");
    expect(extensionStatus({ status: "incompatible" }).tone).toBe("red");
    expect(extensionStatus({ status: "whatever" }).label).toBe("状态未知");
  });

  it("says the node program's aggregate, not the idle hub process", () => {
    const node_status = { total: 2, running: 2, starting: 0, stopped: 0, offline: 0, abnormal: 0 };
    expect(extensionStatus({ status: "running", node_status })).toEqual({ tone: "green", label: "在 2 台机器上运行" });
    expect(extensionRunNote({ status: "running", node_status })).toBe("");
    const mixed = { total: 2, running: 1, starting: 0, stopped: 0, offline: 0, abnormal: 1 };
    expect(extensionStatus({ status: "running", node_status: mixed })).toEqual({ tone: "green", label: "在 1 台机器上运行 · 1 台机器异常" });
    const abnormalOnly = { total: 2, running: 0, starting: 0, stopped: 0, offline: 1, abnormal: 1 };
    expect(extensionStatus({ status: "error", node_status: abnormalOnly })).toEqual({ tone: "red", label: "1 台机器异常 · 1 台机器离线" });
  });

  it("says whether an attached extension is online and since when", () => {
    const at = new Date();
    at.setHours(10, 42, 0, 0);
    const since = at.toISOString();
    expect([
      extensionRunNote({ status: "running", attached: { online: true, since } }),
      extensionRunNote({ status: "starting", attached: { online: false, since, error: "连不上" } }),
      extensionRunNote({ status: "error", attached: { online: false, since, error: "连不上" } }),
      extensionRunNote({ status: "stopped", attached: { online: false } }),
      extensionRunNote({ status: "disabled", attached: { online: false } }),
    ]).toEqual(["独立运行 · 10:42 起在线", "独立运行 · 正在重连，10:42 起离线：连不上", "独立运行 · 10:42 起离线", "独立运行，需要时再连接", ""]);
  });

  it("checks a source before asking the hub", () => {
    expect(sourceProblem("")).toBe("");
    expect(sourceProblem("dir:/opt/extensions/notes")).toBe("");
    expect(sourceProblem("~/extensions/notes.tar.gz")).toBe("");
    expect(sourceProblem("file:relative.tar.gz")).not.toBe("");
    expect(sourceProblem("http://example.com/p.tar.gz")).toBe("只收 https 链接");
    expect(sourceProblem("https://example.com/p.tar.gz")).toContain("sha256");
    expect(sourceProblem(`https://example.com/p.tar.gz#sha256=${"a".repeat(64)}`)).toBe("");
    expect(sourceProblem("remote:rpc=/run/p.sock,http=/run/p-http.sock")).toBe("");
    expect(sourceProblem("remote:")).not.toBe("");
    expect(sourceProblem("notes")).not.toBe("");
  });

  it("checks an upload before sending it", () => {
    expect(uploadProblem({ name: "p.tar.gz", size: 10 }, 100)).toBe("");
    expect(uploadProblem({ name: "p.ZIP", size: 10 }, 100)).toBe("");
    expect(uploadProblem({ name: "p.tar", size: 10 }, 100)).not.toBe("");
    expect(uploadProblem({ name: "p.tgz", size: 300 << 20 }, 200 << 20)).toBe("包超过 200 MB");
  });

  it("offers upgrade and rollback only for installed packages", () => {
    const base = { id: "x", name: "X", kind: "connectors", status: "running", enabled: true, permissions: [] };
    expect(canUpgrade({ ...base, source: "builtin" })).toBe(false);
    expect(canRollBack({ ...base, source: "installed", previous_version: "1.0.0", origin: "dir:/p" })).toBe(true);
    expect(canRollBack({ ...base, source: "installed", previous_version: "1.0.0", origin: "remote:rpc=/s" })).toBe(false);
    expect(canRollBack({ ...base, source: "installed" })).toBe(false);
  });
});

describe("setup code from the installer's admin URL", () => {
  it("reads #setup= and takes it back off", () => {
    expect(setupCodeFromHash("#setup=ABCD-EFGH")).toBe("ABCD-EFGH");
    expect(setupCodeFromHash("#x=1&setup=abcd-efgh")).toBe("ABCD-EFGH");
    expect(setupCodeFromHash("")).toBe("");
    expect(setupCodeFromHash("#setup=<script>")).toBe("");
    expect(withoutSetupCode("#setup=ABCD-EFGH")).toBe("");
    expect(withoutSetupCode("#x=1&setup=ABCD-EFGH")).toBe("#x=1");
  });
});
