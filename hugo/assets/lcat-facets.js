// Type-to-filter for facet groups: a case-insensitive substring
// match over the group's already-rendered entries -- no index, no fetch. At
// vocabulary scale (10k+ terms) the sidebar is unscannable without it. Rows
// hide via the hidden attribute so counts and links stay untouched.
(function () {
  "use strict";
  document.querySelectorAll("[data-lcat-facet-filter]").forEach(function (input) {
    var list = input.parentElement.querySelector("ul");
    if (!list) return;
    input.addEventListener("input", function () {
      var q = input.value.trim().toLowerCase();
      for (var li = list.firstElementChild; li; li = li.nextElementSibling) {
        li.hidden = q !== "" && li.textContent.toLowerCase().indexOf(q) === -1;
      }
    });
  });
})();
