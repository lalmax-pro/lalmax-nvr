package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"
)

// GetSystemDateAndTime retrieves the device's system date and time.
func (c *Client) GetSystemDateAndTime(ctx context.Context) (time.Time, error) {
	body := `<GetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := c.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: c.endpoint,
		Body:       body,
		NoAuth:     true, // GetSystemDateAndTime doesn't require authentication
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("onvif: GetSystemDateAndTime failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetSystemDateAndTimeResponse"`
		SystemDateAndTime struct {
			UTCDateTime *struct {
				Time struct {
					Hour   int `xml:"Hour"`
					Minute int `xml:"Minute"`
					Second int `xml:"Second"`
				} `xml:"Time"`
				Date struct {
					Year  int `xml:"Year"`
					Month int `xml:"Month"`
					Day   int `xml:"Day"`
				} `xml:"Date"`
			} `xml:"UTCDateTime"`
		} `xml:"SystemDateAndTime"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return time.Time{}, err
	}

    if result.SystemDateAndTime.UTCDateTime == nil {
        return time.Time{}, fmt.Errorf("onvif: GetSystemDateAndTime response did not contain UTC time")
    }

	t := time.Date(
		result.SystemDateAndTime.UTCDateTime.Date.Year,
		time.Month(result.SystemDateAndTime.UTCDateTime.Date.Month),
		result.SystemDateAndTime.UTCDateTime.Date.Day,
		result.SystemDateAndTime.UTCDateTime.Time.Hour,
		result.SystemDateAndTime.UTCDateTime.Time.Minute,
		result.SystemDateAndTime.UTCDateTime.Time.Second,
		0, time.UTC,
	)

	return t, nil
}

// GetServices retrieves the list of services from the device.
func (c *Client) GetServices(ctx context.Context) ([]Service, error) {
	body := `<GetServices xmlns="http://www.onvif.org/ver10/device/wsdl">
  <IncludeCapability>false</IncludeCapability>
</GetServices>`

	resp, err := c.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: c.endpoint,
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetServices failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetServicesResponse"`
		Service []struct {
			Namespace string `xml:"Namespace"`
			XAddr     string `xml:"XAddr"`
			Version   struct {
				Major int `xml:"Major"`
				Minor int `xml:"Minor"`
			} `xml:"Version"`
		} `xml:"Service"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	services := make([]Service, 0, len(result.Service))
	for _, svc := range result.Service {
		services = append(services, Service{
			Namespace: svc.Namespace,
			XAddr:     svc.XAddr,
			Version:   fmt.Sprintf("%d.%d", svc.Version.Major, svc.Version.Minor),
		})
	}

	return services, nil
}

// GetCapabilities retrieves device capabilities.
func (c *Client) GetCapabilities(ctx context.Context) (*Capabilities, error) {
	body := `<GetCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl">
  <Category>All</Category>
</GetCapabilities>`

	resp, err := c.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: c.endpoint,
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetCapabilities failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetCapabilitiesResponse"`
		Capabilities struct {
			Device struct {
				XAddr string `xml:"XAddr"`
			} `xml:"Device"`
			Media struct {
				XAddr string `xml:"XAddr"`
			} `xml:"Media"`
			Recording struct {
				XAddr string `xml:"XAddr"`
			} `xml:"Recording"`
			Search struct {
				XAddr string `xml:"XAddr"`
			} `xml:"Search"`
			Replay struct {
				XAddr string `xml:"XAddr"`
			} `xml:"Replay"`
			PTZ struct {
				XAddr string `xml:"XAddr"`
			} `xml:"PTZ"`
			Imaging struct {
				XAddr string `xml:"XAddr"`
			} `xml:"Imaging"`
			Events struct {
				XAddr string `xml:"XAddr"`
			} `xml:"Events"`
			Extension struct {
				Media2 struct {
					XAddr string `xml:"XAddr"`
				} `xml:"Media2"`
			} `xml:"Extension"`
		} `xml:"Capabilities"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	caps := &Capabilities{}

	if result.Capabilities.Device.XAddr != "" {
		caps.Device = &DeviceCapabilities{XAddr: result.Capabilities.Device.XAddr}
	}
	if result.Capabilities.Media.XAddr != "" {
		caps.Media = &MediaCapabilities{XAddr: result.Capabilities.Media.XAddr}
	}
	if result.Capabilities.Extension.Media2.XAddr != "" {
		caps.Media2 = &MediaCapabilities{XAddr: result.Capabilities.Extension.Media2.XAddr}
	}
	if result.Capabilities.Recording.XAddr != "" {
		caps.Recording = &RecordingCapabilities{XAddr: result.Capabilities.Recording.XAddr}
	}
	if result.Capabilities.Search.XAddr != "" {
		caps.Search = &SearchCapabilities{XAddr: result.Capabilities.Search.XAddr}
	}
	if result.Capabilities.Replay.XAddr != "" {
		caps.Replay = &ReplayCapabilities{XAddr: result.Capabilities.Replay.XAddr}
	}
	if result.Capabilities.PTZ.XAddr != "" {
		caps.PTZ = &PTZCapabilities{XAddr: result.Capabilities.PTZ.XAddr}
	}
	if result.Capabilities.Imaging.XAddr != "" {
		caps.Imaging = &ImagingCapabilities{XAddr: result.Capabilities.Imaging.XAddr}
	}
	if result.Capabilities.Events.XAddr != "" {
		caps.Events = &EventsCapabilities{XAddr: result.Capabilities.Events.XAddr}
	}

	return caps, nil
}

// GetDeviceInformation retrieves device information.
func (c *Client) GetDeviceInformation(ctx context.Context) (*DeviceInfo, error) {
	if c.info != nil {
		return c.info, nil
	}

	body := `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`

	resp, err := c.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: c.endpoint,
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetDeviceInformation failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetDeviceInformationResponse"`
		Manufacturer string `xml:"Manufacturer"`
		Model        string `xml:"Model"`
		FirmwareVersion string `xml:"FirmwareVersion"`
		SerialNumber    string `xml:"SerialNumber"`
		HardwareId      string `xml:"HardwareId"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	c.info = &DeviceInfo{
		Manufacturer:    result.Manufacturer,
		Model:           result.Model,
		FirmwareVersion: result.FirmwareVersion,
		SerialNumber:    result.SerialNumber,
		HardwareId:      result.HardwareId,
	}

	return c.info, nil
}
