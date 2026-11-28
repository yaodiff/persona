// /releases/data.json: the small metadata behind the /releases/ page (docs/operations/site.md, "Releases page").
//
// The Worker reads GitHub's public release list, keeps only what the page shows (version, date, a short summary of the
// notes, and each file's name, size, checksum and direct GitHub download address) and caches that at the edge. The
// packages themselves never pass through this site: every download address must be exactly
// https://github.com/nwverse/persona/releases/download/<tag>/<name>, and the browser goes there directly.
//
// Within FRESH_MS the cached copy is served as is; after that GitHub is asked again, and when it cannot be reached the
// old copy is served, marked stale, for up to KEEP_SECONDS. With no copy at all the answer is 503.

export const REPOSITORY = "nwverse/persona";
export const RELEASES_API = `https://api.github.com/repos/${REPOSITORY}/releases?per_page=100`;
export const DOWNLOAD_ORIGIN = `https://github.com/${REPOSITORY}/releases/download/`;
export const FRESH_MS = 10 * 60 * 1000;
const KEEP_SECONDS = 30 * 24 * 3600;
const FETCH_TIMEOUT_MS = 8000;
const CACHE_KEY = "https://persona-site.cache/releases/data.json?v=2";

const TAG = /^v(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:-([0-9A-Za-z][0-9A-Za-z.-]{0,40}))?$/;
const FILE_NAME = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/;
const DIGEST = /^sha256:([0-9a-f]{64})$/;
const MAX_HIGHLIGHTS = 4;

// ---------- versions ----------

export function parseTag(tag) {
  const m = typeof tag === "string" ? TAG.exec(tag) : null;
  if (!m) return null;
  return { major: +m[1], minor: +m[2], patch: +m[3], pre: m[4] || "" };
}

// compareTags: semver order; a prerelease sorts before its release (v1.0.0-rc.1 < v1.0.0).
export function compareTags(a, b) {
  const x = parseTag(a), y = parseTag(b);
  for (const k of ["major", "minor", "patch"]) if (x[k] !== y[k]) return x[k] - y[k];
  if (x.pre === y.pre) return 0;
  if (!x.pre) return 1;
  if (!y.pre) return -1;
  const p = x.pre.split("."), q = y.pre.split(".");
  for (let i = 0; i < Math.max(p.length, q.length); i++) {
    if (p[i] === undefined) return -1;
    if (q[i] === undefined) return 1;
    const n = /^\d+$/.test(p[i]), m = /^\d+$/.test(q[i]);
    if (n && m && +p[i] !== +q[i]) return +p[i] - +q[i];
    if (n !== m) return n ? -1 : 1;
    if (p[i] !== q[i]) return p[i] < q[i] ? -1 : 1;
  }
  return 0;
}

// ---------- files ----------

const OS = { darwin: "macos", linux: "linux", windows: "windows" };
const COMPONENTS = ["personad", "persona-hub", "persona-manager", "persona-node", "persona-link"];
const CHECKSUMS = new Set(["SHA256SUMS", "latest.json", "android.json", "BUILD.json", "ARTIFACTS.md", "VERSION"]);

// classifyAsset says what a release file is, from its name alone:
//   app        Persona-<v>-android.apk / -harmony.hap    { platform: "android" | "harmony" }
//   computer   Persona-<v>-<os>-<arch>.tar.gz / .zip       { platform: "macos" | "linux" | "windows", arch }
//   web        Persona-<v>-web.tar.gz                      { platform: "web" }
//   component  <component>-<os>-<arch>[.exe].gz / .zip     { component, platform, arch }
//   checksum   SHA256SUMS, latest.json, android.json …
//   installer  install.sh (not offered on the page)
//   other      anything else (still offered, as a plain file)
export function classifyAsset(name, version) {
  const v = version.replace(/[.+]/g, "\\$&");
  let m;
  if (name === `Persona-${version}-android.apk`) return { kind: "app", platform: "android" };
  if (name === `Persona-${version}-harmony.hap`) return { kind: "app", platform: "harmony" };
  if (name === `Persona-${version}-web.tar.gz`) return { kind: "web", platform: "web" };
  if ((m = new RegExp(`^Persona-${v}-(darwin|linux|windows)-(amd64|arm64)\\.(tar\\.gz|zip)$`).exec(name))) {
    return { kind: "computer", platform: OS[m[1]], arch: m[2] };
  }
  if ((m = /^([a-z-]+)-(darwin|linux|windows)-(amd64|arm64)(?:\.exe)?\.(gz|zip)$/.exec(name)) && COMPONENTS.includes(m[1])) {
    return { kind: "component", component: m[1], platform: OS[m[2]], arch: m[3] };
  }
  if (CHECKSUMS.has(name)) return { kind: "checksum" };
  // The installer fetches its files from this site's /dl/, not from GitHub; the page does not offer it.
  if (name === "install.sh") return { kind: "installer" };
  return { kind: "other" };
}

