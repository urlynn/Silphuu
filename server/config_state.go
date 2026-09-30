package main

// config_state.go — the projections for the json an administrator edits.
//
// These files are metadata: the renderer reads them, the admin UI writes them, and a node that
// never saw the edit still has to render the same page. They travel through the event log for
// that reason, the way the album's records do (photo_state.go).
//
// Each domain locates a record by a key unique within it, never by list position: two nodes
// can hold the same records in a different order, so a position means different records on
// each. A friend link is keyed by its URL, a sponsor by its nickname, a background by the file
// it was made from.
//
// The deltas are read-modify-write, and each reports whether it changed anything, so a
// repeated delivery lands nothing and a page is invalidated only when its content moved.
// applyConfigEvent runs only for events that actually arrive, never during the startup replay:
// see applyLive.

import (
	"encoding/json"
	"log"
	"slices"
)

const (
	// Friend links, keyed by URL.
	EvFriendAdded   = "friend_added"
	EvFriendEdited  = "friend_edited"
	EvFriendRemoved = "friend_removed"

	// Sponsors, keyed by nickname.
	EvSponsorAdded   = "sponsor_added"
	EvSponsorEdited  = "sponsor_edited"
	EvSponsorRemoved = "sponsor_removed"

	// Backgrounds, keyed by the file each was made from.
	EvBgAdded   = "bg_added"
	EvBgRemoved = "bg_removed"
	EvBgMode    = "bg_mode"
	EvBgOffset  = "bg_offset"
	EvBgTone    = "bg_tone"
	EvBgAvatar  = "bg_avatar"

	// The hero's own record, split by the field the panel edits: the tag row and the avatar.
	EvHeroTags   = "hero_tags"
	EvHeroAvatar = "hero_avatar"

	// The site's hand-written fields. Only the fields the panel form submits travel: the rest
	// of site.json is the deployment's — base_url names this node, and comment_salt signs the
	// comment-owner tokens.
	EvSiteEdited = "site_edited"
)

// friendEditedPayload names the link to replace by the URL it carried before the edit, which
// is also what the replacement may have changed.
type friendEditedPayload struct {
	URL  string     `json:"url"`
	Link FriendLink `json:"link"`
}

// friendRemovedPayload is the friend tombstone.
type friendRemovedPayload struct {
	URL string `json:"url"`
}

// sponsorEditedPayload names the sponsor to replace by the nickname it carried before the
// edit, which is also what the replacement may have changed.
type sponsorEditedPayload struct {
	Nick    string  `json:"nick"`
	Sponsor Sponsor `json:"sponsor"`
}

// sponsorRemovedPayload is the sponsor tombstone.
type sponsorRemovedPayload struct {
	Nick string `json:"nick"`
}

// bgRemovedPayload is the background tombstone.
type bgRemovedPayload struct {
	Src string `json:"src"`
}

// bgModePayload carries the mode the config should hold, not a toggle: replaying a toggle
// would depend on the value already there.
type bgModePayload struct {
	Mode string `json:"mode"`
}

// bgOffsetPayload carries a background's profile-card coordinates.
type bgOffsetPayload struct {
	Src string  `json:"src"`
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
}

// bgTonePayload carries a background's decoration tone.
type bgTonePayload struct {
	Src  string `json:"src"`
	Tone string `json:"tone"`
}

// bgAvatarPayload carries a background's avatar position.
type bgAvatarPayload struct {
	Src string `json:"src"`
	Pos string `json:"pos"`
}

// heroTagsPayload carries the tag row the config should hold, in render order. The row is a
// whole value, not a list edit: the panel posts the row it wants, so the event carries that
// row rather than a difference against the one already there.
type heroTagsPayload struct {
	Tags []string `json:"tags"`
}

// heroAvatarPayload carries the avatar record: the files that were written, most preferred
// first.
type heroAvatarPayload struct {
	Avatar []string `json:"avatar"`
}

// siteEditedPayload carries the fields the panel form submits, as the values the config
// should hold. A nil field is one the edit did not touch, which is what keeps a save from
// moving the keys the panel does not own.
type siteEditedPayload struct {
	SiteName     *string `json:"site_name,omitempty"`
	Author       *string `json:"author,omitempty"`
	Signature    *string `json:"signature,omitempty"`
	Motto        *string `json:"motto,omitempty"`
	PinnedPostID *int    `json:"pinned_post_id,omitempty"`
}

