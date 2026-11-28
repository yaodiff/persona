// The dictionary card for one word (#10977). Loaded the first time a lookup can run, not with the first screen.
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { t } from "../i18n";
import { Button, IconButton, Spinner } from "../ui/atoms";
import { focusables, placeMenu, trapTabIn, useEscapeClose } from "../ui/overlay";
import { lookupKey, lookupRefresh, lookupSetSaved, lookupSnapshot, lookupUnavailable, subscribeLookup, textLookupApi, type LookupEntry } from "./client";
import type { LookupOpen } from "./listen";

const WIDTH = 320;

export default function LookupPopover({ hit, onClose }: { hit: LookupOpen; onClose: () => void }) {
  const label = useId();
  const box = useRef<HTMLDivElement>(null);
  const [attempt, setAttempt] = useState(0);
  const [phase, setPhase] = useState<"loading" | "ready" | "error">("loading");
  const [entry, setEntry] = useState<LookupEntry | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveFailed, setSaveFailed] = useState(false);
  const [place, setPlace] = useState<{ left: number; top: number } | null>(null);
  const close = useRef(onClose);
  useLayoutEffect(() => {
    close.current = onClose;
  });
  useEscapeClose(true, () => close.current());

  useEffect(() => {
    let live = true;
    setPhase("loading");
    setEntry(null);
    setError(null);
    setSaveFailed(false);
    textLookupApi().lookup(hit.word, hit.context).then(
      (next) => {
        if (!live) return;
        setEntry(next);
        const key = lookupKey(next);
        const surface = lookupKey({ word: next.word });
        setSaved(next.saved || lookupSnapshot()?.words.some((word) => {
          const item = lookupKey({ word });
          return item === key || item === surface;
        }) === true);
        setPhase("ready");
      },
      (err: unknown) => {
        if (!live) return;
        setError(err);
        setPhase("error");
        if (lookupUnavailable(err)) lookupRefresh();
      },
    );
    return () => {
      live = false;
    };
  }, [hit.word, hit.context, attempt]);

  useEffect(() => subscribeLookup(() => {
    if (!lookupSnapshot()?.available) close.current();
  }), []);

  useEffect(() => {
    const down = (event: PointerEvent) => {
      if (box.current?.contains(event.target as Node)) return;
      close.current();
    };
    const away = (event: Event) => {
      if (event.target instanceof Node && box.current?.contains(event.target)) return;
      close.current();
    };
    const key = (event: KeyboardEvent) => {
      if (event.key !== "Tab" || !box.current) return;
      const list = focusables(box.current);
      const index = trapTabIn(box.current, list, event.shiftKey);
      if (index === null) return;
      event.preventDefault();
      list[index]?.focus();
    };
    addEventListener("pointerdown", down, true);
    addEventListener("scroll", away, true);
    addEventListener("keydown", key);
    return () => {
      removeEventListener("pointerdown", down, true);
      removeEventListener("scroll", away, true);
      removeEventListener("keydown", key);
    };
  }, []);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const next = placeMenu(hit.rect, el.offsetHeight, { width: WIDTH, align: "left", side: "bottom", screenW: innerWidth, screenH: innerHeight, gap: 6 });
    setPlace({ left: next.left, top: next.top });
  }, [hit, phase, entry, saved]);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const opener = document.activeElement as HTMLElement | null;
    el.focus();
    return () => {
      if (opener && opener.isConnected) opener.focus();
    };
  }, [hit.word]);

  const name = lookupSnapshot()?.name;
  const gone = lookupUnavailable(error);
  const toggle = async () => {
    if (saving || !entry) return;
    const next = !saved;
    setSaved(next);
    setSaving(true);
    setSaveFailed(false);
    const key = lookupKey(entry);
    try {
      if (next) await textLookupApi().save(key, hit.context);
      else await textLookupApi().remove(key);
      lookupSetSaved(key, next);
    } catch (err) {
      setSaved(!next);
      setSaveFailed(true);
      if (lookupUnavailable(err)) lookupRefresh();
    } finally {
      setSaving(false);
    }
  };

  return createPortal(
    <div
      ref={box}
      role="dialog"
      aria-modal="false"
      aria-labelledby={label}
      aria-busy={phase === "loading" || undefined}
      tabIndex={-1}
      data-testid="text-lookup"
      data-no-lookup=""
      className="fixed z-50 w-[min(20rem,calc(100vw-16px))] rounded-xl border border-line bg-surface p-3 text-ink shadow-overlay outline-none motion-reduce:animate-none"
      style={{ left: place?.left ?? hit.rect.left, top: place?.top ?? Math.min(hit.rect.bottom + 6, innerHeight - 16), animation: "pop-in 160ms var(--ease-out-strong, ease-out)" }}
    >
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <p id={label} className="truncate text-card-title font-semibold text-ink">{entry?.word ?? hit.word}</p>
          {entry?.phonetic && <p className="mt-0.5 truncate text-label text-ink-3" lang="en">{entry.phonetic}</p>}
        </div>
        <IconButton icon="x" label={t("text-lookup.close")} size={28} onClick={() => close.current()} />
      </div>
      {phase === "loading" && (
        <p role="status" className="mt-2 flex items-center gap-2 text-meta text-ink-2">
          <Spinner />
          {t("text-lookup.loading")}
        </p>
      )}
      {phase === "error" && (
        <div className="mt-2" role="alert">
          <p className="text-meta text-ink-2">{t(gone ? "text-lookup.unavailable" : "text-lookup.failed")}</p>
          {/* the dictionary is gone: nothing to retry, and the listener is already asking the hub again */}
          {!gone && <Button tone="secondary" className="mt-2" onClick={() => setAttempt((n) => n + 1)}>{t("text-lookup.retry")}</Button>}
        </div>
      )}
      {phase === "ready" && entry && (
        <>
          {entry.senses.length === 0 ? (
            <p className="mt-2 text-meta leading-[1.45] text-ink-2">{t("text-lookup.empty")}</p>
          ) : (
            <ul className="mt-2 flex max-h-60 flex-col gap-1.5 overflow-y-auto">
              {entry.senses.map((sense, index) => (
                <li key={index} className="text-meta leading-[1.45] text-ink">
                  {sense.pos && <span className="mr-1.5 text-ink-3">{sense.pos}</span>}
                  {sense.text}
                </li>
              ))}
            </ul>
          )}
          <div className="mt-3 flex items-center gap-2">
            <Button tone={saved ? "secondary" : "primary"} busy={saving} aria-pressed={saved} onClick={() => void toggle()}>
              {t(saved ? "text-lookup.saved" : "text-lookup.save")}
            </Button>
            {name && <p className="min-w-0 truncate text-label text-ink-3">{t("text-lookup.provider", { name })}</p>}
          </div>
          {saveFailed && <p role="alert" className="mt-2 text-label text-ink-2">{t("text-lookup.save_failed")}</p>}
        </>
      )}
    </div>,
    document.body,
  );
}
