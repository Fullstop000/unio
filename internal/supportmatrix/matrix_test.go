package supportmatrix

import (
	"bytes"
	"os"
	"testing"
)

func TestProfilesAreComplete(t *testing.T) {
	if err := Validate(Profiles()); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedDocumentIsCurrent(t *testing.T) {
	want, err := Markdown()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../docs/API_SUPPORT.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("docs/API_SUPPORT.md is stale; run go generate ./...")
	}
}
