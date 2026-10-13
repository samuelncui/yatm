import { useState } from "react";
import { act, screen } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { Location, type IdenticalRoot } from "@/entity";
import { IdenticalScopePicker } from "./identical-scope";

const { list, get, report } = vi.hoisted(() => ({ list: vi.fn(), get: vi.fn(), report: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list, get } }));
vi.mock("react-toastify", () => ({ toast: { error: report } }));
const call = (locations: Location[], hasMore = false) => ({ response: Promise.resolve({ locations, hasMore }) });
const Picker = ({ disabled = false }: { disabled?: boolean }) => {
  const [value, setValue] = useState<IdenticalRoot[]>([]);
  return (
    <>
      <IdenticalScopePicker value={value} onChange={setValue} disabled={disabled} />
      <button>Next field</button>
      <output aria-label="Selected Locations">{value.map((root) => String(root.locationId)).join(",") || "None"}</output>
    </>
  );
};

beforeEach(() => {
  vi.resetAllMocks();
  list.mockReturnValue(call([]));
  get.mockReturnValue({ response: Promise.resolve({}) });
});

it("hydrates a preselected Location label without opening or enumerating choices", async () => {
  get.mockReturnValue({ response: Promise.resolve({ location: Location.create({ id: 99n, name: "Deep-linked Location" }) }) });
  const onChange = vi.fn();
  render(<IdenticalScopePicker value={[{ locationId: 99n }]} onChange={onChange} />);
  expect(await screen.findByText("Deep-linked Location")).toBeInTheDocument();
  expect(screen.queryByText("Location 99")).not.toBeInTheDocument();
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  expect(get).toHaveBeenCalledExactlyOnceWith({ id: 99n }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
  expect(list).not.toHaveBeenCalled();
  expect(onChange).not.toHaveBeenCalled();
});

it.each(["changed", "removed", "unmounted"] as const)("cancels preselected-label lookup (%s) and ignores its late reply", async (action) => {
  let complete!: (reply: unknown) => void;
  get.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  const onChange = vi.fn();
  const view = render(<IdenticalScopePicker value={[{ locationId: 99n }]} onChange={onChange} />);
  const signal = get.mock.calls[0][1].abort as AbortSignal;
  expect(screen.getByText("Location 99")).toBeInTheDocument();
  if (action === "unmounted") view.unmount();
  else {
    if (action === "changed") get.mockReturnValueOnce({ response: Promise.resolve({ location: Location.create({ id: 100n, name: "Current Location" }) }) });
    view.rerender(<IdenticalScopePicker value={action === "changed" ? [{ locationId: 100n }] : []} onChange={onChange} />);
    if (action === "changed") await screen.findByText("Current Location");
  }
  expect(signal.aborted).toBe(true);
  await act(async () => complete({ location: Location.create({ id: 99n, name: "Obsolete label" }) }));
  expect(screen.queryByText("Obsolete label")).not.toBeInTheDocument();
  expect(screen.queryByText("Location 99")).not.toBeInTheDocument();
  if (action === "changed") expect(screen.getByText("Current Location")).toBeInTheDocument();
  if (action === "removed") {
    get.mockReturnValueOnce({ response: Promise.resolve({ location: Location.create({ id: 99n, name: "Fresh label" }) }) });
    view.rerender(<IdenticalScopePicker value={[{ locationId: 99n }]} onChange={onChange} />);
    expect(await screen.findByText("Fresh label")).toBeInTheDocument();
    expect(get).toHaveBeenCalledTimes(2);
  }
  expect(list).not.toHaveBeenCalled();
  expect(onChange).not.toHaveBeenCalled();
  expect(report).not.toHaveBeenCalled();
});

it("retains the explicit selected ID on lookup failure without toasts or automatic enumeration", async () => {
  get.mockImplementation(() => ({ response: Promise.reject(new Error("Location unavailable")) }));
  const onChange = vi.fn();
  const view = render(<IdenticalScopePicker value={[{ locationId: 99n }]} onChange={onChange} />);
  await act(async () => {});
  view.rerender(<IdenticalScopePicker value={[{ locationId: 99n }]} onChange={onChange} disabled />);
  expect(screen.getByText("Location 99")).toBeInTheDocument();
  expect(get).toHaveBeenCalledOnce();
  expect(list).not.toHaveBeenCalled();
  expect(onChange).not.toHaveBeenCalled();
  expect(report).not.toHaveBeenCalled();
});

it("allows selection beyond 50 Locations and preserves the selected label across searches and openings", async () => {
  const firstPage = Array.from({ length: 50 }, (_, index) => Location.create({ id: BigInt(index + 1), name: `Registered ${index + 1}` }));
  const next = Location.create({ id: 51n, name: "Beyond first page" });
  list.mockReturnValueOnce(call(firstPage, true)).mockReturnValueOnce(call([firstPage[49], next]));
  render(<Picker />);
  expect(list).not.toHaveBeenCalled();
  const input = screen.getByRole("combobox", { name: "Locations" });
  await userEvent.click(input);
  expect(await screen.findByRole("option", { name: "Beyond first page" })).toBeInTheDocument();
  expect(screen.getAllByRole("option", { name: "Registered 50" })).toHaveLength(1);
  expect(list.mock.calls[1][0]).toEqual({ afterId: 50n, limit: 50, query: "" });
  await userEvent.click(screen.getByRole("option", { name: "Beyond first page" }));
  expect(screen.getByRole("status", { name: "Selected Locations" })).toHaveTextContent("51");
  expect(get).not.toHaveBeenCalled();
  await userEvent.type(input, "missing");
  expect(list).toHaveBeenCalledTimes(2);
  expect(input).toHaveValue("missing");
  expect(screen.getByRole("option", { name: "Beyond first page" })).toBeInTheDocument();
  expect(screen.queryByText("Location 51")).not.toBeInTheDocument();
  await userEvent.keyboard("{Escape}");
  expect(screen.getByText("Beyond first page")).toBeInTheDocument();
  expect(screen.getByRole("status", { name: "Selected Locations" })).toHaveTextContent("51");
});

it("retries an incomplete catalog through the keyboard footer without selecting a Location", async () => {
  const first = Location.create({ id: 4n, name: "First" });
  const next = Location.create({ id: 5n, name: "Next" });
  list.mockReturnValueOnce(call([first], true));
  list.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Next page unavailable")) }));
  list.mockReturnValueOnce(call([first, next]));
  render(<Picker />);
  const input = screen.getByRole("combobox", { name: "Locations" });
  await userEvent.click(input);
  expect(await screen.findByRole("alert")).toHaveTextContent("Next page unavailable");
  expect(screen.queryByRole("button", { name: "More Locations" })).not.toBeInTheDocument();
  await userEvent.tab();
  expect(screen.getByRole("button", { name: "Retry Locations" })).toHaveFocus();
  await userEvent.tab({ shift: true });
  expect(input).toHaveFocus();
  await userEvent.tab();
  await userEvent.keyboard("{Escape}");
  expect(input).toHaveFocus();
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  await userEvent.click(input);
  await userEvent.tab();
  await userEvent.keyboard("{Enter}");
  expect(await screen.findByRole("option", { name: "Next" })).toBeInTheDocument();
  expect(screen.getByRole("option", { name: "First" })).toBeInTheDocument();
  expect(list.mock.calls[2][0]).toEqual({ afterId: 0n, limit: 50, query: "" });
  expect(screen.getByRole("status", { name: "Selected Locations" })).toHaveTextContent("None");
  expect(input).toHaveFocus();
  expect(report).not.toHaveBeenCalled();
});

it("shows an initial empty error inline and retries without selecting a Location", async () => {
  list.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Locations unavailable")) }));
  list.mockReturnValueOnce(call([Location.create({ id: 2n, name: "Available" })]));
  render(<Picker />);
  await userEvent.click(screen.getByRole("combobox", { name: "Locations" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Locations unavailable");
  await userEvent.click(screen.getByRole("button", { name: "Retry Locations" }));
  expect(await screen.findByRole("option", { name: "Available" })).toBeInTheDocument();
  expect(list.mock.calls[1][0].afterId).toBe(0n);
  expect(screen.getByRole("status", { name: "Selected Locations" })).toHaveTextContent("None");
  expect(report).not.toHaveBeenCalled();
});

it("applies the current search to a shared catalog response without issuing another request", async () => {
  let resolve!: (reply: unknown) => void;
  list.mockReturnValueOnce({
    response: new Promise((complete) => {
      resolve = complete;
    }),
  });
  render(<Picker />);
  const input = screen.getByRole("combobox", { name: "Locations" });
  await userEvent.click(input);
  await userEvent.type(input, "new");
  expect(list).toHaveBeenCalledOnce();
  await act(async () =>
    resolve({ locations: [Location.create({ id: 1n, name: "Other" }), Location.create({ id: 2n, name: "New directory" })], hasMore: false }),
  );
  expect(screen.getByRole("option", { name: "New directory" })).toBeInTheDocument();
  expect(screen.queryByRole("option", { name: "Other" })).not.toBeInTheDocument();
  expect(report).not.toHaveBeenCalled();
});

it.each(["close", "disable", "unmount"] as const)("keeps a shared catalog failure out of a closed chooser after %s", async (action) => {
  let reject!: (error: Error) => void;
  list.mockReturnValueOnce({
    response: new Promise((_, fail) => {
      reject = fail;
    }),
  });
  const view = render(<Picker />);
  await userEvent.click(screen.getByRole("combobox", { name: "Locations" }));
  if (action === "close") await userEvent.keyboard("{Escape}");
  else if (action === "disable") view.rerender(<Picker disabled />);
  else view.unmount();
  await act(async () => reject(new Error("Closed query failed")));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(report).not.toHaveBeenCalled();
  expect(list).toHaveBeenCalledOnce();
});
