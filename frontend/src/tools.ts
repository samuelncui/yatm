import { IntlShape } from "react-intl";
import { Nullable } from "tsdef";

import { filesize } from "filesize";
import { I18nConfig, FileData, defaultFormatters } from "@samuelncui/chonky";

export const hexEncode = (buf: string) => {
  var str = "";
  for (var i = 0; i < buf.length; i++) {
    str += buf[i].charCodeAt(0).toString(16);
  }
  return str;
};

export const formatFilesize = (size: number | bigint): string => filesize(size as any as number, { standard: "iec" }) as string;

export const download = (buf: Uint8Array, filename: string, contentType: string) => {
  const blob = new Blob([buf], { type: contentType });

  const link = document.createElement("a");
  link.href = window.URL.createObjectURL(blob);
  link.download = filename;
  link.click();
};

export const sleep = (ms: number): Promise<null> =>
  new Promise((resolve) => {
    setTimeout(resolve, ms);
  });

export const chonkyI18n: I18nConfig = {
  formatters: {
    ...defaultFormatters,
    formatFileSize: (_intl: IntlShape, file: Nullable<FileData>): Nullable<string> => {
      if (!file || typeof file.size !== "number") return null;
      return filesize(file.size, { standard: "iec" }) as string;
    },
  },
};
