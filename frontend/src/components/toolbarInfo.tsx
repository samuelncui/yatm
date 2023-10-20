import { memo, useMemo } from "react";
import Typography from "@mui/material/Typography";
import { FileArray } from "@samuelncui/chonky";

import { formatFilesize } from "../tools";

export interface ToobarInfoProps {
  files?: FileArray;
}

export const ToobarInfo: React.FC<ToobarInfoProps> = memo(({ files }) => {
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

  return (
    <div className="chonky-infoContainer">
      <Typography variant="body1" className="chonky-infoText">
        {notFinished && "? "}
        {formatFilesize(size)}
      </Typography>
    </div>
  );
});
