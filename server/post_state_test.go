package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// postPath resolves a pair the projection would accept, failing the test when it would not.
func postPath(t *testing.T, category, slug string) string {
	t.Helper()
	path, err := postTargetPath(category, slug)
	if err != nil {
		t.Fatalf("postTargetPath(%q, %q): %v", category, slug, err)
	}
	return path
}

// TestAPostEventLandsTheFile: an article delta has to reach posts/, which is what lets a node
// that never took the edit render the article.
func TestAPostEventLandsTheFile(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()

	applyPostEvent(testEvent(EvPostSaved, `{"id":4242,"category":"essay","slug":"from the peer",`+
		`"date":"2026-01-02","summary":"s","tags":["a","b"],"body":"hello",`+
		`"updated_at":"2026-01-03T04:05:06Z"}`))

	path := postPath(t, "essay", "from the peer")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the article did not land: %v", err)
	}
	text := string(body)
	for _, want := range []string{"id: 4242\n", "title: from the peer\n", "date: 2026-01-02\n",
		"summary: s\n", "tags: [a, b]\n", "words: 5\n", "updated_at: 2026-01-03T04:05:06Z\n",
		"\nhello"} {
		if !strings.Contains(text, want) {
			t.Errorf("front matter is missing %q:\n%s", want, text)
		}
	}
	// The index is what the pages read, so the article has to be in it, not only on disk.
	if _, ok := findPostByID(4242); !ok {
		t.Error("the article landed on disk but not in the index")
	}
}

// TestAPostEventMovesTheFileItRenamed: the record's key is its id, so a retitled article lands
// at a new path and the file it came from goes with it. Leaving it behind would put one id in
// two files, and the next lookup would land on whichever the directory order reached first.
func TestAPostEventMovesTheFileItRenamed(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()

	applyPostEvent(testEvent(EvPostSaved, `{"id":4243,"category":"essay","slug":"first name",`+
		`"date":"2026-01-02","body":"one"}`))
	from := postPath(t, "essay", "first name")
	if _, err := os.Stat(from); err != nil {
		t.Fatalf("the article did not land: %v", err)
	}

	applyPostEvent(testEvent(EvPostSaved, `{"id":4243,"category":"software","slug":"second name",`+
		`"date":"2026-01-02","body":"one"}`))
	to := postPath(t, "software", "second name")
	if _, err := os.Stat(to); err != nil {
		t.Fatalf("the article did not move: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Errorf("the file the article moved from is still there: %v", err)
	}
}

// TestAPostTombstoneRemovesTheFile: a deletion travels as a record, so the file it names has to
// go, and the id must not resolve anywhere afterwards.
func TestAPostTombstoneRemovesTheFile(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()

	applyPostEvent(testEvent(EvPostSaved, `{"id":4244,"category":"essay","slug":"to delete",`+
		`"date":"2026-01-02","body":"gone soon"}`))
	path := postPath(t, "essay", "to delete")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the article did not land: %v", err)
	}

	applyPostEvent(testEvent(EvPostRemoved, `{"id":4244}`))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the tombstone left the file behind: %v", err)
	}
	if _, ok := findPostByID(4244); ok {
		t.Error("the tombstone left the id resolvable")
	}
}

// TestAPostEventRefusesWhatItCannotPlace: the slug arrives from the peer, so one that walks out
// of posts/ has to be refused. A category the registry does not declare is a different matter:
// posts/ counts an undeclared directory once it holds a post, and the handler that takes an edit
// accepts the same key, so refusing it here would make the two nodes disagree about an article
// both of them can render.
func TestAPostEventRefusesWhatItCannotPlace(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()

	applyPostEvent(testEvent(EvPostSaved, `{"id":4246,"category":"essay","slug":"../escaped",`+
		`"date":"2026-01-02","body":"b"}`))
	applyPostEvent(testEvent(EvPostSaved, `{"id":4247,"category":"../up","slug":"x",`+
		`"date":"2026-01-02","body":"b"}`))
	applyPostEvent(testEvent(EvPostSaved, `{"id":4248,"category":"essay","slug":"",`+
		`"date":"2026-01-02","body":"b"}`))

	if _, err := os.Stat(filepath.Join(RootPosts, "..", "escaped.md")); !os.IsNotExist(err) {
		t.Errorf("a slug walked out of posts/: %v", err)
	}
	for _, id := range []int{4246, 4247, 4248} {
		if _, ok := findPostByID(id); ok {
			t.Errorf("article %d was written from a payload that names no place", id)
		}
	}

	applyPostEvent(testEvent(EvPostSaved, `{"id":4249,"category":"undeclared","slug":"still filed",`+
		`"date":"2026-01-02","body":"b"}`))
	if _, ok := findPostByID(4249); !ok {
		t.Error("a category the registry does not declare was refused, unlike the local handler")
	}
}

// TestResolvePostDateKeepsTheLineForAnUnchangedDay: the editor's date field carries a day, so a
// form value cannot express the time an RFC3339 line holds. A save that leaves the day alone has
// to leave the line alone, or an unrelated edit silently drops the time.
func TestResolvePostDateKeepsTheLineForAnUnchangedDay(t *testing.T) {
	useExampleSite(t)

	cases := []struct {
		slug   string
		stored string
		form   string
		want   string
	}{
		{"date-same-day", "2026-07-01T23:30:00+08:00", "2026-07-01", "2026-07-01T23:30:00+08:00"},
		{"date-other-day", "2026-07-01T23:30:00+08:00", "2026-07-02", "2026-07-02"},
		{"date-no-form-value", "2026-07-01", "", "2026-07-01"},
		{"date-no-stored-line", "", "2026-07-03", "2026-07-03"},
	}
	for _, c := range cases {
		t.Run(c.slug, func(t *testing.T) {
			path := postPath(t, "essay", c.slug)
			writeTestFile(t, path, "---\ntitle: "+c.slug+"\ndate: "+c.stored+"\n---\n\nb")
			if got := resolvePostDate(c.form, path); got != c.want {
				t.Errorf("resolvePostDate(%q) over a stored %q line = %q, want %q",
					c.form, c.stored, got, c.want)
			}
		})
	}
}

// TestResolvePostDateFallsBackToToday: a new post has no file to read a date from, so the day it
// is written is the day it publishes under.
func TestResolvePostDateFallsBackToToday(t *testing.T) {
	useExampleSite(t)
	if got, want := resolvePostDate("", ""), time.Now().Format("2006-01-02"); got != want {
		t.Errorf("resolvePostDate for a new post = %q, want %q", got, want)
	}
}
