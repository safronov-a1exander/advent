package rates

import (
	"net"
	"net/url"
)

func netListen(u string) (net.Listener, error) {
	p, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	return net.Listen("tcp", p.Host)
}
