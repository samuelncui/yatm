import { i18n, type MessageDescriptor } from "@lingui/core";
import { msg, t } from "@lingui/core/macro";
import { useEffect, useRef, useState } from "react";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, MenuItem, Stack, TextField } from "@mui/material";
import { mediaCli } from "@/api";
import {
  DeviceFeatureAvailability,
  DeviceFeatureEvidence,
  DeviceFeatureKind,
  DeviceFeatureSupport,
  DeviceReason,
  TapeCapacityQuality,
  type TapeCapacityLimits,
  type TapeEncryptionLimits,
  type TapeGeometryLimits,
  type DeviceFeature,
  type DeviceSummary,
  type InspectDeviceResponse,
} from "@/entity";
import { Feedback } from "@/components/feedback";
import { densityLabel } from "@/components/tape-density";
import { ActionRow } from "@/components/action-row";
import { contentTime } from "@/components/content-status";
import { errorMessage } from "@/tools";
import { dateFromNs } from "@/tools/time";

export const soleAvailableDevice = (devices: DeviceSummary[]): string => {
  const available = devices.filter((device) => !device.busy);
  return available.length === 1 ? available[0].device : "";
};

// Enum values map to messages explicitly; an unspecified or unlisted value reads "unknown".
const enumLabel = (labels: Record<number, MessageDescriptor>, value: number): string => i18n._(labels[value] ?? labels[0]);
const unknownReport = msg({ message: "unknown", context: "Device report" });

// Typed by the enum so every value needs a label.
const reasonLabels: Record<DeviceReason, MessageDescriptor> = {
  [DeviceReason.UNSPECIFIED]: unknownReport,
  [DeviceReason.BUSY]: msg({ message: "busy", context: "Device report" }),
  [DeviceReason.NO_MEDIUM]: msg({ message: "no medium", context: "Device report" }),
  [DeviceReason.PERMISSION]: msg({ message: "permission", context: "Device report" }),
  [DeviceReason.TRANSPORT_UNAVAILABLE]: msg({ message: "transport unavailable", context: "Device report" }),
  [DeviceReason.UNSUPPORTED]: msg({ message: "unsupported", context: "Device report" }),
  [DeviceReason.MALFORMED_RESPONSE]: msg({ message: "malformed response", context: "Device report" }),
  [DeviceReason.TRANSPORT_ERROR]: msg({ message: "transport error", context: "Device report" }),
  [DeviceReason.OWNERSHIP_UNCERTAIN]: msg({ message: "ownership uncertain", context: "Device report" }),
  [DeviceReason.NOT_IMPLEMENTED]: msg({ message: "not implemented", context: "Device report" }),
  [DeviceReason.NOT_READY]: msg({ message: "not ready", context: "Device report" }),
  [DeviceReason.MISSING_TOOL]: msg({ message: "missing tool", context: "Device report" }),
};
const featureLabels: Record<DeviceFeatureKind, MessageDescriptor> = {
  [DeviceFeatureKind.UNSPECIFIED]: unknownReport,
  [DeviceFeatureKind.INITIALIZE]: msg({ message: "initialize", context: "Device report" }),
  [DeviceFeatureKind.SEQUENTIAL_READ]: msg({ message: "sequential read", context: "Device report" }),
  [DeviceFeatureKind.SEQUENTIAL_WRITE]: msg({ message: "sequential write", context: "Device report" }),
  [DeviceFeatureKind.LOCATE]: msg({ message: "locate", context: "Device report" }),
  [DeviceFeatureKind.READ_POSITION]: msg({ message: "read position", context: "Device report" }),
  [DeviceFeatureKind.EARLY_WARNING]: msg({ message: "early warning", context: "Device report" }),
  [DeviceFeatureKind.HARDWARE_ENCRYPTION]: msg({ message: "hardware encryption", context: "Device report" }),
  [DeviceFeatureKind.END_OF_WRAP_POSITION]: msg({ message: "end of wrap position", context: "Device report" }),
  [DeviceFeatureKind.CAPACITY_TO_WARNING]: msg({ message: "capacity to warning", context: "Device report" }),
  [DeviceFeatureKind.SOFTWARE_READ_ORDER]: msg({ message: "software read order", context: "Device report" }),
};
const supportLabels: Record<DeviceFeatureSupport, MessageDescriptor> = {
  [DeviceFeatureSupport.UNSPECIFIED]: unknownReport,
  [DeviceFeatureSupport.UNKNOWN]: unknownReport,
  [DeviceFeatureSupport.UNSUPPORTED]: msg({ message: "unsupported", context: "Device report" }),
  [DeviceFeatureSupport.PARTIAL]: msg({ message: "partial", context: "Device report" }),
  [DeviceFeatureSupport.SUPPORTED]: msg({ message: "supported", context: "Device report" }),
};
const availabilityLabels: Record<DeviceFeatureAvailability, MessageDescriptor> = {
  [DeviceFeatureAvailability.UNSPECIFIED]: unknownReport,
  [DeviceFeatureAvailability.UNKNOWN]: unknownReport,
  [DeviceFeatureAvailability.UNAVAILABLE]: msg({ message: "unavailable", context: "Device report" }),
  [DeviceFeatureAvailability.READY]: msg({ message: "ready", context: "Device report" }),
};
const evidenceLabels: Record<DeviceFeatureEvidence, MessageDescriptor> = {
  [DeviceFeatureEvidence.UNSPECIFIED]: unknownReport,
  [DeviceFeatureEvidence.REPORTED]: msg({ message: "reported", context: "Device report" }),
  [DeviceFeatureEvidence.OBSERVED]: msg({ message: "observed", context: "Device report" }),
};

