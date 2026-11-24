package selfupdate

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int, body string, hdr ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Set(hdr[i], hdr[i+1])
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

const relJSON = `{"tag_name":"v0.5.2","assets":[
{"name":"rig-linux-amd64","browser_download_url":"https://github.com/AindriuB/rig/releases/download/v0.5.2/rig-linux-amd64"},
{"name":"SHA256SUMS","browser_download_url":"https://github.com/AindriuB/rig/releases/download/v0.5.2/SHA256SUMS"},
{"name":"SHA256SUMS.sig","browser_download_url":"https://github.com/AindriuB/rig/releases/download/v0.5.2/SHA256SUMS.sig"}]}`

func TestLookup(t *testing.T) {
	var urls []string
	c := NewClient(rt(func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.String())
		if strings.HasSuffix(r.URL.Path, "/tags/v9") {
			return resp(200, `{"tag_name":"v9","prerelease":true}`), nil
		}
		return resp(200, relJSON), nil
	}))
	rel, err := c.Lookup(context.Background(), "")
	if err != nil || rel.Tag != "v0.5.2" {
		t.Fatalf("%v %v", rel, err)
	}
	if _, err := c.Lookup(context.Background(), "v0.5.2"); err != nil {
		t.Fatal(err)
	}
	if urls[0] != "https://api.github.com/repos/AindriuB/rig/releases/latest" ||
		urls[1] != "https://api.github.com/repos/AindriuB/rig/releases/tags/v0.5.2" {
		t.Fatalf("urls %v", urls)
	}
	if _, err := c.Lookup(context.Background(), "v9"); err == nil || !strings.Contains(err.Error(), "v9") {
		t.Fatalf("prerelease: %v", err)
	}
}

func TestRedirectPolicy(t *testing.T) {
	var hit []string
	c := NewClient(rt(func(r *http.Request) (*http.Response, error) {
		hit = append(hit, r.URL.Host)
		switch r.URL.Host {
		case "api.github.com":
			return resp(302, "", "Location", "https://evil.example.com/x"), nil
		case "github.com":
			return resp(302, "", "Location", "http://objects.githubusercontent.com/x"), nil
		case "objects.githubusercontent.com":
			return resp(302, "", "Location", "https://release-assets.githubusercontent.com/x"), nil
		}
		return resp(200, "ok"), nil
	}))
	_, err := c.Lookup(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "evil.example.com") {
		t.Fatalf("err %v", err)
	}
	for _, h := range hit {
		if h == "evil.example.com" {
			t.Fatal("request sent to refused host")
		}
	}
	if _, err := c.get(context.Background(), "https://github.com/a", "a", 10); err == nil {
		t.Fatal("http redirect allowed")
	}
	if b, err := c.get(context.Background(), "https://objects.githubusercontent.com/a", "a", 10); err != nil || string(b) != "ok" {
		t.Fatalf("%s %v", b, err)
	}
	if _, err := c.get(context.Background(), "https://evil.example.com/a", "a", 10); err == nil {
		t.Fatal("direct bad host allowed")
	}
}

func TestFetchAndCaps(t *testing.T) {
	big := 0
	c := NewClient(rt(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS.sig"):
			return resp(200, strings.Repeat("s", big)), nil
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
			return resp(200, "sums"), nil
		}
		return resp(200, "bin"), nil
	}))
	rel := &Release{Tag: "v0.5.2", assets: map[string]string{}}
	rel.assets["rig-linux-amd64"] = "https://github.com/AindriuB/rig/releases/download/v0.5.2/rig-linux-amd64"
	rel.assets["SHA256SUMS"] = "https://github.com/AindriuB/rig/releases/download/v0.5.2/SHA256SUMS"
	big = 10
	if _, err := c.Fetch(context.Background(), rel, "linux", "amd64"); err == nil {
		t.Fatal("missing sig")
	} else {
		checkName(t, err, "signature")
	}
	rel.assets["SHA256SUMS.sig"] = "https://github.com/AindriuB/rig/releases/download/v0.5.2/SHA256SUMS.sig"
	d, err := c.Fetch(context.Background(), rel, "linux", "amd64")
	if err != nil || !bytes.Equal(d.Binary, []byte("bin")) {
		t.Fatalf("%v", err)
	}
	big = 64<<10 + 1
	if _, err := c.Fetch(context.Background(), rel, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "SHA256SUMS.sig") {
		t.Fatalf("cap: %v", err)
	}
}

func TestAssetName(t *testing.T) {
	if AssetName("windows", "amd64") != "rig-windows-amd64.exe" || AssetName("linux", "arm64") != "rig-linux-arm64" {
		t.Fatal("names")
	}
}
