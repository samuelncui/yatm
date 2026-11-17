import { t } from "@lingui/core/macro";
import { memo, useMemo } from "react";
import Typography from "@mui/material/Typography";
import Tooltip from "@mui/material/Tooltip";
import { FileArray } from "@samuelncui/chonky";

import { formatFilesize } from "@/tools";
import type { MeasurementState } from "./use-files-measure";

export interface ToobarInfoProps {
  files?: FileArray;
  measurement?: MeasurementState;
}

export const ToobarInfo: React.FC<ToobarInfoProps> = memo(({ files, measurement }) => {
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
  const usageError = summary?.error ?? "";
  const label = measurement?.running
    ? t`Calculating data usage`
    : (measurement?.error ??
      (summary ? (summary.complete ? t`Data usage for all matching items` : t`Known data usage · ${usageError}`) : t`Loaded items size`));
  return (
    <Tooltip title={label}>
      <Typography variant="body1" className="chonky-infoText" aria-label={label} role="status">
        {measurement?.running ? (
          t`Calculating…`
        ) : measurement?.error ? (
          t`Size unavailable`
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
