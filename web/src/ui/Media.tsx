// Video and audio from a machine (docs/web/web.md "Media", #1422): played straight from file/download, which the browser
// reads by Range, so seeking never waits for the whole file. A codec this browser lacks (HEVC outside Safari, most
// .mkv) says so and offers the download instead of a black box.
import { useEffect, useState } from "react";
import { fileDownloadURL } from "../lib/api";
import { cx } from "./atoms";
import { t } from "../i18n";
import { Icon } from "./icons";

export type MediaKind = "video" | "audio";

export interface MediaPlayerProps {
  mid: string;
  path: string;
  cwd?: string;
  kind: MediaKind;
  className?: string;
  compact?: boolean;
  /** The file's real length once the browser has read its header (seconds). */
  onDuration?: (seconds: number) => void;
  /** A panel with its own file resources supplies its download URL resolver instead of calling the default API helper. */
  resolveDownloadURL?: (path: string, options: { inline?: boolean }) => string;
}

export function MediaPlayer({
  mid,
  path,
  cwd,
  kind,
  className,
  compact,
  onDuration,
  resolveDownloadURL,
}: MediaPlayerProps) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [mid, path, cwd, kind, resolveDownloadURL]);
  const url = (inline: boolean) =>
    resolveDownloadURL ? resolveDownloadURL(path, { inline }) : fileDownloadURL(mid, path, cwd, inline ? { inline: true } : undefined);
  const src = url(true);
  const reportDuration = (d: number) => {
    if (Number.isFinite(d) && d > 0) onDuration?.(d);
  };
  if (failed)
    return (
      <div className={cx("flex flex-col items-center justify-center gap-2 rounded-[10px] bg-inset px-4 py-6 text-center", className)}>
        <Icon name="info" size={18} className="text-ink-3" />
        <div className="text-[13px] text-ink">{kind === "video" ? t("ui-media.video_unplayable") : t("ui-media.audio_unplayable")}</div>
        <div className="max-w-72 text-[12px] leading-relaxed text-ink-3">{kind === "video" ? t("ui-media.video_hint") : t("ui-media.audio_hint")}</div>
        <a href={url(false)} download className="mt-1 inline-flex h-7 items-center gap-1 rounded-[8px] bg-surface px-2.5 text-[12px] font-medium whitespace-nowrap text-ink shadow-btn hover:bg-hover">
          <Icon name="arrowDown" size={13} />
          {t("ui-media.download")}
        </a>
      </div>
    );
  return kind === "video" ? (
    <video src={src} controls preload="metadata" playsInline onError={() => setFailed(true)} onLoadedMetadata={(e) => reportDuration(e.currentTarget.duration)} className={cx("block rounded-[10px] bg-black", className)} />
  ) : (
    <audio src={src} controls preload="metadata" onError={() => setFailed(true)} onLoadedMetadata={(e) => reportDuration(e.currentTarget.duration)} className={cx(compact ? "h-9 w-full" : "w-full max-w-[560px]", className)} />
  );
}
