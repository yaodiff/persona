import { beforeAll, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import {
  captureProfileInput,
  parseProfileChange,
  profileApplyDrift,
  type ModelProfile,
} from "./model-profiles";
import {
  draftToProfileInput,
  isProfileDraftDirty,
  profileDraftProblems,
  profileToDraft,
} from "./model-profile-editor";

beforeAll(async () => {
  await initI18n("en");
});

const baseProfile: ModelProfile = {
  id: "economy",
  name: "Economy",
  description: "Everyday models",
  purposes: {
    assistant: { provider: "launcher.gw", model: "sonnet-4", effort: "medium", permission_mode: "acceptEdits" },
    kb: {},
  },
  launchers: {
    gw: { model: "sonnet-4", small_model: "", effort: "low" },
  },
  tiers: {
    strong: { launcher: "gw", model: "opus-4", effort: "high", permission_mode: "plan", speed: "fast", description: "Hard work" },
  },
  changes: [],
};

describe("model-profile-editor", () => {
  it("round-trips a profile through draft and save input without emitting config_dir or secrets", () => {
    const draft = profileToDraft(baseProfile, ["assistant", "agent.new", "kb", "inbox.triage", "agent.delegate", "translate"]);
    expect(isProfileDraftDirty(draft, profileToDraft(baseProfile))).toBe(false);
    expect(profileDraftProblems(draft, { gw: "launcher" })).toEqual([]);

    const input = draftToProfileInput(draft, 5);
    expect(input).toEqual({
      revision: 5,
      name: "Economy",
      description: "Everyday models",
      purposes: {
        assistant: { provider: "launcher.gw", model: "sonnet-4", effort: "medium", permission_mode: "acceptEdits" },
        kb: {},
      },
      launchers: {
        gw: { model: "sonnet-4", small_model: "", effort: "low" },
      },
      tiers: {
        strong: { launcher: "gw", model: "opus-4", effort: "high", permission_mode: "plan", speed: "fast", description: "Hard work" },
      },
    });
    expect(JSON.stringify(input)).not.toContain("config_dir");
    expect(JSON.stringify(input)).not.toContain("api_key");
  });

  it("keeps a tier's runtime and channel, and takes a candidate given by them alone", () => {
    const profile: ModelProfile = { ...baseProfile, tiers: { strong: { launcher: "gw", runtime: "codex", channel: "team-gw", model: "opus-4", effort: "high" } } };
    const draft = profileToDraft(profile);
    expect(draft.tiers[0].candidates[0]).toMatchObject({ launcher: "gw", runtime: "codex", channel: "team-gw" });
    expect(draftToProfileInput(draft, 3).tiers?.strong).toMatchObject({ launcher: "gw", runtime: "codex", channel: "team-gw", model: "opus-4" });
    // the hub resolves the launcher alias of a route on save
    draft.tiers[0].candidates[0].launcher = "";
    expect(profileDraftProblems(draft, { gw: "launcher" })).toEqual([]);
    // without a route either, the launcher is required
    draft.tiers[0].candidates[0].runtime = undefined;
    draft.tiers[0].candidates[0].channel = undefined;
    expect(profileDraftProblems(draft, { gw: "launcher" })).not.toEqual([]);
    // a profile without them sends none
    expect(draftToProfileInput(profileToDraft(baseProfile), 3).tiers?.strong).not.toHaveProperty("runtime");
  });

  it("omits unchecked purposes and empty launcher entries", () => {
    const draft = profileToDraft(baseProfile);
    draft.purposes.kb.included = false;
    draft.launchers.gw = {};
    const input = draftToProfileInput(draft, 2);
    expect(input.purposes).toEqual({
      assistant: { provider: "launcher.gw", model: "sonnet-4", effort: "medium", permission_mode: "acceptEdits" },
    });
    expect(input.launchers).toBeUndefined();
    expect(isProfileDraftDirty(draft, profileToDraft(baseProfile))).toBe(true);
  });

  it("validates profile name, launcher API restrictions, and tier constraints", () => {
    const draft = profileToDraft(baseProfile);
    draft.name = "   ";
    draft.launchers.api_only = { model: "m-1", small_model: "m-2" };
    draft.tiers.push({
      key: "bad-1",
      name: "Invalid Name",
      description: "line1\nline2",
      candidates: [
        {
          key: "bad-1-c0",
          launcher: "",
          model: "bad model id!",
          effort: "extreme",
          permission_mode: "bogusMode",
          speed: "warp",
        },
      ],
    });
    const problems = profileDraftProblems(draft, { gw: "launcher", api_only: "api" });
    expect(problems.length).toBeGreaterThanOrEqual(7);
    expect(problems.some((p) => p.includes("Profile name"))).toBe(true);
    expect(problems.some((p) => p.includes("api_only"))).toBe(true);
    expect(problems.some((p) => p.includes("Invalid Name"))).toBe(true);
    expect(problems.some((p) => p.includes("bad model id!") && p.includes("#1"))).toBe(true);
    expect(problems.some((p) => p.includes("permission_mode") && p.includes("#1"))).toBe(true);
  });

  it("detects duplicate tier names and preserves untouched legacy tier model IDs", () => {
    const legacy: ModelProfile = {
      id: "legacy",
      name: "Legacy",
      tiers: {
        old: { launcher: "claude", model: "bad!legacy" },
      },
      changes: [],
    };
    const draft = profileToDraft(legacy);
    // Untouched legacy model ID does not block editing unrelated fields (mirrors server/internal/assistant/model_profiles_edit.go).
    expect(profileDraftProblems(draft)).toEqual([]);

    // Adding a duplicate tier name is flagged.
    draft.tiers.push({
      key: "dup",
      name: "old",
      description: "",
      candidates: [
        {
          key: "dup-c0",
          launcher: "claude",
          model: "valid-model",
          effort: "",
          permission_mode: "",
          speed: "",
        },
      ],
    });
    expect(profileDraftProblems(draft).some((p) => p.includes("Duplicate"))).toBe(true);
  });

  it("round-trips multi-candidate tiers with aliases and sends a one-item list when cut to one", () => {
    const multi: ModelProfile = {
      id: "routing",
      name: "Routing",
      tiers: {
        dev: {
          launcher: "example-a",
          model: "model-a",
          effort: "medium",
          description: "Development work",
          aliases: ["standard"],
          candidates: [
            { launcher: "example-a", model: "model-a", effort: "medium" },
            { launcher: "example-b", model: "model-b", effort: "high", speed: "fast" },
          ],
        },
      },
      changes: [],
    };
    const original = profileToDraft(multi);
    const draft = profileToDraft(multi);
    expect(draft.listedTierNames).toEqual(["dev"]);
    expect(draft.tiers[0].aliases).toEqual(["standard"]);
    expect(draft.tiers[0].candidates).toHaveLength(2);
    expect(profileDraftProblems(draft)).toEqual([]);
    expect(isProfileDraftDirty(draft, original)).toBe(false);

    // Reorder candidates: second becomes primary flat fields.
    const [first, second] = draft.tiers[0].candidates;
    draft.tiers[0].candidates = [second, first];
    const reordered = draftToProfileInput(draft, 4);
    expect(reordered.tiers?.dev).toEqual({
      launcher: "example-b",
      model: "model-b",
      effort: "high",
      speed: "fast",
      description: "Development work",
      aliases: ["standard"],
      candidates: [
        { launcher: "example-b", model: "model-b", effort: "high", speed: "fast" },
        { launcher: "example-a", model: "model-a", effort: "medium" },
      ],
    });
    expect(isProfileDraftDirty(draft, original)).toBe(true);

    // Removing B from [A, B] sends `candidates: [A]` so the hub drops B instead of merging it back.
    draft.tiers[0].candidates = [first];
    const single = draftToProfileInput(draft, 4);
    expect(single.tiers?.dev).toEqual({
      launcher: "example-a",
      model: "model-a",
      effort: "medium",
      description: "Development work",
      aliases: ["standard"],
      candidates: [{ launcher: "example-a", model: "model-a", effort: "medium" }],
    });
    expect(isProfileDraftDirty(draft, original)).toBe(true);

    // Never emit an empty `candidates` array even if all candidates are removed in the draft.
    draft.tiers[0].candidates = [];
    expect(draftToProfileInput(draft, 4).tiers?.dev?.candidates).toBeUndefined();
  });

  it("sends candidates when deleting a multi-candidate tier and recreating or swapping into its name", () => {
    const multi: ModelProfile = {
      id: "routing",
      name: "Routing",
      tiers: {
        core: {
          launcher: "example-c",
          model: "model-c",
          description: "Core work",
        },
        dev: {
          launcher: "example-a",
          model: "model-a",
          description: "Development work",
          candidates: [
            { launcher: "example-a", model: "model-a" },
            { launcher: "example-b", model: "model-b" },
          ],
        },
      },
      changes: [],
    };
    const original = profileToDraft(multi);
    expect(original.listedTierNames).toEqual(["dev"]);

    // Delete multi-candidate `dev` and create a new single-candidate `dev`.
    const recreated = profileToDraft(multi);
    recreated.tiers = recreated.tiers.filter((item) => item.name !== "dev");
    recreated.tiers.push({
      key: "new-dev",
      name: "dev",
      description: "Fresh dev",
      candidates: [
        {
          key: "new-dev-c0",
          launcher: "example-c",
          model: "model-c",
          effort: "",
          permission_mode: "",
          speed: "",
        },
      ],
    });
    const recreatedInput = draftToProfileInput(recreated, 2);
    expect(recreatedInput.tiers?.dev).toEqual({
      launcher: "example-c",
      model: "model-c",
      description: "Fresh dev",
      candidates: [{ launcher: "example-c", model: "model-c" }],
    });
    expect("candidates" in (recreatedInput.tiers?.core ?? {})).toBe(false);
    expect(isProfileDraftDirty(recreated, original)).toBe(true);

    // Swap names: multi-candidate `dev` -> `core`, single-candidate `core` -> `dev`.
    const swapped = profileToDraft(multi);
    const coreEntry = swapped.tiers.find((item) => item.name === "core")!;
    const devEntry = swapped.tiers.find((item) => item.name === "dev")!;
    coreEntry.name = "dev";
    devEntry.name = "core";
    const swappedInput = draftToProfileInput(swapped, 2);
    expect(swappedInput.tiers?.dev?.candidates).toEqual([{ launcher: "example-c", model: "model-c" }]);
    expect(swappedInput.tiers?.core?.candidates).toEqual([
      { launcher: "example-a", model: "model-a" },
      { launcher: "example-b", model: "model-b" },
    ]);
  });

  it("keeps tiers in the flat shape when the hub did not list candidates", () => {
    const flatHub: ModelProfile = {
      id: "flat",
      name: "Flat",
      tiers: {
        dev: { launcher: "example-a", model: "model-a", description: "Development work" },
      },
      changes: [],
    };
    const original = profileToDraft(flatHub);
    const draft = profileToDraft(flatHub);
    expect(draft.listedTierNames).toEqual([]);
    const unchanged = draftToProfileInput(draft, 1);
    expect(unchanged.tiers?.dev).toEqual({
      launcher: "example-a",
      model: "model-a",
      description: "Development work",
    });
    expect("candidates" in (unchanged.tiers?.dev ?? {})).toBe(false);

    draft.tiers[0].candidates[0].model = "model-b";
    const edited = draftToProfileInput(draft, 1);
    expect(edited.tiers?.dev).toEqual({
      launcher: "example-a",
      model: "model-b",
      description: "Development work",
    });
    expect("candidates" in (edited.tiers?.dev ?? {})).toBe(false);
    expect(isProfileDraftDirty(draft, original)).toBe(true);
  });

  it("validates candidate count limits and includes 1-based candidate indices in errors", () => {
    const draft = profileToDraft(baseProfile);
    draft.tiers[0].candidates.push({
      key: "bad-c2",
      launcher: "",
      model: "not valid!",
      effort: "bogus",
      permission_mode: "",
      speed: "ludicrous",
    });
    const problems = profileDraftProblems(draft);
    expect(problems.some((p) => p.includes("#2") && p.includes("launcher"))).toBe(true);
    expect(problems.some((p) => p.includes("#2") && p.includes("not valid!"))).toBe(true);
    expect(problems.some((p) => p.includes("#2") && p.includes("bogus"))).toBe(true);
    expect(problems.some((p) => p.includes("#2") && p.includes("ludicrous"))).toBe(true);

    // More than 6 candidates is rejected.
    while (draft.tiers[0].candidates.length <= 6) {
      draft.tiers[0].candidates.push({
        key: `extra-${draft.tiers[0].candidates.length}`,
        launcher: "example-a",
        model: "",
        effort: "",
        permission_mode: "",
        speed: "",
      });
    }
    expect(profileDraftProblems(draft).some((p) => p.includes("at most 6"))).toBe(true);
  });

  it("omits empty optional fields and default standard speed for cross-version hub compatibility", () => {
    const profile: ModelProfile = {
      id: "compat",
      name: "Compat",
      purposes: {
        assistant: { provider: "launcher.claude", model: "sonnet-4" },
      },
      tiers: {
        standard: { launcher: "claude", model: "sonnet-4", speed: "standard" },
        quick: { launcher: "claude", speed: "fast", permission_mode: "plan" },
      },
      changes: [],
    };
    const draft = profileToDraft(profile);
    const input = draftToProfileInput(draft, 4);
    expect(input.purposes?.assistant).toEqual({ provider: "launcher.claude", model: "sonnet-4" });
    expect("permission_mode" in (input.purposes?.assistant ?? {})).toBe(false);
    expect(input.tiers?.standard).toEqual({ launcher: "claude", model: "sonnet-4" });
    expect("speed" in (input.tiers?.standard ?? {})).toBe(false);
    expect("permission_mode" in (input.tiers?.standard ?? {})).toBe(false);
    expect(input.tiers?.quick).toEqual({ launcher: "claude", speed: "fast", permission_mode: "plan" });
    expect("capture" in input).toBe(false);
  });

  it("builds minimal capture: true payloads preserving name and description without tiers", () => {
    expect(captureProfileInput({ name: "Performance", description: "Fast models" }, 7)).toEqual({
      revision: 7,
      name: "Performance",
      description: "Fast models",
      capture: true,
    });
    const noDesc = captureProfileInput({ name: "Economy", description: "" }, 2);
    expect(noDesc).toEqual({ revision: 2, name: "Economy", capture: true });
    expect("description" in noDesc).toBe(false);
    expect("tiers" in noDesc).toBe(false);
    expect("purposes" in noDesc).toBe(false);
    expect("launchers" in noDesc).toBe(false);
  });

  it("parses dotted category IDs and dotted launcher field changes unambiguously", () => {
    expect(parseProfileChange("assistant")).toEqual({ kind: "purpose", id: "assistant" });
    expect(parseProfileChange("agent.new")).toEqual({ kind: "purpose", id: "agent.new" });
    expect(parseProfileChange("inbox.triage")).toEqual({ kind: "purpose", id: "inbox.triage" });
    expect(parseProfileChange("agent.delegate")).toEqual({ kind: "purpose", id: "agent.delegate" });
    expect(parseProfileChange("gw.small_model")).toEqual({ kind: "launcher", launcher: "gw", field: "small_model" });
    expect(parseProfileChange("relay.example.com.effort")).toEqual({ kind: "launcher", launcher: "relay.example.com", field: "effort" });
    expect(parseProfileChange("custom.unknown_field")).toEqual({ kind: "other", raw: "custom.unknown_field" });
  });

  it("computes apply drift and active-profile capture availability accurately", () => {
    expect(profileApplyDrift({ id: "economy", changes: [] }, { id: "performance", changes: [] })).toEqual({
      targetChanges: [],
      activeDrift: [],
      hasDrift: false,
      hasActiveDrift: false,
      canCaptureActiveFirst: true,
    });
    expect(profileApplyDrift({ id: "economy", changes: ["assistant"] }, { id: "performance", changes: ["kb"] })).toEqual({
      targetChanges: ["assistant"],
      activeDrift: ["kb"],
      hasDrift: true,
      hasActiveDrift: true,
      canCaptureActiveFirst: true,
    });
    expect(profileApplyDrift({ id: "economy", changes: [] }, { id: "performance", changes: ["translate"] })).toEqual({
      targetChanges: [],
      activeDrift: ["translate"],
      hasDrift: true,
      hasActiveDrift: true,
      canCaptureActiveFirst: true,
    });
    expect(profileApplyDrift({ id: "performance", changes: ["assistant"] }, { id: "performance", changes: ["assistant"] })).toEqual({
      targetChanges: ["assistant"],
      activeDrift: [],
      hasDrift: true,
      hasActiveDrift: false,
      canCaptureActiveFirst: false,
    });
    expect(profileApplyDrift({ id: "economy", changes: ["assistant"] }, undefined)).toEqual({
      targetChanges: ["assistant"],
      activeDrift: [],
      hasDrift: true,
      hasActiveDrift: false,
      canCaptureActiveFirst: false,
    });
  });
});
