import { storedValue } from "../../lib/compatibility";
// Beautiful UI's Prompt Bar, wired to a session: multi-line input, / commands, @ files and sessions, image attachments
// (paste, drop, pick; click one to mark it up), files uploaded to the session's machine, send — and stop while the
// agent works and nothing is typed. Quotes, edits and edited images from the chat arrive as draft events (drafts.ts).
// Its bottom row is cindy's toolbar (#5324, at the owner's request): a bare round + that opens the + / @ panel
// (ComposerPanel.tsx) and the `leading` permission picker on the left; the model pill, a second Stop while the agent
// works and something is typed, voice and one round Send / Queue / Stop button on the right.
import { memo, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuickPrompts } from "../../lib/extension-declared";
import { promptCommands, type ComposerCommand as Command } from "../../lib/extension-prompts";
import { useSavedPromptRows } from "../../lib/saved-prompt-rows";
import { LOOP_TEXT, loopHint } from "../../lib/loop-command";
import { WORKFLOWS_COMMAND } from "../../lib/background-panel";
import { ApiError, errorText } from "../../lib/api";
import { composerEntryDrop, dragHasEntry, droppedEntry } from "../../lib/entry-drag";
import { uuid } from "../../lib/format";
import { lazyPage } from "../../lib/lazy-page";
import { enterSends, hasFinePointer, useMedia } from "../../lib/viewport";
import { useModalFocus } from "../../ui/overlay";
import { COMPACT_COMPOSER, COMPOSER_LINES, lineCount, offersExpand } from "./composer-grow";
import { INPUT_MAX } from "./dock";
import { imagePayload, MAX_IMAGE, quotePath, releaseAttachments, sendFile } from "../../lib/attachments";
import { activateComposer, addContext, getContexts, newContext, registerComposer, removeContext, useContexts, withContexts, type ComposerHandle } from "../../lib/links";
import { addQuote, applyDraft, getQuotes, onDraftEvent, parseQuotes, removeQuote, replaceNeedsConfirm, sendDraft, setQuotes, splitQuotes, storeDraft, useQuotes, withQuoteBlock, withUnsent, type DraftImage } from "../../lib/drafts";
import type { EditedImage } from "../../lib/image-edit";
import { mentionAt, replaceToken, sessionQuote, workspaceContext, type MentionToken } from "../../lib/mentions";
import { machineWithHub } from "../../lib/mesh";
import { skillContext, skillDraft, splitSkillDraft } from "../../lib/skill-mentions";
import { getState } from "../../lib/store";
import { AttachmentTray } from "../../ui/AttachmentTray";
import { cx, EASE, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ContextChips } from "../link/ContextChips";
import { ForwardBars } from "./ForwardCard";
import { QuoteBars } from "./Quote";
import type { ComposerPanelHandle, MentionRow, MentionSource, PanelPick } from "./ComposerPanel";
import type { PlanMode } from "./PermissionPicker";
import { useSendHistory } from "./useSendHistory";
import { isModelCommand, requestModelPicker } from "../../lib/model-switch";
import { isPermissionsCommand, permissionsCommand, PERMISSIONS_COMMAND, requestPermissionPicker } from "../../lib/permissions-command";
import { MAX_IMAGES, rankCommands } from "../../lib/composer-commands";
import { escapeCancelsEdit, escapeCountsForEdit, upStartsEdit, type EditLastBox } from "../../lib/edit-last-key";
import { t } from "../../i18n";
import type { VoiceOffer } from "./voice/VoiceParts";

// Drawn only while an attached image is edited: its own chunk, fetched by the idle preload or the first edit (lib/lazy-page.ts).
const ImageEditor = lazyPage(() => import("./ImageEditor").then((m) => m.ImageEditor), { idle: true });
// The + / @ panel, drawn only while it is open: its own chunk and words, fetched by the idle preload or the first opening.
const ComposerPanel = lazyPage(() => import("./ComposerPanel").then((m) => m.ComposerPanel), { idle: true });
const AssistantComposerPanel = lazyPage(() => import("./AssistantComposerPanel").then((m) => m.AssistantComposerPanel));
// Skill-aware history labels are needed only when recall search opens, not on the first screen.
const HistorySearch = lazyPage(() => import("./HistorySearch").then((m) => m.HistorySearch));
// The "/" palette (cindy's, #5325), drawn once "/" is typed: its own chunk too, fetched by the idle preload.
const SlashPalette = lazyPage(() => import("./SlashPalette").then((m) => m.SlashPalette), { idle: true });
/** How long "Not sent yet" stays under the box after Enter was pressed while blocked. */
const NUDGE_MS = 4000;

export interface Attachment {
  id: string;
  name: string;
  mime: string;
  data: string; // base64
  url: string; // object URL for the preview
  /** Set once the picture has been through the image editor: its source, edits and history (lib/image-edit.ts), so
   *  its tile opens it where the editor stopped; freed with the picture (releaseAttachments). */
  edited?: EditedImage;
}

export interface Outgoing {
  text: string;
  images: { mime: string; data: string }[];
  /** An edit of the last message, sent after its rewind (#4605): the old prompt, which Claude may have put back in its
   *  box, cleared exactly before typing. A refusal leaves the edit in the box unsent, not as a failed bubble. */
  edit?: { clearDraft: string };
}

/** The box is editing the last message (#4503; ChatView.editLastMessage). */
export interface EditingLast {
  /** Runs before the edit is sent: the rewind to before the old message. False: it did not happen, the edit stays.
   *  `clearDraft`: the old prompt the send asks the node to clear from Claude's box (#4605). */
  commit: () => Promise<boolean | { clearDraft: string }>;
  /** The edit was cancelled, or the box moved on to other words. */
  cancel: () => void;
}

export const AGENT_COMMANDS: Command[] = [
  { word: t("chat-composer.cmd_continue_word"), desc: t("chat-composer.cmd_continue_desc"), text: t("chat-composer.cmd_continue_text") },
  { word: t("chat-composer.cmd_commit_word"), desc: t("chat-composer.cmd_commit_desc"), text: t("chat-composer.cmd_commit_text") },
  { word: t("chat-composer.cmd_report_word"), desc: t("chat-composer.cmd_report_desc"), text: t("chat-composer.cmd_report_text") },
  // Persona's own (cindy's /workflows): the chat page opens the Background tasks panel instead of sending it (#5421)
  { word: WORKFLOWS_COMMAND, desc: t("chat-composer.cmd_workflows_desc"), text: WORKFLOWS_COMMAND },
  // the agent's own commands: the palette names it beside them (agentCommands)
  { word: "/compact", desc: t("chat-composer.cmd_compact_desc"), text: "/compact", agent: true },
  { word: "/context", desc: t("chat-composer.cmd_context_desc"), text: "/context", agent: true },
  { word: "/model", desc: t("chat-composer.cmd_model_desc"), text: "/model ", agent: true },
  // Persona's own: opens the permission picker with every mode instead of sending it (#6476, lib/permissions-command.ts)
  permissionsCommand(),
  { word: "/loop", desc: t("chat-composer.cmd_loop_desc"), text: LOOP_TEXT, agent: true },
  { word: "/clear", desc: t("chat-composer.cmd_clear_desc"), text: "/clear", confirm: t("chat-composer.cmd_clear_confirm"), agent: true },
];

function parseSlash(draft: string) {
  const m = /^\/(\S*)$/.exec(draft);
  return m ? m[1].toLowerCase() : null;
}

async function readImage(file: File): Promise<Attachment> {
  return { ...await imagePayload(file), id: uuid(), name: file.name || "image.png", url: URL.createObjectURL(file) };
}

