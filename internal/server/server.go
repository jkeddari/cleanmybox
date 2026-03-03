package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jkeddari/cleanmybox/internal/auth"
	"github.com/jkeddari/cleanmybox/internal/cleaner"
	"github.com/jkeddari/cleanmybox/internal/stripe"
	"github.com/jkeddari/cleanmybox/internal/web"
)

type Config struct {
	Port      string
	UIBaseURL string
}

type Server struct {
	config    Config
	authSvc   *auth.Service
	stripeSvc *stripe.Service
	cleaner   *cleaner.Service
}

func New(cfg Config, authSvc *auth.Service, stripeSvc *stripe.Service, cleanerSvc *cleaner.Service) *Server {
	return &Server{
		config:    cfg,
		authSvc:   authSvc,
		stripeSvc: stripeSvc,
		cleaner:   cleanerSvc,
	}
}

func (s *Server) Start() error {
	handler := withCORS(s.config.UIBaseURL, s.routes())
	log.Printf("cleanmybox backend listening on :%s", s.config.Port)
	if err := http.ListenAndServe(":"+s.config.Port, handler); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	ui := web.New(s.authSvc, s.cleaner)

	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Clean("assets")))))
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Clean("assets/robots.txt"))
	})
	mux.HandleFunc("GET /sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Clean("assets/sitemap.xml"))
	})
	mux.HandleFunc("GET /", ui.HomePage)
	mux.HandleFunc("GET /login", ui.LoginPage)
	mux.HandleFunc("GET /cleanup", ui.CleanupPage)
	mux.HandleFunc("GET /ui/job-fragment", ui.JobFragment)

	mux.HandleFunc("GET /auth/google", s.authSvc.HandleGoogleAuth)
	mux.HandleFunc("GET /auth/google/callback", s.authSvc.HandleGoogleCallback)

	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"loggedIn": s.authSvc.IsLoggedIn(r)})
	})

	mux.HandleFunc("POST /api/checkout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.authSvc.IsLoggedIn(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sessionRef, ok := s.authSvc.SessionIDFromRequest(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var payload struct {
			Plan string `json:"plan"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		url, err := s.stripeSvc.CreateCheckout(payload.Plan, sessionRef)
		if err != nil {
			if errors.Is(err, stripe.ErrInvalidPlan) {
				http.Error(w, "invalid plan", http.StatusBadRequest)
				return
			}
			log.Printf("stripe session error: %v", err)
			http.Error(w, "payment error", http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"url": url})
	})

	mux.HandleFunc("POST /webhook/stripe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		event, statusCode, err := s.stripeSvc.ParseWebhook(r)
		if err != nil {
			log.Printf("stripe webhook error: %v", err)
			http.Error(w, http.StatusText(statusCode), statusCode)
			return
		}
		if event.Duplicate {
			log.Printf("stripe webhook duplicate ignored: type=%s id=%s session=%s", event.Type, event.ID, event.SessionID)
			w.WriteHeader(http.StatusOK)
			return
		}

		log.Printf("stripe webhook received: type=%s id=%s session=%s plan=%s", event.Type, event.ID, event.SessionID, event.Plan)

		switch event.Type {
		case "checkout.session.completed", "checkout.session.async_payment_succeeded":
			if !event.Paid {
				log.Printf("stripe checkout not paid yet, skipping fulfillment launch: session=%s type=%s", event.SessionID, event.Type)
				w.WriteHeader(http.StatusOK)
				return
			}
			if !s.stripeSvc.MarkSessionFulfilled(event.SessionID) {
				log.Printf("stripe checkout already fulfilled, skipping launch: session=%s", event.SessionID)
				w.WriteHeader(http.StatusOK)
				return
			}
			s.cleaner.Launch(event.Plan, event.SessionID, event.SessionRef)
		case "checkout.session.async_payment_failed":
			log.Printf("stripe async payment failed: session=%s plan=%s", event.SessionID, event.Plan)
		case "checkout.session.expired":
			log.Printf("stripe checkout expired: session=%s plan=%s", event.SessionID, event.Plan)
		default:
			log.Printf("stripe webhook ignored event type: type=%s id=%s", event.Type, event.ID)
		}

		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /api/job", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, "missing job id in path", http.StatusBadRequest)
	})

	mux.HandleFunc("GET /api/job/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		checkoutSessionID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/job/"))
		if checkoutSessionID == "" {
			http.Error(w, "missing job id in path", http.StatusBadRequest)
			return
		}
		job, ok := s.cleaner.Job(checkoutSessionID)
		if !ok {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})

	return mux
}

func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func maskValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<empty>"
	}
	if len(value) <= 8 {
		return "****"
	}
	return value[:6] + "..." + value[len(value)-4:]
}

func LogConfig(port, uiBaseURL, redirectURL, successURL, cancelURL, priceClean, priceCleanPlus, clientID, clientSecret, stripeKey, webhookSecret string, allowVersionMismatch, cleanerDryRun bool, openAIModel, openAIKey string) {
	log.Printf("config loaded: PORT=%s UI_BASE_URL=%s", port, uiBaseURL)
	log.Printf("config loaded: GOOGLE_REDIRECT_URL=%s", redirectURL)
	log.Printf("config loaded: STRIPE_SUCCESS_URL=%s STRIPE_CANCEL_URL=%s", successURL, cancelURL)
	log.Printf("config loaded: STRIPE_PRICE_CLEAN=%s STRIPE_PRICE_CLEANPLUS=%s", priceClean, priceCleanPlus)
	log.Printf("config loaded: GOOGLE_CLIENT_ID=%s", maskValue(clientID))
	log.Printf("config loaded: GOOGLE_CLIENT_SECRET=%s", maskValue(clientSecret))
	log.Printf("config loaded: STRIPE_SECRET_KEY=%s", maskValue(stripeKey))
	log.Printf("config loaded: STRIPE_WEBHOOK_SECRET=%s STRIPE_WEBHOOK_ALLOW_VERSION_MISMATCH=%t", maskValue(webhookSecret), allowVersionMismatch)
	log.Printf("config loaded: CLEANER_DRY_RUN=%t", cleanerDryRun)
	log.Printf("config loaded: OPENAI_MODEL=%s OPENAI_API_KEY=%s", openAIModel, maskValue(openAIKey))
}
