import { i18n, type Messages } from "@lingui/core";
import moment from "moment";
import { readStored, stringCodec, writeStored } from "@/state/storage";

export const locales = ["en", "zh-CN"] as const;
export type Locale = (typeof locales)[number];

/** Language names are shown in their own language, so they are never translated. */
export const localeNames: Record<Locale, string> = { en: "English", "zh-CN": "简体中文" };

export const localeStorageKey = "yatm.locale";

const isLocale = (value: unknown): value is Locale => locales.includes(value as Locale);

/** The saved choice wins; otherwise any Chinese browser language selects Simplified Chinese. */
export const detectLocale = (): Locale => {
  const stored = readStored("local", localeStorageKey, stringCodec);
  if (isLocale(stored)) return stored;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
};

export const saveLocale = (locale: Locale) => writeStored("local", localeStorageKey, locale, stringCodec);

// Every locale has a catalog chunk: production builds keep only message ids in code, so English text
// also comes from its catalog. Tests and development builds keep the source text in code.
const catalogs: Record<Locale, () => Promise<{ messages: Messages }>> = {
  en: () => import("@/locales/en/messages.po"),
  "zh-CN": () => import("@/locales/zh-CN/messages.po"),
};

/** Loads the locale's catalog and makes it the one every message call resolves against. */
export const activateLocale = async (locale: Locale) => {
  // Development builds fall back to uncompiled source text, which needs a runtime compiler.
  if (import.meta.env.DEV) i18n.setMessagesCompiler((await import("@lingui/message-utils/compileMessage")).compileMessage);
  const { messages } = await catalogs[locale]();
  if (locale === "zh-CN") await import("moment/locale/zh-cn");
  // Importing a moment locale also selects it globally; date components pass their locale explicitly.
  moment.locale("en");
  i18n.loadAndActivate({ locale, messages });
  document.documentElement.lang = locale;
};

/** The active locale for code outside React; components read it with useLocale. */
export const currentLocale = (): Locale => (isLocale(i18n.locale) ? i18n.locale : "en");

/** The locale for Date and Intl formatting: English keeps the browser's regional format. */
export const formattingLocale = (): string | undefined => (currentLocale() === "en" ? undefined : currentLocale());
