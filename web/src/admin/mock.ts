// Dev-only canned admin API (`npm run dev`, /admin?mock=<scenario>): build and screenshot the pages without a hub.
// Placeholder data only (documentation addresses, example names). main.tsx loads this behind import.meta.env.DEV, so
// production builds leave it out.
//
// Scenarios: login (any other name) · setup / setup-local / setup-remote · app (a lone hub, older computers, a restart
// pending) · nopeers (a hub without the mesh API) · center (the center of a group: members, computers with several
// paths, a service computer, one to pair again, one only a member has, an old ssh one) · member / member-offline (a
// member hub, its center reachable or not; member-stale: the page still thinks the center is up, the hub knows better
// and answers set-center with confirm_required) · empty (nothing but the hub's own machine). Adding computers works in every
// signed-in scenario: a connector link containing "fail" fails to pair. Extensions: a built-in one, two installed (one
// upgraded, one waiting for its key), a broken hub.json one; a source containing "fail" does not preview, an upgrade
// asks for one more host.
import { setTransport } from "./api";
import { installMockFollower, mockLogs, mockMetrics, mockOverview } from "./mock-metrics";
import type { CatalogItem, Market } from "../lib/extension-market";
import type { UsageSource, UsageSources, AdminMachine, AdminExtension, AdminState, Device, DiscoveredHub, MeshView, MetricsWindow, NetworkView, Overview, Peer, PermissionLine, ExtensionPreview } from "./types";

const SELF = "0d9e8f7a-6b5c-4d3e-8f2a-1b0c9d8e7f6a";
const OFFICE = "6f1c2a9e-0b7d-4c55-9e3a-2b8f41d07c11";
const OLD = "93b0d4e2-5a1f-4c7e-8d2b-6e9f0a1c3b57";
const LAB = "5e2b8c1d-7f4a-4b6e-9c3d-0a1b2c3d4e5f";
const NAS = "a24d77c0-91e2-4f1b-8f2d-5c0b3e6a9d42";
const VERSION = "v1.4.0-12-gabc1234";
const SIGNED_IN = new Set(["app", "nopeers", "center", "member", "member-offline", "member-stale", "empty"]);
const REPAIR = "连接器已经配对给了别的 hub，原来的凭据失效：在管理界面重新配对这台电脑";

const iso = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString();
const later = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString();

function fakeQr(n = 29): string[] {
  let seed = 7;
  const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
  const grid = Array.from({ length: n }, () => Array.from({ length: n }, () => (rnd() > 0.52 ? "1" : "0")));
  const finder = (r0: number, c0: number) => {
    for (let r = -1; r < 8; r++)
      for (let c = -1; c < 8; c++) {
        const y = r0 + r,
          x = c0 + c;
        if (y < 0 || x < 0 || y >= n || x >= n) continue;
        const ring = Math.max(Math.abs(r - 3), Math.abs(c - 3));
        grid[y][x] = r < 0 || c < 0 || r > 6 || c > 6 ? "0" : ring === 2 ? "0" : "1";
      }
  };
  finder(0, 0);
  finder(0, n - 7);
  finder(n - 7, 0);
  return grid.map((row) => row.join(""));
}

