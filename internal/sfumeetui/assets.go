// Package sfumeetui contains the shared local and NVR SFU Meet assets.
package sfumeetui

import (
	"bytes"
	"embed"
	"fmt"
)

//go:embed web/style.css web/livekit.html web/livekit-app.js web/livekit-sdk.js LIVEKIT-LICENSE LIVEKIT-SDK.md third-party-licenses/*.txt
var Assets embed.FS

// LiveKitNotices returns the bundled SDK and dependency license texts.
func LiveKitNotices() ([]byte, error) {
	files, err := Assets.ReadDir("third-party-licenses")
	if err != nil {
		return nil, err
	}
	names := []string{"LIVEKIT-SDK.md", "LIVEKIT-LICENSE"}
	for _, file := range files {
		names = append(names, "third-party-licenses/"+file.Name())
	}
	var output bytes.Buffer
	for _, name := range names {
		data, err := Assets.ReadFile(name)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&output, "\n--- %s ---\n\n", name)
		output.Write(data)
	}
	return output.Bytes(), nil
}
