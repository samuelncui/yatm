import * as React from "react";
import { ChonkyIconProps, setChonkyDefaults } from "@samuelncui/chonky";
import { ChonkyIconFA } from "@samuelncui/chonky-icon-fontawesome";

import { unstable_ClassNameGenerator as ClassNameGenerator } from "@mui/material/className";
import { styled } from "@mui/material/styles";

import DataUsageIcon from "@mui/icons-material/DataUsage";
import DriveFileRenameOutlineIcon from "@mui/icons-material/DriveFileRenameOutline";
import FiberNewIcon from "@mui/icons-material/FiberNew";
import CleaningServicesIcon from "@mui/icons-material/CleaningServices";

const MUIStyled = (Icon: typeof DataUsageIcon) => styled(Icon)({ verticalAlign: "-0.2em", fontSize: "1.1rem" });

const MUIIconMap = {
  "mui-data-usage": MUIStyled(DataUsageIcon),
  "mui-rename": MUIStyled(DriveFileRenameOutlineIcon),
  "mui-fiber-new": MUIStyled(FiberNewIcon),
  "mui-cleaning": MUIStyled(CleaningServicesIcon),
} as const;

setChonkyDefaults({
  iconComponent: React.memo((props) => {
    const { icon, ...otherProps } = props;

    const MUIIcon = MUIIconMap[icon as keyof typeof MUIIconMap];
    if (!!MUIIcon) {
      const { fixedWidth: _, ...props } = otherProps;
      return <MUIIcon {...props} />;
    }

    return <ChonkyIconFA {...props} />;
  }) as React.FC<ChonkyIconProps>,
});

ClassNameGenerator.configure(
  // Do something with the componentName
  (componentName: string) => `app-${componentName}`,
);
