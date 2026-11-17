import { t } from "@lingui/core/macro";
import type { InspectDeviceResponse, Media } from "@/entity";

/** Formats YATM writes; older recorded formats remain readable but cannot be chosen for new writes. */
export const writableTapeFormats = ["ltfs_v1", "tar_pax_v1"];
/** The format FORMAT uses when a device has no saved default. */
export const defaultTapeFormat = "tar_pax_v1";

export const tapeFormatOf = (media?: Media) => (media?.profile?.kind.oneofKind === "tape" ? media.profile.kind.tape.format : "");
export const isWritableTapeFormat = (format: string) => writableTapeFormats.includes(format);
export const isLtfsFormat = (format: string) => format === "ltfs_v0" || format === "ltfs_v1";
export const tapeFormatLabel = (format: string) => (isLtfsFormat(format) ? "LTFS" : format === "tar_pax_v1" ? "Tar" : format || t`Unknown`);

// LTFS needs the partitions LTO-5 (density code 0x58) introduced; later LTO generations report higher codes.
const ltfsMinimumDensity = 0x58;
export const ltfsUnsupportedReason = () => t`This drive does not support LTFS (needs LTO-5 or later).`;

/** Whether the report knows the drive supports no LTO-5 or later density; unknown densities keep LTFS available. */
export const ltfsUnsupported = (report?: InspectDeviceResponse | null): boolean => {
  const codes = report?.report.oneofKind === "tape" ? report.report.tape.supportedDensityCodes : [];
  return codes.length > 0 && Math.max(...codes) < ltfsMinimumDensity;
};
