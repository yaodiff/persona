// The part of noVNC's RFB (core/rfb.js, @novnc/novnc 1.7) the live screen uses; the package ships no types.
declare module "@novnc/novnc" {
  export default class RFB extends EventTarget {
    /** `urlOrChannel`: a WebSocket URL noVNC opens, or a WebSocket the page opened (noVNC takes it over and closes it). */
    constructor(target: HTMLElement, urlOrChannel: string | WebSocket, options?: { shared?: boolean; credentials?: { username?: string; password?: string }; wsProtocols?: string[] });
    viewOnly: boolean;
    scaleViewport: boolean;
    clipViewport: boolean;
    resizeSession: boolean;
    focusOnClick: boolean;
    background: string;
    qualityLevel: number;
    compressionLevel: number;
    sendCredentials(credentials: { username?: string; password?: string }): void;
    /** A key's press and release (down omitted), or one of them; ignored while viewOnly. */
    sendKey(keysym: number, code: string | null, down?: boolean): void;
    disconnect(): void;
    focus(): void;
  }
}
