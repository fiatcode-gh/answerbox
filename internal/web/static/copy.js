// Exam page: the Copy link button puts the student link on the clipboard.
// Without this script the button stays hidden and the link can be selected.
(function () {
  "use strict";

  var button = document.getElementById("copy-link");
  var status = document.getElementById("copy-status");
  if (!button || !status || !navigator.clipboard) {
    return;
  }
  button.hidden = false;
  button.addEventListener("click", function () {
    navigator.clipboard.writeText(button.dataset.copy).then(function () {
      status.textContent = button.dataset.done;
    }, function () {
      status.textContent = button.dataset.failed;
    });
  });
})();
