import type { Step } from "react-joyride";

export type TutorialTourStep = Step & {
  data: { route: string };
  target: string;
};

export function createTutorialTourSteps(isMobile: boolean): TutorialTourStep[] {
  const navigation = `[data-tour-id="workspace-navigation-${isMobile ? "mobile" : "desktop"}"]`;

  return [
    {
      id: "chat",
      title: "Start with a conversation",
      content:
        "Talk with Agents and teammates, or open a task chat to keep decisions with the work. Your CEO Agent helps turn ideas into a plan.",
      data: { route: "/app" },
      target: `${navigation} [data-tour-id="workspace-navigation-chat"]`,
      placement: isMobile ? "top-start" : "right-start",
    },
    {
      id: "projects",
      title: "Keep work together",
      content:
        "Projects bring tasks, dependencies, files, and results together. Open a project to run work, answer questions, and review results.",
      data: { route: "/projects" },
      target: '[data-tour-id="projects-overview-heading"]',
      spotlightTarget: '[data-tour-id="projects-overview"]',
      spotlightPadding: 0,
      spotlightRadius: 12,
      placement: "bottom-start",
    },
    {
      id: "agents",
      title: "Meet your Agents",
      content:
        "See each Agent’s role, current task, and availability. Set up an Agent on your computer, or open their profile to adjust how they work.",
      data: { route: "/agents" },
      target: '[data-tour-id="agents-overview-heading"]',
      spotlightTarget: '[data-tour-id="agents-overview"]',
      spotlightPadding: 0,
      spotlightRadius: 12,
      placement: "bottom-start",
    },
    {
      id: "wiki",
      title: "Give your team shared context",
      content:
        "Keep company knowledge in pages and collections. Use Library to browse, or Graph to see how pages connect.",
      data: { route: "/wiki" },
      target: '[data-tour-id="wiki-overview-heading"]',
      spotlightTarget: '[data-tour-id="wiki-overview"]',
      spotlightPadding: 0,
      spotlightRadius: 12,
      placement: "bottom-start",
    },
    {
      id: "team",
      title: "Work with your teammates",
      content:
        "Find the people in your company and see their roles. Invite teammates or manage membership when you have access.",
      data: { route: "/team" },
      target: `${navigation} [data-tour-id="workspace-navigation-team"]`,
      placement: isMobile ? "top-end" : "right-start",
    },
    {
      id: "explore",
      title: "Find more in Explore",
      content:
        "Open Calendar, Mailbox, Workflows, Social accounts, and Assets, plus support and any billing or usage tools you have access to.",
      data: { route: "/wiki" },
      target: `${navigation} [data-tour-id="workspace-navigation-explore"]`,
      placement: isMobile ? "top-end" : "right-start",
    },
    {
      id: "workflows",
      title: "Start from a Workflow",
      content:
        "Browse ready-to-run templates, pin your favorites, and fill in a short form to create a task for an Agent.",
      data: { route: "/workflows" },
      target: '[data-tour-id="workflows-overview-heading"]',
      spotlightTarget: '[data-tour-id="workflows-overview"]',
      spotlightPadding: 0,
      spotlightRadius: 12,
      placement: "bottom-start",
    },
    {
      id: "workspace-tools",
      title: "Your everyday shortcuts",
      content:
        "Search across your workspace, check notifications, or use Ask CEO to start a new conversation with your CEO Agent.",
      data: { route: "/app" },
      target: '[data-tour-id="workspace-utility-actions"]',
      placement: "bottom-end",
    },
    {
      id: "account",
      title: "Make the workspace yours",
      content:
        "Manage your account and appearance, and find this tour again under Help. Company settings are available when you have access.",
      data: { route: "/app" },
      target: '[data-tour-id="workspace-account-menu"]',
      placement: isMobile ? "bottom-end" : "right-end",
    },
  ];
}

export function isTutorialTourTargetVisible(selector: string) {
  const element = document.querySelector<HTMLElement>(selector);
  if (!element?.isConnected || !element.getClientRects().length) return false;

  const { width, height } = element.getBoundingClientRect();
  const { visibility } = window.getComputedStyle(element);
  return width > 0 && height > 0 && visibility === "visible";
}

export function waitForTutorialTourTarget(
  selector: string,
  timeoutMs: number,
  signal: AbortSignal,
): Promise<boolean> {
  if (signal.aborted) return Promise.resolve(false);
  if (isTutorialTourTargetVisible(selector)) return Promise.resolve(true);

  return new Promise((resolve) => {
    const finish = (found: boolean) => {
      observer.disconnect();
      window.clearTimeout(timeout);
      window.cancelAnimationFrame(frame);
      window.removeEventListener("resize", checkTarget);
      signal.removeEventListener("abort", abort);
      resolve(found);
    };
    const checkTarget = () => {
      if (isTutorialTourTargetVisible(selector)) finish(true);
    };
    const abort = () => finish(false);
    const observer = new MutationObserver(checkTarget);
    const timeout = window.setTimeout(() => finish(false), timeoutMs);
    const frame = window.requestAnimationFrame(checkTarget);
    observer.observe(document.body, {
      attributes: true,
      childList: true,
      subtree: true,
    });
    window.addEventListener("resize", checkTarget);
    signal.addEventListener("abort", abort, { once: true });
  });
}
