export interface CoreHost {
  changed(version: number): void;
  changedChat?(chat: string, version: number): void;
  run(effect: Uint8Array): void;
}

export interface PersonaCoreNative {
  coreCreate(config: Uint8Array, changed: CoreHost['changed'], effects: CoreHost['run']): number;
  coreCreateWithChat(config: Uint8Array, changed: CoreHost['changed'], changedChat: NonNullable<CoreHost['changedChat']>, effects: CoreHost['run']): number;
  coreDispatch(handle: number, action: Uint8Array): boolean;
  coreDrain(handle: number, chat: string): Uint8Array;
  coreFlush(handle: number, timeoutMillis: number): boolean;
  coreClose(handle: number): void;
  coreConfig(): Uint8Array;
  coreProjectSort(input: string): string;
}

declare const native: PersonaCoreNative;
export default native;
