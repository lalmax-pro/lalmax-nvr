package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"time"

	onvif "github.com/lalmax-pro/lalmax-nvr/onvif"
)

func main() {
	endpoint := "http://100.100.107.210/onvif/device_service"
	username := "admin"
	password := "zsc123456"

	fmt.Printf("Testing ONVIF connection to %s\n", endpoint)
	fmt.Println("==================================================")

	// Test network connectivity first
	fmt.Println("\n1. Testing network connectivity...")
	conn, err := net.DialTimeout("tcp", "100.100.107.210:80", 5*time.Second)
	if err != nil {
		log.Printf("Cannot reach device: %v", err)
		log.Println("Please check if:")
		log.Println("  - Device is powered on")
		log.Println("  - Network connection is active")
		log.Println("  - IP address is correct")
		log.Println("  - Device is on the same network")
		return
	}
	conn.Close()
	fmt.Println("✓ Device is reachable")

	// Create ONVIF client
	fmt.Println("\n2. Creating ONVIF client...")
	client, err := onvif.NewClient(endpoint, username, password,
		onvif.WithTimeout(10*time.Second),
	)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	fmt.Println("✓ Client created")

	// Connect to device
	fmt.Println("\n3. Connecting to device...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		log.Printf("Failed to connect: %v", err)
		log.Println("\nTrying alternative endpoints...")

		// Try alternative endpoints
		altEndpoints := []string{
			"http://100.100.107.210/onvif/device_service",
			"http://100.100.107.210:8080/onvif/device_service",
			"http://100.100.107.210:80/onvif/device_service",
		}

		for _, alt := range altEndpoints {
			fmt.Printf("Trying %s...\n", alt)
			client, err = onvif.NewClient(alt, username, password,
				onvif.WithTimeout(10*time.Second),
			)
			if err != nil {
				continue
			}

			if err := client.Connect(ctx); err != nil {
				fmt.Printf("  Failed: %v\n", err)
				continue
			}

			fmt.Println("✓ Connected successfully!")
			break
		}

		if !client.IsReady() {
			log.Fatal("Could not connect to device with any endpoint")
		}
	} else {
		fmt.Println("✓ Connected successfully!")
	}

	// Get device information
	fmt.Println("\n4. Getting device information...")
	info, err := client.GetDeviceInformation(ctx)
	if err != nil {
		log.Printf("Failed to get device info: %v", err)
	} else {
		fmt.Printf("  Manufacturer: %s\n", info.Manufacturer)
		fmt.Printf("  Model: %s\n", info.Model)
		fmt.Printf("  Firmware: %s\n", info.FirmwareVersion)
		fmt.Printf("  Serial: %s\n", info.SerialNumber)
		fmt.Printf("  Hardware ID: %s\n", info.HardwareId)
	}

	// Get service endpoints
	fmt.Println("\n5. Service endpoints...")
	endpoints := client.Endpoints()
	if endpoints != nil {
		printEndpoint("Device", endpoints.Device)
		printEndpoint("Media", endpoints.Media)
		printEndpoint("Media2", endpoints.Media2)
		printEndpoint("Recording", endpoints.Recording)
		printEndpoint("Search", endpoints.Search)
		printEndpoint("Replay", endpoints.Replay)
		printEndpoint("PTZ", endpoints.PTZ)
		printEndpoint("Imaging", endpoints.Imaging)
		printEndpoint("Events", endpoints.Events)
	}

	// Get media profiles
	fmt.Println("\n6. Getting media profiles...")
	mediaService := client.MediaService()
	profiles, err := mediaService.GetProfiles(ctx)
	if err != nil {
		log.Printf("Failed to get profiles: %v", err)
	} else {
		fmt.Printf("Found %d profiles:\n", len(profiles))
		for i, p := range profiles {
			fmt.Printf("  [%d] %s\n", i+1, p.Name)
			fmt.Printf("      Token: %s\n", p.Token)
			fmt.Printf("      Encoding: %s\n", p.Encoding)
			fmt.Printf("      Resolution: %dx%d\n", p.Resolution.Width, p.Resolution.Height)
			fmt.Printf("      Framerate: %g fps\n", p.Framerate)
			fmt.Printf("      Bitrate: %d kbps\n", p.Bitrate)
		}
	}

	// Get stream URI
	if len(profiles) > 0 {
		fmt.Println("\n7. Getting stream URI...")
		uri, err := mediaService.GetStreamURI(ctx, profiles[0].Token)
		if err != nil {
			log.Printf("Failed to get stream URI: %v", err)
		} else {
			fmt.Printf("  Stream URI: %s\n", uri)
		}
	}

	// Test recording service
	fmt.Println("\n8. Testing recording service...")
	recordingService := client.RecordingService()
	recordings, err := recordingService.GetRecordings(ctx)
	if err != nil {
		log.Printf("Failed to get recordings: %v", err)
	} else {
		fmt.Printf("Found %d recordings\n", len(recordings))
		for i, rec := range recordings {
			fmt.Printf("  [%d] %s\n", i+1, rec.Name)
			fmt.Printf("      Token: %s\n", rec.Token)
			fmt.Printf("      Status: %s\n", rec.Status)
			fmt.Printf("      Tracks: %d\n", len(rec.Tracks))
		}
	}

	// Test PTZ service
	fmt.Println("\n9. Testing PTZ service...")
	if len(profiles) > 0 {
		ptzService := client.PTZService()
		status, err := ptzService.GetStatus(ctx, profiles[0].Token)
		if err != nil {
			log.Printf("Failed to get PTZ status: %v", err)
		} else {
			fmt.Printf("  Position: Pan=%.2f, Tilt=%.2f, Zoom=%.2f\n",
				status.Position.PanTilt.X,
				status.Position.PanTilt.Y,
				status.Position.Zoom.X,
			)
			fmt.Printf("  Moving: %v\n", status.Moving)
		}

		presets, err := ptzService.GetPresets(ctx, profiles[0].Token)
		if err != nil {
			log.Printf("Failed to get presets: %v", err)
		} else {
			fmt.Printf("  Found %d presets\n", len(presets))
			for i, preset := range presets {
				fmt.Printf("    [%d] %s (%s)\n", i+1, preset.Name, preset.Token)
			}
		}
	}

	fmt.Println("\n==================================================")
	fmt.Println("Test completed!")
}

func printEndpoint(name string, endpoint *url.URL) {
	if endpoint != nil {
		fmt.Printf("  %-10s: %s\n", name, endpoint.String())
	} else {
		fmt.Printf("  %-10s: not available\n", name)
	}
}
