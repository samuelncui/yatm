import { setChonkyDefaults } from "chonky";
import { ChonkyIconFA } from "chonky-icon-fontawesome";

import { FontAwesomeIcon } from "@fortawesome/react-fontawesome";
import { faPencilAlt } from "@fortawesome/free-solid-svg-icons/faPencilAlt";

const ExternalIcons: Record<string, any> = {
  edit: faPencilAlt,
};

setChonkyDefaults({
  iconComponent: (props) => {
    const icon = ExternalIcons[props.icon] as any;
    if (!!icon) {
      const faProps = {
        ...props,
        icon: icon,
      } as const;
      return <FontAwesomeIcon {...faProps} />;
    }

    return <ChonkyIconFA {...props} />;
  },
});

import { unstable_ClassNameGenerator as ClassNameGenerator } from "@mui/material/className";

ClassNameGenerator.configure(
  // Do something with the componentName
  (componentName: string) => `app-${componentName}`
);
