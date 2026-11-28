import type { InboxItem } from "../../lib/inbox";
import { mailItemReadOnly, useMailReading } from "../../lib/mail";
import { useLarkItemReadOnly } from "./lark-write-gate";

/** Whether a reply to `item` cannot be sent because the hub turned off the module that sends it: Lark's for a Lark
 *  item (lark-write-gate), mail's for a mail (#15995). What the item already holds stays readable; only the controls
 *  that would send are left out. */
export function useItemReadOnly(item?: InboxItem): boolean {
  const lark = useLarkItemReadOnly(item);
  const reading = useMailReading();
  return mailItemReadOnly(reading, item) || lark;
}
