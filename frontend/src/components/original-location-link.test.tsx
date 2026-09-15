import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router";
import { expect, it } from "vitest";
import { OriginalLocationLink } from "./original-location-link";

const Route = () => <output>{useLocation().pathname + useLocation().search}</output>;

it("links the Location chip to its root and the original path to its containing folder", async () => {
  render(
    <MemoryRouter>
      <OriginalLocationLink location={{ id: 4n, name: "Documents", rootPath: "/documents" }} path="Research & notes/handbook.md" />
      <Route />
    </MemoryRouter>,
  );
  const location = screen.getByRole("link", { name: "Documents" });
  expect(location).toHaveAttribute("href", "/file?location=4");
  expect(location).toHaveAttribute("title", "/documents");
  await userEvent.click(screen.getByRole("link", { name: "Research & notes/handbook.md" }));
  expect(screen.getByRole("status")).toHaveTextContent("/file?location=4&path=Research+%26+notes&reveal=Research+%26+notes%2Fhandbook.md");
  await userEvent.click(location);
  expect(screen.getByRole("status")).toHaveTextContent("/file?location=4");
});
