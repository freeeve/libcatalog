// The palette, checked rather than asserted.
//
// a11y.test.ts skips axe's color-contrast rule -- jsdom has no rendering engine --
// and says instead that "the palette in app.css is chosen for WCAG AA contrast".
// It was not. `--danger` is two roles at once: ink for `.error` text, and the
// background of `.button--danger` and both FAILED badges. Dark mode brightened it
// so the ink would hold on a dark surface, which left white-on-salmon at 2.42:1 on
// Execute -- the button that CAS-writes a bulk edit -- and on the badge a librarian
// reads to learn a job failed.
//
// Contrast between two hex colours needs no browser, so the palette is verified
// here directly, in both themes, and the structural rule that let one token play
// both parts is verified with it: a rule may not paint a literal colour on a
// tokenised background. Then a theme that redefines the background redefines the
// ink with it.
//
// The sources are pulled in through Vite's raw glob rather than node:fs, so
// svelte-check needs no Node types and this file stays inside the same module
// graph as the app it audits.
import { describe, expect, it } from "vitest";

const AA_NORMAL = 4.5;

/** Every stylesheet and component style in the SPA, as raw text. */
const styleSources = Object.entries(
  import.meta.glob("./**/*.{svelte,css}", { query: "?raw", import: "default", eager: true }),
) as Array<[string, string]>;

const appCSS = styleSources.find(([p]) => p === "./app.css")?.[1] ?? "";

/** Relative luminance per WCAG 2.x, from an #rgb or #rrggbb string. */
function luminance(hex: string): number {
  let h = hex.replace("#", "");
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  const channel = (i: number) => {
    const c = parseInt(h.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * channel(0) + 0.7152 * channel(2) + 0.0722 * channel(4);
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** The hex-valued custom properties declared in the block opened by `selector`. */
function tokens(selector: string): Record<string, string> {
  const at = appCSS.indexOf(selector + " {");
  if (at < 0) throw new Error(`no ${selector} block in app.css`);
  const body = appCSS.slice(at, appCSS.indexOf("\n}", at));
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/(--[a-z-]+):\s*(#[0-9a-fA-F]{3,6})\s*;/g)) out[m[1]] = m[2];
  return out;
}

const themes = {
  light: tokens(":root"),
  dark: tokens('html[data-theme="dark"]'),
};

describe("palette", () => {
  it("declares both themes", () => {
    // Control: a typo in a selector would empty every check below.
    expect(Object.keys(themes.light).length).toBeGreaterThan(8);
    expect(Object.keys(themes.dark).length).toBeGreaterThan(8);
  });

  // A token used as a background needs a token for its ink, redefined per theme.
  // These are the pairs; adding a third surface colour means adding a pair here.
  const inkOn: Array<[string, string]> = [
    ["--accent", "--accent-ink"],
    ["--danger", "--danger-ink"],
  ];

  for (const [theme, t] of Object.entries(themes)) {
    for (const [bg, ink] of inkOn) {
      it(`${theme}: ${ink} on ${bg} holds AA`, () => {
        expect(t[bg], `${bg} undefined in ${theme}`).toBeTruthy();
        expect(t[ink], `${ink} undefined in ${theme} -- a background token with no ink token is how happened`).toBeTruthy();
        expect(contrast(t[ink], t[bg])).toBeGreaterThanOrEqual(AA_NORMAL);
      });
    }

    // --danger is also ink. That is why it cannot simply be darkened for dark mode,
    // and why the check above is not the whole story.
    for (const ink of ["--ink", "--ink-muted", "--danger", "--info"]) {
      for (const surface of ["--bg", "--surface"]) {
        it(`${theme}: ${ink} as text on ${surface} holds AA`, () => {
          expect(contrast(t[ink], t[surface])).toBeGreaterThanOrEqual(AA_NORMAL);
        });
      }
    }
  }
});

describe("no literal ink on a tokenised background", () => {
  it("finds style rules to check", () => {
    // Without this the walk below could match nothing and the check would pass by
    // looking at an empty list -- which is how the harness's own D5 shipped green.
    expect(styleSources.length).toBeGreaterThan(20);
    const painted = styleSources.flatMap(([, text]) => [...text.matchAll(/\{[^{}]*\}/g)]).filter((m) => /background:\s*var\(--/.test(m[0]));
    expect(painted.length).toBeGreaterThan(3);
  });

  it("holds across the SPA", () => {
    // The whole audit surface, as a property rather than a list of three files.
    // A rule that sets `background: var(--token)` must not fix its `color` to a
    // literal: the token moves between themes and the literal does not.
    const offenders: string[] = [];
    for (const [file, text] of styleSources) {
      for (const m of text.matchAll(/\{[^{}]*\}/g)) {
        const rule = m[0];
        if (!/background:\s*var\(--/.test(rule)) continue;
        const color = rule.match(/(?:^|[\s;{])color:\s*(#[0-9a-fA-F]{3,6}|white|black)\s*;/);
        if (color) offenders.push(`${file}: color: ${color[1]} on a var() background`);
      }
    }
    expect(offenders).toEqual([]);
  });
});
