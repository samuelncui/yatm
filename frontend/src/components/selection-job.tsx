import { lazy, Suspense, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
import { Alert, Box, Button, Checkbox, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, MenuItem, Stack, TextField } from "@mui/material";
import {
  ChonkyActions,
  FileBrowser,
  FileContextMenu,
  FileList,
  FileNavbar,
  FileToolbar,
  defineFileAction,
  type ChonkyFileActionData,
  type FileBrowserHandle,
  type FileData,
} from "@samuelncui/chonky";
import { archiveJobCli, cli, fileCatalogCli, restoreJobCli } from "@/api";
import {
  ArchiveJobSpec,
  FileScope,
  FileSelection,
  FileVersion,
  OnlineBinding,
  PreviewPolicy,
  RestoreJobSpec,
  InspectSelectionRequest,
  type InspectSelectionReply,
} from "@/entity";
import { RefreshListAction } from "@/actions";
import { useFileBrowser } from "@/pages/file";
import { RestoreDestinationPicker, type RestoreTarget } from "@/components/restore-destination";
import { PreviewPolicySelect } from "@/components/preview-policy-select";
import { ChooseVersionDialog } from "@/components/version-picker";
import { associatedLibraryFileID, selectionForFile } from "@/components/location-files";
import { ToobarInfo } from "@/components/toolbarInfo";
import { SelectionWaitlist, waitlistRootID, type SelectionEntry as Entry } from "@/components/selection-waitlist";
import { followRestorePolicy, restoreCutoff, type RestorePolicy } from "@/components/restore-version-policy";
import { errorMessage, formatFilesize, runUIAction } from "@/tools";

const AddSelection = defineFileAction({ id: "add_job_selection", requiresSelection: true, button: { name: "Add to list", toolbar: true, contextMenu: true } });
const labels = { archive: "Backup", restore: "Restore" };
const RestoreTimePicker = lazy(() => import("@/components/restore-time-picker"));

function savedEntries(key: string): Entry[] {
  try {
    const entries = JSON.parse(sessionStorage.getItem(key) ?? "[]") as {
      name: string;
      path: string;
      selection?: string;
      version?: string;
      fileID?: string;
      target?: string;
      isDir?: boolean;
    }[];
    return entries.map((entry) => {
      const selection = entry.selection ? FileSelection.fromJsonString(entry.selection) : undefined;
      const version = entry.version ? FileVersion.fromJsonString(entry.version) : undefined;
      return { ...entry, selection, version, key: version ? `version:${version.id}` : FileSelection.toJsonString(selection!) };
    });
  } catch {
    return [];
  }
}

function savedPolicy(key: string): RestorePolicy {
  try {
    const saved = JSON.parse(sessionStorage.getItem(key) ?? "null");
    if (saved?.mode === "before" && typeof saved.date === "string") return { mode: "before", date: saved.date };
  } catch {
    // A discarded browser preference does not discard the waitlist.
  }
  return { mode: "latest", date: "" };
}

export const SelectionJobPage = ({ kind }: { kind: keyof typeof labels }) => {
  const navigate = useNavigate();
  const location = useLocation();
  const [params] = useSearchParams();
  const storageKey = `job-selection:${kind}`;
  const [entries, setEntries] = useState<Entry[]>(() => savedEntries(storageKey));
  const policyStorageKey = `${storageKey}:version-policy`;
  const [versionPolicy, setVersionPolicy] = useState<RestorePolicy>(() => savedPolicy(policyStorageKey));
  const [skipConsent, setSkipConsent] = useState("");
  const cutoff = restoreCutoff(versionPolicy);
  const validPolicy = kind !== "restore" || versionPolicy.mode === "latest" || cutoff !== undefined;
  const [review, setReview] = useState<{ key: string; result: InspectSelectionReply }>();
  const [estimating, setEstimating] = useState(false);
  const [estimateError, setEstimateError] = useState("");
  const [estimateAttempt, setEstimateAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const [configuring, setConfiguring] = useState(false);
  const creationTitleID = useId();
  const submitting = useRef(false);
  const [error, setError] = useState("");
  const [previewPolicy, setPreviewPolicy] = useState(PreviewPolicy.PREVIEW_NONE);
  const [force, setForce] = useState(false);
  const [destination, setDestination] = useState<RestoreTarget>();
  const destinationID = destination ? String(destination.location.id) : "";
  const destinationPath = destination?.path ?? "";
  const [allowDamagedCopies, setAllowDamagedCopies] = useState(false);
  const [choosing, setChoosing] = useState<Entry & { resolvedVersion?: FileVersion }>();
  const browserRef = useRef<FileBrowserHandle>(null);
  const refreshRef = useRef<() => Promise<void>>(async () => {});
  const refresh = useCallback(() => refreshRef.current(), []);
  const ignoreOpen = useCallback(() => {}, []);
  const browser = useFileBrowser(
    browserRef,
    `selection-browser:${kind}`,
    refresh,
    ignoreOpen,
    ignoreOpen,
    undefined,
    undefined,
    kind === "restore" ? FileScope.SAVED : FileScope.DEFAULT,
  );
  useEffect(() => {
    refreshRef.current = browser.refresh;
  }, [browser.refresh]);
  useEffect(() => {
    sessionStorage.setItem(
      storageKey,
      JSON.stringify(
        entries.map((entry) => ({
          ...entry,
          selection: entry.selection ? FileSelection.toJsonString(entry.selection) : undefined,
          version: entry.version ? FileVersion.toJsonString(entry.version) : undefined,
        })),
      ),
    );
  }, [entries, storageKey]);
  useEffect(() => {
    if (kind === "restore") sessionStorage.setItem(policyStorageKey, JSON.stringify(versionPolicy));
  }, [kind, policyStorageKey, versionPolicy]);
  const merge = useCallback(
    (items: Entry[]) =>
      setEntries((current) => {
        const result = new Map(current.map((entry) => [entry.key, entry]));
        let changed = false;
        for (const entry of items) {
          if (entry.selection && !entry.isDir && entry.fileID && [...result.values()].some((existing) => existing.version && existing.fileID === entry.fileID))
            continue;
          if (entry.version) {
            for (const existing of result.values()) {
              if (existing.selection && !existing.isDir && existing.fileID === entry.fileID) {
                result.delete(existing.key);
                changed = true;
              }
            }
          }
          if (!result.has(entry.key)) {
            result.set(entry.key, entry);
            changed = true;
          }
        }
        return changed ? [...result.values()] : current;
      }),
    [],
  );

  const versionEntry = useCallback(async (version: FileVersion): Promise<Entry> => {
    const parents = await cli.fileListParents({ id: version.fileId }).response;
    const path = parents.parents
      .filter((file) => file.id !== 0n)
      .map((file) => file.name)
      .join("/");
    return {
      key: `version:${version.id}`,
      name: parents.parents.at(-1)?.name ?? path,
      path,
      target: path,
      fileID: String(version.fileId),
      version,
    };
  }, []);
  const initialVersion = params.get("version_id");
  useEffect(() => {
    if (kind !== "restore" || !initialVersion || !/^[1-9]\d*$/.test(initialVersion)) return;
    runUIAction(async () => {
      const reply = await fileCatalogCli.getVersion({ id: BigInt(initialVersion) }).response;
      if (reply.version) merge([await versionEntry(reply.version)]);
    }, "Could not add saved version");
  }, [initialVersion, kind, merge, versionEntry]);
  useEffect(() => {
    const selected = location.state?.selections as { selection: FileSelection; name: string; path: string; fileID?: string }[] | undefined;
    if (kind !== "archive" || !selected) return;
    let active = true;
    runUIAction(async () => {
      const additions: Entry[] = [];
      for (const entry of selected) {
        const id = entry.selection.target.oneofKind === "library" ? entry.selection.target.library.fileId : entry.fileID ? BigInt(entry.fileID) : undefined;
        if (id === undefined) {
          additions.push({ ...entry, key: FileSelection.toJsonString(entry.selection) });
          continue;
        }
        const reply = await cli.fileListParents({ id }).response;
        const path = reply.parents
          .filter((file) => file.id !== 0n)
          .map((file) => file.name)
          .join("/");
        additions.push({
          ...entry,
          key: FileSelection.toJsonString(entry.selection),
          name: reply.parents.at(-1)?.name ?? "Library",
          target: path,
          path: entry.selection.target.oneofKind === "library" ? path || "Library" : entry.path,
        });
      }
      if (!active) return;
      merge(additions);
      navigate(location.pathname, { replace: true, state: null });
    }, "Could not load selected files");
    return () => {
      active = false;
    };
  }, [kind, location.state, location.pathname, merge, navigate]);

  const addFiles = async (files: FileData[]) => {
    if (browser.loadError) return;
    if (kind !== "restore" && browser.sourceLocation?.binding === OnlineBinding.UNCONFIRMED) {
      setError("Confirm this imported Location before adding files.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const additions: Entry[] = [];
      for (const file of files) {
        const fileID = associatedLibraryFileID(file);
        if (kind === "restore" && !file.isDir) {
          if (!fileID) throw new Error(`${file.name} has no known saved version. Scan it or choose a Library file.`);
          const selection = FileSelection.create({ target: { oneofKind: "library", library: { fileId: BigInt(fileID) } }, scope: FileScope.SAVED });
          const parents = await cli.fileListParents({ id: BigInt(fileID) }).response;
          const path = parents.parents
            .filter((parent) => parent.id !== 0n)
            .map((parent) => parent.name)
            .join("/");
          additions.push({ key: FileSelection.toJsonString(selection), name: file.name, path, target: path, fileID, selection });
          continue;
        }
        const selection = selectionForFile(file, browser.scope);
        let target: string | undefined;
        if (fileID) {
          const parents = await cli.fileListParents({ id: BigInt(fileID) }).response;
          target = parents.parents
            .filter((file) => file.id !== 0n)
            .map((file) => file.name)
            .join("/");
        }
        additions.push({
          key: FileSelection.toJsonString(selection),
          name: file.name,
          path: file.physicalPath !== undefined ? `${browser.sourceLocation?.name ?? "Location"}/${file.physicalPath}` : (target ?? file.name),
          target,
          selection,
          fileID: file.isDir ? undefined : fileID,
          isDir: file.isDir,
        });
      }
      merge(additions);
    } catch (error) {
      setError(errorMessage(error, "Could not add selection"));
    } finally {
      setBusy(false);
    }
  };
  const action = (data: ChonkyFileActionData) => {
    if (busy) return;
    if (data.id === ChonkyActions.MoveFiles.id) {
      if (data.payload.sourceInstanceId === `select-${kind}` && data.payload.destination.id === waitlistRootID(kind)) void addFiles(data.payload.files);
      return;
    }
    if (data.id === AddSelection.id) {
      void addFiles(data.state.selectedFilesForAction);
      return;
    }
    if (data.id === ChonkyActions.OpenFiles.id) {
      const file = data.payload.targetFile ?? data.payload.files[0];
      if (file && !file.isDir) {
        void addFiles([file]);
        return;
      }
    }
    if ([ChonkyActions.OpenFiles.id, ChonkyActions.ChangeSelection.id, RefreshListAction.id].includes(data.id)) browser.browserProps.onFileAction(data);
  };
  const consentKey = JSON.stringify([entries.map((entry) => entry.key), versionPolicy]);
  useEffect(() => setSkipConsent(""), [consentKey]);
  const skipUnmatchedVersions = skipConsent === consentKey;
  const create = async () => {
    if (submitting.current || !canCreate || estimating || !summary) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const selections = summary.selections;
      const reply =
        kind === "archive"
          ? await archiveJobCli.create({
              priority: 1n,
              spec: ArchiveJobSpec.create({ selections }),
              previewPolicy,
              forceRehash: previewPolicy !== PreviewPolicy.PREVIEW_NONE && force,
            }).response
          : await restoreJobCli.create({
              priority: 1n,
              spec: RestoreJobSpec.create({
                selections,
                fileVersionIds: entries.flatMap((entry) => (entry.version ? [entry.version.id] : [])),
                destination: { locationId: BigInt(destinationID), path: destinationPath },
                allowDamagedCopies,
                versionPolicy: { beforeAtMs: cutoff },
                skipUnmatchedVersions,
              }),
            }).response;
      if (!reply.job) throw new Error("Job was not returned. Check Jobs before retrying.");
      sessionStorage.removeItem(storageKey);
      navigate(`/jobs/${reply.job.id}`);
    } catch (error) {
      setError(errorMessage(error, "Could not create job"));
    } finally {
      setBusy(false);
      submitting.current = false;
    }
  };
  const inspection = useMemo(
    () =>
      InspectSelectionRequest.create({
        selections: entries.flatMap((entry) => (entry.selection ? [entry.selection] : [])),
        fileVersionIds: entries.flatMap((entry) => (entry.version ? [entry.version.id] : [])),
        restore: kind === "restore",
        destination: kind === "restore" && destinationID ? { locationId: BigInt(destinationID), path: destinationPath } : undefined,
        allowDamagedCopies,
        versionPolicy: kind === "restore" ? { beforeAtMs: cutoff } : undefined,
        skipUnmatchedVersions,
      }),
    [entries, kind, destinationID, destinationPath, allowDamagedCopies, cutoff, skipUnmatchedVersions],
  );
  const inspectionKey = JSON.stringify(inspection, (_, value) => (typeof value === "bigint" ? value.toString() : value));
  const summary = validPolicy && review?.key === inspectionKey ? review.result : undefined;
  useEffect(() => {
    if (!entries.length || !validPolicy) return;
    let active = true;
    const timer = setTimeout(() => {
      setEstimating(true);
      setEstimateError("");
      void fileCatalogCli
        .inspectSelection(inspection)
        .response.then((result) => {
          if (active) setReview({ key: inspectionKey, result });
        })
        .catch((error) => {
          if (active) setEstimateError(errorMessage(error, "Could not estimate selection"));
        })
        .finally(() => {
          if (active) setEstimating(false);
        });
    }, 350);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [inspection, inspectionKey, estimateAttempt, entries.length, validPolicy]);
  const canCreate =
    validPolicy &&
    !!summary &&
    summary.files > 0n &&
    !busy &&
    (kind === "restore"
      ? summary.missingCopies === 0n && (summary.unmatchedVersions === 0n || skipUnmatchedVersions) && !!destinationID
      : summary.missingOriginals === 0n);
  return (
    <Box className="browser-box selection-job-page">
      <div className="selection-job-workspace">
        <Stack component="section" className="selection-browser" spacing={1.5}>
          {error && !configuring && <Alert severity="error">{error}</Alert>}
          {kind !== "restore" && browser.sourceLocation && browser.sourceLocation.binding === OnlineBinding.UNCONFIRMED && (
            <Alert
              severity="info"
              action={
                <Button component={Link} to={`/settings/locations/${browser.sourceLocation.id}`}>
                  Confirm path
                </Button>
              }
            >
              Confirm this imported Location before adding files.
            </Alert>
          )}
          <FileBrowser
            ref={browserRef}
            {...browser.browserProps}
            files={browser.files.map((file) => (file ? { ...file, draggable: !busy, droppable: false } : null))}
            folderChain={browser.browserProps.folderChain?.map((file) => (file ? { ...file, droppable: false } : null))}
            instanceId={`select-${kind}`}
            onFileAction={action}
            disableDragAndDrop={!!browser.loadError}
            fileActions={browser.loadError ? [RefreshListAction] : [AddSelection, ChonkyActions.ToggleHiddenFiles, RefreshListAction]}
          >
            <FileNavbar rootContent={browser.selector} />
            <FileToolbar layout="inline">{!browser.browserProps.hideToolbarInfo && <ToobarInfo files={browser.files} />}</FileToolbar>
            <FileList {...browser.listProps} />
            <FileContextMenu />
          </FileBrowser>
        </Stack>
        <section className="selection-todo" aria-label={`${labels[kind]} selection`}>
          <SelectionWaitlist
            kind={kind}
            label={labels[kind]}
            entries={entries}
            resolutions={summary?.resolvedVersions}
            cutoff={cutoff}
            busy={busy}
            onClear={() => setEntries([])}
            onRemove={(keys) => setEntries((current) => current.filter((entry) => !keys.has(entry.key)))}
            onChooseVersion={(entry, version) => setChoosing({ ...entry, resolvedVersion: version })}
            footer={
              <div className="selection-submit">
                <Button variant="contained" disabled={busy || !entries.length} onClick={() => setConfiguring(true)}>
                  {labels[kind]}…
                </Button>
              </div>
            }
          />
        </section>
      </div>
      <Dialog
        open={configuring}
        keepMounted
        fullWidth
        maxWidth="sm"
        aria-labelledby={creationTitleID}
        onClose={() => {
          if (!submitting.current) setConfiguring(false);
        }}
      >
        <DialogTitle id={creationTitleID}>{labels[kind]}</DialogTitle>
        <DialogContent dividers>
          <Stack spacing={2}>
            {summary && (
              <strong>
                {String(summary.files)}{" "}
                {kind === "restore" ? (summary.files === 1n ? "restore item" : "restore items") : summary.files === 1n ? "file" : "files"} ·{" "}
                {formatFilesize(Number(summary.bytes))}
                {summary.unknownSizeFiles > 0n ? ` known · ${summary.unknownSizeFiles} sizes unknown` : ""}
              </strong>
            )}
            {kind === "restore" ? (
              <>
                <TextField
                  select
                  fullWidth
                  label="Versions"
                  value={versionPolicy.mode}
                  disabled={busy}
                  onChange={(event) => setVersionPolicy((current) => ({ ...current, mode: event.target.value as RestorePolicy["mode"] }))}
                >
                  <MenuItem value="latest">Latest saved version</MenuItem>
                  <MenuItem value="before">Latest backup at or before…</MenuItem>
                </TextField>
                {versionPolicy.mode === "before" && (
                  <Suspense fallback={<Box role="status">Loading date picker…</Box>}>
                    <RestoreTimePicker
                      value={versionPolicy.date}
                      disabled={busy}
                      onChange={(date) => setVersionPolicy((current) => ({ ...current, date }))}
                      error={!validPolicy}
                    />
                  </Suspense>
                )}
                {entries.some((entry) => entry.version) && (
                  <Button disabled={busy} onClick={() => setEntries(followRestorePolicy)}>
                    Apply current policy to all
                  </Button>
                )}
                <RestoreDestinationPicker value={destination} onChange={setDestination} disabled={busy} />
                <details>
                  <summary>Advanced recovery</summary>
                  <Stack spacing={2}>
                    <FormControlLabel
                      control={<Checkbox checked={allowDamagedCopies} disabled={busy} onChange={(_, checked) => setAllowDamagedCopies(checked)} />}
                      label="Allow damaged copies"
                    />
                    {allowDamagedCopies && (
                      <Alert severity="warning">Complete damaged output may be retained for recovery. It is not a verified saved version.</Alert>
                    )}
                  </Stack>
                </details>
              </>
            ) : (
              <Stack spacing={2}>
                <PreviewPolicySelect value={previewPolicy} onChange={setPreviewPolicy} disabled={busy} />
                {previewPolicy !== PreviewPolicy.PREVIEW_NONE && (
                  <FormControlLabel control={<Checkbox checked={force} disabled={busy} onChange={(_, checked) => setForce(checked)} />} label="Force rehash" />
                )}
              </Stack>
            )}
            {error && <Alert severity="error">{error}</Alert>}
            {estimateError && (
              <Alert severity="error" action={<Button onClick={() => setEstimateAttempt((value) => value + 1)}>Retry estimate</Button>}>
                {estimateError}
              </Alert>
            )}
            {entries.length > 0 && validPolicy && !summary && !estimateError && <Box role="status">Estimating selection…</Box>}
            {kind === "restore" && summary && summary.unmatchedVersions > 0n && !skipUnmatchedVersions && (
              <Alert
                severity="warning"
                action={
                  cutoff !== undefined && (
                    <Button disabled={busy} onClick={() => setSkipConsent(consentKey)}>
                      Skip {String(summary.unmatchedVersions)} {summary.unmatchedVersions === 1n ? "item" : "items"}
                    </Button>
                  )
                }
              >
                {String(summary.unmatchedVersions)} {summary.unmatchedVersions === 1n ? "item has" : "items have"} no version matching this policy.
              </Alert>
            )}
            {kind === "restore" && summary && summary.skippedVersions > 0n && (
              <Alert
                severity="info"
                action={
                  <Button disabled={busy} onClick={() => setSkipConsent("")}>
                    Include again
                  </Button>
                }
              >
                {String(summary.skippedVersions)} {summary.skippedVersions === 1n ? "item" : "items"} skipped.
              </Alert>
            )}
            {kind !== "restore" && summary && summary.missingOriginals > 0n && (
              <Alert severity="warning">
                {String(summary.missingOriginals)} {summary.missingOriginals === 1n ? "file has" : "files have"} no usable original. Check their Location or
                remove them from the list.
              </Alert>
            )}
            {kind === "restore" && summary && summary.missingCopies > 0n && (
              <Alert severity="warning">
                {String(summary.missingCopies)} {summary.missingCopies === 1n ? "item has" : "items have"} no usable archived copy.
              </Alert>
            )}
            {kind === "restore" && summary && summary.ignoredOutputs > 0n && (
              <Alert severity="warning">
                {String(summary.ignoredOutputs)} outputs match Ignore rules. They will be restored without linking them to Library files.
              </Alert>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button disabled={busy} onClick={() => setConfiguring(false)}>
            Cancel
          </Button>
          <Button variant="contained" disabled={!canCreate || estimating} onClick={() => void create()}>
            {busy ? "Preparing…" : `Prepare ${labels[kind].toLowerCase()}`}
          </Button>
        </DialogActions>
      </Dialog>
      {choosing?.fileID && (
        <ChooseVersionDialog
          file={{ id: choosing.fileID, name: choosing.name }}
          selectedVersion={
            choosing.version ??
            choosing.resolvedVersion ??
            summary?.resolvedVersions.find((resolution) => String(resolution.fileId) === choosing.fileID)?.version
          }
          cutoff={cutoff}
          allowDamagedCopies={allowDamagedCopies}
          onClose={() => setChoosing(undefined)}
          onChoose={async (version) => {
            const next = await versionEntry(version);
            setEntries((current) => [
              ...current.filter(
                (entry) => entry.key !== choosing.key && entry.key !== next.key && !(entry.selection && !entry.isDir && entry.fileID === next.fileID),
              ),
              next,
            ]);
          }}
        />
      )}
    </Box>
  );
};