// Explicit labels for reported fields; protocol abbreviations keep their usual spelling.
const fieldLabels: Record<string, MessageDescriptor> = {
  vendor: msg({ message: "Vendor", context: "Device field" }),
  model: msg({ message: "Model", context: "Device field" }),
  firmware: msg({ message: "Firmware", context: "Device field" }),
  serialNumber: msg({ message: "Serial number", context: "Device field" }),
  accessIdentity: msg({ message: "Access identity", context: "Device field" }),
  transport: msg({ message: "Transport", context: "Device field" }),
  mediumPresent: msg({ message: "Medium present", context: "Device field" }),
  writeProtected: msg({ message: "Write protected", context: "Device field" }),
  densityCode: msg({ message: "Density code", context: "Device field" }),
  partitionCount: msg({ message: "Partition count", context: "Device field" }),
  recordBytes: msg({ message: "Record size (bytes)", context: "Device field" }),
  fixedRecords: msg({ message: "Fixed records", context: "Device field" }),
  bufferedWrites: msg({ message: "Buffered writes", context: "Device field" }),
  encryptionEnabled: msg({ message: "Encryption enabled", context: "Device field" }),
  recordMode: msg({ message: "Record mode", context: "Device field" }),
  minimumBytes: msg({ message: "Minimum bytes", context: "Device field" }),
  maximumBytes: msg({ message: "Maximum bytes", context: "Device field" }),
  alignmentBytes: msg({ message: "Alignment bytes", context: "Device field" }),
  fixed: msg({ message: "Fixed", context: "Device field" }),
  variable: msg({ message: "Variable", context: "Device field" }),
  readPositionShort: msg({ message: "READ POSITION short form", context: "Device field" }),
  readPositionLong: msg({ message: "READ POSITION long form", context: "Device field" }),
  addressBits: msg({ message: "Address bits", context: "Device field" }),
  currentAlgorithmIndex: msg({ message: "Current algorithm index", context: "Device field" }),
  encryptionMode: msg({ message: "Encryption mode", context: "Device field" }),
  decryptionMode: msg({ message: "Decryption mode", context: "Device field" }),
  iTNexusScope: msg({ message: "I_T nexus scope", context: "Device field" }),
  keyScope: msg({ message: "Key scope", context: "Device field" }),
  parametersControl: msg({ message: "Parameters control", context: "Device field" }),
  keyInstanceCounter: msg({ message: "Key instance counter", context: "Device field" }),
  allWraps: msg({ message: "All wraps", context: "Device field" }),
  reportedWraps: msg({ message: "Reported wraps", context: "Device field" }),
  source: msg({ message: "Source", context: "Device field" }),
};

