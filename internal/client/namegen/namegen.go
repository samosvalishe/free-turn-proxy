// Package namegen produces random Russian-style display names for
// client identities. Name pools are embedded from data/*.txt.
package namegen

import (
	_ "embed"
	"math/rand/v2"
	"strings"
)

//go:embed data/first_names_male.txt
var rawMaleFirstNames string

//go:embed data/first_names_female.txt
var rawFemaleFirstNames string

//go:embed data/last_names.txt
var rawLastNames string

var (
	maleFirstNames   = parseLines(rawMaleFirstNames)
	femaleFirstNames = parseLines(rawFemaleFirstNames)
	lastNames        = parseLines(rawLastNames)
)

func parseLines(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// feminizeSurname converts a Russian male surname to its female form.
// Operates on runes since Cyrillic letters are multibyte in UTF-8.
// Surnames whose suffixes do not match Russian patterns are returned
// unchanged (covers foreign / meme surnames).
func feminizeSurname(surname string) string {
	rs := []rune(surname)
	n := len(rs)
	if n < 2 {
		return surname
	}
	switch string(rs[n-2:]) {
	case "ий", "ый", "ой":
		return string(rs[:n-2]) + "ая"
	case "ов", "ев", "ёв", "ин", "ын":
		return surname + "а"
	}
	return surname
}

// SurnameProbability is the chance that Generate returns a name with a surname.
const SurnameProbability = 0.7

// Generate returns a random "FirstName" or "FirstName LastName".
// Gender is chosen uniformly; surnames are feminized for female names.
func Generate() string {
	isFemale := rand.IntN(2) == 0

	pool := maleFirstNames
	if isFemale {
		pool = femaleFirstNames
	}
	fn := pool[rand.IntN(len(pool))]

	if rand.Float64() >= SurnameProbability {
		return fn
	}

	ln := lastNames[rand.IntN(len(lastNames))]
	if isFemale {
		ln = feminizeSurname(ln)
	}
	return fn + " " + ln
}
