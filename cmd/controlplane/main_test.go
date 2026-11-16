package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardJSONUsesFrontendFieldNames(t *testing.T) {
	data := dashboardData{Username: "admin", CSRF: "csrf", Clients: []dashboardClient{{ID: "client-1", Name: "lab-client"}}}
	raw, err := json.Marshal(data)
	if err != nil { t.Fatal(err) }
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil { t.Fatal(err) }
	if decoded["csrf_token"] != "csrf" { t.Fatalf("csrf_token missing from dashboard response: %s", raw) }
	clients, ok := decoded["clients"].([]any)
	if !ok || clients[0].(map[string]any)["id"] != "client-1" { t.Fatalf("client fields do not match frontend API shape: %s", raw) }
}

func TestUIHandlerServesAppForBrowserRoutes(t *testing.T) {
	handler := (&server{}).uiHandler()
	for _, route := range []string{"/", "/login", "/restore-points"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("GET", route, nil))
		if recorder.Code != 200 { t.Errorf("GET %s returned %d", route, recorder.Code); continue }
		if !strings.Contains(recorder.Body.String(), "Backup Console") { t.Errorf("GET %s did not serve the app shell", route) }
	}
}
