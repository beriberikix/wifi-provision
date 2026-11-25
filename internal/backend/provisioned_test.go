package backend

import "testing"

func TestHasAccessPoints(t *testing.T) {
	cases := map[string]bool{
		"null\n":                  false,
		"":                        false,
		"wlan0:\n  dhcp4: true\n": false,
		"wlan0:\n  dhcp4: true\n  regulatory-domain: \"US\"\n  access-points:\n    \"funrun\":\n      auth:\n        key-management: \"psk\"\n": true,
	}
	for in, want := range cases {
		if got := hasAccessPoints(in); got != want {
			t.Errorf("hasAccessPoints(%q) = %v, want %v", in, got, want)
		}
	}
}
