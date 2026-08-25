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

  // Plugin settings live in Stash's own configuration, so the UI half can
  // read them without spending an exec round trip. Deliberately re-read per
  // scene rather than cached: a toggle the user just flipped should take
  // effect on the next scene page, not after a browser reload.
  async function pluginSettings() {
    const data = await gql("query { configuration { plugins } }");
    return (data.configuration && data.configuration.plugins &&
      data.configuration.plugins[PLUGIN_ID]) || {};
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
      // Three states, kept distinct on purpose. A subtitle authored for
      // this exact file, one authored for another cut with a measured
      // correction the server applies on download, and one authored for
      // another cut that nobody has checked. Collapsing the last two into
      // a single "sync?" badge is what left a subtitle running seconds
      // early with nothing on screen to explain it.
      let sync = "";
      if (c.sibling_of && c.sibling_sync_known) {
        const shift = Math.round(c.sibling_offset_ms / 10) / 100;
        const signed = (shift >= 0 ? "+" : "") + shift + "s";
        // An estimate must not wear the same badge as a measurement.
        // "duration-delta" means nobody with both files ever checked: it
        // is the runtime difference, which is only right when the extra
        // footage happens to sit at the head.
        const estimated = c.sibling_offset_source === "duration-delta";
        sync = estimated
          ? ' <span class="badge badge-warning" title="Authored for another cut of this video. The shift below is ESTIMATED from the runtime difference — nobody with both files has checked it, and it is only right if the extra footage is at the start. Applied on download.">sync ~' +
            signed + "</span>"
          : ' <span class="badge badge-info" title="Authored for another cut of this video, and somebody with both files measured the shift. Applied on download, so the file you get is already corrected.">sync ' +
            signed + "</span>";
      } else if (c.sibling_of) {
        sync =
          ' <span class="badge badge-warning" title="Authored for another cut of this video, and nobody has recorded how far out it is. Offered as-is — it may not line up.">sync unknown</span>';
      } else if (c.cross_release) {
        sync =
          ' <span class="badge badge-warning" title="Timed against a different release of this scene — sync may be off by a few seconds.">sync?</span>';
      }
      // Date is only present on name-match candidates — the release's own
      // stored date, so a mismatch with the scene's date is visible
      // alongside the score-based reasons below.
      const dated =
        c.confidence === "name" && c.date
          ? ' <span class="text-muted small">dated ' + esc(c.date) + "</span>"
          : "";
      // Reasons are only present on name-match candidates — the server
      // scorer's justification, shown so a user can judge the offer
      // instead of trusting a bare label. A "date mismatch" reason gets a
      // visible ⚠ marker since it's the one reason that argues against the
      // candidate rather than for it.
      const reasonsText = (c.reasons || [])
        .map((r) => (r.indexOf("date mismatch") === 0 ? "⚠ " + r : r))
        .join(", ");
      const reasons =
        c.reasons && c.reasons.length
          ? '<div class="text-muted small">' + esc(reasonsText) + "</div>"
          : "";
      // Stash-box links (WP-C9a): every stash id the release carries, not
      // just the one that matched — a candidate can carry several (e.g.
      // both StashDB and FansDB). endpoint minus its trailing "/graphql"
      // plus "/scenes/<id>" is the scene page on that stash-box.
      const stashLinks = (c.release.stash_ids || [])
        .map((s) => {
          // The endpoint is server-controlled data, not a trusted config
          // value — a hostile or compromised server could hand back
          // "javascript:..." instead of a URL. esc() only HTML-escapes; it
          // does nothing to stop an anchor whose href scheme itself runs
          // script in Stash's origin on click. Parse first and only ever
          // render an <a> when the scheme is genuinely http(s); anything
          // else falls back to a plain-text label with no link at all.
          let parsed = null;
          try {
            parsed = new URL(s.endpoint);
          } catch (e) {
            parsed = null;
          }
          const host = parsed ? parsed.host : String(s.endpoint);
          const label =
            host === "stashdb.org" ? "StashDB" : host === "fansdb.cc" ? "FansDB" : host;
          if (!parsed || (parsed.protocol !== "http:" && parsed.protocol !== "https:")) {
            return esc(label);
          }
          const url =
            String(s.endpoint).replace(/\/graphql\/?$/, "") +
            "/scenes/" +
            encodeURIComponent(s.stash_id);
          return (
            '<a href="' + esc(url) + '" target="_blank" rel="noopener">' + esc(label) + " ↗</a>"
          );
        })
        .join(" ");
      const stashLinksHTML = stashLinks
        ? '<div class="small mt-1">' + stashLinks + "</div>"
        : "";
      const tracks = (c.release.tracks || [])
        .map((t) => {
          const ai = t.generated
            ? ' <span class="badge badge-secondary" title="Machine-generated — quality varies more than human-written subtitles.">AI</span>'
            : "";
          // kind (WP-K1/K3) is additive: absent on an older server, and
          // "default" is the unremarkable common case, so both render as
          // nothing rather than a badge nobody needs to read.
          const kindLabel =
            t.kind === "other" && t.kind_label ? t.kind_label : t.kind;
          const kind =
            t.kind && t.kind !== "default"
              ? ' <span class="badge badge-info" title="Subtitle kind">' + esc(kindLabel) + "</span>"
              : "";
          // Counts and vote buttons are filled in by decorateTrackRows
          // once this markup is in the DOM — via createElement, not
          // string concatenation, since the note field takes free-text
          // user input that must never round-trip through innerHTML.
          return (
            '<div class="d-flex align-items-center flex-wrap mb-1 moansubs-track-row" data-track="' +
            esc(t.id) + '" data-up="' + esc(t.up || 0) + '" data-down="' + esc(t.down || 0) +
            '" data-downloads="' + esc(t.downloads || 0) + '">' +
            "<span>" + esc(t.lang) + "</span>" + kind + ai +
            ' <span class="text-muted small ml-2">' + esc(t.license) + "</span>" +
            ' <span class="moansubs-counts text-muted small ml-2"></span>' +
            ' <span class="moansubs-vote-controls ml-2"></span>' +
            ' <button class="btn btn-sm btn-primary ml-auto moansubs-dl" data-track="' +
            esc(t.id) + '" data-for-release="' + esc(c.sibling_of ? c.release.id : "") +
            '">Download</button>' +
            "</div>"
          );
        })
        .join("");
      out.push(
        '<div class="card p-2 mb-2">' +
          '<div class="mb-1">' + confidenceBadge(c.confidence) + dated + sync +
          ' <span class="text-muted small">' + esc(meta) + "</span></div>" +
          reasons +
          stashLinksHTML +
          tracks +
          "</div>"
      );
    });
    panel.querySelector(".moansubs-body").innerHTML = out.join("");
    const votesEnabled = !!(result.features && result.features.indexOf("votes") !== -1);
    decorateTrackRows(panel, votesEnabled, !!result.has_token);
  }

  // forRelease is the release the local file actually matched, sent only
  // when the chosen track belongs to a different cut so the server can
  // retime it. Omitted otherwise: a track fetched for its own release
  // needs no shift and must come back exactly as authored.
  async function download(panel, sceneId, trackId, overwrite, forRelease) {
    const body = panel.querySelector(".moansubs-body");
    const note = document.createElement("div");
    try {
      const args = {
        mode: "download",
        scene_id: sceneId,
        track_id: String(trackId),
        overwrite: !!overwrite,
      };
      if (forRelease) args.for_release = String(forRelease);
      const res = await runOp(args);
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
        btn.onclick = () => download(panel, sceneId, trackId, true, forRelease);
        note.appendChild(btn);
      }
    }
    body.prepend(note);
    // The download just changed what is on disk, so the push offer's
    // cached answer for this scene is now stale.
    pushStatusCache.delete(sceneId);
    offerPush(panel, sceneId);
  }

  // -- Push local subs ------------------------------------------------------

  // Whether a scene has sidecars is a question only the exec half can
  // answer (they live on the Stash machine's disk, and Stash's caption
  // records exist only after a metadata scan), so the offer costs one
  // round trip per scene. Cached for the page session; invalidated by
  // anything this panel does to the disk.
  const pushStatusCache = new Map(); // sceneId -> push_status result

  async function offerPush(panel, sceneId) {
    // Opt-out, not opt-in: the setting is absent until someone ticks it,
    // and absent has to mean "offer the button" (see moansubs.yml).
    try {
      if ((await pluginSettings()).hide_push_button) return;
    } catch (err) {
      console.debug("[moansubs] reading settings failed:", err.message);
      return;
    }
    let status = pushStatusCache.get(sceneId);
    if (!status) {
      try {
        status = await runOp({ mode: "push_status", scene_id: sceneId });
      } catch (err) {
        // A scene we cannot inspect simply gets no offer — the panel must
        // not open with an error nobody asked for.
        console.debug("[moansubs] push status failed:", err.message);
        return;
      }
      pushStatusCache.set(sceneId, status);
    }
    // The page may have moved on during the await.
    if (!panel.isConnected || panel.dataset.scene !== sceneId) return;
    offerContribute(panel, sceneId, status);
    if (panel.querySelector(".moansubs-push")) return;
    if (!status.has_token || !status.sidecars || !status.sidecars.length) return;

    const btn = document.createElement("button");
    // Same solid class as "Find subtitles": Stash's dark theme renders an
    // outline button as a faint border on an already-grey panel, which
    // reads as "no button here" until you happen to mouse over it.
    btn.className = "btn btn-sm btn-secondary moansubs-push ml-2";
    btn.textContent = "Push local subs";
    btn.title =
      "Upload this scene's sidecar subtitles (" +
      status.sidecars.join(", ") +
      ") to the moansubs server";
    btn.onclick = () => push(panel, sceneId, btn);
    const kindPicker = buildKindPicker();
    panel.querySelector(".moansubs-search").after(btn, kindPicker);
  }

  // The push button's kind select (WP-K3): "auto-detect" (empty value)
  // leaves every sidecar's filename-inferred kind alone; any other choice
  // overrides all of them for that click. A server that predates the
  // "kinds" feature silently ignores whatever is picked here — the same
  // additive degrade every other capability in this file takes — so the
  // picker itself needs no server-feature check.
  function buildKindPicker() {
    const wrap = document.createElement("span");
    wrap.className = "moansubs-push-kind d-inline-flex align-items-center ml-2";

    // Stash's theme only styles form controls inside its own form groups,
    // so a bare .form-control here renders white; dress it as a button.
    const control = (el) => {
      el.className = "btn btn-sm btn-secondary";
      el.style.height = "auto";
      el.style.lineHeight = "1.5";
      el.style.textAlign = "left";
      return el;
    };

    const select = control(document.createElement("select"));
    select.title = "Kind to push this scene's sidecars as";
    [
      ["", "kind: auto"],
      ["default", "default"],
      ["cc", "cc"],
      ["sdh", "sdh"],
      ["forced", "forced"],
      ["other", "other…"],
    ].forEach(([value, text]) => {
      const opt = document.createElement("option");
      opt.value = value;
      opt.textContent = text;
      select.appendChild(opt);
    });

    const label = control(document.createElement("input"));
    label.type = "text";
    label.maxLength = 40;
    label.placeholder = "label";
    label.classList.add("ml-1");
    label.style.width = "8rem";
    label.hidden = true;
    select.onchange = () => {
      label.hidden = select.value !== "other";
    };

    wrap.appendChild(select);
    wrap.appendChild(label);
    return wrap;
  }

  // Contributing scene details is offered whenever the server can accept
  // them and a token exists — no disk check, because unlike a push it
  // sends nothing but what Stash already knows about the scene.
  function offerContribute(panel, sceneId, status) {
    if (!status.has_token || !status.metadata_feature) return;
    if (panel.querySelector(".moansubs-contribute")) return;
    const btn = document.createElement("button");
    btn.className = "btn btn-sm btn-secondary moansubs-contribute ml-2";
    btn.textContent = "Send scene details";
    btn.title =
      "Tell the server what this scene is — title, date, studio, performers " +
      "and stash-box ids. No subtitle is uploaded.";
    btn.onclick = () => contribute(panel, sceneId, btn);
    (panel.querySelector(".moansubs-push") || panel.querySelector(".moansubs-search")).after(btn);
  }

  async function contribute(panel, sceneId, btn) {
    const body = panel.querySelector(".moansubs-body");
    const note = document.createElement("div");
    btn.disabled = true;
    btn.textContent = "Sending\u2026";
    try {
      const res = await runOp({ mode: "contribute", scene_id: sceneId });
      let text;
      if (res.recorded) {
        text = "Sent. The server has this scene's details on record from your account.";
      } else if (res.unknown) {
        text = "Sent, but this server holds no release matching your file, so there was nothing to attach the details to.";
      } else if (res.skipped) {
        text = "Nothing to send: Stash has no title, date, studio, performers or stash-box id for this scene.";
      } else {
        text = "Sent.";
      }
      if (res.notes && res.notes.length) text += " \u2014 " + res.notes.join("; ");
      note.className = res.errors ? "alert alert-warning py-1 px-2" : "alert alert-success py-1 px-2";
      note.textContent = text;
    } catch (err) {
      note.className = "alert alert-danger py-1 px-2";
      note.textContent = err.message;
    } finally {
      btn.disabled = false;
      btn.textContent = "Send scene details";
    }
    body.prepend(note);
  }

  async function push(panel, sceneId, btn) {
    const body = panel.querySelector(".moansubs-body");
    const note = document.createElement("div");
    const picker = panel.querySelector(".moansubs-push-kind");
    const kind = picker ? picker.querySelector("select").value : "";
    const kindLabel = picker ? picker.querySelector("input").value : "";
    btn.disabled = true;
    btn.textContent = "Pushing\u2026";
    try {
      const res = await runOp({ mode: "push", scene_id: sceneId, kind: kind, kind_label: kindLabel });
      const parts = [];
      if (res.uploaded) parts.push(res.uploaded + " uploaded");
      if (res.duplicates) parts.push(res.duplicates + " already there");
      if (res.skipped) parts.push(res.skipped + " skipped");
      if (res.errors) parts.push(res.errors + " failed");
      note.className = res.errors
        ? "alert alert-warning py-1 px-2"
        : "alert alert-success py-1 px-2";
      note.textContent =
        (parts.length ? parts.join(", ") : "Nothing to push") +
        (res.notes && res.notes.length ? " \u2014 " + res.notes.join("; ") : "");
    } catch (err) {
      note.className = "alert alert-danger py-1 px-2";
      note.textContent = err.message;
    } finally {
      btn.disabled = false;
      btn.textContent = "Push local subs";
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
      if (dl) download(panel, sceneId, dl.dataset.track, false, dl.dataset.forRelease);
    });

    host.appendChild(panel);
    offerPush(panel, sceneId);
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
