import { useEffect, useState, type ReactNode } from "react";
import { t } from "../i18n";
import { Loading } from "../ui/atoms";
import { enabledModules, loadedModule, loadModule, useModuleStates, type ModuleFileKind, type ModuleFilePreviewProps } from "./module-host";

/** The viewer supplies the ordinary download card for an unavailable preview. */
export function ModuleFilePreview({ kind, children, ...props }: ModuleFilePreviewProps & { kind: ModuleFileKind; children: ReactNode }) {
  useModuleStates();
  const id = enabledModules().find((d) => d.filePreviews?.includes(kind))?.id;
  const Preview = id && loadedModule(id)?.FilePreview;
  const [failed, setFailed] = useState<string>();
  useEffect(() => {
    if (!id || Preview) return;
    let live = true;
    setFailed(undefined);
    loadModule(id).catch(() => { if (live) setFailed(id); });
    return () => { live = false; };
  }, [id, Preview]);
  if (!id || failed === id) return children;
  if (Preview) return <Preview {...props} />;
  return <div className="flex flex-1 items-center justify-center"><Loading label={t("session-files-fileview.reading")} /></div>;
}
