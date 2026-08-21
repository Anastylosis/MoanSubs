package api

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// requestLog is a pure function of a Server and the handler it wraps (like
// clientIP in ratelimit_test.go and secureCookie in session_test.go), so
// these tests need no DATABASE_URL / DB-backed store.

// captureLog redirects the standard logger to a buffer for the test's
// duration and restores it on cleanup. requestLog logs through log.Printf
// like every other log site in this package, so this is the only way to
// assert on what it wrote.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

func TestRequestLog_CapturesStatusAndBytesOn200(t *testing.T) {
	buf := captureLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	})
	h := (&Server{}).requestLog(mux)

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("recorder status = %d, want 200", rec.Code)
	}
	if line := buf.String(); !strings.Contains(line, "req GET /ok 200 5B ") {
		t.Errorf("log line = %q, want it to report status 200 and 5B", line)
	}
}

func TestRequestLog_CapturesStatusOn404AndFallsBackToPath(t *testing.T) {
	buf := captureLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) {})
	h := (&Server{}).requestLog(mux)

	// /nope matches no registered pattern at all (no catch-all "/" here),
	// so the mux's default NotFoundHandler runs with r.Pattern left unset —
	// routeLabel must fall back to the literal path for this case.
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("recorder status = %d, want 404", rec.Code)
	}
	if line := buf.String(); !strings.Contains(line, "req GET /nope 404 ") {
		t.Errorf("log line = %q, want the unrouted path and status 404", line)
	}
}

func TestRequestLog_OmitsQueryString(t *testing.T) {
	buf := captureLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, _ *http.Request) {})
	h := (&Server{}).requestLog(mux)

	req := httptest.NewRequest(http.MethodGet, "/search?q=a+private+query", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	line := buf.String()
	if strings.Contains(line, "q=") || strings.Contains(line, "private") {
		t.Errorf("log line leaked the query string: %q", line)
	}
	if !strings.Contains(line, "req GET /search ") {
		t.Errorf("log line = %q, want the bare path /search with no query string", line)
	}
}

func TestRequestLog_PanicRecoversTo500AndSurvives(t *testing.T) {
	buf := captureLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, _ *http.Request) {
		panic("kaboom")
	})
	h := (&Server{}).requestLog(mux)

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()

	// A panic escaping h.ServeHTTP would itself panic this test (proof the
	// process wouldn't survive); recover here only to turn that into a
	// clear failure rather than a crashed test binary.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped requestLog: %v", r)
			}
		}()
		h.ServeHTTP(rec, req)
	}()

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("recorder status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "internal error") {
		t.Errorf("response body = %q, want it to contain \"internal error\"", body)
	}
	line := buf.String()
	if !strings.Contains(line, "panic: kaboom") {
		t.Errorf("log = %q, want a panic line naming the recovered value", line)
	}
	if !strings.Contains(line, "req GET /boom 500 ") {
		t.Errorf("log = %q, want the completion line to report status 500", line)
	}
}

func TestRequestLog_DoesNotOverwriteAHandlerThatAlreadyWrote(t *testing.T) {
	buf := captureLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /partial", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("started"))
		panic("boom after writing")
	})
	h := (&Server{}).requestLog(mux)

	req := httptest.NewRequest(http.MethodGet, "/partial", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The handler already committed a status before panicking, so recover
	// must not attempt to answer 500 on top of it.
	if rec.Code != http.StatusAccepted {
		t.Errorf("recorder status = %d, want 202 (already written, not overwritten)", rec.Code)
	}
	if line := buf.String(); !strings.Contains(line, "req GET /partial 202 ") {
		t.Errorf("log = %q, want the completion line to keep the already-written 202", line)
	}
}

func TestRequestLog_RepanicsOnAbortHandler(t *testing.T) {
	captureLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /abort", func(w http.ResponseWriter, _ *http.Request) {
		panic(http.ErrAbortHandler)
	})
	h := (&Server{}).requestLog(mux)

	req := httptest.NewRequest(http.MethodGet, "/abort", nil)
	rec := httptest.NewRecorder()

	defer func() {
		r := recover()
		if err, ok := r.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recover() = %v, want http.ErrAbortHandler to propagate out", r)
		}
	}()
	h.ServeHTTP(rec, req)
	t.Fatal("ServeHTTP returned normally, want it to panic with http.ErrAbortHandler")
}

func TestRequestLog_SkipsHealthz(t *testing.T) {
	buf := captureLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := (&Server{}).requestLog(mux)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if buf.Len() != 0 {
		t.Errorf("log = %q, want no line at all for /healthz", buf.String())
	}
}

// -- routeLabel / sanitizeUA (pure helpers) ----------------------------

func TestRouteLabel_FallsBackToPathWhenUnrouted(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/whatever/nothing/matched", nil)
	if got := routeLabel(r); got != "/whatever/nothing/matched" {
		t.Errorf("routeLabel = %q, want the literal path", got)
	}
}

func TestRouteLabel_StripsMethodFromPattern(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/release/42", nil)
	r.Pattern = "GET /release/{id}"
	if got := routeLabel(r); got != "/release/{id}" {
		t.Errorf("routeLabel = %q, want /release/{id} (id aggregated, not one line per release)", got)
	}
}

func TestSanitizeUA_StripsControlCharsAndTruncates(t *testing.T) {
	ua := "curl/8.0\x00\x1b[31m" + strings.Repeat("x", maxLoggedUA+50)
	got := sanitizeUA(ua)
	if strings.ContainsAny(got, "\x00\x1b") {
		t.Errorf("sanitizeUA left a control character in %q", got)
	}
	if len(got) != maxLoggedUA {
		t.Errorf("len(sanitizeUA(...)) = %d, want %d", len(got), maxLoggedUA)
	}
}
