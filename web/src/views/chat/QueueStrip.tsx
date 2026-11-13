// Messages typed while the agent works and not yet taken (Session.queued): the panel at the top of the composer that
// shows them is its own chunk (QueuePanel.tsx, cindy's pending queue, #5327), loaded once something is queued; what
// other parts read without it stays here. A queued message is only text, with what it carried written into it (#3032):
// pulling one back puts the words under what is typed, its photos back in the tray (read from the machine), its
// contexts back on their chips and a file back as its path. In a Persona chat (#4342) the node takes messages back
// over stream-json, and its answer carries their photos, which go back in the tray as they were sent.
import { Suspense } from "react";
import { fileDownloadURL } from "../../lib/api";
import { imagePayload, MAX_IMAGE, quotePath } from "../../lib/attachments";
import { sendDraft, type DraftImage } from "../../lib/drafts";
import { lazyPage } from "../../lib/lazy-page";
import { addContext, newContext, type ContextRef } from "../../lib/links";
import { queuedDraft, type QueuedParts } from "../../lib/queue";
import type { Session } from "../../lib/types";
import { toast } from "../../ui/atoms";
import { t } from "../../i18n";
import type { QueuePanelProps } from "./QueuePanel";

// Not `idle`: ChatView renders QueueStrip only while a message is queued (hasQueue), so this chunk loads on that first
// queued message instead of in every cold start's idle tier. The Suspense below holds the composer while it arrives.
const QueuePanel = lazyPage(() => import("./QueuePanel").then((m) => m.QueuePanel));

/** A photo of a queued message, read back from the session's machine for the composer's tray; null when it can't be. */
export async function readPhoto(mid: string, path: string, cwd?: string): Promise<DraftImage | null> {
  try {
    const r = await fetch(fileDownloadURL(mid, path, cwd, { inline: true }));
    const blob = r.ok ? await r.blob() : null;
    if (!blob || !blob.type.startsWith("image/") || blob.size > MAX_IMAGE) return null;
    const name = path.split("/").pop() || "image.png";
    return { name, ...(await imagePayload(new File([blob], name, { type: blob.type }))) };
  } catch {
    return null;
  }
}

/** A photo a Persona chat's queue gave back with its message (#4342), as the composer's tray holds it. */
export interface QueuedImage {
  mime: string;
  data: string; // base64
}

/** Puts a pulled-back message in the composer of `draftKey`, read apart. Photos the machine no longer gives back go in
 *  as their paths, so nothing that was sent is lost; `images` are photos that came back with it (a Persona chat's). */
export async function restoreQueued(draftKey: string, session: Session, p: QueuedParts, images: QueuedImage[] = []) {
  const text = queuedDraft(p);
  if (text) sendDraft(draftKey, { type: "append", text });
  // a kind this composer has no chip of its own for is shown as a quote and sent back as it came
  for (const c of p.contexts) addContext(draftKey, { ...newContext(c.kind as ContextRef["kind"], c.title, c.url, c.text), ...(c.attrs ? { attrs: c.attrs } : {}) });
  images.forEach((img, i) => sendDraft(draftKey, { type: "image", image: { name: `photo-${i + 1}.${img.mime.split("/")[1] || "png"}`, ...img } }));
  if (!p.photos.length) return;
  const read = await Promise.all(p.photos.map((path) => readPhoto(session.machine_id, path, session.cwd)));
  const lost: string[] = [];
  read.forEach((img, i) => {
    if (!img || !sendDraft(draftKey, { type: "image", image: img })) lost.push(p.photos[i]);
  });
  if (lost.length) {
    sendDraft(draftKey, { type: "append", text: lost.map(quotePath).join(" ") });
    toast(t("chat-queuestrip.photos_as_paths", { count: lost.length }), "error");
  }
}

/** The queued messages at the top of the composer (QueuePanel.tsx), drawn once its chunk is in. */
export function QueueStrip(props: QueuePanelProps) {
  return (
    <Suspense fallback={null}>
      <QueuePanel {...props} />
    </Suspense>
  );
}