const fieldLabel = (name: string): string => (fieldLabels[name] ? i18n._(fieldLabels[name]) : name);

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

const labelled = (label: string, value: string) => t`${label}: ${value}`;

const CapacityDetails = ({ capacity }: { capacity: TapeCapacityLimits }) => {
  const estimate = capacity.quality === TapeCapacityQuality.ESTIMATE;
  const quality = estimate ? t`Estimate` : t`Unknown`;
  const partition = capacity.partition === undefined ? t`Unknown` : String(capacity.partition);
  const remainingBytes = String(capacity.remainingBytes);
  const remaining = capacity.remainingBytes === undefined ? t`Unknown` : t`${remainingBytes} bytes`;
  const started = dateFromNs(capacity.issuedAtNs)?.toISOString() ?? t`Unknown`;
  const completed = dateFromNs(capacity.completedAtNs)?.toISOString() ?? t`Unknown`;
  return (
    <>
      <span>{t`Capacity quality: ${quality}`}</span>
      <span>{t`Partition: ${partition}`}</span>
      <span>{t`Native space before early warning: ${remaining}`}</span>
      <span>{t`Observation started: ${started}`}</span>
      <span>{t`Observation completed: ${completed}`}</span>
      <Alert severity="info">
        {estimate
          ? t`This capacity estimate does not guarantee that a file will fit. Actual early warning and write errors remain authoritative.`
          : t`This observation alone does not guarantee that a file will fit. Actual early warning and write errors remain authoritative.`}
      </Alert>
    </>
  );
};

const EncryptionDetails = ({ encryption }: { encryption: TapeEncryptionLimits }) => {
  const algorithms = encryption.algorithms.length
    ? encryption.algorithms
        .map((algorithm) => {
          const { name } = algorithm;
          const index = String(algorithm.index);
          const keyBytes = String(algorithm.keyBytes);
          return t`${name} · index ${index} · key ${keyBytes} bytes`;
        })
        .join("; ")
    : t`Unknown`;
  return (
    <>
      <span>{t`Reported algorithms: ${algorithms}`}</span>
      {(["currentAlgorithmIndex", "encryptionMode", "decryptionMode", "iTNexusScope", "keyScope", "parametersControl", "keyInstanceCounter"] as const).map(
        (name) => (
          <span key={name}>{labelled(fieldLabel(name), encryption[name] === undefined ? t`Unknown` : String(encryption[name]))}</span>
        ),
      )}
      <span>{t`Algorithm indexes apply to this device. A key instance counter does not establish key presence or a matching recovery key.`}</span>
    </>
  );
};

const GeometryDetails = ({ geometry }: { geometry: TapeGeometryLimits }) => (
  <>
    {(["allWraps", "reportedWraps", "source"] as const).map((name) => (
      <span key={name}>{labelled(fieldLabel(name), geometry[name] === undefined ? t`Unknown` : String(geometry[name]))}</span>
    ))}
    <span>{t`All wraps describes the reported command form, not qualified full coverage. Geometry observations alone do not qualify Auto read order.`}</span>
  </>
);

