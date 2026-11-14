import { afterEach, describe, expect, it } from "vitest";
import {
  applyDimensionFilters,
  buildAgentFacets,
  buildEvidenceSnippet,
  compileSearchAsCode,
  DEFAULT_DIMENSION_FILTERS,
  getSearchAsCodeProvider,
  hasActiveDimensionFilters,
  matchesSearchScope,
  parseSearchQuery,
  registerSearchAsCodeProvider,
  sessionHarnessId,
  sessionHasError,
  sessionModelId,
  sessionProjectName,
  toggleFacetValue,
  type AgentDimensionFilters,
  type EvalContext,
} from "./agent-chatlist-filter";
import { agentSearchHit, conditionCount, conditionValue, conditionValues, dropConditions, hasSearchOperators, toggleCondition } from "./agent-search";
import type { Machine, Session } from "./types";

const NOW = Date.parse("2026-11-09T12:00:00Z");

const machines: Machine[] = [
  { id: "gcp", name: "Cloud Dev", online: true },
  { id: "mac", name: "Mac Studio", online: true },
];

const mkSession = (id: string, patch: Partial<Session> = {}): Session => ({
  machine_id: "gcp",
  session_id: id,
  title: `Session ${id}`,
  cwd: "/workspace/persona",
  repo_root: "/workspace/persona",
  status: "working",
  state: "working",
  agent: "claude",
  model: "claude-opus-5-5",
  updated_at: "2026-11-09T10:00:00Z",
  last_text: "Finished running tests and updated the filter strip.",
  ...patch,
});

const baseCtx: EvalContext = {
  machines,
  pins: { spaces: [], sessions: ["gcp/s-pinned"] },
  interactions: [],
  now: NOW,
};

afterEach(() => {
  registerSearchAsCodeProvider(null);
});

describe("session metadata extractors & facet building", () => {
  it("extracts harness, project, model, and error status", () => {
    const s1 = mkSession("s1", { agent: "claude-code", model: "claude-opus-5-5[1m]", repo_root: "/code/persona" });
    const s2 = mkSession("s2", { agent: "", launcher: "codex-gpt-5", model: "", repo_root: undefined, cwd: "/code/weather", turn_error: "rate_limited" });
    expect(sessionHarnessId(s1)).toBe("claude");
    expect(sessionProjectName(s1)).toBe("persona");
    expect(sessionModelId(s1)).toBe("opus 5.5 1M");
    expect(sessionHasError(s1)).toBe(false);

    expect(sessionHarnessId(s2)).toBe("codex");
    expect(sessionProjectName(s2)).toBe("weather");
    expect(sessionModelId(s2)).toBe("gpt-5");
    expect(sessionHasError(s2)).toBe(true);
  });

  it("builds facet counts across harnesses, projects, models, machines, and pins", () => {
    const list = [
      mkSession("s-pinned", { agent: "claude", model: "claude-opus-5-5", repo_root: "/code/persona", machine_id: "gcp" }),
      mkSession("s2", { agent: "codex", model: "gpt-5", repo_root: "/code/persona", machine_id: "gcp" }),
      mkSession("s3", { agent: "claude", model: "gemini-4-argon-medium", repo_root: "/code/orca", machine_id: "mac" }),
    ];
    const facets = buildAgentFacets(list, machines, baseCtx.pins);
    expect(facets.pinned).toBe(1);
    expect(facets.harnesses.map((h) => [h.id, h.count])).toEqual([
      ["claude", 2],
      ["codex", 1],
    ]);
    expect(facets.projects.map((p) => [p.id, p.count])).toEqual([
      ["persona", 2],
      ["orca", 1],
    ]);
    expect(facets.machines.map((m) => [m.id, m.count])).toEqual([
      ["gcp", 2],
      ["mac", 1],
    ]);
  });

  it("toggles facet values with Orca All/multi-select collapse semantics", () => {
    const all = ["claude", "codex"];
    const step1 = toggleFacetValue([], "claude", all);
    expect(step1).toEqual(["claude"]);
    // Selecting all available options collapses back to [] (All)
    const step2 = toggleFacetValue(step1, "codex", all);
    expect(step2).toEqual([]);
  });
});

