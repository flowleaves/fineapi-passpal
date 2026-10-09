import { ref } from "vue";

export type ThemeName = "light" | "dark";
export type Density = "compact" | "comfortable";

const THEME_KEY = "passpal.theme";
const DENSITY_KEY = "passpal.density";

function safeGet(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function safeSet(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // 存储不可用不应影响功能
  }
}

function initialTheme(): ThemeName {
  const attr = document.documentElement.getAttribute("data-theme");
  if (attr === "dark" || attr === "light") return attr;
  return "light";
}

const theme = ref<ThemeName>(initialTheme());
const density = ref<Density>(safeGet(DENSITY_KEY) === "comfortable" ? "comfortable" : "compact");

function apply(): void {
  document.documentElement.setAttribute("data-theme", theme.value);
}

apply();

export function useTheme() {
  function setTheme(next: ThemeName): void {
    theme.value = next;
    safeSet(THEME_KEY, next);
    apply();
  }

  function toggleTheme(): void {
    setTheme(theme.value === "dark" ? "light" : "dark");
  }

  function setDensity(next: Density): void {
    density.value = next;
    safeSet(DENSITY_KEY, next);
  }

  return { theme, density, setTheme, toggleTheme, setDensity };
}
