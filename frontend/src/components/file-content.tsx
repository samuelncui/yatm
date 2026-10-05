import { useSelectionActions } from "@/state/react";
import { Feedback } from "@/components/feedback";
import { toast } from "react-toastify";
import { useActionDialog } from "./action-dialog";
import { type ReactNode, useCallback, useEffect, useId, useRef, useState } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  accordionClasses,
  accordionDetailsClasses,
  accordionSummaryClasses,
  Alert,
  Button,
  styled,
} from "@mui/material";
import { useNavigate } from "react-router";
import ExpandMoreRoundedIcon from "@mui/icons-material/ExpandMoreRounded";
import StorageRoundedIcon from "@mui/icons-material/StorageRounded";
import FolderRoundedIcon from "@mui/icons-material/FolderRounded";
import { filesCli, mediaCli } from "@/api";
import {
  EntryKind,
  FileOperationKind,
  MediaAccess,
  MediaKind,
  PositionHealth,
  type FilesEntry,
  type Media,
  type Position,
  type FilesDetail,
  type FileVersion,
} from "@/entity";
import { contentHex, contentTime } from "@/components/content-status";
import { errorMessage, formatFilesize } from "@/tools";
import { DetailSurface } from "./detail-surface";
import { ActionRow } from "./action-row";

const ArchivedCopy = styled(Accordion)(({ theme }) => ({
  margin: "7px 0",
  background: theme.palette.background.paper,
  border: `1px solid ${theme.palette.divider}`,
  borderRadius: 8,
  boxShadow: "none",
  [`&.${accordionClasses.expanded}`]: { margin: "7px 0" },
  "&:before": { display: "none" },
  [`& .${accordionSummaryClasses.root}`]: { minHeight: 0, padding: 10 },
  [`& .${accordionSummaryClasses.content}, & .${accordionSummaryClasses.content}.${accordionSummaryClasses.expanded}`]: { margin: 0 },
  [`& .${accordionSummaryClasses.expandIconWrapper}`]: { color: theme.palette.text.secondary, alignSelf: "flex-start", marginTop: 0 },
  [`& .${accordionDetailsClasses.root}`]: { padding: 10, borderTop: `1px solid ${theme.palette.divider}`, "& > a": { display: "inline-block", marginTop: 12 } },
}));
import { PreviewMedia } from "./file-preview";
import { SavedVersionChoice } from "./saved-version-choice";
import { RelocateOriginalDialog } from "./relocate-original";
import { OriginalLocationLink } from "./original-location-link";
import { InlineFileMetadata } from "./file-inline-metadata";
import { allowsFileOperation, filesEntryData, filesEntryLibraryID } from "./files-browser";
import { archiveEntryForDetail, restoreVersionEntry, selectionAddMessage, type SelectionKind } from "./selection-waitlist-state";
import { useContentPreview } from "./use-content-preview";
import { compareVersionDates, useFileVersionPages, useLatestSavedVersion, versionArchiveTime as archiveTime } from "./use-file-versions";

const sortVersions = (versions: FileVersion[]) => [...versions].sort(compareVersionDates);

