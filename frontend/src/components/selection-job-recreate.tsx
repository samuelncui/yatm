import { t } from "@lingui/core/macro";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { Stack } from "@mui/material";
import { ActionDialog } from "./action-dialog";
import { archiveJobCli, restoreJobCli, locationCli, filesCli } from "@/api";
import type { CreateArchiveJobRequest, CreateRestoreJobRequest } from "@/entity";
import { errorMessage } from "@/tools";
import { dateFromNs } from "@/tools/time";
import { useAppStore, useSelections } from "@/state/react";
import { encodeSelection, selectionActions, type SelectionEntry, type SelectionKind } from "@/state/selections";
import { JobCreationLoader, readCreationEntries } from "./job-creation";
import { canChooseLocationDirectory } from "./directory-picker";
import { locationDirectoryReference } from "./files-browser";
import { Feedback } from "./feedback";
import type { RestorePolicy } from "./restore-version-policy";
import type { RestoreTarget } from "./restore-destination";

export type SelectionCreation = ({ kind: "archive"; request: CreateArchiveJobRequest } | { kind: "restore"; request: CreateRestoreJobRequest }) & {
  entries: SelectionEntry[];
  reason: string;
  destination?: RestoreTarget;
  destinationError?: string;
};

const loadArchive = async (id: bigint, signal: AbortSignal): Promise<SelectionCreation> => {
  const { request, unavailableReason } = await archiveJobCli.getCreation({ id, latest: false }, { abort: signal }).response;
  signal.throwIfAborted();
  if (!request) throw new Error(unavailableReason || t`The original Archive inputs are unavailable.`);
  return { kind: "archive", request, reason: unavailableReason, entries: await readCreationEntries(request.spec?.selections ?? [], [], signal) };
};

const loadRestore = async (id: bigint, signal: AbortSignal): Promise<SelectionCreation> => {
  const { request, unavailableReason } = await restoreJobCli.getCreation({ id, latest: false }, { abort: signal }).response;
  signal.throwIfAborted();
  if (!request) throw new Error(unavailableReason || t`The original Restore inputs are unavailable.`);
  const entries = await readCreationEntries(request.spec?.selections ?? [], request.spec?.fileVersionIds ?? [], signal);
  const result: SelectionCreation = { kind: "restore", request, entries, reason: unavailableReason };
  const destination = request.spec?.destination;
  if (!destination) {
    result.destinationError = t`The original Restore destination is unavailable. Choose a destination.`;
    return result;
  }
  try {
    const { location } = await locationCli.get({ id: destination.locationId }, { abort: signal }).response;
    signal.throwIfAborted();
    if (!location || location.id !== destination.locationId) throw new Error(t`This Location is unavailable.`);
    const { detail } = await filesCli.get({ reference: locationDirectoryReference(String(location.id), destination.path) }, { abort: signal }).response;
    signal.throwIfAborted();
    if (!canChooseLocationDirectory(detail?.entry)) throw new Error(t`This destination is unavailable.`);
    result.destination = { location, path: destination.path };
  } catch (failure) {
    signal.throwIfAborted();
    const locationID = String(destination.locationId);
    const path = destination.path;
    const reason = errorMessage(failure, t`Could not read the destination`);
    result.destinationError = t`Location ${locationID}/${path}: ${reason} Choose a destination.`;
  }
  return result;
};

function creationPolicy(creation: SelectionCreation): RestorePolicy | undefined {
  if (creation.kind !== "restore") return undefined;
  const cutoff = creation.request.spec?.versionPolicy?.beforeAtNs;
  if (cutoff === undefined) return { mode: "latest", date: "" };
  // Date is presentation only. The untouched request retains its exact integer.
  const date = dateFromNs(cutoff)?.toISOString() ?? "";
  return { mode: "before", date, cutoff: { valueNs: String(cutoff), date } };
}

const ApplyCreation = ({
  creation,
  startingRevision,
  children,
}: {
  creation: SelectionCreation;
  startingRevision: number;
  children: (creation: SelectionCreation) => ReactNode;
}) => {
  const store = useAppStore();
  const { entries, revision } = useSelections(creation.kind);
  const [approved, setApproved] = useState<number | undefined>(() => (!entries.length && revision === startingRevision ? revision : undefined));
  const confirmed = useRef(false);
  const navigate = useNavigate();
  const currentCount = entries.length;
  const newCount = creation.entries.length;
  const [applied, setApplied] = useState(false);
  const [changed, setChanged] = useState(false);
  useEffect(() => {
    if (applied || approved === undefined) return;
    if (store.getState().selections[creation.kind].revision !== approved) {
      // The dialog stays open for another decision, so Cancel must leave again.
      confirmed.current = false;
      setApproved(undefined);
      setChanged(true);
      return;
    }
    store.dispatch(
      selectionActions.creationLoaded({
        kind: creation.kind,
        revision: approved,
        entries: creation.entries.map(encodeSelection),
        policy: creationPolicy(creation),
      }),
    );
    setApplied(true);
  }, [approved, applied, creation, store]);
  if (applied) return children(creation);
  return (
    <ActionDialog
      title={t`Replace unsent selections?`}
      confirmLabel={t`Replace selections`}
      onConfirm={() => {
        confirmed.current = true;
        setApproved(revision);
      }}
      onClose={() => {
        if (!confirmed.current) navigate(`/${creation.kind}`);
      }}
    >
      <Stack spacing={2}>
        <span>
          {creation.kind === "archive"
            ? t`Replace the current ${currentCount} Archive selections with ${newCount} selections from this Job?`
            : t`Replace the current ${currentCount} Restore selections with ${newCount} selections from this Job?`}
        </span>
        <span>{t`Review the creation form before submitting.`}</span>
        {creation.reason && <Feedback severity="warning">{creation.reason}</Feedback>}
        {changed && <Feedback severity="warning">{t`The selections changed while this form was opening. Review them before replacing.`}</Feedback>}
      </Stack>
    </ActionDialog>
  );
};

export const SelectionJobRecreate = ({ kind, id, children }: { kind: SelectionKind; id: string; children: (creation: SelectionCreation) => ReactNode }) => {
  const store = useAppStore();
  const [startingRevision] = useState(() => store.getState().selections[kind].revision);
  return (
    <JobCreationLoader id={id} load={kind === "archive" ? loadArchive : loadRestore} cancelTo={`/${kind}`}>
      {(creation) => (
        <ApplyCreation creation={creation} startingRevision={startingRevision}>
          {children}
        </ApplyCreation>
      )}
    </JobCreationLoader>
  );
};
