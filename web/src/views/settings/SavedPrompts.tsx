// Settings › Saved Prompts (#7329, docs/web/web.md "Saved prompts"): the user's own "/" prompts, the current assistant's,
// in the order every chat's "/" list shows them. Add, change (SavedPromptDialog), move up or down, delete (confirmed in
// the row, RowConfirm). The list is lib/saved-prompts.ts's, the one the composers read, so a change shows in an open chat
// at once and on another device through the hub's assistant_saved_prompts.
import { useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { deleteSavedPrompt, moveSavedPrompt, reloadSavedPrompts, SAVED_PROMPTS_MAX, useSavedPrompts, type SavedPrompt } from "../../lib/saved-prompts";
import { Button, IconButton, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { SavedPromptDialog } from "./SavedPromptDialog";
import { Group, RowConfirm, RowFailed, SettingsHint } from "./SettingsParts";

const text = (key: string, vars?: Record<string, string | number>) => t(`settings-savedprompts.${key}`, vars);

/** What the editor shows: a new prompt, or the one being changed; `n` remounts it, so each opening starts afresh, and
 *  it stays while it closes (its exit plays). */
type Editing = { prompt: SavedPrompt | null; n: number; open: boolean };

export function SavedPrompts() {
  const list = useSavedPrompts();
  const [editing, setEditing] = useState<Editing | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const ready = list.status === "ready";
  const full = list.prompts.length >= SAVED_PROMPTS_MAX;
  const edit = (prompt: SavedPrompt | null) => setEditing((e) => ({ prompt, n: (e?.n ?? 0) + 1, open: true }));
  const act = async (key: string, run: () => Promise<void>) => {
    if (busy) return;
    setBusy(key);
    try {
      await run();
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy(null);
    }
  };

  const add = (
    <Button tone="secondary" icon="plus" disabled={!ready || full} onClick={() => edit(null)} testId="saved-prompt-add">
      {text("add")}
    </Button>
  );
  return (
    <>
      <Group
        anchor="saved-prompts"
        title={text("title")}
        right={ready && list.prompts.length > 0 ? add : undefined}
        footer={ready && list.prompts.length > 0 ? text("footer", { n: list.prompts.length, max: SAVED_PROMPTS_MAX }) : undefined}
      >
        {list.status === "loading" && <div className="px-4 py-3"><Loading /></div>}
        {list.status === "failed" && <RowFailed text={list.error} retry={text("retry")} onRetry={() => void reloadSavedPrompts()} />}
        {list.status === "unsupported" && <SettingsHint>{text("unsupported")}</SettingsHint>}
        {ready && list.error && <SettingsHint alert>{list.error}</SettingsHint>}
        {ready && list.prompts.length === 0 && (
          <div className="flex flex-col items-center gap-2 px-6 py-8 text-center" data-testid="saved-prompts-empty">
            <span className="flex size-9 items-center justify-center rounded-card bg-inset text-ink-2 shadow-hairline">
              <Icon name="book" size={18} />
            </span>
            <p className="text-meta font-medium text-ink">{text("empty_title")}</p>
            <p className="max-w-[360px] text-label leading-relaxed text-ink-3">{text("empty_body")}</p>
            <div className="mt-1">{add}</div>
          </div>
        )}
        {ready &&
          list.prompts.map((p, i) => (
            <div key={p.id} className="flex items-start gap-3 px-4 py-2.5" data-saved-prompt={p.name}>
              <div className="min-w-0 flex-1">
                <p className="truncate font-mono text-meta text-ink">/{p.name}</p>
                <p className="mt-0.5 line-clamp-2 whitespace-pre-line break-words text-submeta leading-snug text-ink-3">{p.text}</p>
              </div>
              <div className="flex shrink-0 items-center gap-0.5 self-center">
                <RowConfirm
                  open={deleting === p.id}
                  confirm={text("delete")}
                  busy={busy === `delete:${p.id}`}
                  onCancel={() => setDeleting(null)}
                  onConfirm={() =>
                    void act(`delete:${p.id}`, async () => {
                      await deleteSavedPrompt(p.id);
                      setDeleting(null);
                      toast(text("deleted", { name: p.name }), "info");
                    })
                  }
                  trigger={
                    <>
                      <IconButton icon="arrowUp" label={text("move_up", { name: p.name })} disabled={i === 0 || !!busy} onClick={() => void act(`move:${p.id}`, () => moveSavedPrompt(p.id, -1))} />
                      <IconButton icon="arrowDown" label={text("move_down", { name: p.name })} disabled={i === list.prompts.length - 1 || !!busy} onClick={() => void act(`move:${p.id}`, () => moveSavedPrompt(p.id, 1))} />
                      <Button tone="quiet" disabled={!!busy} onClick={() => edit(p)}>
                        {text("edit")}<span className="sr-only"> /{p.name}</span>
                      </Button>
                      <Button tone="quiet" disabled={!!busy} onClick={() => setDeleting(p.id)}>
                        {text("delete")}<span className="sr-only"> /{p.name}</span>
                      </Button>
                    </>
                  }
                />
              </div>
            </div>
          ))}
      </Group>
      {editing && <SavedPromptDialog key={editing.n} open={editing.open} editing={editing.prompt} onClose={() => setEditing((e) => e && { ...e, open: false })} />}
    </>
  );
}
