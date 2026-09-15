export const FileBrowserType = "file";
export const MediaType = "media";
export const LocationsType = "settings/locations";
export const JobsType = "jobs";
export const jobListPath = (returnTo: unknown): string => (typeof returnTo === "string" && returnTo.startsWith("/jobs?") ? returnTo : "/jobs");
export const BackupType = "backup";
export const RestoreType = "restore";
export const ScanType = "scan";
export const SettingsType = "settings";

export const libraryLayouts = {
  dual: "dual",
  inspector: "inspector",
} as const;

export type LibraryLayout = (typeof libraryLayouts)[keyof typeof libraryLayouts];
