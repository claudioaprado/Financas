package validate

import (
	"errors"
	"strings"
	"testing"
)

func TestName(t *testing.T) {
	cases := []struct {
		desc string
		in   string
		want error
	}{
		{"plain", "Conta Corrente", nil},
		{"interior spaces allowed", "PETR4 - Petrobras PN", nil},
		{"accented", "Alimentação", nil},
		{"at limit", strings.Repeat("a", MaxNameLen), nil},
		{"over limit", strings.Repeat("a", MaxNameLen+1), ErrNameTooLong},
		{"newline rejected", "Conta\nCorrente", ErrNameBadChars},
		{"tab rejected", "Conta\tCorrente", ErrNameBadChars},
	}
	for _, c := range cases {
		if got := Name(c.in); !errors.Is(got, c.want) {
			t.Errorf("Name(%q) = %v, want %v", c.desc, got, c.want)
		}
	}
}

func TestNameLenCountsRunesNotBytes(t *testing.T) {
	// MaxNameLen runes of a multi-byte char must be accepted (rune count, not
	// byte count, so a valid accented name near the cap isn't wrongly rejected).
	if err := Name(strings.Repeat("ç", MaxNameLen)); err != nil {
		t.Fatalf("Name(%d×ç) = %v, want nil", MaxNameLen, err)
	}
	if err := Name(strings.Repeat("ç", MaxNameLen+1)); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("Name(%d×ç) = %v, want ErrNameTooLong", MaxNameLen+1, err)
	}
}

func TestSymbol(t *testing.T) {
	cases := []struct {
		desc string
		in   string
		want error
	}{
		{"plain ticker", "PETR4", nil},
		{"dotted", "BRK.B", nil},
		{"hyphenated", "BRK-B", nil},
		{"at limit", strings.Repeat("A", MaxSymbolLen), nil},
		{"over limit", strings.Repeat("A", MaxSymbolLen+1), ErrSymbolTooLong},
		{"interior space rejected", "PE TR 4", ErrSymbolBadChars},
		{"trailing space rejected", "PETR4 ", ErrSymbolBadChars},
		{"control char rejected", "PET\tR4", ErrSymbolBadChars},
	}
	for _, c := range cases {
		if got := Symbol(c.in); !errors.Is(got, c.want) {
			t.Errorf("Symbol(%q) = %v, want %v", c.desc, got, c.want)
		}
	}
}
