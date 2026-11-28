// Loaded only when a launch is delivered, after the local conversation has painted.
import { t } from "../i18n";
import { agentId } from "./agent-kind";
import { ApiError, errorText, isAbort, isTransient, sessionPath } from "./api";
import { quotePath, releaseAttachments, sendFile } from "./attachments";
import { imageLimitsFor, MAX_PICTURE_SOURCE, pictureRoom } from "./image-limits";
import { uuid } from "./format";
import { toast } from "../ui/atoms";
import { launchHasFirstTurn } from "./launch-handoff";
import { apiOp } from "./ops";
import { href, navigate } from "./router";
import { getState, putLaunch, upsertSession } from "./store";
import { ultracodeErrorText, ultracodeRefused } from "./ultracode";
import type { AgentLaunch } from "./new-agent";
import type { Session } from "./types";

function launchOpIds() {
  return { startOpId: uuid(), startPromptOpId: uuid(), messageId: uuid() };
}

/** One step of a start under its op id. A repeat can come back 202: the first try still running (ask again; the hub
 *  waits about 10 s each time), or lost to a hub restart (nobody knows how it went). */
async function launchOp<T>(path: string, opId: string, signal: AbortSignal, json: unknown): Promise<T> {
  for (;;) {
    const r = await apiOp<T & { state?: string; launcher_note?: string; session?: Session }>(path, { method: "POST", signal, json, opId });
    if (r.state === "unknown") throw new ApiError(202, t("lib-new-agent.outcome_unknown"), { delivered: "unknown" });
    if (r.state === "pending") continue;
    // a default launcher the computer lacks gave way to another (#4661): said once, as for the main chat; loaded only
    // then, so New Agent's first screen does not carry it
    if (r.launcher_note) void import("./assistant-start").then((m) => m.showLauncherNote(`${r.session?.machine_id}/${r.session?.session_id}`, r.launcher_note));
    return r;
  }
}

