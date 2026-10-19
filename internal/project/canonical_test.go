package project

import (
	"os/exec"
	"strings"
	"testing"
)

var canonicalCases = []struct{ in, want string }{
	{"My--Repo", "my-repo"},
	{"A__B", "a_b"},
	{"a----b____c", "a-b_c"},
	{"  Spaced Name  ", "spaced name"},
	{"", ""},
	{"   ", ""},
	{".GIT", ".git"},
	{"Ñandú--Proyecto", "ñandú-proyecto"},
	{"already-ok", "already-ok"},
}

func TestCanonical(t *testing.T) {
	for _, tc := range canonicalCases {
		got, warn := Canonical(tc.in)
		if got != tc.want {
			t.Errorf("Canonical(%q) = %q; want %q", tc.in, got, tc.want)
		}
		if (got != tc.in) != (warn != "") && tc.in != "" {
			t.Errorf("Canonical(%q): warning %q inconsistent with change", tc.in, warn)
		}
	}
}

func TestNormalizeDelegatesToCanonical(t *testing.T) {
	for _, tc := range canonicalCases {
		want := tc.want
		if want == "" {
			want = "unknown"
		}
		if got := normalize(tc.in); got != want {
			t.Errorf("normalize(%q) = %q; want %q", tc.in, got, want)
		}
	}
	for _, in := range []string{"/", "."} {
		if got := normalize(in); got != "unknown" {
			t.Errorf("normalize(%q) = %q; want unknown", in, got)
		}
	}
}

func TestExtractRepoNameEdgeCases(t *testing.T) {
	cases := map[string]string{
		"https://github.com/user/repo/":         "repo",
		"https://github.com/user/repo.git/":     "repo",
		"https://github.com/user/repo.GIT":      "repo",
		"git@github.com:user/repo.git/":         "repo",
		"ssh://git@host:2222/user/repo.git":     "repo",
		"ssh://git@host:2222/user/repo":         "repo",
		"ssh://git@host:2222":                   "",
		"https://github.com/user/My--Repo.git":  "My--Repo",
		"  https://github.com/user/repo.git \n": "repo",
		"/srv/git/repo.git":                     "repo",
		"/":                                     "",
	}
	for in, want := range cases {
		if got := extractRepoName(in); got != want {
			t.Errorf("extractRepoName(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestDetectProjectRemoteIsCanonical(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"https://github.com/u/My--Repo.git/").CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v\n%s", err, out)
	}
	if got := DetectProject(dir); got != "my-repo" {
		t.Fatalf("DetectProject = %q; want my-repo", got)
	}
}

func TestCanonicalRedactsCredentialsDeterministically(t *testing.T) {
	const secret = "sk-F4K3F4K3F4K3F4K3F4K3F4K3"
	got, warn := Canonical("App-" + secret)
	if strings.Contains(strings.ToLower(got), "f4k3") || strings.Contains(strings.ToLower(warn), "f4k3") {
		t.Fatalf("credential survived: %q / %q", got, warn)
	}
	if got != "app-[redacted:openai_key]" {
		t.Fatalf("got %q", got)
	}
	for _, in := range []string{"App-" + secret, "token=abcdefgh12345", "a://user:fakepassword1@h", "x_" + secret + "__y", "<private>x</private>", "Mi Proyecto"} {
		a, _ := Canonical(in)
		b, _ := Canonical(in)
		c, _ := Canonical(a)
		if a != b || a != c {
			t.Errorf("Canonical(%q): %q %q, again %q", in, a, b, c)
		}
	}
}
