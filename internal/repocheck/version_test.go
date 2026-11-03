package repocheck

import (
	"os"
	"regexp"
	"testing"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
)

// TestGoVersionsMatch: the lcd349 binary reports the kit version of the
// headers, and looks for the ESP-IDF release VERSIONS pins.
func TestGoVersionsMatch(t *testing.T) {
	core, err := os.ReadFile(includeDir + "/lcd349_core.h")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`#define LCD349_VERSION_STR "([^"]+)"`).FindSubmatch(core); m == nil || string(m[1]) != lcd349kit.Version {
		t.Errorf("lcd349kit.Version %q != LCD349_VERSION_STR %q", lcd349kit.Version, m)
	}
	versions, err := os.ReadFile("../../VERSIONS")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?m)^ESP_IDF=(\S+)`).FindSubmatch(versions); m == nil || string(m[1]) != lcd349kit.IDFVersion {
		t.Errorf("lcd349kit.IDFVersion %q != VERSIONS ESP_IDF %q", lcd349kit.IDFVersion, m)
	}
}

// TestViewerSourceIsEmbedded: every file the viewer's CMakeLists builds is in
// the binary, so lcd349 can build the viewer from its own copy.
func TestViewerSourceIsEmbedded(t *testing.T) {
	cm, err := os.ReadFile(viewerDir + "/CMakeLists.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`add_executable\(lcd349-view ([^)]+)\)|add_library\(view_core STATIC ([^)]+)\)`).FindAllSubmatch(cm, -1) {
		for _, f := range regexp.MustCompile(`\S+\.c`).FindAll(append(m[1], m[2]...), -1) {
			if _, err := lcd349kit.ViewerSource.ReadFile("viewer/" + string(f)); err != nil {
				t.Errorf("viewer/%s isn't embedded", f)
			}
		}
	}
}

// TestRepoOwnerMatchesLauncher: lcd349 new pins the same repository the
// plugin launcher downloads from, so the release (task 10.5) changes both.
func TestRepoOwnerMatchesLauncher(t *testing.T) {
	launcher, err := os.ReadFile("../../plugin/scripts/lcd349-launch")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`https://github.com/([^/]+)/lcd349-kit/releases`).FindSubmatch(launcher)
	r := regexp.MustCompile(`^https://github.com/([^/]+)/lcd349-kit\.git$`).FindStringSubmatch(lcd349kit.RepoURL)
	if m == nil || r == nil || string(m[1]) != r[1] {
		t.Fatalf("launcher owner %q, RepoURL %q", m, lcd349kit.RepoURL)
	}
}

// TestContainersMatchIDFVersion: CI's ESP-IDF container, pinned by tag and
// index digest, and the scenarios doc's CI example run the ESP-IDF the kit
// is pinned to. Dependabot can't bump a workflow's container, so a VERSIONS
// change that misses them fails here.
func TestContainersMatchIDFVersion(t *testing.T) {
	ci, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	pins := regexp.MustCompile(`(?m)^\s*container:\s*espressif/idf:(\S+?)@sha256:[0-9a-f]{64}\s*$`).FindAllSubmatch(ci, -1)
	if len(pins) != 1 {
		t.Fatalf("ci.yml: %d espressif/idf containers pinned by tag and digest, want 1", len(pins))
	}
	if tag := string(pins[0][1]); tag != lcd349kit.IDFVersion {
		t.Errorf("ci.yml's container is espressif/idf:%s, IDFVersion is %s", tag, lcd349kit.IDFVersion)
	}
	if n := len(regexp.MustCompile(`espressif/idf:`).FindAll(ci, -1)); n != 1 {
		t.Errorf("ci.yml names espressif/idf %d times, want once (pinned)", n)
	}
	doc, err := os.ReadFile("../../docs/scenarios.md")
	if err != nil {
		t.Fatal(err)
	}
	tags := regexp.MustCompile(`(?m)^\s*container:\s*espressif/idf:(\S+)\s*$`).FindAllSubmatch(doc, -1)
	if len(tags) == 0 {
		t.Fatal("docs/scenarios.md has no espressif/idf container")
	}
	for _, m := range tags {
		if tag := string(m[1]); tag != lcd349kit.IDFVersion {
			t.Errorf("docs/scenarios.md's container is espressif/idf:%s, IDFVersion is %s", tag, lcd349kit.IDFVersion)
		}
	}
}
