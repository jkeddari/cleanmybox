# CleanMyBox

CleanMyBox is a one-shot mailbox cleanup MVP.

It supports:
- Google and Outlook OAuth login
- Newsletter cleanup (delete + unsubscribe attempts)
- AI-assisted cleanup (`cleanplus`)
- Stripe checkout (live/test)
- Per-user cleanup history (PostgreSQL / Supabase)

## Local run

1. Copy `.env.example` to `.env` and fill required variables.
2. Start PostgreSQL (or use Supabase).
3. Run:

```bash
go run ./cmd/server
```

App runs on `http://localhost:8080` by default.
