package onvif

import (
	"strings"
	"testing"
)

func TestParseHelloXML(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing">
  <s:Body>
    <d:Hello>
      <a:EndpointReference><a:Address>urn:uuid:hello-1</a:Address></a:EndpointReference>
      <d:Types>dn:NetworkVideoTransmitter</d:Types>
      <d:Scopes>onvif://www.onvif.org/name/FrontDoor onvif://www.onvif.org/hardware/IPC</d:Scopes>
      <d:XAddrs>http://192.168.8.20/onvif/device_service</d:XAddrs>
    </d:Hello>
  </s:Body>
</s:Envelope>`
	dev, err := parseProbeMatchResponse([]byte(xml), "192.168.8.20:3702")
	if err != nil {
		t.Fatal(err)
	}
	if dev == nil {
		t.Fatal("expected device from Hello")
	}
	if !strings.Contains(dev.Endpoint, "192.168.8.20") {
		t.Fatalf("endpoint=%q", dev.Endpoint)
	}
	if dev.Name != "FrontDoor" {
		t.Fatalf("name=%q", dev.Name)
	}
	if dev.UUID != "urn:uuid:hello-1" {
		t.Fatalf("uuid=%q", dev.UUID)
	}
}
