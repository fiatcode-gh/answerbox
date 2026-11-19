package web

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// importImageExam imports a zip with one PNG through the handler and returns
// the image's media URL and bytes.
func importImageExam(t *testing.T, env *testEnv) (src string, png []byte) {
	t.Helper()
	cookie, csrf := loginThroughHandler(t, env)
	png = testPNG(t)
	detail := importExam(t, env, cookie, csrf, "shapes.zip", zipOf(t, map[string][]byte{
		"exam.md":     []byte(zipExam),
		"img/tri.png": png,
	}))
	body := do(env.h, "GET", detail, cookie).Body.String()
	m := regexp.MustCompile(`<img src="(/media/[0-9a-f]{32}/[0-9a-f]{32}\.png)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("detail has no media image:\n%s", body)
	}
	return m[1], png
}

func TestMedia_ServesImage(t *testing.T) {
	env := newTestEnv(t)
	src, png := importImageExam(t, env)

	rec := do(env.h, "GET", src, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", src, rec.Code)
	}
	for k, want := range map[string]string{
		"Content-Type":           "image/png",
		"Cache-Control":          "public, max-age=31536000, immutable",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Values(k); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Error("body differs from the uploaded image")
	}
}

func TestMedia_RejectsBadPaths(t *testing.T) {
	env := newTestEnv(t)
	src, _ := importImageExam(t, env)
	parts := strings.Split(src, "/") // "", "media", key, name
	key, name := parts[2], parts[3]
	hash := strings.TrimSuffix(name, ".png")

	tmpName := "." + name + ".tmp-" + strings.Repeat("A", 26)
	if err := env.media.WriteFile(key+"/"+tmpName, []byte("partial"), 0o640); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"/media/" + key[:31] + "/" + name,
		"/media/" + key + "0/" + name,
		"/media/" + strings.ToUpper(key) + "/" + name,
		"/media/" + key + "/" + strings.ToUpper(hash) + ".png",
		"/media/" + key + "/" + hash + ".svg",
		"/media/" + key + "/" + hash + ".PNG",
		"/media/" + key + "/" + hash[:31] + ".png",
		"/media/" + key + "/%2e%2e",
		"/media/%2e%2e/" + name,
		"/media/" + key + "/%2e%2e%2f" + name,
		"/media/" + key,
		"/media/" + key + "/",
		"/media/" + strings.Repeat("0", 32) + "/" + name,
		"/media/" + key + "/" + strings.Repeat("0", 32) + ".png",
		// The temporary name an image is written under before its rename,
		// present on disk.
		"/media/" + key + "/" + tmpName,
	} {
		rec := do(env.h, "GET", target, nil)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Page not found") {
			t.Errorf("GET %s = %d, want the 404 page", target, rec.Code)
		}
	}
}
