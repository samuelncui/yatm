import { useState } from "react";
import { Button, Tooltip } from "@mui/material";
import type { FileData } from "@samuelncui/chonky";
import type { LibraryFileData } from "@/api";
import { admitLocationFile, fileLocationReference } from "@/components/location-files";
import { FileMetadataDialog } from "@/components/file-metadata-dialog";
import { formatFilesize, runUIAction } from "@/tools";
import { FileContentSummary, type Location } from "@/entity";
import { backupColors, backupSummary } from "@/components/content-status";
import { OriginalLocationLink } from "@/components/original-location-link";

export const LiveFileInspector = ({ file, location, onRefresh }: { file: FileData; location?: Location; onRefresh: () => Promise<void> }) => {
  const [editing, setEditing] = useState<LibraryFileData[]>([]);
  const reference = fileLocationReference(file);
  const regular = file.isRegularFile === true;
  const status = regular && FileContentSummary.is(file.contentSummary) ? backupSummary(file.contentSummary) : undefined;
  return (
    <section className="file-inspector">
      <h2>{file.name}</h2>
      <OriginalLocationLink location={location ?? { id: reference.locationId, name: "Location", rootPath: "" }} path={reference.path} />
      {status && (
        <Tooltip title={status.description} placement="left" arrow>
          <span className="backup-summary-label" tabIndex={0}>
            <span className="backup-dot" style={{ backgroundColor: backupColors[status.tone] }} />
            {status.title}
          </span>
        </Tooltip>
      )}
      <dl>
        <dt>Type</dt>
        <dd>{file.isSymlink ? "Symbolic link" : file.isDir ? "Folder" : regular ? "File" : "Special file"}</dd>
        {regular && (
          <>
            <dt>Size</dt>
            <dd>{formatFilesize(reference.facts?.size ?? 0n)}</dd>
          </>
        )}
        <dt>Modified</dt>
        <dd>{reference.facts ? new Date(Number(reference.facts.mtimeNs / 1000000n)).toLocaleString() : "—"}</dd>
      </dl>
      {regular && (
        <div className="product-actions">
          <Button onClick={() => runUIAction(async () => setEditing([await admitLocationFile(file)]), "Could not prepare annotations")}>
            Edit tags &amp; note
          </Button>
          <Button
            onClick={() =>
              runUIAction(async () => {
                await admitLocationFile(file);
                await onRefresh();
              }, "Could not add file to Library")
            }
          >
            Add to Library
          </Button>
        </div>
      )}
      <FileMetadataDialog files={editing} open={editing.length > 0} onClose={() => setEditing([])} onSaved={onRefresh} />
    </section>
  );
};
