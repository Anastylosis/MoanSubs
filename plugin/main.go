// The moansubs Stash plugin, exec half: an RPC-interface plugin binary that
// searches the moansubs server for subtitles matching a scene's fingerprints,
// downloads a chosen track as a sidecar file, and triggers a metadata scan so
// Stash picks the caption up.
//
// interface: rpc rather than raw, deliberately — raw tasks are killed with no
// signal, and rpc gets a Stop call for graceful cancellation (PLAN.md "The
// Stash plugin"). Stash speaks net/rpc with the JSON-RPC codec over this
// process's stdin/stdout (the natefinch/pie provider protocol), calling
// RPCRunner.Run with a PluginInput and RPCRunner.Stop for cancellation. That
// protocol surface is small enough that it is implemented here directly (see
// rpcserve.go) instead of importing the whole stash module for its
// pkg/plugin/common package.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

func main() {
	if err := servePlugin(&runner{}); err != nil {
		// Nothing is listening on the RPC channel anymore; stderr with the
		// error control prefix is the only voice left.
		logError("moansubs plugin: %v", err)
		os.Exit(1)
	}
}

// runner implements the RPCRunner service Stash expects. One process serves
// one task invocation; Stop cancels the in-flight Run. net/rpc dispatches
// calls concurrently, so cancel is mutex-guarded.
type runner struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// PluginInput mirrors stash's pkg/plugin/common.PluginInput. Field names on
// ServerConnection are Go-default (no json tags upstream), so they arrive
// capitalized; PluginInput's own fields are tagged snake_case upstream.
type PluginInput struct {
	ServerConnection ServerConnection `json:"server_connection"`
	Args             map[string]any   `json:"args"`
}

type ServerConnection struct {
	Scheme        string         `json:"Scheme"`
	Host          string         `json:"Host"`
	Port          int            `json:"Port"`
	SessionCookie *SessionCookie `json:"SessionCookie"`
	Dir           string         `json:"Dir"`
	PluginDir     string         `json:"PluginDir"`
}

type SessionCookie struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

// PluginOutput mirrors stash's pkg/plugin/common.PluginOutput.
type PluginOutput struct {
	Error  *string `json:"error"`
	Output any     `json:"output"`
}

func (r *runner) Run(input PluginInput, output *PluginOutput) error {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	defer cancel()

	out, err := dispatch(ctx, input)
	if err != nil {
		msg := err.Error()
		output.Error = &msg
		return nil
	}
	output.Output = out
	return nil
}

func (r *runner) Stop(_ struct{}, output *bool) error {
	logInfo("stop requested, cancelling")
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	*output = true
	return nil
}

// dispatch routes a task invocation by its "mode" arg. The mode is validated
// before any connection is made — a typo'd task must fail with "unknown
// mode", not with whatever the Stash dial happens to say. The UI half calls
// these via runPluginOperation, so return values must be JSON-shaped data,
// not human prose.
func dispatch(ctx context.Context, input PluginInput) (any, error) {
	mode, _ := input.Args["mode"].(string)
	switch mode {
	case "probe", "search", "download", "vote", "badge", "push", "push_status", "push_all":
	default:
		return nil, fmt.Errorf("unknown mode %q (want probe, search, download, vote, badge, push, push_status or push_all)", mode)
	}

	app, err := newApp(ctx, input)
	if err != nil {
		return nil, err
	}

	switch mode {
	case "probe":
		return app.probe(ctx)
	case "search":
		return app.search(ctx, argString(input.Args, "scene_id"))
	case "vote":
		return app.vote(ctx, voteArgs{
			TrackID: argString(input.Args, "track_id"),
			Value:   argString(input.Args, "value"),
			Reason:  argString(input.Args, "reason"),
			Note:    argString(input.Args, "note"),
		})
	case "badge":
		return app.badge(ctx, argStrings(input.Args, "scene_ids"))
	case "push":
		return app.push(ctx, argString(input.Args, "scene_id"), argBool(input.Args, "dry_run"))
	case "push_status":
		return app.pushStatus(ctx, argString(input.Args, "scene_id"))
	case "push_all":
		return app.pushAll(ctx, argBool(input.Args, "dry_run"))
	default:
		return app.download(ctx, downloadArgs{
			SceneID:    argString(input.Args, "scene_id"),
			TrackID:    argString(input.Args, "track_id"),
			ForRelease: argInt64(input.Args, "for_release"),
			Overwrite:  argBool(input.Args, "overwrite"),
		})
	}
}

// argString reads a string arg, tolerating JSON numbers (Stash args pass
// through JavaScript, where a scene id may arrive as either).
func argString(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return v
	case float64:
		return json.Number(fmt.Sprintf("%.0f", v)).String()
	default:
		return ""
	}
}

// argBool reads a boolean arg, coercing the way argString does rather than
// asserting a Go bool.
//
// This is load-bearing, not defensive dressing: Stash delivers a task's
// defaultArgs as strings, so the manifest's `dry_run: true` arrives here as
// the string "true", not a bool. A bare type assertion silently yields
// false — which turned "Push subtitles (dry run)" into a real push that
// uploaded the whole library. A wrong answer here is indistinguishable from
// the user not having asked for a dry run at all, so it must not guess.
func argBool(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && b
	case float64:
		return v != 0
	}
	return false
}

// argInt64 reads a numeric arg, coercing the same way argString and
// argBool do — Stash hands a task's defaultArgs over as strings, and a
// bare assertion would silently yield zero.
func argInt64(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// argStrings reads an array arg of string-or-number scene ids.
func argStrings(args map[string]any, key string) []string {
	arr, _ := args[key].([]any)
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		switch s := v.(type) {
		case string:
			out = append(out, s)
		case float64:
			out = append(out, fmt.Sprintf("%.0f", s))
		}
	}
	return out
}
