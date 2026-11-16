import { Feedback } from "@/components/feedback";
import { Fragment, useEffect, useId, useState } from "react";
import { Button, Dialog, useMediaQuery, useTheme } from "@mui/material";
import FolderRoundedIcon from "@mui/icons-material/FolderRounded";
import { filesCli } from "@/api";
import { FileOperationRef, type FilesDetail } from "@/entity";
import { FileContent } from "@/components/file-content";
import { DetailDialog, DetailSurface } from "@/components/detail-surface";
import { errorMessage } from "@/tools";

const identity = (target?: FileOperationRef) => (target ? FileOperationRef.toJsonString(target) : "");
export const useFileDetail = (target?: FileOperationRef, refreshKey = 0) => {
  const key = identity(target);
  const [snapshot, setSnapshot] = useState<{ key: string; detail: FilesDetail }>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    if (!key) {
      setSnapshot(undefined);
      return;
    }
    let active = true;
    setLoading(true);
    setError("");
    void filesCli
      .get({ reference: FileOperationRef.fromJsonString(key) })
      .response.then(({ detail }) => {
        if (!detail) throw new Error("File details are missing");
        if (active) setSnapshot({ key, detail });
      })
      .catch((failure) => active && setError(errorMessage(failure, "Could not load file details")))
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [key, refreshKey, retry]);
  return { detail: snapshot?.key === key ? snapshot.detail : undefined, loading, error, refresh: () => setRetry((value) => value + 1) };
};

type Props = { target?: FileOperationRef; name?: string; refreshKey?: number; onRefresh: () => Promise<void> };
export const FileInspector = (props: Props) => <Controlled {...props} />;
export const DetailModal = ({
  target,
  name,
  refreshKey,
  onRefresh,
  onClose,
  ...dialogProps
}: Props & Omit<React.ComponentProps<typeof Dialog>, "open" | "children" | "onClose"> & { onClose?: () => void }) => {
  const theme = useTheme();
  const fullScreen = useMediaQuery(theme.breakpoints.down("sm"));
  const titleId = useId();
  return (
    <DetailDialog
      open={!!target}
      scroll="paper"
      maxWidth="sm"
      fullWidth
      fullScreen={fullScreen}
      aria-labelledby={titleId}
      onClose={() => onClose?.()}
      {...dialogProps}
    >
      <Controlled target={target} name={name} titleId={titleId} refreshKey={refreshKey} onRefresh={onRefresh} onClose={onClose} />
    </DetailDialog>
  );
};
const Controlled = ({ target, name, refreshKey, onRefresh, onClose, titleId }: Props & { onClose?: () => void; titleId?: string }) => {
  const { detail, loading, error, refresh } = useFileDetail(target, refreshKey);
  const [view, setView] = useState<"overview" | "versions">("overview");
  const notice = error && (
    <Feedback severity="error" action={<Button onClick={refresh}>Retry</Button>}>
      {error}
    </Feedback>
  );
  if (detail)
    return (
      <FileContent
        key={identity(target)}
        detail={detail}
        title={name}
        titleId={titleId}
        busy={loading}
        notice={notice}
        selectedView={view}
        onViewChange={setView}
        onClose={onClose}
        onRefresh={async () => {
          refresh();
          await onRefresh();
        }}
      />
    );
  return (
    <DetailSurface title={name || "File details"} titleId={titleId} icon={<FolderRoundedIcon fontSize="small" />} onClose={onClose} notice={notice}>
      {!target ? (
        <div className="detail-surface-empty">
          <FolderRoundedIcon />
          <h3>Select a file</h3>
        </div>
      ) : (
        !error && <FileDetailSkeleton />
      )}
    </DetailSurface>
  );
};

// Reading a selected file keeps the shape its detail will take, so switching files replaces a
// skeleton in place instead of collapsing the panel to a spinner and expanding it again.
const FileDetailSkeleton = () => (
  <div className="file-detail-skeleton" role="status" aria-label="Loading file details">
    <div className="file-detail-skeleton-frame" />
    <div className="file-detail-skeleton-rows">
      {[0, 1, 2].map((row) => (
        <Fragment key={row}>
          <span />
          <span />
        </Fragment>
      ))}
    </div>
  </div>
);
