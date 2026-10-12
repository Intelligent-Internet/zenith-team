import xLogo from "@/assets/brands/x.svg";
import facebookLogo from "@/assets/brands/facebook.svg";
import linkedinLogo from "@/assets/brands/linkedIn.svg";
import instagramLogo from "@/assets/brands/instagram.svg";
import tiktokLogo from "@/assets/brands/tiktok.png";
import "./social-platform-mark.css";

export function SocialPlatformMark({ platform }: { platform: string }) {
  const logos: Record<string, string> = {
    x: xLogo,
    facebook: facebookLogo,
    linkedin: linkedinLogo,
    instagram: instagramLogo,
    tiktok: tiktokLogo,
  };
  return (
    <span
      className={`social-platform-mark social-platform-mark--${platform}`}
      aria-hidden="true"
    >
      {logos[platform] && <img src={logos[platform]} alt="" />}
    </span>
  );
}
