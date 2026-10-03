package version

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v1.8.1", "1.8.1"},
		{"1.8.1", "1.8.1"},
		{" v2.0.0 ", "2.0.0"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeVersion(tt.in); got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSplitVersion(t *testing.T) {
	tests := []struct {
		in   string
		want [3]int
	}{
		{"1.8.1", [3]int{1, 8, 1}},
		{"2.0.0", [3]int{2, 0, 0}},
		{"1.0", [3]int{1, 0, 0}},
		{"", [3]int{0, 0, 0}},
		{"1.8.1-beta", [3]int{1, 8, 1}},
	}
	for _, tt := range tests {
		if got := splitVersion(tt.in); got != tt.want {
			t.Errorf("splitVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"1.8.1", "1.8.0", true},
		{"2.0.0", "1.9.9", true},
		{"1.8.1", "1.8.1", false},
		{"1.7.0", "1.8.1", false},
		{"1.8.2", "1.8.1", true},
	}
	for _, tt := range tests {
		if got := isNewer(tt.latest, tt.current); got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestCheckLatest(t *testing.T) {
	t.Run("dev and empty versions fail honestly", func(t *testing.T) {
		result := CheckLatest("dev")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "do not map to a release version") {
			t.Fatalf("message = %q", result.Message)
		}

		result = CheckLatest("")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "current version is unknown") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("update available", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.8"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusUpdateAvailable {
			t.Fatalf("status = %q, want %q", result.Status, StatusUpdateAvailable)
		}
		if !strings.Contains(result.Message, "Update available: 1.10.7 -> 1.10.8") || !strings.Contains(result.Message, "To update:") {
			t.Fatalf("message = %q", result.Message)
		}
		want := "Update available: 1.10.7 -> 1.10.8\nTo update:\n  pegasus upgrade, then pegasus update --cli <cli>\n  (DARQ: darq upgrade, then darq update --cli <cli>)\nRelease: https://github.com/balerdis/engram/releases/latest"
		if result.Message != want {
			t.Fatalf("message = %q, want %q", result.Message, want)
		}
		for _, banned := range []string{"brew", "go install", "Gentleman-Programming"} {
			if strings.Contains(result.Message, banned) {
				t.Fatalf("message mentions %q: %q", banned, result.Message)
			}
		}
	})

	t.Run("up to date", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusUpToDate {
			t.Fatalf("status = %q, want %q", result.Status, StatusUpToDate)
		}
		if result.Message != "" {
			t.Fatalf("message = %q, want empty", result.Message)
		}
	})

	t.Run("non-200 becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rate limited", http.StatusForbidden)
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "403 Forbidden") || !strings.Contains(result.Message, "GH_TOKEN") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("decode error becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "could not read the GitHub response") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("missing tag becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":""}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "did not return a release version") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("timeout becomes check failed", func(t *testing.T) {
		withCheckTimeout(t, 20*time.Millisecond)
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.8"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "took too long to respond") {
			t.Fatalf("message = %q", result.Message)
		}
	})
}

func TestCheckLatestUsesGitHubToken(t *testing.T) {
	t.Run("prefers GH_TOKEN", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "gh-token")
		t.Setenv("GITHUB_TOKEN", "github-token")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer gh-token" {
				t.Fatalf("authorization = %q", got)
			}
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Fatalf("accept = %q", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})

	t.Run("falls back to GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "github-token")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer github-token" {
				t.Fatalf("authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})

	t.Run("omits authorization header without token", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("authorization = %q, want empty", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})
}

func TestUpdateInstructions(t *testing.T) {
	want := "To update:\n  pegasus upgrade, then pegasus update --cli <cli>\n  (DARQ: darq upgrade, then darq update --cli <cli>)\nRelease: https://github.com/balerdis/engram/releases/latest"
	if updateInstructions != want {
		t.Fatalf("updateInstructions = %q, want %q", updateInstructions, want)
	}
}

func withCheckServer(t *testing.T, handler http.Handler) {
	t.Helper()

	srv := httptest.NewServer(handler)
	oldURL := githubLatestReleaseURL
	githubLatestReleaseURL = srv.URL
	t.Cleanup(func() {
		githubLatestReleaseURL = oldURL
		srv.Close()
	})
}

func withCheckTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	oldTimeout := checkTimeout
	checkTimeout = timeout
	t.Cleanup(func() { checkTimeout = oldTimeout })
}

func TestNonOKStatusMessage(t *testing.T) {
	if got := nonOKStatusMessage(fmt.Sprintf("%d %s", http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))); !strings.Contains(got, "GH_TOKEN") {
		t.Fatalf("message = %q", got)
	}
}

func TestRepoOwnerIsFork(t *testing.T) {
	if repoOwner != "balerdis" {
		t.Fatalf("repoOwner = %q, want balerdis", repoOwner)
	}
	if !strings.Contains(githubLatestReleaseURL, "/repos/balerdis/engram/") && !strings.HasPrefix(githubLatestReleaseURL, "http://127.0.0.1") {
		t.Fatalf("githubLatestReleaseURL = %q", githubLatestReleaseURL)
	}
}

func TestUpdateCheckDisabled(t *testing.T) {
	tests := []struct {
		val  string
		want bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {" Yes ", true},
		{"", false}, {"0", false}, {"false", false}, {"no", false}, {"on", false},
	}
	for _, tt := range tests {
		t.Run(tt.val, func(t *testing.T) {
			t.Setenv("ENGRAM_NO_UPDATE_CHECK", tt.val)
			if got := UpdateCheckDisabled(); got != tt.want {
				t.Fatalf("UpdateCheckDisabled() with %q = %v, want %v", tt.val, got, tt.want)
			}
		})
	}
}

func TestCheckLatestSkipsRequestWhenDisabled(t *testing.T) {
	for _, val := range []string{"1", "true", "YES"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("ENGRAM_NO_UPDATE_CHECK", val)
			hits := 0
			withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
			}))

			result := CheckLatest("1.10.7")
			if hits != 0 {
				t.Fatalf("server hit %d times, want 0", hits)
			}
			if result.Status != StatusUpToDate || result.Message != "" {
				t.Fatalf("result = %+v, want silent up-to-date", result)
			}
		})
	}

	t.Run("falsy value still checks", func(t *testing.T) {
		t.Setenv("ENGRAM_NO_UPDATE_CHECK", "0")
		hits := 0
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
		}))
		if result := CheckLatest("1.10.7"); result.Status != StatusUpdateAvailable || hits != 1 {
			t.Fatalf("result = %+v, hits = %d", result, hits)
		}
	})
}
