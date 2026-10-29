import { useEffect, useRef, useState } from "react";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, MenuItem, Stack, TextField } from "@mui/material";
import { mediaCli } from "@/api";
import {
  DeviceFeatureAvailability,
  DeviceFeatureEvidence,
  DeviceFeatureKind,
  DeviceFeatureSupport,
  DeviceFeatureValidation,
  DeviceReason,
  TapeCapacityQuality,
  TapeRecordMode,
  type TapeCapacityLimits,
  type TapeEncryptionLimits,
  type TapeGeometryLimits,
  type DeviceFeature,
  type DeviceSummary,
  type InspectDeviceResponse,
} from "@/entity";
import { Feedback } from "@/components/feedback";
import { ActionRow } from "@/components/action-row";
import { contentTime } from "@/components/content-status";
import { errorMessage } from "@/tools";
import { dateFromNs } from "@/tools/time";

export const soleAvailableDevice = (devices: DeviceSummary[]): string => {
  const available = devices.filter((device) => !device.busy);
  return available.length === 1 ? available[0].device : "";
};

const enumLabel = (values: Record<number, string>, value: number): string =>
  (value === 0 ? "Unknown" : (values[value] ?? "Unknown")).toLowerCase().replaceAll("_", " ");

// Explicit labels for reported fields; protocol abbreviations keep their usual spelling.
const fieldLabels: Record<string, string> = {
  vendor: "Vendor",
  model: "Model",
  firmware: "Firmware",
  serialNumber: "Serial number",
  accessIdentity: "Access identity",
  transport: "Transport",
  mediumPresent: "Medium present",
  writeProtected: "Write protected",
  densityCode: "Density code",
  partitionCount: "Partition count",
  recordBytes: "Record size (bytes)",
  fixedRecords: "Fixed records",
  bufferedWrites: "Buffered writes",
  compressionEnabled: "Compression enabled",
  decompressionEnabled: "Decompression enabled",
  encryptionEnabled: "Encryption enabled",
  recordMode: "Record mode",
  hardwareCompression: "Hardware compression",
  minimumBytes: "Minimum bytes",
  maximumBytes: "Maximum bytes",
  alignmentBytes: "Alignment bytes",
  fixed: "Fixed",
  variable: "Variable",
  transportMaximumBytes: "Transport maximum bytes",
  readPositionShort: "READ POSITION short form",
  readPositionLong: "READ POSITION long form",
  addressBits: "Address bits",
  currentAlgorithmIndex: "Current algorithm index",
  encryptionMode: "Encryption mode",
  decryptionMode: "Decryption mode",
  iTNexusScope: "I_T nexus scope",
  keyScope: "Key scope",
  parametersControl: "Parameters control",
  keyInstanceCounter: "Key instance counter",
  allWraps: "All wraps",
  reportedWraps: "Reported wraps",
  fresh: "Fresh",
  source: "Source",
};

const fieldLabel = (name: string): string => fieldLabels[name] ?? name;

const featureLimits = ({ limits }: DeviceFeature) => {
  switch (limits.oneofKind) {
    case "records":
      return limits.records;
    case "navigation":
      return limits.navigation;
    default:
      return {};
  }
};

const CapacityDetails = ({ capacity }: { capacity: TapeCapacityLimits }) => (
  <>
    <span>
      Capacity quality:{" "}
      {capacity.quality === TapeCapacityQuality.ESTIMATE ? "Estimate" : capacity.quality === TapeCapacityQuality.LOWER_BOUND ? "Lower bound" : "Unknown"}
    </span>
    <span>Partition: {capacity.partition ?? "Unknown"}</span>
    <span>Native space before early warning: {capacity.remainingBytes === undefined ? "Unknown" : `${capacity.remainingBytes} bytes`}</span>
    <span>Observation started: {dateFromNs(capacity.issuedAtNs)?.toISOString() ?? "Unknown"}</span>
    <span>Observation completed: {dateFromNs(capacity.completedAtNs)?.toISOString() ?? "Unknown"}</span>
    <span>Observation error allowance: {capacity.observationErrorBytes === undefined ? "Unknown" : `${capacity.observationErrorBytes} bytes`}</span>
    <span>Unreflected write allowance: {capacity.unreflectedBytes === undefined ? "Unknown" : `${capacity.unreflectedBytes} bytes`}</span>
    <Alert severity="info">
      {capacity.quality === TapeCapacityQuality.ESTIMATE
        ? "This capacity estimate does not guarantee that a file will fit."
        : "This observation alone does not guarantee that a file will fit."}{" "}
      Actual early warning and write errors remain authoritative.
    </Alert>
  </>
);

