// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({ useStore: () => [], refreshAssistant: vi.fn() }));
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { AssistantSettings } from "./AssistantSettings";

// Settings › Persona › Recordings (#3780): the two retentions, only from a hub that says them, each saved on leaving
// the field within its bounds and confirmed by the days the hub says are in effect.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
let saved: { recording_days?: number; minutes_days?: number } | null;
let writes: Record<string, unknown>[];
/** What a PUT answers instead of the stored state, when set. */
let answer: ((body: Record<string, unknown>) => Response) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
/** False: a manager from before minutes_days (#3780), which says only recording_days. */
let withMinutes: boolean;
const effective = () => saved && { recording_days: saved.recording_days, ...(withMinutes ? { minutes_days: (saved.minutes_days ?? 0) === 0 ? 0 : Math.max(saved.minutes_days!, saved.recording_days ?? 14) } : {}) };
const state = () => ({ manager: "assistant", notes: "", ...(effective() ?? {}) });
const input = (label: string) => host.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
const recordings = () => input(t("settings-assistantsettings.recording_days"));
const minutes = () => input(t("settings-assistantsettings.minutes_days"));
const mode = () => host.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-assistantsettings.minutes_days")}"]`)!;
async function choose(value: string) {
  await act(async () => { mode().value = value; mode().dispatchEvent(new Event("change", { bubbles: true })); });
  await flush();
}
async function enter(el: HTMLInputElement, value: string, commit: "blur" | "Enter" = "blur") {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => { setter.call(el, value); el.dispatchEvent(new Event("input", { bubbles: true })); });
  await act(async () => { el.dispatchEvent(commit === "Enter"
    ? new KeyboardEvent("keydown", { key: "Enter", bubbles: true })
    : new FocusEvent("focusout", { bubbles: true })); });
  await flush();
}
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  vi.mocked(toast).mockClear();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  saved = { recording_days: 14 }; writes = []; answer = undefined; withMinutes = true;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    if (path.endsWith("/assistant")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)); writes.push(body);
        if (answer) return answer(body);
        saved = { ...saved, ...body };
      }
      return json(state());
    }
    if (path.endsWith("/tasks")) return json({ tasks: [] });
    if (path.endsWith("/memory")) return json({ files: [] });
    throw new Error(`Unexpected path: ${path}`);
  }));
});
afterEach(async () => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<AssistantSettings />)); await flush(); }

it("shows both retentions in effect and saves a change the hub confirms", async () => {
  await render();
  expect(host.textContent).toContain("Recordings");
  expect(host.textContent).toContain(t("settings-assistantsettings.recordings_footer"));
  expect(host.textContent).not.toContain(t("settings-assistantsettings.recordings_footer_with_minutes"));
  expect(recordings()!.value).toBe("14");
  expect(mode().value).toBe("forever");
  expect(minutes()).toBeNull();
  await choose("days");
  expect(minutes()!.value).toBe("365");
  expect(writes).toEqual([]);
  expect(saved!.minutes_days ?? 0).toBe(0);
  await enter(minutes()!, "730");
  expect(writes).toEqual([{ minutes_days: 730 }]);
  expect(minutes()!.value).toBe("730");
  await enter(recordings()!, "30");
  expect(writes.at(-1)).toEqual({ recording_days: 30 });
  expect(recordings()!.value).toBe("30");
  expect(toast).not.toHaveBeenCalled();
});

it("has no group on a hub that does not say its retention", async () => {
  saved = null;
  await render();
  expect(recordings()).toBeNull();
  expect(minutes()).toBeNull();
  expect(host.textContent).not.toContain(t("settings-assistantsettings.recordings_footer"));
});

it("promises nothing about the minutes on a manager that deletes them with the recording", async () => {
  withMinutes = false;
  await render();
  expect(recordings()!.value).toBe("14");
  expect(minutes()).toBeNull();
  expect(host.textContent).toContain(t("settings-assistantsettings.recordings_footer_with_minutes"));
  expect(host.textContent).not.toContain(t("settings-assistantsettings.recordings_footer"));
  await enter(recordings()!, "30");
  expect(writes).toEqual([{ recording_days: 30 }]);
  expect(recordings()!.value).toBe("30");
  expect(toast).not.toHaveBeenCalled();
});

it("sends nothing out of bounds or not a whole number, and says the bounds", async () => {
  saved = { recording_days: 14, minutes_days: 365 };
  await render();
  for (const [field, value, max] of [[minutes, "0", 3650], [minutes, "3651", 3650], [minutes, "1.5", 3650], [recordings, "366", 365], [recordings, "", 365]] as const) {
    await enter(field()!, value);
    expect(field()!.getAttribute("aria-invalid")).toBe("true");
    expect(host.textContent).toContain(t("settings-assistantsettings.days_range", { min: 1, max }));
    await act(async () => { field()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
  }
  expect(writes).toEqual([]);
  expect([recordings()!.value, minutes()!.value]).toEqual(["14", "365"]);
});

it("shows the minutes' days in effect when the value asked for is below the recordings'", async () => {
  saved = { recording_days: 60, minutes_days: 90 };
  await render();
  await enter(minutes()!, "7");
  expect(writes).toEqual([{ minutes_days: 7 }]);
  expect(minutes()!.value).toBe("60");
  expect(toast).not.toHaveBeenCalled();
});

it.each([
  ["another value", () => json({ manager: "assistant", recording_days: 14, minutes_days: 365 }), () => t("settings-assistantsettings.retention_unconfirmed")],
  ["no manager identity", () => json({ recording_days: 14, minutes_days: 30 }), () => t("settings-assistantsettings.retention_unconfirmed")],
  ["a refusal", () => json({ error: "Synthetic refusal" }, 400), () => "Synthetic refusal"],
])("keeps what is in effect and says so after %s", async (_, reply, why) => {
  answer = reply;
  saved = { recording_days: 14, minutes_days: 365 };
  await render();
  await enter(minutes()!, "30");
  expect(writes).toEqual([{ minutes_days: 30 }]);
  expect(toast).toHaveBeenCalledWith(expect.stringContaining(why()), "error");
  expect(minutes()!.value).toBe("365");
});


it("saves forever as zero, even with longer recording retention, and switches back", async () => {
  saved = { recording_days: 60, minutes_days: 90 };
  await render();
  await choose("forever");
  expect(writes).toEqual([{ minutes_days: 0 }]);
  expect(mode().value).toBe("forever");
  expect(minutes()).toBeNull();
  await enter(recordings()!, "365");
  expect(mode().value).toBe("forever");
  await choose("days");
  expect(writes).toEqual([{ minutes_days: 0 }, { recording_days: 365 }]);
  expect(minutes()!.value).toBe("365");
  await enter(minutes()!, "3650");
  expect(writes.at(-1)).toEqual({ minutes_days: 3650 });
  expect(toast).not.toHaveBeenCalled();
});

it("does not claim forever when the hub refuses or ignores it", async () => {
  saved = { recording_days: 14, minutes_days: 365 };
  answer = () => json(state());
  await render();
  await choose("forever");
  expect(mode().value).toBe("days");
  expect(minutes()!.value).toBe("365");
  expect(toast).toHaveBeenCalledWith(expect.stringContaining(t("settings-assistantsettings.retention_unconfirmed")), "error");
});

it.each(["blur", "Enter"] as const)("commits only the intended finite days on %s", async (commit) => {
  await render();
  await choose("days");
  expect(writes).toEqual([]);
  await enter(minutes()!, "1000", commit);
  expect(writes).toEqual([{ minutes_days: 1000 }]);
  expect(saved!.minutes_days).toBe(1000);
  expect(minutes()!.value).toBe("1000");
  await act(async () => { minutes()!.dispatchEvent(new FocusEvent("focusout", { bubbles: true })); });
  await flush();
  expect(writes).toEqual([{ minutes_days: 1000 }]);
});

it("can cancel a finite draft without changing forever retention", async () => {
  await render();
  await choose("days");
  await act(async () => { minutes()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
  expect(mode().value).toBe("forever");
  expect(minutes()).toBeNull();
  await choose("days");
  await choose("forever");
  expect(mode().value).toBe("forever");
  expect(minutes()).toBeNull();
  expect(writes).toEqual([]);
});

it("restores forever when the hub refuses a finite draft", async () => {
  answer = () => json({ error: "Synthetic refusal" }, 400);
  await render();
  await choose("days");
  expect(writes).toEqual([]);
  await enter(minutes()!, "1000");
  expect(writes).toEqual([{ minutes_days: 1000 }]);
  expect(mode().value).toBe("forever");
  expect(minutes()).toBeNull();
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic refusal"), "error");
});

// A hub with separate audio and text retentions (#16412): two rows, each forever or a number of days past the old
// 365 cap; the text is kept forever by default and minutes stay forever while it is.
it("shows audio and text retentions separately on a hub that says both", async () => {
  type Split = { recording_audio_days: number; recording_text_days: number; minutes_days: number };
  let split: Split = { recording_audio_days: 14, recording_text_days: 0, minutes_days: 0 };
  const splitWrites: Record<string, unknown>[] = [];
  const effectiveSplit = () => ({ ...split, recording_days: split.recording_audio_days,
    minutes_days: split.minutes_days === 0 || split.recording_text_days === 0 ? 0 : Math.max(split.minutes_days, split.recording_text_days) });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    if (path.endsWith("/assistant")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)); splitWrites.push(body);
        split = { ...split, ...body };
      }
      return json({ manager: "assistant", notes: "", ...effectiveSplit() });
    }
    if (path.endsWith("/tasks")) return json({ tasks: [] });
    if (path.endsWith("/memory")) return json({ files: [] });
    throw new Error(`Unexpected path: ${path}`);
  }));
  await render();
  const audioLabel = t("settings-assistantsettings.recording_audio_days");
  const textLabel = t("settings-assistantsettings.recording_text_days");
  const select = (label: string) => host.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!;
  const pick = async (label: string, value: string) => {
    await act(async () => { select(label).value = value; select(label).dispatchEvent(new Event("change", { bubbles: true })); });
    await flush();
  };
  expect(host.textContent).toContain(t("settings-assistantsettings.recordings_footer_split"));
  expect(recordings()).toBeNull(); // no old single row
  expect(input(audioLabel)!.value).toBe("14");
  expect(select(textLabel).value).toBe("forever");
  expect(input(textLabel)).toBeNull();
  // The text kept for a number of days past the old cap.
  await pick(textLabel, "days");
  await enter(input(textLabel)!, "1000");
  expect(splitWrites).toEqual([{ recording_text_days: 1000 }]);
  expect(input(textLabel)!.value).toBe("1000");
  // The audio kept forever, the text unchanged.
  await pick(audioLabel, "forever");
  expect(splitWrites.at(-1)).toEqual({ recording_audio_days: 0 });
  expect(select(audioLabel).value).toBe("forever");
  expect(input(textLabel)!.value).toBe("1000");
  // Minutes below the text's days show the text's in effect.
  await choose("days");
  await enter(minutes()!, "30");
  expect(splitWrites.at(-1)).toEqual({ minutes_days: 30 });
  expect(minutes()!.value).toBe("1000");
  expect(toast).not.toHaveBeenCalled();
});
