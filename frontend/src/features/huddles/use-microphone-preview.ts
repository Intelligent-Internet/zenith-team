import { useCallback, useEffect, useRef, useState } from "react";

export function useMicrophonePreview() {
  const [devices, setDevices] = useState<MediaDeviceInfo[]>([]);
  const [deviceId, setDeviceId] = useState("");
  const [testing, setTesting] = useState(false);
  const [level, setLevel] = useState(0);
  const [error, setError] = useState("");
  const streamRef = useRef<MediaStream | null>(null);
  const contextRef = useRef<AudioContext | null>(null);
  const frameRef = useRef<number | null>(null);
  const requestRef = useRef(0);

  const stop = useCallback(() => {
    requestRef.current += 1;
    if (frameRef.current !== null) cancelAnimationFrame(frameRef.current);
    frameRef.current = null;
    streamRef.current?.getTracks().forEach((track) => track.stop());
    streamRef.current = null;
    if (contextRef.current) void contextRef.current.close();
    contextRef.current = null;
    setTesting(false);
    setLevel(0);
  }, []);

  const refreshDevices = useCallback(async () => {
    if (!navigator.mediaDevices?.enumerateDevices) return;
    try {
      const found = (await navigator.mediaDevices.enumerateDevices()).filter(
        (device) => device.kind === "audioinput",
      );
      setDevices(found);
      setDeviceId((current) =>
        current && found.some((device) => device.deviceId === current)
          ? current
          : "",
      );
    } catch {
      // The system default remains available when enumeration is blocked.
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
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: deviceId ? { deviceId: { exact: deviceId } } : true,
      });
      if (request !== requestRef.current) {
        stream.getTracks().forEach((track) => track.stop());
        return;
      }
      streamRef.current = stream;
      await refreshDevices();
      if (request !== requestRef.current) return;
      const context = new AudioContext();
      contextRef.current = context;
      const source = context.createMediaStreamSource(stream);
      const analyser = context.createAnalyser();
      analyser.fftSize = 512;
      source.connect(analyser);
      const samples = new Uint8Array(analyser.fftSize);
      const measure = () => {
        analyser.getByteTimeDomainData(samples);
        const sum = samples.reduce(
          (total, sample) => total + (sample - 128) ** 2,
          0,
        );
        setLevel(
          Math.min(100, Math.round(Math.sqrt(sum / samples.length) * 3)),
        );
        frameRef.current = requestAnimationFrame(measure);
      };
      setTesting(true);
      measure();
    } catch (cause) {
      if (request !== requestRef.current) return;
      stop();
      setError(
        cause instanceof Error ? cause.message : "Microphone test failed.",
      );
    }
  }, [deviceId, refreshDevices, stop]);

  return { devices, deviceId, setDeviceId, testing, level, error, start, stop };
}
