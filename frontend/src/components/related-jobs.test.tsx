import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { Job, JobKind, JobStatus } from "@/entity";
const { list } = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@/api", async (original) => ({ ...(await original<typeof import("@/api")>()), jobCli: { list } }));
import { RelatedJobs } from "./related-jobs";
describe("Related Jobs table", () => {
  it("shows a bounded recent table and a resource-filtered full history link", async () => {
    list.mockReturnValue({
      response: Promise.resolve({ jobs: [Job.create({ id: 9n, kind: JobKind.ARCHIVE, status: JobStatus.COMPLETED, createdAtMs: 100n })] }),
    });
    render(
      <MemoryRouter>
        <RelatedJobs locationID={3n} />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("link", { name: "#9 · Backup" })).toHaveAttribute("href", "/jobs/9");
    expect(screen.getByRole("table", { name: "Related Jobs" })).toHaveTextContent("Completed");
    expect(screen.getByRole("link", { name: "All related Jobs" })).toHaveAttribute("href", "/jobs?location=3");
    expect(list).toHaveBeenCalledWith({ filter: { locationId: 3n, mediaId: undefined, limit: 10n } });
  });
  it("clears the old resource rows while loading another Location", async () => {
    list.mockReturnValueOnce({ response: Promise.resolve({ jobs: [Job.create({ id: 9n, kind: JobKind.ARCHIVE })] }) });
    const view = render(
      <MemoryRouter>
        <RelatedJobs locationID={3n} />
      </MemoryRouter>,
    );
    await screen.findByRole("link", { name: "#9 · Backup" });
    list.mockReturnValueOnce({ response: new Promise(() => {}) });
    view.rerender(
      <MemoryRouter>
        <RelatedJobs locationID={4n} />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("link", { name: "#9 · Backup" })).not.toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Related Jobs" })).toHaveTextContent("Loading…");
    expect(screen.getByRole("link", { name: "All related Jobs" })).toHaveAttribute("href", "/jobs?location=4");
  });
});
