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
 * The facet panel renders from RrfFacets.facets() (meta-only boot: names +
 * full-corpus counts) into the #lcat-browse-facets host the list template
 * emits. Display polish (labels for subject ids, i18n of field names) rides the
 * task-157 sidebar rework.
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
          renderPanel();
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

  /** selected returns the checked [field, category] pairs from the panel. */
  function selected() {
    if (!panel) return [];
    return Array.from(panel.querySelectorAll("input:checked")).map((cb) => [
      cb.getAttribute("data-field"),
      cb.getAttribute("data-cat"),
    ]);
  }

  function renderPanel() {
    if (!panel || !facets) return;
    const fields = facets.facets() || [];
    if (!fields.length) return;
    const html = fields.map((f) => {
      const cats = (f.cats || []).slice().sort((a, b) => b.count - a.count).slice(0, CATS_SHOWN);
      const rows = cats
        .map(
          (c) =>
            '<li><label><input type="checkbox" data-field="' +
            esc(f.field) +
            '" data-cat="' +
            esc(c.name) +
            '"> ' +
            esc(c.name) +
            ' <span class="lcat-count">' +
            c.count +
            "</span></label></li>",
        )
        .join("");
      return (
        '<details class="lcat-browse-facet"><summary>' +
        esc(f.field) +
        "</summary><ul>" +
        rows +
        "</ul></details>"
      );
    });
    panel.innerHTML = html.join("");
    panel.hidden = false;
    panel.addEventListener("change", refresh);
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
