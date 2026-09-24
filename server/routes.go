package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
)

// 1. Top-level root directories.
//
// These are variables, not constants: InitRoots() (server/paths.go) repoints the
// content roots at a deployment directory so configuration, posts and uploaded
// media can live outside this repository. Defaults are relative to the process
// working directory (server/), which is unchanged behaviour.
var (
	// Engine roots — ship with the release, never user data.
	RootSrc       = "src"
	RootTemplates = "templates"

	// Content roots — repointed by InitRoots / SILPHUU_HOME.
	RootAssets      = "assets"
	RootStatic      = "static"
	RootConfig      = "config"
	RootState       = "state"
	RootRender      = "render"
	RootPublic      = "public"
	RootPublish     = "publish"
	RootPosts       = "posts"
	RootError       = filepath.Join(RootRender, "error")
	RootFontSources = "Fonts"

	// Staging root — visitor submissions awaiting review. It is neither served
	// nor published, so an unreviewed upload never has a URL.
	RootStaging = "staging"
)

// URL path prefixes (never change at runtime, so these stay constants).
const (
	PrefixAssets = "/assets"
	PrefixStatic = "/static"
	PrefixCommit = "/admin/commit"
	PrefixThumb  = "/assets/thumb"
)

// 2. On-disk physical paths (used by Go I/O, ImageMagick, avifenc, ffmpeg).
//
// Declared here and assigned by initDerivedPaths() in server/paths.go, so that
// every path is recomputed whenever the roots change.
var (
	// Media directories (all singular names)
	DirImagePost    string
	DirImageComment string
	DirPhoto        string
	DirSticker      string
	DirBackground   string
	DirSponsor      string

	// Thumbnail directories
	DirThumbRoot           string
	DirThumbPhotoBlur      string
	DirThumbStickerRoot    string
	DirThumbStickerPreview = func(group string) string {
		return filepath.Join(RootAssets, "thumb", "sticker", group, "preview")
	}
	DirThumbStickerDynamic = func(group string) string {
		return filepath.Join(RootAssets, "thumb", "sticker", group, "dynamic")
	}

	// Font directory
	DirFont string

	// Data file paths
	FileEventLog        string
	FileEventJSONL      string
	FileSyncState       string
	FileSnapshot        string
	FileConfig          string
	FileSocial          string
	FileFontsData       string
	FilePhotoData       string
	FileBackgroundData  string
	FileStickerData     string
	FileSponsorData     string
	FileTopicData       string
	FileFriendData      string
	FileUIStrings       string
	FileUIStringsAdmin  string
	FilePreviewFixtures string
	FileNavSignatures   string
	FileFontMap         string
	FileFontExtraChars  string
	DirPresets          string

	// Staging directories, one per flow that accepts visitor submissions.
	DirStagingFriend  string
	DirStagingSticker string
)

// 3. Web URL paths (used by HTML templates, AOT baking, frontend JS).

// 3.1 Prefix constants
const (
	PrefixAPI   = "/api"
	PrefixAdmin = "/admin"
	PrefixPost  = "/post"
)

// 3.2 Full route constants (shared by route registration and the frontend)
const (
	// Interactive
	RouteView               = PrefixAPI + "/view"
	RouteStats              = PrefixAPI + "/stats"
	RoutePublicEcho         = PrefixAPI + "/echo"
	RouteCommentAdd         = PrefixAPI + "/comment/add"
	RouteCommentLike        = PrefixAPI + "/comment/like"
	RouteCommentList        = PrefixAPI + "/comment/list"
	RouteCommentUpload      = PrefixAPI + "/comment/upload"
	RouteStickerUpload      = PrefixAPI + "/sticker/upload"
	RouteCommentDeleteOwner = PrefixAPI + "/comment/delete"
	RouteUnifiedDelete      = PrefixAPI + "/comment/{id}"
	RouteFriendApply        = PrefixAPI + "/friend/apply"

	// Admin Commit
	RouteAdminCommitCategory       = PrefixCommit + "/{category}"
	RouteAdminCommitCategoryAction = PrefixCommit + "/{category}/{action}"
	RouteAdminSystemReload         = PrefixAdmin + "/action/system/reload"

	// Admin Pages
	RouteAdminDashboard = PrefixAdmin + "/dashboard"
	RouteAdminNewPost   = PrefixAdmin + "/action/post/new"
	RouteAdminEditPost  = PrefixAdmin + "/action/post/{id}/edit"
	RouteAdminStatus    = PrefixAdmin + "/status"
	RouteAdminEcho      = PrefixAdmin + "/status/echo"
	RouteAdminSentinel  = PrefixAdmin + "/status/sentinel"

	// Pages
	PageHome      = "/"
	PageAbout     = "/about"
	PageGuestbook = "/guestbook"
	// PageFunError serves the "random lalafell" page; the path avoids /error-style
	// naming that clashes with real 4xx/5xx error-page semantics (page content unchanged).
	PageFunError  = "/lalafell"
	PageFeed      = "/feed.xml"
	PageSitemap   = "/sitemap.xml"
	PageRobots    = "/robots.txt"
	PageArchive   = "/archive"
	PageSponsor   = "/sponsor"
	PageFriends   = "/friends"
	PageCategory  = "/topic/{category}"
	PagePosts     = "/posts"
	PagePost      = "/post/{id}"
	PagePostSlash = "/post/{id}/"
	PageSearch    = "/search"

	// Assets
	RouteFaviconSvg = "/favicon.svg"
	RouteFaviconIco = "/favicon.ico"
	RouteSwJs       = "/sw.js"
)

