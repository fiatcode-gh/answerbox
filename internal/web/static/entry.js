// The entry card of /: the Join and Resume tabs, and the two code boxes (the
// exam code under Join, the resume code under Resume). The tabs' labels
// become tab buttons with arrow keys, Home and End. Each code box
// upper-cases what is typed, refuses characters that are never in a code
// (with a short polite hint), takes a pasted code (the exam code also a
// pasted link), puts in the resume code's dash itself, and draws the mask
// behind the text. The alphabet and every sentence come from the page.
// Without this script the tabs are radios with labels, and the boxes are
// plain inputs whose placeholder is the mask.
(function () {
  "use strict";

  // Typed or pasted between groups; dropped without a word.
  var SEPARATORS = " \t\n\r-_.·/";

  // The words of the entry card around el: its alphabet and refusal hints.
  function wordsOf(el) {
    var root = el.closest(".entry-modes");
    var get = function (name) {
      return (root && root.getAttribute(name)) || "";
    };
    return {
      alphabet: get("data-alphabet"),
      letters: get("data-refuse-letters"),
      digits: get("data-refuse-digits"),
      other: get("data-refuse-other"),
    };
  }

  function refusal(words, ch) {
    if (ch >= "A" && ch <= "Z") return words.letters;
    if (ch >= "0" && ch <= "9") return words.digits;
    return words.other;
  }

  // The code's symbols in text, how many of them stand before position
  // caret, and the hint for the first refused character.
  function read(words, text, caret) {
    var symbols = "";
    var before = 0;
    var refused = "";
    for (var i = 0; i < text.length; i++) {
      var ch = text.charAt(i).toUpperCase();
      if (words.alphabet.indexOf(ch) >= 0) {
        symbols += ch;
      } else if (SEPARATORS.indexOf(ch) < 0 && !refused) {
        refused = refusal(words, ch);
      }
      if (i < caret) before = symbols.length;
    }
    return { symbols: symbols, before: before, refused: refused };
  }

  function codeBox(input) {
    var words = wordsOf(input);
    var count = Number(input.getAttribute("data-symbols"));
    var group = Number(input.getAttribute("data-group")) || 0;
    var takesLink = input.hasAttribute("data-link");
    var refuseLink = input.getAttribute("data-refuse-link") || "";
    var field = input.closest(".mask-field");
    var mask = field.querySelector(".mask");
    var ids = (input.getAttribute("aria-describedby") || "").split(" ");
    var msg = null;
    for (var k = 0; k < ids.length; k++) {
      var el = ids[k] && document.getElementById(ids[k]);
      if (el && el.classList.contains("code-msg")) msg = el;
    }
    // The placeholder is the mask; the drawn mask takes over from it.
    var template = input.getAttribute("placeholder") || "";
    input.removeAttribute("placeholder");
    var last = "";
    // A keyboard is composing a word (Gboard and others): the value is
    // the keyboard's until the composition ends.
    var composing = false;

    function format(symbols) {
      return group && symbols.length > group ? symbols.slice(0, group) + "-" + symbols.slice(group) : symbols;
    }

    function position(before) {
      return group && before > group ? before + 1 : before;
    }

    function say(text) {
      if (msg && msg.textContent !== text) msg.textContent = text;
    }

    function slot(className, text) {
      var span = document.createElement("span");
      if (className) span.className = className;
      span.textContent = text;
      mask.appendChild(span);
    }

    // The mask: the value again, invisible, then what is still open. While
    // composing, one slot per missing symbol and no dash yet.
    function paint() {
      var value = input.value;
      var focused = document.activeElement === input;
      var rest;
      if (composing) {
        var missing = Math.max(0, count - read(words, value, 0).symbols.length);
        rest = new Array(missing + 1).join("_");
      } else {
        rest = template.slice(value.length);
      }
      mask.textContent = "";
      slot("typed", value);
      var nextMarked = !focused;
      for (var i = 0; i < rest.length; i++) {
        var ch = rest.charAt(i);
        if (ch === "-") {
          slot("fixed", ch);
        } else if (!nextMarked) {
          slot("next", ch);
          nextMarked = true;
        } else {
          slot("", ch);
        }
      }
    }

    function put(symbols, before) {
      symbols = symbols.slice(0, count);
      before = Math.min(before, symbols.length);
      var value = format(symbols);
      if (input.value !== value) input.value = value;
      var at = position(before);
      if (document.activeElement === input) input.setSelectionRange(at, at);
      last = symbols;
      paint();
    }

    // Upper-case, drop separators and refused characters, cut to length,
    // put in the dash. Running it twice changes nothing.
    function clean(inputType) {
      var caret = input.selectionStart == null ? input.value.length : input.selectionStart;
      var r = read(words, input.value, caret);
      // A delete that only took the dash takes the symbol beside it.
      if (r.symbols === last && inputType === "deleteContentBackward" && r.before > 0) {
        r.symbols = r.symbols.slice(0, r.before - 1) + r.symbols.slice(r.before);
        r.before -= 1;
      } else if (r.symbols === last && inputType === "deleteContentForward") {
        r.symbols = r.symbols.slice(0, r.before) + r.symbols.slice(r.before + 1);
      }
      say(r.refused);
      put(r.symbols, r.before);
    }

    // Typing, autocorrect and anything else that changes the value.
    input.addEventListener("input", function (e) {
      if (composing || e.isComposing) {
        say(read(words, input.value, 0).refused);
        paint();
        return;
      }
      clean(e.inputType);
    });

    input.addEventListener("compositionstart", function () {
      composing = true;
      paint();
    });

    // Chromium sends no input event after compositionend, so the cleaning
    // runs here.
    input.addEventListener("compositionend", function () {
      composing = false;
      clean("");
    });

    // A paste of "fg2j62", "FG2-J62" or, for the exam code, a link with
    // ?code= fills the box.
    input.addEventListener("paste", function (e) {
      var data = e.clipboardData || window.clipboardData;
      var text = data && data.getData("text");
      if (!text) return;
      e.preventDefault();
      var link = /[?&]code=([^&#\s]*)/i.exec(text);
      if (takesLink && link) {
        var code = null;
        try {
          code = decodeURIComponent(link[1]);
        } catch (err) {
          code = null;
        }
        var fromLink = code === null ? null : read(words, code, Infinity);
        if (!fromLink || !fromLink.symbols) {
          say(refuseLink);
          return;
        }
        say(fromLink.refused);
        put(fromLink.symbols, fromLink.symbols.length);
        return;
      }
      if (/^\s*(https?:\/\/|www\.)/i.test(text)) {
        say(refuseLink);
        return;
      }
      var pasted = read(words, text, Infinity);
      var start = read(words, input.value, input.selectionStart).before;
      var end = read(words, input.value, input.selectionEnd).before;
      // A whole code replaces what is there; a part goes in at the caret.
      if (pasted.symbols.length === count) {
        start = 0;
        end = last.length;
      }
      say(pasted.refused);
      put(last.slice(0, start) + pasted.symbols + last.slice(end), start + pasted.symbols.length);
    });

    ["blur", "click", "keyup", "select"].forEach(function (type) {
      input.addEventListener(type, paint);
    });
    document.addEventListener("selectionchange", function () {
      if (document.activeElement === input) paint();
    });

    // Focus with the caret before a value the server sent back (autofocus
    // puts it there) moves it after the value. A click places the caret
    // after this runs, so a tap still lands where it was aimed.
    function caretToEnd() {
      var n = input.value.length;
      if (n && input.selectionStart === 0 && input.selectionEnd === 0) input.setSelectionRange(n, n);
      paint();
    }
    input.addEventListener("focus", caretToEnd);

    // A value the server sent back is shown in the code's own format.
    last = read(words, input.value, input.value.length).symbols.slice(0, count);
    if (input.value) input.value = format(last);
    field.classList.add("is-live");
    if (document.activeElement === input) caretToEnd();
    else paint();
  }

  // The tabs: the radios' labels become role=tab buttons, the radios go,
  // and the checked one's panel stays open. Arrow keys, Home and End move
  // between the tabs and open the one they land on.
  function tabs(root) {
    var labels = root.querySelectorAll(".tablist > label.tab");
    var list = root.querySelector(".tablist");
    if (!list || !labels.length) return;
    var items = [];
    var chosen = 0;
    for (var i = 0; i < labels.length; i++) {
      var label = labels[i];
      var radio = document.getElementById(label.htmlFor);
      if (radio && radio.checked) chosen = i;
      var panelID = label.getAttribute("data-panel");
      var tab = document.createElement("button");
      tab.type = "button";
      tab.className = label.className;
      tab.id = panelID.replace("panel-", "tab-");
      tab.setAttribute("role", "tab");
      tab.setAttribute("aria-controls", panelID);
      tab.textContent = label.textContent;
      var panel = document.getElementById(panelID);
      panel.setAttribute("role", "tabpanel");
      panel.setAttribute("aria-labelledby", tab.id);
      var lead = document.getElementById(label.getAttribute("data-lead"));
      label.replaceWith(tab);
      if (radio) radio.remove();
      items.push({ tab: tab, panel: panel, lead: lead });
    }
    list.setAttribute("role", "tablist");
    list.setAttribute("aria-label", list.getAttribute("data-label") || "");

    function select(index, focus) {
      for (var j = 0; j < items.length; j++) {
        var on = j === index;
        items[j].tab.setAttribute("aria-selected", on ? "true" : "false");
        items[j].tab.tabIndex = on ? 0 : -1;
        items[j].panel.hidden = !on;
        if (items[j].lead) items[j].lead.hidden = !on;
      }
      if (focus) items[index].tab.focus();
    }

    items.forEach(function (item, index) {
      item.tab.addEventListener("click", function () {
        select(index, false);
      });
    });

    list.addEventListener("keydown", function (e) {
      var at = items.findIndex(function (item) {
        return item.tab === document.activeElement;
      });
      if (at < 0) return;
      var to = { ArrowRight: at + 1, ArrowLeft: at - 1, Home: 0, End: items.length - 1 }[e.key];
      if (to === undefined) return;
      e.preventDefault();
      select((to + items.length) % items.length, true);
    });

    select(chosen, false);
    root.classList.add("is-live");
  }

  var roots = document.querySelectorAll(".entry-modes");
  for (var r = 0; r < roots.length; r++) tabs(roots[r]);
  var boxes = document.querySelectorAll("input.mask-input");
  for (var b = 0; b < boxes.length; b++) codeBox(boxes[b]);
})();
