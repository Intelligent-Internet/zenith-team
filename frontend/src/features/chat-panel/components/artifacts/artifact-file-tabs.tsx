import { useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import {
  ChevronDown,
  FileCode2,
  FileText,
  ImageIcon,
  Music,
  Video,
} from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import type { ArtifactPreviewFile } from "@/features/chat-panel/model/artifact-file-access";

export function ArtifactFileTabs({
  id,
  files,
  selectedId,
  onSelect,
}: {
  id: string;
  files: Pick<ArtifactPreviewFile, "id" | "filename" | "contentType">[];
  selectedId: string;
  onSelect: (id: string) => void;
}) {
  const listRef = useRef<HTMLDivElement>(null);
  const [overflow, setOverflow] = useState(false);

  useLayoutEffect(() => {
    const list = listRef.current;
    if (!list) return;
    const measure = () => {
      setOverflow(list.scrollWidth > list.clientWidth + 1);
      list.querySelector('[aria-selected="true"]')?.scrollIntoView({
        block: "nearest",
        inline: "nearest",
      });
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(list);
    for (const tab of list.children) observer.observe(tab);
    return () => observer.disconnect();
  }, [files]);

  useLayoutEffect(() => {
    listRef.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({
      block: "nearest",
      inline: "nearest",
    });
  }, [selectedId]);

  const navigateTabs = (
    event: KeyboardEvent<HTMLButtonElement>,
    index: number,
  ) => {
    let nextIndex: number;
    switch (event.key) {
      case "ArrowRight":
        nextIndex = (index + 1) % files.length;
        break;
      case "ArrowLeft":
        nextIndex = (index - 1 + files.length) % files.length;
        break;
      case "Home":
        nextIndex = 0;
        break;
      case "End":
        nextIndex = files.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    onSelect(files[nextIndex].id);
    (
      listRef.current?.children[nextIndex] as HTMLButtonElement | undefined
    )?.focus();
  };

  return (
    <div className="artifact-file-tabs">
      <div
        ref={listRef}
        aria-label="Artifact files"
        className="artifact-file-tabs__list"
        role="tablist"
      >
        {files.map((file, index) => (
          <button
            aria-controls={`${id}-panel`}
            aria-label={file.filename}
            aria-selected={file.id === selectedId}
            className="artifact-file-tab"
            id={`${id}-${file.id}`}
            key={file.id}
            onClick={() => onSelect(file.id)}
            onKeyDown={(event) => navigateTabs(event, index)}
            role="tab"
            tabIndex={file.id === selectedId ? 0 : -1}
            title={file.filename}
            type="button"
          >
            <FileIcon contentType={file.contentType} />
            <FileName filename={file.filename} />
          </button>
        ))}
      </div>
      {overflow && files.length > 1 ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              className="artifact-action-button artifact-file-tabs__more"
              type="button"
            >
              Files ({files.length}) <ChevronDown aria-hidden size={14} />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="end"
            aria-label="All artifact files"
            className="artifact-file-menu"
          >
            <DropdownMenuRadioGroup value={selectedId} onValueChange={onSelect}>
              {files.map((file) => (
                <DropdownMenuRadioItem
                  key={file.id}
                  value={file.id}
                  textValue={file.filename}
                >
                  <FileIcon contentType={file.contentType} />
                  <span>{file.filename}</span>
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      ) : null}
    </div>
  );
}

function FileName({ filename }: { filename: string }) {
  const extensionIndex = filename.lastIndexOf(".");
  return (
    <span className="artifact-file-tab__name">
      <span>
        {extensionIndex > 0 ? filename.slice(0, extensionIndex) : filename}
      </span>
      {extensionIndex > 0 ? (
        <span className="artifact-file-tab__extension">
          {filename.slice(extensionIndex)}
        </span>
      ) : null}
    </span>
  );
}

function FileIcon({ contentType }: { contentType: string }) {
  const Icon = contentType.startsWith("image/")
    ? ImageIcon
    : contentType.startsWith("video/")
      ? Video
      : contentType.startsWith("audio/")
        ? Music
        : /json|javascript|xml|yaml/.test(contentType)
          ? FileCode2
          : FileText;
  return <Icon aria-hidden size={16} />;
}