const EncryptionDetails = ({ encryption }: { encryption: TapeEncryptionLimits }) => (
  <>
    <span>
      Reported algorithms:{" "}
      {encryption.algorithms.length
        ? encryption.algorithms.map((algorithm) => `${algorithm.name} · index ${algorithm.index} · key ${algorithm.keyBytes} bytes`).join("; ")
        : "Unknown"}
    </span>
    {(["currentAlgorithmIndex", "encryptionMode", "decryptionMode", "iTNexusScope", "keyScope", "parametersControl", "keyInstanceCounter"] as const).map(
      (name) => (
        <span key={name}>
          {fieldLabel(name)}: {encryption[name] === undefined ? "Unknown" : String(encryption[name])}
        </span>
      ),
    )}
    <span>Algorithm indexes apply to this device. A key instance counter does not establish key presence or a matching recovery key.</span>
  </>
);

const GeometryDetails = ({ geometry }: { geometry: TapeGeometryLimits }) => (
  <>
    {(["allWraps", "reportedWraps", "fresh", "source"] as const).map((name) => (
      <span key={name}>
        {fieldLabel(name)}: {geometry[name] === undefined ? "Unknown" : String(geometry[name])}
      </span>
    ))}
    <span>All wraps describes the reported command form, not qualified full coverage. Geometry observations alone do not qualify Auto read order.</span>
  </>
);

