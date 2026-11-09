This directory contains github.com/mackerelio/go-osstat v0.2.8 (Apache-2.0).
The Darwin non-cgo CPU implementation calls the same Mach host_statistics API
as the upstream cgo implementation, through purego. This allows the embedded
LiveKit server to compile and collect host CPU statistics with CGO_ENABLED=0.
