// Package testtls provides ephemeral test-only mTLS identities.
package testtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"example.com/edge-delta-lab/hubclient"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type Material struct {
	Client hubclient.Config
	Server *tls.Config
}

func New(t testing.TB) Material {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, []byte) {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		d, e := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, key)
		if e != nil {
			t.Fatal(e)
		}
		kb, e := x509.MarshalECPrivateKey(k)
		if e != nil {
			t.Fatal(e)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	}
	serverCert, serverKey := issue(2, x509.ExtKeyUsageServerAuth)
	pair, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, clientKey := issue(3, x509.ExtKeyUsageClientAuth)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	return Material{Client: hubclient.Config{CA: write("ca.pem", caPEM), ClientCert: write("client.pem", clientCert), ClientKey: write("client.key", clientKey)}, Server: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}}
}
