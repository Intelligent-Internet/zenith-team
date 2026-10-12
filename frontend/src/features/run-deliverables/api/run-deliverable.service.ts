import { apiClient } from "@/lib/api-client";
import { downloadDesktopArtifactFile } from "@/app/desktop/desktop-artifact-file";
import type {
  ArtifactAccess,
  ArtifactDetail,
  ArtifactPermission,
  ArtifactShareLink,
  UnownedArtifactPage,
  OutputPublication,
  PublicationCreateResult,
  PublicationSubdomainInput,
  RunResult,
} from "@/features/run-deliverables/model/run-deliverable";

function companyPath(companyId: string) {
  return `/companies/${encodeURIComponent(companyId)}`;
}

class RunDeliverableService {
  listUnownedArtifacts(companyId: string): Promise<UnownedArtifactPage> {
    return apiClient.get<UnownedArtifactPage>(
      `${companyPath(companyId)}/artifacts/unowned`,
    );
  }

  claimUnownedArtifact(
    companyId: string,
    artifactId: string,
    expectedRevision: number,
  ): Promise<ArtifactDetail> {
    return apiClient.post<ArtifactDetail>(
      `${companyPath(companyId)}/artifacts/${encodeURIComponent(artifactId)}/claim`,
      { expectedRevision },
    );
  }

  getArtifact(companyId: string, artifactId: string): Promise<ArtifactDetail> {
    return apiClient.get<ArtifactDetail>(
      `${companyPath(companyId)}/artifacts/${encodeURIComponent(artifactId)}`,
    );
  }

  putArtifactGrant(
    companyId: string,
    artifactId: string,
    userId: string,
    permission: ArtifactPermission,
    expectedRevision: number,
  ): Promise<ArtifactAccess> {
    return apiClient.put<ArtifactAccess>(
      `${companyPath(companyId)}/artifacts/${encodeURIComponent(artifactId)}/grants/${encodeURIComponent(userId)}`,
      { permission, expectedRevision },
    );
  }

  deleteArtifactGrant(
    companyId: string,
    artifactId: string,
    userId: string,
    expectedRevision: number,
  ): Promise<ArtifactAccess> {
    return apiClient.delete<ArtifactAccess>(
      `${companyPath(companyId)}/artifacts/${encodeURIComponent(artifactId)}/grants/${encodeURIComponent(userId)}?expectedRevision=${expectedRevision}`,
    );
  }

  async downloadArtifactFile(
    companyId: string,
    artifactId: string,
    ordinal: number,
    signal?: AbortSignal,
  ): Promise<Blob> {
    const cached = await downloadDesktopArtifactFile(
      { companyId, artifactId, ordinal },
      signal,
    );
    if (cached) return cached;
    return apiClient.get<Blob>(
      `${companyPath(companyId)}/artifacts/${encodeURIComponent(artifactId)}/items/${ordinal}/download`,
      { responseType: "blob", timeout: 0, signal },
    );
  }
  fetchRunResult(
    companyId: string,
    runId: string | null,
    artifactId?: string,
  ): Promise<RunResult> {
    if (!runId && artifactId)
      return this.getArtifact(companyId, artifactId).then(
        (detail) => detail.result,
      );
    if (!runId) return Promise.reject(new Error("A delivery is required."));
    const selection = artifactId
      ? `?artifactId=${encodeURIComponent(artifactId)}`
      : "";
    return apiClient.get<RunResult>(
      `${companyPath(companyId)}/runs/${encodeURIComponent(runId)}/result${selection}`,
    );
  }

  async downloadItem(
    companyId: string,
    runId: string | null,
    artifactId: string,
    ordinal: number,
    signal?: AbortSignal,
  ): Promise<Blob> {
    if (!runId)
      return this.downloadArtifactFile(companyId, artifactId, ordinal, signal);
    const cached = await downloadDesktopArtifactFile(
      { companyId, runId, artifactId, ordinal },
      signal,
    );
    if (cached) return cached;
    return apiClient.get<Blob>(
      `${companyPath(companyId)}/runs/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(artifactId)}/items/${ordinal}/download`,
      { responseType: "blob", timeout: 0, signal },
    );
  }

  shareItem(
    companyId: string,
    runId: string | null,
    artifactId: string,
    ordinal: number,
    signal?: AbortSignal,
  ): Promise<ArtifactShareLink> {
    if (!runId)
      return this.getArtifact(companyId, artifactId).then((detail) => {
        const url = new URL(detail.url);
        url.searchParams.set("item", String(ordinal));
        return { url: url.href };
      });
    return apiClient.post<ArtifactShareLink>(
      `${companyPath(companyId)}/runs/${encodeURIComponent(runId)}/artifacts/${encodeURIComponent(artifactId)}/items/${ordinal}/share`,
      undefined,
      { signal },
    );
  }

  publish(companyId: string, runId: string): Promise<PublicationCreateResult> {
    return apiClient.post<PublicationCreateResult>(
      `${companyPath(companyId)}/runs/${encodeURIComponent(runId)}/result/publication`,
      { confirmPublicExposure: true },
    );
  }

  setSubdomain(
    companyId: string,
    runId: string,
    input: PublicationSubdomainInput,
  ): Promise<OutputPublication> {
    return apiClient.put<OutputPublication>(
      `${companyPath(companyId)}/runs/${encodeURIComponent(runId)}/result/publication/subdomain`,
      input,
    );
  }

  unpublish(
    companyId: string,
    runId: string,
    expectedRevision: number,
  ): Promise<OutputPublication> {
    return apiClient.post<OutputPublication>(
      `${companyPath(companyId)}/runs/${encodeURIComponent(runId)}/result/publication/unpublish`,
      { expectedRevision },
    );
  }
}

export const runDeliverableService = new RunDeliverableService();
