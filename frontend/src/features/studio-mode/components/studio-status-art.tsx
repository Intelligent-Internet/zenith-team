import { ImageIcon, Text, Video } from "@/shared/ui/icons";

export function StudioStatusArt() {
  return (
    <div
      className="shrink-0 [container:studio-loading-art_/_inline-size] relative w-[min(100%,_400px)] aspect-[10_/_7] text-workspace-muted"
      aria-hidden="true"
    >
      <svg
        className="studio-mode-panel__loading-links absolute inset-0 w-full h-full overflow-visible [stroke:color-mix(_in_srgb,_var(--workspace-native-text-muted)_30%,_transparent_)] [stroke-width:1.5]"
        viewBox="0 0 400 280"
        fill="none"
      >
        <path d="M156 74 C216 74 198 140 252 140" />
        <path d="M156 218 C216 218 198 140 252 140" />
        <circle cx="156" cy="74" r="3" />
        <circle cx="156" cy="218" r="3" />
        <circle cx="252" cy="140" r="3" />
      </svg>
      <div className="studio-mode-panel__loading-node top-0 left-[3%]">
        <div className="studio-mode-panel__loading-node-heading">
          <ImageIcon size={15} />
          <i />
        </div>
        <div className="studio-mode-panel__loading-preview grid place-items-center aspect-[1.7] rounded-workspace-control [background:linear-gradient(_135deg,_var(--workspace-hover-bg),_var(--workspace-thread-bg)_)] text-[color-mix(_in_srgb,_var(--workspace-native-text-muted)_45%,_transparent_)]">
          <ImageIcon size={26} />
        </div>
        <span className="studio-mode-panel__loading-line" />
      </div>
      <div className="studio-mode-panel__loading-node studio-mode-panel__loading-node--note">
        <div className="studio-mode-panel__loading-node-heading">
          <Text size={15} />
          <i />
        </div>
        <span className="studio-mode-panel__loading-line" />
        <span className="studio-mode-panel__loading-line" />
        <span className="studio-mode-panel__loading-line" />
      </div>
      <div className="studio-mode-panel__loading-node studio-mode-panel__loading-node--video">
        <div className="studio-mode-panel__loading-node-heading">
          <Video size={15} />
          <i />
        </div>
        <div className="studio-mode-panel__loading-preview grid place-items-center aspect-[1.7] rounded-workspace-control [background:linear-gradient(_135deg,_var(--workspace-hover-bg),_var(--workspace-thread-bg)_)] text-[color-mix(_in_srgb,_var(--workspace-native-text-muted)_45%,_transparent_)]">
          <Video size={30} />
        </div>
        <span className="studio-mode-panel__loading-line" />
      </div>
    </div>
  );
}
