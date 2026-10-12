import { ImageIcon, Text, Video } from "@/shared/ui/icons";

export function StudioStatusArt() {
  return (
    <div className="studio-mode-panel__loading-art" aria-hidden="true">
      <svg
        className="studio-mode-panel__loading-links"
        viewBox="0 0 400 280"
        fill="none"
      >
        <path d="M156 74 C216 74 198 140 252 140" />
        <path d="M156 218 C216 218 198 140 252 140" />
        <circle cx="156" cy="74" r="3" />
        <circle cx="156" cy="218" r="3" />
        <circle cx="252" cy="140" r="3" />
      </svg>
      <div className="studio-mode-panel__loading-node studio-mode-panel__loading-node--image">
        <div className="studio-mode-panel__loading-node-heading">
          <ImageIcon size={15} />
          <i />
        </div>
        <div className="studio-mode-panel__loading-preview">
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
        <div className="studio-mode-panel__loading-preview">
          <Video size={30} />
        </div>
        <span className="studio-mode-panel__loading-line" />
      </div>
    </div>
  );
}
