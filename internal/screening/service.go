package screening

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

var errNoVerdict = errors.New("screening returned no verdict")

var refusalReasons = map[string]listings.PhotoRefusal{
	"explicit":             listings.RefusedExplicit,
	"violent":              listings.RefusedViolent,
	"hateful":              listings.RefusedHateful,
	"personal_information": listings.RefusedPersonalInformation,
	"contact_details":      listings.RefusedContactDetails,
	"unrelated":            listings.RefusedUnrelated,
}

type ScreeningService struct {
	client anthropic.Client
}

func ConfigFromLookup(lookup func(string) string) (Config, bool) {
	config := Config{APIKey: strings.TrimSpace(lookup("ANTHROPIC_API_KEY"))}
	return config, config.APIKey != ""
}

func NewScreeningService(config Config, options ...option.RequestOption) *ScreeningService {
	defaults := []option.RequestOption{option.WithAPIKey(config.APIKey), option.WithMaxRetries(screeningRetries)}
	return &ScreeningService{client: anthropic.NewClient(append(defaults, options...)...)}
}

func (service *ScreeningService) Screen(ctx context.Context, photo []byte, contentType string) (listings.PhotoRefusal, error) {
	ctx, cancel := context.WithTimeout(ctx, screeningDeadline)
	defer cancel()
	message, err := service.client.Beta.Messages.New(ctx, screeningRequest(photo, contentType))
	if err != nil {
		return "", fmt.Errorf("screen photo: %w", err)
	}
	if message.StopReason == anthropic.BetaStopReasonRefusal {
		return listings.RefusedUnsafe, nil
	}
	if message.StopReason != anthropic.BetaStopReasonEndTurn {
		return "", fmt.Errorf("screening stopped early (%s): %w", message.StopReason, errNoVerdict)
	}
	for _, block := range message.Content {
		if text, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			return refusalFrom(text.Text)
		}
	}
	return "", errNoVerdict
}

func screeningRequest(photo []byte, contentType string) anthropic.BetaMessageNewParams {
	return anthropic.BetaMessageNewParams{
		Model:     screeningModel,
		MaxTokens: maxVerdictTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: screeningInstructions}},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortLow,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: verdictSchema()},
		},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Messages: []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(
			anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
				Data:      base64.StdEncoding.EncodeToString(photo),
				MediaType: anthropic.BetaBase64ImageSourceMediaType(contentType),
			}),
			anthropic.NewBetaTextBlock("Categorize this photo."),
		)},
	}
}

func verdictSchema() map[string]any {
	categories := []string{categoryProperty}
	for category := range refusalReasons {
		categories = append(categories, category)
	}
	slices.Sort(categories)
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"category": map[string]any{"type": "string", "enum": categories}},
		"required":             []string{"category"},
		"additionalProperties": false,
	}
}

func refusalFrom(answer string) (listings.PhotoRefusal, error) {
	var verdict verdictPayload
	if err := json.Unmarshal([]byte(answer), &verdict); err != nil {
		return "", fmt.Errorf("decode verdict: %w", err)
	}
	if verdict.Category == categoryProperty {
		return "", nil
	}
	if refusal, known := refusalReasons[verdict.Category]; known {
		return refusal, nil
	}
	return "", fmt.Errorf("unknown category %q: %w", verdict.Category, errNoVerdict)
}
