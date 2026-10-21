import { runService } from "@/shared/api/run.service";
import type { RunDto } from "@/shared/api/types";
import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import type {
  ArtifactAccess,
  ArtifactDetail,
  ArtifactPermission,
  UnownedArtifactPage,
  OutputPublication,
  PublicationCreateResult,
  PublicationSubdomainInput,
  RunResult,
} from "@/features/run-deliverables/model/run-deliverable";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";

type RunResultArgument = {
  artifactId?: string;
  companyId: string;
  runId: string | null;
};

type PublicationArgument = { companyId: string; runId: string };
type UnpublishArgument = PublicationArgument & { expectedRevision: number };
type ArtifactArgument = { companyId: string; artifactId: string };
type GrantArgument = ArtifactArgument & {
  userId: string;
  expectedRevision: number;
  permission?: ArtifactPermission;
};

export const runDeliverablesApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    getRunDetails: builder.query<RunDto, { companyId: string; runId: string }>({
      providesTags: (_result, _error, { runId }) => [
        { type: "Run", id: runId },
      ],
      queryFn: ({ companyId, runId }) =>
        executeZenithTeamQuery(
          () => runService.getRun(companyId, runId),
          "Run details could not be loaded.",
        ),
    }),
    listUnownedArtifacts: builder.query<UnownedArtifactPage, string>({
      providesTags: [{ type: "Deliverable" }],
      queryFn: (companyId) =>
        executeZenithTeamQuery(
          () => runDeliverableService.listUnownedArtifacts(companyId),
          "Artifacts needing recovery could not be loaded.",
        ),
    }),
    claimUnownedArtifact: builder.mutation<
      ArtifactDetail,
      ArtifactArgument & { expectedRevision: number }
    >({
      invalidatesTags: [{ type: "Deliverable" }],
      queryFn: ({ companyId, artifactId, expectedRevision }) =>
        executeZenithTeamQuery(
          () =>
            runDeliverableService.claimUnownedArtifact(
              companyId,
              artifactId,
              expectedRevision,
            ),
          "This artifact could not be claimed.",
        ),
    }),
    getArtifact: builder.query<ArtifactDetail, ArtifactArgument>({
      providesTags: (_result, _error, argument) => [
        { id: argument.artifactId, type: "Deliverable" },
      ],
      queryFn: ({ companyId, artifactId }) =>
        executeZenithTeamQuery(
          () => runDeliverableService.getArtifact(companyId, artifactId),
          "This artifact is unavailable or you do not have access.",
          { retry: true },
        ),
    }),
    putArtifactGrant: builder.mutation<ArtifactAccess, GrantArgument>({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.artifactId, type: "Deliverable" },
        { id: `${argument.companyId}:ready`, type: "Deliverable" },
      ],
      queryFn: ({
        companyId,
        artifactId,
        userId,
        permission,
        expectedRevision,
      }) =>
        executeZenithTeamQuery(
          () =>
            runDeliverableService.putArtifactGrant(
              companyId,
              artifactId,
              userId,
              permission!,
              expectedRevision,
            ),
          "Access could not be updated.",
        ),
    }),
    deleteArtifactGrant: builder.mutation<ArtifactAccess, GrantArgument>({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.artifactId, type: "Deliverable" },
        { id: `${argument.companyId}:ready`, type: "Deliverable" },
      ],
      queryFn: ({ companyId, artifactId, userId, expectedRevision }) =>
        executeZenithTeamQuery(
          () =>
            runDeliverableService.deleteArtifactGrant(
              companyId,
              artifactId,
              userId,
              expectedRevision,
            ),
          "Access could not be revoked.",
        ),
    }),
    getRunResult: builder.query<RunResult, RunResultArgument>({
      providesTags: (_result, _error, argument) => [
        { id: argument.runId ?? undefined, type: "Deliverable" },
        { id: argument.runId ?? undefined, type: "Run" },
        ...(argument.artifactId
          ? [{ id: argument.artifactId, type: "Artifact" as const }]
          : []),
      ],
      queryFn: ({ artifactId, companyId, runId }) =>
        executeZenithTeamQuery(
          () =>
            runDeliverableService.fetchRunResult(companyId, runId, artifactId),
          "This run result could not be loaded.",
          { retry: true },
        ),
    }),
    publishRunOutput: builder.mutation<
      PublicationCreateResult,
      PublicationArgument
    >({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.runId ?? undefined, type: "Deliverable" },
      ],
      queryFn: ({ companyId, runId }) =>
        executeZenithTeamQuery(
          () => runDeliverableService.publish(companyId, runId),
          "This output could not be published.",
        ),
    }),
    unpublishRunOutput: builder.mutation<OutputPublication, UnpublishArgument>({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.runId ?? undefined, type: "Deliverable" },
      ],
      queryFn: ({ companyId, expectedRevision, runId }) =>
        executeZenithTeamQuery(
          () =>
            runDeliverableService.unpublish(companyId, runId, expectedRevision),
          "This publication could not be removed.",
        ),
    }),
    setPublicationSubdomain: builder.mutation<
      OutputPublication,
      PublicationArgument & PublicationSubdomainInput
    >({
      invalidatesTags: (_result, _error, argument) => [
        { id: argument.runId ?? undefined, type: "Deliverable" },
      ],
      queryFn: ({ companyId, runId, subdomain, expectedRevision }) =>
        executeZenithTeamQuery(
          () =>
            runDeliverableService.setSubdomain(companyId, runId, {
              subdomain,
              expectedRevision,
            }),
          "Your custom URL could not be saved.",
        ),
    }),
  }),
});

export const {
  useGetRunDetailsQuery,
  useListUnownedArtifactsQuery,
  useClaimUnownedArtifactMutation,
  useGetArtifactQuery,
  useLazyGetArtifactQuery,
  usePutArtifactGrantMutation,
  useDeleteArtifactGrantMutation,
  useGetRunResultQuery,
  usePublishRunOutputMutation,
  useUnpublishRunOutputMutation,
  useSetPublicationSubdomainMutation,
} = runDeliverablesApi;
