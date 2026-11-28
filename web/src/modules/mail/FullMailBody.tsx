import { useEffect, useMemo, useRef, useState } from "react";
import { api, enc, errorText, isAbort } from "../../lib/api";
import { bytes, clock, safeWebURL } from "../../lib/format";
import { isDone, itemKey, withToken, type InboxItem } from "../../lib/inbox";
import { defaultManager, itemHeaders, useItemManager, withManager } from "../../lib/manager-choice";
import { openLink } from "../../lib/links";
import { useMailAccounts } from "../../lib/mail";
import { addressText } from "../../lib/mail-stored";
import { markMailSeen, replyAllDraft, replyDraft, type ComposeDraft, type MailMessage } from "./mail";
import { hubTranslation, isForeignCached, languageName, strippedHTML, translateMail, translatesMail, useTranslatePrefs, type MailTranslation } from "../../lib/translate";
import { t } from "../../i18n";
import { Button, cx, Loading, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ComposeDialog } from "./ComposeDialog";
import { Glyph } from "../../views/inbox/InboxParts";

export function FullMailBody({ item, onLatest }: { item: InboxItem; onLatest?: () => void }) {
  const m = item.mail!;
  const uid = m.uids[m.uids.length - 1];
  const mailbox = m.mailbox || "INBOX";
  const [msg, setMsg] = useState<MailMessage | null>(null);
  const [error, setError] = useState("");
  // "Reply" open: its template as it opened, so the dialog stays over a new mail of the thread read under it (#4563)
  const [replying, setReplying] = useState<{ item: string; mode: "reply" | "reply-all"; initial: ComposeDraft } | null>(null);
  const accounts = useMailAccounts();
  const prefs = useTranslatePrefs();
  const [translation, setTranslation] = useState<MailTranslation | null>(null);
  const [translating, setTranslating] = useState(false);
  const [translateError, setTranslateError] = useState("");
  const [showOriginal, setShowOriginal] = useState(false);
  const path = `/mail/messages/${enc(m.account_id)}/${uid}`;
  const unread = !isDone(item) && item.read !== true;
  const manager = useItemManager();

  useEffect(() => {
    let live = true;
    const abort = new AbortController();
    setMsg(null);
    setError("");
    api<{ message: MailMessage }>(`${path}?mailbox=${enc(mailbox)}`, { headers: itemHeaders(manager), signal: abort.signal })
      .then((r) => {
        if (!live) return;
        setMsg(r.message);
        if (unread && r.message?.seen !== true) void markMailSeen(m.account_id, uid, mailbox, manager);
      })
      .catch((e) => { if (live && !isAbort(e)) setError(errorText(e)); });
    return () => {
      live = false;
      abort.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, mailbox, manager]);

  // Settings › Translation: a mail in another language than the target is asked of the hub once it is here.
  const wants = translatesMail(prefs);
  useEffect(() => {
    setTranslation(null);
    setTranslateError("");
    setShowOriginal(false);
    if (!msg || !wants) return;
    const body = msg.text?.trim() ? msg.text : strippedHTML(msg.html ?? "");
    if (!isForeignCached(`${msg.subject ?? ""}\n${body}`, prefs.target)) return;
    let live = true;
    setTranslating(true);
    translateMail(m.account_id, uid, mailbox, prefs.target, manager)
      .then((tr) => live && setTranslation(tr))
      .catch((e) => live && setTranslateError(errorText(e)))
      .finally(() => live && setTranslating(false));
    return () => {
      live = false;
      setTranslating(false);
    };
  }, [msg, wants, prefs.target, m.account_id, uid, mailbox, manager]);

  // kept by the item (the thread): a new mail of it moves `uid` on and reads the body again under the dialog
  // another item opened in this body: that reply's dialog goes (and keeps what was typed for its own item)
  const replyItem = itemKey({ id: item.id, manager: item.manager || defaultManager() });
  const replyKey = replying && `${replying.mode}:${replying.item}`;
  const dialog = replying?.item === replyItem && <ComposeDialog key={replyKey} draftKey={replyKey!} initial={replying.initial} accounts={accounts} onClose={() => setReplying((current) => current === replying ? null : current)} />;
  if (!msg) return <div className="p-5">{error ? <p className="text-[12.5px] text-red">{error}</p> : <Loading label={t("inbox-inboxbodies.reading_mail")} />}{dialog}</div>;
  const shown = showOriginal ? null : translation;
  const lang = languageName(prefs.target);
  const allDraft = replyAllDraft(m.account_id, msg, accounts.find((a) => a.id === m.account_id)?.email);
  return (
    <div className="mx-auto max-w-[760px] px-4 py-4 sm:px-6">
      {onLatest && (
        <div className="mb-3 flex justify-center">
          <Button tone="quiet" icon="chevronDown" onClick={onLatest}>{t("inbox-inboxbodies.latest")}</Button>
        </div>
      )}
      <div className="flex flex-col items-start gap-3 sm:flex-row">
        <dl className="grid min-w-0 flex-1 grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[12.5px]">
          <dt className="text-ink-3">{t("inbox-inboxbodies.from")}</dt>
          <dd className="min-w-0 truncate text-ink">{addressText(msg.from)}</dd>
          {!!msg.to?.length && (<><dt className="text-ink-3">{t("inbox-inboxbodies.to")}</dt><dd className="min-w-0 truncate text-ink-2">{msg.to.map(addressText).join(t("inbox-inboxbodies.address_sep"))}</dd></>)}
          {!!msg.cc?.length && (<><dt className="text-ink-3">{t("inbox-inboxbodies.cc")}</dt><dd className="min-w-0 truncate text-ink-2">{msg.cc.map(addressText).join(t("inbox-inboxbodies.address_sep"))}</dd></>)}
          {msg.date && (<><dt className="text-ink-3">{t("inbox-inboxbodies.date")}</dt><dd className="text-ink-2">{clock(msg.date)}</dd></>)}
        </dl>
        <div className="flex shrink-0 gap-2">
          <Button tone="secondary" onClick={() => setReplying({ item: replyItem, mode: "reply", initial: replyDraft(m.account_id, msg) })} title={t("inbox-inboxbodies.reply_title")}>
            <Glyph name="reply" size={14} stroke={2.1} />
            {t("inbox-inboxbodies.reply")}
          </Button>
          <Button tone="secondary" disabled={!allDraft} onClick={() => allDraft && setReplying({ item: replyItem, mode: "reply-all", initial: allDraft })} title={t(allDraft ? "inbox-inboxbodies.reply_all_title" : "inbox-inboxbodies.reply_all_unavailable")}>
            {t("inbox-inboxbodies.reply_all")}
          </Button>
        </div>
      </div>
      {(translating || translation || translateError) && (
        <div className="mt-3 flex min-h-8 items-center gap-2 rounded-control bg-inset px-3 py-1.5 text-[12px] text-ink-3 shadow-hairline">
          {translating ? (
            <>
              <Spinner />
              <span className="truncate">{t("inbox-inboxbodies.translating", { language: lang })}</span>
            </>
          ) : translation ? (
            <>
              <Icon name="translate" size={13} className="shrink-0" />
              <span className="min-w-0 flex-1 truncate">{showOriginal ? t("inbox-inboxbodies.original") : t("inbox-inboxbodies.translated_to", { language: lang })}</span>
              <button type="button" onClick={() => setShowOriginal((v) => !v)} className="shrink-0 rounded-[6px] px-1.5 py-0.5 font-medium whitespace-nowrap text-accent-ink hover:bg-hover">
                {showOriginal ? t("inbox-inboxbodies.show_translation") : t("inbox-inboxbodies.show_original")}
              </button>
            </>
          ) : (
            <>
              <Icon name="info" size={13} className="shrink-0" />
              <span className="min-w-0 flex-1 truncate" title={translateError}>{t("inbox-inboxbodies.translate_failed", { error: translateError })}</span>
            </>
          )}
        </div>
      )}
      <div className="mt-4">
        {shown ? (
          <div className="rounded-card bg-surface px-[18px] py-4 shadow-card">
            {shown.subject && shown.subject !== (hubTranslation(item, prefs)?.subject || item.title) && <p className="mb-2 text-[14px] font-semibold text-ink">{shown.subject}</p>}
            <div className="text-[13.5px] leading-relaxed break-words whitespace-pre-wrap text-ink">{shown.text}</div>
            {shown.clipped && <p className="mt-3 text-[12px] text-ink-3">{t("inbox-inboxbodies.clipped")}</p>}
          </div>
        ) : msg.html ? <MailFrame html={msg.html} /> : <div className="text-[13.5px] leading-relaxed break-words whitespace-pre-wrap text-ink">{msg.text || t("inbox-inboxbodies.no_body")}</div>}
      </div>
      {!!msg.attachments?.length && (
        <div className="mt-5">
          <p className="mb-1.5 text-[12px] font-medium text-ink-3">{t("inbox-inboxbodies.attachments", { count: msg.attachments.length })}</p>
          <div className="flex flex-wrap gap-2">
            {msg.attachments.map((a, i) => {
              const url = a.id ? withToken(withManager(`/api/v1${path}/attachments/${enc(a.id)}?mailbox=${enc(mailbox)}`, manager)) : undefined;
              const body = (
                <>
                  <Icon name="clip" size={13} className="shrink-0 text-ink-3" />
                  <span className="truncate">{a.filename || t("inbox-inboxbodies.attachment")}</span>
                  {a.size !== undefined && <span className="shrink-0 text-ink-3">{bytes(a.size)}</span>}
                </>
              );
              const cls = "flex max-w-full items-center gap-2 rounded-[8px] bg-surface px-2.5 py-1.5 text-[12.5px] text-ink shadow-card";
              return url ? <a key={i} href={url} download={a.filename} className={cx(cls, "hover:bg-hover")}>{body}</a> : <span key={i} className={cls}>{body}</span>;
            })}
          </div>
        </div>
      )}
      {dialog}
    </div>
  );
}

// Mail HTML is somebody else's page: no scripts, no forms, no active embeds, no meta refresh, and no credentialed
// same-origin requests. Same origin is retained only so the parent page can size the frame to its content and route
// link clicks through openLink.
const FRAME_CSP =
  "default-src 'none'; img-src https: http: data: cid:; style-src 'unsafe-inline'; font-src https: http: data:; base-uri 'none'; form-action 'none'";

const FRAME_HEAD_OPEN =
  '<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer">' +
  `<meta http-equiv="Content-Security-Policy" content="${FRAME_CSP}"><style>` +
  ":root{color-scheme:light}html{background:Canvas;color:CanvasText}" +
  'body{margin:0;padding:16px 18px;font:14px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}' +
  "img{max-width:100%;height:auto}table{max-width:100%}pre{white-space:pre-wrap}</style>";

const MAIL_CTRL = /[\u0000-\u001f\u007f]/;
const SAFE_DATA_IMAGE = /^data:image\/(?:png|jpe?g|gif|webp|avif|bmp|x-icon|vnd\.microsoft\.icon)(?:;[^,]*)?,/i;
const SAFE_CID = /^cid:[^\s<>"]+$/i;
const BANNED_MAIL_TAGS = "script,iframe,frame,frameset,object,embed,applet,base,meta,link,template,animate,animatemotion,animatetransform,set,discard";
const BANNED_MAIL_ATTRS = new Set(["action", "srcdoc", "formaction", "ping", "dynsrc", "lowsrc"]);
const SVG_PAINT_ATTRS = new Set(["cursor", "filter", "mask", "clip-path", "fill", "stroke", "marker", "marker-start", "marker-mid", "marker-end"]);
const CSS_URL_FN_TRIGGER = /(?:@import\b|(?:^|[^a-z0-9_-])(?:url|image|src|(?:-[a-z0-9]+-)?image-set)\s*\()/i;
const CSS_IMAGE_FN_RE = /(^|[^a-z0-9_-])((?:-[a-z0-9]+-)?image-set|image|src)\s*\(/gi;

function normalizeOrigin(origin: string): string {
  if (!origin || origin === "null") return "";
  try {
    const o = new URL(origin).origin;
    return o && o !== "null" ? o : "";
  } catch {
    return "";
  }
}

function pageOrigin(): string {
  return typeof location !== "undefined" ? normalizeOrigin(location.origin) : "";
}

function safeMailtoHref(raw: string): string | undefined {
  const s = raw.trim();
  if (!s || MAIL_CTRL.test(s)) return undefined;
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return undefined;
  }
  if (u.protocol.toLowerCase() !== "mailto:" || u.username || u.password) return undefined;
  const rest = s.slice(7);
  if (!rest || rest.startsWith("//") || !(u.pathname || u.search)) return undefined;
  return s;
}

/** Validates an email link target: credential-free http(s) or strict mailto:. */
export function safeMailHref(raw: string | null | undefined): string | undefined {
  if (!raw) return undefined;
  return safeWebURL(raw) ?? safeMailtoHref(raw);
}

/** Validates an email subresource URL: external http(s) off this hub's origin, safe raster data:, or cid:. */
function safeMailMediaURL(raw: string | null | undefined, ownOrigin = pageOrigin()): string | undefined {
  const s = raw?.trim();
  if (!s || MAIL_CTRL.test(s)) return undefined;
  if (SAFE_CID.test(s) || SAFE_DATA_IMAGE.test(s)) return s;
  const web = safeWebURL(s);
  if (!web) return undefined;
  if (ownOrigin && new URL(web).origin === ownOrigin) return undefined;
  return web;
}

function sanitizeSrcset(raw: string, ownOrigin: string): string {
  const s = raw.trim();
  if (!s) return "";
  // Single data: URI candidate (contains exactly one comma separating header and payload).
  if (/^data:/i.test(s)) {
    const m = /^(\S+)(?:\s+(\d+(?:\.\d+)?[wx]))?$/i.exec(s);
    if (!m || m[1].endsWith(",")) return "";
    const firstComma = m[1].indexOf(",");
    if (firstComma < 0 || m[1].indexOf(",", firstComma + 1) !== -1) return "";
    const safe = safeMailMediaURL(m[1], ownOrigin);
    return safe ? (m[2] ? `${safe} ${m[2]}` : safe) : "";
  }
  const kept: string[] = [];
  for (const part of s.split(",")) {
    const trimmed = part.trim();
    if (!trimmed) continue;
    const tokens = trimmed.split(/\s+/);
    if (tokens.length > 2) continue;
    const safe = safeMailMediaURL(tokens[0], ownOrigin);
    if (!safe) continue;
    if (tokens[1] && !/^\d+(?:\.\d+)?[wx]$/i.test(tokens[1])) continue;
    kept.push(tokens[1] ? `${safe} ${tokens[1]}` : safe);
  }
  return kept.join(", ");
}

function stripCssComments(css: string): string {
  return css.replace(/\r\n|\r|\f/g, "\n").replace(/\/\*[\s\S]*?(?:\*\/|$)/g, "");
}

function decodeCssEscapes(css: string): string {
  return stripCssComments(css)
    .replace(/\\\n/g, "")
    .replace(/\\([0-9a-f]{1,6})[\t\n ]?/gi, (_, hex: string) => {
      const cp = parseInt(hex, 16);
      return cp > 0 && cp <= 0x10ffff ? String.fromCodePoint(cp) : "";
    })
    .replace(/\\([^\n0-9a-f])/gi, "$1");
}

function scrubCssUrlCalls(css: string, validate: (target: string) => string | undefined): string {
  return css.replace(
    /(^|[^a-z0-9_-])url\(\s*(?:"([^"]*)"(?:\s*\)|$)|'([^']*)'(?:\s*\)|$)|([^)]*?)(?:\)|$))/gi,
    (match, prefix: string, dq?: string, sq?: string, bare?: string) => {
      if (!match.endsWith(")")) return `${prefix}none`;
      const target = dq ?? sq ?? bare ?? "";
      const safe = validate(target);
      return safe ? `${prefix}url("${safe.replace(/["\\]/g, encodeURIComponent)}")` : `${prefix}none`;
    },
  );
}

/** Scrubs CSS image-set(), -webkit-image-set(), image(), and src() calls that accept bare string URLs. */
function scrubCssImageFns(css: string, ownOrigin: string): string {
  let out = "";
  let pos = 0;
  CSS_IMAGE_FN_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = CSS_IMAGE_FN_RE.exec(css)) !== null) {
    const fnStart = m.index + m[1].length;
    out += css.slice(pos, fnStart);
    const argsStart = CSS_IMAGE_FN_RE.lastIndex;
    let depth = 1;
    let quote: string | null = null;
    let i = argsStart;
    for (; i < css.length && depth > 0; i++) {
      const ch = css[i];
      if (quote) {
        if (ch === "\\" && i + 1 < css.length) {
          i++;
        } else if (ch === quote) {
          quote = null;
        }
      } else if (ch === '"' || ch === "'") {
        quote = ch;
      } else if (ch === "(") {
        depth++;
      } else if (ch === ")") {
        depth--;
      }
    }
    if (depth !== 0) {
      // Unclosed image-set/image/src at EOF: fail closed.
      out += "none";
      pos = css.length;
      break;
    }
    const args = css.slice(argsStart, i - 1);
    let safeCall = true;
    // Validate every bare string argument outside already-scrubbed url(...) and type(...).
    const sanitizedArgs = args.replace(/(url|type)\(\s*(?:"[^"]*"|'[^']*')\s*\)|"([^"]*)"|'([^']*)'/gi, (tok, fn?: string, dq?: string, sq?: string) => {
      if (fn) return tok;
      const candidate = dq ?? sq ?? "";
      const safe = safeMailMediaURL(candidate, ownOrigin);
      if (!safe) {
        safeCall = false;
        return '""';
      }
      return `"${safe.replace(/["\\]/g, encodeURIComponent)}"`;
    });
    out += safeCall ? `${m[2]}(${sanitizedArgs})` : "none";
    pos = i;
    CSS_IMAGE_FN_RE.lastIndex = i;
  }
  return out + css.slice(pos);
}

function sanitizeCss(raw: string, ownOrigin: string): string {
  let css = stripCssComments(raw);
  const decoded = decodeCssEscapes(css);
  if (css.includes("\\") && CSS_URL_FN_TRIGGER.test(decoded)) {
    css = decoded;
  }
  css = css.replace(/@import\b(?:"[^"]*"|'[^']*'|[^;])*(?:;|$)/gi, "");
  css = scrubCssUrlCalls(css, (target) => safeMailMediaURL(target, ownOrigin));
  css = scrubCssImageFns(css, ownOrigin);
  return css.replace(/<\/style/gi, "<\\/style");
}

function sanitizeSvgPaintAttr(raw: string): string {
  let val = stripCssComments(raw);
  const decoded = decodeCssEscapes(val);
  if (val.includes("\\") && CSS_URL_FN_TRIGGER.test(decoded)) {
    val = decoded;
  }
  val = scrubCssUrlCalls(val, (target) => {
    const t = target.trim();
    return /^#[A-Za-z0-9_.:-]+$/.test(t) ? t : undefined;
  });
  return scrubCssImageFns(val, "").replace(/\b(?:(?:-[a-z0-9]+-)?image-set|image|src)\s*\([^)]*\)?/gi, "none");
}

function escapeAttr(val: string): string {
  return val.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/** Sanitizes untrusted email HTML inside an inert DOMParser document before constructing the iframe srcDoc. */
export function buildMailSrcDoc(html: string, ownOrigin = pageOrigin()): string {
  if (typeof DOMParser === "undefined") {
    return `${FRAME_HEAD_OPEN}</head><body></body></html>`;
  }
  const normalizedOrigin = normalizeOrigin(ownOrigin);
  const doc = new DOMParser().parseFromString(html, "text/html");
  for (const el of doc.querySelectorAll(BANNED_MAIL_TAGS)) el.remove();

  const headStyles: string[] = [];
  for (const styleEl of doc.head.querySelectorAll("style")) {
    const clean = sanitizeCss(styleEl.textContent ?? "", normalizedOrigin).trim();
    if (clean) headStyles.push(`<style>${clean}</style>`);
  }

  const scrubElement = (el: Element) => {
    const tag = el.tagName.toLowerCase();
    if (tag === "style") {
      el.textContent = sanitizeCss(el.textContent ?? "", normalizedOrigin);
    }
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase();
      if (name.startsWith("on") || BANNED_MAIL_ATTRS.has(name)) {
        el.removeAttribute(attr.name);
        continue;
      }
      if (name === "style") {
        const clean = sanitizeCss(attr.value, normalizedOrigin).trim();
        if (clean) el.setAttribute("style", clean);
        else el.removeAttribute("style");
        continue;
      }
      if (SVG_PAINT_ATTRS.has(name)) {
        const clean = sanitizeSvgPaintAttr(attr.value).trim();
        if (clean) el.setAttribute(attr.name, clean);
        else el.removeAttribute(attr.name);
        continue;
      }
      if (name === "src" || name === "poster" || name === "background") {
        const safe = safeMailMediaURL(attr.value, normalizedOrigin);
        if (safe) el.setAttribute(attr.name, safe);
        else el.removeAttribute(attr.name);
        continue;
      }
      if (name === "srcset") {
        const clean = sanitizeSrcset(attr.value, normalizedOrigin);
        if (clean) el.setAttribute(attr.name, clean);
        else el.removeAttribute(attr.name);
        continue;
      }
    }
    if (tag === "a" || tag === "area") {
      const rawHref = el.getAttribute("href") ?? el.getAttribute("xlink:href") ?? "";
      el.removeAttribute("xlink:href");
      const trimmed = rawHref.trim();
      if (trimmed.startsWith("#") && !MAIL_CTRL.test(trimmed)) {
        el.setAttribute("href", trimmed);
        el.removeAttribute("target");
        el.removeAttribute("rel");
      } else {
        const safe = safeMailHref(trimmed);
        if (safe) {
          el.setAttribute("href", safe);
          if (safe.toLowerCase().startsWith("mailto:")) el.removeAttribute("target");
          else el.setAttribute("target", "_blank");
          el.setAttribute("rel", "noopener noreferrer");
        } else {
          el.removeAttribute("href");
          el.removeAttribute("target");
          el.removeAttribute("rel");
        }
      }
    } else if (el.hasAttribute("href") || el.hasAttribute("xlink:href")) {
      const rawHref = (el.getAttribute("href") ?? el.getAttribute("xlink:href") ?? "").trim();
      el.removeAttribute("xlink:href");
      if (tag === "use" && rawHref.startsWith("#") && !MAIL_CTRL.test(rawHref)) {
        el.setAttribute("href", rawHref);
      } else if (tag === "image" || tag === "feimage") {
        const safe = safeMailMediaURL(rawHref, normalizedOrigin);
        if (safe) el.setAttribute("href", safe);
        else el.removeAttribute("href");
      } else {
        el.removeAttribute("href");
      }
    }
  };

  for (const el of doc.body.querySelectorAll("*")) scrubElement(el);
  const bodyStyle = doc.body.getAttribute("style");
  const cleanBodyStyle = bodyStyle ? sanitizeCss(bodyStyle, normalizedOrigin).trim() : "";
  const bodyOpen = cleanBodyStyle ? `<body style="${escapeAttr(cleanBodyStyle)}">` : "<body>";
  return `${FRAME_HEAD_OPEN}${headStyles.join("")}</head>${bodyOpen}${doc.body.innerHTML}</body></html>`;
}

function MailFrame({ html }: { html: string }) {
  const ref = useRef<HTMLIFrameElement>(null);
  const manager = useItemManager();
  const [height, setHeight] = useState(240);
  const srcDoc = useMemo(() => buildMailSrcDoc(html), [html]);
  // undoes what the last load hooked onto the frame's document. The observer lives in this window: left connected, it
  // keeps that document and this frame's state alive, one more on each load and on each frame that goes (a new mail of
  // the thread, Show translation, another item).
  const release = useRef<(() => void) | null>(null);
  useEffect(() => () => release.current?.(), []);

  const onLoad = () => {
    release.current?.();
    release.current = null;
    const doc = ref.current?.contentDocument;
    if (!doc?.body) return;
    const fit = () => setHeight(Math.max(80, Math.ceil(doc.body.getBoundingClientRect().height)));
    fit();
    const sizes = new ResizeObserver(fit);
    sizes.observe(doc.body);
    const openAnchor = (e: MouseEvent) => {
      const a = (e.target as Element | null)?.closest?.("a[href], area[href]");
      if (!a) return;
      const rawHref = a.getAttribute("href")?.trim() ?? "";
      if (!rawHref || (rawHref.startsWith("#") && !MAIL_CTRL.test(rawHref))) return;
      e.preventDefault();
      const safe = safeMailHref(rawHref);
      if (safe) openLink(safe, undefined, manager);
    };
    const auxClick = (e: MouseEvent) => {
      if (e.button === 1) openAnchor(e);
    };
    doc.addEventListener("click", openAnchor);
    doc.addEventListener("auxclick", auxClick);
    release.current = () => {
      sizes.disconnect();
      doc.removeEventListener("click", openAnchor);
      doc.removeEventListener("auxclick", auxClick);
    };
  };

  return (
    <iframe
      ref={ref}
      title={t("inbox-inboxbodies.mail_frame")}
      referrerPolicy="no-referrer"
      sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
      srcDoc={srcDoc}
      onLoad={onLoad}
      className="block w-full overflow-hidden rounded-card shadow-card"
      style={{ height, colorScheme: "light" }}
    />
  );
}
