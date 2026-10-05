import { memo, useMemo } from "react";
import Typography from "@mui/material/Typography";
import Tooltip from "@mui/material/Tooltip";
import { FileArray } from "@samuelncui/chonky";

import { formatFilesize } from "@/tools";
import type { MeasurementState } from "./use-files-measure";

export interface ToobarInfoProps {
  files?: FileArray;
  measurement?: MeasurementState;
  /** The settled number of entries the listing found, absent for a partial result set. */
  total?: bigint;
}

export const ToobarInfo: React.FC<ToobarInfoProps> = memo(({ files, measurement, total }) => {
  const [size, notFinished] = useMemo(() => {
    let size = 0;
    let notFinished = false;
    for (const file of files || []) {
      if (!file) {
        continue;
      }

      if (file.size === undefined) {
        notFinished = true;
        continue;
      }

      size += file.size;
    }

    return [size, notFinished];
  }, [files]);

  const summary = measurement?.summary;
  const label = measurement?.running
    ? "Calculating data usage"
    : (measurement?.error ??
      (summary ? (summary.complete ? "Data usage for all matching items" : `Known data usage · ${summary.error}`) : "Loaded items size"));
  // The count answers "how many entries are here" as soon as a listing arrives; a query
  // result set is partial, so it states no total.
  const count = total === undefined ? undefined : `${total} ${total === 1n ? "entry" : "entries"}`;
  return (
    <Tooltip title={label}>
      <Typography variant="body1" className="chonky-infoText" aria-label={label} role="status">
        {count && <span className="chonky-infoCount">{count} · </span>}
        {measurement?.running ? (
          "Calculating…"
        ) : measurement?.error ? (
          "Size unavailable"
        ) : (
          <>
            {(summary ? !summary.complete : notFinished) && "? "}
            {formatFilesize(summary ? Number(summary.knownBytes) : size)}
          </>
        )}
      </Typography>
    </Tooltip>
  );
});
