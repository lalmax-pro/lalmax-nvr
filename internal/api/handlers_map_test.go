package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestParseTileCoord(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	var gotZ, gotX, gotY int
	var ok bool
	r.Get("/api/map/tiles/{z}/{x}/{y}.png", func(w http.ResponseWriter, req *http.Request) {
		gotZ, gotX, gotY, ok = parseTileCoord(req)
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/map/tiles/4/13/6.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !ok || gotZ != 4 || gotX != 13 || gotY != 6 {
		t.Fatalf("got z=%d x=%d y=%d ok=%v", gotZ, gotX, gotY, ok)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/map/tiles/2/8/0.png", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if ok {
		t.Fatalf("expected invalid x for zoom 2, got z=%d x=%d y=%d", gotZ, gotX, gotY)
	}
}

func TestHandleMapTileRejectsBadCoords(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	r := chi.NewRouter()
	r.Get("/api/map/tiles/{z}/{x}/{y}.png", h.handleMapTile)
	req := httptest.NewRequest(http.MethodGet, "/api/map/tiles/99/0/0.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
