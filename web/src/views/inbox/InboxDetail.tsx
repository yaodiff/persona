// One item opened: what it is and where from, "Done" on the right; the AI's summary, its "Trail" (InboxLine), "AI draft reply" / "Hand to AI"; then
// the thing itself — the chat at its first new message, the mail, the document's rows, the Moments post. An item without
// event fields can instead have its body drawn by the extension whose connector brought it in (inbox_item).
// An event (a hub that sends this phase's fields, lib/inbox-events.ts) opens on its event body instead (InboxEvent.tsx:
// summary, recap, the suggestion, the handling chain); "Original" and the chain's source entries open the thing itself,
// "Handling" goes back, and browser Back follows the same step. A chat item's event sits above its chat and a mail event
// above its message instead. Their original starts collapsed and unmounted so merely opening the event cannot report it
// read; Original expands it, and the chain's source entries expand it before focusing a message (#3262, #5471).
import { useEffect, useState } from "react";
import { ItemManager, useKnownDefault } from "../../lib/manager-choice";
import { hasEvent, itemAtUid } from "../../lib/inbox-events";
import { agentDraft, agentWorking, canDraft, isDone, itemKey, kindLabel, labelIsUrgent, muteItem, muteShortName, muteTitle, openTitle, policyMenuTitle, openURL, setDone, shortTime, sourceLine, useInbox, type InboxItem } from "../../lib/inbox";
import { rowMeta } from "../../lib/inbox-now";
import { openTodoDialog } from "./InboxTodoDialog";
import { openFolderDialog, useFoldersShown } from "./InboxFolders";
import { openPolicyDialog } from "./PolicyDialog";
import { useInboxItemPlace } from "../../lib/extension-slots";
import { PHONE, useMedia } from "../../lib/viewport";
import { backToInboxList } from "../../lib/inbox-nav";
import { useCloseOnBack } from "../../lib/back-close";
import { useInboxView } from "../../lib/inbox-view-memory";
import { hubTranslation, useTranslatePrefs } from "../../lib/translate";
import { t } from "../../i18n";
import { Button, cx, IconButton, Loading, Menu, useMenu, type MenuItem } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ConnectorBody, openSystem } from "./ConnectorThread";
import { DocBody, MailBody, MomentBody } from "./InboxBodies";
import { DraftDialog, HandOffDialog } from "./InboxDialogs";
import { markFromRow } from "./InboxList";
import { AgentWorking, ItemAvatar, Spark, Tag } from "./InboxParts";
import { InboxExtensionBody } from "./InboxExtensionBody";
import { InboxLine } from "./InboxLine";
import { InboxTaskLinks } from "./InboxTaskLinks";
import { EventBody, type SourceAt } from "./InboxEvent";
import { chatOfItem, LarkThread } from "./LarkThread";
import { lazyDialog } from "../LazyOverlay";

const SameMatterDialog = lazyDialog(() => import("./InboxSameMatter").then((m) => m.InboxSameMatter));

export function InboxDetail({ item, loaded }: { item?: InboxItem; loaded: boolean }) {
  // Whether an item's requests name its manager goes by the roster's default (docs/assistant/managers.md "Clients"): the page
  // waits for it, and is drawn anew when it moves, so nothing it asks goes by a default it was not.
  const known = useKnownDefault();
  if (item && !known) {
    return (
      <div className="flex h-full items-center justify-center p-6">
        <Loading />
      </div>
    );
  }
  return <ItemDetail key={`${known}\u0000${item ? itemKey(item) : ""}`} item={item} loaded={loaded} />;
}

