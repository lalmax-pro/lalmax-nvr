package logic

import (
	"testing"

	"github.com/q191201771/lal/pkg/base"
)

func TestCustomizePublisherStatisticsHaveSessionIdentity(t *testing.T) {
	first := NewCustomizePubSessionContext("voip_first")
	defer first.Dispose()
	second := NewCustomizePubSessionContext("voip_second")
	defer second.Dispose()
	for _, session := range []*CustomizePubSessionContext{first, second} {
		stat := base.Session2StatPub(session)
		if stat.SessionId == "" || stat.SessionId != session.UniqueKey() {
			t.Fatalf("publisher control key %q does not match statistics ID %q", session.UniqueKey(), stat.SessionId)
		}
		if stat.Protocol != base.SessionProtocolCustomizeStr || stat.BaseType != base.SessionBaseTypePubStr {
			t.Fatalf("unexpected custom publisher statistics: %+v", stat)
		}
	}
	if first.UniqueKey() == second.UniqueKey() {
		t.Fatal("custom publishers must have unique IDs")
	}
}
