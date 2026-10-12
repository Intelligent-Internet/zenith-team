import { publicAssetPath } from "@/shared/lib/public-assets";

const publicLandingBackgroundVideoSrc =
  "https://files.example.com/boardly/bg-auth.mp4";
const publicLandingBackgroundBlurSrc = publicAssetPath(
  "images/bg-auth-blur.png",
);

export function PublicLandingBackground() {
  return (
    <div className="pointer-events-none absolute inset-0 overflow-hidden">
      <video
        aria-hidden="true"
        autoPlay
        className="public-landing-video absolute inset-0 z-0 h-full w-full object-cover"
        loop
        muted
        playsInline
        poster={publicLandingBackgroundBlurSrc}
        src={publicLandingBackgroundVideoSrc}
      />
      <img
        alt=""
        aria-hidden="true"
        className="absolute inset-0 z-[1] h-full w-full object-cover"
        src={publicLandingBackgroundBlurSrc}
      />
      <span
        aria-hidden="true"
        className="absolute inset-0 z-[2] bg-white/10"
      />
      <span
        aria-hidden="true"
        className="absolute inset-0 z-[2] bg-[radial-gradient(circle_at_50%_44%,transparent_0%,rgba(6,7,23,0.04)_52%,rgba(6,7,23,0.18)_100%)]"
      />
      <span
        aria-hidden="true"
        className="absolute inset-x-0 bottom-0 z-[3] h-[34%] bg-[linear-gradient(180deg,transparent_0%,rgba(3,9,20,0.22)_58%,rgba(3,9,20,0.54)_100%)]"
      />
    </div>
  );
}
