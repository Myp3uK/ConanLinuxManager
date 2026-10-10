// Package selfupdate asks GitHub which release is the newest and compares it
// with the running build.
//
// It only ever reads. Installing is deliberately not done here: the manager
// runs as an unprivileged user and cannot write its own binaries or restart
// its own unit, so the install is a root-owned script behind a systemd unit
// the manager is allowed to start and nothing more. What gets installed is
// decided by that script, not by anything the manager sends it, so an
// intruder in the panel can force an upgrade to the current official release
// and cannot substitute their own.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Myp3uK/ConanLinuxManager/internal/version"
)

// DefaultRepo is where releases are published.
const DefaultRepo = "Myp3uK/ConanLinuxManager"

// Release is the published release the manager compares itself against.
type Release struct {
	Tag         string    `json:"tag"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
}

// Status is what the panel shows: what is running, what is published, and
// whether the one can be replaced by the other.
type Status struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	URL       string    `json:"url,omitempty"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
	// Supported is false for a build that names no release, which cannot be
	// compared, and for a host without the updater installed.
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Client struct {
	HTTPClient *http.Client
	// BaseURL is GitHub's API; tests point it at their own server.
	BaseURL string
	Repo    string
}

func (c Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.github.com"
}

func (c Client) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

// Latest reads the newest published release. A repository with no releases
// answers 404, which is not an error the operator needs to see as a failure.
func (c Client) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.base(), c.repo())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("ask GitHub for the newest release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Release{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub answered %s", response.Status)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("read GitHub's answer: %w", err)
	}
	// A draft or a pre-release is not what an operator's panel should offer.
	if body.Draft || body.Prerelease {
		return Release{}, nil
	}
	return Release{Tag: body.TagName, URL: body.HTMLURL, PublishedAt: body.PublishedAt}, nil
}

// Check reports what the panel shows. installed says whether the host has the
// updater; without it the answer still names both versions, so an operator
// can see that an update exists and install it by hand.
func (c Client) Check(ctx context.Context, installed bool) Status {
	status := Status{Current: version.Version, CheckedAt: time.Now().UTC(), Supported: installed}
	if !version.Released() {
		status.Supported = false
		status.Reason = "сборка не из релиза: обновлять нечего и не с чем сравнивать"
	} else if !installed {
		status.Reason = "обновление из панели не установлено на этом хосте"
	}
	release, err := c.Latest(ctx)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Latest, status.URL = release.Tag, release.URL
	status.Available = version.Newer(release.Tag, version.Version)
	return status
}
