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
  var TYPE_PAUSE_MS = 800; // a typed answer is sent once typing pauses this long
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
  var eventURL = main.dataset.eventUrl;
  var monitorOn = main.dataset.monitor === "on";

  // Deadline arithmetic uses the monotonic performance.now(), not the wall
  // clock, so a change of the device clock cannot end the exam early or late.
  // It is anchored to the server's remaining time at load and on every response.
  var deadline = performance.now() + Number(main.dataset.remainingMs);
  var countdown = null;
  var announced = {};
  var leaving = false; // submitting or reloading on purpose: no unload prompt
  var stopped = false; // time is up or the attempt moved: no more input
  var moved = false;

  // textValue is a typed answer as the queue holds it: null when blank.
  function textValue(input) {
    return input.value.trim() === "" ? null : input.value;
  }

  // One save record per question, in page order. A fill-in question has a
  // text box (input) and no radios; its value is the typed text.
  var questions = Array.prototype.map.call(
    document.querySelectorAll("section.question"),
    function (section) {
      var input = section.querySelector("input.answer-text");
      var checked = section.querySelector("input[type=radio]:checked");
      var value = input ? textValue(input) : checked ? checked.value : null;
      return {
        id: section.dataset.questionId,
        radios: section.querySelectorAll("input[type=radio]"),
        input: input,
        state: section.querySelector(".q-state"),
        bubble: nav.querySelector('a.bubble[href="#' + section.id + '"]'),
        want: value,
        sent: value,
        inFlight: false,
        failed: false,
        rejected: false, // the server refused the last answer: wait for a new one
        retryDelay: RETRY_FIRST_MS,
        retryTimer: null,
        typeTimer: null, // waiting for typing to pause
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
    var left = Math.max(0, deadline - performance.now());
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
      if (q.rejected) {
        state = "error";
        text = "Not saved";
      } else if (!unsaved(q)) {
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

    var retrying = questions.some(function (q) {
      return unsaved(q) && q.failed;
    });
    var rejected = questions.find(function (q) {
      return q.rejected;
    });
    if (retrying) {
      saveState.dataset.state = "error";
      saveState.textContent = "✗ Not saved — retrying";
    } else if (anyUnsaved()) {
      saveState.dataset.state = "saving";
      saveState.textContent = "Saving…";
    } else if (rejected) {
      saveState.dataset.state = "error";
      saveState.textContent = rejected.input ? "✗ Not saved — check your answer" : "✗ Not saved — choose again";
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
    var fields = { question: q.id };
    if (q.input) {
      fields.text = value === null ? "" : value;
    } else {
      fields.option = value;
    }
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
      body: new URLSearchParams(fields).toString(),
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
              deadline = performance.now() + Number(result.data.remaining_ms);
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
              if (q.want === value) {
                rejectAnswer(q);
              } else {
                pump(q); // a newer answer is waiting; send that one
              }
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

  // rejectAnswer handles an answer the server refused. Retrying cannot help,
  // so the question goes back to its last saved answer (Submit is not blocked
  // by it) and shows an error until the student answers again. A text box
  // keeps what the student typed.
  function rejectAnswer(q) {
    q.want = q.sent;
    q.failed = false;
    q.rejected = true;
    Array.prototype.forEach.call(q.radios, function (radio) {
      radio.checked = radio.value === q.sent;
    });
    q.bubble.dataset.answered = q.want === null ? "false" : "true";
    renderAnswered();
    render();
  }

  function clearTypeTimer(q) {
    if (q.typeTimer !== null) {
      clearTimeout(q.typeTimer);
      q.typeTimer = null;
    }
  }

  questions.forEach(function (q) {
    Array.prototype.forEach.call(q.radios, function (radio) {
      radio.addEventListener("change", function () {
        if (stopped || !radio.checked) {
          return;
        }
        q.want = radio.value;
        q.rejected = false;
        q.bubble.dataset.answered = "true";
        renderAnswered();
        render();
        pump(q);
      });
    });
    if (!q.input) {
      return;
    }
    q.input.addEventListener("input", function () {
      if (stopped) {
        return;
      }
      q.want = textValue(q.input);
      q.rejected = false;
      q.bubble.dataset.answered = q.want === null ? "false" : "true";
      renderAnswered();
      render();
      clearTypeTimer(q);
      q.typeTimer = setTimeout(function () {
        q.typeTimer = null;
        pump(q);
      }, TYPE_PAUSE_MS);
    });
    ["change", "blur"].forEach(function (type) {
      q.input.addEventListener(type, function () {
        clearTypeTimer(q);
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
      if (q.input) {
        q.input.disabled = true;
      }
    });
  }

  function timeUp() {
    if (stopped) {
      return;
    }
    stopped = true;
    stopCountdown();
    // The last typed characters go out now, inside the wait below.
    questions.forEach(function (q) {
      if (q.typeTimer !== null) {
        clearTypeTimer(q);
        pump(q);
      }
    });
    disableInputs();
    document.getElementById("time-up").hidden = false;
    var since = performance.now();
    (function waitForSaves() {
      if (!anyInFlight() || performance.now() - since >= TIME_UP_WAIT_MS) {
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
      clearTypeTimer(q);
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
            deadline = performance.now() + Number(data.remaining_ms);
            tick();
          }
        });
      })
      .catch(function () {
        // Offline: the local countdown keeps running; the next resync corrects it.
      });
  });

  // ---- Monitor -------------------------------------------------------------
  // With the exam's monitor on: report leaving the page for more than 3 s,
  // block and report copy, cut, paste and the context menu, report window
  // resizes, warn the student after each event and keep the screen on.
  // Events have their own queue, apart from the answer queue, so a slow or
  // failing event request never delays saving an answer. A lost event is
  // acceptable.

  var AWAY_MS = 3000;
  var RESIZE_SETTLE_MS = 1000;
  var BANNER_MS = 5000;
  var EVENT_QUEUE_MAX = 50;
  // Touch devices change the viewport height when the address bar slides
  // away on scroll, so there only width changes count as a resize.
  var TOUCH = window.matchMedia("(pointer: coarse)");
  var bar = document.querySelector(".exam-bar");
  var sections = document.querySelectorAll("section.question");
  var monitorBanner = null;
  var bannerText = "";
  var bannerTimer = null;
  var eventQueue = [];
  var eventSending = false;
  var eventsStopped = false;
  var eventRetryDelay = RETRY_FIRST_MS;
  var eventRetryTimer = null;

  // currentQuestion is the id of the first question still visible below the
  // sticky bar, or "" when none is. Leaving the page and resizing use it, as
  // do blocked actions outside every question.
  function currentQuestion() {
    var top = bar.getBoundingClientRect().bottom;
    for (var i = 0; i < sections.length; i++) {
      if (sections[i].getBoundingClientRect().bottom > top) {
        return sections[i].dataset.questionId;
      }
    }
    return "";
  }

  // questionOf is the id of the question containing target, or "" when the
  // target is outside every question (or there is none).
  function questionOf(target) {
    var el = target instanceof Element ? target : target && target.parentElement;
    var section = el ? el.closest("section.question") : null;
    return section ? section.dataset.questionId : "";
  }

  // setBanner shows or hides the warning. It overlays the page under the
  // sticky bar, so showing it moves no question. While it is shown,
  // --ge-monitor-offset (how far it reaches below the bar) moves question
  // jump targets and the sticky navigator below it once the bar is stuck; at
  // the very top of the page it briefly covers the first question's heading.
  function setBanner(shown) {
    var root = document.documentElement.style;
    monitorBanner.hidden = !shown;
    if (!shown) {
      root.removeProperty("--ge-monitor-offset");
      return;
    }
    // Setting the text again makes screen readers announce the alert again.
    monitorBanner.textContent = bannerText;
    var reach = monitorBanner.getBoundingClientRect().bottom - bar.getBoundingClientRect().bottom;
    root.setProperty("--ge-monitor-offset", reach + "px");
  }

  // flag reports one event and shows the warning. Copy, cut, paste and the
  // context menu pass their event target and name the question containing
  // it; leaving the page and resizing pass none and name currentQuestion().
  function flag(kind, target) {
    if (stopped) {
      return;
    }
    setBanner(true);
    clearTimeout(bannerTimer);
    bannerTimer = setTimeout(function () {
      setBanner(false);
    }, BANNER_MS);
    if (eventsStopped || eventQueue.length >= EVENT_QUEUE_MAX) {
      return;
    }
    eventQueue.push({ kind: kind, question: questionOf(target) || currentQuestion() });
    sendEvents();
  }

  // retryEvents schedules the one pending retry; while it waits, new events
  // only join the queue.
  function retryEvents() {
    var delay = eventRetryDelay;
    eventRetryDelay = Math.min(eventRetryDelay * 2, RETRY_MAX_MS);
    eventRetryTimer = setTimeout(function () {
      eventRetryTimer = null;
      sendEvents();
    }, delay);
  }

  function sendEvents() {
    if (eventSending || eventRetryTimer !== null || eventsStopped || eventQueue.length === 0) {
      return;
    }
    var ev = eventQueue[0];
    var body = new URLSearchParams({ kind: ev.kind });
    if (ev.question) {
      body.set("question", ev.question);
    }
    var controller = new AbortController();
    var timeout = setTimeout(function () {
      controller.abort();
    }, REQUEST_TIMEOUT_MS);
    eventSending = true;
    fetch(eventURL, {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        Accept: "application/json",
      },
      body: body.toString(),
      signal: controller.signal,
      keepalive: true,
    }).then(
      function (res) {
        clearTimeout(timeout);
        eventSending = false;
        if (res.status === 401 || res.status === 409) {
          // The attempt moved or finished; the answer and status paths
          // handle that. Later events have nowhere to go.
          eventsStopped = true;
          eventQueue = [];
          return;
        }
        if (res.status === 200 || res.status === 422) {
          eventQueue.shift();
          eventRetryDelay = RETRY_FIRST_MS;
          sendEvents();
          return;
        }
        retryEvents();
      },
      function () {
        clearTimeout(timeout);
        eventSending = false;
        retryEvents();
      }
    );
  }

  // An absence starts when the page is hidden or loses focus and ends when it
  // is visible and focused again. It is reported once, after AWAY_MS: by a
  // timer while away, or on return if the timer could not run (a sleeping
  // phone).
  var pageHidden = document.visibilityState === "hidden";
  var pageBlurred = false;
  var awaySince = null;
  var awayCounted = false;
  var awayTimer = null;

  function countAway() {
    if (awaySince === null || awayCounted) {
      return;
    }
    awayCounted = true;
    flag("left_page");
  }

  function updateAway() {
    var away = pageHidden || pageBlurred;
    if (away && awaySince === null) {
      awaySince = performance.now();
      awayCounted = false;
      awayTimer = setTimeout(countAway, AWAY_MS + 1);
    } else if (!away && awaySince !== null) {
      clearTimeout(awayTimer);
      awayTimer = null;
      if (performance.now() - awaySince > AWAY_MS) {
        countAway();
      }
      awaySince = null;
    }
  }

  // The screen wake lock avoids false left_page events from a screen that
  // turns off. Browsers release it when the page is hidden, so it is asked
  // for again on return. Failure is silent.
  var wakeLock = null;

  function requestWakeLock() {
    if (stopped || !navigator.wakeLock || document.visibilityState !== "visible") {
      return;
    }
    if (wakeLock !== null && !wakeLock.released) {
      return;
    }
    try {
      navigator.wakeLock.request("screen").then(
        function (lock) {
          wakeLock = lock;
        },
        function () {}
      );
    } catch (e) {
      // Unsupported or not allowed: the exam works without it.
    }
  }

  function startMonitor() {
    // Without the banner element these throw before any listener is added:
    // the monitor stays off and the rest of the page keeps working.
    monitorBanner = document.getElementById("monitor-banner");
    bannerText = monitorBanner.textContent; // the template holds the wording
    // A drop into the answer box inserts text like a paste, so it counts as one.
    var blocked = { copy: "copy", cut: "copy", paste: "paste", drop: "paste", contextmenu: "right_click" };
    Object.keys(blocked).forEach(function (type) {
      document.addEventListener(type, function (event) {
        event.preventDefault();
        flag(blocked[type], event.target);
      });
    });

    document.addEventListener("visibilitychange", function () {
      pageHidden = document.visibilityState === "hidden";
      updateAway();
      requestWakeLock();
    });
    window.addEventListener("blur", function () {
      pageBlurred = true;
      updateAway();
    });
    window.addEventListener("focus", function () {
      pageBlurred = false;
      updateAway();
    });

    var lastWidth = window.innerWidth;
    var lastHeight = window.innerHeight;
    var resizeTimer = null;
    window.addEventListener("resize", function () {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(function () {
        var widthChanged = window.innerWidth !== lastWidth;
        var heightChanged = window.innerHeight !== lastHeight;
        lastWidth = window.innerWidth;
        lastHeight = window.innerHeight;
        if (widthChanged || (heightChanged && !TOUCH.matches)) {
          flag("resize");
        }
      }, RESIZE_SETTLE_MS);
    });

    window.addEventListener("online", function () {
      clearTimeout(eventRetryTimer);
      eventRetryTimer = null;
      sendEvents();
    });
    updateAway();
    requestWakeLock();
  }

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
  var bootSeconds = Math.ceil(Math.max(0, deadline - performance.now()) / 1000);
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
  // The monitor starts last and on its own, so a failure in it can never stop
  // the countdown, auto-submit or answer saving.
  if (monitorOn) {
    try {
      startMonitor();
    } catch (e) {
      // The exam works without the monitor.
    }
  }
})();
