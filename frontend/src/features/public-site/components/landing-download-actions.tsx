import type { DesktopEnvironmentName } from "../../../../electron/shared/desktop-bridge.types";
import {
  Apple,
  ArrowDown,
  ChevronDown,
  Cpu,
  Monitor,
  Terminal,
} from "@/shared/ui/icons";
import {
  detectDesktopPlatform,
  type DesktopDownloadOption,
  type MacosArchitecture,
} from "@/shared/desktop/desktop-download.service";
import "@/features/public-site/styles/landing-downloads.css";

type LandingDownloadProps = {
  options: DesktopDownloadOption[];
  macosArchitecture: MacosArchitecture | null;
  loading?: boolean;
};

export function LandingDownloadButton({
  options,
  macosArchitecture,
  loading = false,
}: LandingDownloadProps) {
  const platform = getDesktopPlatform();

  if (platform === "unknown") {
    return (
      <a className="landing-download-button" href="#download">
        Download Zenith Team <ArrowDown aria-hidden="true" size={17} />
      </a>
    );
  }

  if (loading) {
    return (
      <button
        aria-busy="true"
        className="landing-download-button"
        disabled
        type="button"
      >
        Finding your download…
      </button>
    );
  }

  const platformOptions = options.filter(
    (option) => option.platform === platform,
  );

  if (
    platform === "macos" &&
    (!macosArchitecture || macosArchitecture === "unknown")
  ) {
    return <MacDownloadMenu options={platformOptions} />;
  }

  const option = platformOptions.find((candidate) =>
    platform === "macos"
      ? candidate.id === `macos-${macosArchitecture}`
      : candidate.id === platform,
  );
  const platformName =
    platform === "macos"
      ? "macOS"
      : platform === "windows"
        ? "Windows"
        : "Linux";

  if (!option?.isAvailable || !option.url) {
    return (
      <UnavailableDownload
        platformName={platformName}
        reason={option?.disabledReason}
      />
    );
  }

  return (
    <div className="inline-flex flex-col items-center gap-[8px]">
      <a
        aria-label={option.label}
        className="landing-download-button"
        download={option.url.startsWith("/") ? true : undefined}
        href={option.url}
      >
        <DownloadIcon option={option} />
        Download for {platformName}
        <ArrowDown aria-hidden="true" size={17} />
      </a>
      <DownloadChannel environment={option.environment} />
    </div>
  );
}

export function LandingDownloadOptions({
  options,
  loading = false,
}: LandingDownloadProps) {
  return (
    <div
      aria-busy={loading}
      aria-label="Choose your desktop download"
      className="landing-download-options"
      role="group"
    >
      {options.map((option) => {
        const content = (
          <>
            <span className="landing-download-option-icon">
              <DownloadIcon option={option} />
            </span>
            <span className="text-[length:15px] font-[600] leading-[1.5]">
              {option.title}
            </span>
            <span className="landing-download-option-subtitle">
              {option.platform === "macos" ? "macOS · " : ""}
              {option.subtitle}
            </span>
            <span className="landing-download-option-action">
              {loading
                ? "Checking availability…"
                : option.isAvailable && option.url
                  ? "Download"
                  : "Coming soon"}
              {!loading && option.isAvailable && option.url ? (
                <ArrowDown aria-hidden="true" size={15} />
              ) : null}
            </span>
            {!loading && option.isAvailable && option.url ? (
              <DownloadChannel environment={option.environment} />
            ) : null}
          </>
        );

        if (loading || !option.isAvailable || !option.url) {
          return (
            <div
              aria-disabled="true"
              className="landing-download-option landing-download-option-unavailable"
              key={option.id}
              title={loading ? undefined : option.disabledReason}
            >
              {content}
            </div>
          );
        }

        return (
          <a
            aria-label={option.label}
            className="landing-download-option"
            download={option.url.startsWith("/") ? true : undefined}
            href={option.url}
            key={option.id}
          >
            {content}
          </a>
        );
      })}
    </div>
  );
}

