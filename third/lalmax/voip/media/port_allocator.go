package media

import (
	"fmt"
	"net"
	"sync"
)

type PortAllocator struct {
	min       int
	max       int
	allocated map[int]bool
	mutex     sync.Mutex
}

func NewPortAllocator(min, max int) *PortAllocator {
	if min < 1024 {
		min = 1024
	}
	if max > 65535 {
		max = 65535
	}
	if max < min {
		max = min
	}
	return &PortAllocator{
		min:       min,
		max:       max,
		allocated: make(map[int]bool),
	}
}

func (pa *PortAllocator) Alloc() (int, error) {
	pa.mutex.Lock()
	defer pa.mutex.Unlock()

	for port := pa.min; port <= pa.max; port++ {
		if pa.allocated[port] {
			continue
		}
		if !pa.isPortAvailable(port) {
			continue
		}
		pa.allocated[port] = true
		return port, nil
	}
	return 0, fmt.Errorf("no available port in range %d-%d", pa.min, pa.max)
}

// AllocMedia reserves RTP and, without rtcp-mux, its adjacent RTCP port.
// Reserve both atomically so another call cannot take the RTCP socket.
func (pa *PortAllocator) AllocMedia(mux bool) (int, int, error) {
	if mux {
		port, err := pa.Alloc()
		return port, port, err
	}
	pa.mutex.Lock()
	defer pa.mutex.Unlock()
	for port := pa.min; port < pa.max; port++ {
		if port%2 != 0 || pa.allocated[port] || pa.allocated[port+1] || !pa.isPortAvailable(port) || !pa.isPortAvailable(port+1) {
			continue
		}
		pa.allocated[port], pa.allocated[port+1] = true, true
		return port, port + 1, nil
	}
	return 0, 0, fmt.Errorf("no available RTP/RTCP pair in range %d-%d", pa.min, pa.max)
}

func (pa *PortAllocator) Free(port int) {
	pa.mutex.Lock()
	defer pa.mutex.Unlock()
	delete(pa.allocated, port)
}

func (pa *PortAllocator) isPortAvailable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
