import { vibrator } from '@kit.SensorServiceKit';

/**
 * Tactile feedback matching iOS `Haptics` (apple/Persona/App/AppModel.swift):
 * tap / selection / success / error / attention.
 * All calls degrade silently on devices or emulators without a vibrator motor or permission.
 */
export class Haptics {
  private static triggerPreset(effectId: string, usage: vibrator.Usage): void {
    try {
      vibrator.startVibration({ type: 'preset', effectId: effectId, count: 1 }, { usage: usage }).catch(() => {});
    } catch (_) {
      // Emulator or device without vibrator support: degrade quietly.
    }
  }

  static tap(): void {
    Haptics.triggerPreset(vibrator.HapticFeedback.EFFECT_SOFT, 'touch');
  }

  static selection(): void {
    Haptics.triggerPreset(vibrator.EffectId.EFFECT_CLOCK_TIMER, 'physicalFeedback');
  }

  static success(): void {
    Haptics.triggerPreset(vibrator.HapticFeedback.EFFECT_NOTICE_SUCCESS, 'notification');
  }

  static error(): void {
    Haptics.triggerPreset(vibrator.HapticFeedback.EFFECT_NOTICE_FAILURE, 'notification');
  }

  static attention(): void {
    Haptics.triggerPreset(vibrator.HapticFeedback.EFFECT_NOTICE_WARNING, 'notification');
  }
}
