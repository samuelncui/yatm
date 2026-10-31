import { act, renderHook } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { DeviceSummary } from "@/entity";
import { useTapeDevices } from "./use-tape-devices";

const { listDevices } = vi.hoisted(() => ({ listDevices: vi.fn() }));
vi.mock("@/api", () => ({ mediaCli: { listDevices } }));

const drive = DeviceSummary.create({ device: "drive", kind: 1 });
const reply = (devices: DeviceSummary[]) => ({ response: Promise.resolve({ devices }) });
const pending = () => {
  let finish!: (value: { devices: DeviceSummary[] }) => void;
  const response = new Promise<{ devices: DeviceSummary[] }>((resolve) => (finish = resolve));
  return { call: { response }, finish };
};

beforeEach(() => listDevices.mockReset());

it("keeps availability unknown until a load succeeds and reports a failed load", async () => {
  const { result } = renderHook(() => useTapeDevices("Fallback failure"));
  expect(result.current).toMatchObject({ devices: null, loading: false, error: "" });

  listDevices.mockReturnValueOnce({ response: Promise.reject("offline") });
  await act(async () => expect(await result.current.load()).toBeNull());
  expect(result.current).toMatchObject({ devices: null, loading: false, error: "Fallback failure" });

  listDevices.mockReturnValueOnce(reply([drive]));
  await act(async () => expect(await result.current.load()).toEqual([drive]));
  expect(result.current).toMatchObject({ devices: [drive], loading: false, error: "" });
});

it("rejects a failed refresh without replacing the list and clears the error after success", async () => {
  const { result } = renderHook(() => useTapeDevices());
  listDevices.mockReturnValueOnce({ response: Promise.reject(new Error("Device list offline")) });
  await act(() => result.current.load());
  expect(result.current.error).toBe("Device list offline");

  listDevices.mockReturnValueOnce({ response: Promise.reject(new Error("Still offline")) });
  await act(() => expect(result.current.refresh()).rejects.toThrow("Still offline"));
  expect(result.current).toMatchObject({ devices: null, loading: false, error: "Device list offline" });

  listDevices.mockReturnValueOnce(reply([drive]));
  await act(async () => expect(await result.current.refresh()).toEqual([drive]));
  expect(result.current).toMatchObject({ devices: [drive], error: "" });
});

it("ignores replies superseded by a newer read or reset", async () => {
  const { result } = renderHook(() => useTapeDevices());
  const first = pending();
  const second = pending();
  listDevices.mockReturnValueOnce(first.call).mockReturnValueOnce(second.call);

  let older!: Promise<DeviceSummary[] | null>;
  let newer!: Promise<DeviceSummary[] | null>;
  act(() => {
    older = result.current.load();
    newer = result.current.refresh();
  });
  expect(result.current.loading).toBe(true);
  await act(async () => first.finish({ devices: [DeviceSummary.create({ device: "stale" })] }));
  expect(await older).toBeNull();
  expect(result.current).toMatchObject({ devices: null, loading: true });

  act(() => result.current.reset());
  await act(async () => second.finish({ devices: [drive] }));
  expect(await newer).toBeNull();
  expect(result.current).toMatchObject({ devices: null, loading: false, error: "" });
});
