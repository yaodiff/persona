// The /releases/ page's logic without the DOM: which device the visitor is on, which file each device gets, and how
// sizes, dates and areas read. page.js draws with it; site/releases.test.mjs tests it.
import { EN_COMPONENTS, EN_DEVICES, EN_WEB_GUIDE } from "./text.js";

export const GITHUB_RELEASES = "https://github.com/nwverse/persona/releases";
export const DOWNLOAD_ORIGIN = GITHUB_RELEASES + "/download/";
export const TESTFLIGHT = "https://testflight.apple.com/join/vsjfy3hE";
export const HARMONY_GUIDE = "https://github.com/nwverse/persona/blob/main/harmony/README.md";

// The devices, in the order the page lists them. arches: the files offered, the usual one first.
export const DEVICES = [
  { id: "ios", short: "iPhone", name: "iPhone 与 Apple Watch", sub: "TestFlight 公开测试 · 需要 iOS 26",
    what: "主力 App：和助手聊天，看每个 Agent 做到哪了，替它们拿主意。手表版随 iPhone 版一起提供。", store: true },
  { id: "android", short: "Android", name: "Android", sub: "APK 安装包 · Android 11 及以上",
    what: "和 iPhone 版一样：聊天、看进度、回答 Agent 的问题，通知也能直接处理。", arches: [""] },
  { id: "harmony", short: "鸿蒙", name: "鸿蒙", sub: "原生手机与折叠屏 · HarmonyOS 7",
    what: "原生 ArkUI：聊天、Agents、收件箱和文件。正式安装包发布后可直接下载，现在也可从源码构建。", arches: [""] },
  { id: "macos", short: "Mac", name: "Mac", sub: "Apple 芯片和 Intel 芯片",
    what: "装在电脑上：可以当 hub，替你保管所有数据；也可以连上 hub，让这台电脑跑 Agent。",
    caveat: "还没有经过苹果公证。打开时如果被拦下，到「系统设置 › 隐私与安全性」里点「仍要打开」。", arches: ["arm64", "amd64"] },
  { id: "windows", short: "Windows", name: "Windows", sub: "x64 和 ARM",
    what: "装在电脑上：连上 hub，让这台电脑跑 Agent，也能收发文件和通知。",
    caveat: "程序还没有签名。Windows 提示有风险时，点「更多信息 › 仍要运行」。", arches: ["amd64", "arm64"] },
  { id: "linux", short: "Linux", name: "Linux", sub: "x86-64 和 ARM64",
    what: "适合长期开着的机器当 hub，也可以连上 hub 跑 Agent。", arches: ["amd64", "arm64"] },
  { id: "web", short: "网页版", name: "网页版", sub: "不用安装，打开就能用",
    what: "网页版就在你的 hub 上，用浏览器打开就行：", guide: true },
];

export function devicesFor(lang = "zh") {
  return lang === "en" ? EN_DEVICES : DEVICES;
}

export function deviceFor(id, lang = "zh") {
  return devicesFor(lang).find((device) => device.id === id);
}

// How to reach the web app: it is served by the hub, so there is nothing to download (docs/operations/getting-started.md).
export const WEB_GUIDE = [
  { title: "打开 hub 的地址", text: "装 hub 时终端里打出的管理页地址，去掉末尾的 /admin 就是，比如", code: "http://<局域网地址>:18660" },
  { title: "用手机扫码登录", text: "在已连上的手机里打开「设置 › 设备 › 连接新设备」，选「浏览器」，扫网页上的二维码。" },
  { title: "装成应用（可选）", text: "Chrome、Edge 点地址栏里的「安装」；Safari 选「文件 › 添加到程序坞」。" },
];
export function webGuideFor(lang = "zh") {
  return lang === "en" ? EN_WEB_GUIDE : WEB_GUIDE;
}
export const GETTING_STARTED = "https://github.com/nwverse/persona/blob/main/docs/operations/getting-started.md";

const ARCH = {
  macos: { arm64: "Apple 芯片", amd64: "Intel 芯片" },
  windows: { amd64: "x64", arm64: "ARM" },
  linux: { amd64: "x86-64", arm64: "ARM64" },
};
const ARCH_EN = {
  macos: { arm64: "Apple silicon", amd64: "Intel" },
  windows: { amd64: "x64", arm64: "ARM" },
  linux: { amd64: "x86-64", arm64: "ARM64" },
};
export function archLabel(platform, arch, lang = "zh") {
  const labels = lang === "en" ? ARCH_EN : ARCH;
  return (labels[platform] && labels[platform][arch]) || "";
}

