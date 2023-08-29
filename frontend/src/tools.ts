import { filesize } from "filesize";

export const hexEncode = (buf: string) => {
  var str = "";
  for (var i = 0; i < buf.length; i++) {
    str += buf[i].charCodeAt(0).toString(16);
  }
  return str;
};

export const formatFilesize = (size: number | bigint): string =>
  filesize(size, {
    base: 2,
    standard: "jedec",
  }) as string;

export const download = (buf: Uint8Array, filename: string, contentType: string) => {
  const blob = new Blob([buf], { type: contentType });

  const link = document.createElement("a");
  link.href = window.URL.createObjectURL(blob);
  link.download = filename;
  link.click();
};
