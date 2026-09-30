package main

// photo_state.go — the album's records as an event projection.
//
// A record is metadata; its bytes live under static/photo. The node that takes an upload
// writes the bytes and commits the record to the log, and every node then lands the same
// delta on its own config/photo.json. That is what lets a node which never saw the upload
// still render the photo.
//
// The file stays the on-disk form: the renderer and the font collector read it, and a record
// placed there by hand survives a delta that does not mention it.
//
// applyPhotoEvent runs only for events that actually arrive, never during the startup replay.
// Replaying the log would rewrite the site's own file on every start, which is the
// full-replacement shape the log exists to avoid.

import (
	"encoding/json"
	"log"
)

const (
	// EvPhotoAdded carries a whole Photo. The projection assigns the Order.
	EvPhotoAdded = "photo_added"
	// EvPhotoRemoved is a tombstone: its payload carries the id it drops.
	EvPhotoRemoved = "photo_removed"
	// EvPhotoOrdered carries the new order, front to back.
	EvPhotoOrdered = "photo_ordered"
)

// photoRemovedPayload is the tombstone body.
type photoRemovedPayload struct {
	ID string `json:"id"`
}

// photoOrderedPayload is the reorder body.
type photoOrderedPayload struct {
	IDs []string `json:"ids"`
}

// photoIndex returns the position of an album record, or -1 when the list does not hold it.
func photoIndex(list []Photo, id string) int {
	for i, p := range list {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// photoOrderEqual reports whether two lists name the same records in the same order.
func photoOrderEqual(a, b []Photo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// applyPhotoEvent lands one album delta on disk and invalidates the page that shows it.
// Every branch is idempotent, so a repeated delivery changes nothing.
func applyPhotoEvent(evt *Event) {
	switch evt.Type {
	case EvPhotoAdded:
		var p Photo
		if json.Unmarshal(evt.Payload, &p) != nil || p.ID == "" {
			return
		}
		list := loadPhotos()
		if photoIndex(list, p.ID) >= 0 {
			return // already there: the delivery repeated
		}
		p.Order = len(list)
		if err := savePhotos(append(list, p)); err != nil {
			log.Printf("photo: land %s: %v", EvPhotoAdded, err)
			return
		}

	case EvPhotoRemoved:
		var p photoRemovedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.ID == "" {
			return
		}
		list := loadPhotos()
		kept := make([]Photo, 0, len(list))
		for _, e := range list {
			if e.ID != p.ID {
				kept = append(kept, e)
			}
		}
		if len(kept) == len(list) {
			return // already gone
		}
		if err := savePhotos(kept); err != nil {
			log.Printf("photo: land %s: %v", EvPhotoRemoved, err)
			return
		}

	case EvPhotoOrdered:
		var p photoOrderedPayload
		if json.Unmarshal(evt.Payload, &p) != nil || len(p.IDs) == 0 {
			return
		}
		list := loadPhotos()
		ordered := make([]Photo, 0, len(p.IDs))
		for i, id := range p.IDs {
			at := photoIndex(list, id)
			if at < 0 {
				continue
			}
			e := list[at]
			e.Order = i
			ordered = append(ordered, e)
		}
		// The payload is the whole order, not a delta: a record it does not name is dropped.
		// Whoever builds the payload is the one holding the full list.
		if photoOrderEqual(list, ordered) {
			return // already in that order
		}
		if err := savePhotos(ordered); err != nil {
			log.Printf("photo: land %s: %v", EvPhotoOrdered, err)
			return
		}

	default:
		return
	}
	invalidateCache("/about")
}
