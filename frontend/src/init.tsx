import * as React from "react";
import { ChonkyIconProps, setChonkyDefaults } from "@samuelncui/chonky";
import { ChonkyIconFA } from "@samuelncui/chonky-icon-fontawesome";

import { styled } from "@mui/material/styles";

import DataUsageIcon from "@mui/icons-material/DataUsageRounded";
import DriveFileRenameOutlineIcon from "@mui/icons-material/DriveFileRenameOutline";
import FiberNewIcon from "@mui/icons-material/FiberNew";
import CleaningServicesIcon from "@mui/icons-material/CleaningServices";
import EditNoteOutlinedIcon from "@mui/icons-material/EditNoteOutlined";
import RefreshIcon from "@mui/icons-material/RefreshRounded";
import FilterAltOutlinedIcon from "@mui/icons-material/FilterAltOutlined";

const MUIStyled = (Icon: typeof DataUsageIcon) => styled(Icon)({ verticalAlign: "-0.2em", fontSize: "1.1rem" });

const MUIIconMap = {
  filter: MUIStyled(FilterAltOutlinedIcon),
  "mui-data-usage": MUIStyled(DataUsageIcon),
  "mui-rename": MUIStyled(DriveFileRenameOutlineIcon),
  "mui-fiber-new": MUIStyled(FiberNewIcon),
  "mui-cleaning": MUIStyled(CleaningServicesIcon),
  "mui-edit-metadata": MUIStyled(EditNoteOutlinedIcon),
  "mui-refresh": MUIStyled(RefreshIcon),
} as const;

setChonkyDefaults({
  iconComponent: React.memo((props) => {
    const { icon, ...otherProps } = props;

    const MUIIcon = MUIIconMap[icon as keyof typeof MUIIconMap];
    if (MUIIcon) {
      const { fixedWidth: _, ...props } = otherProps;
      // Refresh has wider built-in padding; normalize its optical size beside Data Usage.
      return <MUIIcon {...props} viewBox={icon === "mui-refresh" ? "2 2 20 20" : undefined} />;
    }

    return <ChonkyIconFA {...props} />;
  }) as React.FC<ChonkyIconProps>,
});