export async function deliverLaunch(id: string, clearDraft?: string) {
  let launch: AgentLaunch = getState().launches[id];
  if (!launch || (launch.phase !== "failed" && launch.phase !== "creating")) return;
  const controller = new AbortController();
  const deadline = setTimeout(() => controller.abort(), 240_000);
  const update = (patch: Partial<AgentLaunch>) => {
    launch = { ...launch, ...patch };
    if (getState().launches[id]) putLaunch(launch);
  };
  const hasFirstTurn = launchHasFirstTurn(launch);
  update({ phase: launch.session ? "uploading" : "creating", error: undefined, uncertain: false, clearable: undefined, draft: undefined });
  try {
    const imageBytes = launch.attachments.filter((a) => a.mime.startsWith("image/")).reduce((total, a) => total + Math.ceil(a.file.size / 3) * 4, 0);
    if (imageBytes + new TextEncoder().encode(launch.text).byteLength > 31 * 1024 * 1024) throw new Error(t("lib-new-agent.attachments_too_large_start"));
    const target = {
      machine_id: launch.machineId, cwd: launch.cwd, workspace_id: launch.workspaceId || undefined,
      desktop_project_id: launch.desktopProjectId || undefined,
      launcher: launch.launcher || undefined, agent_type: launch.agentType, work_session: launch.workSession,
      model: launch.model || undefined, effort: launch.effort || undefined, host: launch.host || undefined, todo_id: launch.todoId || undefined,
      launcher_default_effort: launch.launcherDefaultEffort,
      permission_mode: launch.permissionMode || undefined,
    };
    if (launch.session && launch.desktopSetupIncomplete && launch.launcher === "claude-desktop") {
      // A failed seed can precede import, model settings or permission setup. Resume only the known native id before
      // delivering the first message: the node validates it and never seeds another conversation or sends a model turn.
      update({ phase: "creating" });
      const r = await launchOp<{ session: Session }>("/sessions", launch.startOpId, controller.signal, {
        ...target, resume_session_id: launch.session.session_id, prompt: "", async: false,
      });
      update({ session: r.session, desktopSetupIncomplete: false });
      upsertSession(r.session);
    }
    if (!launch.session) {
      // A Codex (and a compatible CLI) takes its first message with the start. With none, a computer that can opens
      // Codex waiting for one (docs/integrations/codex.md "Starting without a first message"); an older one refuses.
      const startWithPrompt = async () => {
        if (launch.attachments.length) throw new Error(t("lib-new-agent.launcher_no_attachments"));
        let r: { session: Session };
        try {
          r = await launchOp<{ session: Session }>("/sessions", launch.startPromptOpId, controller.signal, { ...target, service_tier: launch.serviceTier, name_hint: launch.nameHint, prompt: launch.text || undefined });
        } catch (e) {
          if (!launch.text && e instanceof ApiError && (e.code === "codex_needs_prompt" || e.code === "work_prompt_required")) throw new Error(t("lib-new-agent.launcher_needs_text"));
          throw e;
        }
        update({ outgoing: { text: "", images: [] } });
        return r.session;
      };
      let session: Session;
      if (launch.inlinePrompt) {
        session = await startWithPrompt();
      } else {
        try {
          const r = await launchOp<{ session: Session }>("/sessions", launch.startOpId, controller.signal, {
            ...target,
            service_tier: launch.serviceTier,
            ultracode: launch.ultracode === true ? true : undefined, async: launch.launcher !== "claude-desktop" && launch.launcher !== "codex-app", name_hint: launch.nameHint ?? (launch.text || undefined),
          });
          session = r.session;
        } catch (e) {
          if (!(e instanceof ApiError) || e.code !== "codex_needs_prompt") throw e;
          session = await startWithPrompt();
        }
      }
      if (!getState().launches[id]) return;
      update({ session, ...(hasFirstTurn ? { handoffAt: Date.now() } : {}) });
      upsertSession(session);
    }
    if (!launch.outgoing) {
      update({ phase: "uploading" });
      const paths: string[] = [];
      const images: { mime: string; data: string }[] = [];
      // the pictures go scaled down as in a chat's box, held to the hub's limits (#12973); the ones past them, and any
      // the page cannot read, go to the computer as files with the others
      const limits = imageLimitsFor(launch.agentType && agentId(launch.agentType));
      const spilled = { count: 0, bytes: 0 };
      for (const att of launch.attachments) {
        let image: { mime: string; data: string } | null = null;
        if (att.mime.startsWith("image/") && images.length >= limits.per_message) spilled.count++;
        else if (att.mime.startsWith("image/") && att.file.size <= MAX_PICTURE_SOURCE) {
          const { fitPicture } = await import("./image-scale");
          image = await fitPicture(att.file, limits);
          if (image && pictureRoom(images.map((i) => i.data.length), image.data.length, limits) !== "ok") {
            image = null;
            spilled.bytes++;
          }
        }
        if (image) {
          images.push(image);
        } else {
          let path = launch.uploaded[att.id];
          if (!path) {
            path = await sendFile(launch.machineId, att.file, { name: att.name, signal: controller.signal });
            update({ uploaded: { ...launch.uploaded, [att.id]: path } });
          }
          paths.push(quotePath(path));
        }
      }
      if (spilled.count) toast(t("chat-composeroverlays.images_as_files", { max: limits.per_message, count: spilled.count }));
      if (spilled.bytes) toast(t("chat-composeroverlays.images_bytes_as_files", { count: spilled.bytes }));
      update({ outgoing: { text: [launch.text, ...paths].filter(Boolean).join("\n"), images } });
    }
    if (!getState().launches[id]) return;
    controller.signal.throwIfAborted();
    const out = launch.outgoing!;
    if (out.text || out.images.length) {
      update({ phase: "sending" });
      const payload = { ...out, client_msg_id: launch.messageId, clear_draft: clearDraft, todo_id: launch.todoId || undefined };
      if (new TextEncoder().encode(JSON.stringify(payload)).byteLength > 32 * 1024 * 1024) throw new Error(t("lib-new-agent.attachments_too_large_retry"));
      await launchOp(`${sessionPath(launch.machineId, launch.session!.session_id)}/messages`, launch.messageId, controller.signal, payload);
    }
    releaseAttachments(launch.attachments);
    update({ phase: "ready", desktopSetupIncomplete: false, attachments: [], ...(hasFirstTurn ? { handoffAt: Date.now() } : {}) });
    const local = { name: "session" as const, mid: launch.machineId, sid: launch.id };
    if (location.pathname === href(local)) navigate({ ...local, sid: launch.session!.session_id }, true);
  } catch (e) {
    const phase: AgentLaunch["phase"] = getState().launches[id]?.phase ?? "failed";
    const sending = phase === "sending";
    const creating = phase === "creating";
    // Native creation succeeded before import or first-message delivery failed. Keep its identity so Retry can only
    // send to this session; another session.start would create a second native conversation.
    const partial = creating && e instanceof ApiError && e.body.created === true && typeof e.body.session_id === "string" && e.body.session_id.trim()
      && (launch.launcher === "claude-desktop" || launch.launcher === "codex-app") ? e.body : undefined;
    if (partial) {
      const recovered = partial.session && typeof partial.session === "object" ? partial.session as Partial<Session> : {};
      update({ desktopSetupIncomplete: true, session: { title: launch.text.split("\n")[0] || t("startingagent.title"), cwd: launch.cwd, status: "unknown", ...recovered,
        machine_id: launch.machineId, session_id: partial.session_id as string } });
      upsertSession(launch.session!);
    }
    // no answer: the same op id on Retry finds out whether the hub had it, so nothing starts or sends twice
    const gap = isTransient(e) || isAbort(e);
    // the hub answered that it doesn't know, and its receipt would say so again: going on takes new ids and the person's word
    const uncertain = partial ? Boolean((launch.text || launch.attachments.length) && partial.first_message_delivered !== "none")
      : (creating || sending) && !gap && e instanceof ApiError && (e.body.delivered === "unknown" || (e.status >= 500 && e.body.delivered !== "none"));
    // the hub won't start it with ultracode here (an old node, a CLI or engine that can't take it): asking again would
    // get the same answer, so "Retry" starts it without, and the card says so (#3428)
    const noUltracode = creating && launch.ultracode === true && ultracodeRefused(e);
    update({ phase: "failed", handoffAt: undefined, uncertain, repeatOnly: creating && (gap || launch.workSession === true), ...(gap ? {} : launchOpIds()),
      ...(noUltracode ? { ultracode: undefined } : {}),
      error: noUltracode ? t("ultracode.start_refused", { error: ultracodeErrorText(e) }) : t(creating ? "lib-new-agent.start_failed" : sending ? "lib-new-agent.first_message_failed" : "lib-new-agent.attachments_failed", { error: errorText(e) }),
      clearable: e instanceof ApiError && e.code === "input_draft" && typeof e.body.clearable === "string" ? e.body.clearable : undefined,
      draft: e instanceof ApiError && e.code === "input_draft" ? String(e.body.draft ?? "") : undefined,
      shellWaiting: e instanceof ApiError && e.code === "shell_waiting" ? e.body : undefined,
    });
  } finally {
    clearTimeout(deadline);
  }
}
