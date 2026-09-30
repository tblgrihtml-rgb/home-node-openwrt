package transmission

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionNegotiationAndAdd(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		username, password, ok := r.BasicAuth()
		if !ok || username != "user" || password != "pass" {
			t.Fatal("missing RPC auth")
		}
		if r.Header.Get("X-Transmission-Session-Id") != "session-1" {
			w.Header().Set("X-Transmission-Session-Id", "session-1")
			w.WriteHeader(http.StatusConflict)
			return
		}
		fmt.Fprint(w, `{"arguments":{"torrent-added":{"id":7,"name":"test"}},"result":"success"}`)
	}))
	defer server.Close()

	client := New(server.URL, "user", "pass", 2*time.Second)
	result, err := client.AddMagnet(context.Background(), "magnet:?xt=urn:btih:1234567890", "/downloads")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two calls, got %d", calls.Load())
	}
	if result["torrent-added"] == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestTorrentControls(t *testing.T) {
	var methods []string
	var deleteData []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") != "session-1" {
			w.Header().Set("X-Transmission-Session-Id", "session-1")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var request struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, request.Method)
		if request.Method == "torrent-remove" {
			value, _ := request.Arguments["delete-local-data"].(bool)
			deleteData = append(deleteData, value)
		}
		fmt.Fprint(w, `{"arguments":{},"result":"success"}`)
	}))
	defer server.Close()

	client := New(server.URL, "user", "pass", 2*time.Second)
	ctx := context.Background()
	if err := client.Start(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if err := client.Stop(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if err := client.Remove(ctx, 7, false); err != nil {
		t.Fatal(err)
	}
	if err := client.Remove(ctx, 7, true); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(methods); got != "[torrent-start torrent-stop torrent-remove torrent-remove]" {
		t.Fatalf("unexpected methods: %s", got)
	}
	if got := fmt.Sprint(deleteData); got != "[false true]" {
		t.Fatalf("unexpected delete-local-data values: %s", got)
	}
}
