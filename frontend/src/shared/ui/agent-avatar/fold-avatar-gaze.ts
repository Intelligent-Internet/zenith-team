const followers = new Set<HTMLElement>();
let pointer: { x: number; y: number } | null = null;
let frame: number | null = null;

function resetGaze(avatar: HTMLElement) {
  avatar.style.setProperty("--fold-gaze-x", "0px");
  avatar.style.setProperty("--fold-gaze-y", "0px");
}

function updateGaze() {
  frame = null;
  if (document.hidden) return;
  const target = pointer;
  if (!target) {
    followers.forEach(resetGaze);
    return;
  }

  // Read every active avatar before writing any styles.
  const positions = Array.from(followers)
    .filter((avatar) => !avatar.hasAttribute("data-paused"))
    .map((avatar) => ({ avatar, bounds: avatar.getBoundingClientRect() }));
  for (const { avatar, bounds } of positions) {
    if (!bounds.width || !bounds.height) continue;
    const dx = target.x - (bounds.left + bounds.width / 2);
    const dy = target.y - (bounds.top + bounds.height / 2);
    const distance = Math.max(bounds.width * 0.6, Math.hypot(dx, dy));
    avatar.style.setProperty("--fold-gaze-x", `${(dx / distance) * 10}px`);
    avatar.style.setProperty("--fold-gaze-y", `${(dy / distance) * 11}px`);
  }
}

function scheduleGaze() {
  if (document.hidden || frame !== null) return;
  frame = requestAnimationFrame(updateGaze);
}

function movePointer(event: PointerEvent) {
  if (event.pointerType === "touch") return;
  pointer = { x: event.clientX, y: event.clientY };
  scheduleGaze();
}

function clearPointer() {
  pointer = null;
  if (frame !== null) cancelAnimationFrame(frame);
  frame = null;
  followers.forEach(resetGaze);
}

function syncVisibility() {
  if (document.hidden) clearPointer();
}

/** Share pointer events and one pending frame across the opted-in avatars. */
export function followAvatarPointer(avatar: HTMLElement) {
  followers.add(avatar);
  if (followers.size === 1) {
    window.addEventListener("pointermove", movePointer, { passive: true });
    window.addEventListener("resize", scheduleGaze, { passive: true });
    document.addEventListener("scroll", scheduleGaze, {
      passive: true,
      capture: true,
    });
    document.documentElement.addEventListener("pointerleave", clearPointer);
    window.addEventListener("blur", clearPointer);
    document.addEventListener("visibilitychange", syncVisibility);
  }
  scheduleGaze();

  return () => {
    resetGaze(avatar);
    followers.delete(avatar);
    if (followers.size) return;
    if (frame !== null) cancelAnimationFrame(frame);
    frame = null;
    window.removeEventListener("pointermove", movePointer);
    window.removeEventListener("resize", scheduleGaze);
    document.removeEventListener("scroll", scheduleGaze, true);
    document.documentElement.removeEventListener("pointerleave", clearPointer);
    window.removeEventListener("blur", clearPointer);
    document.removeEventListener("visibilitychange", syncVisibility);
  };
}
