// SFU local development server. Run: go run ./tools/sfu-meet
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/livekitsfu"
)

func randomID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func run() error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	lkTCP, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := lkTCP.Addr().(*net.TCPAddr).Port
	if err := lkTCP.Close(); err != nil {
		return err
	}
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	if err := udp.Close(); err != nil {
		return err
	}
	launchSecret, err := randomID()
	if err != nil {
		return err
	}
	key, err := randomID()
	if err != nil {
		return err
	}
	secret, err := randomID()
	if err != nil {
		return err
	}
	lkURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	yaml := fmt.Sprintf("development: true\nport: %d\nbind_addresses: [127.0.0.1]\nkeys:\n  %s: %s\nrtc:\n  node_ip: 127.0.0.1\n  enable_loopback_candidate: true\n  ips:\n    includes: [127.0.0.1/32]\n  tcp_port: 0\n  port_range_start: 0\n  port_range_end: 0\n  udp_port: %d\n", port, key, secret, udpPort)
	server, err := livekitsfu.NewServer(yaml)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- livekitsfu.RunPrepared(ctx, server) }()
	defer func() { stop(); <-done }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	for !server.IsRunning() {
		select {
		case err := <-done:
			// Keep the deferred receiver from blocking after an early exit.
			done <- err
			if err == nil {
				return errors.New("LiveKit stopped during startup")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return errors.New("LiveKit startup timed out")
		case <-ticker.C:
		}
	}
	origin := "http://" + listener.Addr().String()
	handler, err := newLiveKitDemoHandler(origin, launchSecret, lkURL, key, secret)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("SFU demo: %s\nAccess token: %s\n", origin, launchSecret)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err = srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
