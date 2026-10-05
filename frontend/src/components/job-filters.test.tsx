import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { JobKind } from "@/entity";
vi.mock("@/api", () => ({ cli: {}, locationCli: {} }));
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
});
