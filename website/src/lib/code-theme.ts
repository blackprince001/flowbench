import type { ThemeRegistration } from "shiki";

// Shiki dressed in the report's palette so a snippet on the page and a value in
// a run read as the same system. Background is transparent — the panel around
// it owns the surface.
export const codeTheme: ThemeRegistration = {
  name: "flowbench",
  type: "dark",
  colors: {
    "editor.background": "#00000000",
    "editor.foreground": "#f5f3f0",
  },
  settings: [
    { scope: ["comment", "punctuation.definition.comment"], settings: { foreground: "#7c746d" } },
    { scope: ["string", "constant.character"], settings: { foreground: "#a9a19a" } },
    {
      scope: ["entity.name.tag", "support.type.property-name", "variable.other.key"],
      settings: { foreground: "#3987e5" },
    },
    { scope: ["constant.numeric", "constant.language"], settings: { foreground: "#d95926" } },
    { scope: ["keyword", "storage.type", "keyword.control"], settings: { foreground: "#d95926" } },
    { scope: ["entity.name.function", "support.function"], settings: { foreground: "#199e70" } },
    { scope: ["punctuation", "meta.brace"], settings: { foreground: "#7c746d" } },
    { scope: ["variable", "meta.function-call.arguments"], settings: { foreground: "#f5f3f0" } },
  ],
};
