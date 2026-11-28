// The words of a mail the hub kept with its Inbox item (#15995): what a hub with mail off can still show.
//
// The hub reads a new mail once for the assistant and keeps that reading as the item's `context`
// (GET /api/v1/inbox/{id}; server/internal/assistant/inbox_mail.go): a heading and the mail's header lines written
// for the model, then up to 3,000 characters of the newest mail's text (readMailBody), or, when the body was never
// read, one short excerpt per mail between `---` lines (mailContext). The page shows the mail's own words, so the
// lines for the model come off here. A context in neither shape is shown as it is: never less than the hub kept.

// The hub writes these lines in Chinese whatever the user's language (they are text for a model).
const HEADING = "邮箱 [^\\n]*收到的[^\\n]*：\\n"; // i18n-ignore: the hub's line for a model
const FROM = "发件人：[^\\n]*\\n"; // i18n-ignore: the hub's line for a model
const TO = "收件人：[^\\n]*\\n"; // i18n-ignore: the hub's line for a model
const CC = "抄送：[^\\n]*\\n"; // i18n-ignore: the hub's line for a model
const DATE = "时间：[^\\n]*\\n"; // i18n-ignore: the hub's line for a model
const SUBJECT = "主题：[^\\n]*\\n"; // i18n-ignore: the hub's line for a model

/** The newest mail read in full: heading, headers, a blank line, the text. */
const READ = new RegExp(`(?:^|\\n)${HEADING}${FROM}${TO}${CC}${DATE}${SUBJECT}\\n`, "g");
/** Mail never read in full: heading, then `---` and the headers before each excerpt. */
const LISTED = new RegExp(`(?:^|\\n)${HEADING}(?=---\\n)`, "g");
const LISTED_HEADERS = new RegExp(`^${FROM}${TO}${DATE}${SUBJECT}`);

function lastMatch(re: RegExp, s: string): { start: number; end: number } | null {
  let last: { start: number; end: number } | null = null;
  re.lastIndex = 0;
  for (let m = re.exec(s); m; m = re.exec(s)) last = { start: m.index, end: m.index + m[0].length };
  return last;
}

/** The mail's own words out of the item's stored `context`, else the list's preview. A merged item's context holds
 *  its members' before its own: the last mail in it is the item's newest. */
export function storedMailText(context: string | undefined, preview: string | undefined): string {
  const kept = context?.trim() ?? "";
  const read = lastMatch(READ, kept);
  const listed = lastMatch(LISTED, kept);
  if (read && (!listed || read.start > listed.start)) {
    const text = kept.slice(read.end).trim();
    if (text) return text;
  } else if (listed) {
    const excerpts = kept
      .slice(listed.end)
      .split(/(?:^|\n)---\n/)
      .map((block) => block.replace(LISTED_HEADERS, "").trim())
      .filter(Boolean);
    if (excerpts.length) return excerpts.join("\n\n");
  }
  return kept || (preview?.trim() ?? "");
}

export interface MailAddress {
  name?: string;
  email?: string;
}

export function addressText(a?: MailAddress): string {
  return a ? (a.name && a.email ? `${a.name} <${a.email}>` : a.email || a.name || "") : "";
}

/** Whether the text the hub stored for a mail is all of the newest mail (#15995), from the item's own page:
 *  - `clipped`: the hub cut it at the 3,000 characters it stores;
 *  - `whole`: the stored text is the whole text;
 *  - `unknown`: the hub did not say: a hub from before it sent `mail_body_truncated`, an item stored before the hub
 *    recorded it, a mail whose text the hub never read, or the list's own copy of the item (which never carries it). */
export type StoredMailClipping = "clipped" | "whole" | "unknown";

/** `mail_body_truncated` of GET /inbox/{id} as the closed type; anything but a boolean is `unknown`, never `whole`. */
export function storedMailClipping(item?: { mail_body_truncated?: unknown }): StoredMailClipping {
  const v = item?.mail_body_truncated;
  return v === true ? "clipped" : v === false ? "whole" : "unknown";
}