export const FileContent = ({
  detail,
  onRefresh,
  title,
  titleId,
  onClose,
  busy,
  notice,
  selectedView,
  onViewChange,
}: {
  detail: FilesDetail;
  onRefresh: () => Promise<void>;
  title?: string;
  titleId?: string;
  onClose?: () => void;
  busy?: boolean;
  notice?: ReactNode;
  selectedView?: "overview" | "versions";
  onViewChange?: (view: "overview" | "versions") => void;
}) => {
  const { add: addSelectionEntries } = useSelectionActions();
  const navigate = useNavigate();
  const panelID = useId();
  const [localView, setLocalView] = useState<"overview" | "versions">("overview");
  const view = selectedView ?? localView;
  const [versionsOpened, setVersionsOpened] = useState(view === "versions");
  const [selectedVersion, setSelectedVersion] = useState<FileVersion>();
  const [relocating, setRelocating] = useState(false);
  const [selectionFeedback, setSelectionFeedback] = useState<{ kind: SelectionKind; message: string; added: boolean; error?: boolean }>();
  const entry = detail.entry;
  if (!entry) return null;
  const fileID = filesEntryLibraryID(entry);
  const regular = entry.kind === EntryKind.FILE;
  const metadata = { ...filesEntryData(entry), tags: detail.organization?.tags ?? [], note: detail.organization?.note ?? "" };
  const name = title || entry.name || "File details";
  const status = regular ? filesEntryData(entry).status : undefined;
  const archiveEligible =
    allowsFileOperation(filesEntryData(entry), FileOperationKind.ARCHIVE) &&
    (!regular || entry.reference?.target.oneofKind === "location" || !!detail.original);
  const extension = name.includes(".") ? name.split(".").at(-1)?.slice(0, 4).toUpperCase() : "";
  const addToSelection = (kind: SelectionKind, version?: FileVersion) => {
    try {
      let addition;
      if (kind === "archive") addition = archiveEntryForDetail(detail);
      else {
        if (!version) throw new Error("Select a saved version before adding it to the Restore list.");
        addition = restoreVersionEntry(detail, version);
      }
      const result = addSelectionEntries(kind, [addition]);
      setSelectionFeedback({ kind, message: selectionAddMessage(kind, result), added: result.added > 0 });
    } catch (failure) {
      setSelectionFeedback({
        kind,
        message: errorMessage(failure, `Could not add this item to the ${kind === "archive" ? "Archive" : "Restore"} list`),
        added: false,
        error: true,
      });
    }
  };
  const selectionNotice = selectionFeedback && (
    <Feedback
      severity={selectionFeedback.error ? "error" : selectionFeedback.added ? "success" : "info"}
      action={
        !selectionFeedback.error && (
          <Button color="inherit" size="small" onClick={() => navigate(`/${selectionFeedback.kind}`)}>
            View list
          </Button>
        )
      }
    >
      {selectionFeedback.message}
    </Feedback>
  );
  const preview =
    regular &&
    (view === "overview" ? (
      detail.contentSignature.length > 0 ? (
        <ContentPreview signature={detail.contentSignature} refresh={detail} label="Preview · current original" reserveSpace />
      ) : !detail.contentReference ? (
        <SavedPreview detail={detail} reserveSpace />
      ) : (
        <ContentPreview signature={new Uint8Array()} label="Preview · current original" reserveSpace />
      )
    ) : (
      <ContentPreview
        signature={selectedVersion?.signature ?? new Uint8Array()}
        refresh={selectedVersion}
        label="Preview · selected saved version"
        reserveSpace
      />
    ));
  return (
    <DetailSurface
      title={name}
      titleId={titleId}
      icon={entry.kind === EntryKind.DIRECTORY ? <FolderRoundedIcon fontSize="small" /> : extension || "FILE"}
      status={status}
      busy={busy}
      notice={
        <>
          {notice}
          {selectionNotice}
        </>
      }
      onClose={onClose}
      idPrefix={panelID}
      tabs={
        regular
          ? [
              { value: "overview", label: "Overview" },
              { value: "versions", label: "Saved versions" },
            ]
          : undefined
      }
      selectedTab={regular ? view : undefined}
      onTabChange={(value) => {
        const next = value as "overview" | "versions";
        if (onViewChange) onViewChange(next);
        else setLocalView(next);
        if (value === "versions") setVersionsOpened(true);
      }}
      preview={preview}
    >
      <div
        role={regular ? "tabpanel" : undefined}
        id={regular ? `${panelID}-panel-overview` : undefined}
        aria-labelledby={regular ? `${panelID}-tab-overview` : undefined}
        hidden={regular && view !== "overview"}
      >
        <Overview detail={detail} />
        <InlineFileMetadata
          reference={entry.reference!}
          tags={detail.organization?.tags}
          note={detail.organization?.note ?? ""}
          editable={allowsFileOperation(metadata, FileOperationKind.UPDATE_METADATA)}
          onRefresh={onRefresh}
        />
        <OverviewTechnical detail={detail} />
        {(archiveEligible || (regular && fileID !== undefined)) && (
          <section className="file-detail-actions" aria-label="File actions">
            <ActionRow>
              {archiveEligible && (
                <Button variant="outlined" onClick={() => addToSelection("archive")}>
                  Add to Archive list
                </Button>
              )}
              {regular && fileID !== undefined && (
                <Button variant="outlined" disabled={busy} onClick={() => setRelocating(true)}>
                  Locate original
                </Button>
              )}
            </ActionRow>
          </section>
        )}
        {relocating && regular && fileID !== undefined && (
          <RelocateOriginalDialog key={String(fileID)} file={{ id: fileID, name: entry.name }} onClose={() => setRelocating(false)} onSaved={onRefresh} />
        )}
      </div>
      {regular && versionsOpened && (
        <div hidden={view !== "versions"}>
          <Versions
            key={String(fileID ?? "")}
            fileID={fileID}
            panelID={panelID}
            refresh={detail}
            onRefresh={onRefresh}
            onSelectedVersion={setSelectedVersion}
            onAddToRestore={(version) => addToSelection("restore", version)}
          />
        </div>
      )}
    </DetailSurface>
  );
};