export function installMock(scenario: string) {
  const member = scenario.startsWith("member");
  const state: AdminState = {
    password_set: !scenario.startsWith("setup"),
    logged_in: SIGNED_IN.has(scenario),
    can_setup: scenario !== "setup-remote",
    setup_code_required: scenario === "setup-local",
    csrf: SIGNED_IN.has(scenario) ? "mock-csrf" : "",
    hub_name: member ? "公司的 hub" : "家里的 hub",
    version: VERSION,
  };
  let restart = { required: scenario === "app", reasons: scenario === "app" ? ["添加电脑「studio」", "TLS 监听改成 :18662"] : [] };
  const startedAt = iso(60 * 26 + 17);
  // Usage sources (docs/server/usage-windows.md"Account-pool gateways"): one Relay that works, one generic that does not answer. Any key
  // other than "wrong" tests fine; an address containing "dead" does not connect. The key is never given back.
  const usage: UsageSources = {
    enabled: true,
    poll_minutes: 5,
    limit: 8,
    sources: [
      { id: "src-1a2b3c4d", name: "Relay", type: "relay", url: "https://gateway.example.com", has_key: true, enabled: true, status: { ok_at: iso(2), tried_at: iso(2), seen: { accounts: 12, windows: 31, listed: 14, providers: { codex: 8, claude: 4 } } } },
      { id: "src-5e6f7a8b", name: "Team pool", type: "generic", url: "https://pool.example.com/persona/usage", has_key: true, enabled: true, status: { ok_at: iso(95), tried_at: iso(1), error: "unreachable", seen: { accounts: 3, windows: 5, listed: 3, providers: { kimi: 3 } } } },
    ],
  };
  const usageSource = (id: string) => usage.sources.find((x) => x.id === id);
  const local: AdminMachine = { id: "server", name: "服务器", kind: "local", icon: "server", online: true, transport: "local", node_version: VERSION, os: "linux", state: "active", hub_id: SELF, paths: [{ via: "local", hub_id: SELF, id: "server", ok: true, active: true }] };
  const machines: AdminMachine[] =
    scenario === "center"
      ? [
          local,
          {
            id: "example-workstation", name: "开发机", kind: "service", icon: "server", online: true, transport: "service", node_version: VERSION, os: "linux", state: "active",
            address: "192.0.2.30:18663", proxy: "socks5://192.0.2.5:1080", hub_id: SELF,
            paths: [
              { via: "service", hub_id: SELF, id: "example-workstation", ok: true, rtt_ms: 9, active: true },
              { via: "hub", hub_id: OFFICE, id: "example-workstation", ok: true, rtt_ms: 41 },
            ],
          },
          {
            id: "macbook", name: "MacBook", kind: "link", icon: "laptop", online: true, transport: "hub", node_version: VERSION, os: "darwin", state: "active", hub_id: OFFICE,
            paths: [
              { via: "hub", hub_id: OFFICE, id: "mbp", ok: true, rtt_ms: 57, active: true },
              { via: "link", hub_id: SELF, id: "macbook", ok: false, error: "Persona Link 没有连上来（最后一次：连接被对方重置）" },
            ],
          },
          {
            id: "nas2", name: "客厅 NAS", kind: "service", icon: "server", online: false, error: REPAIR, os: "linux", last_seen: iso(60 * 5), state: "active",
            address: "192.0.2.41:18663", hub_id: SELF, paths: [{ via: "service", hub_id: SELF, id: "nas2", ok: false, error: REPAIR }],
          },
          {
            id: "office-pc", name: "公司台式机", kind: "hub", icon: "desktop", online: true, transport: "hub", node_version: VERSION, os: "linux", state: "active", hub_id: OFFICE,
            paths: [{ via: "hub", hub_id: OFFICE, id: "office-pc", ok: true, rtt_ms: 38, active: true }],
          },
          {
            id: "laptop", name: "旧笔记本", kind: "ssh", ssh_host: "laptop", online: true, transport: "ssh", node_version: "v1.3.9-4-gdef5678", node_mismatch: true, os: "darwin", state: "active", hub_id: SELF,
            paths: [{ via: "ssh", hub_id: SELF, id: "laptop", ok: true, rtt_ms: 15, active: true }],
          },
        ]
      : member
        ? [local, { id: "macbook", name: "MacBook", kind: "link", icon: "laptop", online: true, transport: "link", node_version: VERSION, os: "darwin", state: "active", hub_id: SELF, paths: [{ via: "link", hub_id: SELF, id: "macbook", ok: true, rtt_ms: 21, active: true }] }]
        : scenario === "empty"
          ? [local]
          : [
              { id: "server", name: "服务器", kind: "local", icon: "server", online: true, transport: "local", node_version: VERSION, os: "linux", state: "active" },
              { id: "laptop", name: "笔记本", kind: "ssh", ssh_host: "laptop", online: true, transport: "ssh", node_version: "v1.3.9-4-gdef5678", node_mismatch: true, os: "darwin", state: "active" },
              { id: "desk", name: "办公室电脑", kind: "link", online: false, error: "Persona Link 没有连上来（最后一次：连接被对方重置）", os: "darwin", last_seen: iso(180), state: "active" },
              { id: "studio", name: "工作室", kind: "ssh", ssh_host: "studio", online: false, state: "pending_add" },
            ];
  let mesh: MeshView =
    scenario === "center"
      ? {
          role: "center",
          center: { hub_id: SELF, name: state.hub_name, online: true },
          term: 3,
          hubs: [
            { hub_id: SELF, name: state.hub_name, role: "center", online: true, self: true },
            { hub_id: OFFICE, name: "公司", role: "member", online: true, paired: true },
            { hub_id: OLD, name: "老家", role: "member", online: false, paired: true },
            { hub_id: LAB, name: "实验室", role: "member", online: true },
          ],
        }
      : member
        ? {
            role: "member",
            center: { hub_id: NAS, name: "书房 NAS", online: scenario !== "member-offline" },
            term: 5,
            hubs: [
              { hub_id: NAS, name: "书房 NAS", role: "center", online: scenario !== "member-offline", paired: true },
              { hub_id: SELF, name: state.hub_name, role: "member", online: true, self: true },
              { hub_id: LAB, name: "实验室", role: "member", online: true },
            ],
          }
        : { role: "center", center: { hub_id: SELF, name: state.hub_name, online: true }, term: 1, hubs: [{ hub_id: SELF, name: state.hub_name, role: "center", online: true, self: true }] };
  let devices: Device[] = [
    { id: "dev_a1", name: "iPhone", kind: "phone", role: "full", created_at: iso(60 * 24 * 40), last_seen: iso(3) },
    { id: "dev_b2", name: "Apple Watch", kind: "watch", role: "answer", created_at: iso(60 * 24 * 38), last_seen: iso(50) },
    { id: "dev_c3", name: "Chrome · 网页", kind: "other", role: "full", created_at: iso(60 * 24 * 2), last_seen: iso(60 * 5) },
    { id: "dev_d4", name: "办公室电脑 的 Persona Link", kind: "link", role: "link", machine_id: "desk", created_at: iso(60 * 24 * 9), last_seen: iso(180) },
  ];
  let peers: Peer[] =
    scenario === "empty"
      ? []
      : member
        ? [{ hub_id: NAS, name: "书房 NAS", state: scenario === "member-offline" ? "offline" : "connected", address: "https://192.0.2.20:18662", rtt_ms: 4, since: iso(60 * 3), machines: 2, role: "center", error: scenario === "member-offline" ? "dial tcp 192.0.2.20:18662: connect: no route to host" : undefined }]
        : [
            { hub_id: OFFICE, name: "公司", state: "connected", address: "https://hub.example.com:18662", rtt_ms: 38, since: iso(60 * 7), machines: 3, role: scenario === "center" ? "member" : undefined },
            { hub_id: OLD, name: "老家", state: "refused", address: "https://198.51.100.40:18662", since: iso(60 * 30), machines: 0, role: scenario === "center" ? "member" : undefined },
          ];
  const discovered: DiscoveredHub[] = [
    { hub_id: "a24d77c0-91e2-4f1b-8f2d-5c0b3e6a9d42", name: "书房 NAS", addresses: ["https://192.0.2.20:18662", "https://[2001:db8::20]:18662"], fp: "q3J8pX0m-Vb7kQ2sT9yH4eW1rL6uN5cD8fG0aZ3iK7o", version: "v1.4.0" },
    { hub_id: "c8e1f5b3-2d6a-4e9f-b7c0-1a3d5f7e9b24", name: "客厅迷你主机", addresses: ["https://192.0.2.31:18662"], fp: "M2nB5vC8xZ1aS4dF7gH0jK3lQ6wE9rT2yU5iO8pA1sD", version: "v1.3.8" },
  ];
  const network: NetworkView = {
    listen: ":18660",
    public_url: "http://hub.example.com:18660",
    tls: { listen: ":18662", port: 18662, fp: "Xk3v9QeR2mT8bN1cY6wZ4pL7sD0fG5hJ2kA9uE3iO6q" },
    advertise: ["https://hub.example.com:18662"],
    advertise_ipv6: true,
    advertise_lan: true,
    endpoints: [
      { url: "https://[2001:db8:1::10]:18662", kind: "ipv6", port: 18662, scheme: "https", seen_at: iso(2) },
      { url: "https://hub.example.com:18662", kind: "name", port: 18662, scheme: "https", seen_at: iso(2) },
      { url: "https://192.0.2.10:18662", kind: "lan", port: 18662, scheme: "https", seen_at: iso(2) },
      { url: "http://hub.example.com:18660", kind: "name", port: 18660, scheme: "http", seen_at: iso(2) },
    ],
    security: { trusted_cidrs: ["198.51.100.0/24"], allow_public_plaintext: false },
    admin: { access: "any", cidrs: [], session_hours: 12, from_env: false, container: false },
    you: { address: "192.0.2.55", tls: false, allowed: { loopback: false, lan: true, cidrs: false, any: true } },
    restart,
  };

  const RUNS: PermissionLine = { key: "runs_code", title: "在 hub 机器上运行程序", detail: "以 hub 的系统用户运行，能读写这个用户能读写的文件；不在沙箱里" };
  const net = (hosts: string) => ({ key: "network", title: "访问网络", detail: `${hosts}（hub 暂不拦截别的地址）` });
  const keys = (names: string) => ({ key: "secrets", title: "使用你填的密钥", detail: names });
  let extensions: AdminExtension[] =
    scenario === "empty"
      ? []
      : [
          { id: "example-calendar", name: "Example Calendar", kind: "connectors", status: "needs_setup", version: "0.3.0", source: "installed", enabled: true, description: "把日程同步进知识库",
            contributes: { connectors: ["examplecal"] }, needs_setup: ["API Key"], origin: "upload:example-calendar-0.3.0.tar.gz", publisher: "example",
            permissions: [RUNS, net("calendar.example.com"), keys("API Key")], secrets: [{ name: "api_key", title: "API Key", required: true, set: false }] },
          { id: "example-notes", name: "Example Notes", kind: "connectors", status: "running", version: "1.2.0", source: "installed", enabled: true, description: "把 Example Notes 的笔记同步进知识库，并给助手一个搜索工具",
            contributes: { connectors: ["examplenotes"] }, previous_version: "1.1.0", origin: `https://example.com/example-notes-1.2.0.tar.gz#sha256=${"9f2c".repeat(16)}`, publisher: "example", homepage: "https://example.com/persona-extension",
            permissions: [RUNS, net("notes.example.com"), keys("API Key")], secrets: [{ name: "api_key", title: "API Key", required: true, set: true }] },
          { id: "telegram", name: "Telegram", kind: "connectors", status: "stopped", version: VERSION, source: "builtin", enabled: true, description: "用你自己的 Telegram 账号收消息（只读）",
            contributes: { connectors: ["telegram"] }, permissions: [RUNS, net("*.telegram.org")] },
          { id: "voice", name: "语音", kind: "voice", status: "error", error: "连不上 socket：/run/persona/voice.sock", source: "hub.json", enabled: true, read_only: true, contributes: { voice: ["voice"] }, permissions: [] },
        ];
  const previewOf = (source: string, upgrade?: string): ExtensionPreview => {
    const cur = extensions.find((p) => p.id === upgrade);
    const base = { preview_id: `pv_${Date.now()}`, permissions_digest: "d1c3".repeat(16), expires_at: later(10) };
    if (cur)
      return {
        ...base,
        manifest: { id: cur.id, name: cur.name, version: "1.3.0", description: cur.description, publisher: cur.publisher, contributes: { connectors: [{}] } },
        permissions: [RUNS, net("files.example.com、notes.example.com"), keys("API Key")],
        upgrade: true,
        from: cur.version,
        more_permissions: true,
        added_permissions: [{ key: "network", title: "访问更多网络", detail: "files.example.com" }],
      };
    return {
      ...base,
      manifest: { id: "example-weather", name: "Example Weather", version: "2.0.1", description: "给助手一个查天气的工具，结果按城市缓存", publisher: "example", contributes: { connectors: [{}] } },
      permissions: [RUNS, net("api.weather.example.com"), { key: "hub_api", title: "完整使用 hub", detail: "拿到 hub 的地址和 token，能做 hub 主人能做的一切：读知识库、驱动各台电脑上的 Agent……" }, keys("API Key")],
      warnings: [source.startsWith("upload:") ? "这个扩展要求的 hub 版本比现在的新，部分功能可能不生效" : "配置里的 token 是明文存的：应该放在 secrets 里"],
    };
  };
  let pending: ExtensionPreview | null = null;

  // Markets (docs/extensions/extensions.md "Extension marketplace"): a public site and a private GitHub repository (token set, one warning); the
  // catalog has an extension to install, two installed ones with an upgrade, a built-in one, and one this hub cannot install.
  // A spec containing "fail" is not read; "private" without a token asks for one.
  const MA = "3f2a9c1d0b";
  const MB = "8e01b7c4a2";
  let markets: Market[] =
    scenario === "empty"
      ? []
      : [
          { id: MA, spec: "https://extensions.example.com/persona/", name: "Example 扩展", description: "Example 官方维护的扩展", has_token: false, extensions: 4, warnings: [], fetched_at: iso(42), added_at: iso(60 * 72), official: true },
          { id: MB, spec: "github:example/team-extensions", name: "团队扩展", has_token: true, extensions: 2, warnings: ["第 3 个扩展：没有 manifest，跳过"], fetched_at: iso(95), added_at: iso(60 * 48) },
        ];
  const catalogItems = (): CatalogItem[] => {
    const raw: CatalogItem[] = [
      { market: MA, market_name: "", id: "example-notes", name: "Example Notes", version: "1.3.0", description: "把 Example Notes 的笔记同步进知识库，并给助手一个搜索工具", publisher: "example", tags: ["笔记", "连接器"],
        contributes: { connectors: ["examplenotes"] }, permissions: [RUNS, net("files.example.com、notes.example.com"), keys("API Key")] },
      { market: MA, market_name: "", id: "example-translate", name: "Example 翻译", version: "0.9.0", description: "给助手一个翻译工具，按术语表翻", publisher: "example", homepage: "https://extensions.example.com/translate", tags: ["翻译", "助手"],
        contributes: { assistant: ["translate"] }, permissions: [RUNS, net("api.translate.example.com"), keys("API Key")] },
      { market: MA, market_name: "", id: "example-calendar", name: "Example Calendar", version: "0.4.0", description: "把日程同步进知识库", publisher: "example", tags: ["日历"],
        contributes: { connectors: ["examplecal"] }, permissions: [RUNS, net("calendar.example.com"), keys("API Key")] },
      { market: MA, market_name: "", id: "telegram", name: "Telegram", version: "1.0.0", description: "用你自己的 Telegram 账号收消息（只读）", publisher: "example", tags: ["连接器"], contributes: { connectors: ["telegram"] }, permissions: [RUNS] },
      { market: MB, market_name: "", id: "team-digest", name: "每日摘要", version: "2.3.0", description: "每天早上把收件箱和 Agent 的进展总结成一条消息", publisher: "team", tags: ["摘要", "事件"],
        contributes: { events: ["digest"] }, permissions: [RUNS, { key: "events", title: "收到 hub 的事件", detail: "收件箱里有新的或更新了的条目；有 Agent 做完了" }] },
      { market: MB, market_name: "", id: "team-windows", name: "Windows 工具", version: "0.2.0", description: "只有 Windows 版的包", publisher: "team", tags: [], contributes: { assistant: ["tools"] }, permissions: [RUNS],
        problem: "没有这台 hub（linux-amd64）能用的包" },
    ];
    return raw
      .filter((c) => markets.some((m) => m.id === c.market))
      .map((c) => {
        const pl = extensions.find((p) => p.id === c.id);
        const it: CatalogItem = { ...c, market_name: markets.find((m) => m.id === c.market)?.name ?? "" };
        if (pl) {
          it.installed = pl.version;
          it.installed_source = pl.source;
          it.from_this_market = pl.origin === `market:${c.market}/${c.id}`;
          it.upgradable = pl.source === "installed" && (pl.version ?? "") < c.version;
        }
        return it;
      });
  };

  const overview = (): Overview => ({
    hub: { hub_id: SELF, name: state.hub_name, name_set: true, default_name: "server", version: state.version, started_at: startedAt, os: "linux", arch: "amd64", container: false, can_restart: true },
    listen: network.listen,
    public_url: network.public_url,
    tls: network.tls,
    endpoints: network.endpoints,
    machines: { total: machines.filter((m) => m.state === "active").length, online: machines.filter((m) => m.online).length },
    devices: devices.length,
    restart,
  });

  const memberHub = () => fail(409, "member_hub", `这台 hub 是组里的成员，设备由中心 hub「${mesh.center.name}」管理：到它的管理界面去`);
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  const fail = (status: number, code: string, error = code, extra: Record<string, unknown> = {}) => json({ error, code, ...extra }, status);
  const wait = (ms = 220) => new Promise((r) => setTimeout(r, ms));
  let failures = 0;

  const answer = async (url: string, init: RequestInit): Promise<Response> => {
    await wait();
    const path = url.replace(/^\/api\/v1\/admin/, "");
    const method = init.method ?? "GET";
    const body = typeof init.body === "string" && init.body ? JSON.parse(init.body) : {};
    const key = `${method} ${path.split("?")[0]}`;
    if (key === "GET /state") return json(state);
    if (key === "POST /setup") {
      if (state.setup_code_required) {
        if (!body.setup_code) return fail(403, "setup_code_required");
        if (String(body.setup_code).replace(/-/g, "").toUpperCase() !== "ABCDEFGH") return fail(401, "bad_setup_code");
      }
      if (String(body.password ?? "").length < 8) return fail(400, "weak_password");
      Object.assign(state, { password_set: true, logged_in: true, csrf: "mock-csrf" });
      return json(state);
    }
    if (key === "POST /login") {
      if (body.password !== "password123") {
        failures++;
        return failures >= 3 ? fail(429, "rate_limited", "too many failures", { retry_after: 840 }) : fail(401, "bad_password");
      }
      Object.assign(state, { logged_in: true, csrf: "mock-csrf" });
      return json(state);
    }
    if (!state.logged_in) return fail(401, "login_required");
    if (method !== "GET" && (init.headers as Record<string, string>)["X-Persona-CSRF"] !== state.csrf) return fail(403, "csrf");
    switch (key) {
      case "POST /logout":
        state.logged_in = false;
        return json({ ok: true });
      case "POST /password":
        return body.current === "password123" ? json({ ok: true }) : fail(401, "bad_password");
      case "GET /overview":
        return json(overview());
      case "PUT /hub":
        state.hub_name = String(body.name || "server");
        return json({ name: state.hub_name, name_set: Boolean(body.name), restart });
      case "POST /doctor":
        await wait(900);
        return json({
          ran_at: new Date().toISOString(),
          items: [
            { level: "ok", item: "明文 listen", detail: ":18660；仅供内网或 VPN 使用" },
            { level: "ok", item: "public.tls_listen", detail: ":18662（配置值，尚未验证 TLS 监听）" },
            { level: "ok", item: "TLS 证书", detail: "SPKI SHA-256 钉扎值 Xk3v9QeR2mT8…；到期时间 2027-10-01T00:00:00Z" },
            {
              level: "ok",
              item: "候选地址",
              detail:
                "已读取运行中的 hub 当前发布列表（不代表外部可达）\nhttps://[2001:db8::2]:18662（ipv6，端口 18662，发现时间 2026-11-13T08:00:00Z）\nhttps://192.0.2.10:18662（lan，端口 18662，发现时间 2026-11-13T08:00:00Z）",
            },
            { level: "problem", item: "本机 IPv6", detail: "没有可确认稳定的全局 IPv6；请检查运营商 IPv6 和网卡地址" },
            { level: "info", item: "外部可达性", detail: "未验证。本机连自己的地址走环回，不能证明公网可达。" },
          ],
          next: ["在主机、路由器和光猫上，对公网只放行 hub 的 TCP TLS 端口 18662；明文端口仅限内网或 VPN", "用手机关掉 Wi-Fi，打开 https://hub.example.com:18662/api/v1/health 测一次（不要携带 token）"],
        });
      case "POST /restart":
        restart = { required: false, reasons: [] };
        return json({ ok: true });
      case "GET /machines":
        return json({ machines, tls: Boolean(network.tls), restart });
      case "POST /machines": {
        if (body.kind === "ssh" || body.kind === "tailcat")
          return fail(400, "ssh_unsupported", "不再添加 ssh 电脑：在那台电脑上装 node 服务，选「电脑来连」或「hub 来连」；hub.json 里已有的 ssh 电脑照常能用");
        if (body.kind !== "link" && body.kind !== "service") return fail(400, "invalid_kind", "kind: link or service");
        if (!/^[a-z0-9][a-z0-9._-]{0,62}$/.test(String(body.id ?? ""))) return fail(400, "invalid_id");
        if (machines.some((m) => m.id === body.id)) return fail(409, "exists", "已经有这个 id 的电脑");
        const base = { id: body.id, name: body.name || body.id, kind: body.kind, icon: body.icon || "laptop", state: "active" as const, hub_id: SELF };
        if (body.kind === "link") {
          const m: AdminMachine = { ...base, online: false, paths: [{ via: "link", hub_id: SELF, id: body.id, ok: false }] };
          machines.push(m);
          return json({ machine: m, restart });
        }
        const link = String(body.pair_link ?? "");
        if (!/^persona:\/\/connector/.test(link)) return fail(502, "pair_failed", "这不是连接器的配对链接（persona://connector?…）");
        if (body.proxy && !/^socks5h?:\/\//.test(body.proxy)) return fail(400, "invalid_proxy", "proxy 要写成 socks5://主机:端口");
        await wait(1400);
        const addr = body.address || new URLSearchParams(link.split("?")[1] ?? "").get("addr") || "";
        if (link.includes("fail")) return fail(502, "pair_failed", `连不上那台电脑的连接器：${addr}：dial tcp ${addr}: connect: connection refused`);
        const m: AdminMachine = { ...base, online: false, address: addr, proxy: body.proxy || undefined, paths: [{ via: "service", hub_id: SELF, id: body.id, ok: false }] };
        machines.push(m);
        setTimeout(() => Object.assign(m, { online: true, transport: "service", node_version: VERSION, os: "linux", paths: [{ via: "service", hub_id: SELF, id: body.id, ok: true, rtt_ms: 11, active: true }] }), 3000);
        return json({ machine: m, restart });
      }
      case "GET /extensions":
        return json({ extensions, can_install: scenario !== "nopeers", upload_most: 200 << 20 });
      case "POST /extensions/preview": {
        await wait(700);
        const source = String(body.source ?? "");
        if (source.includes("fail")) return fail(400, "invalid_plugin", "包里没有 persona-extension.json（根目录或唯一的顶层目录里）");
        pending = previewOf(source, body.upgrade);
        return json(pending);
      }
      case "POST /extensions/upload": {
        await wait(900);
        const q = new URLSearchParams(url.split("?")[1] ?? "");
        pending = previewOf(`upload:${q.get("name")}`, q.get("upgrade") ?? undefined);
        return json(pending);
      }
      case "POST /extensions/install": {
        if (!pending || body.preview_id !== pending.preview_id) return fail(400, "install_failed", "预览过期了，重新预览一次");
        const m = pending.manifest;
        const p: AdminExtension = { id: m.id, name: m.name, kind: "connectors", status: "needs_setup", version: m.version, source: "installed", enabled: true, description: m.description,
          contributes: { connectors: ["exampleweather"] }, needs_setup: ["API Key"], origin: pending.market ? `market:${pending.market}/${m.id}` : "dir:/opt/persona-extensions/example-weather", publisher: m.publisher, permissions: pending.permissions };
        extensions = [...extensions, p].sort((a, b) => a.id.localeCompare(b.id));
        pending = null;
        return json(p);
      }
      case "GET /extensions/-/markets":
        return json({ markets });
      case "POST /extensions/-/markets": {
        await wait(800);
        const spec = String(body.spec ?? "").trim();
        if (!/^(https:\/\/|github:|git:|git@)/.test(spec)) return fail(400, "market_failed", "市场的地址是 https://…、github:owner/repo 或 git:<地址>");
        if (spec.includes("fail")) return fail(400, "market_failed", `读不到 ${spec}：找不到（404）：地址不对，或者是私有的，要填 token`);
        if (spec.includes("private") && !body.token) return fail(400, "market_failed", `读不到 ${spec}：要 token（401）`);
        const m: Market = { id: `m${Date.now().toString(16).slice(-8)}`, spec, name: spec.replace(/^(github|git):/, "").replace(/\.git(#.*)?$/, "").split(/[/:]/).filter(Boolean).pop() ?? spec, has_token: Boolean(body.token), extensions: 0, warnings: [], fetched_at: iso(0), added_at: iso(0) };
        markets = [...markets, m];
        return json(m);
      }
      case "GET /extensions/-/catalog": {
        const q = new URLSearchParams(url.split("?")[1] ?? "");
        if (q.get("refresh") === "1") await wait(700);
        const words = (q.get("q") ?? "").toLowerCase();
        const only = q.get("market") ?? "";
        const items = catalogItems().filter(
          (c) => (!only || c.market === only) && (!words || [c.id, c.name, c.description ?? "", c.publisher ?? "", ...(c.tags ?? [])].join("\n").toLowerCase().includes(words)),
        );
        return json({ markets, extensions: items, platform: "linux-amd64" });
      }
      case "POST /extensions/-/catalog/preview": {
        await wait(700);
        const c = catalogItems().find((x) => x.market === body.market && x.id === body.extension);
        if (!c || c.problem) return fail(400, "invalid_plugin", c?.problem ?? `这个市场里没有扩展 ${body.extension}`);
        const cur = extensions.find((p) => p.id === c.id && p.source === "installed");
        const base = { preview_id: `pv_${Date.now()}`, permissions_digest: "a7e2".repeat(16), expires_at: later(10), market: c.market, permissions: c.permissions ?? [] };
        const manifest = { id: c.id, name: c.name, version: c.version, description: c.description, publisher: c.publisher, contributes: Object.fromEntries(Object.entries(c.contributes ?? {}).map(([k, v]) => [k, v.map(() => ({}))])) };
        pending = cur
          ? { ...base, manifest, upgrade: true, from: cur.version, more_permissions: c.id === "example-notes", added_permissions: c.id === "example-notes" ? [{ key: "network", title: "访问更多网络", detail: "files.example.com" }] : undefined }
          : { ...base, manifest };
        return json(pending);
      }
      case "GET /usage-sources":
        return json(usage);
      case "PUT /usage-sources":
        if (typeof body.enabled === "boolean") usage.enabled = body.enabled;
        if (typeof body.poll_minutes === "number") usage.poll_minutes = body.poll_minutes;
        return json(usage);
      case "POST /usage-sources": {
        if (usage.sources.length >= usage.limit) return fail(409, "too_many", "最多 8 个来源");
        if (!/^https?:\/\//.test(String(body.url ?? ""))) return fail(400, "invalid_url", "地址要写成 http(s)://主机[:端口]");
        const src: UsageSource = { id: `src-${Math.random().toString(16).slice(2, 10)}`, name: String(body.name || "Relay"), type: body.type, url: String(body.url).replace(/\/+$/, ""), has_key: Boolean(body.key), enabled: true, status: {} };
        usage.sources.push(src);
        return json({ source: src });
      }
      case "POST /usage-sources/test": {
        await wait(400);
        const url = String(body.url ?? "");
        if (url.includes("dead")) return json({ ok: false, code: "unreachable" });
        const saved = body.id ? usageSource(String(body.id)) : undefined;
        const key = body.key || (saved && saved.url === url ? "saved" : "");
        if (!key || key === "wrong") return json({ ok: false, code: "unauthorized" });
        return json({ ok: true, accounts: 12, windows: 31, listed: 14, providers: { codex: 8, claude: 4 } });
      }
      case "GET /mesh":
        return scenario === "nopeers" ? fail(501, "not_implemented") : json(mesh);
      case "GET /devices":
        if (mesh.role === "member") return memberHub();
        return json({ devices });
      case "POST /devices/pair":
        if (mesh.role === "member") return memberHub();
        return json({ pair_url: "persona://pair?url=http%3A%2F%2Fhub.example.com%3A18660&token=jp_mock", url: network.public_url, expires_at: later(10), fp: network.tls?.fp, qr: { size: 29, rows: fakeQr() } });
      case "GET /network":
        return json({ ...network, restart });
      case "PUT /network/access":
        if (!body.force && !network.you.allowed[body.access as keyof typeof network.you.allowed]) return fail(409, "would_lock_you_out");
        network.admin = { ...network.admin, access: body.access, cidrs: body.cidrs ?? [] };
        return json({ ...network, restart });
      case "PUT /network/public":
        network.public_url = body.public_url;
        network.advertise = body.advertise;
        restart = { required: true, reasons: [...restart.reasons, "公网地址"] };
        return json({ ...network, restart });
      case "GET /peers":
        return scenario === "nopeers" ? fail(501, "not_implemented") : json({ peers, discovered });
      case "POST /peers/code":
        return json({ code: "persona://peer?hub=0d9e8f7a&fp=Xk3v9QeR2mT8&url=https%3A%2F%2Fhub.example.com%3A18662&code=pc_mock", expires_at: later(10) });
      case "POST /peers/probe": {
        const address = String(body.address ?? "").trim();
        if (!address) return fail(400, "invalid_address", "address is required");
        await wait(500);
        if (address.includes("192.0.2.31")) return json({ hub_id: "c8e1f5b3-2d6a-4e9f-b7c0-1a3d5f7e9b24", name: "客厅迷你主机", tls_port: 0, fp: "", version: "v1.3.8" });
        if (address.includes("unreachable")) return fail(502, "unreachable", "could not reach that address");
        const d = discovered.find((x) => x.addresses.some((a) => a.includes(address) || address.includes(a))) ?? discovered[0];
        return json({ hub_id: d.hub_id, name: d.name, tls_port: 18662, fp: d.fp, version: d.version });
      }
      case "POST /peers": {
        if (!/^persona:\/\/peer\?/.test(String(body.code ?? ""))) return fail(400, "invalid_code", "not a peer code");
        const p: Peer = { hub_id: "a24d77c0-91e2-4f1b-8f2d-5c0b3e6a9d42", name: "书房 NAS", state: "connecting", address: "https://192.0.2.20:18662", machines: 0 };
        peers = [...peers, p];
        return json(p);
      }
    }
    const us = /^\/usage-sources\/([^/?]+)$/.exec(path);
    if (us) {
      const src = usageSource(decodeURIComponent(us[1]));
      if (!src) return fail(404, "not_found", "没有这个来源");
      if (method === "DELETE") {
        usage.sources.splice(usage.sources.indexOf(src), 1);
        return json({ ok: true });
      }
      const moved = (typeof body.url === "string" && body.url !== src.url) || (typeof body.type === "string" && body.type !== src.type);
      if (moved && !body.key && !body.clear_key) return fail(400, "key_required", "要填这个来源的 Key（换了地址或类型时需要重新填）");
      for (const k of ["name", "type", "url", "enabled"] as const) if (body[k] !== undefined) (src as unknown as Record<string, unknown>)[k] = body[k];
      if (body.key) src.has_key = true;
      if (body.clear_key) src.has_key = false;
      return json({ source: src });
    }
    const m = /^\/machines\/([^/]+)(\/.*)?$/.exec(path);
    if (m) {
      const id = decodeURIComponent(m[1]);
      const machine = machines.find((x) => x.id === id);
      if (!machine) return fail(404, "not_found");
      if (m[2] === "/connector") {
        if (machine.kind !== "service") return fail(400, "not_service");
        const link = String(body.pair_link ?? "");
        if (!/^persona:\/\/connector/.test(link)) return fail(502, "pair_failed", "这不是连接器的配对链接（persona://connector?…）");
        await wait(1200);
        if (link.includes("fail")) return fail(502, "pair_failed", "配对码不对、用过了或过期了：在那台电脑上重新生成");
        const address = body.address || machine.address || "";
        const moved = address !== machine.address;
        Object.assign(machine, { error: undefined, online: true, transport: "service", address, paths: [{ via: "service", hub_id: SELF, id, ok: true, rtt_ms: 7, active: true }] });
        return json({ ok: true, address, version: VERSION, restart_required: moved });
      }
      if (method === "DELETE") {
        if (machine.state === "pending_add") machines.splice(machines.indexOf(machine), 1);
        else machine.state = "pending_remove";
        restart = { required: true, reasons: [...restart.reasons, `删除电脑「${id}」`] };
        return json({ ok: true, restart });
      }
      if (m[2] === undefined && method === "PUT") {
        const name = String(body.name ?? "").trim();
        if (name.length > 40 || /[\n\r\t]/.test(name)) return fail(400, "invalid_name", "名字最多 40 个字，不能换行");
        machine.name = name || id;
        return json({ ok: true, name: machine.name });
      }
      if (m[2] === "/reconnect") return json({ ok: true });
      if (m[2] === "/link-pair") return json({ pair_url: `persona://link?url=https%3A%2F%2Fhub.example.com%3A18662&machine=${id}&token=jp_mock`, expires_at: later(10), command: `persona-link pair 'persona://link?…&machine=${id}'` });
    }
    const mk = /^\/extensions\/-\/markets\/([^/?]+)(\/[a-z]+)?$/.exec(path);
    if (mk) {
      const m = markets.find((x) => x.id === decodeURIComponent(mk[1]));
      if (!m) return fail(404, "not_found", "没有这个市场");
      if (method === "DELETE") {
        markets = markets.filter((x) => x !== m);
        return json({ ok: true });
      }
      await wait(600);
      const read = { ...m, fetched_at: iso(0) };
      if (mk[2] === "/token") read.has_token = Boolean(body.token);
      markets = markets.map((x) => (x.id === m.id ? read : x));
      return json(read);
    }
    // How they run (docs/extensions/extensions.md "Runtime status").
    const query = new URLSearchParams(path.split("?")[1] ?? "");
    const win = (query.get("window") || "24h") as MetricsWindow;
    if (key === "GET /extensions/-/metrics") return json(mockOverview(extensions.map((x) => x.id), win));
    const run = /^\/extensions\/([^/?]+)\/(metrics|logs)$/.exec(path.split("?")[0]);
    if (run && method === "GET") {
      const id = decodeURIComponent(run[1]);
      if (!extensions.some((x) => x.id === id)) return fail(404, "not_found", "没有这个扩展");
      if (run[2] === "metrics") return json(mockMetrics(id, win));
      const since = query.get("since");
      return json(mockLogs(id, query.get("q") ?? "", since === null ? undefined : Number(since)));
    }
    const pl = /^\/extensions\/([^/?]+)(\/[a-z]+)?$/.exec(path);
    if (pl) {
      const i = extensions.findIndex((x) => x.id === decodeURIComponent(pl[1]));
      if (i < 0) return fail(404, "not_found", "没有这个扩展");
      const p = extensions[i];
      const put = (np: AdminExtension) => ((extensions = extensions.map((x, j) => (j === i ? np : x))), json(np));
      if (method === "DELETE") {
        if (p.source !== "installed") return fail(409, "uninstall_failed", `${p.id} 是内置的扩展，只能停用`);
        extensions = extensions.filter((x) => x.id !== p.id);
        return json({ ok: true });
      }
      if (pl[2] === "/enabled") return put({ ...p, enabled: body.enabled, status: body.enabled ? "running" : "disabled" });
      if (pl[2] === "/rollback") return put({ ...p, version: p.previous_version, previous_version: undefined });
      if (pl[2] === "/upgrade") {
        await wait(600);
        if (!pending?.upgrade) return fail(400, "upgrade_failed", "预览过期了，重新预览一次");
        const np = { ...p, previous_version: p.version, version: pending.manifest.version, permissions: pending.permissions };
        pending = null;
        return put(np);
      }
    }
    const d = /^\/devices\/([^/]+)$/.exec(path);
    if (d && method === "DELETE") {
      devices = devices.filter((x) => x.id !== decodeURIComponent(d[1]));
      return json({ ok: true });
    }
    const p = /^\/peers\/([^/]+)$/.exec(path);
    if (p && method === "DELETE") {
      peers = peers.filter((x) => x.hub_id !== decodeURIComponent(p[1]));
      return json({ ok: true });
    }
    const c = /^\/peers\/([^/]+)\/center$/.exec(path);
    if (c && method === "POST") {
      const target = mesh.hubs.find((h) => h.hub_id === decodeURIComponent(c[1]));
      if (!target) return fail(404, "not_in_group", "这台 hub 不在组里");
      await wait(600);
      if (!target.self && !target.online) return fail(409, "unreachable", "那台 hub 现在连不上");
      const centerDown = !mesh.center.online || (scenario === "member-stale" && mesh.term === 5);
      if (centerDown && !body.confirm) return fail(409, "confirm_required", `现在的中心「${mesh.center.name}」连不上`);
      const hubs = mesh.hubs.map((h) => ({ ...h, role: h.hub_id === target.hub_id ? ("center" as const) : ("member" as const) }));
      mesh = { role: target.self ? "center" : "member", center: { hub_id: target.hub_id, name: target.name, online: true }, term: mesh.term + 1, hubs };
      peers = peers.map((x) => ({ ...x, role: x.hub_id === target.hub_id ? "center" : "member" }));
      return json(mesh);
    }
    return fail(404, "not_found", "404 page not found");
  };

  installMockFollower();
  // A page from before #1845 asks /api/v1/admin/plugins…: the same answers under the keys it reads (docs/extensions/extensions.md
  // "Naming and compatibility").
  setTransport(async (url, init) => {
    const legacy = /^\/api\/v1\/admin\/plugins(?=[/?]|$)/.test(url);
    const res = await answer(legacy ? url.replace("/api/v1/admin/plugins", "/api/v1/admin/extensions") : url, init);
    if (!legacy) return res;
    return json(legacyKeys(await res.json()), res.status);
  });
}

/** An answer under the old keys: the list (`extensions` → `plugins`) and each market's count. */
function legacyKeys(body: unknown): unknown {
  if (!body || typeof body !== "object" || Array.isArray(body)) return body;
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(body)) {
    if (k === "extensions") out[Array.isArray(v) || typeof v === "number" ? "plugins" : k] = v;
    else if (k === "markets" && Array.isArray(v)) out[k] = v.map(legacyKeys);
    else out[k] = v;
  }
  return out;
}