// 3.3 Dynamic route helper functions
func URLPost(id any) string           { return "/post/" + fmt.Sprint(id) }
func URLPostEdit(id any) string       { return "/admin/action/post/" + fmt.Sprint(id) + "/edit" }
func URLUnifiedComment(id any) string { return "/api/comment/" + fmt.Sprint(id) }
func URLTopic(category any) string    { return "/topic/" + fmt.Sprint(category) }

// URLTag returns the tag-filter URL. The tag travels in the fragment, so it never reaches the
// server: /posts renders every card once and the browser hides the ones that do not carry the
// tag — the only shape that survives a static publish, where no handler answers per-tag URLs.
func URLTag(tag any) string { return PagePosts + "#" + url.PathEscape(fmt.Sprint(tag)) }

// Static page routes (no params)
func URLArchive() string     { return PageArchive }
func URLAbout() string       { return PageAbout }
func URLFriends() string     { return PageFriends }
func URLSponsor() string     { return PageSponsor }
func URLGuestbook() string   { return PageGuestbook }
func URLFunError() string    { return PageFunError }
func URLSearch() string      { return PageSearch }
func URLFriendApply() string { return RouteFriendApply }

// URLImagePost returns the URL for an article-body illustration.
func URLImagePost(filename string) string {
	return filepath.ToSlash(filepath.Join(PrefixStatic, "image/post", filename))
}

// URLImageComment returns the URL for a comment-section illustration.
func URLImageComment(filename string) string {
	return filepath.ToSlash(filepath.Join(PrefixStatic, "image/comment", filename))
}

// URLPhoto returns the full-size album photo URL.
func URLPhoto(filename string) string {
	return filepath.ToSlash(filepath.Join(PrefixStatic, "photo", filename))
}

// URLThumbPhotoBlur returns the album 230px gaussian-blur skeleton image URL.
func URLThumbPhotoBlur(photoID string) string {
	return fmt.Sprintf("%s/photo/blur/%s.avif", PrefixThumb, photoID)
}

// URLThumbSticker returns a sticker thumbnail URL (mode: "preview" static | "dynamic" animated).
//
// The URL carries a content hash: /assets/ is served immutable for a year, and asset()
// cannot supply the token — its manifest is built once at startup, while thumbnails are
// regenerated at runtime.
func URLThumbSticker(group, name, mode string) string {
	dir := DirThumbStickerPreview(group)
	if mode != "dynamic" {
		mode = "preview"
	} else {
		dir = DirThumbStickerDynamic(group)
	}
	url := fmt.Sprintf("%s/sticker/%s/%s/%s.avif", PrefixThumb, group, mode, name)
	if h, err := computeFileHash(filepath.Join(dir, name+".avif")); err == nil {
		return url + "?v=" + h
	}
	return url
}

// URLCommit returns the write-gateway URL for a category.
func URLCommit(category string) string {
	return fmt.Sprintf("%s/%s", PrefixCommit, category)
}

