package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Myp3uK/ConanLinuxManager/internal/version"
)

func serve(t *testing.T, status int, body string) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/repos/Myp3uK/ConanLinuxManager/releases/latest"; r.URL.Path != want {
			t.Errorf("asked for %s, want %s", r.URL.Path, want)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return Client{BaseURL: srv.URL}
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	saved := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = saved })
}

func TestAnUpdateIsOfferedOnlyWhenItIsNewer(t *testing.T) {
	c := serve(t, http.StatusOK, `{"tag_name":"v0.3.0","html_url":"https://example.invalid/r","published_at":"2026-10-10T00:00:00Z"}`)
	withVersion(t, "v0.2.0")
	got := c.Check(context.Background(), true)
	if !got.Available || got.Latest != "v0.3.0" || !got.Supported {
		t.Fatalf("expected v0.3.0 on offer: %+v", got)
	}
	if got.Error != "" {
		t.Errorf("unexpected error: %s", got.Error)
	}

	withVersion(t, "v0.3.0")
	if got := c.Check(context.Background(), true); got.Available {
		t.Errorf("the running release is the newest; nothing to offer: %+v", got)
	}
	withVersion(t, "v0.4.0")
	if got := c.Check(context.Background(), true); got.Available {
		t.Errorf("a newer build must not be told to downgrade: %+v", got)
	}
}

func TestABuildWithoutAReleaseIsNeverOfferedAnUpdate(t *testing.T) {
	c := serve(t, http.StatusOK, `{"tag_name":"v9.9.9"}`)
	withVersion(t, "dev")
	got := c.Check(context.Background(), true)
	if got.Supported {
		t.Error("a dev build cannot be updated by the panel")
	}
	if got.Available {
		t.Error("a dev build must not be offered an update: there is no telling which is newer")
	}
	if got.Reason == "" {
		t.Error("the panel needs a reason to show")
	}
}

func TestWithoutTheUpdaterTheVersionsAreStillReported(t *testing.T) {
	c := serve(t, http.StatusOK, `{"tag_name":"v0.3.0"}`)
	withVersion(t, "v0.2.0")
	got := c.Check(context.Background(), false)
	if got.Supported {
		t.Error("the host has no updater")
	}
	// The operator can still see that something newer exists and install it.
	if !got.Available || got.Latest != "v0.3.0" {
		t.Errorf("the newer release should still be named: %+v", got)
	}
}

func TestDraftsAndPreReleasesAreIgnored(t *testing.T) {
	withVersion(t, "v0.2.0")
	for _, body := range []string{
		`{"tag_name":"v0.3.0","draft":true}`,
		`{"tag_name":"v0.3.0","prerelease":true}`,
	} {
		got := serve(t, http.StatusOK, body).Check(context.Background(), true)
		if got.Available || got.Latest != "" {
			t.Errorf("%s should not be offered: %+v", body, got)
		}
	}
}

func TestNoReleasesAtAllIsNotAFailure(t *testing.T) {
	c := serve(t, http.StatusNotFound, `{"message":"Not Found"}`)
	withVersion(t, "v0.2.0")
	got := c.Check(context.Background(), true)
	if got.Error != "" {
		t.Errorf("a repository without releases is not an error: %s", got.Error)
	}
	if got.Available {
		t.Error("nothing is published, so nothing is on offer")
	}
}

func TestGitHubFailingIsReportedRatherThanGuessed(t *testing.T) {
	c := serve(t, http.StatusInternalServerError, `oops`)
	withVersion(t, "v0.2.0")
	got := c.Check(context.Background(), true)
	if got.Error == "" {
		t.Error("the operator should be told the check failed")
	}
	if got.Available {
		t.Error("a failed check must not offer an update")
	}
}
