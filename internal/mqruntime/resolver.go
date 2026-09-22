package mqruntime

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/apache/rocketmq-client-go/v2/primitive"
)

// The SDK validates NameServer addresses as IP literals before dialing. Resolve
// Docker service names on every SDK refresh so container replacement can change
// their IP without putting ephemeral container addresses in Nacos.
type dnsResolver struct {
	config Config
	lookup func(context.Context, string, string) ([]net.IP, error)
}

func nameServerResolver(c Config) primitive.NsResolver {
	return &dnsResolver{config: c, lookup: net.DefaultResolver.LookupIP}
}

func (r *dnsResolver) Description() string { return "DNS resolver for " + r.config.Address() }

func (r *dnsResolver) Resolve() []string {
	if net.ParseIP(r.config.Host) != nil {
		return []string{r.config.Address()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := r.lookup(ctx, "ip4", r.config.Host)
	if err != nil {
		// Startup fails closed; on later refreshes the SDK retains its last list.
		return nil
	}
	addresses := make([]string, 0, len(ips))
	for _, ip := range ips {
		addresses = append(addresses, net.JoinHostPort(ip.String(), strconv.Itoa(r.config.Port)))
	}
	return addresses
}
