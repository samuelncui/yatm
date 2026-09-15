import { useCallback, useMemo, useRef, useState } from "react";
import type { Nullable } from "tsdef";

import EditOutlinedIcon from "@mui/icons-material/EditOutlined";
import FolderRoundedIcon from "@mui/icons-material/FolderRounded";
import Dialog, { type DialogProps } from "@mui/material/Dialog";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import LinearProgress from "@mui/material/LinearProgress";
import Button from "@mui/material/Button";
import Chip from "@mui/material/Chip";
import Stack from "@mui/material/Stack";
import type { FileData } from "@samuelncui/chonky";
import { toast } from "react-toastify";

import { cli, convertFiles, MODE_DIR } from "@/api";
import { FileMetadataDialog } from "@/components/file-metadata-dialog";
import { FileContent } from "@/components/file-content";
import { LibraryArchiveButton } from "@/components/library-archive";
import { associatedLibraryFileID } from "@/components/location-files";
import { FileScope, type FileGetReply } from "@/entity";

import "./file-detail.less";

export type Detail = FileGetReply;

export const useFileDetail = () => {
  const [detail, setDetail] = useState<Nullable<Detail>>(null);
  const [loading, setLoading] = useState(false);
  const request = useRef(0);

  const loadDetail = useCallback(async (id: string) => {
    const currentRequest = ++request.current;
    if (!/^\d+$/.test(id)) {
      setDetail(null);
      setLoading(false);
      return;
    }
    setDetail((current) => (current?.file?.id.toString() === id ? current : null));
    setLoading(true);

    try {
      const reply = await cli.fileGet({ id: BigInt(id), scope: FileScope.ALL, cursor: "", limit: 100 }).response;
      if (request.current === currentRequest) setDetail(reply);
    } catch (error) {
      if (request.current === currentRequest) {
        toast.error(error instanceof Error ? error.message : "Load file details failed");
      }
    } finally {
      if (request.current === currentRequest) setLoading(false);
    }
  }, []);

  const clearDetail = useCallback(() => {
    request.current += 1;
    setDetail(null);
    setLoading(false);
  }, []);

  return { detail, loading, loadDetail, clearDetail };
};

export const FileInspector = ({
  selected,
  detail,
  loading,
  onRefresh,
}: {
  selected: Nullable<FileData>;
  detail: Nullable<Detail>;
  loading: boolean;
  onRefresh: () => Promise<void>;
}) => {
  const visibleDetail = selected && associatedLibraryFileID(selected) === detail?.file?.id.toString() ? detail : null;

  return (
    <section className="file-inspector">
      <header className="file-inspector-header">
        <strong title={selected?.name}>{selected?.name?.split("/").at(-1) ?? "File details"}</strong>
      </header>
      <div className="file-inspector-body">
        {!selected && (
          <div className="file-inspector-empty">
            <FolderRoundedIcon />
            <h3>Select a file</h3>
          </div>
        )}
        {selected && loading && <LinearProgress sx={{ position: "absolute", top: 0, right: 0, left: 0, zIndex: 2 }} />}
        {selected && visibleDetail && <FileDetailContent detail={visibleDetail} onRefresh={onRefresh} />}
        {selected?.physicalPath && selected.isDir && <code className="physical-folder-path">{selected.physicalPath}</code>}
      </div>
    </section>
  );
};

export const DetailModal = (props: Omit<DialogProps, "open" | "children"> & { detail: Nullable<Detail>; onRefresh: () => Promise<void> }) => {
  const { detail, onRefresh, ...otherProps } = props;
  if (!detail) return null;

  return (
    <Dialog className="detail-dialog" open scroll="body" maxWidth="md" fullWidth {...otherProps}>
      <DialogTitle>{detail.file?.name}</DialogTitle>
      <DialogContent dividers>
        <FileDetailContent detail={detail} onRefresh={onRefresh} />
      </DialogContent>
    </Dialog>
  );
};

const FileDetailContent = ({ detail, onRefresh }: { detail: Detail; onRefresh: () => Promise<void> }) => {
  const [editing, setEditing] = useState(false);
  const file = detail.file;
  const metadataFiles = useMemo(() => (file ? convertFiles([file]) : []), [file]);
  if (!file) return <div className="file-inspector-empty">File information is unavailable.</div>;

  const isDir = (file.mode & MODE_DIR) > 0;
  const organization = (
    <section className="content-section file-organization">
      <div className="section-heading">
        <h3>Organization</h3>
        <Button size="small" startIcon={<EditOutlinedIcon />} onClick={() => setEditing(true)}>
          Edit tags & note
        </Button>
      </div>
      <dl className="file-detail-summary">
        <dt>Name</dt>
        <dd>{file.name}</dd>
        <dt>Type</dt>
        <dd>{isDir ? "Directory" : "File"}</dd>
        <dt>Tags</dt>
        <dd>
          <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: "wrap" }}>
            {file.tags.length === 0 ? <span className="product-muted">None</span> : file.tags.map((tag) => <Chip key={tag} label={tag} size="small" />)}
          </Stack>
        </dd>
        <dt>Note</dt>
        <dd className="file-detail-note">{file.note || <span className="product-muted">None</span>}</dd>
      </dl>
    </section>
  );

  return (
    <div className="file-detail-content">
      {isDir ? (
        <>
          <div className="file-detail-preview">
            <FolderRoundedIcon />
          </div>
          {organization}
          <LibraryArchiveButton fileIDs={[file.id]} label="Back up folder" />
        </>
      ) : (
        <FileContent key={file.id.toString()} file={file} currentPreview={detail.preview} organization={organization} />
      )}
      <FileMetadataDialog files={metadataFiles} open={editing} onClose={() => setEditing(false)} onSaved={onRefresh} />
    </div>
  );
};
