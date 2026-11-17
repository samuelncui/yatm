import { t } from "@lingui/core/macro";
import { FileBrowser } from "@/components/file-browser";
import { SettingsSection } from "@/components/settings-page";
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
  type CreateArchiveJobRequest,
  type CreateRestoreJobRequest,
  type SelectionInspectionResult,
} from "@/entity";
import { RefreshListAction } from "@/actions";
import { useFileBrowser } from "@/components/use-file-browser";
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
import { parseTargetMediaCount } from "./archive-preferences";
import { useLatestCreation } from "./latest-creation";

const AddSelection = defineFileAction({ id: "add_job_selection", requiresSelection: true, button: { name: "Add to list", toolbar: true, contextMenu: true } });
type SelectionKind = "archive" | "restore";
const kindLabel = (kind: SelectionKind) =>
  kind === "archive" ? t({ message: "Archive", context: "Job kind" }) : t({ message: "Restore", context: "Job kind" });
const RestoreTimePicker = lazy(() => import("@/components/restore-time-picker"));

export const SelectionJobPage = ({ kind }: { kind: SelectionKind }) => {
  const route = useLocation();
  const id = new URLSearchParams(route.search).get("recreate");
  return id !== null ? (
    <SelectionJobRecreate key={route.key} kind={kind} id={id}>
      {(initial) => <SelectionJobForm kind={kind} initial={initial} />}
    </SelectionJobRecreate>
  ) : (
    <NewSelectionJob key={kind} kind={kind} />
  );
};

type LatestCreation = { kind: "archive"; request: CreateArchiveJobRequest } | { kind: "restore"; request: CreateRestoreJobRequest };
const loadLatestArchive = async (signal: AbortSignal): Promise<LatestCreation | undefined> => {
  const { request } = await archiveJobCli.getCreation({ id: 0n, latest: true }, { abort: signal }).response;
  return request && { kind: "archive", request };
};
const loadLatestRestore = async (signal: AbortSignal): Promise<LatestCreation | undefined> => {
  const { request } = await restoreJobCli.getCreation({ id: 0n, latest: true }, { abort: signal }).response;
  return request && { kind: "restore", request };
};

/** A new Job's form starts its options from the most recently created Job of its kind. */
const NewSelectionJob = ({ kind }: { kind: SelectionKind }) => {
  const latest = useLatestCreation(kind === "archive" ? loadLatestArchive : loadLatestRestore);
  // The form reads its starting options once, so it mounts when the read settles.
  if (!latest.settled) return null;
  return <SelectionJobForm kind={kind} last={latest.value} />;
};

