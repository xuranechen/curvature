import { zhCN } from "./locales/zh-CN";

export type Locale = "zh-CN" | "en-US";
export type MessageKey = keyof typeof zhCN;
export type Messages = Record<MessageKey, string>;
export type MessageParams = Record<string, string | number | boolean | null | undefined>;

export type I18nContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: MessageKey, params?: MessageParams) => string;
  formatDate: (value: Date | number | string, options?: Intl.DateTimeFormatOptions) => string;
  formatTime: (value: Date | number | string, options?: Intl.DateTimeFormatOptions) => string;
  formatDateTime: (value: Date | number | string, options?: Intl.DateTimeFormatOptions) => string;
  formatNumber: (value: number, options?: Intl.NumberFormatOptions) => string;
};

