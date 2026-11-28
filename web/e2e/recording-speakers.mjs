// Synthetic Knowledge recording detail with hub-separated speakers (#17675), no live hub.
// Run against an already running Vite server:
//   BASE_URL=http://127.0.0.1:5792 PERSONA_E2E_CHROMIUM=<chrome> SHOTS_DIR=/tmp/spk/shots node web/e2e/recording-speakers.mjs
// Set E2E_ONLY=none to run only the same meeting without speakers (before/after comparison).
// Checks: every reliably timed line shows a speaker; immediate (<2 s) naming updates transcript and minutes;
// Undo restores; same-name auto-merge and Unmerge returning unnamed; single-confirmation Delete; every state
// (waiting, ready/ok, ready/low, failed reasons, cleared, trimmed); read-only detail (module off); keyboard Tab /
// Enter / Esc in the naming dialog; screen-reader labels ("?" -> possibly, hidden neutral avatars, text states);
// page/card/line overflow and single-line buttons; zh-Hans, en and ja at 1440 and 390, light and dark, Large text.
// A 10-minute watchdog ends the run with a verdict.
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:5792");
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback Vite server");
assert.ok(
  ["http:", "https:"].includes(base.protocol) &&
    !base.username &&
    !base.password &&
    base.pathname === "/" &&
    !base.search &&
    !base.hash,
  "Use an origin without credentials or a route",
);
const executablePath = process.env.PERSONA_E2E_CHROMIUM || process.env.CHROME_BIN;
assert.ok(executablePath, "Set PERSONA_E2E_CHROMIUM or CHROME_BIN");
const output = resolve(process.env.SHOTS_DIR || "/tmp/spk/shots");
await mkdir(output, { recursive: true });

const id = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb";
const detailPath = `/kb/recordings/${id}`;
const speakersEditPath = `/recordings/${id}/speakers`;

const MINOR_SEC = 20.0;
const EPS = 1e-9;

