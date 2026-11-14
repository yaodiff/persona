// The inline <head> script vite.config.ts's startup-preload plugin writes into index.html, the brotli copies it
// writes of the files that script and the page load, and its checks of the startup namespaces and the first screen's
// size. It lives here, with no imports of the client code, so the config
// can load it and a test can check it against src/i18n/index.ts: the languages it has catalogs for (LANGS) and how it
// picks one (preferredLanguage) must stay the same as the client's.
import { readdirSync, readFileSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";
import ts from "typescript";
import { brotliCompress, constants, gzip as gzipCompress } from "node:zlib";
import { startupMatcher } from "./i18n/startup-keys";

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
  screen: string[];
  catalogs: Record<string, string>;
  /** language subtag → language code (each _meta.json's matches) */
  matches: Record<string, string>;
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
  var screen = /^\\/screen\\/[^/]/.test(path);
  [files.catalogs[lang]].concat(files.shared, screen ? files.screen : files.app).forEach(add);
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
// views/knowledge/minutes-overview.css) named in the app's preload map, and Button passing aria-controls and aria-label
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
export const FIRST_SCREEN_BUDGET = 480_639;

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
 */
// Preserve the 7 B feature allowance while retaining main's queue-panel idle reduction.
export const IDLE_TIER_BUDGET = 44_590;

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
