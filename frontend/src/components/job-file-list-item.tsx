import { memo } from "react";
import { Link } from "react-router";

import { styled } from "@mui/material/styles";
import ListItem from "@mui/material/ListItem";
import ListItemText from "@mui/material/ListItemText";
import ListItemButton from "@mui/material/ListItemButton";
import Skeleton from "@mui/material/Skeleton";

import { CopyStatus } from "@/entity";
import { formatFilesize } from "@/tools";

const FileListItemText = styled(ListItemText)({ padding: 0, margin: 5, marginLeft: 10 });
const FileListItemButton = styled(ListItemButton)({ padding: 0 });

export interface FileState {
  path: string;
  status: CopyStatus;
  size: bigint;
  resultLabel?: string;
  resultMessage?: string;
  resultFileID?: bigint;
}

export const FileListItem = memo(({ src, onClick, className }: { src?: FileState; onClick?: () => void; className?: string }) => {
  if (!src) {
    return null;
  }

  const text = (
    <FileListItemText
      primary={src.path}
      secondary={
        <>
          {formatFilesize(src.size)} · {src.resultLabel || CopyStatus[src.status]}
          {src.resultMessage ? ` · ${src.resultMessage}` : ""}
          {src.resultFileID ? (
            <>
              {" "}
              · <Link to={`/file?file=${src.resultFileID}`}>View file</Link>
            </>
          ) : null}
        </>
      }
    />
  );
  if (!onClick) {
    return (
      <ListItem component="div" className={className} disablePadding>
        {text}
      </ListItem>
    );
  }

  return (
    <ListItem component="div" className={className} disablePadding>
      <FileListItemButton onClick={onClick}>{text}</FileListItemButton>
    </ListItem>
  );
});

export const FileRow = styled(FileListItem)(({ indent }: { indent?: number }) => ({
  paddingLeft: indent !== undefined ? indent : 16,
}));

export const FileRowPlaceholder = memo(({ indent }: { indent?: number }) => (
  <div style={{ paddingLeft: indent !== undefined ? indent : 16, display: "flex", alignItems: "center", height: 54 }}>
    <Skeleton variant="text" width="80%" height={24} animation="wave" />
  </div>
));
