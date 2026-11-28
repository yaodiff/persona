// The inline <head> script vite.config.ts's startup-preload plugin writes into index.html, the brotli copies it
// writes of the files that script and the page load, and its checks of the startup namespaces and the first screen's
// size. It lives here, with no imports of the client code, so the config
// can load it and a test can check it against src/i18n/index.ts: the languages it has catalogs for (LANGS) and how it
// picks one (preferredLanguage) must stay the same as the client's.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import ts from "typescript";
import { brotliCompress, constants, gzip as gzipCompress } from "node:zlib";
import { startupMatcher } from "./i18n/startup-keys";
import { MODULE_SWITCH_KEY } from "./core/module-switch";
import { MODULE_HUB_KEY_PREFIX } from "./core/module-hub-storage";

/** The languages under `dir` (src/i18n/locales): code → the language subtags its _meta.json matches. Build time only. */
export function localeDirs(dir: string): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const d of readdirSync(dir, { withFileTypes: true })) {
    if (!d.isDirectory()) continue;
    try {
      out[d.name] = (JSON.parse(readFileSync(join(dir, d.name, "_meta.json"), "utf8")) as { matches: string[] }).matches;
    } catch {
      // not a language directory
    }
  }
  return out;
}

export interface PreloadFiles {
  shared: string[];
  app: string[];
  /** The modules' pages of their own (core/module-host.ts `standalonePages`), by their address' first segment: the
   *  module (left out while this browser switched it off, core/module-switch.ts) and its standalone.tsx's files. */
  standalone: Record<string, { module: string; files: string[] }>;
  catalogs: Record<string, string>;
  /** language subtag → language code (each _meta.json's matches) */
  matches: Record<string, string>;
}

/** The modules' pages of their own (core/module-host.ts `standalonePages`), read from each src/modules/<id>/module.ts as
 *  eslint.modules.config.mjs reads `needs`: each address segment with its module, whose code is its standalone.tsx. */
