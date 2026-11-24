package controllers

import "testing"

func TestMaskEmailKeepsTheFirstLetterAndTheDomain(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"  ":                     "",
		"owner.name@example.com": "o***@example.com",
		"Émile@example.nl":       "É***@example.nl",
		"no-at-sign":             "***",
		"@example.com":           "***",
	} {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
