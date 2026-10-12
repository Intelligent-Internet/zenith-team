import { useEffect, useLayoutEffect, useRef, useState } from "react";
import "./image-preview.css";

export function ImagePreview({ src, alt }: { src: string; alt: string }) {
  return <ImagePreviewContent key={src} src={src} alt={alt} />;
}

function ImagePreviewContent({ src, alt }: { src: string; alt: string }) {
  const stageRef = useRef<HTMLDivElement>(null);
  const imageRef = useRef<HTMLImageElement>(null);
  const anchorRef = useRef<{
    imageX: number;
    imageY: number;
    pointerX: number;
    pointerY: number;
  } | null>(null);
  const [naturalSize, setNaturalSize] = useState({ width: 0, height: 0 });
  const [stageSize, setStageSize] = useState({ width: 0, height: 0 });
  const [zoom, setZoom] = useState<"fit" | number>("fit");
  const [failed, setFailed] = useState(false);
  const ready =
    naturalSize.width > 0 && stageSize.width > 0 && stageSize.height > 0;
  const fit = ready
    ? Math.min(
        stageSize.width / naturalSize.width,
        stageSize.height / naturalSize.height,
      )
    : 1;
  const scale = zoom === "fit" ? fit : zoom;
  const minimum = Math.min(0.1, fit);
  const maximum = Math.max(4, fit);

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) return;
    const observer = new ResizeObserver(([entry]) => {
      const { width, height } = entry.contentRect;
      setStageSize((previous) =>
        previous.width === width && previous.height === height
          ? previous
          : { width, height },
      );
    });
    observer.observe(stage);
    return () => observer.disconnect();
  }, []);

  function changeZoom(next: number, clientX?: number, clientY?: number) {
    const stage = stageRef.current;
    const image = imageRef.current;
    if (!ready || !stage || !image) return;
    const stageRect = stage.getBoundingClientRect();
    const imageRect = image.getBoundingClientRect();
    const x = clientX ?? stageRect.left + stage.clientWidth / 2;
    const y = clientY ?? stageRect.top + stage.clientHeight / 2;
    anchorRef.current = {
      imageX: (x - imageRect.left) / scale,
      imageY: (y - imageRect.top) / scale,
      pointerX: x - stageRect.left,
      pointerY: y - stageRect.top,
    };
    setZoom(Math.min(maximum, Math.max(minimum, next)));
  }

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) return;
    const onWheel = (event: WheelEvent) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      changeZoom(
        scale * Math.exp(-event.deltaY * 0.01),
        event.clientX,
        event.clientY,
      );
    };
    stage.addEventListener("wheel", onWheel, { passive: false });
    return () => stage.removeEventListener("wheel", onWheel);
  }, [scale, ready, minimum, maximum]);

  useLayoutEffect(() => {
    const stage = stageRef.current;
    const image = imageRef.current;
    if (!stage || !image || !ready) return;
    const anchor = anchorRef.current;
    if (anchor) {
      const stageRect = stage.getBoundingClientRect();
      const imageRect = image.getBoundingClientRect();
      stage.scrollLeft +=
        imageRect.left -
        stageRect.left +
        anchor.imageX * scale -
        anchor.pointerX;
      stage.scrollTop +=
        imageRect.top - stageRect.top + anchor.imageY * scale - anchor.pointerY;
      anchorRef.current = null;
    } else {
      stage.scrollLeft = Math.max(
        0,
        (stage.scrollWidth - stage.clientWidth) / 2,
      );
      stage.scrollTop = Math.max(
        0,
        (stage.scrollHeight - stage.clientHeight) / 2,
      );
    }
  }, [scale, ready, stageSize]);

  function fitToView() {
    anchorRef.current = null;
    setZoom("fit");
  }

  return (
    <div
      className="image-preview"
      onKeyDown={(event) => {
        if (event.target !== stageRef.current) return;
        if (event.key === "+" || event.key === "=") changeZoom(scale * 1.25);
        else if (event.key === "-") changeZoom(scale / 1.25);
        else if (event.key === "0") fitToView();
        else return;
        event.preventDefault();
        event.stopPropagation();
      }}
    >
      <div
        className="image-preview__stage"
        ref={stageRef}
        tabIndex={0}
        role="group"
        aria-label={`${alt} preview. Use plus and minus to zoom, and 0 to fit.`}
      >
        {failed ? (
          <p className="image-preview__message" role="status">
            This image could not be displayed. Download it to open it elsewhere.
          </p>
        ) : (
          <div
            className="image-preview__canvas"
            style={{
              width: Math.max(stageSize.width, naturalSize.width * scale),
              height: Math.max(stageSize.height, naturalSize.height * scale),
            }}
          >
            <img
              ref={imageRef}
              src={src}
              alt={alt}
              draggable={false}
              onLoad={(event) =>
                setNaturalSize({
                  width: event.currentTarget.naturalWidth,
                  height: event.currentTarget.naturalHeight,
                })
              }
              onError={() => setFailed(true)}
              style={{
                width: naturalSize.width * scale || undefined,
                height: naturalSize.height * scale || undefined,
                visibility: ready ? "visible" : "hidden",
              }}
            />
          </div>
        )}
      </div>
    </div>
  );
}
