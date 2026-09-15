import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { Alert, Button, Chip, LinearProgress, Tab, Tabs, Tooltip } from "@mui/material";
import HistoryRoundedIcon from "@mui/icons-material/HistoryRounded";
import StorageRoundedIcon from "@mui/icons-material/StorageRounded";
import { cli, fileBase, fileCatalogCli, locationCli } from "@/api";
import { ContentCoverage, FileContentSummary, FileScope, MediaAccess, MediaKind, OnlineBinding, OriginalAvailability, PositionHealth } from "@/entity";
import type { File, FileLocation, FileStateReply, FileVersion, LocationEntry, Media, Position, PreviewManifest } from "@/entity";
import { PreviewMedia } from "@/components/file-preview";
import { RelocateOriginalDialog } from "@/components/relocate-original";
import { LibraryArchiveButton } from "@/components/library-archive";
import { backupColors, backupSummary, contentHex, contentTime } from "@/components/content-status";
import { errorMessage, formatFilesize } from "@/tools";
import { OriginalLocationLink } from "@/components/original-location-link";

export const FileContent = ({ file, currentPreview, organization }: { file: File; currentPreview?: PreviewManifest; organization?: ReactNode }) => {
  const [state, setState] = useState<FileStateReply>();
  const [observed, setObserved] = useState<LocationEntry>();
  const [accessError, setAccessError] = useState("");
  const [versions, setVersions] = useState<FileVersion[]>([]);
  const [more, setMore] = useState(false);
  const [view, setView] = useState<"original" | "versions">("original");
  const [selected, setSelected] = useState<FileVersion>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [relocating, setRelocating] = useState(false);
  const request = useRef(0);
  const loadedPages = useRef(1);
  const load = useCallback(
    async (afterID = 0n) => {
      const sequence = ++request.current;
      setLoading(true);
      setAccessError("");
      try {
        const [next, firstPage] = await Promise.all([
          fileCatalogCli.getState({ fileId: file.id }).response,
          fileCatalogCli.listVersions({ fileId: file.id, afterId: afterID, limit: 20 }).response,
        ]);
        if (sequence !== request.current) return;
        let page = firstPage;
        const versions = [...page.versions];
        let pages = 1;
        while (!afterID && pages < loadedPages.current && page.hasMore) {
          page = await fileCatalogCli.listVersions({ fileId: file.id, afterId: page.versions.at(-1)!.id, limit: 20 }).response;
          if (sequence !== request.current) return;
          versions.push(...page.versions);
          pages++;
        }
        loadedPages.current = afterID ? loadedPages.current + 1 : pages;
        setState(next);
        setVersions((current) => (afterID ? [...current, ...versions] : versions));
        setMore(page.hasMore);
        setError("");
        if (!afterID) setSelected((current) => versions.find((version) => version.id === current?.id) ?? next.latestVersion);
        setAccessError("");
        if (next.location && next.original) {
          try {
            const access = await locationCli.getEntry({ locationId: next.location.id, path: next.original.path }).response;
            if (sequence === request.current) setObserved(access);
          } catch (error) {
            if (sequence === request.current) {
              setObserved(undefined);
              setAccessError(errorMessage(error, "Could not check the original's availability"));
            }
          }
        }
      } catch (error) {
        if (sequence === request.current) {
          setObserved(undefined);
          setError(errorMessage(error, "Could not load backup information"));
        }
      } finally {
        if (sequence === request.current) setLoading(false);
      }
    },
    [file.id],
  );
  useEffect(() => {
    const pending = request;
    void load();
    return () => {
      pending.current++;
    };
  }, [load, file]);

  if (!state)
    return (
      <section aria-label="Backup information">
        {loading && <LinearProgress />}
        {error && (
          <Alert severity="error">
            {error}
            <Button onClick={() => void load()}>Try again</Button>
          </Alert>
        )}
        {organization}
      </section>
    );
  const original = state.original;
  const location = state.location;
  const reference = observed?.reference;
  const facts = reference?.facts;
  const available =
    location?.binding !== OnlineBinding.UNCONFIRMED &&
    !!location?.bindingToken &&
    reference?.bindingToken === location.bindingToken &&
    reference.locationId === original?.locationId &&
    reference.path === original.path &&
    observed?.file?.id === file.id &&
    !!facts &&
    (facts.mode & 0x8f280000) === 0;
  const unchanged =
    available &&
    original?.observedBindingToken === reference?.bindingToken &&
    facts?.size === original.size &&
    facts.mode === original.mode &&
    facts.mtimeNs === original.mtimeNs &&
    !!observed?.original &&
    contentHex(observed.original.signature) === contentHex(original.signature);
  const signatureKnown = unchanged && state.coverage !== ContentCoverage.CONTENT_UNKNOWN;
  const summary = backupSummary(
    FileContentSummary.create({
      ...state.summary,
      originalAvailability: accessError ? OriginalAvailability.ORIGINAL_UNAVAILABLE : state.summary?.originalAvailability,
      currentObservationValid: !!state.summary?.currentObservationValid && (!original || unchanged),
    }),
  );
  const choices = [...versions];
  if (state.latestVersion && !choices.some((value) => value.id === state.latestVersion!.id)) choices.push(state.latestVersion);
  choices.sort((a, b) => (a.id > b.id ? -1 : a.id < b.id ? 1 : 0));

  return (
    <>
      <section className="backup-summary" aria-label="Backup status">
        <Tooltip title={summary.description} placement="left" arrow>
          <span className="backup-summary-label" tabIndex={0}>
            <span className="backup-dot" style={{ backgroundColor: backupColors[summary.tone] }} />
            {summary.title}
          </span>
        </Tooltip>
        <div className="product-actions">
          <Button size="small" onClick={() => setRelocating(true)}>
            Locate original
          </Button>
          {original && (
            <LibraryArchiveButton
              fileIDs={[file.id]}
              scope={FileScope.ALL}
              label={signatureKnown && state.coverage === ContentCoverage.ARCHIVED_CONTENT ? "Back up again" : "Back up file"}
              disabled={!available}
            />
          )}
        </div>
      </section>
      {relocating && <RelocateOriginalDialog key={String(file.id)} file={file} onClose={() => setRelocating(false)} onSaved={load} />}
      {loading && <LinearProgress aria-label="Refreshing content information" />}
      <Tabs value={view} onChange={(_, value) => setView(value)} aria-label="File information" className="content-tabs" variant="fullWidth">
        <Tab value="original" label="Overview" id={`overview-tab-${file.id}`} aria-controls={`overview-${file.id}`} />
        <Tab value="versions" label="Saved versions" id={`versions-tab-${file.id}`} aria-controls={`versions-${file.id}`} />
      </Tabs>
      {error && (
        <Alert severity="error">
          {error}
          <Button onClick={() => void load()}>Try again</Button>
        </Alert>
      )}
      {view === "original" ? (
        <div role="tabpanel" id={`overview-${file.id}`} aria-labelledby={`overview-tab-${file.id}`}>
          {unchanged &&
            original &&
            currentPreview &&
            original.sha256.length > 0 &&
            original.size === file.size &&
            contentHex(original.sha256) === contentHex(file.hash) && <ContentPreview fileID={file.id} manifest={currentPreview} />}
          {original && location && (
            <section className="content-section">
              <OriginalLocationLink location={location} path={original.path} />
              <div className="product-actions">
                <Chip
                  size="small"
                  variant="outlined"
                  color={available ? "success" : "default"}
                  label={available ? (unchanged ? "Available" : "Changed") : observed ? "Location changed · Refresh" : accessError ? "Unavailable" : "Checking"}
                />
                <span className="product-muted">
                  {formatFilesize(facts?.size ?? original.size)} · {contentTime((facts?.mtimeNs ?? original.mtimeNs) / 1000000n)}
                </span>
              </div>
              {accessError && (
                <Alert severity="warning">
                  {accessError}
                  <Button onClick={() => void load()}>Check again</Button>
                </Alert>
              )}
              {available && observed?.original && <TechnicalDetails facts={observed.original} />}
            </section>
          )}
          {organization}
          {signatureKnown && original?.signature.length ? (
            <ContentCopies key={contentHex(original.signature)} signature={original.signature} fileID={file.id} />
          ) : original ? (
            <section className="content-section">
              <h3>Archived copies</h3>
              <p className="product-muted">Scan this file to find archived copies.</p>
            </section>
          ) : null}
        </div>
      ) : (
        <div role="tabpanel" id={`versions-${file.id}`} aria-labelledby={`versions-tab-${file.id}`}>
          <section className="content-section">
            {loading && <LinearProgress />}
            {!loading && choices.length === 0 && (
              <div className="product-empty">
                <HistoryRoundedIcon />
                <strong>No saved versions yet</strong>
                <p>Back up this file to save its first version.</p>
              </div>
            )}
            <div className="version-list" aria-label="Saved content versions">
              {choices.map((version) => (
                <button key={String(version.id)} className="version-choice" aria-pressed={selected?.id === version.id} onClick={() => setSelected(version)}>
                  <span>
                    <strong>{contentTime(version.firstArchivedAtMs, "Archive date not recorded")}</strong>
                    <small>
                      {formatFilesize(version.size)} · modified {contentTime(version.mtimeNs / 1000000n)}
                    </small>
                  </span>
                  {version.id === state.latestVersion?.id && <span className="version-latest">Latest</span>}
                </button>
              ))}
            </div>
            {more && (
              <Button disabled={loading} onClick={() => void load(versions.at(-1)?.id)} size="small">
                Load more saved versions
              </Button>
            )}
          </section>
          {selected && <SavedVersion key={String(selected.id)} fileID={file.id} version={selected} />}
        </div>
      )}
    </>
  );
};

