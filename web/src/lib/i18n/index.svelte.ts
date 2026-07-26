/**
 * Lightweight i18n module for lalmax-nvr
 * Svelte 5 reactive — state.currentLang is $state, t() reads it directly
 */

import zh from './zh.json';
import en from './en.json';

type Translations = Record<string, string>;

const locales: Record<string, Translations> = { zh, en };

// $state object — components import and read i18nState.currentLang for reactive tracking
// t() also reads i18nState.currentLang, so any template calling t() re-evaluates on lang change
// Named i18nState (not "state") to avoid conflicting with Svelte 5's $state rune.
export const i18nState = $state({ currentLang: 'en' });

function detectLanguage(): string {
  const saved = localStorage.getItem('nvr_lang');
  if (saved && locales[saved]) return saved;

  const nav = navigator.language || '';
  if (/^zh\b/i.test(nav)) return 'zh';

  return 'en';
}

export function initI18n(): void {
  i18nState.currentLang = detectLanguage();
}

export function setLang(lang: string): void {
  if (!locales[lang]) return;
  i18nState.currentLang = lang;
  localStorage.setItem('nvr_lang', lang);
}

export function t(key: string, params?: Record<string, string | number>): string {
  // Read i18nState.currentLang ($state) and USE the value — compiler cannot optimize away
  const lang = i18nState.currentLang;
  const dict = locales[lang] || locales['en'];
  let value = dict[key];

  if (value === undefined) {
    // Fallback to English
    value = locales['en'][key];
  }

  if (value === undefined) {
    return key;
  }

  if (params) {
    for (const [k, v] of Object.entries(params)) {
      value = value.replace(`{${k}}`, String(v));
    }
  }

  return value;
}
