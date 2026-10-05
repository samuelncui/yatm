import { Feedback } from "@/components/feedback";
import { useCallback, useRef, useState } from "react";

import Alert from "@mui/material/Alert";
import CircularProgress from "@mui/material/CircularProgress";
import Stack from "@mui/material/Stack";
import { contentTime } from "@/components/content-status";

import { mediaCli } from "@/api";
import { MediaKind } from "@/entity";
import type { InspectMediaResponse, InspectMediaRequest } from "@/entity";
import { formatFilesize } from "@/tools";

export const useMediaInspect = () => {
  const [reply, setReply] = useState<InspectMediaResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const sequence = useRef(0);

  const inspect = useCallback(async (target: InspectMediaRequest["target"], identity?: string) => {
    const current = ++sequence.current;
    setLoading(true);
    setError("");
    try {
      const value = await mediaCli.inspect({ target, identity: identity || undefined }).response;
      if (current !== sequence.current) return null;
      setReply(value);
      return value;
    } catch (reason) {
      if (current !== sequence.current) return null;
      setReply(null);
      setError(reason instanceof Error ? reason.message : "Unable to inspect Media");
      return null;
    } finally {
      if (current === sequence.current) setLoading(false);
    }
  }, []);

  const reset = useCallback(() => {
    sequence.current += 1;
    setReply(null);
    setLoading(false);
    setError("");
  }, []);

  return { reply, loading, error, inspect, reset };
};

export const MediaInspectResult = ({ reply, loading, error }: { reply: InspectMediaResponse | null; loading: boolean; error?: string }) => {
  if (loading) {
    return (
      <Stack direction="row" spacing={1} sx={{ mt: 2, alignItems: "center" }}>
        <CircularProgress size={18} />
        <span>Inspecting Media…</span>
      </Stack>
    );
  }
  if (error) return <Feedback severity="error">{error}</Feedback>;
  if (!reply) return null;

  const media = reply.media;
  const kind = media?.kind === MediaKind.TAPE ? "Tape" : media?.kind === MediaKind.VOLUME ? "Volume" : "Media";
  return (
    <Stack spacing={0.5} sx={{ mt: 2 }}>
      <strong>{reply.identity || "No Media identity detected"}</strong>
      {reply.mountPoint && <span>Mount point: {reply.mountPoint}</span>}
      {media ? (
        <>
          <span>
            {kind}: {media.name || "Unnamed"}
          </span>
          <span>
            {reply.fileCount.toString()} files · {formatFilesize(media.writtenBytes)} written
          </span>
          <span>Created {contentTime(media.createdAtNs)}</span>
          {reply.lastWrittenAtNs !== undefined && <span>Last written {contentTime(reply.lastWrittenAtNs)}</span>}
          {media.kind === MediaKind.VOLUME && (
            <span>{media.mounted ? `Online · ${formatFilesize(media.filesystemAvailableBytes ?? 0n)} available` : "Mount required"}</span>
          )}
        </>
      ) : (
        <Alert severity="info">This Media is not registered in the Library.</Alert>
      )}
    </Stack>
  );
};
