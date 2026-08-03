// moansubs Stash plugin, UI half.
//
// All network egress to the moansubs server happens in the exec half via
// runPluginOperation — this file only ever talks to Stash's own /graphql
// (same origin, no CSP entry needed), which is why the manifest carries no
// csp block and the server address lives in plugin settings rather than a
// baked-in connect-src (PLAN.md "The Stash plugin").
//
// Patching strategy, deliberately conservative because PluginApi internals
// shift between Stash releases:
//  - ScenePage: a self-contained panel appended after ScenePage.TabContent's
//    render result. No cloning of Stash's tab tree — appending to the
//    render output is version-proof.
//  - SceneCard: a badge appended after SceneCard.Popovers. Each badge
//    registers its scene id in a shared debounced batch, so a wall of cards
//    costs one exec invocation (badge mode), not one per card.
(function () {
  "use strict";

  const api = window.PluginApi;
  if (!api) return;
  const React = api.React;
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

  // runOp calls the exec half synchronously and returns its output.
  // PluginOutput.Error surfaces as a GraphQL error, handled in gql().
  async function runOp(args) {
    const data = await gql(
      `mutation($id: ID!, $args: Map!) { runPluginOperation(plugin_id: $id, args: $args) }`,
      { id: PLUGIN_ID, args }
    );
    return data.runPluginOperation;
  }

  // -- SceneCard badge: shared debounced batch ------------------------------

  // cache: sceneId -> {matches, best} | "pending". Subscribers are badge
  // components waiting for their scene's answer.
  const badgeCache = new Map();
  const badgeSubs = new Map();
  let badgeQueue = new Set();
  let badgeTimer = null;

  function requestBadge(sceneId, cb) {
    const hit = badgeCache.get(sceneId);
    if (hit && hit !== "pending") {
      cb(hit);
      return;
    }
    if (!badgeSubs.has(sceneId)) badgeSubs.set(sceneId, []);
    badgeSubs.get(sceneId).push(cb);
    if (hit === "pending") return;

    badgeCache.set(sceneId, "pending");
    badgeQueue.add(sceneId);
    if (badgeTimer) clearTimeout(badgeTimer);
    // One flush per wall render; 300ms comfortably collects a page of cards.
    badgeTimer = setTimeout(flushBadges, 300);
  }

  async function flushBadges() {
    const ids = Array.from(badgeQueue);
    badgeQueue = new Set();
    if (!ids.length) return;
    let result = {};
    try {
      // Exec-side cap is 100 scenes per call; chunk to stay under it.
      for (let i = 0; i < ids.length; i += 100) {
        const chunk = ids.slice(i, i + 100);
        Object.assign(
          result,
          (await runOp({ mode: "badge", scene_ids: chunk })) || {}
        );
      }
    } catch (err) {
      // A dead moansubs server must not break the scene wall: resolve
      // everything as no-match and log once.
      console.debug("[moansubs] badge lookup failed:", err.message);
      ids.forEach((id) => (result[id] = { matches: 0 }));
    }
    for (const id of ids) {
      const st = result[id] || { matches: 0 };
      badgeCache.set(id, st);
      (badgeSubs.get(id) || []).forEach((cb) => cb(st));
      badgeSubs.delete(id);
    }
  }

  function SceneCardBadge(props) {
    const sceneId = props.scene && props.scene.id;
    const [status, setStatus] = React.useState(null);
    React.useEffect(() => {
      if (!sceneId) return;
      let live = true;
      requestBadge(String(sceneId), (st) => live && setStatus(st));
      return () => {
        live = false;
      };
    }, [sceneId]);

    if (!status || !status.matches) return null;
    const title =
      status.best === "exact"
        ? "Subtitles available (exact match)"
        : "Subtitles available (different encode — may need sync check)";
    return React.createElement(
      "span",
      {
        className: "moansubs-badge badge badge-info",
        title: title,
        style: { marginLeft: "0.25rem" },
      },
      "CC"
    );
  }

  api.patch.after("SceneCard.Popovers", function (props, result) {
    return [result, React.createElement(SceneCardBadge, props)];
  });

  // -- ScenePage panel ------------------------------------------------------

  function confidenceLabel(c) {
    if (c === "exact") return { text: "Exact match", cls: "badge-success" };
    if (c === "high") return { text: "Different encode", cls: "badge-primary" };
    return { text: "Possible match", cls: "badge-warning" };
  }

  function CandidateRow(props) {
    const { candidate, sceneId, onDone } = props;
    const [busy, setBusy] = React.useState(false);
    const [msg, setMsg] = React.useState(null);
    const conf = confidenceLabel(candidate.confidence);
    const h = React.createElement;

    async function download(trackId, overwrite) {
      setBusy(true);
      setMsg(null);
      try {
        const res = await runOp({
          mode: "download",
          scene_id: sceneId,
          track_id: String(trackId),
          overwrite: !!overwrite,
        });
        let note = "Saved " + res.path + ".";
        if (res.lang_normalized) {
          note +=
            " Language written as ." +
            res.lang +
            " — caption filenames cannot carry a region.";
        }
        note += res.scan_job_id
          ? " Scan triggered; reload the page once it finishes."
          : " Reload the page to see it.";
        setMsg({ ok: true, text: note });
        if (onDone) onDone();
      } catch (err) {
        const exists = /already exists/.test(err.message);
        setMsg({
          ok: false,
          text: err.message,
          offerOverwrite: exists ? trackId : null,
        });
      } finally {
        setBusy(false);
      }
    }

    const tracks = (candidate.release.tracks || []).map((t) =>
      h(
        "div",
        { key: t.id, className: "moansubs-track d-flex align-items-center mb-1" },
        h("span", { className: "mr-2" }, t.lang),
        t.generated
          ? h(
              "span",
              {
                className: "badge badge-secondary mr-2",
                title:
                  "Machine-generated (ASR" +
                  (t.has_provenance ? ", provenance recorded" : "") +
                  ") — quality varies more than human-written subtitles.",
              },
              "AI"
            )
          : null,
        h("span", { className: "text-muted small mr-2" }, t.license),
        h(
          "button",
          {
            className: "btn btn-sm btn-primary ml-auto",
            disabled: busy,
            onClick: () => download(t.id, false),
          },
          busy ? "Saving…" : "Download"
        )
      )
    );

    return h(
      "div",
      { className: "moansubs-candidate card p-2 mb-2" },
      h(
        "div",
        { className: "d-flex align-items-center mb-1" },
        h("span", { className: "badge " + conf.cls + " mr-2" }, conf.text),
        candidate.cross_release
          ? h(
              "span",
              {
                className: "badge badge-warning mr-2",
                title:
                  "Timed against a different release of this scene — sync may be off by a few seconds.",
              },
              "sync?"
            )
          : null,
        h(
          "span",
          { className: "text-muted small" },
          candidate.hamming_distance >= 0
            ? "phash distance " + candidate.hamming_distance + ", "
            : "",
          "Δ " + Math.round(candidate.duration_delta_ms / 100) / 10 + "s"
        )
      ),
      tracks,
      msg
        ? h(
            "div",
            {
              className:
                "alert py-1 px-2 mb-0 " +
                (msg.ok ? "alert-success" : "alert-danger"),
            },
            msg.text,
            msg.offerOverwrite
              ? h(
                  "button",
                  {
                    className: "btn btn-sm btn-outline-danger ml-2",
                    onClick: () => download(msg.offerOverwrite, true),
                  },
                  "Overwrite"
                )
              : null
          )
        : null
    );
  }

  function MoansubsPanel(props) {
    const sceneId = props.sceneId;
    const [state, setState] = React.useState({ phase: "idle" });
    const h = React.createElement;

    async function search() {
      setState({ phase: "loading" });
      try {
        const res = await runOp({ mode: "search", scene_id: sceneId });
        setState({ phase: "done", result: res });
      } catch (err) {
        setState({ phase: "error", error: err.message });
      }
    }

    let body = null;
    if (state.phase === "loading") {
      body = h("div", { className: "text-muted" }, "Searching…");
    } else if (state.phase === "error") {
      body = h("div", { className: "alert alert-danger py-1 px-2" }, state.error);
    } else if (state.phase === "done") {
      const r = state.result;
      const parts = [];
      if (r.note) {
        parts.push(
          h("div", { key: "note", className: "alert alert-info py-1 px-2" }, r.note)
        );
      }
      if (!r.candidates.length) {
        parts.push(
          h("div", { key: "none", className: "text-muted" }, "No subtitles found for this scene.")
        );
      } else {
        r.candidates.forEach((c, i) =>
          parts.push(
            h(CandidateRow, { key: i, candidate: c, sceneId: sceneId })
          )
        );
      }
      body = h("div", null, parts);
    }

    return h(
      "div",
      { className: "moansubs-panel mt-3 px-3" },
      h(
        "div",
        { className: "d-flex align-items-center mb-2" },
        h("h5", { className: "mb-0 mr-3" }, "Subtitles"),
        h(
          "button",
          {
            className: "btn btn-sm btn-secondary",
            disabled: state.phase === "loading",
            onClick: search,
          },
          state.phase === "idle" ? "Find subtitles" : "Search again"
        )
      ),
      body
    );
  }

  api.patch.after("ScenePage.TabContent", function (props, result) {
    const sceneId = props.scene && props.scene.id;
    if (!sceneId) return result;
    return [
      result,
      React.createElement(MoansubsPanel, {
        key: "moansubs",
        sceneId: String(sceneId),
      }),
    ];
  });
})();
