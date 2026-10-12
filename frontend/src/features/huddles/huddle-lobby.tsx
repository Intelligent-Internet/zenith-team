import {
  ChevronDown,
  Headphones,
  LoaderCircle,
  Mic,
  MicOff,
  Video,
  VideoOff,
} from "@/shared/ui/icons";
import { useEffect, useRef, type CSSProperties, type ReactNode } from "react";
import { huddleInitials } from "./huddle-tile";
import { useCameraPreview } from "./use-camera-preview";
import type { useMicrophonePreview } from "./use-microphone-preview";
import { Room } from "livekit-client";

const METER_SEGMENTS = 12;

function DeviceSelect({
  id,
  label,
  icon,
  value,
  fallbackName,
  devices,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  icon: ReactNode;
  value: string;
  fallbackName: string;
  devices: MediaDeviceInfo[];
  disabled?: boolean;
  onChange: (deviceId: string) => void;
}) {
  return (
    <div className="huddle-lobby-field">
      <label htmlFor={id}>{label}</label>
      <div className="huddle-lobby-select">
        <span className="huddle-lobby-select-icon" aria-hidden>
          {icon}
        </span>
        <select
          id={id}
          value={value}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
        >
          <option value="">System default</option>
          {devices
            .filter(
              (device) => device.deviceId && device.deviceId !== "default",
            )
            .map((device, index) => (
              <option key={device.deviceId} value={device.deviceId}>
                {device.label || `${fallbackName} ${index + 1}`}
              </option>
            ))}
        </select>
        <ChevronDown size={16} className="huddle-lobby-select-chevron" />
      </div>
    </div>
  );
}

export function HuddleLobby({
  displayName,
  microphone,
  cameraDeviceId,
  onCameraDeviceChange,
  requesting,
  errorMessage,
  canJoin,
  onJoin,
}: {
  displayName: string;
  microphone: ReturnType<typeof useMicrophonePreview>;
  cameraDeviceId: string;
  onCameraDeviceChange: (deviceId: string) => void;
  requesting: boolean;
  errorMessage?: string;
  canJoin: boolean;
  onJoin: (devices: {
    microphoneEnabled: boolean;
    cameraEnabled: boolean;
  }) => void;
}) {
  const camera = useCameraPreview(cameraDeviceId);
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    const url = import.meta.env.VITE_LIVEKIT_URL;
    if (!url) return;
    // Warm DNS/TLS only; admission and media access still require Join.
    const room = new Room();
    void room.prepareConnection(url).catch(() => {});
    return () => {
      void room.disconnect();
    };
  }, []);

  useEffect(() => {
    if (videoRef.current) videoRef.current.srcObject = camera.stream;
  }, [camera.stream]);

  // Devices must be free before the call claims them.
  const join = () => {
    const devices = {
      microphoneEnabled: microphone.testing,
      cameraEnabled: camera.previewing,
    };
    camera.stop();
    onJoin(devices);
  };

  const level = microphone.testing ? microphone.level / 100 : 0;
  const lit = Math.round(level * METER_SEGMENTS);
  const speaking = level > 0.08;

  return (
    <main className="huddle-lobby">
      <section className="huddle-green-room" aria-label="Join Huddle">
        <div
          className="huddle-preview"
          data-camera={camera.previewing}
          data-speaking={speaking}
          style={{ "--level": level } as CSSProperties}
        >
          <video
            ref={videoRef}
            autoPlay
            playsInline
            muted
            hidden={!camera.previewing}
            aria-label="Local camera preview"
          />
          {!camera.previewing && (
            <div className="huddle-preview-portrait" aria-hidden>
              <span className="huddle-tile-ring" />
              <span className="huddle-tile-ring" data-outer />
              <span className="huddle-preview-avatar">
                {huddleInitials(displayName)}
              </span>
              <p>Camera is off</p>
            </div>
          )}
          <span className="huddle-preview-name">{displayName}</span>
          {microphone.testing && (
            <span className="huddle-preview-check" role="status">
              <span className="huddle-eq" aria-hidden>
                <i />
                <i />
                <i />
              </span>
              {speaking ? "Sounds good" : "Say something…"}
            </span>
          )}
          <div className="huddle-preview-controls">
            <button
              type="button"
              className="huddle-control"
              data-active={microphone.testing}
              aria-pressed={microphone.testing}
              aria-label={
                microphone.testing
                  ? "Turn off microphone"
                  : "Turn on microphone"
              }
              data-tooltip={
                microphone.testing
                  ? "Turn off microphone"
                  : "Turn on microphone"
              }
              disabled={requesting}
              onClick={() =>
                void (microphone.testing
                  ? microphone.stop()
                  : microphone.start())
              }
            >
              {microphone.testing ? <Mic size={20} /> : <MicOff size={20} />}
            </button>
            <button
              type="button"
              className="huddle-control"
              data-active={camera.previewing}
              aria-pressed={camera.previewing}
              aria-label={
                camera.previewing ? "Stop camera preview" : "Preview camera"
              }
              data-tooltip={
                camera.previewing ? "Stop camera preview" : "Preview camera"
              }
              disabled={requesting}
              onClick={() =>
                void (camera.previewing ? camera.stop() : camera.start())
              }
            >
              {camera.previewing ? <Video size={20} /> : <VideoOff size={20} />}
            </button>
          </div>
        </div>

        <div className="huddle-join-panel">
          <h1>Ready to start?</h1>

          <DeviceSelect
            id="huddle-microphone"
            label="Microphone"
            icon={<Mic size={16} />}
            value={microphone.deviceId}
            fallbackName="Microphone"
            devices={microphone.devices}
            disabled={requesting}
            onChange={(next) => {
              microphone.stop();
              microphone.setDeviceId(next);
            }}
          />
          <div
            className="huddle-lobby-meter"
            role="meter"
            aria-label="Microphone level"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={microphone.testing ? microphone.level : 0}
            data-idle={!microphone.testing}
          >
            {Array.from({ length: METER_SEGMENTS }, (_, index) => (
              <i key={index} data-on={index < lit} />
            ))}
          </div>
          {microphone.error && (
            <p className="huddle-lobby-error" role="alert">
              {microphone.error}
            </p>
          )}

          <DeviceSelect
            id="huddle-camera"
            label="Camera"
            icon={<Video size={16} />}
            value={cameraDeviceId}
            fallbackName="Camera"
            devices={camera.devices}
            disabled={requesting}
            onChange={(next) => {
              camera.stop();
              onCameraDeviceChange(next);
            }}
          />
          {camera.error && (
            <p className="huddle-lobby-error" role="alert">
              {camera.error}
            </p>
          )}

          {errorMessage && (
            <p className="huddle-lobby-error" role="alert">
              {errorMessage}
            </p>
          )}
          <button
            type="button"
            onClick={join}
            disabled={requesting || !canJoin}
            className="huddle-join-button"
          >
            {requesting ? (
              <LoaderCircle size={18} className="huddle-spin" />
            ) : (
              <Headphones size={18} />
            )}
            {requesting ? "Connecting…" : "Start huddle"}
          </button>
        </div>
      </section>
    </main>
  );
}
