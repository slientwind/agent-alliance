export type Theme = "light" | "dark";

export const themeKey = "alliance.theme";

export function readTheme(storage?: Pick<Storage, "getItem">): Theme {
  try {
    const saved = (storage ?? window.localStorage).getItem(themeKey);
    return saved === "dark" ? "dark" : "light";
  } catch {
    return "light";
  }
}

export function applyTheme(theme: Theme, root: Pick<HTMLElement, "dataset">, storage?: Pick<Storage, "setItem">) {
  root.dataset.theme = theme;
  try {
    (storage ?? window.localStorage).setItem(themeKey, theme);
  } catch {
    // A browser can block storage while still permitting the current theme change.
  }
}
