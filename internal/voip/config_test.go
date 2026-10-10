package voip

import "testing"

func TestConfigKeepsTlsSettings(t *testing.T) {
	cfg := Config{
		SipTlsListenAddr:  "0.0.0.0:5061",
		SipDtlsListenAddr: "0.0.0.0:5061",
		SipTlsCertFile:    "cert.pem",
		SipTlsKeyFile:     "key.pem",
		BundleEnable:      true,
	}

	cfg.Normalize()

	if cfg.SipTlsListenAddr != "0.0.0.0:5061" {
		t.Fatalf("SipTlsListenAddr = %q", cfg.SipTlsListenAddr)
	}
	if cfg.SipDtlsListenAddr != "0.0.0.0:5061" {
		t.Fatalf("SipDtlsListenAddr = %q", cfg.SipDtlsListenAddr)
	}
	if cfg.SipTlsCertFile != "cert.pem" {
		t.Fatalf("SipTlsCertFile = %q", cfg.SipTlsCertFile)
	}
	if cfg.SipTlsKeyFile != "key.pem" {
		t.Fatalf("SipTlsKeyFile = %q", cfg.SipTlsKeyFile)
	}
	if !cfg.BundleEnable {
		t.Fatalf("BundleEnable = false")
	}
}
