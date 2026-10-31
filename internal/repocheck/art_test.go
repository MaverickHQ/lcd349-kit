package repocheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const redrawArt = "run python3 tools/media/art.py"

// TestReadmeArtMatchesGoldens enforces requirement 2.9: the README's art
// (docs/images) shows the scenario goldens as they are now. tools/media/art.py
// draws it and records each golden's sha256 in docs/images/sources.json; CI
// checks the record and doesn't draw.
func TestReadmeArtMatchesGoldens(t *testing.T) {
	stale, err := staleArt("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stale {
		t.Error(s)
	}
}

func TestStaleGoldenIsCaught(t *testing.T) {
	root := t.TempDir()
	golden := "examples/x/tests/goldens/home.png"
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte("drawn"))
	write("docs/images/sources.json", fmt.Sprintf(`{%q: %q}`, golden, hex.EncodeToString(sum[:])))
	write(golden, "drawn")
	if stale, err := staleArt(root); err != nil || len(stale) != 0 {
		t.Fatalf("unchanged golden: stale = %q, err = %v", stale, err)
	}
	write(golden, "changed")
	stale, err := staleArt(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || !strings.Contains(stale[0], golden) || !strings.Contains(stale[0], redrawArt) {
		t.Fatalf("changed golden: stale = %q", stale)
	}
}

// staleArt lists, with what to do, each golden in docs/images/sources.json
// whose sha256 differs from the one recorded there, or that is gone.
func staleArt(root string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, "docs/images/sources.json"))
	if err != nil {
		return nil, fmt.Errorf("%w; %s", err, redrawArt)
	}
	var want map[string]string
	if err := json.Unmarshal(b, &want); err != nil {
		return nil, fmt.Errorf("docs/images/sources.json: %w; %s", err, redrawArt)
	}
	if len(want) == 0 {
		return nil, fmt.Errorf("docs/images/sources.json lists no goldens; %s", redrawArt)
	}
	var stale []string
	for rel, sum := range want {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			stale = append(stale, fmt.Sprintf("%s, which the README's art shows, is gone; %s", rel, redrawArt))
			continue
		}
		if h := sha256.Sum256(got); hex.EncodeToString(h[:]) != sum {
			stale = append(stale, fmt.Sprintf("%s changed since the README's art was drawn from it; %s", rel, redrawArt))
		}
	}
	sort.Strings(stale)
	return stale, nil
}
