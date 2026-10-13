import { getNextRoute } from "@/app/session/session-routing";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import {
  completeCompanyOnboarding,
  refreshSessionSnapshot,
  type SessionSnapshot,
} from "@/shared/session/session";
import {
  createCompanyOnboardingRequest,
  initialCompanyOnboardingDraft,
  type CompanyOnboardingDraft,
} from "@/features/onboarding/company/model/onboarding-request";
import { getOnboardingCompletionErrorMessage } from "@/features/onboarding/model/onboarding-error";
import { getCompanyOnboardingDestination } from "@/features/onboarding/model/onboarding-destination";
import {
  initialOnboardingStage,
  onboardingDraftKey,
  onboardingStageKey,
  resolveOnboardingScreen,
  type OnboardingStage,
} from "@/features/onboarding/model/onboarding-flow";
import { markTutorialTourEligible } from "@/features/tutorial-tour/model/tutorial-tour-storage";
import { companyService } from "@/shared/api/company.service";
import { attachmentService } from "@/shared/api/attachment.service";
import { boardlyApi } from "@/shared/api/boardly-api";
import { useAppDispatch } from "@/state";
import {
  readStorageItem,
  writeStorageItem,
  removeStorageItem,
} from "@/shared/lib/browser-storage";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";
import { useCompanyInitialization } from "@/features/onboarding/company/components/company-initialization-provider";

export const maxOnboardingReferenceFiles = 10;
export const maxOnboardingReferenceFileBytes = 25 * 1024 * 1024;

export type OnboardingReferenceFile = {
  id: string;
  file: File;
  progress: number;
  status: "pending" | "uploading" | "uploaded" | "error";
};

type UploadedReference = { id: string; filename: string };

function getSessionInitializationKey(session: SessionSnapshot) {
  return [
    session.email ?? "",
    session.nextStep,
    session.company?.workspaceName ?? "",
  ].join("|");
}

// Objects are merged onto their defaults so a stored draft written before a
// field existed still reads back complete.
function readStoredObject<T extends object>(key: string, fallback: T): T {
  const raw = readStorageItem("session", key);
  if (!raw) return fallback;
  try {
    return { ...fallback, ...(JSON.parse(raw) as Partial<T>) };
  } catch {
    removeStorageItem("session", key);
    return fallback;
  }
}

