package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// loadOrCreateCert loads the configured pair, or generates a self-signed one
// when both files are missing. Dropping in real files (e.g. Let's Encrypt)
// and restarting is all it takes to switch.
func loadOrCreateCert(certFile, keyFile string, extraNames []string) (tls.Certificate, error) {
	_, errC := os.Stat(certFile)
	_, errK := os.Stat(keyFile)
	if errC == nil && errK == nil {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}
	if !errors.Is(errC, fs.ErrNotExist) || !errors.Is(errK, fs.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("need both %s and %s, or neither to generate them", certFile, keyFile)
	}

	certPEM, keyPEM, err := selfSigned(extraNames, time.Now())
	if err != nil {
		return tls.Certificate{}, err
	}
	for _, f := range []string{certFile, keyFile} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			return tls.Certificate{}, err
		}
	}
	if err := writeNew(keyFile, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := writeNew(certFile, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	slog.Info("generated a self-signed certificate", "cert", certFile)
	return tls.X509KeyPair(certPEM, keyPEM)
}

func writeNew(name string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func selfSigned(extraNames []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	var names []string
	for _, n := range append([]string{host, host + ".local", "localhost"}, extraNames...) {
		if n != "" && n != ".local" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	// 10.42.0.1 is NetworkManager's hotspot address; the rest is whatever the
	// Pi has right now. Browsers warn about a self-signed cert either way.
	ips := []net.IP{net.ParseIP("10.42.0.1"), net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() &&
				!slices.ContainsFunc(ips, ipn.IP.Equal) {
				ips = append(ips, ipn.IP)
			}
		}
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(0, 0, 825),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}
