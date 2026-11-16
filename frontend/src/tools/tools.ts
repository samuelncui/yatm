import { IntlShape } from "react-intl";
import { Nullable } from "tsdef";

import { filesize } from "filesize";
import { I18nConfig, FileData, defaultFormatters } from "@samuelncui/chonky";
import { toast } from "react-toastify";
import { RpcError } from "@protobuf-ts/runtime-rpc";

export const errorMessage = (error: unknown, fallback: string): string => {
  if (!(error instanceof Error)) return fallback;
  // The installed gRPC-Web transport retains percent-encoded grpc-message text.
  if (!(error instanceof RpcError)) return error.message;
  try {
    return decodeURIComponent(error.message);
  } catch {
    return error.message;
  }
};

export const runUIAction = (action: () => Promise<unknown>, fallback: string): void => {
  void action().catch((error: unknown) => toast.error(errorMessage(error, fallback)));
};

export const formatFilesize = (size: number | bigint): string =>
  filesize(size, {
    base: 2,
    standard: "jedec",
  });

export const chonkyI18n: I18nConfig = {
  formatters: {
    ...defaultFormatters,
    formatFileSize: (_intl: IntlShape, file: Nullable<FileData>): Nullable<string> => {
      if (!file || typeof file.size !== "number") return null;
      return formatFilesize(file.size);
    },
  },
};
