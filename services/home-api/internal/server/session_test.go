package server

import (
	"net/http/httptest"
	"testing"

	"homenode/services/home-api/internal/config"
)

func TestRememberedSessionCookie(t *testing.T) {
	server := &Server{cfg: config.Config{
		APIUsername: "home", APIPassword: "test-password",
		SessionSecret: "0123456789abcdef0123456789abcdef",
	}}
	recorder := httptest.NewRecorder()
	server.setSessionCookie(recorder, true)
	response := recorder.Result()
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge <= 0 || !cookies[0].HttpOnly {
		t.Fatalf("unexpected cookie: %#v", cookies)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookies[0])
	if !server.authenticated(request) {
		t.Fatal("valid remembered session was rejected")
	}
	cookies[0].Value += "tampered"
	request = httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookies[0])
	if server.authenticated(request) {
		t.Fatal("tampered session was accepted")
	}
}
