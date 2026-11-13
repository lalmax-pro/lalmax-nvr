package media

import "github.com/lalmax-pro/lalmax-nvr/internal/transcode"

// RecordingSource exposes only the logical provenance for a currently reserved
// derived output. It does not expose publisher or playback bearer tokens.
func (a *TranscodeAdapter) RecordingSource(physicalID string) (transcode.SourceRef, bool) {
    a.mu.RLock()
    defer a.mu.RUnlock()
    output, ok := a.outputs[physicalID]
    return output.source, ok
}
