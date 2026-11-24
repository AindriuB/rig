package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// releaseAPI is the only release source. It is a constant on purpose: no
// function, environment variable or config key changes it.
const releaseAPI = "https://api.github.com/repos/AindriuB/rig/releases"

const (
	maxBinary = 100 << 20
	maxSmall  = 64 << 10
	maxAPI    = 4 << 20
)

var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// Client talks to the fixed release source. Tests substitute the transport.
type Client struct {
	hc *http.Client
}

// NewClient returns a Client over rt; nil means http.DefaultTransport.
func NewClient(rt http.RoundTripper) *Client {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &Client{hc: &http.Client{
		Transport: rt,
		Timeout:   10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := checkURL(req.URL); err != nil {
				return err
			}
			if len(via) >= 10 {
				return fmt.Errorf("selfupdate: too many redirects")
			}
			return nil
		},
	}}
}

func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("selfupdate: refusing %s request to host %s; https only", u.Scheme, u.Host)
	}
	if !allowedHosts[u.Hostname()] || u.Port() != "" {
		return fmt.Errorf("selfupdate: refusing to contact host %s; not a GitHub release host", u.Host)
	}
	return nil
}

// Release is a published GitHub release.
type Release struct {
	Tag    string
	assets map[string]string
}

type apiRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (c *Client) get(ctx context.Context, rawURL, name string, limit int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: %s: %w", name, err)
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "rig-selfupdate")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: fetching %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("selfupdate: fetching %s: HTTP %d", name, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: reading %s: %w", name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("selfupdate: %s exceeds the %d byte limit", name, limit)
	}
	return b, nil
}

// Lookup returns the latest release, or the one for tag when tag is not
// empty. Drafts and prereleases are never returned.
func (c *Client) Lookup(ctx context.Context, tag string) (*Release, error) {
	endpoint := releaseAPI + "/latest"
	label := "latest release"
	if tag != "" {
		endpoint = releaseAPI + "/tags/" + url.PathEscape(tag)
		label = "release " + tag
	}
	b, err := c.get(ctx, endpoint, label, maxAPI)
	if err != nil {
		return nil, err
	}
	var r apiRelease
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("selfupdate: decoding %s: %w", label, err)
	}
	if r.Draft || r.Prerelease {
		if tag == "" {
			tag = r.Tag
		}
		return nil, fmt.Errorf("selfupdate: %s is a draft or prerelease and cannot be installed", tag)
	}
	rel := &Release{Tag: r.Tag, assets: map[string]string{}}
	for _, a := range r.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel, nil
}

// AssetName is the release asset for a platform.
func AssetName(goos, goarch string) string {
	n := "rig-" + goos + "-" + goarch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// Download is the fetched, not yet verified, material for one platform.
type Download struct {
	Asset  string
	Binary []byte
	Sums   []byte
	Sig    []byte
}

// Fetch downloads the platform binary, SHA256SUMS and SHA256SUMS.sig.
func (c *Client) Fetch(ctx context.Context, rel *Release, goos, goarch string) (*Download, error) {
	asset := AssetName(goos, goarch)
	d := &Download{Asset: asset}
	for _, p := range []struct {
		name  string
		limit int64
		dst   *[]byte
	}{
		{"SHA256SUMS", maxSmall, &d.Sums},
		{"SHA256SUMS.sig", maxSmall, &d.Sig},
		{asset, maxBinary, &d.Binary},
	} {
		u, ok := rel.assets[p.name]
		if !ok {
			if p.name != asset {
				return nil, &CheckError{Check: "signature", Detail: fmt.Sprintf("release %s has no asset %s", rel.Tag, p.name)}
			}
			return nil, fmt.Errorf("selfupdate: release %s has no asset %s", rel.Tag, p.name)
		}
		b, err := c.get(ctx, u, p.name, p.limit)
		if err != nil {
			return nil, err
		}
		*p.dst = b
	}
	return d, nil
}

// Verify checks the download against the embedded keys.
func (d *Download) Verify() error {
	return Verify(d.Asset, d.Binary, d.Sums, d.Sig)
}
