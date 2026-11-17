import { i18n } from "@lingui/core";
import { I18nProvider as LinguiProvider } from "@lingui/react";
import { createContext, Fragment, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { activateLocale, currentLocale, saveLocale, type Locale } from "./locale";

type LocaleState = { locale: Locale; setLocale: (locale: Locale) => Promise<void> };

const LocaleContext = createContext<LocaleState>({ locale: "en", setLocale: async () => {} });

export const useLocale = () => useContext(LocaleContext);

/**
 * Owns the interface language. The initial locale must already be active (bootstrap awaits
 * activateLocale). A language change loads the new catalog first, then remounts the tree under a new
 * key, so every message call, including those in modules without a hook, re-evaluates.
 */
export const I18nProvider = ({ children }: { children: ReactNode }) => {
  const [locale, setActive] = useState<Locale>(currentLocale);
  const setLocale = useCallback(async (next: Locale) => {
    await activateLocale(next);
    saveLocale(next);
    setActive(next);
  }, []);
  const value = useMemo(() => ({ locale, setLocale }), [locale, setLocale]);
  return (
    <LocaleContext.Provider value={value}>
      <LinguiProvider i18n={i18n}>
        <Fragment key={locale}>{children}</Fragment>
      </LinguiProvider>
    </LocaleContext.Provider>
  );
};
