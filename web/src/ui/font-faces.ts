let loading: Promise<unknown> | undefined;

/** Register the shipped faces before a consumer measures text. The first screen loads this after paint. */
export function loadFontFaces() {
  return loading ??= import("./font-assets/faces.css").catch(() => { loading = undefined; });
}
