import { FileData } from "@samuelncui/chonky";
import { GrpcWebFetchTransport } from "@protobuf-ts/grpcweb-transport";
import { ServiceClient, File, SourceFile, Tape, Position } from "./entity";

import moment from "moment";

export const MODE_DIR = 2147483648n; // d: is a directory
export const JOB_STATUS_VISIBLE = 128;

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

export const cli = (() => {
  return new ServiceClient(
    new GrpcWebFetchTransport({
      baseUrl: apiBase,
      format: "binary",
    }),
  );
})();
(window as any).cli = cli;

export const Root: FileData = {
  id: "0",
  name: "Root",
  isDir: true,
  openable: true,
  selectable: true,
  draggable: true,
  droppable: true,
};

export function convertFiles(files: Array<File>, dirWithSize: boolean = false): FileData[] {
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
      size: isDir ? undefined : Number(file.size),
      modDate: moment.unix(Number(file.modTime)).toDate(),
    };
  });
}

export function convertTapes(tapes: Array<Tape>): FileData[] {
  return tapes.map((tape) => {
    // const isDir = (file.mode & ModeDir) > 0;

    return {
      id: `${tape.id}`,
      name: tape.barcode,
      ext: "",
      isDir: true,
      isHidden: false,
      openable: true,
      selectable: true,
      draggable: false,
      droppable: false,
      size: Number(tape.writenBytes),
      modDate: moment.unix(Number(tape.createTime)).toDate(),
      isTape: true,
    };
  });
}

export function convertPositions(positions: Array<Position>): FileData[] {
  return positions.map((posi) => {
    const isDir = (posi.mode & MODE_DIR) > 0;
    const name = isDir ? splitPath(posi.path.slice(0, -1)) : splitPath(posi.path);

    return {
      id: `${posi.tapeId}:${posi.path}`,
      name: name,
      ext: extname(name),
      isDir: isDir,
      isHidden: false,
      openable: isDir,
      selectable: false,
      draggable: false,
      droppable: false,
      size: Number(posi.size),
      modDate: moment.unix(Number(posi.writeTime)).toDate(),
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