const Overview = ({ detail }: { detail: FilesDetail }) => {
  const entry = detail.entry!;
  const original = detail.original;
  const target = original?.reference?.target;
  const live = target?.oneofKind === "location" ? target.location : undefined;
  const regular = entry.kind === EntryKind.FILE;
  return (
    <section className="file-detail-facts-section">
      <dl className="file-detail-summary">
        {original && (
          <>
            <dt>Location</dt>
            <dd>
              {live ? (
                <OriginalLocationLink location={{ id: live.locationId, name: original.sourceName, rootPath: "" }} path={original.path} />
              ) : (
                `${original.sourceName}: ${original.path}`
              )}
            </dd>
          </>
        )}
        {!original && regular && filesEntryLibraryID(entry) !== undefined && (
          <>
            <dt>Location</dt>
            <dd>Saved versions only</dd>
          </>
        )}
        {!regular && (
          <>
            <dt>Type</dt>
            <dd>{entry.kind === EntryKind.DIRECTORY ? "Folder" : entry.kind === EntryKind.LINK ? "Symbolic link" : "Special file"}</dd>
          </>
        )}
        {regular && (
          <>
            <dt>Size</dt>
            <dd>{entry.sizeBytes === undefined ? "Unknown" : formatFilesize(entry.sizeBytes)}</dd>
          </>
        )}
        <dt>Modified</dt>
        <dd>{contentTime(entry.mtimeNs, "—")}</dd>
      </dl>
    </section>
  );
};

const OverviewTechnical = ({ detail }: { detail: FilesDetail }) => {
  const target = detail.original?.reference?.target;
  const live = target?.oneofKind === "location" ? target.location : undefined;
  return (
    <details className="technical-details file-detail-technical">
      <summary>Technical details</summary>
      <dl className="file-detail-summary">
        <dt>Permission</dt>
        <dd>{live?.facts ? (live.facts.mode & 0o777).toString(8).padStart(3, "0") : "Not recorded"}</dd>
        <dt>Signature</dt>
        <dd className="file-detail-hash">{contentHex(detail.contentSignature) || "Not recorded"}</dd>
        <dt>SHA-256</dt>
        <dd className="file-detail-hash">Not recorded for the current original</dd>
      </dl>
    </details>
  );
};

// A File whose current original is not available still has saved content with a Preview. The
// region names the version it shows, so archived assets are never read as the original's.
const SavedPreview = ({ detail, reserveSpace = false }: { detail: FilesDetail; reserveSpace?: boolean }) => {
  const fileID = detail.entry ? filesEntryLibraryID(detail.entry) : undefined;
  const version = useLatestSavedVersion(fileID, detail);
  if (version === null) return null;
  if (!version) return reserveSpace ? <ContentPreview signature={new Uint8Array()} label="Preview · saved version" reserveSpace /> : null;
  return <ContentPreview signature={version.signature} refresh={detail} label="Preview of the latest saved version" reserveSpace={reserveSpace} />;
};

