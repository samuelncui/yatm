import { IntlShape } from "react-intl";
import { Nullable } from "tsdef";

import { filesize } from "filesize";
import { I18nConfig, FileData, defaultFormatters } from "@samuelncui/chonky";
import { toast } from "react-toastify";

export const errorMessage = (error: unknown, fallback: string): string => (error instanceof Error ? error.message : fallback);

export const runUIAction = (action: () => Promise<unknown>, fallback: string): void => {
  try {
    void action().catch((error: unknown) => toast.error(errorMessage(error, fallback)));
  } catch (error) {
    toast.error(errorMessage(error, fallback));
  }
};

export const hexEncode = (buf: string) => {
  let str = "";
  for (let i = 0; i < buf.length; i++) {
    str += buf[i].charCodeAt(0).toString(16);
  }
  return str;
};

export const formatFilesize = (size: number | bigint): string =>
  filesize(size, {
    base: 2,
    standard: "jedec",
  });

export const sleep = (ms: number): Promise<null> =>
  new Promise((resolve) => {
    setTimeout(resolve, ms);
  });

export const chonkyI18n: I18nConfig = {
  formatters: {
    ...defaultFormatters,
    formatFileSize: (_intl: IntlShape, file: Nullable<FileData>): Nullable<string> => {
      if (!file || typeof file.size !== "number") return null;
      return filesize(file.size, {
        base: 2,
        standard: "jedec",
      });
    },
  },
};

export function cleanPath(path: string): string {
  if (path.length === 0) {
    return ".";
  }

  const rooted = path.startsWith("/");
  const parts: string[] = [];
  let i = 0;
  while (i < path.length) {
    while (i < path.length && path[i] === "/") {
      i += 1;
    }
    if (i >= path.length) {
      break;
    }

    const start = i;
    while (i < path.length && path[i] !== "/") {
      i += 1;
    }
    const elem = path.slice(start, i);

    if (elem === ".") {
      continue;
    }

    if (elem === "..") {
      if (parts.length > 0 && parts[parts.length - 1] !== "..") {
        parts.pop();
        continue;
      }
      if (!rooted) {
        parts.push("..");
      }
      continue;
    }

    parts.push(elem);
  }

  const result = `${rooted ? "/" : ""}${parts.join("/")}`;
  if (result.length === 0) {
    return ".";
  }
  return result;
}

export function joinPath(...parts: string[]): string {
  return cleanPath(parts.join("/"));
}
