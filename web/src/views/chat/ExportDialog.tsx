// Export Conversation… (#7375, docs/web/web.md "Exporting a conversation"): the chat's ⋯ opens it over the page. It
// reads the whole conversation from the hub (lib/chat-export.ts readConversation), whatever the page has loaded, says how
// far it is, and saves the Markdown as a file or copies it. "Include work steps" reads it again unfolded. A chunk of its
// own: nothing of it is on the first screen.
import { useEffect, useId, useState } from "react";
import { createRoot } from "react-dom/client";
import { t } from "../../i18n";
import { agentName, sessionAgent } from "../../lib/agent-kind";
import { api, enc, errorText, sessionPath } from "../../lib/api";
import { exportFileName, ExportStopped, localTime, readConversation, transcriptMarkdown, type ExportWords } from "../../lib/chat-export";
import { bytes } from "../../lib/format";
import type { Item, ItemsPage, Session } from "../../lib/types";
import { Button, copyText, Dialog, Spinner, toast } from "../../ui/atoms";
import { usePersona } from "../assistant/Persona";

const text = (key: string, vars?: Record<string, string | number>) => t(`chat-export.${key}`, vars);

/** The document's words in this page's language; `persona`: the assistant's name, for the messages it typed in. */
export function exportWords(persona: string): ExportWords {
  return {
    you: text("you"),
    persona,
    from: (name) => text("from", { name }),
    computer: text("computer"),
    folder: text("folder"),
    agent: text("agent"),
    exported: text("exported"),
    thinking: text("thinking"),
    input: text("input"),
    result: text("result"),
    error: text("error"),
    image: text("image"),
    cut: (n) => text("cut", { n }),
    partial: (from) => text("partial", { from }),
  };
}

/** How many messages the document holds: yours and the agent's replies with words in them. */
export function messageCount(items: readonly Item[]): number {
  return items.filter((i) => i.kind === "text" && (i.role === "user" || i.role === "assistant") && i.text?.trim()).length;
}

type Reading = { state: "reading"; count: number } | { state: "ready"; markdown: string; messages: number; partial: string } | { state: "failed"; error: string };

export interface ExportTarget {
  session: Session;
  title: string;
  machineName: string;
  /** A Persona conversation: its replies are the assistant's, by its name. */
  assistant: boolean;
}

export function ExportDialog({ open, target, onClose }: { open: boolean; target: ExportTarget; onClose: () => void }) {
  const persona = usePersona();
  const { session, title, machineName, assistant } = target;
  const [work, setWork] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [reading, setReading] = useState<Reading>({ state: "reading", count: 0 });
  const workId = useId();
  const who = sessionAgent(session);
  const agent = assistant ? persona.name : agentName(who.agent, who.label);

  useEffect(() => {
    if (!open) return;
    let stopped = false;
    setReading({ state: "reading", count: 0 });
    const path = sessionPath(session.machine_id, session.session_id);
    readConversation({
      work,
      page: (query) => api<ItemsPage>(`${path}/items?${query}`),
      full: (id) => api<Item>(`${path}/items/${enc(id)}/full`),
      progress: (count) => !stopped && setReading({ state: "reading", count }),
      stopped: () => stopped,
    }).then(
      ({ items, partial }) => {
        if (stopped) return;
        const markdown = transcriptMarkdown(
          items,
          { title, agent, computer: machineName, folder: session.cwd, exported: localTime(new Date().toISOString()) },
          { work, partial, words: exportWords(persona.name) },
        );
        const first = items.find((i) => i.ts)?.ts;
        setReading({ state: "ready", markdown, messages: messageCount(items), partial: partial && first ? localTime(first) : "" });
      },
      (e: unknown) => {
        if (stopped || (e instanceof ExportStopped && e.why === "cancelled")) return;
        setReading({ state: "failed", error: e instanceof ExportStopped ? text("changed") : errorText(e) });
      },
    );
    return () => {
      stopped = true;
    };
  }, [open, work, attempt, session.machine_id, session.session_id, session.cwd, title, agent, machineName, persona.name]);

  const ready = reading.state === "ready" ? reading : null;
  const download = () => {
    if (!ready) return;
    const name = exportFileName(title, new Date());
    const url = URL.createObjectURL(new Blob([ready.markdown], { type: "text/markdown;charset=utf-8" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.rel = "noopener";
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
    toast(text("downloaded", { name }), "ok");
    onClose();
  };
  const copy = async () => {
    if (ready && (await copyText(ready.markdown, { done: text("copied") }))) onClose();
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={480}
      title={text("title")}
      footer={
        <>
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button disabled={!ready} onClick={() => void copy()} testId="export-copy">{text("copy")}</Button>
          <Button tone="primary" icon="download" disabled={!ready} onClick={download} testId="export-download">{text("download")}</Button>
        </>
      }
    >
      <div className="space-y-3" data-export-dialog>
        <p className="text-meta leading-relaxed text-ink-2">{text("intro")}</p>
        <label htmlFor={workId} className="flex cursor-pointer items-start gap-2.5 rounded-control bg-field px-3 py-2.5">
          <input
            id={workId}
            type="checkbox"
            checked={work}
            onChange={(e) => setWork(e.target.checked)}
            className="mt-0.5 size-3.5 shrink-0 accent-[var(--accent)]"
            data-testid="export-work"
          />
          <span className="min-w-0">
            <span className="block text-meta text-ink">{text("work")}</span>
            <span className="mt-0.5 block text-label leading-snug text-ink-3">{text("work_hint")}</span>
          </span>
        </label>
        <div role="status" aria-live="polite" className="min-h-5 text-label" data-testid="export-status">
          {reading.state === "reading" && (
            <span className="flex items-center gap-2 text-ink-2">
              <Spinner size={12} />
              {text("reading", { n: reading.count })}
            </span>
          )}
          {ready && (
            <span className="block text-ink-2">
              {text("ready", { n: ready.messages, size: bytes(new Blob([ready.markdown]).size) })}
              {ready.partial && <span className="mt-1 block text-orange">{text("partial_note", { from: ready.partial })}</span>}
            </span>
          )}
          {reading.state === "failed" && (
            <span className="flex items-center gap-3">
              <span role="alert" className="min-w-0 flex-1 text-red">{reading.error}</span>
              <Button tone="secondary" icon="retry" onClick={() => setAttempt((n) => n + 1)}>{text("retry")}</Button>
            </span>
          )}
        </div>
      </div>
    </Dialog>
  );
}

/** The chat's ⋯ › Export Conversation…: the dialog over the page, gone once it closed. */
export function openExportDialog(target: ExportTarget) {
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
    return <ExportDialog open={open} target={target} onClose={() => setOpen(false)} />;
  }
  root.render(<Host />);
}
