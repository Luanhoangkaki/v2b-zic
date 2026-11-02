package dispatcher

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
)

type overrideTestResult struct {
	domain string
}

func (r overrideTestResult) Domain() string   { return r.domain }
func (r overrideTestResult) Protocol() string { return "tls" }

func TestShouldOverrideDomainExclusions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domain  string
		exclude bool
		want    bool
	}{
		{"no exclusions", "excluded.example", false, true},
		{"full match case insensitive", "EXCLUDED.EXAMPLE", true, false},
		{"regexp match", "private.example", true, false},
		{"unmatched domain", "public.example", true, true},
		{"full match does not exclude subdomain", "sub.excluded.example", true, true},
		{"empty domain", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := session.SniffingRequest{OverrideDestinationForProtocol: []string{"tls"}}
			if tc.exclude {
				request.ExcludeForDomain = []string{"excluded.example", `regexp:^private\.`}
			}
			d := &DefaultDispatcher{}
			destination := net.TCPDestination(net.DomainAddress("origin.example"), 443)
			if got := d.shouldOverride(context.Background(), overrideTestResult{tc.domain}, request, destination); got != tc.want {
				t.Fatalf("shouldOverride() = %v, want %v", got, tc.want)
			}
		})
	}
}
