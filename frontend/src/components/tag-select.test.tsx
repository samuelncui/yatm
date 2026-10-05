import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { listTags } = vi.hoisted(() => ({ listTags: vi.fn() }));
vi.mock("@/api", () => ({ cli: { listTags } }));
import { TagSelect } from "./tag-select";

const call = (names: string[], nextCursor = "") => ({ response: Promise.resolve({ tags: names.map((name) => ({ name })), nextCursor }) });
const Picker = () => {
  const [value, setValue] = useState<string[]>([]);
  return (
    <>
      <TagSelect value={value} onChange={setValue} />
      <button>Next field</button>
      <output aria-label="Selected tags">{value.join(",") || "None"}</output>
    </>
  );
};

beforeEach(() => {
  listTags.mockReset();
  listTags.mockReturnValue(call(["archive"], "more"));
});

describe("Tag search recovery and focus", () => {
  it("shows an empty initial failure beside the free input and retries with the keyboard", async () => {
    listTags.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Tag service unavailable")) }));
    render(<Picker />);
    const input = screen.getByRole("combobox", { name: "Tags" });
    await userEvent.click(input);
    expect(await screen.findByRole("alert")).toHaveTextContent("Tag service unavailable");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Retry" })).toHaveFocus();
    await userEvent.tab({ shift: true });
    expect(input).toHaveFocus();
    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("option", { name: "archive" })).toBeInTheDocument();
    expect(input).toHaveFocus();
    expect(screen.getByRole("status", { name: "Selected tags" })).toHaveTextContent("None");
  });

  it("keeps pagination and retry in the field's tab order without selecting a tag", async () => {
    listTags.mockReturnValueOnce(call(["archive"], "more"));
    listTags.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Next page failed")) }));
    listTags.mockReturnValue(call(["photos"]));
    render(<Picker />);
    const input = screen.getByRole("combobox", { name: "Tags" });
    await userEvent.click(input);
    await screen.findByRole("option", { name: "archive" });
    await userEvent.keyboard("{ArrowDown}");
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Load more tags" })).toHaveFocus();
    await userEvent.tab({ shift: true });
    expect(input).toHaveFocus();
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("Next page failed");
    expect(input).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Retry loading tags" })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("option", { name: "photos" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "archive" })).toBeInTheDocument();
    expect(listTags).toHaveBeenLastCalledWith({ prefix: "", cursor: "more" });
    expect(screen.getByRole("status", { name: "Selected tags" })).toHaveTextContent("None");
  });

  it("lets Tab leave the popup and Escape return from pagination to the input", async () => {
    render(<Picker />);
    const input = screen.getByRole("combobox", { name: "Tags" });
    await userEvent.click(input);
    await screen.findByRole("option", { name: "archive" });
    await userEvent.tab();
    await userEvent.keyboard("{Escape}");
    expect(input).toHaveFocus();
    await waitFor(() => expect(screen.queryByRole("listbox")).not.toBeInTheDocument());
    await userEvent.keyboard("{ArrowDown}");
    await screen.findByRole("option", { name: "archive" });
    await userEvent.tab();
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Next field" })).toHaveFocus();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });
});
