import { loadFontFaces } from "./font-faces";
import { startFontSync } from "./font-sync";

// Vite may share this chunk with Settings. Wait for the after-paint signal even if Settings loaded it first.
addEventListener("persona-font-startup", () => {
  void loadFontFaces();
  startFontSync();
}, { once: true });
