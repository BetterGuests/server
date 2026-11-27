package proofscan

import (
	"regexp"
	"strings"
	"unicode"
)

type NameVerdict string

const (
	NameMatch      NameVerdict = "match"
	NameLookCloser NameVerdict = "look-closer"
	NameNoMatch    NameVerdict = "no-match"
	NameNotFound   NameVerdict = "not-found"
)

const (
	minNameWords = 2
	maxNameWords = 6
)

var namePunctuation = regexp.MustCompile(`[.,]`)

func nameTokens(name string) []string {
	return strings.Fields(namePunctuation.ReplaceAllString(name, " "))
}

func sameWord(left, right []string, pick func([]string) string) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	return strings.EqualFold(pick(left), pick(right))
}

func first(words []string) string { return words[0] }

func last(words []string) string { return words[len(words)-1] }

func CompareNames(expected, found string) NameVerdict {
	want, got := nameTokens(expected), nameTokens(found)
	firstAgrees := sameWord(want, got, first)
	lastAgrees := sameWord(want, got, last)
	switch {
	case firstAgrees && lastAgrees:
		return NameMatch
	case lastAgrees:
		return NameLookCloser
	default:
		return NameNoMatch
	}
}

func verdictRank(verdict NameVerdict) int {
	switch verdict {
	case NameMatch:
		return 3
	case NameLookCloser:
		return 2
	case NameNoMatch:
		return 1
	default:
		return 0
	}
}

func sharedFullWords(expected, candidate []string) int {
	shared := 0
	for _, word := range candidate {
		if len([]rune(word)) < 2 {
			continue
		}
		for _, wanted := range expected {
			if strings.EqualFold(word, wanted) {
				shared++
				break
			}
		}
	}
	return shared
}

func nameLike(line string) string {
	cleaned := strings.Map(func(letter rune) rune {
		if unicode.IsLetter(letter) || letter == '.' || letter == '-' || letter == '\'' {
			return letter
		}
		return ' '
	}, line)
	return strings.Join(strings.Fields(cleaned), " ")
}

func FindName(text, expected string) (string, NameVerdict) {
	want := nameTokens(expected)
	if len(want) == 0 {
		return "", NameNotFound
	}
	bestName, bestVerdict, bestShared := "", NameNotFound, 0
	for _, line := range strings.Split(text, "\n") {
		candidate := nameLike(line)
		words := nameTokens(candidate)
		if len(words) < minNameWords || len(words) > maxNameWords {
			continue
		}
		shared := sharedFullWords(want, words)
		if shared == 0 {
			continue
		}
		verdict := CompareNames(expected, candidate)
		better := verdictRank(verdict) > verdictRank(bestVerdict) ||
			(verdict == bestVerdict && shared > bestShared)
		if better {
			bestName, bestVerdict, bestShared = candidate, verdict, shared
		}
	}
	return bestName, bestVerdict
}
