// Language selection for the site's static HTML. English lives at /en/; the legacy URLs stay Chinese.
// Negotiated legacy responses and redirects are private/no-store because an entry cache may not key on
// Accept-Language or the preference cookie. The distinct /en/ pages remain ordinarily cacheable.
const COOKIE = "persona_lang";
const PAGE_DIRS = new Set(["extensions", "releases", "privacy", "terms"]);

export function acceptLanguage(header) {
  let best = { language: "", quality: 0 };
  let otherLanguage = false;
  for (const entry of String(header || "").split(",")) {
    const [rawTag, ...parameters] = entry.toLowerCase().split(";");
    const tag = rawTag.trim();
    // A basic language range, excluding wildcards and malformed entries.
    if (!/^[a-z]{1,8}(?:-[a-z0-9]{1,8})*$/.test(tag)) continue;
    const language = tag === "en" || tag.startsWith("en-") ? "en"
      : tag === "zh" || tag.startsWith("zh-") ? "zh" : "";
    let quality = 1;
    let valid = true;
    let sawQuality = false;
    for (const parameter of parameters) {
      const match = /^\s*q\s*=\s*([^\s]+)\s*$/.exec(parameter);
      if (!match) continue;
      if (sawQuality) valid = false;
      sawQuality = true;
      if (!/^(?:0(?:\.\d{0,3})?|1(?:\.0{0,3})?)$/.test(match[1])) valid = false;
      else quality = Number(match[1]);
    }
    if (!valid || quality === 0) continue;
    // Any positive supported range outranks an unsupported language. The first supported tag wins a quality tie.
    if (language && quality > best.quality) best = { language, quality };
    if (!language) otherLanguage = true;
  }
  // People who name only another real language get English; absent, wildcard, malformed and all-zero headers stay Chinese.
  return best.language || (otherLanguage ? "en" : "zh");
}

function rememberedLanguage(request) {
  for (const part of (request.headers.get("cookie") || "").split(";")) {
    const equals = part.indexOf("=");
    if (equals < 0) continue;
    const name = part.slice(0, equals).trim();
    const value = part.slice(equals + 1).trim();
    if (name === COOKIE && (value === "en" || value === "zh")) return value;
  }
  return "";
}

function pagePath(pathname) {
  const english = pathname === "/en" || pathname.startsWith("/en/");
  let path = english ? pathname.slice(3) : pathname;
  if (!path) path = "/";
  if (path === "/" || path === "/index" || path === "/index.html") return { page: true, english, zh: "/" };
  if (path === "/404" || path === "/404.html") return { page: true, english, zh: "/404.html" };
  const match = /^\/(extensions|releases|privacy|terms)(?:\/(?:index(?:\.html)?)?)?$/.exec(path);
  if (!match || !PAGE_DIRS.has(match[1])) return { page: false, english, zh: "" };
  return { page: true, english, zh: `/${match[1]}/` };
}

function localizedPath(zh, language) {
  return language === "en" ? (zh === "/" ? "/en/" : "/en" + zh) : zh;
}

function vary(response) {
  const headers = new Headers(response.headers);
  const existing = new Set((headers.get("vary") || "").split(",").map((v) => v.trim().toLowerCase()).filter(Boolean));
  existing.add("accept-language");
  existing.add("cookie");
  headers.set("vary", [...existing].join(", "));
  headers.set("cache-control", "private, no-store");
  return headers;
}

function remember(headers, language) {
  headers.set("set-cookie", `${COOKIE}=${language}; Path=/; Max-Age=31536000; Secure; HttpOnly; SameSite=Lax`);
}

function withoutLanguage(search) {
  return search.replace(/^\?/, "").split("&").filter((component) => {
    const rawKey = component.split("=", 1)[0];
    let key = rawKey;
    try { key = decodeURIComponent(rawKey.replace(/\+/g, " ")); } catch { /* keep malformed keys */ }
    return key !== "lang";
  }).join("&");
}

function redirect(request, path, language, persist = true) {
  const url = new URL(request.url);
  url.pathname = path;
  if (persist) {
    const search = withoutLanguage(url.search);
    url.search = search ? "?" + search : "";
  }
  const headers = vary(new Response(null));
  headers.set("location", url.pathname + url.search);
  if (persist) remember(headers, language);
  return new Response(null, { status: 302, headers });
}

async function asset(request, env, pathname) {
  if (new URL(request.url).pathname === pathname) return env.ASSETS.fetch(request);
  const url = new URL(request.url);
  url.pathname = pathname;
  return env.ASSETS.fetch(new Request(url, request));
}

async function errorAsset(request, env, pathname) {
  const url = new URL(request.url);
  url.pathname = pathname;
  const headers = new Headers(request.headers);
  // A validator or range for the missing URL is not a validator or range for the shared error page.
  for (const name of ["range", "if-range", "if-match", "if-none-match", "if-modified-since", "if-unmodified-since"]) {
    headers.delete(name);
  }
  return env.ASSETS.fetch(new Request(url, { method: request.method, headers }));
}

async function responseWith(response, init = {}) {
  return new Response(response.body, {
    status: init.status || response.status,
    statusText: init.status ? undefined : response.statusText,
    headers: init.headers || response.headers,
  });
}

export async function serveSite(request, env) {
  const url = new URL(request.url);
  const info = pagePath(url.pathname);
  if ((request.method === "GET" || request.method === "HEAD") && info.page) {
    const explicit = url.searchParams.get("lang");
    if (explicit === "en" || explicit === "zh") {
      return redirect(request, localizedPath(info.zh, explicit), explicit);
    }
    if (info.english) {
      // A shared /en/ URL is explicit about its content, but does not change the visitor's saved preference.
      return asset(request, env, url.pathname);
    }
    const language = rememberedLanguage(request) || acceptLanguage(request.headers.get("accept-language"));
    if (language === "en") return redirect(request, localizedPath(info.zh, "en"), "en", false);
    const response = await asset(request, env, url.pathname);
    return responseWith(response, { headers: vary(response) });
  }

  const response = await env.ASSETS.fetch(request);
  if (response.status !== 404 || (request.method !== "GET" && request.method !== "HEAD")) return response;

  // Cloudflare's 404-page handling is Chinese by default. Replace only that body, keeping the missing URL and status.
  const language = info.english ? "en" : rememberedLanguage(request) || acceptLanguage(request.headers.get("accept-language"));
  const notFound = await errorAsset(request, env, localizedPath("/404.html", language));
  if (notFound.status !== 200) return responseWith(response, { headers: vary(response) });
  const headers = vary(notFound);
  return responseWith(notFound, { status: 404, headers });
}
