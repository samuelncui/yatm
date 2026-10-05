import { Feedback } from "@/components/feedback";
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import {
  Button,
  Card,
  Checkbox,
  CircularProgress,
  FormControlLabel,
  IconButton,
  List,
  ListItem,
  ListItemText,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import Close from "@mui/icons-material/Close";
import { mediaCli, filesCli, locationCli, scanJobCli } from "@/api";
import { FileScope, FileSelection, Location, Media, MediaKind, MediaAccess, PreviewPolicy, ScanJobSpec, ScanResultPolicy, ScanSignaturePolicy } from "@/entity";
import { errorMessage } from "@/tools";
import { type InitialScan, type ScanOptions } from "@/pages/scan-input";
import { resultForSource, saveScanPreferences } from "@/pages/scan-preferences";
import { LocationSearchSelect, MediaSearchSelect } from "./catalog-search-select";
import { ImportPositionsDialog, useLibraryAdmission } from "./import-positions-dialog";
import { PreviewPolicySelect, usePreviewGeneration } from "./preview-policy-select";
import { ScanSelectionDialog } from "./scan-selection-dialog";
import { scanSelectionEntry, scanSelectionKey, type ScanSelectionEntry } from "./scan-selection";
import { filesEntryData, locationDirectoryReference } from "./files-browser";
import { ActionRow } from "./action-row";

type ScanTarget =
  | { kind: "location"; location: Location | null; partial: boolean; entries: ScanSelectionEntry[] }
  | { kind: "files"; entries: ScanSelectionEntry[] }
  | { kind: "media"; media: Media | null };

const emptyTarget = (kind: ScanTarget["kind"]): ScanTarget => {
  if (kind === "location") return { kind, location: null, partial: false, entries: [] };
  if (kind === "media") return { kind, media: null };
  return { kind, entries: [] };
};

function previewAvailable(target: ScanTarget) {
  if (target.kind !== "media") return true;
  return (
    !!target.media &&
    target.media.kind !== MediaKind.TAPE &&
    [MediaAccess.RANDOM, MediaAccess.CONCURRENT_RANDOM].includes(target.media.capabilities?.read ?? MediaAccess.UNSPECIFIED)
  );
}

// All entry points submit through this conversion; labels never become identities.
export function scanRequest(target: ScanTarget, options: ScanOptions) {
  const selections = target.kind === "files" || (target.kind === "location" && target.partial) ? target.entries.map((entry) => entry.selection) : [];
  if (target.kind === "location" && !target.partial && target.location) {
    selections.push(
      FileSelection.create({
        target: { oneofKind: "location", location: { locationId: target.location.id, path: "" } },
        scope: FileScope.ALL,
      }),
    );
  }
  return ScanJobSpec.create({
    mediaId: target.kind === "media" ? target.media?.id : 0n,
    selections,
    signaturePolicy: options.resultPolicy === ScanResultPolicy.VERIFY_COPIES ? ScanSignaturePolicy.FORCE_READ : options.signaturePolicy,
    resultPolicy: options.resultPolicy,
    compareLibrary: options.compare,
    previewPolicy: previewAvailable(target) ? options.previewPolicy : PreviewPolicy.NONE,
  });
}

export const ScanForm = ({ initial }: { initial: InitialScan }) => {
  const navigate = useNavigate();
  const [target, setTarget] = useState<ScanTarget>(() => (initial.target.kind === "files" ? initial.target : emptyTarget(initial.target.kind)));
  const [options, setOptions] = useState(initial.options);
  const [error, setError] = useState(initial.error);
  const [loadingTarget, setLoadingTarget] = useState(initial.target.kind !== "files" && !!initial.target.id);
  const [choosing, setChoosing] = useState(false);
  const [busy, setBusy] = useState(false);
  const admission = useLibraryAdmission(target.kind === "media" ? target.media?.id : undefined);
  const submitting = useRef(false);
  const lookup = useRef<AbortController | null>(null);
  const active = useRef(true);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  useEffect(() => {
    const source = initial.target;
    if (source.kind === "files" || !source.id) return;
    const controller = new AbortController();
    lookup.current = controller;
    const load = async () => {
      if (source.kind === "location") {
        const location = (await locationCli.get({ id: BigInt(source.id) }, { abort: controller.signal }).response).location;
        if (controller.signal.aborted) return;
        if (!location || String(location.id) !== source.id) throw new Error("The selected Location is unavailable. Choose a Location.");
        if (!initial.explicit && location.rootPath !== initial.saved?.location?.rootPath) throw new Error("The last Location has changed. Select it again.");
        const entries: ScanSelectionEntry[] = [];
        // Routed path shortcuts resolve only the requested roots, never descendants.
        for (let start = 0; start < (source.paths?.length ?? 0); start += 20) {
          const page = await Promise.all(
            source.paths!.slice(start, start + 20).map(async (path) => {
              const detail = await filesCli.get({ reference: locationDirectoryReference(source.id, path) }, { abort: controller.signal }).response;
              const entry = detail.detail?.entry;
              if (!entry) throw new Error("The selected entry is unavailable.");
              const reference = entry.reference?.target;
              if (reference?.oneofKind !== "location" || reference.location.locationId !== location.id)
                throw new Error("The selected Location has changed. Choose the range again.");
              return scanSelectionEntry(filesEntryData(entry), FileScope.ALL, location.name);
            }),
          );
          if (controller.signal.aborted) return;
          entries.push(...page);
        }
        setTarget({ kind: "location", location, partial: !!source.paths?.length, entries });
        return;
      }
      const reply = await mediaCli.list({ param: { oneofKind: "ids", ids: { ids: [BigInt(source.id)] } } }, { abort: controller.signal }).response;
      if (controller.signal.aborted) return;
      const media = reply.media.find((item) => String(item.id) === source.id);
      if (!media || ![MediaKind.TAPE, MediaKind.VOLUME].includes(media.kind))
        throw new Error(`Media ${source.id} is no longer available. Choose another Media.`);
      if (!initial.explicit && (media.identity !== initial.saved?.media?.identity || media.kind !== initial.saved?.media?.kind))
        throw new Error("The last Media has changed. Select it again.");
      setTarget({ kind: "media", media });
    };
    void load()
      .catch((failure) => {
        if (!controller.signal.aborted) setError(errorMessage(failure, "Could not load selected source"));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoadingTarget(false);
      });
    return () => controller.abort();
  }, [initial]);

  const changeTarget = (next: ScanTarget) => {
    lookup.current?.abort();
    setLoadingTarget(false);
    setTarget(next);
    setOptions((current) => ({ ...current, resultPolicy: resultForSource(next.kind, current.resultPolicy) }));
    setError("");
  };
  const update = (patch: Partial<ScanOptions>) => setOptions((current) => ({ ...current, ...patch }));
  const setEntries = (entries: ScanSelectionEntry[]) => {
    setTarget((current) => (current.kind === "media" ? current : { ...current, entries }));
    setError("");
  };
  const selectedRange = target.kind === "files" || (target.kind === "location" && target.partial);
  const entries = target.kind === "media" ? [] : target.entries;
  const canPreview = previewAvailable(target);
  const generation = usePreviewGeneration();
  const verifying = options.resultPolicy === ScanResultPolicy.VERIFY_COPIES;
  const ready =
    !loadingTarget &&
    !entries.some((entry) => entry.unavailableReason) &&
    (options.previewPolicy === PreviewPolicy.NONE || (canPreview && generation.available)) &&
    (target.kind === "media" ? !!target.media : target.kind === "files" ? entries.length > 0 : !!target.location && (!target.partial || entries.length > 0));
  const submit = async () => {
    if (submitting.current || !ready) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const spec = scanRequest(target, options);
      const reply = await scanJobCli.create({ priority: initial.priority ?? 1n, spec }).response;
      if (!reply.job) throw new Error("Job was not returned");
      saveScanPreferences({
        ...initial.saved,
        kind: target.kind === "files" ? (initial.saved?.kind ?? "location") : target.kind,
        ...(target.kind === "location" && target.location ? { location: { id: String(target.location.id), rootPath: target.location.rootPath } } : {}),
        ...(target.kind === "media" && target.media
          ? { media: { id: String(target.media.id), identity: target.media.identity, kind: target.media.kind } }
          : {}),
        signaturePolicy: spec.signaturePolicy,
        resultPolicy: spec.resultPolicy,
        compare: spec.compareLibrary,
        previewPolicy: spec.previewPolicy,
      });
      if (active.current) navigate(`/jobs/${reply.job.id}`);
    } catch (failure) {
      if (active.current) setError(errorMessage(failure, "Could not create scan"));
    } finally {
      if (active.current) setBusy(false);
      submitting.current = false;
    }
  };
  return (
    <div className="job-create-page">
      <div className="short-form-page">
        <Card className="job-create-card" sx={{ borderRadius: "12px" }}>
          {initial.priority !== undefined && (
            <Feedback severity="info">
              Review the original selections and options, then submit to create a new Job. Priority: {String(initial.priority)}.
            </Feedback>
          )}
          {initial.creationReason && <Feedback severity="warning">{initial.creationReason}</Feedback>}
          <TextField
            select
            label="Source"
            value={target.kind}
            disabled={busy}
            onChange={(event) => changeTarget(emptyTarget(event.target.value as ScanTarget["kind"]))}
          >
            <MenuItem value="location">Location</MenuItem>
            <MenuItem value="files">Files</MenuItem>
            <MenuItem value="media">Media</MenuItem>
          </TextField>
          {target.kind === "location" && (
            <>
              <LocationSearchSelect
                value={target.location}
                disabled={busy}
                onChange={(location) => changeTarget({ kind: "location", location, partial: false, entries: [] })}
              />
              <TextField
                select
                label="Scope"
                value={target.partial ? "selected" : "all"}
                disabled={busy || loadingTarget || !target.location}
                onChange={(event) => setTarget({ ...target, partial: event.target.value === "selected" })}
              >
                <MenuItem value="all">Entire Location</MenuItem>
                <MenuItem value="selected">Selected files and folders</MenuItem>
              </TextField>
            </>
          )}
          {target.kind === "media" && <MediaSearchSelect value={target.media} disabled={busy} onChange={(media) => changeTarget({ kind: "media", media })} />}
          {loadingTarget && (
            <Stack direction="row" role="status" sx={{ gap: 1, alignItems: "center" }}>
              <CircularProgress size={16} />
              <Typography variant="body2">Loading source…</Typography>
            </Stack>
          )}
          {selectedRange && (
            <>
              <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
                <Typography variant="body2" sx={{ flex: 1 }}>
                  {entries.length} selected {entries.length === 1 ? "root" : "roots"}
                </Typography>
                <Button disabled={busy || loadingTarget} onClick={() => setChoosing(true)}>
                  Edit selection
                </Button>
                <Button disabled={busy || !entries.length} onClick={() => setEntries([])}>
                  Clear
                </Button>
              </Stack>
              {!!entries.length && (
                <List dense aria-label="Selected roots" sx={{ maxHeight: 220, overflowY: "auto", py: 0 }}>
                  {entries.map((entry, index) => (
                    <ListItem
                      key={`${scanSelectionKey(entry)}:${index}`}
                      secondaryAction={
                        <IconButton disabled={busy} aria-label={`Remove ${entry.path}`} onClick={() => setEntries(entries.filter((_, at) => at !== index))}>
                          <Close fontSize="small" />
                        </IconButton>
                      }
                    >
                      <ListItemText
                        primary={entry.name}
                        secondary={[entry.path, entry.unavailableReason].filter(Boolean).join(" · ")}
                        slotProps={{ primary: { noWrap: true }, secondary: { noWrap: true, title: entry.path } }}
                      />
                    </ListItem>
                  ))}
                </List>
              )}
            </>
          )}
          <TextField
            select
            label="Signatures"
            value={verifying ? ScanSignaturePolicy.FORCE_READ : options.signaturePolicy}
            disabled={busy || verifying}
            onChange={(event) => update({ signaturePolicy: Number(event.target.value) })}
          >
            <MenuItem value={ScanSignaturePolicy.KNOWN_ONLY}>Reuse known signatures only</MenuItem>
            <MenuItem value={ScanSignaturePolicy.FILL_MISSING}>Read new and changed files</MenuItem>
            <MenuItem value={ScanSignaturePolicy.FORCE_READ}>Read every file</MenuItem>
          </TextField>
          <TextField
            select
            label="Results"
            disabled={busy}
            value={options.resultPolicy}
            onChange={(event) => update({ resultPolicy: Number(event.target.value) })}
          >
            <MenuItem value={ScanResultPolicy.REPORT_ONLY}>View results only</MenuItem>
            {target.kind === "media" ? (
              [
                <MenuItem key="inventory" value={ScanResultPolicy.PUBLISH_INVENTORY}>
                  Update inventory
                </MenuItem>,
                <MenuItem key="verify" value={ScanResultPolicy.VERIFY_COPIES}>
                  Check recorded copies
                </MenuItem>,
              ]
            ) : (
              <MenuItem value={ScanResultPolicy.PUBLISH_ORIGINALS}>Update Library originals</MenuItem>
            )}
          </TextField>
          <FormControlLabel
            control={<Checkbox disabled={busy} checked={options.compare} onChange={(_, compare) => update({ compare })} />}
            label="Find matching Library content"
          />
          <PreviewPolicySelect
            generation={generation}
            value={options.previewPolicy}
            disabled={busy || (!canPreview && options.previewPolicy === PreviewPolicy.NONE)}
            onChange={(previewPolicy) => update({ previewPolicy })}
            helperText={
              target.kind === "media" && target.media && !canPreview
                ? target.media.kind === MediaKind.TAPE
                  ? "Not supported on Tape"
                  : "Previews require random-readable Media"
                : undefined
            }
          />
          {!canPreview && options.previewPolicy !== PreviewPolicy.NONE && (
            <Feedback severity="error">The selected Media cannot generate Previews. Choose different Media or turn Previews off.</Feedback>
          )}
          {entries.some((entry) => entry.unavailableReason) && (
            <Feedback severity="error">Some selected entries are unavailable. Remove them or select them again before starting.</Feedback>
          )}
          {error && <Feedback severity="error">{error}</Feedback>}
          <ActionRow className="job-create-actions">
            <Button variant="contained" disabled={busy || !ready} onClick={() => void submit()}>
              {busy ? "Creating…" : "Start scan"}
            </Button>
            {target.kind === "media" && options.resultPolicy === ScanResultPolicy.PUBLISH_INVENTORY && (
              <Button disabled={busy || !target.media} onClick={admission.ask}>
                Add to Library
              </Button>
            )}
          </ActionRow>
          <ImportPositionsDialog admission={admission} name={target.kind === "media" ? target.media?.name : undefined} />
          {choosing && target.kind !== "media" && (
            <ScanSelectionDialog
              entries={entries}
              location={target.kind === "location" ? (target.location ?? undefined) : undefined}
              onClose={() => setChoosing(false)}
              onChoose={(next) => {
                setEntries(next);
                setChoosing(false);
              }}
            />
          )}
        </Card>
      </div>
    </div>
  );
};