// Synthetic copy per language (strictly invented names: Ada, Grace, Bo, Chen).
const WORDS = {
  "zh-Hans": {
    title: "导出方案评审 · 发言人示例",
    where: "示例会议室 2",
    headline: "导出上限暂按单条计算；周报默认开关待下周确认",
    conclusions: [
      ["导出按单条保留 4 MB 上限，超限给出提示", "4 MB", 312],
      ["周报默认开启或关闭留待下周评审决定", "下周评审", 900],
    ],
    groups: [
      [
        "导出与通知",
        [
          [
            "导出上限",
            "progress",
            [
              ["超过 4 MB 的单个条目会给出明确提示", "4 MB", 312],
              ["本版导出上限按单个条目计算", "单个条目", 600, true],
            ],
          ],
          [
            "周报提醒",
            "schedule",
            [
              ["周报邮件默认开关尚未达成一致", "默认开关", 900],
              ["周五前补充两套方案的说明文档", "周五前", 1200],
            ],
          ],
        ],
      ],
    ],
    segments: [
      { start: 120, end: 185, text: "今天先对导出上限和周报默认设置做一次收口。", speaker: "s1" },
      { start: 312, end: 420, text: "按单个条目限制更稳妥，不会因为一个大附件把整份导出拖垮。", speaker: "s1" },
      { start: 510, end: 590, text: "如果按整份录音算，规则写进帮助页会更容易解释。", speaker: "s2" },
      { start: 600, end: 690, text: "这一版先按单条计算，同时补上清晰的超限提示。", speaker: "s1" },
      { start: 780, end: 840, text: "我也同意先说明整份录音的计算方式，方便后续对照。", speaker: "s3" },
      { start: 900, end: 980, text: "周报邮件建议默认开启，方便大家回顾上周决定。", speaker: "s2" },
      { start: 1050, end: 1110, text: "不过新用户没订阅前收到邮件可能会觉得打扰。", speaker: "s3", speaker_unsure: true },
      { start: 1200, end: 1209, text: "我这边记录一下，周五前整理好文档。", speaker: "s4" },
    ],
    debates: [
      {
        question: "导出上限按单条还是按整份录音计算？",
        topic: "导出上限",
        status: "settled",
        at: 312,
        positions: [
          { stance: "按单条算，避免一个大附件拖垮整次导出", at: 312, speaker: "s1" },
          {
            stance: "按整份录音算，规则更容易解释",
            at: 510,
            speaker: "s2",
            also: [{ speaker: "s3", at: 780 }],
          },
        ],
        outcome: { text: "本版先按单条计算并给出超限提示", at: 600, decision: 0 },
      },
      {
        question: "周报邮件是否默认开启？",
        status: "open",
        at: 900,
        positions: [
          { stance: "默认开启，便于回顾决定", at: 900, speaker: "s2" },
          { stance: "默认关闭，减少未订阅打扰", at: 1050, speaker: "s3", speaker_unsure: true, who: "Chen" },
        ],
      },
    ],
    actions: [{ id: "a1", who: "Bo", what: "周五前补充两套方案的说明文档", by: "周五前", at: 1200 }],
    open: [{ text: "周报邮件是否默认开启？", at: 900 }],
  },
  en: {
    title: "Export review · speakers sample",
    where: "Example room 2",
    headline: "Export cap stays per item; weekly digest default stays open",
    conclusions: [
      ["Export keeps the 4 MB per-item cap with a clear warning", "4 MB", 312],
      ["Weekly digest default moves to next week's review", "next week", 900],
    ],
    groups: [
      [
        "Export and digest",
        [
          [
            "Export cap",
            "progress",
            [
              ["Items over 4 MB show a clear warning on export", "4 MB", 312],
              ["Ship the cap per item for this release", "per item", 600, true],
            ],
          ],
          [
            "Weekly digest",
            "schedule",
            [
              ["The default for the weekly digest is still open", "still open", 900],
              ["Draft notes for both options by Friday", "Friday", 1200],
            ],
          ],
        ],
      ],
    ],
    segments: [
      { start: 120, end: 185, text: "Let's settle the export cap and the weekly digest default today.", speaker: "s1" },
      { start: 312, end: 420, text: "Counting per item keeps one large attachment from failing the whole export.", speaker: "s1" },
      { start: 510, end: 590, text: "Counting per recording is easier to explain in the help text.", speaker: "s2" },
      { start: 600, end: 690, text: "Let's keep per item for this release and add a clear warning.", speaker: "s1" },
      { start: 780, end: 840, text: "I also like documenting the per-recording option for comparison.", speaker: "s3" },
      { start: 900, end: 980, text: "Defaulting the weekly digest on helps people review last week's decisions.", speaker: "s2" },
      { start: 1050, end: 1110, text: "Unsolicited emails before opt-in could still feel noisy for new users.", speaker: "s3", speaker_unsure: true },
      { start: 1200, end: 1209, text: "Noted, I'll write up the short comparison before Friday.", speaker: "s4" },
    ],
    debates: [
      {
        question: "Should the export cap count items or recordings?",
        topic: "Export cap",
        status: "settled",
        at: 312,
        positions: [
          { stance: "Per item: one large attachment should not fail the whole export", at: 312, speaker: "s1" },
          {
            stance: "Per recording: easier to explain",
            at: 510,
            speaker: "s2",
            also: [{ speaker: "s3", at: 780 }],
          },
        ],
        outcome: { text: "Per item for this release with a clear warning", at: 600, decision: 0 },
      },
      {
        question: "Should the weekly digest default to on?",
        status: "open",
        at: 900,
        positions: [
          { stance: "Default on so people see the recap", at: 900, speaker: "s2" },
          { stance: "Default off to avoid unsolicited email", at: 1050, speaker: "s3", speaker_unsure: true, who: "Chen" },
        ],
      },
    ],
    actions: [{ id: "a1", who: "Bo", what: "Write up both options before Friday", by: "Friday", at: 1200 }],
    open: [{ text: "Should the weekly digest default to on?", at: 900 }],
  },
  ja: {
    title: "エクスポート検討・話者サンプル",
    where: "サンプル会議室 2",
    headline: "上限は項目ごとで進め、週次ダイジェストの既定値は次回判断",
    conclusions: [
      ["項目ごとに 4 MB の上限を設け、超過時は警告を出す", "4 MB", 312],
      ["週次ダイジェストの既定値は来週のレビューで決める", "来週", 900],
    ],
    groups: [
      [
        "エクスポートと通知",
        [
          [
            "エクスポート上限",
            "progress",
            [
              ["4 MB を超える項目には明確な警告を表示する", "4 MB", 312],
              ["今版では項目ごとに上限を計算する", "項目ごと", 600, true],
            ],
          ],
          [
            "週次ダイジェスト",
            "schedule",
            [
              ["週次ダイジェストの既定値はまだ合意していない", "既定値", 900],
              ["金曜までに両案の比較メモをまとめる", "金曜まで", 1200],
            ],
          ],
        ],
      ],
    ],
    segments: [
      { start: 120, end: 185, text: "今日はエクスポート上限と週次ダイジェストの既定値を整理します。", speaker: "s1" },
      { start: 312, end: 420, text: "項目ごとに制限すれば、大きな添付1件で全体が失敗しません。", speaker: "s1" },
      { start: 510, end: 590, text: "録音全体で数える方がヘルプでは説明しやすいと思います。", speaker: "s2" },
      { start: 600, end: 690, text: "今版は項目ごとにして、分かりやすい警告を添えましょう。", speaker: "s1" },
      { start: 780, end: 840, text: "録音全体案の比較も残しておくと後で振り返りやすいです。", speaker: "s3" },
      { start: 900, end: 980, text: "週次ダイジェストは既定でオンにすると先週の決定を追いやすいです。", speaker: "s2" },
      { start: 1050, end: 1110, text: "ただ登録前にメールが届くと通知過多に感じるかもしれません。", speaker: "s3", speaker_unsure: true },
      { start: 1200, end: 1209, text: "承知しました、金曜までに比較メモを作っておきます。", speaker: "s4" },
    ],
    debates: [
      {
        question: "エクスポート上限は項目ごとか録音全体か？",
        topic: "エクスポート上限",
        status: "settled",
        at: 312,
        positions: [
          { stance: "項目ごとにして1件の大きな添付で全体を止めない", at: 312, speaker: "s1" },
          {
            stance: "録音全体の方が説明しやすい",
            at: 510,
            speaker: "s2",
            also: [{ speaker: "s3", at: 780 }],
          },
        ],
        outcome: { text: "今版は項目ごとにし、明確な警告を添える", at: 600, decision: 0 },
      },
      {
        question: "週次ダイジェストを既定でオンにするか？",
        status: "open",
        at: 900,
        positions: [
          { stance: "既定でオンにして決定事項を振り返りやすくする", at: 900, speaker: "s2" },
          { stance: "既定でオフにして通知過多を避ける", at: 1050, speaker: "s3", speaker_unsure: true, who: "Chen" },
        ],
      },
    ],
    actions: [{ id: "a1", who: "Bo", what: "金曜までに両案の比較メモをまとめる", by: "金曜まで", at: 1200 }],
    open: [{ text: "週次ダイジェストを既定でオンにするか？", at: 900 }],
  },
};

function baseStats() {
  return {
    s1: [420.0, 14],
    s2: [180.0, 7],
    s3: [95.0, 4],
    s4: [9.0, 1],
  };
}

function basePeople() {
  return [
    { id: "s1", name: "Ada", seconds: 420.0, lines: 14, samples: [120, 312, 600] },
    { id: "s2", seconds: 180.0, lines: 7, samples: [510, 900] },
    { id: "s3", name: "Bo", seconds: 95.0, lines: 4, samples: [780, 1050] },
    { id: "s4", minor: true, seconds: 9.0, lines: 1, samples: [1200] },
  ];
}

