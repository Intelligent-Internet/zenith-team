import { useEffect, useRef, useState } from "react";
import searching from "../../assets/research-motion/searching.webm?url";
import searchingApple from "../../assets/research-motion/searching.mov?url";
import searchingPoster from "../../assets/research-motion/searching-poster.webp";
import building from "../../assets/research-motion/building.webm?url";
import buildingApple from "../../assets/research-motion/building.mov?url";
import buildingPoster from "../../assets/research-motion/building-poster.webp";

const clips = {
  searching: { sources: [searching, searchingApple], poster: searchingPoster },
  building: { sources: [building, buildingApple], poster: buildingPoster },
};

/** Native video playback at the source's 24 fps; no JavaScript frame loop. */
export function OnboardingResearchAction({
  action,
  current,
  paused,
}: {
  action: keyof typeof clips;
  current: boolean;
  paused: boolean;
}) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const [started, setStarted] = useState(false);
  const [sourceIndex, setSourceIndex] = useState(0);
  const [transparent, setTransparent] = useState(false);
  const clip = clips[action];
  const source = clip.sources[sourceIndex];

  useEffect(() => {
    if (!paused) setStarted(true);
  }, [paused]);

  // A clip shown again starts from its keyframe, the pose the other states
  // are framed against, rather than wherever it was left.
  useEffect(() => {
    const video = videoRef.current;
    if (current && video) video.currentTime = 0;
  }, [current]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    if (paused || !transparent) {
      video.pause();
      return;
    }
    // A browser may deny autoplay; an interrupted play request is harmless.
    let cancelled = false;
    void video.play().catch(() => {
      if (!cancelled) setTransparent(false);
    });
    return () => {
      cancelled = true;
      video.pause();
    };
  }, [paused, transparent, sourceIndex, started]);

  const nextSource = () => {
    setTransparent(false);
    setSourceIndex((index) => index + 1);
  };

  const checkTransparency = () => {
    const video = videoRef.current;
    if (!video) return;
    // Both assets have a fully transparent top-left corner. Some browsers decode
    // a codec but discard alpha; probe once instead of showing a black rectangle.
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = 1;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    if (!context) return nextSource();
    try {
      context.drawImage(video, 0, 0, 8, 8, 0, 0, 1, 1);
      if (context.getImageData(0, 0, 1, 1).data[3] > 16) return nextSource();
      setTransparent(true);
    } catch {
      nextSource();
    }
  };

  return (
    <span className="research-action" data-painted={transparent}>
      <img src={clip.poster} alt="" draggable={false} />
      {started && source && (
        <video
          key={source}
          ref={videoRef}
          src={source}
          muted
          loop
          playsInline
          preload="auto"
          crossOrigin="anonymous"
          disablePictureInPicture
          aria-hidden="true"
          onLoadedData={checkTransparency}
          onError={nextSource}
        />
      )}
    </span>
  );
}
