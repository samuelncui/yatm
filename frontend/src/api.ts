import type { FileData } from "@samuelncui/chonky";
import { GrpcWebFetchTransport } from "@protobuf-ts/grpcweb-transport";
import {
  ArchiveJobServiceClient,
  JobServiceClient,
  MediaAccess,
  MediaKind,
  RestoreJobServiceClient,
  ScanJobServiceClient,
  FileOperationServiceClient,
  FilesServiceClient,
  LocationServiceClient,
  FileCatalogServiceClient,
  ServiceClient,
  SettingsServiceClient,
  VolumeType,
} from "@/entity";
import type { File, FileSearchResult, Media, Position, SourceFile } from "@/entity";

import moment from "moment";
import { backupIndicator } from "@/components/content-status";

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
  interceptors: [
    {
      interceptUnary(next, method, input, options) {
        console.log(`[gRPC Request] ${method.name}`, input);
        const call = next(method, input, options);
        call.response
          .then((resp) => {
            console.log(`[gRPC Response] ${method.name}`, resp);
          })
          .catch((err) => {
            console.error(`[gRPC Error] ${method.name}`, err);
          });
        return call;
      },
    },
  ],
});

export const cli = new ServiceClient(transport);
export const jobCli = new JobServiceClient(transport);
export const archiveJobCli = new ArchiveJobServiceClient(transport);
export const restoreJobCli = new RestoreJobServiceClient(transport);
export const scanJobCli = new ScanJobServiceClient(transport);
export const fileOperationCli = new FileOperationServiceClient(transport);
export const filesCli = new FilesServiceClient(transport);
export const locationCli = new LocationServiceClient(transport);
export const fileCatalogCli = new FileCatalogServiceClient(transport);
export const settingsCli = new SettingsServiceClient(transport);
(window as any).cli = {
  service: cli,
  jobs: jobCli,
  archiveJobs: archiveJobCli,
  restoreJobs: restoreJobCli,
  scanJobs: scanJobCli,
  locations: locationCli,
  files: fileCatalogCli,
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
  positionID: bigint;
  signature: Uint8Array;
  detailsAvailable: boolean;
};

export const isArchivePosition = (file: FileData | null | undefined): file is MediaPositionFileData =>
  !!file && typeof file.positionID === "bigint" && !file.isDir;

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
      size: !isDir || dirWithSize ? Number(file.size) : undefined,
      modDate: moment.unix(Number(file.modTime)).toDate(),
      parentId: `${file.parentId}`,
      tags: file.tags,
      note: file.note,
      detailsAvailable: true,
      contentSummary: file.contentSummary,
      status: !isDir && file.contentSummary ? backupIndicator(file.contentSummary) : undefined,
    };
  });
}

export function convertSearchResults(results: Array<FileSearchResult>): LibraryFileData[] {
  const converted: LibraryFileData[] = [];
  for (const result of results) {
    if (!result.file) continue;
    const file = convertFiles([result.file])[0];
    converted.push({
      ...file,
      name: result.path,
      ext: extname(result.file.name),
      draggable: false,
      droppable: false,
    });
  }
  return converted;
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
      size: isDir ? undefined : Number(file.size),
      modDate: moment.unix(Number(file.modTime)).toDate(),
    };
  });
}

export function convertMedia(media: Array<Media>): FileData[] {
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
      modDate: moment.unix(Number(value.createTime)).toDate(),
      isMedia: true,
      mediaKind: value.kind,
      mediaIdentity: value.identity,
      mediaMounted: value.mounted,
      mediaAvailableBytes: value.filesystemAvailableBytes,
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

function accessLabel(access?: MediaAccess): string {
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
      size: Number(posi.size),
      modDate: moment.unix(Number(posi.writeTime)).toDate(),
      detailsAvailable,
      positionID: posi.id,
      signature: posi.signature,
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
