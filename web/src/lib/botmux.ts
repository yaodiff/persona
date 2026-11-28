// BotMux sessions as in Lark (docs/protocol/protocol.md "BotMux messages"): the node reads what botmux wrapped
// (Item.botmux); these read it for the chat. A message shows what was written in Lark with its facts as tags, a reply
// what the agent sent there. Items without `botmux` (other sessions, older nodes, text the node could not read) are
// drawn as before.
import { t } from "../i18n";
import type { Step, UserMessage } from "./turns";
import type { Botmux } from "./types";

/** The message as written in Lark: its words in place of the transcript's, no context chips (botmux's blocks are
 *  not the user's). Undefined when the node did not read it. */
export function decodedUser(msg: UserMessage): UserMessage | undefined {
  const b = msg.botmux;
  if (!b) return undefined;
  return { ...msg, text: b.text, contexts: [], files: undefined };
}

/** The tags under a message or a reply, in reading order. */
export function botmuxTags(b: Botmux): string[] {
  const tags: string[] = [];
  if (b.kind === "trigger") tags.push(b.source ? t("chat-botmux.via", { source: b.source }) : t("chat-botmux.triggered"));
  if (b.kind === "reply") tags.push(t(b.final ? "chat-botmux.sent_final" : "chat-botmux.sent"));
  if (b.sender) tags.push(b.sender_type === "bot" ? t("chat-botmux.bot", { name: b.sender }) : b.sender);
  if (b.quoted) tags.push(t("chat-botmux.quoted"));
  if (b.topic_context) tags.push(t("chat-botmux.topic"));
  if (b.attachments) tags.push(t("chat-botmux.attachments", { count: b.attachments }));
  for (const name of b.mentions ?? []) tags.push(`@${name}`);
  if (b.voice) tags.push(t("chat-botmux.voice"));
  if (b.truncated) tags.push(t("chat-botmux.truncated"));
  return tags;
}

/** The replies an exchange sent to Lark, in order: its completed, successful `botmux send` calls, and those its folded work hides. */
export function botmuxReplies(steps: readonly Step[]): { id: string; reply: Botmux }[] {
  const out: { id: string; reply: Botmux }[] = [];
  for (const s of steps) {
    if (s.kind === "tool" && s.use.botmux?.kind === "reply" && s.result && !s.result.is_error) out.push({ id: s.use.id, reply: s.use.botmux });
    else if (s.kind === "fold") s.item.fold?.botmux?.forEach((reply, i) => out.push({ id: `${s.item.id}:${i}`, reply }));
  }
  return out;
}
