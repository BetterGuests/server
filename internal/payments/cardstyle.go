package payments

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxBankNameRunes = 28

var ErrInvalidCardStyle = errors.New("use a short bank name without card numbers and a listed color")

var cardColors = []string{"ocean", "forest", "teal", "plum", "crimson", "sunset", "gold", "graphite"}

var longDigitRun = regexp.MustCompile(`\d(?:[ .-]?\d){5,}`)

type CardStyle struct {
	BankName string `json:"bank_name"`
	Color    string `json:"color"`
}

func (style CardStyle) cleaned() (CardStyle, error) {
	style.BankName = strings.Join(strings.Fields(style.BankName), " ")
	switch {
	case utf8.RuneCountInString(style.BankName) > maxBankNameRunes,
		strings.ContainsFunc(style.BankName, func(r rune) bool { return !unicode.IsPrint(r) }),
		longDigitRun.MatchString(style.BankName),
		style.Color != "" && !slices.Contains(cardColors, style.Color):
		return CardStyle{}, ErrInvalidCardStyle
	}
	return style, nil
}

func (service *PaymentService) StyleCard(ctx context.Context, accountID uuid.UUID, cardID string, style CardStyle) error {
	cleaned, err := style.cleaned()
	if err != nil {
		return err
	}
	if _, err := service.ownedCard(ctx, accountID, cardID); err != nil {
		return err
	}
	return service.wallet.StyleCard(ctx, cardID, cleaned)
}