export const ContentPreview = ({
  signature,
  refresh,
  label,
  reserveSpace = false,
}: {
  signature: Uint8Array;
  refresh?: unknown;
  label?: string;
  reserveSpace?: boolean;
}) => {
  const { key, assets, error, reload } = useContentPreview(signature, refresh);
  return (
    <>
      {error && (
        <Feedback severity="info" action={<Button onClick={reload}>Retry Preview</Button>}>
          {error}
        </Feedback>
      )}
      {(assets.length > 0 || reserveSpace) && (
        <div className="content-preview">
          <div className="file-detail-preview">
            {assets.length > 0 ? (
              <PreviewMedia key={`${key}:${JSON.stringify(assets)}`} assets={assets} />
            ) : (
              <span className="file-detail-preview-empty">Preview not available</span>
            )}
          </div>
          {label && <p className="file-detail-preview-label">{label}</p>}
        </div>
      )}
    </>
  );
};

const Versions = ({
  fileID,
  panelID,
  refresh,
  onRefresh,
  onSelectedVersion,
  onAddToRestore,
}: {
  fileID?: bigint;
  panelID: string;
  refresh: FilesDetail;
  onRefresh: () => Promise<void>;
  onSelectedVersion: (version?: FileVersion) => void;
  onAddToRestore: (version: FileVersion) => void;
}) => {
  const { ask, dialog } = useActionDialog();
  const { versions, more, loading, failure, load, remove } = useFileVersionPages(fileID, refresh, true);
  const [selectedID, setSelectedID] = useState<bigint>();
  const error = failure === undefined ? "" : errorMessage(failure, "Could not load saved versions");
  const displayed = sortVersions(versions);
  const selected = versions.find((version) => version.id === selectedID) ?? displayed[0];
  useEffect(() => {
    onSelectedVersion(selected);
  }, [onSelectedVersion, selected]);
  return (
    <div role="tabpanel" id={`${panelID}-panel-versions`} aria-labelledby={`${panelID}-tab-versions`} aria-label="Saved versions">
      {error && (
        <Feedback severity="error" action={<Button onClick={() => void load()}>Retry</Button>}>
          {error}
        </Feedback>
      )}
      {loading && !versions.length && <p role="status">Loading saved versions…</p>}
      {!loading && !error && !versions.length && <p className="product-muted">No saved versions are available to add to the Restore list.</p>}
      <p className="version-list-label">Last archived</p>
      <div className="version-list" aria-label="Saved content versions">
        {displayed.map((version) => {
          const date = archiveTime(version);
          return (
            <SavedVersionChoice
              key={String(version.id)}
              aria-label={
                date !== undefined
                  ? `Last archived ${contentTime(date)} · ${formatFilesize(version.sizeBytes)}`
                  : `Archive date not recorded · ${formatFilesize(version.sizeBytes)}`
              }
              selected={selected?.id === version.id}
              onClick={() => setSelectedID(version.id)}
            >
              <strong>{date !== undefined ? contentTime(date) : "Date not recorded"}</strong>
              <small>{formatFilesize(version.sizeBytes)}</small>
            </SavedVersionChoice>
          );
        })}
      </div>
      {more && (
        <Button disabled={loading} onClick={() => void load(versions.at(-1)?.id)}>
          Load more saved versions
        </Button>
      )}
      {selected && (
        <>
          <SavedVersion key={String(selected.id)} version={selected} />
          <ActionRow className="file-detail-version-actions">
            <Button variant="outlined" onClick={() => onAddToRestore(selected)}>
              Add to Restore list
            </Button>
            <Button
              color="error"
              onClick={() =>
                ask({
                  title: "Remove saved version?",
                  confirmLabel: "Remove version",
                  danger: true,
                  children: (
                    <p>
                      The saved version record is removed. Archive copies and other versions are kept. A later Scan or Archive may establish this version again.
                    </p>
                  ),
                  onConfirm: async () => {
                    await filesCli.removeVersion({ fileId: selected.fileId, versionId: selected.id, dryrun: false }).response;
                    remove(selected.id);
                    setSelectedID(undefined);
                    try {
                      await onRefresh();
                    } catch (failure) {
                      toast.error(`Version removed, but refresh failed: ${errorMessage(failure, "Refresh failed")}`);
                    }
                  },
                })
              }
            >
              Remove version
            </Button>
          </ActionRow>
        </>
      )}
      {dialog}
    </div>
  );
};

