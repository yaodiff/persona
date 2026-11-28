/**
 * Pure TypeScript Next-Message Suggestions model and composer chip gating,
 * aligned with iOS (`NextSuggestionsModel.swift`, `ChatView.swift:5684-5742`)
 * and Go Hub (`server/internal/assistant/profile.go`, `suggest_next.go`).
 */

export interface ComposerSuggestionChip {
  readonly text: string;
  readonly risky: boolean;
}

function asObj(raw: Object | null | undefined): Record<string, Object> | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  return raw as Record<string, Object>;
}

/**
 * Parses `next_suggestions` from `GET/PUT /api/v1/assistant`.
 * Returns `null` when absent (legacy Hub without the setting) so the toggle stays hidden.
 */
export function parseAssistantNextSuggestions(raw: Object | null | undefined): boolean | null {
  const obj = asObj(raw);
  if (!obj) return null;
  const val = obj['next_suggestions'];
  return typeof val === 'boolean' ? val : null;
}

/**
 * Builds the `PUT /api/v1/assistant` payload containing only `next_suggestions`.
 */
export function buildNextSuggestionsPayload(enabled: boolean): Record<string, Object> {
  const body: Record<string, Object> = {};
  body['next_suggestions'] = enabled;
  return body;
}

/**
 * Resolves the authoritative value after `PUT /api/v1/assistant` (matching iOS `state.nextSuggestions ?? on`).
 */
export function resolveAuthoritativeNextSuggestions(raw: Object | null | undefined, requested: boolean): boolean {
  const parsed = parseAssistantNextSuggestions(raw);
  return parsed !== null ? parsed : requested;
}

/**
 * Extracts follow-up suggestion chips from a session/conversation wire record.
 * Supports both the canonical single `suggestion` + `suggestion_risky` fields and multi-chip `suggestions` arrays.
 */
export function extractSessionSuggestions(raw: Object | null | undefined): ComposerSuggestionChip[] {
  const obj = asObj(raw);
  if (!obj) return [];
  const chips: ComposerSuggestionChip[] = [];
  const seen = new Set<string>();

  const multi = obj['suggestions'];
  if (Array.isArray(multi)) {
    for (const entry of multi as Object[]) {
      if (typeof entry === 'string') {
        const text = entry.trim();
        if (text && !seen.has(text)) {
          seen.add(text);
          chips.push({ text, risky: false });
        }
      } else {
        const item = asObj(entry);
        const text = item && typeof item['text'] === 'string' ? (item['text'] as string).trim() : '';
        if (text && !seen.has(text)) {
          seen.add(text);
          chips.push({ text, risky: item?.['risky'] === true });
        }
      }
    }
  }

  const single = typeof obj['suggestion'] === 'string' ? (obj['suggestion'] as string).trim() : '';
  if (single && !seen.has(single)) {
    seen.add(single);
    chips.push({ text: single, risky: obj['suggestion_risky'] === true });
  }
  return chips;
}

/**
 * Direct "Send Now" is permitted ONLY when the suggestion is NOT marked risky (`!chip.risky`).
 * Risky suggestions may only fill the composer draft for explicit owner review.
 */
export function canDirectSendSuggestion(chip: ComposerSuggestionChip): boolean {
  return !chip.risky && chip.text.trim().length > 0;
}
