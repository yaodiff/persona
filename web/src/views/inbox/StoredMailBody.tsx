// A mail on a hub with mail off (#15995): the text the hub kept with the Inbox item, and one line saying why the
// whole message is not here. No mail route is asked: they are gone with the module. The item's own page
// (GET /api/v1/inbox/{id}) and the translation the hub kept (POST /api/v1/translate/mail answers from it) are Core's.
import { hubServes, moduleFeature } from "../../core/module-host";
import { useEffect, useState } from "react";
import { api, isAbort } from "../../lib/api";
import { clock } from "../../lib/format";
import { itemPath, itemRequest, type InboxItem } from "../../lib/inbox";
import { addressText, storedMailClipping, type StoredMailClipping, storedMailText } from "../../lib/mail-stored";
import { hubTranslation, languageName, translateMail, useTranslatePrefs, type MailTranslation } from "../../lib/translate";
import { t } from "../../i18n";
import { Button, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

/** What is drawn where the mail would be: nothing yet, or the hub's stored words. */
type StoredBody = { kind: "loading" } | { kind: "stored"; text: string; clipping: StoredMailClipping };

/** The line after the text: why the rest is not here, and what the hub said about the text being cut short. */
function notice(clipping: StoredMailClipping): string {
  if (hubServes(moduleFeature("mail"))) {
    switch (clipping) {
      case "clipped": return t("inbox-inboxbodies.saved_clipped");
      case "whole": return t("inbox-inboxbodies.saved_whole");
      case "unknown": return t("inbox-inboxbodies.saved");
    }
  }
  switch (clipping) {
    case "clipped": return t("inbox-inboxbodies.mail_off_stored_clipped");
    case "whole": return t("inbox-inboxbodies.mail_off_stored_whole");
    case "unknown": return t("inbox-inboxbodies.mail_off_stored");
  }
}

/** `onLatest`: an older message from the handling chain was asked for; the hub keeps only the newest mail's text. */
export function StoredMailBody({ item, onLatest }: { item: InboxItem; onLatest?: () => void }) {
  const m = item.mail!;
  const uid = m.uids[m.uids.length - 1];
  const mailbox = m.mailbox || "INBOX";
  const manager = item.manager;
  const prefs = useTranslatePrefs();
  const [body, setBody] = useState<StoredBody>({ kind: "loading" });
  const [translation, setTranslation] = useState<MailTranslation | null>(null);
  const [showOriginal, setShowOriginal] = useState(false);

  useEffect(() => {
    let live = true;
    const abort = new AbortController();
    setBody({ kind: "loading" });
    api<InboxItem>(itemPath(item.id), { headers: itemRequest(item), signal: abort.signal })
      .then((r) => live && setBody({ kind: "stored", text: storedMailText(r.context, r.preview || item.preview), clipping: storedMailClipping(r) }))
      // the item could not be read again: the list's own words of it are stored text too, and nothing says how much
      .catch((e) => { if (live && !isAbort(e)) setBody({ kind: "stored", text: storedMailText(item.context, item.preview), clipping: "unknown" }); });
    return () => {
      live = false;
      abort.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [item.id, manager, item.version]);

  // Only a translation the hub already kept (the item lists it): a new one would need the mail server.
  const kept = Boolean(hubTranslation(item, prefs));
  useEffect(() => {
    setTranslation(null);
    setShowOriginal(false);
    if (!kept) return;
    let live = true;
    translateMail(m.account_id, uid, mailbox, prefs.target, manager)
      .then((tr) => live && setTranslation(tr))
      .catch(() => {}); // it was made by another model since, or is gone: the stored text stays
    return () => {
      live = false;
    };
  }, [kept, prefs.target, m.account_id, uid, mailbox, manager]);

  if (body.kind === "loading") return <div className="p-5"><Loading label={t("inbox-inboxbodies.reading_mail")} /></div>;
  const shown = showOriginal ? null : translation;
  const from = addressText(m.from) || item.sender || "";
  return (
    <div className="mx-auto max-w-[760px] px-4 py-4 sm:px-6" data-mail-body="stored">
      {onLatest && (
        <div className="mb-3 flex justify-center">
          <Button tone="quiet" icon="chevronDown" onClick={onLatest}>{t("inbox-inboxbodies.latest")}</Button>
        </div>
      )}
      <dl className="grid min-w-0 grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-label">
        {from && (<><dt className="text-ink-3">{t("inbox-inboxbodies.from")}</dt><dd className="min-w-0 truncate text-ink">{from}</dd></>)}
        {item.updated_at && (<><dt className="text-ink-3">{t("inbox-inboxbodies.date")}</dt><dd className="text-ink-2">{clock(item.updated_at)}</dd></>)}
      </dl>
      {translation && (
        <div className="mt-3 flex min-h-8 items-center gap-2 rounded-control bg-inset px-3 py-1.5 text-label text-ink-3 shadow-hairline">
          <Icon name="translate" size={13} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate">{showOriginal ? t("inbox-inboxbodies.original") : t("inbox-inboxbodies.translated_to", { language: languageName(prefs.target) })}</span>
          <button type="button" onClick={() => setShowOriginal((v) => !v)} className="shrink-0 rounded-chip px-1.5 py-0.5 font-medium whitespace-nowrap text-accent-ink hover:bg-hover">
            {showOriginal ? t("inbox-inboxbodies.show_translation") : t("inbox-inboxbodies.show_original")}
          </button>
        </div>
      )}
      <div className="mt-4 text-meta leading-relaxed break-words whitespace-pre-wrap text-ink select-text" data-mail-stored-text>
        {(shown ? shown.text : body.text) || t("inbox-inboxbodies.no_body")}
      </div>
      <p className="mt-4 flex items-start gap-2 rounded-control bg-inset px-3 py-2 text-label leading-relaxed text-ink-2 shadow-hairline" role="note" data-mail-off-notice data-mail-stored-clipping={body.clipping}>
        <Icon name="info" size={13} className="mt-0.5 shrink-0 text-ink-3" />
        <span className="min-w-0">{notice(body.clipping)}</span>
      </p>
    </div>
  );
}
