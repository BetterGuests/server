package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	personaVersion        = "2026-09-29"
	personaRequestTimeout = 15 * time.Second
	personaMaxResponse    = 1 << 20

	personaErrorDetailBytes = 2048
)

var ErrPersonaRequest = errors.New("persona request failed")

type PersonaClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func NewPersonaClient(config Config, client *http.Client) *PersonaClient {
	if client == nil {
		client = &http.Client{Timeout: personaRequestTimeout}
	}
	return &PersonaClient{apiKey: config.APIKey, baseURL: strings.TrimRight(config.BaseURL, "/"), http: client}
}

type createInquiryRequest struct {
	Data struct {
		Attributes struct {
			TemplateID  string `json:"inquiry-template-id"`
			ReferenceID string `json:"reference-id"`
		} `json:"attributes"`
	} `json:"data"`
	Meta struct {
		AutoCreateInquirySession bool `json:"auto-create-inquiry-session"`
		AutoCreateOneTimeLink    bool `json:"auto-create-one-time-link,omitempty"`
	} `json:"meta"`
}

type createInquiryResponse struct {
	Data struct {
		ID string `json:"id"`
	} `json:"data"`
	Meta struct {
		SessionToken string `json:"session-token"`
		OneTimeLink  string `json:"one-time-link"`
	} `json:"meta"`
}

func (client *PersonaClient) CreateInquiry(ctx context.Context, templateID, referenceID string) (Inquiry, error) {
	return client.createInquiry(ctx, templateID, referenceID, false)
}

func (client *PersonaClient) CreateHandoffInquiry(ctx context.Context, templateID, referenceID string) (Inquiry, error) {
	return client.createInquiry(ctx, templateID, referenceID, true)
}

func (client *PersonaClient) createInquiry(ctx context.Context, templateID, referenceID string, handoff bool) (Inquiry, error) {
	var payload createInquiryRequest
	payload.Data.Attributes.TemplateID = templateID
	payload.Data.Attributes.ReferenceID = referenceID
	payload.Meta.AutoCreateInquirySession = !handoff
	payload.Meta.AutoCreateOneTimeLink = handoff
	body, err := json.Marshal(payload)
	if err != nil {
		return Inquiry{}, fmt.Errorf("encode inquiry request: %w", err)
	}
	response, err := client.send(ctx, http.MethodPost, "/api/v1/inquiries", body)
	if err != nil {
		return Inquiry{}, err
	}
	defer response.Body.Close()
	if err := requireSuccess(response); err != nil {
		return Inquiry{}, err
	}
	var decoded createInquiryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, personaMaxResponse)).Decode(&decoded); err != nil {
		return Inquiry{}, fmt.Errorf("%w: decode response: %v", ErrPersonaRequest, err)
	}
	if decoded.Data.ID == "" {
		return Inquiry{}, fmt.Errorf("%w: response missing inquiry id", ErrPersonaRequest)
	}
	if handoff && decoded.Meta.OneTimeLink == "" {
		return Inquiry{}, fmt.Errorf("%w: response missing one-time link", ErrPersonaRequest)
	}
	if !handoff && decoded.Meta.SessionToken == "" {
		return Inquiry{}, fmt.Errorf("%w: response missing session token", ErrPersonaRequest)
	}
	return Inquiry{ID: decoded.Data.ID, SessionToken: decoded.Meta.SessionToken, OneTimeLink: decoded.Meta.OneTimeLink}, nil
}

var inquiryIDPattern = regexp.MustCompile(`^inq_[A-Za-z0-9]+$`)

func (client *PersonaClient) RedactInquiry(ctx context.Context, inquiryID string) error {
	if !inquiryIDPattern.MatchString(inquiryID) {
		return fmt.Errorf("%w: malformed inquiry id", ErrPersonaRequest)
	}
	response, err := client.send(ctx, http.MethodDelete, "/api/v1/inquiries/"+inquiryID, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || (response.StatusCode >= 200 && response.StatusCode <= 299) {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(response.Body, personaErrorDetailBytes))
	return fmt.Errorf("%w: status %d: %s", ErrPersonaRequest, response.StatusCode, strings.TrimSpace(string(detail)))
}

var personaRetryDelays = []time.Duration{200 * time.Millisecond, 600 * time.Millisecond}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func (client *PersonaClient) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		response, err := client.sendOnce(ctx, method, path, body)
		retryable := err != nil || retryableStatus(response.StatusCode)
		if !retryable || attempt >= len(personaRetryDelays) || ctx.Err() != nil {
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrPersonaRequest, err)
			}
			return response, nil
		}
		if response != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, personaErrorDetailBytes))
			response.Body.Close()
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", ErrPersonaRequest, ctx.Err())
		case <-time.After(personaRetryDelays[attempt]):
		}
	}
}

