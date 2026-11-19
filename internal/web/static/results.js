// Results page: while the list can still change, fetch this page again every
// 10 seconds and swap the list in. Filter and sort live in the address, so
// they are kept; reloading the page does the same without this script.
(function () {
  "use strict";

  var PERIOD_MS = 10000;
  var TIMEOUT_MS = 8000;
  var region = document.getElementById("results-live");
  var status = document.getElementById("results-status");
  if (!region || !status || region.dataset.live !== "true" ||
      !window.fetch || !window.DOMParser || !window.AbortController) {
    return;
  }
  var timer = null;
  var busy = false;
  var stopped = false;

  function say(text) {
    status.textContent = text;
    status.hidden = false;
  }

  function stop() {
    stopped = true;
    window.clearInterval(timer);
    status.hidden = true;
  }

  // The teacher is selecting text in the list (a resume code, a name).
  function selecting() {
    var sel = window.getSelection();
    return !!sel && !sel.isCollapsed && region.contains(sel.anchorNode);
  }

  function swap(doc) {
    var next = doc.getElementById("results-live");
    // No list (the sign-in page), or the filter bar appeared or went: show
    // what the server answers.
    if (!next || !doc.getElementById("name-filter") !== !document.getElementById("name-filter")) {
      window.location.reload();
      return;
    }
    if (selecting()) {
      return;
    }
    var box = region.querySelector(".overflow-auto");
    var left = box ? box.scrollLeft : 0;
    var active = document.activeElement;
    var href = active && region.contains(active) ? active.getAttribute("href") : null;
    var fresh = document.importNode(next, true);
    region.replaceWith(fresh);
    region = fresh;
    box = region.querySelector(".overflow-auto");
    if (box) {
      box.scrollLeft = left;
    }
    if (href !== null) {
      var links = region.querySelectorAll("a");
      for (var i = 0; i < links.length; i++) {
        if (links[i].getAttribute("href") === href) {
          links[i].focus({ preventScroll: true });
          break;
        }
      }
    }
    if (region.dataset.live === "true") {
      say(status.dataset.liveText);
    } else {
      stop();
    }
  }

  function refresh() {
    if (stopped || busy || document.hidden) {
      return;
    }
    busy = true;
    var abort = new AbortController();
    var giveUp = window.setTimeout(function () { abort.abort(); }, TIMEOUT_MS);
    fetch(window.location.href, { credentials: "same-origin", cache: "no-store", signal: abort.signal })
      .then(function (res) {
        if (!res.ok) {
          throw new Error("status " + res.status);
        }
        return res.text();
      })
      .then(function (html) {
        swap(new DOMParser().parseFromString(html, "text/html"));
      })
      .catch(function () {
        say(status.dataset.staleText);
      })
      .then(function () {
        window.clearTimeout(giveUp);
        busy = false;
      });
  }

  say(status.dataset.liveText);
  timer = window.setInterval(refresh, PERIOD_MS);
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) {
      refresh();
    }
  });
})();
