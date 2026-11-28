package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

var ErrRejected = errors.New("the email provider rejected the message")

type ResendClient struct {
	config   Config
	doer     Doer
	endpoint string
	spacing  time.Duration
	mu       sync.Mutex
	lastSent time.Time
}

func NewResendClient(config Config, doer Doer) *ResendClient {
	if doer == nil {
		doer = &http.Client{Timeout: requestTimeout}
	}
	return &ResendClient{config: config, doer: doer, endpoint: resendEndpoint, spacing: minSendSpacing}
}

func ConfigFromEnv() (Config, bool, error) {
	return configFromLookup(func(name string) string { return strings.TrimSpace(os.Getenv(name)) })
}

func configFromLookup(lookup func(string) string) (Config, bool, error) {
	config := Config{APIKey: lookup("RESEND_API_KEY"), From: lookup("EMAIL_FROM"), WebURL: lookup("APP_WEB_URL"), APIURL: lookup("API_PUBLIC_URL")}
	if config.APIKey == "" {
		return Config{}, false, nil
	}
	if _, err := mail.ParseAddress(config.From); err != nil {
		return Config{}, false, errors.New("EMAIL_FROM must be an address such as Better Guests <hello@example.com>")
	}
	parsed, err := url.Parse(config.WebURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Hostname() != "localhost") {
		return Config{}, false, errors.New("APP_WEB_URL must be an https address, or localhost")
	}
	if config.APIURL != "" {
		api, err := url.Parse(config.APIURL)
		if err != nil || api.Host == "" || (api.Scheme != "https" && api.Hostname() != "localhost") {
			return Config{}, false, errors.New("API_PUBLIC_URL must be an https address, or localhost")
		}
	}
	return config, true, nil
}

func (client *ResendClient) Send(ctx context.Context, message Message) error {
	payload := map[string]any{
		"from":    client.config.From,
		"to":      []string{message.To},
		"subject": message.Subject,
		"text":    message.Text,
	}
	if message.UnsubscribeURL != "" {
		payload["headers"] = map[string]string{
			"List-Unsubscribe":      "<" + message.UnsubscribeURL + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode email: %w", err)
	}
	if err := client.pace(ctx); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build email request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.config.APIKey)
	request.Header.Set("Content-Type", contentTypeJSON)
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set(idempotencyHeader, message.IdempotencyKey)
	response, err := client.doer.Do(request)
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= http.StatusMultipleChoices {
		var failure struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, maxErrorBodyBytes)).Decode(&failure)
		return fmt.Errorf("%w: status %d %s", ErrRejected, response.StatusCode, failure.Name)
	}
	return nil
}

func (client *ResendClient) pace(ctx context.Context) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	wait := time.Until(client.lastSent.Add(client.spacing))
	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	client.lastSent = time.Now()
	return nil
}
