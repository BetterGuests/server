package verification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	BiometricConsentKind  = "biometric"
	DefaultPersonaBaseURL = "https://api.withpersona.com"
	SignatureTolerance    = 5 * time.Minute
	governmentIDType      = "verification/government-id"
	passedCheckStatus     = "passed"
	maxNamePartLength     = 200
)

var (
	ErrAlreadyVerified    = errors.New("account is already verified")
	ErrConsentRequired    = errors.New("biometric consent is required")
	ErrHandoffUnavailable = errors.New("handoff is not available")
	ErrInvalidSignature   = errors.New("invalid webhook signature")
	ErrInvalidPayload     = errors.New("invalid webhook payload")
	ErrInquiryNotFound    = errors.New("verification inquiry not found")
)

var eventStatuses = map[string]Status{
	EventCompleted:       StatusCompleted,
	EventApproved:        StatusCompleted,
	EventFailed:          StatusFailed,
	EventDeclined:        StatusDeclined,
	EventExpired:         StatusExpired,
	EventMarkedForReview: StatusNeedsReview,
}

type VerificationService struct {
	store         Store
	consents      ConsentChecker
	claimer       InquiryClaimer
	inquiries     InquiryCreator
	templateID    string
	webhookSecret []byte
	now           func() time.Time
	newID         func() uuid.UUID
}

func ConfigFromEnv() (Config, error) {
	return configFromLookup(os.Getenv)
}

func configFromLookup(lookup func(string) string) (Config, error) {
	config := Config{
		APIKey:        lookup("PERSONA_API_KEY"),
		TemplateID:    lookup("PERSONA_TEMPLATE_ID"),
		WebhookSecret: lookup("PERSONA_WEBHOOK_SECRET"),
		BaseURL:       lookup("PERSONA_BASE_URL"),

		PropertyTemplateID: lookup("PERSONA_PROPERTY_TEMPLATE_ID"),
	}
	for name, value := range map[string]string{
		"PERSONA_API_KEY":        config.APIKey,
		"PERSONA_TEMPLATE_ID":    config.TemplateID,
		"PERSONA_WEBHOOK_SECRET": config.WebhookSecret,
	} {
		if value == "" {
			return Config{}, fmt.Errorf("%s is required", name)
		}
	}
	if config.BaseURL == "" {
		config.BaseURL = DefaultPersonaBaseURL
	}
	return config, nil
}

func NewVerificationService(store Store, inquiries InquiryCreator, config Config) *VerificationService {
	return &VerificationService{
		store:         store,
		inquiries:     inquiries,
		templateID:    config.TemplateID,
		webhookSecret: []byte(config.WebhookSecret),
		now:           time.Now,
		newID:         uuid.New,
	}
}

func (service *VerificationService) WithConsents(consents ConsentChecker) *VerificationService {
	service.consents = consents
	return service
}

func (service *VerificationService) WithOtherInquiries(claimer InquiryClaimer) *VerificationService {
	service.claimer = claimer
	return service
}

type inquiryFactory func(ctx context.Context, templateID, referenceID string) (Inquiry, error)

func (service *VerificationService) StartVerification(ctx context.Context, accountID uuid.UUID, replace bool) (StartResult, error) {
	return service.start(ctx, accountID, replace, service.inquiries.CreateInquiry)
}

func (service *VerificationService) StartHandoff(ctx context.Context, accountID uuid.UUID, replace bool) (StartResult, error) {
	creator, ok := service.inquiries.(HandoffInquiryCreator)
	if !ok {
		return StartResult{}, ErrHandoffUnavailable
	}
	return service.start(ctx, accountID, replace, creator.CreateHandoffInquiry)
}

