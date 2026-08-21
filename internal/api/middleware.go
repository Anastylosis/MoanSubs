package api

import (
	"errors"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
	"unicode"
)

// responseLogger wraps an http.ResponseWriter to capture the status code
// and byte count a handler actually wrote, for requestLog's completion
// line. wroteHeader distinguishes "explicitly answered" from "never wrote
// anything" — requestLog's recover only writes a response of its own in
// the latter case, so it never stomps on a handler that already started
// one.
type responseLogger struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (l *responseLogger) WriteHeader(code int) {
	if l.wroteHeader {
		return
	}
	l.wroteHeader = true
	l.status = code
	l.ResponseWriter.WriteHeader(code)
}

func (l *responseLogger) Write(b []byte) (int, error) {
	if !l.wroteHeader {
		l.WriteHeader(http.StatusOK)
	}
	n, err := l.ResponseWriter.Write(b)
	l.bytes += n
	return n, err
}

// flushResponseLogger adds Flush to responseLogger. Kept as a separate type
// rather than always implementing Flush on responseLogger itself: a type
// assertion for http.Flusher must only succeed when the wrapped
// ResponseWriter actually supports it, or the wrapper would silently claim
// a capability (mid-response flushing) it can't deliver.
type flushResponseLogger struct {
	*responseLogger
}

func (l *flushResponseLogger) Flush() {
	l.ResponseWriter.(http.Flusher).Flush()
}

// wrapResponseLogger builds the responseLogger for w, preserving
// http.Flusher when w implements it.
func wrapResponseLogger(w http.ResponseWriter) (http.ResponseWriter, *responseLogger) {
	rl := &responseLogger{ResponseWriter: w, status: http.StatusOK}
	if _, ok := w.(http.Flusher); ok {
		return &flushResponseLogger{rl}, rl
	}
	return rl, rl
}

// routeLabel is requestLog's route field: r.Pattern with its "METHOD "
// prefix stripped (the method is already its own field on the log line) so
// e.g. /release/{id} aggregates one log shape instead of one line per
// release id. r.Pattern is only set once the mux actually dispatches to a
// registered pattern, so a request that matched nothing at all (wrong
// method, no fallback route) falls back to the literal path — still never
// the query string, same reasoning as the analytics tag's own
// data-exclude-search (/search?q= is private).
func routeLabel(r *http.Request) string {
	if r.Pattern == "" {
		return r.URL.Path
	}
	if i := strings.IndexByte(r.Pattern, ' '); i >= 0 {
		return r.Pattern[i+1:]
	}
	return r.Pattern
}

// maxLoggedUA caps the User-Agent slice requestLog logs — long enough to
// identify a browser or bot, short enough that a hostile client can't
// inflate the log by sending a giant header.
const maxLoggedUA = 80

// sanitizeUA strips control characters — the same class that would let a
// crafted User-Agent inject fake newlines/log lines — and caps the result
// to maxLoggedUA runes, in one pass.
func sanitizeUA(ua string) string {
	var b strings.Builder
	n := 0
	for _, r := range ua {
		if n >= maxLoggedUA {
			break
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// requestLog wraps next with one completion-line log per request and panic
// recovery, so a panicking handler answers 500 instead of taking the
// process down — previously the only logging in this package was ~100
// scattered log.Printf("api: ...: %v") error sites, with no per-request
// line and no recover() anywhere. NewMux wraps the whole mux (including
// baseHeaders) in this, so the ResponseWriter it wraps is the one every
// handler actually writes to and the log line sees the real final status.
//
// /healthz is skipped: Docker polls it every 30s and a line for each poll
// would just be noise (panic recovery still applies to it).
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw, rl := wrapResponseLogger(w)

		defer func() {
			rec := recover()
			// recover() returns any, not error — type-assert before
			// errors.Is rather than comparing rec directly (errorlint),
			// since an arbitrary panic value need not implement error at
			// all.
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				// The handler wants the connection silently torn down
				// (e.g. after hijacking it) — net/http's own top-level
				// recover does this quietly, and so must this one: no
				// log line, no response written, just let it propagate.
				panic(rec)
			}
			if rec != nil {
				log.Printf("api: panic: %v\n%s", rec, debug.Stack())
				if !rl.wroteHeader {
					http.Error(lw, "internal error", http.StatusInternalServerError)
				}
			}
			if r.URL.Path == "/healthz" {
				return
			}
			log.Printf("req %s %s %d %dB %s ip=%s ua=%s",
				r.Method, routeLabel(r), rl.status, rl.bytes, time.Since(start),
				s.clientIP(r), sanitizeUA(r.Header.Get("User-Agent")))
		}()

		next.ServeHTTP(lw, r)
	})
}