describe("structured inline query operators & search scopes", () => {
  it("parses machine:, project:, harness:, model:, state:, is:pinned, is:error, and since: operators", () => {
    const parsed = parseSearchQuery("machine:gcp project:persona harness:claude model:opus state:working is:pinned is:error since:24h filter bug");
    expect(parsed.machines).toEqual(["gcp"]);
    expect(parsed.projects).toEqual(["persona"]);
    expect(parsed.harnesses).toEqual(["claude"]);
    expect(parsed.models).toEqual(["opus"]);
    expect(parsed.states).toEqual(["working"]);
    expect(parsed.pinned).toBe(true);
    expect(parsed.hasErrors).toBe(true);
    expect(parsed.since).toBe("24h");
    expect(parsed.terms).toEqual(["filter", "bug"]);
    expect(parsed.text).toBe("filter bug");
  });

  it("isolates search scopes (title, conversation without tools, user, assistant, errors)", () => {
    const s = mkSession("s1", {
      title: "Refactor sidebar navigation",
      asks: "Should we keep the legacy grouping?",
      last_text: "Running `vitest`\nUpdated the filter bar component cleanly.",
      turn_error: "502 Bad Gateway from upstream model",
    });

    expect(matchesSearchScope(s, ["sidebar"], "title", baseCtx)).toBe(true);
    expect(matchesSearchScope(s, ["legacy"], "title", baseCtx)).toBe(false);

    // "conversation" (Without tools) strips "Running `vitest`" tool noise
    expect(matchesSearchScope(s, ["vitest"], "conversation", baseCtx)).toBe(false);
    expect(matchesSearchScope(s, ["component"], "conversation", baseCtx)).toBe(true);

    // "user" matches user questions/goals
    expect(matchesSearchScope(s, ["legacy"], "user", baseCtx)).toBe(true);
    expect(matchesSearchScope(s, ["Gateway"], "user", baseCtx)).toBe(false);

    // "assistant" matches model prose
    expect(matchesSearchScope(s, ["cleanly"], "assistant", baseCtx)).toBe(true);

    // "errors" matches sessions with errors
    expect(matchesSearchScope(s, ["Gateway"], "errors", baseCtx)).toBe(true);
  });

  it("builds role-attributed evidence snippets with highlighted match", () => {
    const s = mkSession("s1", {
      title: "Agent Tab Upgrade",
      asks: "Please add multi-select harness filter pills",
      last_text: "Implemented composable filter bar and pinned rows.",
      turn_error: "Connection reset by peer during stream",
    });

    const userEv = buildEvidenceSnippet(s, "harness", "all", baseCtx);
    expect(userEv?.role).toBe("user");
    expect(userEv?.match.toLowerCase()).toBe("harness");

    const errEv = buildEvidenceSnippet(s, "peer", "errors", baseCtx);
    expect(errEv?.role).toBe("error");
    expect(errEv?.match.toLowerCase()).toBe("peer");
  });
});

