import { memo } from "react";
import Typography from "@mui/material/Typography";
import { FileArray } from "@samuelncui/chonky";

import { formatFilesize } from "../tools";

export interface ToobarInfoProps {
  files?: FileArray;
}

export const ToobarInfo: React.FC<ToobarInfoProps> = memo((props) => {
  return (
    <div className="chonky-infoContainer">
      <Typography variant="body1" className="chonky-infoText">
        {formatFilesize((props.files || []).reduce((total, file) => total + (file?.size ? file.size : 0), 0))}
      </Typography>
    </div>
  );
});
