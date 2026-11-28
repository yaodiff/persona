// An HTML file shown as a page (docs/web/web.md "HTML preview", #1413): in a sandboxed frame with scripts and the network
// off; each can be turned on for this file while it is open. lib/html-preview.ts has the policy.
import { useEffect, useMemo, useState } from "react";
import { fileDownloadURL, READ_TIMEOUT_MS } from "../../../lib/api";
import { inlineSiblings } from "../../../lib/html-inline";
import { previewDocument, relativeRefs, sandboxFor } from "../../../lib/html-preview";
import { cx, IconButton, Loading } from "../../../ui/atoms";
import { t } from "../../../i18n";

export function HtmlPreview({ html, title, mid, path }: { html: string; title: string; mid: string; path: string }) {
  const [scripts, setScripts] = useState(false);
  const [network, setNetwork] = useState(false);
  const [reloads, setReloads] = useState(0);
  const policy = { scripts, network };
  // Changing the sandbox policy requires a new browsing context before any scripts can run.
  const frameKey = `${reloads}:${scripts}:${network}`;
  const local = useMemo(() => relativeRefs(html), [html]);
  // files beside the page, read from the machine and put into it: the frame has no address to load them from
  const [inlined, setInlined] = useState<{ html: string; loaded: number; missing: number } | null>(null);
  useEffect(() => {
    let live = true;
    setInlined(null);
    if (!local) {
      setInlined({ html, loaded: 0, missing: 0 });
      return;
    }
    // Closing the file or reloading aborts the reads still pending; each read also gives up after READ_TIMEOUT_MS like
    // every other hub read, and a read that fails counts as a missing file, so a stalled machine cannot hold the page.
    const closed = new AbortController();
    void inlineSiblings(html, path, async (p) => {
      // a read the inliner starts after the abort (the next @import of a sheet) never begins: the listener would not fire
      if (closed.signal.aborted) return null;
      const one = new AbortController();
      const abort = () => one.abort();
      closed.signal.addEventListener("abort", abort, { once: true });
      const timer = setTimeout(abort, READ_TIMEOUT_MS);
      try {
        const res = await fetch(fileDownloadURL(mid, p, ""), { signal: one.signal });
        return res.ok ? await res.blob() : null;
      } finally {
        clearTimeout(timer);
        closed.signal.removeEventListener("abort", abort);
      }
    })
      .catch(() => ({ html, loaded: 0, missing: local }))
      .then((r) => live && setInlined(r));
    return () => {
      live = false;
      closed.abort();
    };
  }, [html, path, mid, local, reloads]);
  const srcDoc = useMemo(() => (inlined ? previewDocument(inlined.html, { scripts, network }) : ""), [inlined, scripts, network]);
  const state = scripts && network ? t("session-files-htmlpreview.state_both") : scripts ? t("session-files-htmlpreview.state_scripts") : network ? t("session-files-htmlpreview.state_network") : t("session-files-htmlpreview.state_none");
  const chip = (on: boolean) => cx("h-6 shrink-0 rounded-[6px] px-2 text-[12px] font-medium whitespace-nowrap transition-colors", on ? "bg-orange-tint text-orange" : "text-ink-3 hover:bg-hover hover:text-ink");
  return (
    <>
      <div className="flex min-h-9 shrink-0 items-center gap-2 border-b border-line px-3 py-1 text-[11.5px] whitespace-nowrap text-ink-3">
        <span className="min-w-0 flex-1 truncate" title={local ? t("session-files-htmlpreview.local_hint") : undefined}>
          {state}
          {inlined && inlined.loaded > 0 && ` · ${t("session-files-htmlpreview.loaded", { count: inlined.loaded })}`}
          {inlined && inlined.missing > 0 && ` · ${t("session-files-htmlpreview.missing", { count: inlined.missing })}`}
        </span>
        <button type="button" aria-pressed={scripts} onClick={() => setScripts(!scripts)} className={chip(scripts)}>
          {t("session-files-htmlpreview.scripts")}
        </button>
        <button type="button" aria-pressed={network} onClick={() => setNetwork(!network)} className={chip(network)}>
          {t("session-files-htmlpreview.network")}
        </button>
        <IconButton icon="retry" label={t("session-files-htmlpreview.reload")} onClick={() => setReloads((n) => n + 1)} />
      </div>
      {!inlined ? (
        <div className="flex flex-1 items-center justify-center bg-inset">
          <Loading label={t("session-files-htmlpreview.loading_local", { count: local })} />
        </div>
      ) : (
        <iframe
          key={frameKey}
          title={title}
          sandbox={sandboxFor(policy)}
          srcDoc={srcDoc}
          className="min-h-0 w-full flex-1 border-0 bg-white"
        />
      )}
    </>
  );
}
