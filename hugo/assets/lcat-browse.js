/*
 * Client-side ranked search + facet filtering over the RoaringRange WASM reader
 * (libcat tasks/158). Opt-in via [params.search] engine = "roaringrange".
 *
 * Progressive enhancement: the server-rendered work list (task 157) is the
 * default view. When the visitor types a query or selects a facet, this module
 * replaces the results with a client-side result set served from the artifacts
 * the build emits (search.BuildBrowse): a global trigram index
 * (browse-index.rrs), a facet sidecar (browse-facets.rrsf), and a record store
 * (browse-records.{idx,bin}) whose records are compact result-card JSON -- all
 * range-fetched, no backend. Clearing query and facets restores the static
 * list. If the reader or artifacts are unavailable, the static list stays and
 * nothing regresses.
 *
 * Three read paths over one shared doc space:
 *   query only          -> RrsCatalog.search(q, ..., [])
 *   query + facets      -> RrsCatalog.search(q, ..., filters)
 *   facets only         -> RrfFacets.filterIds(allIds, filters) + records.getMany
 *
 * Facet UI (tasks/170): sidebar rows the templates could not link (the
 * minimal profile has no term pages) ship data-lcat-field/-cat attributes;
 * once the reader boots they hydrate into checkbox toggles, making the
 * i18n'd, scheme-grouped sidebar the facet UI. In shared-sidebar mode the
 * fragment arrives async, so hydration also runs on the loader's
 * lcat:facets-loaded event -- and while that fragment is still in flight the
 * fallback panel holds off, so it never flashes over a sidebar about to take
 * over (tasks/173). Only when no hydratable rows exist (term pages present,
 * or no sidebar at all) does the fallback panel render from
 * RrfFacets.facets() into the #lcat-browse-facets host the list template
 * emits -- subjects grouped by vocabulary scheme with localized labels from
 * browse-subjects.json, like the static rail (tasks/173).
 *
 * Negative filters (tasks/144) in browse mode (tasks/173): when the site opts
 * in, every row ships a hidden .lcat-facet-not button; hydration unhides it
 * as an exclude toggle (aria-pressed), and selected() emits those rows as
 * {field, category, exclude: true} entries -- the reader subtracts their
 * posting sets. A row is include- or exclude-filtered, never both.
 */
import init, { RrsCatalog, RrfFacets, RrsRecords } from "/lcat/roaringrange.js";

const PAGE = 60;
const CATS_SHOWN = 40; // per-field category cap in the panel, by descending count

/** esc HTML-escapes untrusted record/facet text before insertion. */
function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c],
  );
}

/** card renders one result row from a decoded record (browseCard JSON). */
function card(dec, rec) {
  let c;
  try {
    c = JSON.parse(dec.decode(rec));
  } catch (e) {
    return "";
  }
  const href = "/works/" + encodeURIComponent(c.id) + "/";
  const contrib = (c.contributors || []).join(", ");
  return (
    '<li><a class="lcat-result" href="' +
    href +
    '">' +
    '<span class="lcat-result-title">' +
    esc(c.title || c.id) +
    "</span>" +
    (c.subtitle ? '<span class="lcat-result-subtitle">' + esc(c.subtitle) + "</span>" : "") +
    (contrib ? '<span class="lcat-result-contributors">' + esc(contrib) + "</span>" : "") +
    "</a></li>"
  );
}

