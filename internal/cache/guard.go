package cache

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The guard is a cheap, deterministic second opinion on a semantic hit. Embedding models rate
// "capital of France" and "capital of Germany" as near duplicates; the guard rejects such pairs by checking
// the details embeddings blur: numbers, negation, antonym pairs and capitalised names.
//
// It is intentionally conservative. A rejected hit costs one upstream call; a wrong hit returns a wrong answer.
// Known limits: lower case names, synonyms and paraphrased negation are not detected.

var (
	numberRe = regexp.MustCompile(`\d+(?:[.,]\d+)*`)
	wordRe   = regexp.MustCompile(`[\p{L}][\p{L}'’]*`)
)

var negationWords = map[string]bool{
	"not": true, "no": true, "never": true, "without": true, "cannot": true,
	"none": true, "neither": true, "nor": true, "nothing": true, "nobody": true,
}

var oppositePairs = [][2]string{
	{"enable", "disable"}, {"increase", "decrease"}, {"add", "remove"}, {"before", "after"},
	{"true", "false"}, {"start", "stop"}, {"open", "close"}, {"allow", "block"},
	{"install", "uninstall"}, {"encrypt", "decrypt"}, {"buy", "sell"}, {"win", "lose"},
	{"lock", "unlock"}, {"on", "off"}, {"include", "exclude"}, {"accept", "reject"}, {"import", "export"},
}

var pronounI = map[string]bool{"i": true, "i'm": true, "i've": true, "i'll": true, "i'd": true}

func numbers(s string) []string {
	found := numberRe.FindAllString(s, -1)
	for i, n := range found {
		found[i] = strings.ReplaceAll(n, ",", "")
	}
	sort.Strings(found)
	return found
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tokens(s string) []string {
	words := wordRe.FindAllString(s, -1)
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.ToLower(strings.ReplaceAll(w, "’", "'"))
	}
	return out
}

func hasNegation(toks []string) bool {
	for _, t := range toks {
		if negationWords[t] || strings.HasSuffix(t, "n't") {
			return true
		}
	}
	return false
}

func tokenSet(toks []string) map[string]bool {
	set := make(map[string]bool, len(toks))
	for _, t := range toks {
		set[t] = true
	}
	return set
}

// capitalised returns the lower cased words that start with a capital letter, ignoring the first word of
// the text (which is capitalised by grammar) and the pronoun I.
func capitalised(s string) []string {
	var out []string
	for i, w := range wordRe.FindAllString(s, -1) {
		if i == 0 {
			continue
		}
		r, _ := utf8.DecodeRuneInString(w)
		lw := strings.ToLower(w)
		if unicode.IsUpper(r) && !pronounI[lw] {
			out = append(out, lw)
		}
	}
	sort.Strings(out)
	return out
}

// GuardAllows reports whether a semantic candidate may be served for newPrompt. When it returns false,
// reason names the first check that failed.
func GuardAllows(newPrompt, cachedPrompt string) (allowed bool, reason string) {
	if !equalStrings(numbers(newPrompt), numbers(cachedPrompt)) {
		return false, "number_mismatch"
	}
	ta, tb := tokens(newPrompt), tokens(cachedPrompt)
	if hasNegation(ta) != hasNegation(tb) {
		return false, "negation_mismatch"
	}
	sa, sb := tokenSet(ta), tokenSet(tb)
	for _, p := range oppositePairs {
		x, y := p[0], p[1]
		if (sa[x] && !sa[y] && sb[y] && !sb[x]) || (sa[y] && !sa[x] && sb[x] && !sb[y]) {
			return false, "antonym_mismatch"
		}
	}
	if !equalStrings(capitalised(newPrompt), capitalised(cachedPrompt)) {
		return false, "entity_mismatch"
	}
	return true, ""
}
