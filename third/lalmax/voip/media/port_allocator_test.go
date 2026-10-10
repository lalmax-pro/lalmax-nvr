package media

import (
	"testing"
)

func TestPortAllocator(t *testing.T) {
	pa := NewPortAllocator(41000, 41010)

	port1, err := pa.Alloc()
	if err != nil {
		t.Fatalf("first alloc failed: %v", err)
	}
	if port1 < 41000 || port1 > 41010 {
		t.Fatalf("port out of range: %d", port1)
	}

	port2, err := pa.Alloc()
	if err != nil {
		t.Fatalf("second alloc failed: %v", err)
	}
	if port2 == port1 {
		t.Fatalf("allocated same port twice: %d", port1)
	}

	pa.Free(port1)

	port3, err := pa.Alloc()
	if err != nil {
		t.Fatalf("third alloc failed: %v", err)
	}
	if port3 != port1 {
		t.Logf("reused port expected=%d, got=%d (order may vary)", port1, port3)
	}
}

func TestPortAllocatorExhaustion(t *testing.T) {
	pa := NewPortAllocator(41000, 41002)

	ports := make([]int, 0)
	for i := 0; i < 10; i++ {
		port, err := pa.Alloc()
		if err != nil {
			if i < 3 {
				t.Fatalf("alloc failed too early at iteration %d: %v", i, err)
			}
			break
		}
		ports = append(ports, port)
	}

	if len(ports) < 3 {
		t.Fatalf("expected at least 3 ports, got %d", len(ports))
	}

	for _, port := range ports {
		pa.Free(port)
	}

	port, err := pa.Alloc()
	if err != nil {
		t.Fatalf("alloc after free failed: %v", err)
	}
	if port < 41000 || port > 41002 {
		t.Fatalf("port out of range after free: %d", port)
	}
}
