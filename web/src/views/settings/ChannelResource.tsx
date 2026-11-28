import { ChannelPageContent, type ChannelPageProps } from "./Channels";
import { useChannelController } from "./useChannelController";

/** Open the scoped editor when its channel detail is requested. */
export function ChannelResource({ id, ...props }: ChannelPageProps) {
  const ctl = useChannelController(id, props.manager);
  return <ChannelPageContent {...props} ctl={ctl} />;
}
