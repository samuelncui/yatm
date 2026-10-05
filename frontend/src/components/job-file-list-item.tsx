import { memo } from "react";
import { Link } from "react-router";

import { styled } from "@mui/material/styles";
import ListItem, { type ListItemProps } from "@mui/material/ListItem";
import ListItemText from "@mui/material/ListItemText";
import ListItemButton from "@mui/material/ListItemButton";
import Skeleton from "@mui/material/Skeleton";

import { CopyStatus } from "@/entity";
import { formatFilesize } from "@/tools";

export const JobResultRow = styled(ListItem)<ListItemProps>({
  boxSizing: "border-box",
  minHeight: 54,
  padding: "0 16px",
  borderBottom: "1px solid rgba(15, 23, 42, 0.08)",
});
export const JobResultText = styled(ListItemText)({ padding: 0, margin: "4px 0" });
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
    <JobResultText
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
      <JobResultRow component="div" className={className} disablePadding>
        {text}
      </JobResultRow>
    );
  }

  return (
    <JobResultRow component="div" className={className} disablePadding>
      <FileListItemButton onClick={onClick}>{text}</FileListItemButton>
    </JobResultRow>
  );
});

export const FileRow = FileListItem;

export const FileRowPlaceholder = memo(() => (
  <JobResultRow component="div" disablePadding>
    <Skeleton variant="text" width="80%" height={24} animation="wave" />
  </JobResultRow>
));
