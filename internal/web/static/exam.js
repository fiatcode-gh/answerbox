// Student exam page: countdown, per-question autosave, navigator, submit.
// No dependencies, no build step; runs only on the exam page.
(function () {
  "use strict";

  var main = document.getElementById("exam");
  if (!main) {
    return;
  }

  var REQUEST_TIMEOUT_MS = 10000;
  var RETRY_FIRST_MS = 1000;
  var RETRY_MAX_MS = 10000;
  var TIME_UP_WAIT_MS = 2000;
  var NARROW = window.matchMedia("(max-width: 991.98px)");
  var ANNOUNCEMENTS = [
    { seconds: 600, text: "10 minutes left" },
    { seconds: 300, text: "5 minutes left" },
    { seconds: 60, text: "1 minute left" },
  ];

  var timer = document.getElementById("timer");
  var announce = document.getElementById("timer-announce");
  var saveState = document.getElementById("save-state");
  var navToggle = document.getElementById("nav-toggle");
  var nav = document.getElementById("question-nav");
  var answeredCount = document.getElementById("answered-count");
  var answeredSummary = document.querySelector(".answered-summary");
  var submitForm = document.getElementById("submit-form");
  var submitOpen = document.getElementById("submit-open");
  var dialog = document.getElementById("submit-dialog");
  var summary = document.getElementById("submit-summary");
  var confirmButton = document.getElementById("submit-confirm");
  var cancelButton = document.getElementById("submit-cancel");
  var answerURL = main.dataset.answerUrl;
  var statusURL = main.dataset.statusUrl;

  var deadline = Date.now() + Number(main.dataset.remainingMs);
  var countdown = null;
  var announced = {};
  var leaving = false; // submitting or reloading on purpose: no unload prompt
  var stopped = false; // time is up or the attempt moved: no more input
  var moved = false;

  // One save record per question, in page order.
  var questions = Array.prototype.map.call(
    document.querySelectorAll("section.question"),
    function (section) {
      var checked = section.querySelector("input[type=radio]:checked");
      var value = checked ? checked.value : null;
      return {
        id: section.dataset.questionId,
        radios: section.querySelectorAll("input[type=radio]"),
        state: section.querySelector(".q-state"),
        bubble: nav.querySelector('a.bubble[href="#' + section.id + '"]'),
        want: value,
        sent: value,
        inFlight: false,
        failed: false,
        retryDelay: RETRY_FIRST_MS,
        retryTimer: null,
      };
    }
  );

  function unsaved(q) {
    return q.want !== q.sent;
  }

  function anyUnsaved() {
    return questions.some(unsaved);
  }

  function anyInFlight() {
    return questions.some(function (q) {
      return q.inFlight;
    });
  }

  function answered() {
    return questions.filter(function (q) {
      return q.want !== null;
    }).length;
  }

  // ---- Countdown -------------------------------------------------------

  function pad(n) {
    return n < 10 ? "0" + n : String(n);
  }

  function tick() {
    var left = Math.max(0, deadline - Date.now());
    var secs = Math.ceil(left / 1000);
    timer.textContent = pad(Math.floor(secs / 60)) + ":" + pad(secs % 60);
    timer.dataset.level = secs <= 60 ? "danger" : secs <= 300 ? "warn" : "normal";

    // Announce only the most urgent threshold crossed since the last tick.
    var message = null;
    ANNOUNCEMENTS.forEach(function (a) {
      if (secs <= a.seconds && !announced[a.seconds]) {
        announced[a.seconds] = true;
        message = a.text;
      }
    });
    if (message) {
      announce.textContent = message;
    }

    if (left === 0) {
      timeUp();
    }
  }

  function stopCountdown() {
    if (countdown !== null) {
      clearInterval(countdown);
      countdown = null;
    }
  }

  // ---- Save queue --------------------------------------------------------

  function render() {
    questions.forEach(function (q) {
      var state;
      var text;
      if (!unsaved(q)) {
        state = q.want === null ? "idle" : "saved";
        text = q.want === null ? "" : "Saved";
      } else if (q.failed) {
        state = "error";
        text = "Not saved";
      } else {
        state = "saving";
        text = "Saving…";
      }
      if (q.state.dataset.state !== state) {
        q.state.dataset.state = state;
        q.state.textContent = text;
      }
    });

    var error = questions.some(function (q) {
      return unsaved(q) && q.failed;
    });
    if (error) {
      saveState.dataset.state = "error";
      saveState.textContent = "✗ Not saved — retrying";
    } else if (anyUnsaved()) {
      saveState.dataset.state = "saving";
      saveState.textContent = "Saving…";
    } else {
      saveState.dataset.state = "saved";
      saveState.textContent = "✓ All answers saved";
    }

    var waiting = anyUnsaved();
    confirmButton.disabled = waiting;
    confirmButton.textContent = waiting ? "Waiting for answers to save…" : "Submit";
  }

  function renderAnswered() {
    var text = "Answered " + answered() + " of " + questions.length;
    answeredCount.textContent = text;
    if (answeredSummary) {
      answeredSummary.textContent = text;
    }
  }

  function retryLater(q) {
    if (moved) {
      return;
    }
    var delay = q.retryDelay;
    q.retryDelay = Math.min(q.retryDelay * 2, RETRY_MAX_MS);
    q.retryTimer = setTimeout(function () {
      q.retryTimer = null;
      pump(q);
    }, delay);
  }

  function pump(q) {
    if (moved || q.inFlight || !unsaved(q)) {
      return;
    }
    if (q.retryTimer !== null) {
      clearTimeout(q.retryTimer);
      q.retryTimer = null;
    }
    var value = q.want;
    var controller = new AbortController();
    var timeout = setTimeout(function () {
      controller.abort();
    }, REQUEST_TIMEOUT_MS);
    q.inFlight = true;
    render();

    fetch(answerURL, {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        Accept: "application/json",
      },
      body: new URLSearchParams({ question: q.id, option: value }).toString(),
      signal: controller.signal,
    })
      .then(function (res) {
        if (res.status === 200) {
          return res.json().then(function (data) {
            return { status: 200, data: data };
          });
        }
        return { status: res.status };
      })
      .then(
        function (result) {
          clearTimeout(timeout);
          q.inFlight = false;
          switch (result.status) {
            case 200:
              q.sent = value;
              q.failed = false;
              q.retryDelay = RETRY_FIRST_MS;
              deadline = Date.now() + Number(result.data.remaining_ms);
              render();
              pump(q);
              return;
            case 409:
              reload();
              return;
            case 401:
              becomeMoved();
              return;
            case 422:
              q.failed = true;
              render();
              return;
            default:
              q.failed = true;
              render();
              retryLater(q);
          }
        },
        function () {
          clearTimeout(timeout);
          q.inFlight = false;
          q.failed = true;
          render();
          retryLater(q);
        }
      );
  }

  questions.forEach(function (q) {
    Array.prototype.forEach.call(q.radios, function (radio) {
      radio.addEventListener("change", function () {
        if (stopped || !radio.checked) {
          return;
        }
        q.want = radio.value;
        q.bubble.dataset.answered = "true";
        renderAnswered();
        render();
        pump(q);
      });
    });
  });

  // ---- Page lifecycle ----------------------------------------------------

  function reload() {
    leaving = true;
    location.reload();
  }

  function submit() {
    leaving = true;
    submitForm.submit();
  }

  function disableInputs() {
    questions.forEach(function (q) {
      Array.prototype.forEach.call(q.radios, function (radio) {
        radio.disabled = true;
      });
    });
  }

  function timeUp() {
    if (stopped) {
      return;
    }
    stopped = true;
    stopCountdown();
    disableInputs();
    document.getElementById("time-up").hidden = false;
    var since = Date.now();
    (function waitForSaves() {
      if (!anyInFlight() || Date.now() - since >= TIME_UP_WAIT_MS) {
        submit();
        return;
      }
      setTimeout(waitForSaves, 50);
    })();
  }

  function becomeMoved() {
    if (moved) {
      return;
    }
    moved = true;
    stopped = true;
    stopCountdown();
    disableInputs();
    submitOpen.disabled = true;
    questions.forEach(function (q) {
      if (q.retryTimer !== null) {
        clearTimeout(q.retryTimer);
        q.retryTimer = null;
      }
    });
    if (dialog.open) {
      dialog.close();
    }
    document.getElementById("moved").hidden = false;
  }

  window.addEventListener("beforeunload", function (event) {
    if (!leaving && anyUnsaved()) {
      event.preventDefault();
      event.returnValue = "";
    }
  });

  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState !== "visible" || stopped) {
      return;
    }
    fetch(statusURL, { credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (res) {
        if (res.status === 401) {
          becomeMoved();
          return;
        }
        if (res.status !== 200) {
          return;
        }
        return res.json().then(function (data) {
          if (data.status === "finished") {
            reload();
          } else if (data.status === "in_progress") {
            deadline = Date.now() + Number(data.remaining_ms);
            tick();
          }
        });
      })
      .catch(function () {
        // Offline: the local countdown keeps running; the next resync corrects it.
      });
  });

  // ---- Navigator ---------------------------------------------------------

  function setNavOpen(open) {
    nav.dataset.open = open ? "true" : "false";
    navToggle.setAttribute("aria-expanded", open ? "true" : "false");
  }

  navToggle.addEventListener("click", function () {
    setNavOpen(nav.dataset.open !== "true");
  });

  nav.addEventListener("click", function (event) {
    if (event.target.closest("a.bubble") && NARROW.matches) {
      setNavOpen(false);
    }
  });

  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape" && nav.dataset.open === "true") {
      setNavOpen(false);
      navToggle.focus();
    }
  });

  // ---- Submit dialog -----------------------------------------------------

  submitOpen.addEventListener("click", function () {
    var done = answered();
    var missing = questions.length - done;
    var text = "You answered " + done + " of " + questions.length + " questions.";
    if (missing === 1) {
      text += " 1 question has no answer.";
    } else if (missing > 1) {
      text += " " + missing + " questions have no answer.";
    }
    summary.textContent = text + " You can't change answers after submitting.";
    render();
    dialog.showModal();
  });

  cancelButton.addEventListener("click", function () {
    dialog.close();
  });

  confirmButton.addEventListener("click", function (event) {
    event.preventDefault();
    if (anyUnsaved()) {
      return;
    }
    submit();
  });

  // ---- Boot ----------------------------------------------------------------

  document.documentElement.classList.add("js");
  navToggle.hidden = false;
  // Thresholds already passed when the page loads are not announced again.
  var bootSeconds = Math.ceil(Math.max(0, deadline - Date.now()) / 1000);
  ANNOUNCEMENTS.forEach(function (a) {
    if (bootSeconds <= a.seconds) {
      announced[a.seconds] = true;
    }
  });
  render();
  tick();
  if (!stopped) {
    countdown = setInterval(tick, 250);
  }
})();
