package main

// post_state.go — the post projection: the article records the event log carries, and the
// markdown files they land as.
//
// An article is one file, so one event carries it whole: the fields of its front matter and
// its body. The receiving node renders the same markdown from them, which is what makes the
// two nodes hold the same bytes without the file itself crossing. The renderer is shared with
// the handler that takes the edit, so the two writers cannot drift into two formats.
//
// The record's key in this domain is the article's id, never its path: a rename moves the file
// and a retitled article keeps its URL, so the path is a consequence of the fields rather than
// something to look the record up by.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EvPostSaved carries one article: its front matter fields and its body.
	EvPostSaved = "post_saved"
	// EvPostRemoved carries the id of an article that is gone (tombstone).
	EvPostRemoved = "post_removed"
)

// postSavedPayload is one article as it is written to posts/<category>/<slug>.md.
//
// The date and the update stamp travel as the strings that go into the front matter rather
// than as times: only the node that took the edit knows the resolved date, and resolving it
// again here would print a different day on the peer.
type postSavedPayload struct {
	ID        int      `json:"id"`
	Category  string   `json:"category"`
	Slug      string   `json:"slug"`
	Date      string   `json:"date"`
	Summary   string   `json:"summary,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Body      string   `json:"body"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// postRemovedPayload names the article a tombstone removes.
type postRemovedPayload struct {
	ID int `json:"id"`
}

// renderPostMarkdown renders one article as the file it is stored in. Both the handler that
// takes an edit and the projection that lands it go through here, so one event cannot produce
// two different files.
func renderPostMarkdown(p postSavedPayload) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("id: %d\n", p.ID))
	b.WriteString("title: " + p.Slug + "\n")
	b.WriteString("date: " + p.Date + "\n")
	if p.Summary != "" {
		b.WriteString("summary: " + p.Summary + "\n")
	}
	if len(p.Tags) > 0 {
		b.WriteString("tags: [" + strings.Join(p.Tags, ", ") + "]\n")
	}
	b.WriteString(fmt.Sprintf("words: %d\n", len([]rune(p.Body))))
	if p.UpdatedAt != "" {
		b.WriteString("updated_at: " + p.UpdatedAt + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(p.Body)
	return b.String()
}

// postTargetPath resolves an article's fields to the file it belongs at. Both halves come from
// the peer, so both are checked: the category has to be a registry key, and the slug has to
// stay one path segment.
func postTargetPath(category, slug string) (string, error) {
	if !validCategoryKey(category) {
		return "", fmt.Errorf("category %q is not a registry key", category)
	}
	if slug == "" || slug != filepath.Base(filepath.Clean(slug)) {
		return "", fmt.Errorf("slug %q is not one path segment", slug)
	}
	return filepath.Join(RootPosts, postDirFor(category), slug+".md"), nil
}

// applyPostEvent lands one article change and asks for the pages it moved.
func applyPostEvent(evt *Event) {
	switch evt.Type {
	case EvPostSaved:
		var p postSavedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.ID <= 0 {
			return
		}
		target, err := postTargetPath(p.Category, p.Slug)
		if err != nil {
			log.Printf("post: %s %d: %v", EvPostSaved, p.ID, err)
			return
		}
		want := renderPostMarkdown(p)
		// The node that took the edit wrote this file already. Rewriting identical bytes would
		// only move the mtime the render fingerprint reads.
		if current, err := os.ReadFile(target); err == nil && string(current) == want {
			return
		}
		// Look the record up before the write: afterwards the id is at the target too, and the
		// file it moved from would be left behind holding the same id.
		ref, moved := findPostByID(p.ID)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			log.Printf("post: %s %d: %v", EvPostSaved, p.ID, err)
			return
		}
		if err := os.WriteFile(target, []byte(want), 0o644); err != nil {
			log.Printf("post: %s %d: %v", EvPostSaved, p.ID, err)
			return
		}
		if moved {
			if from := filepath.Join(ref.Dir, ref.Slug+".md"); from != target {
				if err := os.Remove(from); err != nil {
					log.Printf("post: %s %d: leaving %s: %v", EvPostSaved, p.ID, from, err)
				}
			}
		}
		rebuildPostIndex()
		invalidatePostCache(p.ID)

	case EvPostRemoved:
		var p postRemovedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.ID <= 0 {
			return
		}
		ref, ok := findPostByID(p.ID)
		if !ok {
			return // the node that deleted it has nothing left to do
		}
		if err := os.Remove(filepath.Join(ref.Dir, ref.Slug+".md")); err != nil {
			log.Printf("post: %s %d: %v", EvPostRemoved, p.ID, err)
			return
		}
		rebuildPostIndex()
		// The article is gone, so its page is gone: the invalidation drops it rather than
		// overwriting it.
		invalidatePostCache(p.ID)
	}
}
