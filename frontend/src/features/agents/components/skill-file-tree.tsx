import { useState } from "react";
import {
  ChevronRight,
  FileText,
  Folder,
  FolderOpen,
  Search,
  Undo2,
} from "@/shared/ui/icons";
import type { AgentSkill } from "@/shared/api/agent-skill-api";

type Node = { name: string; path: string; children?: Node[] };

function fileTree(paths: string[]): Node[] {
  const roots: Node[] = [];
  for (const filePath of paths) {
    let nodes = roots;
    const parts = filePath.split("/");
    parts.forEach((name, index) => {
      const path = parts.slice(0, index + 1).join("/");
      let node = nodes.find((item) => item.path === path);
      if (!node) {
        node = {
          name,
          path,
          ...(index < parts.length - 1 ? { children: [] } : {}),
        };
        nodes.push(node);
      }
      if (node.children) nodes = node.children;
    });
  }
  const sort = (nodes: Node[]) => {
    nodes.sort((a, b) => {
      if (a.path === "SKILL.md") return -1;
      if (b.path === "SKILL.md") return 1;
      return (
        Number(!!b.children) - Number(!!a.children) ||
        a.name.localeCompare(b.name)
      );
    });
    nodes.forEach((node) => {
      if (node.children) sort(node.children);
    });
  };
  sort(roots);
  return roots;
}

export function SkillFileTree({
  files,
  selectedPath,
  modifiedPaths,
  deletePaths,
  disabled,
  onSelect,
  onRestore,
}: {
  files: AgentSkill["files"];
  selectedPath: string;
  modifiedPaths: string[];
  deletePaths: string[];
  disabled: boolean;
  onSelect: (path: string) => void;
  onRestore: (path: string) => void;
}) {
  const [search, setSearch] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(
    () =>
      new Set(
        files.flatMap((file) => {
          const parts = file.path.split("/");
          return parts
            .slice(1, -1)
            .map((_, index) => parts.slice(0, index + 2).join("/"));
        }),
      ),
  );
  const [searchCollapsed, setSearchCollapsed] = useState<Set<string>>(
    new Set(),
  );
  const term = search.trim().toLowerCase();
  const matches = (node: Node): boolean =>
    node.path.toLowerCase().includes(term) || !!node.children?.some(matches);
  const renderNodes = (nodes: Node[], depth: number) => (
    <ul className="skill-tree-list">
      {nodes.filter(matches).map((node) => {
        const open = !(term ? searchCollapsed : collapsed).has(node.path);
        const removed = deletePaths.includes(node.path);
        const modified = modifiedPaths.includes(node.path);
        const NodeIcon = node.children
          ? open
            ? FolderOpen
            : Folder
          : FileText;
        return (
          <li key={node.path}>
            <div
              className="skill-tree-row"
              data-selected={!node.children && node.path === selectedPath}
              data-deleted={removed}
            >
              <button
                type="button"
                className="skill-tree-node"
                style={{ paddingLeft: 12 + depth * 16 }}
                aria-expanded={node.children ? open : undefined}
                aria-current={
                  !node.children && node.path === selectedPath
                    ? "page"
                    : undefined
                }
                aria-label={`${node.path}${removed ? ", marked for deletion" : modified ? ", modified" : ""}`}
                title={node.path}
                disabled={disabled}
                onClick={() => {
                  if (!node.children) onSelect(node.path);
                  else
                    (term ? setSearchCollapsed : setCollapsed)((current) => {
                      const next = new Set(current);
                      if (next.has(node.path)) next.delete(node.path);
                      else next.add(node.path);
                      return next;
                    });
                }}
              >
                {node.children ? (
                  <ChevronRight
                    size={12}
                    className={open ? "skill-tree-chevron--open" : ""}
                    aria-hidden
                  />
                ) : (
                  <span className="skill-tree-indent" />
                )}
                <NodeIcon size={16} aria-hidden />
                <span className="skill-tree-name">{node.name}</span>
                {modified && !removed && (
                  <span className="skill-tree-modified" aria-hidden />
                )}
              </button>
              {removed && (
                <button
                  type="button"
                  className="custom-skill-icon-button"
                  aria-label={`Restore ${node.path}`}
                  title="Restore file"
                  disabled={disabled}
                  onClick={() => onRestore(node.path)}
                >
                  <Undo2 size={14} aria-hidden />
                </button>
              )}
            </div>
            {node.children && open && renderNodes(node.children, depth + 1)}
          </li>
        );
      })}
    </ul>
  );
  // Keep buffers accessible if another editor removes their files.
  const roots = fileTree([
    ...new Set([...files.map((file) => file.path), ...modifiedPaths]),
  ]);
  return (
    <aside className="skill-files-sidebar">
      <header>
        <strong>Files</strong>
        <span>{files.length}</span>
      </header>
      <label className="skill-files-search">
        <Search size={15} aria-hidden />
        <input
          type="search"
          placeholder="Find a file…"
          aria-label="Find a skill file"
          value={search}
          onChange={(event) => {
            setSearch(event.target.value);
            setSearchCollapsed(new Set());
          }}
        />
      </label>
      <nav aria-label="Skill files" className="skill-files-tree">
        {roots.some(matches) ? (
          renderNodes(roots, 0)
        ) : (
          <p className="skill-tree-empty">No matching files</p>
        )}
      </nav>
    </aside>
  );
}
