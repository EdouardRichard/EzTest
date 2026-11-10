package platform

import (
	"net/http"
	"testing"
)

func TestRemoteHostParsing(t *testing.T) {
	cases := []struct {
		remoteAddr string
		want       string
	}{
		{"192.0.2.1:1234", "192.0.2.1"},
		{"192.0.2.1", "192.0.2.1"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"2001:db8::1", "2001:db8::1"},
		{"2001:0DB8:0:0::1", "2001:db8::1"},
		{"[fe80::1%eth0]:80", "fe80::1%eth0"},
		{" 192.0.2.1:80 ", "192.0.2.1"},
		{"", ""},
		{"not-an-address", "not-an-address"},
	}
	for _, tc := range cases {
		if got := remoteHost(tc.remoteAddr); got != tc.want {
			t.Errorf("remoteHost(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
		}
	}
}

func TestRequestMetaFor(t *testing.T) {
	if got := RequestMetaFor(nil); got != (RequestMeta{}) {
		t.Fatalf("RequestMetaFor(nil) = %#v", got)
	}
	req, err := http.NewRequest(http.MethodPost, "/api/admin/settings", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.RemoteAddr = "[2001:db8::1]:443"
	req.Header.Set("User-Agent", "  vitest-agent  ")
	meta := RequestMetaFor(req)
	if meta.IP != "2001:db8::1" {
		t.Fatalf("ip = %q, want 2001:db8::1", meta.IP)
	}
	if meta.UserAgent != "vitest-agent" {
		t.Fatalf("user agent = %q, want trimmed vitest-agent", meta.UserAgent)
	}
}
