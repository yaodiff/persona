// The editor of one saved prompt (#7329, docs/web/web.md "Saved prompts"): its name, the word after "/" in every chat,
// and the text picking it puts in the message box. Settings › Saved Prompts opens it to add or change one; a message's
// "Save as Prompt…" opens it filled with that message's words (openSavedPromptDialog, which mounts it on its own, as the
// chat has no place for it). The hub checks everything again; the checks here only say it before asking.
import { useEffect, useId, useState } from "react";
import { createRoot } from "react-dom/client";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import {
  SAVED_PROMPT_NAME_MAX,
  SAVED_PROMPT_TEXT_MAX,
  SAVED_PROMPTS_MAX,
  saveSavedPrompt,
  savedPromptName,
  savedPromptNameProblem,
  savedPromptTextProblem,
  suggestedSavedPromptName,
  useSavedPrompts,
  type SavedPrompt,
} from "../../lib/saved-prompts";
import { Button, cx, Dialog, toast } from "../../ui/atoms";
import { settingsInputClass } from "./SettingsParts";

const text = (key: string, vars?: Record<string, string | number>) => t(`chat-savedprompts.${key}`, vars);
// the settings field's look, its height left to the rows
const areaClass =
  "block w-full resize-y rounded-control bg-field px-2.5 py-2 text-label leading-relaxed text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

/** `editing`: the prompt changed, null for a new one; `from`: the words of the message it is saved from (its name is
 *  suggested from them until the user types one). */
export function SavedPromptDialog({ open, editing, from, onClose }: {
  open: boolean;
  editing: SavedPrompt | null;
  from?: string;
  onClose: () => void;
}) {
  const list = useSavedPrompts();
  const [typedName, setTypedName] = useState<string | null>(editing ? editing.name : null);
  const [body, setBody] = useState(editing?.text ?? from?.trim() ?? "");
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const ids = { name: useId(), nameNote: useId(), text: useId(), textNote: useId() };
  const name = typedName ?? (from ? suggestedSavedPromptName(from, list.prompts) : "");
  const nameProblem = savedPromptNameProblem(name, list.prompts, editing?.id ?? null);
  const textProblem = savedPromptTextProblem(body);
  const full = !editing && list.status === "ready" && list.prompts.length >= SAVED_PROMPTS_MAX;
  const unsupported = list.status === "unsupported";
  // a rule shows once there is something to check or Save was pressed; a name another prompt has, at once
  const showName = nameProblem && (tried || savedPromptName(name) !== "");
  const showText = textProblem && (tried || body.trim() !== "");
  const length = [...body.trim()].length;

  const save = async () => {
    setTried(true);
    if (nameProblem || textProblem || busy || full || unsupported) return;
    setBusy(true);
    setError("");
    try {
      const p = await saveSavedPrompt(editing?.id ?? null, { name, text: body });
      toast(text(editing ? "changed" : "saved", { name: p.name }), "ok");
      onClose();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      width={520}
      title={text(editing ? "title_edit" : from ? "title_from_message" : "title_new")}
      footer={
        <>
          <Button disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" busy={busy} disabled={busy || full || unsupported} onClick={() => void save()} testId="saved-prompt-save">
            {text("save")}
          </Button>
        </>
      }
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
        onKeyDown={(e) => {
          // ⌘/Ctrl-Return saves from the text too, where Return makes a new line
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            void save();
          }
        }}
      >
        {unsupported && <p role="alert" className="text-label leading-relaxed text-orange">{t("settings-savedprompts.unsupported")}</p>}
        {full && <p role="alert" className="text-label leading-relaxed text-orange">{text("full", { max: SAVED_PROMPTS_MAX })}</p>}
        <div>
          <label htmlFor={ids.name} className="block text-xs text-ink-2">{text("name")}</label>
          <div className="relative mt-1">
            <span aria-hidden className="pointer-events-none absolute inset-y-0 left-2.5 flex items-center text-label text-ink-3">/</span>
            <input
              id={ids.name}
              className={cx(settingsInputClass, "pl-5")}
              value={name}
              maxLength={SAVED_PROMPT_NAME_MAX + 1}
              placeholder={text("name_placeholder")}
              disabled={busy}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              autoFocus={!from}
              aria-invalid={showName ? true : undefined}
              aria-describedby={ids.nameNote}
              data-testid="saved-prompt-name"
              onChange={(e) => {
                setTypedName(e.target.value);
                setError("");
              }}
            />
          </div>
          <p id={ids.nameNote} className={cx("mt-1 text-xs leading-snug", showName ? "text-red" : "text-ink-3")}>
            {showName ? nameProblem : savedPromptName(name) ? text("name_hint", { name: savedPromptName(name) }) : text("name_rule", { max: SAVED_PROMPT_NAME_MAX })}
          </p>
        </div>
        <div>
          <label htmlFor={ids.text} className="block text-xs text-ink-2">{text("text")}</label>
          <textarea
            id={ids.text}
            className={cx(areaClass, "mt-1 min-h-32")}
            rows={8}
            value={body}
            placeholder={text("text_placeholder")}
            disabled={busy}
            autoFocus={Boolean(from)}
            aria-invalid={showText ? true : undefined}
            aria-describedby={ids.textNote}
            data-testid="saved-prompt-text"
            onChange={(e) => {
              setBody(e.target.value);
              setError("");
            }}
          />
          <p id={ids.textNote} className={cx("mt-1 flex gap-2 text-xs leading-snug", showText ? "text-red" : "text-ink-3")}>
            <span className="min-w-0 flex-1">{showText ? textProblem : text("text_hint")}</span>
            {length > SAVED_PROMPT_TEXT_MAX * 0.8 && <span className="shrink-0 tabular-nums">{text("count", { n: length, max: SAVED_PROMPT_TEXT_MAX })}</span>}
          </p>
        </div>
        {error && <p role="alert" className="text-xs text-red">{error}</p>}
      </form>
    </Dialog>
  );
}

/** "Save as Prompt…" (a message of yours, MessageActions): the editor over the page, filled with the message's words,
 *  gone once it closed. */
export function openSavedPromptDialog(words: string) {
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  function Host() {
    const [open, setOpen] = useState(true);
    useEffect(() => {
      if (open) return;
      const gone = setTimeout(() => {
        root.unmount();
        host.remove();
      }, 400); // after the dialog's exit
      return () => clearTimeout(gone);
    }, [open]);
    return <SavedPromptDialog open={open} editing={null} from={words} onClose={() => setOpen(false)} />;
  }
  root.render(<Host />);
}