const SavedVersion = ({ version }: { version: FileVersion }) => {
  return (
    <section className="selected-version" aria-label="Selected saved version">
      <p className="selected-version-summary">{version.mtimeNs ? `Modified ${contentTime(version.mtimeNs)}` : "Modification time not recorded"}</p>
      <ContentCopies signature={version.signature} versionID={version.id} fileID={version.fileId} />
      <details className="technical-details file-detail-technical">
        <summary>Technical details</summary>
        <dl className="file-detail-summary">
          <dt>Permission</dt>
          <dd>{(version.mode & 0o777).toString(8).padStart(3, "0")}</dd>
          <dt>SHA-256</dt>
          <dd className="file-detail-hash">{contentHex(version.sha256) || "Not checked"}</dd>
          <dt>Signature</dt>
          <dd className="file-detail-hash">{contentHex(version.signature) || "Not checked"}</dd>
        </dl>
      </details>
    </section>
  );
};

type CopyProps = { signature: Uint8Array; versionID?: bigint; fileID?: bigint };

export const ContentCopies = (props: CopyProps) => (
  <CopyList key={`${contentHex(props.signature)}:${props.versionID ?? ""}:${props.fileID ?? ""}`} {...props} />
);

const CopyList = ({ signature, versionID, fileID }: CopyProps) => {
  const [positions, setPositions] = useState<Position[]>([]);
  const [media, setMedia] = useState<Map<bigint, Media>>(new Map());
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [duplicates, setDuplicates] = useState(false);
  const request = useRef(0);
  const loadedPages = useRef(1);
  const load = useCallback(
    async (afterID = 0n) => {
      const sequence = ++request.current;
      setLoading(true);
      try {
        let page = await filesCli.listCopies({ signature, afterId: afterID, limit: 20 }).response;
        if (sequence !== request.current) return;
        const positions = [...page.positions];
        let pages = 1;
        while (!afterID && pages < loadedPages.current && page.hasMore) {
          page = await filesCli.listCopies({ signature, afterId: page.positions.at(-1)!.id, limit: 20 }).response;
          if (sequence !== request.current) return;
          positions.push(...page.positions);
          pages++;
        }
        const ids = Array.from(new Set(positions.map((position) => position.mediaId)));
        const values = ids.length ? (await mediaCli.list({ param: { oneofKind: "ids", ids: { ids } } }).response).media : [];
        if (sequence !== request.current) return;
        loadedPages.current = afterID ? loadedPages.current + 1 : pages;
        setPositions((current) => (afterID ? [...current, ...positions] : positions));
        setMedia((current) => new Map([...current, ...values.map((value) => [value.id, value] as const)]));
        setMore(page.hasMore);
        setError("");
      } catch (error) {
        if (sequence === request.current) setError(errorMessage(error, "Could not load archived copies"));
      } finally {
        if (sequence === request.current) setLoading(false);
      }
    },
    [signature],
  );
  useEffect(() => {
    const pending = request;
    void load();
    return () => {
      pending.current++;
    };
  }, [load]);
  return (
    <section className="file-detail-copies" aria-label="Archived copies">
      <h3>Archived copies</h3>
      {error && (
        <Feedback severity="error" action={<Button onClick={() => void load()}>Try again</Button>}>
          {error}
        </Feedback>
      )}
      {loading && positions.length === 0 && (
        <p className="product-muted" role="status">
          Loading archived copies…
        </p>
      )}
      {!loading && !error && positions.length === 0 && (
        <Alert severity="warning">{versionID ? "This version cannot currently be restored: no copies available." : "No archived copies recorded."}</Alert>
      )}
      {positions.map((position) => {
        const value = media.get(position.mediaId);
        const health = position.health ?? PositionHealth.UNKNOWN;
        const normalCopy = health === PositionHealth.UNKNOWN || health === PositionHealth.HEALTHY;
        const online = value?.kind === MediaKind.VOLUME && value.mounted === true && value.capabilities?.read === MediaAccess.CONCURRENT_RANDOM;
        return (
          <ArchivedCopy className="copy-card" key={String(position.id)} disableGutters elevation={0}>
            <AccordionSummary expandIcon={<ExpandMoreRoundedIcon />} aria-controls={`copy-${position.id}-details`} id={`copy-${position.id}-summary`}>
              <span className="copy-heading">
                <StorageRoundedIcon fontSize="small" />
                <span className="copy-heading-text">
                  <strong>{value?.name || value?.identity || "Archive storage"}</strong>
                  <small>
                    {value?.kind === MediaKind.TAPE ? "Tape" : "Volume"} · {online ? "Available" : "Not connected"} · {copyHealthLabel(health)}
                  </small>
                </span>
              </span>
            </AccordionSummary>
            <AccordionDetails>
              <dl className="file-detail-summary">
                <dt>Storage path</dt>
                <dd className="file-detail-path">{position.path || "Not recorded"}</dd>
                <dt>Written</dt>
                <dd>{contentTime(position.writtenAtNs || undefined)}</dd>
                <dt>Last check</dt>
                <dd>{position.checkedAtNs ? contentTime(position.checkedAtNs) : "Not checked"}</dd>
              </dl>
              {position.healthJobId ? <a href={`/jobs/${position.healthJobId}`}>Check results</a> : null}
              {!normalCopy && (
                <Feedback severity="warning">
                  Not used by normal Restore.
                  {health === PositionHealth.DAMAGED || health === PositionHealth.UNREADABLE ? " Advanced recovery can attempt this copy." : ""}
                </Feedback>
              )}
              {!online && <p className="product-muted">{value?.kind === MediaKind.TAPE ? "Load this tape to restore" : "Connect this volume to restore"}</p>}
              {online && normalCopy && <p className="product-muted">Available for Restore</p>}
            </AccordionDetails>
          </ArchivedCopy>
        );
      })}
      {more && (
        <Button disabled={loading} onClick={() => void load(positions.at(-1)?.id)}>
          Load more copies
        </Button>
      )}
      <Button size="small" onClick={() => setDuplicates(!duplicates)} aria-expanded={duplicates}>
        {duplicates ? "Hide matching files" : "Find matching files"}
      </Button>
      {duplicates && <ContentDuplicates signature={signature} fileID={fileID} />}
    </section>
  );
};