function fromDraftImage(img: DraftImage, id = uuid(), edited?: EditedImage): Attachment {
  const bin = atob(img.data);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return { ...img, id, url: URL.createObjectURL(new Blob([bytes], { type: img.mime })), ...(edited ? { edited } : {}) };
}

// memo: a chat renders again for each transcript item, and this box takes none of them (ChatView keeps its props stable)
export const Composer = memo(function Composer({
  draftKey,
  placeholder,
  machineId,
  uploadDest,
  working,
  needsYou,
  disabled,
  commands = AGENT_COMMANDS,
  onSend,
  onStop,
  onEditLastMessage,
  editingLast,
  loadingDraft,
  stopping,
  autoFocus,
  footer,
  leading,
  trailing,
  suggestion,
  suggestionRisky,
  mention,
  planMode,
  onFocusChange,
  blocked,
  voice,
  top,
}: {
  draftKey: string;
  placeholder: string;
  machineId?: string;
  /** Where a file sent to machineId lands: "health" keeps it in the health space's reports/ (docs/assistant/health.md "Checkup reports"). */
  uploadDest?: "health";
  working?: boolean;
  /** Waits on a question, plan or approval card: no suggestion pill then (iOS status.isActive). */
  needsYou?: boolean;
  disabled?: boolean;
  commands?: Command[];
  /** False: it did not go and nothing shows it (the chat keeps one that failed as a failed bubble, true), so the words go
   *  back into the box. */
  onSend: (m: Outgoing) => Promise<boolean>;
  onStop?: () => void;
  /** Starts editing the last Claude prompt in this box (↑ in it while empty, Esc twice; #4503, #2945). */
  onEditLastMessage?: () => void;
  /** Set while the box edits the last message: a bar says so, Esc or × cancels, and a send rewinds first. */
  editingLast?: EditingLast;
  /** A rewind is fetching the draft: keep receiving draft events but prevent typing or sending until it finishes. */
  loadingDraft?: boolean;
  /** A stop is on its way: the button shows it and takes no more presses. */
  stopping?: boolean;
  autoFocus?: boolean;
  /** In the bottom row's flexible middle (no chat passes one since #5324: the source and folder are under the box). */
  footer?: React.ReactNode;
  /** Right after the +: an Agent's or a Persona conversation's permission picker (#5324, PermissionPicker.tsx). Keep it the
   *  same object while nothing in it changes, as `trailing`. */
  leading?: React.ReactNode;
  /** At the right of the bottom row, before voice / Stop / Send: an Agent's model pill (#5032, ModelPill.tsx). It sizes
   *  itself by the card's width (the card is the `composer` container); keep it the same object while nothing in it
   *  changes, as `footer`. */
  trailing?: React.ReactNode;
  suggestion?: string;
  /** The suggestion is risky (Session.suggestion_risky): it is only filled in, with no one-tap send. */
  suggestionRisky?: boolean;
  /** Turns on the @ menu: the chat's files and folder, and other sessions. */
  mention?: MentionSource;
  /** The + / @ panel's Plan mode check row (#5324, PermissionPicker.tsx `usePlanMode`), where the session has modes. */
  planMode?: PlanMode;
  /** The text box gains / loses focus (on a phone: the keyboard comes up / goes down). */
  onFocusChange?: (focused: boolean) => void;
  /** The main conversation cannot answer yet (docs/assistant/onboarding.md 4.4): the send button says what to do first and opens
   *  it; the box still takes typing and keeps the draft, nothing is sent or queued. */
  blocked?: { label: string; onClick?: () => void; onNudge?: () => void };
  /** Voice chat (docs/web/web.md "Voice"): with an empty box and an idle agent it takes the send button's place, round;
   *  otherwise it is a plain icon before send / stop. "explain": its click says why a call cannot start here. */
  voice?: VoiceOffer;
  /** First in the card, flush with its top and edges, a hairline under it: the chat's queued messages (QueuePanel.tsx,
   *  cindy's pending queue, #5327). Keep it the same object while nothing in it changes, as `footer`. */
  top?: React.ReactNode;
}) {
  const [draft, setDraft] = useState(() => splitSkillDraft(storedValue(`draft:${draftKey}`) ?? "").text);
  const [atts, setAtts] = useState<Attachment[]>([]);
  // The suggestion just sent with its arrow: it stays on the hub until the agent starts, and each tap would be a new message.
  const [sentPill, setSentPill] = useState<string | null>(null);
  useEffect(() => {
    if (working) setSentPill(null);
  }, [working]);
  // "Quote" and comments (lib/comments.ts): bars above the input, sent as `> ` blocks in front of the message (lib/drafts.ts)
  const quotes = useQuotes(draftKey).list;
  // files still on their way to the machine, per conversation: the spinner belongs to the one they were picked in
  const [uploads, setUploads] = useState<Record<string, number>>({});
  const uploading = uploads[draftKey] ?? 0;
  const [dismissed, setDismissed] = useState(false);
  const [active, setActive] = useState(0);
  const [dragging, setDragging] = useState(false);
  const [caret, setCaret] = useState(0);
  const [focused, setFocused] = useState(false);
  // the + panel (#5324): open while its search box has the keys
  const [plusOpen, setPlusOpen] = useState(false);
  const panel = useRef<ComposerPanelHandle>(null);
  const [editing, setEditing] = useState<Attachment | null>(null);
  // Enter while blocked (#4480): a line says why nothing went, the button and the card light up; it fades after a while
  const [nudge, setNudge] = useState(0);
  useEffect(() => {
    if (!nudge) return;
    const timer = setTimeout(() => setNudge(0), NUDGE_MS);
    return () => clearTimeout(timer);
  }, [nudge]);
  const input = useRef<HTMLTextAreaElement>(null);
  const lastEscape = useRef<number | null>(null);
  // the edit now, for the draft events' and the send's closures
  const editNow = useRef(editingLast);
  editNow.current = editingLast;
  useEffect(() => { lastEscape.current = null; }, [draftKey, working, disabled, onEditLastMessage]);
  const box = useRef<HTMLDivElement>(null);
  // The box grows to six lines, then scrolls; the dock's CSS cap protects exceptionally short viewports.
  // Cache only the measured height cap and overflow, shared by both widths; phones also offer Expand.
  const compact = useMedia(COMPACT_COMPOSER);
  const [grow, setGrow] = useState({ cap: 0, over: false });
  const [expanded, setExpanded] = useState(false);
  // the caret to put back in the box when the full editor closes
  const collapsedSel = useRef<[number, number] | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  // text picked in the link panel waits here (lib/links.ts), and "AI edit" sends through this composer
  const contexts = useContexts(draftKey);
  const restoreSkills = (text: string) => {
    const split = splitSkillDraft(text);
    getContexts(draftKey).filter((r) => r.kind === "skill").forEach((r) => removeContext(draftKey, r.id));
    split.skills.forEach((r) => addContext(draftKey, r));
    return split.text;
  };
  const skillEnvironment = JSON.stringify([machineId, mention?.cwd, mention?.skills]);
  const previousSkillEnvironment = useRef(skillEnvironment);
  useEffect(() => {
    if (previousSkillEnvironment.current !== skillEnvironment && getContexts(draftKey).some((r) => r.kind === "skill")) {
      toast(t("chat-composer.skills_changed"), "error");
    }
    previousSkillEnvironment.current = skillEnvironment;
  }, [skillEnvironment, draftKey]);
  const sendNow = useRef<(text: string) => Promise<boolean>>(async () => false);
  sendNow.current = async (text) => (disabled || blocked || loadingDraft ? false : onSend({ text, images: [] }));
  const canTake = useRef(!disabled);
  canTake.current = !disabled && !blocked;
  const handle = useMemo<ComposerHandle>(() => ({ key: draftKey, send: (text) => sendNow.current(text), focus: () => input.current?.focus({ preventScroll: true }), ready: () => canTake.current }), [draftKey]);
  useEffect(() => registerComposer(handle), [handle]);

  const quote = quotes.length > 0;
  // the pictures in the box right now and the conversation shown, read by the cleanups and by a send that finishes
  // after the box moved on: their object URLs are freed when they leave the box without being sent
  const attsNow = useRef(atts);
  attsNow.current = atts;
  // pictures being read for this box, counted against MAX_IMAGES before they land
  const reading = useRef(0);
  const roomForImage = () => attsNow.current.length + reading.current < MAX_IMAGES;
  // appends at once to the ref as well, so a second paste or draft image in the same tick sees it
  const appendImage = (a: Attachment) => {
    attsNow.current = [...attsNow.current, a];
    setAtts((cur) => [...cur, a]);
  };
  // one word for the pictures turned away in the same tick (a paste of several, a burst of draft images)
  const refusing = useRef(false);
  const refuseImages = () => {
    if (refusing.current) return;
    refusing.current = true;
    queueMicrotask(() => (refusing.current = false));
    toast(t("chat-composer.too_many_images", { max: MAX_IMAGES }), "error");
  };
  const keyNow = useRef(draftKey);
  keyNow.current = draftKey;
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      releaseAttachments(attsNow.current);
    };
  }, []);

  // a different conversation: its own draft
  useEffect(() => {
    setDraft(restoreSkills(storedValue(`draft:${draftKey}`) ?? ""));
    setPlusOpen(false);
    releaseAttachments(attsNow.current);
    setAtts([]);
    // not on a touch screen alone: there the focus brings the keyboard up over the conversation just opened
    if (autoFocus && hasFinePointer()) setTimeout(() => input.current?.focus({ preventScroll: true }), 0);
  }, [draftKey, autoFocus]);

  // "Quote" / "Edit" / a queued message pulled back / an edited picture, from the chat (lib/drafts.ts)
  const draftNow = useRef(draft);
  draftNow.current = draft;
  useEffect(
    () =>
      onDraftEvent(draftKey, (e) => {
        // a send from an earlier box of this conversation failed: its words come back even while sending is off
        if (e.type === "unsent") {
          const split = splitSkillDraft(e.text);
          split.skills.forEach((r) => addContext(draftKey, r));
          setDraft((cur) => withUnsent(cur, split.text));
          return;
        }
        if (!canTake.current) return;
        if (e.type === "image") {
          if (!roomForImage()) {
            refuseImages();
            return;
          }
          appendImage(fromDraftImage(e.image, uuid(), e.edited));
          input.current?.focus({ preventScroll: true });
          return;
        }
        if (e.type === "quote") {
          // the input keeps what is typed; the bar shows the quote, a new one in place of the last
          addQuote(draftKey, { text: e.text.trim(), from: e.from, ts: e.ts, task: e.task }, false);
          requestAnimationFrame(() => input.current?.focus({ preventScroll: true }));
          return;
        }
        // other words in place of an edit's (Edit as new, a rewind's message): no longer an edit
        const startsEdit = e.type === "replace" && Boolean(e.edit);
        if (e.type === "replace" && !startsEdit) editNow.current?.cancel();
        // "Edit" a message sent with a quote: the quote goes back to the bar, the words to the input
        const selected = e.type === "replace" ? splitSkillDraft(e.text) : null;
        const split = e.type === "replace" ? splitQuotes(selected!.text) : null;
        const nextQuotes = e.type === "replace" ? (split ? parseQuotes(split.quotes) : []) : null;
        const next = split ? split.rest : selected ? selected.text : applyDraft(draftNow.current, e);
        // "Edit" or a rewind over unsent words, quotes or comments puts the message in at once, with no question first:
        // the notice's Undo brings back what was typed, the quotes on the bar and where the caret was (#2450). An empty
        // bar, or one that would send the same blocks, loses nothing.
        const before = getQuotes(draftKey).list;
        const dropsQuotes = before.length > 0 && withQuoteBlock(before, "") !== withQuoteBlock(nextQuotes ?? [], "");
        const kept =
          nextQuotes && (replaceNeedsConfirm(draftNow.current, next) || dropsQuotes || getContexts(draftKey).some((r) => r.kind === "skill"))
            ? { text: draftNow.current, skills: getContexts(draftKey).filter((r) => r.kind === "skill"), quotes: before, caret: input.current?.selectionStart ?? draftNow.current.length }
            : null;
        if (kept) {
          toast(t("chat-composer.replaced"), "ok", {
            label: t("chat-composer.undo"),
            run: () => {
              if (startsEdit) editNow.current?.cancel();
              setQuotes(draftKey, kept.quotes);
              restoreSkills(skillDraft(kept.skills, kept.text));
              // the box went away or shows another conversation: the words go back to this one's stored draft
              if (!mounted.current || keyNow.current !== draftKey) {
                storeDraft(draftKey, skillDraft(kept.skills, kept.text));
                return;
              }
              setDraft(kept.text);
              setDismissed(false);
              requestAnimationFrame(() => {
                const el = input.current;
                if (!el) return;
                el.focus({ preventScroll: true });
                el.setSelectionRange(kept.caret, kept.caret);
                setCaret(kept.caret);
              });
            },
          });
        }
        if (nextQuotes) setQuotes(draftKey, nextQuotes);
        if (selected) restoreSkills(skillDraft(selected.skills, selected.text));
        setDraft(next);
        setDismissed(false);
        requestAnimationFrame(() => {
          const el = input.current;
          if (!el) return;
          el.focus({ preventScroll: true });
          el.setSelectionRange(next.length, next.length);
          setCaret(next.length);
        });
      }),
    [draftKey],
  );

  // What is typed goes into the conversation's stored draft 250 ms after the last key; one still waiting is written at
  // once when the box goes away or moves to another conversation, and when the page is hidden (closed, reloaded,
  // left), so the last words typed before switching are not lost (#4591).
  const unsaved = useRef<{ key: string; text: string } | null>(null);
  const saveDraft = useCallback(() => {
    const u = unsaved.current;
    if (!u) return;
    unsaved.current = null;
    storeDraft(u.key, skillDraft(getContexts(u.key), u.text));
  }, []);
  // The render that moves the box to another conversation still holds the old words: they are not that one's draft
  // (the box reads its own next, and this runs again if it differs).
  const savedFor = useRef(draftKey);
  useEffect(() => {
    if (savedFor.current !== draftKey) {
      savedFor.current = draftKey;
      return;
    }
    unsaved.current = { key: draftKey, text: draft };
    const t = setTimeout(saveDraft, 250);
    return () => clearTimeout(t);
  }, [draft, draftKey, saveDraft, contexts]);
  useEffect(() => {
    addEventListener("pagehide", saveDraft);
    return () => {
      removeEventListener("pagehide", saveDraft);
      // only this conversation's: on a switch the box still holds the old words under the new key for one render
      if (unsaved.current?.key === draftKey) saveDraft();
    };
  }, [draftKey, saveDraft]);

  useLayoutEffect(() => {
    const el = input.current;
    if (!el) return;
    // Measuring collapses the box to 0 for one layout: its parent holds its height meanwhile. Otherwise the chat above
    // grows by the box's height for that layout, the browser clamps the chat's scroll offset to the taller view, and
    // with the box back the chat sits that much above its newest line (#5375). Under the cap the chat's own
    // ResizeObserver brought the end back; at the cap the chat's height no longer changes, so every key left it there.
    const holder = el.parentElement;
    const held = holder?.style.minHeight ?? "";
    if (holder) holder.style.minHeight = `${holder.getBoundingClientRect().height}px`;
    el.style.height = "0px";
    const h = el.scrollHeight;
    // Both widths use the measured six-line cap before paint; CSS leaves room for Send in very short viewports.
    // Longer drafts scroll inside the same native textarea.
    el.style.height = `${Math.max(h, 24)}px`;
    if (holder) holder.style.minHeight = held;
    const css = getComputedStyle(el);
    const line = parseFloat(css.lineHeight) || 24;
    const pad = (parseFloat(css.paddingTop) || 0) + (parseFloat(css.paddingBottom) || 0);
    const cap = COMPOSER_LINES * line + pad;
    const over = offersExpand(lineCount(h - pad, line), COMPOSER_LINES);
    setGrow((g) => (g.cap === cap && g.over === over ? g : { cap, over }));
  }, [draft, compact, grow.over]);
  const showExpand = compact && grow.over && !disabled;
  useEffect(() => {
    if (!compact || disabled) setExpanded(false);
  }, [compact, disabled]);
  // Back from the full editor: the caret where it was there, in the box.
  useEffect(() => {
    if (expanded || !collapsedSel.current) return;
    const el = input.current;
    const [a, b] = collapsedSel.current;
    collapsedSel.current = null;
    if (!el) return;
    el.focus({ preventScroll: true });
    el.setSelectionRange(a, b);
  }, [expanded]);

  const quickPrompts = useQuickPrompts();
  // your saved prompts (#7329) after the chat's own commands, read once a box has the focus or a "/"
  const savedPrompts = useSavedPromptRows(focused || draft.startsWith("/"));
  const allCommands = useMemo(() => [...commands, ...savedPrompts, ...promptCommands(quickPrompts)], [commands, savedPrompts, quickPrompts]);
  const query = dismissed ? null : parseSlash(draft);
  // A /loop draft gets a line under the box saying what it will do (only where the menu offers /loop: Claude agents).
  const loop = commands.some((c) => c.text === LOOP_TEXT) ? loopHint(draft) : null;
  // the "/" palette is open on a lone "/word" wherever there are commands, "No matching commands" included (cindy's)
  const slashOpen = query !== null && allCommands.length > 0;
  const rows = useMemo(() => (slashOpen ? rankCommands(allCommands, query) : []), [slashOpen, query, allCommands]);
  useEffect(() => setActive(0), [query]);
  const closeSlash = useCallback(() => setDismissed(true), []);

  // @: the token just before the caret (lib/mentions.ts)
  const token = mention && focused && !dismissed && query === null && caret <= draft.length ? mentionAt(draft, caret) : null;
  const mOpen = Boolean(token);

  const trackCaret = () => {
    const el = input.current;
    if (el) setCaret(el.selectionStart);
  };

  // Editing the last message (#4503): the words the box got when it began, so an untouched edit counts as the history
  // entry shown (↑ goes on to older messages, ↓ back to an empty box); each of those ends the edit, its quotes with it.
  const editStart = useRef<string | null>(null);
  if (!editingLast) editStart.current = null;
  else if (editStart.current === null) editStart.current = draft;
  const leaveEdit = () => {
    if (!editNow.current) return;
    setQuotes(draftKey, []);
    editNow.current.cancel();
  };
  // ↑ / ↓ recall and Ctrl+R search of what was sent (useSendHistory.ts); desktop only
  const history = useSendHistory({
    draftKey,
    draft: skillDraft(contexts, draft),
    setDraft: (text) => {
      leaveEdit();
      setDraft(restoreSkills(text));
    },
    setCaret,
    input,
    disabled,
    editing: editStart.current,
  });
  // Esc or × while editing: the box as it was before, empty; nothing was asked of the computer
  const cancelEdit = () => {
    const edit = editNow.current;
    if (!edit) return;
    setDraft("");
    draftNow.current = "";
    unsaved.current = null;
    storeDraft(draftKey, "");
    setQuotes(draftKey, []);
    getContexts(draftKey).forEach((r) => removeContext(draftKey, r.id));
    releaseAttachments(attsNow.current);
    attsNow.current = [];
    setAtts([]);
    edit.cancel();
    requestAnimationFrame(() => input.current?.focus({ preventScroll: true }));
  };
  // what ↑ and Esc Esc may start an edit from (lib/edit-last-key.ts); the page's dialogs are looked up only for those keys
  const editBox = (): EditLastBox => ({
    offered: Boolean(onEditLastMessage),
    busy: Boolean(working || needsYou || disabled || blocked || loadingDraft),
    holds: Boolean(draft || atts.length || uploading || reading.current || contexts.length || quotes.length),
    menu: Boolean(slashOpen || mOpen || history.searching || document.querySelector("[role=dialog],[role=menu]")),
  });

  // `text` at the caret (or the end), a space before it when it would touch a word: a dropped Files entry, a file from the +
  const insertText = (text: string) => {
    const el = input.current;
    const at = el ? Math.min(el.selectionStart, draft.length) : draft.length;
    const before = draft.slice(0, at);
    const lead = before && !/\s$/.test(before) ? " " : "";
    setDraft(before + lead + text + draft.slice(at));
    const caret = at + lead.length + text.length;
    setCaret(caret);
    requestAnimationFrame(() => {
      input.current?.focus({ preventScroll: true });
      input.current?.setSelectionRange(caret, caret);
    });
  };

  // A row of the + / @ panel: a session goes with the message as a quote, not as text (PromptItems.quote), a workspace as
  // a context the hub fills in; a file as `@path `. In place of the typed @ token (`at`), or at the caret from the +.
  const pickRow = (r: MentionRow, at: MentionToken | null) => {
    if (r.kind === "session" || r.kind === "workspace" || r.kind === "skill") {
      if (r.kind === "session") {
        const q = sessionQuote(r.session);
        addContext(draftKey, newContext("context", q.title, q.url, q.text));
      } else if (r.kind === "skill") {
        addContext(draftKey, skillContext(r.skill));
      } else {
        const w = workspaceContext(r.workspace, (id) => machineWithHub(id, getState().machines));
        addContext(draftKey, { ...newContext("workspace", w.title, w.url, w.text), attrs: w.attrs });
      }
      if (!at) {
        requestAnimationFrame(() => input.current?.focus({ preventScroll: true }));
        return;
      }
      const out = replaceToken(draft, at, "");
      setDraft(out.text);
      setCaret(out.caret);
      requestAnimationFrame(() => input.current?.setSelectionRange(out.caret, out.caret));
      return;
    }
    if (!at) return insertText(r.insert);
    const out = replaceToken(draft, at, r.insert);
    setDraft(out.text);
    setCaret(out.caret);
    requestAnimationFrame(() => {
      input.current?.focus({ preventScroll: true });
      input.current?.setSelectionRange(out.caret, out.caret);
    });
  };
  // The panel's own rows: the file picker, Plan mode. From a typed @ the token goes first, as cindy's.
  const pickPanel = (p: PanelPick, at: MentionToken | null) => {
    if (p.kind === "row") {
      if (!at) setPlusOpen(false);
      return pickRow(p.row, at);
    }
    if (at) {
      const out = replaceToken(draft, at, "");
      setDraft(out.text);
      setCaret(out.caret);
    } else setPlusOpen(false);
    if (p.action === "attach") {
      fileInput.current?.click();
      // from the +, the caret goes back to the box as after any other pick (the dialog hands the focus back to it)
      if (!at && hasFinePointer()) requestAnimationFrame(() => input.current?.focus({ preventScroll: true }));
    } else {
      planMode?.toggle();
      requestAnimationFrame(() => input.current?.focus({ preventScroll: true }));
    }
  };
  const closePlus = useCallback((refocus: boolean) => {
    setPlusOpen(false);
    if (refocus) input.current?.focus({ preventScroll: true });
  }, []);
  const togglePlus = () => {
    if (plusOpen) return setPlusOpen(false);
    setPlusOpen(true);
    // one panel at a time: the / palette (and a typed @'s panel) close until the box is typed in again
    setDismissed(true);
    // a touch screen alone: the keyboard goes down and the search box waits for a tap, so no key lands in the covered box
    if (!hasFinePointer()) input.current?.blur();
  };
  // a box turned off takes its + panel with it: it would come back later and take the focus
  useEffect(() => {
    if (disabled) setPlusOpen(false);
  }, [disabled]);

  // A Files panel entry dropped on the box: `@path ` at the caret, as the @ menu types it
  const dropEntry = (e: React.DragEvent): boolean => {
    const entry = droppedEntry(e.dataTransfer);
    if (!entry) return false;
    if (disabled) return true;
    const drop = composerEntryDrop(entry, { mid: machineId, cwd: mention?.cwd });
    if (drop.action === "refuse") {
      toast(drop.toast);
      return true;
    }
    insertText(drop.text);
    return true;
  };

  const addFiles = async (files: FileList | File[]) => {
    if (disabled) return;
    // at most MAX_IMAGES pictures a message, as on the phone: the ones past it are turned away with a word
    // (the count takes in pictures still being read by an earlier paste or drop)
    let refused = false;
    for (const f of Array.from(files)) {
      if (f.type.startsWith("image/")) {
        if (!roomForImage()) {
          refused = true;
          continue;
        }
        if (f.size > MAX_IMAGE) {
          toast(t("chat-composer.image_too_large", { name: f.name }), "error");
          continue;
        }
        reading.current++;
        let a: Attachment;
        try {
          a = await readImage(f);
        } finally {
          reading.current--;
        }
        // read after the box closed or moved to another conversation: freed, not put into that one
        if (mounted.current && keyNow.current === draftKey) appendImage(a);
        else releaseAttachments([a]);
      } else if (machineId) {
        void upload(f);
      } else {
        toast(t("chat-composer.images_only"), "error");
      }
    }
    if (refused) refuseImages();
  };

  // a file goes to the session's machine; its path lands in the input (docs/protocol/protocol.md "Sending files")
  const upload = async (f: File) => {
    const count = (key: string, by: number) =>
      setUploads((u) => {
        const { [key]: n = 0, ...rest } = u;
        return n + by > 0 ? { ...rest, [key]: n + by } : rest;
      });
    count(draftKey, 1);
    try {
      const path = await sendFile(machineId!, f, { dest: uploadDest });
      const p = quotePath(path);
      // finished after the box closed or moved to another conversation: into the draft it was picked for (its open
      // box, else the stored draft), not into the one shown now
      if (!mounted.current || keyNow.current !== draftKey) sendDraft(draftKey, { type: "append", text: p });
      else {
        const el = input.current;
        setDraft((d) => {
          const at = el && document.activeElement === el ? Math.min(el.selectionStart, d.length) : d.length;
          const before = d.slice(0, at),
            after = d.slice(at);
          return `${before}${before && !before.endsWith(" ") ? " " : ""}${p}${after.startsWith(" ") || !after ? "" : " "}${after}`;
        });
      }
      toast(t("chat-composer.uploaded", { path }), "ok");
    } catch (e) {
      toast(e instanceof ApiError && e.code === "too_large" ? t("chat-composer.file_too_large") : t("chat-composer.upload_failed", { error: errorText(e) }), "error");
    } finally {
      if (mounted.current) count(draftKey, -1);
    }
  };

  const canSend = (draft.trim().length > 0 || atts.length > 0 || contexts.length > 0 || quote) && !disabled && !loadingDraft;
  // Claude's suggested next message (iOS SuggestionPill): while the box is empty and the agent waits for a message
  // (not working, not on a card); a tap fills it
  // in, "Send now" sends it as it is (the node drops it once the agent works again)
  const pill = suggestion && suggestion !== sentPill && !editingLast && !blocked && !draft && !atts.length && !contexts.length && !quote && !working && !needsYou && !disabled ? suggestion : null;
  const fillSuggestion = (text: string) => {
    setDraft(text);
    setDismissed(true);
    setCaret(text.length);
    requestAnimationFrame(() => {
      const el = input.current;
      if (!el) return;
      el.focus({ preventScroll: true });
      el.setSelectionRange(text.length, text.length);
    });
  };
  const typed = Boolean(draft.trim() || atts.length || contexts.length || quote);
  // the round button is Stop while the agent works and nothing is typed; with something typed Send queues it and a
  // second Stop sits left of voice (cindy's mainSlotIsStop / showSecondaryStop)
  const showStop = working && !typed && onStop;
  const secondStop = Boolean(working && typed && onStop && !blocked);
  const queues = Boolean(working || needsYou);
  // an empty box and an idle agent: voice takes the send button's place (it would be off anyway), as on iOS
  const roundVoice = voice?.mode === "start" && !canSend && !showStop && !blocked && !disabled;

  const submit = async (text = draft) => {
    const refs = getContexts(draftKey);
    const q = getQuotes(draftKey).list;
    if (disabled || blocked || loadingDraft || (!text.trim() && !atts.length && !refs.length && !q.length)) return;
    // Any message spends the suggestion shown: it stays on the hub until the agent starts (#4644).
    if (suggestion) setSentPill(suggestion);
    // An edit of the last message: the rewind to before the old one first, the box kept as it is (read-only meanwhile).
    // Refused or not possible: nothing changed on the computer, and the edit stays in the box (ChatView says why).
    const edit = editNow.current;
    let committed: Outgoing["edit"];
    if (edit) {
      const done = await edit.commit();
      if (!done) return;
      if (typeof done === "object") committed = done;
      // the box went away or shows another conversation: the words wait in this one's stored draft
      if (!mounted.current || keyNow.current !== draftKey) return;
    }
    // The message shows in the chat at once (a sending bubble), so the box empties at once too: waiting for the hub to
    // hand it to the agent (seconds when it starts up) left the same words in both places. A send that never went
    // (onSend false) puts them back; one that failed stays in the chat as a failed bubble.
    const sent = atts;
    setDraft("");
    draftNow.current = "";
    unsaved.current = null;
    storeDraft(draftKey, "");
    setAtts([]);
    refs.forEach((r) => removeContext(draftKey, r.id));
    setQuotes(draftKey, []);
    // Ready for the next message now, not when the hub answers (the chat sends them in order). Back to the box only with
    // a precise pointer and if the focus has not gone elsewhere: on a touch screen it would raise the keyboard and fold
    // the waiting cards (dock.ts).
    const at = document.activeElement;
    if (hasFinePointer() && (!at || at === document.body || box.current?.contains(at))) input.current?.focus({ preventScroll: true });
    // picked contexts ride in front as <persona-context> tags (docs/protocol/protocol.md "Tags in messages"), the quote as `> ` lines
    const ok = await onSend({ text: withContexts(refs, withQuoteBlock(q, text), sent.length > 0), images: sent.map((a) => ({ mime: a.mime, data: a.data })), ...(committed ? { edit: committed } : {}) });
    if (ok) {
      history.record(skillDraft(refs, text));
      releaseAttachments(sent);
    } else {
      // the box closed or shows another conversation now: the words go back to this conversation's draft (its open
      // box, or the stored one), not into the box; the pictures are not put back there, only freed
      if (mounted.current && keyNow.current === draftKey) {
        setDraft((cur) => withUnsent(cur, text));
        setAtts((cur) => [...sent, ...cur]);
      } else {
        if (text) sendDraft(draftKey, { type: "unsent", text });
        releaseAttachments(sent);
      }
      refs.forEach((r) => addContext(draftKey, r));
      // the quotes back above this conversation's box (kept for it when the box shows another one or closed)
      if (q.length && !getQuotes(draftKey).list.length) setQuotes(draftKey, q);
    }
  };

  const pick = (c: Command) => {
    if (c.confirm && !confirm(c.confirm)) return;
    // `/permissions` opens the permission menu at once; where the chat has no picker it goes in the box as any command
    if (c.text === PERMISSIONS_COMMAND && requestPermissionPicker(draftKey)) {
      setDraft("");
      setDismissed(true);
      return;
    }
    setDraft(c.text);
    setDismissed(true);
    input.current?.focus({ preventScroll: true });
  };

  const MentionPanel = mention?.skills ? AssistantComposerPanel : ComposerPanel;
  return (
    <div
      ref={box}
      className="relative"
      inert={loadingDraft || undefined}
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes("Files") || dragHasEntry(e.dataTransfer)) {
          e.preventDefault();
          setDragging(true);
        }
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false);
      }}
      onDrop={(e) => {
        e.preventDefault();
        setDragging(false);
        if (dropEntry(e)) return;
        void addFiles(e.dataTransfer.files);
      }}
    >
      {slashOpen && (
        <Suspense fallback={null}>
          <SlashPalette rows={rows} active={Math.min(active, Math.max(rows.length - 1, 0))} onHover={setActive} onPick={pick} onClose={closeSlash} within={box} />
        </Suspense>
      )}

      {history.searching && <Suspense fallback={null}><HistorySearch onPick={history.pick} onClose={history.closeSearch} /></Suspense>}

      {token && (
        <Suspense fallback={null}>
          <MentionPanel from="at" query={token.query} source={mention} machineId={machineId} attach={disabled ? undefined : machineId ? "files" : "images"} plan={planMode} onPick={(p) => pickPanel(p, token)} onClose={closePlus} handle={panel} />
        </Suspense>
      )}
      {plusOpen && !disabled && (
        <Suspense fallback={null}>
          <MentionPanel from="plus" source={mention} machineId={machineId} attach={machineId ? "files" : "images"} plan={planMode} onPick={(p) => pickPanel(p, null)} onClose={closePlus} />
        </Suspense>
      )}

      {pill && <SuggestionPill text={pill} onFill={() => fillSuggestion(pill)} onSend={suggestionRisky ? undefined : () => { setSentPill(pill); void submit(pill); }} />}

      <div
        className={cx(
          // a container: what sits in its bottom row (the model pill) takes its width modes from the card's own width
          "@container/composer relative flex flex-col gap-1.5 rounded-[14px] border bg-surface p-1.5 shadow-card transition-[border-color,box-shadow] duration-150 focus-within:border-line-strong",
          dragging ? "border-accent shadow-[0_0_0_3px_var(--accent-tint)]" : "border-line",
        )}
      >
        {/* out to the card's border over its padding; nothing drawn yet (its chunk on the way), no gap either */}
        {top && <div data-composer-top className="-mx-1.5 -mt-1.5 empty:hidden">{top}</div>}
        {editingLast && (
          <div role="status" data-testid="composer-editing" className="flex min-w-0 items-center gap-1.5 pt-0.5 pr-0.5 pl-2 text-[11.5px] whitespace-nowrap" style={{ animation: "fade-in 160ms ease-out both" }}>
            <Icon name="pencil" size={12} stroke={2.2} className="shrink-0 text-accent-ink" />
            <span className="shrink-0 font-medium text-ink-2">{t("rewind.editing")}</span>
            {hasFinePointer() && <span className="min-w-0 truncate text-ink-3">{t("rewind.editing_hint")}</span>}
            <span className="flex-1" />
            <button
              type="button"
              aria-label={t("rewind.cancel_editing")}
              title={t("rewind.cancel_editing")}
              disabled={loadingDraft}
              onClick={cancelEdit}
              className="touch-hit flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2"
            >
              <Icon name="x" size={12} stroke={2.4} />
            </button>
          </div>
        )}
        <ContextChips draftKey={draftKey} />
        <ForwardBars draftKey={draftKey} onRemove={() => input.current?.focus()} />
        {quote && (
          <QuoteBars
            draftKey={draftKey}
            quotes={quotes}
            onRemove={(id) => {
              removeQuote(draftKey, id);
              input.current?.focus();
            }}
          />
        )}
        {atts.length > 0 && (
          <div className="px-0.5 pt-0.5">
            <AttachmentTray attachments={atts} onOpen={(id) => setEditing(atts.find((a) => a.id === id) ?? null)} onRemove={(id) => {
              const att = atts.find((a) => a.id === id);
              // an edited picture's source and edits go with it
              if (att) releaseAttachments([att]);
              setAtts((cur) => cur.filter((a) => a.id !== id));
            }} />
          </div>
        )}
        {/* a field that turns disabled loses focus with no blur event: the keyboard is down, say so */}
        <DisabledBlur disabled={Boolean(disabled)} focused={focused} onBlur={() => { setFocused(false); onFocusChange?.(false); }} />
        <div className="relative">
        <textarea
          ref={input}
          rows={1}
          value={draft}
          disabled={disabled}
          readOnly={loadingDraft}
          aria-busy={loadingDraft || undefined}
          onFocus={() => {
            activateComposer(handle);
            setFocused(true);
            onFocusChange?.(true);
          }}
          onBlur={() => {
            lastEscape.current = null;
            setFocused(false);
            onFocusChange?.(false);
          }}
          onChange={(e) => {
            setDraft(e.target.value);
            setDismissed(false);
            setCaret(e.target.selectionStart);
          }}
          onSelect={trackCaret}
          onClick={trackCaret}
          onPaste={(e) => {
            const files = Array.from(e.clipboardData.files);
            if (files.length) {
              e.preventDefault();
              void addFiles(files);
            }
          }}
          onKeyDown={(e) => {
            const previousEscape = lastEscape.current;
            lastEscape.current = null;
            if (loadingDraft) {
              e.preventDefault();
              e.stopPropagation();
              return;
            }
            if (e.nativeEvent.isComposing || e.keyCode === 229) {
              // the IME's own Escape (cancelling a candidate) must not reach ChatView's interrupt on the window
              if (e.key === "Escape") e.stopPropagation();
              return;
            }
            // `/model` alone opens the model panel instead of sending (where the agent switches models)
            if (enterSends(e, hasFinePointer()) && isModelCommand(draft) && requestModelPicker(draftKey)) {
              e.preventDefault();
              setDraft("");
              return;
            }
            // so does `/permissions` alone: the permission menu (#6476)
            if (enterSends(e, hasFinePointer()) && isPermissionsCommand(draft) && requestPermissionPicker(draftKey)) {
              e.preventDefault();
              setDraft("");
              return;
            }
            // ↑ in an empty box edits the last message where Edit last message is on offer (#4503); else the history's
            if (e.key === "ArrowUp" && onEditLastMessage && upStartsEdit(e, editBox())) {
              e.preventDefault();
              onEditLastMessage();
              return;
            }
            if (history.onKeyDown(e)) return;
            if (mOpen) {
              // ↑ ↓ move in the panel, Enter / Tab pick its row (none while it is empty, or its chunk still on the way)
              if ((e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter" || e.key === "Tab") && panel.current?.key(e.key)) {
                e.preventDefault();
                return;
              }
              if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                setDismissed(true);
                return;
              }
            }
            if (slashOpen) {
              if ((e.key === "ArrowDown" || e.key === "ArrowUp") && rows.length) {
                e.preventDefault();
                setActive((a) => (a + (e.key === "ArrowDown" ? 1 : rows.length - 1)) % rows.length);
                return;
              }
              // with nothing matching, Enter sends the words as they are: the list holds only a few of the agent's commands
              if ((e.key === "Enter" || e.key === "Tab") && rows.length) {
                e.preventDefault();
                pick(rows[Math.min(active, rows.length - 1)]);
                return;
              }
              if (e.key === "Escape") {
                // closing the menu only: keep it from ChatView's interrupt on the window
                e.preventDefault();
                e.stopPropagation();
                setDismissed(true);
                return;
              }
            }
            // Esc while editing cancels it; kept from ChatView's interrupt on the window
            if (escapeCancelsEdit(e, Boolean(editingLast))) {
              e.preventDefault();
              e.stopPropagation();
              cancelEdit();
              return;
            }
            if (e.key === "Escape" && onEditLastMessage && escapeCountsForEdit(e, editBox())) {
              e.preventDefault();
              e.stopPropagation();
              const now = performance.now();
              if (previousEscape !== null && now - previousEscape <= 500) onEditLastMessage();
              else lastEscape.current = now;
              return;
            }
            if (e.key === "Tab" && pill) {
              e.preventDefault();
              fillSuggestion(pill);
              return;
            }
            // a touch keyboard's return is a new line there; the send button sends (a hardware keyboard with a
            // trackpad, an iPad's, sends on Enter)
            if (enterSends(e, hasFinePointer())) {
              e.preventDefault();
              // any Enter while blocked says why (also an empty box or attachments only): submit() would do nothing
              if (blocked) {
                setNudge((n) => n + 1);
                // held down, the key repeats: one fresh ask, not one per repeat
                if (!e.repeat) blocked.onNudge?.();
              } else void submit();
            }
          }}
          placeholder={pill && hasFinePointer() ? t("chat-composer.suggestion", { suggestion: pill }) : placeholder}
          aria-label={t("chat-composer.message")}
          title={hasFinePointer() ? (onEditLastMessage ? t("composer.history_hint_edit") : t("composer.history_hint")) : undefined}
          style={{ maxHeight: grow.cap ? `min(${grow.cap}px, ${INPUT_MAX})` : INPUT_MAX }}
          className={cx("min-h-6 w-full resize-none overflow-y-auto bg-transparent px-2 pt-1.5 text-[14px] leading-[21px] text-ink outline-none [@media(pointer:coarse)]:text-[16px] [@media(pointer:coarse)]:leading-[24px] [overflow-wrap:anywhere] placeholder:text-ink-3 disabled:opacity-50", showExpand && "pr-8")}
        />
        {showExpand && (
          <button
            type="button"
            data-testid="composer-expand"
            aria-label={t("chat-composer.expand")}
            title={t("chat-composer.expand")}
            onClick={() => setExpanded(true)}
            className="touch-hit absolute top-0.5 right-0 flex size-7 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover hover:text-ink"
          >
            <Icon name="expand" size={14} stroke={2} />
          </button>
        )}
        </div>
        {loop && (
          <div role="status" className={cx("flex items-center gap-1.5 px-2 text-[11.5px]", loop.missing ? "text-ink-3" : "text-ink-2")}>
            <Icon name="loop" size={12} stroke={2.2} className="shrink-0" />
            <span className="min-w-0 truncate">{loop.text}</span>
          </div>
        )}
        {nudge > 0 && blocked && (
          <div role="status" data-testid="composer-nudge" className="flex items-center gap-1.5 px-2 text-[11.5px] text-ink-2" style={{ animation: "fade-in 160ms ease-out both" }}>
            <Icon name="info" size={12} stroke={2.2} className="shrink-0 text-accent-ink" />
            <span className="min-w-0 truncate">{t("chat-composer.blocked_hint", { label: blocked.label })}</span>
          </div>
        )}
        <div className="flex items-center gap-1 [@media(pointer:coarse)]:gap-2">
          <input ref={fileInput} type="file" multiple hidden onChange={(e) => e.target.files && void addFiles(e.target.files).finally(() => (e.target.value = ""))} />
          {/* cindy's +: bare until hover, as the model pill; it opens the + / @ panel (files, Plan mode, sessions…) */}
          <button
            type="button"
            data-composer-plus
            aria-label={t("chat-composer.add_more")}
            title={t("chat-composer.more_actions")}
            aria-haspopup="listbox"
            aria-expanded={plusOpen}
            disabled={disabled}
            onMouseDown={(e) => e.preventDefault()}
            onClick={togglePlus}
            className={cx(TOOL, "text-ink-2 disabled:opacity-50")}
          >
            {uploading ? <Spinner /> : <Icon name="plus" size={15} stroke={2} />}
          </button>
          {leading}
          {/* not overflow-hidden: the chips' tap areas reach past this row (touch-hit); the footer truncates its own text */}
          <div className="min-w-0 flex-1 px-1 text-[11.5px] whitespace-nowrap text-ink-3">{footer}</div>
          {trailing}
          {/* while the agent works and something is typed, Stop stays left of voice and Send queues (cindy's second slot) */}
          {secondStop && <StopButton stopping={stopping} onStop={onStop!} />}
          {voice && !roundVoice && (
            <button
              type="button"
              data-testid="composer-voice"
              data-voice-mode={voice.mode}
              aria-label={t("voice.voice_chat")}
              title={voice.label}
              onClick={voice.onClick}
              className={cx(TOOL, voice.mode === "start" ? "text-ink-2" : "text-ink-3")}
            >
              <Icon name="voice" size={15} stroke={2} />
            </button>
          )}
          {roundVoice ? (
            <button
              type="button"
              data-testid="composer-voice"
              data-voice-mode="start"
              aria-label={t("voice.voice_chat")}
              title={voice!.label}
              onClick={voice!.onClick}
              className={ROUND}
              style={{ animation: `pop-in 200ms ${EASE} both` }}
            >
              <Icon name="voice" size={15} stroke={2.2} />
            </button>
          ) : showStop ? (
            <StopButton stopping={stopping} onStop={onStop!} />
          ) : blocked ? (
            <button
              key={nudge}
              type="button"
              data-testid="composer-blocked"
              onClick={blocked.onClick}
              disabled={!blocked.onClick}
              className="touch-hit flex h-7 max-w-[60%] shrink-0 items-center gap-1 rounded-full bg-ink px-3 text-[12px] font-medium whitespace-nowrap text-surface transition-[opacity,transform] duration-150 enabled:hover:opacity-90 enabled:active:scale-[0.96] disabled:opacity-40"
              style={{ animation: nudge ? `nudge-x 420ms ease-out both` : `pop-in 200ms ${EASE} both` }}
            >
              <span className="truncate">{blocked.label}</span>
              <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0" />
            </button>
          ) : (
            <button
              type="button"
              data-testid="composer-send"
              // a message sent while the agent works or waits on you goes in its queue (Session.queued)
              aria-label={queues ? t("chat-composer.queue") : t("chat-composer.send")}
              title={queues ? t("chat-composer.queue_hint") : t("chat-composer.send_hint")}
              disabled={!canSend}
              onClick={() => void submit()}
              className={ROUND}
            >
              <Icon name="send" size={14} stroke={2} className="-translate-x-px translate-y-px fill-current" />
            </button>
          )}
        </div>
      </div>
      {expanded && (
        <ExpandedComposer
          value={draft}
          placeholder={placeholder}
          selection={[input.current?.selectionStart ?? draft.length, input.current?.selectionEnd ?? draft.length]}
          onChange={(v) => {
            setDraft(v);
            setDismissed(false);
          }}
          onCollapse={(sel) => {
            collapsedSel.current = sel;
            setExpanded(false);
          }}
          canSend={Boolean(canSend) && !blocked}
          onSend={(sel) => {
            collapsedSel.current = sel;
            setExpanded(false);
            void submit();
          }}
        />
      )}
      {editing && (
        <Suspense fallback={null}>
          <ImageEditor
            src={editing.edited?.source ?? editing.url}
            start={editing.edited}
            onCancel={() => setEditing(null)}
            onDone={(img, state) => {
              const old = editing;
              setEditing(null);
              // the picture as it came stays the source (the first edit's tile URL, kept); the earlier result goes
              const source = old.edited?.source ?? old.url;
              setAtts((cur) => cur.map((a) => (a.id === old.id ? fromDraftImage(img, a.id, { source, ...state }) : a)));
              if (old.url !== source) URL.revokeObjectURL(old.url);
            }}
          />
        </Suspense>
      )}
    </div>
  );
});