const featureSupport = (feature: DeviceFeature) => {
  const support = enumLabel(supportLabels, feature.support);
  const availability = enumLabel(availabilityLabels, feature.availability);
  return t`Support: ${support} · Availability: ${availability}`;
};
const featureEvidence = (feature: DeviceFeature) => {
  const evidence = enumLabel(evidenceLabels, feature.evidence);
  return t`Evidence: ${evidence}`;
};
const featureReason = (feature: DeviceFeature) => {
  const reason = enumLabel(reasonLabels, feature.reason);
  return t`Reason: ${reason}`;
};
const scopeLine = (name: string, value: string) => {
  const field = fieldLabel(name);
  return t`Scope · ${field}: ${value}`;
};

export const DeviceInspectionResult = ({ reply }: { reply: InspectDeviceResponse }) => {
  const observed = reply.observedAtNs === undefined ? t`Unknown` : contentTime(reply.observedAtNs);
  const prepared = reply.preparedRead;
  const preparedAt = prepared ? contentTime(prepared.preparedAtNs) : "";
  const preparedJob = String(prepared?.jobId);
  const preparedIdentity = prepared?.identity ?? "";
  const requestedOrder = prepared?.requestedOrder ?? "";
  const effectiveOrder = prepared?.effectiveOrder ?? "";
  const parametersSource = prepared?.parametersSource ?? "";
  const applicability = prepared?.applicabilityReason || t`Matched the prepared conditions`;
  const positionSource = prepared?.positionSource || t`Unavailable`;
  const tape = reply.report.oneofKind === "tape" ? reply.report.tape : undefined;
  const densities = tape?.supportedDensityCodes.length ? tape.supportedDensityCodes.map((code) => densityLabel(code)).join(", ") : t`Unknown`;
  return (
    <Stack spacing={1} sx={{ mt: 2, overflowWrap: "anywhere" }}>
      <strong>{reply.device?.device}</strong>
      {reply.device?.busy && <Alert severity="info">{reply.device.detail || t`Device is busy.`}</Alert>}
      {reply.reason !== DeviceReason.UNSPECIFIED && (
        <span>
          {enumLabel(reasonLabels, reply.reason)}
          {reply.detail && ` · ${reply.detail}`}
        </span>
      )}
      {reply.reason === DeviceReason.UNSPECIFIED && reply.detail && <span>{reply.detail}</span>}
      <span>{t`Observed: ${observed}`}</span>
      {(["vendor", "model", "firmware", "serialNumber", "accessIdentity", "transport"] as const).map((name) => (
        <span key={name}>{labelled(fieldLabel(name), reply[name] ?? t`Unknown`)}</span>
      ))}
      {reply.preparedRead && (
        <Stack spacing={0.5} sx={{ borderTop: 1, borderColor: "divider", pt: 1 }}>
          <strong>{t`Last prepared read`}</strong>
          <span>{t`${preparedAt} · Job ${preparedJob} · Media ${preparedIdentity}`}</span>
          <span>{t`This records preparation for that read; it does not identify the currently loaded cartridge or the final read order.`}</span>
          <span>{t`Requested: ${requestedOrder} · Prepared: ${effectiveOrder}`}</span>
          <span>{t`Parameters: ${parametersSource}`}</span>
          {reply.preparedRead.parametersReference && <span>{reply.preparedRead.parametersReference}</span>}
          <span>{t`Applicability: ${applicability}`}</span>
          <span>{t`Position source: ${positionSource}`}</span>
          {reply.preparedRead.coverageDetail && <span>{reply.preparedRead.coverageDetail}</span>}
        </Stack>
      )}
      {reply.report.oneofKind === "tape" && (
        <>
          <strong>{t`Tape device`}</strong>
          {(
            ["mediumPresent", "writeProtected", "densityCode", "partitionCount", "recordBytes", "fixedRecords", "bufferedWrites", "encryptionEnabled"] as const
          ).map((name) => (
            <span key={name}>
              {labelled(
                fieldLabel(name),
                reply.report.oneofKind === "tape" && reply.report.tape[name] !== undefined ? String(reply.report.tape[name]) : t`Unknown`,
              )}
            </span>
          ))}
          <span>{t`Supported densities: ${densities}`}</span>
          {reply.report.tape.features.map((feature, index) => (
            <Stack key={`${feature.feature}-${index}`} spacing={0.5} sx={{ borderTop: 1, borderColor: "divider", pt: 1 }}>
              <strong>{enumLabel(featureLabels, feature.feature)}</strong>
              <span>{featureSupport(feature)}</span>
              <span>{featureEvidence(feature)}</span>
              {feature.reason !== DeviceReason.UNSPECIFIED && <span>{featureReason(feature)}</span>}
              {feature.detail && <span>{feature.detail}</span>}
              {feature.scope &&
                Object.entries(feature.scope).map(([name, value]) => value !== undefined && <span key={name}>{scopeLine(name, String(value))}</span>)}
              {Object.entries(featureLimits(feature)).map(
                ([name, value]) => value !== undefined && <span key={name}>{labelled(fieldLabel(name), String(value))}</span>,
              )}
              {feature.limits.oneofKind === "geometry" && <GeometryDetails geometry={feature.limits.geometry} />}
              {feature.limits.oneofKind === "capacity" && <CapacityDetails capacity={feature.limits.capacity} />}
              {feature.limits.oneofKind === "encryption" && <EncryptionDetails encryption={feature.limits.encryption} />}
            </Stack>
          ))}
        </>
      )}
    </Stack>
  );
};

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
      if (sequence === refreshRequest.current) setRefreshError(errorMessage(error, t`Could not refresh devices`));
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
      if (sequence === request.current) setInspectError(errorMessage(error, t`Could not inspect device`));
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
        label={t`Tape device`}
        fullWidth
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        {entries.map((device) => (
          <MenuItem key={device.device} value={device.device} disabled={device.busy} sx={{ whiteSpace: "normal", overflowWrap: "anywhere" }}>
            {device.device}
            {device.busy ? ` · ${device.detail || t`Busy`}` : device.detail ? ` · ${device.detail}` : ""}
          </MenuItem>
        ))}
      </TextField>
      <ActionRow>
        <Button disabled={disabled || refreshing || loading} onClick={() => void refresh()}>
          {refreshing ? t`Refreshing devices…` : t`Refresh devices`}
        </Button>
        <Button
          disabled={disabled || loading || !entries.length}
          onClick={() => {
            setDetailsOpen(true);
            void inspect(value || entries[0].device);
          }}
        >
          {t`Device details`}
        </Button>
      </ActionRow>
      {loading && <p role="status">{t`Loading devices…`}</p>}
      {!loading && devices === null && <Alert severity="info">{t`Device availability is unknown. Refresh devices to try again.`}</Alert>}
      {!loading && devices !== null && !devices.length && <Alert severity="info">{t`No Tape drive is configured.`}</Alert>}
      {refreshError && <Feedback severity="error">{refreshError}</Feedback>}
      {detailsOpen && (
        <Dialog open onClose={closeDetails} maxWidth="sm" fullWidth>
          <DialogTitle>{t`Device details`}</DialogTitle>
          <DialogContent>
            <TextField
              select
              margin="normal"
              label={t`Device to inspect`}
              fullWidth
              value={detailDevice}
              onChange={(event) => void inspect(event.target.value)}
            >
              {entries.map((device) => (
                <MenuItem key={device.device} value={device.device}>
                  {device.device}
                  {device.busy ? ` · ${t`Busy`}` : ""}
                </MenuItem>
              ))}
            </TextField>
            {inspecting && <p role="status">{t`Inspecting device…`}</p>}
            {inspectError && <Feedback severity="error">{inspectError}</Feedback>}
            {reply && <DeviceInspectionResult reply={reply} />}
          </DialogContent>
          <DialogActions>
            <Button disabled={inspecting} onClick={() => void inspect(detailDevice)}>
              {t`Refresh device details`}
            </Button>
            <Button onClick={closeDetails}>{t`Close`}</Button>
          </DialogActions>
        </Dialog>
      )}
    </>
  );
};
