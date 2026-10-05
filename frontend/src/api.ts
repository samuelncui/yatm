import type { FileData } from "@samuelncui/chonky";
import { GrpcWebFetchTransport } from "@protobuf-ts/grpcweb-transport";
import {
  ArchiveJobServiceClient,
  JobServiceClient,
  MediaAccess,
  MediaKind,
  RestoreJobServiceClient,
  ScanJobServiceClient,
  FilesServiceClient,
  LibraryServiceClient,
  LocationServiceClient,
  MediaServiceClient,
  PreviewServiceClient,
  SettingsServiceClient,
  VolumeType,
} from "@/entity";
import type { File, Media, Position, SourceFile } from "@/entity";

import { dateFromNs } from "@/tools/time";
import { archiveIndicator } from "@/components/content-status";

export const MODE_DIR = 2147483648n; // d: is a directory

const apiBase: string = (() => {
  const base = (window as any).apiBase as string;
  if (!base || base === "%%API_BASE%%") {
    return "http://localhost:5173/services";
  }
  return base;
})();

export const fileBase: string = (() => {
  return apiBase.replace("/services", "/files");
})();

const transport = new GrpcWebFetchTransport({
  baseUrl: apiBase,
  format: "binary",
});

export const cli = new LibraryServiceClient(transport);
export const mediaCli = new MediaServiceClient(transport);
export const previewCli = new PreviewServiceClient(transport);
export const jobCli = new JobServiceClient(transport);
export const archiveJobCli = new ArchiveJobServiceClient(transport);
export const restoreJobCli = new RestoreJobServiceClient(transport);
export const scanJobCli = new ScanJobServiceClient(transport);
export const filesCli = new FilesServiceClient(transport);
export const locationCli = new LocationServiceClient(transport);
export const settingsCli = new SettingsServiceClient(transport);
(window as any).cli = {
  service: cli,
  jobs: jobCli,
  archiveJobs: archiveJobCli,
  restoreJobs: restoreJobCli,
  scanJobs: scanJobCli,
  locations: locationCli,
  files: filesCli,
};

export const Root: FileData = {
  id: "0",
  name: "Library",
  isDir: true,
  openable: true,
  selectable: true,
  draggable: true,
  droppable: true,
};

export type LibraryFileData = FileData & {
  parentId: string;
  tags: string[];
  note: string;
  detailsAvailable: true;
};

export type MediaPositionFileData = FileData & {
  /** The Position this browser row stands for; its detail reads these facts. */
  position: Position;
  detailsAvailable: boolean;
};

export type MediaFileData = FileData & {
  isMedia: true;
  /** The Media this browser row stands for; its detail reads these facts. */
  media: Media;
};

export const isMediaFile = (file: FileData | null | undefined): file is MediaFileData => file?.isMedia === true;

export const isArchivePosition = (file: FileData | null | undefined): file is MediaPositionFileData =>
  !!file && typeof file.position?.id === "bigint" && !file.isDir;

export function convertFiles(files: Array<File>, dirWithSize: boolean = false): LibraryFileData[] {
  return files.map((file) => {
    const isDir = (file.mode & MODE_DIR) > 0;

    return {
      id: `${file.id}`,
      name: file.name,
      ext: extname(file.name),
      isDir,
      isHidden: file.name.startsWith("."),
      openable: true,
      selectable: true,
      draggable: true,
      droppable: isDir,
      size: !isDir || dirWithSize ? Number(file.sizeBytes) : undefined,
      modDate: dateFromNs(file.mtimeNs),
      modDateNs: file.mtimeNs,
      parentId: `${file.parentId}`,
      tags: file.tags,
      note: file.note,
      detailsAvailable: true,
      contentSummary: file.contentSummary,
      status: !isDir && file.contentSummary ? archiveIndicator(file.contentSummary) : undefined,
    };
  });
}

export function convertSourceFiles(files: Array<SourceFile>): FileData[] {
  return files.map((file) => {
    const isDir = (file.mode & MODE_DIR) > 0;

    return {
      id: file.path,
      name: file.name,
      ext: extname(file.name),
      isDir,
      isHidden: file.name.startsWith("."),
      openable: isDir,
      selectable: true,
      draggable: true,
      droppable: false,
      size: isDir ? undefined : Number(file.sizeBytes),
      modDate: dateFromNs(file.mtimeNs),
      modDateNs: file.mtimeNs,
    };
  });
}

export function convertMedia(media: Array<Media>): MediaFileData[] {
  return media.map((value) => {
    const label = mediaCapabilityLabel(value);
    return {
      id: `${value.id}`,
      name: value.name || value.identity,
      details: [label],
      ext: "",
      isDir: true,
      isHidden: false,
      openable: true,
      selectable: true,
      draggable: false,
      droppable: false,
      size: Number(value.writtenBytes),
      modDate: dateFromNs(value.createdAtNs),
      modDateNs: value.createdAtNs,
      isMedia: true,
      media: value,
    };
  });
}

function mediaCapabilityLabel(media: Media): string {
  const kind =
    media.kind === MediaKind.TAPE
      ? "Tape"
      : media.profile?.kind.oneofKind === "volume" && media.profile.kind.volume.type === VolumeType.HM_SMR
        ? "HM-SMR"
        : "HDD";
  const read = accessLabel(media.capabilities?.read);
  const write = accessLabel(media.capabilities?.write);
  const availability = mediaAvailabilityLabel(media);
  return `${kind} · ${read} read · ${write} write · ${availability}`;
}

export function mediaAvailabilityLabel(media: Media): string {
  if (media.capabilities?.read !== MediaAccess.CONCURRENT_RANDOM) return "Restore required";
  if (media.kind === MediaKind.VOLUME && media.mounted !== true) return "Mount required";
  return "Online";
}

export function accessLabel(access?: MediaAccess): string {
  switch (access) {
    case MediaAccess.CONCURRENT_RANDOM:
      return "concurrent random";
    case MediaAccess.RANDOM:
      return "random";
    case MediaAccess.SEQUENTIAL:
      return "sequential";
    default:
      return "unknown";
  }
}

export function convertPositions(positions: Array<Position>): MediaPositionFileData[] {
  return positions.map((posi) => {
    const isDir = (posi.mode & MODE_DIR) > 0;
    const detailsAvailable = !isDir;
    const name = isDir ? splitPath(posi.path.slice(0, -1)) : splitPath(posi.path);

    return {
      id: `${posi.mediaId}:${posi.path}`,
      name: name,
      ext: extname(name),
      isDir: isDir,
      isHidden: false,
      openable: isDir || detailsAvailable,
      selectable: true,
      draggable: false,
      droppable: false,
      size: Number(posi.sizeBytes),
      modDate: dateFromNs(posi.writtenAtNs),
      modDateNs: posi.writtenAtNs,
      detailsAvailable,
      position: posi,
    };
  });
}

function splitPath(filename: string): string {
  const idx = filename.lastIndexOf("/");
  if (idx < 0) {
    return filename;
  }
  return filename.slice(idx + 1);
}

function extname(filename: string): string {
  const idx = filename.lastIndexOf(".");
  if (idx < 0) {
    return "";
  }
  return filename.slice(idx);
}
