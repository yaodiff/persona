// The file viewer as its own panel (a path from a tool step or a message; docs/apple/ios.md "File viewer"):
// shares the unified File Workbench (FilesPanel) with a collapsible directory tree and in-place file switching.
import { dirName, fileName } from "../../lib/files";
import { relativeSegments, sameTreePath } from "../../lib/files-tree";
import { openPanel } from "../../lib/panel";
import { navigate, routeAt } from "../../lib/router";
import { FilesPanel } from "./FilesPanel";

export function FileViewerPanel({
  mid,
  sid,
  path,
  cwd,
  line,
  to,
  readOnly,
  onClose,
}: {
  mid: string;
  sid?: string;
  path: string;
  cwd?: string;
  line?: number;
  to?: number;
  readOnly?: boolean;
  onClose: () => void;
}) {
  const folder = dirName(path) || "/";
  const inCwd = Boolean(cwd && (sameTreePath(cwd, path) || relativeSegments(cwd, path) !== null));
  const root = inCwd ? cwd! : folder;
  function showInFolder(file: string) {
    const targetFolder = dirName(file) || "/";
    const route = routeAt(location.href);
    if (route?.name === "machine" && route.mid === mid && route.subview === "files") {
      onClose();
      navigate({ name: "machine", mid, subview: "files", path: targetFolder });
    } else {
      openPanel({ kind: "files", mid, sid: sid ?? "", cwd: cwd || targetFolder, path: targetFolder, highlight: fileName(file) });
    }
  }
  return (
    <FilesPanel
      mid={mid}
      sid={sid ?? ""}
      cwd={cwd || folder}
      path={root}
      file={path}
      line={line}
      to={to}
      readOnly={readOnly}
      onClose={onClose}
      // Show in Files: the Files panel at its folder, the file marked (#5827). Omitted in read-only mode so it cannot escape to writable file management.
      onShowInFolder={readOnly ? undefined : showInFolder}
    />
  );
}