function ItemDetail({ item, loaded }: { item?: InboxItem; loaded: boolean }) {
  const [dialog, setDialog] = useState<"" | "draft" | "handoff" | "same">("");
  const [pins, setPins] = useState(false);
  // an event's own page (the chat, the mail) when "Original" or a source entry opened it; null: the event body
  const [source, setSource] = useState<SourceAt | null>(null);
  const view = useInboxView();
  const originalKey = item ? itemKey(item) : "";
  const revision = view.revision;
  const [originalOpen, setOriginalOpen] = useState(() => Boolean(item && view.originalOpen(originalKey)));
  const [originalSeen, setOriginalSeen] = useState({ key: originalKey, revision });
  if (originalSeen.key !== originalKey || originalSeen.revision !== revision) {
    setOriginalSeen({ key: originalKey, revision });
    setOriginalOpen(view.originalOpen(originalKey));
  }
  useEffect(() => {
    if (originalKey) view.setOriginalOpen(originalKey, originalOpen);
  }, [view, originalKey, originalOpen]);
  useEffect(() => {
    setPins(false);
    setSource(null);
  }, [item?.id]);
  const prefs = useTranslatePrefs();
  const allItems = useInbox((s) => s.items);
  const foldersShown = useFoldersShown(allItems);
  // the item's manager's extensions draw it (each manager has its own)
  const place = useInboxItemPlace(item?.source, item?.manager);
  const [ownShown, setOwnShown] = useState(false); // under an extension's page: it fell back to the item's own body
  const muteMenu = useMenu();
  const moreMenu = useMenu();
  // on a phone the title comes first: Pin, Mute, Folder and Same matter fold into one ⋯ menu
  const phone = useMedia(PHONE);
  const event = Boolean(item && (item.now || hasEvent(item)));
  // A chat item's event sits above its chat (#3262), and a mail event above its message (#5471), whether an extension
  // offers a page for the source or not. A chain entry focuses the chat or mail in place instead of switching the page.
  const embed = event && Boolean(item?.lark || item?.mail);
  // Other events replace their body with the original. Give that page its own same-address entry so Back returns to
  // the event; "Handling" closes it and takes the entry back off. Item navigation takes the entry with it instead.
  useCloseOnBack(event && !embed && source !== null, () => setSource(null));
  if (!item) {
    return (
      <div className="flex h-full items-center justify-center p-6">
        {loaded ? <p className="text-[13px] text-ink-3">{t("inbox-inboxdetail.gone")}</p> : <Loading />}
      </div>
    );
  }
  const done = isDone(item);
  const label = kindLabel(item);
  const showEvent = event && (embed || !source);
  // a suggestion takes the AI twin's draft's place (the hub mirrors a reply suggestion into the draft for older apps)
  const draft = item.suggestion ? undefined : agentDraft(item);
  const translated = hubTranslation(item, prefs);
  const time = shortTime(item.updated_at);
  const detailMeta = item.where ? rowMeta(item) : sourceLine(item);
  const canPin = Boolean(item.lark) && (!embed || originalOpen) && (embed || !place || ownShown) && (!showEvent || embed);
  const toggleOriginal = () => {
    setOriginalOpen(!originalOpen);
    if (originalOpen) setSource(null);
  };
  const sourceToggle = event ? (
    <Button tone="quiet" size="sm" className="-my-1 ml-auto shrink-0" testId="inbox-original-toggle" expanded={embed ? originalOpen : undefined}
      icon={embed ? (originalOpen ? "list" : item.mail ? "mail" : "chat") : showEvent ? (item.mail ? "mail" : "chat") : "list"}
      onClick={embed ? toggleOriginal : () => setSource(showEvent ? {} : null)}>
      {embed ? t(originalOpen ? "inbox-inboxdetail.hide_source" : "inbox-inboxdetail.view_source") : t(showEvent ? "inbox-inboxdetail.view_source" : "inbox-inboxdetail.view_event")}
    </Button>
  ) : null;
  const muteRows: MenuItem[] = [
    ...(item.mutes ?? []).map((m): MenuItem => ({ key: `${m.tag}:${m.value}`, label: muteTitle(m), detail: muteShortName(m), icon: "bellOff", onSelect: () => void muteItem(item, m) })),
    // and a rule for each (docs/assistant/inbox.md "Policies"), under a divider
    ...(item.mutes ?? []).map((m, i): MenuItem => ({ key: `policy:${m.tag}:${m.value}`, label: policyMenuTitle(m), detail: muteShortName(m), icon: "target", divider: i === 0, onSelect: () => openPolicyDialog({ item, option: m, source: item.source }) })),
  ];
  // the item's own body: the chat, the mail, the document's rows, the post, a connector's thread
  // (a chain entry opens the chat at its message, the mail at its uid)
  const at = source ?? {};
  const openSource = (at: SourceAt) => {
    if (embed && !originalOpen) setOriginalOpen(true);
    setSource(at);
  };
  const own = () => (
    <div className={cx("min-h-0 flex-1", item.lark || item.connector ? "flex flex-col overflow-hidden" : "overflow-y-auto")}>
      {item.doc ? <DocBody item={item} /> : item.moment ? <MomentBody item={item} /> : item.lark ? <LarkThread chat={at.position ? { ...chatOfItem(item), focus: at.position } : chatOfItem(item)} pinsOpen={pins} onPinsOpen={setPins} /> : item.connector ? <ConnectorBody item={item} /> : item.mail ? <MailBody key={at.uid ?? ""} item={itemAtUid(item, at.uid)} onLatest={at.uid !== undefined && at.uid !== item.mail.uids[item.mail.uids.length - 1] ? () => setSource(null) : undefined} /> : (
        <div className="mx-auto max-w-[760px] px-6 py-5 text-[13.5px] whitespace-pre-wrap text-ink">{item.preview}</div>
      )}
    </div>
  );
  // its chat, mail or connector thread is its manager's (a merged list's stamp, docs/assistant/managers.md "Clients"): every
  // request the bodies below make names it, so one still going out after the next item opened keeps its own
  return (
    <ItemManager.Provider value={item.manager}>
      <div className="flex h-full min-h-0 flex-col" key={item.id}>
        <div className="shrink-0 border-b border-line px-4 pt-4 pb-3 sm:px-6">
          <div className="mx-auto max-w-[760px]">
            <div className="flex items-start gap-3">
              <ItemAvatar it={item} size={36} />
              <div className="min-w-0 flex-1">
                {/* where a narrow page puts the keyboard when the item opens (Inbox.tsx), so its ring shows there */}
                <h2 tabIndex={-1} data-item-title className={cx("flex min-w-0 items-center gap-2 rounded-chip text-[15px] font-semibold text-ink outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent", item.source === "larkcli" && "flex-wrap gap-y-1 sm:flex-nowrap")}>
                  <span className={item.source === "larkcli" ? "min-w-0 max-w-full break-words sm:truncate" : "truncate"} title={translated?.subject ? item.title : undefined}>{translated?.subject || item.title}</span>
                  {label && <Tag urgent={labelIsUrgent(item)}>{label}</Tag>}
                </h2>
                {/* with a place, the small line gives way to Original / Handling (at its end); an older item keeps its
                    time right after where it came from, "Lark · DM · 08:29": the words take only their width, so the
                    time no longer sits at the far end as a stray " · 08:29" (audit item 60), and on a narrow screen the
                    words are cut before it, which stays whole */}
                <p className="mt-0.5 flex min-w-0 items-center text-[12px] text-ink-3" data-detail-meta>
                  <span className="min-w-0 truncate">{detailMeta}</span>
                  {!item.where && time && <span className="shrink-0 whitespace-pre">{detailMeta ? ` · ${time}` : time}</span>}
                  {sourceToggle}
                </p>
              </div>
              {phone ? (
                <IconButton icon="more" label={t("inbox-inboxdetail.more")} onClick={moreMenu.open} active={Boolean(moreMenu.anchor)} size={32} />
              ) : (
                <>
                  {canPin && <IconButton icon="pin" label={t("inbox-inboxdetail.pin_messages")} onClick={() => setPins(true)} size={32} />}
                  {muteRows.length > 0 && <IconButton icon="bellOff" label={t("inbox-inboxdetail.mute")} onClick={muteMenu.open} active={Boolean(muteMenu.anchor)} size={32} />}
                  {foldersShown && <IconButton icon="folder" label={item.folder ? t("inbox-folders.move_from", { folder: item.folder }) : t("inbox-folders.move_menu")} onClick={() => openFolderDialog(item)} size={32} />}
                  <IconButton icon="link" label={t("inbox-inboxline.same")} onClick={() => setDialog("same")} size={32} />
                </>
              )}
              {muteMenu.anchor && <Menu anchor={muteMenu.anchor} onClose={muteMenu.close} align="right" width={224} items={muteRows} />}
              {moreMenu.anchor && (
                <Menu anchor={moreMenu.anchor} onClose={moreMenu.close} align="right" width={224}
                  items={[
                    ...(!done ? [{ key: "todo", label: t("inbox-inboxlist.todo_add_short"), icon: "todo" as const, onSelect: () => openTodoDialog([item]) }] : []),
                    ...(canPin ? [{ key: "pins", label: t("inbox-inboxdetail.pin_messages"), icon: "pin" as const, onSelect: () => setPins(true) }] : []),
                    ...muteRows.map((r, i) => (i === 0 && canPin ? { ...r, divider: true } : r)),
                    ...(foldersShown ? [{ key: "folder", label: t("inbox-folders.move_menu"), hint: item.folder, icon: "folder" as const, divider: true, onSelect: () => openFolderDialog(item) }] : []),
                    { key: "same", label: t("inbox-inboxline.same"), icon: "link" as const, divider: canPin || muteRows.length > 0 || foldersShown, onSelect: () => setDialog("same") },
                  ]} />
              )}
              {done ? (
                <Button tone="secondary" icon="inbox" onClick={() => void setDone(item, false)}>{t("inbox-inboxdetail.move_back")}</Button>
              ) : (
                <Button
                  tone="secondary"
                  icon="check"
                  onClick={() => {
                    // the page leaves the item at once, so say so with "Undo" on any screen (#4162), as a row does on touch
                    void markFromRow(item, true, true);
                    backToInboxList();
                  }}
                >
                  {t("inbox-inboxdetail.done")}
                </Button>
              )}
            </div>
            <InboxTaskLinks item={item} />
            {item.summary && !showEvent && (
              <p className="mt-3 text-[13px] leading-relaxed text-ink">
                <Spark />
                {item.summary}
              </p>
            )}
            {!showEvent && <InboxLine item={item} />}
            {!done && !item.handling && agentWorking(item) && <div className="mt-2"><AgentWorking it={item} /></div>}
            <div className="mt-3 flex flex-wrap gap-2">
              {canDraft(item) ? (
                <Button tone="accent" icon="write" onClick={() => setDialog("draft")}>{draft && !done ? t("inbox-inboxdetail.view_draft") : t("inbox-inboxdetail.ai_draft")}</Button>
              ) : (
                // Answered in the source's own app (a CI run has nothing to answer: only "Hand to AI").
                item.source !== "larkcli" && openURL(item) && <Button tone="accent" icon="external" onClick={() => openSystem(openURL(item)!)}>{openTitle(item)}</Button>
              )}
              {item.source === "larkcli" && openURL(item) && (
                <Button tone="secondary" icon="external" onClick={() => openSystem(openURL(item)!)}>
                  {openURL(item)!.startsWith("https://applink.larksuite.com/") ? t("inbox-inboxdetail.open_in_lark") : t("inbox-inboxdetail.open_in_feishu")}
                </Button>
              )}
              {item.source !== "assistant" && <Button tone="secondary" icon="spark" onClick={() => setDialog("handoff")}>{t("inbox-inboxdetail.hand_to_ai")}</Button>}
            </div>
          </div>
        </div>
        {embed ? (
          <>
            <EventBody item={item} onSource={openSource} embedded={originalOpen} />
            {!originalOpen && (
              <div className="shrink-0 border-b border-line px-4 py-1 sm:px-6">
                <div className="mx-auto max-w-[760px]">
                  <Button tone="quiet" size="sm" testId="inbox-original-row-toggle" expanded={false} icon={item.mail ? "mail" : "chat"} onClick={toggleOriginal}>
                    {t("inbox-inboxdetail.view_source")}
                  </Button>
                </div>
              </div>
            )}
            {originalOpen && own()}
          </>
        ) : showEvent ? <EventBody item={item} onSource={setSource} /> : !event && place ? <InboxExtensionBody key={`${itemKey(item)}/${place.extension}/${place.id}`} item={item} place={place} own={own} onOwn={setOwnShown} /> : own()}
        {dialog === "draft" && <DraftDialog item={item} onClose={() => setDialog("")} />}
        {dialog === "handoff" && <HandOffDialog item={item} onClose={() => setDialog("")} />}
        <SameMatterDialog open={dialog === "same"} item={item} onClose={() => setDialog("")} />
      </div>
    </ItemManager.Provider>
  );
}

export function DetailPlaceholder() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
      <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3">
        <Icon name="inbox" size={18} />
      </span>
      <p className="text-[13.5px] font-medium text-ink">{t("inbox-inboxdetail.placeholder_title")}</p>
      <p className="text-[12.5px] text-ink-3">{t("inbox-inboxdetail.placeholder_hint")}</p>
    </div>
  );
}
