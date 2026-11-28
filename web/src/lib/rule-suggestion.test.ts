// @vitest-environment jsdom
// A suggested rule (#4929, #5802): which suggestions the page draws, what it says Always stores for each kind, what
// it says afterwards, and the two requests (a 404 is "gone", any other failure throws).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import { credentialOwner, setToken } from "./api";
import { answerRuleSuggestion, markRuleAnswered, markRuleMade, resetRuleAnswered, ruleAnswered, ruleCreates, ruleJoin, ruleMade, ruleResult, ruleScope, ruleSuggestion, subscribeRuleAnswered, type RuleSuggestion } from "./rule-suggestion";

const plan: RuleSuggestion = {
  id: "rs_plan", kind: "plan_start", count: 3, creates: "tasks.trust",
  text: "You started tasks like this 3 times. Always start tasks that change files in /code/example on Example node directly?",
  scope: { machine_id: "example-node", machine: "Example node", directory: "/code/example" },
};
const standing: RuleSuggestion = {
  id: "rs_merge", kind: "standing_authorization", count: 3, creates: "standing_authorization",
  text: "You approved this 3 times. Always allow merging in /code/example on Example node once checks pass and there are no conflicts?",
  scope: { machine_id: "example-node", machine: "Example node", directory: "/code/example", action: "merge", conditions: ["ci_green", "no_conflicts"], quote: "Merge the PR now? → Yes" },
};
const delegate: RuleSuggestion = {
  id: "rs_delegate", kind: "delegate", count: 3, creates: "", setting: "agents.delegate", scope: {},
  text: "You took the recommendation 3 times. Let Persona answer agents' own-call questions for you?",
};

beforeEach(async () => {
  await initI18n("en");
  resetRuleAnswered();
});
afterEach(async () => {
  vi.unstubAllGlobals();
  await initI18n("zh-Hans");
});

describe("which suggestions are drawn", () => {
  it("the three kinds the hub sends, as sent", () => {
    for (const s of [plan, standing, delegate]) expect(ruleSuggestion(s)).toBe(s);
  });

  it("nothing without the field, without an id or a sentence, or for a kind this page has no words for", () => {
    for (const none of [undefined, null, "", 0, [], {}]) expect(ruleSuggestion(none)).toBeUndefined();
    expect(ruleSuggestion({ ...plan, id: "" })).toBeUndefined();
    expect(ruleSuggestion({ ...plan, text: "  " })).toBeUndefined();
    expect(ruleSuggestion({ ...plan, id: 7 })).toBeUndefined();
    expect(ruleSuggestion({ ...plan, kind: "a_newer_kind" })).toBeUndefined();
  });
});

