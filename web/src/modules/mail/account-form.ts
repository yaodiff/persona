import { t } from "../../i18n";
import type { MailAccount } from "../../lib/agent-settings";

// ── Connectors › Mail › Add ──

export type MailKind = "gmail" | "outlook" | "feishu" | "imap";

/** The kinds, in the phone's order (iOS MailAccountForm.Kind). Gmail and Outlook sign in with Google / Microsoft. */
export const MAIL_KINDS: { k: MailKind; label: string }[] = [
  { k: "gmail", label: "Gmail" },
  { k: "outlook", label: "Outlook" },
  { k: "feishu", label: t("lib-agent-settings.mail_kind_feishu") },
  { k: "imap", label: t("lib-agent-settings.mail_kind_imap") },
];

export interface MailForm {
  kind: MailKind;
  email: string;
  password: string;
  imapHost: string;
  imapPort: string;
  smtpHost: string;
  smtpPort: string;
}

export const EMPTY_MAIL_FORM: MailForm = { kind: "imap", email: "", password: "", imapHost: "", imapPort: "993", smtpHost: "", smtpPort: "465" };

/** POST /api/v1/mail/accounts's body (docs/protocol/protocol.md "Mail API"). */
export interface AddMailAccount {
  name: string;
  email: string;
  provider: "gmail" | "feishu" | "imap";
  auth: "password";
  password: string;
  imap_host?: string;
  imap_port?: number;
  smtp_host?: string;
  smtp_port?: number;
  smtp_security?: "tls" | "starttls";
}

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

const port = (p: string) => {
  const n = Number(p.trim());
  return Number.isInteger(n) && n > 0 && n < 65536 ? n : undefined;
};

/** Gmail and Outlook sign in on a provider's page that calls back into the phone app (a custom scheme the browser can't
 *  catch), so they are added on the phone; the web only sets Outlook's client ID up. */
export function signsInOnPhone(k: MailKind): k is "gmail" | "outlook" {
  return k === "gmail" || k === "outlook";
}

/** What is wrong with the form, field by field (empty = can be sent). Outlook is never sent from here, nor is Gmail
 *  unless the hub has no Google sign-in (`gmailAppPassword`: then it goes in with an app password). */
export function mailErrors(f: MailForm, gmailAppPassword = false): Partial<Record<keyof MailForm, string>> {
  const e: Partial<Record<keyof MailForm, string>> = {};
  if (f.kind === "gmail" && !gmailAppPassword) return { kind: t("lib-agent-settings.mail_gmail_phone_only") };
  if (f.kind === "outlook") return { kind: t("lib-agent-settings.mail_outlook_phone_only") };
  if (!f.email.trim()) e.email = t("lib-agent-settings.mail_email_empty");
  else if (!EMAIL.test(f.email.trim())) e.email = t("lib-agent-settings.mail_email_invalid");
  if (!f.password)
    e.password = t(
      f.kind === "feishu" ? "lib-agent-settings.mail_password_feishu" : f.kind === "gmail" ? "lib-agent-settings.mail_password_gmail" : "lib-agent-settings.mail_password_empty",
    );
  if (f.kind === "imap") {
    if (f.imapHost.trim() && !port(f.imapPort)) e.imapPort = t("lib-agent-settings.mail_port_invalid");
    if (f.smtpHost.trim() && !port(f.smtpPort)) e.smtpPort = t("lib-agent-settings.mail_port_invalid");
  }
  return e;
}

/** The request for a password account; null for Outlook, and for Gmail on a hub that signs in with Google (both on the
 * phone). Servers left empty are guessed by the hub from the domain; Lark's are fixed by the hub; Gmail's are spelled
 * out, since a Google Workspace address has a domain no preset knows. SMTP on 587 is STARTTLS, anything else TLS. */
