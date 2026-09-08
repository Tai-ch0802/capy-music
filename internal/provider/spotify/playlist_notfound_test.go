package spotify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 清單 items 回 404(清單已刪)要映成 provider.ErrNotFound,T8 才分得出「已刪」與「其他錯」。
func TestPlaylistItemsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"status":404,"message":"Not found."}}`))
	}))
	defer srv.Close()
	_, err := New(srv.Client(), srv.URL).GetPlaylistItems(context.Background(), "gone")
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("404 應為 ErrNotFound:%v", err)
	}
}
