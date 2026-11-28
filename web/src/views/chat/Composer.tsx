import { conversationDeletionScope, conversationKeyDeleted } from "../../lib/conversation-deleted";
import { storedValue } from "../../lib/compatibility";
// Beautiful UI's Prompt Bar, wired to a session: multi-line input, / commands, @ files and sessions, image attachments
// (paste, drop, pick; click one to mark it up), files uploaded to the session's machine, send — and stop while the
// agent works and nothing is typed. Quotes, edits and edited images from the chat arrive as draft events (drafts.ts).
// Its bottom row is cindy's toolbar (#5324, at the owner's request): a bare round + that opens the + / @ panel
// (ComposerPanel.tsx) and the `leading` permission picker on the left; the model pill, a second Stop while the agent
// works and something is typed, a module's action (core/module-host.ts composer.trailingAction) and one round Send / Queue / Stop button on the right.
import { memo, Suspense, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, type Ref } from "react";
import { BUSY_MOTION, spinStyle } from "../../lib/busy-motion";
import { useQuickPrompts } from "../../lib/extension-declared";
import { promptCommands, type ComposerCommand as Command } from "../../lib/extension-prompts";
import { useSavedPromptRows } from "../../lib/saved-prompt-rows";
import { LOOP_TEXT, loopHint } from "../../lib/loop-command";
import { WORKFLOWS_COMMAND } from "../../lib/background-panel";
import { credentialOwner } from "../../lib/api";
import { baseName, uuid } from "../../lib/format";
import { lazyPage } from "../../lib/lazy-page";
import { enterSends, hasFinePointer, useMedia } from "../../lib/viewport";
import { focusTaken } from "../../lib/dock-focus";
import { useComposition } from "../../lib/ime";
import { COMPACT_COMPOSER, COMPOSER_LINES, lineCount, offersExpand } from "./composer-grow";
import { INPUT_MAX } from "./dock";
import { imageLimitsFor, pictureRoom } from "../../lib/image-limits";
import { filesBusy, readyFiles, restoreFiles, splitFileTags, takeReadyFiles, useComposerFiles, withFiles } from "../../lib/composer-files";
import { fromDraftImage, getComposerImages, moveComposerImages, setComposerImages, takeComposerImages, useComposerImages, type Attachment } from "../../lib/composer-images";
import { activateComposer, addContext, getContexts, newContext, registerComposer, removeContext, useContexts, withContexts, type ContextRef, type ComposerHandle } from "../../lib/links";
import { addQuote, getQuotes, onDraftEvent, parseQuotes, removeQuote, replaceNeedsConfirm, restoreQuotes, sendDraft, setQuotes, splitQuotes, storeDraft, useQuotes, withQuoteBlock, type CommentImagePicker, type DraftEvent, type DraftImage } from "../../lib/drafts";
import type { EditState } from "../../lib/image-edit";
import { mentionAt, type MentionToken } from "../../lib/mentions";
import { skillContext, skillDraft, splitSkillDraft } from "../../lib/skill-mentions";
import { contextXML } from "../../lib/context-xml";
import {
  editorText, inlineMessage, INLINE_TRAILER_START, joinInline, loadInlineMessage, readInlineDraft, sliceInline,
  writeInlineDraft, type InlineDocument, type InlineSelection,
} from "../../lib/inline-mentions";
import { parseUserText } from "../../lib/turns";
import { AttachmentTiles } from "../../ui/AttachmentTray";
import { FileTiles } from "./FileTiles";
import { cx, EASE, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ContextChips, ContextChipList } from "../link/ContextChips";
import { ForwardBars } from "./ForwardCard";
import { ComposerField, preloadInlineField } from "./ComposerField";
import type { ComposerImageStageHandle } from "./ComposerImageStage";
import type { InlineMentionInputHandle } from "./InlineMentionInput";
import { QuoteBars } from "./Quote";
import type { ComposerPanelHandle, MentionRow, MentionSource, PanelPick } from "./ComposerPanel";
import { OverlayBoundary } from "../LazyOverlay";
import type { PlanMode } from "./PermissionPicker";
import { useSendHistory } from "./useSendHistory";
import { permissionsCommand, PERMISSIONS_COMMAND, requestPermissionPicker } from "../../lib/permissions-command";
import { hasSlashSkillQuery, openPickerCommand, rankCommands } from "../../lib/composer-commands";
import { escapeCancelsEdit, escapeCountsForEdit, upStartsEdit, type EditLastBox } from "../../lib/edit-last-key";
import { t } from "../../i18n";
import type { ComposerAction } from "../../core/module-host";

// The + / @ panel loads on picker intent.
const ComposerPanel = lazyPage(() => import("./ComposerPanel").then((m) => m.ComposerPanel));
const AssistantComposerPanel = lazyPage(() => import("./AssistantComposerPanel").then((m) => m.AssistantComposerPanel));
const loadComposerOverlays = () => import("./ComposerOverlays");
const ComposerOverlay = lazyPage(() => loadComposerOverlays().then((m) => m.ComposerOverlay));
const preloadComposerOverlay = () => void ComposerOverlay.preload().catch(() => {});
const SlashSkillEntry = lazyPage(() => import("./SlashSkillView").then((m) => m.SlashSkillEntry));
// Skill-aware history labels are needed only when recall search opens, not on the first screen.
const HistorySearch = lazyPage(() => import("./HistorySearch").then((m) => m.HistorySearch));
// The shared picker shell is already idle-preloaded with @. Load slash-specific rows when the user types /.
const SlashPalette = lazyPage(() => import("./SlashPalette").then((m) => m.SlashPalette));
/** How long "Not sent yet" stays under the box after Enter was pressed while blocked. */
const NUDGE_MS = 4000;

export type { Attachment } from "../../lib/composer-images";