function MacDownloadMenu({ options }: { options: DesktopDownloadOption[] }) {
  const hasDownload = options.some(
    (option) => option.isAvailable && option.url,
  );

  if (!hasDownload) {
    return <UnavailableDownload platformName="macOS" />;
  }

  return (
    <details className="landing-download-menu">
      <summary className="landing-download-button">
        <Apple aria-hidden="true" size={18} />
        Download for macOS
        <ChevronDown
          aria-hidden="true"
          className="landing-download-chevron"
          size={17}
        />
      </summary>
      <div className="absolute z-[80] top-[calc(100%_+_10px)] left-[50%] w-[min(290px,_calc(100vw_-_40px))] [transform:translateX(-50%)] [border:1px_solid_#e5ded4] rounded-[13px] [background:#faf8f4] p-[8px] text-[#342b26] font-[inherit] [box-shadow:0_16px_40px_rgb(52_43_38_/_13%)]">
        <p className="m-0 p-[7px_10px] text-[#6b6056] text-[length:10px] font-[600] tracking-[0.07em] uppercase">
          Choose your Mac
        </p>
        {options.map((option) => {
          const content = (
            <>
              <DownloadIcon option={option} />
              <span>
                <span className="block text-[length:13px] font-[600] leading-[1.5]">
                  {option.title}
                </span>
                <span className="block text-[#6b6056] text-[length:11px] leading-[1.6]">
                  {option.isAvailable && option.url
                    ? option.subtitle
                    : "Coming soon"}
                </span>
                {option.isAvailable && option.url ? (
                  <DownloadChannel environment={option.environment} />
                ) : null}
              </span>
            </>
          );

          if (!option.isAvailable || !option.url) {
            return (
              <span
                aria-disabled="true"
                className="landing-download-menu-item landing-download-menu-item-unavailable"
                key={option.id}
                title={option.disabledReason}
              >
                {content}
              </span>
            );
          }

          return (
            <a
              aria-label={option.label}
              className="landing-download-menu-item"
              download={option.url.startsWith("/") ? true : undefined}
              href={option.url}
              key={option.id}
            >
              {content}
              <ArrowDown aria-hidden="true" size={15} />
            </a>
          );
        })}
        <p className="m-[7px_10px_5px] [border-top:1px_solid_#e5ded4] pt-[11px] text-[#6b6056] text-[length:10px] leading-[1.6]">
          Find your chip in Apple menu → About This Mac.
        </p>
      </div>
    </details>
  );
}

function UnavailableDownload({
  platformName,
  reason,
}: {
  platformName: string;
  reason?: string;
}) {
  return (
    <div className="inline-flex flex-col items-center gap-[8px]">
      <button
        className="landing-download-button"
        disabled
        title={reason}
        type="button"
      >
        {platformName} · Coming soon
      </button>
      <a
        className="text-[#a85438] text-[length:12px] font-[500] [text-decoration:underline] [text-underline-offset:4px]"
        href="#download"
      >
        See available downloads
      </a>
    </div>
  );
}

function DownloadChannel({
  environment,
}: {
  environment: DesktopEnvironmentName;
}) {
  if (environment !== "dev") {
    return null;
  }

  return <span className="landing-download-channel">Development build</span>;
}

function DownloadIcon({ option }: { option: DesktopDownloadOption }) {
  const props = { "aria-hidden": true as const, size: 19, strokeWidth: 1.7 };

  switch (option.id) {
    case "macos-arm64":
      return <Apple {...props} />;
    case "macos-x64":
      return <Cpu {...props} />;
    case "windows":
      return <Monitor {...props} />;
    case "linux":
      return <Terminal {...props} />;
  }
}

function getDesktopPlatform() {
  if (typeof navigator === "undefined") {
    return "unknown";
  }

  const isMobile =
    /Android|iPhone|iPad|iPod|Mobile/i.test(navigator.userAgent) ||
    (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);

  return isMobile ? "unknown" : detectDesktopPlatform();
}