export const COMPONENTS = [
  { id: "personad", name: "personad", what: "全部合一：hub、管理和节点都在里面" },
  { id: "persona-hub", name: "persona-hub", what: "hub：所有设备连接的地方" },
  { id: "persona-manager", name: "persona-manager", what: "管理：助手、收件箱和扩展" },
  { id: "persona-node", name: "persona-node", what: "节点：在这台电脑上跑 Agent" },
  { id: "persona-link", name: "persona-link", what: "连接器：把电脑连到 hub" },
];
export function componentsFor(lang = "zh") {
  return lang === "en" ? EN_COMPONENTS : COMPONENTS;
}

// detectPlatform reads the browser's own report. arch is a guess where browsers don't say (Safari on a Mac reports
// Intel on Apple silicon too), so callers still show every file.
export function detectPlatform(nav) {
  const ua = String((nav && nav.userAgent) || "");
  const hint = String((nav && nav.userAgentData && nav.userAgentData.platform) || "");
  const touch = nav && nav.maxTouchPoints > 1;
  if (/iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && touch)) return { platform: "ios", arch: "" };
  if (/HarmonyOS|OpenHarmony|OHOS/i.test(ua) || /HarmonyOS|OpenHarmony|OHOS/i.test(hint)) return { platform: "harmony", arch: "" };
  if (/Android/i.test(ua) || /Android/i.test(hint)) return { platform: "android", arch: "" };
  if (/Mac/.test(hint) || /Macintosh|Mac OS X/.test(ua)) return { platform: "macos", arch: "arm64" };
  if (/Windows/.test(hint) || /Windows/.test(ua)) return { platform: "windows", arch: /ARM64|aarch64/i.test(ua) ? "arm64" : "amd64" };
  if (/CrOS/.test(ua)) return { platform: null, arch: "" };
  if (/Linux/.test(hint) || /Linux|X11/.test(ua)) return { platform: "linux", arch: /aarch64|arm64/i.test(ua) ? "arm64" : "amd64" };
  return { platform: null, arch: "" };
}

// isDirectDownload: a file address the page will link to. Anything else is dropped rather than shown.
// It must be GitHub's own download address for this repository: <origin><tag>/<name>, nothing more.
export function isDirectDownload(asset) {
  if (!asset || typeof asset.url !== "string" || typeof asset.name !== "string") return false;
  if (!asset.url.startsWith(DOWNLOAD_ORIGIN)) return false;
  const parts = asset.url.slice(DOWNLOAD_ORIGIN.length).split("/");
  if (parts.length !== 2) return false;
  let tag;
  try { tag = decodeURIComponent(parts[0]); } catch { return false; }
  return /^v\d+\.\d+\.\d+/.test(tag) && parts[0] === encodeURIComponent(tag) && parts[1] === encodeURIComponent(asset.name);
}

function matches(asset, platform, arch) {
  if (!isDirectDownload(asset)) return false;
  if (platform === "android" || platform === "harmony") return asset.kind === "app" && asset.platform === platform;
  if (platform === "web") return asset.kind === "web";
  return asset.kind === "computer" && asset.platform === platform && asset.arch === arch;
}

export function latestRelease(data) {
  const list = (data && Array.isArray(data.releases)) ? data.releases : [];
  return list.find((r) => r.tag === data.latest) || null;
}

// downloadFor: the file for a device and architecture from the latest stable release, or else from the newest stable
// release that has one (a release whose files are still uploading). null when no release has it.
export function downloadFor(data, platform, arch) {
  const latest = latestRelease(data);
  if (!latest) return null;
  const stable = data.releases.filter((r) => !r.prerelease).sort((a, b) => compareVersions(b.version, a.version));
  for (const r of stable) {
    const asset = r.assets.find((a) => matches(a, platform, arch));
    if (asset) return { asset, tag: r.tag, current: r.tag === latest.tag };
  }
  return null;
}

