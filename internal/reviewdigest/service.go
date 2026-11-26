package reviewdigest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
)

var (
	errNoSummary      = errors.New("review digest returned no summary")
	errUnknownSubject = errors.New("unknown review summary subject")
)

type ReviewDigestService struct {
	client anthropic.Client
}

func ConfigFromLookup(lookup func(string) string) (Config, bool) {
	config := Config{
		APIKey:      strings.TrimSpace(lookup("ANTHROPIC_API_KEY")),
		WorkspaceID: strings.TrimSpace(lookup("ANTHROPIC_WORKSPACE_ID")),
	}
	return config, config.APIKey != ""
}

func NewReviewDigestService(config Config, options ...option.RequestOption) *ReviewDigestService {
	defaults := []option.RequestOption{option.WithAPIKey(config.APIKey), option.WithMaxRetries(digestRetries)}
	if config.WorkspaceID != "" {
		defaults = append(defaults, option.WithHeader(workspaceHeader, config.WorkspaceID))
	}
	return &ReviewDigestService{client: anthropic.NewClient(append(defaults, options...)...)}
}

func (service *ReviewDigestService) Summarize(ctx context.Context, request reviews.SummaryRequest) (reviews.SummaryAnswer, error) {
	params, err := digestRequest(request)
	if err != nil {
		return reviews.SummaryAnswer{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, digestDeadline)
	defer cancel()
	message, err := service.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return reviews.SummaryAnswer{}, fmt.Errorf("summarize reviews: %w", err)
	}
	if message.StopReason != anthropic.BetaStopReasonEndTurn {
		return reviews.SummaryAnswer{}, fmt.Errorf("summary stopped early (%s): %w", message.StopReason, errNoSummary)
	}
	for _, block := range message.Content {
		if text, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			return answerFrom(text.Text, request.Current != "")
		}
	}
	return reviews.SummaryAnswer{}, errNoSummary
}

func digestRequest(request reviews.SummaryRequest) (anthropic.BetaMessageNewParams, error) {
	audience, err := audienceFor(request.Subject)
	if err != nil {
		return anthropic.BetaMessageNewParams{}, err
	}
	input := digestInput{CurrentSummary: request.Current, Reviews: make([]digestReview, 0, len(request.Reviews))}
	for _, review := range request.Reviews {
		input.Reviews = append(input.Reviews, digestReview{
			Rating:  review.Rating,
			Comment: review.Comment,
			New:     arrivedAfter(review, request.CurrentSince),
		})
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return anthropic.BetaMessageNewParams{}, fmt.Errorf("encode reviews: %w", err)
	}
	return anthropic.BetaMessageNewParams{
		Model:     digestModel,
		MaxTokens: maxDigestTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: fmt.Sprintf(digestInstructions, audience)}},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortMedium,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: answerSchema()},
		},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Messages: []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(
			anthropic.NewBetaTextBlock(string(encoded)),
		)},
	}, nil
}

func arrivedAfter(review reviews.Review, since *time.Time) bool {
	if since == nil {
		return false
	}
	arrived := review.CreatedAt
	if review.PublishedAt != nil {
		arrived = *review.PublishedAt
	}
	return arrived.After(*since)
}

func audienceFor(subject reviews.SummarySubject) (string, error) {
	switch subject {
	case reviews.SummaryOfListing:
		return listingAudience, nil
	case reviews.SummaryOfGuest:
		return guestAudience, nil
	default:
		return "", fmt.Errorf("%w: %q", errUnknownSubject, subject)
	}
}

func answerSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"current_summary_still_accurate": map[string]any{"type": "boolean"},
			"summary":                        map[string]any{"type": "string"},
		},
		"required":             []string{"current_summary_still_accurate", "summary"},
		"additionalProperties": false,
	}
}

func answerFrom(text string, hadSummary bool) (reviews.SummaryAnswer, error) {
	var payload digestPayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return reviews.SummaryAnswer{}, fmt.Errorf("decode summary: %w", err)
	}
	answer := reviews.SummaryAnswer{
		Summary:       strings.TrimSpace(payload.Summary),
		StillAccurate: hadSummary && payload.StillAccurate,
	}
	if !answer.StillAccurate && answer.Summary == "" {
		return reviews.SummaryAnswer{}, errNoSummary
	}
	return answer, nil
}
