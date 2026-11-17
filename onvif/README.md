# ONVIF Go Library

A pure Go implementation of the ONVIF protocol for IP camera communication. No external ONVIF dependencies required.

## Features

| Service | Operations | Status |
|---------|-----------|--------|
| **Device** | 25+ operations | ✅ Complete |
| **Media** | 35+ operations | ✅ Complete |
| **PTZ** | 15 operations | ✅ Complete |
| **Imaging** | 10 operations | ✅ Complete |
| **Events** | PullPoint and service capabilities | |
| **Recording** | 15+ operations | ✅ Complete |
| **Replay** | 1 operation | ✅ Complete |
| **Discovery** | HTTP Probe | ✅ Complete |

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "github.com/lalmax-pro/lalmax-nvr/onvif"
)

func main() {
    client, _ := onvif.NewClient("http://192.168.1.100/onvif/device_service", "admin", "password")
    client.Connect(context.Background())

    info, _ := client.GetDeviceInformation(context.Background())
    fmt.Printf("Device: %s %s\n", info.Manufacturer, info.Model)
}
```

## Service Reference

### Device Service

```go
// Device Information
info, err := client.GetDeviceInformation(ctx)

// Service Discovery
services, err := client.GetServices(ctx)
caps, err := client.GetCapabilities(ctx)

// Device Manager
mgr := client.DeviceManager()
mgr.SystemReboot(ctx)
mgr.GetNetworkInterfaces(ctx)
mgr.GetUsers(ctx)
mgr.CreateUsers(ctx, users)
mgr.DeleteUsers(ctx, usernames)
mgr.SetUser(ctx, username, password)

// Network Configuration
mgr.GetNTP(ctx)
mgr.SetNTP(ctx, fromDHCP, servers)
mgr.GetDNS(ctx)
mgr.SetDNS(ctx, fromDHCP, servers, searchDomains)
mgr.GetHostname(ctx)
mgr.SetHostname(ctx, name)
mgr.GetNetworkProtocols(ctx)
mgr.GetNetworkDefaultGateway(ctx)
mgr.SetNetworkDefaultGateway(ctx, gateway)
mgr.SetNetworkInterfaces(ctx, iface)
mgr.SetSystemDateAndTime(ctx, dateTimeType, hour, minute, second, year, month, day)
mgr.SetSystemFactoryDefault(ctx, hardReset)

// Scopes
mgr.GetScopes(ctx)
mgr.SetScopes(ctx, scopeItems)

// Service Capabilities
caps, err := mgr.GetServiceCapabilities(ctx)
mgr.SendAuxiliaryCommand(ctx, auxiliaryData)
```

### Media Service

```go
media := client.MediaService()

// Profiles
profiles, err := media.GetProfiles(ctx)
profile, err := media.CreateProfile(ctx, name)
err := media.DeleteProfile(ctx, profileToken)

// Stream URI
uri, err := media.GetStreamURI(ctx, profileToken)
snapshotUri, err := media.GetSnapshotUri(ctx, profileToken)

// Video Sources
sources, err := media.GetVideoSources(ctx)
configs, err := media.GetVideoSourceConfigurations(ctx)

// Video Encoder
encConfigs, err := media.GetVideoEncoderConfigurations(ctx)
options, err := media.GetVideoEncoderConfigurationOptions(ctx, profileToken)
err := media.SetVideoEncoderConfiguration(ctx, config)

// Audio Sources
audioSources, err := media.GetAudioSources(ctx)
audioConfigs, err := media.GetAudioSourceConfigurations(ctx)

// Audio Encoder
audioEncConfigs, err := media.GetAudioEncoderConfigurations(ctx)
audioOptions, err := media.GetAudioEncoderConfigurationOptions(ctx)

// Audio Outputs
outputs, err := media.GetAudioOutputs(ctx)
outputConfigs, err := media.GetAudioOutputConfigurations(ctx)