// The toolbar's two looks (#5324, cindy's): bare 30 px round tools that show the model pill's frame on hover or while open,
// and the one dark 28 px round action (Send, Queue, Stop, round voice), faded when it can't be pressed.
const TOOL = "touch-hit flex size-[30px] shrink-0 items-center justify-center rounded-full border border-transparent transition-colors duration-150 hover:border-line-strong hover:bg-hover aria-expanded:border-line-strong aria-expanded:bg-hover";
const ROUND = "touch-hit flex size-7 shrink-0 items-center justify-center rounded-full bg-ink text-surface transition-[opacity,transform] duration-150 enabled:hover:opacity-90 enabled:active:scale-[0.94] disabled:opacity-40";

/** Stop in the round button's shell: a small square, a spinner while the stop is on its way. */
function StopButton({ stopping, onStop }: { stopping?: boolean; onStop: () => void }) {
  return (
    <button
      type="button"
      data-testid="composer-stop"
      aria-label={stopping ? t("chat-composer.stopping") : t("chat-composer.interrupt")}
      title={stopping ? t("chat-composer.stopping") : t("chat-composer.interrupt_hint")}
      onClick={onStop}
      disabled={stopping}
      className={ROUND}
      style={{ animation: `pop-in 200ms ${EASE} both` }}
    >
      {stopping ? <Spinner size={12} /> : <span aria-hidden className="size-2.5 rounded-[1.5px] bg-current" />}
    </button>
  );
}

