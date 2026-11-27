package sut

import (
	"fmt"
	"memeval/internal/jsonstrict"
	"memeval/internal/suite"
	"net/url"
	"os"
	"strings"
)

// Upstream is maintained by the project owner in suts/<name>/UPSTREAM.json.
// Format (no comments in JSON): {"project":"...","repo":"https://...",
// "package":"...","version":"...","license":"..."}. All five are required;
// do not create empty placeholders or infer versions from the current install.
type Upstream struct {
	Project string `json:"project" jsonschema:"minLength=1"`
	Repo    string `json:"repo" jsonschema:"minLength=1,pattern=^https://"`
	Package string `json:"package" jsonschema:"minLength=1"`
	Version string `json:"version" jsonschema:"minLength=1"`
	License string `json:"license" jsonschema:"minLength=1"`
}

func (u Upstream) Validate() error {
	for _, f := range []struct{ name, value string }{{"project", u.Project}, {"repo", u.Repo}, {"package", u.Package}, {"version", u.Version}, {"license", u.License}} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("UPSTREAM.json: %s must be non-empty", f.name)
		}
	}
	v, e := url.Parse(u.Repo)
	if e != nil || v.Scheme != "https" || v.Hostname() == "" || v.User != nil {
		return fmt.Errorf("UPSTREAM.json: repo must be an https URL without credentials")
	}
	return nil
}
func LoadUpstream(path string) (*Upstream, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var u Upstream
	if e = jsonstrict.Decode(b, &u); e != nil {
		return nil, fmt.Errorf("UPSTREAM.json: %w", e)
	}
	if e = u.Validate(); e != nil {
		return nil, e
	}
	return &u, nil
}
func UpstreamSchema() ([]byte, error) { return suite.SchemaJSON(Upstream{}) }