// Profile Configuration
err := media.AddVideoSourceConfiguration(ctx, profileToken, configToken)
err := media.AddVideoEncoderConfiguration(ctx, profileToken, configToken)
err := media.AddAudioSourceConfiguration(ctx, profileToken, configToken)
err := media.AddAudioEncoderConfiguration(ctx, profileToken, configToken)
err := media.RemoveVideoSourceConfiguration(ctx, profileToken, configToken)
err := media.RemoveVideoEncoderConfiguration(ctx, profileToken, configToken)
err := media.RemoveAudioSourceConfiguration(ctx, profileToken, configToken)
err := media.RemoveAudioEncoderConfiguration(ctx, profileToken, configToken)

// Synchronization
err := media.SetSynchronizationPoint(ctx, profileToken)

// OSD (On-Screen Display)
osds, err := media.GetOSDs(ctx, videoSourceToken)
osdOptions, err := media.GetOSDOptions(ctx, videoSourceToken)
osd, err := media.CreateOSD(ctx, videoSourceToken, osd)
err := media.SetOSD(ctx, osd)
err := media.DeleteOSD(ctx, osdToken)

// Service Capabilities
caps, err := media.GetMediaServiceCapabilities(ctx)
```

### PTZ Service

```go
ptz := client.PTZService()

// Movement
err := ptz.ContinuousMove(ctx, profileToken, velocity)
err := ptz.AbsoluteMove(ctx, profileToken, position)
err := ptz.RelativeMove(ctx, profileToken, displacement)
err := ptz.Stop(ctx, profileToken, stopPanTilt, stopZoom)

// Status
status, err := ptz.GetStatus(ctx, profileToken)

// Presets
presets, err := ptz.GetPresets(ctx, profileToken)
err := ptz.GotoPreset(ctx, profileToken, presetToken)
token, err := ptz.SetPreset(ctx, profileToken, presetName)
err := ptz.RemovePreset(ctx, profileToken, presetToken)

// Home Position
err := ptz.GotoHomePosition(ctx, profileToken)
err := ptz.SetHomePosition(ctx, profileToken)

// PTZ Nodes & Configuration
nodes, err := ptz.GetPTZNodes(ctx)
configs, err := ptz.GetPTZConfigurations(ctx)
options, err := ptz.GetPTZConfigurationOptions(ctx, configToken)

// Auxiliary Commands
result, err := ptz.PTZSendAuxiliaryCommand(ctx, profileToken, auxiliaryData)
```

### Imaging Service

```go
imaging := client.ImagingService()

// Settings
settings, err := imaging.GetImagingSettings(ctx, videoSourceToken)
err := imaging.SetImagingSettings(ctx, videoSourceToken, settings)

// Options
options, err := imaging.GetMoveOptions(ctx, videoSourceToken)
err := imaging.Move(ctx, videoSourceToken, focus)
err := imaging.Stop(ctx, videoSourceToken)

// Presets
presets, err := imaging.GetImagingPresets(ctx, videoSourceToken)
preset, err := imaging.GetCurrentImagingPreset(ctx, videoSourceToken)
err := imaging.SetCurrentImagingPreset(ctx, videoSourceToken, presetToken)

// Status
status, err := imaging.GetImagingStatus(ctx, videoSourceToken)

// Service Capabilities
caps, err := imaging.GetImagingServiceCapabilities(ctx)
```

### Events Service

```go
events := client.EventsService()

// The subscription carries the device's complete endpoint reference.
sub, err := events.CreatePullPointSubscription(ctx)
err = sub.SetSynchronizationPoint(ctx)
messages, err := sub.PullMessages(ctx, timeout, limit)
err = sub.Renew(ctx, duration)
err = sub.Unsubscribe(ctx)

// Event Properties
props, err := events.GetEventProperties(ctx)
caps, err := events.GetEventServiceCapabilities(ctx)
```

### Recording Service (Profile G)

```go
rec := client.RecordingService()

