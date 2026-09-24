package main

// comment_state.go — in-memory comment projection (Event Sourcing read model).
//
// Events are the source of truth: comment_added / comment_liked(±delta) /
// comment_deleted(tombstone) flow in via event.go's applyEvent -> storeApply; all reads
// (listing/sorting/likes/delete targeting) touch only this projection, with zero
// storage calls.
//
// Assembly semantics: orphan replies are promoted
// to the top level, nested levels sort by id ascending, three sort modes
// (oldest/newest/hottest).

import (
	"encoding/json"
	sortpkg "sort"
	"sync"
)

var (
	cmMu sync.RWMutex
	cm   commentStore
)

type commentStore struct {
	byPost map[int][]*Comment  // per post, appended in event order (= creation order)
	byID   map[string]*Comment // ULID -> *Comment
}

func initCommentStore() {
	cmMu.Lock()
	defer cmMu.Unlock()
	cm.byPost = make(map[int][]*Comment)
	cm.byID = make(map[string]*Comment)
}

// storeApply applies one event to the comment projection (shared by event.go replay
// and live events).
func storeApply(evt *Event) {
	var typ struct {
		ID     string `json:"id"`
		PostID int    `json:"post_id"`
		Rid    string `json:"rid"`
		Delta  int    `json:"delta"`
	}
	switch evt.Type {
	case EvCommentAdded:
		var c Comment
		if err := json.Unmarshal(evt.Payload, &c); err != nil || c.ID == "" {
			return
		}
		cmMu.Lock()
		if _, dup := cm.byID[c.ID]; dup {
			cmMu.Unlock()
			return // idempotent: apply each event only once
		}
		if c.Rid == "" {
			c.Rid = ""
		}
		cm.byID[c.ID] = &c
		cm.byPost[c.PostID] = append(cm.byPost[c.PostID], &c)
		cmMu.Unlock()

	case EvCommentLiked:
		if err := json.Unmarshal(evt.Payload, &typ); err != nil || typ.ID == "" {
			return
		}
		cmMu.Lock()
		if c, ok := cm.byID[typ.ID]; ok {
			if typ.Delta == 0 {
				typ.Delta = 1
			}
			c.Likes += typ.Delta
			if c.Likes < 0 {
				c.Likes = 0
			}
		}
		cmMu.Unlock()

	case EvCommentDeleted:
		if err := json.Unmarshal(evt.Payload, &typ); err != nil || typ.ID == "" {
			return
		}
		cmMu.Lock()
		if c, ok := cm.byID[typ.ID]; ok {
			list := cm.byPost[c.PostID]
			for i, x := range list {
				if x.ID == typ.ID {
					cm.byPost[c.PostID] = append(list[:i], list[i+1:]...)
					break
				}
			}
			delete(cm.byID, typ.ID)
		}
		cmMu.Unlock()
	}
}

func cmGet(id string) *Comment {
	cmMu.RLock()
	defer cmMu.RUnlock()
	c, ok := cm.byID[id]
	if !ok {
		return nil
	}
	cp := *c
	return &cp
}

func cmLikeCount(id string) int {
	if c := cmGet(id); c != nil {
		return c.Likes
	}
	return 0
}

func cmTotalCount() int {
	cmMu.RLock()
	defer cmMu.RUnlock()
	return len(cm.byID)
}

// commentSortModes are the orders sortCommentsSQL knows. Anything else — including the
// empty string a plain /guestbook carries — means oldest.
var commentSortModes = map[string]bool{"oldest": true, "newest": true, "hottest": true}

// normalizeCommentSort maps a requested order onto a known one. The value ends up in a
// Location header and in a form field, so it is never passed through verbatim.
func normalizeCommentSort(sort string) string {
	if commentSortModes[sort] {
		return sort
	}
	return "oldest"
}

// sortCommentsSQL keeps the legacy name: assembles the comment list for a post_id
// (orphan promotion, three sort modes) from the in-memory projection.
func sortCommentsSQL(postID int, sort string) []Comment {
	cmMu.RLock()
	all := make([]*Comment, 0, len(cm.byPost[postID]))
	all = append(all, cm.byPost[postID]...)
	cmMu.RUnlock()
	if len(all) == 0 {
		return nil
	}

	byID := make(map[string]*Comment, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}
	children := make(map[string][]*Comment)
	var topLevel []*Comment
	for _, c := range all {
		if c.Rid == "" {
			topLevel = append(topLevel, c)
			continue
		}
		if _, ok := byID[c.Rid]; ok {
			children[c.Rid] = append(children[c.Rid], c)
		} else {
			// orphan: promote to top level and clear the dangling rid
			c.Rid = ""
			topLevel = append(topLevel, c)
		}
	}
	for k := range children {
		sortpkg.Slice(children[k], func(i, j int) bool { return children[k][i].ID < children[k][j].ID })
	}

	switch sort {
	case "oldest":
	case "newest":
		for i, j := 0, len(topLevel)-1; i < j; i, j = i+1, j-1 {
			topLevel[i], topLevel[j] = topLevel[j], topLevel[i]
		}
	case "hottest":
		for i := 0; i < len(topLevel); i++ {
			for j := i + 1; j < len(topLevel); j++ {
				if topLevel[j].Likes > topLevel[i].Likes || (topLevel[j].Likes == topLevel[i].Likes && topLevel[j].ID > topLevel[i].ID) {
					topLevel[i], topLevel[j] = topLevel[j], topLevel[i]
				}
			}
		}
	}

	var result []Comment
	var addReplies func(list []*Comment)
	addReplies = func(list []*Comment) {
		for _, c := range list {
			result = append(result, *c)
			if kids, ok := children[c.ID]; ok {
				addReplies(kids)
			}
		}
	}
	addReplies(topLevel)
	return result
}
