// Turns the front page's stacked catalogue sections into tabs. Without
// JavaScript the sections stay stacked under their own headings, so
// nothing here is needed to reach any of them.
(function () {
  "use strict";

  var root = document.querySelector("[data-tabs]");
  if (!root) return;
  var panels = Array.prototype.filter.call(root.children, function (el) {
    return el.tagName === "SECTION" && el.querySelector("h2");
  });
  if (panels.length < 2) return;

  var list = document.createElement("div");
  list.className = "tabs";
  list.setAttribute("role", "tablist");
  list.setAttribute("aria-label", "Catalogue");

  var tabs = panels.map(function (panel, i) {
    var heading = panel.querySelector("h2");
    var tab = document.createElement("button");
    tab.type = "button";
    tab.id = "tab-" + heading.id;
    tab.textContent = heading.textContent;
    tab.setAttribute("role", "tab");
    tab.setAttribute("aria-controls", panel.id || (panel.id = "panel-" + heading.id));
    panel.setAttribute("role", "tabpanel");
    panel.setAttribute("aria-labelledby", tab.id);
    heading.hidden = true;
    tab.addEventListener("click", function () { select(i, false); });
    list.appendChild(tab);
    return tab;
  });

  function select(i, focus) {
    tabs.forEach(function (tab, j) {
      var on = i === j;
      tab.setAttribute("aria-selected", String(on));
      tab.tabIndex = on ? 0 : -1;
      panels[j].hidden = !on;
    });
    if (focus) tabs[i].focus();
  }

  list.addEventListener("keydown", function (e) {
    var i = tabs.indexOf(document.activeElement);
    if (i < 0) return;
    var next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
    if (next === undefined) return;
    e.preventDefault();
    select((next + tabs.length) % tabs.length, true);
  });

  root.insertBefore(list, panels[0]);
  select(0, false);
})();