// registerAPIRoutes mounts the API surface: the admin write gateway and the public
// interactive gateway. It lives here, next to the URL constants and buildSiteURLs, so
// the three can be read against each other — every URL the frontend is handed must
// have a matching registration, and both delete spellings the comment JS can emit
// (DELETE /api/comment/{id} for admins, POST /api/comment/delete for the owner token)
// route to the same handler.
//
// Shared with routes_test.go on purpose: a test that registers its own table cannot
// notice a route the server never mounted.
func registerAPIRoutes(mux *http.ServeMux) {
	// 1. Admin-only write gateway (strong auth, tamper-proof).
	mux.HandleFunc("POST "+RouteAdminCommitCategory, handleCommitGateway)
	mux.HandleFunc("POST "+RouteAdminCommitCategoryAction, handleCommitGateway)

	mux.HandleFunc("POST "+RouteAdminSystemReload, handleSystemReload)

	// 2. Public interactive gateway (rate control, anti-abuse).
	mux.HandleFunc("POST "+RouteView, handleViewAPI)
	mux.HandleFunc("GET "+RouteStats, handleStatsAPI)
	mux.HandleFunc("GET "+RoutePublicEcho, handlePublicEcho)
	mux.HandleFunc("POST "+RouteCommentAdd, handleCommentAdd)
	mux.HandleFunc("POST "+RouteCommentLike, handleCommentLike)
	mux.HandleFunc("POST "+RouteCommentUpload, handleCommentUploadImage)
	mux.HandleFunc("POST "+RouteStickerUpload, handleUploadSticker)
	mux.HandleFunc("POST "+RouteFriendApply, handleFriendApply)
	mux.HandleFunc("GET "+RouteCommentList, handleCommentList)
	mux.HandleFunc("DELETE "+RouteUnifiedDelete, handleUnifiedCommentDelete)
	mux.HandleFunc("POST "+RouteCommentDeleteOwner, handleUnifiedCommentDelete)
}

// buildSiteURLs builds the dictionary injected into the frontend window.SITE_URLS.
func buildSiteURLs() map[string]interface{} {
	return map[string]interface{}{
		"postsJSON":    postsIndexURL(),
		"stickersJSON": stickerIndexURL(),
		"comment": map[string]interface{}{
			"add":       RouteCommentAdd,
			"like":      RouteCommentLike,
			"del":       RouteUnifiedDelete,
			"delOwner":  RouteCommentDeleteOwner,
			"list":      RouteCommentList,
			"uploadImg": RouteCommentUpload,
		},
		"commit": map[string]interface{}{
			"photo":        URLCommit("photo"),
			"photoReorder": URLCommit("photo/reorder"),
			"config":       URLCommit("config"),
			"background":   URLCommit("background"),
			"setOffset":    URLCommit("background/set-offset"),
			"setAvatar":    URLCommit("background/set-avatar"),
			"refreshStks":  URLCommit("refresh-stickers"),
			"post":         URLCommit("post"),
			"sponsor":      URLCommit("sponsor"),
			"font":         URLCommit("font"),
			"fontActivate": URLCommit("font/activate"),
			"fontSave":     URLCommit("font/save"),
			"uploadImg":    URLCommit("post"), // article illustrations: CommitPresets keys are post/comment/photo/background; "post" is the article-illustration preset
			"rebuild":      URLCommit("rebuild"),
		},
		"stickerUpload": RouteStickerUpload,
		"friendApply":   RouteFriendApply,
		"view":          RouteView,
		"stats":         RouteStats,
		"assets":        PrefixAssets,
		"static":        PrefixStatic,
		"admin": map[string]interface{}{
			"dashboard": RouteAdminDashboard,
			"newPost":   RouteAdminNewPost,
			"edit":      RouteAdminEditPost,
			"status":    RouteAdminStatus,
			"sentinel":  RouteAdminSentinel,
			"root":      PrefixAdmin,
		},
		"pages": map[string]interface{}{
			"home":      PageHome,
			"about":     PageAbout,
			"guestbook": PageGuestbook,
			"funError":  PageFunError,
			"archive":   PageArchive,
			"sponsor":   PageSponsor,
			"friends":   PageFriends,
			"search":    PageSearch,
			"post":      PrefixPost,
			"posts":     PagePosts,
			"topic":     "/topic",
		},
	}
}

// buildPageResources builds the asset table injected into the frontend window.__PAGE_RESOURCES__.
// Layered separately from buildSiteURLs: this only holds hashed asset URLs for frontend
// build artifacts / CDN cache entrypoints, with no backend route semantics (routes live
// in SITE_URLS). routeAssets() reads it when loading page-specific resources.
func buildPageResources() map[string]string {
	return map[string]string{
		"coreBundleCss":    asset("css/core.bundle.css"),
		"coreBundleJs":     asset("js/core.bundle.js"),
		"homeBundleCss":    asset("css/home.bundle.css"),
		"homeBundleJs":     asset("js/home.bundle.js"),
		"browseBundleCss":  asset("css/browse.bundle.css"),
		"browseBundleJs":   asset("js/browse.bundle.js"),
		"articleBundleCss": asset("css/article.bundle.css"),
		"articleBundleJs":  asset("js/article.bundle.js"),
		"commentBundleCss": asset("css/comment.bundle.css"),
		"commentBundleJs":  asset("js/comment.bundle.js"),
		"adminBundleCss":   asset("css/admin.bundle.css"),
	}
}
