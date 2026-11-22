// English UI text uses the source string as the key. Keeping this helper means
// call sites can retain interpolation without shipping locale dictionaries.
export const intlLocale = "en-US";

export type TVars = Record<string, string | number>;

export function t(key: string, vars?: TVars): string {
  if (!vars) return key;
  return key.replace(/\{(\w+)\}/g, (m, name: string) =>
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : m,
  );
}
