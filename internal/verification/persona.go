package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	personaVersion        = "2026-09-29"
	personaRequestTimeout = 15 * time.Second
	personaMaxResponse    = 1 << 20
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
		AutoCreateInquirySession     bool   `json:"auto-create-inquiry-session"`
		AutoCreateAccountReferenceID string `json:"auto-create-account-reference-id"`
	} `json:"meta"`
}

type createInquiryResponse struct {
	Data struct {
		ID string `json:"id"`
	} `json:"data"`
	Meta struct {
		SessionToken string `json:"session-token"`
	} `json:"meta"`
}

func (client *PersonaClient) CreateInquiry(ctx context.Context, templateID, referenceID string) (Inquiry, error) {
	var payload createInquiryRequest
	payload.Data.Attributes.TemplateID = templateID
	payload.Data.Attributes.ReferenceID = referenceID
	payload.Meta.AutoCreateInquirySession = true
	payload.Meta.AutoCreateAccountReferenceID = referenceID
	body, err := json.Marshal(payload)
	if err != nil {
		return Inquiry{}, fmt.Errorf("encode inquiry request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/api/v1/inquiries", bytes.NewReader(body))
	if err != nil {
		return Inquiry{}, fmt.Errorf("build inquiry request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("Persona-Version", personaVersion)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return Inquiry{}, fmt.Errorf("%w: %v", ErrPersonaRequest, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return Inquiry{}, fmt.Errorf("%w: status %d", ErrPersonaRequest, response.StatusCode)
	}
	var decoded createInquiryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, personaMaxResponse)).Decode(&decoded); err != nil {
		return Inquiry{}, fmt.Errorf("%w: decode response: %v", ErrPersonaRequest, err)
	}
	if decoded.Data.ID == "" || decoded.Meta.SessionToken == "" {
		return Inquiry{}, fmt.Errorf("%w: response missing inquiry id or session token", ErrPersonaRequest)
	}
	return Inquiry{ID: decoded.Data.ID, SessionToken: decoded.Meta.SessionToken}, nil
}
