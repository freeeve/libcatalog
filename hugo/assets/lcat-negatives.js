/*
 * Opt-in negative facet filters (libcat tasks/144): filter works OUT by a
 * facet term. URL state is x<taxonomy>=<term>, repeatable, so exclusions are
 * shareable and bookmarkable. Exclusions are
 * buttons, not links -- "hide X" URLs stay out of crawlers -- and everything
 * is client-side over the already-rendered page: cards carrying an excluded
 * term hide, active exclusions render as dismissible "Not X" chips above the
 * results, and sidebar/pagination links are rewritten to carry the exclusions
 * along while browsing. Like lcat-search.js this filters the CURRENT page's
 * cards only; a fully server-shaped result set is the roaringrange reader's
 * job (tasks/010). No results list on the page (taxonomy landings) still
 * toggles the URL state, which the rewritten links then carry.
 */
(function () {
  "use strict";
  var results = document.getElementById("lcat-results");
  var strings = { exclude: "Exclude %s", excluded: "Not %s", remove: "Remove exclusion of %s" };
  var cfgEl = document.getElementById("lcat-negatives-config");
  if (cfgEl) {
    try { strings = JSON.parse(cfgEl.textContent); } catch (e) { /* defaults */ }
  }

  // The buttons ship hidden and nearly attribute-free (tasks/148: at catalog
  // scale, repeated attributes cost gigabytes of built HTML). Everything
  // hydrates from the row's anchor: the taxonomy is the term URL's
  // second-to-last path segment (language prefixes and subpath deploys both
  // precede it), the term key its last segment, the label the anchor's
  // facet-value text. Rows whose indexed key differs from the URL slug
  // (contributors "Byron, Grace" -> byron-grace, classifications lowercase)
  // ship data-lcat-term with the exact key x-params and card attributes use.
  // A row without a link (no term page) keeps its button hidden.
  var buttons = [];
  var taxonomies = {};
  Array.prototype.forEach.call(document.querySelectorAll("button.lcat-facet-not"), function (el) {
    var a = el.parentElement && el.parentElement.querySelector("a[href]");
    if (!a) return;
    var segs = new URL(a.getAttribute("href"), window.location.href).pathname.split("/").filter(Boolean);
    if (segs.length < 2) return;
    var label = a.querySelector(".lcat-facet-value");
    var b = {
      el: el,
      taxonomy: segs[segs.length - 2],
      term: el.getAttribute("data-lcat-term") || segs[segs.length - 1],
      label: (label ? label.textContent : a.textContent).trim(),
    };
    el.setAttribute("aria-label", strings.exclude.replace("%s", b.label));
    el.title = strings.exclude.replace("%s", b.label);
    el.hidden = false;
    buttons.push(b);
    taxonomies[b.taxonomy] = true;
  });
  if (buttons.length === 0) return;

  function exclusions() {
    var out = [];
    new URLSearchParams(window.location.search).forEach(function (term, key) {
      if (key.charAt(0) === "x" && taxonomies[key.slice(1)] && term !== "") {
        out.push({ taxonomy: key.slice(1), term: term });
      }
    });
    return out;
  }

  function isExcluded(xs, taxonomy, term) {
    return xs.some(function (ex) { return ex.taxonomy === taxonomy && ex.term === term; });
  }

  function labelFor(ex) {
    var label = ex.term;
    buttons.forEach(function (b) {
      if (b.taxonomy === ex.taxonomy && b.term === ex.term) {
        label = b.label || ex.term;
      }
    });
    return label;
  }

  function chipsContainer() {
    var chips = document.getElementById("lcat-excluded");
    if (!chips && results) {
      chips = document.createElement("ul");
      chips.id = "lcat-excluded";
      chips.className = "lcat-excluded";
      chips.setAttribute("role", "status");
      results.parentNode.insertBefore(chips, results);
    }
    return chips;
  }

  function renderChips(xs) {
    var chips = chipsContainer();
    if (!chips) return;
    chips.textContent = "";
    chips.hidden = xs.length === 0;
    xs.forEach(function (ex) {
      var label = labelFor(ex);
      var li = document.createElement("li");
      li.appendChild(document.createTextNode(strings.excluded.replace("%s", label) + " "));
      var rm = document.createElement("button");
      rm.type = "button";
      rm.textContent = "×";
      rm.setAttribute("aria-label", strings.remove.replace("%s", label));
      rm.addEventListener("click", function () { toggle(ex.taxonomy, ex.term, false); });
      li.appendChild(rm);
      chips.appendChild(li);
    });
  }

  function hideCards(xs) {
    if (!results) return;
    for (var li = results.firstElementChild; li; li = li.nextElementSibling) {
      var card = li.querySelector(".lcat-card");
      var hide = false;
      if (card) {
        xs.forEach(function (ex) {
          var attr = card.getAttribute("data-lcat-" + ex.taxonomy);
          if (attr && attr.split("|").indexOf(ex.term) !== -1) hide = true;
        });
      }
      li.classList.toggle("lcat-neg-hidden", hide);
    }
  }

  // Static links do not know about exclusions, so browsing to another facet
  // or page would drop them -- carry the x-params on sidebar and pagination
  // links (never work links: a detail page has no result list to filter).
  function rewriteLinks(xs) {
    var links = document.querySelectorAll(".lcat-facets a, .pagination a");
    Array.prototype.forEach.call(links, function (a) {
      var url = new URL(a.getAttribute("href"), window.location.href);
      Object.keys(taxonomies).forEach(function (t) { url.searchParams.delete("x" + t); });
      xs.forEach(function (ex) { url.searchParams.append("x" + ex.taxonomy, ex.term); });
      a.setAttribute("href", url.pathname + url.search + url.hash);
    });
  }

  function apply() {
    var xs = exclusions();
    renderChips(xs);
    hideCards(xs);
    rewriteLinks(xs);
    buttons.forEach(function (b) {
      b.el.setAttribute("aria-pressed", isExcluded(xs, b.taxonomy, b.term) ? "true" : "false");
    });
  }

  function toggle(taxonomy, term, add) {
    var params = new URLSearchParams(window.location.search);
    var key = "x" + taxonomy;
    var vals = params.getAll(key).filter(function (v) { return v !== term; });
    if (add) vals.push(term);
    params.delete(key);
    vals.forEach(function (v) { params.append(key, v); });
    var q = params.toString();
    history.replaceState(null, "", window.location.pathname + (q ? "?" + q : "") + window.location.hash);
    apply();
  }

  buttons.forEach(function (b) {
    b.el.addEventListener("click", function () {
      toggle(b.taxonomy, b.term, !isExcluded(exclusions(), b.taxonomy, b.term));
    });
  });
  apply();
})();
