// This browser's notification device id (#2971): what its Settings › Notifications go by on the hub (lib/notify.ts),
// its Web Push subscription (lib/web-push.ts, #7459), and what its socket's client_hello names (lib/store.ts), so the
// hub knows a page of this browser is open and pushes nothing to it then. Its own module: the socket is on the first
// screen, the rest of the notification code is not.

const DEVICE_KEY = "persona.notifyDevice";

const randomId = () => crypto.randomUUID?.() ?? Math.random().toString(36).slice(2);

/**
 * The id this browser's prefs go by on the hub: made once, kept in this browser (`kept`). The hub ties the row to the
 * device token that wrote it, so revoking this browser in Settings › Devices removes it. Without storage the id would be new
 * on every load: it is this page's own and nothing is sent (`kept: false`), so no browser leaves rows behind or shares
 * one id with another.
 */
let pageId: string | undefined;
export function deviceId(): { id: string; kept: boolean } {
  try {
    let id = localStorage.getItem(DEVICE_KEY);
    if (!id) {
      id = `web-${randomId()}`;
      localStorage.setItem(DEVICE_KEY, id);
    }
    return { id, kept: true };
  } catch {
    pageId ??= `web-${randomId()}`;
    return { id: pageId, kept: false };
  }
}

/** The device id a client_hello names: this browser's when it is kept, else none (a page's own id means nothing to the hub). */
export function helloDeviceId(): string | undefined {
  const { id, kept } = deviceId();
  return kept ? id : undefined;
}
