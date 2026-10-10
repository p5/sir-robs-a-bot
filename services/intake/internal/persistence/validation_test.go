package persistence

import (
	"net"
	"net/url"
	"testing"
)

func FuzzEndpointOrigin(f *testing.F) {
	for _, endpoint := range []string{"", "https://s3.example.com", "http://127.0.0.1:8000", "http://user:secret@127.0.0.1/", "https://example.com/path"} {
		f.Add(endpoint)
	}
	f.Fuzz(func(t *testing.T, endpoint string) {
		if len(endpoint) > 8192 || endpoint == "" || validateEndpoint(endpoint) != nil {
			return
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatal("unsafe accepted endpoint")
		}
		if parsed.Scheme != "https" {
			ip := net.ParseIP(parsed.Hostname())
			if parsed.Scheme != "http" || ip == nil || !ip.IsLoopback() {
				t.Fatal("insecure accepted endpoint")
			}
		}
	})
}