export const copyHealthLabel = (health: PositionHealth) =>
  ({
    [PositionHealth.UNSPECIFIED]: "Not checked",
    [PositionHealth.UNKNOWN]: "Not checked",
    [PositionHealth.HEALTHY]: "Last check passed",
    [PositionHealth.DAMAGED]: "Damaged",
    [PositionHealth.MISSING]: "Missing",
    [PositionHealth.UNREADABLE]: "Unreadable",
  })[health];

const ContentDuplicates = ({ signature, fileID }: { signature: Uint8Array; fileID?: bigint }) => {
  const [files, setFiles] = useState<FilesEntry[]>([]);
  const [cursor, setCursor] = useState(0n);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const request = useRef(0);
  const load = useCallback(
    async (afterFileId = 0n) => {
      const sequence = ++request.current;
      setLoading(true);
      try {
        const reply = await filesCli.listDuplicates({ signature, afterFileId, limit: 20 }).response;
        if (sequence !== request.current) return;
        setFiles((current) => (afterFileId ? [...current, ...reply.entries] : reply.entries));
        setCursor(filesEntryLibraryID(reply.entries.at(-1)) ?? afterFileId);
        setMore(reply.hasMore);
        setError("");
      } catch (error) {
        if (sequence === request.current) setError(errorMessage(error, "Could not find matching files"));
      } finally {
        if (sequence === request.current) setLoading(false);
      }
    },
    [signature],
  );
  useEffect(() => {
    const pending = request;
    void load();
    return () => {
      pending.current++;
    };
  }, [load]);
  const matches = files.filter((value) => filesEntryLibraryID(value) !== fileID);
  return (
    <div className="matching-files">
      {loading && !files.length && (
        <p className="product-muted" role="status">
          Finding matching files…
        </p>
      )}
      {error && (
        <Feedback severity="error" action={<Button onClick={() => void load()}>Try again</Button>}>
          {error}
        </Feedback>
      )}
      {matches.map((file) => (
        <a key={String(filesEntryLibraryID(file))} href={`/file?file=${filesEntryLibraryID(file)}`}>
          <strong>{file.name}</strong>
          <small>{file.path || "Open in Library"}</small>
        </a>
      ))}
      {!loading && !error && matches.length === 0 && <p className="product-muted">No other matching files on this page.</p>}
      {more && (
        <Button disabled={loading} onClick={() => void load(cursor)}>
          Load more matching files
        </Button>
      )}
    </div>
  );
};