export function standalonePages(dir = fileURLToPath(new URL("./modules/", import.meta.url))): { base: string; module: string }[] {
  return readdirSync(dir, { withFileTypes: true })
    .filter((e) => e.isDirectory() && existsSync(join(dir, e.name, "module.ts")))
    .flatMap((e) => {
      const list = /\bstandalonePages:\s*\[([^\]]*)\]/.exec(readFileSync(join(dir, e.name, "module.ts"), "utf8"))?.[1];
      if (list === undefined) return [];
      if (!existsSync(join(dir, e.name, "standalone.tsx"))) throw new Error(`startup-preload: module "${e.name}" names standalonePages but has no standalone.tsx`);
      return [...list.matchAll(/["']([a-z][a-z0-9-]*)["']/g)].map((m) => ({ base: m[1]!, module: e.name }));
    });
}

/** The script: it adds a modulepreload (or style preload) link for the preferred language's catalog and the page's files. */
export function preloadScript(files: PreloadFiles): string {
  return `(function () {
  var files = ${JSON.stringify(files)};
  function add(href) {
    var l = document.createElement("link");
    if (/\\.css$/.test(href)) {
      l.rel = "preload";
      l.as = "style";
    } else l.rel = "modulepreload";
    l.crossOrigin = "";
    l.href = "./" + href;
    document.head.appendChild(l);
  }
  var lang = null;
  try { lang = localStorage.getItem("persona.language"); } catch (e) {}
  if (!Object.prototype.hasOwnProperty.call(files.catalogs, lang)) {
    var tag = (navigator.languages && navigator.languages[0]) || navigator.language || "";
    var primary = tag.trim().replace(/_/g, "-").split("-")[0].toLowerCase();
    lang = Object.prototype.hasOwnProperty.call(files.matches, primary) ? files.matches[primary] : "en";
  }
  var base = new URL(document.baseURI).pathname;
  var path = location.pathname.indexOf(base) === 0 ? location.pathname.slice(base.length - 1) : location.pathname;
  var seg = /^\\/([^/]+)\\/[^/]/.exec(path);
  var own = seg && Object.prototype.hasOwnProperty.call(files.standalone, seg[1]) ? files.standalone[seg[1]] : null;
  if (own) {
    try {
      var off = JSON.parse(localStorage.getItem(${JSON.stringify(MODULE_SWITCH_KEY)}) || "[]");
      if (Array.isArray(off) && off.indexOf(own.module) >= 0) own = null;
    } catch (e) {}
  }
  if (own) {
    try {
      var kept = JSON.parse(localStorage.getItem(${JSON.stringify(MODULE_HUB_KEY_PREFIX)} + location.origin + base.replace(/\\/+$/, "")) || "null");
      var valid = Array.isArray(kept) && kept.every(function (x) { return typeof x === "string" && x.indexOf("module.") === 0; });
      if (valid && kept.indexOf("module." + own.module) < 0) own = null;
    } catch (e) {}
  }
  [files.catalogs[lang]].concat(files.shared, own ? own.files : files.app).forEach(add);
})();`;
}

/** Font registration and the Hub read are deferred until after the app commits and the browser has painted it. */
export function fontStartupScript(module: string): string {
  return `(function () {
  var root = document.getElementById("root");
  if (/^screen\\/[^/]/.test(location.pathname.slice(new URL(document.baseURI).pathname.length))) return;
  function start() {
    requestAnimationFrame(function () {
      requestAnimationFrame(function () {
        import(new URL(${JSON.stringify(`./${module}`)}, document.baseURI).href).then(function () { dispatchEvent(new Event("persona-font-startup")); }).catch(function () {});
      });
    });
  }
  if (root.childElementCount) start();
  else {
    var observer = new MutationObserver(function () {
      if (!root.childElementCount) return;
      observer.disconnect();
      start();
    });
    observer.observe(root, { childList: true });
  }
})();`;
}

/** The smallest file worth a brotli copy: server/internal/webasset MinSize, below which the hub compresses nothing. */
export const BROTLI_MIN_SIZE = 1024;

const brotliCompressAsync = promisify(brotliCompress);
// Quality 11 with the input's size as hint, so the same bytes always give the same copy.
const brotli = (data: Buffer) =>
  brotliCompressAsync(data, { params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: data.length } });

/**
 * Writes name.br beside each of `files` (paths below `dir`) of BROTLI_MIN_SIZE bytes or more, when brotli makes it
 * smaller: quality 11 with the input's size as hint, so the same file always gives the same bytes. The hub sends
 * these as stored to a browser that takes br (server/internal/hub/web.go serveBrotli); Go has no brotli encoder, so
 * the build makes them. Returns the files it wrote and their bytes before and after. Build time only.
 */
export async function brotliCopies(dir: string, files: string[]): Promise<{ files: number; before: number; after: number }> {
  const out = { files: 0, before: 0, after: 0 };
  await Promise.all(files.map(async (file) => {
    const data = await readFile(join(dir, file));
    if (data.length < BROTLI_MIN_SIZE) return;
    const br = await brotli(data);
    if (br.length >= data.length) return;
    await writeFile(join(dir, `${file}.br`), br);
    out.files++;
    out.before += data.length;
    out.after += br.length;
  }));
  return out;
}

/**
 * The most a cold first screen of the client page may weigh, as firstScreenBytes counts it over the page's own files,
 * the shared and App groups and one language's startup catalog (vite.config.ts startupPreload fails the build past
 * it): the heaviest language's at #2689 (ja, 559,565 B) plus 3%, and 2,560 B for the sidebar's assistant switcher
 * (views/AssistantSwitcher.tsx and the current assistant's scope), which every first screen draws, and 512 B for the
 * launcher chips' stale-pick check and its toast (views/assistant/AssistantChat.tsx, #3895), and 1,024 B for the
 * interrupted receipts' text and handling (#3903 added 883 B, #3917), and 512 B for adding a computer to the current
 * assistant (views/machines/AddComputer.tsx's words and its add.manager handling, #3508), and 512 B for the address's manager that conversation requests fall back on (lib/manager-choice.ts routedConversationManager, #3670),
 * and 1,024 B for the Agent header's permission mode (#3956 added 708 B: its button and tones, the mode menu's question
 * on leaving Allow all and its disabled Allow all row, their words), and 512 B for the Machines page asking each online
 * manager for its computers' metrics (#4026 added 254 B over), and 800 B for the Topics kind (#3783: its tab and name, the
 * shared stylesheet's classes for the topic page and the knowledge map, and its switch in Settings' search; the pages,
 * their code and words load on demand), and 2,048 B for tool names in words (#2986 added 1,996 B: lib/turns.ts's
 * subjects of Claude Code's newer tools and other servers' MCP tools named in words, the verbs views/chat/ActionRow.tsx
 * gives them), and 1,024 B for background rounds in the main conversation (#4314 added about 580 B: lib/turns.ts's
 * background-round detection and source blocks in an opened round, the folded line's words and Needs you's grouping by
 * supervisor), and 4,096 B for the authoritative workspace catalog and its direct workspace rows, the first-screen
 * worktree action words, and shared sticky group controls (PR #3222: 3,605 B over its main baseline), and 1,536 B for
 * when each session was last active (#4236 added 1,081 B to ja: the sidebar row's age at its end, the paged list's
 * Active within menu and its folded Earlier group, their words), and 512 B for a Persona chat's queue actions (#4342
 * added 312 B: views/chat/QueueStrip.tsx's own and relayed rows, the photos a take-back brings back, the Persona Send now
 * words and the relayed line), and 1,536 B for the refusal fallback (#2528 added 1,115 B to ja: the refusal notice
 * line, the fallback card above the composer with its way back to the configured model, lib/model-fallback.ts and
 * lib/refusal-public.ts, their words), and 640 B for holding earlier feed cards off the newest page until their page
 * loads (#4485 added 520 B to ja: the first-screen feed-page gate), and 64 B for the credential-owner bootstrap
 * (#4518, allocated in #4597), and 128 B for sending an edit of the last message safely (#4605 added 115 B to ja:
 * ChatView's wait for Claude's box after the rewind, the send's clear_draft and the give-back of a refused edit), and
 * 1,664 B for Forward (#3899 added 1,588 B: the action, the forward's card in a sent bubble and over the box, and its
 * strings; the picker loads on the first Forward), and 1,152 B for task cards (#5030 added 1,105 B to ja: the shared
 * task card head, a workflow call's card with its progress line and agent strip, the chat's Background dialog opened
 * from it, the old subagent links' redirect and the background-subtasks line, their words; the subagent card itself
 * loads when a summary is opened). Then 9,614 B less for the extension view renderer leaving the first screen (#3614:
 * ViewBody.tsx and what only it imports became a chunk of its own; ja went from 598,940 B on main 52b3fae6f to 589,326 B).
 * main was already 732 B over its budget at 52b3fae6f (ja 598,940 B against 598,208 B); that overage is kept here, not
 * new room. And 3,072 B for the composer's model pill (#5032 added 2,600 B to ja, 589,326 B to 591,926 B: the pill, the
 * session's model lists it reads for its name, the composer card's width modes and their words; the model panel, its
 * data and its words load on the pill's first tap). And 192 B for provider display names (#5029 added 165 B to ja:
 * plan task chips name their launcher by the provider's display name through lib/provider-names.ts, the shared cache of
 * the providers' names and the hook that reads them again after a rename). And 448 B for clearing the main chat's context
 * (#5095 added 405 B to ja, 593,185 B to 593,590 B: /clear in the main chat's command list, its ⋯ row, the cleared page's
 * state in lib/main-clear.ts and their words); main was already 595 B over its budget at 286d88fb2 (ja 593,185 B against
 * 592,590 B), and that overage is kept here, not new room. And 672 B for the main chat's hand-off cards (#5096 added 1,166
 * B to ja, 593,124 B at 7ff35f2e9 to 594,290 B, 509 B of it in the room left: a card for each dispatched Agent, a task's
 * or an Agent's page opened from the chat with the chat's place kept, their words; the task side panel and the
 * dispatched line they replace go, and the way-back strip on the Agent's page loads on its own; #5136: measured first
 * against 9d3f343d3, which came 17 B short). Then 1,939 B less for the image editor's geometry leaving the first screen
 * (#5142: ja 594,290 B at 5d97a6e77 to 592,351 B, the 15 B of room kept): the image viewer imported fitRect from
 * lib/image-edit.ts, which put that whole module, otherwise only the lazy editor's, on the first screen; fitIn and
 * fitRect now live in lib/image-zoom.ts. And 64 B for the sidebar's Recent count and its Show more (#5146 added 222 B
 * to ja, 592,153 B at 9534ce535 to 592,375 B, 213 B of it in the room left: lib/sidebar-recent.ts, which the sidebar
 * reads for how many to list and how far Show more opened it, and the Show more / Show less row; the setting's row and
 * its words are in the lazy Settings page and the ui-look catalog). And 174 B more for it as merged (#5166): #5161 added
 * 451 B to ja, not 222 B, 592,153 B at 76b9cf3b4a to 592,604 B at b73f259f71 (505 B with the file names' hashes counted
 * as a fixed string, so not their noise), which left main 174 B over; the room is 0 B. Take it back down by as much as
 * the sidebar's Recent code gets lighter. Every number so far is bytes as sent. From #5164 on, firstScreenBytes counts
 * every built file name's hash in the first screen's code as a fixed string, which is 1,454 B less in every language at
 * 0554037152 (ja 592,604 B sent, 591,150 B counted; en 589,016 / 587,562 B; zh-Hans 589,854 / 588,400 B): the budget
 * moved down by the same 1,454 B and kept its 0 B of room. That is no new room; it only stops a change to a lazy chunk or
 * to strings off the first screen from moving the count (#5145 moved ja 34 B as sent with no first-screen code changed).
 * And 2,624 B for child sessions folded into their parent (#5120 added 2,596 B to ja as counted, 591,150 B at dc5d1e3a3
 * to 593,746 B: every list's folding and the parent row's folded state and running line, a child's request counted,
 * opened and answerable under its parent (the store's lookup of its row), the paged list's roots, the parent chat's
 * placement of its children's cards, the hook that reads and holds their rows and the old child links' redirect; the
 * child card and what it reads when opened load with the card). And 256 B for a launcher switch that waits (#5035 added
 * 77 B to ja as counted, 593,746 B to 593,823 B: the pill's waiting state, the Agent chat sending the switch with the
 * user's message and keeping the words in the box when it is refused; the panel's switch code and words load with the
 * panel). And 13 B for the background supervisor (#5082: ja counts 594,043 B with it, built on main at a5747f46b; the
 * rest of what it adds fits the room main had: lib/background-supervisor.ts, which the sidebar, the conversation menu,
 * the chat's header and its review line read, the store's re-read of the assistant for a supervisor made since, the
 * line's reviewer, and its name and "Built-in"; the switch, the model row and their words are in the lazy Settings
 * page). And 238 B for the work summary's review fixes (#5155: ja counted 594,228 B with them against 594,043 B on main
 * at 4885b1458, +185 B; 594,241 B merged with main at ebcb0fa62, plus 40 B of room: the duration units back in the
 * catalogs as CLAUDE.md asks of user-facing words, wordless thoughts left out before grouping, rows spinning while the
 * exchange runs; the thought row and its parser stay lazy). And 371 B for model quotas, prices and composer usage
 * (#5176, #5208: ja 594,414 B before #5155's review fixes: the composer's quota and the context meter's model price,
 * the pill's model-panel handoff and the quota helpers; the panel's prices and quota rows and their words load with
 * the panel). A Codex opened with no first message (#4961) moved ja as counted by +223 B against b0a4a08b5 (594,241 B
 * to 594,464 B) and by -80 B against 449a12d7f (593,469 B to 593,389 B), inside the room either way: the waiting chat's
 * note, its box's placeholder, the page following the pane to the thread the first message began, their words and
 * the list row's line (New Agent's optional first message is in its lazy dialog). And 498 B for the changed-files card
 * and the error notices (#5157: ja 595,110 B against 593,659 B on main at f6f07e375, +1,451 B, of which 993 B fit the
 * room main had, plus 40 B of room: the card at the end of a finished Agent exchange, its count of the exchange's own
 * edits without drawing their diffs (lib/turn-changes.ts) and of a fold's `changes`, its review and file targets, the
 * store read of the session's folder, the feed cache's check of `changes`, the shared ErrorNotice the model-error and
 * usage-limit lines become, their marks and words). And 98 B for the provider wizard's paste and found aliases (#5193:
 * no first-screen code or words; the lazy wizard's pieces shared through views/settings/ProviderFields.tsx changed how
 * Rollup names the shared chunks' exports, which renamed App's imports and moved App 138 B as counted, its length
 * unchanged: ja 595,110 B on main at d7908558d to 595,248 B, 40 B of it in the room main had; take it back when App's
 * imports are renamed back). From #5331 on, firstScreenBytes also counts the minified names one file imports from
 * another as one fixed string and each export list as an empty one (placeholderExportNames). A lazy chunk that imported
 * one more name from a first-screen chunk renamed that chunk's exports and moved the count by hundreds of bytes with no
 * first-screen code changed: #5310 +255 B, which failed main's build, #5193 +138 B, the two merged -321 B, #5320 +29 B.
 * Counted so, ja is 589,323 B on main at c7164e477e (595,073 B the old way; en 585,737 B, zh-Hans 586,565 B), and the
 * same within 1 B at 1c78a837d6, 482f3b93df and 4b72a763c8. The budget is that, with 0 B of room: the 175 B main had,
 * #5193's 98 B among them, are taken back. And 529 B for the error banner over the composer (#5313: ja 589,852 B
 * against 589,323 B on main at fc16299dd: which error a chat stopped on and the rules that keep it there or in the chat,
 * its dismissal kept per conversation, a Retry that did not go bringing it back, the banner scrolled into view in its
 * box once, the keyboard back to the composer, the notice's ✕ and 28 px buttons, the failure note and the refused-send
 * card drawn as the same notice, and their words). And 444 B for cindy's run status bar (#5312: ja 590,296 B against
 * 589,852 B on main at a38ddd0cd: the state words worked out from the turn's newest steps in an Agent session and the plain
 * phases in a Persona conversation, Done only for a finished turn and its pop, their words; the speed history, its button
 * and card load once an Agent's turn runs). And 1,231 B for switching an Agent between Claude Code and Codex (#5054:
 * ja 591,480 B against 590,071 B on main at d7536a601c, +1,409 B, of which 225 B fit the room main had, plus 47 B of
 * room: the switch divider's chip above the first message the other engine got, the live stretch's items in a
 * switched-back conversation, the old session link's redirect to the conversation's row, the pill's engine switch state
 * and their words; the earlier stretches' windows, reading and drawing, the handoff panel and their words, the "Switch
 * Agent?" dialog and the panel's engine code load lazily). And 453 B because #5303 and #5457 landed together (#5488):
 * #5303 counted the room main had at d7536a601c as its own, and #5457 (#5420, the provider import link) used that room
 * too, merged in between; ja 591,939 B on main at 48f1e053b5, plus 41 B of room. And 684 B for each turn's cost on the
 * reply bar (#5426: ja 592,664 B against 591,753 B on main at cc83e770df, +911 B, of which 227 B fit the room main had:
 * collecting an exchange's usage while the chat's lines are built, a subagent's once in the chat, in the exchange that
 * launched it, and handing it to the reply; the prices, the sums by model, the card and their words load with the reply
 * bar). And 279 B for the same usage bar after the command card's wording landed (#5512, #5539: ja 592,943 B against
 * 592,664 B on main at 0856795596: the command-answer labels and the usage computation share the reply bar's helpers;
 * the command card itself and its usage card stay lazy, outside the idle tier). And 82 B for Done paging the list's last
 * days now that the list carries only the newest done items (#5549: ja 593,025 B against 592,867 B on main at 86c439b97,
 * +158 B, of which 76 B fit the room main had: Undo reopening what the hub moved, listed or not, a route reading an item
 * only the history has or, once, the hub's copy by id, and the state that says it is being read). And 32 B because #5592
 * and #4743 landed together: #5592 (+64 B, a Done group's first page starting past the done items the list holds)
 * counted the 69 B of room main had at 0bfcf2879 as its own, and #4743 (+37 B) used part of it, merged in between; ja
 * 593,057 B on main at 18de01b8d, with 0 B of room. And 94 B for the six-line composer (#5674, #5704, #5713: ja
 * 593,151 B on main at 5f60a1c45: the composer's fixed maximum-height token and manual-scroll measurement; the
 * layout is unchanged by this accounting). And 194 B for a refusal in plain words in a Persona conversation (#5202:
 * ja 593,305 B against 592,937 B on main at 5790ee8be3, +368 B, of which 214 B fit the room main had, plus 40 B of room:
 * the refusal line's one sentence with the model's short name and its closed Details, the fallback card's one-line
 * title, its Details and the short name while going back, their words). Raise it on purpose, in the change that needs
 * it.
 * From #5760 on, firstScreenBytes also normalizes local bindings (placeholderLocalNames): a minifier renaming a chunk's
 * locals no longer moves the count. On this main, that is 470,502 B en / 474,216 B ja / 471,480 B zh-Hans; the budget
 * is rebased to the new ja baseline, with 0 B of room. That is a rebase of the measurement, not new room for first-screen code.
 * And 228 B for Resume in terminal (#5521: ja 474,444 B against 474,216 B on main at 9223f7f92: the rule that offers it in
 * an Agent's ⋯ or disables it where its computer has no terminal engine, the item and its two strings, the one action it
 * shares with the stopped line's Resume, Codex threads counted as stopped Agents, and a stable Terminal handler so the
 * header still draws only when it changes). And 31 B because #5579 and #5860 landed together (#5879): #5579 was measured
 * on main at 9223f7f92 (+228 B) and #5860 merged before it, on whose main at bae989cf42 (ja 474,213 B) it costs +262 B;
 * ja 474,475 B on main at 624f81dde, with 0 B of room. And 79 B for the former GitHub star prompts (#5761: ja 474,554 B
 * against 474,421 B on main at 47c38a3466, +133 B, of which 54 B fit the room main had); #6570 removes those prompts
 * and their readiness/launch hooks without raising the budget. And 128 B for recent main changes (#6346: ja 474,617 B on main at 2010e8229f, 63 B over the old budget,
 * themselves, views/agents/GroupedSessions.tsx, load when a Browse by is chosen, which took 407 B back off; the same
 * code measured +1,396 B and +1,510 B on the two mains before, docs/lessons/web.md; the 30 B of room main had stay).
 * +100 B for the scheduled tasks list's "Inbox: …" marks and the assistant's own Inbox row (#6430): the Japanese first
 * screen was 49 B over with them, in three languages' text and a few lines of code.
 * And 720 B for "By to-do" as one kind of row (#6458: main at 8269cd921 measured ja 476,774 B, 114 B over the budget
 * before; this change takes it to 477,349 B, +575 B: the to-dos at the top level and their groups, "Recent (not running)"
 * among them, as rows with a count, a state and an ⓘ, a to-do's state in the To-dos page's word, and the groups'
 * explanations; the ⓘ's words, ui/InfoTipPanel.tsx, load the first time one is asked for, which took 287 B back off;
 * 31 B of room).
 * And 176 B for side sessions drawn under their parent, with main's own growth (#5935: main at 2d9596043 measured ja
 * 477,447 B, 67 B over the budget before; this change takes it to 477,525 B, +78 B: lib/side-sessions.ts's
 * arrangement, the paged list's use of it for its order and the Earlier split, and a row's parent identity in
 * lib/runtime-list.ts; 31 B of room).
 * And 394 B for the existing round-aware task-decision route and completed-task ownership (#5929),
 * retaining that feature allocation when merging the latest main.
 */
// #6575: verified runtime checkout display membership adds 79 B to the first screen on main at 2e57ac117
// (ja 478,119 B to 478,198 B). Carry that measured feature allowance onto the current main budget.
// #6542: the main chat model pill's revision-driven re-read adds ~10 B (ja measured 477,984 B on its own base), but
// #6741 measured en 475,060 B, ja 478,757 B, zh-Hans 476,047 B after later main landed; the hub measured ja 478,776 B.
// #6753's stopped-Agent dismissal adds 131 B to each language (ja 478,888 B, en 475,191 B, zh-Hans 476,178 B).
// #6759's Agent skills selection adds 49 B more to ja (478,937 B; en 475,240 B, zh-Hans 476,227 B).
// The 31 B room covers the hub's small build-environment difference.
// #6295: Paseo's workspaces add 11 B (ja 478,937 B on main at 8919ac022 to 478,948 B; en 475,240 B to 475,251 B,
// zh-Hans 476,227 B to 476,238 B): New worktree on a Paseo Git row, the engine sent with an agent's worktree status, add
// and remove, and no herdr ID to copy on a runtime's space. The budget moves by the same 11 B, keeping that 31 B room.
// #6480: a to-do suggestion kind (its title, button and toast in lib-inbox-events, which the first screen's suggestion
// rows and toasts name) and a merged row's list of sources raise the first screen to ja 479,163 B (en 475,454 B, zh-Hans 476,439 B), 184 B over the
// old budget, which moves by 215 B. Merge and to-do texts are in lib-inbox-actions, outside the first screen. The 31 B room stays.
// #6240: Fast on a new conversation adds 187 B to each language, measured in one environment against main at
// c1552d2e4 (ja 479,132 B to 479,319 B; en 475,423 B to 475,610 B, zh-Hans 476,408 B to 476,595 B): the draft, its
// place in the first request, and dropping it when the manager, the hub or the model can no longer honor it, which
// has to run while the model panel (loaded on intent) is closed. Its 675 B of code compressed to 180 B, 304 B and
// 187 B against three states of main: the cost follows the code around it. The budget moves by this one's 187 B.
// #7070: the model pill decides in an outer component whether a conversation gets one, so its hooks no longer follow
// an early return: 25 B more in each language against main at 57110700a6 (ja 479,319 B to 479,344 B; en 475,610 B to
// 475,635 B, zh-Hans 476,595 B to 476,620 B). The budget moves by the same 25 B.
// #6563: the minutes' summary card adds 85 B to each language (ja 478,898 B on fd8c988b6 to 478,983 B; en 475,201 B to
// 475,286 B, zh-Hans 476,188 B to 476,273 B): the Knowledge page's first stylesheet (the card's tints and grid,
// modules/recording/minutes-overview.css) named in the app's preload map, and Button passing aria-controls and aria-label
// (a topic's Show N more names its topic). The budget moves by the same 85 B, keeping that 31 B room.
// #6662: the paged Agents list under the complete list's title row, grouping bar and quick filters (By to-do / By
// status sections, the to-do names, the quick filters' hub keys, the quiet Load more line, the ⋯'s sorts ordering its
// sections) adds 318 B to ja, measured in one environment against main at eeac9a579 (ja 479,359 B to 479,677 B; en
// 475,650 B to 475,958 B, zh-Hans 476,635 B to 476,959 B). The budget moves by these 318 B on top of #6563's.
// #6770's Electron bridge adds 135 B to ja (479,944 B; en 476,225 B, zh-Hans 477,226 B): fixed native notification/badge
// calls, authenticated register/unregister presence, and clearing notifications on sign-out. The browser has no desktop
// chunk or eager import. The existing 31 B room covers the hub's small build-environment difference.
// #6589: the Agents list's Filter on its title row with its count, the chips with the query ⌘K put on the list (its text
// reopens ⌘K) and the small store the palette sets it through, the paged list's search taken from it in place of its
// own field, the phone page's search button: ja 480,189 B against 479,848 B on main at d916689d8, +341 B (en 476,485 B
// against 476,155 B, zh-Hans 477,467 B against 477,133 B; the lists' fields and their strings went; ⌘K's matcher,
// lib/agent-search.ts, and its Filter's menu load on intent, not here; the idle tier, 43,106 B with ⌘K's Filter button
// against 41,966 B, stays under its ceiling). main's 96 B of room stay.
// The Filter menu reads, toggles and drops the search parser's operator aliases (host, repo, status, after and
// pin), so a valid alias can be switched off instead of duplicated (#7339). Measured: en 476,598 B, ja 480,302 B,
// zh-Hans 477,580 B. The budget moves by the same 17 B.
// #7329: saved prompts in every composer's "/" list and "Save as Prompt…" on your own messages add 192 B to ja, measured
// in one environment against main at ec4c0513f9 (ja 480,302 B to 480,494 B; en 476,598 B to 476,803 B, zh-Hans
// 477,580 B to 477,783 B; the same code measured +110 B on aa5e1ec43e): the small store the composers read their rows
// from (lib/saved-prompt-rows.ts) and the Composer's use of it, the page event, the menu item and its words, and the
// settings tab's name and line. The list, its reads, its words, the editor and the settings page are chunks of their
// own, loaded the first time a box has the focus, the menu item is picked or the page opens. The budget moves by these
// 192 B on top of #7339's.
// A folded-search retry now clears its partial warning only after every requested fold has loaded; real failures and
// budget caps still show it (#7389). Measured: en 476,854 B, ja 480,545 B, zh-Hans 477,834 B. The budget moves by 51 B.
// #7375: Export Conversation… in a chat's ⋯ adds 77 B to ja, measured in one environment against main at 47973bbb81 (ja
// 480,562 B to 480,639 B; en 476,871 B to 476,941 B, zh-Hans 477,851 B to 477,922 B): the menu item, its word and the
// import of its dialog. Reading the conversation, the Markdown and the dialog's words are a chunk of their own
// (views/chat/ExportDialog.tsx, lib/chat-export.ts). That main measured 17 B over the 480,545 B budget in this
// environment before the change; the budget moves to what this change measured, 480,639 B.
// #7418: the budget stays where #7375 left it and the first screen gives back about 8.5 KB in each language, so the
// next changes have room again (measured in one environment against main at 85cfde464d: ja 480,639 B to 471,693 B;
// en 476,941 B to 468,489 B, zh-Hans 477,922 B to 469,339 B). The startup catalog takes just the keys the first screen
// reads of a namespace it reads a few of (src/i18n/startup-keys.ts: settings, todos, settings-managers, machines and
// five more), five namespaces no first-screen code names left it, and Tailwind no longer scans test files and e2e
// scripts (styles.css). Every build prints the room left (firstScreenRoom); when it runs low, take bytes off the
// first screen before raising this.
// #7459 (Web Push) takes about 90 B of that room in each language: the client_hello's device_id (lib/device-id.ts), the
// switch that says Web Push is on here (lib/web-push-flag.ts) and the two imports of lib/web-push.ts, a chunk of its own.
// #7440 takes 1,356 B of that room in each language, measured in one environment against main at 8ab19cb754 (ja
// 473,110 B to 474,466 B; en 469,919 B to 471,275 B, zh-Hans 470,752 B to 472,108 B): the sidebar's long groups drawn
// a part at a time (views/WindowedRows.tsx), the fold-all a group missed while it was not drawn (lib/fold-all.ts) and
// the headings' heights from one ResizeObserver (views/SidebarSection.tsx). WindowedRows is not a chunk of its own: the
// sidebar has no error boundary, so a failed download of it would leave the whole app blank.
// #8116: the changes since used the rest of the room, and main measured ja 480,853 B at cc030f78d, 214 B over. The
// budget stays; the startup catalog takes just the keys the first screen reads of thirteen more namespaces it reads
// part of (agentmenus, chat-comments, chat-contextgroup, chat-modelpill, chat-subagentcard, composer, context,
// lib-tasks, lib-todos, node-health, settings-nav, work-entry, worktree), measured in one environment against that
// main: ja 480,853 B to 479,549 B (en 477,669 B to 476,385 B, zh-Hans 478,451 B to 477,198 B), 1,090 B of room.
// #8380: the changes since used that room and put ja 569 B over (en 478,073 B, ja 481,208 B, zh-Hans 478,862 B).
// The budget stays. Recovery cards and refusal notices, the external-image consent gate, and the archived-agents and
// script-request routes load on intent. Their words, and other strings used only by already-lazy components, live in
// client-rest beside those components rather than in a whole startup namespace. On main 196aacec7e, after later
// changes had also reduced the first screen, the matched before/after counts are en 476,559 B to 473,866 B,
// ja 479,767 B to 476,944 B, zh-Hans 477,391 B to 474,647 B: 3,695 B of room, up from 872 B.
// #8261: the permission modes a provider lists (the composer's switch and the header's menu on Paseo's Grok, OpenCode
// and Codex chats, New Agent's rule in lib/launcher-models.ts) add 418 B to ja. Against the same main (1a5722542c,
// ja 569 B over) it kept the budget on its own: Session info is a chunk of its own, loaded on its first open
// (views/chat/SessionInfoDialog.tsx, with a Persona conversation's standing permissions), and its words a namespace of
// their own (chat-sessioninfo, out of chat-sessiondialogs): ja 481,208 B to 480,451 B (en 478,073 B to 477,315 B,
// zh-Hans 478,862 B to 478,105 B). On top of #8380, merged with main 1941e2e779: en 473,455 B, ja 476,517 B, zh-Hans
// 474,236 B, 4,122 B of room.
// The sessions list's invalidation throttle (lib/session-list.ts, one read per 2 s hint window instead of one per
// hint) takes 59 B of that room in ja, measured in one environment against its own main (ja 480,249 B to 480,308 B;
// en 476,545 B to 476,604 B, zh-Hans 477,527 B to 477,586 B). The budget stays.
// Parallel Agent worktrees add a Run again action and its shared launcher wiring; the measured first screen is
// en 477,537 B, ja 480,666 B, zh-Hans 478,356 B, so the budget accounts for the 27 B ja overage.
// #9146: a table cell's short code chips kept whole (ui/ReplyBlocks.tsx rehypeLinkedCode marks them data-chip; styles.css
// keeps them on one line) add 69 B in each language, measured in one environment against main c53557621 (ja 480,666 B
// to 480,735 B; en 477,537 B to 477,606 B, zh-Hans 478,356 B to 478,425 B). The cells' 40em cap is the same bytes.
// #9175: the inbox reads the hub's deltas (lib/inbox.ts, its cursor and applyInboxDelta), 124 B in ja; the Feishu card
// text (cardText) moves to the Inbox page's lib/inbox-page.ts and takes 150 B off: ja 480,666 B to 480,640 B (en
// 477,537 B to 477,511 B, zh-Hans 478,356 B to 478,330 B), measured in one environment against main 576970b459; merged
// with main 27a828ec48 (#9146 above): en 477,579 B, ja 480,708 B, zh-Hans 478,398 B, 27 B of room.
// #9215: a paged list keeps Load more on screen while it refreshes (lib/session-list.ts loadingMore, read by the flat
// list), a busy fleet's hints wait for a window counted from when the last read came back, and a reconnect reads the
// Agents list at once. 80 B in ja, measured in one environment against main 211207fd5f: ja 480,708 B to 480,788 B
// (en 477,579 B to 477,659 B, zh-Hans 478,398 B to 478,478 B). The budget takes the 53 B over.
// #9158 (UI version 8) adds 84 B in each language, measured in one environment against main b802a4d51c (ja 480,788 B to
// 480,872 B; en 477,659 B to 477,743 B, zh-Hans 478,478 B to 478,562 B): readView keeps the columns layout and a
// section's span, icon, link and columns (lib/extension-ui.ts readSection8), as it reads every other field, because the
// assistant's cards read views on the first screen too. Drawing them is a chunk loaded on intent (ViewBody8.tsx).
// The task-chat model display and supervisor follow-up (#9248) adds 75 B in ja; the measured first screen is
// en 477,818 B, ja 480,947 B, zh-Hans 478,637 B, so the budget accounts for that overage.
// Paseo workspace pins (#9300), after sharing the pin comparator and reusing sorted subtree summaries, add 39 B:
// en 477,857 B, ja 480,986 B, zh-Hans 478,676 B. The budget accounts for this measured feature delta.
// Agent-only task-row status and separate to-do chips (#6492), measured against main 3324a7323c in the same environment:
// en 478,843 -> 479,284 B, ja 481,979 -> 482,430 B, zh-Hans 479,676 -> 480,113 B. The budget adds 424 B of ja's measured
// 451 B feature delta, using 27 B of main's 123 B of room and keeping 96 B rather than adding any new room.
// #9283: an extension's sidebar entries and an open page's header drawn from the last places list at first paint
// (lib/extension-slots.ts keeps it under lib/feed-cache.ts's hub and sign-in scope; it is read at startup, so it is
// first-screen code, in the shared chunk that holds extension-slots) add 178 B in each language, measured in one
// environment against main 96a337b73: ja 480,986 B to 481,164 B (en 477,857 B to 478,035 B, zh-Hans 478,676 B to
// 478,854 B). The page's last view is drawn from a chunk of its own (lib/extension-view-cache.ts, with ExtensionView).
// #9337 (UI version 9) takes 99 B of that room in each language, measured in one environment against main 2491fb513 (ja
// 480,827 B to 480,926 B; en 477,709 B to 477,808 B, zh-Hans 478,522 B to 478,621 B): readView keeps a tile's gauge when
// its ratio is a finite number (lib/extension-ui-v6.ts readTile), as the assistant's cards read views on the first screen
// too; the ratio's bounds, its unit and the label are checked where the ring is drawn, a chunk loaded on intent
// (ViewBody9.tsx). The budget stays.
// #9071: the start page adds 348 B in each language, measured in one environment against main c11cdb8e3 (ja 480,926 B
// to 481,274 B; en 477,808 B to 478,156 B, zh-Hans 478,621 B to 478,969 B): an opening at the root address goes to the
// chosen page before the first paint, the hub's choice is read at the hello, and the sidebar gives way to the main chat
// when that page is not there (lib/start-page.ts). Its setting is in the Settings chunk. Main's room is kept on top.
// #9071 groups add 590 B in each language, measured in one environment against main 2b81cbc72, which has the start page
// (ja 481,286 B to 481,876 B; en 478,168 B to 478,758 B, zh-Hans 478,981 B to 479,571 B): the sidebar draws each group of
// pages as its own folding section (lib/sidebar-pages.ts arrangeSections, views/Sidebar.tsx PageGroup), and the
// arrangement goes to and from the hub with the start page (lib/start-page.ts). The editor is in the Settings chunk.
// Main's room is kept on top.
// #9132: a finished reply's Sources row is a chunk of its own (views/chat/TurnSources.tsx with lib/turn-sources.ts and
// its words); the first screen keeps only SegmentView's check for a web call and the lazy entry, the smallest of four
// layouts tried (mounting it under every exchange, or riding the reply bar's chunk, cost more). 85 B in ja, measured in
// one environment against main 21cd52cc2 (ja 482,104 B to 482,189 B; en 478,990 B to 479,075 B, zh-Hans 479,859 B to
// 479,944 B). Main had 422 B of room; the budget stays.
// #9751: Just finished (lib/agent-groups.ts justFinished, the paged list's section in lib/session-groups.ts, the seen
// outbox's unseen rows in lib/seen-outbox.ts) adds 112 B in ja, measured in one environment against main 7f756aa00a (ja
// 482,373 B to 482,485 B; en 479,275 B to 479,391 B, zh-Hans 480,079 B to 480,221 B). Main had 153 B of room; the
// budget stays.
// #9053: the Inbox section bar adds 9 B in each language, measured in one environment against main a2531b0b62 (ja
// 482,485 B to 482,494 B; en 479,391 B to 479,400 B, zh-Hans 480,221 B to 480,230 B): two of atoms' hard-coded sizes
// become the named one they equal, which pays the font ratchet for CountBadge's. Everything else is in the Inbox page's
// chunk: CountBadge (ui/CountBadge.tsx; 131 B in atoms against main 094253763d), its Needs-you tone in the existing
// --attention utility the sidebar's counts use until #4653 brings the blue (its text token was 29 B in the token table
// alone, more in the CSS), the grouping button with its mark (IconButton's menu attributes and the icon set's mark took
// 43 B), the bar's own copies of the edge fade's widths and the arrow keys' step (up to 131 B in the App chunk), and
// spacing classes the CSS already has (a new one took 44 B). The budget stays.
// 2026-11-17 (#9847): #9784 landed on a main that had moved since its measurement and added 270 B in ja, not 112 B
// (main 6fc9e85a00~1: ja 482,373 B; 6fc9e85a00: ja 482,643 B, en 479,549 B, zh-Hans 480,379 B), 117 B over the budget,
// so main stopped building and raised its budget to 482,683 B. This branch keeps the existing 482,526 B limit;
// equivalent first-screen rendering changes recover the required room without changing the guard.
// #10226: Developing Persona's To-dos view shadow reads today's triage through App's module (App.tsx re-exports three of
// lib/todo-triage.ts's functions; a direct import from the recorder's chunk split lib/todo-triage.ts into a chunk of its
// own, about 700 B). 49 B in each language, measured in one environment against main d5050affc5 (ja 482,098 B to
// 482,147 B; en 478,979 B to 479,028 B, zh-Hans 479,841 B to 479,890 B). The budget stays.
// 2026-11-18 (#10538): #10524 (ja 482,147 B to 482,619 B) and #10504 (to 482,722 B) left main's ja first screen 39 B over
// the budget, so main stopped building. The main conversation's readiness help (views/assistant/ReadinessHelp.tsx, a
// lazyOverlay started once something that opens it is on screen) and its assistant-readiness words left the first screen:
// ja 482,722 B to 481,367 B, en 479,634 B to 478,331 B, zh-Hans 480,477 B to 479,146 B, measured in one environment
// against main 51eb4b6a47. The budget stays.
// #10358: a conversation an extension opened is marked with that extension's own icon and, while the icon is drawn,
// named after it. The icon and the installed extensions it is picked from are a lazy chunk (ui/OpenerTile.tsx,
// lib/opener-icons.ts); the first screen keeps OriginMark's check for an opener, the lazy entry, the opener handed to
// the first message's line and the name the mark tells its row and that line. 251 B in each language, measured in one
// environment against main 4732e71a5 (ja 481,367 B to 481,618 B; en 478,331 B to 478,582 B, zh-Hans 479,146 B to
// 479,397 B), much of it the lazy entry's preload list: its chunks' names in the App chunk's dependency table, which
// renumbers the entries of every lazy import after it, so the count moves with main (117 B against affb08b4d, 258 B
// against c53addf40; before the names: 45 B against 04cffd3d4, 198 B against c2fec3548, 155 B against bda346e8c). Main
// had 1,316 B of room; the budget stays.
// #9727: UI version 10's graph adds 759 B in every language, measured in one environment against main b90d75a42 (ja
// 481,100 B to 481,859 B; en 478,017 B to 478,776 B, zh-Hans 478,868 B to 479,627 B): the reading of a view's graph
// (lib/extension-ui.ts readGraph and the UIGraph fields), its one-line lazy entry and Suspense case in
// views/extensions/ViewBody.tsx with the fallback that keeps a card's height while the canvas's chunk loads, and the
// graph's owner as a React context (lib/extension-owner.tsx) with its provider at every surface that draws an
// extension's view (ExtensionSlots, the assistant's cards, an inbox item's body, the subscription pages, the launcher
// sections), all in the first screen's static closure; the canvas itself is a chunk of its own (ViewBody10.tsx).
// Main had 1,583 B of room; the budget stays.
// #11040: main had 29 B of ja room. The voice call's UI (its bar, the thread's captions, the ended row and the dialog on
// why a call cannot start: modules/voice/chat/VoiceCall.tsx, loaded with the call's code) and 97 of the 101 voice strings
// left the first screen, and startup entries only lazy chunks read (chat-navrail but target_failed, the
// chat-interactions fields and input toggle, assistant-history.sidebar, agentspage.new_agent) went to the rest of the
// catalog: ja 482,497 B to 477,736 B (en 479,378 B to 474,739 B, zh-Hans 480,224 B to 475,592 B), measured in one
// environment against main b6b333e7e4. Main then went 16 B over (de8b2b4c41: en 479,423 B, ja 482,542 B, zh-Hans
// 480,269 B); merged with it: en 474,792 B, ja 477,789 B, zh-Hans 475,645 B, 4,737 B of room. The budget stays.
// #10922: the Assistant tray's Finished entry (lib/assistant-entries.ts `finished` with lib/todo-triage.ts `finishedAt`,
// its entry in views/assistant/Tasks.tsx, a new row's Just finished mark and the entry's word), less the Just finished
// group In progress no longer draws, adds 47 B in ja, measured in one environment against main ac465e3d3 (ja 478,078 B
// to 478,125 B; en 475,068 B to 475,117 B, zh-Hans 475,930 B to 475,960 B; against main f4d81af87 it was 10 B in ja).
// Its rows, seeing a task on opening it and the tray's empty line come with the tray's first opening, in the chunk of
// the Parked rows (FinishedTaskRows.tsx through ParkedTaskRow.tsx: no lazy entry of their own), which also took
// lib/todo-seen.ts off the first screen; on it they cost 228 B. Main had 4,653 B of room; the budget stays.
// #8578: directory-hidden Agents and their pending cards leave the lists, counts and attention events (lib/sessions.ts
// directoryVisible / directoryVisibleCards, lib/attention.ts) and the Agents page gets its Hidden sessions button; the
// inspection dialog and its hint (key temporary_view, outside the startup prefix "inspect") are a chunk of its own
// (views/HiddenAgentSessionsDialog.tsx); pending cards' sessions are looked up until each is known (lib/store.ts
// cardSessionRow). 549 B in ja, measured in one environment against main 0c97da818c (ja 479,051 B to 479,600 B; en
// 476,160 B to 476,701 B, zh-Hans 476,915 B to 477,472 B), 3,131 B of room. The budget stays.
// Keep the stricter existing limit while combining these changes.
// #10904: the overview's task cards name who works on each task, the lead Agent's computer, model and effort and "+N"
// for the others (lib/home-tasks.ts leadAgent; views/Home.tsx taskAgentLine over the model pill's pillView, modelList and
// effortTitle, and home.task_agents_more). 205 B in ja, measured in one environment against main e7a12f7323 (ja
// 482,497 B to 482,702 B; en 479,378 B to 479,585 B, zh-Hans 480,224 B to 480,422 B). Main had 29 B of room, so the
// upstream budget became this ja size plus those 29 B.
// #9745: Overview's extension cards drawn as the user arranged them add 749 B in ja, measured in one environment
// against main 54408eed1b (ja 478,171 B to 478,920 B; en 475,161 B to 475,957 B, zh-Hans 476,023 B to 476,809 B): the
// arrangement's store, kept per hub and sign-in, with the revision Reset's Undo checks, and the order, hidden cards and
// widths it draws (lib/overview-cards.ts), Arrange cards in Overview's top bar, and the arrangement to and from the hub
// with the start page, one change on its way at a time and pushes heard during it settled once the hub pushed it, a
// read a later hello or another sign-in overtook left alone (lib/start-page.ts). The editor (its heading, each card's
// controls, the drag and the edits) is a chunk of its own (views/extensions/OverviewArrange.tsx, lib/overview-edit.ts).
// Main had 4,560 B of room, so the budget is this ja size plus those 4,560 B.
// #11090: a waiting to-do's label says what it waits on (lib/todo-wait-reason.ts waitReason and todoLook, read on the
// first screen by Overview's task cards, the main chat's trays and the sidebar's By to-do rows; the seven
// lib-state-labels waits_* words in place of task_waiting). 621 B in ja, measured in one environment against main
// 591ad5d369 (ja 480,763 B to 481,384 B; en 477,858 B to 478,422 B, zh-Hans 478,582 B to 479,142 B). Main had 2,717 B
// of room; the budget stays (merged with #9778 below, those 621 B come out of the room main had).
// #9778 (#9745's follow-up): what is on its way kept to the sign-in it was made under (a change sent, queued or not
// taken under an earlier one dropped, its answer changing nothing) and a hello's read a push or another tab's change
// overtook left alone (lib/start-page.ts, lib/overview-cards.ts), and Arrange cards opening a folded cards' section and
// scrolling to it, its heading counting the cards shown (views/extensions/ExtensionSlots.tsx). 63 B in ja, measured in
// one environment against main cacd8d75a6 (ja 480,670 B to 480,733 B; en 477,744 B to 477,807 B, zh-Hans 478,482 B to
// 478,545 B). Main had 2,810 B of room, so the budget is this ja size plus those 2,810 B.
// #10822: Colored sidebar icons, an Appearance choice off by default: the choice's store and the built-in pages' colors
// (lib/sidebar-icons.ts), read where the sidebar draws its pages, and the style a colored icon takes. 356 B in each
// language, measured in one environment against main baacfe89c6 (ja 480,552 B to 480,908 B; en 477,626 B to 477,982 B,
// zh-Hans 478,364 B to 478,720 B); the setting's row and words load with Settings. Main had 2,928 B of room (a budget of
// 483,480 B), which is kept, so the budget moves by these 356 B, to this ja size plus those 2,928 B.
// Merged with #9778, the budget adds both feature deltas (63 B + 356 B) to the previous 483,480 B limit.
// #10905: Overview's own sections arranged with the extensions' cards add 323 B in ja, measured in one environment
// against main 1e34873c71 (ja 483,878 B to 484,201 B; en 480,952 B to 481,265 B, zh-Hans 481,673 B to 482,012 B): the
// sections' ids and their place in the arrangement, Needs you and Stuck never hidden (lib/overview-cards.ts),
// Overview's cards and sections drawn in that order, each row of cards with its heading and all of them under one fold
// (views/OverviewBody.tsx, Home's sections handed to it, Working's with the background-wait group of #10938), the
// Arrange button always there, and where a default manager of #9745 refuses the sections, the cards sent on their own
// and the sections kept here under the hub's cards (keepSections and its note per sign-in, lib/overview-cards.ts;
// lib/start-page.ts). The sections' bars and their names while arranging, and what a change writes, are the editor's
// chunk (views/extensions/OverviewArrange.tsx, lib/overview-edit.ts).
// This branch keeps the existing 482,526 B limit with the same measurement.
// #11442: phone fixes add 739 B in ja, measured in one environment against main 989b48a766 (ja 482,305 B to 483,044 B;
// en 479,417 B to 480,159 B, zh-Hans 480,166 B to 480,922 B): toasts held only by a mouse pointer and a sheet's focused
// field kept in view above the keyboard (ui/atoms.tsx), Back closing the search palette and the composer's overlays
// (lib/back-close.ts), the header on a gone tab's and an ambiguous link's page (App.tsx), a waiting card's field kept
// unfolded and in sight with Android's keyboard (lib/viewport.ts, views/chat/ChatView.tsx), and the classes behind the
// keyboard-aware palette and dock, the attachment ✕'s corner reach and Overview's task-card overlay (styles.css).
// Where the toasts sit on a phone is a chunk of its own (ui/toast-lift.tsx) a narrow screen loads after the shell.
// Main had 221 B of room, so the budget is this ja size plus those 221 B.
// Preserve the backend workspace branch ceiling.
// #11511: phone fixes add 272 B in ja, measured in one environment against main bb39eb7e10 (ja 482,369 B to 482,641 B;
// en 479,515 B to 479,787 B, zh-Hans 480,246 B to 480,518 B): Conversation tasks in ⋯ on a phone and the chat's pills'
// touch targets, with the Loading earlier row drawn over the top padding (views/chat/ChatView.tsx), and the Agents
// page's scroll kept on Back with its title row's and By to-do rows' touch boxes (views/Sidebar.tsx,
// views/SidebarTree.tsx). Main had 157 B of room, so the budget is this ja size plus those 157 B (482,798 B).
// #10822: Colored sidebar icons, an Appearance choice off by default: the choice's store and the built-in pages' colors
// (lib/sidebar-icons.ts), read where the sidebar draws its pages, and the style a colored icon takes. 172 B in each
// language, measured in one environment against main 8df9e781b0 (ja 482,711 B to 482,883 B; en 479,826 B to 479,998 B,
// zh-Hans 480,595 B to 480,767 B); the setting's row and words load with Settings. The budget is main's 482,798 B
// ceiling plus those 172 B.
// #11838: the sidebar's To-dos badge reads the To-dos page's own inputs through one hook the page shares
// (lib/todo-owner-needs.ts: the to-dos' Agents with the list's task rows and cards answered here, and one minute
// clock), and ownerNeeds keeps why each to-do counts. 88 B in each language, measured in one environment against main
// 136308faf5 (ja 482,906 B to 482,994 B; en 479,979 B to 480,067 B, zh-Hans 480,757 B to 480,845 B); the rows' mark
// and its words load with the To-dos page. The budget is main's 482,970 B ceiling plus those 88 B.
// #11804 + #11820: Antigravity queue/send-now runtime copy and launcher-scoped Assistant sign-in guidance add
// 85 B to the ja first screen (ja 483,143 B, en 480,233 B, zh-Hans 480,987 B). The budget is main's 483,058 B
// ceiling plus those 85 B.
// #11825: A prepaid balance row in the sidebar's usage strip (views/UsageBars.tsx, lib/usage-windows.ts): the provider's
// amount and currency for an official DeepSeek launcher, with no bar; the popover's longer words load with it. 260 B in
// ja, measured in one environment against main c4459e56f3 (ja 482,994 B to 483,254 B; en 480,067 B to 480,344 B,
// zh-Hans 480,845 B to 481,098 B), from a 483,058 B ceiling. Merged with #11804 + #11820 (main fc31360177), the two
// changes share bytes: the row adds 25 B to that main's ja size, measured in one environment (ja 483,143 B to
// 483,168 B; en 480,233 B to 480,258 B, zh-Hans 480,987 B to 481,012 B). The budget is the merged ja size.
// #7524: Starred adds 610 B in ja against 4dc8a1ee44 (483,143 B to 483,753 B) for the route, compact index,
// start-page availability, sidebar and message identity. The snapshots, their operations and deep-link paging
// load on demand. The bound leaves 37 B above the measured ja size.
// Merged, the two changes add 25 B + 610 B to their shared 483,143 B ja baseline, giving 483,778 B;
// #7524's 37 B of room is kept on top.
// #11979 (in-session model/effort switching in lib/model-switch.ts on main) raised the ja first screen by 80 B
// (en 481,036 B, ja 483,895 B, zh-Hans 481,759 B).
// #12038: the sidebar's Starred count/access has a small summary store of its own. Per-message identity and marks
// belong to the lazy message controls, not the first screen. ja 483,895 B to 483,788 B; the budget stays 483,815 B.
// #12034's delivery closeout gate row lives in the lazy-loaded Settings page and adds 0 B, so the budget stays too.
// #12055: an Agent's page opened from inside another page keeps that page's sidebar row lit (lib/persona-nav.ts
// sidebarRoute), and a briefing's waiting and later rows open their chat in the app: 101 B more in ja on its base.
// Merged, #12038's 107 B reduction covers #12055's 101 B increase, so main's 483,815 B budget stays.
// #12090: the offline face's own outline and shadow tokens (the dark disc keeps its ink outline) add 48 B in ja
// (and in en and zh-Hans) against main 86ff5c5259.
// #11801: stable structural sidebar ordering adds 343 B in ja against main 0009fe34d6 (ja 483,664 B to 484,007 B;
// en 480,757 B to 481,104 B, zh-Hans 481,491 B to 481,816 B). Keep main's 199 B of room above the new ja size.
// #8236: a conversationless direction stays visible and opens only its actual outside Agent, while a targetless one is
// a non-link (lib/direction-names.ts, lib/direction-tree.ts, views/Sidebar.tsx and views/SidebarRows.tsx). This adds
// 285 B in each language, measured in one environment against main c04291e493 after merging it (ja 484,098 B to
// 484,383 B; en 481,251 B to 481,536 B, zh-Hans 481,974 B to 482,259 B). Main had 108 B of room, so the budget is
// main's 484,206 B ceiling plus those 285 B.
// #13846: the module host serves the chat slots, Settings categories and card renderers (core/ChatSlots.tsx, the
// served fields and the hub flag rule in core/module-host.ts, their places in views/chat/ChatView.tsx, Composer.tsx and
// Interactions.tsx). 799 B in ja, measured in one environment against main 7be9a71cc (ja 484,445 B to 485,244 B; en
// 481,786 B to 482,585 B, zh-Hans 482,421 B to 483,220 B). Main had 46 B of room, so the budget is main's 484,491 B
// ceiling plus those 799 B. The web half of #13431 takes voice's first-screen parts off with the move into a module.
// #13021: main ec2563b7d7 itself measures ja 484,587 B (en 481,938 B, zh-Hans 482,579 B), 96 B over the 484,491 B
// above. The question rail's marks add 141 B in ja (en 141 B, zh-Hans 141 B): the rail now drawn on a touch screen for
// Persona's chats, the marks' state in ChatView and the lazy import of views/chat/QuestionMarksHost.tsx (the rules and
// read marks moved out of the first screen into that chunk, which took 808 B off the first version). ja 484,728 B;
// 10 B of room on top. Merged with #13846, the voice module's first-screen reduction leaves room for these 141 B,
// so the budget stays at #13846's 485,290 B ceiling.
// #13803: mount-once long-chat windowing adds 200 B to the ja first screen, measured in one environment against main at
// 464c1b17b3 (ja 484,433 B to 484,633 B; en 481,792 B to 481,992 B, zh-Hans 482,418 B to 482,618 B). The near-viewport
// observer, searchable estimated placeholders and synchronous scroll compensation start with the chat; rich bodies stay
// deferred. The budget moves by the same 200 B, keeping the 58 B of room main had. Merged with #13846, the measured
// first screens are ja 485,259 B, en 482,600 B and zh-Hans 483,235 B; the budget keeps #13846's 46 B of room rather
// than adding the whole separate allowance again.
// #14398: the Agents list's ⋯ menu toggles "按最近更新排序" and "显示筛选" (sidebar.sort_activity, sidebar.show_list_filter;
// the sidebar namespace is first-screen whole) add 77 B to the ja first screen, measured with origin/main 1ad9a001e6
// merged: en 482,711 B, ja 485,382 B, zh-Hans 483,352 B. The budget moves by the same 77 B plus 10 B of room.
// #12585: keeping read Unread/Just finished Agent rows in place until refresh adds lib/seen-rows.ts, heldSeen grouping
// in lib/agent-groups.ts, and sidebar/store wiring (SeenRowsNotice, ClearSeenButton, SeenRowsSetting and seen-rows
// strings stay lazy); measured on origin/main 9baa8e3a1: en 483,193 B, ja 485,750 B, zh-Hans 483,805 B. Raise budget
// to the measured ja size plus 10 B of room.
// #15511: an explicitly promoted main relay keeps its incoming source collapsed until opened, including its deferred
// placeholder. This change on base 4bab19c283 measures en 483,782 B, ja 486,399 B, zh-Hans 484,384 B. Raise the budget
// to the measured ja size plus 10 B of room.
// #16992: Hub Admin shares the main app's titlebar inset under the desktop's macOS titlebar (the expanded sidebar's rule
// in styles.css also names the admin sidebar; lib/mac-chrome.ts is now shared with the admin page, and macChromeMode
// gives both shells their data-mac-chrome). 77 B in every language, measured in one environment on base 4a81814ec7
// (ja 485,962 B to 486,039 B; en 483,466 B to 483,543 B, zh-Hans 484,115 B to 484,192 B). The budget moves by the
// same 77 B, keeping the room main had.
// #17136: align the sidebar glide highlight to each row's own horizontal bounds inside the scroller (no scroll wobble,
// no active-row overlap) and host the macOS sidebar collapse toggle in MacBayTools beside the traffic lights (+46 B on
// base 53884840c4: ja 486,430 B to 486,476 B). Merged with origin/main (including #17071, which added +109 B to the
// first screen), this tree measures en 484,077 B, ja 486,585 B, zh-Hans 485,079 B. Raise the budget to the measured
// ja size plus 10 B of room.
// Frosted Glass & macOS bay polish: fade truncated overlay-menu sidebar row text before ⋯ without "..."+"⋯" six dots,
// apply translucent glass tokens inside [data-sidebar-column] under liquid-glass, and host both Collapse and Search in
// MacBayTools (+274 B on origin/main 0e974208f9: en 484,357 B, ja 486,859 B, zh-Hans 485,362 B). Raise the budget to
// the measured ja size plus 10 B of room.
// Vibrancy focus-churn reduction + 3-track wide-screen sidebar transition + continuous Apple G2 contour morph
// (lib/sidebar-transition.ts, useMacChromeMode, appleG2ContourAt, MacMainContour live SVG driver, desktop-glass
// inner backdrop-filter suppression, and animating top-inset preservation): en 485,174 B, ja 487,676 B,
// zh-Hans 486,179 B. Raise the budget to the measured ja size plus 10 B of room.
// #17037: the Agents list's "Orca isn't running on <computer>" row (HerdrNotices.tsx, engines.ts orcaStopped, the
// herdr-notice strings) and the herdr row keyed on the engine set add 41 B to the ja first screen, measured in one
// environment against main at 83cad7ccd7 (ja 487,986 B to 488,027 B; en 485,511 B to 485,537 B, zh-Hans 486,517 B to
// 486,541 B). That main already measured over the budget there; the budget moves by these 41 B only.
// #17697: eliminating wide-screen sidebar expand/collapse flicker (render-phase leaving state + deferred clearStyles
// in lib/sidebar-transition.ts, active outgoing Sidebar in App.tsx, non-inert CollapseButton handoff in
// SidebarRail.tsx, preloaded root counts in PagedSessionList.tsx) adds 140 B to the ja first screen, measured in
// one environment against main at 2443320510 (ja 488,035 B to 488,175 B; en 485,565 B to 485,705 B, zh-Hans
// 486,567 B to 486,707 B). That main already measured over budget (tracked in #17695); the budget moves by these 140 B only.
export const FIRST_SCREEN_BUDGET = 487_867;
// The hash in a built file's name: the 8 characters of Rollup's [hash] (url-safe base64) between "-" and the
// extension, as vite.config.ts names every chunk and asset ("assets/[name]-[hash].js", "[name]-[hash][extname]").
const NAME_HASH = /-([A-Za-z0-9_-]{8})\.[^./]+$/;
// Where a file's code names another built file ("./api-<hash>.js", "assets/App-<hash>.css"); counted only when the
// hash is one of the build's, so other code that happens to look like one is left alone.
const NAMED_HASH = /-([A-Za-z0-9_-]{8})(?=\.)/g;
/** What firstScreenBytes counts in place of each hash: as long as one, the same for every file. */
export const HASH_PLACEHOLDER = "00000000";

/** `code` with the hash of each of the build's files it names (`hashes`, nameHashes) replaced by HASH_PLACEHOLDER. */
export function placeholderHashes(code: string, hashes: ReadonlySet<string>): string {
  return code.replace(NAMED_HASH, (m, hash: string) => (hashes.has(hash) ? `-${HASH_PLACEHOLDER}` : m));
}

// A file's import of names from another file, as Rollup writes it (`import{eu as a,j}from"./App-<hash>.js"`, no spaces
// once minified), and an export list (`export{Yme as eu,…}`, or one that re-exports `from` another file).
const IMPORT_CLAUSE = /\bimport\s*\{([^{}]*)\}\s*from\s*(["'])([^"']+)\2/g;
const EXPORT_CLAUSE = /\bexport\s*\{([^{}]*)\}/g;
// One name in either list: the name, then ` as ` and another name or nothing.
const SPECIFIER = /^(\s*)([\w$]+)(?:(\s+as\s+)([\w$]+))?(\s*)$/;
/** What firstScreenBytes counts in place of each name a file imports from another of the build's files. */
export const EXPORT_NAME_PLACEHOLDER = "_";

/**
 * `code` with the names it imports from the build's other files (`hashes`, nameHashes) counted as
 * EXPORT_NAME_PLACEHOLDER, keeping the local names its code uses (`import{eu as a,j}` → `import{_ as a,_ as j}`), and
 * each of its export lists counted as an empty one (`export{Yme as eu,…}` → `export{}`). Rollup names a chunk's
 * exports by their place in it, a minified alias each (a, b, …, eu, ev, …), and sorts the list by alias: a lazy chunk
 * that imports one more name from a first-screen chunk moves every later alias, reorders that list and renames the
 * imports of every first-screen file reading from it, with the code the same length within a few bytes (#5331). A
 * list whose entries are not plain names, or an import from a file the build did not write, stays as it is.
 */
export function placeholderExportNames(code: string, hashes: ReadonlySet<string>): string {
  const entries = (list: string) => {
    const out = list.split(",").map((s) => SPECIFIER.exec(s));
    return out.every((m) => m !== null) ? (out as RegExpExecArray[]) : null;
  };
  return code
    .replace(IMPORT_CLAUSE, (clause, list: string, _quote: string, from: string) => {
      const hash = NAME_HASH.exec(from)?.[1];
      const names = hash && hashes.has(hash) ? entries(list) : null;
      if (!names) return clause;
      const counted = names.map(([, lead, name, as, local, trail]) => `${lead}${EXPORT_NAME_PLACEHOLDER}${as ?? " as "}${local ?? name}${trail}`);
      const open = clause.indexOf("{") + 1; // the list's own brace: nothing before it can hold one
      return clause.slice(0, open) + counted.join(",") + clause.slice(open + list.length);
    })
    .replace(EXPORT_CLAUSE, (clause, list: string) => (entries(list) ? "export{}" : clause));
}

// Common globals are not local names: replacing them would only make a chunk's count less meaningful. Property
// names, method names and labels are not local bindings either and are left alone by the AST walk below.
const GLOBAL_IDENTIFIERS = new Set([
  "arguments", "globalThis", "undefined", "NaN", "Infinity", "console", "window", "document", "navigator", "location", "history",
  "localStorage", "sessionStorage", "performance", "fetch", "Request", "Response", "Headers", "AbortController", "FormData", "Blob", "File",
  "FileReader", "URL", "URLSearchParams", "URLPattern", "TextEncoder", "TextDecoder", "DOMParser", "MutationObserver", "ResizeObserver",
  "IntersectionObserver", "Image", "Event", "CustomEvent", "Node", "Element", "HTMLElement", "EventTarget", "structuredClone", "crypto",
  "setTimeout", "clearTimeout", "setInterval", "clearInterval", "queueMicrotask", "requestAnimationFrame", "cancelAnimationFrame",
  "Object", "Array", "String", "Number", "Boolean", "BigInt", "Symbol", "Math", "JSON", "Date", "RegExp", "Error", "TypeError", "RangeError",
  "Map", "Set", "WeakMap", "WeakSet", "Promise", "Intl", "Reflect", "Proxy", "ArrayBuffer", "DataView", "Uint8Array", "Uint16Array",
  "Uint32Array", "Int8Array", "Int16Array", "Int32Array", "Float32Array", "Float64Array", "BigInt64Array", "BigUint64Array",
  "require", "module", "exports", "process", "Buffer", "__dirname", "__filename",
]);

/** What firstScreenBytes counts in place of a local binding: one character, so the minifier's choice of names cannot move it. */
export const LOCAL_NAME_PLACEHOLDER = "_";

/**
 * `code` with its local bindings counted as LOCAL_NAME_PLACEHOLDER, using TypeScript's parser rather than a regex:
 * strings, comments, property names, method names, labels and the globals above stay as they are; local declarations
 * and every reference to them become the same one-character name. Rollup gives locals short names by how often and
 * where they are used, so adding code anywhere in a chunk can otherwise rename thousands of unrelated locals and move
 * the compressed size without any first-screen change (#5760). This is for measurement only, never emitted.
 */
const JS_CODE_HINT = /\b(?:const|let|var|function|class|import|export|return|if|for|while|switch|try|new|async|await)\b/;

export function placeholderLocalNames(code: string): string {
  // firstScreenBytes also reads catalogs, styles and placeholders that are not JavaScript. A file with no code
  // structure is measured as it is, not treated as one long identifier to replace.
  if (!JS_CODE_HINT.test(code)) return code;
  const source = ts.createSourceFile("chunk.js", code, ts.ScriptTarget.ES2022, true, ts.ScriptKind.JS);
  const replacements: { start: number; end: number }[] = [];
  const propertyName = (parent: ts.Node, name: ts.Node): boolean =>
    (ts.isPropertyAssignment(parent) || ts.isPropertyDeclaration(parent) || ts.isPropertySignature(parent) ||
      ts.isMethodDeclaration(parent) || ts.isMethodSignature(parent) || ts.isGetAccessorDeclaration(parent) ||
      ts.isSetAccessorDeclaration(parent) || ts.isEnumMember(parent)) && (parent as { name?: ts.Node }).name === name;
  const visit = (node: ts.Node) => {
    if (ts.isIdentifier(node)) {
      const parent = node.parent;
      const propertyAccess = ts.isPropertyAccessExpression(parent) && parent.name === node;
      const label = (ts.isLabeledStatement(parent) || ts.isBreakOrContinueStatement(parent));
      if (!propertyAccess && !label && !propertyName(parent, node) && !GLOBAL_IDENTIFIERS.has(node.text) && node.text !== LOCAL_NAME_PLACEHOLDER) {
        replacements.push({ start: node.getStart(source), end: node.end });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  let out = code;
  for (let i = replacements.length - 1; i >= 0; i--) {
    const r = replacements[i];
    out = out.slice(0, r.start) + LOCAL_NAME_PLACEHOLDER + out.slice(r.end);
  }
  return out;
}

/** The hashes in the build's file names (`names`); a name without one (sw.js, index.html) has none. */
export function nameHashes(names: Iterable<string>): Set<string> {
  const out = new Set<string>();
  for (const name of names) {
    const hash = NAME_HASH.exec(name)?.[1];
    if (hash) out.add(hash);
  }
  return out;
}

/**
 * What FIRST_SCREEN_BUDGET holds each language's first screen to (`screens`: language → its files, paths below `dir`):
 * what the built-in hub sends for them to a browser that takes br (each one's brotli copy as brotliCopies makes it, or
 * the file itself under BROTLI_MIN_SIZE or when brotli does not make it smaller), with every hash of the build's files
 * (`names`, paths below `dir`) in their code counted as HASH_PLACEHOLDER, and the names they import from each other and
 * their export lists as placeholderExportNames counts them, and their local bindings as placeholderLocalNames counts them. A first-screen file names the files it imports and
 * preloads by their hashes, which change with any change to those files and are close to incompressible: counted as
 * sent, a change only to a lazy chunk or to strings off the first screen moves the total by tens of bytes either way
 * (#5164). A lazy chunk that imports one more name from a first-screen chunk renames that chunk's minified exports
 * and every import of them, which moved the total by hundreds (#5331). Counted this way neither moves it; any other
 * change to the first screen's code counts as it is sent. Build time only.
 */
export async function firstScreenBytes(dir: string, screens: [string, string[]][], names: Iterable<string>): Promise<[string, number][]> {
  const hashes = nameHashes(names);
  const sizes = new Map<string, Promise<number>>(); // each file once: the languages share all but their catalog
  const size = async (file: string) => {
    // latin1 keeps every byte as it is; the names, hashes and placeholders are ASCII. The export names before the
    // hashes: an import is recognized by the hash of the file it names.
    const code = (await readFile(join(dir, file))).toString("latin1");
    const normalized = file.endsWith(".js") ? placeholderLocalNames(placeholderHashes(placeholderExportNames(code, hashes), hashes)) : placeholderHashes(code, hashes);
    const data = Buffer.from(normalized, "latin1");
    if (data.length < BROTLI_MIN_SIZE) return data.length;
    return Math.min((await brotli(data)).length, data.length);
  };
  const sized = (file: string) => sizes.get(file) ?? sizes.set(file, size(file)).get(file)!;
  return Promise.all(screens.map(async ([lang, files]): Promise<[string, number]> => [lang, (await Promise.all(files.map(sized))).reduce((sum, n) => sum + n, 0)]));
}

/** Room under FIRST_SCREEN_BUDGET below which the build warns (firstScreenRoom): about one page's new strings. */
export const FIRST_SCREEN_LOW_ROOM = 1_024;

/**
 * The line the build prints under the budget: each language's first screen (firstScreenBytes) and the room left in
 * the fullest one, `low` when that is under FIRST_SCREEN_LOW_ROOM. The room is what the next first-screen change may
 * add; a change that needs more takes bytes off the first screen first (docs/web/web.md, "Running it").
 */
export function firstScreenRoom(bytes: [string, number][], budget = FIRST_SCREEN_BUDGET): { line: string; low: boolean } {
  const [fullest, most] = bytes.reduce((a, b) => (b[1] > a[1] ? b : a));
  const room = budget - most;
  const n = (v: number) => v.toLocaleString("en-US");
  const all = bytes.map(([lang, v]) => `${lang} ${n(v)} B`).join(", ");
  const low = room < FIRST_SCREEN_LOW_ROOM;
  return { line: `startup-preload: first screen ${all}; ${n(room)} B left under FIRST_SCREEN_BUDGET (${fullest})${low ? ", under FIRST_SCREEN_LOW_ROOM: take bytes off the first screen before adding to it" : ""}`, low };
}

/** The build's error when any language's first screen (firstScreenBytes) is over `budget`, else undefined. */
export function firstScreenOverBudget(bytes: [string, number][], budget = FIRST_SCREEN_BUDGET): string | undefined {
  const over = bytes.filter(([, n]) => n > budget);
  if (!over.length) return undefined;
  const all = bytes.map(([lang, n]) => `${lang} ${n} B`).join(", ");
  return `startup-preload: the ${over.map(([lang]) => lang).join(", ")} first screen is over its budget of ${budget} B (${all}; src/startup-preload.ts FIRST_SCREEN_BUDGET, counted by firstScreenBytes)`;
}

/**
 * The most the idle tier may send (src/lib/lazy-page.ts: the chunks of each `lazyPage(…, { idle: true })` and what they
 * import beyond the first screen), in bytes as gzipBytes counts them (vite.config.ts startupPreload fails the build past
 * it): 29,143 B at #2798 plus 3% (28,085 B at #2880; #2798 moved ModelIdField, which the model popover now shares with
 * the idle ModelPicker, out of the first screen into a chunk of its own), and 64 B for managing launchers from the
 * pickers (#4389's controls took the idle tier 8 B over; take it back if those controls get lighter), and 13,113 B for
 * the extension view renderer (#3614: it left the first screen for the idle tier, which went from 18,615 B to 31,728 B;
 * 31,802 B after merging main). And 535 B for the composer's + / @ panel (#5324, 2026-11-12: ComposerPanel, which
 * took the @ menu's rows off the first screen, beside #5325's / palette and #5327's queue panel, which together left
 * less room than it needs: 43,729 B merged with main at f18875688; 43,809 B with the review's fixes merged with main at
 * 0fef425ef, and 31 B over that so the next main does not tip it). It stays idle: a typed @ opens it at once.
 * And 183 B for the single-category stacked bar's share predicate and horizontal segmented renderer (#5386,
 * 2026-11-12: 43,782 B measured on main before the change, 43,965 B after; the chart chunk is idle by contract —
 * code-views-lazy.test enforces `idle: true` — so the renderer has no intent tier to move into).
 * And 35 B of minifier drift (2026-11-13): #5534 left no room, and by bdbfabd69 the idle tier sent 43,969 B with none
 * of its pages (ComposerPanel, QueuePanel, SlashPalette, ImageEditor, ViewBody, ExtensionChart, Palette) changed since,
 * only the code they share with other chunks (#5760); 31 B of room so the next main does not tip it.
 * And 475 B for finding a Persona chat by what was said in it in ⌘K (#6283, 2026-11-13: 43,846 B on main at 39b5b57403,
 * 44,444 B after, +598 B, of which 154 B fit the room main had: the merge of the manager's results into the palette's
 * rows and the matched sentence as a row's second line; the search itself, lib/assistant-search.ts, loads on the first
 * query typed, off the idle tier; 31 B of room so the next main does not tip it). And 108 B for the same main
 * with the lead row-title restoration (#6400: 44,583 B after merging it, +108 B, of which 0 B fit the old room).
 * The foldable sidebar Recent (#6228) adds nothing to this tier: the fold stays on the first screen. Later mains then
 * drifted past 44,583 through shared-chunk minifier growth alone (#6512/#6521/#6528 changed no idle page's own source):
 * on main at d65cad3b4 the idle pages send 44,718 B, 135 B over this ceiling with only the queue panel still idle.
 * And moving the queue panel off the idle tier (#6571, 2026-11-13): QueueStrip renders QueuePanel only while a message
 * is queued (ChatView hasQueue), so preloading it at idle on every cold start never served a first open; it now loads
 * on intent like the other lazy pages. That takes the tier to 40,467 B measured on this machine, 4,116 B under the
 * ceiling, so the budget is held here at 44,583 B — not raised to cover the drift, which the move removes instead.
 * #6542's model-pill re-read adds ~4 B to this tier, still far under the cap after the queue-panel reduction.
 * Every other lazy page loads on intent, not at idle; raise it on purpose, in the change that needs it.
 * And 432 B for the image editor's fit geometry (#8746, 2026-11-16): the chat's image viewer left the first screen for
 * a chunk loaded on its first open (views/chat/LazyImageViewer.tsx), which took en 476,644 B to 474,342 B, ja 479,772 B
 * to 477,462 B and zh-Hans 477,452 B to 475,146 B against main at 3765c39bcb. The idle editor's fitIn / fitRect had
 * come with the viewer on the first screen; they now come at idle in lib/image-fit.ts (44,543 B to 44,975 B), so the
 * bytes a cold start fetches before idle still go down by about 1.9 KB. Main's 47 B of room is kept on top.
 * And 34 B of minifier drift (#8778, 2026-11-16): main at 168f8311e sent 45,025 B at idle, 3 B over, with no idle page's
 * own source changed; #8778's provider metadata adds nothing of its own there (its code is in the Settings chunk: 45,023 B
 * with it). 31 B of room so the next main does not tip it.
 * And 31 B of minifier drift (#9069, 2026-11-16): hiding the built-in assistant from the switcher adds nothing at idle
 * (its code is in the Settings chunk and the locale catalogs). A CI build of the PR merged with main measured 45,500 B,
 * 8 B over; a local build of the same tree measured 45,476 B against main's 45,478 B, each of the 11 idle files within
 * 5 B of main's either way (the chunk names they import). 31 B of room on top of the CI figure.
 */
// Preserve the 7 B feature allowance while retaining main's queue-panel idle reduction.
// Conversation-deletion guards, file retirement and open-search invalidation (#6172):
// main 86a706664 measures 44,981 B with identical Node/dependencies; integrated tree measured 45,492 B.
// The 511 B increment consumes 75 B of existing room, adding exactly 436 B. Intent-loading and check logic stay intact.
// And 291 B for UI version 8 (#9158): main b802a4d51c sends 45,484 B at idle, the same tree with #9158 45,775 B, in one
// environment. The idle renderer (ViewBody.tsx) gains a section's rows side by side in Run, the head hook and the
// lazy registration of ViewBody8.tsx; the columns grid and the section heads themselves (1.2 KB) load on intent with
// the first view that has them. Main's 47 B of room is kept on top.
// And 241 B for UI version 9 (#9337): main 2491fb513 sends 45,762 B at idle, the same tree with #9337 46,003 B, in one
// environment. The idle renderer (ViewBody6.tsx's TileView) gains a gauge tile's label and meter role, the ring's
// space while it loads and the lazy registration of ViewBody9.tsx; the ring itself (about 1 KB) loads on intent with
// the first tile that has a gauge. Main's 60 B of room is kept on top.
// UI version 10 (#9727) takes 166 B of idle room: main b90d75a42 sends 44,626 B at idle, the same tree with #9727
// 44,792 B, in one environment. The idle renderer (ViewBody.tsx) gains the graph's case with its Suspense fallback of
// the kept height and the lazy registration of ViewBody10.tsx (whose chunk name it embeds), and the launcher sections
// (ExtensionView.tsx's chunk) gain the owner's context provider; the canvas itself (about 8 KB) loads on intent with
// the first view that has a graph. Main had 1,437 B of room; the budget stays.
// And 12 B for the Agents list's Filter switch (#13727): the idle Palette's "Filter Agents list" row (palette/
// SavedSearchesDialog.tsx) draws only while Settings > Agents has the list's Filter on (useShowFilter), which is the
// tier's whole growth: this tree merged with main measured 46,065 B against the 46,063 B budget, 2 B over. 10 B of room on top.
// And 26 B for the stalled-background hint (#13744): the idle sidebar rows (SidebarRows.tsx) draw "Background commands
// quiet for N h" under a Working row whose background work has stalled (ui/BackgroundStallHint.tsx). Main 7b023aa70 with
// #13842 sends 46,091 B at idle, 16 B over; 10 B of room on top.
// And 33 B for Dashboards phase 2 watch rendering (#13020): the idle renderer (ViewBody.tsx) wraps a lone metric's
// label and detail instead of truncating and caps a row's value at max-w-[45%]. Core-only build on main 9760656350
// with #13020 sends 46,124 B at idle (23 B over); 10 B of room on top.
export const IDLE_TIER_BUDGET = 46_134;

const gzip = promisify(gzipCompress);

/** What the built-in hub sends for `files` (paths below `dir`, none with a brotli copy) once the release precompressed
 *  them (server/internal/webasset): each one gzipped at the best level, or the file itself under BROTLI_MIN_SIZE. Build
 *  time only. */
export async function gzipBytes(dir: string, files: string[]): Promise<number> {
  const sizes = await Promise.all(files.map(async (file) => {
    const data = await readFile(join(dir, file));
    return data.length < BROTLI_MIN_SIZE ? data.length : (await gzip(data, { level: 9 })).length;
  }));
  return sizes.reduce((sum, n) => sum + n, 0);
}

// The arguments of the call whose "(" ends just before `from` in `code`, as written and split at its top-level commas;
// null when it never closes (not a call).
function callArgs(code: string, from: number): string[] | null {
  const args: string[] = [];
  let depth = 0;
  let start = from;
  for (let i = from; i < code.length; i++) {
    const c = code[i];
    if (c === '"' || c === "'" || c === "`") {
      for (i++; i < code.length && code[i] !== c; i++) if (code[i] === "\\") i++;
    } else if ("([{".includes(c)) depth++;
    else if (")]}".includes(c) && depth-- === 0) return [...args, code.slice(start, i)].filter((a) => a.trim());
    else if (c === "," && !depth) {
      args.push(code.slice(start, i));
      start = i + 1;
    }
  }
  return null;
}

/**
 * The modules a source file (`code`) loads at idle: the "x" of each `lazyPage(() => import("x")…, { idle: true })`, as
 * written, wherever `idle` stands among its options. Throws on a lazyPage marked idle whose loader is written another
 * way, and on one whose options are not an object literal: either would load at idle without counting against
 * IDLE_TIER_BUDGET. Build time only.
 */
export function idleImports(code: string): string[] {
  const specs: string[] = [];
  for (const m of code.matchAll(/(?<!function\s*)\blazyPage\(/g)) { // each call, not lazyPage's own definition
    const [load, opts] = callArgs(code, m.index + m[0].length) ?? [];
    if (opts === undefined) continue;
    const call = `lazyPage(${load.trim().slice(0, 60)}…)`;
    if (!opts.trim().startsWith("{")) throw new Error(`${call}: write its options as an object literal, so the idle tier's budget sees whether it is idle`);
    if (!/\bidle\b(?!\s*:\s*false\b)/.test(opts)) continue;
    const spec = /^\(\s*\)\s*=>\s*import\(\s*(["'])([^"'`]+)\1\s*\)/.exec(load.trim())?.[2];
    if (!spec) throw new Error(`${call} is idle, but its loader is not \`() => import("…")\`, so the idle tier's budget cannot count it`);
    specs.push(spec);
  }
  return specs;
}

/**
 * What the first screen's chunks (`code`) read of the client catalog (`keys`, each `namespace.name`) that `startup`
 * (src/i18n/startup-namespaces.json, src/i18n/startup-keys.ts) leaves to the rest, as the entries to add, sorted. A
 * key literal ("ns.name", 'ns.name' or `ns.name…) may be completed by a template or a wrapper's suffix, so it reads
 * every key that begins with it: it asks for `ns.name` when any of those is left out, and for the whole namespace
 * when it holds no name ("ns." + x). Those keys could be read before the rest of the catalog is in
 * (src/i18n/index.ts startI18n), so the build fails on any. Build time only.
 */
export function startupGaps(code: string[], keys: Iterable<string>, startup: readonly string[]): string[] {
  const inStartup = startupMatcher(startup);
  const byNamespace = new Map<string, string[]>();
  for (const key of keys) {
    const ns = key.slice(0, key.indexOf("."));
    const list = byNamespace.get(ns);
    if (list) list.push(key);
    else byNamespace.set(ns, [key]);
  }
  const gaps = new Set<string>();
  for (const c of code) {
    for (const [, ns, name] of c.matchAll(/["'`]([a-z0-9][a-z0-9-]*)\.(\w*)/g)) {
      const start = `${ns}.${name}`;
      if (byNamespace.get(ns)?.some((key) => key.startsWith(start) && !inStartup(key))) gaps.add(name ? start : ns);
    }
  }
  // An entry another one already takes ("ns" over "ns.a", "ns.a" over "ns.ab") is left out.
  const covers = (wider: string, entry: string) => wider !== entry && (wider.includes(".") ? entry.startsWith(wider) : entry.startsWith(`${wider}.`));
  return [...gaps].filter((entry) => ![...gaps].some((wider) => covers(wider, entry))).sort();
}
