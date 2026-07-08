/*
 * Interim client-side search filter (progressive enhancement) for the libcat
 * Hugo module. It filters the rendered results list by a case-insensitive
 * substring match on each card's text. This is a stopgap until the roaringrange
 * WASM reader is wired (libcat tasks/010), which will replace this with a real
 * ranked index query over the search-manifest.json artifact. Kept intentionally
 * small and dependency-free; the form still works (full list) with JS disabled.
 */
(function () {
  "use strict";
  var form = document.querySelector(".lcat-search");
  var results = document.getElementById("lcat-results");
  if (!form || !results) {
    return;
  }
  var input = form.querySelector('input[name="q"]');
  var items = Array.prototype.slice.call(results.querySelectorAll("li"));

  function apply(q) {
    q = (q || "").trim().toLowerCase();
    var shown = 0;
    items.forEach(function (li) {
      var match = q === "" || li.textContent.toLowerCase().indexOf(q) !== -1;
      li.hidden = !match;
      if (match) {
        shown++;
      }
    });
    var count = document.querySelector(".lcat-resultcount");
    if (count) {
      count.textContent = q === "" ? items.length + " works" : shown + " of " + items.length + " works match “" + q + "”";
    }
  }

  var params = new URLSearchParams(window.location.search);
  if (params.has("q")) {
    input.value = params.get("q");
    apply(input.value);
  }
  form.addEventListener("submit", function (e) {
    e.preventDefault();
    apply(input.value);
  });
  input.addEventListener("input", function () {
    apply(input.value);
  });
})();
