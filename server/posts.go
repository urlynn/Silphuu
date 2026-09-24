package main

// posts.go — in-memory post index (outside the Event domain; the .md files are the source
// of truth). Rebuilt by scanning the posts/ directory at startup and on .md change.
// Views come from the views.go memory layer.

import (
	"encoding/json"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// In-memory index.

var postIdx struct {
	mu   sync.RWMutex
	list []Post      // sorted by Date descending
	byID map[int]int // id -> index into list
}

// rebuildPostIndex fully rescans the posts/ directory and rebuilds the in-memory index
// (idempotent; called at startup and on .md changes).
func rebuildPostIndex() {
	var posts []Post
	byID := map[int]int{}
	for _, d := range postDirs() {
		dir := d.Path
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			text := string(body)
			title, tags, modTime, rest, words, id, summary := parseFrontmatter(text)
			if id <= 0 {
				continue
			}
			slug := strings.TrimSuffix(e.Name(), ".md")
			fi, _ := e.Info()
			// Missing frontmatter date -> fall back to the file's mtime. Compare against the
			// zero time parseFrontmatter returns when the date is missing, never time.Now():
			// two such calls essentially never match at nanosecond precision.
			if modTime.IsZero() {
				if fi != nil {
					modTime = fi.ModTime()
				}
			}
			p := Post{
				ID:         id,
				Slug:       slug,
				Category:   d.Name,
				Title:      title,
				Tags:       tags,
				Date:       modTime,
				Body:       rest,
				SummaryStr: summary,
				Summary:    template.HTML(summary),
				Words:      words,
			}
			if p.Date.IsZero() {
				if fi != nil {
					p.Date = fi.ModTime()
				}
			}
			posts = append(posts, p)
		}
	}
	sort.SliceStable(posts, func(i, j int) bool { return posts[i].Date.After(posts[j].Date) })
	for i := range posts {
		byID[posts[i].ID] = i
	}
	postIdx.mu.Lock()
	postIdx.list = posts
	postIdx.byID = byID
	postIdx.mu.Unlock()
	publishPostsIndex()
	log.Printf("[posts] 内存索引重建完成: %d 篇", len(posts))
}

// PostJSONItem is one post as published to the frontend search index. Only what the
// browser reads is here: the engine scores on title/tags/category/summary/body and links
// by id, so slug, date and word count have no consumer.
type PostJSONItem struct {
	ID       int      `json:"id"`
	Category string   `json:"cat"`
	Title    string   `json:"title"`
	Tags     []string `json:"tags"`
	Summary  string   `json:"summary"`
	Text     string   `json:"text"`
}

// postsIndexPath is where the published search index is written.
func postsIndexPath() string {
	return filepath.Join(RootAssets, "posts.json")
}

// postsIndexURL returns the search index's URL, hash-addressed so a changed index reaches
// clients without a purge.
func postsIndexURL() string {
	url := PrefixAssets + "/posts.json"
	if h, err := computeFileHash(postsIndexPath()); err == nil {
		return url + "?v=" + h
	}
	return url
}

// publishPostsIndex writes the frontend search index.
//
// It is derived from the post files alone — the live view counter deliberately stays out,
// so the file changes only when an article does and its URL hash is a faithful identity
// for its content.
func publishPostsIndex() {
	posts := idxSnapshot(true)
	items := make([]PostJSONItem, 0, len(posts))
	for _, p := range posts {
		items = append(items, PostJSONItem{
			ID:       p.ID,
			Category: p.Category,
			Title:    p.Title,
			Tags:     p.Tags,
			Summary:  p.SummaryStr,
			Text:     stripMarkdown(p.Body),
		})
	}
	data, err := json.Marshal(items)
	if err != nil {
		log.Printf("posts: marshal index error: %v", err)
		return
	}
	if err := os.WriteFile(postsIndexPath(), data, 0644); err != nil {
		log.Printf("posts: write index error: %v", err)
	}
}

// prefetchPostIDs returns the posts the service worker warms: the pinned post, and the
// newest post that is not the pinned one — the same two the home page highlights.
func prefetchPostIDs() []int {
	pinned := loadAppConfig().PinnedPostID
	posts := idxSnapshot(false)
	out := []int{}
	for _, p := range posts {
		if p.ID == pinned {
			out = append(out, p.ID)
			break
		}
	}
	for _, p := range posts {
		if p.ID != pinned {
			out = append(out, p.ID)
			break
		}
	}
	return out
}

// idxSnapshot returns a deep copy of the index list (with views attached).
func idxSnapshot(withBody bool) []Post {
	postIdx.mu.RLock()
	defer postIdx.mu.RUnlock()
	out := make([]Post, 0, len(postIdx.list))
	for _, p := range postIdx.list {
		q := p
		q.Views = postViewCount(p.ID)
		if !withBody {
			q.Body = ""
		}
		out = append(out, q)
	}
	return out
}

func idxByID(id int) *Post {
	postIdx.mu.RLock()
	defer postIdx.mu.RUnlock()
	if i, ok := postIdx.byID[id]; ok {
		p := postIdx.list[i]
		p.Views = postViewCount(p.ID)
		return &p
	}
	return nil
}

func idxDate(p Post) time.Time {
	if !p.Date.IsZero() {
		return p.Date
	}
	return time.Time{}
}

// Public reads.

func backfillPosts() {
	rebuildPostIndex()
}

func getPostSummary(id int) string {
	if p := idxByID(id); p != nil {
		return p.SummaryStr
	}
	return ""
}

func getPostTitleFromDB(id int) string {
	if p := idxByID(id); p != nil {
		return p.Title
	}
	return ""
}

func getAllTagsFromDB() []string {
	counts := map[string]int{}
	for _, p := range idxSnapshot(false) {
		for _, t := range p.Tags {
			t = strings.TrimSpace(t)
			if t != "" {
				counts[t]++
			}
		}
	}
	var result []string
	for t := range counts {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool {
		if counts[result[i]] == counts[result[j]] {
			return result[i] < result[j]
		}
		return counts[result[i]] > counts[result[j]]
	})
	return result
}

func loadPostsFromDB() []Post {
	return idxSnapshot(false)
}

func loadPostsByCategoryFromDB(cat string) []Post {
	var posts []Post
	for _, p := range idxSnapshot(false) {
		if p.Category == cat {
			posts = append(posts, p)
		}
	}
	return posts
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			result = append(result, t)
		}
	}
	return result
}
