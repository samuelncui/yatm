// @vitest-environment node

import { describe, expect, it } from "vitest";
import { errorMessage, formatFilesize } from "./tools";
import { RpcError } from "@protobuf-ts/runtime-rpc";

describe("errorMessage", () => {
  it("decodes wire-encoded RPC text once, preserving literal percent escapes in filenames", () => {
    expect(errorMessage(new RpcError('directory "/a%2520b"%0Anot%20permitted', "PERMISSION_DENIED"), "Unavailable")).toBe('directory "/a%20b"\nnot permitted');
  });

  it("preserves ordinary error messages and the unknown-error fallback", () => {
    expect(errorMessage(new Error("File a%20b not found"), "Unavailable")).toBe("File a%20b not found");
    expect(errorMessage(undefined, "Unavailable")).toBe("Unavailable");
  });

  it("keeps malformed RPC text readable without throwing from the error handler", () => {
    expect(errorMessage(new RpcError("Invalid %ZZ escape", "UNKNOWN"), "Unavailable")).toBe("Invalid %ZZ escape");
  });
});

describe("Catalog byte formatting", () => {
  it("accepts bigint counters directly without coercing their type", () => {
    expect(formatFilesize(0n)).toBe("0 B");
    expect(formatFilesize(1024n)).toBe("1 KB");
    expect(formatFilesize(2n ** 60n)).toBe("1 EB");
    expect(formatFilesize(1024)).toBe("1 KB");
  });
});
