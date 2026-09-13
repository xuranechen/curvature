import React from "react";
import { getStoredString, setStoredString } from "../services/storage";
import { enUS } from "./locales/en-US";
import { zhCN } from "./locales/zh-CN";
import type { I18nContextValue, Locale, MessageKey, MessageParams, Messages } from "./types";

export type { I18nContextValue, Locale, MessageKey, MessageParams } from "./types";

export const LOCALE_STORAGE_KEY = "curvature-locale";
export const LOCALE_CHANGE_EVENT = "curvature:locale-changed";

const LOCALES: Locale[] = ["zh-CN", "en-US"];
const localeSet = new Set<Locale>(LOCALES);
const dictionaries: Record<Locale, Messages> = {
  "zh-CN": zhCN,
  "en-US": enUS,
};

function normalizeLocale(value: unknown): Locale | null {
  if (typeof value !== "string") {
    return null;
  }
  const normalized = value.trim().replace("_", "-");
  if (localeSet.has(normalized as Locale)) {
    return normalized as Locale;
  }
  const lower = normalized.toLowerCase();
  if (lower === "zh" || lower.startsWith("zh-")) {
    return "zh-CN";
  }
  if (lower === "en" || lower.startsWith("en-")) {
    return "en-US";
  }
  return null;
}

export function getStoredLocale(): Locale | null {
  return normalizeLocale(getStoredString(LOCALE_STORAGE_KEY));
}

export function detectLocale(): Locale {
  const stored = getStoredLocale();
  if (stored) {
    return stored;
  }
  if (typeof navigator !== "undefined") {
    const candidates = [navigator.language, ...(navigator.languages || [])];
    for (const candidate of candidates) {
      const locale = normalizeLocale(candidate);
      if (locale) {
        return locale;
      }
    }
  }
  return "zh-CN";
}

export function persistLocale(locale: Locale): void {
  setStoredString(LOCALE_STORAGE_KEY, locale);
}

export function applyDocumentLocale(locale: Locale): void {
  if (typeof document === "undefined") {
    return;
  }
  document.documentElement.lang = locale;
}

export function translateWithLocale(locale: Locale, key: MessageKey, params?: MessageParams): string {
  const dictionary = dictionaries[locale] || dictionaries["zh-CN"];
  let template = dictionary[key] || dictionaries["zh-CN"][key] || key;
  if (!dictionary[key] && locale !== "zh-CN" && typeof console !== "undefined") {
    console.warn(`[i18n] missing message: ${locale}:${key}`);
  }
  if (!params) {
    return template;
  }
  return template.replace(/\{(\w+)\}/g, (match, name) => {
    const value = params[name];
    return value === undefined || value === null ? match : String(value);
  });
}

export function translateNow(key: MessageKey, params?: MessageParams): string {
  return translateWithLocale(detectLocale(), key, params);
}

const I18nContext = React.createContext<I18nContextValue | null>(null);

function coerceDate(value: Date | number | string): Date {
  return value instanceof Date ? value : new Date(value);
}

export function I18nProvider({ children }: { children: React.ReactNode }): React.ReactElement {
  const [locale, setLocaleState] = React.useState<Locale>(() => detectLocale());

  React.useEffect(() => {
    applyDocumentLocale(locale);
  }, [locale]);

  React.useEffect(() => {
    if (typeof window === "undefined") {
      return;
    }
    const syncLocale = () => {
      setLocaleState(detectLocale());
    };
    window.addEventListener("storage", syncLocale);
    window.addEventListener(LOCALE_CHANGE_EVENT, syncLocale);
    return () => {
      window.removeEventListener("storage", syncLocale);
      window.removeEventListener(LOCALE_CHANGE_EVENT, syncLocale);
    };
  }, []);

  const setLocale = React.useCallback((nextLocale: Locale) => {
    persistLocale(nextLocale);
    applyDocumentLocale(nextLocale);
    setLocaleState(nextLocale);
    if (typeof window !== "undefined") {
      window.dispatchEvent(new CustomEvent<Locale>(LOCALE_CHANGE_EVENT, { detail: nextLocale }));
    }
  }, []);

  const value = React.useMemo<I18nContextValue>(() => ({
    locale,
    setLocale,
    t: (key, params) => translateWithLocale(locale, key, params),
    formatDate: (value, options) => new Intl.DateTimeFormat(locale, options).format(coerceDate(value)),
    formatTime: (value, options) => new Intl.DateTimeFormat(locale, {
      hour: "2-digit",
      minute: "2-digit",
      ...options,
    }).format(coerceDate(value)),
    formatDateTime: (value, options) => new Intl.DateTimeFormat(locale, {
      dateStyle: "medium",
      timeStyle: "short",
      ...options,
    }).format(coerceDate(value)),
    formatNumber: (value, options) => new Intl.NumberFormat(locale, options).format(value),
  }), [locale, setLocale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  const value = React.useContext(I18nContext);
  if (!value) {
    throw new Error("useI18n must be used within I18nProvider");
  }
  return value;
}
