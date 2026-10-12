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
  type DesktopEnvironmentName,
  type MacosArchitecture,
} from "@/app/desktop/desktop-download.service";
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
    <div className="landing-download-primary">
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
            <span className="landing-download-option-title">
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
      <div className="landing-download-menu-panel">
        <p className="landing-download-menu-label">Choose your Mac</p>
        {options.map((option) => {
          const content = (
            <>
              <DownloadIcon option={option} />
              <span>
                <span className="landing-download-menu-title">
                  {option.title}
                </span>
                <span className="landing-download-menu-description">
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
        <p className="landing-download-menu-help">
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
    <div className="landing-download-primary">
      <button
        className="landing-download-button"
        disabled
        title={reason}
        type="button"
      >
        {platformName} · Coming soon
      </button>
      <a className="landing-download-alternative" href="#download">
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
