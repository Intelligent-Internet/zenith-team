import { useCallback, useEffect, useRef, useState } from "react";

export function useCameraPreview(deviceId: string) {
  const [devices, setDevices] = useState<MediaDeviceInfo[]>([]);
  const [stream, setStream] = useState<MediaStream | null>(null);
  const [error, setError] = useState("");
  const streamRef = useRef<MediaStream | null>(null);
  const requestRef = useRef(0);

  const stop = useCallback(() => {
    requestRef.current += 1;
    streamRef.current?.getTracks().forEach((track) => track.stop());
    streamRef.current = null;
    setStream(null);
  }, []);

  const refreshDevices = useCallback(async () => {
    try {
      const found = await navigator.mediaDevices?.enumerateDevices();
      setDevices(found?.filter((device) => device.kind === "videoinput") ?? []);
    } catch {
      setDevices([]);
    }
  }, []);

  useEffect(() => {
    void refreshDevices();
    navigator.mediaDevices?.addEventListener?.("devicechange", refreshDevices);
    return () => {
      navigator.mediaDevices?.removeEventListener?.(
        "devicechange",
        refreshDevices,
      );
      stop();
    };
  }, [refreshDevices, stop]);

  const start = useCallback(async () => {
    stop();
    const request = requestRef.current;
    setError("");
    try {
      const next = await navigator.mediaDevices.getUserMedia({
        video: deviceId ? { deviceId: { exact: deviceId } } : true,
        audio: false,
      });
      if (request !== requestRef.current) {
        next.getTracks().forEach((track) => track.stop());
        return;
      }
      streamRef.current = next;
      setStream(next);
      // Labels become readable once permission is granted.
      await refreshDevices();
    } catch (cause) {
      if (request !== requestRef.current) return;
      stop();
      setError(
        cause instanceof Error ? cause.message : "Camera preview failed.",
      );
    }
  }, [deviceId, refreshDevices, stop]);

  return { devices, stream, previewing: stream !== null, error, start, stop };
}
