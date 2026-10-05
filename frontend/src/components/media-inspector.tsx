import { Alert } from "@mui/material";
import StorageRoundedIcon from "@mui/icons-material/StorageRounded";
import { MediaKind, PositionHealth, VolumeType, type Media, type Position } from "@/entity";
import { ContentCopies, ContentPreview, copyHealthLabel } from "@/components/file-content";
import { contentHex, contentTime } from "@/components/content-status";
import { accessLabel, isArchivePosition, isMediaFile, mediaAvailabilityLabel, type MediaFileData, type MediaPositionFileData } from "@/api";
import { formatFilesize } from "@/tools";
import { DetailSurface } from "./detail-surface";

/**
 * The Media page's panel: it details whichever row the browser selected - a Media, or one Position
 * on it - from facts the row already carries, so selecting costs no read. A Position also shows
 * the Archived copies of its content and, when it records a Signature, that content's Preview.
 */
export const MediaInspector = ({ selection, media }: { selection?: MediaFileData | MediaPositionFileData | null; media?: MediaFileData }) => {
  const position = isArchivePosition(selection) ? selection.position : undefined;
  const selected = isMediaFile(selection) ? selection.media : undefined;
  // The header names the selected row: a Media's name or identity, or the Position's file name.
  const name = selection?.name || selected?.identity;
  const health = position?.health ?? PositionHealth.UNKNOWN;
  return (
    <DetailSurface
      title={name || "Media details"}
      icon={<StorageRoundedIcon fontSize="small" />}
      status={
        selected
          ? { label: mediaAvailabilityLabel(selected), color: selected.mounted ? "#16a34a" : "#94a3b8" }
          : position
            ? { label: copyHealthLabel(health), color: health === PositionHealth.HEALTHY ? "#16a34a" : "#94a3b8" }
            : undefined
      }
      preview={position?.signature.length ? <ContentPreview signature={position.signature} label="Preview · archived content" reserveSpace /> : undefined}
    >
      {selected ? (
        <MediaContent media={selected} />
      ) : position ? (
        <PositionContent position={position} media={media?.media} />
      ) : (
        <div className="detail-surface-empty">
          <StorageRoundedIcon />
          <h3>Select a Media or Position</h3>
        </div>
      )}
    </DetailSurface>
  );
};

const MediaContent = ({ media }: { media: Media }) => {
  const profile = media.profile?.kind;
  const serial = profile?.oneofKind === "tape" ? profile.tape.serialNumber : profile?.oneofKind === "volume" ? profile.volume.serialNumber : "";
  const volumeType = profile?.oneofKind === "volume" ? (profile.volume.type === VolumeType.HM_SMR ? "HM-SMR" : "HDD") : "";
  return (
    <div className="detail-surface-content">
      <section className="file-detail-facts-section">
        <dl className="file-detail-summary">
          <dt>Type</dt>
          <dd>{media.kind === MediaKind.TAPE ? "Tape" : "Volume"}</dd>
          <dt>Capacity</dt>
          <dd>{formatFilesize(media.capacityBytes)}</dd>
          <dt>Written</dt>
          <dd>{formatFilesize(media.writtenBytes)}</dd>
          {media.filesystemAvailableBytes !== undefined && (
            <>
              <dt>Available</dt>
              <dd>{formatFilesize(media.filesystemAvailableBytes)}</dd>
            </>
          )}
        </dl>
      </section>
      <details className="technical-details file-detail-technical">
        <summary>Technical details</summary>
        <dl className="file-detail-summary">
          <dt>Identity</dt>
          <dd className="file-detail-path">{media.identity || "—"}</dd>
          {serial && (
            <>
              <dt>Serial number</dt>
              <dd>{serial}</dd>
            </>
          )}
          {volumeType && (
            <>
              <dt>Volume type</dt>
              <dd>{volumeType}</dd>
            </>
          )}
          <dt>Access</dt>
          <dd>
            {accessLabel(media.capabilities?.read)} read · {accessLabel(media.capabilities?.write)} write
          </dd>
          <dt>Created</dt>
          <dd>{contentTime(media.createdAtNs || undefined)}</dd>
        </dl>
      </details>
    </div>
  );
};

const PositionContent = ({ position, media }: { position: Position; media?: Media }) => {
  const health = position.health ?? PositionHealth.UNKNOWN;
  const normalCopy = health === PositionHealth.UNSPECIFIED || health === PositionHealth.UNKNOWN || health === PositionHealth.HEALTHY;
  const mediaLabel = media ? `${media.name || media.identity}${media.kind === MediaKind.TAPE ? " · Tape" : " · Volume"}` : "—";
  return (
    <div className="detail-surface-content">
      <section className="file-detail-facts-section">
        <dl className="file-detail-summary">
          <dt>Media</dt>
          <dd>{mediaLabel}</dd>
          <dt>Path</dt>
          <dd className="file-detail-path">{position.path}</dd>
          <dt>Size</dt>
          <dd>{formatFilesize(position.sizeBytes)}</dd>
          <dt>Modified</dt>
          <dd>{contentTime(position.mtimeNs || undefined)}</dd>
          <dt>Written</dt>
          <dd>{contentTime(position.writtenAtNs || undefined)}</dd>
        </dl>
        {(position.checkedAtNs || position.healthJobId) && (
          <p className="product-muted">
            {position.checkedAtNs ? `Checked ${contentTime(position.checkedAtNs)}` : "Not checked"}
            {position.healthJobId && (
              <>
                {" "}
                · <a href={`/jobs/${position.healthJobId}`}>Check results</a>
              </>
            )}
          </p>
        )}
        {!normalCopy && (
          <Alert severity="warning">
            Not used by normal Restore.
            {health === PositionHealth.DAMAGED || health === PositionHealth.UNREADABLE ? " Advanced recovery can attempt this copy." : ""}
          </Alert>
        )}
      </section>
      {position.signature.length > 0 ? (
        <ContentCopies signature={position.signature} />
      ) : (
        <section className="file-detail-copies">
          <p className="product-muted">This Position records no Signature, so its copies cannot be matched.</p>
        </section>
      )}
      <details className="technical-details file-detail-technical">
        <summary>Technical details</summary>
        <dl className="file-detail-summary">
          <dt>Permission</dt>
          <dd>{(position.mode & 0o777n).toString(8).padStart(3, "0")}</dd>
          <dt>SHA-256</dt>
          <dd className="file-detail-hash">{contentHex(position.sha256) || "Not checked"}</dd>
          <dt>Signature</dt>
          <dd className="file-detail-hash">{contentHex(position.signature) || "Not checked"}</dd>
        </dl>
      </details>
    </div>
  );
};
