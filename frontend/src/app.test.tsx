import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/pages/backup", () => ({
  BackupBrowser: () => <div>Archive creation page</div>,
}));
vi.mock("@/pages/restore", () => ({ RestoreBrowser: () => <div>Restore creation page</div> }));
vi.mock("@/pages/scan", () => ({ ScanBrowser: () => <div>Scan creation page</div> }));
vi.mock("@/pages/online", () => ({ LocationsBrowser: () => <div>Locations page</div> }));
vi.mock("@/pages/file", () => ({ FileBrowser: () => <div>Library files page</div> }));
vi.mock("@/pages/media", () => ({ MediaBrowser: () => <div>Library media page</div> }));
vi.mock("@/pages/jobs", () => ({ JobsBrowser: () => <div>Jobs page</div> }));
vi.mock("@/pages/settings", () => ({ SettingsBrowser: () => <div>Settings page</div> }));

import App from "@/app";

const CurrentRoute = () => {
  const location = useLocation();
  return <output aria-label="Current route">{location.pathname + location.search}</output>;
};

describe("Job creation navigation", () => {
  it.each(["All", "Jobs"])("returns from Job details using the already-selected %s navigation link", async (name) => {
    render(
      <MemoryRouter initialEntries={[{ pathname: "/jobs/42", state: { returnTo: "/jobs?kind=1&location=8" } }]}>
        <App />
        <CurrentRoute />
      </MemoryRouter>,
    );
    await screen.findByText("Jobs page");
    const tab = screen.getByRole("tab", { name });
    expect(tab).toHaveAttribute("aria-selected", "true");
    expect(tab).toHaveAttribute("href", "/jobs?kind=1&location=8");
    await userEvent.click(tab);
    expect(screen.getByLabelText("Current route")).toHaveTextContent(/^\/jobs\?kind=1&location=8$/);
  });

  it("offers one Scan entry for all scanning, comparison and Preview stages", async () => {
    render(
      <MemoryRouter initialEntries={["/scan"]}>
        <App />
      </MemoryRouter>,
    );
    expect(await screen.findByText("Scan creation page")).toBeInTheDocument();
    for (const name of ["Backup", "Restore", "Scan"]) expect(screen.getByRole("tab", { name })).toBeInTheDocument();
    for (const name of ["Preview", "Analyze", "Sync"]) expect(screen.queryByRole("tab", { name })).not.toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Jobs" })).toHaveAttribute("aria-selected", "true");
  });

  it("uses a stable Settings destination with page tabs instead of a sidebar dropdown", async () => {
    render(
      <MemoryRouter initialEntries={["/file"]}>
        <App />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByRole("tab", { name: "Settings" }));
    expect(await screen.findByText("Locations page")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Settings" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("button", { name: "Settings" })).not.toBeInTheDocument();
    await userEvent.click(within(screen.getByRole("navigation", { name: "Settings views" })).getByRole("tab", { name: "Library" }));
    expect(await screen.findByText("Settings page")).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Settings views" })).getByRole("tab", { name: "Library" })).toHaveAttribute("aria-selected", "true");
    await userEvent.click(within(screen.getByRole("tablist", { name: "YATM modules" })).getByRole("tab", { name: "Library" }));
    expect(await screen.findByText("Library files page")).toBeInTheDocument();
  });
});
