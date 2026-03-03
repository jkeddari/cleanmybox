package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/jkeddari/cleanmybox/internal/auth"
	"github.com/jkeddari/cleanmybox/internal/cleaner"
	"github.com/jkeddari/cleanmybox/internal/server"
	"github.com/jkeddari/cleanmybox/internal/stripe"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	port := envOr("PORT", "8080")
	uiBaseURL := envOr("UI_BASE_URL", "http://localhost:5173")

	googleClientID := mustEnv("GOOGLE_CLIENT_ID")
	googleClientSecret := mustEnv("GOOGLE_CLIENT_SECRET")
	googleRedirectURL := envOr("GOOGLE_REDIRECT_URL", "http://localhost:8080/auth/google/callback")

	stripeSecretKey := mustEnv("STRIPE_SECRET_KEY")
	stripePriceClean := mustEnv("STRIPE_PRICE_CLEAN")
	stripePriceCleanPlus := mustEnv("STRIPE_PRICE_CLEANPLUS")
	stripeSuccessURL := envOr("STRIPE_SUCCESS_URL", uiBaseURL+"/cleanup?success=1")
	stripeCancelURL := envOr("STRIPE_CANCEL_URL", uiBaseURL+"/cleanup?canceled=1")
	stripeWebhookSecret := envOr("STRIPE_WEBHOOK_SECRET", "")
	stripeAllowVersionMismatch := envBool("STRIPE_WEBHOOK_ALLOW_VERSION_MISMATCH", false)
	cleanerDryRun := envBool("CLEANER_DRY_RUN", false)
	openAIAPIKey := envOr("OPENAI_API_KEY", "")
	openAIModel := envOr("OPENAI_MODEL", "gpt-4o-mini")
	openAIBaseURL := envOr("OPENAI_BASE_URL", "https://api.openai.com")

	server.LogConfig(
		port,
		uiBaseURL,
		googleRedirectURL,
		stripeSuccessURL,
		stripeCancelURL,
		stripePriceClean,
		stripePriceCleanPlus,
		googleClientID,
		googleClientSecret,
		stripeSecretKey,
		stripeWebhookSecret,
		stripeAllowVersionMismatch,
		cleanerDryRun,
		openAIModel,
		openAIAPIKey,
	)

	authService := auth.NewService(auth.Config{
		ClientID:     googleClientID,
		ClientSecret: googleClientSecret,
		RedirectURL:  googleRedirectURL,
		UIBaseURL:    uiBaseURL,
		SessionTTL:   30 * time.Minute,
		StateTTL:     10 * time.Minute,
	})

	stripeService := stripe.NewService(stripe.Config{
		SecretKey:            stripeSecretKey,
		PriceClean:           stripePriceClean,
		PriceCleanPlus:       stripePriceCleanPlus,
		SuccessURL:           stripeSuccessURL,
		CancelURL:            stripeCancelURL,
		WebhookSecret:        stripeWebhookSecret,
		AllowVersionMismatch: stripeAllowVersionMismatch,
	})

	cleanerService := cleaner.NewService(authService, cleaner.Config{
		OpenAIAPIKey:  openAIAPIKey,
		OpenAIModel:   openAIModel,
		OpenAIBaseURL: openAIBaseURL,
		MaxAIAnalysis: 120,
		DryRun:        cleanerDryRun,
	})

	httpServer := server.New(server.Config{
		Port:      port,
		UIBaseURL: uiBaseURL,
	}, authService, stripeService, cleanerService)

	if err := httpServer.Start(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOr(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func mustEnv(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		log.Fatalf("missing required env: %s", key)
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