const SelectionJobForm = ({ kind, initial, last }: { kind: SelectionKind; initial?: SelectionCreation; last?: LatestCreation }) => {
  // Recreate keeps the original Job's options; a new Job starts from the last created one's.
  const lastArchive = !initial && last?.kind === "archive" ? last.request : undefined;
  const lastRestore = !initial && last?.kind === "restore" ? last.request.spec : undefined;
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
  const [previewPolicy, setPreviewPolicy] = useState<PreviewPolicy>(
    // A Recreate keeps an unrecorded policy unset, so the form asks for one.
    initial?.kind === "archive" ? initial.request.previewPolicy : lastArchive?.previewPolicy || PreviewPolicy.NONE,
  );
  const validPreview = [PreviewPolicy.NONE, PreviewPolicy.MISSING_ONLY, PreviewPolicy.REGENERATE_ALL].includes(previewPolicy);
  const generation = usePreviewGeneration(kind === "archive" && configuring);
  const [force, setForce] = useState(initial?.kind === "archive" ? initial.request.forceRehash : !!lastArchive?.forceRehash);
  const [writeSignatureCache, setWriteSignatureCache] = useState(
    (initial?.kind === "archive" ? initial.request.spec : lastArchive?.spec)?.writeSignatureCache ?? true,
  );
  // A Restore chooses the Media files it reads and the restored files separately.
  const [restoreCache, setRestoreCache] = useState(() => {
    const spec = initial?.kind === "restore" ? initial.request.spec : lastRestore;
    return { source: spec?.writeSourceSignatureCache ?? true, target: spec?.writeTargetSignatureCache ?? true };
  });
  // An empty target Media count is off.
  const [targetMedia, setTargetMedia] = useState(String((initial?.kind === "archive" ? initial.request.spec : lastArchive?.spec)?.targetMediaCount ?? ""));
  const targetMediaCount = kind === "archive" ? parseTargetMediaCount(targetMedia) : undefined;
  const validTarget = kind !== "archive" || targetMedia.trim() === "" || targetMediaCount !== undefined;
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
  const priorityLabel = String(priority);
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
    runUIAction(
      async () => {
        const reply = await filesCli.getVersion({ id: BigInt(initialVersion) }).response;
        if (!active) return;
        if (!reply.version) throw new Error(t`This saved version no longer exists.`);
        const entry = await loadRestoreVersionEntry(reply.version);
        if (active) merge([entry]);
      },
      t`Could not add saved version`,
    );
    return () => {
      active = false;
    };
  }, [initial, initialVersion, kind, merge]);
  useEffect(() => {
    const selected = location.state?.selections as { selection: FileSelection; name: string; path: string; fileID?: string; isDir?: boolean }[] | undefined;
    if (initial || kind !== "archive" || !selected) return;
    let active = true;
    runUIAction(
      async () => {
        const additions: Entry[] = [];
        for (const entry of selected) {
          const id = entry.selection.target.oneofKind === "library" ? entry.selection.target.library.fileId : entry.fileID ? BigInt(entry.fileID) : undefined;
          if (id === undefined) {
            additions.push({ ...entry, key: FileSelection.toJsonString(entry.selection) });
            continue;
          }
          const reply = (await filesCli.get({ reference: libraryDirectoryReference(String(id)) }).response).detail;
          if (!reply) throw new Error(t`File details are unavailable.`);
          const path = reply.organization?.path ?? reply.entry?.name ?? "";
          additions.push({
            ...entry,
            key: FileSelection.toJsonString(entry.selection),
            name: reply.entry?.name ?? t`Library`,
            target: path,
            isDir: reply.entry?.kind === EntryKind.DIRECTORY,
            path: entry.selection.target.oneofKind === "library" ? path || t`Library` : entry.path,
          });
        }
        if (!active) return;
        merge(additions);
        navigate(location.pathname, { replace: true, state: null });
      },
      t`Could not load selected files`,
    );
    return () => {
      active = false;
    };
  }, [initial, kind, location.state, location.pathname, merge, navigate]);

  const addFiles = async (files: FileData[]) => {
    if (browser.loadError || files.some((file) => !canAdd(file))) return;
    setBusy(true);
    setError("");
    try {
      merge(await selectionEntriesForFiles(kind, files, browser.scope, browser.source?.kind === "location" ? browser.source.name : t`Library`));
    } catch (error) {
      setError(errorMessage(error, t`Could not add selection`));
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
    if (submitting.current || !canCreate) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const selections = summary.selections;
      const reply =
        kind === "archive"
          ? await archiveJobCli.create({
              priority,
              spec: ArchiveJobSpec.create({ selections, writeSignatureCache, targetMediaCount }),
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
                writeSourceSignatureCache: restoreCache.source,
                writeTargetSignatureCache: restoreCache.target,
              }),
            }).response;
      if (!reply.job) throw new Error(t`Job was not returned. Check Jobs before retrying.`);
      submitted(revision);
      navigate(`/jobs/${reply.job.id}`);
    } catch (error) {
      setError(errorMessage(error, t`Could not create job`));
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
  const inspectionKey = `${JSON.stringify(inspection, (_, value) => (typeof value === "bigint" ? value.toString() : value))}|${targetMediaCount ?? ""}`;
  const summary = validPolicy && validTarget && review?.key === inspectionKey ? review.result : undefined;
  useEffect(() => {
    if (!entries.length || !validPolicy || !validTarget || unavailable) return;
    let active = true;
    const timer = setTimeout(() => {
      setEstimating(true);
      setEstimateError("");
      void (kind === "restore" ? restoreJobCli.estimate(inspection) : archiveJobCli.estimate({ selections: inspection.selections, targetMediaCount })).response
        .then((response) => {
          if (!response.result) throw new Error(t`Selection estimate is missing`);
          if (active) setReview({ key: inspectionKey, result: response.result });
        })
        .catch((error) => {
          if (active) setEstimateError(errorMessage(error, t`Could not estimate selection`));
        })
        .finally(() => {
          if (active) setEstimating(false);
        });
    }, 350);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [inspection, inspectionKey, estimateAttempt, entries.length, validPolicy, validTarget, targetMediaCount, kind, unavailable]);
  const canCreate =
    validPolicy &&
    validTarget &&
    (kind !== "archive" || (validPreview && (previewPolicy === PreviewPolicy.NONE || generation.available))) &&
    !unavailable &&
    !estimating &&
    !!summary &&
    summary.fileCount > 0n &&
    !busy &&
    (kind !== "restore" || (summary.missingCopyCount === 0n && (summary.unmatchedVersionCount === 0n || skipUnmatchedVersions) && !!destinationID));
  const canAdd = (file: FileData | null) => canAddFileToSelection(kind, file);
  const fileCount = Number(summary?.fileCount ?? 0n);
  const totalSize = formatFilesize(Number(summary?.totalBytes ?? 0n));
  const unknownSizes = Number(summary?.unknownSizeFileCount ?? 0n);
  const unmatchedCount = Number(summary?.unmatchedVersionCount ?? 0n);
  const skippedCount = Number(summary?.skippedVersionCount ?? 0n);
  const targetReachedCount = Number(summary?.skippedTargetReachedCount ?? 0n);
  const missingOriginalCount = Number(summary?.missingOriginalCount ?? 0n);
  const missingCopyCount = Number(summary?.missingCopyCount ?? 0n);
  const ignoredCount = String(summary?.ignoredOutputCount ?? 0n);
  return (
    <Box className="browser-box selection-job-page">
      <div className="selection-job-workspace">
        <Stack component="section" className="selection-browser" spacing={1.5}>
          {initial && (
            <Feedback severity="info">{t`Review the original selections and options, then submit to create a new Job. Priority: ${priorityLabel}.`}</Feedback>
          )}
          {initial?.reason && <Feedback severity="warning">{initial.reason}</Feedback>}
          {unavailable && <Feedback severity="error">{t`Some selected entries are unavailable. Remove them or select them again before preparing.`}</Feedback>}
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
            <FileToolbar layout="inline" {...browser.toolbarProps}>
              {!browser.browserProps.hideToolbarInfo && <ToobarInfo files={browser.files} measurement={browser.measurement} />}
            </FileToolbar>
            <FileList {...browser.listProps} />
            <FileContextMenu />
          </FileBrowser>
        </Stack>
        <section className="selection-todo" aria-label={kind === "archive" ? t`Archive selection` : t`Restore selection`}>
          <SelectionWaitlist
            kind={kind}
            label={kindLabel(kind)}
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
                  {kindLabel(kind)}…
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
        <DialogTitle id={creationTitleID}>{kindLabel(kind)}</DialogTitle>
        <DialogContent dividers>
          <Stack spacing={2}>
            {summary && (
              <strong>
                {kind === "restore"
                  ? fileCount === 1
                    ? t`${fileCount} restore item`
                    : t`${fileCount} restore items`
                  : fileCount === 1
                    ? t`${fileCount} file`
                    : t`${fileCount} files`}
                {" · "}
                {unknownSizes > 0 ? t`${totalSize} known · ${unknownSizes} sizes unknown` : totalSize}
              </strong>
            )}
            {kind === "restore" ? (
              <>
                <TextField
                  select
                  fullWidth
                  label={t`Versions`}
                  value={versionPolicy.mode}
                  disabled={busy}
                  onChange={(event) => setVersionPolicy((current) => ({ ...current, mode: event.target.value as RestorePolicy["mode"], cutoff: undefined }))}
                >
                  <MenuItem value="latest">{t`Latest saved version`}</MenuItem>
                  <MenuItem value="before">{t`Latest archive at or before…`}</MenuItem>
                </TextField>
                {versionPolicy.mode === "before" && (
                  <Suspense fallback={<Box role="status">{t`Loading date picker…`}</Box>}>
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
                    {t`Apply current policy to all`}
                  </Button>
                )}
                <RestoreDestinationPicker value={destination} onChange={chooseDestination} disabled={busy} last={lastRestore?.destination} />
                {destinationError && <Feedback severity="error">{destinationError}</Feedback>}
                <SettingsSection
                  title={t`Advanced`}
                  collapsible
                  description={
                    [
                      allowDamagedCopies && t`Damaged copies allowed`,
                      !restoreCache.source && t`No signature cache on Media`,
                      !restoreCache.target && t`No signature cache on restored files`,
                    ]
                      .filter(Boolean)
                      .join(" · ") || undefined
                  }
                >
                  <FormControlLabel
                    control={<Checkbox checked={allowDamagedCopies} disabled={busy} onChange={(_, checked) => setAllowDamagedCopies(checked)} />}
                    label={t`Allow damaged copies`}
                  />
                  <FormControlLabel
                    control={
                      <Checkbox
                        checked={restoreCache.source}
                        disabled={busy}
                        onChange={(_, source) => setRestoreCache((current) => ({ ...current, source }))}
                      />
                    }
                    label={t`Write signature cache onto Media`}
                  />
                  <span>{t`Stores the signature of each file read from a Volume or LTFS Tape on that file. On an LTFS Tape this rewrites its Index.`}</span>
                  <FormControlLabel
                    control={
                      <Checkbox
                        checked={restoreCache.target}
                        disabled={busy}
                        onChange={(_, target) => setRestoreCache((current) => ({ ...current, target }))}
                      />
                    }
                    label={t`Write signature cache onto restored files`}
                  />
                  <span>{t`Stores each restored file's signature in an extended attribute so later reads can reuse it. File content is unchanged.`}</span>
                </SettingsSection>
                {allowDamagedCopies && (
                  <Alert severity="warning">
                    {t`Damaged copies allowed. Complete damaged output may be retained for recovery. It is not a verified saved version.`}
                  </Alert>
                )}
              </>
            ) : (
              <Stack spacing={2}>
                <TextField
                  fullWidth
                  label={t`Target Media count`}
                  value={targetMedia}
                  disabled={busy}
                  error={!validTarget}
                  slotProps={{ htmlInput: { inputMode: "numeric" } }}
                  onChange={(event) => setTargetMedia(event.target.value)}
                  helperText={
                    validTarget
                      ? t`Optional. Content already on this many Media or more is skipped. Files whose current content is not confirmed are archived normally.`
                      : t`Enter a whole number of at least 1, or leave it empty.`
                  }
                />
                <PreviewPolicySelect generation={generation} value={previewPolicy} onChange={setPreviewPolicy} disabled={busy} />
                {!validPreview && <Feedback severity="warning">{t`Choose a Preview option before preparing.`}</Feedback>}
                {previewPolicy !== PreviewPolicy.NONE && (
                  <SettingsSection title={t`Advanced Preview`} collapsible description={force ? t`Source hashes will be rechecked` : undefined}>
                    <FormControlLabel
                      control={<Checkbox checked={force} disabled={busy} onChange={(_, checked) => setForce(checked)} />}
                      label={t`Recheck source hashes for previews`}
                    />
                    <span>{t`Reads source files again to identify previews before copying. The Archive copy hashes content independently.`}</span>
                  </SettingsSection>
                )}
                <SettingsSection title={t`Advanced`} collapsible description={writeSignatureCache ? undefined : t`No signature cache`}>
                  <FormControlLabel
                    control={<Checkbox checked={writeSignatureCache} disabled={busy} onChange={(_, checked) => setWriteSignatureCache(checked)} />}
                    label={t`Write signature cache`}
                  />
                  <span>{t`Stores each read original's signature in an extended attribute so later reads can reuse it. File content is unchanged.`}</span>
                </SettingsSection>
              </Stack>
            )}
            {error && <Feedback severity="error">{error}</Feedback>}
            {estimateError && (
              <Feedback severity="error" action={<Button onClick={() => setEstimateAttempt((value) => value + 1)}>{t`Retry estimate`}</Button>}>
                {estimateError}
              </Feedback>
            )}
            {unavailable && (
              <Feedback severity="error">{t`Some selected entries are unavailable. Remove them or select them again before preparing.`}</Feedback>
            )}
            {entries.length > 0 && validPolicy && !unavailable && !summary && !estimateError && <Box role="status">{t`Estimating selection…`}</Box>}
            {kind === "restore" && summary && summary.unmatchedVersionCount > 0n && !skipUnmatchedVersions && (
              <Feedback
                severity="warning"
                action={
                  cutoff !== undefined && (
                    <Button disabled={busy} onClick={() => setSkipConsent(consentKey)}>
                      {unmatchedCount === 1 ? t`Skip ${unmatchedCount} item` : t`Skip ${unmatchedCount} items`}
                    </Button>
                  )
                }
              >
                {unmatchedCount === 1
                  ? t`${unmatchedCount} item has no version matching this policy.`
                  : t`${unmatchedCount} items have no version matching this policy.`}
              </Feedback>
            )}
            {kind === "restore" && summary && summary.skippedVersionCount > 0n && (
              <Feedback
                severity="warning"
                action={
                  skipUnmatchedVersions && (
                    <Button disabled={busy} onClick={() => setSkipConsent("")}>
                      {t`Include again`}
                    </Button>
                  )
                }
              >
                {skippedCount === 1
                  ? t`${skippedCount} item has no version to restore and is left out.`
                  : t`${skippedCount} items have no version to restore and are left out.`}
              </Feedback>
            )}
            {kind === "archive" && summary && targetMediaCount !== undefined && (
              <Feedback severity="info">
                {targetReachedCount === 1
                  ? t`Target Media count ${targetMediaCount}: ${targetReachedCount} file already reaches it and will be skipped.`
                  : t`Target Media count ${targetMediaCount}: ${targetReachedCount} files already reach it and will be skipped.`}
              </Feedback>
            )}
            {kind !== "restore" && summary && summary.missingOriginalCount > 0n && (
              <Alert severity="warning">
                {missingOriginalCount === 1
                  ? t`${missingOriginalCount} file has no linked original and will be skipped.`
                  : t`${missingOriginalCount} files have no linked original and will be skipped.`}
              </Alert>
            )}
            {kind === "restore" && summary && summary.missingCopyCount > 0n && (
              <Alert severity="warning">
                {missingCopyCount === 1 ? t`${missingCopyCount} item has no usable archived copy.` : t`${missingCopyCount} items have no usable archived copy.`}
              </Alert>
            )}
            {kind === "restore" && summary && summary.ignoredOutputCount > 0n && (
              <Alert severity="warning">{t`${ignoredCount} outputs match Ignore rules. They will be restored without linking them to Library files.`}</Alert>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button variant="contained" disabled={!canCreate} onClick={() => void create()}>
            {busy ? t`Preparing…` : kind === "archive" ? t`Prepare archive` : t`Prepare restore`}
          </Button>
          <Button disabled={busy} onClick={() => setConfiguring(false)}>
            {t`Cancel`}
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
