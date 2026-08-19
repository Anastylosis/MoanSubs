// moansubs Stash plugin, UI half.
//
// All network egress to the moansubs server happens in the exec half via
// runPluginOperation — this file only ever talks to Stash's own /graphql
// (same origin, no CSP entry needed), which is why the manifest carries no
// csp block and the server address lives in plugin settings rather than a
// baked-in connect-src (PLAN.md "The Stash plugin").
//
// Integration is deliberately DOM injection, not PluginApi.patch: a React
// patch that returns anything the minified tree doesn't expect crashes the
// whole page (observed as React error #31 on the front page against
// v0.31.1), while DOM injection degrades to "no badge" at worst. Navigation
// is tracked via the stash:location event plus a MutationObserver for
// content that renders after the route settles.
(function () {
  "use strict";

  const api = window.PluginApi;
  const PLUGIN_ID = "moansubs";

  // -- GraphQL bridge -------------------------------------------------------

  async function gql(query, variables) {
    const res = await fetch("/graphql", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ query, variables }),
    });
    const body = await res.json();
    if (body.errors && body.errors.length) {
      throw new Error(body.errors.map((e) => e.message).join("; "));
    }
    return body.data;
  }

  async function runOp(args) {
    const data = await gql(
      `mutation($id: ID!, $args: Map!) { runPluginOperation(plugin_id: $id, args: $args) }`,
      { id: PLUGIN_ID, args }
    );
    return data.runPluginOperation;
  }

  // esc makes server-provided strings safe to place into innerHTML.
  function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    })[c]);
  }

  // -- SceneCard badges: shared debounced batch -----------------------------

  const badgeCache = new Map(); // sceneId -> {matches, best} | "pending"
  let badgeQueue = new Set();
  let badgeTimer = null;

  function queueBadge(sceneId) {
    if (badgeCache.has(sceneId)) return;
    badgeCache.set(sceneId, "pending");
    badgeQueue.add(sceneId);
    if (badgeTimer) clearTimeout(badgeTimer);
    badgeTimer = setTimeout(flushBadges, 400);
  }

  async function flushBadges() {
    const ids = Array.from(badgeQueue);
    badgeQueue = new Set();
    if (!ids.length) return;
    let result = {};
    try {
      // Exec-side cap is 100 scenes per call; chunk to stay under it.
      for (let i = 0; i < ids.length; i += 100) {
        Object.assign(
          result,
          (await runOp({ mode: "badge", scene_ids: ids.slice(i, i + 100) })) || {}
        );
      }
    } catch (err) {
      // A dead moansubs server must not affect browsing: cache no-match so
      // the wall doesn't retry on every mutation, log once at debug.
      console.debug("[moansubs] badge lookup failed:", err.message);
    }
    for (const id of ids) {
      badgeCache.set(id, result[id] || { matches: 0 });
    }
    decorateCards();
  }

  // sceneIdFromCard pulls the scene id out of a card's detail link.
  function sceneIdFromCard(card) {
    const a = card.querySelector('a[href*="/scenes/"]');
    if (!a) return null;
    const m = a.getAttribute("href").match(/\/scenes\/(\d+)/);
    return m ? m[1] : null;
  }

  function decorateCards() {
    document.querySelectorAll(".scene-card").forEach((card) => {
      const id = sceneIdFromCard(card);
      if (!id) return;
      const status = badgeCache.get(id);
      if (status === undefined) {
        queueBadge(id);
        return;
      }
      if (status === "pending" || !status.matches) return;
      if (card.querySelector(".moansubs-badge")) return;

      const host =
        card.querySelector(".card-section") ||
        card.querySelector(".scene-card-body") ||
        card;
      const badge = document.createElement("span");
      badge.className = "moansubs-badge badge badge-info";
      badge.style.cssText =
        "position:absolute;top:0.3rem;left:0.3rem;z-index:8;opacity:0.9;";
      badge.textContent = "CC";
      badge.title =
        status.best === "exact"
          ? "Subtitles available (exact match)"
          : "Subtitles available (different encode — may need sync check)";
      if (host === card || getComputedStyle(host).position === "static") {
        card.style.position = "relative";
        card.appendChild(badge);
      } else {
        host.appendChild(badge);
      }
    });
  }

  // -- ScenePage panel ------------------------------------------------------

  function confidenceBadge(c) {
    if (c === "exact") return '<span class="badge badge-success">Exact match</span>';
    if (c === "high") return '<span class="badge badge-primary">Different encode</span>';
    // "name" is the v2 no-phash fallback (server-side title/filename
    // scorer) — offer-only regardless of the server's verdict, same as
    // "offer" below, but visibly distinct since there's no fingerprint
    // evidence behind it at all.
    if (c === "name") return '<span class="badge badge-warning">Name match</span>';
    return '<span class="badge badge-warning">Possible match</span>';
  }

  // -- Votes -----------------------------------------------------------

  // The five closed-vocabulary down-vote reasons (API.md "Votes"), with
  // readable labels for the <select>.
  const VOTE_REASONS = [
    ["out_of_sync", "Out of sync"],
    ["wrong_content", "Wrong content"],
    ["wrong_language", "Wrong language"],
    ["low_quality", "Low quality"],
    ["spam", "Spam"],
  ];

  function setCounts(row) {
    row.querySelector(".moansubs-counts").textContent =
      "↓" + row.dataset.downloads + " ▲" + row.dataset.up + " ▼" + row.dataset.down;
  }

  function clearRowError(row) {
    const err = row.querySelector(".moansubs-vote-error");
    if (err) err.remove();
  }

  function showRowError(row, message) {
    let err = row.querySelector(".moansubs-vote-error");
    if (!err) {
      err = document.createElement("div");
      err.className = "moansubs-vote-error alert alert-danger py-1 px-2 mt-1 w-100";
      row.appendChild(err);
    }
    err.textContent = message;
  }

  // castVote runs the "vote" mode and updates the row's counts in place;
  // value 0 retracts. Errors surface next to the row, the same place
  // download() puts its own note.
  async function castVote(row, value, reason, note) {
    clearRowError(row);
    try {
      const res = await runOp({
        mode: "vote",
        track_id: row.dataset.track,
        value: value,
        reason: reason || "",
        note: note || "",
      });
      row.dataset.up = res.up;
      row.dataset.down = res.down;
      setCounts(row);
    } catch (err) {
      showRowError(row, err.message);
    }
  }

  // buildDownvoteForm creates the inline reason-picker: a <select> of the
  // five reasons, an optional note, and confirm/cancel buttons — built
  // with createElement throughout, since the note field carries a value
  // the user typed, not just server data run through esc().
  function buildDownvoteForm(row) {
    const form = document.createElement("div");
    form.className = "moansubs-vote-form d-flex align-items-center mt-1 w-100";

    const select = document.createElement("select");
    select.className = "form-control form-control-sm mr-1";
    select.style.maxWidth = "10rem";
    VOTE_REASONS.forEach(([value, label]) => {
      const opt = document.createElement("option");
      opt.value = value;
      opt.textContent = label;
      select.appendChild(opt);
    });

    const note = document.createElement("input");
    note.type = "text";
    note.maxLength = 300;
    note.placeholder = "Optional note";
    note.className = "form-control form-control-sm mr-1";

    const confirm = document.createElement("button");
    confirm.className = "btn btn-sm btn-outline-danger";
    confirm.textContent = "Confirm";
    confirm.onclick = () => {
      form.remove();
      castVote(row, -1, select.value, note.value);
    };

    const cancel = document.createElement("button");
    cancel.className = "btn btn-sm btn-outline-secondary ml-1";
    cancel.textContent = "Cancel";
    cancel.onclick = () => form.remove();

    form.appendChild(select);
    form.appendChild(note);
    form.appendChild(confirm);
    form.appendChild(cancel);
    return form;
  }

  // decorateTrackRows fills in each track row's counts and, when the
  // server advertises "votes", its two vote buttons — hidden entirely
  // when the feature isn't there, disabled with a tooltip when it is but
  // no upload token is configured (WP-C4 spec).
  function decorateTrackRows(panel, votesEnabled, hasToken) {
    panel.querySelectorAll(".moansubs-track-row").forEach((row) => {
      setCounts(row);
      if (!votesEnabled) return;

      const controls = row.querySelector(".moansubs-vote-controls");
      const up = document.createElement("button");
      up.className = "btn btn-sm btn-outline-success mr-1";
      up.textContent = "▲";
      const down = document.createElement("button");
      down.className = "btn btn-sm btn-outline-danger";
      down.textContent = "▼";

      if (!hasToken) {
        up.disabled = true;
        down.disabled = true;
        up.title = down.title = "set an upload token to vote";
      } else {
        up.onclick = () => castVote(row, 1, "", "");
        down.onclick = () => {
          const existing = row.querySelector(".moansubs-vote-form");
          if (existing) {
            existing.remove();
            return;
          }
          row.appendChild(buildDownvoteForm(row));
        };
      }
      controls.appendChild(up);
      controls.appendChild(down);
    });
  }

  function renderCandidates(panel, sceneId, result) {
    const out = [];
    if (result.note) {
      out.push('<div class="alert alert-info py-1 px-2">' + esc(result.note) + "</div>");
    }
    if (!result.candidates.length) {
      out.push('<div class="text-muted">No subtitles found for this scene.</div>');
    }
    result.candidates.forEach((c) => {
      const delta = Math.round(c.duration_delta_ms / 100) / 10;
      const meta =
        (c.hamming_distance >= 0 ? "phash distance " + c.hamming_distance + ", " : "") +
        "Δ " + delta + "s";
      const sync = c.cross_release
        ? ' <span class="badge badge-warning" title="Timed against a different release of this scene — sync may be off by a few seconds.">sync?</span>'
        : "";
      // Reasons are only present on name-match candidates — the server
      // scorer's justification, shown so a user can judge the offer
      // instead of trusting a bare label.
      const reasons =
        c.reasons && c.reasons.length
          ? '<div class="text-muted small">' + esc(c.reasons.join(", ")) + "</div>"
          : "";
      const tracks = (c.release.tracks || [])
        .map((t) => {
          const ai = t.generated
            ? ' <span class="badge badge-secondary" title="Machine-generated — quality varies more than human-written subtitles.">AI</span>'
            : "";
          // Counts and vote buttons are filled in by decorateTrackRows
          // once this markup is in the DOM — via createElement, not
          // string concatenation, since the note field takes free-text
          // user input that must never round-trip through innerHTML.
          return (
            '<div class="d-flex align-items-center flex-wrap mb-1 moansubs-track-row" data-track="' +
            esc(t.id) + '" data-up="' + esc(t.up || 0) + '" data-down="' + esc(t.down || 0) +
            '" data-downloads="' + esc(t.downloads || 0) + '">' +
            "<span>" + esc(t.lang) + "</span>" + ai +
            ' <span class="text-muted small ml-2">' + esc(t.license) + "</span>" +
            ' <span class="moansubs-counts text-muted small ml-2"></span>' +
            ' <span class="moansubs-vote-controls ml-2"></span>' +
            ' <button class="btn btn-sm btn-primary ml-auto moansubs-dl" data-track="' +
            esc(t.id) + '">Download</button>' +
            "</div>"
          );
        })
        .join("");
      out.push(
        '<div class="card p-2 mb-2">' +
          '<div class="mb-1">' + confidenceBadge(c.confidence) + sync +
          ' <span class="text-muted small">' + esc(meta) + "</span></div>" +
          reasons +
          tracks +
          "</div>"
      );
    });
    panel.querySelector(".moansubs-body").innerHTML = out.join("");
    const votesEnabled = !!(result.features && result.features.indexOf("votes") !== -1);
    decorateTrackRows(panel, votesEnabled, !!result.has_token);
  }

  async function download(panel, sceneId, trackId, overwrite) {
    const body = panel.querySelector(".moansubs-body");
    const note = document.createElement("div");
    try {
      const res = await runOp({
        mode: "download",
        scene_id: sceneId,
        track_id: String(trackId),
        overwrite: !!overwrite,
      });
      let text = "Saved " + res.path + ".";
      if (res.lang_normalized) {
        text += " Language written as ." + res.lang +
          " — caption filenames cannot carry a region.";
      }
      text += res.scan_job_id
        ? " Scan triggered; reload the page once it finishes."
        : " Reload the page to see it.";
      note.className = "alert alert-success py-1 px-2";
      note.textContent = text;
    } catch (err) {
      note.className = "alert alert-danger py-1 px-2";
      note.textContent = err.message;
      if (/already exists/.test(err.message)) {
        const btn = document.createElement("button");
        btn.className = "btn btn-sm btn-outline-danger ml-2";
        btn.textContent = "Overwrite";
        btn.onclick = () => download(panel, sceneId, trackId, true);
        note.appendChild(btn);
      }
    }
    body.prepend(note);
  }

  function injectScenePanel() {
    const m = window.location.pathname.match(/\/scenes\/(\d+)/);
    const existing = document.getElementById("moansubs-panel");
    if (!m) {
      if (existing) existing.remove();
      return;
    }
    const sceneId = m[1];
    if (existing) {
      if (existing.dataset.scene === sceneId) return;
      existing.remove();
    }
    // The tab content column is the stable anchor across v0.3x layouts.
    const host =
      document.querySelector(".scene-tabs .tab-content") ||
      document.querySelector(".scene-tabs");
    if (!host) return; // page still rendering; the observer will retry

    const panel = document.createElement("div");
    panel.id = "moansubs-panel";
    panel.dataset.scene = sceneId;
    panel.className = "mt-3";
    panel.innerHTML =
      '<div class="d-flex align-items-center mb-2">' +
      '<h5 class="mb-0 mr-3">Subtitles</h5>' +
      '<button class="btn btn-sm btn-secondary moansubs-search">Find subtitles</button>' +
      "</div>" +
      '<div class="moansubs-body"></div>';

    panel.querySelector(".moansubs-search").onclick = async function () {
      const btn = this;
      btn.disabled = true;
      btn.textContent = "Searching…";
      try {
        const res = await runOp({ mode: "search", scene_id: sceneId });
        renderCandidates(panel, sceneId, res);
      } catch (err) {
        panel.querySelector(".moansubs-body").innerHTML =
          '<div class="alert alert-danger py-1 px-2">' + esc(err.message) + "</div>";
      } finally {
        btn.disabled = false;
        btn.textContent = "Search again";
      }
    };
    panel.addEventListener("click", (e) => {
      const dl = e.target.closest(".moansubs-dl");
      if (dl) download(panel, sceneId, dl.dataset.track, false);
    });

    host.appendChild(panel);
  }

  // -- wiring ---------------------------------------------------------------

  let scheduled = null;
  function refresh() {
    // Coalesce mutation bursts; both injectors are idempotent.
    if (scheduled) return;
    scheduled = setTimeout(() => {
      scheduled = null;
      try {
        injectScenePanel();
        decorateCards();
      } catch (err) {
        console.debug("[moansubs] ui refresh failed:", err.message);
      }
    }, 250);
  }

  if (api && api.Event && api.Event.addEventListener) {
    api.Event.addEventListener("stash:location", refresh);
  }
  new MutationObserver(refresh).observe(document.body, {
    childList: true,
    subtree: true,
  });
  refresh();
})();
