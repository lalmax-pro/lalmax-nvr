package media

import "strings"

const subStreamSuffix = "_sub"

// SubStreamID returns the lalmax stream_name used for a camera's sub-stream.
func SubStreamID(cameraID string) string {
	return cameraID + subStreamSuffix
}

// IsSubStreamID reports whether streamID is a camera sub-stream group.
func IsSubStreamID(streamID string) bool {
	return strings.HasSuffix(streamID, subStreamSuffix)
}

// MainStreamID strips the sub-stream suffix when present.
func MainStreamID(streamID string) string {
	return strings.TrimSuffix(streamID, subStreamSuffix)
}