export function mailRequest(f: MailForm, gmailAppPassword = false): AddMailAccount | null {
  if (f.kind === "outlook" || (f.kind === "gmail" && !gmailAppPassword)) return null;
  const email = f.email.trim();
  const req: AddMailAccount = { name: email, email, provider: f.kind, auth: "password", password: f.password };
  if (f.kind === "gmail") return { ...req, imap_host: "imap.gmail.com", imap_port: 993, smtp_host: "smtp.gmail.com", smtp_port: 465, smtp_security: "tls" };
  if (f.kind === "imap") {
    if (f.imapHost.trim()) {
      req.imap_host = f.imapHost.trim();
      req.imap_port = port(f.imapPort);
    }
    if (f.smtpHost.trim()) {
      req.smtp_host = f.smtpHost.trim();
      req.smtp_port = port(f.smtpPort);
      req.smtp_security = req.smtp_port === 587 ? "starttls" : "tls";
    }
  }
  return req;
}

/**
 * Where a QQ / NetEase address typed under Other IMAP makes its authorization code (the provider and its web mail), null for any
 * other. The hub says the same steps for the same domains (server/internal/modules/mail/preset.go, Guide; iOS CodeGuide):
 * change them together.
 */
export function mailCodeGuide(f: Pick<MailForm, "kind" | "email" | "imapHost" | "smtpHost">): { provider: "qq" | "netease"; webMail: string } | null {
  // Servers typed: they may not be the provider's.
  if (f.kind !== "imap" || f.imapHost.trim() || f.smtpHost.trim()) return null;
  const at = f.email.lastIndexOf("@");
  if (at < 0) return null;
  const domain = f.email.slice(at + 1).trim().toLowerCase();
  switch (domain) {
    case "qq.com":
    case "foxmail.com":
    case "vip.qq.com":
      return { provider: "qq", webMail: "https://mail.qq.com" };
    case "163.com":
    case "126.com":
    case "yeah.net":
      return { provider: "netease", webMail: `https://mail.${domain}` };
    case "vip.163.com":
    case "vip.126.com":
      return { provider: "netease", webMail: `https://${domain}` };
    case "188.com":
      return { provider: "netease", webMail: "https://www.188.com" };
    default:
      return null;
  }
}

/** The line under an account's address: its error, how it signs in, or its server. */
export function mailAccountLine(a: MailAccount): string {
  if (a.status === "error") return a.error || t("lib-agent-settings.mail_unreachable");
  if (a.auth === "oauth2") return a.provider === "outlook" ? t("lib-agent-settings.mail_line_outlook") : t("lib-agent-settings.mail_line_gmail");
  if (a.provider === "feishu") return t("lib-agent-settings.mail_line_feishu");
  return a.imap_host ? `IMAP · ${a.imap_host}` : "IMAP";
}

/** The badge after an account's address: its provider's name for the ones signed in with a provider (iOS MailProviderMark). */
export function mailProviderBadge(a: MailAccount): string | null {
  if (a.provider === "gmail") return "Gmail";
  if (a.provider === "outlook") return "Outlook";
  if (a.provider === "feishu") return t("lib-agent-settings.mail_kind_feishu");
  return null;
}

/** GET / PUT /api/v1/mail/oauth/microsoft/config (docs/extensions/mail.md "Outlook: Sign In with Microsoft"): whether Outlook sign-in can start,
 *  with which client ID (not a secret) from where (app | config), and the redirect URI to register in Microsoft Entra. */
export interface MicrosoftConfig {
  configured: boolean;
  client_id?: string;
  source?: string;
  redirect_uri: string;
}

const CLIENT_ID = /^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$/;

/** What is wrong with a typed client ID ("" = fine), the same rule as the hub's. */
export function clientIDError(id: string): string {
  const v = id.trim();
  if (!v) return t("lib-agent-settings.mail_client_id_empty");
  return CLIENT_ID.test(v) ? "" : t("lib-agent-settings.mail_client_id_invalid");
}