// compareVersions: numeric x.y.z order; enough for picking among stable releases.
export function compareVersions(a, b) {
  const p = String(a).split(/[.-]/), q = String(b).split(/[.-]/);
  for (let i = 0; i < 3; i++) if ((+p[i] || 0) !== (+q[i] || 0)) return (+p[i] || 0) - (+q[i] || 0);
  return 0;
}

export function hasPackages(release) {
  return release.assets.some((a) => a.kind === "app" || a.kind === "computer" || a.kind === "component");
}

export function formatSize(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return Math.max(1, Math.round(bytes / 1024)) + " KB";
  const mb = bytes / (1024 * 1024);
  return (mb < 10 ? mb.toFixed(1) : Math.round(mb)) + " MB";
}

const EN_MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
export function formatDate(iso, lang = "zh") {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  if (lang === "en") return `${EN_MONTHS[d.getMonth()]} ${d.getDate()}, ${d.getFullYear()}`;
  return `${d.getFullYear()} 年 ${d.getMonth() + 1} 月 ${d.getDate()} 日`;
}

export function formatAge(iso, now = Date.now(), lang = "zh") {
  if (typeof now === "string") {
    lang = now;
    now = Date.now();
  }
  const min = Math.max(0, Math.round((now - Date.parse(iso)) / 60000));
  if (!Number.isFinite(min)) return "";
  if (lang === "en") {
    if (min < 1) return "just now";
    if (min < 60) return `${min} ${min === 1 ? "minute" : "minutes"} ago`;
    if (min < 48 * 60) {
      const hours = Math.round(min / 60);
      return `${hours} ${hours === 1 ? "hour" : "hours"} ago`;
    }
    const days = Math.round(min / 1440);
    return `${days} ${days === 1 ? "day" : "days"} ago`;
  }
  if (min < 1) return "刚刚";
  if (min < 60) return `${min} 分钟前`;
  if (min < 48 * 60) return `${Math.round(min / 60)} 小时前`;
  return `${Math.round(min / 1440)} 天前`;
}

// Areas from the notes' "feat(<area>)", in words a visitor knows; others are not shown.
const AREAS = {
  ios: "iPhone", apple: "Apple", watch: "Apple Watch", watchos: "Apple Watch", vision: "Vision Pro", carplay: "CarPlay",
  android: "Android", harmony: "鸿蒙", web: "网页版", assistant: "助手", chat: "聊天", inbox: "收件箱", tasks: "任务", todos: "待办",
  kb: "知识库", knowledge: "知识库", voice: "语音", recording: "录音", mail: "邮件", extensions: "扩展", site: "官网",
  agents: "Agent", sidebar: "侧边栏", health: "健康", link: "Persona Link", hub: "hub", server: "hub", node: "电脑",
};
const AREAS_EN = {
  ios: "iPhone", apple: "Apple", watch: "Apple Watch", watchos: "Apple Watch", vision: "Vision Pro", carplay: "CarPlay",
  android: "Android", harmony: "HarmonyOS", web: "Web", assistant: "Assistant", chat: "Chat", inbox: "Inbox", tasks: "Tasks", todos: "To-dos",
  kb: "Knowledge base", knowledge: "Knowledge base", voice: "Voice", recording: "Recording", mail: "Mail", extensions: "Extensions", site: "Website",
  agents: "Agent", sidebar: "Sidebar", health: "Health", link: "Persona Link", hub: "hub", server: "hub", node: "Computer",
};
export function areaLabel(scope, lang = "zh") {
  const labels = lang === "en" ? AREAS_EN : AREAS;
  return labels[String(scope || "").toLowerCase()] || "";
}

export function countsLine(counts, lang = "zh") {
  if (!counts) return "";
  const parts = [];
  if (lang === "en") {
    if (counts.features) parts.push(`${counts.features} new ${counts.features === 1 ? "feature" : "features"}`);
    if (counts.fixes) parts.push(`${counts.fixes} ${counts.fixes === 1 ? "fix" : "fixes"}`);
    if (!parts.length && counts.other) parts.push(`${counts.other} ${counts.other === 1 ? "change" : "changes"}`);
    return parts.join(" · ");
  }
  if (counts.features) parts.push(`${counts.features} 项新功能`);
  if (counts.fixes) parts.push(`${counts.fixes} 项修复`);
  if (!parts.length && counts.other) parts.push(`${counts.other} 项改动`);
  return parts.join(" · ");
}