export const DeviceInspectionResult = ({ reply }: { reply: InspectDeviceResponse }) => (
  <Stack spacing={1} sx={{ mt: 2, overflowWrap: "anywhere" }}>
    <strong>{reply.device?.device}</strong>
    {reply.device?.busy && <Alert severity="info">{reply.device.detail || "Device is busy."}</Alert>}
    {reply.reason !== DeviceReason.UNSPECIFIED && (
      <span>
        {enumLabel(DeviceReason, reply.reason)}
        {reply.detail && ` · ${reply.detail}`}
      </span>
    )}
    {reply.reason === DeviceReason.UNSPECIFIED && reply.detail && <span>{reply.detail}</span>}
    <span>Observed: {reply.observedAtNs === undefined ? "Unknown" : contentTime(reply.observedAtNs)}</span>
    {(["vendor", "model", "firmware", "serialNumber", "accessIdentity", "transport"] as const).map((name) => (
      <span key={name}>
        {fieldLabel(name)}: {reply[name] ?? "Unknown"}
      </span>
    ))}
    {reply.preparedRead && (
      <Stack spacing={0.5} sx={{ borderTop: 1, borderColor: "divider", pt: 1 }}>
        <strong>Last prepared read</strong>
        <span>
          {contentTime(reply.preparedRead.preparedAtNs)} · Job {reply.preparedRead.jobId} · Media {reply.preparedRead.identity}
        </span>
        <span>This records preparation for that read; it does not identify the currently loaded cartridge or the final read order.</span>
        <span>
          Requested: {reply.preparedRead.requestedOrder} · Prepared: {reply.preparedRead.effectiveOrder}
        </span>
        <span>Parameters: {reply.preparedRead.parametersSource}</span>
        {reply.preparedRead.parametersReference && <span>{reply.preparedRead.parametersReference}</span>}
        <span>Applicability: {reply.preparedRead.applicabilityReason || "Matched the prepared conditions"}</span>
        <span>Position source: {reply.preparedRead.positionSource || "Unavailable"}</span>
        {reply.preparedRead.coverageDetail && <span>{reply.preparedRead.coverageDetail}</span>}
        {reply.preparedRead.learnedMaeSeconds !== undefined && <span>Learned read parameters mean error: {reply.preparedRead.learnedMaeSeconds} seconds</span>}
        {reply.preparedRead.learnedMaxErrorSeconds !== undefined && (
          <span>Learned read parameters maximum error: {reply.preparedRead.learnedMaxErrorSeconds} seconds</span>
        )}
      </Stack>
    )}
    {reply.report.oneofKind === "tape" && (
      <>
        <strong>Tape device</strong>
        {(
          [
            "mediumPresent",
            "writeProtected",
            "densityCode",
            "partitionCount",
            "recordBytes",
            "fixedRecords",
            "bufferedWrites",
            "compressionEnabled",
            "decompressionEnabled",
            "encryptionEnabled",
          ] as const
        ).map((name) => (
          <span key={name}>
            {fieldLabel(name)}: {reply.report.oneofKind === "tape" && reply.report.tape[name] !== undefined ? String(reply.report.tape[name]) : "Unknown"}
          </span>
        ))}
        {reply.report.tape.recommendationDetail && <Alert severity="info">{reply.report.tape.recommendationDetail}</Alert>}
        {reply.report.tape.recommendedWriteOptions && (
          <>
            <strong>Tar FORMAT recommendations</strong>
            {Object.entries(reply.report.tape.recommendedWriteOptions).map(
              ([name, value]) =>
                value !== undefined && (
                  <span key={name}>
                    {fieldLabel(name)}: {name === "recordMode" ? enumLabel(TapeRecordMode, Number(value)) : String(value)}
                  </span>
                ),
            )}
          </>
        )}
        {reply.report.tape.features.map((feature, index) => (
          <Stack key={`${feature.feature}-${index}`} spacing={0.5} sx={{ borderTop: 1, borderColor: "divider", pt: 1 }}>
            <strong>{enumLabel(DeviceFeatureKind, feature.feature)}</strong>
            <span>
              Support: {enumLabel(DeviceFeatureSupport, feature.support)} · Availability: {enumLabel(DeviceFeatureAvailability, feature.availability)} ·
              Validation: {enumLabel(DeviceFeatureValidation, feature.validation)}
            </span>
            <span>Evidence: {enumLabel(DeviceFeatureEvidence, feature.evidence)}</span>
            {feature.reason !== DeviceReason.UNSPECIFIED && <span>Reason: {enumLabel(DeviceReason, feature.reason)}</span>}
            {feature.detail && <span>{feature.detail}</span>}
            {feature.scope &&
              Object.entries(feature.scope).map(
                ([name, value]) =>
                  value !== undefined && (
                    <span key={name}>
                      Scope · {fieldLabel(name)}: {String(value)}
                    </span>
                  ),
              )}
            {Object.entries(featureLimits(feature)).map(
              ([name, value]) =>
                value !== undefined && (
                  <span key={name}>
                    {fieldLabel(name)}: {String(value)}
                  </span>
                ),
            )}
            {feature.limits.oneofKind === "geometry" && <GeometryDetails geometry={feature.limits.geometry} />}
            {feature.limits.oneofKind === "capacity" && <CapacityDetails capacity={feature.limits.capacity} />}
            {feature.limits.oneofKind === "encryption" && <EncryptionDetails encryption={feature.limits.encryption} />}
            {feature.limits.oneofKind === "compression" && (
              <>
                <span>
                  Current algorithm:{" "}
                  {feature.limits.compression.currentAlgorithm === undefined
                    ? "Unknown"
                    : `0x${feature.limits.compression.currentAlgorithm.toString(16).padStart(8, "0")} (${feature.limits.compression.currentAlgorithm})`}
                </span>
                <span>
                  Enable changeable:{" "}
                  {feature.limits.compression.enableChangeable === undefined ? "Unknown" : String(feature.limits.compression.enableChangeable)}
                </span>
              </>
            )}
          </Stack>
        ))}
      </>
    )}
  </Stack>
);

