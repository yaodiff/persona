// Exact persisted keys, not a prefix rewrite: user keys and message content are never migrated.
const STORAGE_ALIASES: Record<string, string> = Object.assign(Object.create(null), {
  "persona.token": "jarvis.token",
  "persona.tokenId": "jarvis.tokenId",
  "persona.theme": "jarvis.theme",
  "persona.agents.prefs": "jarvis.agents.prefs",
  "persona.agentGrouping": "jarvis.agentGrouping",
  "persona.agentFilter": "jarvis.agentFilter",
  "persona.files.view": "jarvis.files.view",
  "draft:persona-new": "draft:jarvis-new",
});
const REVERSE_STORAGE_ALIASES: Record<string, string> = Object.assign(
  Object.create(null),
  Object.fromEntries(Object.entries(STORAGE_ALIASES).map(([canonical, legacy]) => [legacy, canonical])),
);

/** The other key an aliased storage pair is mirrored to, or null for keys without a legacy alias. */
export function storedKeyAlias(key: string): string | null {
  return STORAGE_ALIASES[key] ?? REVERSE_STORAGE_ALIASES[key] ?? null;
}

/** The current name of an aliased pair: a legacy key resolves to its canonical key, anything else maps to itself. */
export function canonicalStorageKey(key: string): string {
  return REVERSE_STORAGE_ALIASES[key] ?? key;
}

/** The browser's token and the id of its device token at the hub. "persona.tokenId" is where this browser keeps that id,
 *  to revoke it on signing out and to have it replaced when it signs in again. */
export function storedCredentials(store: Storage = localStorage): { token: string; tokenId: string } {
  const token = store.getItem("persona.token");
  const id = store.getItem("persona.tokenId");
  const oldToken = store.getItem(STORAGE_ALIASES["persona.token"]);
  const oldId = store.getItem(STORAGE_ALIASES["persona.tokenId"]);
  // An orphan canonical id is not evidence it belongs to the legacy token.
  if (token === null && id === null && oldToken !== null) {
    try {
      store.setItem("persona.token", oldToken);
      if (oldToken && oldId !== null) store.setItem("persona.tokenId", oldId);
    } catch {
      // A full or blocked store cannot stop using the legacy pair that was already read.
    }
    return { token: oldToken, tokenId: oldToken ? oldId ?? "" : "" };
  }
  if (token && token === oldToken && id === null && oldId !== null) {
    try {
      store.setItem("persona.tokenId", oldId);
    } catch {
      // Keep the legacy device id paired with this same token.
    }
    return { token, tokenId: oldId };
  }
  return { token: token ?? "", tokenId: token ? id ?? "" : "" };
}

export function storedValue(key: string, store: Storage = localStorage): string | null {
  if (key === "persona.token" || key === "persona.tokenId") {
    const credentials = storedCredentials(store);
    const current = store.getItem(key);
    const old = store.getItem(STORAGE_ALIASES[key]);
    const restored = key === "persona.token" ? credentials.token : credentials.tokenId;
    // A failed migration write leaves no canonical value; keep the legacy value only when the credential reader
    // actually accepted it, never for an orphan canonical id or a credential belonging to another token.
    return current ?? (old !== null && restored === old ? old : current);
  }
  const current = store.getItem(key);
  if (current !== null || !STORAGE_ALIASES[key]) return current;
  const old = store.getItem(STORAGE_ALIASES[key]);
  if (old !== null) {
    try {
      store.setItem(key, old);
    } catch {
      // storage is full or blocked: keep serving the legacy value and never throw into the caller's render
    }
  }
  return old;
}

export function storeValue(key: string, value: string, store: Storage = localStorage) {
  store.setItem(key, value);
  if (STORAGE_ALIASES[key]) store.setItem(STORAGE_ALIASES[key], value);
}

export function removeStoredValue(key: string, store: Storage = localStorage) {
  store.removeItem(key);
  if (STORAGE_ALIASES[key]) store.removeItem(STORAGE_ALIASES[key]);
}

