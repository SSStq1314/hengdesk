package tlsinfo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mk(t *testing.T, notAfter time.Time, parent *x509.Certificate, pk *ecdsa.PrivateKey) ([]byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "hd.local"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, DNSNames: []string{"hd.local"},
		IPAddresses: []net.IP{net.ParseIP("10.0.0.5")}, IsCA: parent == nil, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	if parent == nil {
		parent, pk = tpl, k
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, &k.PublicKey, pk)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), c, k
}

func TestParse(t *testing.T) {
	now := time.Now()
	self, ca, caKey := mk(t, now.Add(365*24*time.Hour), nil, nil)
	i, err := Parse(self, now)
	if err != nil || !i.Present || !i.SelfSigned || i.Expiring || i.DaysLeft < 364 || len(i.SANs) != 2 {
		t.Fatalf("self %+v %v", i, err)
	}
	leaf, _, _ := mk(t, now.Add(10*24*time.Hour), ca, caKey)
	i, _ = Parse(append([]byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), leaf...), now)
	if i.SelfSigned || !i.Expiring || i.Expired || i.DaysLeft != 9 {
		t.Fatalf("leaf %+v", i)
	}
	old, _, _ := mk(t, now.Add(-time.Hour), nil, nil)
	if i, _ = Parse(old, now); !i.Expired || !i.Expiring {
		t.Fatalf("expired %+v", i)
	}
	if _, err := Parse([]byte("nope"), now); err == nil {
		t.Fatal("want error")
	}
	if i, err := Inspect(filepath.Join(t.TempDir(), "none.crt"), now); err != nil || i.Present {
		t.Fatal("missing file must be absent")
	}
	p := filepath.Join(t.TempDir(), "c.crt")
	_ = os.WriteFile(p, self, 0o644)
	if i, err := Inspect(p, now); err != nil || !i.SelfSigned {
		t.Fatal(i, err)
	}
}
