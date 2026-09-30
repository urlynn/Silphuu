package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCommentFragmentRevalidatesInsteadOfRefetching holds the fragment's validator: the fragment
// is rechecked on every read, so with no ETag a revalidation has nothing to compare and every
// reader costs the origin a full render and the whole list. This guard fails if the tag stops
// tracking the body — a tag that never moves would freeze the list at whatever the edge holds.
func TestCommentFragmentRevalidatesInsteadOfRefetching(t *testing.T) {
	useExampleSite(t)
	initTemplates()
	initCommentStore()

	mux := http.NewServeMux()
	registerAPIRoutes(mux)

	seedComment := func(id, content string) {
		c := &Comment{ID: id, Nick: "probe", Content: content, Date: "2026-09-30T09:00:00+08:00", PostID: 1}
		cmMu.Lock()
		cm.byID[c.ID] = c
		cm.byPost[1] = append(cm.byPost[1], c)
		cmMu.Unlock()
	}
	seedComment(NewULID(), "hello")

	get := func(ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", RouteCommentList+"?post_id=1", nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	first := get("")
	if first.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", RouteCommentList, first.Code, firstLine(first.Body.String()))
	}
	// A body-less render would satisfy every assertion below for the wrong reason.
	if !strings.Contains(first.Body.String(), "hello") {
		t.Fatalf("the seeded comment is not in the fragment: %s", firstLine(first.Body.String()))
	}
	if cc := first.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("the fragment carries no ETag, so nothing downstream can revalidate it")
	}

	// Unchanged: the reader that already holds these bytes gets the tag back and no body.
	again := get(tag)
	if again.Code != http.StatusNotModified {
		t.Errorf("revalidating an unchanged fragment = %d, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Errorf("the 304 carried %d bytes, want 0", again.Body.Len())
	}
	if got := again.Header().Get("ETag"); got != tag {
		t.Errorf("the 304 answered tag %q, want the request's %q", got, tag)
	}

	// Changed: the tag has to move with the bytes, or a reader keeps a list the origin no
	// longer serves.
	seedComment(NewULID(), "second")
	changed := get(tag)
	if changed.Code != http.StatusOK {
		t.Fatalf("a changed fragment = %d, want 200", changed.Code)
	}
	if got := changed.Header().Get("ETag"); got == tag {
		t.Errorf("the tag %q did not move with the body", got)
	}
	if !strings.Contains(changed.Body.String(), "second") {
		t.Errorf("the new comment is not in the fragment: %s", firstLine(changed.Body.String()))
	}
}

// TestFragmentRevalidationComparesTheWholeHeader covers the shapes a conditional request
// arrives in. A stale tag, a list of tags and the wildcard all have to land on the right side of
// the decision, and a wrong "match" would serve an empty body for a list the reader does not
// hold.
func TestFragmentRevalidationComparesTheWholeHeader(t *testing.T) {
	tag := `W/"0123456789ab"`
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"", false},
		{tag, true},
		{`"0123456789ab"`, true},
		{`"ffffffffffff"`, false},
		{`"ffffffffffff", ` + tag, true},
		{`W/"ffffffffffff", "0123456789ab"`, true},
		{"*", true},
	} {
		if got := matchesETag(tc.header, tag); got != tc.want {
			t.Errorf("matchesETag(%q, %q) = %v, want %v", tc.header, tag, got, tc.want)
		}
	}
}
