import { SettingsSection } from "@/components/settings-page";
import { isLtfsFormat, tapeFormatOf } from "@/components/tape-format";
import { Feedback } from "@/components/feedback";
import { Fragment, useContext, useEffect, useRef, useState, type FormEvent } from "react";
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  MenuItem,
  TextField,
  Checkbox,
  FormControlLabel,
} from "@mui/material";
import { mediaCli, volumeSpaceLabel } from "@/api";
import {
  type Media,
  MediaKind,
  ReadMediaTarget,
  ScanResultPolicy,
  DeviceFeatureKind,
  DeviceFeatureSupport,
  DeviceFeatureAvailability,
  DeviceFeatureValidation,
  type InspectDeviceResponse,
} from "@/entity";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { TapeDeviceChoice, soleAvailableDevice, DeviceInspectionResult } from "@/components/tape-device-choice";
import { useTapeDevices } from "@/components/use-tape-devices";
import { tapeEncryption } from "@/components/media-inspector";
import { RefreshContext } from "@/pages/jobs";
import { errorMessage, runUIAction } from "@/tools";

export const ReadMediaDialog = ({
  mediaIDs,
  operation,
  loadScanResultPolicy,
  onRead,
}: {
  mediaIDs: bigint[];
  operation: "restore" | "scan";
  loadScanResultPolicy?: () => Promise<ScanResultPolicy | undefined>;
  onRead: (target: ReadMediaTarget) => Promise<void>;
}) => {
  const refresh = useContext(RefreshContext);
  const [open, setOpen] = useState(false);
  const [backend, setBackend] = useState<"tape" | "volume">("volume");
  const [media, setMedia] = useState<Media[]>([]);
  const [device, setDevice] = useState("");
  const [volumeUUID, setVolumeUUID] = useState("");
  const [tapeID, setTapeID] = useState("");
  const [deviceReport, setDeviceReport] = useState<InspectDeviceResponse | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const tapeDevices = useTapeDevices();
  const { devices } = tapeDevices;
  const [scanResultPolicy, setScanResultPolicy] = useState<ScanResultPolicy>();
  const [refreshPositionMap, setRefreshPositionMap] = useState(false);
  const inspection = useMediaInspect();
  const request = useRef(0);
  const deviceRequest = useRef(0);
  const policyRequest = useRef(0);
  useEffect(() => {
    const pending = request;
    const pendingPolicy = policyRequest;
    return () => {
      pending.current++;
      pendingPolicy.current++;
    };
  }, []);
  const volumes = media.filter((value) => value.kind === MediaKind.VOLUME);
  const tapes = media.filter((value) => value.kind === MediaKind.TAPE);
  const candidates = backend === "volume" ? volumes : tapes;

  // Every open, close and successful read starts from no Media, device or inspection choice.
  const resetChoices = () => {
    tapeDevices.reset();
    setMedia([]);
    setDevice("");
    setVolumeUUID("");
    setTapeID("");
    setRefreshPositionMap(false);
    setScanResultPolicy(undefined);
    setDeviceReport(null);
    inspection.reset();
  };
  const dismiss = () => {
    request.current++;
    policyRequest.current++;
    setOpen(false);
    setBackend("volume");
    resetChoices();
  };
  const show = async () => {
    const current = ++request.current;
    const currentPolicy = ++policyRequest.current;
    setOpen(true);
    setLoading(true);
    setError("");
    resetChoices();
    try {
      if (!mediaIDs.length) throw new Error("This Job has no pending Media.");
      const mediaReply = await mediaCli.list({ param: { oneofKind: "ids", ids: { ids: mediaIDs } } }).response;
      if (current !== request.current) return;
      const required = mediaReply.media.filter((value) => mediaIDs.includes(value.id));
      if (mediaIDs.some((id) => !required.some((value) => value.id === id))) throw new Error("Required Media is missing from the Library.");
      const pendingVolumes = required.filter((value) => value.kind === MediaKind.VOLUME);
      const hasTape = required.some((value) => value.kind === MediaKind.TAPE);
      if (!pendingVolumes.length && !hasTape) throw new Error("This Job has no supported Media.");
      setMedia(required);
      setBackend(pendingVolumes.length ? "volume" : "tape");
      if (pendingVolumes.length === 1) setVolumeUUID(pendingVolumes[0].identity);
      if (!hasTape) return;
      if (operation === "scan" && loadScanResultPolicy && required.some(isTar)) {
        void loadScanResultPolicy()
          .then((policy) => {
            if (currentPolicy === policyRequest.current) setScanResultPolicy(policy);
          })
          // An unavailable policy leaves the optional preparation action hidden instead of blocking the read.
          .catch(() => undefined);
      }
      const listed = await tapeDevices.load();
      if (!listed || current !== request.current) return;
      const available = soleAvailableDevice(listed);
      if (!pendingVolumes.length && available) {
        setDevice(available);
        inspectTape(
          available,
          required.filter((value) => value.kind === MediaKind.TAPE),
        );
      }
    } catch (error) {
      if (current === request.current) setError(errorMessage(error, "Could not load archive storage"));
    } finally {
      if (current === request.current) setLoading(false);
    }
  };
  const close = () => {
    if (!submitting) dismiss();
  };
  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      const target =
        backend === "volume"
          ? { backend: { oneofKind: "volume" as const, volume: { uuid: volumeUUID } } }
          : { backend: { oneofKind: "tape" as const, tape: { device, refreshPositionMap, ...(tapeID ? { mediaId: BigInt(tapeID) } : {}) } } };
      const selected = backend === "volume" ? selectedVolume : useLTFS ? inspection.reply?.media : undefined;
      await onRead(ReadMediaTarget.create({ ...target, ...(selected ? { expectedMediaId: selected.id, expectedIdentity: selected.identity } : {}) }));
      dismiss();
      runUIAction(refresh, "Media reading started, but the Job list could not refresh");
    } catch (error) {
      setError(errorMessage(error, `Could not start ${operation}`));
    } finally {
      setSubmitting(false);
    }
  };
  const selectedVolume = volumes.find((volume) => volume.identity === volumeUUID);
  const tapeMatches = tapes
    .filter((value) => !tapeID || value.id.toString() === tapeID)
    .some((value) => value.id === inspection.reply?.media?.id && value.identity === inspection.reply.identity);
  const selectedTape = tapes.find((value) => value.id.toString() === tapeID);
  const isLTFS = (value: Media) => isLtfsFormat(tapeFormatOf(value));
  const useLTFS = selectedTape ? isLTFS(selectedTape) : tapes.length > 0 && tapes.every(isLTFS);
  const isTar = (value: Media) => tapeFormatOf(value) === "tar_pax_v1";
  const refreshApplicable =
    backend === "tape" &&
    (selectedTape ? isTar(selectedTape) : tapes.length > 0 && tapes.every(isTar)) &&
    (operation === "restore" || scanResultPolicy === ScanResultPolicy.VERIFY_COPIES);
  const reload =
    deviceReport?.report.oneofKind === "tape"
      ? deviceReport.report.tape.features.find((feature) => feature.feature === DeviceFeatureKind.NON_EJECTING_RELOAD)
      : undefined;
  const refreshAvailable =
    reload?.support === DeviceFeatureSupport.SUPPORTED &&
    reload.availability === DeviceFeatureAvailability.READY &&
    reload.validation !== DeviceFeatureValidation.FAILED;
  const inspectTape = (nextDevice: string, choices = tapes, hint = tapeID) => {
    if (!nextDevice) return;
    const inspectionSequence = ++deviceRequest.current;
    const selected = choices.find((value) => value.id.toString() === hint);
    if (selected ? isLTFS(selected) : choices.length > 0 && choices.every(isLTFS)) {
      void inspection.inspect({ oneofKind: "tape", tape: { device: nextDevice } });
      return;
    }
    const sequence = request.current;
    void mediaCli
      .inspectDevice({ device: nextDevice })
      .response.then((reply) => {
        if (sequence === request.current && inspectionSequence === deviceRequest.current) setDeviceReport(reply);
      })
      .catch((reason) => {
        if (sequence === request.current && inspectionSequence === deviceRequest.current) setError(errorMessage(reason, "Could not inspect device"));
      });
  };
  const refreshDevices = async () => {
    const current = request.current;
    const listed = await tapeDevices.refresh();
    if (!listed || current !== request.current) return;
    if (!listed.some((value) => value.device === device && !value.busy)) {
      deviceRequest.current++;
      setDevice("");
      setDeviceReport(null);
      inspection.reset();
    }
  };
  const canSubmit =
    !loading &&
    !submitting &&
    (!refreshPositionMap || (refreshApplicable && refreshAvailable)) &&
    (backend === "volume"
      ? selectedVolume?.mounted === true
      : !tapeDevices.loading && devices?.some((value) => value.device === device && !value.busy) && (!useLTFS || (tapeMatches && !inspection.loading)));

  return (
    <Fragment>
      <Button size="small" disabled={open || submitting} onClick={show}>
        {operation === "restore" ? "Choose archive storage to read" : "Choose Media to read"}
      </Button>
      {open && (
        <Dialog
          open
          onClose={close}
          maxWidth="sm"
          fullWidth
          slotProps={{
            paper: {
              component: "form",
              onSubmit: (event: FormEvent) => {
                event.preventDefault();
                void submit();
              },
            },
          }}
        >
          <DialogTitle>{operation === "restore" ? "Restore from archive storage" : "Read scan Media"}</DialogTitle>
          <DialogContent>
            {error && <Feedback severity="error">{error}</Feedback>}
            {loading && <p role="status">Loading required Media…</p>}
            {!loading && candidates.length === 1 && (
              <p>
                <strong>
                  {backend === "tape" ? "Tape" : "Volume"}: {candidates[0].name || candidates[0].identity}
                </strong>
                {candidates[0].name && <span className="product-muted"> · {candidates[0].identity}</span>}
              </p>
            )}
            {!loading && volumes.length > 0 && tapes.length > 0 && (
              <TextField
                select
                required
                disabled={submitting}
                margin="normal"
                label="Storage type"
                fullWidth
                value={backend}
                onChange={(event) => {
                  const selected = event.target.value as "tape" | "volume";
                  setBackend(selected);
                  setRefreshPositionMap(false);
                  const available = soleAvailableDevice(devices ?? []);
                  if (selected === "tape" && available && !device) {
                    setDevice(available);
                    inspectTape(available);
                  }
                }}
              >
                <MenuItem value="tape">Tape</MenuItem>
                <MenuItem value="volume">Mounted Volume</MenuItem>
              </TextField>
            )}
            {!loading &&
              media.length > 0 &&
              (backend === "volume" ? (
                <Fragment>
                  {volumes.length === 1 ? (
                    selectedVolume?.mounted !== true && <Alert severity="warning">Mount this Volume before continuing.</Alert>
                  ) : (
                    <TextField
                      select
                      required
                      disabled={submitting}
                      margin="normal"
                      label="Volume"
                      fullWidth
                      value={volumeUUID}
                      onChange={(event) => setVolumeUUID(event.target.value)}
                    >
                      {volumes.map((volume) => (
                        <MenuItem key={volume.id.toString()} value={volume.identity} disabled={volume.mounted !== true}>
                          {volume.name || volume.identity} · {volumeSpaceLabel(volume)}
                        </MenuItem>
                      ))}
                    </TextField>
                  )}
                </Fragment>
              ) : (
                <Fragment>
                  <DialogContentText>Load the required Tape and select its drive.</DialogContentText>
                  <TapeDeviceChoice
                    devices={devices}
                    loading={tapeDevices.loading}
                    value={device}
                    disabled={submitting}
                    onRefresh={refreshDevices}
                    onChange={(value) => {
                      setDevice(value);
                      setRefreshPositionMap(false);
                      inspection.reset();
                      setDeviceReport(null);
                      inspectTape(value);
                    }}
                  />
                  {tapeDevices.error && <Feedback severity="error">{tapeDevices.error}</Feedback>}
                  <TextField
                    select
                    fullWidth
                    margin="normal"
                    label="Tape Media"
                    slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
                    value={tapeID}
                    disabled={submitting}
                    onChange={(event) => {
                      const hint = event.target.value;
                      setTapeID(hint);
                      setRefreshPositionMap(false);
                      inspection.reset();
                      setDeviceReport(null);
                      inspectTape(device, tapes, hint);
                    }}
                    helperText="The server verifies the loaded Tape against this Job before reading."
                  >
                    <MenuItem value="">Automatic identification</MenuItem>
                    {tapes.map((value) => (
                      <MenuItem key={value.id.toString()} value={value.id.toString()}>
                        {value.name || value.identity} · {value.identity}
                      </MenuItem>
                    ))}
                  </TextField>
                  <Alert severity="info">
                    Encryption is handled by the configured Encrypt script using the saved Media profile.
                    {selectedTape && tapeEncryption(selectedTape) && ` Encryption: ${tapeEncryption(selectedTape)}.`}
                  </Alert>
                  {useLTFS ? (
                    <MediaInspectResult {...inspection} />
                  ) : (
                    <>
                      <Alert severity="info">
                        Automatic identification uses the Tape's cartridge memory barcode, and the matching Media supplies its saved key. YATM does not try
                        multiple keys.
                      </Alert>
                    </>
                  )}
                  {refreshApplicable && (
                    <SettingsSection
                      title="Advanced"
                      collapsible
                      hasError={refreshPositionMap && !refreshAvailable}
                      description={refreshPositionMap ? "Position map refresh enabled" : undefined}
                    >
                      <FormControlLabel
                        control={
                          <Checkbox
                            checked={refreshPositionMap}
                            disabled={submitting || (!refreshAvailable && !refreshPositionMap)}
                            onChange={(event) => setRefreshPositionMap(event.target.checked)}
                          />
                        }
                        label="Refresh position map before reading"
                      />
                      <Alert severity="info">
                        {refreshAvailable
                          ? "This explicitly unloads and reloads the cartridge without ejecting it to recollect the position map. Identity and read settings are rechecked before reading. The map is not guaranteed to be fresh. Auto read order alone never reloads."
                          : reload?.detail || "Refresh requires an available non-ejecting reload. Ordinary reading remains available."}
                      </Alert>
                    </SettingsSection>
                  )}
                  {refreshPositionMap && (
                    <Alert severity="warning">Position map refresh enabled: the cartridge will unload and reload without ejecting before reading.</Alert>
                  )}
                  {!useLTFS && deviceReport && (
                    <SettingsSection title="Device details" collapsible>
                      <DeviceInspectionResult reply={deviceReport} />
                    </SettingsSection>
                  )}
                  {useLTFS && inspection.reply?.identity && !tapeMatches && (
                    <Feedback severity="error">The inserted Tape does not match this Job’s required Media.</Feedback>
                  )}
                </Fragment>
              ))}
          </DialogContent>
          <DialogActions>
            <Button type="submit" disabled={!canSubmit}>
              {operation === "restore" ? "Start restore" : "Start reading"}
            </Button>
            <Button disabled={submitting} onClick={close}>
              Cancel
            </Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};
