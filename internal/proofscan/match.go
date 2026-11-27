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

var columnGap = regexp.MustCompile(`\s{2,}|\t`)

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
	var words []string
	for _, word := range strings.Fields(cleaned) {
		if strings.IndexFunc(word, unicode.IsLetter) >= 0 {
			words = append(words, word)
		}
	}
	return strings.Join(words, " ")
}

func isExpectedWord(word string, expected []string) bool {
	return sharedFullWords(expected, nameTokens(word)) > 0
}

func nameRuns(words, expected []string) [][]string {
	var runs [][]string
	for from := range words {
		startsOnName := isExpectedWord(words[from], expected)
		for to := from + minNameWords; to <= len(words) && to-from <= maxNameWords; to++ {
			if startsOnName || isExpectedWord(words[to-1], expected) {
				runs = append(runs, words[from:to])
			}
		}
	}
	return runs
}

type nameCandidate struct {
	name    string
	verdict NameVerdict
	shared  int
	words   int
}

func (candidate nameCandidate) beats(other nameCandidate) bool {
	if verdictRank(candidate.verdict) != verdictRank(other.verdict) {
		return verdictRank(candidate.verdict) > verdictRank(other.verdict)
	}
	if candidate.shared != other.shared {
		return candidate.shared > other.shared
	}
	return candidate.words < other.words
}

func FindName(text, expected string) (string, NameVerdict) {
	want := nameTokens(expected)
	if len(want) == 0 {
		return "", NameNotFound
	}
	best := nameCandidate{verdict: NameNotFound}
	for _, line := range strings.Split(text, "\n") {
		for _, column := range columnGap.Split(line, -1) {
			best = bestRunIn(column, expected, want, best)
		}
	}
	return best.name, best.verdict
}

func bestRunIn(column, expected string, want []string, best nameCandidate) nameCandidate {
	for _, run := range nameRuns(strings.Fields(nameLike(column)), want) {
		name := strings.Join(run, " ")
		candidate := nameCandidate{
			name:    name,
			verdict: CompareNames(expected, name),
			shared:  sharedFullWords(want, nameTokens(name)),
			words:   len(run),
		}
		if candidate.beats(best) {
			best = candidate
		}
	}
	return best
}
