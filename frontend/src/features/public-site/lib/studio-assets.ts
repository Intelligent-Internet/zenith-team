export function getStudioHeroPoster() {
  return window.innerWidth <= 1440 && window.devicePixelRatio <= 1
    ? "/images/company-studio/hero-poster-1440.webp"
    : "/images/company-studio/hero-poster.webp?v=4";
}
