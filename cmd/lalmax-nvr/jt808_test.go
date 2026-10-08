package main

import (
	"github.com/lalmax-pro/lalmax-nvr/internal/jt808"
	"net"
	"strings"
	"testing"
)

func TestAppStartReturnsJT808ListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	svr, err := jt808.NewServer(jt808.Config{Enabled: true, Port: ln.Addr().(*net.TCPAddr).Port, MediaIP: "127.0.0.1", AuthCode: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{jt808Svr: svr}
	if err := app.Start(); err == nil || !strings.Contains(err.Error(), "start JT808 signaling") {
		t.Fatalf("Start error=%v", err)
	}
	if app.startCtx.Err() == nil {
		t.Fatal("failed startup context not canceled")
	}
}
