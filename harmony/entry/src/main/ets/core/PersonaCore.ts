import native from 'libpersona_core_ohos.so';
import type { Action, ChatPatch, Effect } from './rustcore/Wire';
import { decodeChatPatch, decodeEffect, encodeAction } from './rustcore/Wire';

export interface CoreHost {
  changed(version: number): void;
  changedChat?(chat: string, version: number): void;
  run(effect: Effect): void;
}

/** The only ArkTS owner of a NAPI runtime handle. Transport remains in the native client. */
export class PersonaCore {
  private handle: number | null = null;

  open(host: CoreHost): void {
    if (this.handle !== null) throw new Error('core is already open');
    if (host.changedChat) {
      this.handle = native.coreCreateWithChat(native.coreConfig(), host.changed, host.changedChat,
        (bytes: Uint8Array): void => host.run(decodeEffect(bytes)));
    } else {
      this.handle = native.coreCreate(native.coreConfig(), host.changed,
        (bytes: Uint8Array): void => host.run(decodeEffect(bytes)));
    }
  }

  dispatch(action: Action): boolean {
    const handle = this.handle;
    if (handle === null) return false;
    return native.coreDispatch(handle, encodeAction(action));
  }

  drain(chat: string): ChatPatch | null {
    const handle = this.handle;
    if (handle === null) return null;
    return decodeChatPatch(native.coreDrain(handle, chat));
  }

  /** Tests and shutdown only; never wait for this on the UI thread. */
  flush(timeoutMillis: number): boolean {
    const handle = this.handle;
    return handle !== null && native.coreFlush(handle, timeoutMillis);
  }

  close(): void {
    const handle = this.handle;
    if (handle === null) return;
    this.handle = null;
    native.coreClose(handle);
  }
}