const ContentPreview = ({ fileID, versionID, manifest }: { fileID: bigint; versionID?: bigint; manifest: PreviewManifest }) => (
  <div className="file-detail-preview content-preview">
    <PreviewMedia
      key={`${versionID ?? "current"}:${contentHex(manifest.fileSignature)}:${manifest.generator}:${contentHex(manifest.settingsJson)}`}
      fileID={fileID}
      versionID={versionID}
      manifest={manifest}
    />
  </div>
);

const TechnicalDetails = ({ facts }: { facts: FileLocation | FileVersion }) => (
  <details className="technical-details">
    <summary>Technical details</summary>
    <dl className="file-detail-summary">
      <dt>Permission</dt>
      <dd>{(facts.mode & 0o777).toString(8).padStart(3, "0")}</dd>
      <dt>SHA-256</dt>
      <dd className="file-detail-hash">{contentHex(facts.sha256) || "Not checked"}</dd>
      <dt>Signature</dt>
      <dd className="file-detail-hash">{contentHex(facts.signature) || "Not checked"}</dd>
    </dl>
  </details>
);

const SavedVersion = ({ fileID, version }: { fileID: bigint; version: FileVersion }) => {
  const [preview, setPreview] = useState<PreviewManifest>();
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void fileCatalogCli
      .getVersion({ id: version.id })
      .response.then((reply) => {
        if (!active) return;
        setPreview(reply.preview);
        setError("");
      })
      .catch((error) => {
        if (active) setError(errorMessage(error, "Preview unavailable"));
      });
    return () => {
      active = false;
    };
  }, [version]);
  return (
    <>
      <section className="content-section selected-version" aria-label="Selected saved version">
        <h3>{version.firstArchivedAtMs ? `Saved ${contentTime(version.firstArchivedAtMs)}` : "Saved version"}</h3>
        {error && <Alert severity="info">{error}</Alert>}
        {preview && <ContentPreview fileID={fileID} versionID={version.id} manifest={preview} />}
        <p className="product-muted">
          {formatFilesize(version.size)}
          {version.lastArchivedAtMs ? ` · Last backed up ${contentTime(version.lastArchivedAtMs)}` : ""}
        </p>
        <Button component="a" href={`/restore?version_id=${version.id}`} variant="contained" size="small">
          Restore this version
        </Button>
        <TechnicalDetails facts={version} />
      </section>
      <ContentCopies signature={version.signature} versionID={version.id} fileID={fileID} />
    </>
  );
};

