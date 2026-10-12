import { Users } from "@/shared/ui/icons";
import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { WorkspaceAppearanceProvider } from "@/app/appearance/workspace-appearance-provider";
import {
  joinHuddle,
  leaveHuddle,
  startHuddle,
} from "../../discussions/api/huddle-api";
import { HuddleCallPanel } from "../huddle-call-panel";
import { HuddleLobby } from "../huddle-lobby";
import { useMicrophonePreview } from "../use-microphone-preview";
import { useGetHuddleAIQuery } from "../huddle-ai-api";
import { HUDDLE_STATUS_FALLBACK_POLL_MS } from "../huddle-ai-status";

// The call window opens outside the workspace shell, so it applies the same
// per-user, per-Company theme itself.
export function HuddleWindowPage() {
  const session = useSessionSnapshot();
  return (
    <WorkspaceAppearanceProvider
      userId={session.userId ?? undefined}
      companyId={session.company?.id}
    >
      <HuddleWindowContent />
    </WorkspaceAppearanceProvider>
  );
}

function HuddleWindowContent() {
  const params = useParams<{ huddleId: string; conversationId: string }>();
  // A lobby opened to start a call has only its conversation: the Huddle is
  // created when Start is pressed, so nobody rings before the caller joins.
  const [startedHuddleId, setStartedHuddleId] = useState("");
  const huddleId = params.huddleId ?? startedHuddleId;
  const sourceConversationId = params.conversationId;
  const session = useSessionSnapshot();
  const companyId = session.company?.id;
  const leavingRef = useRef(false);
  const microphone = useMicrophonePreview();
  const [cameraDeviceId, setCameraDeviceId] = useState("");
  const [state, setState] = useState<
    | {
        status: "idle" | "requesting" | "error" | "left" | "ended";
        message?: string;
      }
    | {
        status: "joined";
        huddleId: string;
        connectionId: string;
        chatConversationId: string;
        deadlineAt: string;
        media: { url: string; token: string };
        microphoneEnabled: boolean;
        cameraEnabled: boolean;
      }
  >({ status: "idle" });
  const callStatus = useGetHuddleAIQuery(
    { companyId: companyId ?? "", huddleId: huddleId ?? "" },
    {
      skip: !companyId || !huddleId || session.status !== "authenticated",
      pollingInterval:
        state.status === "ended" || state.status === "left"
          ? 0
          : HUDDLE_STATUS_FALLBACK_POLL_MS,
    },
  );

  useEffect(() => {
    if (callStatus.currentData?.state !== "ended") return;
    leavingRef.current = true;
    microphone.stop();
    setState((current) =>
      current.status === "ended" ? current : { status: "ended" },
    );
  }, [callStatus.currentData?.state, microphone.stop]);

  useEffect(() => {
    if (state.status !== "joined" || !companyId) return undefined;
    const connection = state;
    const leaveOnClose = () => {
      if (leavingRef.current) return;
      leavingRef.current = true;
      void leaveHuddle(companyId, connection.huddleId, connection.connectionId);
    };
    window.addEventListener("pagehide", leaveOnClose);
    return () => {
      window.removeEventListener("pagehide", leaveOnClose);
      leaveOnClose();
    };
  }, [companyId, state]);

  const join = async (devices: {
    microphoneEnabled: boolean;
    cameraEnabled: boolean;
  }) => {
    if ((!huddleId && !sourceConversationId) || !companyId) return;
    setState({ status: "requesting" });
    try {
      microphone.stop();
      let targetHuddleId = huddleId;
      if (!targetHuddleId && sourceConversationId) {
        // Start returns the conversation's active Huddle when there is one.
        targetHuddleId = (await startHuddle(companyId, sourceConversationId))
          .id;
        setStartedHuddleId(targetHuddleId);
      }
      const result = await joinHuddle(companyId, targetHuddleId);
      if (!result.media) {
        throw new Error(
          "Audio is unavailable right now. Please try joining again.",
        );
      }
      leavingRef.current = false;
      setState({
        status: "joined",
        huddleId: result.huddle.id,
        connectionId: result.connection.id,
        chatConversationId: result.huddle.chatConversationId,
        deadlineAt: result.huddle.deadlineAt,
        media: result.media,
        ...devices,
      });
    } catch (cause) {
      setState({
        status: "error",
        message:
          cause instanceof Error ? cause.message : "Could not join Huddle.",
      });
    }
  };

  if (session.status !== "authenticated" || !companyId) {
    return (
      <main className="grid min-h-dvh place-items-center bg-background text-foreground">
        Sign in to join this Huddle.
      </main>
    );
  }

  if (state.status === "left" || state.status === "ended") {
    return (
      <main className="huddle-lobby">
        <section className="huddle-join-card">
          <div className="huddle-join-symbol">
            <Users size={32} />
          </div>
          <h1>
            {state.status === "ended"
              ? "This call has ended"
              : "You left the Huddle"}
          </h1>
          <p className="huddle-join-copy">
            Your microphone, camera and screen sharing are off. You can close
            this window.
          </p>
          <button
            type="button"
            className="huddle-join-button"
            onClick={() => window.close()}
          >
            Close window
          </button>
          <Link to="/" className="huddle-join-link">
            Back to Zenith Team
          </Link>
        </section>
      </main>
    );
  }

  if (state.status === "joined") {
    return (
      <main className="huddle-window">
        <HuddleCallPanel
          media={state.media}
          microphoneDeviceId={microphone.deviceId}
          cameraDeviceId={cameraDeviceId}
          microphoneEnabled={state.microphoneEnabled}
          cameraEnabled={state.cameraEnabled}
          companyId={companyId}
          huddleId={state.huddleId}
          chatConversationId={state.chatConversationId}
          deadlineAt={state.deadlineAt}
          onRejoin={() => setState({ status: "idle" })}
          onLeave={async () => {
            if (leavingRef.current) return;
            leavingRef.current = true;
            setState({ status: "left" });
            await Promise.race([
              leaveHuddle(companyId, state.huddleId, state.connectionId).catch(
                () => {},
              ),
              new Promise<void>((resolve) => window.setTimeout(resolve, 1200)),
            ]);
            window.close();
          }}
        />
      </main>
    );
  }

  return (
    <HuddleLobby
      displayName={session.displayName ?? "You"}
      microphone={microphone}
      cameraDeviceId={cameraDeviceId}
      onCameraDeviceChange={setCameraDeviceId}
      requesting={state.status === "requesting"}
      errorMessage={state.status === "error" ? state.message : undefined}
      canJoin={Boolean(huddleId || sourceConversationId)}
      onJoin={(devices) => void join(devices)}
    />
  );
}
