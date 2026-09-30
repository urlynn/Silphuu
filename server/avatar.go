package main

import (
	"log"
	"path/filepath"
	"strings"
)

// The avatar is a record, not a discovery. config/hero.json lists the files the site
// actually wrote (see HeroConfig.Avatar), and rendering copies that list into markup
// without asking any disk what exists — a node that does not hold the site's static/
// tree renders exactly what a node that holds it renders.
//
// Format negotiation stays with the browser and costs nothing: a <source type> is
// chosen before a request is made, so a browser that cannot decode jxl never asks for
// it. A 404 is not a negotiation mechanism — a <source> pointing at a file the site
// never shipped is a broken image, not a fall-through (whatwg/html#8916).

// avatarPicture is the avatar in the shape the templates need: the URL every browser
// gets, and the higher-preference ones offered by type.
type avatarPicture struct {
	Src     string
	Sources []avatarSource
}

// avatarSource is one <source> a browser may choose by type.
type avatarSource struct {
	URL  string
	Type string
}

// imageMIME maps a file extension to the MIME type a <source type> carries. An
// extension outside this table has no answer to the format question, so such a file can
// only be the fallback.
var imageMIME = map[string]string{
	".jxl":  "image/jxl",
	".avif": "image/avif",
	".svg":  "image/svg+xml",
}

// avatarURL returns the site's avatar, or nil when the site has none. A pointer, not a
// value: the templates guard the whole element with {{with}}, and an empty struct is
// truthy, which would render an <img> with no src.
//
// The last recorded file becomes Src — that is what a browser without <picture> support
// gets — and the earlier ones become sources, most preferred first.
func avatarURL() *avatarPicture {
	urls := loadHeroConfig().Avatar
	if len(urls) == 0 {
		return nil
	}
	pic := &avatarPicture{Src: urls[len(urls)-1]}
	for _, u := range urls[:len(urls)-1] {
		mime, ok := imageMIME[strings.ToLower(filepath.Ext(u))]
		if !ok {
			log.Printf("avatar: %s carries no MIME type this engine can name; not offered as a source", u)
			continue
		}
		pic.Sources = append(pic.Sources, avatarSource{URL: u, Type: mime})
	}
	return pic
}

// errorImageURL returns the art for one error page.
//
// The URL follows from the status code, so there is nothing to discover and nothing to
// fall back to. A site that ships no art for a code renders a broken image.
func errorImageURL(code string) string {
	return StaticPrefix + "/image/error/" + code + ".avif"
}

// logAvatarResolution reports at startup which avatar the pages will carry, so an
// operator learns the record is empty without having to load a page.
func logAvatarResolution() {
	cfg := loadHeroConfig()
	if len(cfg.Avatar) == 0 {
		log.Printf("avatar: %s records no avatar; pages render none", dataReadPath(FileHeroData))
		return
	}
	msg := "avatar: serving " + cfg.Avatar[len(cfg.Avatar)-1]
	if len(cfg.Avatar) > 1 {
		msg += " (also offered: " + strings.Join(cfg.Avatar[:len(cfg.Avatar)-1], ", ") + ")"
	}
	log.Print(msg)
}
