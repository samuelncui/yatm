import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DeviceSummary,
  DeviceKind,
  InspectDeviceResponse,
  DeviceFeature,
  DeviceFeatureKind,
  DeviceFeatureSupport,
  DeviceFeatureAvailability,
  DeviceFeatureValidation,
  DeviceFeatureEvidence,
  DeviceReason,
  TapeCapacityLimits,
  TapeCapacityQuality,
  TapeCompressionLimits,
  TapeEncryptionLimits,
  TapeGeometryLimits,
  TapePreparedRead,
} from "@/entity";

const { inspectDevice } = vi.hoisted(() => ({ inspectDevice: vi.fn() }));
vi.mock("@/api", () => ({ mediaCli: { inspectDevice } }));
import { TapeDeviceChoice, DeviceInspectionResult, soleAvailableDevice } from "./tape-device-choice";

const busy = DeviceSummary.create({ device: "busy-drive", kind: DeviceKind.TAPE, busy: true, detail: "Archive owns the device" });
const available = DeviceSummary.create({ device: "free-drive", kind: DeviceKind.TAPE });
beforeEach(() => {
  vi.resetAllMocks();
});

describe("Tape device choices", () => {
  it("labels the retained prepared read separately from current device observations", () => {
    render(
      <DeviceInspectionResult
        reply={InspectDeviceResponse.create({
          device: busy,
          preparedRead: TapePreparedRead.create({
            preparedAtNs: 1792586908000000000n,
            jobId: 12n,
            mediaId: 5n,
            identity: "ABC123",
            requestedOrder: "AUTO",
            effectiveOrder: "LOGICAL",
            parametersSource: "learned",
            coverageDetail: "No retained position map",
            learnedMaeSeconds: 0.5,
          }),
        })}
      />,
    );
    expect(screen.getByText("Observed: Unknown")).toBeInTheDocument();
    expect(screen.getByText("Last prepared read")).toBeInTheDocument();
    expect(screen.getByText(/does not identify the currently loaded cartridge/)).toBeInTheDocument();
    expect(screen.getByText("Parameters: learned")).toBeInTheDocument();
    expect(screen.getByText("No retained position map")).toBeInTheDocument();
    expect(screen.getByText("Learned read parameters mean error: 0.5 seconds")).toBeInTheDocument();
  });
  it("keeps failed or unobserved lists unknown and permits explicit refresh", () => {
    render(<TapeDeviceChoice devices={null} value="" onChange={vi.fn()} onRefresh={vi.fn()} />);
    expect(screen.queryByText("No Tape drive is configured.")).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Tape device" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("button", { name: "Refresh devices" })).toBeEnabled();
  });

  it("finds the sole available device while retaining busy rows", async () => {
    expect(soleAvailableDevice([busy, available])).toBe("free-drive");
    expect(soleAvailableDevice([busy])).toBe("");
    expect(soleAvailableDevice([available, DeviceSummary.create({ device: "other" })])).toBe("");
    const refresh = vi.fn().mockResolvedValue(undefined);
    render(<TapeDeviceChoice devices={[busy, available]} value="" onChange={vi.fn()} onRefresh={refresh} />);
    await userEvent.click(screen.getByRole("combobox", { name: "Tape device" }));
    expect(screen.getByRole("option", { name: "busy-drive · Archive owns the device" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("option", { name: "free-drive" })).not.toHaveAttribute("aria-disabled", "true");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Refresh devices" }));
    await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
    expect(inspectDevice).not.toHaveBeenCalled();
  });

  it("inspects only on explicit device details actions, including a busy device", async () => {
    const reply = InspectDeviceResponse.create({ device: busy, detail: "No hardware observation while busy" });
    inspectDevice.mockReturnValue({ response: Promise.resolve(reply) });
    render(<TapeDeviceChoice devices={[busy, available]} value="" onChange={vi.fn()} onRefresh={vi.fn()} />);
    expect(inspectDevice).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Device details" }));
    expect(await screen.findByText("No hardware observation while busy")).toBeInTheDocument();
    expect(screen.getByText("Observed: Unknown")).toBeInTheDocument();
    expect(inspectDevice).toHaveBeenCalledOnce();
    expect(inspectDevice).toHaveBeenCalledWith({ device: "busy-drive" });
    await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
    await waitFor(() => expect(inspectDevice).toHaveBeenCalledTimes(2));
  });

  it("discards a closed details read", async () => {
    let finish!: (reply: InspectDeviceResponse) => void;
    inspectDevice.mockReturnValueOnce({
      response: new Promise((resolve) => {
        finish = resolve;
      }),
    });
    inspectDevice.mockReturnValue({ response: Promise.resolve(InspectDeviceResponse.create({ device: available, model: "Current" })) });
    render(<TapeDeviceChoice devices={[available]} value="free-drive" onChange={vi.fn()} onRefresh={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Device details" }));
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    await userEvent.click(screen.getByRole("button", { name: "Device details" }));
    expect(await screen.findByText("Model: Current")).toBeInTheDocument();
    await act(async () => finish(InspectDeviceResponse.create({ device: available, model: "Obsolete" })));
    expect(screen.queryByText("Model: Obsolete")).not.toBeInTheDocument();
  });

  it("keeps reported support separate from validation and unknown physical facts", () => {
    render(
      <DeviceInspectionResult
        reply={InspectDeviceResponse.create({
          device: available,
          report: {
            oneofKind: "tape",
            tape: {
              writeProtected: false,
              recommendationDetail: "",
              features: [
                DeviceFeature.create({
                  feature: DeviceFeatureKind.LOCATE,
                  support: DeviceFeatureSupport.SUPPORTED,
                  availability: DeviceFeatureAvailability.UNKNOWN,
                  validation: DeviceFeatureValidation.UNVALIDATED,
                  evidence: DeviceFeatureEvidence.REPORTED,
                }),
                DeviceFeature.create({
                  feature: DeviceFeatureKind.END_OF_WRAP_POSITION,
                  support: DeviceFeatureSupport.UNSUPPORTED,
                  availability: DeviceFeatureAvailability.UNAVAILABLE,
                  validation: DeviceFeatureValidation.UNVALIDATED,
                }),
                DeviceFeature.create({
                  feature: DeviceFeatureKind.HARDWARE_ENCRYPTION,
                  support: DeviceFeatureSupport.UNKNOWN,
                  availability: DeviceFeatureAvailability.UNAVAILABLE,
                  validation: DeviceFeatureValidation.UNVALIDATED,
                  reason: DeviceReason.NOT_IMPLEMENTED,
                }),
              ],
            },
          },
        })}
      />,
    );
    expect(screen.getByText(/Support: supported · Availability: unknown · Validation: unvalidated/)).toBeInTheDocument();
    expect(screen.getByText("Medium present: Unknown")).toBeInTheDocument();
    expect(screen.getByText("Write protected: false")).toBeInTheDocument();
    expect(screen.getByText("Evidence: reported")).toBeInTheDocument();
    expect(screen.getByText(/Support: unsupported · Availability: unavailable · Validation: unvalidated/)).toBeInTheDocument();
    expect(screen.getByText("hardware encryption")).toBeInTheDocument();
    expect(screen.getByText(/Support: unknown · Availability: unavailable · Validation: unvalidated/)).toBeInTheDocument();
    expect(screen.getByText("Reason: not implemented")).toBeInTheDocument();
  });
});

it.each([undefined, false, true])("keeps passive encryption state %s distinct from unknown", (encryptionEnabled) => {
  render(
    <DeviceInspectionResult
      reply={InspectDeviceResponse.create({ report: { oneofKind: "tape", tape: { encryptionEnabled, recommendationDetail: "", features: [] } } })}
    />,
  );
  expect(screen.getByText(`Encryption enabled: ${encryptionEnabled === undefined ? "Unknown" : String(encryptionEnabled)}`)).toBeVisible();
});

it.each([TapeCapacityQuality.ESTIMATE, TapeCapacityQuality.LOWER_BOUND, TapeCapacityQuality.UNSPECIFIED])(
  "keeps capacity quality %s, exact bytes and missing allowances distinct",
  (quality) => {
    const issuedAtNs = 1792029600000000000n;
    const completedAtNs = issuedAtNs + 60000000000n;
    render(
      <DeviceInspectionResult
        reply={InspectDeviceResponse.create({
          device: available,
          report: {
            oneofKind: "tape",
            tape: {
              recommendationDetail: "",
              features: [
                DeviceFeature.create({
                  feature: DeviceFeatureKind.CAPACITY_TO_WARNING,
                  support: DeviceFeatureSupport.SUPPORTED,
                  availability: DeviceFeatureAvailability.READY,
                  validation: DeviceFeatureValidation.UNVALIDATED,
                  evidence: DeviceFeatureEvidence.REPORTED,
                  limits: {
                    oneofKind: "capacity",
                    capacity: TapeCapacityLimits.create({
                      quality,
                      partition: 0,
                      remainingBytes: 18446744073709551615n,
                      observationErrorBytes: 0n,
                      issuedAtNs,
                      completedAtNs,
                    }),
                  },
                }),
              ],
            },
          },
        })}
      />,
    );
    expect(
      screen.getByText(
        `Capacity quality: ${quality === TapeCapacityQuality.ESTIMATE ? "Estimate" : quality === TapeCapacityQuality.LOWER_BOUND ? "Lower bound" : "Unknown"}`,
      ),
    ).toBeVisible();
    expect(screen.getByText("Native space before early warning: 18446744073709551615 bytes")).toBeVisible();
    expect(screen.getByText("Partition: 0")).toBeVisible();
    expect(screen.getByText("Observation error allowance: 0 bytes")).toBeVisible();
    expect(screen.getByText("Unreflected write allowance: Unknown")).toBeVisible();
    expect(screen.getByText("Observation started: 2026-10-15T02:00:00.000Z")).toBeVisible();
    expect(screen.getByText("Observation completed: 2026-10-15T02:01:00.000Z")).toBeVisible();
    expect(screen.getByText(/does not guarantee that a file will fit/)).toBeVisible();
    expect(screen.getByText(/Validation: unvalidated/)).toBeVisible();
  },
);

it.each([undefined, 0n])("keeps unknown capacity and observed zero %s distinct", (remainingBytes) => {
  render(
    <DeviceInspectionResult
      reply={InspectDeviceResponse.create({
        report: {
          oneofKind: "tape",
          tape: {
            recommendationDetail: "",
            features: [
              DeviceFeature.create({
                feature: DeviceFeatureKind.CAPACITY_TO_WARNING,
                limits: { oneofKind: "capacity", capacity: TapeCapacityLimits.create({ remainingBytes }) },
              }),
            ],
          },
        },
      })}
    />,
  );
  expect(screen.getByText(`Native space before early warning: ${remainingBytes === undefined ? "Unknown" : "0 bytes"}`)).toBeVisible();
  expect(screen.getByText("Partition: Unknown")).toBeVisible();
  expect(screen.getByText("Observation error allowance: Unknown")).toBeVisible();
  expect(screen.getByText("Observation started: Unknown")).toBeVisible();
  expect(screen.getByText("Observation completed: Unknown")).toBeVisible();
});

it.each([
  { currentAlgorithm: undefined, enableChangeable: undefined },
  { currentAlgorithm: 0, enableChangeable: false },
  { currentAlgorithm: 0xffffffff, enableChangeable: true },
])("shows observed compression algorithm/changeability without inferring qualification: %o", ({ currentAlgorithm, enableChangeable }) => {
  render(
    <DeviceInspectionResult
      reply={InspectDeviceResponse.create({
        report: {
          oneofKind: "tape",
          tape: {
            recommendationDetail: "",
            compressionEnabled: true,
            decompressionEnabled: false,
            features: [
              DeviceFeature.create({
                feature: DeviceFeatureKind.HARDWARE_COMPRESSION,
                support: DeviceFeatureSupport.SUPPORTED,
                availability: DeviceFeatureAvailability.READY,
                validation: DeviceFeatureValidation.UNVALIDATED,
                evidence: DeviceFeatureEvidence.REPORTED,
                limits: { oneofKind: "compression", compression: TapeCompressionLimits.create({ currentAlgorithm, enableChangeable }) },
              }),
            ],
          },
        },
      })}
    />,
  );
  expect(
    screen.getByText(
      currentAlgorithm === undefined
        ? "Current algorithm: Unknown"
        : `Current algorithm: 0x${currentAlgorithm.toString(16).padStart(8, "0")} (${currentAlgorithm})`,
    ),
  ).toBeVisible();
  expect(screen.getByText(`Enable changeable: ${enableChangeable === undefined ? "Unknown" : String(enableChangeable)}`)).toBeVisible();
  expect(screen.getByText("Compression enabled: true")).toBeVisible();
  expect(screen.getByText("Decompression enabled: false")).toBeVisible();
  expect(screen.getByText(/Support: supported · Availability: ready · Validation: unvalidated/)).toBeVisible();
  expect(screen.queryByText("Tar FORMAT recommendations")).not.toBeInTheDocument();
});

it.each([undefined, 0])("keeps encryption protocol counter %s unknown/zero without key-presence claims", (keyInstanceCounter) => {
  render(
    <DeviceInspectionResult
      reply={InspectDeviceResponse.create({
        report: {
          oneofKind: "tape",
          tape: {
            recommendationDetail: "",
            encryptionEnabled: false,
            features: [
              DeviceFeature.create({
                feature: DeviceFeatureKind.HARDWARE_ENCRYPTION,
                support: DeviceFeatureSupport.SUPPORTED,
                availability: DeviceFeatureAvailability.READY,
                validation: DeviceFeatureValidation.UNVALIDATED,
                limits: {
                  oneofKind: "encryption",
                  encryption: TapeEncryptionLimits.create({
                    algorithms: [{ name: "AES-256-GCM-128", index: 17, keyBytes: 32 }],
                    currentAlgorithmIndex: 0,
                    encryptionMode: 0,
                    decryptionMode: 2,
                    iTNexusScope: 0,
                    keyScope: 2,
                    parametersControl: 0,
                    keyInstanceCounter,
                  }),
                },
              }),
            ],
          },
        },
      })}
    />,
  );
  expect(screen.getByText("Reported algorithms: AES-256-GCM-128 · index 17 · key 32 bytes")).toBeVisible();
  expect(screen.getByText("Current algorithm index: 0")).toBeVisible();
  expect(screen.getByText("Encryption mode: 0")).toBeVisible();
  expect(screen.getByText("Decryption mode: 2")).toBeVisible();
  expect(screen.getByText("I_T nexus scope: 0")).toBeVisible();
  expect(screen.getByText("Key scope: 2")).toBeVisible();
  expect(screen.getByText("Parameters control: 0")).toBeVisible();
  expect(screen.getByText(`Key instance counter: ${keyInstanceCounter === undefined ? "Unknown" : "0"}`)).toBeVisible();
  expect(screen.getByText(/does not establish key presence/)).toBeVisible();
  expect(screen.getByText(/Validation: unvalidated/)).toBeVisible();
  expect(screen.queryByText("Tar FORMAT recommendations")).not.toBeInTheDocument();
});

it.each([undefined, false, true])("shows map freshness %s and reported all-wrap command form without qualifying Auto", (fresh) => {
  render(
    <DeviceInspectionResult
      reply={InspectDeviceResponse.create({
        report: {
          oneofKind: "tape",
          tape: {
            recommendationDetail: "",
            features: [
              DeviceFeature.create({
                feature: DeviceFeatureKind.SOFTWARE_READ_ORDER,
                validation: DeviceFeatureValidation.UNVALIDATED,
                detail: "No qualified geometry and timing model; effective order is Logical",
                limits: {
                  oneofKind: "geometry",
                  geometry: TapeGeometryLimits.create({ allWraps: true, reportedWraps: 0, fresh, source: "synthetic REOWP response" }),
                },
              }),
            ],
          },
        },
      })}
    />,
  );
  expect(screen.getByText("All wraps: true")).toBeVisible();
  expect(screen.getByText("Reported wraps: 0")).toBeVisible();
  expect(screen.getByText(`Fresh: ${fresh === undefined ? "Unknown" : String(fresh)}`)).toBeVisible();
  expect(screen.getByText("Source: synthetic REOWP response")).toBeVisible();
  expect(screen.getByText(/reported command form, not qualified full coverage/)).toBeVisible();
  expect(screen.getByText("No qualified geometry and timing model; effective order is Logical")).toBeVisible();
  expect(screen.queryByText("Tar FORMAT recommendations")).not.toBeInTheDocument();
});

it("keeps reported wraps and freshness unknown when only the command form is reported", () => {
  render(
    <DeviceInspectionResult
      reply={InspectDeviceResponse.create({
        report: {
          oneofKind: "tape",
          tape: {
            recommendationDetail: "",
            features: [
              DeviceFeature.create({
                feature: DeviceFeatureKind.END_OF_WRAP_POSITION,
                limits: { oneofKind: "geometry", geometry: TapeGeometryLimits.create({ allWraps: true, source: "RSOC command form" }) },
              }),
            ],
          },
        },
      })}
    />,
  );
  expect(screen.getByText("All wraps: true")).toBeVisible();
  expect(screen.getByText("Reported wraps: Unknown")).toBeVisible();
  expect(screen.getByText("Fresh: Unknown")).toBeVisible();
  expect(screen.getByText(/reported command form, not qualified full coverage/)).toBeVisible();
});
