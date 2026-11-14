// Draws the /releases/ page from /releases/data.json (site/releases.mjs). Every piece of text from the data goes in as a
// text node; links are only GitHub's own download and release addresses (model.js isDirectDownload). The packages are
// downloaded from GitHub directly: nothing here goes through this site.
import {
  GETTING_STARTED, GITHUB_RELEASES, HARMONY_GUIDE, TESTFLIGHT,
  archLabel, areaLabel, compareVersions, componentsFor, countsLine, detectPlatform, deviceFor, devicesFor, downloadFor,
  formatAge, formatDate, formatSize, hasPackages, isDirectDownload, latestRelease, webGuideFor,
} from "./model.js";
import { pageText } from "./text.js";

const LANG = document.documentElement.lang === "en" ? "en" : "zh";
const T = pageText(LANG);
const DEVICES = devicesFor(LANG);
const $ = (id) => document.getElementById(id);
const RELEASE_PAGE = GITHUB_RELEASES + "/tag/";
const HISTORY_FIRST = 8;

// ---------- small builders ----------

function el(tag, props = {}, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
    else n.setAttribute(k, v === true ? "" : v);
  }
  for (const k of kids.flat()) if (k) n.append(k);
  return n;
}

// Icons: fixed markup only, never data.
const ICONS = {
  ios: '<rect x="6.5" y="2.5" width="11" height="19" rx="2.8"/><path d="M10.5 18.5h3"/>',
  android: '<path d="M5.5 17.5v-2a6.5 6.5 0 0 1 13 0v2z"/><path d="m8 10.2-1.6-2.6M16 10.2l1.6-2.6"/><circle cx="9.5" cy="14.2" r=".6" fill="currentColor"/><circle cx="14.5" cy="14.2" r=".6" fill="currentColor"/>',
  harmony: '<rect x="3" y="4" width="18" height="16" rx="3"/><path d="M12 4v16M6.5 9.5h2.5M15 14.5h2.5"/>',
  macos: '<rect x="4" y="5" width="16" height="11" rx="2"/><path d="M2 19.5h20"/>',
  windows: '<rect x="3.5" y="3.5" width="7.5" height="7.5" rx="1.2"/><rect x="13" y="3.5" width="7.5" height="7.5" rx="1.2"/><rect x="3.5" y="13" width="7.5" height="7.5" rx="1.2"/><rect x="13" y="13" width="7.5" height="7.5" rx="1.2"/>',
  linux: '<rect x="3" y="4" width="18" height="16" rx="3"/><path d="m7.5 9.5 3 2.5-3 2.5M12.5 15h4"/>',
  web: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.6 2.6 3.8 5.6 3.8 9s-1.2 6.4-3.8 9c-2.6-2.6-3.8-5.6-3.8-9S9.4 5.6 12 3z"/>',
  down: '<path d="M12 4v11M7 10.5l5 5 5-5M5 20h14"/>',
  out: '<path d="M8 16 16 8M9.5 8H16v6.5"/>',
  file: '<path d="M7 3h7l4 4v14H7z"/><path d="M14 3v4h4"/>',
};
function icon(name) {
  const t = document.createElement("template");
  t.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name]}</svg>`;
  return t.content.firstChild;
}

const deviceOf = (id) => deviceFor(id, LANG);
const PLATFORM_NAME = { macos: "Mac", linux: "Linux", windows: "Windows", android: "Android", harmony: deviceOf("harmony").short, web: deviceOf("web").short };

function releaseURL(r) {
  return typeof r.url === "string" && r.url === RELEASE_PAGE + encodeURIComponent(r.tag) ? r.url : GITHUB_RELEASES;
}

function fileChip(asset, label) {
  const a = el("a", { class: "file", href: asset.url, title: asset.sha256 ? `${asset.name}\nSHA-256 ${asset.sha256}` : asset.name },
    icon("down"), el("span", { text: label }), el("small", { text: formatSize(asset.size) }));
  return a;
}

function assetLabel(a) {
  if (a.kind === "app") return a.platform === "harmony" ? deviceOf("harmony").short : "Android";
  if (a.kind === "computer" || a.kind === "component") return `${PLATFORM_NAME[a.platform]} · ${archLabel(a.platform, a.arch, LANG)}`;
  return a.name;
}

function webGuide(cls) {
  return el("ol", { class: cls }, webGuideFor(LANG).map((step) => el("li", {}, el("b", { text: step.title }),
    el("span", {}, document.createTextNode(step.text), step.code ? el("code", { text: step.code }) : null))));
}

// ---------- the ticket ----------

function primaryButton({ href, text, size, external, cls = "btn btn-cheek", iconName = "down" }) {
  return el("a", { class: cls, href, target: external ? "_blank" : null, rel: external ? "noopener" : null },
    icon(external ? "out" : iconName), el("span", { text }), size ? el("small", { text: size }) : null);
}

function notice(kind, ...parts) {
  return el("p", { class: `notice notice-${kind}` }, el("i", { "aria-hidden": "true" }), el("span", {}, ...parts));
}

function drawTicket(data, det) {
  const ticket = $("ticket");
  const latest = latestRelease(data);
  ticket.classList.remove("loading");
  ticket.removeAttribute("aria-busy");

  $("tk-ver").replaceChildren(el("span", { text: "v" }), document.createTextNode(latest.version));
  $("tk-chip").hidden = false;
  $("tk-date").textContent = T.releasedOn(formatDate(latest.published_at, LANG));
  const c = latest.counts || {};
  if (c.features || c.fixes) {
    $("tk-feat").textContent = String(c.features || 0);
    $("tk-fix").textContent = String(c.fixes || 0);
    $("tk-stats").hidden = false;
  }

  const mine = det.platform ? deviceOf(det.platform) : null;
  $("tk-for").replaceChildren(...(mine
    ? [icon(mine.id), document.createTextNode(T.detected(mine))]
    : [document.createTextNode(T.chooseDevice)]));

  // One tab per device: the visitor's own first selected, every other one a click away.
  const tabs = $("tk-tabs");
  const select = (id) => {
    for (const b of tabs.children) b.setAttribute("aria-pressed", String(b.dataset.id === id));
    drawDownload(data, deviceOf(id), id === det.platform ? det.arch : "");
  };
  tabs.replaceChildren(...DEVICES.map((dev) => {
    const b = el("button", { type: "button", class: "tab", "data-id": dev.id, "aria-pressed": "false" }, icon(dev.id), el("span", { text: dev.short }));
    b.addEventListener("click", () => select(dev.id));
    return b;
  }));
  tabs.hidden = false;
  select(mine ? mine.id : "ios");
  ticket.classList.add("ready");
}

// drawDownload: the ticket's buttons, file line and notices for one device.
function drawDownload(data, dev, arch) {
  const latest = latestRelease(data);
  const notices = $("tk-notices");
  const actions = $("tk-actions");
  notices.replaceChildren();
  actions.replaceChildren();
  $("tk-meta").textContent = "";
  if (dev.id === "ios") {
    $("tk-title").textContent = T.iosTicketTitle;
    actions.append(primaryButton({ href: TESTFLIGHT, text: T.openTestFlight, external: true }));
    $("tk-meta").textContent = T.iosTicketMeta;
  } else if (dev.guide) {
    $("tk-title").textContent = T.webTicketTitle;
    actions.append(webGuide("tk-guide"));
    $("tk-meta").textContent = T.webTicketMeta;
  } else {
    $("tk-title").textContent = T.downloadTitle(dev);
    const arches = dev.arches.slice().sort((a, b) => (a === arch ? -1 : b === arch ? 1 : 0));
    const found = arches.map((x) => ({ arch: x, d: downloadFor(data, dev.id, x) })).filter((x) => x.d);
    found.forEach(({ arch: x, d }, i) => {
      const text = dev.id === "android" ? T.downloadAPK : dev.id === "harmony" ? T.downloadHAP : archLabel(dev.id, x, LANG);
      actions.append(primaryButton({ href: d.asset.url, text, size: formatSize(d.asset.size), cls: i === 0 ? "btn btn-cheek" : "btn btn-paper" }));
    });
    if (found.length) {
      const first = found[0].d;
      const hint = dev.id === "macos" ? T.macArchHint
        : dev.id === "windows" ? T.windowsArchHint : dev.id === "linux" ? T.linuxArchHint
        : "";
      $("tk-meta").textContent = T.directMeta(first.asset.name, hint);
      if (!first.current) {
        notices.append(notice("amber", document.createTextNode(T.uploadingNotice(latest.tag, dev, first.tag))));
      }
    } else {
      if (dev.id === "harmony") {
        actions.append(primaryButton({ href: HARMONY_GUIDE, text: T.buildFromSource, external: true }));
        $("tk-meta").textContent = T.harmonySourceMeta;
      } else {
        actions.append(primaryButton({ href: releaseURL(latest), text: T.viewOnGitHub, external: true, cls: "btn btn-paper" }));
      }
      notices.append(notice("cheek", document.createTextNode(T.noPackage(dev))));
    }
  }
  if (data.stale) {
    notices.append(notice("amber", document.createTextNode(T.staleNotice(formatAge(data.fetched_at, Date.now(), LANG)))));
  }
}

function drawFailure(retry) {
  const ticket = $("ticket");
  ticket.classList.remove("loading", "ready");
  ticket.removeAttribute("aria-busy");
  $("tk-ver").replaceChildren(el("span", { text: "v" }), document.createTextNode("?"));
  $("tk-date").textContent = T.failureDate;
  $("tk-for").replaceChildren(document.createTextNode(T.failureFor));
  $("tk-title").textContent = T.failureTitle;
  $("tk-meta").textContent = T.failureMeta;
  $("tk-notices").replaceChildren();
  $("tk-tabs").hidden = true;
  const again = el("button", { class: "btn btn-cheek", type: "button" }, el("span", { text: T.retry }));
  again.addEventListener("click", retry);
  $("tk-actions").replaceChildren(again,
    primaryButton({ href: GITHUB_RELEASES + "/latest", text: T.downloadOnGitHub, external: true, cls: "btn btn-paper" }));
  $("history-list").replaceChildren(el("li", {}, notice("cheek", document.createTextNode(T.historyUnavailable))));
}

// ---------- devices ----------

function drawDevices(data, det) {
  const grid = $("devices-grid");
  grid.replaceChildren(...DEVICES.map((dev) => {
    const you = det.platform === dev.id;
    const card = el("article", { class: ["dev", you ? "you" : "", dev.guide ? "guide" : ""].filter(Boolean).join(" "), id: `dev-${dev.id}` },
      you ? el("span", { class: "chip you-chip", text: T.yourDevice }) : null,
      el("div", { class: "dev-head" }, el("span", { class: "dev-ico" }, icon(dev.id)),
        el("div", {}, el("h3", { text: dev.name }), el("p", { text: dev.sub }))),
      el("p", { class: "dev-what", text: dev.what }),
      dev.caveat ? el("p", { class: "dev-caveat", text: dev.caveat }) : null);
    const box = el("div", { class: "dev-dl" });
    if (dev.store) {
      box.append(el("a", { class: you ? "dl primary" : "dl", href: TESTFLIGHT, target: "_blank", rel: "noopener" },
        el("span", { class: "l" }, icon("out"), el("span", { text: T.openTestFlight })), el("span", { class: "r", text: "Apple" })));
    } else if (dev.guide) {
      card.append(webGuide("dev-guide"));
      box.append(el("a", { class: "dl", href: GETTING_STARTED, target: "_blank", rel: "noopener" },
        el("span", { class: "l" }, icon("out"), el("span", { text: T.fullGuide })), el("span", { class: "r", text: "GitHub" })));
    } else if (!data) {
      box.append(el("a", { class: "dl", href: GITHUB_RELEASES + "/latest", target: "_blank", rel: "noopener" },
        el("span", { class: "l" }, icon("out"), el("span", { text: T.downloadFromGitHub })), el("span", { class: "r", text: "GitHub" })));
    } else {
      const arches = you ? dev.arches.slice().sort((a, b) => (a === det.arch ? -1 : b === det.arch ? 1 : 0)) : dev.arches;
      arches.forEach((arch, i) => {
        const d = downloadFor(data, dev.id, arch);
        const label = dev.id === "android" ? T.downloadAPK : dev.id === "harmony" ? T.downloadHAP : archLabel(dev.id, arch, LANG);
        if (!d) {
          if (dev.id === "harmony") {
            box.append(el("a", { class: "dl", href: HARMONY_GUIDE, target: "_blank", rel: "noopener" },
              el("span", { class: "l" }, icon("out"), el("span", { text: T.buildFromSource })), el("span", { class: "r", text: "GitHub" })));
          } else {
            box.append(el("span", { class: "dl off" }, el("span", { class: "l", text: label }), el("span", { class: "r", text: T.notAvailable })));
          }
          return;
        }
        const side = formatSize(d.asset.size) + (d.current ? "" : ` · ${d.tag}`);
        box.append(el("a", { class: you && i === 0 ? "dl primary" : "dl", href: d.asset.url, title: d.asset.name },
          el("span", { class: "l" }, icon("down"), el("span", { text: label })), el("span", { class: "r", text: side })));
      });
    }
    card.append(box);
    return card;
  }));
}

// ---------- components and checksums ----------

function drawParts(data) {
  // The newest stable release that has components: the latest one may still be uploading them.
  const hasComponents = (r) => r.assets.some((a) => a.kind === "component");
  const rel = data.releases.filter((r) => !r.prerelease).sort((a, b) => compareVersions(b.version, a.version)).find(hasComponents);
  $("parts-sec").hidden = !rel;
  if (!rel) return;
  $("parts-sub").textContent = T.partsSubtitle(rel.tag);
  const body = $("parts-body");
  const rows = [];
  for (const comp of componentsFor(LANG)) {
    const files = rel.assets.filter((a) => a.kind === "component" && a.component === comp.id && isDirectDownload(a));
    if (!files.length) continue;
    rows.push(el("div", { class: "comp" },
      el("div", {}, el("b", { text: comp.name }), el("p", { text: comp.what })),
      el("div", { class: "files" }, files.map((a) => fileChip(a, assetLabel(a))))));
  }
  const others = rel.assets.filter((a) => a.kind === "other" && isDirectDownload(a));
  if (others.length) {
    rows.push(el("div", { class: "comp" }, el("div", {}, el("b", { text: T.otherFiles })),
      el("div", { class: "files" }, others.map((a) => fileChip(a, a.name)))));
  }
  const sums = rel.assets.filter((a) => a.kind === "checksum" && isDirectDownload(a));
  if (sums.length) {
    rows.push(el("div", { class: "comp" },
      el("div", {}, el("b", { text: T.checksumFiles }), el("p", { text: T.checksumDescription })),
      el("div", { class: "files" }, sums.map((a) => fileChip(a, a.name)))));
  }
  const verify = el("p", { class: "verify" }, document.createTextNode(T.verifyBefore),
    el("code", { text: T.verifyCommand }), document.createTextNode(T.verifyLinuxBefore), el("code", { text: T.verifyLinuxCommand }),
    document.createTextNode(T.verifyAfter));
  body.replaceChildren(...rows, verify);
}

// ---------- history ----------

function historyItem(r, data) {
  const isLatest = r.tag === data.latest;
  const pending = !hasPackages(r);
  const cls = ["rel", isLatest ? "latest" : "", r.prerelease ? "pre" : "", pending ? "pending" : ""].filter(Boolean).join(" ");
  const tags = el("span", { class: "rel-tags" },
    isLatest ? el("span", { class: "chip chip-go", text: T.latest }) : null,
    r.prerelease ? el("span", { class: "chip chip-amber", text: T.prerelease }) : null,
    pending ? el("span", { class: "chip chip-cheek", text: T.uploading }) : null);
  const meta = el("span", { class: "rel-meta", text: [formatDate(r.published_at, LANG), countsLine(r.counts, LANG)].filter(Boolean).join(" · ") });
  const summary = el("summary", {}, el("span", { class: "rel-ver", text: r.tag }), tags, meta, el("i", { class: "chev", "aria-hidden": "true" }));

  const body = el("div", { class: "rel-body" });
  if (r.highlights && r.highlights.length) {
    body.append(el("ul", { class: "rel-hl" }, r.highlights.map((h) => {
      const area = areaLabel(h.scope, LANG);
      return el("li", {}, el("span", {}, area ? el("span", { class: "area", text: area + T.areaSeparator }) : null, document.createTextNode(h.title)));
    })));
  }
  const main = r.assets.filter((a) => (a.kind === "app" || a.kind === "computer") && isDirectDownload(a));
  const order = { app: 0, computer: 1 };
  main.sort((a, b) => order[a.kind] - order[b.kind] || String(a.platform).localeCompare(String(b.platform)) || String(b.arch).localeCompare(String(a.arch)));
  if (main.length) body.append(el("div", { class: "rel-files" }, main.map((a) => fileChip(a, assetLabel(a)))));
  else if (pending) body.append(notice("amber", document.createTextNode(T.pendingHistory)));
  const rest = r.assets.length - main.length;
  body.append(el("p", { class: "rel-foot" },
    el("a", { class: "link", href: releaseURL(r), target: "_blank", rel: "noopener", text: T.fullNotes(rest) })));
  return el("li", { class: cls }, el("details", {}, summary, body));
}

function drawHistory(data) {
  const list = $("history-list");
  const items = data.releases.map((r) => historyItem(r, data));
  if (!items.length) {
    list.replaceChildren(el("li", {}, notice("amber", document.createTextNode(T.noReleases))));
    return;
  }
  list.replaceChildren(...items.slice(0, HISTORY_FIRST));
  if (items.length > HISTORY_FIRST) {
    const more = el("button", { class: "btn btn-paper", type: "button" }, el("span", { text: T.showEarlier(items.length - HISTORY_FIRST) }));
    const li = el("li", { class: "rel" }, more);
    more.addEventListener("click", () => li.replaceWith(...items.slice(HISTORY_FIRST)));
    list.append(li);
  }
}

// ---------- load ----------

const det = detectPlatform(navigator);
drawDevices(null, det);

async function load(fresh) {
  try {
    const res = await fetch("/releases/data.json", { cache: fresh ? "reload" : "default", headers: { accept: "application/json" } });
    if (!res.ok) throw new Error(String(res.status));
    const data = await res.json();
    if (!latestRelease(data)) throw new Error("no release");
    drawTicket(data, det);
    drawDevices(data, det);
    drawParts(data);
    drawHistory(data);
  } catch {
    drawFailure(() => {
      $("ticket").classList.add("loading");
      load(true);
    });
    drawDevices(null, det);
  }
}

// Chromium tells the real architecture when asked; Safari and Firefox don't, so the guess stays.
const ua = navigator.userAgentData;
const ask = ua && typeof ua.getHighEntropyValues === "function"
  ? ua.getHighEntropyValues(["architecture", "bitness"]).then((v) => {
    if (det.platform === "macos" || det.platform === "windows" || det.platform === "linux") {
      if (v.architecture === "arm") det.arch = "arm64";
      else if (v.architecture === "x86") det.arch = "amd64";
    }
  }).catch(() => {})
  : Promise.resolve();
ask.then(() => load(false));
