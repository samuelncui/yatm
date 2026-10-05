import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { Button, Dialog, DialogActions, DialogContent, DialogTitle, Stack } from "@mui/material";
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
  const { request, unavailableReason } = await archiveJobCli.getCreation({ id }, { abort: signal }).response;
  signal.throwIfAborted();
  if (!request) throw new Error(unavailableReason || "The original Archive inputs are unavailable.");
  return { kind: "archive", request, reason: unavailableReason, entries: await readCreationEntries(request.spec?.selections ?? [], [], signal) };
};

const loadRestore = async (id: bigint, signal: AbortSignal): Promise<SelectionCreation> => {
  const { request, unavailableReason } = await restoreJobCli.getCreation({ id }, { abort: signal }).response;
  signal.throwIfAborted();
  if (!request) throw new Error(unavailableReason || "The original Restore inputs are unavailable.");
  const entries = await readCreationEntries(request.spec?.selections ?? [], request.spec?.fileVersionIds ?? [], signal);
  const result: SelectionCreation = { kind: "restore", request, entries, reason: unavailableReason };
  const destination = request.spec?.destination;
  if (!destination) {
    result.destinationError = "The original Restore destination is unavailable. Choose a destination.";
    return result;
  }
  try {
    const { location } = await locationCli.get({ id: destination.locationId }, { abort: signal }).response;
    signal.throwIfAborted();
    if (!location || location.id !== destination.locationId) throw new Error("This Location is unavailable.");
    const { detail } = await filesCli.get({ reference: locationDirectoryReference(String(location.id), destination.path) }, { abort: signal }).response;
    signal.throwIfAborted();
    if (!canChooseLocationDirectory(detail?.entry, location.id, destination.path)) throw new Error("This destination is unavailable.");
    result.destination = { location, path: destination.path };
  } catch (failure) {
    signal.throwIfAborted();
    result.destinationError = `Location ${destination.locationId}/${destination.path}: ${errorMessage(failure, "Could not read the destination")} Choose a destination.`;
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
  const [applied, setApplied] = useState(false);
  const [changed, setChanged] = useState(false);
  useEffect(() => {
    if (applied || approved === undefined) return;
    if (store.getState().selections[creation.kind].revision !== approved) {
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
    <Dialog open fullWidth maxWidth="sm" aria-labelledby="replace-job-selections">
      <DialogTitle id="replace-job-selections">Replace unsent selections?</DialogTitle>
      <DialogContent>
        <Stack spacing={2}>
          <span>
            Replace the current {entries.length} {creation.kind === "archive" ? "Archive" : "Restore"} selections with {creation.entries.length} selections from
            this Job?
          </span>
          <span>Review the creation form before submitting.</span>
          {creation.reason && <Feedback severity="warning">{creation.reason}</Feedback>}
          {changed && <Feedback severity="warning">The selections changed while this form was opening. Review them before replacing.</Feedback>}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setApproved(revision)}>Replace selections</Button>
        <Button component={Link} to={`/${creation.kind}`}>
          Cancel
        </Button>
      </DialogActions>
    </Dialog>
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
