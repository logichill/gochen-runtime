package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticHandlerRejectsHiddenSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".private", "public", ".well-known"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".secret", ".private/index.html", "public/index.html", ".well-known/token"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("test content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for alias, target := range map[string]string{
		"secret.txt": ".secret", "private": ".private", "index.html": ".secret",
		"visible": "public", "token": ".well-known/token",
	} {
		if err := os.Symlink(target, filepath.Join(root, alias)); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewStaticHandler(root)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/secret.txt", http.StatusNotFound},
		{"/private/", http.StatusNotFound},
		{"/", http.StatusNotFound},
		{"/visible/", http.StatusOK},
		{"/token", http.StatusOK},
		{"/.well-known/token", http.StatusOK},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("status=%d, want %d", w.Code, tc.want)
			}
		})
	}
}
