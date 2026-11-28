/**
 * App-wide floating toast capsule state matching iOS `Toasts.swift`.
 */

export type FloatingToastKind = 'info' | 'success' | 'error';

export class FloatingToastItem {
  readonly id: number;
  readonly kind: FloatingToastKind;
  readonly message: string;
  readonly actionTitle: string;
  readonly onAction: (() => void) | undefined;

  constructor(
    id: number,
    kind: FloatingToastKind,
    message: string,
    actionTitle: string = '',
    onAction?: () => void
  ) {
    this.id = id;
    this.kind = kind;
    this.message = message;
    this.actionTitle = actionTitle;
    this.onAction = onAction;
  }
}

@ObservedV2
export class ToastCenter {
  @Trace current: FloatingToastItem | undefined = undefined;
  private nextId: number = 1;
  private timer: number = -1;

  private durationFor(kind: FloatingToastKind, hasAction: boolean): number {
    if (hasAction) return 5000;
    if (kind === 'error') return 4000;
    return 2500;
  }

  show(
    message: string,
    kind: FloatingToastKind = 'info',
    actionTitle: string = '',
    onAction?: () => void
  ): void {
    const text = (message || '').trim();
    if (!text) return;
    if (this.timer !== -1) {
      clearTimeout(this.timer);
      this.timer = -1;
    }
    const id = this.nextId++;
    this.current = new FloatingToastItem(id, kind, text, actionTitle, onAction);
    const delay = this.durationFor(kind, !!actionTitle && !!onAction);
    this.timer = setTimeout(() => {
      this.timer = -1;
      if (this.current?.id === id) this.current = undefined;
    }, delay);
  }

  info(message: string, actionTitle: string = '', onAction?: () => void): void {
    this.show(message, 'info', actionTitle, onAction);
  }

  success(message: string, actionTitle: string = '', onAction?: () => void): void {
    this.show(message, 'success', actionTitle, onAction);
  }

  error(message: string, actionTitle: string = '', onAction?: () => void): void {
    this.show(message, 'error', actionTitle, onAction);
  }

  dismiss(): void {
    if (this.timer !== -1) {
      clearTimeout(this.timer);
      this.timer = -1;
    }
    this.current = undefined;
  }
}

export const toastCenter: ToastCenter = new ToastCenter();
