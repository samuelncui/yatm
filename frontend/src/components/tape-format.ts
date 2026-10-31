import type { Media } from "@/entity";

/** Formats YATM writes; older recorded formats remain readable but cannot be chosen for new writes. */
export const writableTapeFormats = ["ltfs_v1", "tar_pax_v1"];

export const tapeFormatOf = (media?: Media) => (media?.profile?.kind.oneofKind === "tape" ? media.profile.kind.tape.format : "");
export const isWritableTapeFormat = (format: string) => writableTapeFormats.includes(format);
export const isLtfsFormat = (format: string) => format === "ltfs_v0" || format === "ltfs_v1";
export const tapeFormatLabel = (format: string) => (isLtfsFormat(format) ? "LTFS" : format === "tar_pax_v1" ? "Tar" : format || "Unknown");
