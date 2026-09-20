// Package httptransport constructs independently owned outbound transports.
package httptransport

import (
	"net"
	"net/http"
	"time"
)

// New uses net/http's standard transport defaults without reading the mutable
// http.DefaultTransport or http.DefaultClient. Each caller owns its connection
// pool. Environment proxy settings are intentionally honored; host TLS identity,
// custom dialers, proxy credentials, and trust overrides are never inherited.
// A nil TLSClientConfig uses system trust without a client certificate.
func New() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}
