package mainland

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
)

const blockedSuffix = "twitch.tv"

// benignSNI 返回 CNAME 链中最深、且不含 "twitch.tv" 的基础设施域名; 无则空.
func benignSNI(res dohResult) string {
	sni := ""
	for _, cn := range res.CNAMEs {
		if !strings.Contains(strings.ToLower(cn), blockedSuffix) {
			sni = cn
		}
	}
	return sni
}

func verifyPeerCertificates(certs []*x509.Certificate, host string, roots *x509.CertPool) error {
	if len(certs) == 0 {
		return fmt.Errorf("对端未提供证书")
	}
	opts := x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots, // nil = 系统根
		Intermediates: x509.NewCertPool(),
	}
	for _, c := range certs[1:] {
		opts.Intermediates.AddCert(c)
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return fmt.Errorf("证书对 %s 校验失败: %w", host, err)
	}
	return nil
}

// tlsConfigFor 构造对 host 的良性-SNI + 自校验配置. sni 为空即空 SNI.
func tlsConfigFor(host, sni string) *tls.Config {
	return &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, // 配 VerifyConnection 使用; 二者必须成对
		NextProtos:         []string{"h2", "http/1.1"},
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyPeerCertificates(cs.PeerCertificates, host, nil)
		},
	}
}
