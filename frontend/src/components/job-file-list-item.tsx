import { memo } from "react";
import { Link } from "react-router";

import { styled } from "@mui/material/styles";
import ListItem, { type ListItemProps } from "@mui/material/ListItem";
import ListItemText from "@mui/material/ListItemText";
import ListItemButton from "@mui/material/ListItemButton";
import Skeleton from "@mui/material/Skeleton";
import Typography from "@mui/material/Typography";

import { CopyStatus } from "@/entity";
import { formatFilesize } from "@/tools";

export const jobResultRowHeight = 54;
export const JobResultRow = styled(ListItem)<ListItemProps>({
  boxSizing: "border-box",
  height: jobResultRowHeight,
  minHeight: jobResultRowHeight,
  padding: "0 16px",
  borderBottom: "1px solid rgba(15, 23, 42, 0.08)",
});
const ResultText = styled(ListItemText)({ padding: 0, margin: "4px 0" });
export const JobResultText = ({ primary, secondary }: { primary: string; secondary: string }) => (
  <ResultText
    primary={primary}
    secondary={secondary}
    slotProps={{
      primary: { noWrap: true, title: primary },
      secondary: { noWrap: true, title: secondary },
    }}
  />
);
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
      secondary={`${formatFilesize(src.size)} · ${src.resultLabel || CopyStatus[src.status]}${src.resultMessage ? ` · ${src.resultMessage}` : ""}`}
    />
  );
  return (
    <JobResultRow component="div" className={className} disablePadding>
      {onClick ? <FileListItemButton onClick={onClick}>{text}</FileListItemButton> : text}
      {src.resultFileID ? (
        <Typography component={Link} to={`/file?file=${src.resultFileID}`} variant="body2" noWrap sx={{ ml: 2, flexShrink: 0 }}>
          View file
        </Typography>
      ) : null}
    </JobResultRow>
  );
});

export const FileRow = FileListItem;

export const FileRowPlaceholder = memo(() => (
  <JobResultRow component="div" disablePadding aria-hidden>
    <Skeleton variant="text" width="80%" height={24} animation={false} />
  </JobResultRow>
));
