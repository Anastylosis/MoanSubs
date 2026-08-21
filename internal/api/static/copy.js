// Adds a "Copy" button to every token block on the page.
//
// The button is created here rather than sitting in the template, so a
// visitor with JavaScript disabled sees exactly the page they saw before
// this file existed — a selectable token and no dead control. That is the
// same progressive-enhancement rule the upload fingerprinter follows.
(function () {
  "use strict";

  var LABEL = "Copy";
  var DONE = "Copied";
  var FAILED = "Press ⌘/Ctrl+C";

  // Selecting the token's text is the fallback path and also what makes a
  // manual copy easy when the clipboard API is unavailable (it needs a
  // secure context, which a plain-HTTP node on a LAN is not).
  function selectText(el) {
    var range = document.createRange();
    range.selectNodeContents(el);
    var sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  }

  function flash(button, text) {
    button.textContent = text;
    button.disabled = true;
    window.setTimeout(function () {
      button.textContent = LABEL;
      button.disabled = false;
    }, 1600);
  }

  function attach(token) {
    var button = document.createElement("button");
    button.type = "button";
    button.className = "copybtn";
    button.textContent = LABEL;
    // The token element is the accessible name a screen reader needs here;
    // "Copy" alone does not say what is being copied.
    button.setAttribute("aria-label", "Copy the API token");

    button.addEventListener("click", function () {
      var text = token.getAttribute("data-token") || token.textContent.trim();
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(
          function () { flash(button, DONE); },
          function () { selectText(token); flash(button, FAILED); }
        );
        return;
      }
      // No clipboard API: select it so one keystroke finishes the job.
      selectText(token);
      flash(button, FAILED);
    });

    token.appendChild(button);
    token.classList.add("has-copy");
  }

  var tokens = document.querySelectorAll("code.token");
  for (var i = 0; i < tokens.length; i++) {
    attach(tokens[i]);
  }
})();
