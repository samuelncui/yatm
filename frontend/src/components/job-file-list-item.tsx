import { memo } from "react";

import { styled } from "@mui/material/styles";
import ListItem from "@mui/material/ListItem";
import ListItemText from "@mui/material/ListItemText";
import ListItemButton from "@mui/material/ListItemButton";

import { CopyStatus, SourceState } from "../entity";
import { formatFilesize } from "../tools";

const FileListItemText = styled(ListItemText)({ padding: 0, margin: 5, marginLeft: 10 });
const FileListItemButton = styled(ListItemButton)({ padding: 0 });

export interface FileState {
  path: string;
  status: CopyStatus;
  size: bigint;
  //   message?: string;
}

export const FileListItem = memo(({ src, onClick, className }: { src?: FileState; onClick?: () => void; className?: string }) => {
  if (!src) {
    return null;
  }

  const text = <FileListItemText primary={src.path} secondary={`Size: ${formatFilesize(src.size)} Status: ${CopyStatus[src.status]}`} />;
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
