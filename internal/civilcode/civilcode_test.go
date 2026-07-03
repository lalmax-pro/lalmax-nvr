package civilcode

import "testing"

func TestLoaded(t *testing.T) {
	if !Loaded() {
		t.Fatal("civil code table failed to load")
	}
	if Size() < 3000 {
		t.Fatalf("expected >3000 entries, got %d", Size())
	}
}

func TestGet(t *testing.T) {
	c, ok := Get("4401")
	if !ok {
		t.Fatal("4401 not found")
	}
	if c.Name != "广州市" || c.ParentCode != "44" {
		t.Fatalf("4401 unexpected: %+v", c)
	}
}

func TestParentAndChain(t *testing.T) {
	// 440106 (天河区) -> 4401 -> 44
	p, ok := Parent("440106")
	if !ok || p.Code != "4401" {
		t.Fatalf("parent of 440106 = %+v ok=%v", p, ok)
	}
	chain := AllParents("440106")
	if len(chain) < 2 {
		t.Fatalf("expected chain >=2, got %d: %+v", len(chain), chain)
	}
	// 链中应包含省级 44
	found := false
	for _, c := range chain {
		if c.Code == "44" {
			found = true
		}
	}
	if !found {
		t.Fatalf("chain missing province 44: %+v", chain)
	}
}

func TestDescription(t *testing.T) {
	desc := Description("440106")
	if desc == "" {
		t.Fatal("empty description")
	}
	// 形如 广东省/广州市/天河区
	if n := len(splitSlash(desc)); n < 3 {
		t.Fatalf("expected >=3 levels, got %d: %s", n, desc)
	}
}

func TestChildrenTopLevel(t *testing.T) {
	top := Children("")
	if len(top) < 30 {
		t.Fatalf("expected >=30 provinces, got %d", len(top))
	}
}

func splitSlash(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '/' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	out = append(out, cur)
	return out
}