describe("Search as Code compilation, evaluation, and provider hook", () => {
  const list = [
    mkSession("s-pinned", {
      machine_id: "gcp",
      repo_root: "/workspace/persona",
      agent: "claude",
      model: "claude-opus-5-5",
      state: "working",
      turn_error: "timeout waiting for daemon",
      updated_at: "2026-11-09T11:00:00Z",
    }),
    mkSession("s-clean", {
      machine_id: "gcp",
      repo_root: "/workspace/persona",
      agent: "codex",
      model: "gpt-5",
      state: "idle",
      updated_at: "2026-11-09T11:30:00Z",
    }),
    mkSession("s-old-mac", {
      machine_id: "mac",
      repo_root: "/workspace/orca",
      agent: "claude",
      model: "gemini-4-argon-medium",
      state: "idle",
      updated_at: "2026-10-01T00:00:00Z",
    }),
  ];

  it("compiles complex natural-language queries into executable SearchPlan expressions", () => {
    const facets = buildAgentFacets(list, machines, baseCtx.pins);
    const filters: AgentDimensionFilters = {
      ...DEFAULT_DIMENSION_FILTERS,
      query: "最近两天 gcp persona 报错 工作中",
      searchAsCode: true,
    };
    const { sessions, plan } = applyDimensionFilters(list, filters, baseCtx, facets);
    expect(plan.code).toContain('s.machine === "gcp"');
    expect(plan.code).toContain('s.project === "persona"');
    expect(plan.code).toContain("s.hasErrors");
    expect(plan.code).toContain('s.state === "working"');
    expect(plan.code).toContain('s.updatedWithin("24h")');
    expect(sessions.map((s) => s.session_id)).toEqual(["s-pinned"]);
  });

  it("evaluates user-edited custom SearchPlan code", () => {
    const filters: AgentDimensionFilters = {
      ...DEFAULT_DIMENSION_FILTERS,
      searchAsCode: true,
      customPlanCode: 'session.where(s => s.machine === "mac" && /argon/i.test(s.model))',
    };
    expect(hasActiveDimensionFilters(filters)).toBe(true);
    const { sessions } = applyDimensionFilters(list, filters, baseCtx);
    expect(sessions.map((s) => s.session_id)).toEqual(["s-old-mac"]);
  });

  it("supports pluggable SearchAsCodeProvider extension hook", () => {
    registerSearchAsCodeProvider({
      id: "ext-search-as-code",
      compile: (q) => ({
        code: `ext.plan(${JSON.stringify(q)})`,
        predicates: [(s) => s.session_id === "s-clean"],
        highlightTerms: [q],
        source: "provider",
      }),
    });
    expect(getSearchAsCodeProvider()?.id).toBe("ext-search-as-code");
    const facets = buildAgentFacets(list, machines, baseCtx.pins);
    const plan = compileSearchAsCode("find clean session", { ...DEFAULT_DIMENSION_FILTERS, searchAsCode: true }, facets);
    expect(plan.source).toBe("provider");
    expect(plan.code).toBe('ext.plan("find clean session")');
  });
});

// #4397: with Search as Code off, plain words are matched as text and never turned into hidden filters.
describe("plain search never adds hidden filters", () => {
  const monthOld = "2026-10-09T12:00:00Z";
  const cases: [query: string, title: string][] = [
    ["handling", "fix error handling in parser"],
    ["error handling", "fix error handling in parser"],
    ["pinned notes", "write pinned notes doc"],
    ["today standup", "today standup summary"],
    ["修复失败的测试", "修复失败的测试"], // i18n-ignore
    ["claude opus", "ask claude about opus pricing"],
    ["running unread", "running unread counts"],
  ];
  const ctx: EvalContext = { ...baseCtx, pins: { spaces: [], sessions: [] } };

  it.each(cases)("finds %s in %s", (query, title) => {
    const s = mkSession("plain", { title, state: "idle", agent: "codex", model: "gpt-5", updated_at: monthOld, last_text: "" });
    const { sessions, plan } = applyDimensionFilters([s], { ...DEFAULT_DIMENSION_FILTERS, query }, ctx);
    expect(sessions.map((x) => x.session_id)).toEqual(["plain"]);
    expect(plan.code).toBe(`session.where(s => s.matches(${JSON.stringify(query)}, "all"))`);
  });

  it("still honors explicit operators", () => {
    const pinned = mkSession("s-pinned", { title: "notes", updated_at: monthOld });
    const other = mkSession("other", { title: "notes", machine_id: "mac", updated_at: monthOld });
    const run = (query: string) => applyDimensionFilters([pinned, other], { ...DEFAULT_DIMENSION_FILTERS, query }, baseCtx).sessions.map((x) => x.session_id);
    expect(run("is:pinned notes")).toEqual(["s-pinned"]);
    expect(run("machine:mac notes")).toEqual(["other"]);
    expect(run("since:24h notes")).toEqual([]);
  });

  it("keeps the word lists and a provider for Search as Code", () => {
    registerSearchAsCodeProvider({ id: "p", compile: () => ({ code: "ext", predicates: [() => false], highlightTerms: [], source: "provider" }) });
    const s = mkSession("plain", { title: "fix error handling", updated_at: monthOld });
    expect(applyDimensionFilters([s], { ...DEFAULT_DIMENSION_FILTERS, query: "error handling" }, ctx).sessions).toHaveLength(1);
    expect(compileSearchAsCode("error handling", { ...DEFAULT_DIMENSION_FILTERS, searchAsCode: true }, buildAgentFacets([s], machines, ctx.pins)).source).toBe("provider");
    registerSearchAsCodeProvider(null);
    expect(applyDimensionFilters([s], { ...DEFAULT_DIMENSION_FILTERS, query: "error handling", searchAsCode: true }, ctx).plan.code).toContain("s.hasErrors");
  });
});

