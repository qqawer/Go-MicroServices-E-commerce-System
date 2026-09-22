package mqruntime

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/apache/rocketmq-client-go/v2/primitive"
)

func TestNameServerHostnameWorksWithSDK(t *testing.T) {
	// AWS reproduced "IP addr error" when a Docker hostname was passed directly.
	if (primitive.NamesrvAddr{"mqnamesrv:9876"}).Check() == nil {
		t.Fatal("SDK hostname behavior changed; reconsider the DNS adapter")
	}
	addresses := nameServerResolver(Config{Host: "localhost", Port: 9876}).Resolve()
	if len(addresses) == 0 {
		t.Fatal("localhost did not resolve")
	}
	if err := primitive.NamesrvAddr(addresses).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestNameServerRefreshUsesNewDNSAndBoundsFailures(t *testing.T) {
	current := "172.18.0.2"
	r := &dnsResolver{config: Config{Host: "mqnamesrv", Port: 9876}, lookup: func(ctx context.Context, network, host string) ([]net.IP, error) {
		if _, ok := ctx.Deadline(); !ok || host != "mqnamesrv" || network != "ip4" {
			t.Fatal("DNS lookup must use the configured host and a deadline")
		}
		if current == "" {
			return nil, errors.New("DNS unavailable")
		}
		return []net.IP{net.ParseIP(current)}, nil
	}}
	if got := r.Resolve(); len(got) != 1 || got[0] != "172.18.0.2:9876" {
		t.Fatal(got)
	}
	current = "172.18.0.9"
	if got := r.Resolve(); len(got) != 1 || got[0] != "172.18.0.9:9876" {
		t.Fatal("container replacement must update the resolved address", got)
	}
	current = ""
	if len(r.Resolve()) != 0 {
		t.Fatal("failed DNS must not publish an invalid address")
	}
}
