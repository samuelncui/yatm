import { t } from "@lingui/core/macro";
import { Feedback } from "@/components/feedback";
import { useCallback, useRef, useState } from "react";

import Alert from "@mui/material/Alert";
import CircularProgress from "@mui/material/CircularProgress";
import Stack from "@mui/material/Stack";
import { contentTime } from "@/components/content-status";

import { mediaCli, volumeSpaceLabel } from "@/api";
import { MediaKind } from "@/entity";
import type { InspectMediaResponse, InspectMediaRequest } from "@/entity";
import { formatFilesize, errorMessage } from "@/tools";

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
      setError(errorMessage(reason, t`Unable to inspect Media`));
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
        <span>{t`Inspecting Media…`}</span>
      </Stack>
    );
  }
  if (error) return <Feedback severity="error">{error}</Feedback>;
  if (!reply) return null;

  const media = reply.media;
  const kind = media?.kind === MediaKind.TAPE ? t`Tape` : media?.kind === MediaKind.VOLUME ? t`Volume` : t`Media`;
  const name = media?.name || t`Unnamed`;
  const fileCount = media ? reply.fileCount.toString() : "";
  const written = media ? formatFilesize(media.writtenBytes) : "";
  const mountPoint = reply.mountPoint ?? "";
  const created = media ? contentTime(media.createdAtNs) : "";
  const lastWritten = reply.lastWrittenAtNs !== undefined ? contentTime(reply.lastWrittenAtNs) : "";
  const space = media?.kind === MediaKind.VOLUME ? volumeSpaceLabel(media) : "";
  return (
    <Stack spacing={0.5} sx={{ mt: 2 }}>
      <strong>{reply.identity || t`No Media identity detected`}</strong>
      {reply.mountPoint && <span>{t`Mount point: ${mountPoint}`}</span>}
      {media ? (
        <>
          <span>{t`${kind}: ${name}`}</span>
          <span>{t`${fileCount} files · ${written} written`}</span>
          <span>{t`Created ${created}`}</span>
          {reply.lastWrittenAtNs !== undefined && <span>{t`Last written ${lastWritten}`}</span>}
          {media.kind === MediaKind.VOLUME && <span>{media.mounted ? t`Online · ${space}` : space}</span>}
        </>
      ) : (
        <Alert severity="info">{t`This Media is not registered in the Library.`}</Alert>
      )}
    </Stack>
  );
};
