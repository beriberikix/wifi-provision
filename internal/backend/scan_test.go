package backend

import (
	"reflect"
	"testing"
)

func TestClassifySecurity(t *testing.T) {
	cases := []struct {
		flags string
		want  Security
	}{
		{"[WPA2-PSK-CCMP][ESS]", SecurityWPA},
		{"[WPA-PSK-CCMP][WPA2-PSK-CCMP][ESS]", SecurityWPA},
		{"[WPA2-EAP-CCMP][ESS]", SecurityEnterprise},
		{"[WPA2-EAP-CCMP][WPA2-PSK-CCMP][ESS]", SecurityEnterprise}, // EAP wins
		{"[WEP][ESS]", SecurityWEP},
		{"[ESS]", SecurityNone},
		{"", SecurityNone},
	}
	for _, tc := range cases {
		if got := classifySecurity(tc.flags); got != tc.want {
			t.Errorf("classifySecurity(%q) = %q, want %q", tc.flags, got, tc.want)
		}
	}
}

func TestParseScanResults(t *testing.T) {
	out := "bssid / frequency / signal level / flags / ssid\n" +
		"00:11:22:33:44:55\t2412\t-40\t[WPA2-PSK-CCMP][ESS]\tHomeNet\n" +
		"00:11:22:33:44:66\t2437\t-55\t[WPA2-EAP-CCMP][ESS]\tCorpNet\n" +
		"00:11:22:33:44:77\t2462\t-70\t[ESS]\tOpenNet\n" +
		"00:11:22:33:44:88\t2412\t-90\t[WPA2-PSK-CCMP][ESS]\tHomeNet\n" + // dup SSID
		"00:11:22:33:44:99\t2412\t-90\t[WPA2-PSK-CCMP][ESS]\t\n" // hidden

	got := parseScanResults(out)
	want := []Network{
		{SSID: "HomeNet", Security: SecurityWPA},
		{SSID: "CorpNet", Security: SecurityEnterprise},
		{SSID: "OpenNet", Security: SecurityNone},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseScanResults =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseScanResultsEmpty(t *testing.T) {
	got := parseScanResults("bssid / frequency / signal level / flags / ssid\n")
	if len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
	if got == nil {
		t.Error("expected non-nil empty slice")
	}
}
