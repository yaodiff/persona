import { act } from "react";
import { t } from "../i18n";

/** Expand More in an open single-Agent menu so its secondary commands can be exercised. */
export function openAgentMore() {
  const more = [...document.querySelectorAll<HTMLElement>('[role="menuitem"][aria-haspopup="menu"]')]
    .find((row) => row.textContent === t("sidebar.more"));
  if (!more) throw new Error("The Agent menu has no More submenu");
  act(() => more.click());
  return [...document.querySelectorAll<HTMLElement>('[role="menu"]')].at(-1)!;
}
