import { FileBrowser } from "@/components/file-browser";
import { Feedback } from "@/components/feedback";
import { lazy, Suspense, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import { Alert, Box, Button, Checkbox, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, MenuItem, Stack, TextField } from "@mui/material";
import {
  ChonkyActions,
  FileContextMenu,
  FileList,
  FileNavbar,
  FileToolbar,
  defineFileAction,
  type ChonkyFileActionData,
  type FileBrowserHandle,
  type FileData,
} from "@samuelncui/chonky";
import { archiveJobCli, filesCli, restoreJobCli } from "@/api";
import {
  ArchiveJobSpec,
  FileScope,
  FileSelection,
  FileVersion,
  EntryKind,
  PreviewPolicy,
  RestoreJobSpec,
  EstimateRestoreJobRequest,
  type SelectionInspectionResult,
} from "@/entity";
import { RefreshListAction } from "@/actions";
import { useFileBrowser } from "@/pages/file";
import { RestoreDestinationPicker, type RestoreTarget } from "@/components/restore-destination";
import { PreviewPolicySelect, usePreviewGeneration } from "@/components/preview-policy-select";
import { ChooseVersionDialog } from "@/components/version-picker";
import { ToobarInfo } from "@/components/toolbarInfo";
import { SelectionWaitlist, waitlistRootID } from "@/components/selection-waitlist";
import { canAddFileToSelection, loadRestoreVersionEntry, selectionEntriesForFiles, type SelectionEntry as Entry } from "@/components/selection-waitlist-state";
import { followRestorePolicy, restoreCutoff, type RestorePolicy } from "@/components/restore-version-policy";
import { errorMessage, formatFilesize, runUIAction } from "@/tools";
import { libraryDirectoryReference } from "@/components/files-browser";

import { useSelections } from "@/state/react";
import { SelectionJobRecreate, type SelectionCreation } from "./selection-job-recreate";

const AddSelection = defineFileAction({ id: "add_job_selection", requiresSelection: true, button: { name: "Add to list", toolbar: true, contextMenu: true } });
const labels = { archive: "Archive", restore: "Restore" };
const RestoreTimePicker = lazy(() => import("@/components/restore-time-picker"));

export const SelectionJobPage = ({ kind }: { kind: keyof typeof labels }) => {
  const route = useLocation();
  const id = new URLSearchParams(route.search).get("recreate");
  return id !== null ? (
    <SelectionJobRecreate key={route.key} kind={kind} id={id}>
      {(initial) => <SelectionJobForm kind={kind} initial={initial} />}
    </SelectionJobRecreate>
  ) : (
    <SelectionJobForm key={kind} kind={kind} />
  );
};

const SelectionJobForm = ({ kind, initial }: { kind: keyof typeof labels; initial?: SelectionCreation }) => {
  const navigate = useNavigate();
  const location = useLocation();
  const [params] = useSearchParams();
  const { entries, revision, setEntries, versionPolicy, setVersionPolicy, merge, submitted } = useSelections(kind);
  const consentKey = JSON.stringify([entries.map((entry) => entry.key), versionPolicy, revision]);
  const [skipConsent, setSkipConsent] = useState(initial?.kind === "restore" && initial.request.spec?.skipUnmatchedVersions ? consentKey : "");
  const cutoff = restoreCutoff(versionPolicy);
  const validPolicy = kind !== "restore" || versionPolicy.mode === "latest" || cutoff !== undefined;
  const [review, setReview] = useState<{ key: string; result: SelectionInspectionResult }>();
  const [estimating, setEstimating] = useState(false);
  const [estimateError, setEstimateError] = useState("");
  const [estimateAttempt, setEstimateAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const [configuring, setConfiguring] = useState(false);
  const creationTitleID = useId();
  const submitting = useRef(false);
  const [error, setError] = useState("");
  const [previewPolicy, setPreviewPolicy] = useState(initial?.kind === "archive" ? initial.request.previewPolicy : PreviewPolicy.NONE);
  const validPreview = [PreviewPolicy.NONE, PreviewPolicy.MISSING_ONLY, PreviewPolicy.REGENERATE_ALL].includes(previewPolicy);
  const generation = usePreviewGeneration(kind === "archive" && configuring);
  const [force, setForce] = useState(initial?.kind === "archive" && initial.request.forceRehash);
  const [destination, setDestination] = useState<RestoreTarget | undefined>(initial?.destination);
  const [destinationError, setDestinationError] = useState(initial?.destinationError ?? "");
  const chooseDestination = useCallback((target: RestoreTarget) => {
    setDestination(target);
    setDestinationError("");
  }, []);
  const destinationID = destination ? String(destination.location.id) : "";
  const destinationPath = destination?.path ?? "";
  const [allowDamagedCopies, setAllowDamagedCopies] = useState(initial?.kind === "restore" && !!initial.request.spec?.allowDamagedCopies);
  const priority = initial?.request.priority ?? 1n;
  const unavailable = entries.some((entry) => !!entry.unavailableReason);
  const [choosing, setChoosing] = useState<Entry & { resolvedVersion?: FileVersion }>();
  const browserRef = useRef<FileBrowserHandle>(null);
  const refreshRef = useRef<() => Promise<void>>(async () => {});
  const refresh = useCallback(() => refreshRef.current(), []);
  const ignoreLocate = useCallback(() => {}, []);
  const browser = useFileBrowser(
    browserRef,
    `selection-browser:${kind}`,
    refresh,
    undefined,
    ignoreLocate,
    undefined,
    undefined,
    kind === "restore" ? FileScope.SAVED : FileScope.DEFAULT,
  );
  useEffect(() => {
    refreshRef.current = browser.refresh;
  }, [browser.refresh]);
  const initialVersion = params.get("version_id");
  useEffect(() => {
    if (initial || kind !== "restore" || !initialVersion || !/^[1-9]\d*$/.test(initialVersion)) return;
    let active = true;
    runUIAction(async () => {
      const reply = await filesCli.getVersion({ id: BigInt(initialVersion) }).response;
      if (!active) return;
      if (!reply.version) throw new Error("This saved version no longer exists.");
      const entry = await loadRestoreVersionEntry(reply.version);
      if (active) merge([entry]);
    }, "Could not add saved version");
    return () => {
      active = false;
    };
  }, [initial, initialVersion, kind, merge]);
  useEffect(() => {
    const selected = location.state?.selections as { selection: FileSelection; name: string; path: string; fileID?: string; isDir?: boolean }[] | undefined;
    if (initial || kind !== "archive" || !selected) return;
    let active = true;
    runUIAction(async () => {
      const additions: Entry[] = [];
      for (const entry of selected) {
        const id = entry.selection.target.oneofKind === "library" ? entry.selection.target.library.fileId : entry.fileID ? BigInt(entry.fileID) : undefined;
        if (id === undefined) {
          additions.push({ ...entry, key: FileSelection.toJsonString(entry.selection) });
          continue;
        }
        const reply = (await filesCli.get({ reference: libraryDirectoryReference(String(id)) }).response).detail;
        if (!reply) throw new Error("File details are unavailable.");
        const path = reply.organization?.path ?? reply.entry?.name ?? "";
        additions.push({
          ...entry,
          key: FileSelection.toJsonString(entry.selection),
          name: reply.entry?.name ?? "Library",
          target: path,
          isDir: reply.entry?.kind === EntryKind.DIRECTORY,
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
  }, [initial, kind, location.state, location.pathname, merge, navigate]);

  const addFiles = async (files: FileData[]) => {
    if (browser.loadError || files.some((file) => !canAdd(file))) return;
    setBusy(true);
    setError("");
    try {
      merge(await selectionEntriesForFiles(kind, files, browser.scope, browser.source?.kind === "location" ? browser.source.name : "Library"));
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
    if ([ChonkyActions.OpenFiles.id, ChonkyActions.ChangeSelection.id, RefreshListAction.id].includes(data.id)) browser.browserProps.onFileAction(data);
  };
  const skipUnmatchedVersions = skipConsent === consentKey;
  const create = async () => {
    if (submitting.current || !canCreate || estimating || !summary || (kind === "archive" && previewPolicy !== PreviewPolicy.NONE && !generation.available))
      return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const selections = summary.selections;
      const reply =
        kind === "archive"
          ? await archiveJobCli.create({
              priority,
              spec: ArchiveJobSpec.create({ selections }),
              previewPolicy,
              forceRehash: previewPolicy !== PreviewPolicy.NONE && force,
            }).response
          : await restoreJobCli.create({
              priority,
              spec: RestoreJobSpec.create({
                selections,
                fileVersionIds: entries.flatMap((entry) => (entry.version ? [entry.version.id] : entry.versionID ? [BigInt(entry.versionID)] : [])),
                destination: { locationId: BigInt(destinationID), path: destinationPath },
                allowDamagedCopies,
                versionPolicy: { beforeAtNs: cutoff },
                skipUnmatchedVersions,
              }),
            }).response;
      if (!reply.job) throw new Error("Job was not returned. Check Jobs before retrying.");
      submitted(revision);
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
      EstimateRestoreJobRequest.create({
        selections: entries.flatMap((entry) => (entry.selection ? [entry.selection] : [])),
        fileVersionIds: entries.flatMap((entry) => (entry.version ? [entry.version.id] : entry.versionID ? [BigInt(entry.versionID)] : [])),
        destination: kind === "restore" && destinationID ? { locationId: BigInt(destinationID), path: destinationPath } : undefined,
        allowDamagedCopies,
        versionPolicy: kind === "restore" ? { beforeAtNs: cutoff } : undefined,
        skipUnmatchedVersions,
      }),
    [entries, kind, destinationID, destinationPath, allowDamagedCopies, cutoff, skipUnmatchedVersions],
  );
  const inspectionKey = JSON.stringify(inspection, (_, value) => (typeof value === "bigint" ? value.toString() : value));
  const summary = validPolicy && review?.key === inspectionKey ? review.result : undefined;
  useEffect(() => {
    if (!entries.length || !validPolicy || unavailable) return;
    let active = true;
    const timer = setTimeout(() => {
      setEstimating(true);
      setEstimateError("");
      void (kind === "restore" ? restoreJobCli.estimate(inspection) : archiveJobCli.estimate({ selections: inspection.selections })).response
        .then((response) => {
          if (!response.result) throw new Error("Selection estimate is missing");
          if (active) setReview({ key: inspectionKey, result: response.result });
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
  }, [inspection, inspectionKey, estimateAttempt, entries.length, validPolicy, kind, unavailable]);
  const canCreate =
    validPolicy &&
    (kind !== "archive" || validPreview) &&
    !unavailable &&
    !!summary &&
    summary.fileCount > 0n &&
    !busy &&
    (kind !== "restore" || (summary.missingCopyCount === 0n && (summary.unmatchedVersionCount === 0n || skipUnmatchedVersions) && !!destinationID));
  const canAdd = (file: FileData | null) => canAddFileToSelection(kind, file);
  return (
    <Box className="browser-box selection-job-page">
      <div className="selection-job-workspace">
        <Stack component="section" className="selection-browser" spacing={1.5}>
          {initial && (
            <Feedback severity="info">Review the original selections and options, then submit to create a new Job. Priority: {String(priority)}.</Feedback>
          )}
          {initial?.reason && <Feedback severity="warning">{initial.reason}</Feedback>}
          {unavailable && <Feedback severity="error">Some selected entries are unavailable. Remove them or select them again before preparing.</Feedback>}
          {error && !configuring && <Feedback severity="error">{error}</Feedback>}
          <FileBrowser
            ref={browserRef}
            {...browser.browserProps}
            files={browser.files.map((file) => (file ? { ...file, draggable: !busy && canAdd(file), droppable: false } : null))}
            folderChain={browser.browserProps.folderChain?.map((file) => (file ? { ...file, droppable: false } : null))}
            instanceId={`select-${kind}`}
            onFileAction={action}
            disableDragAndDrop={!!browser.loadError}
            fileActions={
              browser.loadError ? [RefreshListAction] : [{ ...AddSelection, fileFilter: canAdd }, ChonkyActions.ToggleHiddenFiles, RefreshListAction]
            }
          >
            <FileNavbar rootContent={browser.selector} />
            <FileToolbar layout="inline">
              {!browser.browserProps.hideToolbarInfo && <ToobarInfo files={browser.files} measurement={browser.measurement} />}
            </FileToolbar>
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
                {String(summary.fileCount)}{" "}
                {kind === "restore" ? (summary.fileCount === 1n ? "restore item" : "restore items") : summary.fileCount === 1n ? "file" : "files"} ·{" "}
                {formatFilesize(Number(summary.totalBytes))}
                {summary.unknownSizeFileCount > 0n ? ` known · ${summary.unknownSizeFileCount} sizes unknown` : ""}
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
                  onChange={(event) => setVersionPolicy((current) => ({ ...current, mode: event.target.value as RestorePolicy["mode"], cutoff: undefined }))}
                >
                  <MenuItem value="latest">Latest saved version</MenuItem>
                  <MenuItem value="before">Latest archive at or before…</MenuItem>
                </TextField>
                {versionPolicy.mode === "before" && (
                  <Suspense fallback={<Box role="status">Loading date picker…</Box>}>
                    <RestoreTimePicker
                      value={versionPolicy.date}
                      disabled={busy}
                      onChange={(date) => setVersionPolicy((current) => ({ ...current, date, cutoff: undefined }))}
                      error={!validPolicy}
                    />
                  </Suspense>
                )}
                {entries.some((entry) => entry.version) && (
                  <Button disabled={busy} onClick={() => setEntries(followRestorePolicy)}>
                    Apply current policy to all
                  </Button>
                )}
                <RestoreDestinationPicker value={destination} onChange={chooseDestination} disabled={busy} usePreference={!initial} />
                {destinationError && <Feedback severity="error">{destinationError}</Feedback>}
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
                <PreviewPolicySelect generation={generation} value={previewPolicy} onChange={setPreviewPolicy} disabled={busy} />
                {!validPreview && <Feedback severity="warning">Choose a Preview option before preparing.</Feedback>}
                {previewPolicy !== PreviewPolicy.NONE && (
                  <FormControlLabel control={<Checkbox checked={force} disabled={busy} onChange={(_, checked) => setForce(checked)} />} label="Force rehash" />
                )}
              </Stack>
            )}
            {error && <Feedback severity="error">{error}</Feedback>}
            {estimateError && (
              <Feedback severity="error" action={<Button onClick={() => setEstimateAttempt((value) => value + 1)}>Retry estimate</Button>}>
                {estimateError}
              </Feedback>
            )}
            {unavailable && <Feedback severity="error">Some selected entries are unavailable. Remove them or select them again before preparing.</Feedback>}
            {entries.length > 0 && validPolicy && !unavailable && !summary && !estimateError && <Box role="status">Estimating selection…</Box>}
            {kind === "restore" && summary && summary.unmatchedVersionCount > 0n && !skipUnmatchedVersions && (
              <Feedback
                severity="warning"
                action={
                  cutoff !== undefined && (
                    <Button disabled={busy} onClick={() => setSkipConsent(consentKey)}>
                      Skip {String(summary.unmatchedVersionCount)} {summary.unmatchedVersionCount === 1n ? "item" : "items"}
                    </Button>
                  )
                }
              >
                {String(summary.unmatchedVersionCount)} {summary.unmatchedVersionCount === 1n ? "item has" : "items have"} no version matching this policy.
              </Feedback>
            )}
            {kind === "restore" && summary && summary.skippedVersionCount > 0n && (
              <Feedback
                severity="info"
                action={
                  <Button disabled={busy} onClick={() => setSkipConsent("")}>
                    Include again
                  </Button>
                }
              >
                {String(summary.skippedVersionCount)} {summary.skippedVersionCount === 1n ? "item" : "items"} skipped.
              </Feedback>
            )}
            {kind !== "restore" && summary && summary.missingOriginalCount > 0n && (
              <Alert severity="warning">
                {String(summary.missingOriginalCount)} {summary.missingOriginalCount === 1n ? "file has" : "files have"} no linked original and will be skipped.
              </Alert>
            )}
            {kind === "restore" && summary && summary.missingCopyCount > 0n && (
              <Alert severity="warning">
                {String(summary.missingCopyCount)} {summary.missingCopyCount === 1n ? "item has" : "items have"} no usable archived copy.
              </Alert>
            )}
            {kind === "restore" && summary && summary.ignoredOutputCount > 0n && (
              <Alert severity="warning">
                {String(summary.ignoredOutputCount)} outputs match Ignore rules. They will be restored without linking them to Library files.
              </Alert>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button
            variant="contained"
            disabled={!canCreate || estimating || (kind === "archive" && previewPolicy !== PreviewPolicy.NONE && !generation.available)}
            onClick={() => void create()}
          >
            {busy ? "Preparing…" : `Prepare ${labels[kind].toLowerCase()}`}
          </Button>
          <Button disabled={busy} onClick={() => setConfiguring(false)}>
            Cancel
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
            const next = await loadRestoreVersionEntry(version);
            setEntries((current) => [
              ...current.filter(
                (entry) => entry.key !== choosing.key && entry.key !== next.key && !(entry.selection && !entry.isDir && entry.fileID === next.fileID),
              ),
              next,
            ]);
          }}
        />
      )}
      {browser.dialog}
    </Box>
  );
};
