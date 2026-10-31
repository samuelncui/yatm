import type { FileVersion } from "@/entity";
import { dateFromNs } from "@/tools/time";

export const contentTime = (value?: bigint, fallback = "Date unknown") =>
  dateFromNs(value)?.toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) ?? fallback;

export const versionArchiveTime = (version: FileVersion) => version.lastArchivedAtNs ?? version.firstArchivedAtNs;
/** A saved version's display date; every version list uses the same unrecorded wording. */
export const versionArchiveLabel = (version: FileVersion) => contentTime(versionArchiveTime(version), "Date not recorded");

/** Permission bits in the familiar three-digit octal form. */
export const contentMode = (mode: number | bigint) => (BigInt(mode) & 0o777n).toString(8).padStart(3, "0");

export const contentHex = (value: Uint8Array) => Array.from(value, (byte) => byte.toString(16).padStart(2, "0")).join("");

export type ArchiveTone = "success" | "warning" | "archive" | "error" | "info";
export type ArchiveMarker = { label: string; color: string; kind: "warning" | "changed" | "unknown" };

export const archiveColors = { success: "#15803d", warning: "#b77900", archive: "#2563eb", error: "#dc2626", info: "#87909e" } as const;