/** The draft full screen on a phone (#5688): collapse at the top right, the text filling the visible height, send at the
 *  bottom right. It edits the box's own draft; closing hands back the caret so the box picks up where this left off. */
function ExpandedComposer({ value, placeholder, selection, onChange, onCollapse, canSend, onSend }: {
  value: string;
  placeholder: string;
  selection: [number, number];
  onChange: (v: string) => void;
  onCollapse: (sel: [number, number]) => void;
  canSend: boolean;
  onSend: (sel: [number, number]) => void;
}) {
  const panel = useRef<HTMLDivElement>(null);
  const field = useRef<HTMLTextAreaElement>(null);
  const start = useRef(selection);
  useModalFocus(panel);
  useLayoutEffect(() => {
    const el = field.current;
    if (!el) return;
    el.focus({ preventScroll: true });
    el.setSelectionRange(start.current[0], start.current[1]);
  }, []);
  const sel = (): [number, number] => [field.current?.selectionStart ?? value.length, field.current?.selectionEnd ?? value.length];
  return (
    <div
      ref={panel}
      role="dialog"
      aria-modal="true"
      aria-label={t("chat-composer.message")}
      tabIndex={-1}
      data-testid="composer-expanded"
      className="fixed inset-x-0 z-50 flex flex-col bg-surface outline-none"
      style={{ top: "var(--kb-top)", height: "var(--kb-h)", animation: `fade-in 160ms ease-out both` }}
      onKeyDown={(e) => {
        if (e.key === "Escape" && !e.nativeEvent.isComposing) {
          e.preventDefault();
          e.stopPropagation();
          onCollapse(sel());
        }
      }}
    >
      <div className="flex justify-end px-2 pt-[max(env(safe-area-inset-top),8px)]">
        <button
          type="button"
          data-testid="composer-collapse"
          aria-label={t("chat-composer.collapse")}
          title={t("chat-composer.collapse")}
          onClick={() => onCollapse(sel())}
          className="touch-hit flex size-9 items-center justify-center rounded-[10px] text-ink-2 transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="collapse" size={18} stroke={2} />
        </button>
      </div>
      <textarea
        ref={field}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={t("chat-composer.message")}
        className="min-h-0 w-full flex-1 resize-none bg-transparent px-5 py-1 text-[16px] leading-[24px] text-ink outline-none [overflow-wrap:anywhere] placeholder:text-ink-3"
      />
      <div className="flex justify-end px-3 pt-2 pb-[max(env(safe-area-inset-bottom),12px)]">
        <button
          type="button"
          data-testid="composer-expanded-send"
          aria-label={t("chat-composer.send")}
          disabled={!canSend}
          onClick={() => onSend(sel())}
          className="touch-hit flex size-9 shrink-0 items-center justify-center rounded-full transition-[background-color,color,transform] duration-200 enabled:active:scale-[0.94]"
          style={{ background: canSend ? "var(--ink)" : "var(--line-strong)", color: canSend ? "var(--surface)" : "var(--ink-2)" }}
        >
          <Icon name="up" size={18} stroke={2.4} />
        </button>
      </div>
    </div>
  );
}

