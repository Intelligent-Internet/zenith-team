import {
  LayoutDashboard,
  PackageCheck,
  Paperclip,
  Plus,
} from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import "./artifact-picker.css";

export function ChatAddMenu({
  disabled,
  uploadDisabled,
  onUpload,
  onSelectArtifact,
  onCreateWhiteboard,
}: {
  disabled?: boolean;
  uploadDisabled?: boolean;
  onUpload: () => void;
  onSelectArtifact?: () => void;
  /** Makes a Whiteboard in this chat, as the bar under its header does. */
  onCreateWhiteboard?: () => void;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="chat-add-button"
          disabled={disabled}
          title="Add to message"
          aria-label="Add to message"
        >
          <Plus size={19} />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        side="top"
        sideOffset={8}
        className="min-w-48"
      >
        <DropdownMenuItem disabled={uploadDisabled} onSelect={onUpload}>
          <Paperclip size={17} />
          Upload files
        </DropdownMenuItem>
        {onSelectArtifact && (
          <DropdownMenuItem onSelect={onSelectArtifact}>
            <PackageCheck size={17} />
            Select artifact
          </DropdownMenuItem>
        )}
        {onCreateWhiteboard && (
          <DropdownMenuItem onSelect={onCreateWhiteboard}>
            <LayoutDashboard size={17} />
            New whiteboard
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