// ⌘K's match for an Agent (#6589): the list's operators narrow, then the name, the project or computer, what is inside.
describe("agentSearchHit", () => {
  const s = mkSession("a", { title: "Parser rewrite", machine_id: "mac", cwd: "/workspace/orca", repo_root: "/workspace/orca", branch: "fix-timeouts", last_text: "Retry after the socket closed" });
  const ctx: EvalContext = { ...baseCtx, pins: { spaces: [], sessions: [] } };
  const hit = (q: string) => agentSearchHit(s, q, ctx);

  it("ranks the name, then the project or computer, then what is inside", () => {
    expect(hit("parser").score).toBe(100);
    expect(hit("rewrite").score).toBe(80);
    expect(hit("orca").score).toBe(60);
    expect(hit("studio parser").score).toBe(60);
    expect(hit("socket").score).toBe(40);
    expect(hit("socket").snippet?.match).toBe("socket");
    expect(hit("fix-timeouts").score).toBe(40);
    expect(hit("orca nowhere").score).toBe(0);
  });

  it("narrows with the operators, which alone match at 30, and nothing typed matches at 1", () => {
    expect(hit("machine:studio").score).toBe(30);
    expect(hit("machine:cloud").score).toBe(0);
    expect(hit("project:orca socket").score).toBe(40);
    expect(hit("model:opus parser").score).toBe(100);
    expect(hit("is:pinned parser").score).toBe(0);
    expect(hit("").score).toBe(1);
  });

  it("tells a query with operators from plain words", () => {
    expect(hasSearchOperators("machine:mac")).toBe(true);
    expect(hasSearchOperators("parser is:error")).toBe(true);
    expect(hasSearchOperators("error handling")).toBe(false);
    expect(hasSearchOperators("state:bogus")).toBe(false);
    expect(hasSearchOperators("")).toBe(false);
  });
});

describe("⌘K's Filter writes the list's operators into the query (#6589)", () => {
  it("adds a condition after the words, takes it out when chosen again, any case", () => {
    expect(toggleCondition("parser", "machine", "box")).toBe("parser machine:box");
    expect(toggleCondition("parser machine:box", "machine", "mac")).toBe("parser machine:box machine:mac");
    expect(toggleCondition("parser Machine:BOX", "machine", "box")).toBe("parser");
    expect(toggleCondition("", "is", "pinned")).toBe("is:pinned");
  });
  it("keeps one time window", () => {
    expect(toggleCondition("since:24h parser", "since", "7d", true)).toBe("parser since:7d");
  });
  it("reads an operator's values and counts the operators, the words apart", () => {
    expect(conditionValues("parser machine:box project:blog machine:mac machine:", "machine")).toEqual(["box", "mac"]);
    expect(conditionCount("parser machine:box is:pinned")).toBe(2);
    expect(conditionCount("ratio 3:2")).toBe(0);
    expect(hasSearchOperators("since:7d")).toBe(true);
  });
  it("drops one operator's values, or every operator and leaves the words", () => {
    expect(dropConditions("parser machine:box project:blog machine:mac", "machine")).toBe("parser project:blog");
    expect(dropConditions("parser machine:box is:pinned since:7d crash")).toBe("parser crash");
  });
  it("offers only values an operator can spell", () => {
    expect(conditionValue("persona")).toBe(true);
    expect(conditionValue("My Project")).toBe(false);
    expect(conditionValue("")).toBe(false);
  });
});
