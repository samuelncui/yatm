import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Checkbox, FormControlLabel } from "@mui/material";
import { ActionDialog, useActionDialog } from "./action-dialog";

describe("Application action dialog", () => {
  it("cancels without running the action and supports operation-specific options", async () => {
    const onConfirm = vi.fn();
    const onClose = vi.fn();
    render(
      <ActionDialog title="Check integrity" confirmLabel="Create check" onConfirm={onConfirm} onClose={onClose}>
        <FormControlLabel control={<Checkbox />} label="An optional setting" />
      </ActionDialog>,
    );
    expect(screen.getByRole("dialog", { name: "Check integrity" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox"));
    expect(onConfirm).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledOnce();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("validates input without trimming valid names and supports keyboard submission", async () => {
    const onConfirm = vi.fn();
    const onClose = vi.fn();
    render(<ActionDialog title="Rename" confirmLabel="Save" input={{ label: "Name", defaultValue: "old.txt" }} onConfirm={onConfirm} onClose={onClose} />);
    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input).toHaveValue("old.txt");
    fireEvent.change(input, { target: { value: "  " } });
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.change(input, { target: { value: " new.txt " } });
    await userEvent.type(input, "{Enter}");
    expect(onConfirm).toHaveBeenCalledWith(" new.txt ");
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("prevents duplicate submission and dismissal while pending, then retains errors for retry", async () => {
    let reject!: (reason: Error) => void;
    const onConfirm = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise((_, fail) => {
            reject = fail;
          }),
      )
      .mockResolvedValue(undefined);
    const onClose = vi.fn();
    render(<ActionDialog title="Check integrity" confirmLabel="Create check" onConfirm={onConfirm} onClose={onClose} />);
    const form = screen.getByRole("button", { name: "Create check" }).closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);
    expect(onConfirm).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => reject(new Error("Media is busy")));
    expect(screen.getByRole("alert")).toHaveTextContent("Media is busy");
    await userEvent.click(screen.getByRole("button", { name: "Create check" }));
    expect(onConfirm).toHaveBeenCalledTimes(2);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("keeps the first request until dismissed and resets input for the next request", async () => {
    const onConfirm = vi.fn();
    const Harness = () => {
      const { ask, dialog } = useActionDialog();
      return (
        <>
          <button onClick={() => ask({ title: "First", confirmLabel: "Save", input: { label: "Name", defaultValue: "first" }, onConfirm })}>First</button>
          <button onClick={() => ask({ title: "Second", confirmLabel: "Save", input: { label: "Name", defaultValue: "second" }, onConfirm })}>Second</button>
          {dialog}
        </>
      );
    };
    render(<Harness />);
    const first = screen.getByRole("button", { name: "First" });
    const second = screen.getByRole("button", { name: "Second" });
    fireEvent.click(first);
    fireEvent.click(second);
    expect(screen.getByRole("dialog")).toHaveAccessibleName("First");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(second);
    expect(within(screen.getByRole("dialog", { name: "Second" })).getByRole("textbox")).toHaveValue("second");
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
