import { Component, type ReactNode } from "react";
import { failureMessage, type InlineDiagnostic } from "./recovery";

export function InlineStage({
  name = "component",
  failed = false,
  streaming = false,
}: {
  name?: string;
  failed?: boolean;
  streaming?: boolean;
}) {
  const waiting = streaming && !failed;
  const label =
    (
      {
        body: "interactive content",
        module: "interactive content",
        expression: "content",
        AppBlock: "application",
        Chart: "chart",
      } as Record<string, string>
    )[name] ??
    (name || "component");
  const large = [
    "Chart",
    "AppBlock",
    "AsyncImage",
    "AsyncImageGroup",
    "table",
    "grid",
  ].includes(name);
  return (
    <span
      role="status"
      data-inline-stage={waiting ? "loading" : "unavailable"}
      className={`inline-flex max-w-full items-center gap-2 rounded-workspace-control border border-workspace-divider px-3 py-2 text-xs text-workspace-muted ${large && waiting ? "min-h-32 w-full justify-center bg-workspace-control" : "min-h-8"}`}
    >
      {waiting && (
        <span
          aria-hidden="true"
          className="h-2 w-2 rounded-full bg-workspace-accent motion-safe:animate-pulse"
        />
      )}
      {waiting ? `Preparing ${label}…` : `This ${label} couldn’t be displayed.`}
    </span>
  );
}

// React exceptions happen after expression evaluation. Keep them local too,
// retaining a committed version when possible, with a second boundary in case
// a dependency (such as a provider) makes that previous version fail as well.
export class InlineBoundary extends Component<
  {
    children: ReactNode;
    resetKey: unknown;
    name: string;
    start: number;
    fallback?: ReactNode;
    retain?: boolean;
    onDiagnostic?: (diagnostic: InlineDiagnostic) => void;
  },
  { error: string; resetKey: unknown }
> {
  state = { error: "", resetKey: this.props.resetKey };
  committed: ReactNode;
  static getDerivedStateFromError(error: unknown) {
    return { error: failureMessage(error) };
  }
  static getDerivedStateFromProps(
    props: { resetKey: unknown },
    state: { resetKey: unknown },
  ) {
    return props.resetKey !== state.resetKey
      ? { error: "", resetKey: props.resetKey }
      : null;
  }
  componentDidMount() {
    if (!this.state.error) this.committed = this.props.children;
  }
  componentDidUpdate() {
    if (!this.state.error) this.committed = this.props.children;
  }
  componentDidCatch() {
    this.props.onDiagnostic?.({
      code: "component_error",
      phase: "render",
      start: this.props.start,
      component: this.props.name,
      message: this.state.error,
      repair:
        this.committed && this.props.retain !== false ? "retained" : "omitted",
    });
  }
  render(): ReactNode {
    if (!this.state.error) return this.props.children;
    const fallback = this.props.fallback ?? (
      <InlineStage name={this.props.name} failed />
    );
    return (
      <>
        {this.committed && this.props.retain !== false ? (
          <InlineBoundary
            resetKey={this.props.resetKey}
            name={this.props.name}
            start={this.props.start}
            retain={false}
            fallback={fallback}
          >
            {this.committed}
          </InlineBoundary>
        ) : (
          fallback
        )}
        {!this.props.onDiagnostic && this.props.retain !== false && (
          <details className="text-xs text-workspace-muted">
            <summary className="cursor-pointer">Display issue</summary>
            <p>{this.state.error}</p>
          </details>
        )}
      </>
    );
  }
}
