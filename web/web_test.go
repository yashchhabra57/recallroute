package web

import (
	"bytes"
	"io/fs"
	"os"
	"testing"
)

// The demo files served to the UI must stay identical to the test fixtures.
func TestDemoDataMatchesFixtures(t *testing.T) {
	for _, name := range []string{"openfda_sample.json", "inventory_sample.csv"} {
		served, err := fs.ReadFile(Static(), "demo/"+name)
		if err != nil {
			t.Fatal(err)
		}
		fixture, err := os.ReadFile("../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(served, fixture) {
			t.Errorf("web/static/demo/%s differs from testdata/%s", name, name)
		}
	}
}

func TestIndexEmbedded(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "style.css"} {
		if _, err := fs.Stat(Static(), name); err != nil {
			t.Errorf("%s not embedded: %v", name, err)
		}
	}
}
