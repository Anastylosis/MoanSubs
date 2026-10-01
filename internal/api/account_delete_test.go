package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

func postDeleteAccount(t *testing.T, client *http.Client, ts *httptest.Server, password, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/me/delete",
		strings.NewReader(url.Values{"password": {password}}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /me/delete: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestDeleteAccount_FormRequiresLogin(t *testing.T) {
	ts, _, client, _ := sessionServer(t)

	resp, err := client.Get(ts.URL + "/me/delete")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `action="/me/delete"`) {
		t.Errorf("GET /me/delete logged in = %d, want 200 with the confirmation form", resp.StatusCode)
	}

	anon := jarClient(t)
	resp, err = anon.Get(ts.URL + "/me/delete")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET /me/delete anonymous = %d, want 303 to /login", resp.StatusCode)
	}
}

func TestDeleteAccount_WrongPasswordRefused(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	if resp := postDeleteAccount(t, client, ts, "not-the-password", ts.URL); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /me/delete wrong password = %d, want 400", resp.StatusCode)
	}
	if _, err := st.GetAccountByName(context.Background(), "webuser"); err != nil {
		t.Errorf("account gone after a wrong-password delete: %v", err)
	}
}

func TestDeleteAccount_CrossOriginRefused(t *testing.T) {
	ts, st, client, _ := sessionServer(t)

	if resp := postDeleteAccount(t, client, ts, testAccountPassword, "https://evil.example"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST /me/delete = %d, want 403", resp.StatusCode)
	}
	if _, err := st.GetAccountByName(context.Background(), "webuser"); err != nil {
		t.Errorf("account gone after a cross-origin delete: %v", err)
	}
}

func TestDeleteAccount_DeletesAndKeepsTracksAnonymised(t *testing.T) {
	ts, st, client, token := sessionServer(t)

	up := doUpload(t, ts, token, map[string]any{
		"oshash": "dededededededede", "duration_ms": 12000, "lang": "en", "body": basicSRT,
		"authorship": "credited",
	})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %d, want 201", up.StatusCode)
	}
	created := decodeJSON[uploadResponse](t, up)

	resp := postDeleteAccount(t, client, ts, testAccountPassword, ts.URL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /me/delete = %d, want 200", resp.StatusCode)
	}

	if _, err := st.GetAccountByName(context.Background(), "webuser"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetAccountByName after deletion = %v, want ErrNotFound", err)
	}
	if r, err := client.Get(ts.URL + "/me"); err != nil {
		t.Fatal(err)
	} else {
		_ = r.Body.Close()
		if r.StatusCode != http.StatusSeeOther {
			t.Errorf("GET /me after deletion = %d, want 303 (session gone)", r.StatusCode)
		}
	}
	if r := doLogin(t, jarClient(t), ts, "webuser", testAccountPassword); r.StatusCode == http.StatusSeeOther {
		t.Error("deleted account can still log in")
	}
	if r := doUpload(t, ts, token, map[string]any{
		"oshash": "dededededededed0", "duration_ms": 12000, "lang": "en", "body": basicSRT,
	}); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("upload with the deleted account's token = %d, want 401", r.StatusCode)
	}

	getResp, err := http.Get(ts.URL + "/api/v1/subtitles/" + strconv.FormatInt(created.TrackID, 10))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET track after uploader deletion = %d, want 200", getResp.StatusCode)
	}
	if got := decodeJSON[getSubtitleResponse](t, getResp); got.CreditedTo != "" {
		t.Errorf("credited_to = %q after deletion, want empty", got.CreditedTo)
	}

	if r, err := http.Get(ts.URL + "/u/webuser"); err != nil {
		t.Fatal(err)
	} else {
		_ = r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("GET /u/webuser after deletion = %d, want 404", r.StatusCode)
		}
	}
}

func TestDeleteAccount_LastAdminRefused(t *testing.T) {
	ts, st, client, _ := sessionServer(t)
	if err := st.SetAccountRole(context.Background(), "webuser", "admin"); err != nil {
		t.Fatal(err)
	}

	if resp := postDeleteAccount(t, client, ts, testAccountPassword, ts.URL); resp.StatusCode != http.StatusConflict {
		t.Errorf("POST /me/delete as the last admin = %d, want 409", resp.StatusCode)
	}
	if _, err := st.GetAccountByName(context.Background(), "webuser"); err != nil {
		t.Errorf("last admin deleted anyway: %v", err)
	}
}
