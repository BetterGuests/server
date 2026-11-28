package email

import (
	"net/http"
	"time"
)

const (
	resendEndpoint    = "https://api.resend.com/emails"
	requestTimeout    = 10 * time.Second
	minSendSpacing    = 600 * time.Millisecond
	maxErrorBodyBytes = 2 << 10
	idempotencyHeader = "Idempotency-Key"
	contentTypeJSON   = "application/json"
	userAgent         = "betterguests-api"
)

type Config struct {
	APIKey string
	From   string
	WebURL string
	APIURL string
}

type Message struct {
	To             string
	Subject        string
	Text           string
	IdempotencyKey string
	UnsubscribeURL string
}

type Doer interface {
	Do(request *http.Request) (*http.Response, error)
}