function buildDetail(lang, mode = "ready", { reason = "too_long", readOnly = false } = {}) {
  const w = WORDS[lang];
  const line = ([text, strong, at, decision]) => ({
    text,
    strong: [strong],
    at,
    ...(decision === true ? { decision: true } : {}),
  });
  const groups = w.groups.map(([title, topics]) => ({
    title,
    topics: topics.map(([topic, icon, bullets]) => ({ title: topic, icon, bullets: bullets.map(line) })),
  }));
  const all = groups.flatMap((g) => g.topics.flatMap((t) => t.bullets));
  const detail = {
    kind: "recordings",
    id,
    path: `recordings/${id}`,
    ...(readOnly ? { paused: "module_off" } : {}),
    meta: {
      title: w.title,
      kind: "meeting",
      complete: true,
      started_at: "2026-11-10T14:00:00+08:00",
      duration: 1800,
      device: "phone",
      language: lang,
      revision: 3,
      minutes_state: "ready",
      minutes_asked: true,
      ...(mode === "trimmed" ? { trimmed_at: "2026-11-26T00:00:00Z" } : {}),
    },
    summary: w.headline,
    segments: mode === "trimmed"
      ? []
      : w.segments.map((seg) =>
          mode === "none"
            ? { start: seg.start, end: seg.end, text: seg.text }
            : structuredClone(seg),
        ),
    minutes: {
      version: 2,
      revision: 3,
      generated_at: "2026-11-10T06:31:00Z",
      title: w.title,
      when: "2026-11-10 14:00–14:30",
      where: w.where,
      attendees: ["Ada", "Grace", "Bo", "Chen"],
      points: all.filter((b) => !b.decision).map((b) => ({ text: b.text, at: b.at })),
      decisions: all.filter((b) => b.decision).map((b) => ({ text: b.text, at: b.at })),
      actions: structuredClone(w.actions),
      open: structuredClone(w.open),
      overview: {
        headline: w.headline,
        conclusions: w.conclusions.map(([text, strong, at]) => ({ text, strong: [strong], at })),
        groups,
      },
      debates: w.debates.map((d) => ({
        ...structuredClone(d),
        positions: d.positions.map((p) => {
          if (mode !== "none") return structuredClone(p);
          const clean = { stance: p.stance, at: p.at };
          if (p.who) clean.who = p.who;
          return clean;
        }),
      })),
      markdown: `# ${w.title}\n\n${w.headline}\n`,
    },
  };

  if (mode === "ready" || mode === "low" || mode === "trimmed") {
    detail.speakers = {
      rev: 3,
      state: "ready",
      reason: "",
      quality: mode === "low" ? "low" : "ok",
      source: "hub",
      people: basePeople().map((p) => (mode === "trimmed" ? { ...p, samples: undefined } : p)),
      candidates: ["Ada", "Grace", "Bo", "Chen"],
      spans: mode === "trimmed" ? [] : [[120, 420, "s1"], [510, 590, "s2"], [780, 840, "s3"], [1200, 1209, "s4"]],
      covered: mode === "trimmed" ? [] : [[0, 1800]],
      trimmed: mode === "trimmed",
    };
  } else if (mode === "waiting") {
    detail.speakers = {
      rev: 1,
      state: "waiting",
      reason: "",
      quality: "ok",
      done: 720,
      end: 1800,
      people: [],
      candidates: [],
      spans: [],
      covered: [],
      trimmed: false,
    };
  } else if (mode === "failed") {
    detail.speakers = {
      rev: 2,
      state: "failed",
      reason,
      quality: "ok",
      people: [],
      candidates: [],
      spans: [],
      covered: [],
      trimmed: false,
    };
  } else if (mode === "cleared") {
    detail.speakers = {
      rev: 4,
      state: "cleared",
      reason: "",
      quality: "ok",
      people: [],
      candidates: [],
      spans: [],
      covered: [],
      trimmed: false,
    };
  }
  return detail;
}

