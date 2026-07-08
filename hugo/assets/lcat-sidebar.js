/*
 * Shared facet sidebar loader (libcat tasks/150, opt-in via
 * [params.facets] shared). The sidebar body is published once per language as
 * a fingerprinted fragment asset instead of being inlined into every page;
 * this fetches it (immutable-cached, so one network hit per visit) and
 * inserts it into the page's host element. Scripts inserted via innerHTML are
 * inert per the HTML spec, so each executable script tag in the fragment (the
 * type-to-filter, the negatives hydration -- both written to run over the
 * already-rendered DOM) is re-created in place, which does execute; JSON
 * config scripts stay as parsed data. After insertion a lcat:facets-loaded
 * event tells already-running consumers (lcat-browse.js hydrates unlinked
 * rows into reader toggles, tasks/170) the sidebar DOM is ready. On any
 * fetch failure the host's no-JS fallback links are left in place.
 */
(function () {
  "use strict";
  var host = document.querySelector("[data-lcat-facets-src]");
  if (!host || !window.fetch) return;
  window
    .fetch(host.getAttribute("data-lcat-facets-src"))
    .then(function (res) {
      if (!res.ok) throw new Error("facets fragment: HTTP " + res.status);
      return res.text();
    })
    .then(function (html) {
      host.innerHTML = html;
      Array.prototype.forEach.call(host.querySelectorAll("script"), function (inert) {
        var type = inert.getAttribute("type");
        if (type && type !== "text/javascript" && type !== "module") return;
        var live = document.createElement("script");
        Array.prototype.forEach.call(inert.attributes, function (a) {
          live.setAttribute(a.name, a.value);
        });
        live.textContent = inert.textContent;
        inert.parentNode.replaceChild(live, inert);
      });
      document.dispatchEvent(new CustomEvent("lcat:facets-loaded"));
    })
    .catch(function () {
      /* keep the fallback links */
    });
})();
