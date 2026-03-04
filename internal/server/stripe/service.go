package stripe

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v81"
	stripecheckout "github.com/stripe/stripe-go/v81/checkout/session"
	stripewebhook "github.com/stripe/stripe-go/v81/webhook"
)

var ErrInvalidPlan = errors.New("invalid plan")

type Config struct {
	SecretKey            string
	PriceClean           string
	PriceCleanPlus       string
	SuccessURL           string
	CancelURL            string
	WebhookSecret        string
	AllowVersionMismatch bool
}

type Service struct {
	priceClean           string
	priceCleanPlus       string
	successURL           string
	cancelURL            string
	webhookSecret        string
	allowVersionMismatch bool

	mu                    sync.Mutex
	processedEventIDs     map[string]time.Time
	processedObjectEvents map[string]time.Time
	fulfilledSessionIDs   map[string]time.Time
}

type WebhookEvent struct {
	Type       string
	ID         string
	Plan       string
	SessionID  string
	SessionRef string
	Paid       bool
	Duplicate  bool
}

func NewService(cfg Config) *Service {
	stripe.Key = cfg.SecretKey

	return &Service{
		priceClean:            cfg.PriceClean,
		priceCleanPlus:        cfg.PriceCleanPlus,
		successURL:            cfg.SuccessURL,
		cancelURL:             cfg.CancelURL,
		webhookSecret:         cfg.WebhookSecret,
		allowVersionMismatch:  cfg.AllowVersionMismatch,
		processedEventIDs:     make(map[string]time.Time),
		processedObjectEvents: make(map[string]time.Time),
		fulfilledSessionIDs:   make(map[string]time.Time),
	}
}

func (s *Service) CreateCheckout(plan, sessionRef string) (string, error) {
	plan = strings.ToLower(strings.TrimSpace(plan))

	priceID := ""
	switch plan {
	case "clean":
		priceID = s.priceClean
	case "cleanplus":
		priceID = s.priceCleanPlus
	default:
		return "", ErrInvalidPlan
	}

	params := &stripe.CheckoutSessionParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL:        stripe.String(withCheckoutSessionID(s.successURL)),
		CancelURL:         stripe.String(s.cancelURL),
		ClientReferenceID: stripe.String(sessionRef),
		Metadata: map[string]string{
			"plan": plan,
		},
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceID),
				Quantity: stripe.Int64(1),
			},
		},
	}
	params.SetIdempotencyKey("checkout:" + sessionRef + ":" + plan)

	checkoutSession, err := stripecheckout.New(params)
	if err != nil {
		return "", err
	}

	return checkoutSession.URL, nil
}

func (s *Service) ParseWebhook(r *http.Request) (WebhookEvent, int, error) {
	if s.webhookSecret == "" {
		return WebhookEvent{}, http.StatusInternalServerError, errors.New("missing webhook secret")
	}

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		return WebhookEvent{}, http.StatusBadRequest, err
	}
	if len(payload) == 0 {
		return WebhookEvent{}, http.StatusBadRequest, errors.New("empty webhook payload")
	}

	s.cleanupProcessedCache()

	construct := func() (stripe.Event, error) {
		if s.allowVersionMismatch {
			return stripewebhook.ConstructEventWithOptions(
				payload,
				r.Header.Get("Stripe-Signature"),
				s.webhookSecret,
				stripewebhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true},
			)
		}
		return stripewebhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), s.webhookSecret)
	}

	event, err := construct()
	if err != nil {
		return WebhookEvent{}, http.StatusBadRequest, err
	}

	if s.markEventDuplicate(event.ID) {
		return WebhookEvent{Type: string(event.Type), ID: event.ID, Duplicate: true}, http.StatusOK, nil
	}

	parsed := WebhookEvent{Type: string(event.Type), ID: event.ID}

	if event.Type == "checkout.session.completed" {
		var checkoutSession stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &checkoutSession); err != nil {
			return WebhookEvent{}, http.StatusBadRequest, err
		}
		parsed.SessionID = checkoutSession.ID
		parsed.Plan = checkoutSession.Metadata["plan"]
		parsed.SessionRef = checkoutSession.ClientReferenceID
		parsed.Paid = checkoutSession.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid
		if parsed.Plan == "" {
			parsed.Plan = "unknown"
		}
		if parsed.SessionRef == "" {
			parsed.SessionRef = checkoutSession.Metadata["session_ref"]
		}
		if parsed.SessionID != "" {
			objKey := parsed.Type + ":" + parsed.SessionID
			if s.markObjectEventDuplicate(objKey) {
				parsed.Duplicate = true
			}
		}
	}

	if event.Type == "checkout.session.async_payment_succeeded" || event.Type == "checkout.session.async_payment_failed" || event.Type == "checkout.session.expired" {
		var checkoutSession stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &checkoutSession); err == nil {
			parsed.SessionID = checkoutSession.ID
			parsed.Plan = checkoutSession.Metadata["plan"]
			parsed.SessionRef = checkoutSession.ClientReferenceID
			parsed.Paid = checkoutSession.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid
			if parsed.SessionRef == "" {
				parsed.SessionRef = checkoutSession.Metadata["session_ref"]
			}
			if parsed.SessionID != "" {
				objKey := parsed.Type + ":" + parsed.SessionID
				if s.markObjectEventDuplicate(objKey) {
					parsed.Duplicate = true
				}
			}
		}
	}

	return parsed, http.StatusOK, nil
}

func (s *Service) MarkSessionFulfilled(sessionID string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.fulfilledSessionIDs[sessionID]; exists {
		return false
	}
	s.fulfilledSessionIDs[sessionID] = time.Now()
	return true
}

func (s *Service) markEventDuplicate(eventID string) bool {
	if strings.TrimSpace(eventID) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.processedEventIDs[eventID]; exists {
		return true
	}
	s.processedEventIDs[eventID] = time.Now()
	return false
}

func (s *Service) markObjectEventDuplicate(key string) bool {
	if strings.TrimSpace(key) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.processedObjectEvents[key]; exists {
		return true
	}
	s.processedObjectEvents[key] = time.Now()
	return false
}

func (s *Service) cleanupProcessedCache() {
	cutoff := time.Now().Add(-24 * time.Hour)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.processedEventIDs {
		if t.Before(cutoff) {
			delete(s.processedEventIDs, k)
		}
	}
	for k, t := range s.processedObjectEvents {
		if t.Before(cutoff) {
			delete(s.processedObjectEvents, k)
		}
	}
	for k, t := range s.fulfilledSessionIDs {
		if t.Before(cutoff) {
			delete(s.fulfilledSessionIDs, k)
		}
	}
}

func withCheckoutSessionID(rawURL string) string {
	if strings.Contains(rawURL, "{CHECKOUT_SESSION_ID}") {
		return rawURL
	}
	if strings.Contains(rawURL, "?") {
		return rawURL + "&session_id={CHECKOUT_SESSION_ID}"
	}
	return rawURL + "?session_id={CHECKOUT_SESSION_ID}"
}
