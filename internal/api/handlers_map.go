package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	mapTileMaxZoom = 18
	mapTileSize    = 256
	mapTileCacheN  = 512
	mapTileTimeout = 8 * time.Second
)

type mapTileEntry struct {
	body    []byte
	ctype   string
	fetched time.Time
}

var (
	mapTileOnce   sync.Once
	mapTileClient *http.Client
	mapTileMu     sync.Mutex
	mapTileCache  = make(map[string]mapTileEntry, mapTileCacheN)
	mapTileOrder  []string
)

func mapHTTPClient() *http.Client {
	mapTileOnce.Do(func() {
		mapTileClient = &http.Client{
			Timeout: mapTileTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	})
	return mapTileClient
}

func mapTileSources(z, x, y int) []string {
	return []string{
		fmt.Sprintf("https://tile.openstreetmap.org/%d/%d/%d.png", z, x, y),
		fmt.Sprintf("https://a.basemaps.cartocdn.com/rastertiles/voyager/%d/%d/%d.png", z, x, y),
		fmt.Sprintf("https://webrd01.is.autonavi.com/appmaptile?lang=zh_cn&size=1&scale=1&style=8&x=%d&y=%d&z=%d", x, y, z),
	}
}

func cacheGetTile(key string) (mapTileEntry, bool) {
	mapTileMu.Lock()
	defer mapTileMu.Unlock()
	e, ok := mapTileCache[key]
	if !ok {
		return mapTileEntry{}, false
	}
	return e, true
}

func cachePutTile(key string, e mapTileEntry) {
	mapTileMu.Lock()
	defer mapTileMu.Unlock()
	if _, ok := mapTileCache[key]; !ok && len(mapTileOrder) >= mapTileCacheN {
		old := mapTileOrder[0]
		mapTileOrder = mapTileOrder[1:]
		delete(mapTileCache, old)
	}
	if _, ok := mapTileCache[key]; !ok {
		mapTileOrder = append(mapTileOrder, key)
	}
	mapTileCache[key] = e
}

func fetchMapTile(z, x, y int) (body []byte, ctype string, err error) {
	key := fmt.Sprintf("%d/%d/%d", z, x, y)
	if e, ok := cacheGetTile(key); ok {
		return e.body, e.ctype, nil
	}
	client := mapHTTPClient()
	var last error
	for _, src := range mapTileSources(z, x, y) {
		req, reqErr := http.NewRequest(http.MethodGet, src, nil)
		if reqErr != nil {
			last = reqErr
			continue
		}
		req.Header.Set("User-Agent", "lalmax-nvr/1.0 (map tiles; https://github.com/lalmax-pro/lalmax-nvr)")
		req.Header.Set("Accept", "image/png,image/*;q=0.8")
		resp, doErr := client.Do(req)
		if doErr != nil {
			last = doErr
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		resp.Body.Close()
		if readErr != nil {
			last = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK || len(data) < 32 {
			last = fmt.Errorf("%s: status %d", src, resp.StatusCode)
			continue
		}
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "image/png"
		}
		cachePutTile(key, mapTileEntry{body: data, ctype: ct, fetched: time.Now()})
		return data, ct, nil
	}
	if last == nil {
		last = fmt.Errorf("no tile source")
	}
	return nil, "", last
}

func parseTileCoord(r *http.Request) (z, x, y int, ok bool) {
	zi, errZ := strconv.Atoi(chi.URLParam(r, "z"))
	xi, errX := strconv.Atoi(chi.URLParam(r, "x"))
	yi, errY := strconv.Atoi(chi.URLParam(r, "y"))
	if errZ != nil || errX != nil || errY != nil {
		return 0, 0, 0, false
	}
	if zi < 0 || zi > mapTileMaxZoom || xi < 0 || yi < 0 {
		return 0, 0, 0, false
	}
	n := 1 << zi
	if xi >= n || yi >= n {
		return 0, 0, 0, false
	}
	return zi, xi, yi, true
}

func (h *Handler) handleMapTile(w http.ResponseWriter, r *http.Request) {
	z, x, y, ok := parseTileCoord(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tile coordinates")
		return
	}
	body, ctype, err := fetchMapTile(z, x, y)
	if err != nil {
		writeError(w, http.StatusBadGateway, "map tile unavailable")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
