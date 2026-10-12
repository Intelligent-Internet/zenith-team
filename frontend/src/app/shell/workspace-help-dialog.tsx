import {
  ArrowUpRight,
  Bot,
  CircleHelp,
  Inbox,
  Keyboard,
  ListTodo,
  Sparkles,
} from "@/shared/ui/icons";
import { Link, useNavigate } from "react-router";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { DisclosureSummary } from "@/shared/ui/disclosure-summary";
import { useTutorialTour } from "@/features/tutorial-tour/tutorial-tour-context";

export function WorkspaceHelpDialog({
  open,
  onOpenChange,
  ceoHref,
  tourHref,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  ceoHref?: string;
  tourHref?: string;
}) {
  const navigate = useNavigate();
  const { requestReplay } = useTutorialTour();
  const destinations = [
    ...(ceoHref
      ? [
          {
            icon: Bot,
            title: "Talk to your CEO Assistant",
            description:
              "Your default Agent from company onboarding. Ask about goals, priorities, or the next step.",
            to: ceoHref,
          },
        ]
      : []),
    {
      icon: ListTodo,
      title: "Explore your work",
      description: "Switch between Board, Table, Focus, and Dependencies.",
      to: "/tasks",
    },
    {
      icon: Inbox,
      title: "Review your Inbox",
      description:
        "Questions, access approvals, and saved results waiting for your input.",
      to: "/settings/notifications",
    },
  ];
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="workspace-help-dialog">
        <header>
          <DialogTitle>Help</DialogTitle>
        </header>
        <div className="workspace-help-body">
          <DialogDescription>
            Find your way around your company.
          </DialogDescription>
          <nav>
            <button
              type="button"
              onClick={() => {
                requestReplay();
                onOpenChange(false);
                navigate(tourHref ?? "/app");
              }}
            >
              <Sparkles size={17} />
              <span>
                <strong>Replay product tour</strong>
                <small>
                  Find your way around Chat, Projects, Agents, Wiki, Team, and
                  Workflows, plus your workspace tools and settings.
                </small>
              </span>
              <ArrowUpRight size={13} />
            </button>
            {destinations.map((item) => (
              <DialogClose asChild key={item.to}>
                <Link to={item.to}>
                  <item.icon size={17} />
                  <span>
                    <strong>{item.title}</strong>
                    <small>{item.description}</small>
                  </span>
                  <ArrowUpRight size={13} />
                </Link>
              </DialogClose>
            ))}
          </nav>
          <details open>
            <DisclosureSummary>
              <Keyboard size={15} />
              Keyboard shortcuts
            </DisclosureSummary>
            <dl>
              <dt>Search your company</dt>
              <dd>
                <kbd>⌘ K / Ctrl K</kbd>
              </dd>
              <dt>Close a menu or dialog</dt>
              <dd>
                <kbd>Esc</kbd>
              </dd>
            </dl>
          </details>
          <footer>
            <DialogClose asChild>
              <Link to="/settings/support">
                <CircleHelp size={13} />
                Updates & support
              </Link>
            </DialogClose>
            <DialogClose asChild>
              <button className="dashboard-button dashboard-button-primary">
                Done
              </button>
            </DialogClose>
          </footer>
        </div>
      </DialogContent>
    </Dialog>
  );
}
