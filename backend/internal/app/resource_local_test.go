package app

import "testing"

func TestLocalResourceBaseURL(t *testing.T) {
	t.Setenv("CANVAS_BACKEND_ADDR", "")
	if got := localResourceBaseURL(); got != "http://127.0.0.1:8090" {
		t.Fatalf("default: got %q", got)
	}
	t.Setenv("CANVAS_BACKEND_ADDR", ":8080")
	if got := localResourceBaseURL(); got != "http://127.0.0.1:8080" {
		t.Fatalf("bare port: got %q", got)
	}
	t.Setenv("CANVAS_BACKEND_ADDR", "0.0.0.0:9000")
	if got := localResourceBaseURL(); got != "http://127.0.0.1:9000" {
		t.Fatalf("wildcard: got %q", got)
	}
	t.Setenv("CANVAS_BACKEND_ADDR", "10.0.0.5:8090")
	if got := localResourceBaseURL(); got != "http://10.0.0.5:8090" {
		t.Fatalf("explicit host: got %q", got)
	}
}