// applyConfigEvent lands one admin edit and invalidates the page it moved.
func applyConfigEvent(evt *Event) {
	switch evt.Type {
	case EvFriendAdded, EvFriendEdited, EvFriendRemoved:
		if applyFriendEvent(evt) {
			invalidateCache(PageFriends)
		}
	case EvSponsorAdded, EvSponsorEdited, EvSponsorRemoved:
		if applySponsorEvent(evt) {
			invalidateCache(PageSponsor)
		}
	case EvBgAdded, EvBgRemoved, EvBgMode, EvBgOffset, EvBgTone, EvBgAvatar:
		if applyBgEvent(evt) {
			// A background is drawn on the home page and named in the indexes, so every
			// list cache goes with it.
			invalidateListCache()
		}
	case EvHeroTags, EvHeroAvatar:
		if applyHeroEvent(evt) {
			// The tag row is drawn on the home hero and feeds the text subset; the avatar
			// is drawn on every page.
			invalidateAllCache()
		}
	case EvSiteEdited:
		changed, pageWide := applySiteEvent(evt)
		if changed {
			if pageWide {
				// The site name, the author, the signature and the motto are drawn on every page.
				invalidateAllCache()
			} else {
				// The pin moves the home page and the post it names.
				invalidateListCache()
			}
		}
	}
}

// friendIndex returns the position of a friend link, or -1 when the list does not hold it.
func friendIndex(list []FriendLink, url string) int {
	for i, f := range list {
		if f.URL == url {
			return i
		}
	}
	return -1
}

// applyFriendEvent lands one friend-link delta, reporting whether the list changed.
func applyFriendEvent(evt *Event) bool {
	switch evt.Type {
	case EvFriendAdded:
		var link FriendLink
		if json.Unmarshal(evt.Payload, &link) != nil || link.URL == "" {
			return false
		}
		list := loadFriends()
		if friendIndex(list, link.URL) >= 0 {
			return false // already there: the delivery repeated
		}
		return saveFriends(append(list, link)) == nil

	case EvFriendEdited:
		var p friendEditedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.URL == "" {
			return false
		}
		list := loadFriends()
		at := friendIndex(list, p.URL)
		if at < 0 {
			return false // the link it names is gone
		}
		list[at] = p.Link
		return saveFriends(list) == nil

	case EvFriendRemoved:
		var p friendRemovedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.URL == "" {
			return false
		}
		list := loadFriends()
		at := friendIndex(list, p.URL)
		if at < 0 {
			return false // already gone
		}
		return saveFriends(append(list[:at], list[at+1:]...)) == nil
	}
	return false
}

// sponsorIndex returns the position of a sponsor, or -1 when the list does not hold it.
func sponsorIndex(list []Sponsor, nick string) int {
	for i, s := range list {
		if s.Nick == nick {
			return i
		}
	}
	return -1
}

// applySponsorEvent lands one sponsor delta, reporting whether the list changed.
func applySponsorEvent(evt *Event) bool {
	switch evt.Type {
	case EvSponsorAdded:
		var s Sponsor
		if json.Unmarshal(evt.Payload, &s) != nil || s.Nick == "" {
			return false
		}
		list := loadSponsors()
		if sponsorIndex(list, s.Nick) >= 0 {
			return false // already there
		}
		return saveSponsors(append(list, s)) == nil

	case EvSponsorEdited:
		var p sponsorEditedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.Nick == "" {
			return false
		}
		list := loadSponsors()
		at := sponsorIndex(list, p.Nick)
		if at < 0 {
			return false // the sponsor it names is gone
		}
		list[at] = p.Sponsor
		return saveSponsors(list) == nil

	case EvSponsorRemoved:
		var p sponsorRemovedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.Nick == "" {
			return false
		}
		list := loadSponsors()
		at := sponsorIndex(list, p.Nick)
		if at < 0 {
			return false // already gone
		}
		return saveSponsors(append(list[:at], list[at+1:]...)) == nil
	}
	return false
}

// bgIndex returns the position of a background, or -1 when the list does not hold it.
func bgIndex(list []BgItem, src string) int {
	for i, b := range list {
		if b.Src == src {
			return i
		}
	}
	return -1
}

