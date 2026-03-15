package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

const sessionCookieName = "cbox_session"

type Config struct {
	ClientID              string
	ClientSecret          string
	RedirectURL           string
	MicrosoftClientID     string
	MicrosoftClientSecret string
	MicrosoftRedirectURL  string
	MicrosoftTenant       string
	UIBaseURL             string
	SessionTTL            time.Duration
	StateTTL              time.Duration
}

type session struct {
	ID        string
	Provider  string
	Email     string
	Token     *oauth2.Token
	ExpiresAt time.Time
}

type stateEntry struct {
	ExpiresAt time.Time
	Provider  string
}

type store struct {
	mu       sync.Mutex
	sessions map[string]session
	states   map[string]stateEntry
}

type Service struct {
	googleOAuthConfig    *oauth2.Config
	microsoftOAuthConfig *oauth2.Config
	store                *store
	sessionTTL           time.Duration
	stateTTL             time.Duration
	uiBaseURL            string
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
		googleOAuthConfig: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Scopes: []string{
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

	microsoftTenant := strings.TrimSpace(cfg.MicrosoftTenant)
	if microsoftTenant == "" {
		microsoftTenant = "common"
	}
	if strings.TrimSpace(cfg.MicrosoftClientID) != "" && strings.TrimSpace(cfg.MicrosoftClientSecret) != "" && strings.TrimSpace(cfg.MicrosoftRedirectURL) != "" {
		s.microsoftOAuthConfig = &oauth2.Config{
			ClientID:     cfg.MicrosoftClientID,
			ClientSecret: cfg.MicrosoftClientSecret,
			RedirectURL:  cfg.MicrosoftRedirectURL,
			Scopes: []string{
				"openid",
				"profile",
				"email",
				"offline_access",
				"User.Read",
				"Mail.Read",
				"Mail.ReadWrite",
			},
			Endpoint: oauth2.Endpoint{
				AuthURL:  fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize", microsoftTenant),
				TokenURL: fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", microsoftTenant),
			},
		}
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

	s.saveState(state, "google")
	url := s.googleOAuthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusFound)
}

func (s *Service) HandleMicrosoftAuth(w http.ResponseWriter, r *http.Request) {
	if s.microsoftOAuthConfig == nil {
		http.Error(w, "microsoft oauth is not configured", http.StatusNotImplemented)
		return
	}

	state, err := generateToken(32)
	if err != nil {
		http.Error(w, "unable to start oauth", http.StatusInternalServerError)
		return
	}

	s.saveState(state, "outlook")
	url := s.microsoftOAuthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusFound)
}

func (s *Service) HandleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if state == "" || code == "" {
		http.Error(w, "missing oauth params", http.StatusBadRequest)
		return
	}
	provider, ok := s.consumeState(state)
	if !ok || provider != "google" {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	token, err := s.googleOAuthConfig.Exchange(context.Background(), code)
	if err != nil {
		http.Error(w, "oauth exchange failed", http.StatusBadRequest)
		return
	}
	email := s.fetchEmail(context.Background(), "google", token)

	s.completeSession(w, r, "google", email, token)
}

func (s *Service) HandleMicrosoftCallback(w http.ResponseWriter, r *http.Request) {
	if s.microsoftOAuthConfig == nil {
		http.Error(w, "microsoft oauth is not configured", http.StatusNotImplemented)
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if state == "" || code == "" {
		http.Error(w, "missing oauth params", http.StatusBadRequest)
		return
	}
	provider, ok := s.consumeState(state)
	if !ok || provider != "outlook" {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	token, err := s.microsoftOAuthConfig.Exchange(context.Background(), code)
	if err != nil {
		http.Error(w, "oauth exchange failed", http.StatusBadRequest)
		return
	}
	email := s.fetchEmail(context.Background(), "outlook", token)

	s.completeSession(w, r, "outlook", email, token)
}

func (s *Service) completeSession(w http.ResponseWriter, r *http.Request, providerName, email string, token *oauth2.Token) {

	sessionID, err := generateToken(32)
	if err != nil {
		http.Error(w, "unable to create session", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	s.saveSession(session{
		ID:        sessionID,
		Provider:  providerName,
		Email:     email,
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

func (s *Service) EmailFromRequest(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie == nil {
		return "", false
	}
	sess, ok := s.getSession(cookie.Value)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(sess.Email) == "" {
		return "", false
	}
	return sess.Email, true
}

func (s *Service) EmailBySessionID(sessionID string) (string, bool) {
	if strings.TrimSpace(sessionID) == "" {
		return "", false
	}
	sess, ok := s.getSession(sessionID)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(sess.Email) == "" {
		return "", false
	}
	return sess.Email, true
}

func (s *Service) ProviderBySessionID(sessionID string) (string, bool) {
	if strings.TrimSpace(sessionID) == "" {
		return "", false
	}
	sess, ok := s.getSession(sessionID)
	if !ok {
		return "", false
	}
	providerName := strings.ToLower(strings.TrimSpace(sess.Provider))
	if providerName == "" {
		providerName = "google"
	}
	return providerName, true
}

func (s *Service) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie != nil {
		s.deleteSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.uiBaseURL, "https://"),
	})

	http.Redirect(w, r, s.uiBaseURL+"/", http.StatusFound)
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

func (s *Service) saveState(state, provider string) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.states[state] = stateEntry{ExpiresAt: time.Now().Add(s.stateTTL), Provider: strings.TrimSpace(provider)}
}

func (s *Service) consumeState(state string) (string, bool) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	entry, ok := s.store.states[state]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(s.store.states, state)
		return "", false
	}
	delete(s.store.states, state)
	return strings.TrimSpace(entry.Provider), true
}

func (s *Service) saveSession(sess session) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.sessions[sess.ID] = sess
}

func (s *Service) deleteSession(id string) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	delete(s.store.sessions, id)
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

func (s *Service) fetchEmail(ctx context.Context, providerName string, token *oauth2.Token) string {
	if strings.EqualFold(providerName, "outlook") {
		return s.fetchOutlookEmail(ctx, token)
	}
	return s.fetchGoogleEmail(ctx, token)
}

func (s *Service) fetchGoogleEmail(ctx context.Context, token *oauth2.Token) string {
	if token == nil {
		return ""
	}
	gmailSvc, err := gmail.NewService(ctx, option.WithTokenSource(oauth2.StaticTokenSource(token)))
	if err != nil {
		return ""
	}
	profile, err := gmailSvc.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(profile.EmailAddress)
}

func (s *Service) fetchOutlookEmail(ctx context.Context, token *oauth2.Token) string {
	if token == nil {
		return ""
	}

	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))
	client.Timeout = 15 * time.Second
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me?$select=mail,userPrincipalName", nil)
	if err != nil {
		return ""
	}

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	var me struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return ""
	}
	if strings.TrimSpace(me.Mail) != "" {
		return strings.TrimSpace(me.Mail)
	}
	return strings.TrimSpace(me.UserPrincipalName)
}