/** Credentials are written as one logical pair; changing the token cannot inherit an unrelated token id. */
export function storeCredentials(token: string, tokenId?: string, store?: Storage) {
  if (!token) {
    let s: Storage | null = store ?? null;
    if (!s) {
      try {
        s = (typeof localStorage === "undefined" ? null : localStorage) ?? null;
      } catch {
        return;
      }
    }
    if (!s) return;
    for (const key of ["persona.token", STORAGE_ALIASES["persona.token"], "persona.tokenId", STORAGE_ALIASES["persona.tokenId"]]) {
      try {
        s.removeItem(key);
      } catch {
        // isolate each key removal so a single blocked key cannot leave a legacy token behind to resurrect on reload
      }
    }
    return;
  }
  const s = store ?? localStorage;
  const previous = storedCredentials(s);
  const knownId = tokenId ?? (previous.token === token ? s.getItem("persona.tokenId") : null);
  if (knownId === null) removeStoredValue("persona.tokenId", s);
  storeValue("persona.token", token, s);
  if (knownId !== null) storeValue("persona.tokenId", knownId, s);
}

// Cross-version identities are transport contracts, never product copy.
// Baseline: 39a706a338; the naming-only migration did not bump the protocol major.
export const LEGACY_SCHEME = "jarvis";
export const APP_SCHEMES = new Set(["persona", LEGACY_SCHEME]);
export const isPersonaStateDir = (name: string): boolean => name === ".persona" || name === ".jarvis";
export const canonicalAppURL = (url: string): string => url.replace(/^jarvis:\/\//i, "persona://");
export const isAppLink = (url: string): boolean => APP_SCHEMES.has(/^([a-z]+):\/\//i.exec(url)?.[1]?.toLowerCase() ?? "");
export const PERSONA_SERVERS = new Set(["persona", "jarvis"]);
export const CLIENT_HEADERS = ["X-Persona-Client", "X-Jarvis-Client"] as const;
export const OP_HEADERS = ["X-Persona-Op-Id", "X-Jarvis-Op-Id"] as const;
export const CSRF_HEADERS = ["X-Persona-CSRF", "X-Jarvis-CSRF"] as const;
export const CONTEXT_RE = /<(?:persona|jarvis)-context\b([^>]*)>([\s\S]*?)<\/(?:persona|jarvis)-context>/g;
export const LEAD_CONTEXT_RE = /^\s*<(?:persona|jarvis)-context\b[^>]*>[\s\S]*?<\/(?:persona|jarvis)-context>/;
export const CONTEXT_CLOSE_RE = /<\s*\/\s*(?:persona|jarvis)-context\s*>/gi;
export const FILE_RE = /<(?:persona|jarvis)-file\b([^>]*?)\/?>(?:\s*<\/(?:persona|jarvis)-file>)?/g;
/** A source whose legacy context or file marker could be rewritten by an older hub. */
export const LEGACY_CONTEXT_OR_FILE_RE = /<\s*\/?\s*(?:persona|jarvis)-(?:context|file)\b/i;
const TOOL_ALIASES: Record<string, string> = { ask_jarvis: "ask_persona", jarvis_settings: "persona_settings" };
export const canonicalTool = (name: string): string => TOOL_ALIASES[name] ?? name;
export const managedProvider = (source?: string): boolean => source === "persona" || source === "jarvis";
/** A launcher's explicit model ID; older nodes sent Chinese display text when they meant no override. */
export const explicitLauncherModel = (model?: string): string | undefined => model && model !== "默认" ? model : undefined; // i18n-ignore: legacy wire value, never displayed
/** Legacy hubs wrote the Codex terminal fallback notice into Supervision.Progress and report feed items (#14822). */
const LEGACY_CODEX_ROUTE_NOTICES = new Set([
  "Codex will start in the terminal to use the requested reasoning effort. Approvals and questions will use the terminal screen.",
  "为使用指定的推理强度，Codex 将通过终端启动。审批和提问将通过终端画面处理。", // i18n-ignore: legacy hub notice literal, never displayed
  "指定された推論レベルを使うため、Codex はターミナルで起動します。承認や質問はターミナル画面を通じて行います。", // i18n-ignore: legacy hub notice literal, never displayed
]);
export const isLegacyCodexRouteNotice = (text?: string): boolean => Boolean(text && LEGACY_CODEX_ROUTE_NOTICES.has(text.trim()));
export const legacyBuiltins = ["jarvis", "jarvis-circuit", "jarvis-pixel", "jarvis-facet", "jarvis-blob"];

/** Apply only to typed session objects; tool input and message text stay opaque. */
export function compatibleSession<T extends { started_by_persona?: boolean }>(s: T): T {
  if (!s || typeof s !== "object") return s;
  const old = (s as T & { started_by_jarvis?: unknown }).started_by_jarvis;
  return s.started_by_persona === undefined && typeof old === "boolean" ? { ...s, started_by_persona: old } : s;
}

/** Normalize REST/session-event envelopes, not arbitrary keys within messages or tool arguments. */
export function compatibleEnvelope<T>(value: T): T {
  if (!value || typeof value !== "object" || Array.isArray(value)) return value;
  const v = value as Record<string, unknown>;
  if (Array.isArray(v.sessions)) return { ...v, sessions: v.sessions.map(s => compatibleSession(s)) } as T;
  if (Array.isArray(v.upsert)) return { ...v, upsert: v.upsert.map(s => compatibleSession(s)) } as T;
  if (Array.isArray(v.looks)) return { ...v, looks: v.looks.map(l => l.source === "builtin" ? { ...l, id: canonicalBuiltin(l.id), app_icon: canonicalBuiltin(l.app_icon) } : l), current: compatibleEnvelope(v.current) } as T;
  if (typeof v.effective === "string") return { ...v, id: typeof v.id === "string" ? canonicalBuiltin(v.id) : v.id, effective: canonicalBuiltin(v.effective) } as T;
  if (v.session && typeof v.session === "object") return { ...v, session: compatibleSession(v.session) } as T;
  return value;
}

/** Old-default wire spelling, until the target explicitly advertises the new identifiers. */
export function messageForHub(text: string, features: readonly string[] = hubFeatures): string {
  if (features.includes("persona_contracts_v1")) return text;
  return text.replace(/<\s*\/?\s*(?:persona|jarvis)-(?:context|file)\b[^>]*>/gi, tag =>
    tag.replace(/(^<\s*\/?\s*)persona-(context|file)/i, "$1jarvis-$2")
      .replace(/(\burl=["'])persona:\/\//g, "$1jarvis://"));
}

/** Only message submission endpoints contain generated attachment/context markup. */
export function requestForHub(path: string, json: unknown, features: readonly string[] = hubFeatures): unknown {
  if (path === "/looks/current" && json && typeof json === "object" && "id" in json && typeof json.id === "string") return { ...json, id: builtinForHub(json.id, features) };
  if (!json || typeof json !== "object" || Array.isArray(json) || !/(?:\/messages|\/queue|\/fork|\/rewind|\/sessions|\/assistant\/(?:send|conversations))\/?$/.test(path)) return json;
  const body = { ...json } as Record<string, unknown>;
  for (const field of ["text", "prompt"]) if (typeof body[field] === "string") body[field] = messageForHub(body[field], features);
  return body;
}

let hubFeatures: readonly string[] = [];
export function setCompatibilityFeatures(features: readonly string[]) { hubFeatures = features; }
export const assistantPath = (segment: string): boolean => segment === "persona" || segment === "j";
export const focusSource = (source: unknown): "client" | "persona" | "other" => source === "client" ? "client" : PERSONA_SERVERS.has(String(source)) ? "persona" : "other";

const BUILTIN_ALIASES: Record<string, string> = { jarvis: "persona-visor", "jarvis-circuit": "persona-circuit", "jarvis-pixel": "persona-pixel", "jarvis-facet": "persona-facet", "jarvis-blob": "persona-blob" };
export const canonicalBuiltin = (id: string): string => BUILTIN_ALIASES[id] ?? id;
export const builtinForHub = (id: string, features: readonly string[] = hubFeatures): string => features.includes("persona_contracts_v1") ? id : Object.entries(BUILTIN_ALIASES).find(([, value]) => value === id)?.[0] ?? id;