// applyBgEvent lands one background delta, reporting whether the config changed.
//
// It reads through the same loader the handler it mirrors used: adding starts from the stored
// file, while the field setters start from the candidate list the home page rendered.
func applyBgEvent(evt *Event) bool {
	switch evt.Type {
	case EvBgAdded:
		var item BgItem
		if json.Unmarshal(evt.Payload, &item) != nil || item.Src == "" {
			return false
		}
		cfg, _ := loadStoredBgConfig()
		if bgIndex(cfg.Backgrounds, item.Src) >= 0 {
			return false // already there
		}
		cfg.Backgrounds = append(cfg.Backgrounds, item)
		return saveBgConfig(cfg) == nil

	case EvBgRemoved:
		var p bgRemovedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.Src == "" {
			return false
		}
		cfg := loadBgConfig()
		at := bgIndex(cfg.Backgrounds, p.Src)
		if at < 0 {
			return false // already gone
		}
		cfg.Backgrounds = append(cfg.Backgrounds[:at], cfg.Backgrounds[at+1:]...)
		return saveBgConfig(cfg) == nil

	case EvBgMode:
		var p bgModePayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.Mode == "" {
			return false
		}
		cfg := loadBgConfig()
		if cfg.Mode == p.Mode {
			return false // already in that mode
		}
		cfg.Mode = p.Mode
		return saveBgConfig(cfg) == nil

	case EvBgOffset:
		var p bgOffsetPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.Src == "" {
			return false
		}
		cfg := loadBgConfig()
		at := bgIndex(cfg.Backgrounds, p.Src)
		if at < 0 {
			return false
		}
		cfg.Backgrounds[at].OffsetX, cfg.Backgrounds[at].OffsetY = p.X, p.Y
		return saveBgConfig(cfg) == nil

	case EvBgTone:
		var p bgTonePayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.Src == "" || p.Tone == "" {
			return false
		}
		cfg := loadBgConfig()
		at := bgIndex(cfg.Backgrounds, p.Src)
		if at < 0 {
			return false
		}
		cfg.Backgrounds[at].Tone = p.Tone
		return saveBgConfig(cfg) == nil

	case EvBgAvatar:
		var p bgAvatarPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.Src == "" || p.Pos == "" {
			return false
		}
		cfg := loadBgConfig()
		at := bgIndex(cfg.Backgrounds, p.Src)
		if at < 0 {
			return false
		}
		cfg.Backgrounds[at].AvatarPos = p.Pos
		return saveBgConfig(cfg) == nil
	}
	return false
}

// applyHeroEvent lands one hero delta, reporting whether the record changed.
//
// Each field travels on its own event and the record is written whole, so a tag edit keeps
// the avatar the panel did not touch.
func applyHeroEvent(evt *Event) bool {
	cfg := loadHeroConfig()
	switch evt.Type {
	case EvHeroTags:
		var p heroTagsPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return false
		}
		if slices.Equal(cfg.Tags, p.Tags) {
			return false // already there: the delivery repeated
		}
		cfg.Tags = p.Tags

	case EvHeroAvatar:
		var p heroAvatarPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return false
		}
		if slices.Equal(cfg.Avatar, p.Avatar) {
			return false // already there
		}
		cfg.Avatar = p.Avatar

	default:
		return false
	}
	return saveHeroConfig(cfg) == nil
}

// applySiteEvent lands one site.json delta. It reports whether the document changed and
// whether the change is one every page draws: the name, the author, the signature and the
// motto sit in the layout, while the pin only moves the home page and the post it names, so
// the two call for different widths of invalidation.
//
// It writes through saveAppConfig, so the keys the panel does not own survive on the node
// receiving the edit exactly as they do on the node that made it.
func applySiteEvent(evt *Event) (changed, pageWide bool) {
	var p siteEditedPayload
	if json.Unmarshal(evt.Payload, &p) != nil {
		return false, false
	}
	cfg := loadAppConfig()
	if p.SiteName != nil && cfg.SiteName != *p.SiteName {
		cfg.SiteName = *p.SiteName
		changed, pageWide = true, true
	}
	if p.Author != nil && cfg.Author != *p.Author {
		cfg.Author = *p.Author
		changed, pageWide = true, true
	}
	if p.Signature != nil && cfg.Signature != *p.Signature {
		cfg.Signature = *p.Signature
		changed, pageWide = true, true
	}
	if p.Motto != nil && cfg.Motto != *p.Motto {
		cfg.Motto = *p.Motto
		changed, pageWide = true, true
	}
	// The pin names a post by id, so the field travels as a pointer: zero means "unpin" and
	// an absent field means "this edit did not touch the pin".
	if p.PinnedPostID != nil && cfg.PinnedPostID != *p.PinnedPostID {
		cfg.PinnedPostID = *p.PinnedPostID
		changed = true
	}
	if !changed {
		return false, false // the delivery repeated, or named no field
	}
	return saveAppConfig(cfg) == nil, pageWide
}

// commitConfig records one admin edit in the log. A failure is the caller's to report: the
// page would otherwise answer success for an edit no other node learns about.
func commitConfig(typ string, payload any) error {
	if err := CommitEvent(typ, payload); err != nil {
		log.Printf("config: %s commit failed: %v", typ, err)
		return err
	}
	return nil
}
