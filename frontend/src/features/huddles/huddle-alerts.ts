import {
  closeDesktopHuddleCallNotification,
  shouldUseDesktopHashRouter,
  showDesktopHuddleCallNotification,
} from "@/app/desktop/desktop-environment.service";
import {
  isDesktopNotificationKindEnabled,
  loadDesktopWorkbenchPreferences,
} from "@/app/desktop/desktop-workbench-preferences";

export function playHuddleRingtone() {
  if (typeof window === "undefined") return;
  const AudioContextCtor =
    window.AudioContext ??
    (window as Window & { webkitAudioContext?: typeof AudioContext })
      .webkitAudioContext;
  if (!AudioContextCtor) return;

  try {
    const context = new AudioContextCtor();
    const oscillator = context.createOscillator();
    const gain = context.createGain();
    oscillator.frequency.value = 880;
    gain.gain.setValueAtTime(0.0001, context.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.08, context.currentTime + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.0001, context.currentTime + 0.25);
    oscillator.connect(gain);
    gain.connect(context.destination);
    oscillator.start();
    oscillator.stop(context.currentTime + 0.25);
    oscillator.addEventListener("ended", () => void context.close());
  } catch {
    // Browser autoplay policy or an unavailable audio device is non-fatal.
  }
}

export async function showHuddleNotification(
  invitationId: string,
  copy: { title: string; body: string },
): Promise<{ shown: boolean; close?: () => void }> {
  if (shouldUseDesktopHashRouter()) {
    // Calls stay on screen until answered, cancelled or expired, and offer
    // Join from the notification itself.
    const result = await showDesktopHuddleCallNotification({
      ...copy,
      invitationId,
      allowNative: isDesktopNotificationKindEnabled(
        loadDesktopWorkbenchPreferences(),
        "huddle_call",
      ),
    });
    if (!result.ok || result.disposition !== "shown") return { shown: false };
    return {
      shown: true,
      close: () => closeDesktopHuddleCallNotification(invitationId),
    };
  }
  if (
    typeof Notification === "undefined" ||
    Notification.permission !== "granted"
  )
    return { shown: false };
  try {
    const notification = new Notification(copy.title, {
      body: copy.body,
      tag: `zenith-huddle-invitation-${invitationId}`,
    });
    notification.onclick = () => {
      window.focus();
      notification.close();
    };
    return { shown: true, close: () => notification.close() };
  } catch {
    // Notification permission can disappear between the check and constructor.
  }
  return { shown: false };
}

export async function requestHuddleNotifications(): Promise<boolean> {
  if (shouldUseDesktopHashRouter()) return true;
  if (typeof Notification === "undefined") return false;
  if (Notification.permission === "granted") return true;
  if (Notification.permission === "denied") return false;
  return (await Notification.requestPermission()) === "granted";
}
