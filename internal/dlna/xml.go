package dlna

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

type specVersion struct {
	Major int `xml:"major"`
	Minor int `xml:"minor"`
}
type deviceXML struct {
	XMLName     xml.Name    `xml:"root"`
	XMLNS       string      `xml:"xmlns,attr"`
	SpecVersion specVersion `xml:"specVersion"`
	Device      deviceInfo  `xml:"device"`
}
type deviceInfo struct {
	DeviceType   string      `xml:"deviceType"`
	FriendlyName string      `xml:"friendlyName"`
	Manufacturer string      `xml:"manufacturer"`
	ModelName    string      `xml:"modelName"`
	UDN          string      `xml:"UDN"`
	ServiceList  serviceList `xml:"serviceList"`
}
type serviceList struct {
	Services []serviceInfo `xml:"service"`
}
type serviceInfo struct {
	ServiceType string `xml:"serviceType"`
	SCPDURL     string `xml:"SCPDURL"`
	ControlURL  string `xml:"controlURL"`
	EventSubURL string `xml:"eventSubURL"`
}

type scpdXML struct {
	XMLName           xml.Name    `xml:"scpd"`
	XMLNS             string      `xml:"xmlns,attr"`
	SpecVersion       specVersion `xml:"specVersion"`
	ActionList        actionList  `xml:"actionList"`
	ServiceStateTable stateTable  `xml:"serviceStateTable"`
}
type actionList struct {
	Actions []action `xml:"action"`
}
type action struct {
	Name         string        `xml:"name"`
	ArgumentList *argumentList `xml:"argumentList,omitempty"`
}
type argumentList struct {
	Arguments []argument `xml:"argument"`
}
type argument struct {
	Name                 string `xml:"name"`
	Direction            string `xml:"direction"`
	RelatedStateVariable string `xml:"relatedStateVariable"`
}
type stateTable struct {
	Variables []stateVariable `xml:"stateVariable"`
}
type stateVariable struct {
	SendEvents   string `xml:"sendEvents,attr,omitempty"`
	Name         string `xml:"name"`
	DataType     string `xml:"dataType"`
	DefaultValue string `xml:"defaultValue,omitempty"`
}

func contentSCPDXML() scpdXML {
	return scpd([]string{"Browse", "GetSearchCapabilities", "GetSortCapabilities", "GetSystemUpdateID"}, []stateVariable{{SendEvents: "no", Name: "A_ARG_TYPE_ObjectID", DataType: "string"}, {SendEvents: "no", Name: "A_ARG_TYPE_Result", DataType: "string"}, {SendEvents: "no", Name: "A_ARG_TYPE_BrowseFlag", DataType: "string"}, {SendEvents: "no", Name: "A_ARG_TYPE_Filter", DataType: "string"}, {SendEvents: "no", Name: "A_ARG_TYPE_SortCriteria", DataType: "string"}, {SendEvents: "no", Name: "A_ARG_TYPE_SearchCriteria", DataType: "string"}, {SendEvents: "no", Name: "SystemUpdateID", DataType: "ui4"}})
}
func connectionSCPDXML() scpdXML {
	return scpd([]string{"GetProtocolInfo"}, []stateVariable{{SendEvents: "no", Name: "SourceProtocolInfo", DataType: "string"}, {SendEvents: "no", Name: "SinkProtocolInfo", DataType: "string"}})
}
func scpd(names []string, vars []stateVariable) scpdXML {
	a := make([]action, 0, len(names))
	for _, n := range names {
		a = append(a, action{Name: n})
	}
	return scpdXML{XMLNS: "urn:schemas-upnp-org:service-1-0", SpecVersion: specVersion{1, 0}, ActionList: actionList{a}, ServiceStateTable: stateTable{vars}}
}

func soapResponse(w http.ResponseWriter, name string, values map[string]string) {
	soapResponseService(w, contentDirectoryType, name, values)
}

func soapResponseService(w http.ResponseWriter, serviceType, name string, values map[string]string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:` + name + ` xmlns:u="` + serviceType + `">`)
	for k, v := range values {
		b.WriteString("<" + k + ">" + xmlEscape(v) + "</" + k + ">")
	}
	b.WriteString("</u:" + name + "></s:Body></s:Envelope>")
	_, _ = w.Write([]byte(b.String()))
}
func soapFault(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode><errorDescription>%s</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code, xmlEscape(desc))
}
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

type didlObject struct {
	ID, Parent, Title, Class, URL, Protocol string
	Size                                    int64
	Duration                                float64
}

func didl(items []didlObject) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`)
	for _, it := range items {
		tag := "item"
		if strings.HasPrefix(it.Class, "object.container") {
			tag = "container"
		}
		b.WriteString(`<` + tag + ` id="` + xmlEscape(it.ID) + `" parentID="` + xmlEscape(it.Parent) + `" restricted="1">`)
		b.WriteString(`<dc:title>` + xmlEscape(it.Title) + `</dc:title><upnp:class>` + xmlEscape(it.Class) + `</upnp:class>`)
		if it.URL != "" {
			b.WriteString(`<res protocolInfo="` + xmlEscape(it.Protocol) + `"`)
			if it.Size > 0 {
				b.WriteString(fmt.Sprintf(` size="%d"`, it.Size))
			}
			b.WriteString(`>` + xmlEscape(it.URL) + `</res>`)
		}
		b.WriteString(`</` + tag + `>`)
	}
	b.WriteString(`</DIDL-Lite>`)
	return b.String()
}
