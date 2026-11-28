/**
 * Long user message bubble folding matching iOS `BubbleFold.swift` (#5676).
 * Prevents multi-paragraph pasted prompts/logs from dominating the phone viewport
 * while preserving instant Show More / Show Less expansion.
 */

export interface BubbleFoldSpec {
  readonly limit: number;
  readonly shown: number;
}

export interface BubbleFoldDecision {
  readonly foldable: boolean;
  readonly estimatedLines: number;
  readonly foldedText: string;
}

export function bubbleFoldSpec(fontScale: number): BubbleFoldSpec {
  // Large accessibility scales (>= 1.35x) fold earlier so 10 lines never exceed screen height.
  if (fontScale >= 1.35) {
    return { limit: 9, shown: 5 };
  }
  return { limit: 14, shown: 10 };
}

export function charEmWidth(codePoint: number): number {
  // CJK, Kana, Hangul, full-width punctuation, and emoji occupy ~1 em; Latin/digits ~0.52 em.
  return codePoint >= 0x2e80 ? 1.0 : 0.52;
}

/**
 * Estimates visual line count and builds a clean folded prefix for long user messages.
 */
export function evaluateBubbleFold(text: string, bubbleWidthVp: number, fontScale: number): BubbleFoldDecision {
  const raw = text || '';
  if (raw.length < 120 && !raw.includes('\n')) {
    return { foldable: false, estimatedLines: 1, foldedText: raw };
  }
  const spec = bubbleFoldSpec(fontScale);
  const effectiveFontVp = 17 * Math.max(1, Math.min(fontScale, 2.2));
  const innerWidth = Math.max(140, (bubbleWidthVp > 0 ? bubbleWidthVp : 260) - 28);
  const emsPerLine = Math.max(8, innerWidth / effectiveFontVp);

  let lines = 1;
  let lineEms = 0;
  let cutIndex = -1;

  for (let i = 0; i < raw.length; i++) {
    const code = raw.charCodeAt(i);
    if (code === 10) {
      lines++;
      lineEms = 0;
    } else {
      const w = charEmWidth(code);
      if (lineEms > 0 && lineEms + w > emsPerLine) {
        lines++;
        lineEms = w;
      } else {
        lineEms += w;
      }
    }
    if (cutIndex < 0 && lines > spec.shown) {
      cutIndex = i;
    }
  }

  if (lines <= spec.limit || cutIndex <= 0) {
    return { foldable: false, estimatedLines: lines, foldedText: raw };
  }

  // Trim back to a clean whitespace boundary when possible (up to 24 chars back).
  let safeCut = cutIndex;
  for (let back = 0; back < 24 && safeCut > 0; back++) {
    const ch = raw.charCodeAt(safeCut - 1);
    if (ch === 32 || ch === 10 || ch === 9) {
      safeCut--;
      break;
    }
    safeCut--;
  }
  const prefix = (safeCut > cutIndex - 24 ? raw.slice(0, safeCut) : raw.slice(0, cutIndex)).trimEnd();
  return {
    foldable: true,
    estimatedLines: lines,
    foldedText: prefix + '…'
  };
}
