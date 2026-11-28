import { cx } from "../atoms";
import { Icon } from "../icons";

export function SpeedMark({ speed, size, className }: { speed?: string; size?: number; className?: string }) {
  if (speed !== "fast" && speed !== "ultrafast") return null;
  return <Icon name={speed === "fast" ? "bolt" : "bolt-double"} size={size} className={cx("shrink-0 fill-current", className)} />;
}
