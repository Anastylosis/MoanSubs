package api

import "net/http"

// features is the API surface this build advertises via
// GET /api/v1/version. It is a hand-maintained slice literal, not
// introspected from the mux: each later package that adds a new endpoint
// appends its own name here, in that package's own commit, so an old
// client can tell a feature is missing before it trips over a 404.
var features = []string{"lookup", "match", "withdraw", "stats", "srt"}

// versionResponse is the body of GET /api/v1/version.
type versionResponse struct {
	Version  string   `json:"version"`
	Features []string `json:"features"`
}

// handleVersion implements GET /api/v1/version: lets the plugin discover
// this node's version and feature set up front, so it can degrade a
// missing feature (skip with one log line) instead of discovering it as a
// 404 mid-task. Anonymous and unthrottled — unlike every other endpoint it
// never touches the database, so it costs nothing to poll once per task.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, versionResponse{
		Version:  s.Version,
		Features: features,
	})
}