export function useOnboardingController(
  session: SessionSnapshot,
  { creatingCompany = false }: { creatingCompany?: boolean } = {},
) {
  const navigate = useNavigate();
  const dispatch = useAppDispatch();
  const initialization = useCompanyInitialization();
  const startWithInvitation = useLocation().state?.joinCompany === true;
  const initializedSessionKeyRef = useRef<string | null>(null);
  const [errorMessage, setErrorMessage] = useState("");
  const [noticeMessage, setNoticeMessage] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [draft, setDraft] = useState<CompanyOnboardingDraft>(
    initialCompanyOnboardingDraft,
  );
  const [stage, setStage] = useState<OnboardingStage>(initialOnboardingStage);
  const [referenceFiles, setReferenceFiles] = useState<
    OnboardingReferenceFile[]
  >([]);

  // A new Company draft must not reuse or overwrite the active Company's
  // unfinished setup, including its retry slug.
  const draftKey = `${onboardingDraftKey(session)}${creatingCompany ? ":new-company" : ""}`;
  const seedKey = `boardly:onboarding:seed:${session.email}${creatingCompany ? ":new-company" : ""}`;
  const sessionInitializationKey = `${getSessionInitializationKey(session)}|${creatingCompany}`;
  const screen = useMemo(
    () =>
      creatingCompany ? "company" : resolveOnboardingScreen(session, stage),
    [creatingCompany, session, stage],
  );

  useEffect(() => {
    if (initializedSessionKeyRef.current === sessionInitializationKey) {
      return;
    }
    initializedSessionKeyRef.current = sessionInitializationKey;
    setDraft(
      readStoredObject(draftKey, {
        ...initialCompanyOnboardingDraft,
      }),
    );
    const savedStage = creatingCompany
      ? initialOnboardingStage
      : readStoredObject(onboardingStageKey(session), initialOnboardingStage);
    setStage(
      startWithInvitation ? { ...savedStage, introSeen: false } : savedStage,
    );
  }, [
    creatingCompany,
    draftKey,
    session,
    sessionInitializationKey,
    startWithInvitation,
  ]);

  useEffect(() => {
    if (initializedSessionKeyRef.current !== sessionInitializationKey) return;
    writeStorageItem("session", draftKey, JSON.stringify(draft));
    if (creatingCompany) return;
    writeStorageItem(
      "session",
      onboardingStageKey(session),
      JSON.stringify(stage),
    );
  }, [
    creatingCompany,
    draft,
    draftKey,
    stage,
    session,
    sessionInitializationKey,
  ]);

  const addReferenceFiles = useCallback((files: File[]) => {
    setNoticeMessage("");
    setReferenceFiles((current) => {
      const room = maxOnboardingReferenceFiles - current.length;
      if (room <= 0) {
        setNoticeMessage(
          `You can attach up to ${maxOnboardingReferenceFiles} files.`,
        );
        return current;
      }
      const accepted: OnboardingReferenceFile[] = [];
      let rejectedForSize = 0;
      for (const file of files.slice(0, room)) {
        if (file.size > maxOnboardingReferenceFileBytes) {
          rejectedForSize += 1;
          continue;
        }
        accepted.push({
          id: generateUuidV7(),
          file,
          progress: 0,
          status: "pending",
        });
      }
      if (rejectedForSize > 0) {
        setNoticeMessage(
          `${rejectedForSize} file${rejectedForSize === 1 ? "" : "s"} skipped — each must be under 25 MB.`,
        );
      } else if (files.length > room) {
        setNoticeMessage(
          `Only the first ${room} file${room === 1 ? "" : "s"} were added.`,
        );
      }
      return [...current, ...accepted];
    });
  }, []);

  const removeReferenceFile = useCallback((id: string) => {
    setReferenceFiles((current) => current.filter((entry) => entry.id !== id));
  }, []);

  // Uploads run after the Company exists, because an Attachment is scoped to a
  // Company. A file that fails is reported and skipped rather than blocking
  // onboarding: the founder can always attach it again in the conversation.
  const uploadReferenceFiles = async (
    companyId: string,
  ): Promise<UploadedReference[]> => {
    if (referenceFiles.length === 0) return [];
    const uploaded: UploadedReference[] = [];
    let failures = 0;
    for (const entry of referenceFiles) {
      setReferenceFiles((current) =>
        current.map((item) =>
          item.id === entry.id
            ? { ...item, status: "uploading", progress: 0 }
            : item,
        ),
      );
      try {
        const attachment = await attachmentService.uploadFile(
          companyId,
          entry.file,
          (percent) =>
            setReferenceFiles((current) =>
              current.map((item) =>
                item.id === entry.id
                  ? { ...item, progress: percent ?? item.progress }
                  : item,
              ),
            ),
        );
        uploaded.push({ id: attachment.id, filename: entry.file.name });
        setReferenceFiles((current) =>
          current.map((item) =>
            item.id === entry.id
              ? { ...item, status: "uploaded", progress: 100 }
              : item,
          ),
        );
      } catch {
        failures += 1;
        setReferenceFiles((current) =>
          current.map((item) =>
            item.id === entry.id ? { ...item, status: "error" } : item,
          ),
        );
      }
    }
    if (failures > 0) {
      setNoticeMessage(
        `${failures} file${failures === 1 ? "" : "s"} didn't upload — you can attach ${failures === 1 ? "it" : "them"} in the chat later.`,
      );
    }
    if (uploaded.length > 0) {
      // Registering is what makes these the Company's own documents: an
      // unregistered upload is an orphan the server sweeps away, and no Agent
      // would ever see it. It is therefore worth reporting when it fails, even
      // though the files themselves uploaded fine.
      try {
        await companyService.addReferenceDocuments(
          companyId,
          uploaded.map((reference) => reference.id),
        );
      } catch (error) {
        console.error(
          "Registering onboarding reference documents failed",
          error,
        );
        setNoticeMessage(
          "Your files uploaded but could not be saved to the company — Andrew will not see them.",
        );
        return [];
      }
    }
    return uploaded;
  };

  const submitCompany = async () => {
    if (
      !draft.companyName.trim() ||
      !draft.companyDescription.trim() ||
      isSubmitting
    ) {
      return;
    }
    const seed = readStorageItem("session", seedKey) || generateUuidV7();
    writeStorageItem("session", seedKey, seed);
    const request = createCompanyOnboardingRequest({ draft }, () => seed);
    setIsSubmitting(true);
    setErrorMessage("");

    try {
      const result = await companyService.onboardCompany(request);
      removeStorageItem("session", seedKey);
      // Uploads finish before the session advances: advancing swaps this form
      // for the agent screen, and a founder should not watch their files
      // disappear mid-transfer.
      await uploadReferenceFiles(result.company.id);
      dispatch(boardlyApi.util.invalidateTags(["Company"]));
      if (creatingCompany) removeStorageItem("session", draftKey);
      const destination = getCompanyOnboardingDestination(result);
      if (destination === "agent_setup") {
        const nextStage = {
          ...initialOnboardingStage,
          introSeen: true,
          companyId: result.company.id,
        };
        writeStorageItem(
          "session",
          onboardingStageKey(session),
          JSON.stringify(nextStage),
        );
        setStage(nextStage);
        completeCompanyOnboarding(result);
        if (creatingCompany) navigate("/onboarding", { replace: true });
        return;
      }
      const nextSession = completeCompanyOnboarding(result);
      markTutorialTourEligible(nextSession.userId, nextSession.company?.id);
      navigate("/app", { replace: true });
    } catch (error) {
      setErrorMessage(getOnboardingCompletionErrorMessage(error));
    } finally {
      setIsSubmitting(false);
    }
  };

  const startInitializing = () => {
    setErrorMessage("");
    setStage((current) => ({
      ...current,
      initializing: true,
      companyId: session.company?.id,
    }));
    void initialization?.continuePreparation();
  };

  const openWorkspace = async () => {
    if (!initialization?.hasReceipt || isSubmitting) return;
    setIsSubmitting(true);
    setErrorMessage("");
    try {
      const nextSession = await refreshSessionSnapshot();
      if (
        nextSession.company?.id !== session.company?.id ||
        !nextSession.company?.agentSetupCompleted
      ) {
        throw new Error("Finish your CEO setup before opening the workspace.");
      }
      markTutorialTourEligible(nextSession.userId, nextSession.company.id);
      removeStorageItem("session", onboardingDraftKey(session));
      removeStorageItem("session", onboardingStageKey(session));
      navigate("/app", { replace: true });
    } catch (cause) {
      setErrorMessage(getOnboardingCompletionErrorMessage(cause));
    } finally {
      setIsSubmitting(false);
    }
  };

  return {
    addReferenceFiles,
    cancelCompanyCreation: () =>
      navigate(getNextRoute(session), { replace: true }),
    continueFromIntroduction: () =>
      setStage((current) => ({ ...current, introSeen: true })),
    returnToIntroduction: () =>
      setStage((current) => ({ ...current, introSeen: false })),
    draft,
    errorMessage,
    openWorkspace,
    reviewConnection: () =>
      setStage((current) => ({
        ...current,
        companyId: session.company?.id,
        reviewingSetup: true,
      })),
    goToConnections: () =>
      setStage((current) =>
        current.reviewingSetup
          ? { ...current, reviewingSetup: false, initializing: true }
          : {
              ...current,
              companyId: session.company?.id,
              connectionsReached: true,
              teamReached: false,
            },
      ),
    goToTeam: () => {
      setErrorMessage("");
      setStage((current) => ({ ...current, teamReached: true }));
    },
    isSubmitting,
    noticeMessage,
    referenceFiles,
    removeReferenceFile,
    screen,
    startInitializing,
    submitCompany,
    updateDraft: (patch: Partial<CompanyOnboardingDraft>) =>
      setDraft((current) => ({ ...current, ...patch })),
  };
}
