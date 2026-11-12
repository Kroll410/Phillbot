package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWheelUserAssets(t *testing.T) {
	a := newTestApp(t)
	a.baseDir = t.TempDir()
	_ = os.MkdirAll(a.wheelDir(), 0755)
	_ = os.WriteFile(filepath.Join(a.wheelDir(), "center_1.gif"), []byte("GIF89a"), 0644)
	_ = os.WriteFile(filepath.Join(a.baseDir, "tokens.json"), []byte("secret"), 0644)
	h := a.userAssets()
	get := func(p string) (int, string, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		return w.Code, w.Header().Get("Content-Type"), w.Body.String()
	}
	if c, ct, b := get("/userimg/wheel/center_1.gif"); c != 200 || ct != "image/gif" || b != "GIF89a" {
		t.Fatalf("gif %d %s %q", c, ct, b)
	}
	for _, p := range []string{"/userimg/wheel/../tokens.json", "/userimg/wheel/..%5Ctokens.json", "/userimg/wheel/x.json", "/tokens.json"} {
		if c, _, _ := get(p); c != 404 {
			t.Errorf("%s → %d", p, c)
		}
	}
	a.cleanWheelDir("")
	if _, err := os.Stat(filepath.Join(a.wheelDir(), "center_1.gif")); err == nil {
		t.Fatal("clean")
	}
}

func TestSaveWheelImage(t *testing.T) {
	a := newTestApp(t)
	a.baseDir = t.TempDir()
	name, err := a.SaveWheelImage("cat.GIF", "data:image/gif;base64,R0lGODlh")
	if err != nil || filepath.Ext(name) != ".gif" {
		t.Fatalf("save %q %v", name, err)
	}
	if b, _ := os.ReadFile(filepath.Join(a.wheelDir(), name)); string(b) != "GIF89a" {
		t.Fatalf("content %q", b)
	}
	if _, err := a.SaveWheelImage("x.exe", "AAAA"); err == nil {
		t.Fatal("exe accepted")
	}
}
