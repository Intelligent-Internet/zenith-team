import { useCallback, useEffect, useRef, useState } from "react";
import type { ToolActivity } from "@/features/onboarding/model/initialization-activity";
import { Icon, type IconName } from "@/shared/ui/icon";
import { Check, CircleAlert, RefreshCwIcon } from "@/shared/ui/icons";

const activityIcons: Record<ToolActivity["category"], IconName> = {
  website: "compass",
  search: "search",
  document: "book",
  command: "code",
  email: "email",
  tool: "toolbox",
};

export function OnboardingInitializationActivity({
  activities,
  running,
  unavailable,
  waitingText,
}: {
  activities: ToolActivity[];
  running: boolean;
  unavailable: boolean;
  waitingText: string;
}) {
  const [reducedMotion, setReducedMotion] = useState(true);
  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const sync = () => setReducedMotion(media.matches);
    sync();
    media.addEventListener("change", sync);
    return () => media.removeEventListener("change", sync);
  }, []);
  // The list is a reel centered on the current activity: the one running,
  // else the latest. Scrolling by hand or opening a row stops following
  // until Current activity is chosen.
  const scrollRef = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const current = currentActivityId(activities, running && !unavailable);
  const focusCurrent = useCallback(
    (behavior: ScrollBehavior) => {
      const scroller = scrollRef.current;
      const item = scroller?.querySelector<HTMLElement>(
        `[data-activity-id="${CSS.escape(current ?? "")}"]`,
      );
      if (!scroller || !item) return false;
      scroller.scrollTo({
        top: item.offsetTop - (scroller.clientHeight - item.offsetHeight) / 2,
        behavior,
      });
      return true;
    },
    [current],
  );
  const firstFocus = useRef(true);
  useEffect(() => {
    if (!following) return;
    if (
      focusCurrent(firstFocus.current || reducedMotion ? "instant" : "smooth")
    )
      firstFocus.current = false;
  }, [following, focusCurrent, reducedMotion, activities.length]);
  const stopFollowing = () => setFollowing(false);

  return (
    <section
      className="onboarding-initialization__activity"
      aria-label="Preparation activity"
    >
      {unavailable ? (
        <p>Reconnecting to activity. Your preparation status is shown above.</p>
      ) : null}
      {activities.length ? (
        <>
          <div
            ref={scrollRef}
            className="onboarding-initialization__activity-scroll"
            tabIndex={0}
            role="region"
            aria-label="Preparation activity history"
            onWheel={stopFollowing}
            onTouchMove={stopFollowing}
            onKeyDown={(event) => {
              if (scrollKeys.has(event.key)) stopFollowing();
            }}
          >
            <ol>
              {activities.map((activity) => {
                const active =
                  activity.status === "in_progress" && running && !unavailable;
                const failed = ["failed", "declined", "cancelled"].includes(
                  activity.status,
                );
                const completed = activity.status === "completed";
                const status = active
                  ? "Running"
                  : completed
                    ? "Completed"
                    : failed
                      ? "Could not complete"
                      : "Last reported activity";
                return (
                  <li
                    key={activity.id}
                    data-activity-id={activity.id}
                    data-current={activity.id === current}
                    data-active={active}
                    data-completed={completed}
                    data-failed={failed}
                  >
                    <details
                      onToggle={(event) => {
                        if (event.currentTarget.open) stopFollowing();
                      }}
                    >
                      <summary title="View activity details">
                        <span
                          className="onboarding-initialization__activity-icon"
                          aria-hidden="true"
                        >
                          {failed ? (
                            <CircleAlert size={16} />
                          ) : completed ? (
                            <Check size={16} />
                          ) : (
                            <Icon
                              name={activityIcons[activity.category]}
                              size={16}
                            />
                          )}
                        </span>
                        <span className="onboarding-initialization__activity-name">
                          {activity.label}
                          {activity.count > 1 ? (
                            <small> · {activity.count} steps</small>
                          ) : null}
                        </span>
                        <span
                          className="onboarding-initialization__activity-detail"
                          title={activity.detail || undefined}
                        >
                          {activity.detail}
                        </span>
                        <span
                          className="onboarding-initialization__activity-status"
                          aria-label={status}
                        >
                          {active ? (
                            <RefreshCwIcon
                              size={14}
                              className="onboarding-initialization__spinner"
                              aria-hidden="true"
                            />
                          ) : failed ? (
                            <small>{status}</small>
                          ) : null}
                        </span>
                      </summary>
                      <dl className="onboarding-initialization__activity-info">
                        <dt>Tool</dt>
                        <dd>{activity.name}</dd>
                        <dt>Status</dt>
                        <dd>{status}</dd>
                        {activity.url ? (
                          <>
                            <dt>Website</dt>
                            <dd>
                              <a
                                href={activity.url}
                                target="_blank"
                                rel="noopener noreferrer"
                              >
                                {activity.url}
                              </a>
                            </dd>
                          </>
                        ) : null}
                      </dl>
                    </details>
                  </li>
                );
              })}
            </ol>
          </div>
          <div className="onboarding-initialization__activity-footer">
            {!following ? (
              <button type="button" onClick={() => setFollowing(true)}>
                <Icon name="arrow-down" size={14} aria-hidden="true" />
                Current activity
              </button>
            ) : null}
          </div>
        </>
      ) : (
        <p>{waitingText}</p>
      )}
    </section>
  );
}

const scrollKeys = new Set([
  "ArrowUp",
  "ArrowDown",
  "PageUp",
  "PageDown",
  "Home",
  "End",
]);

function currentActivityId(activities: ToolActivity[], live: boolean) {
  const running = live
    ? [...activities].reverse().find((item) => item.status === "in_progress")
    : undefined;
  return (running ?? activities[activities.length - 1])?.id;
}
