// "More like this" reveal (libcat tasks/310).
//
// The rail renders every neighbour the sidecar carries. When that is more than
// [params] similarShown, the page also emits a hidden "View more" button and this
// script. It collapses the rail to the first N tiles and reveals the button --
// in that order, and only if it runs at all.
//
// The extras are not hidden in the markup on purpose. A `hidden` attribute there
// would bury half the rail for a reader whose script did not load, with no button
// to bring it back. Hiding is this script's job, so failing to run leaves the
// reader with more than they asked for rather than less.
(function () {
  "use strict";
  var rails = document.querySelectorAll(".lcat-similar[data-similar-shown]");
  Array.prototype.forEach.call(rails, function (rail) {
    var extras = rail.querySelectorAll(".lcat-similar-item--extra");
    var button = rail.querySelector(".lcat-similar-more");
    if (!extras.length || !button) return;

    Array.prototype.forEach.call(extras, function (li) {
      li.hidden = true;
    });
    button.hidden = false;

    button.addEventListener("click", function () {
      Array.prototype.forEach.call(extras, function (li) {
        li.hidden = false;
      });
      button.remove();
      // The tiles that just appeared are below the button that was focused, and
      // it no longer exists. Move focus to the first of them so a keyboard reader
      // lands on what changed rather than back at the top of the document.
      var first = extras[0] && extras[0].querySelector("a");
      if (first) first.focus();
    });
  });
})();