// Shared operation choice and passive diagnostics keep cartridge inspection with their caller.
export const TapeDeviceChoice = ({
  devices,
  value,
  disabled = false,
  loading = false,
  onChange,
  onRefresh,
}: {
  devices: DeviceSummary[] | null;
  loading?: boolean;
  value: string;
  disabled?: boolean;
  onChange: (device: string) => void;
  onRefresh: () => Promise<void>;
}) => {
  const entries = devices ?? [];
  const [refreshing, setRefreshing] = useState(false);
  const [refreshError, setRefreshError] = useState("");
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [detailDevice, setDetailDevice] = useState("");
  const [reply, setReply] = useState<InspectDeviceResponse | null>(null);
  const [inspecting, setInspecting] = useState(false);
  const [inspectError, setInspectError] = useState("");
  const request = useRef(0);
  const refreshRequest = useRef(0);
  useEffect(() => {
    const sequence = request;
    const refreshSequence = refreshRequest;
    return () => {
      sequence.current++;
      refreshSequence.current++;
    };
  }, []);
  const refresh = async () => {
    const sequence = ++refreshRequest.current;
    setRefreshing(true);
    setRefreshError("");
    try {
      await onRefresh();
    } catch (error) {
      if (sequence === refreshRequest.current) setRefreshError(errorMessage(error, "Could not refresh devices"));
    } finally {
      if (sequence === refreshRequest.current) setRefreshing(false);
    }
  };
  const inspect = async (device: string) => {
    const sequence = ++request.current;
    setDetailDevice(device);
    setReply(null);
    setInspectError("");
    setInspecting(true);
    try {
      const result = await mediaCli.inspectDevice({ device }).response;
      if (sequence === request.current) setReply(result);
    } catch (error) {
      if (sequence === request.current) setInspectError(errorMessage(error, "Could not inspect device"));
    } finally {
      if (sequence === request.current) setInspecting(false);
    }
  };
  const closeDetails = () => {
    request.current++;
    setDetailsOpen(false);
    setInspecting(false);
  };
  return (
    <>
      <TextField
        select
        required
        disabled={disabled || refreshing || loading || devices === null}
        margin="normal"
        label="Tape device"
        fullWidth
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        {entries.map((device) => (
          <MenuItem key={device.device} value={device.device} disabled={device.busy} sx={{ whiteSpace: "normal", overflowWrap: "anywhere" }}>
            {device.device}
            {device.busy ? ` · ${device.detail || "Busy"}` : device.detail ? ` · ${device.detail}` : ""}
          </MenuItem>
        ))}
      </TextField>
      <ActionRow>
        <Button disabled={disabled || refreshing || loading} onClick={() => void refresh()}>
          {refreshing ? "Refreshing devices…" : "Refresh devices"}
        </Button>
        <Button
          disabled={disabled || loading || !entries.length}
          onClick={() => {
            setDetailsOpen(true);
            void inspect(value || entries[0].device);
          }}
        >
          Device details
        </Button>
      </ActionRow>
      {loading && <p role="status">Loading devices…</p>}
      {!loading && devices === null && <Alert severity="info">Device availability is unknown. Refresh devices to try again.</Alert>}
      {!loading && devices !== null && !devices.length && <Alert severity="info">No Tape drive is configured.</Alert>}
      {refreshError && <Feedback severity="error">{refreshError}</Feedback>}
      {detailsOpen && (
        <Dialog open onClose={closeDetails} maxWidth="sm" fullWidth>
          <DialogTitle>Device details</DialogTitle>
          <DialogContent>
            <TextField select margin="normal" label="Device to inspect" fullWidth value={detailDevice} onChange={(event) => void inspect(event.target.value)}>
              {entries.map((device) => (
                <MenuItem key={device.device} value={device.device}>
                  {device.device}
                  {device.busy ? " · Busy" : ""}
                </MenuItem>
              ))}
            </TextField>
            {inspecting && <p role="status">Inspecting device…</p>}
            {inspectError && <Feedback severity="error">{inspectError}</Feedback>}
            {reply && <DeviceInspectionResult reply={reply} />}
          </DialogContent>
          <DialogActions>
            <Button disabled={inspecting} onClick={() => void inspect(detailDevice)}>
              Refresh device details
            </Button>
            <Button onClick={closeDetails}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </>
  );
};