function slimAsset(a, tag, version) {
  if (!a || typeof a !== "object" || a.state !== undefined && a.state !== "uploaded") return null;
  const { name, size } = a;
  if (typeof name !== "string" || !FILE_NAME.test(name)) return null;
  if (!Number.isSafeInteger(size) || size < 0) return null;
  const url = DOWNLOAD_ORIGIN + encodeURIComponent(tag) + "/" + encodeURIComponent(name);
  if (a.browser_download_url !== url) return null; // only GitHub's own address for this repository, tag and file
  const out = { name, size, url, ...classifyAsset(name, version) };
  const d = typeof a.digest === "string" ? DIGEST.exec(a.digest) : null;
  if (d) out.sha256 = d[1];
  return out;
}

// ---------- notes ----------

// summarizeNotes turns GitHub's generated notes ("* feat(web): … by @someone in https://…") into counts and a few
// feature titles, one per area where it can. Handles, links and PR numbers are left out; the full notes stay on GitHub.
export function summarizeNotes(body) {
  const counts = { features: 0, fixes: 0, other: 0 };
  const features = [], fixes = [];
  if (typeof body !== "string") return { counts, highlights: [] };
  for (const raw of body.slice(0, 400_000).split(/\r?\n/)) {
    if (/^#{1,6}\s*New Contributors/i.test(raw) || /^\*\*Full Changelog\*\*/.test(raw)) break;
    const item = /^\s*[*-]\s+(.+)$/.exec(raw);
    if (!item) continue;
    let text = item[1]
      .replace(/\s+by\s+@[\w-]+(?:\[bot\])?\s+in\s+\S+\s*$/, "")
      .replace(/https?:\/\/\S+/g, "")
      .replace(/@[\w-]+/g, "")
      .replace(/\s*\(#\d+\)/g, "")
      .replace(/[<>`*_]/g, "")
      .replace(/\s+/g, " ")
      .trim();
    const cc = /^([A-Za-z]+)(?:\(([^)]{1,40})\))?!?:\s*(.+)$/.exec(text);
    const type = cc ? cc[1].toLowerCase() : "";
    const scope = cc && cc[2] ? cc[2].split(/[,\s/]+/)[0].toLowerCase() : "";
    if (cc) text = cc[3];
    if (!text) continue;
    if (text.length > 140) text = text.slice(0, 139).trimEnd() + "…";
    text = text[0].toUpperCase() + text.slice(1);
    if (type === "feat") { counts.features++; features.push({ scope, title: text }); }
    else if (type === "fix") { counts.fixes++; fixes.push({ scope, title: text }); }
    else counts.other++;
  }
  const highlights = pick(features);
  return { counts, highlights: highlights.length ? highlights : pick(fixes) };
}

// Areas people use come first in the highlights; build, test and release plumbing never appears there.
const VISIBLE_AREAS = new Set(["assistant", "ios", "apple", "watch", "watchos", "vision", "carplay", "android", "harmony", "web", "chat",
  "inbox", "tasks", "todos", "kb", "knowledge", "voice", "recording", "mail", "extensions", "agents", "health", "link"]);
const PLUMBING = new Set(["release", "releases", "ci", "test", "tests", "build", "deps", "scripts", "mock", "docs", "lint",
  "tokens", "dev", "tooling", "privacy", "hooks", "workflow", "i18n", "site"]);

// pick: up to MAX_HIGHLIGHTS items, the first of each area people use, then the first of other areas, then the rest.
function pick(items) {
  const seen = new Set(), visible = [], other = [], rest = [];
  for (const it of items) {
    if (PLUMBING.has(it.scope)) continue;
    if (it.scope && !seen.has(it.scope)) {
      seen.add(it.scope);
      (VISIBLE_AREAS.has(it.scope) ? visible : other).push(it);
    } else rest.push(it);
  }
  return visible.concat(other, rest).slice(0, MAX_HIGHLIGHTS).map(({ scope, title }) => (scope ? { scope, title } : { title }));
}

// ---------- releases ----------

function slimRelease(r) {
  if (!r || typeof r !== "object" || r.draft !== false) return null;
  const tag = r.tag_name;
  if (!parseTag(tag)) return null; // media and other non-product releases (promo-v1 …)
  const published = typeof r.published_at === "string" ? Date.parse(r.published_at) : NaN;
  if (!Number.isFinite(published)) return null;
  const page = `https://github.com/${REPOSITORY}/releases/tag/${encodeURIComponent(tag)}`;
  if (r.html_url !== page) return null;
  const version = tag.slice(1);
  const assets = (Array.isArray(r.assets) ? r.assets : []).map((a) => slimAsset(a, tag, version)).filter(Boolean);
  return {
    tag,
    version,
    prerelease: r.prerelease === true || parseTag(tag).pre !== "",
    published_at: new Date(published).toISOString(),
    url: page,
    ...summarizeNotes(r.body),
    assets,
  };
}

// slimReleases: GitHub's list → the page's data. Newest published first; latest is the highest stable version.
export function slimReleases(list, fetchedAt = new Date()) {
  if (!Array.isArray(list)) throw new Error("GitHub did not return a release list");
  const seen = new Set();
  const releases = [];
  for (const r of list) {
    const s = slimRelease(r);
    if (s && !seen.has(s.tag)) { seen.add(s.tag); releases.push(s); }
  }
  releases.sort((a, b) => Date.parse(b.published_at) - Date.parse(a.published_at) || compareTags(b.tag, a.tag));
  const stable = releases.filter((r) => !r.prerelease).sort((a, b) => compareTags(b.tag, a.tag));
  return {
    repository: REPOSITORY,
    fetched_at: fetchedAt.toISOString(),
    stale: false,
    latest: stable.length ? stable[0].tag : null,
    releases,
  };
}

// ---------- serving ----------

function edgeCache() {
  return typeof caches !== "undefined" && caches.default ? caches.default : null;
}

async function cached(cache) {
  if (!cache) return null;
  try {
    const hit = await cache.match(CACHE_KEY);
    return hit ? await hit.json() : null;
  } catch {
    return null;
  }
}

async function upstream() {
  const res = await fetch(RELEASES_API, {
    headers: { accept: "application/vnd.github+json", "x-github-api-version": "2022-11-28", "user-agent": "persona-site" },
    signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
  });
  if (!res.ok) throw new Error(`GitHub answered ${res.status}`);
  return slimReleases(await res.json());
}

async function load(ctx) {
  const cache = edgeCache();
  const old = await cached(cache);
  if (old && Date.now() - Date.parse(old.fetched_at) < FRESH_MS) return old;
  try {
    const data = await upstream();
    if (cache) {
      const put = cache.put(CACHE_KEY, new Response(JSON.stringify(data), {
        headers: { "content-type": "application/json", "cache-control": `public, max-age=${KEEP_SECONDS}` },
      })).catch(() => {});
      ctx?.waitUntil ? ctx.waitUntil(put) : await put;
    }
    return data;
  } catch {
    if (old && Date.now() - Date.parse(old.fetched_at) < KEEP_SECONDS * 1000) return { ...old, stale: true };
    return null;
  }
}

export async function serveReleases(request, env, ctx) {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return new Response("method not allowed", { status: 405, headers: { allow: "GET, HEAD" } });
  }
  const data = await load(ctx);
  if (!data) {
    return Response.json({ error: "release list unavailable", releases_page: `https://github.com/${REPOSITORY}/releases` },
      { status: 503, headers: { "cache-control": "no-store" } });
  }
  const headers = {
    "cache-control": data.stale ? "public, max-age=60" : "public, max-age=300",
    "x-content-type-options": "nosniff",
  };
  if (request.method === "HEAD") return new Response(null, { headers: { ...headers, "content-type": "application/json" } });
  return Response.json(data, { headers });
}