function start() {
  const results = document.getElementById("lcat-results");
  const form = document.querySelector(".lcat-search");
  if (!results || !form) return;
  const input = form.querySelector('input[name="q"]');
  if (!input) return;

  const base = (results.getAttribute("data-lcat-browse") || "/search").replace(/\/+$/, "");
  const staticList = results.innerHTML; // restored when query + facets clear
  const countEl = document.querySelector(".lcat-resultcount");
  const staticCount = countEl ? countEl.textContent : "";
  const labels = {
    none: results.getAttribute("data-lcat-noresults") || "No matches",
    results: results.getAttribute("data-lcat-resultsword") || "results",
  };
  const panel = document.getElementById("lcat-browse-facets");
  const dec = new TextDecoder();

  let catalog = null;
  let facets = null;
  let records = null;
  let allIds = null;
  let booting = null;
  function boot() {
    if (catalog) return Promise.resolve(true);
    if (!booting) {
      booting = init()
        .then(() =>
          Promise.all([
            RrsCatalog.openAll(
              base + "/browse-index.rrs",
              base + "/browse-facets.rrsf",
              base + "/browse-records.idx",
              base + "/browse-records.bin",
            ),
            RrfFacets.open(base + "/browse-facets.rrsf"),
            RrsRecords.open(base + "/browse-records.idx", base + "/browse-records.bin"),
          ]),
        )
        .then(([c, f, r]) => {
          catalog = c;
          facets = f;
          records = r;
          allIds = new Uint32Array(r.len());
          for (let i = 0; i < allIds.length; i++) allIds[i] = i;
          if (!adoptSidebar() && !sharedPending()) renderPanel();
          return true;
        })
        .catch((e) => {
          console.warn("lcat-browse: reader unavailable, staying on static list", e);
          return false;
        });
    }
    return booting;
  }

  // Booting on first focus keeps page load free of the wasm fetch, and the
  // reader ready by the first keystroke; a facet panel needs it up front only
  // when the host is present.
  input.addEventListener("focus", boot, { once: true });
  if (panel) boot();

  /** selected returns the active filters: checked [field, category] pairs
   * from the panel and any hydrated sidebar rows, plus {field, category,
   * exclude} entries for pressed exclude toggles (tasks/173). The reader
   * accepts both entry shapes in one array. */
  function selected() {
    const boxes = Array.from(panel ? panel.querySelectorAll("input:checked") : []).concat(
      Array.from(document.querySelectorAll(".lcat-facets input[data-field]:checked")),
    );
    const filters = boxes.map((cb) => [cb.getAttribute("data-field"), cb.getAttribute("data-cat")]);
    document
      .querySelectorAll('.lcat-facets li[data-lcat-field] .lcat-facet-not[aria-pressed="true"]')
      .forEach((btn) => {
        const li = btn.closest("li");
        filters.push({
          field: li.getAttribute("data-lcat-field"),
          category: li.getAttribute("data-lcat-cat"),
          exclude: true,
        });
      });
    return filters;
  }

  /** negLabel formats one of the lcat-negatives-config strings (rendered only
   * when [params.facets] negatives is on) with the row's display label. */
  function negLabel(key, label) {
    const cfgEl = document.getElementById("lcat-negatives-config");
    if (!cfgEl) return label;
    try {
      return (JSON.parse(cfgEl.textContent)[key] || "%s").replace("%s", label);
    } catch (e) {
      return label;
    }
  }

  /** setNot flips one hydrated row's exclude toggle: aria-pressed drives both
   * the CSS state and selected()'s collection, and the accessible name tracks
   * the action the next press performs. */
  function setNot(li, btn, pressed) {
    btn.setAttribute("aria-pressed", pressed ? "true" : "false");
    const value = li.querySelector(".lcat-facet-value");
    const name = negLabel(pressed ? "remove" : "exclude", value ? value.textContent.trim() : "");
    btn.setAttribute("aria-label", name);
    btn.title = name;
  }

  /** adoptSidebar hydrates unlinked sidebar facet rows (data-lcat-field/-cat,
   * emitted where no term page exists) into checkbox toggles driving the
   * reader, and reports whether the sidebar took over as the facet UI --
   * in which case the duplicate panel is skipped or torn down. When the site
   * opted into negatives, each row's shipped-hidden exclude button becomes an
   * exclude toggle; include and exclude on one row are mutually exclusive
   * (tasks/173). Idempotent: already-hydrated rows are left alone. */
  function adoptSidebar() {
    const rows = document.querySelectorAll(".lcat-facets li[data-lcat-field]");
    if (!rows.length) return false;
    rows.forEach((li) => {
      if (li.querySelector("input[data-field]")) return;
      const label = document.createElement("label");
      const cb = document.createElement("input");
      cb.type = "checkbox";
      cb.setAttribute("data-field", li.getAttribute("data-lcat-field"));
      cb.setAttribute("data-cat", li.getAttribute("data-lcat-cat"));
      label.appendChild(cb);
      while (li.firstChild) {
        // The hidden negatives button stays a direct row child; everything
        // else (value + count spans) moves into the toggle label.
        if (li.firstChild.classList && li.firstChild.classList.contains("lcat-facet-not")) break;
        label.appendChild(li.firstChild);
      }
      li.insertBefore(label, li.firstChild);
      const not = li.querySelector(".lcat-facet-not");
      if (not) {
        not.hidden = false;
        setNot(li, not, false);
        not.addEventListener("click", () => {
          const pressed = not.getAttribute("aria-pressed") !== "true";
          if (pressed) cb.checked = false;
          setNot(li, not, pressed);
          refresh();
        });
      }
      li.addEventListener("change", () => {
        if (cb.checked && not) setNot(li, not, false);
        refresh();
      });
    });
    if (panel) {
      panel.innerHTML = "";
      panel.hidden = true;
    }
    return true;
  }

  /** sharedPending reports a shared-sidebar fragment still in flight: the
   * loader host is on the page but no facet nav has been inserted yet. While
   * pending, the fallback panel holds off -- rendering it would flash a flat,
   * unlabeled panel that the arriving fragment immediately tears down
   * (tasks/173). If the fetch fails the loader keeps its static fallback
   * links, which remain the (JS-free) facet UI. */
  function sharedPending() {
    return !!document.querySelector("[data-lcat-facets-src]") && !document.querySelector(".lcat-facets");
  }

  // Shared-sidebar mode inserts the fragment after boot may have finished;
  // hydrate on the loader's signal, or render the panel if the fragment
  // arrived without hydratable rows. Before boot this is a no-op: boot's own
  // adoptSidebar/renderPanel pass sees whatever the fragment inserted.
  document.addEventListener("lcat:facets-loaded", () => {
    if (!facets) return;
    if (adoptSidebar()) refresh();
    else renderPanel();
  });

  /** browseConfig reads the list template's config blob: the localized
   * "Subjects" heading and the [params.subjectSchemes] order/display names. */
  function browseConfig() {
    const el = document.getElementById("lcat-browse-config");
    if (!el) return { subjects: "subject", subjectSchemes: [] };
    try {
      const cfg = JSON.parse(el.textContent) || {};
      return { subjects: cfg.subjects || "subject", subjectSchemes: cfg.subjectSchemes || [] };
    } catch (e) {
      return { subjects: "subject", subjectSchemes: [] };
    }
  }

  /** subjectMeta lazily fetches browse-subjects.json (subject id -> labels +
   * vocabulary scheme; tasks/173); an absent or failed sidecar degrades to
   * ungrouped raw ids, exactly the pre-173 panel. */
  let subjectMetaP = null;
  function subjectMeta() {
    if (!subjectMetaP) {
      subjectMetaP = fetch(base + "/browse-subjects.json")
        .then((r) => (r.ok ? r.json() : {}))
        .catch(() => ({}));
    }
    return subjectMetaP;
  }

  /** panelRows renders one category list, largest counts first. */
  function panelRows(field, cats, display) {
    return cats
      .slice()
      .sort((a, b) => b.count - a.count)
      .slice(0, CATS_SHOWN)
      .map(
        (c) =>
          '<li><label><input type="checkbox" data-field="' +
          esc(field) +
          '" data-cat="' +
          esc(c.name) +
          '"> <span class="lcat-facet-value">' +
          esc(display(c.name)) +
          '</span> <span class="lcat-count">' +
          c.count +
          "</span></label></li>",
      )
      .join("");
  }

  function panelGroup(summary, rowsHTML) {
    return '<details class="lcat-browse-facet"><summary>' + esc(summary) + "</summary><ul>" + rowsHTML + "</ul></details>";
  }

  /** renderPanel builds the fallback facet panel from the sidecar. Subjects
   * render one group per vocabulary scheme in [params.subjectSchemes] order
   * with localized labels, like the static rail (tasks/173); other fields
   * stay one group each. */
  function renderPanel() {
    if (!panel || !facets) return;
    const fields = facets.facets() || [];
    if (!fields.length) return;
    subjectMeta().then((meta) => {
      const cfg = browseConfig();
      const lang = document.documentElement.lang || "en";
      const subjectLabel = (id) => {
        const m = meta[id];
        return (m && m.labels && (m.labels[lang] || m.labels.en || m.labels[""])) || id;
      };
      const html = fields.map((f) => {
        if (f.field !== "subject") {
          return panelGroup(f.field, panelRows(f.field, f.cats || [], (n) => n));
        }
        // Partition subject categories by scheme: configured schemes first in
        // config order, unlisted ones after in first-seen order. A single
        // (or unknown-scheme) vocabulary keeps the one localized group.
        const byScheme = new Map();
        (f.cats || []).forEach((c) => {
          const scheme = (meta[c.name] && meta[c.name].scheme) || "";
          if (!byScheme.has(scheme)) byScheme.set(scheme, []);
          byScheme.get(scheme).push(c);
        });
        const order = [];
        cfg.subjectSchemes.forEach((s) => {
          const scheme = s.scheme || "";
          if (byScheme.has(scheme)) order.push({ scheme, name: s.name || scheme });
        });
        byScheme.forEach((_, scheme) => {
          if (!order.some((o) => o.scheme === scheme)) order.push({ scheme, name: scheme });
        });
        return order
          .map((o) =>
            panelGroup(
              order.length > 1 ? o.name || cfg.subjects : cfg.subjects,
              panelRows(f.field, byScheme.get(o.scheme), subjectLabel),
            ),
          )
          .join("");
      });
      panel.innerHTML = html.join("");
      panel.hidden = false;
      if (!panel.dataset.lcatWired) {
        panel.dataset.lcatWired = "1";
        panel.addEventListener("change", refresh);
      }
    });
  }

  function restore() {
    results.innerHTML = staticList;
    if (countEl) countEl.textContent = staticCount;
  }

  function renderCards(recs, total) {
    const html = [];
    for (const r of recs) {
      if (r) html.push(card(dec, r));
    }
    results.innerHTML = html.length ? html.join("") : '<li class="lcat-noresults">' + esc(labels.none) + "</li>";
    if (countEl) {
      countEl.textContent = total + (total >= PAGE ? "+ " : " ") + labels.results;
    }
  }

  let seq = 0;
  function refresh() {
    const q = input.value.trim();
    const filters = selected();
    if (q === "" && filters.length === 0) {
      seq++;
      restore();
      return;
    }
    const mine = ++seq;
    boot()
      .then((ok) => {
        if (!ok || mine !== seq) return; // reader down, or a newer interaction won
        if (q !== "") {
          return catalog.search(q, 0, PAGE, 0, filters).then((res) => {
            if (mine === seq) renderCards(res.records || [], (res.ids || []).length);
          });
        }
        // Facet-only browse: filter the whole doc space, page the survivors.
        return facets.filterIds(allIds, filters, false).then((fi) => {
          const ids = fi.ids;
          const page = ids.slice(0, PAGE);
          return records.getMany(page).then((recs) => {
            // getMany resolves to an Array aligned with the input ids.
            if (mine === seq) renderCards(recs, ids.length);
          });
        });
      })
      .catch((e) => console.warn("lcat-browse: query failed", e));
  }

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    refresh();
  });
  input.addEventListener("input", refresh);

  // Honor an initial ?q= (a deep link, or the no-JS form landing here).
  const initial = new URLSearchParams(window.location.search).get("q");
  if (initial) {
    input.value = initial;
    refresh();
  }
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", start);
} else {
  start();
}
