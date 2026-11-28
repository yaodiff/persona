import { useEffect, useState } from "react";
import { fontFamily, quoteFont, type FontChoice, type FontKind } from "./font-preference";

/** A width probe compares the Latin sample against several generic stacks. A privacy setting or identical metrics can
 * make the result inconclusive. FontFaceSet.check also returns true for unknown family names, so it cannot do this. */
export function useFontAvailability(kind: FontKind, choice: FontChoice) {
  const family = choice.preset === "system" || choice.preset === "serif" ? null : fontFamily(kind, choice);
  const [result, setResult] = useState<{ family: string; available: boolean }>();
  useEffect(() => {
    if (!family) return;
    const canvas = document.createElement("canvas");
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const probe = () => {
      const f = quoteFont(family);
      const sample = "Wim1l0O @{}[]";
      const available = ["monospace", "serif", "sans-serif"].some((generic) => {
        ctx.font = `48px ${generic}`;
        const fallback = ctx.measureText(sample).width;
        ctx.font = `48px ${f}, ${generic}`;
        return ctx.measureText(sample).width !== fallback;
      });
      setResult({ family, available });
    };
    probe();
    const fonts = document.fonts;
    fonts?.addEventListener("loadingdone", probe);
    fonts?.addEventListener("loadingerror", probe);
    return () => {
      fonts?.removeEventListener("loadingdone", probe);
      fonts?.removeEventListener("loadingerror", probe);
    };
  }, [family]);
  return family && result?.family === family ? result.available : null;
}
