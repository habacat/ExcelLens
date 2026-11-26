package excelformula

import (
	"strings"
	"unicode"
)

func FoldRune(r rune) rune {
	if r < 0x80 {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
	minimum := r
	for folded := unicode.SimpleFold(r); folded != r; folded = unicode.SimpleFold(folded) {
		if folded < minimum {
			minimum = folded
		}
	}
	return minimum
}

type Token struct {
	Kind byte // 'c' literal rune, '?' any single rune, '*' zero or more runes
	Rune rune
}

// Criteria use ~ to escape ?, * or ~. SEARCH and MATCH also consume
// the escape before ordinary characters and ignore a trailing escape.
func Wildcard(pattern string, criteria bool) []Token {
	runes := []rune(pattern)
	tokens := make([]Token, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '~':
			if i+1 < len(runes) && (!criteria || strings.ContainsRune("*?~", runes[i+1])) {
				i++
				r = runes[i]
			} else if !criteria {
				continue
			}
		case '?', '*':
			if r != '*' || len(tokens) == 0 || tokens[len(tokens)-1].Kind != '*' {
				tokens = append(tokens, Token{Kind: byte(r)})
			}
			continue
		}
		tokens = append(tokens, Token{Kind: 'c', Rune: FoldRune(r)})
	}
	return tokens
}
