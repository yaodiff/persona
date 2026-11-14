// Whether this browser turned Web Push on (#7459, lib/web-push.ts): on the first screen, so the page can renew the
// subscription and take back what was pushed while it was closed without loading the rest before it is needed.

export const WEB_PUSH_KEY = "persona.webPush";

/** This browser turned "Notify this browser when Persona is closed" on. */
export function webPushFlag(): boolean {
  try {
    return localStorage.getItem(WEB_PUSH_KEY) === "1";
  } catch {
    return false;
  }
}