describe("what Always stores, in words", () => {
  it("a remembered kind of task, with the directory and the computer", () => {
    expect(ruleCreates(ruleSuggestion(plan)!)).toEqual(["Always remembers this kind of task: tasks that change files in /code/example and its subfolders on Example node start without asking."]);
  });

  it("a standing authorization with its action, its conditions and the words kept as the owner's", () => {
    expect(ruleCreates(ruleSuggestion(standing)!)).toEqual([
      "Always adds a standing authorization for /code/example on Example node: Merge changes.",
      "Only when: All checks pass, No merge conflicts.",
      "Kept as your words: “Merge the PR now? → Yes”.",
    ]);
  });

  it("no condition sentence for an action granted without conditions, no quote sentence without a quote", () => {
    const push = ruleSuggestion({ ...standing, scope: { ...standing.scope, action: "push", conditions: [], quote: "" } })!;
    expect(ruleCreates(push)).toEqual(["Always adds a standing authorization for /code/example on Example node: Push changes."]);
    const deploy = ruleSuggestion({ ...standing, scope: { machine_id: "example-node", directory: "/code/example", action: "deploy-test" } })!;
    // the computer's id when the hub did not send its name; an action and a condition this page has no word for, as sent
    expect(ruleCreates(deploy)).toEqual(["Always adds a standing authorization for /code/example on example-node: Deploy to a test environment."]);
    const newer = ruleSuggestion({ ...standing, scope: { ...standing.scope, action: "tag", conditions: ["reviewed"], quote: undefined } })!;
    expect(ruleCreates(newer)).toEqual(["Always adds a standing authorization for /code/example on Example node: tag.", "Only when: reviewed."]);
  });

  it("nothing for a delegate suggestion, said plainly: no rule, nothing turned on, no key and no file name", () => {
    const said = ruleCreates(ruleSuggestion(delegate)!);
    expect(said).toEqual(["Always stores no rule and turns nothing on: it only puts this suggestion away for 30 days."]);
    expect(said[0]).not.toMatch(/agents\.delegate|manager\.json/);
  });

  it("in Simplified Chinese and Japanese too", async () => {
    await initI18n("zh-Hans");
    expect(ruleJoin(ruleCreates(ruleSuggestion(standing)!))).toBe("选择「总是」将为 Example node 的 /code/example 新增一条长期授权：合入改动。仅在满足以下条件时生效：所有检查通过、无合并冲突。已按你的原话记录：「Merge the PR now? → Yes」。");
    await initI18n("ja");
    expect(ruleCreates(ruleSuggestion(plan)!)[0]).toContain("この種類のタスクを記憶します");
    expect(ruleCreates(ruleSuggestion(delegate)!)[0]).not.toMatch(/agents\.delegate|manager\.json/);
    await initI18n("zh-Hans");
    expect(ruleCreates(ruleSuggestion(delegate)!)[0]).not.toMatch(/agents\.delegate|manager\.json/);
  });
});

describe("what is said after Always", () => {
  it("the rule made, and where it is listed and removed", () => {
    expect(ruleResult(ruleSuggestion(plan)!, { ok: true, kind: "plan_start", creates: "tasks.trust", key: "k" })).toEqual({
      text: "Remembered: tasks like this now start without asking.",
      where: "Listed and removed in Settings › Assistant profile › Remembered auto-start rules.",
      page: "/settings?tab=persona",
    });
    expect(ruleResult(ruleSuggestion(standing)!, { ok: true, kind: "standing_authorization", creates: "standing_authorization", authorization: { directory: "/code/example" } })).toEqual({
      text: "Standing authorization added.",
      where: "Listed and removed in Settings › Assistant profile › Standing authorizations.",
      page: "/settings?tab=persona",
    });
  });

  it("nothing stored for delegate: the setting to turn on, and no page of this app to open", () => {
    const r = ruleResult(ruleSuggestion(delegate)!, { ok: true, kind: "delegate", creates: "", setting: "agents.delegate" });
    expect(r.text).toBe("No rule was stored and nothing was turned on.");
    expect(r.where).toBe("No app has a switch for this yet: it is turned on in the assistant's configuration file on the hub's computer (agents.delegate).");
    expect(r.page).toBeUndefined();
  });

  it("goes by the suggestion's kind when the hub's answer names none", () => {
    expect(ruleResult(ruleSuggestion(standing)!, { ok: true }).text).toBe("Standing authorization added.");
    expect(ruleResult(ruleSuggestion(standing)!, { ok: true, kind: "something_newer" }).text).toBe("Standing authorization added.");
  });
});

