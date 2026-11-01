// A real, authenticated loopback TURN server for isolated integration tests.
// The private JSON config contains username and credential; never commit it.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/pion/turn/v5"
)

func main() {
	configPath := flag.String("config", "", "private JSON credential file")
	flag.Parse()
	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	var config struct {
		Username     string `json:"username"`
		Credential   string `json:"credential"`
		RelayAddress string `json:"relay_address"`
	}
	if json.Unmarshal(data, &config) != nil || config.Username == "" || config.Credential == "" {
		log.Fatal("username and credential are required")
	}
	if config.RelayAddress == "" {
		config.RelayAddress = "127.0.0.1"
	}
	relayIP := net.ParseIP(config.RelayAddress)
	if relayIP == nil || (!relayIP.IsPrivate() && !relayIP.IsLoopback()) {
		log.Fatal("relay_address must be a local test address")
	}
	listener, err := net.ListenPacket("udp4", "127.0.0.1:13478")
	if err != nil {
		log.Fatal(err)
	}
	const realm = "lalmax-ha-isolated-test"
	key := turn.GenerateAuthKey(config.Username, realm, config.Credential)
	server, err := turn.NewServer(turn.ServerConfig{
		Realm: realm,
		AuthHandler: func(request *turn.RequestAttributes) (string, []byte, bool) {
			return config.Username, key, request.Username == config.Username
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:        listener,
			PermissionHandler: func(_ net.Addr, peerIP net.IP) bool { return peerIP.Equal(relayIP) },
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: relayIP, Address: config.RelayAddress,
			},
		}},
	})
	if err != nil {
		_ = listener.Close()
		log.Fatal(err)
	}
	log.Print("isolated TURN listening on 127.0.0.1:13478/udp")
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	<-stopped
	if err := server.Close(); err != nil {
		log.Print(err)
	}
}