/** Hub-equivalent edit engine matching spec/fixtures/speakers-names/cases.json. */
function applyHubEdit(detail, body) {
  const spk = detail.speakers;
  if (!spk) return { status: 404, json: { error: "not_found" } };
  if (body.rev !== spk.rev) return { status: 409, json: { error: "speakers_stale", message: "stale" } };
  const stats = baseStats();
  const baseSamples = Object.fromEntries(basePeople().map((p) => [p.id, p.samples ?? []]));
  let people = spk.people.map((p) => {
    const o = { id: p.id };
    if (p.name) o.name = p.name;
    if (p.me) o.me = true;
    if (p.merged_into) o.merged_into = p.merged_into;
    if (p.minor) o.minor = true;
    return o;
  });
  const canon = (sid) => {
    let cur = sid;
    const seen = new Set();
    while (!seen.has(cur)) {
      seen.add(cur);
      const p = people.find((x) => x.id === cur);
      if (!p?.merged_into) return cur;
      cur = p.merged_into;
    }
    return sid;
  };
  const mergedSecs = () => {
    const m = new Map();
    for (const [sid, st] of Object.entries(stats)) {
      const c = canon(sid);
      m.set(c, (m.get(c) ?? 0) + st[0]);
    }
    return m;
  };
  const undoGroups = [];
  let cleared = false;

  for (const op of body.ops ?? []) {
    if (op.op === "name") {
      const idx = people.findIndex((p) => p.id === op.id);
      if (idx < 0 || people[idx].merged_into) return { status: 400, json: { error: "invalid" } };
      const me = op.me === true;
      const targetName = (me && !String(op.name ?? "").trim() ? "Grace" : String(op.name ?? "")).trim();
      const oldName = people[idx].name ?? "";
      const oldMe = people[idx].me === true;
      const otherIdx = targetName ? people.findIndex((p, i) => i !== idx && !p.merged_into && p.name === targetName) : -1;
      const prevOwnerIdx = me ? people.findIndex((p, i) => i !== idx && i !== otherIdx && !p.merged_into && p.me) : -1;
      const nameOp = (sid, n, m) => (m ? { op: "name", id: sid, name: n, me: true } : { op: "name", id: sid, name: n });
      if (otherIdx < 0) {
        const ug = [];
        if (prevOwnerIdx >= 0) {
          delete people[prevOwnerIdx].me;
          ug.push(nameOp(people[prevOwnerIdx].id, people[prevOwnerIdx].name ?? "", true));
        }
        if (oldName !== targetName || oldMe !== me) {
          if (targetName) people[idx].name = targetName; else delete people[idx].name;
          if (me) people[idx].me = true; else delete people[idx].me;
          ug.unshift(nameOp(op.id, oldName, oldMe));
        }
        if (ug.length) undoGroups.push(ug);
      } else {
        const secs = mergedSecs();
        const [fromIdx, intoIdx] = (secs.get(people[idx].id) ?? 0) > (secs.get(people[otherIdx].id) ?? 0) + EPS ? [otherIdx, idx] : [idx, otherIdx];
        const fromId = people[fromIdx].id, intoId = people[intoIdx].id;
        const otherOldMe = people[otherIdx].me === true;
        const children = people.filter((p) => p.merged_into === fromId).map((p) => p.id);
        const mergedMe = me || oldMe || otherOldMe;
        let prevRestore = null;
        if (prevOwnerIdx >= 0) {
          delete people[prevOwnerIdx].me;
          prevRestore = nameOp(people[prevOwnerIdx].id, people[prevOwnerIdx].name ?? "", true);
        }
        people[intoIdx].name = targetName;
        if (mergedMe) people[intoIdx].me = true; else delete people[intoIdx].me;
        delete people[fromIdx].name; delete people[fromIdx].me; delete people[fromIdx].minor;
        people[fromIdx].merged_into = intoId;
        for (const p of people) if (p.merged_into === fromId) p.merged_into = intoId;
        const ug = [];
        if (fromIdx === idx) {
          ug.push({ op: "unmerge", id: fromId });
          for (const cid of children) ug.push({ op: "unmerge", id: cid }, { op: "merge", from: cid, into: fromId });
          if (oldName || oldMe) ug.push(nameOp(fromId, oldName, oldMe));
          if (mergedMe !== otherOldMe) ug.push(nameOp(intoId, targetName, otherOldMe));
        } else {
          ug.push(nameOp(intoId, oldName, oldMe), { op: "unmerge", id: fromId });
          for (const cid of children) ug.push({ op: "unmerge", id: cid }, { op: "merge", from: cid, into: fromId });
          ug.push(nameOp(fromId, targetName, otherOldMe));
        }
        if (prevRestore) ug.push(prevRestore);
        undoGroups.push(ug);
      }
    } else if (op.op === "merge") {
      const fromIdx = people.findIndex((p) => p.id === op.from);
      const intoId = canon(op.into);
      const children = people.filter((p) => p.merged_into === op.from).map((p) => p.id);
      const oldName = people[fromIdx].name ?? "", oldMe = people[fromIdx].me === true;
      people[fromIdx].merged_into = intoId;
      delete people[fromIdx].name; delete people[fromIdx].me; delete people[fromIdx].minor;
      for (const p of people) if (p.merged_into === op.from) p.merged_into = intoId;
      const ug = [{ op: "unmerge", id: op.from }];
      for (const cid of children) ug.push({ op: "unmerge", id: cid }, { op: "merge", from: cid, into: op.from });
      if (oldName || oldMe) ug.push(oldMe ? { op: "name", id: op.from, name: oldName, me: true } : { op: "name", id: op.from, name: oldName });
      undoGroups.push(ug);
    } else if (op.op === "unmerge") {
      const idx = people.findIndex((p) => p.id === op.id);
      const prev = people[idx].merged_into;
      delete people[idx].merged_into; delete people[idx].name; delete people[idx].me;
      undoGroups.push([{ op: "merge", from: op.id, into: prev }]);
    } else if (op.op === "clear") {
      people = [];
      undoGroups.length = 0;
      cleared = true;
    }
  }

  const secs = mergedSecs();
  const linesMap = new Map();
  for (const [sid, st] of Object.entries(stats)) {
    const c = canon(sid);
    linesMap.set(c, (linesMap.get(c) ?? 0) + st[1]);
  }
  const nextPeople = people.map((p) => {
    if (p.merged_into) return { id: p.id, merged_into: p.merged_into };
    const sec = secs.get(p.id) ?? 0;
    const out = { ...p, seconds: sec, lines: linesMap.get(p.id) ?? 0 };
    if (sec + EPS < MINOR_SEC) out.minor = true; else delete out.minor;
    if (!spk.trimmed) {
      const sampleSet = new Set(baseSamples[p.id] ?? []);
      for (const other of people) if (other.merged_into === p.id) for (const s of baseSamples[other.id] ?? []) sampleSet.add(s);
      out.samples = [...sampleSet].sort((a, b) => a - b).slice(0, 3);
    }
    return out;
  });

  spk.rev += 1;
  if (cleared) {
    spk.state = "cleared";
    spk.people = [];
    spk.spans = [];
    spk.covered = [];
  } else {
    spk.people = nextPeople;
  }
  const undo = [];
  for (let i = undoGroups.length - 1; i >= 0; i--) undo.push(...undoGroups[i]);
  return { status: 200, json: { speakers: structuredClone(spk), undo } };
}

const browser = await chromium.launch({
  executablePath,
  headless: true,
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});
const deadline = setTimeout(() => {
  console.error("FAIL recording speakers: timed out after 10 minutes");
  process.exit(1);
}, 600_000);

const shots = [];
let runs = 0;
let measuredNamingMs = -1;

