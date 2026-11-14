/** Only the currently installed hub window may remove the process-wide hub IPC channels. */
export class DesktopIPCOwner {
  private installed: object | undefined;

  install(): () => void {
    const release = {};
    this.installed = release;
    let owned = true;
    return () => {
      if (!owned) return;
      owned = false;
      if (this.installed === release) this.installed = undefined;
    };
  }

  get active(): boolean { return this.installed !== undefined; }
}
