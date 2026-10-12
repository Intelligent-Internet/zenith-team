import {
  createContext,
  memo,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  Handle,
  Position,
  NodeResizer,
  useStore,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import { Download, FileText, LockKeyhole } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";
import type { components } from "@/shared/api/generated/api.generated";
import {
  boardFilePath,
  boardPath,
  publicBoardPath,
  type BoardFile,
  type BoardScope,
  type BoardItem,
  type BoardHydratedItem,
  type BoardCommand,
} from "./board-api";
import { acquireBoardFile, downloadBoardFile } from "./board-file-cache";
import { BoardNoteSession } from "./board-note-session";
import { BoardNote } from "./board-note";
import { geometryOf } from "./board-scene-session";
import {
  BoardCompartments,
  BoardShapeGraphic,
  boardFragmentTag,
  boardFragmentTagWidth,
  boardLifelineHead,
  boardShapeLabelBox,
  isCompartmented,
  isHollowShape,
} from "./board-shape";
import { hasTextBackground, textBackground } from "./board-text";
export type WhiteboardFlowNode = Node<
  {
    item: BoardItem;
    pending?: boolean;
    points?: components["schemas"]["BoardPoint"][];
  },
  "whiteboard"
>;
export const BoardNodeContext = createContext<{
  scope: BoardScope;
  editable: boolean;
  note?: (item: BoardItem) => BoardNoteSession;
  /** Marks a note session as shown; the returned function releases it. */
  retainNote?: (session: BoardNoteSession) => () => void;
  publicToken?: string;
  hydrate: (id: string) => Promise<BoardHydratedItem>;
  commit: (commands: BoardCommand[]) => void;
  /** Opens a class's, entity's or fragment's compartments for editing. */
  editCompartments?: (item: BoardItem) => void;
} | null>(null);
// Memoized, so a node re-renders for its own props only; zoom reaches it
// solely as the threshold where notes collapse to excerpts.
export const BoardNode = memo(function BoardNode({
  data,
  selected,
  width,
  height,
}: NodeProps<WhiteboardFlowNode>) {
  const context = useContext(BoardNodeContext)!;
  const { item, pending } = data;
  const compact = useStore((state) => state.transform[2] < 0.3);
  const editable = context.editable && !pending && !item.properties.locked;
  const [editingTitle, setEditingTitle] = useState(false);
  useEffect(() => {
    if (!selected || !editable) setEditingTitle(false);
  }, [selected, editable]);
  const resizeOrigin = useRef(item);
  const note = item.type === "sticky" || item.type === "text";
  // Ink drawn here starts from its local points; once those are released to
  // the hydration cache, the same points stay on screen while it answers.
  const [hydratedPoints, setHydratedPoints] = useState<
    components["schemas"]["BoardPoint"][]
  >(data.points ?? []);
  const points = data.points ?? hydratedPoints;
  const stroke = useMemo(() => {
    if (item.type !== "stroke") return undefined;
    let right = 12,
      bottom = 12;
    for (const point of points) {
      right = Math.max(right, point.x + 6);
      bottom = Math.max(bottom, point.y + 6);
    }
    return {
      viewBox: `0 0 ${right} ${bottom}`,
      points: points.map((point) => `${point.x},${point.y}`).join(" "),
    };
  }, [item.type, points]);
  const [media, setMedia] = useState("");
  const [contentFailed, setContentFailed] = useState(false);
  const [contentRequestRevision, setContentRequestRevision] = useState(0);
  const retryContent = () => {
    setContentFailed(false);
    setMedia("");
    setContentRequestRevision((revision) => revision + 1);
  };
  useEffect(() => {
    if (item.type !== "image") return;
    return boardlyRealtimeClient.onEvent((event) => {
      if (
        event.companyId === context.scope.companyId &&
        event.type === "project.access_changed"
      ) {
        setMedia("");
        setContentFailed(false);
        setContentRequestRevision((revision) => revision + 1);
      }
    });
  }, [context.scope.companyId, item.type]);
  useEffect(() => {
    if (item.type !== "stroke" || item.id.startsWith("local:") || data.points)
      return;
    let stopped = false;
    void context
      .hydrate(item.id)
      .then((result) => {
        if (!stopped) {
          setHydratedPoints(result.points ?? []);
          setContentFailed(false);
        }
      })
      .catch(() => {
        if (!stopped) setContentFailed(true);
      });
    return () => {
      stopped = true;
    };
  }, [
    item.id,
    item.type,
    data.points,
    context.hydrate,
    contentRequestRevision,
  ]);
  const fileBase = context.publicToken
    ? publicBoardPath(context.publicToken)
    : boardPath(context.scope);
  // A preview covers ordinary zoom; the original loads once the image is shown
  // larger than its preview's pixels.
  const original = useStore(
    (state) =>
      item.type === "image" &&
      !!item.file?.hasPreview &&
      item.width * state.transform[2] * window.devicePixelRatio >
        boardPreviewWidth(item.file),
  );
  useEffect(() => {
    if (item.type !== "image" || !item.properties.attachmentId) return;
    const file = acquireBoardFile(
      boardFilePath(
        fileBase,
        item.properties.attachmentId,
        original ? "original" : "preview",
      ),
    );
    let stopped = false;
    void file.url.then(
      (url) => {
        if (stopped) return;
        setMedia(url);
        setContentFailed(false);
      },
      () => {
        if (!stopped) setContentFailed(true);
      },
    );
    return () => {
      stopped = true;
      file.release();
    };
  }, [
    fileBase,
    item.properties.attachmentId,
    item.type,
    original,
    contentRequestRevision,
  ]);
  const fill = note
    ? textBackground(item)
    : item.properties.fill ||
      (item.type === "frame" ? "transparent" : "#ffffff");
  const paper = note && hasTextBackground(fill);
  const shape = item.type === "shape" ? item.properties.shape : undefined;
  const nodeWidth = width ?? item.width,
    nodeHeight = height ?? item.height;
  const labelBox = boardShapeLabelBox(
    shape,
    nodeWidth,
    nodeHeight,
    item.properties.head,
  );
  const lifelineHead = boardLifelineHead(nodeHeight, item.properties.head);
  const hollow = item.type === "shape" && isHollowShape(shape);
  // A lifeline connects at its header's middle, where its name is.
  const handleStyle =
    shape === "lifeline" ? { top: lifelineHead / 2 } : undefined;
  const textColor =
    item.properties.color ||
    (note && !paper ? "var(--workspace-native-text)" : "#25252d");
  return (
    <div
      className={`wb-node wb-node-${item.type}${note ? " wb-node-note" : ""}${paper ? " wb-node-paper" : ""}${hollow ? " wb-node-hollow" : ""}`}
      data-board-item={item.id}
      onDoubleClick={() => {
        if (item.type !== "shape" || !editable) return;
        if (isCompartmented(shape)) context.editCompartments?.(item);
        else setEditingTitle(true);
      }}
      style={{
        background: fill,
        color: textColor,
        transform: item.rotation ? `rotate(${item.rotation}deg)` : undefined,
        borderColor: item.type === "frame" ? textColor : undefined,
      }}
    >
      {item.type === "shape" && (
        <BoardShapeGraphic
          shape={shape}
          width={nodeWidth}
          height={nodeHeight}
          color={textColor}
          fill={fill}
          strokeWidth={item.properties.strokeWidth || 1}
          properties={item.properties}
        />
      )}
      {hollow &&
        (shape === "lifeline" ? (
          <>
            <div
              className="wb-hit wb-hit-header"
              style={{ height: lifelineHead }}
            />
            <div className="wb-hit wb-hit-axis" style={{ top: lifelineHead }} />
          </>
        ) : (
          <>
            <div className="wb-hit wb-hit-edge wb-hit-top" />
            <div className="wb-hit wb-hit-edge wb-hit-bottom" />
            <div className="wb-hit wb-hit-edge wb-hit-left" />
            <div className="wb-hit wb-hit-edge wb-hit-right" />
            <div
              className="wb-hit"
              style={{
                left: 0,
                top: 0,
                width: boardFragmentTagWidth(
                  item.properties.title ?? "",
                  nodeWidth,
                ),
                height: Math.min(nodeHeight, boardFragmentTag),
              }}
            />
          </>
        ))}
      <NodeResizer
        isVisible={selected && context.editable && !item.properties.locked}
        handleStyle={note ? { display: "none" } : undefined}
        lineStyle={note ? { borderColor: "transparent" } : undefined}
        shouldResize={() => editable}
        minWidth={item.type === "stroke" ? 8 : 40}
        minHeight={item.type === "stroke" ? 8 : 40}
        onResizeStart={() => {
          resizeOrigin.current = item;
        }}
        onResizeEnd={(_event, size) =>
          context.commit([
            {
              type: "geometry",
              id: item.id,
              expectedRevision: resizeOrigin.current.geometryRevision,
              geometry: {
                ...geometryOf(resizeOrigin.current),
                x: size.x,
                y: size.y,
                width: size.width,
                height: size.height,
              },
            },
          ])
        }
      />
      {item.properties.locked && (
        <LockKeyhole className="wb-lock-indicator" size={13} />
      )}
      {note && context.editable && (
        <div
          className="wb-node-drag-handle"
          role="button"
          tabIndex={editable && !item.properties.locked ? 0 : -1}
          aria-label="Move text"
          aria-disabled={!editable || item.properties.locked}
          title="Drag to move. Select and use arrow keys to move with the keyboard."
        />
      )}
      {note ? (
        item.id.startsWith("local:") || (compact && !selected) ? (
          <div className="wb-note-excerpt">
            {item.text || "Waiting to save…"}
          </div>
        ) : !context.note ? (
          <PublicNote item={item} hydrate={context.hydrate} />
        ) : (
          <NoteBody
            item={item}
            // A locked note's text is read-only, like its style and position.
            readOnly={!context.editable || !!item.properties.locked}
            getNote={context.note}
            retain={context.retainNote}
          />
        )
      ) : contentFailed && (item.type === "stroke" || item.type === "image") ? (
        <div
          className="wb-content-unavailable nodrag nopan"
          onClick={(event) => event.stopPropagation()}
        >
          <span>
            {item.type === "image" ? "Image unavailable" : "Stroke unavailable"}
          </span>
          <button
            type="button"
            aria-label={item.type === "image" ? "Retry image" : "Retry stroke"}
            onClick={retryContent}
          >
            Retry
          </button>
        </div>
      ) : item.type === "stroke" ? (
        <svg
          className="wb-stroke"
          viewBox={stroke?.viewBox}
          preserveAspectRatio="none"
          aria-label={
            item.properties.brush === "highlighter"
              ? "Highlighter stroke"
              : "Pen stroke"
          }
        >
          <polyline
            points={stroke?.points}
            fill="none"
            stroke={textColor}
            strokeWidth={item.properties.strokeWidth || 3}
            opacity={item.properties.brush === "highlighter" ? 0.3 : 1}
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      ) : item.type === "image" ? (
        <>
          {media ? (
            <img
              src={media}
              alt={
                item.properties.alt ||
                item.properties.caption ||
                item.properties.title ||
                "Board image"
              }
              draggable={false}
              onError={() => {
                setMedia("");
                setContentFailed(true);
              }}
            />
          ) : (
            <span>Loading image…</span>
          )}
          {item.properties.caption && (
            <span className="wb-media-caption">{item.properties.caption}</span>
          )}
        </>
      ) : item.type === "file" ? (
        <BoardFileCard
          file={item.file}
          download={() =>
            item.file &&
            downloadBoardFile(
              boardFilePath(fileBase, item.file.attachmentId, "original"),
              item.file.filename,
            ).catch((error) => toast.error(getBoardlyQueryErrorMessage(error)))
          }
        />
      ) : item.type === "shape" && isCompartmented(shape) ? (
        <BoardCompartments
          shape={shape}
          properties={item.properties}
          width={nodeWidth}
        />
      ) : item.type === "shape" && !editingTitle ? (
        <span className="wb-shape-label" style={labelBox}>
          {item.properties.title}
        </span>
      ) : (
        <input
          className={`wb-item-title nodrag nopan${labelBox ? " wb-item-title-placed" : ""}`}
          style={labelBox}
          aria-label={item.type === "frame" ? "Frame name" : "Shape label"}
          key={`${item.id}:${item.contentRevision}`}
          defaultValue={item.properties.title}
          readOnly={!editable}
          placeholder={item.type === "frame" ? "Name this frame" : ""}
          title={
            item.type === "frame" && editable ? "Click to rename" : undefined
          }
          autoFocus={item.type === "shape"}
          onFocus={(event) => {
            if (editable) event.target.select();
          }}
          onKeyDown={(event) => {
            if (event.nativeEvent.isComposing) return;
            if (event.key !== "Escape" && event.key !== "Enter") return;
            event.preventDefault();
            event.stopPropagation();
            if (event.key === "Escape") {
              event.currentTarget.value = item.properties.title ?? "";
            }
            event.currentTarget.blur();
          }}
          onBlur={(event) => {
            setEditingTitle(false);
            if (
              editable &&
              event.target.value !== (item.properties.title ?? "")
            )
              context.commit([
                {
                  type: "content",
                  id: item.id,
                  expectedRevision: item.contentRevision,
                  properties: { ...item.properties, title: event.target.value },
                },
              ]);
          }}
        />
      )}
      {item.type !== "frame" && (
        <>
          <Handle
            type="source"
            position={Position.Right}
            isConnectable={editable}
            style={handleStyle}
          />
          <Handle
            type="target"
            position={Position.Left}
            isConnectable={editable}
            style={handleStyle}
          />
        </>
      )}
    </div>
  );
});
function PublicNote({
  item,
  hydrate,
}: {
  item: BoardItem;
  hydrate: (id: string) => Promise<BoardHydratedItem>;
}) {
  const [text, setText] = useState(item.text);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let stopped = false;
    void hydrate(item.id)
      .then((result) => {
        if (!stopped) {
          setText(result.text ?? "");
          setFailed(false);
        }
      })
      .catch(() => {
        if (!stopped) {
          setText("");
          setFailed(true);
        }
      });
    return () => {
      stopped = true;
    };
  }, [item.id, item.textRevision, hydrate]);
  return (
    <div className="wb-note-excerpt" style={{ whiteSpace: "pre-wrap" }}>
      {failed ? "Note unavailable" : text}
    </div>
  );
}
function NoteBody({
  item,
  readOnly,
  getNote,
  retain,
}: {
  item: BoardItem;
  readOnly: boolean;
  getNote: (item: BoardItem) => BoardNoteSession;
  retain?: (session: BoardNoteSession) => () => void;
}) {
  const session = getNote(item);
  useSyncExternalStore(session.subscribe, session.snapshot);
  useEffect(() => {
    // A note whose first load failed loads again when it is shown again.
    if (session.status === "failed") session.retry();
    return retain?.(session);
  }, [session, retain]);
  return <BoardNote session={session} readOnly={readOnly} label="Text" />;
}

function boardPreviewWidth(file: BoardFile) {
  const width = file.width ?? 0,
    height = file.height ?? 0;
  return width * Math.min(1, 1024 / Math.max(width, height, 1));
}
function formatFileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
function BoardFileCard({
  file,
  download,
}: {
  file?: BoardFile;
  download: () => void;
}) {
  return (
    <div className="wb-file-card">
      <FileText className="wb-file-card__icon" size={28} aria-hidden />
      <span className="wb-file-card__text">
        <strong title={file?.filename}>{file?.filename ?? "File"}</strong>
        <small>
          {file ? formatFileSize(file.sizeBytes) : "Waiting to save…"}
        </small>
      </span>
      {file && (
        <button
          type="button"
          className="wb-file-card__download nodrag nopan"
          aria-label={`Download ${file.filename}`}
          title="Download"
          onClick={(event) => {
            event.stopPropagation();
            download();
          }}
        >
          <Download size={18} />
        </button>
      )}
    </div>
  );
}
