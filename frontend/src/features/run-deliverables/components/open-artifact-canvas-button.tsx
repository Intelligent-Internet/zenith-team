import { useLocation, useNavigate } from "react-router";
import {
  readSessionSnapshot,
  writeSessionSnapshot,
} from "@/app/session/session";
import type { RunOutput } from "@/features/run-deliverables/model/run-deliverable";
import { useLazyGetWorkspaceCompanyQuery } from "@/shared/api/workspace-api";
import { DropdownMenuItem } from "@/shared/ui/dropdown-menu";
import { Button } from "@/shared/ui/button";
import { Layers } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";

export function OpenArtifactCanvasButton({
  companyId,
  output,
  onOpen,
  menuItem = false,
  outputItemId,
}: {
  companyId: string;
  output: RunOutput;
  onOpen?: () => void;
  menuItem?: boolean;
  outputItemId?: string;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const [getCompany, { isFetching }] = useLazyGetWorkspaceCompanyQuery();
  const destination = menuItem
    ? output.canvasUrl
    : output.presentationUrl || output.canvasUrl;

  async function open() {
    if (isFetching) return;
    const original = readSessionSnapshot();
    try {
      // Shared artifact links can belong to a different Company than the
      // selected workspace. Resolve that Company before opening its Project.
      if (original.company?.id !== companyId) {
        const company = await getCompany(companyId).unwrap();
        if (!company) throw new Error("Company unavailable");
        const current = readSessionSnapshot();
        if (
          current.status !== "authenticated" ||
          current.userId !== original.userId ||
          current.company?.id !== original.company?.id
        )
          return;
        const agentSetupCompleted =
          company.role !== "owner" || Boolean(company.agentSetupCompletedAt);
        writeSessionSnapshot({
          ...current,
          company: {
            id: company.id,
            role: company.role,
            permissions: company.permissions,
            workspaceName: company.name,
            agentSetupCompleted,
          },
          nextStep: agentSetupCompleted ? "complete" : "agent_setup",
        });
      }
      onOpen?.();
      const target = new URL(
        destination ||
          `/companies/${companyId}/artifacts/${output.id}?view=canvas`,
        window.location.origin,
      );
      if (menuItem) {
        target.searchParams.set("mode", "review");
        if (outputItemId) target.searchParams.set("outputItemId", outputItemId);
      }
      navigate(`${target.pathname}${target.search}`, {
        state: {
          artifactInBrowser: true,
          ...(!destination
            ? { canvasReturnTo: `${location.pathname}${location.search}` }
            : {}),
        },
      });
    } catch {
      toast.error("The result could not be opened. Please try again.");
    }
  }

  if (menuItem)
    return (
      <DropdownMenuItem disabled={isFetching} onSelect={() => void open()}>
        <Layers aria-hidden size={16} />
        {isFetching ? "Opening…" : "Open in canvas"}
      </DropdownMenuItem>
    );

  return (
    <Button
      variant="ghost"
      size="sm"
      disabled={isFetching}
      onClick={() => void open()}
    >
      <Layers aria-hidden size={16} />
      {isFetching
        ? "Opening…"
        : output.presentationUrl
          ? "Open presentation"
          : "Open in canvas"}
    </Button>
  );
}
