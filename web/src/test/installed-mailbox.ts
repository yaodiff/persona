import { MODULES } from "../modules";
import { loadModule } from "../core/module-host";

export const installedMailbox = MODULES.find((m) => m.mailbox);

/** Load the installed renderer before testing actions inside its page. */
export async function preloadMailbox(part: "Body" | "Compose" | "Settings" | "InboxSwitch") {
  if (!installedMailbox) return;
  const component = (await loadModule(installedMailbox.id)).mailbox![part];
  if ("preload" in component && typeof component.preload === "function") await component.preload();
}