/** The suggested next message over the box (iOS SuggestionPill): the row fills it in, the arrow sends it as it is; a
 *  risky one (no onSend) has no arrow (#4644). */
function SuggestionPill({ text, onFill, onSend }: { text: string; onFill: () => void; onSend?: () => void }) {
  return (
    <div data-testid="composer-suggestion" className="mb-2 flex items-center gap-1 rounded-full bg-surface p-1 pl-1 shadow-card" style={{ animation: `fade-up 200ms ${EASE} both` }}>
      <button
        type="button"
        onMouseDown={(e) => e.preventDefault()}
        onClick={onFill}
        aria-label={t("chat-composer.suggestion_label", { suggestion: text })}
        title={hasFinePointer() ? t("chat-composer.suggestion_use_hint") : undefined}
        className="touch-hit flex min-w-0 flex-1 items-center gap-2 rounded-full py-1 pr-1 pl-2.5 text-left transition-colors hover:bg-hover"
      >
        <Icon name="sparkles" size={13} stroke={2} className="shrink-0 text-accent" />
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-2">{text}</span>
        <span className="shrink-0 whitespace-nowrap text-[12px] font-medium text-accent">{t("chat-composer.suggestion_use")}</span>
      </button>
      {onSend && (
        <button
          type="button"
          onMouseDown={(e) => e.preventDefault()}
          onClick={onSend}
          aria-label={t("chat-composer.suggestion_send")}
          title={t("chat-composer.suggestion_send")}
          className="touch-hit flex size-7 shrink-0 items-center justify-center rounded-full text-ink-3 transition-[background-color,color,transform] duration-150 hover:bg-hover hover:text-ink active:scale-[0.94]"
        >
          <Icon name="send" size={14} stroke={2} />
        </button>
      )}
    </div>
  );
}

/** Reports the blur a focused field misses when it is disabled (browsers send none). */
function DisabledBlur({ disabled, focused, onBlur }: { disabled: boolean; focused: boolean; onBlur: () => void }) {
  const blur = useRef(onBlur);
  blur.current = onBlur;
  useEffect(() => {
    if (disabled && focused) blur.current();
  }, [disabled, focused]);
  return null;
}
