package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const sessionCookieName = "cbox_session"

type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	UIBaseURL    string
	SessionTTL   time.Duration
	StateTTL     time.Duration
}

type session struct {
	ID        string
	Token     *oauth2.Token
	ExpiresAt time.Time
}

type stateEntry struct {
	ExpiresAt time.Time
}

type store struct {
	mu       sync.Mutex
	sessions map[string]session
	states   map[string]stateEntry
}

type Service struct {
	oauthConfig *oauth2.Config
	store       *store
	sessionTTL  time.Duration
	stateTTL    time.Duration
	uiBaseURL   string
}

func NewService(cfg Config) *Service {
	sessionTTL := cfg.SessionTTL
	if sessionTTL <= 0 {
		sessionTTL = 30 * time.Minute
	}
	stateTTL := cfg.StateTTL
	if stateTTL <= 0 {
		stateTTL = 10 * time.Minute
	}

	s := &Service{
		oauthConfig: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Scopes: []string{
				"https://www.googleapis.com/auth/gmail.readonly",
				"https://www.googleapis.com/auth/gmail.modify",
			},
			Endpoint: google.Endpoint,
		},
		store: &store{
			sessions: make(map[string]session),
			states:   make(map[string]stateEntry),
		},
		sessionTTL: sessionTTL,
		stateTTL:   stateTTL,
		uiBaseURL:  cfg.UIBaseURL,
	}

	go s.cleanupLoop()

	return s
}

func (s *Service) HandleGoogleAuth(w http.ResponseWriter, r *http.Request) {
	state, err := generateToken(32)
	if err != nil {
		http.Error(w, "unable to start oauth", http.StatusInternalServerError)
		return
	}

	s.saveState(state)
	url := s.oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusFound)
}

func (s *Service) HandleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if state == "" || code == "" {
		http.Error(w, "missing oauth params", http.StatusBadRequest)
		return
	}
	if !s.consumeState(state) {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	token, err := s.oauthConfig.Exchange(context.Background(), code)
	if err != nil {
		http.Error(w, "oauth exchange failed", http.StatusBadRequest)
		return
	}

	sessionID, err := generateToken(32)
	if err != nil {
		http.Error(w, "unable to create session", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	s.saveSession(session{
		ID:        sessionID,
		Token:     token,
		ExpiresAt: now.Add(s.sessionTTL),
	})

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  now.Add(s.sessionTTL),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.uiBaseURL, "https://"),
	})

	http.Redirect(w, r, s.uiBaseURL+"/", http.StatusFound)
}

func (s *Service) IsLoggedIn(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie == nil {
		return false
	}
	_, ok := s.getSession(cookie.Value)
	return ok
}

func (s *Service) SessionIDFromRequest(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie == nil {
		return "", false
	}
	if _, ok := s.getSession(cookie.Value); !ok {
		return "", false
	}
	return cookie.Value, true
}

func (s *Service) TokenBySessionID(sessionID string) (*oauth2.Token, bool) {
	sess, ok := s.getSession(sessionID)
	if !ok {
		return nil, false
	}
	return sess.Token, true
}

func (s *Service) cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		s.store.mu.Lock()
		for id, sess := range s.store.sessions {
			if now.After(sess.ExpiresAt) {
				delete(s.store.sessions, id)
			}
		}
		for key, state := range s.store.states {
			if now.After(state.ExpiresAt) {
				delete(s.store.states, key)
			}
		}
		s.store.mu.Unlock()
	}
}

func (s *Service) saveState(state string) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.states[state] = stateEntry{ExpiresAt: time.Now().Add(s.stateTTL)}
}

func (s *Service) consumeState(state string) bool {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	entry, ok := s.store.states[state]
	if !ok {
		return false
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(s.store.states, state)
		return false
	}
	delete(s.store.states, state)
	return true
}

func (s *Service) saveSession(sess session) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.sessions[sess.ID] = sess
}

func (s *Service) getSession(id string) (session, bool) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	sess, ok := s.store.sessions[id]
	if !ok {
		return session{}, false
	}
	if time.Now().After(sess.ExpiresAt) {
		delete(s.store.sessions, id)
		return session{}, false
	}
	return sess, true
}

func generateToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
