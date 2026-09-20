package main

import (
	"example.com/edge-delta-lab/hubclient"
	"flag"
)

func hubTLSFlags(fs *flag.FlagSet) *hubclient.Config {
	c := &hubclient.Config{}
	fs.StringVar(&c.CA, "hub-ca", "", "PEM CA bundle for hub/registry TLS (system trust is retained)")
	fs.StringVar(&c.ClientCert, "hub-client-cert", "", "PEM client certificate for hub/registry mTLS")
	fs.StringVar(&c.ClientKey, "hub-client-key", "", "PEM client private key (0600 or stricter)")
	return c
}

func overlayHubTLS(dst *hubclient.Config, flags *hubclient.Config) {
	if flags.CA != "" {
		dst.CA = flags.CA
	}
	if flags.ClientCert != "" {
		dst.ClientCert = flags.ClientCert
	}
	if flags.ClientKey != "" {
		dst.ClientKey = flags.ClientKey
	}
}
