export type ThemeMode = "system" | "light" | "dark";

export type Theme = {
  dark: boolean;
  colors: {
    app: string;
    rail: string;
    surface: string;
    surfaceAlt: string;
    text: string;
    textMuted: string;
    accent: string;
    accentForeground: string;
    border: string;
    success: string;
    warning: string;
    danger: string;
    overlay: string;
  };
  radius: { small: number; medium: number; large: number; pill: number };
  spacing: { xsmall: number; small: number; medium: number; large: number; xlarge: number };
  shadow: {
    shadowColor: string;
    shadowOffset: { width: number; height: number };
    shadowOpacity: number;
    shadowRadius: number;
    elevation: number;
  };
};

export const lightTheme: Theme = {
  dark: false,
  colors: {
    app: "#f6f8fa",
    rail: "#ffffff",
    surface: "#ffffff",
    surfaceAlt: "#f6f8fa",
    text: "#24292f",
    textMuted: "#57606a",
    accent: "#0969da",
    accentForeground: "#ffffff",
    border: "#d0d7de",
    success: "#1a7f37",
    warning: "#9a6700",
    danger: "#cf222e",
    overlay: "rgba(27,31,36,0.45)",
  },
  radius: { small: 6, medium: 8, large: 12, pill: 999 },
  spacing: { xsmall: 4, small: 8, medium: 12, large: 16, xlarge: 24 },
  shadow: {
    shadowColor: "#1f2328",
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.08,
    shadowRadius: 12,
    elevation: 3,
  },
};

export const darkTheme: Theme = {
  dark: true,
  colors: {
    app: "#09090b",
    rail: "#09090b",
    surface: "#18181b",
    surfaceAlt: "#27272a",
    text: "#ffffff",
    textMuted: "#a1a1aa",
    accent: "#f4f4f5",
    accentForeground: "#09090b",
    border: "#3f3f46",
    success: "#3fb950",
    warning: "#d29922",
    danger: "#f85149",
    overlay: "rgba(0,0,0,0.64)",
  },
  radius: lightTheme.radius,
  spacing: lightTheme.spacing,
  shadow: {
    shadowColor: "#000000",
    shadowOffset: { width: 0, height: 6 },
    shadowOpacity: 0.28,
    shadowRadius: 16,
    elevation: 5,
  },
};
