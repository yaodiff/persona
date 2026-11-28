import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { allowBody, denyBody } from "../../lib/quick-answers";
import type { Interaction } from "../../lib/types";
import { Button } from "../../ui/atoms";

/** Progressive-disclosure text editing and deny notes for server-declared editable permission cards. The original
 * Allow/Deny stay plain everywhere else; only an actual text change sends edited_text, and Always never carries it. */
export function usePermissionText(ix: Interaction, reviewing = false) {
  const original = ix.editable_text ?? "";
  const [editing, setEditing] = useState(false);
  const [denyOpen, setDenyOpen] = useState(false);
  const [text, setText] = useState(original);
  const [note, setNote] = useState("");
  useEffect(() => {
    setText(original);
    setNote("");
    setEditing(false);
    setDenyOpen(false);
  }, [ix.id, original]);

  const changed = text !== original;
  const canAllow = !changed || text.trim() !== "";
  const allow = () => changed ? { allow: true, edited_text: text } : allowBody();
  const deny = () => note.trim() ? { allow: false, message: note.trim() } : denyBody();
  const editor = ix.editable_text === undefined ? null : (
    <fieldset disabled={reviewing} className="flex min-w-0 flex-col gap-2" data-permission-text>
      <div className="flex flex-wrap gap-2">
        <Button tone="quiet" onClick={() => setEditing((v) => !v)}>
          {t(editing ? "chat-interactions.hide_edit" : "chat-interactions.edit_text")}
        </Button>
        <Button tone="quiet" onClick={() => setDenyOpen((v) => !v)}>
          {t(denyOpen ? "chat-interactions.hide_deny_note" : "chat-interactions.deny_note")}
        </Button>
      </div>
      {editing && (
        <label className="flex flex-col gap-1 text-xs text-ink-3">
          {t("chat-interactions.edit_text_label")}
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={4}
            aria-label={t("chat-interactions.edit_text_label")}
            className="min-h-24 resize-y rounded-control bg-field px-2.5 py-2 text-sm leading-relaxed text-ink outline-none placeholder:text-ink-3 focus:ring-2 focus:ring-accent"
          />
        </label>
      )}
      {denyOpen && (
        <label className="flex flex-col gap-1 text-xs text-ink-3">
          {t("chat-interactions.deny_note_label")}
          <input
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder={t("chat-interactions.deny_note_placeholder")}
            aria-label={t("chat-interactions.deny_note_label")}
            className="h-8 rounded-control bg-field px-2.5 text-sm text-ink outline-none placeholder:text-ink-3 focus:ring-2 focus:ring-accent"
          />
        </label>
      )}
      {changed && <p className="text-xs text-ink-3">{t("chat-interactions.edited_pending")}</p>}
    </fieldset>
  );
  return { editor, allow, deny, changed, canAllow, composing: editing || denyOpen };
}
