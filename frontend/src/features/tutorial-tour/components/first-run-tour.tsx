import {
  EVENTS,
  Joyride,
  STATUS,
  type EventData,
  type TooltipRenderProps,
} from "react-joyride";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router";

import {
  createTutorialTourSteps,
  isTutorialTourTargetVisible,
  waitForTutorialTourTarget,
} from "@/features/tutorial-tour/model/tutorial-tour-steps";
import { useTutorialTour } from "@/features/tutorial-tour/tutorial-tour-context";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { ArrowLeft, ArrowRight, XIcon } from "@/shared/ui/icons";

import "@/features/tutorial-tour/styles/tutorial-tour.css";

const targetWaitTimeoutMs = 4_000;

type TourPhase = "idle" | "running" | "transitioning" | "welcome";

export function FirstRunTour({ enabled }: { enabled: boolean }) {
  const location = useLocation();
  const navigate = useNavigate();
  const { replayRequest, state, updateStatus } = useTutorialTour();
  const [phase, setPhase] = useState<TourPhase>("idle");
  const [isMobile, setIsMobile] = useState(
    () => window.matchMedia("(max-width: 899px)").matches,
  );
  const [stepIndex, setStepIndex] = useState(0);
  const [runKey, setRunKey] = useState(0);
  const chatHrefRef = useRef("/app");
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const eligibilityHandledRef = useRef(false);
  const handledReplayRequestRef = useRef(0);
  const replaySessionRef = useRef(false);
  const transitionRef = useRef<AbortController | null>(null);
  const directionRef = useRef<1 | -1>(1);
  const steps = useMemo(() => createTutorialTourSteps(isMobile), [isMobile]);
  const welcomeTarget = steps[0].target;

  useEffect(() => {
    const mediaQuery = window.matchMedia("(max-width: 899px)");
    const handleChange = () => setIsMobile(mediaQuery.matches);
    mediaQuery.addEventListener("change", handleChange);
    return () => mediaQuery.removeEventListener("change", handleChange);
  }, []);

  useEffect(() => {
    if (!enabled || phase !== "idle" || location.pathname !== "/app") return;

    const replayRequested = replayRequest > handledReplayRequestRef.current;
    const firstRunEligible =
      state?.status === "eligible" && !eligibilityHandledRef.current;
    if (!replayRequested && !firstRunEligible) return;

    let opened = false;
    const openWelcome = () => {
      if (opened || !isTutorialTourTargetVisible(welcomeTarget)) return;
      opened = true;
      replaySessionRef.current = replayRequested && !firstRunEligible;
      handledReplayRequestRef.current = replayRequest;
      eligibilityHandledRef.current = true;
      chatHrefRef.current = `${location.pathname}${location.search}`;
      returnFocusRef.current =
        document.activeElement instanceof HTMLElement &&
        document.activeElement !== document.body
          ? document.activeElement
          : null;
      setPhase("welcome");
    };

    openWelcome();
    const observer = new MutationObserver(openWelcome);
    observer.observe(document.body, {
      attributes: true,
      childList: true,
      subtree: true,
    });
    return () => observer.disconnect();
  }, [
    enabled,
    location.pathname,
    location.search,
    phase,
    replayRequest,
    state,
    welcomeTarget,
  ]);

  const returnToChat = useCallback(() => {
    transitionRef.current?.abort();
    setPhase("idle");
    navigate(chatHrefRef.current, { replace: true });
    window.requestAnimationFrame(() => {
      const previous = returnFocusRef.current;
      const target =
        previous?.isConnected && previous.getClientRects().length
          ? previous
          : document.querySelector<HTMLElement>(welcomeTarget);
      target?.focus({ preventScroll: true });
    });
  }, [navigate, welcomeTarget]);

  const finishTour = useCallback(() => {
    updateStatus("completed");
    returnToChat();
  }, [returnToChat, updateStatus]);

  const skipTour = useCallback(() => {
    if (!replaySessionRef.current) updateStatus("skipped");
    returnToChat();
  }, [returnToChat, updateStatus]);

  const showAvailableStep = useCallback(
    async (requestedIndex: number, direction: 1 | -1) => {
      transitionRef.current?.abort();
      const transition = new AbortController();
      transitionRef.current = transition;
      directionRef.current = direction;
      setPhase("transitioning");

      for (
        let candidateIndex = requestedIndex;
        candidateIndex >= 0 && candidateIndex < steps.length;
        candidateIndex += direction
      ) {
        const candidate = steps[candidateIndex];
        navigate(
          candidate.data.route === "/app"
            ? chatHrefRef.current
            : candidate.data.route,
          { replace: true },
        );
        const targetReady = await waitForTutorialTourTarget(
          candidate.target,
          targetWaitTimeoutMs,
          transition.signal,
        );
        if (transition.signal.aborted) return;
        if (!targetReady) continue;

        setStepIndex(candidateIndex);
        setPhase("running");
        return;
      }

      finishTour();
    },
    [finishTour, navigate, steps],
  );

  useEffect(() => {
    if (!enabled) {
      transitionRef.current?.abort();
      setPhase("idle");
    }
    return () => transitionRef.current?.abort();
  }, [enabled]);

  useEffect(() => {
    if (phase !== "running" && phase !== "transitioning") return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      skipTour();
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [phase, skipTour]);

  const goToPreviousStep = useCallback(() => {
    if (stepIndex > 0) void showAvailableStep(stepIndex - 1, -1);
  }, [showAvailableStep, stepIndex]);

  const goToNextStep = useCallback(() => {
    if (stepIndex + 1 >= steps.length) finishTour();
    else void showAvailableStep(stepIndex + 1, 1);
  }, [finishTour, showAvailableStep, stepIndex, steps.length]);

  const startTour = useCallback(() => {
    setRunKey((key) => key + 1);
    void showAvailableStep(0, 1);
  }, [showAvailableStep]);

  const handleEvent = useCallback(
    (event: EventData) => {
      if (event.type === EVENTS.TARGET_NOT_FOUND && phase === "running") {
        void showAvailableStep(
          event.index + directionRef.current,
          directionRef.current,
        );
      } else if (event.type === EVENTS.TOUR_END) {
        if (event.status === STATUS.SKIPPED) skipTour();
        else if (event.status === STATUS.FINISHED) finishTour();
      }
    },
    [finishTour, phase, showAvailableStep, skipTour],
  );

  const Tooltip = useCallback(
    (props: TooltipRenderProps) => (
      <TutorialTourTooltip
        {...props}
        onBack={goToPreviousStep}
        onNext={goToNextStep}
        onSkip={skipTour}
      />
    ),
    [goToNextStep, goToPreviousStep, skipTour],
  );

  return (
    <>
      <Dialog
        open={phase === "welcome"}
        onOpenChange={(open) => {
          if (!open && phase === "welcome") skipTour();
        }}
      >
        <DialogContent
          className="tutorial-tour-welcome"
          overlayClassName="tutorial-tour-welcome-overlay"
          showCloseButton={false}
          onCloseAutoFocus={(event) => event.preventDefault()}
        >
          <button
            aria-label="Skip tour"
            className="tutorial-tour-close"
            type="button"
            onClick={skipTour}
          >
            <XIcon size={18} aria-hidden="true" />
          </button>
          <p className="tutorial-tour-welcome__meta">
            {steps.length} quick stops · About a minute
          </p>
          <DialogTitle>Get to know Zenith Team</DialogTitle>
          <DialogDescription>
            Find your conversations, organize work, and explore the tools your
            team shares.
          </DialogDescription>
          <p className="m-[18px_0_0] text-workspace-muted text-[length:12px] leading-[18px]">
            You can take this tour anytime from Help.
          </p>
          <div className="flex justify-end gap-[8px] mt-[24px]">
            <button
              className="tutorial-tour-button tutorial-tour-button--secondary"
              type="button"
              onClick={skipTour}
            >
              Not now
            </button>
            <button
              className="tutorial-tour-button tutorial-tour-button--primary"
              type="button"
              onClick={startTour}
            >
              Start tour
              <ArrowRight size={16} aria-hidden="true" />
            </button>
          </div>
        </DialogContent>
      </Dialog>

      {phase === "transitioning" ? (
        <div className="tutorial-tour-transition">
          <div className="flex items-center gap-[20px] [border:1px_solid_var(--workspace-divider)] rounded-[8px] p-[10px_14px] [background:var(--workspace-overlay-bg)] text-workspace-muted [box-shadow:var(--workspace-overlay-shadow)] font-workspace-body text-[length:13px] leading-[20px]">
            <span role="status">Opening tour step…</span>
            <button type="button" onClick={skipTour}>
              End tour
            </button>
          </div>
        </div>
      ) : null}

      <Joyride
        key={runKey}
        floatingOptions={{
          // Sidebar changes can move an anchor without resizing the anchor itself.
          autoUpdate: { animationFrame: true },
          flipOptions: { crossAxis: true, padding: 16 },
          shiftOptions: { crossAxis: true, padding: 16 },
        }}
        onEvent={handleEvent}
        options={{
          arrowBase: 14,
          arrowSize: 7,
          arrowColor: "var(--workspace-overlay-bg)",
          blockTargetInteraction: true,
          buttons: ["back", "close", "primary"],
          closeButtonAction: "skip",
          dismissKeyAction: false,
          overlayClickAction: false,
          overlayColor: "rgb(0 0 0 / 40%)",
          showProgress: true,
          skipBeacon: true,
          spotlightPadding: 6,
          targetWaitTimeout: targetWaitTimeoutMs,
          width: "min(344px, calc(100vw - 32px))",
          zIndex: 1200,
        }}
        run={phase === "running"}
        scrollToFirstStep
        stepIndex={stepIndex}
        steps={steps}
        tooltipComponent={Tooltip}
      />
    </>
  );
}

function TutorialTourTooltip({
  index,
  isLastStep,
  onBack,
  onNext,
  onSkip,
  size,
  step,
  tooltipProps,
}: TooltipRenderProps & {
  onBack: () => void;
  onNext: () => void;
  onSkip: () => void;
}) {
  return (
    <div
      className="tutorial-tour-tooltip"
      data-tour-step={step.id}
      {...tooltipProps}
      aria-describedby="tutorial-tour-tooltip-content"
      aria-labelledby="tutorial-tour-tooltip-title"
    >
      <button
        aria-label="End tour"
        className="tutorial-tour-close"
        type="button"
        onClick={onSkip}
      >
        <XIcon size={16} aria-hidden="true" />
      </button>
      <p className="tutorial-tour-tooltip__meta">
        {index + 1} of {size}
      </p>
      <h2 id="tutorial-tour-tooltip-title">{step.title}</h2>
      <div id="tutorial-tour-tooltip-content">{step.content}</div>
      <div className="flex items-center justify-between gap-[12px] mt-[20px]">
        <button
          className="tutorial-tour-button tutorial-tour-button--quiet"
          disabled={index === 0}
          type="button"
          onClick={onBack}
        >
          <ArrowLeft size={15} aria-hidden="true" />
          Back
        </button>
        <button
          className="tutorial-tour-button tutorial-tour-button--primary"
          type="button"
          onClick={onNext}
        >
          {isLastStep ? "Back to chat" : "Next"}
          <ArrowRight size={15} aria-hidden="true" />
        </button>
      </div>
    </div>
  );
}
