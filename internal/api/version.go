package api

import "net/http"

// features is the API surface this build advertises via
// GET /api/v1/version. It is a hand-maintained slice literal, not
// introspected from the mux: each later package that adds a new endpoint
// appends its own name here, in that package's own commit, so an old
// client can tell a feature is missing before it trips over a 404.
var features = []string{"lookup", "match", "withdraw", "stats", "srt", "votes", "stash_ids", "metadata", "kinds", "revisions"}

// versionResponse is the body of GET /api/v1/version.
type versionResponse struct {
	Version  string   `json:"version"`
	Features []string `json:"features"`
	// StashEndpoints is the node's stash-box endpoint allow-list (WP-R6,
	// Server.StashEndpoints) verbatim — a single "*" entry means any
	// http(s) endpoint. The plugin's msclientStashIDs filters what it
	// sends on a push against this, rather than racing the server's own
	// 400 one id at a time; a node predating this field (nil here) is
	// read by the plugin as "send everything, as before".
	StashEndpoints []string `json:"stash_endpoints"`
}

// handleVersion implements GET /api/v1/version: lets the plugin discover
// this node's version and feature set up front, so it can degrade a
// missing feature (skip with one log line) instead of discovering it as a
// 404 mid-task. Anonymous and unthrottled — unlike every other endpoint it
// never touches the database, so it costs nothing to poll once per task.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, versionResponse{
		Version:        s.Version,
		Features:       features,
		StashEndpoints: s.StashEndpoints,
	})
}