async function openContext({ theme, lang, width, large = false, readOnly = false, detail }) {
  const s = Object.assign(
    ...(await Promise.all(
      ["knowledge-kbdetail", "recording", "common"].map(async (ns) =>
        JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/${ns}.json`, import.meta.url), "utf8")),
      ),
    )),
  );
  const context = await browser.newContext({
    viewport: { width, height: 1800 },
    colorScheme: theme,
    timezoneId: "Asia/Shanghai",
    reducedMotion: "reduce",
  });
  await context.addInitScript(
    ({ theme, lang, large, readOnly }) => {
      localStorage.setItem("persona.token", "fixture-token");
      localStorage.setItem("persona.language", lang);
      localStorage.setItem("persona.theme", theme);
      if (large) localStorage.setItem("persona.text-size", "large");
      if (readOnly) localStorage.setItem("persona.modules.off", JSON.stringify(["recording"]));
    },
    { theme, lang, large, readOnly },
  );
  const errors = [];
  await context.route("**/*", (route) =>
    new URL(route.request().url()).origin === base.origin ? route.continue() : route.abort(),
  );
  await context.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(/^\/api\/v1/, "");
    if (path === detailPath) return route.fulfill({ json: { detail } });
    if (path === speakersEditPath) {
      assert.equal(request.method(), "POST");
      const res = applyHubEdit(detail, request.postDataJSON());
      return route.fulfill({ status: res.status, json: res.json });
    }
    const data = {
      "/kb/recordings": { items: [{ kind: "recordings", id, name: detail.meta.title, relevance: 0 }], tags: [] },
      "/sessions": { sessions: [], total: 0, has_more: false, revision: "fixture" },
      "/machines": { machines: [] },
      "/interactions": { interactions: [] },
      "/assistant": { conversations: [] },
      "/todos": { todos: [] },
      "/mutes": { spaces: [], sessions: [], allowed: [] },
      "/pins": { spaces: [], sessions: [], sections: [] },
      "/agents/sessions": { sessions: [] },
      "/agents/prefs": {},
      "/managers": { managers: [{ id: "assistant", name: "Persona", builtin: true, online: true }], default: "assistant" },
      "/personas": { personas: [] },
      "/assistant/personas": { personas: [] },
      "/workspaces": { workspaces: [] },
      "/extensions": { extensions: [] },
    };
    return route.fulfill({ json: data[path] ?? {} });
  });
  await context.routeWebSocket(/\/api\/v1\/ws(?:\?|$)/, (ws) => {
    const features = readOnly
      ? ["managers", "modules"]
      : ["managers", "meeting_transcription", "meeting_speakers"];
    ws.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features, hub_name: "Example hub" }));
    ws.send(JSON.stringify({ type: "machines", machines: [] }));
    ws.onMessage((message) => {
      const event = JSON.parse(String(message));
      if (event.type === "app_ping") ws.send(JSON.stringify({ type: "pong", id: event.id }));
    });
  });

  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  page.on("pageerror", (err) => errors.push(err.message));
  await page.goto(new URL(detailPath, base).href);
  await page.getByRole("heading", { name: detail.meta.title }).first().waitFor();
  if (large) assert.equal(await page.evaluate(() => document.documentElement.dataset.textSize), "large");
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(300);
  assert.equal(await page.locator("vite-error-overlay").count(), 0);
  return { context, page, s, errors };
}

async function checkLayout(page, label, scopeSelector = "main section") {
  assert.ok(
    await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1),
    `${label}: no page horizontal overflow`,
  );
  const badButtons = await page.locator(`${scopeSelector} button:not([data-multiline-row])`).evaluateAll((buttons) => {
    const out = [];
    for (const btn of buttons) {
      const rect = btn.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) continue;
      if (!btn.textContent?.trim()) continue;
      // Full-row trigger buttons (speakers bar / minor toggle) keep their spans single-line via nowrap/truncate.
      if (btn.hasAttribute("data-speakers-open") || btn.hasAttribute("data-speakers-minor-toggle")) {
        if (rect.height > 60) out.push(`row-tall:${btn.textContent?.trim().slice(0, 30)}`);
        continue;
      }
      const style = getComputedStyle(btn);
      const range = document.createRange();
      range.selectNodeContents(btn);
      const contentW = range.getBoundingClientRect().width;
      if (style.whiteSpace !== "nowrap" || contentW > btn.clientWidth + 2) {
        out.push(`${btn.textContent?.trim().slice(0, 30)}(ws=${style.whiteSpace},cw=${Math.round(contentW)},w=${btn.clientWidth})`);
      }
    }
    return out;
  });
  assert.deepEqual(badButtons, [], `${label}: all button text fits on one line`);
  const wideLines = await page
    .locator(`${scopeSelector} [data-minutes-debate], ${scopeSelector} [data-minutes-debate] p, ${scopeSelector} [data-speaker-row], ${scopeSelector} [data-speakers-bar]`)
    .evaluateAll((els) =>
      els
        .filter((el) => el.clientWidth > 0 && el.scrollWidth > el.clientWidth + 2)
        .map((el) => `${el.tagName}:${el.textContent?.trim().slice(0, 35)}`),
    );
  assert.deepEqual(wideLines, [], `${label}: no card or line overflows its container`);
}

async function checkA11ySpeakers(page, s, label, { readOnly = false, expectUnsureAll = false } = {}) {
  // Avatars must be hidden from screen readers and strictly neutral (identical bg/color across every speaker).
  const avatarCheck = await page.locator("[data-speaker-avatar]").evaluateAll((els) => {
    const styles = new Set();
    const bad = [];
    for (const el of els) {
      if (el.getAttribute("aria-hidden") !== "true") bad.push("not-aria-hidden");
      const st = getComputedStyle(el);
      styles.add(`${st.backgroundColor}|${st.color}`);
    }
    return { count: els.length, distinctStyles: styles.size, bad };
  });
  if (avatarCheck.count > 0) {
    assert.deepEqual(avatarCheck.bad, [], `${label}: avatars hidden from screen readers`);
    assert.equal(avatarCheck.distinctStyles, 1, `${label}: all avatars share one neutral color`);
  }

  // "?" unsure marks must have role="img" and localized "possibly" aria-label.
  const wantLabel = readOnly ? s.recording_speaker_possibly : s.speaker_possibly;
  const unsureCheck = await page.locator("[data-speaker-unsure-mark]").evaluateAll((els, want) =>
    els.map((el) => ({
      text: el.textContent?.trim(),
      role: el.getAttribute("role"),
      label: el.getAttribute("aria-label"),
      ok: el.textContent?.trim() === "?" && el.getAttribute("role") === "img" && el.getAttribute("aria-label") === want,
    })),
    wantLabel,
  );
  for (const item of unsureCheck) {
    assert.ok(item.ok, `${label}: '?' mark screen-reader label mismatch: ${JSON.stringify(item)} vs ${wantLabel}`);
  }
  if (expectUnsureAll) {
    const groups = await page.locator("[data-speaker-group]").evaluateAll((els) =>
      els.map((el) => ({ id: el.getAttribute("data-speaker-group"), unsure: el.getAttribute("data-speaker-unsure"), mark: !!el.querySelector("[data-speaker-unsure-mark]") })),
    );
    assert.ok(groups.length > 0, `${label}: has speaker groups`);
    for (const g of groups) {
      assert.equal(g.unsure, "true", `${label}: low-quality group ${g.id} marked unsure`);
      assert.ok(g.mark, `${label}: low-quality group ${g.id} shows '?'`);
    }
  }
}

async function shootFull(page, width, file) {
  const tall = await page.evaluate(() => {
    const scroller =
      document.querySelector("[data-recording-minutes]")?.closest(".overflow-y-auto") ??
      document.querySelector("[data-readonly-speakers]")?.closest(".overflow-y-auto") ??
      document.querySelector("main .overflow-y-auto");
    if (!scroller) return 1400;
    return Math.ceil(scroller.getBoundingClientRect().top + scroller.scrollHeight);
  });
  await page.setViewportSize({ width, height: Math.min(Math.max(tall, 900), 16000) });
  await page.waitForTimeout(150);
  await page.screenshot({ path: join(output, `${file}.png`), fullPage: true });
  shots.push(`${file}.png`);
  await page.setViewportSize({ width, height: 1800 });
}

async function shootDialog(page, width, file) {
  await page.setViewportSize({ width, height: Math.max(width === 390 ? 844 : 1100, 900) });
  await page.waitForTimeout(150);
  await page.screenshot({ path: join(output, `${file}.png`), fullPage: false });
  shots.push(`${file}.png`);
  await page.setViewportSize({ width, height: 1800 });
}

async function runNoSpeakers(theme, lang = "zh-Hans", width = 1440) {
  const detail = buildDetail(lang, "none");
  const { context, page, errors } = await openContext({ theme, lang, width, detail });
  assert.equal(await page.locator("[data-speakers-bar]").count(), 0);
  assert.equal(await page.locator("[data-transcript-speakers]").count(), 0);
  await checkLayout(page, `none-${lang}-${theme}-${width}`);
  await shootFull(page, width, `none-${lang}-${theme}-${width}`);
  assert.deepEqual(errors, []);
  await context.close();
  runs++;
}

async function runInteractiveJourney() {
  const lang = "zh-Hans";
  const detail = buildDetail(lang, "ready");
  const { context, page, s, errors } = await openContext({ theme: "light", lang, width: 1440, detail });

  // 1. Every reliably timed line shows a speaker & debates carry initial attribution (Ada; 发言人 2、Bo).
  await page.locator("[data-speakers-bar='ready']").waitFor();
  const groupCount = await page.locator("[data-speaker-group]").count();
  assert.ok(groupCount >= 5, `expected grouped speakers, got ${groupCount}`);
  const debatePositions = await page.locator("[data-minutes-debate-position]").allTextContents();
  assert.ok(debatePositions[0].includes("Ada"), "position 1 shows Ada");
  assert.ok(debatePositions[1].includes("发言人 2") && debatePositions[1].includes("Bo"), "position 2 shows 发言人 2 and Bo");
  await checkA11ySpeakers(page, s, "interactive-initial");
  await checkLayout(page, "interactive-initial");
  await shootFull(page, 1440, "ready-zh-Hans-light-1440");

  // 2. Open Naming dialog and check keyboard Tab traversal from first to last focusable control.
  await page.locator("[data-speakers-open]").click();
  const dialog = page.locator('[role="dialog"]');
  await dialog.waitFor();
  await checkLayout(page, "naming-zh-Hans-light-1440", '[role="dialog"]');
  await checkA11ySpeakers(page, s, "naming-dialog");
  await shootDialog(page, 1440, "naming-zh-Hans-light-1440");

  const focusableCount = await dialog.evaluate((el) => {
    const sel = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), [tabindex]:not([tabindex="-1"])';
    return [...el.querySelectorAll(sel)].filter((n) => n.getClientRects().length > 0 && !n.closest("[inert]")).length;
  });
  assert.ok(focusableCount >= 8, `expected >=8 focusable controls in naming dialog, got ${focusableCount}`);
  const visitedIdx = new Set();
  for (let i = 0; i < focusableCount; i++) {
    await page.keyboard.press("Tab");
    const info = await dialog.evaluate((el) => {
      const sel = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), [tabindex]:not([tabindex="-1"])';
      const list = [...el.querySelectorAll(sel)].filter((n) => n.getClientRects().length > 0 && !n.closest("[inert]"));
      const a = document.activeElement;
      return { inside: el.contains(a), idx: list.indexOf(a) };
    });
    assert.ok(info.inside, `Tab step ${i + 1} stayed inside naming dialog`);
    assert.ok(info.idx >= 0, `Tab step ${i + 1} landed on a focusable control`);
    visitedIdx.add(info.idx);
  }
  assert.equal(visitedIdx.size, focusableCount, `Tab walked every control in naming dialog (${visitedIdx.size}/${focusableCount})`);

  // 3. Name s2 via Enter ("Grace") and measure time until BOTH transcript and minutes show "Grace" (<2000 ms).
  const s2Input = dialog.locator('[data-speaker-input="s2"]');
  await s2Input.focus();
  await s2Input.fill("Grace");
  const t0 = Date.now();
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => {
    const tEl = document.querySelector('[data-speaker-group="s2"]');
    const dEl = document.querySelectorAll("[data-minutes-debate-position]")[1];
    return !!tEl?.textContent?.includes("Grace") && !!dEl?.textContent?.includes("Grace");
  });
  measuredNamingMs = Date.now() - t0;
  assert.ok(measuredNamingMs < 2000, `naming update took ${measuredNamingMs} ms (limit 2000 ms)`);

  // 4. Undo restores "发言人 2".
  const undoBtn = dialog.locator("[data-speakers-undo-bar] button");
  await undoBtn.waitFor();
  await undoBtn.click();
  await page.waitForFunction(() => {
    const tEl = document.querySelector('[data-speaker-group="s2"]');
    const dEl = document.querySelectorAll("[data-minutes-debate-position]")[1];
    return !!tEl?.textContent?.includes("发言人 2") && !!dEl?.textContent?.includes("发言人 2");
  });

  // 5. Same-name auto-merge (click "Ada" on s2 -> merged into s1) and Unmerge returning unnamed.
  await dialog.locator('[data-speaker-row="s2"] [data-speaker-candidate="Ada"]').click();
  await dialog.locator('[data-merged-speaker="s2"]').waitFor();
  assert.equal(await dialog.locator('[data-speaker-row="s2"]').count(), 0);
  await dialog.locator('[data-merged-speaker="s2"] button').click();
  await dialog.locator('[data-speaker-row="s2"]').waitFor();
  assert.equal(await dialog.locator('[data-merged-speaker="s2"]').count(), 0);
  assert.ok((await dialog.locator('[data-speaker-row="s2"]').textContent()).includes("发言人 2"));

  // 6. Expand folded "Rarely spoke" minor speaker, then jump from a sample line.
  assert.equal(await dialog.locator('[data-speaker-row="s4"]').count(), 0);
  await dialog.locator("[data-speakers-minor-toggle]").click();
  await dialog.locator('[data-speaker-row="s4"]').waitFor();
  await dialog.locator('[data-speaker-sample="recording-line-312"]').first().click();
  await dialog.waitFor({ state: "detached" });
  await page.waitForFunction(() => document.activeElement?.id === "recording-line-312");
  assert.ok(await page.locator("#recording-line-312").evaluate((el) => el.classList.contains("bg-accent-tint")));

  // 7. Reopen Naming dialog and close with Escape.
  await page.locator("[data-speakers-open]").click();
  await dialog.waitFor();
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "detached" });

  // 8. Reopen Naming dialog -> menu -> Delete this meeting's speakers -> confirm once -> looks like no speakers.
  await page.locator("[data-speakers-open]").click();
  await dialog.waitFor();
  await dialog.locator(`button[aria-label="${s.speakers_menu}"]`).click();
  await page.getByRole("menuitem", { name: s.speakers_delete }).click();
  const confirm = page.locator('[role="dialog"]');
  await confirm.getByRole("heading", { name: s.speakers_delete_title }).waitFor();
  await confirm.getByRole("button", { name: s.speakers_delete_short, exact: true }).click();
  await confirm.waitFor({ state: "detached" });
  assert.equal(await page.locator("[data-speakers-bar]").count(), 0);
  assert.equal(await page.locator("[data-transcript-speakers]").count(), 0);

  assert.deepEqual(errors, []);
  await context.close();
  runs++;
}

async function runStateShot({ mode, lang, theme, width = 1440, large = false, reason = "too_long", openNaming = false, readOnly = false, file }) {
  const detail = buildDetail(lang, mode, { reason, readOnly });
  const { context, page, s, errors } = await openContext({ theme, lang, width, large, readOnly, detail });
  const label = file;

  if (readOnly) {
    await page.locator('[data-readonly-speakers="ready"]').waitFor();
    assert.equal(await page.locator('[data-readonly-speakers] button').count(), 0, `${label}: zero edit buttons in read-only speakers`);
    assert.equal(await page.locator('[data-readonly-transcript-speakers] button').count(), 0, `${label}: zero edit buttons in read-only transcript`);
    await checkA11ySpeakers(page, s, label, { readOnly: true });
  } else if (mode === "waiting") {
    await page.locator('[data-speakers-bar="waiting"]').waitFor();
  } else if (mode === "failed") {
    await page.locator('[data-speakers-bar="failed"]').waitFor();
    const btns = await page.locator('[data-speakers-bar="failed"] button').allTextContents();
    assert.deepEqual(btns.map((x) => x.trim()), [s.speakers_delete_short], `${label}: failed state has Delete only (no retry)`);
  } else if (mode === "cleared") {
    assert.equal(await page.locator("[data-speakers-bar]").count(), 0);
    assert.equal(await page.locator("[data-transcript-speakers]").count(), 0);
  } else if (mode === "low") {
    await page.locator("[data-speakers-low-quality]").waitFor();
    await checkA11ySpeakers(page, s, label, { expectUnsureAll: true });
    // Position 1 has speaker="s1" and no who -> must carry NO speaker attribution on low quality.
    const pos0 = await page.locator("[data-minutes-debate-position]").first().textContent();
    assert.ok(!pos0.includes("Ada") && !pos0.includes("Speaker 1") && !pos0.includes("发言人 1") && !pos0.includes("話者 1"), `${label}: low quality attributes no speaker to position 1`);
  } else if (mode === "ready") {
    await page.locator('[data-speakers-bar="ready"]').waitFor();
    await checkA11ySpeakers(page, s, label);
  } else if (mode === "trimmed") {
    await page.locator('[data-speakers-bar="ready"]').waitFor();
  }

  if (openNaming) {
    await page.locator("[data-speakers-open]").click();
    const dialog = page.locator('[role="dialog"]');
    await dialog.waitFor();
    if (mode === "trimmed") {
      assert.equal(await dialog.locator("[data-speaker-samples]").count(), 0, `${label}: trimmed has no samples`);
      assert.ok((await dialog.locator("[data-speaker-positions]").count()) > 0, `${label}: trimmed keeps attributed positions`);
      // Verify editing works on trimmed meeting too.
      const s2Input = dialog.locator('[data-speaker-input="s2"]');
      await s2Input.fill("Grace");
      await s2Input.press("Enter");
      await page.waitForFunction(() => document.querySelector('[data-speaker-row="s2"] h3')?.textContent?.includes("Grace"));
    }
    if (mode === "low") {
      await dialog.locator("[data-speakers-low-quality]").waitFor();
    }
    await checkLayout(page, label, '[role="dialog"]');
    await checkA11ySpeakers(page, s, label);
    await shootDialog(page, width, file);
  } else {
    await checkLayout(page, label);
    await shootFull(page, width, file);
  }

  assert.deepEqual(errors, []);
  await context.close();
  runs++;
}

try {
  // Always capture the "no speakers" pair (and stop early when E2E_ONLY=none).
  await runNoSpeakers("light", "zh-Hans", 1440);
  await runNoSpeakers("dark", "zh-Hans", 1440);

  if (process.env.E2E_ONLY !== "none") {
    // Full interactive workflow in zh-Hans light 1440 (also saves ready-zh-Hans-light-1440 and naming-zh-Hans-light-1440).
    await runInteractiveJourney();

    // Ready (ok) across dark, mobile 390, Large text, en, ja.
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "dark", width: 1440, file: "ready-zh-Hans-dark-1440" });
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "light", width: 390, file: "ready-zh-Hans-light-390" });
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "dark", width: 390, file: "ready-zh-Hans-dark-390" });
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "light", width: 1440, large: true, file: "ready-zh-Hans-light-1440-large" });
    await runStateShot({ mode: "ready", lang: "en", theme: "light", width: 1440, file: "ready-en-light-1440" });
    await runStateShot({ mode: "ready", lang: "en", theme: "dark", width: 1440, file: "ready-en-dark-1440" });
    await runStateShot({ mode: "ready", lang: "ja", theme: "light", width: 1440, file: "ready-ja-light-1440" });
    await runStateShot({ mode: "ready", lang: "ja", theme: "dark", width: 1440, file: "ready-ja-dark-1440" });

    // Naming page across dark, 390, Large text, en, ja.
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "dark", width: 1440, openNaming: true, file: "naming-zh-Hans-dark-1440" });
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "dark", width: 390, openNaming: true, file: "naming-zh-Hans-dark-390" });
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "light", width: 1440, large: true, openNaming: true, file: "naming-zh-Hans-light-1440-large" });
    await runStateShot({ mode: "ready", lang: "en", theme: "light", width: 1440, openNaming: true, file: "naming-en-light-1440" });
    await runStateShot({ mode: "ready", lang: "en", theme: "dark", width: 1440, openNaming: true, file: "naming-en-dark-1440" });
    await runStateShot({ mode: "ready", lang: "ja", theme: "light", width: 1440, openNaming: true, file: "naming-ja-light-1440" });
    await runStateShot({ mode: "ready", lang: "ja", theme: "dark", width: 1440, openNaming: true, file: "naming-ja-dark-1440" });

    // Waiting (with progress), three languages + dark.
    await runStateShot({ mode: "waiting", lang: "zh-Hans", theme: "light", file: "waiting-zh-Hans-light-1440" });
    await runStateShot({ mode: "waiting", lang: "zh-Hans", theme: "dark", file: "waiting-zh-Hans-dark-1440" });
    await runStateShot({ mode: "waiting", lang: "en", theme: "light", file: "waiting-en-light-1440" });
    await runStateShot({ mode: "waiting", lang: "ja", theme: "light", file: "waiting-ja-light-1440" });

    // Low quality (transcript + naming dialog), three languages + dark.
    await runStateShot({ mode: "low", lang: "zh-Hans", theme: "light", file: "low-zh-Hans-light-1440" });
    await runStateShot({ mode: "low", lang: "zh-Hans", theme: "dark", file: "low-zh-Hans-dark-1440" });
    await runStateShot({ mode: "low", lang: "en", theme: "light", file: "low-en-light-1440" });
    await runStateShot({ mode: "low", lang: "ja", theme: "light", file: "low-ja-light-1440" });

    // Failed states across reasons + three languages + dark.
    await runStateShot({ mode: "failed", reason: "too_long", lang: "zh-Hans", theme: "light", file: "failed-zh-Hans-light-1440" });
    await runStateShot({ mode: "failed", reason: "timeout", lang: "zh-Hans", theme: "dark", file: "failed-zh-Hans-dark-1440" });
    await runStateShot({ mode: "failed", reason: "engine_unavailable", lang: "en", theme: "light", file: "failed-en-light-1440" });
    await runStateShot({ mode: "failed", reason: "no_audio", lang: "ja", theme: "light", file: "failed-ja-light-1440" });
    await runStateShot({ mode: "failed", reason: "error", lang: "en", theme: "dark", file: "failed-error-en-dark-1440" });

    // Cleared state.
    await runStateShot({ mode: "cleared", lang: "zh-Hans", theme: "light", file: "cleared-zh-Hans-light-1440" });
    await runStateShot({ mode: "cleared", lang: "zh-Hans", theme: "dark", file: "cleared-zh-Hans-dark-1440" });

    // Trimmed recording (naming page still editable, stats & positions kept).
    await runStateShot({ mode: "trimmed", lang: "zh-Hans", theme: "light", openNaming: true, file: "trimmed-zh-Hans-light-1440" });
    await runStateShot({ mode: "trimmed", lang: "zh-Hans", theme: "dark", openNaming: true, file: "trimmed-zh-Hans-dark-1440" });
    await runStateShot({ mode: "trimmed", lang: "en", theme: "light", openNaming: true, file: "trimmed-en-light-1440" });
    await runStateShot({ mode: "trimmed", lang: "ja", theme: "light", openNaming: true, file: "trimmed-ja-light-1440" });

    // Module off -> Read-only detail (zero edit buttons), three languages + dark.
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "light", readOnly: true, file: "readonly-zh-Hans-light-1440" });
    await runStateShot({ mode: "ready", lang: "zh-Hans", theme: "dark", readOnly: true, file: "readonly-zh-Hans-dark-1440" });
    await runStateShot({ mode: "ready", lang: "en", theme: "light", readOnly: true, file: "readonly-en-light-1440" });
    await runStateShot({ mode: "ready", lang: "ja", theme: "light", readOnly: true, file: "readonly-ja-light-1440" });
  }

  console.log(
    `PASS recording speakers: ${shots.length} screenshots in ${output}; ${runs} browser runs; naming latency ${measuredNamingMs} ms; states, undo, merge/unmerge, delete, low quality, trimmed, read-only, keyboard and screen-reader checks passed`,
  );
  for (const file of shots) console.log(`  ${file}`);
} catch (err) {
  console.error(`FAIL recording speakers: ${err?.stack ?? err}`);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
