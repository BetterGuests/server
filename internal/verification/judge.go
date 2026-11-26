package verification

import (
	"crypto/hmac"
	"crypto/sha256"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	adultAge        = 18
	birthDateLayout = "2006-01-02"
)

func judgeCompletion(completion Completion, history History) []ReviewReason {
	var reasons []ReviewReason
	if completion.UnderAge {
		reasons = append(reasons, ReasonUnderAge)
	}
	if history.DuplicateIdentity {
		reasons = append(reasons, ReasonDuplicateIdentity)
	}
	if history.HasDecline && !history.HasEarlierPass {
		reasons = append(reasons, ReasonPreviouslyDeclined)
	}
	previous, current := history.PreviousName, completion.Name
	switch {
	case named(previous) && named(current):
		if !sameName(previous, current) {
			reasons = append(reasons, ReasonNameChanged)
		}
	case named(previous), history.HasEarlierPass && history.HasApprovedProof:
		reasons = append(reasons, ReasonNameUnconfirmed)
	}
	return reasons
}

func named(name IDName) bool {
	return name.First != "" || name.Last != ""
}

func nameWords(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func sameName(left, right IDName) bool {
	return slices.Equal(nameWords(left.First), nameWords(right.First)) &&
		slices.Equal(nameWords(left.Last), nameWords(right.Last)) &&
		slices.Equal(nameWords(left.Suffix), nameWords(right.Suffix))
}

func underAge(birthDate string, at time.Time) bool {
	born, err := time.Parse(birthDateLayout, birthDate)
	return err == nil && born.AddDate(adultAge, 0, 0).After(at)
}

func fingerprint(key []byte, name IDName, birthDate string) []byte {
	first, last := nameWords(name.First), nameWords(name.Last)
	if len(key) == 0 || len(first) == 0 || len(last) == 0 {
		return nil
	}
	if _, err := time.Parse(birthDateLayout, birthDate); err != nil {
		return nil
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.Join(first, " ") + "|" + strings.Join(last, " ") + "|" + birthDate))
	return mac.Sum(nil)
}
