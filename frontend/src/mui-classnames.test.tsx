import { MenuItem, TextField, inputLabelClasses, selectClasses } from "@mui/material";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

describe("Shared MUI class initialization", () => {
  it("keeps cached selectors and rendered utility classes in the same namespace", () => {
    expect(selectClasses.select).toBe("app-MuiSelect-select");
    expect(inputLabelClasses.shrink).toBe("app-MuiInputLabel-shrink");
    render(
      <TextField select label="Content to inspect" value="19" size="small" sx={{ width: 180 }}>
        <MenuItem value="19">Version 19 · first archived with a long localized timestamp</MenuItem>
      </TextField>,
    );
    const select = screen.getByRole("combobox", { name: "Content to inspect" });
    expect(select).toHaveClass(selectClasses.select);
    const style = getComputedStyle(select);
    expect(style.whiteSpace).toBe("nowrap");
    expect(style.overflow).toBe("hidden");
    expect(style.textOverflow).toBe("ellipsis");
  });
});
