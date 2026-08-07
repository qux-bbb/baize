// TLS 证书自动生成 — Server 首次启动时创建自签 CA + Server 证书
//
// 生成的证书（data-dir 下）：
//   ca.crt / ca.key      — 自签 CA（10 年有效期），Agent 侧内置 ca.crt 用于验证 Server
//   server.crt / server.key — Server 证书（SAN 含 localhost/127.0.0.1/public-addr/本机 IP）
//
// 证书存在则复用（幂等），删除 data-dir 对应文件可重新生成。
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	caNotAfterYears  = 10
	serverNotAfter   = 10 * 365 * 24 * time.Hour
)

// ensureTLS 确保 TLS 证书存在，返回 (serverCert, serverKey, caCert)
func ensureTLS(dataDir, publicAddr string) (string, string, string, error) {
	certFile := filepath.Join(dataDir, "server.crt")
	keyFile := filepath.Join(dataDir, "server.key")
	caFile := filepath.Join(dataDir, "ca.crt")
	caKeyFile := filepath.Join(dataDir, "ca.key")

	if fileExists(certFile) && fileExists(keyFile) && fileExists(caFile) {
		return certFile, keyFile, caFile, nil
	}

	log.Printf("[TLS] 未找到证书，正在生成自签 CA + Server 证书（data-dir: %s）...", dataDir)

	// ── CA ──────────────────────────────────────────────
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", "", fmt.Errorf("生成 CA 密钥失败: %w", err)
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "Baize EDR CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(caNotAfterYears, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", fmt.Errorf("生成 CA 证书失败: %w", err)
	}
	if err := writePEM(caFile, "CERTIFICATE", caDER); err != nil {
		return "", "", "", err
	}
	if err := writePEM(caKeyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey)); err != nil {
		return "", "", "", err
	}

	// ── Server 证书（SAN：localhost / 127.0.0.1 / public-addr / 本机非回环 IP）────
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", "", fmt.Errorf("生成 Server 密钥失败: %w", err)
	}
	var dnsNames []string
	var ipAddrs []net.IP
	seen := map[string]bool{}
	addHost := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		if ip := net.ParseIP(h); ip != nil {
			ipAddrs = append(ipAddrs, ip)
		} else {
			dnsNames = append(dnsNames, h)
		}
	}
	addHost("localhost")
	addHost("127.0.0.1")
	if u, err := url.Parse(publicAddr); err == nil && u.Hostname() != "" {
		addHost(u.Hostname())
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				addHost(ipn.IP.String())
			}
		}
	}
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano() + 1),
		Subject:      pkix.Name{CommonName: "Baize EDR Server"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(serverNotAfter),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ipAddrs,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, caTmpl, &serverKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", fmt.Errorf("生成 Server 证书失败: %w", err)
	}
	if err := writePEM(certFile, "CERTIFICATE", serverDER); err != nil {
		return "", "", "", err
	}
	if err := writePEM(keyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverKey)); err != nil {
		return "", "", "", err
	}

	log.Printf("[TLS] 证书已生成: %s（Agent 需配置 ca 指向 ca.crt）", caFile)
	return certFile, keyFile, caFile, nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func writePEM(path, blockType string, der []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		return fmt.Errorf("PEM 编码 %s 失败: %w", path, err)
	}
	return nil
}
