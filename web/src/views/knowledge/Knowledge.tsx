// Knowledge base (docs/assistant/knowledge.md): what Persona knows from the hub's ~/.persona/data — projects first, then chats, people,
// docs, meetings and events, most important and most relevant first. Not a browser of Feishu: every row is Persona'
// own digest, and the way out is handing it to Persona. The iOS app is the reference (KnowledgeView.swift).
//
// Wide enough (the page, not the window: a side panel takes room) it is a list beside the open entry; narrower, the
// entry replaces the list, which stays mounted underneath so its scroll, search and folds survive the round trip.
import { useEffect, useRef, useState } from "react";
import { kindOf, type KBEntry, type KBKind } from "../../lib/kb";
import { navigate } from "../../lib/router";
import { ONE_PANE, useMedia } from "../../lib/viewport";
import { cx } from "../../ui/atoms";
import { remember, searches } from "./cache";
import { EmptyDetail, EntryDetail } from "./KBDetail";
import { ListPane } from "./KBList";
import { KnowledgeMap } from "./KnowledgeMap";

export function Knowledge({ kind, id, onToggleSidebar }: { kind?: string; id?: string; onToggleSidebar: () => void }) {
  const detailOpen = Boolean(kind && id);
  // The list keeps its kind while an entry of another kind is open (a related person from a project).
  const [listKind, setListKind] = useState(() => kindOf(kind));
  useEffect(() => {
    if (!id) setListKind(kindOf(kind));
  }, [kind, id]);
  const [reloadKey, setReloadKey] = useState(0);
  // The first entry of the list, drawn beside it while the address names none. Not a selection: the address stays put
  // and on a narrow screen the list keeps the whole page, as To-dos does.
  const [first, setFirst] = useState<{ listKind: KBKind; entry: KBEntry }>();
  // The search an entry was opened from: its page starts with the places found.
  const [from, setFrom] = useState<{ key: string; q: string } | null>(null);
  // The list and detail take turns on a phone; the list's first entry stands in only when both fit.
  const split = !useMedia(ONE_PANE);
  // Something was opened from this page, so "Back" is the browser's back (to the list or the entry before).
  const opened = useRef(false);

  const open = (e: KBEntry, q?: string) => {
    remember([e]);
    setFrom(q ? { key: `${e.kind}/${e.id}`, q } : null);
    opened.current = true;
    navigate({ name: "kb", kind: e.kind, id: e.id });
  };
  const back = () => {
    if (opened.current) history.back();
    else navigate({ name: "kb", kind: listKind }, true);
  };
  const saved = () => {
    searches.clear();
    setReloadKey((k) => k + 1);
  };

  // /kb/map: the knowledge map (topics and projects as a mind map), a page of its own.
  if (kind === "map") return <KnowledgeMap onToggleSidebar={onToggleSidebar} />;

  const fallback = first?.listKind === listKind && kindOf(first.entry.kind) === listKind ? first.entry : undefined;
  const shown = detailOpen ? { kind: kind!, id: id! } : split ? fallback : undefined;
  const shownKey = shown ? `${shown.kind}/${shown.id}` : "";

  return (
    <div className="@container flex h-full min-w-0">
      <ListPane
        kind={listKind}
        selected={detailOpen ? { kind: kind!, id: id! } : undefined}
        detailOpen={detailOpen}
        reloadKey={reloadKey}
        onToggleSidebar={onToggleSidebar}
        onOpen={open}
        onFirst={setFirst}
      />
      <section className={cx("min-h-0 min-w-0 flex-1 flex-col", detailOpen ? "flex" : "hidden @3xl:flex")}>
        {shown ? (
          <EntryDetail
            key={shownKey}
            kind={shown.kind}
            id={shown.id}
            query={detailOpen && from?.key === shownKey ? from.q : undefined}
            reloadKey={reloadKey}
            onToggleSidebar={onToggleSidebar}
            onBack={back}
            onOpen={(e) => open(e)}
            onSaved={saved}
          />
        ) : (
          <EmptyDetail />
        )}
      </section>
    </div>
  );
}