func (service *VerificationService) start(ctx context.Context, accountID uuid.UUID, replace bool, create inquiryFactory) (StartResult, error) {
	if err := service.requireConsent(ctx, accountID); err != nil {
		return StartResult{}, err
	}
	completed, err := service.store.HasCompleted(ctx, accountID)
	if err != nil {
		return StartResult{}, fmt.Errorf("check existing verification: %w", err)
	}
	if completed && !replace {
		return StartResult{}, ErrAlreadyVerified
	}
	inquiry, err := create(ctx, service.templateID, accountID.String())
	if err != nil {
		return StartResult{}, fmt.Errorf("create persona inquiry: %w", err)
	}
	now := service.now().UTC()
	err = service.store.Create(ctx, Verification{
		ID:               service.newID(),
		AccountID:        accountID,
		PersonaInquiryID: inquiry.ID,
		Status:           StatusCreated,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("record verification: %w", err)
	}
	return StartResult{InquiryID: inquiry.ID, SessionToken: inquiry.SessionToken, OneTimeLink: inquiry.OneTimeLink, Status: StatusCreated}, nil
}

func (service *VerificationService) HandleWebhook(ctx context.Context, signatureHeader string, body []byte) error {
	signedAt, err := service.verifySignature(signatureHeader, body)
	if err != nil {
		return err
	}
	event, err := parseWebhookEvent(body, signedAt)
	if err != nil {
		return err
	}
	status, mapped := eventStatuses[event.Name]
	if !mapped {
		slog.Info("Ignoring unmapped persona event", "event", event.Name)
		return nil
	}
	if service.claimer != nil {
		claimed, err := service.claimer.Claim(ctx, OtherInquiry{
			AccountID: event.AccountID, InquiryID: event.InquiryID, EventName: event.Name, Body: body,
		})
		if err != nil {
			return fmt.Errorf("handle %s for another inquiry: %w", event.Name, err)
		}
		if claimed {
			return nil
		}
	}
	err = service.store.ApplyStatus(ctx, event.AccountID, event.InquiryID, status, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("apply %s: %w", event.Name, err)
	}
	if status == StatusCompleted && event.IDName == (IDName{}) {
		slog.Warn("Persona event has no government ID name", "event", event.Name, "inquiry_id", event.InquiryID)
	}
	if err := service.store.SyncIDName(ctx, event.AccountID, event.InquiryID, event.IDName); err != nil {
		return fmt.Errorf("sync ID name for %s: %w", event.Name, err)
	}
	return nil
}

func (service *VerificationService) verifySignature(header string, body []byte) (time.Time, error) {
	timestamp, signatures := parseSignatureHeader(header)
	if timestamp == "" || len(signatures) == 0 {
		return time.Time{}, ErrInvalidSignature
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return time.Time{}, ErrInvalidSignature
	}
	signedAt := time.Unix(seconds, 0)
	age := service.now().Sub(signedAt)
	if age > SignatureTolerance || age < -SignatureTolerance {
		return time.Time{}, ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, service.webhookSecret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	expected := mac.Sum(nil)
	for _, candidate := range signatures {
		decoded, err := hex.DecodeString(candidate)
		if err == nil && hmac.Equal(decoded, expected) {
			return signedAt, nil
		}
	}
	return time.Time{}, ErrInvalidSignature
}

func parseSignatureHeader(header string) (string, []string) {
	var timestamp string
	var signatures []string
	for _, part := range strings.FieldsFunc(header, func(r rune) bool { return r == ',' || r == ' ' }) {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signatures = append(signatures, value)
		}
	}
	return timestamp, signatures
}

type includedObject struct {
	Type       string          `json:"type"`
	Attributes json.RawMessage `json:"attributes"`
}

type governmentIDAttributes struct {
	Status string `json:"status"`
	First  string `json:"name-first"`
	Middle string `json:"name-middle"`
	Last   string `json:"name-last"`
	Suffix string `json:"name-suffix"`
}

func governmentIDName(included []includedObject) IDName {
	var name IDName
	for _, object := range included {
		if object.Type != governmentIDType {
			continue
		}
		var attributes governmentIDAttributes
		if json.Unmarshal(object.Attributes, &attributes) != nil || attributes.Status != passedCheckStatus {
			continue
		}
		candidate := IDName{
			First:  namePart(attributes.First),
			Middle: namePart(attributes.Middle),
			Last:   namePart(attributes.Last),
			Suffix: namePart(attributes.Suffix),
		}
		if candidate.First != "" || candidate.Last != "" {
			name = candidate
		}
	}
	return name
}

func namePart(value string) string {
	trimmed := strings.TrimSpace(value)
	if utf8.RuneCountInString(trimmed) > maxNamePartLength {
		return ""
	}
	return trimmed
}

type webhookEnvelope struct {
	Data struct {
		Attributes struct {
			Name      string `json:"name"`
			CreatedAt string `json:"created-at"`
			Payload   struct {
				Data struct {
					Type       string `json:"type"`
					ID         string `json:"id"`
					Attributes struct {
						ReferenceID string `json:"reference-id"`
					} `json:"attributes"`
				} `json:"data"`
				Included []includedObject `json:"included"`
			} `json:"payload"`
		} `json:"attributes"`
	} `json:"data"`
}

func parseWebhookEvent(body []byte, signedAt time.Time) (WebhookEvent, error) {
	var envelope webhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	attributes := envelope.Data.Attributes
	if attributes.Name == "" {
		return WebhookEvent{}, fmt.Errorf("%w: missing event name", ErrInvalidPayload)
	}
	occurredAt := signedAt
	if parsed, err := time.Parse(time.RFC3339, attributes.CreatedAt); err == nil {
		occurredAt = parsed
	}
	event := WebhookEvent{Name: attributes.Name, OccurredAt: occurredAt.UTC()}
	if _, mapped := eventStatuses[attributes.Name]; !mapped {
		return event, nil
	}
	inquiry := attributes.Payload.Data
	if inquiry.Type != "inquiry" || inquiry.ID == "" {
		return WebhookEvent{}, fmt.Errorf("%w: missing inquiry", ErrInvalidPayload)
	}
	accountID, err := uuid.Parse(inquiry.Attributes.ReferenceID)
	if err != nil || accountID == uuid.Nil {
		return WebhookEvent{}, fmt.Errorf("%w: reference-id is not an account id", ErrInquiryNotFound)
	}
	event.InquiryID = inquiry.ID
	event.AccountID = accountID
	if eventStatuses[attributes.Name] == StatusCompleted {
		event.IDName = governmentIDName(attributes.Payload.Included)
	}
	return event, nil
}

func (service *VerificationService) requireConsent(ctx context.Context, accountID uuid.UUID) error {
	if service.consents == nil {
		return nil
	}
	granted, err := service.consents.HasConsent(ctx, accountID, BiometricConsentKind)
	if err != nil {
		return fmt.Errorf("check biometric consent: %w", err)
	}
	if !granted {
		return ErrConsentRequired
	}
	return nil
}
