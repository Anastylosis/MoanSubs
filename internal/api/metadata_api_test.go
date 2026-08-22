package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// postMetadata contributes name metadata the way the plugin will.
func postMetadata(t *testing.T, ts *httptest.Server, token string, body map[string]any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/metadata", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/metadata: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// The gap this endpoint exists for: a curated library naming a scene the
// node knows only as a filename, with no subtitle to give for it.
func TestContributeMetadata_NamesAReleaseWithoutAnUpload(t *testing.T) {
	ts, st, token := newTestServer(t)
	ctx := context.Background()

	// A release the node already holds, known only by its filename.
	uploaded := uploadWithToken(t, ts, token, map[string]any{
		"oshash": "a1a1a1a1a1a1a1a1", "stem": "123eqawfdhsgaweroqr3raef",
	})
	before, err := st.GetReleaseByID(ctx, uploaded.ReleaseID)
	if err != nil {
		t.Fatalf("GetReleaseByID: %v", err)
	}
	if before.Title != nil {
		t.Fatalf("precondition: title = %q, want none", *before.Title)
	}

	resp := postMetadata(t, ts, token, map[string]any{"entries": []map[string]any{{
		"oshash": "a1a1a1a1a1a1a1a1",
		"title":  "La Hermana De Mi Amigo",
		"studio": "Real Studio", "date": "2024-03-01",
		"performers": []string{"Alice", "Bob"},
	}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("contribute = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[metadataResponse](t, resp)
	if len(got.Results) != 1 || !got.Results[0].Known || !got.Results[0].Recorded {
		t.Fatalf("results = %+v, want one known, recorded entry", got.Results)
	}

	after, err := st.GetReleaseByID(ctx, uploaded.ReleaseID)
	if err != nil {
		t.Fatalf("GetReleaseByID after: %v", err)
	}
	if after.Title == nil || *after.Title != "La Hermana De Mi Amigo" {
		t.Errorf("title = %v, want the contributed name derived onto the release", after.Title)
	}
	if after.Studio == nil || *after.Studio != "Real Studio" {
		t.Errorf("studio = %v", after.Studio)
	}
	if len(after.Performers) != 2 {
		t.Errorf("performers = %v, want both", after.Performers)
	}
}

// A library sweep will name scenes this node has never heard of. That is
// an answer, not an error, and it must not fail the entries around it.
func TestContributeMetadata_UnknownReleaseIsAnAnswerNotAFailure(t *testing.T) {
	ts, _, token := newTestServer(t)

	uploadWithToken(t, ts, token, map[string]any{"oshash": "b2b2b2b2b2b2b2b2", "stem": "known.file"})

	resp := postMetadata(t, ts, token, map[string]any{"entries": []map[string]any{
		{"oshash": "cccccccccccccccc", "title": "Never Heard Of It"},
		{"oshash": "b2b2b2b2b2b2b2b2", "title": "A Known Scene"},
	}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("contribute = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[metadataResponse](t, resp)
	if len(got.Results) != 2 {
		t.Fatalf("results = %+v, want two", got.Results)
	}
	if got.Results[0].Known {
		t.Errorf("unknown oshash reported as known: %+v", got.Results[0])
	}
	if got.Results[0].Error != "" {
		t.Errorf("unknown oshash reported an error %q, want a plain not-known", got.Results[0].Error)
	}
	if !got.Results[1].Known || !got.Results[1].Recorded {
		t.Errorf("the known entry beside it did not land: %+v", got.Results[1])
	}
}

// Never create. A metadata-only insert would fill the catalogue with
// subtitle-less rows and hand a spammer a release factory.
func TestContributeMetadata_DoesNotCreateReleases(t *testing.T) {
	ts, st, token := newTestServer(t)

	resp := postMetadata(t, ts, token, map[string]any{"entries": []map[string]any{
		{"oshash": "dddddddddddddddd", "title": "Should Not Exist"},
	}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("contribute = %d, want 200", resp.StatusCode)
	}
	oh := mustOSHash(t, "dddddddddddddddd")
	if _, err := st.GetReleaseByOshash(context.Background(), oh); err == nil {
		t.Error("contributing metadata created a release; it must only ever name one that exists")
	}
}

// Anonymous contribution would let one script manufacture both unlimited
// agreement and stash-box provenance -- the two signals derivation ranks
// by. Attribution is the gate.
func TestContributeMetadata_RequiresAnAccount(t *testing.T) {
	ts, _, token := newTestServer(t)
	uploadWithToken(t, ts, token, map[string]any{"oshash": "e6e6e6e6e6e6e6e6", "stem": "some.file"})

	resp := postMetadata(t, ts, "", map[string]any{"entries": []map[string]any{
		{"oshash": "e6e6e6e6e6e6e6e6", "title": "Anonymous Claim"},
	}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous contribute = %d, want 401", resp.StatusCode)
	}
}

// Same account, same release: revision, not a second voice.
func TestContributeMetadata_ResubmitRevisesRatherThanStacks(t *testing.T) {
	ts, st, token := newTestServer(t)
	ctx := context.Background()

	up := uploadWithToken(t, ts, token, map[string]any{"oshash": "f7f7f7f7f7f7f7f7", "stem": "revisable"})
	for _, title := range []string{"First Guess", "Better Guess"} {
		resp := postMetadata(t, ts, token, map[string]any{"entries": []map[string]any{
			{"release_id": up.ReleaseID, "title": title},
		}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("contribute %q = %d, want 200", title, resp.StatusCode)
		}
	}

	props, err := st.ProposalsFor(ctx, []int64{up.ReleaseID})
	if err != nil {
		t.Fatalf("ProposalsFor: %v", err)
	}
	// One from the upload's own bundle is impossible here (the upload sent
	// only a stem, which is not a proposal field), so the account has
	// exactly one row however many times it contributed.
	if len(props) != 1 {
		t.Fatalf("proposals = %d, want exactly one row for one account", len(props))
	}
	if props[0].Title == nil || *props[0].Title != "Better Guess" {
		t.Errorf("title = %v, want the revision", props[0].Title)
	}
}

// A withdrawn release is refused per entry, the same 410-shaped answer the
// upload path gives, without failing the batch.
func TestContributeMetadata_WithdrawnReleaseRefused(t *testing.T) {
	ts, st, token := newTestServer(t)
	ctx := context.Background()

	up := uploadWithToken(t, ts, token, map[string]any{"oshash": "a8a8a8a8a8a8a8a8", "stem": "doomed"})
	if err := st.WithdrawRelease(ctx, up.ReleaseID, "test"); err != nil {
		t.Fatalf("WithdrawRelease: %v", err)
	}

	resp := postMetadata(t, ts, token, map[string]any{"entries": []map[string]any{
		{"release_id": up.ReleaseID, "title": "Too Late"},
	}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("contribute = %d, want 200 with a per-entry refusal", resp.StatusCode)
	}
	got := decodeJSON[metadataResponse](t, resp)
	if len(got.Results) != 1 || got.Results[0].Error != "release withdrawn" {
		t.Errorf("results = %+v, want a withdrawn refusal", got.Results)
	}
	if got.Results[0].Recorded {
		t.Error("a withdrawn release recorded a proposal")
	}
}

// The batch cap is a refusal, not a silent truncation.
func TestContributeMetadata_RejectsOversizedBatch(t *testing.T) {
	ts, _, token := newTestServer(t)

	entries := make([]map[string]any, maxMetadataEntries+1)
	for i := range entries {
		entries[i] = map[string]any{"release_id": 1, "title": "x"}
	}
	resp := postMetadata(t, ts, token, map[string]any{"entries": entries})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized batch = %d, want 400", resp.StatusCode)
	}
}

// The feature list is how a plugin learns this node can be told things
// without being sent a file.
func TestVersion_AdvertisesMetadataFeature(t *testing.T) {
	found := false
	for _, f := range features {
		if f == "metadata" {
			found = true
		}
	}
	if !found {
		t.Errorf("features = %v, want it to advertise \"metadata\"", features)
	}
}

// uploadWithToken puts a release behind the endpoint under test.
func uploadWithToken(t *testing.T, ts *httptest.Server, token string, extra map[string]any) uploadResponse {
	t.Helper()
	body := map[string]any{"duration_ms": 60000, "lang": "en", "body": basicSRT}
	for k, v := range extra {
		body[k] = v
	}
	resp := doUpload(t, ts, token, body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %d, want 201", resp.StatusCode)
	}
	return decodeJSON[uploadResponse](t, resp)
}