export interface Outgoing {
  text: string;
  images: { mime: string; data: string }[];
  /** Ctrl/⌘+Enter hands the message over immediately through the queue's Send now action. */
  sendNow?: boolean;
  /** A real Quote action, not typed Markdown, a comment or an edit's restored quote (#10346): keep an earlier reader's
   *  place; already at the end, the send follows as usual. UI-only, never part of the hub's wire body. */
  quoteReply?: boolean;
  /** Confirmed refusals return a message with comment images to the composer. UI-only. */
  restoreCommentDraft?: boolean;
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

interface PastedBlock {
  id: string;
  text: string;
}

interface ConfirmState {
  word: string;
  message: string;
  onConfirm: () => void;
}

const PASTE_CHIP_CHARS = 800;
const PASTE_CHIP_LINES = 12;
const ENTRY_DRAG_TYPE = "application/x-persona-file";

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

function joinDrafts(first: InlineDocument, second: InlineDocument): InlineDocument {
  return first.length && second.length ? joinInline(first, ["\n"], second) : first.length ? first : second;
}

// memo: a chat renders again for each transcript item, and this box takes none of them (ChatView keeps its props stable)
export const Composer = memo(function Composer({
  draftKey,
  commentImagesRef,
  editor,
  placeholder,
  machineId,
  agent,
  uploadDest,
  working,
  needsYou,
  disabled,
  commands = AGENT_COMMANDS,
  onSend = async () => false,
  onStop,
  onSendNow,
  onEditLastMessage,
  editingLast,
  loadingDraft,
  stopping,
  autoFocus,
  carryDraft,
  footer,
  leading,
  trailing,
  suggestion,
  suggestionRisky,
  mention,
  planMode,
  onFocusChange,
  blocked,
  action,
  top,
}: {
  draftKey: string;
  commentImagesRef?: Ref<CommentImagePicker | null>;
  /** Prompt editing shares the draft document format and keeps its own wire serialization. */
  editor?: { text: string; refs: ContextRef[]; onChange: (text: string, refs: ContextRef[]) => void; label: string };
  placeholder: string;
  machineId?: string;
  /** The session's agent (Session.agent): which of the hub's image limits its messages are held to (lib/image-limits.ts);
   *  none for a conversation not started yet. */
  agent?: string;
  /** Where a file sent to machineId lands: "health" keeps it in the health space's reports/ (docs/assistant/health.md "Checkup reports"). */
  uploadDest?: "health";
  working?: boolean;
  /** Waits on a question, plan or approval card: no suggestion pill then (iOS status.isActive). */
  needsYou?: boolean;
  disabled?: boolean;
  commands?: Command[];
  /** False: it did not go and nothing shows it (the chat keeps one that failed as a failed bubble, true), so the words go
   *  back into the box. */
  onSend?: (m: Outgoing) => Promise<boolean>;
  onStop?: () => void;
  /** Ctrl/⌘+Enter while the agent works or waits: after the message is queued, hands the queue over now (the queue panel's
   *  Send now). Absent where the agent cannot take messages now; Ctrl/⌘+Enter then queues as Enter does. */
  onSendNow?: () => void | Promise<void>;
  /** Starts editing the last Claude prompt in this box (↑ in it while empty, Esc twice; #4503, #2945). */
  onEditLastMessage?: () => void;
  /** Set while the box edits the last message: a bar says so, Esc or × cancels, and a send rewinds first. */
  editingLast?: EditingLast;
  /** A rewind is fetching the draft: keep receiving draft events but prevent typing or sending until it finishes. */
  loadingDraft?: boolean;
  /** A stop is on its way: the button shows it and takes no more presses. */
  stopping?: boolean;
  autoFocus?: boolean;
  /** A new `draftKey` can be the same conversation at a newly known address (the assistant's chat, whose computer is
   *  read after a cached first view; PersonaAssistant): the words and pictures in the box stay rather than being swapped. */
  carryDraft?: boolean;
  /** In the bottom row's flexible middle (no chat passes one since #5324: the source and folder are under the box). */
  footer?: React.ReactNode;
  /** Right after the +: an Agent's or a Persona conversation's permission picker (#5324, PermissionPicker.tsx). Keep it the
   *  same object while nothing in it changes, as `trailing`. */
  leading?: React.ReactNode;
  /** At the right of the bottom row, before the action / Stop / Send: an Agent's model pill (#5032, ModelPill.tsx). It sizes
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
  /** A module's action (core/module-host.ts `composer.trailingAction`, ChatView useComposerAction): a primary one takes
   *  the send button's place, round, with an empty box and an idle agent; otherwise it is a plain icon before send / stop. */
  action?: ComposerAction;
  /** First in the card, flush with its top and edges, a hairline under it: the chat's queued messages (QueuePanel.tsx,
   *  cindy's pending queue, #5327). Keep it the same object while nothing in it changes, as `footer`. */
  top?: React.ReactNode;
}) {
  // Deferred saves and failed sends still belong to this account and conversation after unmount.
  const canWriteDraft = useMemo(() => {
    const owner = credentialOwner(), scope = conversationDeletionScope();
    return () => owner === credentialOwner() && scope === conversationDeletionScope() && !conversationKeyDeleted(draftKey);
  }, [draftKey]);
  const [chatDraft, setDraft] = useState<InlineDocument>(() => editor ? [] : readInlineDraft(splitSkillDraft(storedValue(`draft:${draftKey}`) ?? "").text));
  const promptDoc = useMemo(() => readInlineDraft(editor?.text ?? ""), [editor?.text]);
  const doc = editor ? promptDoc : chatDraft;
  const draft = useMemo(() => editorText(doc), [doc]);
  const hasInline = doc.some((part) => typeof part !== "string");
  const editingPrompt = Boolean(editor);
  const atts = useComposerImages(editingPrompt ? null : draftKey);
  // The suggestion just sent with its arrow: it stays on the hub until the agent starts, and each tap would be a new message.
  const [sentPill, setSentPill] = useState<string | null>(null);
  useEffect(() => {
    if (working) setSentPill(null);
  }, [working]);
  // "Quote" and comments (lib/comments.ts): bars above the input, sent as `> ` blocks in front of the message (lib/drafts.ts)
  const quotes = useQuotes(draftKey).list;
  // files picked for this conversation (lib/composer-files.ts, #8361): tiles above the input, uploading or there
  const files = useComposerFiles(draftKey);
  const uploading = files.some((f) => f.state === "waiting" || f.state === "uploading");
  const filesReady = files.some((f) => f.file);
  const [pastedBlocks, setPastedBlocks] = useState<PastedBlock[]>([]);
  const [confirmState, setConfirmState] = useState<ConfirmState | null>(null);
  const [dismissed, setDismissed] = useState(false);
  const [active, setActive] = useState(0);
  const [dragging, setDragging] = useState(false);
  const [caret, setCaret] = useState(0);
  const [focused, setFocused] = useState(false);
  // the + panel (#5324): open while its search box has the keys
  const [plusOpen, setPlusOpen] = useState(false);
  const panel = useRef<ComposerPanelHandle>(null);
  const [imagePreview, setImagePreview] = useState<{ id: string; closing?: boolean } | null>(null);
  const imageExpanded = Boolean(imagePreview && !imagePreview.closing);
  const dockSlot = useRef<HTMLDivElement>(null);
  const stageHandle = useRef<ComposerImageStageHandle | null>(null);
  // Enter while blocked (#4480): a line says why nothing went, the button and the card light up; it fades after a while
  const [nudge, setNudge] = useState(0);
  useEffect(() => {
    if (!nudge) return;
    const timer = setTimeout(() => setNudge(0), NUDGE_MS);
    return () => clearTimeout(timer);
  }, [nudge]);
  const nudgeBlocked = (repeat?: boolean) => {
    setNudge((n) => n + 1);
    // held down, the key repeats: one fresh ask, not one per repeat
    if (!repeat) blocked?.onNudge?.();
  };
  const input = useRef<InlineMentionInputHandle>(null);
  // an input method's composition (CJK): its Enter picks a candidate, and "/" or "@" typed in it opens no menu yet
  const ime = useComposition();
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
  const chatContexts = useContexts(draftKey);
  const contexts = editor?.refs ?? chatContexts;
  const changeDocument = (value: InlineDocument) => editor ? editor.onChange(writeInlineDraft(value), contexts) : setDraft(value);
  const changeText = (text: string) => changeDocument(readInlineDraft(text));
  const restoreSkills = (text: string) => {
    const split = splitSkillDraft(text);
    getContexts(draftKey).filter((r) => r.kind === "skill").forEach((r) => removeContext(draftKey, r.id));
    split.skills.forEach((r) => addContext(draftKey, r));
    return readInlineDraft(split.text);
  };
  const skillEnvironment = editingPrompt && !mention ? "" : JSON.stringify([machineId, mention?.cwd, mention?.skills]);
  const catalogMemory = useMemo<import("./useSkillCatalog").CatalogMemory>(() => ({}), [skillEnvironment, draftKey]);
  const previousSkillEnvironment = useRef(skillEnvironment);
  useEffect(() => {
    setActive(0);
    if (editingPrompt && !skillEnvironment) return;
    if (previousSkillEnvironment.current && previousSkillEnvironment.current !== skillEnvironment && [...contexts, ...doc].some((part) => typeof part !== "string" && part.kind === "skill")) {
      toast(t("chat-composer.skills_changed"), "error");
    }
    previousSkillEnvironment.current = skillEnvironment;
  }, [skillEnvironment, draftKey]);
  const sendNow = useRef<(text: string) => Promise<boolean>>(async () => false);
  sendNow.current = async (text) => (disabled || blocked || loadingDraft ? false : onSend({ text, images: [] }));
  const canTake = useRef(!disabled);
  canTake.current = !disabled && !blocked;
  const handle = useMemo<ComposerHandle>(() => ({ key: draftKey, send: (text) => sendNow.current(text), focus: () => input.current?.focus({ preventScroll: true }), ready: () => canTake.current }), [draftKey]);
  useEffect(() => editor ? undefined : registerComposer(handle), [handle, editingPrompt]);

  const quote = quotes.length > 0;
  // pictures being read for this box, counted against a message's pictures before they land (their size unknown yet)
  const reading = useRef(0);
  const commentReads = useMemo(() => new Set<object>(), [draftKey]);
  const limits = () => imageLimitsFor(agent);
  // whether a picture of `size` base64 characters fits beside those in the box and those being read (#12973)
  const roomForImage = (size: number) => {
    const comments = getQuotes(draftKey).list.flatMap((q) => q.images ?? []);
    const held = [...getComposerImages(draftKey), ...comments].map((a) => a.data.length);
    return pictureRoom([...held, ...Array<number>(reading.current + commentReads.size).fill(0)], size, limits());
  };
  const imageRoomError = (why: "count" | "bytes") =>
    void loadComposerOverlays().then((m) => m.showImageRoomError(why, limits()));
  const imageTarget = { draftKey, machineId, uploadDest, limits };
  const appendImage = (a: Attachment) => {
    preloadComposerOverlay();
    setComposerImages(draftKey, [...getComposerImages(draftKey), a]);
    setImagePreview((cur) => (cur && !cur.closing ? { ...cur, id: a.id } : cur));
  };
  const closeImagePreview = () => {
    setImagePreview((cur) => (cur && ComposerOverlay.ready() ? { ...cur, closing: true } : null));
  };
  const openImagePreview = (id: string) => {
    preloadComposerOverlay();
    setExpanded(false);
    if (stageHandle.current && imageExpanded) stageHandle.current.select(id);
    else setImagePreview({ id });
  };
  const removeAttachment = (id: string) => {
    if (!canWriteDraft()) return;
    const images = getComposerImages(draftKey);
    const idx = images.findIndex((a) => a.id === id);
    if (idx < 0) return;
    const next = images.filter((a) => a.id !== id);
    setComposerImages(draftKey, next);
    setImagePreview((cur) => {
      if (!cur) return null;
      if (!next.length) return ComposerOverlay.ready() ? { ...cur, closing: true } : null;
      if (cur.id !== id) return cur;
      return { ...cur, id: next[Math.min(Math.max(idx, 0), next.length - 1)].id };
    });
  };
  const updateAttachment = (id: string, img: DraftImage, state: EditState) => {
    if (!canWriteDraft()) return;
    const images = getComposerImages(draftKey);
    const old = images.find((a) => a.id === id);
    if (!old) return;
    // the picture as it came stays the source (the first edit's tile URL, kept); the earlier result goes
    const source = old.edited?.source ?? old.url;
    const updated = fromDraftImage(img, old.id, { source, ...state }, old.original);
    setComposerImages(draftKey, images.map((a) => (a.id === id ? updated : a)));
  };
  const keyNow = useRef(draftKey);
  keyNow.current = draftKey;
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  // a different conversation: its own draft
  const shownKey = useRef(draftKey);
  useEffect(() => {
    if (editingPrompt) return;
    const from = shownKey.current;
    shownKey.current = draftKey;
    if (carryDraft && from !== draftKey && (doc.length || getComposerImages(from).length)) {
      // the same conversation re-addressed: what is being typed stays, saved under its new key only
      storeDraft(draftKey, skillDraft(getContexts(from), writeInlineDraft(doc)));
      storeDraft(from, "");
      moveComposerImages(from, draftKey);
      return;
    }
    setDraft(restoreSkills(storedValue(`draft:${draftKey}`) ?? ""));
    setPlusOpen(false);
    setImagePreview(null);
    setPastedBlocks([]);
    // not on a touch screen alone: there the focus brings the keyboard up over the conversation just opened. Nor when
    // the person already went to another field or a dialog by then.
    if (!autoFocus || !hasFinePointer()) return;
    const timer = setTimeout(() => {
      const el = input.current;
      if (el && !focusTaken(document.activeElement, el.element)) el.focus({ preventScroll: true });
    }, 0);
    return () => clearTimeout(timer);
  }, [draftKey, autoFocus]);

  // "Quote" / "Edit" / a queued message pulled back / an edited picture, from the chat (lib/drafts.ts)
  const draftNow = useRef(doc);
  draftNow.current = doc;
  useEffect(() => {
    if (editingPrompt) return;
    let lastReplace = 0;
    function applyEvent(e: DraftEvent) {
      if (!canWriteDraft()) return;
      // a send from an earlier box of this conversation failed: its words come back even while sending is off
      if (e.type === "unsent") {
        const split = splitSkillDraft(e.text);
        split.skills.forEach((r) => addContext(draftKey, r));
        setDraft((cur) => joinDrafts(readInlineDraft(split.text), cur));
        return;
      }
      if (!canTake.current) return;
      if (e.type === "image") {
        const room = roomForImage(e.image.data.length);
        if (room !== "ok") {
          void loadComposerOverlays().then((m) => m.spillDraftImage(imageTarget, e.image, room, canWriteDraft));
          return;
        }
        appendImage(fromDraftImage(e.image, undefined, e.edited));
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
      // "Edit" a message sent with files: they go back to their tiles (#8361), the words to the input
      const sentFiles = e.type === "replace" ? splitFileTags(e.text) : null;
      const overflow = sentFiles ? restoreFiles(draftKey, sentFiles.files) : [];
      // "Edit" a message sent with a quote: the quote goes back to the bar, the words to the input
      const selected = sentFiles ? splitSkillDraft(withFiles(sentFiles.text, overflow)) : null;
      const parsed = sentFiles?.text.includes(INLINE_TRAILER_START) ? parseUserText(sentFiles.text) : null;
      const restored = parsed?.inlineTrailer && inlineMessage()?.readInlineBody(parsed.text + parsed.inlineTrailer, parsed.contexts);
      const replaced = restored ? joinDrafts(restored.document, readInlineDraft(withFiles("", overflow))) : readInlineDraft(selected?.text ?? "");
      const replacementText = editorText(replaced);
      const split = e.type === "replace" ? splitQuotes(replacementText) : null;
      const nextQuotes = e.type === "replace" ? (split ? parseQuotes(split.quotes) : []) : null;
      const next = split ? sliceInline(replaced, replacementText.length - split.rest.length) : selected ? replaced : joinDrafts(draftNow.current, readInlineDraft(e.text));
      // "Edit" or a rewind over unsent words, quotes or comments puts the message in at once, with no question first:
      // the notice's Undo brings back what was typed, the quotes on the bar and where the caret was (#2450). An empty
      // bar, or one that would send the same blocks, loses nothing.
      const before = getQuotes(draftKey).list;
      const dropsQuotes = before.length > 0 && withQuoteBlock(before, "") !== withQuoteBlock(nextQuotes ?? [], "");
      const kept =
        nextQuotes && (replaceNeedsConfirm(writeInlineDraft(draftNow.current), writeInlineDraft(next)) || dropsQuotes || getContexts(draftKey).some((r) => r.kind === "skill"))
          ? { text: writeInlineDraft(draftNow.current), skills: getContexts(draftKey).filter((r) => r.kind === "skill"), quotes: before, caret: input.current?.selection()[0] ?? editorText(draftNow.current).length }
          : null;
      if (kept) {
        toast(t("chat-composer.replaced"), "ok", {
          label: t("chat-composer.undo"),
          run: () => {
            if (!canWriteDraft()) return;
            if (startsEdit) editNow.current?.cancel();
            setQuotes(draftKey, kept.quotes);
            const previous = restoreSkills(skillDraft(kept.skills, kept.text));
            // the box went away or shows another conversation: the words go back to this one's stored draft
            if (!mounted.current || keyNow.current !== draftKey) {
              storeDraft(draftKey, skillDraft(kept.skills, kept.text));
              return;
            }
            setDraft(previous);
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
      if (restored && parsed) {
        restoreSkills("");
        for (const ref of parsed.contexts) if (!restored.used.has(ref)) {
          addContext(draftKey, { ...newContext(ref.kind as ContextRef["kind"], ref.title ?? "", ref.url ?? "", ref.text), attrs: ref.attrs });
        }
      } else if (selected) restoreSkills(skillDraft(selected.skills, selected.text));
      setDraft(next);
      setDismissed(false);
      requestAnimationFrame(() => {
        const el = input.current;
        if (!el) return;
        el.focus({ preventScroll: true });
        const end = editorText(next).length;
        el.setSelectionRange(end, end);
        setCaret(end);
      });
    }
    const unsubscribe = onDraftEvent(draftKey, (e) => {
      if (e.type !== "replace") return applyEvent(e);
      const request = ++lastReplace;
      if (!e.text.includes(INLINE_TRAILER_START) || inlineMessage()) return applyEvent(e);
      void loadInlineMessage().then(() => {
        if (request === lastReplace) applyEvent(e);
      }, () => {
        if (request !== lastReplace || !canWriteDraft()) return;
        if (e.edit) editNow.current?.cancel();
        toast(t("chat-composer.reference_load_failed"), "error");
      });
    });
    return () => { lastReplace++; unsubscribe(); };
  }, [draftKey, canWriteDraft, editingPrompt]);

  // What is typed goes into the conversation's stored draft 250 ms after the last key; one still waiting is written at
  // once when the box goes away or moves to another conversation, and when the page is hidden (closed, reloaded,
  // backgrounded or the screen locked — mobile WebViews often skip pagehide there), so the last words typed before
  // switching are not lost (#4591).
  const unsaved = useRef<{ key: string; text: string; current: () => boolean } | null>(null);
  const saveDraft = useCallback(() => {
    const u = unsaved.current;
    if (!u) return;
    unsaved.current = null;
    if (u.current()) storeDraft(u.key, skillDraft(getContexts(u.key), u.text));
  }, []);
  // The render that moves the box to another conversation still holds the old words: they are not that one's draft
  // (the box reads its own next, and this runs again if it differs).
  const savedFor = useRef(draftKey);
  useEffect(() => {
    if (editingPrompt) return;
    if (savedFor.current !== draftKey) {
      savedFor.current = draftKey;
      return;
    }
    unsaved.current = { key: draftKey, text: writeInlineDraft(doc), current: canWriteDraft };
    const t = setTimeout(saveDraft, 250);
    return () => clearTimeout(t);
  }, [doc, draftKey, saveDraft, contexts, canWriteDraft, editingPrompt]);
  useEffect(() => {
    // visibilitychange to hidden reaches mobile WebViews that never fire pagehide; saveDraft is a no-op without unsaved words
    const flushOnHidden = () => {
      if (document.visibilityState === "hidden") saveDraft();
    };
    addEventListener("pagehide", saveDraft);
    document.addEventListener("visibilitychange", flushOnHidden);
    return () => {
      removeEventListener("pagehide", saveDraft);
      document.removeEventListener("visibilitychange", flushOnHidden);
      // only this conversation's: on a switch the box still holds the old words under the new key for one render
      if (unsaved.current?.key === draftKey) saveDraft();
    };
  }, [draftKey, saveDraft]);

  // the words the box was last measured with: while it only grows past the cap, it is not measured again
  const sizedFor = useRef({ draft: "", compact, imageExpanded });
  useLayoutEffect(() => {
    const el = input.current?.element;
    if (!el) return;
    const was = sizedFor.current;
    const breaks = (text: string) => text.split("\n").length;
    const grows = was.compact === compact && was.imageExpanded === imageExpanded && draft.length >= was.draft.length && breaks(draft) >= breaks(was.draft);
    // Mid-composition, or past the six-line cap and only growing, the box is not collapsed to measure: its scrollHeight
    // already covers the words, and a collapse would throw its scroll to the top and make the IME's candidates flicker.
    // It shrinks when the composition ends (this runs again then).
    if (ime.composing || (grow.over && grows)) {
      const h = el.scrollHeight;
      if (h > el.clientHeight) el.style.height = `${h}px`;
      return;
    }
    // Measuring collapses the box to 0 for one layout: its parent holds its height meanwhile. Otherwise the chat above
    // grows by the box's height for that layout, the browser clamps the chat's scroll offset to the taller view, and
    // with the box back the chat sits that much above its newest line (#5375). Under the cap the chat's own
    // ResizeObserver brought the end back; at the cap the chat's height no longer changes, so every key left it there.
    const holder = el.parentElement;
    const held = holder?.style.minHeight ?? "";
    if (holder) holder.style.minHeight = `${holder.getBoundingClientRect().height}px`;
    sizedFor.current = { draft, compact, imageExpanded };
    // the collapse resets the box's own scroll: put it back, or a long draft jumps to its first line on every key
    const top = el.scrollTop;
    el.style.height = "0px";
    const h = el.scrollHeight;
    // Both widths use the measured six-line cap before paint; CSS leaves room for Send in very short viewports.
    // Longer drafts scroll inside the same native textarea.
    el.style.height = `${Math.max(h, 24)}px`;
    el.scrollTop = top;
    if (holder) holder.style.minHeight = held;
    const css = getComputedStyle(el);
    const line = parseFloat(css.lineHeight) || 24;
    const pad = (parseFloat(css.paddingTop) || 0) + (parseFloat(css.paddingBottom) || 0);
    const cap = COMPOSER_LINES * line + pad;
    const over = offersExpand(lineCount(h - pad, line), COMPOSER_LINES);
    setGrow((g) => (g.cap === cap && g.over === over ? g : { cap, over }));
  }, [doc, compact, grow.over, ime.composing, imageExpanded]);
  const showExpand = compact && grow.over && !disabled && !imagePreview;
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
  const query = dismissed || hasInline ? null : parseSlash(draft);
  // A /loop draft gets a line under the box saying what it will do (only where the menu offers /loop: Claude agents).
  const loop = commands.some((c) => c.text === LOOP_TEXT) ? loopHint(draft) : null;
  // Keep bare "/" focused on commands; a non-empty "/<keyword>" also matches against the active skill catalog.
  const slashSkillActive = Boolean(mention?.skills) && hasSlashSkillQuery(query);
  // A menu already open stays through a composition; none opens in the middle of one (it would cover the candidates).
  const menusShown = useRef({ slash: false, at: false });
  const slashOpen = !disabled && query !== null && (allCommands.length > 0 || slashSkillActive) && (!ime.composing || menusShown.current.slash);
  const closeSlash = useCallback(() => setDismissed(true), []);
  const rows = useMemo(() => (slashOpen ? rankCommands(allCommands, query) : []), [slashOpen, query, allCommands]);
  useEffect(() => setActive(0), [query]);

  // @: the token just before the caret (lib/mentions.ts)
  const atToken = !disabled && mention && focused && !dismissed && query === null && caret >= 0 && caret <= draft.length ? mentionAt(draft, caret) : null;
  const token = atToken && (!ime.composing || menusShown.current.at) ? atToken : null;
  const mOpen = Boolean(token);
  menusShown.current = { slash: slashOpen, at: mOpen };
  useEffect(() => {
    const picking = mOpen || slashSkillActive || plusOpen;
    if (picking) preloadInlineField();
    if ((picking || hasInline) && !inlineMessage()) void loadInlineMessage().catch(() => {});
  }, [mOpen, slashSkillActive, plusOpen, hasInline]);

  const trackCaret = ([from, to]: InlineSelection) => setCaret(from === to ? from : -1);

  // Editing the last message (#4503): the words the box got when it began, so an untouched edit counts as the history
  // entry shown (↑ goes on to older messages, ↓ back to an empty box); each of those ends the edit, its quotes with it.
  const historyDraft = useMemo(() => skillDraft(contexts, writeInlineDraft(doc)), [contexts, doc]);
  const editStart = useRef<string | null>(null);
  if (!editingLast) editStart.current = null;
  else if (editStart.current === null) editStart.current = historyDraft;
  const leaveEdit = () => {
    if (!editNow.current) return;
    setQuotes(draftKey, []);
    editNow.current.cancel();
  };
  // ↑ / ↓ recall and Ctrl+R search of what was sent (useSendHistory.ts); desktop only
  const history = useSendHistory({
    draftKey,
    draft: historyDraft,
    editorText: draft,
    setDraft: (text) => {
      leaveEdit();
      setDraft(restoreSkills(text));
    },
    setCaret,
    input,
    disabled: disabled || !!editor,
    editing: editStart.current,
  });
  // Esc or × while editing: the box as it was before, empty; nothing was asked of the computer
  const cancelEdit = () => {
    const edit = editNow.current;
    if (!edit) return;
    setDraft([]);
    draftNow.current = [];
    unsaved.current = null;
    storeDraft(draftKey, "");
    setQuotes(draftKey, []);
    getContexts(draftKey).forEach((r) => removeContext(draftKey, r.id));
    setComposerImages(draftKey, []);
    setImagePreview(null);
    edit.cancel();
    requestAnimationFrame(() => input.current?.focus({ preventScroll: true }));
  };
  // what ↑ and Esc Esc may start an edit from (lib/edit-last-key.ts); the page's dialogs are looked up only for those keys
  const editBox = (): EditLastBox => ({
    offered: Boolean(onEditLastMessage),
    busy: Boolean(working || needsYou || disabled || blocked || loadingDraft),
    holds: Boolean(draft || pastedBlocks.length || atts.length || files.length || reading.current || contexts.length || quotes.length),
    menu: Boolean(slashOpen || mOpen || history.searching || imageExpanded || document.querySelector("[role=dialog],[role=menu]")),
  });

  const replaceAtCaret = (insert: InlineDocument, at: MentionToken | null, space = false) => {
    if (ime.tracker.current.active) return;
    const [from, to] = at ? [at.start, at.end] : input.current?.selection() ?? [draft.length, draft.length];
    const lead = space && (!at || typeof insert[0] !== "string" && insert[0]?.kind === "file") && from > 0 && !/\s/.test(draft[from - 1]) ? " " : "";
    const end = space && draft[to] === " " ? to + 1 : to;
    const addition = joinInline([lead], insert, space ? [" "] : []);
    input.current?.replaceRange([from, end], addition);
  };

  const fileReference = (path: string, title: string): ContextRef => ({
    id: `file:${path}`, kind: "file", title, url: "", text: path,
    attrs: { path, ...(mention?.referenceFiles ?? { ...(machineId ? { machine: machineId } : {}), ...(mention?.cwd ? { cwd: mention.cwd } : {}) }) },
  });

  // Replace the query from @ or /, or use the selection the field kept while the + search had focus.
  const pickRow = (r: MentionRow, at: MentionToken | null) => {
    let ref: ContextRef | undefined;
    if (r.kind === "session") {
      const q = r.toContext();
      ref = newContext("context", q.title, q.url, q.text);
    } else if (r.kind === "skill") {
      ref = skillContext(r.skill);
    } else if (r.kind === "workspace") {
      const w = r.toContext();
      ref = { ...newContext("workspace", w.title, w.url, w.text), attrs: w.attrs };
    } else if (r.kind === "repository") {
      const c = r.context;
      ref = { ...newContext("repository", c.title, c.url, c.text), attrs: c.attrs };
    } else if (r.kind === "file") {
      if (r.dir && mention?.referenceFiles) return;
      ref = fileReference(r.insert.slice(1, r.dir ? undefined : -1), r.name);
    }
    if (ref) replaceAtCaret([ref], at, true);
  };
  // The panel's own rows: the file picker, Plan mode. From a typed @ the token goes first, as cindy's.
  const pickPanel = (p: PanelPick, at: MentionToken | null) => {
    if (p.kind === "browse") return replaceAtCaret([p.insert], at);
    if (p.kind === "row") {
      if (!at) setPlusOpen(false);
      return pickRow(p.row, at);
    }
    if (at) replaceAtCaret([], at);
    else setPlusOpen(false);
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
  // a box turned off takes its + panel and expanded image stage with it
  useEffect(() => {
    if (disabled) {
      setPlusOpen(false);
      setImagePreview(null);
    }
  }, [disabled]);

  // A Files panel entry dropped on the box becomes the same reference as a picker result.
  const dropEntry = (e: React.DragEvent): boolean => {
    const raw = e.dataTransfer.getData(ENTRY_DRAG_TYPE);
    if (!raw) return false;
    if (!disabled && !editor) {
      void loadComposerOverlays().then((m) =>
        m.resolveEntryDrop(raw, { mid: machineId, cwd: mention?.cwd }, (path) =>
          replaceAtCaret([fileReference(path, baseName(path.replace(/\/$/, "")) || path)], null, true),
        ),
      );
    }
    return true;
  };

  const addFiles = async (picked: FileList | File[]) => {
    if (disabled || editor) return;
    const files = Array.from(picked);
    const { addPickedFiles } = await loadComposerOverlays();
    await addPickedFiles(files, imageTarget, {
      current: () => mounted.current && keyNow.current === draftKey && canWriteDraft(),
      reading, roomForImage, attach: appendImage,
    });
  };

  const addCommentImages: CommentImagePicker = async (target, files, signal) => {
    if (disabled || editor || loadingDraft) return;
    if (files[0]?.type.startsWith("image/")) {
      const room = roomForImage(0);
      if (room !== "ok") {
        imageRoomError(room);
        return;
      }
    }
    const read = {};
    const release = () => commentReads.delete(read);
    commentReads.add(read);
    signal.addEventListener("abort", release, { once: true });
    try {
      const { runCommentImagePicker } = await loadComposerOverlays();
      await runCommentImagePicker({
        target,
        files,
        signal,
        draftKey,
        limits,
        roomForImage,
        imageRoomError,
        commentReads,
        releaseLoader: release,
        current: (id) => !signal.aborted && mounted.current && keyNow.current === draftKey && canWriteDraft()
          && (!id || getQuotes(draftKey).list.some((q) => q.id === id && q.comment)),
      });
    } finally {
      release();
      signal.removeEventListener("abort", release);
    }
  };
  useImperativeHandle(commentImagesRef, () => addCommentImages);

  const canSend = (draft.trim().length > 0 || pastedBlocks.length > 0 || atts.length > 0 || filesReady || contexts.length > 0 || quote) && !uploading && !disabled && !loadingDraft;
  // Claude's suggested next message (iOS SuggestionPill): while the box is empty and the agent waits for a message
  // (not working, not on a card); a tap fills it
  // in, "Send now" sends it as it is (the node drops it once the agent works again)
  const pill = suggestion && suggestion !== sentPill && !editingLast && !blocked && !draft && !pastedBlocks.length && !atts.length && !contexts.length && !quote && !working && !needsYou && !disabled ? suggestion : null;
  const fillSuggestion = (text: string) => {
    setDraft([text]);
    setDismissed(true);
    setCaret(text.length);
    requestAnimationFrame(() => {
      const el = input.current;
      if (!el) return;
      el.focus({ preventScroll: true });
      el.setSelectionRange(text.length, text.length);
    });
  };
  const typed = Boolean(draft.trim() || pastedBlocks.length || atts.length || files.length || contexts.length || quote);
  // the round button is Stop while the agent works and nothing is typed; with something typed Send queues it and a
  // second Stop sits left of the action (cindy's mainSlotIsStop / showSecondaryStop)
  const showStop = working && !typed && onStop;
  const secondStop = Boolean(working && typed && onStop && !blocked);
  const queues = Boolean(working || needsYou);
  // an empty box and an idle agent: a primary action takes the send button's place (it would be off anyway), as on iOS
  const roundAction = Boolean(action?.primary) && !canSend && !showStop && !blocked && !disabled;

  // `/model` alone opens the model panel (where the agent switches models), `/permissions` alone the permission menu
  // (#6476), from Enter and the send buttons alike. Where the chat has no such picker, the words are sent as typed.
  const pickerCommand = (text: string) => {
    const opened = openPickerCommand(draftKey, text);
    if (opened) setDraft([]);
    return opened;
  };

  const submit = async (value = doc, now = false) => {
    if (!canWriteDraft() || ime.tracker.current.active) return;
    const text = editorText(value);
    if (pickerCommand(text)) return;
    let codec = inlineMessage();
    if (!codec && hasInline) {
      try {
        codec = await loadInlineMessage();
      } catch {
        if (mounted.current && keyNow.current === draftKey && canWriteDraft()) toast(t("chat-composer.reference_load_failed"), "error");
        return;
      }
      if (!mounted.current || keyNow.current !== draftKey || draftNow.current !== value || !canWriteDraft()) return;
    }
    const pendingEdits = stageHandle.current?.flush();
    if (pendingEdits && (!(await pendingEdits) || !mounted.current || keyNow.current !== draftKey || !canWriteDraft())) return;
    const refs = getContexts(draftKey);
    const q = getQuotes(draftKey).list;
    if (commentReads.size) {
      toast(t("chat-comments.preparing_images"));
      return;
    }
    // a file still on its way holds the message, as a video does on the phone (#8361); the words stay in the box
    if (filesBusy(draftKey)) {
      toast(t("chat-composer.files_uploading"));
      return;
    }
    const hasFiles = readyFiles(draftKey).length > 0;
    const sent = getComposerImages(draftKey);
    const sentPasted = pastedBlocks;
    const fullDoc = sentPasted.length ? joinInline([sentPasted.map((b) => b.text).join("\n\n")], text.trim() ? ["\n\n"] : [], text.trim() ? value : []) : value;
    const fullText = codec?.inlineBody(fullDoc) ?? editorText(fullDoc);
    const selected = codec?.inlineReferences(value) ?? [];
    if (editor || disabled || blocked || loadingDraft || (!fullText.trim() && !sent.length && !hasFiles && !refs.length && !q.length)) return;
    const commentImages = q.flatMap((quote) => quote.images ?? []);
    const images = [...sent, ...commentImages].map(({ mime, data }) => ({ mime, data }));
    const lastImage = images.at(-1);
    if (lastImage) {
      const room = pictureRoom(images.slice(0, -1).map((a) => a.data.length), lastImage.data.length, limits());
      if (room !== "ok") {
        imageRoomError(room);
        return;
      }
    }
    // Any message spends the suggestion shown: it stays on the hub until the agent starts (#4644).
    if (suggestion) setSentPill(suggestion);
    // An edit of the last message: the rewind to before the old one first, the box kept as it is (read-only meanwhile).
    // Refused or not possible: nothing changed on the computer, and the edit stays in the box (ChatView says why).
    const edit = editNow.current;
    let committed: Outgoing["edit"];
    if (edit) {
      const done = await edit.commit();
      if (!done || !canWriteDraft()) return;
      if (typeof done === "object") committed = done;
      // the box went away or shows another conversation: the words wait in this one's stored draft
      if (!mounted.current || keyNow.current !== draftKey) return;
    }
    // The message shows at once, so the box empties at once too: waiting for the hub to
    // hand it to the agent (seconds when it starts up) left the same words in both places. A send that never went
    // (onSend false) puts them back; one that failed stays in the chat as a failed bubble.
    const sentFiles = takeReadyFiles(draftKey);
    setDraft([]);
    draftNow.current = [];
    unsaved.current = null;
    storeDraft(draftKey, "");
    const settleImages = takeComposerImages(draftKey, sent);
    closeImagePreview();
    setPastedBlocks([]);
    refs.forEach((r) => removeContext(draftKey, r.id));
    setQuotes(draftKey, []);
    // Ready for the next message now, not when the hub answers (the chat sends them in order). Back to the box only with
    // a precise pointer and if the focus has not gone elsewhere: on a touch screen it would raise the keyboard and fold
    // the waiting cards (dock.ts).
    const at = document.activeElement;
    if (hasFinePointer() && (!at || at === document.body || box.current?.contains(at))) input.current?.focus({ preventScroll: true });
    // picked contexts ride in front as <persona-context> tags (docs/protocol/protocol.md "Tags in messages"), the quote as `> ` lines,
    // files after the words as <persona-file> tags, the words as typed (with none, a line saying to look at them)
    const said = !fullText.trim() && !images.length && sentFiles.length ? t("chat-composer.see_files") : fullText;
    const body = withFiles(withQuoteBlock(q, said, sent.length), sentFiles);
    const quoteReply = !edit && q.some((x) => !x.comment);
    const leading = [...refs.filter((r) => r.kind !== "skill" || !selected.some((s) => s.kind === "skill" && s.attrs?.ref === r.attrs?.ref)),
      ...selected.filter((r) => r.kind !== "file")];
    // Only scheduled prompts can execute leading file selections. Live chats keep their native @path, with the
    // file's display metadata after the prose where promptcontent.Files does not see a selection.
    const liveFiles = selected.filter((r) => r.kind === "file");
    const wire = withContexts(leading, body, images.length > 0) + (liveFiles.length ? `\n\n${liveFiles.map(contextXML).join("\n")}` : "");
    const ok = await onSend({ text: wire, images,
      ...(commentImages.length ? { restoreCommentDraft: true } : {}),
      ...(now && queues ? { sendNow: true } : {}),
      ...(quoteReply ? { quoteReply: true } : {}), ...(committed ? { edit: committed } : {}) }).catch((error) => {
      settleImages(false);
      throw error;
    });
    const current = canWriteDraft();
    settleImages(!ok && current);
    if (!current) return;
    if (ok && now && queues) await onSendNow?.();
    if (ok) {
      history.record(skillDraft(refs, writeInlineDraft(fullDoc)));
    } else {
      // the box closed or shows another conversation now: the words go back to this conversation's draft (its open
      // box, or the stored one). The image store has already restored the pictures to that same draft.
      if (mounted.current && keyNow.current === draftKey) {
        setDraft((cur) => joinDrafts(value, cur));
        if (sentPasted.length) setPastedBlocks((cur) => [...sentPasted, ...cur]);
      } else {
        if (fullDoc.length) sendDraft(draftKey, { type: "unsent", text: writeInlineDraft(fullDoc) });
      }
      refs.forEach((r) => addContext(draftKey, r));
      // the files back above this conversation's box: they are on the machine, ready to go again; ones over the
      // tile limit stay in the draft as tags rather than disappearing
      const overflow = restoreFiles(draftKey, sentFiles);
      if (overflow.length) {
        const restored = withFiles("", overflow);
        if (mounted.current && keyNow.current === draftKey) setDraft((cur) => joinDrafts([restored], cur));
        else sendDraft(draftKey, { type: "unsent", text: restored });
      }
      // the quotes back above this conversation's box (kept for it when the box shows another one or closed)
      restoreQuotes(draftKey, q);
    }
  };

  const pick = (c: Command | MentionRow) => {
    if ("kind" in c) {
      setDismissed(true);
      // The lone /word is the same kind of query as @word.
      pickRow(c, { start: 0, end: draft.length, query: "" });
      return;
    }
    const apply = () => {
      // `/permissions` opens the permission menu at once; where the chat has no picker it goes in the box as any command
      if (c.text === PERMISSIONS_COMMAND && requestPermissionPicker(draftKey)) {
        setDraft([]);
        setDismissed(true);
        return;
      }
      changeText(c.text);
      setDismissed(true);
      input.current?.focus({ preventScroll: true });
    };
    if (c.confirm) {
      preloadComposerOverlay();
      setConfirmState({ word: c.word, message: c.confirm, onConfirm: apply });
      return;
    }
    apply();
  };

  const MentionPanel = mention?.workspaces || mention?.repositories ? AssistantComposerPanel : ComposerPanel;
  const slashProps = {
    rows, active, onHover: setActive, onPick: pick, onClose: closeSlash,
    query, source: mention, machineId, within: box, memory: catalogMemory, ime: ime.tracker,
  };
  const commandPalette = () => <SlashPalette {...slashProps} active={Math.min(active, Math.max(rows.length - 1, 0))} />;
  const innerOverlay = Boolean(slashOpen || mOpen || plusOpen || history.searching || confirmState || expanded);
  const drag = {
    onDragOver: (e: React.DragEvent) => {
      if (!editor && (e.dataTransfer.types.includes("Files") || e.dataTransfer.types.includes(ENTRY_DRAG_TYPE))) {
        e.preventDefault();
        setDragging(true);
      }
    },
    onDragLeave: (e: React.DragEvent) => {
      const r = e.relatedTarget as Node | null;
      if (r && (e.currentTarget.contains(r) || box.current?.contains(r) || (r instanceof Element && r.closest("[data-composer-image-stage]")))) return;
      setDragging(false);
    },
    onDrop: (e: React.DragEvent) => {
      e.preventDefault();
      setDragging(false);
      if (!dropEntry(e)) void addFiles(e.dataTransfer.files);
    },
  };
  return (
    <>
      {imagePreview && (
        <>
          <div ref={dockSlot} aria-hidden="true" data-composer-placeholder="" className="pointer-events-none shrink-0" />
          <Suspense fallback={null}>
            <ComposerOverlay
              imageStage={{
                attachments: atts,
                activeId: imagePreview.id,
                closing: Boolean(imagePreview.closing),
                compact,
                composerRef: box,
                slotRef: dockSlot,
                handleRef: stageHandle,
                innerOverlayOpen: innerOverlay,
                dragging,
                drag,
                onSelect: (id) => setImagePreview((cur) => (cur && !cur.closing ? { ...cur, id } : cur)),
                onUpdateAttachment: updateAttachment,
                onClose: closeImagePreview,
                onCloseComplete: () => setImagePreview((cur) => (cur?.closing ? null : cur)),
              }}
            />
          </Suspense>
        </>
      )}
      <div
        ref={box}
        data-composer-image-expanded={imageExpanded ? "" : undefined}
        className="relative"
        inert={loadingDraft || undefined}
        {...drag}
      >
      {slashOpen && (
        <Suspense fallback={null}>
          {slashSkillActive ? (
            <OverlayBoundary open onClose={closeSlash} fallback={commandPalette()}>
              <Suspense fallback={commandPalette()}>
                <SlashSkillEntry {...slashProps} />
              </Suspense>
            </OverlayBoundary>
          ) : commandPalette()}
        </Suspense>
      )}

      {history.searching && <Suspense fallback={null}><HistorySearch onPick={history.pick} onClose={history.closeSearch} /></Suspense>}

      {token && (
        <Suspense fallback={null}>
          <MentionPanel key={skillEnvironment} memory={catalogMemory} from="at" query={token.query} source={mention} machineId={machineId} attach={disabled || editor ? undefined : machineId ? "files" : "images"} plan={planMode} onPick={(p) => pickPanel(p, token)} onClose={closePlus} handle={panel} />
        </Suspense>
      )}
      {plusOpen && !disabled && (
        <Suspense fallback={null}>
          <MentionPanel key={skillEnvironment} memory={catalogMemory} from="plus" source={mention} machineId={machineId} attach={editor ? undefined : machineId ? "files" : "images"} plan={planMode} onPick={(p) => pickPanel(p, null)} onClose={closePlus} />
        </Suspense>
      )}

      {pill && <Suspense fallback={null}><ComposerOverlay text={pill} onFill={() => fillSuggestion(pill)} onSend={suggestionRisky ? undefined : () => { setSentPill(pill); void submit([pill]); }} /></Suspense>}

      <div
        className={cx(
          // a container: what sits in its bottom row (the model pill) takes its width modes from the card's own width
          "@container/composer relative flex flex-col gap-1.5 rounded-[14px] bg-surface/95 p-1.5 backdrop-blur-md transition-shadow duration-150 focus-within:shadow-[0_0_0_1px_var(--line-strong)]",
          imageExpanded ? "shadow-overlay ring-1 ring-line-strong" : "shadow-card",
          dragging && "ring-2 ring-accent",
        )}
      >
        {dragging && <Suspense fallback={null}><ComposerOverlay dropHint /></Suspense>}
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
        {editor ? <ContextChipList refs={contexts} onRemove={(id) => editor.onChange(editor.text, contexts.filter((r) => r.id !== id))} /> : <ContextChips draftKey={draftKey} />}
        <ForwardBars draftKey={draftKey} onRemove={() => input.current?.focus()} />
        {quote && (
          <QuoteBars
            draftKey={draftKey}
            quotes={quotes}
            onAddCommentImages={disabled || editor || loadingDraft ? undefined : addCommentImages}
            onRemove={(id) => {
              removeQuote(draftKey, id);
              input.current?.focus();
            }}
          />
        )}
        {(atts.length > 0 || files.length > 0) && (
          <div data-composer-attachments className="flex flex-wrap items-center gap-2 px-0.5 pt-0.5 empty:hidden">
            <AttachmentTiles
              attachments={atts}
              activeId={imageExpanded ? imagePreview?.id : undefined}
              keepFocus
              onOpen={openImagePreview}
              onRemove={removeAttachment}
            />
            <FileTiles draftKey={draftKey} files={files} />
          </div>
        )}
        {pastedBlocks.length > 0 && (
          <div className="flex flex-col gap-1.5 px-0.5 pt-0.5">
            <Suspense fallback={null}><ComposerOverlay pastedBlocks={pastedBlocks}
              onRemove={(id) => setPastedBlocks((cur) => cur.filter((block) => block.id !== id))}
              onExpand={(id, text) => {
                changeDocument(joinInline(doc, [draft ? "\n\n" : "", text]));
                setPastedBlocks((cur) => cur.filter((block) => block.id !== id));
                input.current?.focus();
              }} /></Suspense>
          </div>
        )}
        {/* a field that turns disabled loses focus with no blur event: the keyboard is down, say so */}
        <DisabledBlur disabled={Boolean(disabled)} focused={focused} onBlur={() => { setFocused(false); onFocusChange?.(false); }} />
        <div className="relative">
        <ComposerField
          inputRef={input}
          composerInput
          value={doc}
          disabled={disabled}
          {...ime.handlers}
          readOnly={loadingDraft}
          onFocus={() => {
            if (!editor) activateComposer(handle);
            setFocused(true);
            onFocusChange?.(true);
          }}
          onBlur={() => {
            lastEscape.current = null;
            ime.onBlur();
            setFocused(false);
            onFocusChange?.(false);
          }}
          onChange={(next, selection) => {
            if (parseSlash(editorText(next)) !== query) setActive(0);
            changeDocument(next);
            setDismissed(false);
            trackCaret(selection);
          }}
          onSelectionChange={trackCaret}
          onPaste={(e) => {
            if (editor) return;
            const files = Array.from(e.clipboardData.files);
            if (files.length) {
              e.preventDefault();
              void addFiles(files);
              return;
            }
            const text = e.clipboardData.getData("text/plain");
            if (text && (text.length > PASTE_CHIP_CHARS || text.split(/\r?\n/).length > PASTE_CHIP_LINES)) {
              e.preventDefault();
              setPastedBlocks((cur) => [...cur, { id: uuid(), text }]);
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
            // also the Enter macOS Safari sends right after compositionend, which committed the candidate (lib/ime.ts)
            if (ime.busy(e.nativeEvent)) {
              // the IME's own Escape (cancelling a candidate) must not reach ChatView's interrupt on the window
              if (e.key === "Escape") e.stopPropagation();
              return;
            }
            const send = !editor && enterSends(e, hasFinePointer());
            // Mod+Enter sends rather than choosing a slash/@ row; Mod+Alt/Shift falls through to the window unchanged.
            const modEnter = e.ctrlKey || e.metaKey;
            // a plain Enter picks the highlighted row; Shift+Enter or Alt+Enter is always a new line
            const pickEnter = e.key === "Enter" && !modEnter && !e.shiftKey && !e.altKey;
            // `/model` or `/permissions` alone opens its picker, ahead of the slash palette's Enter, which would pick its row
            if (send && pickerCommand(draft)) {
              e.preventDefault();
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
              // ↑ ↓ move in the panel, unmodified Enter / Tab pick its row (none while it is empty or still loading)
              const browseFolder = e.key === "ArrowRight" && !e.shiftKey && !e.altKey && !modEnter && caret === draft.length;
              if ((e.key === "ArrowDown" || e.key === "ArrowUp" || browseFolder || pickEnter || e.key === "Tab") && panel.current?.key(e.key)) {
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
              // The command-only fallback keeps the keys working while the skill chunk is on its way.
              if ((e.key === "ArrowDown" || e.key === "ArrowUp") && rows.length) {
                e.preventDefault();
                setActive((a) => (a + (e.key === "ArrowDown" ? 1 : rows.length - 1)) % rows.length);
                return;
              }
              // with nothing matching, Enter sends the words as they are: the list holds only a few of the agent's commands
              if ((pickEnter || e.key === "Tab") && rows.length) {
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
            if (e.key === "Escape" && imageExpanded && !innerOverlay && !document.querySelector("[role=dialog],[role=menu]")) {
              if (!stageHandle.current) {
                e.preventDefault();
                e.stopPropagation();
                closeImagePreview();
              }
              return;
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
            // Alt+Enter, which sends on a desktop, never sends the half-typed "/com" or "@file" an open menu is completing:
            // the menu's rows take only a plain Enter, and Mod+Enter stays the explicit send
            if (send && (slashOpen || mOpen) && e.altKey && !modEnter) return;
            // a touch keyboard's return is a new line there; the send button sends (a hardware keyboard with a
            // trackpad, an iPad's, sends on Enter)
            if (send) {
              e.preventDefault();
              // any Enter while blocked says why (also an empty box or attachments only): submit() would do nothing
              if (blocked) nudgeBlocked(e.repeat);
              else void submit(doc, modEnter && Boolean(onSendNow));
            }
          }}
          placeholder={pill && hasFinePointer() ? t("chat-composer.suggestion", { suggestion: pill }) : placeholder}
          label={editor?.label ?? t("chat-composer.message")}
          title={!editor && hasFinePointer() ? (onEditLastMessage ? t("composer.history_hint_edit") : t("composer.history_hint")) : undefined}
          style={{
            minHeight: imageExpanded ? (compact ? 44 : 64) : undefined,
            maxHeight: imageExpanded ? "min(240px, calc(var(--kb-h) * 0.28))" : grow.cap ? `min(${grow.cap}px, ${INPUT_MAX})` : INPUT_MAX,
          }}
          className={cx("min-h-[calc(1lh+var(--spacing)*1.5)] w-full resize-none overflow-y-auto bg-transparent px-2 pt-1.5 text-[14px] leading-[21px] text-ink outline-none [@media(pointer:coarse)]:text-[16px] [@media(pointer:coarse)]:leading-[24px] [overflow-wrap:anywhere] placeholder:text-ink-3 disabled:opacity-50 aria-disabled:opacity-50", showExpand && "pr-8")}
        />
        {showExpand && (
          <button
            type="button"
            data-testid="composer-expand"
            aria-label={t("chat-composer.expand")}
            title={t("chat-composer.expand")}
            onPointerDown={preloadComposerOverlay}
            onFocus={preloadComposerOverlay}
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
            disabled={disabled || Boolean(editor && !mention)}
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
          {/* while the agent works and something is typed, Stop stays left of the action and Send queues (cindy's second slot) */}
          {secondStop && <StopButton stopping={stopping} onStop={onStop!} />}
          {action && !roundAction && (
            <button
              type="button"
              data-testid="composer-action"
              {...action.attributes}
              aria-label={action.name}
              title={action.label}
              onClick={action.onClick}
              className={cx(TOOL, action.primary ? "text-ink-2" : "text-ink-3")}
            >
              <Icon name={action.icon} size={15} stroke={2} />
            </button>
          )}
          {editor ? null : roundAction ? (
            <button
              type="button"
              data-testid="composer-action"
              {...action!.attributes}
              aria-label={action!.name}
              title={action!.label}
              onClick={action!.onClick}
              className={ROUND}
              style={{ animation: `pop-in 200ms ${EASE} both` }}
            >
              <Icon name={action!.icon} size={15} stroke={2.2} />
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
              <Icon name="up" size={16} stroke={2.4} />
            </button>
          )}
        </div>
      </div>
      {expanded && (
        <Suspense fallback={null}>
          <ComposerOverlay
            editLabel={editor?.label}
            value={doc}
            placeholder={placeholder}
            selection={input.current?.selection() ?? [draft.length, draft.length]}
            onChange={(v) => {
              changeDocument(v);
              setDismissed(false);
            }}
            onCollapse={(sel) => {
              collapsedSel.current = sel;
              setExpanded(false);
            }}
            canSend={Boolean(canSend) && !blocked}
            blocked={Boolean(blocked)}
            onSend={(sel, repeat) => {
              collapsedSel.current = sel;
              setExpanded(false);
              if (blocked) nudgeBlocked(repeat);
              else void submit();
            }}
          />
        </Suspense>
      )}
      {confirmState && (
        <Suspense fallback={null}>
          <ComposerOverlay confirm={confirmState} onClose={() => setConfirmState(null)} />
        </Suspense>
      )}
      </div>
    </>
  );
});

// The toolbar's two looks (#5324, cindy's): bare 30 px round tools that show the model pill's frame on hover or while open,
// and the one dark 28 px round action (Send, Queue, Stop, a round action), faded when it can't be pressed.
const TOOL = "touch-hit flex size-[30px] shrink-0 items-center justify-center rounded-full border border-transparent transition-colors duration-150 hover:border-line-strong hover:bg-hover aria-expanded:border-line-strong aria-expanded:bg-hover";
const ROUND = "touch-hit flex size-7 shrink-0 items-center justify-center rounded-full bg-ink text-surface transition-[opacity,transform] duration-150 enabled:hover:opacity-90 enabled:active:scale-[0.94] disabled:opacity-40";

/** Stop in the round button's shell: a spinning ring with a small square, a spinner while the stop is on its way. */
function StopButton({ stopping, onStop }: { stopping?: boolean; onStop: () => void }) {
  return (
    <button
      type="button"
      data-testid="composer-stop"
      aria-label={stopping ? t("chat-composer.stopping") : t("chat-composer.interrupt")}
      title={stopping ? t("chat-composer.stopping") : t("chat-composer.interrupt_hint")}
      onClick={onStop}
      disabled={stopping}
      className={cx(ROUND, "relative")}
      style={{ animation: `pop-in 200ms ${EASE} both` }}
    >
      {stopping ? (
        <Spinner size={12} />
      ) : (
        <span className="relative flex size-5 items-center justify-center" aria-hidden="true">
          <svg
            width="20"
            height="20"
            viewBox="0 0 20 20"
            fill="none"
            className={cx("absolute inset-0", BUSY_MOTION)}
            style={spinStyle(1100)}
          >
            <circle cx="10" cy="10" r="8" stroke="currentColor" strokeOpacity="0.28" strokeWidth="1.6" />
            <path d="M10 2a8 8 0 0 1 8 8" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
          </svg>
          <span className="size-1.5 rounded-xs bg-current" />
        </span>
      )}
    </button>
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
