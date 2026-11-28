// Parse large delimited text files off the main thread.
import { delimitedSheet } from "./sheets";
import type { SheetReply } from "./sheet-worker";

self.onmessage = ({ data }: MessageEvent<{ name: string; text: string }>) => {
  let reply: SheetReply;
  try {
    reply = { sheets: [delimitedSheet(data.name, data.text)] };
  } catch (err) {
    reply = { error: err instanceof Error ? err.message : String(err) };
  }
  self.postMessage(reply);
};
