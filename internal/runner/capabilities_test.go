package runner

import (
	"slices"
	"testing"
)

func TestParseMissingCapabilities(t *testing.T) {
	all := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "KILL", "SETGID", "SETUID"}
	cases := []struct {
		name   string
		status string
		want   []string
	}{
		{"all present", "Name:\trunboxd\nCapEff:\t00000000000000eb\n", nil},
		{"none present", "CapEff:\t0000000000000000\n", all},
		{"missing kill", "CapEff:\t00000000000000cb\n", []string{"KILL"}},
		{"missing setuid", "CapEff:\t000000000000006b\n", []string{"SETUID"}},
		{"no cap line", "Name:\trunboxd\n", all},
		{"unparseable", "CapEff:\tnot-hex\n", all},
		{"extra bits", "CapEff:\tffffffffffffffff\n", nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := parseMissingCapabilities(test.status); !slices.Equal(got, test.want) {
				t.Fatalf("missing = %v, want %v", got, test.want)
			}
		})
	}
}
