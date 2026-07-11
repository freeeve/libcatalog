// Named keymap presets: bundles of action-id -> chord remaps
// applied through the keyboard registry in one action. Koha's advanced-editor
// chords ship as an opt-in preset, minus the reserved ones (Ctrl+C/X/V shadow
// the system clipboard; Ctrl+D/P/S/H are browser chords) -- those ops keep
// our defaults, which the preset UI calls out.
import { applyKeymap, resetKeymap } from "./keyboard";

export interface KeymapPreset {
  name: string;
  label: string;
  description: string;
  entries: Record<string, string>;
}

export const PRESETS: KeymapPreset[] = [
  {
    name: "default",
    label: "Default",
    description: "The shipped bindings; applying clears every remap.",
    entries: {},
  },
  {
    name: "koha-advanced-editor",
    label: "Koha advanced editor",
    description:
      "Koha's shortcut table where safe: field copy/cut/paste on Shift+mod+C/X/V, © on Alt+A, ℗ on Alt+P. " +
      "Koha's Ctrl+C/X/V, Ctrl+D, Ctrl+P, Ctrl+S and Ctrl+H are reserved (clipboard/browser) and keep our defaults.",
    entries: {
      "editor:alt+c": "mod+shift+c",
      "editor:alt+x": "mod+shift+x",
      "editor:alt+v": "mod+shift+v",
      "editor:alt+g": "alt+a",
      "editor:alt+r": "alt+p",
    },
  },
];

/** Applies a preset by name; "default" resets everything. Returns the action
 *  ids a conflict or reservation kept on their previous key. */
export function applyPreset(name: string): string[] {
  const preset = PRESETS.find((p) => p.name === name);
  if (!preset) return [];
  if (preset.name === "default") {
    resetKeymap();
    return [];
  }
  return applyKeymap(preset.entries);
}
