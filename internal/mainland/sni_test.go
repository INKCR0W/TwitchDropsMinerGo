package mainland

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

func TestBenignSNI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  dohResult
		want string
	}{
		{"fastly", dohResult{CNAMEs: []string{"h2.twitch.map.fastly.net"}}, "h2.twitch.map.fastly.net"},
		{"elb 链含 twitch.tv 中间跳", dohResult{CNAMEs: []string{"spade.sci.twitch.tv", "science-edge.us-west-2.elb.amazonaws.com"}}, "science-edge.us-west-2.elb.amazonaws.com"},
		{"无 CNAME", dohResult{}, ""},
		{"全部含 twitch.tv", dohResult{CNAMEs: []string{"a.twitch.tv", "b.twitch.tv"}}, ""},
		{"大小写混合仍识别", dohResult{CNAMEs: []string{"X.TWITCH.TV", "edge.fastly.net"}}, "edge.fastly.net"},
	}
	for _, c := range cases {
		if got := benignSNI(c.res); got != c.want {
			t.Fatalf("%s: benignSNI=%q, want %q", c.name, got, c.want)
		}
	}
}

func TestVerifyPeerCertificatesRejectsWrongHost(t *testing.T) {
	t.Parallel()
	// 自签一张 CN=example.com 的证书, 用它自己当根.
	roots, leaf := selfSignedFor(t, "example.com")

	if err := verifyPeerCertificates([]*x509.Certificate{leaf}, "example.com", roots); err != nil {
		t.Fatalf("对签发域名应通过: %v", err)
	}
	if err := verifyPeerCertificates([]*x509.Certificate{leaf}, "id.twitch.tv", roots); err == nil {
		t.Fatal("对不匹配域名必须拒绝(伪造 IP 场景)")
	}
}

func TestVerifyConnectionWiring(t *testing.T) {
	t.Parallel()
	// 确认 VerifyConnection 闭包在 cs.PeerCertificates 为空时安全报错, 不 panic.
	cfg := tlsConfigFor("id.twitch.tv", "")
	if cfg.VerifyConnection == nil || !cfg.InsecureSkipVerify {
		t.Fatal("必须 InsecureSkipVerify=true 且提供 VerifyConnection")
	}
	if err := cfg.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Fatal("空证书链必须报错")
	}
}

func selfSignedFor(t *testing.T, host string) (*x509.CertPool, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool, cert
}
