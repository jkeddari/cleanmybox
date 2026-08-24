package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/a-h/templ"
	"github.com/jkeddari/cleanmybox/internal/ui/pages"
)

func main() {
	port := os.Getenv("PREVIEW_PORT")
	if port == "" {
		port = "8090"
	}

	mux := http.NewServeMux()
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Clean("assets")))))
	mux.HandleFunc("GET /favicon.ico", serveFile("assets/img/favicon.ico"))
	mux.HandleFunc("GET /robots.txt", serveFile("assets/robots.txt"))
	mux.HandleFunc("GET /sitemap.xml", serveFile("assets/sitemap.xml"))
	mux.HandleFunc("GET /", render(pages.Home(false, "")))
	mux.HandleFunc("GET /gmail-inbox-cleaner", render(pages.GmailInboxCleaner(false, "")))
	mux.HandleFunc("GET /outlook-inbox-cleaner", render(pages.OutlookInboxCleaner(false, "")))
	mux.HandleFunc("GET /bulk-unsubscribe-newsletters", redirect("/guides"))
	mux.HandleFunc("GET /guides", render(pages.Guides(false, "")))
	mux.HandleFunc("GET /guides/clean-up-gmail-inbox", redirect("/gmail-inbox-cleaner"))
	mux.HandleFunc("GET /login", render(pages.Login(false, "")))
	mux.HandleFunc("GET /legal/privacy", render(pages.LegalPrivacy(false, "")))
	mux.HandleFunc("GET /legal/terms", render(pages.LegalTerms(false, "")))

	log.Printf("CleanMyBox preview listening on http://127.0.0.1:%s", port)
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		log.Fatal(err)
	}
}

func render(component templ.Component) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := component.Render(r.Context(), w); err != nil {
			log.Printf("render %s: %v", r.URL.Path, err)
		}
	}
}

func serveFile(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Clean(path))
	}
}

func redirect(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, path, http.StatusMovedPermanently)
	}
}
