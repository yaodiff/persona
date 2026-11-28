import { common } from '@kit.AbilityKit';
import { preferences } from '@kit.ArkData';
import { insightIntent as suggestionsKit } from '@kit.IntentsKit';
import {
  SuggestionSeed,
  XIAOYI_INTENT_CATALOG,
  buildSafeSuggestionSeeds,
  isGracefulSuggestionError
} from '../core/XiaoYiIntentCatalog';

const PREF_STORE: string = 'persona.xiaoyi';
const PREF_KEY: string = 'xiaoyi.suggestions.enabled';

export interface SuggestionShareOutcome {
  shared: boolean;
  count: number;
  status: 'disabled' | 'shared' | 'unregistered' | 'unavailable';
  code: number;
}

function readErrorCode(error: Error): number {
  const fields = error as Object as Record<string, Object>;
  const code = fields['code'];
  return typeof code === 'number' ? code as number : 0;
}

/**
 * Manages opt-in XiaoYi Suggestions (`@kit.IntentsKit` shareIntent/deleteIntent).
 * Off by default until the user enables it in XiaoYi settings; gracefully degrades in unsigned dev builds.
 */
export class IntentSuggestionsService {
  private static debounceTimer: number = -1;
  private static lastSignature: string = '';

  static resetSchedule(): void {
    if (IntentSuggestionsService.debounceTimer !== -1) {
      clearTimeout(IntentSuggestionsService.debounceTimer);
      IntentSuggestionsService.debounceTimer = -1;
    }
    IntentSuggestionsService.lastSignature = '';
  }

  /** Debounce live Hub status updates before calling `shareIntent` when user opted in. */
  static scheduleStatusShare(
    context: common.Context,
    pendingCount: number,
    workingCount: number,
    delayMs: number = 2000
  ): void {
    const signature = String(pendingCount) + ':' + String(workingCount);
    if (signature === IntentSuggestionsService.lastSignature && IntentSuggestionsService.debounceTimer === -1) {
      return;
    }
    if (IntentSuggestionsService.debounceTimer !== -1) {
      clearTimeout(IntentSuggestionsService.debounceTimer);
      IntentSuggestionsService.debounceTimer = -1;
    }
    IntentSuggestionsService.debounceTimer = setTimeout(() => {
      IntentSuggestionsService.debounceTimer = -1;
      IntentSuggestionsService.shareStatusSuggestions(context, pendingCount, workingCount)
        .then((outcome: SuggestionShareOutcome) => {
          if (outcome.shared || outcome.status === 'disabled') {
            IntentSuggestionsService.lastSignature = signature;
          }
        })
        .catch(() => {});
    }, Math.max(0, delayMs));
  }

  static async isEnabled(context: common.Context): Promise<boolean> {
    try {
      const store = await preferences.getPreferences(context, PREF_STORE);
      const val = await store.get(PREF_KEY, false);
      return val === true || val === '1';
    } catch (_) {
      return false;
    }
  }

  static async setEnabled(context: common.Context, enabled: boolean): Promise<boolean> {
    try {
      const store = await preferences.getPreferences(context, PREF_STORE);
      await store.put(PREF_KEY, enabled);
      await store.flush();
    } catch (_) {
      return false;
    }
    if (!enabled) {
      IntentSuggestionsService.resetSchedule();
      await IntentSuggestionsService.clearAll(context);
    }
    return true;
  }

  static async shareStatusSuggestions(
    context: common.Context,
    pendingCount: number,
    workingCount: number
  ): Promise<SuggestionShareOutcome> {
    const enabled = await IntentSuggestionsService.isEnabled(context);
    if (!enabled) {
      return { shared: false, count: 0, status: 'disabled', code: 0 };
    }
    if (!canIUse('SystemCapability.AI.InsightIntent')) {
      return { shared: false, count: 0, status: 'unavailable', code: 0 };
    }
    const seeds: SuggestionSeed[] = buildSafeSuggestionSeeds(pendingCount, workingCount);
    const items: suggestionsKit.InsightIntent[] = seeds.map((seed: SuggestionSeed): suggestionsKit.InsightIntent => {
      const actionInfo: suggestionsKit.IntentActionInfo = {};
      actionInfo['actionMode'] = seed.actionMode;
      const entityInfo: suggestionsKit.IntentEntityInfo = {
        entityId: seed.entityId,
        entityName: seed.entityName
      };
      entityInfo['title'] = seed.title;
      return {
        intentName: seed.intentName,
        intentVersion: '1.0.0',
        identifier: seed.identifier,
        intentActionInfo: actionInfo,
        intentEntityInfo: entityInfo
      };
    });
    try {
      await suggestionsKit.shareIntent(context, items);
      return { shared: true, count: items.length, status: 'shared', code: 0 };
    } catch (error) {
      const code = readErrorCode(error as Error);
      if (code === 1000101101) {
        return { shared: false, count: 0, status: 'unregistered', code: code };
      }
      if (isGracefulSuggestionError(code)) {
        return { shared: false, count: 0, status: 'unavailable', code: code };
      }
      return { shared: false, count: 0, status: 'unavailable', code: code };
    }
  }

  static async clearAll(context: common.Context): Promise<void> {
    IntentSuggestionsService.resetSchedule();
    if (!canIUse('SystemCapability.AI.InsightIntent')) return;
    for (let i = 0; i < XIAOYI_INTENT_CATALOG.length; i++) {
      try {
        await suggestionsKit.deleteIntent(context, XIAOYI_INTENT_CATALOG[i].intentName);
      } catch (_) {
        // Unregistered or unavailable in local dev builds is non-fatal.
      }
    }
  }
}
