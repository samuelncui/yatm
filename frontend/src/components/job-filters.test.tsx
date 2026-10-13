import { act, screen } from "@testing-library/react";
import { render } from "@/state/test-render";
import { createAppStore } from "@/state/store";
import { loadLocations, saveLocation } from "@/state/locations";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { JobKind, Location, Media } from "@/entity";
const { locations, media } = vi.hoisted(() => ({ locations: vi.fn(), media: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list: locations }, mediaCli: { list: media } }));
import { JobFilters } from "./job-filters";

function FilterPage() {
  const location = useLocation();
  return (
    <>
      <JobFilters />
      <output aria-label="Query">{location.search}</output>
    </>
  );
}

const source = Location.create({ id: 4n, name: "Photos", rootPath: "/photos" });
const volume = Media.create({ id: 7n, name: "Archive" });
beforeEach(() => {
  vi.resetAllMocks();
  locations.mockReturnValue({ response: Promise.resolve({ locations: [source], hasMore: false }) });
  media.mockReturnValue({ response: Promise.resolve({ media: [volume], hasMore: false }) });
});

describe("Jobs filters", () => {
  it("shows named all-values in labeled responsive fields independent of MUI's class prefix", async () => {
    render(
      <MemoryRouter>
        <FilterPage />
      </MemoryRouter>,
    );
    for (const [label, value] of [
      ["Job type", "All types"],
      ["Resource", "All resources"],
    ]) {
      const field = screen.getByRole("combobox", { name: label });
      expect(field).toHaveTextContent(value);
      expect(field.closest(".job-filter")).not.toBeNull();
    }
    await userEvent.click(screen.getByRole("combobox", { name: "Job type" }));
    await userEvent.click(screen.getByRole("option", { name: "Scan" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Resource" }));
    await userEvent.click(screen.getByRole("option", { name: "All resources" }));
    expect(screen.getByLabelText("Query")).toHaveTextContent(`?kind=${JobKind.SCAN}`);
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(screen.getByLabelText("Query")).toBeEmptyDOMElement();
    expect(screen.getByRole("combobox", { name: "Job type" })).toHaveTextContent("All types");
  });

  it("reuses configured Locations, including later edits, without rereading them with Media", async () => {
    const store = createAppStore();
    await loadLocations(store);
    render(
      <MemoryRouter>
        <FilterPage />
      </MemoryRouter>,
      { store },
    );
    await userEvent.click(screen.getByRole("combobox", { name: "Resource" }));
    expect(await screen.findByRole("option", { name: "Media · Archive" })).toBeVisible();
    expect(screen.getByRole("option", { name: "Location · Photos" })).toBeVisible();
    expect(locations).toHaveBeenCalledOnce();
    act(() => saveLocation(store, Location.create({ ...source, name: "Renamed" })));
    expect(screen.getByRole("option", { name: "Location · Renamed" })).toBeVisible();
    expect(screen.queryByRole("option", { name: "Location · Photos" })).not.toBeInTheDocument();
    expect(locations).toHaveBeenCalledOnce();
    expect(media).toHaveBeenCalledOnce();
  });

  it.each(["Location", "Media"])("keeps successful choices when %s fails and retries only that read explicitly", async (kind) => {
    const failed = kind === "Location" ? locations : media;
    const successful = kind === "Location" ? media : locations;
    failed.mockImplementationOnce(() => ({ response: Promise.reject(new Error(`${kind} unavailable`)) }));
    render(
      <MemoryRouter>
        <FilterPage />
      </MemoryRouter>,
    );
    const input = screen.getByRole("combobox", { name: "Resource" });
    await userEvent.click(input);
    expect(await screen.findByText(`${kind} unavailable`)).toBeInTheDocument();
    expect(screen.getByRole("option", { name: kind === "Location" ? "Media · Archive" : "Location · Photos" })).toBeVisible();
    await userEvent.keyboard("{Escape}");
    await userEvent.click(input);
    expect(failed).toHaveBeenCalledOnce();
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await userEvent.click(input);
    expect(await screen.findByRole("option", { name: "Location · Photos" })).toBeVisible();
    expect(await screen.findByRole("option", { name: "Media · Archive" })).toBeVisible();
    expect(failed).toHaveBeenCalledTimes(2);
    expect(successful).toHaveBeenCalledOnce();
  });
});

it("retains Media pages and retries a failed continuation at the same offset", async () => {
  media.mockReturnValueOnce({ response: Promise.resolve({ media: [volume], hasMore: true }) });
  media.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Next page unavailable")) }));
  media.mockReturnValueOnce({ response: Promise.resolve({ media: [Media.create({ id: 8n, name: "Second" })], hasMore: false }) });
  render(
    <MemoryRouter>
      <FilterPage />
    </MemoryRouter>,
  );
  const input = screen.getByRole("combobox", { name: "Resource" });
  await userEvent.click(input);
  await userEvent.click(await screen.findByRole("option", { name: "Load more resources…" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Next page unavailable");
  await userEvent.click(input);
  expect(screen.getByRole("option", { name: "Media · Archive" })).toBeVisible();
  expect(media).toHaveBeenCalledTimes(2);
  await userEvent.keyboard("{Escape}");
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  await userEvent.click(input);
  expect(await screen.findByRole("option", { name: "Media · Second" })).toBeVisible();
  expect(screen.getByRole("option", { name: "Media · Archive" })).toBeVisible();
  expect(media).toHaveBeenCalledTimes(3);
  for (const [request] of media.mock.calls.slice(1)) expect(request.param.list.offset).toBe(1n);
  expect(locations).toHaveBeenCalledOnce();
});