describe("the two requests", () => {
  const hub = (status: number, body: unknown) => {
    const fetcher = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("localStorage", { getItem: () => "mock" });
    vi.stubGlobal("fetch", fetcher);
    return fetcher;
  };

  it("Always posts to the suggestion's route with no body and returns what was made", async () => {
    const fetcher = hub(200, { ok: true, kind: "plan_start", creates: "tasks.trust", key: "k" });
    await expect(answerRuleSuggestion("rs_plan", "always", { "X-Test": "1" })).resolves.toEqual({ state: "made", made: { ok: true, kind: "plan_start", creates: "tasks.trust", key: "k" } });
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/api/v1/assistant/rule-suggestions/rs_plan/always");
    expect(init?.method).toBe("POST");
    expect(init?.body).toBeUndefined();
    expect(new Headers(init?.headers).get("X-Test")).toBe("1");
  });

  it("Not now posts to its route", async () => {
    const fetcher = hub(200, { ok: true });
    await expect(answerRuleSuggestion("rs a/b", "not-now")).resolves.toEqual({ state: "hidden" });
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/assistant/rule-suggestions/rs%20a%2Fb/not-now");
  });

  it("a 404 is a suggestion that is gone, for either answer", async () => {
    hub(404, { error: "This suggestion is no longer available." });
    await expect(answerRuleSuggestion("rs_plan", "always")).resolves.toEqual({ state: "gone" });
    await expect(answerRuleSuggestion("rs_plan", "not-now")).resolves.toEqual({ state: "gone" });
  });

  it("any other failure throws: 403 without full access, 409 when the authorizations changed, 503", async () => {
    for (const status of [403, 409, 503]) {
      hub(status, { error: "refused" });
      await expect(answerRuleSuggestion("rs_merge", "always")).rejects.toMatchObject({ status });
    }
  });
});

describe("suggestions answered on this page", () => {
  const scope = () => ruleScope({ "X-Persona-Manager": "first" });

  it("are hidden from then on and told to whoever draws one", () => {
    const heard = vi.fn();
    const stop = subscribeRuleAnswered(heard);
    expect(ruleAnswered(scope(), plan)).toBe(false);
    markRuleAnswered(scope(), plan);
    expect(ruleAnswered(scope(), plan)).toBe(true);
    expect(ruleAnswered(scope(), standing)).toBe(false);
    expect(heard).toHaveBeenCalledTimes(1);
    stop();
    markRuleAnswered(scope(), standing);
    expect(heard).toHaveBeenCalledTimes(1);
  });

  it("say nothing about the same id under another manager or on another hub (delegate's id is the same everywhere)", () => {
    markRuleAnswered(scope(), delegate);
    expect(ruleAnswered(scope(), delegate)).toBe(true);
    expect(ruleAnswered(ruleScope({ "X-Persona-Manager": "second" }), delegate)).toBe(false);
    expect(ruleAnswered(ruleScope(undefined), delegate)).toBe(false);
    // the page pairs with another hub: another credential, the same manager name
    const before = scope();
    setToken("another-hub-token");
    expect(scope()).not.toBe(before);
    expect(ruleAnswered(scope(), delegate)).toBe(false);
    setToken("");
  });

  it("hide the copies the page held and one arriving right after; a copy the hub sends later is earned again and drawn", () => {
    const t0 = 1_000_000;
    const other = { ...delegate }; // the same suggestion on another card
    markRuleAnswered(scope(), delegate, t0);
    expect(ruleAnswered(scope(), other, t0 + 500)).toBe(true); // a read that was on its way
    // both stay hidden however long the page keeps them
    expect(ruleAnswered(scope(), delegate, t0 + 3_600_000)).toBe(true);
    expect(ruleAnswered(scope(), other, t0 + 3_600_000)).toBe(true);
    // the hub sends the suggestion again (the same kind and scope has the same id): drawn, and the copies with it
    const again = { ...delegate };
    expect(ruleAnswered(scope(), again, t0 + 3_600_000)).toBe(false);
    expect(ruleAnswered(scope(), again, t0 + 3_600_001)).toBe(false);
  });

  it("keep what Always made by card, on the hub it was made on", () => {
    const made = { text: "Standing authorization added.", where: "Listed there." };
    const owner = credentialOwner();
    expect(ruleMade("card-1")).toBeUndefined();
    markRuleMade(owner, "card-1", made);
    expect(ruleMade("card-1")).toBe(made);
    expect(ruleMade("card-2")).toBeUndefined();
    expect(ruleMade(undefined)).toBeUndefined();
    markRuleMade(owner, undefined, made);
    // paired with another hub: its card of the same id says nothing; an answer that comes back late is kept for the hub it was given to
    setToken("another-hub-token");
    expect(ruleMade("card-1")).toBeUndefined();
    markRuleMade(owner, "card-3", made);
    expect(ruleMade("card-3")).toBeUndefined();
    setToken("");
  });
});