export const ContentCopies = ({ signature, versionID, fileID }: { signature: Uint8Array; versionID?: bigint; fileID?: bigint }) => {
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
        let page = await fileCatalogCli.listCopies({ signature, afterId: afterID, limit: 20 }).response;
        if (sequence !== request.current) return;
        const positions = [...page.positions];
        let pages = 1;
        while (!afterID && pages < loadedPages.current && page.hasMore) {
          page = await fileCatalogCli.listCopies({ signature, afterId: page.positions.at(-1)!.id, limit: 20 }).response;
          if (sequence !== request.current) return;
          positions.push(...page.positions);
          pages++;
        }
        const ids = Array.from(new Set(positions.map((position) => position.mediaId)));
        const values = ids.length ? (await cli.mediaList({ param: { oneofKind: "mget", mget: { ids } } }).response).media : [];
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
    <section className="content-section" aria-label="Archived copies">
      <h3>
        <StorageRoundedIcon fontSize="small" /> Archived copies
      </h3>
      {loading && <LinearProgress />}
      {error && (
        <Alert severity="error">
          {error}
          <Button onClick={() => void load()}>Try again</Button>
        </Alert>
      )}
      {!loading && !error && positions.length === 0 && (
        <Alert severity="warning">
          {versionID ? "This version cannot currently be restored: no copies available." : "No archived copies. Back up this file to save one."}
        </Alert>
      )}
      {positions.map((position) => {
        const value = media.get(position.mediaId);
        const health = position.health ?? PositionHealth.POSITION_HEALTH_UNKNOWN;
        const normalCopy = health === PositionHealth.POSITION_HEALTH_UNKNOWN || health === PositionHealth.HEALTHY;
        const online = value?.kind === MediaKind.VOLUME && value.mounted === true && value.capabilities?.read === MediaAccess.CONCURRENT_RANDOM;
        const url = `${fileBase}/content/${position.id}?${versionID ? `version_id=${versionID}&` : ""}`;
        return (
          <article className="copy-card" key={String(position.id)}>
            <div className="copy-heading">
              <StorageRoundedIcon fontSize="small" />
              <strong>{value?.name || value?.identity || "Archive storage"}</strong>
              <span>{value?.kind === MediaKind.TAPE ? "Tape" : "Volume"}</span>
            </div>
            <p className="product-muted">
              {copyHealthLabel(health)}
              {position.checkedAtMs ? ` · ${contentTime(position.checkedAtMs)}` : ""}
              {position.healthJobId ? (
                <>
                  {" "}
                  · <a href={`/jobs/${position.healthJobId}`}>Check results</a>
                </>
              ) : null}
            </p>
            {!normalCopy && (
              <Alert severity="warning">
                Not used by normal Restore.
                {health === PositionHealth.DAMAGED || health === PositionHealth.UNREADABLE ? " Advanced recovery can attempt this copy." : ""}
              </Alert>
            )}
            {!online && <p className="product-muted">{value?.kind === MediaKind.TAPE ? "Load this tape to restore" : "Connect this volume to restore"}</p>}
            {online && normalCopy && (
              <div className="product-actions">
                <Button component="a" href={url} target="_blank" rel="noreferrer" size="small">
                  Open copy
                </Button>
              </div>
            )}
            <details className="technical-details">
              <summary>Storage path</summary>
              <p className="file-detail-path">{position.path}</p>
            </details>
          </article>
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
    [PositionHealth.POSITION_HEALTH_UNKNOWN]: "Not checked",
    [PositionHealth.HEALTHY]: "Last check passed",
    [PositionHealth.DAMAGED]: "Damaged",
    [PositionHealth.MISSING]: "Missing",
    [PositionHealth.UNREADABLE]: "Unreadable",
  })[health];

const ContentDuplicates = ({ signature, fileID }: { signature: Uint8Array; fileID?: bigint }) => {
  const [files, setFiles] = useState<File[]>([]);
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
        const reply = await fileCatalogCli.listDuplicates({ signature, afterFileId, limit: 20 }).response;
        if (sequence !== request.current) return;
        setFiles((current) => (afterFileId ? [...current, ...reply.files] : reply.files));
        setCursor(reply.files.at(-1)?.id ?? afterFileId);
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
  const matches = files.filter((value) => value.id !== fileID);
  return (
    <div className="matching-files">
      {loading && <LinearProgress />}
      {error && (
        <Alert severity="error">
          {error}
          <Button onClick={() => void load()}>Try again</Button>
        </Alert>
      )}
      {matches.map((file) => (
        <a key={String(file.id)} href={`/file?file=${file.id}`}>
          <strong>{file.name}</strong>
          <small>{file.tags.join(" · ") || "Open in Library"}</small>
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
