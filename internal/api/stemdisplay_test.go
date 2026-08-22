package api

import (
	"testing"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

func strp(s string) *string { return &s }

func TestCleanStem(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"readable dotted", "La.Hermana.De.Mi.Amigo.2024.1080p", "La Hermana De Mi Amigo 2024"},
		{"underscores", "Some_Scene_Title_720p_x264", "Some Scene Title"},
		{"spaces kept", "A Real Title", "A Real Title"},
		{"hash", "123eqawfdhsgaweroqr3raef", ""},
		{"hex with dashes but no words", "8f3a-91cc-4de1-aa02", ""},
		{"single word", "scene", ""},
		{"digits only", "12345.67890", ""},
		{"empty", "", ""},
		{"noise only", "1080p.x264", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanStem(tc.in); got != tc.want {
				t.Errorf("cleanStem(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A stem is never a curated title, however readable it is -- that is the
// whole point of the split.
func TestCuratedTitle_IgnoresStem(t *testing.T) {
	r := store.Release{Stem: strp("La.Hermana.De.Mi.Amigo")}
	if got := curatedTitle(r); got != "" {
		t.Errorf("curatedTitle with only a stem = %q, want empty", got)
	}
	if releaseIsIndexable(r, true) {
		t.Error("a release named only by its filename must not be indexable")
	}

	r.Title = strp("La Hermana De Mi Amigo")
	if got := curatedTitle(r); got != "La Hermana De Mi Amigo" {
		t.Errorf("curatedTitle = %q", got)
	}
	if releaseIsIndexable(r, false) {
		t.Error("a curated title nobody has pinned must not be indexable on its own")
	}
	if !releaseIsIndexable(r, true) {
		t.Error("a curated title a moderator pinned should be indexable")
	}
}

// The readable filenames are the dangerous ones for privacy: a legibility
// test cannot tell "Jane Doe - SiteRip 2019" from a real title, so
// indexability must not depend on one.
func TestReleaseIsIndexable_ReadableFilenameStillNotIndexable(t *testing.T) {
	r := store.Release{Stem: strp("Jane Doe - SiteRip 2019")}
	if cleanStem(*r.Stem) == "" {
		t.Fatal("precondition: this stem is meant to survive cleaning")
	}
	if releaseIsIndexable(r, true) {
		t.Error("a readable filename is still a filename; it must not open the page to crawlers")
	}
}

func TestDisplayTitle(t *testing.T) {
	for _, tc := range []struct {
		name string
		rel  store.Release
		want string
	}{
		{"title wins", store.Release{Title: strp("Real"), Stem: strp("junk.file.name")}, "Real"},
		{"stem cleaned", store.Release{Stem: strp("Some.Scene.Title")}, "Some Scene Title"},
		{"hash stem suppressed", store.Release{Stem: strp("123eqawfdhsgaweroqr3raef")}, "(untitled)"},
		{"nothing", store.Release{}, "(untitled)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayTitle(tc.rel); got != tc.want {
				t.Errorf("displayTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

// A crawlable listing must not carry filenames as link text -- an indexed
// browse page caches the name just as durably as a heading would.
func TestBuildCatalogueRelease_CrawlableSuppressesFilename(t *testing.T) {
	r := store.Release{ID: 1, Stem: strp("Jane Doe - SiteRip 2019")}

	if got := buildCatalogueRelease(r, nil, false, true).Title; got != "Jane Doe SiteRip 2019" {
		t.Errorf("non-crawlable title = %q, want the cleaned filename", got)
	}
	if got := buildCatalogueRelease(r, nil, true, true).Title; got != "(untitled)" {
		t.Errorf("crawlable title = %q, want (untitled)", got)
	}

	r.Title = strp("A Curated Name")
	if got := buildCatalogueRelease(r, nil, true, false).Title; got != "(untitled)" {
		t.Errorf("crawlable title, curated but unpinned = %q, want (untitled)", got)
	}
	if got := buildCatalogueRelease(r, nil, true, true).Title; got != "A Curated Name" {
		t.Errorf("crawlable title with a curated, pinned name = %q, want it kept", got)
	}
}
