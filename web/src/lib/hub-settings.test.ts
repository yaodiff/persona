import { describe, expect, it } from "vitest";
import { botStateText, botSummary, botTone, credentialsChanged, credentialsOf, credentialsUpdate, DEFAULT_FEATURES, domainChoice, featureEdit, livePairing, nudgeChoices, nudgeLabel, pairingLeft, withFeature, type FeishuBotStatus } from "./feishu-bot";
import type { HubUpdateStatus } from "./hub-update";
import {
  canCheckHubUpdate, channelTitle, dockerSteps, DOCKER_COMMAND, latestWarning, recordTitle, recordTone, runningChannelOf,
  shouldAutoCheckAfterSourceSwitch, sourceBehindWarning, updateFinished, updateGuidance, updateStateTitle, updateView, updateWorking,
} from "./hub-update-settings";

const bot = (o: Partial<FeishuBotStatus> = {}): FeishuBotStatus => ({
  enabled: true, app_id: "cli_x", domain: "https://open.feishu.cn", has_secret: true, from_config: false,
  features: DEFAULT_FEATURES, connected: true, owner: { name: "我", paired_at: 1 }, ...o,
});

describe("feishu bot", () => {
  it("maps the resolved domain to the picker", () => {
    expect(domainChoice("https://open.feishu.cn")).toBe("feishu");
    expect(domainChoice("")).toBe("feishu");
    expect(domainChoice("https://larksuite.com")).toBe("lark");
    expect(domainChoice("https://open.example.com")).toBe("https://open.example.com");
  });

  it("tells changed credentials and sends only a typed secret", () => {
    const st = bot();
    const c = credentialsOf(st);
    expect(c).toEqual({ appID: "cli_x", appSecret: "", domain: "feishu" });
    expect(credentialsChanged(st, c)).toBe(false);
    expect(credentialsChanged(st, { ...c, appID: " cli_x " })).toBe(false);
    expect(credentialsChanged(st, { ...c, appSecret: "s" })).toBe(true);
    expect(credentialsChanged(st, { ...c, domain: "lark" })).toBe(true);
    expect(credentialsChanged(null, { appID: "", appSecret: "", domain: "feishu" })).toBe(false);
    expect(credentialsUpdate({ appID: " a ", appSecret: "  ", domain: "lark" })).toEqual({ app_id: "a", domain: "lark" });
    expect(credentialsUpdate({ appID: "a", appSecret: " s ", domain: "feishu" })).toEqual({ app_id: "a", domain: "feishu", app_secret: "s" });
  });

  it("couples 所有 Agent 的提问 with 提问和批准", () => {
    expect(withFeature({ ...DEFAULT_FEATURES, all_cards: true }, "cards", false)).toMatchObject({ cards: false, all_cards: false });
    expect(withFeature({ ...DEFAULT_FEATURES, cards: false }, "all_cards", true)).toMatchObject({ cards: true, all_cards: true });
    expect(withFeature(DEFAULT_FEATURES, "sync", true)).toEqual({ ...DEFAULT_FEATURES, sync: true });
  });

  it("sends only the switch that changed, so one changed elsewhere is not put back (#74)", () => {
    // The page may be stale: whatever it holds for the other switches stays out of the request.
    expect(featureEdit(DEFAULT_FEATURES, "board", true)).toEqual({ board: true });
    expect(featureEdit(DEFAULT_FEATURES, "pushes", true)).toEqual({ pushes: true });
    expect(featureEdit({ ...DEFAULT_FEATURES, board: true }, "sync", true)).toEqual({ sync: true });
    expect(featureEdit(DEFAULT_FEATURES, "reports", false)).toEqual({ reports: false });
    // The pair that goes together names both, since the hub does not tie them.
    expect(featureEdit(DEFAULT_FEATURES, "cards", false)).toEqual({ cards: false, all_cards: false });
    expect(featureEdit(DEFAULT_FEATURES, "all_cards", true)).toEqual({ all_cards: true, cards: true });
    expect(featureEdit(DEFAULT_FEATURES, "all_cards", false)).toEqual({ all_cards: false });
    expect(featureEdit(DEFAULT_FEATURES, "cards", true)).toEqual({ cards: true });
  });

  it("sends every switch to a hub from before 0.1.12, which replaces the whole object (no `reports` in its status)", () => {
    const { reports: _reports, board: _board, ...old } = DEFAULT_FEATURES;
    expect(featureEdit(old, "pushes", true)).toEqual({ ...old, pushes: true });
    expect(featureEdit({ ...old, all_cards: true }, "cards", false)).toEqual({ ...old, cards: false, all_cards: false });
  });

  it("lists the nudge delays, with the one in force among them", () => {
    expect(nudgeChoices(60)).toEqual([0, 15, 30, 60, 120, 240, 480]);
    expect(nudgeChoices(90)).toEqual([0, 15, 30, 60, 90, 120, 240, 480]);
    expect(nudgeChoices(10080).at(-1)).toBe(10080);
    expect([0, 15, 60, 90, 120].map(nudgeLabel)).toEqual(["关闭", "15 分钟", "1 小时", "90 分钟", "2 小时"]);
  });

  it("says the state", () => {
    expect(botStateText(null, true)).toBe("正在检查…");
    expect(botStateText(null)).toBe("无法读取状态");
    expect(botStateText(bot({ enabled: false }))).toBe("未开启");
    expect(botStateText(bot({ has_secret: false }))).toBe("尚未填写应用凭证");
    expect(botStateText(bot({ connected: false, error: "x" }))).toBe("连接失败：x");
    expect(botStateText(bot({ connected: false }))).toBe("正在连接…");
    expect(botStateText(bot({ owner: null }))).toBe("已连接，等待配对");
    expect(botStateText(bot())).toBe("已连接");
    expect([botTone(bot({ enabled: false })), botTone(bot({ connected: false })), botTone(bot({ owner: null })), botTone(bot())]).toEqual([undefined, "red", "orange", "green"]);
    expect([botSummary(null), botSummary(bot({ enabled: false })), botSummary(bot({ connected: false })), botSummary(bot({ owner: null })), botSummary(bot())]).toEqual(["", "未开启", "无法连接", "待配对", "已连接"]);
  });

  it("keeps only a pairing code that is still valid", () => {
    const now = 1_000_000_000_000;
    expect(livePairing(bot({ pairing: { code: "1", expires_at: now / 1000 + 5 } }), now)?.code).toBe("1");
    expect(livePairing(bot({ pairing: { code: "1", expires_at: now / 1000 - 5 } }), now)).toBeNull();
    expect(pairingLeft(now / 1000 + 545, now)).toBe("9 分 5 秒内有效");
    expect(pairingLeft(now / 1000 - 3, now)).toBe("0 分 0 秒内有效");
  });
});

