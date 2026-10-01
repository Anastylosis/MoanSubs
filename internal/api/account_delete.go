package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

type meDeleteData struct {
	Title       string
	Name        string
	UploadCount int
	Error       string
	Deleted     bool
}

func (s *Server) handleDeleteAccountForm(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticateWeb(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.renderDeleteAccount(w, r, ares, http.StatusOK, "")
}

func (s *Server) renderDeleteAccount(w http.ResponseWriter, r *http.Request, ares *authResult, status int, msg string) {
	tracks, err := s.Store.TracksByAccount(r.Context(), ares.Account.ID)
	if err != nil {
		log.Printf("api: TracksByAccount: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, withAuth(r, ares), status, "me_delete.html", meDeleteData{
		Title: "Delete account", Name: ares.Account.Name, UploadCount: len(tracks), Error: msg,
	}, true)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	ares, err := authenticateWeb(r.Context(), s.Store, r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	capFormBody(w, r)
	if err := r.ParseForm(); err != nil {
		s.renderDeleteAccount(w, r, ares, http.StatusBadRequest, "could not read the submitted form")
		return
	}

	if _, err := s.Store.VerifyAccountPassword(r.Context(), ares.Account.Name, r.PostFormValue("password")); err != nil {
		s.renderDeleteAccount(w, r, ares, http.StatusBadRequest, "password is incorrect")
		return
	}

	if err := s.Store.DeleteAccount(r.Context(), ares.Account.ID); err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			s.renderDeleteAccount(w, r, ares, http.StatusConflict,
				"this is the only admin account; make another account admin first")
			return
		}
		log.Printf("api: DeleteAccount: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
	s.renderPage(w, r, http.StatusOK, "me_delete.html", meDeleteData{Title: "Account deleted", Deleted: true}, true)
}
