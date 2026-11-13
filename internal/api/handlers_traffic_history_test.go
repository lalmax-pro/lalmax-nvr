package api

import (
    "testing"
    "time"

    "github.com/lalmax-pro/lalmax-nvr/internal/media"
)

func TestTrafficHistoryRetainsOneDayAndBuckets(t *testing.T) {
    history := &trafficHistory{}
    base := time.Now().Unix()
    base -= base % 5
    for i := 0; i < trafficHistoryCap+3; i++ {
        history.add(trafficSample{Timestamp: base - int64(trafficHistoryCap-i)*5, InKbps: int64(i)})
    }
    if history.count != trafficHistoryCap { t.Fatalf("count=%d", history.count) }
    all := history.since(base-86400, 5)
    if len(all) != trafficHistoryCap { t.Fatalf("samples=%d want %d",len(all),trafficHistoryCap) }
    recent := history.since(base-60, 30)
    if len(recent) < 2 || len(recent) > 4 { t.Fatalf("30-second buckets=%d", len(recent)) }
    for i:=1; i<len(recent);i++ {
        if recent[i].Timestamp <= recent[i-1].Timestamp { t.Fatal("not chronological") }
    }
}

func TestTrafficHistoryCountsReportedAndMissingMediaRates(t *testing.T) {
    h := &Handler{trafficHistory: &trafficHistory{}}
    h.sampleTrafficHistory([]media.StreamInfo{
        {StreamID:"a/stream0", Active:true, Publisher:&media.SessionInfo{ReadBitrateKbits:2000},
         Subscribers:[]media.SessionInfo{{WriteBitrateKbits:1100}, {Protocol:"nvr-record", SessionID:"nvr-record-1"}}},
        {StreamID:"b/stream0", Active:true, Publisher:&media.SessionInfo{},
         Subscribers:[]media.SessionInfo{{WriteBitrateKbits:900}, {}}},
    }, 123456)
    samples := h.trafficHistory.since(123456, 5)
    if len(samples)!=1 {t.Fatalf("samples=%d",len(samples))}
    s:=samples[0]
    if s.InKbps!=2000 || s.OutKbps!=2000 || s.Publishers!=2 || s.Subscribers!=3 || s.UnknownIn!=1 || s.UnknownOut!=1 {
        t.Fatalf("sample=%+v",s)
    }
}
