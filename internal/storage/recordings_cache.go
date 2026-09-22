package storage

import (
	"strconv"
	"strings"
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

const recordingsCacheMaxEntries = 64

type recordingListCacheEntry struct {
	gen  uint64
	recs []model.Recording
}

type recordingCountCacheEntry struct {
	gen uint64
	n   int
}

type recordingsQueryCache struct {
	mu    sync.Mutex
	gen   uint64
	list  map[string]recordingListCacheEntry
	count map[string]recordingCountCacheEntry
}

func newRecordingsQueryCache() recordingsQueryCache {
	return recordingsQueryCache{
		list:  make(map[string]recordingListCacheEntry),
		count: make(map[string]recordingCountCacheEntry),
	}
}

func (d *DB) invalidateRecordingsCache() {
	if d == nil {
		return
	}
	d.recCache.mu.Lock()
	d.recCache.gen++
	if len(d.recCache.list) > recordingsCacheMaxEntries || len(d.recCache.count) > recordingsCacheMaxEntries {
		d.recCache.list = make(map[string]recordingListCacheEntry)
		d.recCache.count = make(map[string]recordingCountCacheEntry)
	}
	d.recCache.mu.Unlock()
}

func (d *DB) recordingsCacheGen() uint64 {
	d.recCache.mu.Lock()
	defer d.recCache.mu.Unlock()
	return d.recCache.gen
}

func (d *DB) lookupListRecordings(key string) ([]model.Recording, bool) {
	d.recCache.mu.Lock()
	defer d.recCache.mu.Unlock()
	e, ok := d.recCache.list[key]
	if !ok || e.gen != d.recCache.gen {
		return nil, false
	}
	return cloneRecordings(e.recs), true
}

func (d *DB) storeListRecordings(key string, recs []model.Recording, gen uint64) {
	d.recCache.mu.Lock()
	defer d.recCache.mu.Unlock()
	if gen != d.recCache.gen {
		return
	}
	if len(d.recCache.list) >= recordingsCacheMaxEntries {
		d.recCache.list = make(map[string]recordingListCacheEntry)
	}
	d.recCache.list[key] = recordingListCacheEntry{gen: gen, recs: cloneRecordings(recs)}
}

func (d *DB) lookupCountRecordings(key string) (int, bool) {
	d.recCache.mu.Lock()
	defer d.recCache.mu.Unlock()
	e, ok := d.recCache.count[key]
	if !ok || e.gen != d.recCache.gen {
		return 0, false
	}
	return e.n, true
}

func (d *DB) storeCountRecordings(key string, n int, gen uint64) {
	d.recCache.mu.Lock()
	defer d.recCache.mu.Unlock()
	if gen != d.recCache.gen {
		return
	}
	if len(d.recCache.count) >= recordingsCacheMaxEntries {
		d.recCache.count = make(map[string]recordingCountCacheEntry)
	}
	d.recCache.count[key] = recordingCountCacheEntry{gen: gen, n: n}
}

func cloneRecordings(in []model.Recording) []model.Recording {
	if in == nil {
		return nil
	}
	out := make([]model.Recording, len(in))
	copy(out, in)
	return out
}

func recordingFilterKey(filter model.RecordingFilter, includePage bool) string {
	var b strings.Builder
	b.Grow(128)
	b.WriteString(filter.CameraID)
	b.WriteByte('|')
	b.WriteString(string(filter.Format))
	b.WriteByte('|')
	b.WriteString(filter.Search)
	b.WriteByte('|')
	writeBoolPtr(&b, filter.Merged)
	b.WriteByte('|')
	writeBoolPtr(&b, filter.Archived)
	b.WriteByte('|')
	if !filter.StartTime.IsZero() {
		b.WriteString(strconv.FormatInt(filter.StartTime.UnixNano(), 10))
	}
	b.WriteByte('|')
	if !filter.EndTime.IsZero() {
		b.WriteString(strconv.FormatInt(filter.EndTime.UnixNano(), 10))
	}
	if includePage {
		b.WriteByte('|')
		b.WriteString(filter.SortBy)
		b.WriteByte('|')
		b.WriteString(filter.SortOrder)
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(filter.Limit))
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(filter.Offset))
	}
	return b.String()
}

func writeBoolPtr(b *strings.Builder, v *bool) {
	if v == nil {
		b.WriteByte('-')
		return
	}
	if *v {
		b.WriteByte('1')
		return
	}
	b.WriteByte('0')
}