// Recordings
recordings, err := rec.GetRecordings(ctx)

// Search (with pagination)
filter := onvif.SearchFilter{
    StartTime:  startTime,
    EndTime:    endTime,
    MaxResults: 100,
}
result, err := rec.SearchRecordings(ctx, filter)

// Keep the same filters when continuing the search.
if err == nil && result.HasMore {
    filter.SearchToken = result.SearchToken
    nextResult, err := rec.SearchRecordings(ctx, filter)
}

err := rec.EndSearch(ctx, searchToken)

// Recording Jobs
jobs, err := rec.GetRecordingJobs(ctx)
job, err := rec.CreateRecordingJob(ctx, recordingToken, "Active", 1)
err := rec.DeleteRecordingJob(ctx, jobToken)
state, err := rec.GetRecordingJobState(ctx, jobToken)
err := rec.SetRecordingJobMode(ctx, jobToken, "Active")
jobConfig, err := rec.GetRecordingJobConfiguration(ctx, jobToken)

// Recording Information
summary, err := rec.GetRecordingSummary(ctx)
info, err := rec.GetRecordingInformation(ctx, recordingToken)
options, err := rec.GetRecordingOptions(ctx, recordingToken)
caps, err := rec.GetRecordingServiceCapabilities(ctx)
searchCaps, err := rec.GetSearchServiceCapabilities(ctx)
```

### Replay Service

```go
replay := client.ReplayService()
uri, err := replay.GetReplayURI(ctx, recordingToken)
```

### Discovery

```go
// Direct HTTP probe to specific host
device, err := onvif.ProbeDevice(ctx, "192.168.1.100", 80, 5*time.Second)
```

## Architecture

```
onvif/
├── client.go           # Main client with service discovery
├── soap.go             # SOAP client with WS-Security + HTTP Digest Auth
├── types.go            # Core type definitions
├── cache.go            # Connection pool and TTL cache
│
├── device.go           # Device Service (GetDeviceInformation, GetServices, GetCapabilities)
├── device_mgmt.go      # Device Management (Reboot, Network, Users)
├── device_config.go    # Device Configuration (NTP, DNS, Hostname, Scopes, Protocols)
│
├── media.go            # Media Service (Profiles, Stream URI, Snapshot URI)
├── media_config.go     # Media Configuration (Video/Audio Encoder, Profile CRUD)
├── media_osd.go        # OSD Management (Create, Set, Delete OSD)
│
├── ptz.go              # PTZ Service (Movement, Presets, Home Position)
├── ptz_config.go       # PTZ Configuration (Nodes, Configurations, Options)
│
├── imaging.go          # Imaging Service (Settings, Move, Stop)
├── imaging_preset.go   # Imaging Presets and Status
│
├── events.go           # Events Service (PullPoint Subscription, Polling)
├── events_ext.go       # Events Extended (EventProperties, ServiceCapabilities)
│
├── recording.go        # Recording Service (GetRecordings, Search)
├── recording_job.go    # Recording Jobs (Create, Delete, State, Mode)
│
├── replay.go           # Replay Service (GetReplayUri)
├── discovery.go        # WS-Discovery (HTTP Probe)
└── snapshot.go         # Snapshot URI
```

## Authentication

The client supports WS-Security PasswordDigest and HTTP Digest with SHA-256 or MD5. Digest challenges select the algorithm and quality of protection, including legacy challenges without a qop.

The PasswordText fallback requires certificate-verified HTTPS at the configured camera authority. Service URLs may derive from the configured camera address and may use a different port for Digest or PasswordDigest. SOAP requests stay on the configured camera host and preserve HTTPS when the camera is configured with HTTPS.

## Device Compatibility

Tested with:
- Hikvision (DS-2CD series)
- Dahua
- Axis
- Generic ONVIF Profile S/T/G devices

## License

MIT
