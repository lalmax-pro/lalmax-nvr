// Package sfumeetui contains the shared local and NVR SFU Meet assets.
package sfumeetui

import "embed"

//go:embed web/index.html web/app.js web/style.css
var Assets embed.FS