const hub = (o: Partial<HubUpdateStatus> = {}): HubUpdateStatus => ({
  version: "v1", build: 1, channel: "release", can_update: true, state: "idle", busy: [], auto: false, auto_notice: false, confirmed: true, ...o,
});

describe("hub update", () => {
  it("names the channel, the state and the history", () => {
    expect([channelTitle("release"), channelTitle("docker"), channelTitle("")]).toEqual(["发布版", "Docker", "开发版"]);
    expect(updateStateTitle(hub({ state: "waiting_idle" }))).toBe("等待助手和 Agent 进入空闲状态");
    expect(updateStateTitle(hub({ state: "waiting_idle", busy: [{ machine_id: "m", machine: "M", title: "t" }] }))).toBe("等待 1 个进行中的任务完成");
    expect(updateStateTitle(hub())).toBe("已是最新");
    expect([recordTitle("updated"), recordTitle("held"), recordTitle("failed")]).toEqual(["已更新", "未自动安装", "更新失败"]);
    expect([recordTone("updated"), recordTone("rolled_back"), recordTone("held"), recordTone("failed")]).toEqual(["green", "orange", "orange", "red"]);
  });

  it("knows an update under way and one that just ended", () => {
    expect(updateWorking(hub({ state: "restarting" }))).toBe(true);
    expect(updateWorking(hub({ state: "available" }))).toBe(false);
    expect(updateFinished("restarting", "updated")).toBe(true);
    expect(updateFinished("updated", "updated")).toBe(false);
    expect(updateFinished(undefined, "available")).toBe(false);
  });

  it("picks what the 更新 group shows", () => {
    const latest = { version: "v2", build: 2, newer: true, bad: false };
    expect(updateView(hub({ channel: "docker", can_update: false }))).toEqual({ kind: "docker" });
    expect(updateView(hub({ can_update: false, why_not: "开发版" }))).toEqual({ kind: "cannot", why: "此 hub 不支持自动更新：开发版。" });
    expect(updateView(hub({ latest }))).toEqual({ kind: "newer", working: false });
    expect(updateView(hub({ latest, state: "waiting_idle" }))).toEqual({ kind: "newer", working: true });
    expect(updateView(hub({ state: "confirming" }))).toEqual({ kind: "working" });
    expect(updateView(hub({ build: latest.build, latest: { ...latest, newer: false } }))).toEqual({ kind: "current" });
  });

  it("compares build numbers for release manifests and keeps other update flows separate", () => {
    const latest = { version: "v1", build: 1, newer: false, bad: false };
    expect(updateView(hub({ build: 2, latest }))).toEqual({ kind: "ahead" });
    expect(updateView(hub({ source: "beta", build: 2, latest, can_update: false }))).toEqual({ kind: "ahead" });
    expect(updateView(hub({ source: "release", build: 2, latest, state: "confirming" }))).toEqual({ kind: "working" });
    expect(updateView(hub({ source: "web-dev", build: 2, latest }))).toEqual({ kind: "current" });
    expect(updateView(hub({ channel: "docker", build: 2, latest }))).toEqual({ kind: "docker" });
    expect(updateView(hub({ source: "release", build: 2, latest: { ...latest, build: 0 } }))).toEqual({ kind: "current" });
  });

  it.each(["release", "beta"] as const)("classifies an unchecked %s source", source => {
    expect(updateView(hub({ source }))).toEqual({ kind: "unchecked" });
    expect(updateView(hub({ source, last_check: "2026-11-20T15:04:05Z" }))).toEqual({ kind: "current" });
  });

  it("warns about a release that went back or is held", () => {
    const latest = { version: "v2", build: 2, newer: true, bad: false, held: "旧代码" };
    expect(latestWarning(hub({ latest }))).toBeNull(); // held only matters with auto-update on
    expect(latestWarning(hub({ latest, auto: true }))).toBe("自动更新不会安装此版本：旧代码。若确认无误，你可以手动更新。");
    expect(latestWarning(hub({ latest: { ...latest, bad: true } }))).toContain("已自动回滚");
    expect(latestWarning(hub({ latest: { ...latest, newer: false }, auto: true }))).toBeNull();
  });

  it("says when the update source looks stopped", () => {
    const latest = { version: "v0.1.17-177-g563f7fde5b-202611071005", build: 202611071005, newer: false, bad: false };
    const site = { latest, update_url: "https://personaside.example/dl", update_url_from: "build" };
    expect(sourceBehindWarning(hub({ ...site, source_behind: true }))).toBe(
      "更新源 https://personaside.example/dl（编译时写入）的最新版本仅为 v0.1.17-177-g563f7fde5b-202611071005，低于当前 hub 版本，可能已停止更新。请在 hub.json 中修改 update.url 以更换更新源。",
    );
    expect(sourceBehindWarning(hub({ ...site, update_url_from: "hub.json", source_behind: true }))).toContain("（hub.json 的 update.url）");
    expect(sourceBehindWarning(hub(site))).toBeNull(); // a current source: "Up to date" as before
    expect(sourceBehindWarning(hub({ source_behind: true }))).toBeNull(); // nothing to name
    const stopped = hub({ ...site, build: 202611141806, source_behind: true });
    expect(updateView(stopped)).toEqual({ kind: "source_behind", warning: sourceBehindWarning(stopped) });
  });

  it("tells a Docker hub what to run", () => {
    const docker = hub({ channel: "docker", can_update: false, image: { name: "ghcr.io/owner/x", tag: "latest", pinned: false } });
    expect(dockerSteps(docker).headline).toBeUndefined();
    const newer = { ...docker, latest: { version: "0.3.1", build: 0, newer: true, bad: false } };
    expect(dockerSteps(newer).steps.map((s) => s.copy)).toEqual([DOCKER_COMMAND]);
    const pinned = { ...newer, image: { name: "ghcr.io/owner/x", tag: "0.3.0", pinned: true } };
    expect(dockerSteps(pinned)).toMatchObject({ headline: "新镜像 0.3.1", steps: [{ copy: "ghcr.io/owner/x:0.3.1" }, { copy: DOCKER_COMMAND }] });
  });

  it("derives running channel, check eligibility, and post-switch auto-check", () => {
    expect(runningChannelOf(hub({ version: "web-dev-abc123", channel: "release" }))).toBe("web-dev");
    expect(runningChannelOf(hub({ version: "v1.0.0", channel: "release" }))).toBe("release");
    expect(runningChannelOf(hub({ version: "v1.0.0-beta.1", channel: "beta" }))).toBe("beta");

    expect(canCheckHubUpdate(hub({ can_update: true, channel: "release" }))).toBe(true);
    expect(canCheckHubUpdate(hub({ can_update: false, channel: "docker" }))).toBe(true);
    expect(canCheckHubUpdate(hub({ can_update: false, channel: "release" }))).toBe(false);
    expect(canCheckHubUpdate(hub({ can_update: false, channel: "dev" }))).toBe(false);

    expect(shouldAutoCheckAfterSourceSwitch(hub({ source: "release", can_update: true, latest: undefined }))).toBe(true);
    expect(shouldAutoCheckAfterSourceSwitch(hub({ source: "beta", can_update: true, latest: undefined }))).toBe(true);
    expect(shouldAutoCheckAfterSourceSwitch(hub({ source: "web-dev", can_update: true, latest: undefined, supports_preview: true }))).toBe(true);
    expect(shouldAutoCheckAfterSourceSwitch(hub({ source: "web-dev", can_update: true, latest: undefined, supports_preview: false }))).toBe(false);
    expect(shouldAutoCheckAfterSourceSwitch(hub({ source: "release", can_update: false, latest: undefined }))).toBe(false);
    expect(shouldAutoCheckAfterSourceSwitch(hub({ source: "release", can_update: true, latest: { version: "v1", build: 1, newer: false, bad: false } }))).toBe(false);
    expect(shouldAutoCheckAfterSourceSwitch(hub({ source: "release", can_update: true, state: "checking", latest: undefined }))).toBe(false);
    expect(shouldAutoCheckAfterSourceSwitch(hub({ source: "release", can_update: true, state: "downloading", latest: undefined }))).toBe(false);
  });

  it("builds structured guidance for ahead, cannot, and source_behind states", () => {
    const latest = { version: "v1.0.0", build: 100, newer: false, bad: false };

    const webDevAhead = updateGuidance(hub({ version: "web-dev-aaaaaaaaaaaa-20261125", build: 200, source: "release", latest }));
    expect(webDevAhead).toMatchObject({ tone: "neutral" });
    expect(webDevAhead?.title).toContain("Web 开发版");
    expect(webDevAhead?.body).toContain("不支持降级");
    expect(webDevAhead?.nextStep).toContain("更新来源");

    const releaseAhead = updateGuidance(hub({ version: "v1.2.0-beta.1", channel: "beta", build: 200, source: "release", latest }));
    expect(releaseAhead).toMatchObject({ tone: "neutral" });
    expect(releaseAhead?.title).not.toContain("Web 开发版");
    expect(releaseAhead?.body).toContain("不支持降级");
    expect(releaseAhead?.nextStep).toContain("更新来源");

    const privateCannot = updateGuidance(hub({
      can_update: false,
      source: "release",
      why_not: "hub 已启用私有网络模式。在 hub.json 中将 update.url 设为内部发布源的地址，然后重启 hub。",
      development: { configured: true, branch: "main" },
    }));
    expect(privateCannot).toMatchObject({
      tone: "caution",
      body: "hub 已启用私有网络模式。在 hub.json 中将 update.url 设为内部发布源的地址，然后重启 hub。",
    });
    expect(privateCannot?.nextStep).toContain("hub.json");
    expect(privateCannot?.nextStep).toContain("切回");

    const badSourceCannot = updateGuidance(hub({
      can_update: false,
      source: "release",
      why_not: "hub.json 里的 update.url 用不了：only https:// and file:// addresses work",
    }));
    expect(badSourceCannot).toMatchObject({ tone: "caution" });
    expect(badSourceCannot?.body).toContain("update.url");
    expect(badSourceCannot?.nextStep).toContain("重启 hub");

    const devBuildCannot = updateGuidance(hub({
      can_update: false,
      channel: "dev",
      source: "release",
      why_not: "开发版（dev）",
    }));
    expect(devBuildCannot?.nextStep).toContain("官方发布版本");

    const devBuildWithWebDev = updateGuidance(hub({
      can_update: false,
      channel: "dev",
      source: "release",
      why_not: "开发版（dev）",
      development: { configured: true, branch: "main" },
    }));
    expect(devBuildWithWebDev?.nextStep).toContain("Web 开发版");

    const webDevCannot = updateGuidance(hub({
      can_update: false,
      source: "web-dev",
      why_not: "开发版更新需要 Linux 或 macOS 上的 hub。",
    }));
    expect(webDevCannot?.nextStep).toContain("发布版");

    const fallbackCannot = updateGuidance(hub({ can_update: false, why_not: undefined }));
    expect(fallbackCannot?.body).toBe("此 hub 不支持自动更新。");

    const stopped = hub({
      build: 202611141806,
      latest: { version: "v0.1.17", build: 202611071005, newer: false, bad: false },
      update_url: "https://personaside.example/dl",
      update_url_from: "build",
      source_behind: true,
    });
    expect(updateGuidance(stopped)).toMatchObject({ tone: "caution", body: sourceBehindWarning(stopped) });

    expect(updateGuidance(hub({ build: 100, latest }))).toBeNull();
    expect(updateGuidance(hub({ channel: "docker", can_update: false }))).toBeNull();
  });
});
