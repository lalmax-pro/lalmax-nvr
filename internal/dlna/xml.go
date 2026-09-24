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
	URLBase     string      `xml:"URLBase,omitempty"`
	Device      deviceInfo  `xml:"device"`
}
type deviceInfo struct {
	DeviceType       string      `xml:"deviceType"`
	FriendlyName     string      `xml:"friendlyName"`
	Manufacturer     string      `xml:"manufacturer"`
	ManufacturerURL  string      `xml:"manufacturerURL,omitempty"`
	ModelDescription string      `xml:"modelDescription,omitempty"`
	ModelName        string      `xml:"modelName"`
	ModelNumber      string      `xml:"modelNumber,omitempty"`
	SerialNumber     string      `xml:"serialNumber,omitempty"`
	UDN              string      `xml:"UDN"`
	DLNADoc          string      `xml:",innerxml"`
	ServiceList      serviceList `xml:"serviceList"`
}
type serviceList struct {
	Services []serviceInfo `xml:"service"`
}
type serviceInfo struct {
	ServiceType string `xml:"serviceType"`
	ServiceID   string `xml:"serviceId,omitempty"`
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

func arg(name, dir, rel string) argument {
	return argument{Name: name, Direction: dir, RelatedStateVariable: rel}
}
func act(name string, args ...argument) action {
	a := action{Name: name}
	if len(args) > 0 {
		a.ArgumentList = &argumentList{Arguments: args}
	}
	return a
}
func svar(events, name, typ string) stateVariable {
	return stateVariable{SendEvents: events, Name: name, DataType: typ}
}

func contentSCPDXML() scpdXML {
	return scpdXML{
		XMLNS:       "urn:schemas-upnp-org:service-1-0",
		SpecVersion: specVersion{Major: 1, Minor: 0},
		ActionList: actionList{Actions: []action{
			act("Browse",
				arg("ObjectID", "in", "A_ARG_TYPE_ObjectID"),
				arg("BrowseFlag", "in", "A_ARG_TYPE_BrowseFlag"),
				arg("Filter", "in", "A_ARG_TYPE_Filter"),
				arg("StartingIndex", "in", "A_ARG_TYPE_Index"),
				arg("RequestedCount", "in", "A_ARG_TYPE_Count"),
				arg("SortCriteria", "in", "A_ARG_TYPE_SortCriteria"),
				arg("Result", "out", "A_ARG_TYPE_Result"),
				arg("NumberReturned", "out", "A_ARG_TYPE_Count"),
				arg("TotalMatches", "out", "A_ARG_TYPE_Count"),
				arg("UpdateID", "out", "A_ARG_TYPE_UpdateID"),
			),
			act("GetSearchCapabilities", arg("SearchCaps", "out", "SearchCapabilities")),
			act("GetSortCapabilities", arg("SortCaps", "out", "SortCapabilities")),
			act("GetSystemUpdateID", arg("Id", "out", "SystemUpdateID")),
		}},
		ServiceStateTable: stateTable{Variables: []stateVariable{
			svar("no", "A_ARG_TYPE_ObjectID", "string"),
			svar("no", "A_ARG_TYPE_Result", "string"),
			svar("no", "A_ARG_TYPE_BrowseFlag", "string"),
			svar("no", "A_ARG_TYPE_Filter", "string"),
			svar("no", "A_ARG_TYPE_SortCriteria", "string"),
			svar("no", "A_ARG_TYPE_Index", "ui4"),
			svar("no", "A_ARG_TYPE_Count", "ui4"),
			svar("no", "A_ARG_TYPE_UpdateID", "ui4"),
			svar("no", "SearchCapabilities", "string"),
			svar("no", "SortCapabilities", "string"),
			svar("yes", "SystemUpdateID", "ui4"),
		}},
	}
}
func connectionSCPDXML() scpdXML {
	return scpdXML{
		XMLNS:       "urn:schemas-upnp-org:service-1-0",
		SpecVersion: specVersion{Major: 1, Minor: 0},
		ActionList: actionList{Actions: []action{
			act("GetProtocolInfo", arg("Source", "out", "SourceProtocolInfo"), arg("Sink", "out", "SinkProtocolInfo")),
			act("GetCurrentConnectionIDs", arg("ConnectionIDs", "out", "CurrentConnectionIDs")),
			act("GetCurrentConnectionInfo",
				arg("ConnectionID", "in", "A_ARG_TYPE_ConnectionID"),
				arg("RcsID", "out", "A_ARG_TYPE_RcsID"),
				arg("AVTransportID", "out", "A_ARG_TYPE_AVTransportID"),
				arg("ProtocolInfo", "out", "A_ARG_TYPE_ProtocolInfo"),
				arg("PeerConnectionManager", "out", "A_ARG_TYPE_ConnectionManager"),
				arg("PeerConnectionID", "out", "A_ARG_TYPE_ConnectionID"),
				arg("Direction", "out", "A_ARG_TYPE_Direction"),
				arg("Status", "out", "A_ARG_TYPE_ConnectionStatus"),
			),
		}},
		ServiceStateTable: stateTable{Variables: []stateVariable{
			svar("no", "SourceProtocolInfo", "string"),
			svar("no", "SinkProtocolInfo", "string"),
			svar("no", "CurrentConnectionIDs", "string"),
			svar("no", "A_ARG_TYPE_ConnectionID", "i4"),
			svar("no", "A_ARG_TYPE_RcsID", "i4"),
			svar("no", "A_ARG_TYPE_AVTransportID", "i4"),
			svar("no", "A_ARG_TYPE_ProtocolInfo", "string"),
			svar("no", "A_ARG_TYPE_ConnectionManager", "string"),
			svar("no", "A_ARG_TYPE_Direction", "string"),
			svar("no", "A_ARG_TYPE_ConnectionStatus", "string"),
		}},
	}
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