func (client *PersonaClient) sendOnce(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("Persona-Version", personaVersion)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return client.http.Do(request)
}

func requireSuccess(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode <= 299 {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(response.Body, personaErrorDetailBytes))
	return fmt.Errorf("%w: status %d: %s", ErrPersonaRequest, response.StatusCode, strings.TrimSpace(string(detail)))
}

type InquirySnapshot struct {
	ID          string
	Status      string
	ReferenceID string
	ChangedAt   time.Time
	Included    []includedObject
}

type inquiryTimestamps struct {
	Updated         string `json:"updated-at"`
	Completed       string `json:"completed-at"`
	Failed          string `json:"failed-at"`
	Expired         string `json:"expired-at"`
	Decisioned      string `json:"decisioned-at"`
	MarkedForReview string `json:"marked-for-review-at"`
}

func (stamps inquiryTimestamps) forStatus(status string) time.Time {
	byStatus := map[string]string{
		"completed":    stamps.Completed,
		"approved":     stamps.Decisioned,
		"declined":     stamps.Decisioned,
		"failed":       stamps.Failed,
		"expired":      stamps.Expired,
		"needs_review": stamps.MarkedForReview,
	}
	for _, candidate := range []string{byStatus[status], stamps.Updated} {
		if parsed, err := time.Parse(time.RFC3339, candidate); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

type retrieveInquiryResponse struct {
	Data struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		Attributes struct {
			inquiryTimestamps
			Status      string `json:"status"`
			ReferenceID string `json:"reference-id"`
		} `json:"attributes"`
	} `json:"data"`
	Included []includedObject `json:"included"`
}

func (client *PersonaClient) GetInquiry(ctx context.Context, inquiryID string) (InquirySnapshot, error) {
	if !inquiryIDPattern.MatchString(inquiryID) {
		return InquirySnapshot{}, fmt.Errorf("%w: malformed inquiry id", ErrPersonaRequest)
	}
	response, err := client.send(ctx, http.MethodGet, "/api/v1/inquiries/"+inquiryID+"?include=verifications", nil)
	if err != nil {
		return InquirySnapshot{}, err
	}
	defer response.Body.Close()
	if err := requireSuccess(response); err != nil {
		return InquirySnapshot{}, err
	}
	var decoded retrieveInquiryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, personaMaxResponse)).Decode(&decoded); err != nil {
		return InquirySnapshot{}, fmt.Errorf("%w: decode inquiry: %v", ErrPersonaRequest, err)
	}
	if decoded.Data.ID != inquiryID || decoded.Data.Attributes.Status == "" {
		return InquirySnapshot{}, fmt.Errorf("%w: response is not inquiry %s", ErrPersonaRequest, inquiryID)
	}
	attributes := decoded.Data.Attributes
	return InquirySnapshot{
		ID:          decoded.Data.ID,
		Status:      attributes.Status,
		ReferenceID: attributes.ReferenceID,
		ChangedAt:   attributes.forStatus(attributes.Status),
		Included:    decoded.Included,
	}, nil
}

func (client *PersonaClient) ResumeInquiry(ctx context.Context, inquiryID string) (Inquiry, error) {
	if !inquiryIDPattern.MatchString(inquiryID) {
		return Inquiry{}, fmt.Errorf("%w: malformed inquiry id", ErrPersonaRequest)
	}
	response, err := client.send(ctx, http.MethodPost, "/api/v1/inquiries/"+inquiryID+"/resume", nil)
	if err != nil {
		return Inquiry{}, err
	}
	defer response.Body.Close()
	if err := requireSuccess(response); err != nil {
		return Inquiry{}, err
	}
	var decoded createInquiryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, personaMaxResponse)).Decode(&decoded); err != nil {
		return Inquiry{}, fmt.Errorf("%w: decode resume: %v", ErrPersonaRequest, err)
	}
	if decoded.Meta.SessionToken == "" {
		return Inquiry{}, fmt.Errorf("%w: resume response missing session token", ErrPersonaRequest)
	}
	return Inquiry{ID: inquiryID, SessionToken: decoded.Meta.SessionToken}, nil
}
