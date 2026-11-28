import { Suspense } from "react";
import { lazyPage } from "../../lib/lazy-page";
import { OverlayBoundary } from "../LazyOverlay";
import type { CommentBarProps } from "./CommentBar";

const Bar = lazyPage(() => import("./CommentBar").then((module) => module.CommentBar));

export function preloadCommentBar() {
  return Bar.preload();
}

export function CommentBarOverlay(props: CommentBarProps) {
  return (
    <OverlayBoundary open onClose={props.onDismiss ?? props.onCancel}>
      <Suspense fallback={null}>
        <Bar {...props} />
      </Suspense>
    </OverlayBoundary>
  );
}
