package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	_ "image/gif"  // register the GIF decoder for image.DecodeConfig
	_ "image/jpeg" // register the JPEG decoder
	_ "image/png"  // register the PNG decoder
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Handlers: public.

// adminRespond returns JSON (AJAX) or a redirect (classic form) depending on the request type.
// Frontend fetch submissions carry the X-Requested-With: fetch header.
func adminRespond(w http.ResponseWriter, r *http.Request, msg string) {
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ok": "true", "msg": msg})
		return
	}
	http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
}

// adminError returns a JSON error (AJAX) or http.Error (classic form) depending on the request type.
func adminError(w http.ResponseWriter, r *http.Request, msg string, code int) {
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"ok": "false", "msg": msg})
		return
	}
	http.Error(w, msg, code)
}

// adminRespondRedirect is the unified response policy for admin-side actions: **full page refresh**.
//
// Only two paths:
//   - fetch (X-Requested-With: fetch) -> JSON {ok, redirect}; the caller navigates itself
//   - anything else (native form submission) -> 303 redirect, browser follows with a full load
//
// The HX-Redirect branch was deliberately removed: this project's htmx build does NOT
// handle that response header (its HX-* handling covers only HX-Boosted / HX-Current-URL /
// HX-History-Restore-Request / HX-Request / HX-Request-Type / HX-Source / HX-Target).
// The old implementation answered htmx requests with 200 + **empty body** + HX-Redirect;
// htmx didn't recognize the header, treated the empty body as content, matched nothing
// with hx-select="#content-wrapper", and the outerHTML swap deleted the container —
// "after saving, only Nav + background remain". Admin pages now all set
// hx-boost:inherited="false" (see admin.html .admin-page / editor.html editor-form), so
// no htmx requests reach this path anymore.
func adminRespondRedirect(w http.ResponseWriter, r *http.Request, url string) {
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ok": "true", "redirect": url})
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

type contextKey string

const heroBgCtxKey contextKey = "heroBg"

// heroBgs carries the home hero backgrounds from the 103 middleware to handleHome, so a
// preloaded image is always one the page actually renders.
type heroBgs struct {
	Desktop BgItem
	Mobile  BgItem
}

// pickHomeBackgrounds returns the home hero backgrounds: one for wide viewports, one for
// narrow ones.
//
// It is deterministic and takes BgItem.Device straight from the config: the home page is
// rendered once into a single cache file (public/index.html, see cachePath) so that a static
// web server can serve it. Which of the two a visitor gets is therefore decided by a CSS
// media query, never by the server — a per-request choice would need one HTML file per
// variant, which is exactly what makes "/" unpublishable.
//
// With no mobile background configured the desktop one is returned for both: a site that
// has not found a mobile image yet simply shows the same picture everywhere. With no
// desktop background configured either, the engine's own default is returned — see
// engineDefaultBgItem.
func pickHomeBackgrounds() (desktop, mobile BgItem) {
	var haveDesktop, haveMobile bool
	for _, b := range loadBgConfig().Backgrounds {
		if b.Src == "" {
			continue
		}
		b.Device = b.normalizedDevice()
		b.Tone = b.normalizedTone()
		b.AvatarPos = b.normalizedAvatarPos()
		switch b.Device {
		case "mobile":
			if !haveMobile {
				mobile, haveMobile = b, true
			}
		default:
			if !haveDesktop {
				desktop, haveDesktop = b, true
			}
		}
	}
	if !haveDesktop {
		desktop = engineDefaultBgItem()
	}
	if !haveMobile {
		mobile = desktop
	}
	return desktop, mobile
}

// engineDefaultBgItem is the wallpaper the hero falls back to when a site configures none.
// A hero without a wallpaper would borrow the fixed global tangram layer, which then stays
// in the viewport and overlaps the second screen's own layer.
func engineDefaultBgItem() BgItem {
	return BgItem{
		Src:       PrefixAssets + "/image/background/default.avif",
		Device:    "desktop",
		Tone:      "dark",
		OffsetX:   0.42,
		OffsetY:   0.50,
		AvatarPos: "left",
	}
}

// adminPreviewHeroBg is the hero background for the admin font preview: the desktop one,
// because the preview renders at a desktop viewport.
func adminPreviewHeroBg() BgItem {
	desktop, _ := pickHomeBackgrounds()
	return desktop
}

// handleHome renders the home page. The "GET /" pattern is Go's catch-all, so this also
// receives every path no other pattern claims — the check below is what keeps an unknown
// URL from being answered with the home page and a 200.
func handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != PageHome {
		http.NotFound(w, r)
		return
	}
	posts, err := loadPosts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	bgCfg := loadBgConfig()
	cfg := loadAppConfig()
	// Take the backgrounds the 103 middleware already picked from context; pick them here
	// otherwise. Both must name the same files, or the preload is wasted.
	var heroBg, heroBgMobile BgItem
	if v := r.Context().Value(heroBgCtxKey); v != nil {
		pair := v.(heroBgs)
		heroBg, heroBgMobile = pair.Desktop, pair.Mobile
	} else {
		heroBg, heroBgMobile = pickHomeBackgrounds()
	}

	var pinnedPost *Post
	var latestPost *Post
	if cfg.PinnedPostID > 0 {
		for i := range posts {
			if posts[i].ID == cfg.PinnedPostID {
				p := posts[i]
				pinnedPost = &p
				break
			}
		}
	}
	for i := range posts {
		if pinnedPost == nil || posts[i].ID != pinnedPost.ID {
			p := posts[i]
			latestPost = &p
			break
		}
	}

	data := PageData{
		Title:           siteName(),
		Nickname:        cfg.Author,
		Motto:           cfg.Motto,
		HomeMotto:       cfg.HomeMotto,
		Posts:           posts,
		PinnedPost:      pinnedPost,
		LatestPost:      latestPost,
		PinnedPostID:    cfg.PinnedPostID,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		AllTags:         getAllTags(),
		Year:            time.Now().Year(),
		Backgrounds:     bgCfg.Backgrounds,
		Interval:        bgCfg.Interval,
		Mode:            bgCfg.Mode,
		HeroBg:          heroBg,
		HeroBgMobile:    heroBgMobile,
		IsAdmin:         isAdmin(r),
		VisitStats:      getVisitStats(),
	}
	execute(w, r, "home.html", data)
}

func handleSponsor(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title:           pageTitle("赞助"),
		Nickname:        loadAppConfig().Author,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		Year:            time.Now().Year(),
		IsAdmin:         isAdmin(r),
		Sponsors:        loadSponsors(),
	}
	execute(w, r, "sponsor.html", data)
}

func handleFriends(w http.ResponseWriter, r *http.Request) {
	admin := isAdmin(r)
	// A pending application carries the submitter's address and its own URL; only the
	// moderator who has to decide on it reads the staging area.
	var pending []FriendApplication
	if admin {
		pending = loadFriendApplications()
	}
	data := PageData{
		Title:           pageTitle("友链"),
		Nickname:        loadAppConfig().Author,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		Year:            time.Now().Year(),
		IsAdmin:         admin,
		Friends:         loadFriends(),
		FriendPending:   pending,
	}
	execute(w, r, "friends.html", data)
}

func handleAuthor(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title:           pageTitle("作者"),
		Nickname:        loadAppConfig().Author,
		Motto:           loadAppConfig().Motto,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		Year:            time.Now().Year(),
		IsAdmin:         isAdmin(r),
		Photos:          loadPhotos(),
	}
	execute(w, r, "author.html", data)
}

func handleMessage(w http.ResponseWriter, r *http.Request) {
	posts, _ := loadPosts()
	bgCfg := loadBgConfig()
	// Sort carries through to the form so a posted comment re-renders in the order the
	// page is already showing. Without it the fragment falls back to its own default and
	// the list flips under the reader.
	//
	// The guestbook renders no sort switcher, so a reader cannot pick an order: its own
	// default is newest-first, which is what arriving at /guestbook should show. An
	// explicit ?sort= still wins.
	sort := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sort == "" {
		sort = "newest"
	}
	sort = normalizeCommentSort(sort)
	comments := sortCommentsSQL(0, sort) // post_id = 0 -> guestboard
	dummyPost := &Post{ID: 0, Category: "_page", Slug: "guestbook"}
	data := PageData{
		Title:           pageTitle("留言"),
		Nickname:        loadAppConfig().Author,
		Motto:           loadAppConfig().Motto,
		Posts:           posts,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		AllTags:         getAllTags(),
		Year:            time.Now().Year(),
		IsAdmin:         isAdmin(r),
		PostComments:    comments,
		CurrentPost:     dummyPost,
		Sort:            sort,
		Backgrounds:     bgCfg.Backgrounds,
		Interval:        bgCfg.Interval,
		Mode:            bgCfg.Mode,
	}
	execute(w, r, "guestbook.html", data)
}

// /lalafell: "random lalafell" random status-code page. The path avoids /error-style
// naming that clashes with real 4xx/5xx error-page semantics (page content unchanged).

func handleFunError(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title:           pageTitle("随机错误码"),
		Nickname:        loadAppConfig().Author,
		Motto:           loadAppConfig().Motto,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		Year:            time.Now().Year(),
		IsAdmin:         isAdmin(r),
	}
	execute(w, r, "fun_error.html", data)
}

func getBaseURL(r *http.Request) string {
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS == nil && (strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1")) {
		scheme = "http"
	}
	return scheme + "://" + host
}

// feedXML is the single definition of the RSS body.
//
// Both the dynamic handler and the static projection build it, so a published tree and a
// served response cannot disagree.
//
// lastBuildDate comes from the newest post, never from the clock: a projection rebuilds the
// whole tree, so a clock reading would change the bytes on every rebuild even when nothing
// was published. With no dated post at all the element is left out.
//
// The channel title is the site's own name, not the author field. A feed reader shows this
// title as the name of the thing that was subscribed to, and a deployment that sets
// site_name is naming the publication, not the person — the two are separate settings.
func feedXML(baseURL string) string {
	posts := loadAllPostsWithBody()
	cfg := loadAppConfig()

	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:content="http://purl.org/rss/1.0/modules/content/">
  <channel>
    <title>%s</title>
    <link>%s</link>
    <description>%s</description>
    <copyright>%s</copyright>
    <atom:link href="%s/feed.xml" rel="self" type="application/rss+xml"/>
    <language>zh-CN</language>`,
		template.HTMLEscapeString(siteName()),
		baseURL,
		template.HTMLEscapeString(cfg.Motto),
		template.HTMLEscapeString(copyrightNotice()),
		baseURL,
	)
	if len(posts) > 0 && !posts[0].Date.IsZero() {
		fmt.Fprintf(&b, "\n    <lastBuildDate>%s</lastBuildDate>", posts[0].Date.Format(time.RFC1123Z))
	}
	b.WriteString("\n    <generator>Silphuu Blog Engine</generator>")

	for _, p := range posts {
		title := template.HTMLEscapeString(p.Title)
		url := fmt.Sprintf("%s/post/%d", baseURL, p.ID)
		date := p.Date.Format(time.RFC1123Z)
		summary := template.HTMLEscapeString(p.SummaryStr)
		bodyHTML := string(renderMarkdown(p.Body))
		bodyHTML = strings.ReplaceAll(bodyHTML, "]]>", "]]]]><![CDATA[>")

		fmt.Fprintf(&b, `
    <item>
      <title>%s</title>
      <link>%s</link>
      <guid>%s</guid>
      <pubDate>%s</pubDate>
      <category>%s</category>
      <description>%s</description>
      <content:encoded><![CDATA[%s]]></content:encoded>
    </item>`, title, url, url, date, template.HTMLEscapeString(p.Category), summary, bodyHTML)
	}

	b.WriteString("\n  </channel>\n</rss>")
	return b.String()
}

func handleFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=2592000, stale-while-revalidate=86400")

	fmt.Fprint(w, feedXML(getBaseURL(r)))
}

// sitemapXML is the single definition of the sitemap body.
//
// Both the dynamic handler and the static projection build it, so a published tree and a
// served response cannot disagree.
//
// The static entries are the pages that carry content of their own. A sitemap is a list of
// documents worth indexing, not a list of reachable URLs, so the control surfaces stay out:
// search builds its result pages from query parameters, and the random-status page has no
// content of its own to index. The paths come from the route constants rather than being
// repeated as literals, so a route that moves cannot leave a stale URL behind here.
func sitemapXML(baseURL string) string {
	posts, _ := loadPosts()

	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s%s</loc><priority>1.0</priority></url>
  <url><loc>%s%s</loc><priority>0.9</priority></url>
  <url><loc>%s%s</loc><priority>0.8</priority></url>
  <url><loc>%s%s</loc><priority>0.7</priority></url>`,
		baseURL, PageHome, baseURL, PageAbout, baseURL, PageFriends, baseURL, PagePosts)

	for _, p := range posts {
		url := fmt.Sprintf("%s/post/%d", baseURL, p.ID)
		date := p.Date.Format("2006-01-02")
		if !p.UpdatedAt.IsZero() {
			date = p.UpdatedAt.Format("2006-01-02")
		}
		fmt.Fprintf(&b, `
  <url><loc>%s</loc><lastmod>%s</lastmod><priority>0.5</priority></url>`, url, date)
	}

	b.WriteString("\n</urlset>")
	return b.String()
}

func handleSitemap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=2592000, stale-while-revalidate=86400")

	fmt.Fprint(w, sitemapXML(getBaseURL(r)))
}

// robotsTxt is the single definition of the robots.txt body.
//
// Both the dynamic handler and the static projection build it, so a published tree and
// a served response cannot disagree about the sitemap location.
func robotsTxt(baseURL string) string {
	return fmt.Sprintf("User-agent: *\nAllow: /\n\nSitemap: %s/sitemap.xml\n", strings.TrimSuffix(baseURL, "/"))
}

func handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400, s-maxage=2592000, stale-while-revalidate=86400")

	fmt.Fprint(w, robotsTxt(getBaseURL(r)))
}

// Markdown stripping for the published search index.

var (
	reMDImg    = regexp.MustCompile(`!\[.*?\]\(.*?\)`)
	reMDLink   = regexp.MustCompile(`\[(.*?)\]\(.*?\)`)
	reMDCode   = regexp.MustCompile("(?s)```.*?```")
	reInlineC  = regexp.MustCompile("`[^`]+`")
	reHTMLTag  = regexp.MustCompile("<[^>]+>")
	reMDSymbol = regexp.MustCompile(`[#*~_>|\[\]\(\)\-\+]`)
)

func stripMarkdown(md string) string {
	s := reMDCode.ReplaceAllString(md, " ")
	s = reMDImg.ReplaceAllString(s, " ")
	s = reMDLink.ReplaceAllString(s, "$1")
	s = reInlineC.ReplaceAllString(s, " ")
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = reMDSymbol.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func loadAllPostsWithBody() []Post {
	return idxSnapshot(true)
}

// buildArchiveGroups groups posts by year -> month, producing the archive timeline data.
// The archive page (handleArchive) and the admin font preview's archive scenario share
// this single implementation — extracting it from handleArchive's inlined logic keeps
// preview and production from drifting apart.
func buildArchiveGroups(posts []Post) []ArchiveGroup {
	// Group by year -> month
	yearMap := make(map[int]map[int][]Post)
	for _, p := range posts {
		y, m := p.Date.Year(), int(p.Date.Month())
		if yearMap[y] == nil {
			yearMap[y] = make(map[int][]Post)
		}
		yearMap[y][m] = append(yearMap[y][m], p)
	}

	var years []int
	for y := range yearMap {
		years = append(years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))

	var groups []ArchiveGroup
	for i, y := range years {
		var months []ArchiveMonth
		var ys []int
		for m := range yearMap[y] {
			ys = append(ys, m)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(ys)))
		for _, m := range ys {
			months = append(months, ArchiveMonth{Month: m, Posts: yearMap[y][m]})
		}
		// All posts of this year
		var allPosts []Post
		for _, mp := range yearMap[y] {
			allPosts = append(allPosts, mp...)
		}
		sort.Slice(allPosts, func(a, b int) bool {
			return allPosts[a].Date.After(allPosts[b].Date)
		})
		groups = append(groups, ArchiveGroup{
			Year:   y,
			Posts:  allPosts,
			Months: months,
			Open:   i == 0,
		})
	}
	return groups
}

func handleArchive(w http.ResponseWriter, r *http.Request) {
	posts, _ := loadPosts()
	groups := buildArchiveGroups(posts)

	data := PageData{
		Title:           pageTitle("归档"),
		Nickname:        loadAppConfig().Author,
		Motto:           loadAppConfig().Motto,
		Posts:           posts,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		AllTags:         getAllTags(),
		Year:            time.Now().Year(),
		ArchiveGroups:   groups,
		IsArchive:       true,
		TopicDescs:      loadTopicDescs(),
		IsAdmin:         isAdmin(r),
	}
	// Unified nav semantics: site-wide page switches only target #content-wrapper, with
	// no fragment branches.
	// execute renders a div containing #content-wrapper automatically on HX-Request, so
	// the body's hx-select=#content-wrapper can extract and swap it.
	execute(w, r, "posts.html", data)
}

func handleCategory(w http.ResponseWriter, r *http.Request) {
	alias := r.PathValue("category")
	cat := aliasToCategory(alias)
	posts, err := loadPostsByCategory(cat)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	bgCfg := loadBgConfig()
	data := PageData{
		Title:           pageTitle(cat),
		Nickname:        loadAppConfig().Author,
		Motto:           loadAppConfig().Motto,
		Posts:           posts,
		Categories:      getCategories(),
		AllTags:         getAllTags(),
		CurrentCat:      cat,
		Year:            time.Now().Year(),
		Backgrounds:     bgCfg.Backgrounds,
		Interval:        bgCfg.Interval,
		Mode:            bgCfg.Mode,
		IsAdmin:         isAdmin(r),
		TopicDescs:      loadTopicDescs(),
		CategoryAliases: getCategoryAliases(),
	}
	// Unified nav semantics: site-wide page switches only target #content-wrapper, with
	// no fragment branches.
	execute(w, r, "posts.html", data)
}

// handlePosts renders the list page: every post, unfiltered. A #<tag> fragment narrows it in
// the browser, so the tag never reaches the server — this is the page the tag chips can still
// reach in a static publish, where no handler exists to answer a per-tag URL.
func handlePosts(w http.ResponseWriter, r *http.Request) {
	posts, _ := loadPosts()
	bgCfg := loadBgConfig()
	data := PageData{
		Title:           pageTitle("文章"),
		Nickname:        loadAppConfig().Author,
		Motto:           loadAppConfig().Motto,
		Posts:           posts,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		AllTags:         getAllTags(),
		Year:            time.Now().Year(),
		Backgrounds:     bgCfg.Backgrounds,
		Interval:        bgCfg.Interval,
		Mode:            bgCfg.Mode,
		TopicDescs:      loadTopicDescs(),
		IsAdmin:         isAdmin(r),
		IsList:          true,
	}
	execute(w, r, "posts.html", data)
}

// loadPostWithContent reads posts/<cat>/<slug>.md, renders the body, and returns a
// Post usable by templates.
// The post page (handlePostByID) and the admin font preview's article scenario share
// this single implementation — Posts from idxSnapshot() carry no Content (bodies render
// on demand), while the admin preview needs the real body.
func loadPostWithContent(id int) (Post, bool) {
	ref, ok := findPostByID(id)
	if !ok {
		return Post{}, false
	}
	body, err := os.ReadFile(ref.Path())
	if err != nil {
		return Post{}, false
	}
	text := string(body)
	title, tags, modTime, _, words, idFromFM, summaryFromFM := parseFrontmatter(text)
	if idFromFM == 0 {
		idFromFM = id
	}
	if modTime.IsZero() {
		if fi, err := os.Stat(ref.Path()); err == nil {
			modTime = fi.ModTime()
		}
	}
	// Parse updated_at from the frontmatter
	var updatedAt time.Time
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "updated_at: ") {
			updStr := strings.TrimSpace(strings.TrimPrefix(line, "updated_at: "))
			if t, err := time.Parse(time.RFC3339, updStr); err == nil {
				updatedAt = t
			}
			break
		}
	}
	// Match the editor's behavior: take the index's SummaryStr first, fall back to
	// the frontmatter summary.
	summary := getPostSummary(id)
	if summary == "" {
		summary = summaryFromFM
	}
	return Post{
		ID: idFromFM, Slug: ref.Slug, Title: title, Tags: tags,
		Category: ref.Category.Name, Date: modTime,
		UpdatedAt:  updatedAt,
		Content:    renderMarkdown(text),
		Words:      words,
		Summary:    template.HTML(summary),
		SummaryStr: summary,
		// Read count (in-memory layer; counting is done solely by the /api/view beacon)
		Views: postViewCount(id),
	}, true
}

func handlePostByID(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id == 0 {
		http.NotFound(w, r)
		return
	}
	post, ok := loadPostWithContent(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cat := post.Category
	bgCfg := loadBgConfig()
	var catPosts []CategoryWithPosts
	for _, c := range getCategories() {
		cp, _ := loadPostsByCategory(c)
		catPosts = append(catPosts, CategoryWithPosts{Name: c, Posts: cp, Open: c == cat})
	}
	execute(w, r, "post.html", PageData{
		Title:           pageTitle(post.Title),
		Nickname:        loadAppConfig().Author,
		CurrentPost:     &post,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		CurrentCat:      cat,
		CategoryPosts:   catPosts,
		Backgrounds:     bgCfg.Backgrounds,
		Interval:        bgCfg.Interval,
		Mode:            bgCfg.Mode,
		IsAdmin:         isAdmin(r),
		PostComments:    sortCommentsSQL(id, r.URL.Query().Get("sort")),
		Sort:            r.URL.Query().Get("sort"),
		Year:            time.Now().Year(),
	})
}

// Handlers: search.
// /search is a static shell: the query never reaches the server, so every ?q= variant renders
// the same bytes and a one-year CDN copy can never go stale. The shell is rendered once at
// startup (warmSearchShell) and the browser does the searching — the same core the instant
// dropdown uses (src/js/parts/06-search.js).

// searchShellPageData is the page data for the /search shell.
//
// It carries every post — the shell pre-renders all of their cards — but no query and no
// results: that is exactly what makes every ?q= variant identical.
//
// Summaries are blanked because the excerpt a visitor should see is cut from the post *body*
// around the match, which only the browser can do. Leaving the frontmatter summary in place
// would flash the wrong text before the browser replaces it.
func searchShellPageData(r *http.Request) PageData {
	bgCfg := loadBgConfig()
	posts := loadPostsFromDB()
	for i := range posts {
		posts[i].Summary = " "
	}
	return PageData{
		Title:           pageTitle("搜索"),
		Nickname:        loadAppConfig().Author,
		Posts:           posts,
		Categories:      getCategories(),
		CategoryAliases: getCategoryAliases(),
		Year:            time.Now().Year(),
		Backgrounds:     bgCfg.Backgrounds,
		Interval:        bgCfg.Interval,
		Mode:            bgCfg.Mode,
		IsAdmin:         isAdmin(r),
	}
}

// handleSearchPage serves the pre-rendered /search shell.
//
// In production nginx answers this out of the static output and Go never sees it; this route
// exists so the page also works when the engine is reached directly.
func handleSearchPage(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("DEV_MODE") == "1" {
		// DEV_MODE disables the render cache, so nothing was pre-rendered.
		execute(w, r, "search.html", searchShellPageData(r))
		return
	}
	cp := cachePath(PageSearch)
	if _, err := os.Stat(cp); err != nil {
		log.Printf("search: shell missing at %s: %v", cp, err)
		http.Error(w, "search shell missing", http.StatusInternalServerError)
		return
	}
	http.ServeFile(w, r, cp)
}

// Handlers: admin.

// Font preview fixtures (config/preview_fixtures.json). ALL of the admin font
// workshop's preview content comes from here. It is the single
// content source of the "preview fixture layer":
//   · render path — converted to real Go types and fed to the production partials
//     (structure/CSS fidelity)
//   · char path — runAdminFontSubset reads this file directly (font_subset.go)
// Sharing the same source -> every char the preview can show is necessarily within the
// admin subset (the invariant holds by construction).
//
// Text must be written as {"text": ..., "role": ...} to be collected by walkUILeaves
// (font_subset.go); entries missing a role are **silently dropped**. Numbers/dates/bools
// can be bare values — they are ignored automatically.

type fixtureText struct {
	Text string `json:"text"`
	Role string `json:"role"`
}

type fixturePost struct {
	ID        int           `json:"id"`
	Slug      string        `json:"slug"`
	Title     fixtureText   `json:"title"`
	Category  fixtureText   `json:"category"`
	Date      string        `json:"date"`
	UpdatedAt string        `json:"updated_at"`
	Words     int           `json:"words"`
	Views     int           `json:"views"`
	Summary   fixtureText   `json:"summary"`
	Content   fixtureText   `json:"content"`
	Tags      []fixtureText `json:"tags"`
}

type fixtureMonth struct {
	Month int           `json:"month"`
	Posts []fixturePost `json:"posts"`
}

type fixtureArchive struct {
	Year   int            `json:"year"`
	Months []fixtureMonth `json:"months"`
}

type fixtureComment struct {
	ID      string      `json:"id"`
	Rid     string      `json:"rid"`
	Nick    fixtureText `json:"nick"`
	Date    string      `json:"date"`
	Likes   int         `json:"likes"`
	UA      string      `json:"ua"`
	Content fixtureText `json:"content"`
}

type fixtureSponsor struct {
	Nick   fixtureText `json:"nick"`
	Amount fixtureText `json:"amount"`
	Msg    fixtureText `json:"msg"`
	Date   fixtureText `json:"date"`
}

type fixtureStats struct {
	TodayPV int `json:"today_pv"`
	TodayUV int `json:"today_uv"`
	TotalPV int `json:"total_pv"`
	TotalUV int `json:"total_uv"`
}

type fixtureTaxonomy struct {
	Categories []fixtureText `json:"categories"`
	Tags       []fixtureText `json:"tags"`
}

type fixtureFile struct {
	Identity struct {
		Nickname  fixtureText `json:"nickname"`
		HomeMotto fixtureText `json:"home_motto"`
	} `json:"identity"`
	Article  fixturePost      `json:"article"`
	Latest   fixturePost      `json:"latest"`
	Archive  fixtureArchive   `json:"archive"`
	Comments []fixtureComment `json:"comments"`
	Sponsors []fixtureSponsor `json:"sponsors"`
	Stats    fixtureStats     `json:"stats"`
	Taxonomy fixtureTaxonomy  `json:"taxonomy"`
}

// previewData is the fixture-converted preview data (real Go types, feedable directly
// to the production partials).
type previewData struct {
	Nickname   string
	HomeMotto  string
	Post       *Post
	Latest     *Post
	Posts      []Post
	Archive    []ArchiveGroup
	Comments   []Comment
	Sponsors   []Sponsor
	Stats      VisitStats
	Categories []string
	AllTags    []string
}

// toPost converts a fixture entry into a real Post. **Must return the real type** —
// templates call .P.Date.Format / .P.UpdatedAt.IsZero; leaving it a map would miss the
// methods and error out.
func (f fixturePost) toPost() Post {
	p := Post{
		ID:         f.ID,
		Slug:       f.Slug,
		Title:      f.Title.Text,
		Category:   f.Category.Text,
		Words:      f.Words,
		Views:      f.Views,
		Content:    renderMarkdown(f.Content.Text),
		Summary:    renderMarkdown(f.Summary.Text),
		SummaryStr: f.Summary.Text,
	}
	if t, err := time.Parse(time.RFC3339, f.Date); err == nil {
		p.Date = t
	}
	// UpdatedAt must be non-zero or the "last edited" block in components.html doesn't render
	if t, err := time.Parse(time.RFC3339, f.UpdatedAt); err == nil {
		p.UpdatedAt = t
	}
	for _, tag := range f.Tags {
		if tag.Text != "" {
			p.Tags = append(p.Tags, tag.Text)
		}
	}
	return p
}

// loadPreviewFixtures reads config/preview_fixtures.json and converts it to real Go types.
// On failure it returns zero values and logs — an unavailable preview must not take down
// the whole admin.
func loadPreviewFixtures() previewData {
	var out previewData

	raw, err := os.ReadFile(dataReadPath(FilePreviewFixtures))
	if err != nil {
		log.Printf("[字体预览夹具] 读取失败: %v", err)
		return out
	}
	var f fixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[字体预览夹具] 解析失败: %v", err)
		return out
	}

	out.Nickname = f.Identity.Nickname.Text
	out.HomeMotto = f.Identity.HomeMotto.Text

	article := f.Article.toPost()
	latest := f.Latest.toPost()
	out.Post = &article
	out.Latest = &latest
	out.Posts = []Post{article, latest}

	// Archive: the fixture only provides the latest year; the preview doesn't need everything
	if len(f.Archive.Months) > 0 {
		g := ArchiveGroup{Year: f.Archive.Year, Open: true}
		for _, m := range f.Archive.Months {
			am := ArchiveMonth{Month: m.Month}
			for _, fp := range m.Posts {
				p := fp.toPost()
				am.Posts = append(am.Posts, p)
				g.Posts = append(g.Posts, p)
			}
			g.Months = append(g.Months, am)
		}
		out.Archive = []ArchiveGroup{g}
	}

	for _, c := range f.Comments {
		out.Comments = append(out.Comments, Comment{
			ID:      c.ID,
			Rid:     c.Rid,
			Nick:    c.Nick.Text,
			Date:    c.Date,
			Likes:   c.Likes,
			UA:      c.UA, // real UA string so UAShort() can parse it
			Content: c.Content.Text,
		})
	}

	for _, s := range f.Sponsors {
		out.Sponsors = append(out.Sponsors, Sponsor{
			Nick:   s.Nick.Text,
			Amount: s.Amount.Text,
			Msg:    s.Msg.Text,
			Date:   s.Date.Text,
		})
	}

	out.Stats = VisitStats{
		TodayPV: f.Stats.TodayPV,
		TodayUV: f.Stats.TodayUV,
		TotalPV: f.Stats.TotalPV,
		TotalUV: f.Stats.TotalUV,
	}

	for _, c := range f.Taxonomy.Categories {
		if c.Text != "" {
			out.Categories = append(out.Categories, c.Text)
		}
	}
	for _, t := range f.Taxonomy.Tags {
		if t.Text != "" {
			out.AllTags = append(out.AllTags, t.Text)
		}
	}

	return out
}

func handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	bgCfg := loadBgConfig()
	cfg := loadAppConfig()
	posts, _ := loadPostsFull()
	var bgs []BgEntry
	var desktopBgs, mobileBgs []BgEntry
	for _, u := range bgCfg.Backgrounds {
		name := filepath.Base(u.Src)
		if name == "." || name == "/" {
			name = u.Src
		}
		entry := BgEntry{URL: u.Src, Name: name, Device: u.Device, Tone: u.Tone}
		bgs = append(bgs, entry)
		if u.Device == "mobile" {
			mobileBgs = append(mobileBgs, entry)
		} else {
			desktopBgs = append(desktopBgs, entry)
		}
	}

	// Font preview data comes **entirely** from config/preview_fixtures.json (the preview
	// fixture layer). Its character source is the same as runAdminFontSubset's, guaranteeing
	// "preview-visible chars ⊆ admin subset chars".
	// No real posts/stats are read anymore: the preview is decoupled from the content
	// library, keeping the charset controllable and testable, and avoiding the illegitimate
	// dependency where "writing a new post changes the font build output".
	pv := loadPreviewFixtures()

	execute(w, r, "admin.html", struct {
		Title           string
		Nickname        string
		HomeMotto       string
		HeroBg          BgItem
		Theme           string
		Backgrounds     []BgEntry
		DesktopBgs      []BgEntry
		MobileBgs       []BgEntry
		Posts           []Post
		AllTags         []string
		ArchiveGroups   []ArchiveGroup
		PreviewPost     *Post
		PreviewLatest   *Post
		PreviewStats    VisitStats
		PreviewComments []Comment
		// Preview-only data (from config/preview_fixtures.json).
		// It coexists with the same-named real fields below: Posts / Sponsors / Categories
		// etc. are still consumed by non-preview features (post management, sponsor
		// management) and must not be replaced.
		PreviewPosts      []Post
		PreviewArchive    []ArchiveGroup
		PreviewSponsors   []Sponsor
		PreviewCategories []string
		PreviewAllTags    []string
		PreviewNickname   string
		PreviewHomeMotto  string
		Mode              string
		Sponsors          []Sponsor
		Motto             string
		FontPresets       []FontPreset
		ActiveFontPreset  string
		AvailableFonts    []FontOption
		Year              int
		Categories        []string
		CategoryAliases   map[string]string
		CurrentCat        string
		IsArchive         bool
		IsAdmin           bool
		PinnedPostID      int
		NavLabel          string
		HTMXRequest       bool
	}{
		Title:     pageTitle("管理"),
		Nickname:  cfg.Author,
		HomeMotto: cfg.HomeMotto,
		// The preview is a desktop viewport, so it carries only the desktop background.
		HeroBg:            adminPreviewHeroBg(),
		Theme:             defaultTheme,
		Backgrounds:       bgs,
		DesktopBgs:        desktopBgs,
		MobileBgs:         mobileBgs,
		Posts:             posts,
		AllTags:           getAllTags(),
		ArchiveGroups:     buildArchiveGroups(posts),
		PreviewPost:       pv.Post,
		PreviewLatest:     pv.Latest,
		PreviewStats:      pv.Stats,
		PreviewComments:   pv.Comments,
		PreviewPosts:      pv.Posts,
		PreviewArchive:    pv.Archive,
		PreviewSponsors:   pv.Sponsors,
		PreviewCategories: pv.Categories,
		PreviewAllTags:    pv.AllTags,
		PreviewNickname:   pv.Nickname,
		PreviewHomeMotto:  pv.HomeMotto,
		Mode:              bgCfg.Mode,
		Sponsors:          loadSponsors(),
		Motto:             cfg.Motto,
		FontPresets:       cfg.FontPresets,
		ActiveFontPreset:  cfg.ActiveFontPreset,
		AvailableFonts:    getAvailableFonts(),
		Year:              time.Now().Year(),
		Categories:        getCategories(),
		CategoryAliases:   getCategoryAliases(),
		CurrentCat:        "",
		IsArchive:         false,
		IsAdmin:           true,
		PinnedPostID:      cfg.PinnedPostID,
		NavLabel:          navLabelForPath(r.URL.Path),
		HTMXRequest:       r.Header.Get("HX-Request") == "true",
	})
}

func handleAdminBgAdd(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	url := strings.TrimSpace(r.FormValue("url"))
	if url != "" {
		if strings.HasPrefix(url, PrefixStatic+"/") {
			// Local path: read the file data, then run the unified conversion pipeline
			data, err := os.ReadFile("." + url)
			if err != nil {
				adminError(w, r, "无法读取该背景文件", http.StatusBadRequest)
				return
			}
			item, err := saveBackground(data, filepath.Base(url))
			if err != nil {
				adminError(w, r, "背景处理失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			cfg, _ := loadStoredBgConfig()
			cfg.Backgrounds = append(cfg.Backgrounds, item)
			saveBgConfig(cfg)
			incFileRef(item.Src)
			invalidateListCache() // background add/remove changes indexes; invalidate all home caches
		} else {
			// External URL: download, then run the unified conversion pipeline
			item, err := downloadBackground(url)
			if err != nil {
				adminError(w, r, "下载失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			cfg, _ := loadStoredBgConfig()
			cfg.Backgrounds = append(cfg.Backgrounds, item)
			saveBgConfig(cfg)
			incFileRef(item.Src)
			invalidateListCache() // background add/remove changes indexes; invalidate all home caches
		}
	}
	adminRespond(w, r, "")
}

// saveBackground converts raw background image data into JXL+AVIF dual formats and
// registers the BgItem.
// Flow: write a temp raw file -> read dimensions to classify device by ratio ->
// ConvertImage to jxl/avif -> remove the raw temp file -> return a BgItem whose src
// points at the .avif.
// The original upload format is not kept; only JXL and AVIF are stored.
func saveBackground(data []byte, filename string) (BgItem, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedBgExt(ext) {
		return BgItem{}, fmt.Errorf("不支持的格式 %s", ext)
	}

	tmp, err := os.CreateTemp("", "bg-orig-*"+ext)
	if err != nil {
		return BgItem{}, err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return BgItem{}, err
	}
	tmp.Close()
	defer os.Remove(tmpPath)

	// Read dimensions -> classify device (width >= height -> desktop; height > width -> mobile)
	f, err := os.Open(tmpPath)
	if err != nil {
		return BgItem{}, err
	}
	cfgImg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		return BgItem{}, fmt.Errorf("无法解析图片尺寸")
	}
	device := "desktop"
	if cfgImg.Height > cfgImg.Width {
		device = "mobile"
	}

	// Convert to JXL + AVIF (lossless jxl -d 0 -m 1 -e 10; avif default high-fidelity params)
	bgDir := DirBackground
	os.MkdirAll(bgDir, 0755)

	// Compute the next available sequence number (e.g. desktop-01, desktop-02)
	nextSeq := 1
	if entries, err := os.ReadDir(bgDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, device+"-") {
				stem := strings.TrimSuffix(name, filepath.Ext(name))
				parts := strings.Split(stem, "-")
				if len(parts) >= 2 {
					if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && n >= nextSeq {
						nextSeq = n + 1
					}
				}
			}
		}
	}
	baseName := fmt.Sprintf("%s-%02d", device, nextSeq)

	targets, err := ConvertImage(ConvertOptions{
		InputPath: tmpPath,
		OutputDir: bgDir,
		BaseName:  baseName,
		Formats:   []string{"jxl", "avif"},
	})
	if err != nil {
		return BgItem{}, err
	}
	if len(targets) == 0 {
		return BgItem{}, fmt.Errorf("转换失败: 未生成任何格式")
	}

	return BgItem{
		Src:       "/" + bgDir + "/" + baseName + ".avif",
		Device:    device,
		Tone:      "light",
		OffsetX:   0.5,
		OffsetY:   0.5,
		AvatarPos: "left",
	}, nil
}

// downloadBackground downloads an external image and runs the unified conversion
// pipeline (only JXL/AVIF are stored).
// Size limit 50MB, image formats only.
func downloadBackground(url string) (BgItem, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return BgItem{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return BgItem{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return BgItem{}, err
	}
	filename := httpFilenameFromURL(url)
	return saveBackground(data, filename)
}

func httpFilenameFromURL(u string) string {
	// Take the file name after stripping the query (extension included)
	p := strings.SplitN(u, "?", 2)[0]
	base := filepath.Base(p)
	if base == "." || base == "/" || base == "" {
		return "bg.jpg"
	}
	return base
}

func allowedBgExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	}
	return false
}

func handleAdminBgDelete(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	src := strings.TrimSpace(r.FormValue("src"))
	cfg := loadBgConfig()
	found := false
	for i, b := range cfg.Backgrounds {
		if b.Src == src {
			// Also delete all companion format files on disk (.avif, .jxl, etc.)
			base := strings.TrimSuffix(src, filepath.Ext(src))
			for _, ext := range []string{".avif", ".jxl", ".jpg", ".png", ".webp"} {
				os.Remove("." + base + ext)
			}
			decFileRef(src)
			cfg.Backgrounds = append(cfg.Backgrounds[:i], cfg.Backgrounds[i+1:]...)
			saveBgConfig(cfg)
			delOrphanedFiles()
			invalidateListCache() // background deletion changes indexes; invalidate all home caches
			found = true
			break
		}
	}
	if !found {
		adminError(w, r, "背景不存在: "+src, http.StatusNotFound)
		return
	}
	adminRespond(w, r, "")
}

func handleAdminBgToggleMode(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	cfg := loadBgConfig()
	if cfg.Mode == "random" {
		cfg.Mode = "sequential"
	} else {
		cfg.Mode = "random"
	}
	saveBgConfig(cfg)
	adminRespond(w, r, "")
}

// handleAdminBgSetOffset saves a background's free-form profile-card coordinates
// (offset_x/offset_y).
// Called after the admin long-press-drags the profile card on the home page; the position
// is bound to the background and then fixed.
//
// The candidate list is the one the home page rendered (loadBgConfig), not the raw
// stored file: a deployment that has never written config/background.json renders the
// shipped placeholder, and reading only the stored file made the drag match nothing,
// write nothing, and still answer ok — the card snapped back on every refresh.
func handleAdminBgSetOffset(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	src := strings.TrimSpace(r.FormValue("src"))
	ox, err1 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("offset_x")), 64)
	oy, err2 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("offset_y")), 64)
	if src == "" || err1 != nil || err2 != nil || ox < 0 || ox > 1 || oy < 0 || oy > 1 {
		adminError(w, r, "invalid offset", http.StatusBadRequest)
		return
	}
	cfg := loadBgConfig()
	found := false
	for i := range cfg.Backgrounds {
		if cfg.Backgrounds[i].Src == src {
			cfg.Backgrounds[i].OffsetX = ox
			cfg.Backgrounds[i].OffsetY = oy
			saveBgConfig(cfg)
			invalidateListCache() // offset changes affect home cache content
			found = true
			break
		}
	}
	if !found {
		adminError(w, r, "背景不存在: "+src, http.StatusNotFound)
		return
	}
	adminRespond(w, r, "")
}

// handleAdminBgSetAvatar saves a background's avatar position (avatar_pos: left/right).
// Called after the admin double-clicks the avatar on the home page.
func handleAdminBgSetAvatar(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	src := strings.TrimSpace(r.FormValue("src"))
	pos := strings.TrimSpace(r.FormValue("avatar_pos"))
	if src == "" || (pos != "left" && pos != "right") {
		adminError(w, r, "invalid avatar_pos", http.StatusBadRequest)
		return
	}
	cfg := loadBgConfig()
	found := false
	for i := range cfg.Backgrounds {
		if cfg.Backgrounds[i].Src == src {
			cfg.Backgrounds[i].AvatarPos = pos
			saveBgConfig(cfg)
			invalidateListCache() // avatar changes affect home cache content
			found = true
			break
		}
	}
	if !found {
		adminError(w, r, "背景不存在: "+src, http.StatusNotFound)
		return
	}
	adminRespond(w, r, "")
}

// handleAdminBgSetTone saves a background's decoration tone (tone: light = light
// background with dark text, dark = dark background with light text).
// Called after the admin switches a background card's "decoration color" in the admin;
// applies uniformly to all 4 decoration groups.
func handleAdminBgSetTone(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	src := strings.TrimSpace(r.FormValue("src"))
	tone := strings.TrimSpace(r.FormValue("tone"))
	if src == "" || (tone != "light" && tone != "dark") {
		adminError(w, r, "invalid tone", http.StatusBadRequest)
		return
	}
	cfg := loadBgConfig()
	found := false
	for i := range cfg.Backgrounds {
		if cfg.Backgrounds[i].Src == src {
			cfg.Backgrounds[i].Tone = tone
			saveBgConfig(cfg)
			invalidateListCache() // tone changes affect home cache content
			found = true
			break
		}
	}
	if !found {
		adminError(w, r, "背景不存在: "+src, http.StatusNotFound)
		return
	}
	adminRespond(w, r, "")
}

func handleAdminBgUpload(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := os.MkdirAll(DirBackground, 0755); err != nil {
		adminError(w, r, "创建背景目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		adminError(w, r, "读取上传内容失败（单张上限 50MB）: "+err.Error(), http.StatusBadRequest)
		return
	}
	cfg, _ := loadStoredBgConfig()
	// Keep the first error and surface it: the files that did convert are still saved.
	var firstErr error
	if r.MultipartForm != nil {
		for _, fhs := range r.MultipartForm.File {
			for _, fh := range fhs {
				src, err := fh.Open()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("读取 %s 失败: %w", fh.Filename, err)
					}
					continue
				}
				data, err := io.ReadAll(src)
				src.Close()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("读取 %s 失败: %w", fh.Filename, err)
					}
					continue
				}
				item, err := saveBackground(data, fh.Filename)
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("处理 %s 失败: %w", fh.Filename, err)
					}
					continue
				}
				cfg.Backgrounds = append(cfg.Backgrounds, item)
				incFileRef(item.Src)
			}
		}
	}
	saveBgConfig(cfg)
	invalidateListCache() // background count changes affect home index caches
	if firstErr != nil {
		adminError(w, r, firstErr.Error(), http.StatusInternalServerError)
		return
	}
	adminRespond(w, r, "")
}

// editorBackURL derives the editor's "back" button target — return to wherever you came from.
//
// Earlier this was done in editor.html via JS reading `document.referrer`, but htmx boost
// navigation does NOT update document.referrer (it only reflects the document's initial
// load source), so entering the editor from a post page always went back to
// /admin/dashboard. Admin pages now disable boost entirely (admin.html .admin-page and
// editor.html editor-form both carry hx-boost:inherited="false") -> navigation is native
// full-page -> the server-side Referer header is reliable.
//
// Fallback to the dashboard when: no Referer / cross-origin / points at the editor itself
// (otherwise "back" would loop in place).
func editorBackURL(r *http.Request) string {
	const fallback = RouteAdminDashboard
	ref := r.Header.Get("Referer")
	i := strings.Index(ref, "://")
	if i < 0 {
		return fallback
	}
	rest := ref[i+3:]
	j := strings.IndexByte(rest, '/')
	if j < 0 || rest[:j] != r.Host {
		return fallback
	}
	p := rest[j:]
	if k := strings.IndexAny(p, "?#"); k >= 0 {
		p = p[:k]
	}
	if p == "" || strings.HasPrefix(p, "/admin/action/post") {
		return fallback
	}
	return p
}

func handleAdminNewPost(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// Accept either half of the pair, so an old link carrying the display name still
	// preselects the right entry.
	catKey := resolveCategoryKey(r.URL.Query().Get("category"))
	today := time.Now().Format("2006-01-02")
	execute(w, r, "editor.html", map[string]interface{}{
		"Title":      pageTitle("写文章"),
		"Action":     "/admin/commit/post",
		"CatKey":     catKey,
		"Slug":       "",
		"Tags":       "",
		"Date":       today,
		"Body":       "# 标题\n\n正文...",
		"IsEdit":     false,
		"OldPath":    "",
		"Summary":    "",
		"SummaryStr": "",
		"BackURL":    editorBackURL(r),
	})
}

// Handlers: admin post delete.

func handleAdminPostDelete(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	id, _ := strconv.Atoi(r.FormValue("post_id"))
	if id > 0 {
		// Locate the file by ID first, then delete the record (order matters)
		if ref, ok := findPostByID(id); ok {
			// Read the post body for reference tracking
			body, _ := os.ReadFile(ref.Path())
			os.Remove(ref.Path())
			if body != nil {
				decFileRefs(extractCommentImgPaths(string(body)))
			}
		}
		rebuildPostIndex()
		TriggerStaticSync()
		delOrphanedFiles()
		invalidatePostCache(id)
		invalidateListCache()
		// If the deleted post was the pinned one, reset the pin config
		cfg := loadAppConfig()
		if cfg.PinnedPostID == id {
			cfg.PinnedPostID = 0
			if err := saveAppConfig(cfg); err != nil {
				log.Printf("warn: reset pinned post: %v", err)
			}
			runHomeSubsetAndWBN()
		}
		// SW content generation invalidation: post deleted -> /sw.js serves a new generation
		// next time -> client SW upgrades and clears caches
		refreshServiceWorker()
	}
	adminRespond(w, r, "")
}

// Handlers: admin post pin toggle.

func handleAdminPostTogglePin(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	postID, _ := strconv.Atoi(r.FormValue("post_id"))
	if postID <= 0 {
		adminRespond(w, r, "无效的文章 ID")
		return
	}

	cfg := loadAppConfig()
	msg := ""
	if cfg.PinnedPostID == postID {
		cfg.PinnedPostID = 0
		msg = "已取消置顶"
	} else {
		cfg.PinnedPostID = postID
		msg = "已设为置顶"
	}

	if err := saveAppConfig(cfg); err != nil {
		adminError(w, r, "write error", http.StatusInternalServerError)
		return
	}

	invalidateListCache()
	warmHome()
	TriggerStaticSync()
	runHomeSubsetAndWBN()
	adminRespond(w, r, msg)
}

// Handlers: admin sponsors.

func sponsorRedirectTarget(r *http.Request) string {
	if redirect := strings.TrimSpace(r.FormValue("redirect")); redirect != "" {
		if strings.HasPrefix(redirect, "/") && !strings.HasPrefix(redirect, "//") {
			return redirect
		}
	}
	if ref := r.Referer(); strings.Contains(ref, "/sponsor") {
		return "/sponsor"
	}
	return "/admin/dashboard"
}

func handleAdminSponsorAdd(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	date := strings.TrimSpace(r.FormValue("date"))
	nick := strings.TrimSpace(r.FormValue("nick"))
	amount := strings.TrimSpace(r.FormValue("amount"))
	msg := strings.TrimSpace(r.FormValue("msg"))
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	if nick != "" && amount != "" {
		list := loadSponsors()
		list = append(list, Sponsor{Date: date, Nick: nick, Amount: amount, Msg: msg})
		saveSponsors(list)
		invalidateCache("/sponsor")
	}
	target := sponsorRedirectTarget(r)
	adminRespondRedirect(w, r, target)
}

func handleAdminSponsorEdit(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	idx := -1
	if _, err := fmt.Sscanf(r.FormValue("index"), "%d", &idx); err != nil {
		idx = -1
	}
	date := strings.TrimSpace(r.FormValue("date"))
	nick := strings.TrimSpace(r.FormValue("nick"))
	amount := strings.TrimSpace(r.FormValue("amount"))
	msg := strings.TrimSpace(r.FormValue("msg"))
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	list := loadSponsors()
	if idx >= 0 && idx < len(list) && nick != "" && amount != "" {
		list[idx] = Sponsor{Date: date, Nick: nick, Amount: amount, Msg: msg}
		saveSponsors(list)
		invalidateCache("/sponsor")
	}
	target := sponsorRedirectTarget(r)
	adminRespondRedirect(w, r, target)
}

func handleAdminSponsorDelete(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	idx := -1
	if _, err := fmt.Sscanf(r.FormValue("index"), "%d", &idx); err != nil {
		idx = -1
	}
	list := loadSponsors()
	if idx >= 0 && idx < len(list) {
		list = append(list[:idx], list[idx+1:]...)
		saveSponsors(list)
		invalidateCache("/sponsor")
	}
	if r.Header.Get("X-Requested-With") == "fetch" {
		adminRespond(w, r, "")
		return
	}
	target := sponsorRedirectTarget(r)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// Comment list fragment (for HTMX sort).

func handleCommentList(w http.ResponseWriter, r *http.Request) {
	sort := r.URL.Query().Get("sort")
	postID := 0
	fmt.Sscanf(r.URL.Query().Get("post_id"), "%d", &postID)
	comments := sortCommentsSQL(postID, sort)
	data := struct {
		PostComments []Comment
		IsAdmin      bool
		CurrentPost  *Post
		Sort         string
	}{
		PostComments: comments,
		IsAdmin:      isAdmin(r),
		CurrentPost:  &Post{ID: postID},
		Sort:         sort,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Comments are dynamic data: disable browser-side caching (SWR once served a stale
	// structural copy -> duplicate headers, and the layout collapsed when morph removed
	// the extra header = full-text flicker after sorting, observed in practice); keep the
	// CDN-side s-maxage.
	w.Header().Set("Cache-Control", "public, s-maxage=2592000, max-age=0, must-revalidate")
	execute(w, r, "comment_fragment.html", data)
}

// Comment image upload (resize + JXL + AVIF).

func handleCommentUploadImage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20) // 32MB
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSONError(w, "file too large")
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		writeJSONError(w, "no file")
		return
	}
	defer file.Close()

	// Validate the file type
	ext := strings.ToLower(filepath.Ext(header.Filename))
	allowed := map[string]bool{".gif": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".avif": true, ".heic": true, ".heif": true, ".jxl": true}
	if !allowed[ext] {
		writeJSONError(w, "unsupported file type")
		return
	}

	// Generate a unique base name
	buf := make([]byte, 8)
	rand.Read(buf)
	baseName := hex.EncodeToString(buf)

	dir := DirImageComment
	os.MkdirAll(dir, 0755)

	// Write a temp raw file
	tmpIn, _ := os.CreateTemp("", "cmt-*"+ext)
	defer os.Remove(tmpIn.Name())
	io.Copy(tmpIn, file)
	tmpIn.Close()
	inputPath := tmpIn.Name()

	// Resize width: only when oversized (~728px, matching articles; fits the display area
	// 800-36*2)
	maxW := 728
	if w, err := strconv.Atoi(strings.TrimSpace(r.FormValue("width"))); err == nil && w > 0 {
		maxW = w
	}
	resizeW := 0
	if out, err := exec.Command(magickCmd, "identify", "-format", "%w", inputPath).Output(); err == nil {
		if w, _ := strconv.Atoi(strings.TrimSpace(string(out))); w > maxW {
			resizeW = maxW
		}
	}

	// Detect animation
	isAnimated := ext == ".gif" // GIF is unconditionally treated as animated
	if ext != ".gif" {
		// Other formats: detect the frame count
		identifyCmd := magickCmd
		identifyArgs := []string{"identify", "-format", "%n", inputPath}
		if !strings.HasSuffix(magickCmd, "magick") {
			identifyCmd = toolPath("identify")
			identifyArgs = []string{"-format", "%n", inputPath}
		}
		if out, err := exec.Command(identifyCmd, identifyArgs...).Output(); err == nil {
			frames, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			isAnimated = frames > 1
		}
	}

	var targets []ConvertTarget

	if isAnimated {
		// Animated: dual formats (JXL + AVIF), no scaling
		targets, err = ConvertAnimated(ConvertOptions{
			InputPath: inputPath,
			OutputDir: dir,
			BaseName:  baseName,
		})
		if err != nil {
			writeJSONError(w, err.Error())
			return
		}
		if len(targets) == 0 {
			writeJSONError(w, "converter unavailable")
			return
		}
	} else {
		// Static image: call the internal conversion function (lossless JXL + AVIF dual formats)
		targets, err = ConvertImage(ConvertOptions{
			InputPath: inputPath,
			OutputDir: dir,
			BaseName:  baseName,
			Width:     resizeW,
			Formats:   []string{"jxl", "avif"},
		})
		if err != nil {
			writeJSONError(w, err.Error())
			return
		}
		if len(targets) == 0 {
			writeJSONError(w, "converter unavailable")
			return
		}
	}

	// Register generated image files with reference tracking
	for _, t := range targets {
		registerFileRef(t.URL)
	}

	// Return the canonical URL: always .avif (animations append ?anim so the render layer
	// can distinguish static/animated)
	urlSuffix := ".avif"
	if isAnimated {
		urlSuffix = ".avif?anim"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": URLImageComment(baseName + urlSuffix)})
}

func writeJSONError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Article image upload (paste).

func handleAdminUploadImage(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		writeJSONError(w, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSONError(w, "file too large")
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		writeJSONError(w, "no file")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	allowed := map[string]bool{".gif": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".avif": true, ".heic": true, ".heif": true, ".jxl": true}
	if !allowed[ext] {
		writeJSONError(w, "unsupported file type")
		return
	}

	buf := make([]byte, 8)
	rand.Read(buf)
	baseName := hex.EncodeToString(buf)

	dir := DirImagePost
	os.MkdirAll(dir, 0755)

	tmpIn, _ := os.CreateTemp("", "post-img-*"+ext)
	defer os.Remove(tmpIn.Name())
	io.Copy(tmpIn, file)
	tmpIn.Close()
	inputPath := tmpIn.Name()

	// Auto-scale to article width (~728px, matching the body display area 800-36*2)
	maxW := 728
	if out, err := exec.Command(magickCmd, "identify", "-format", "%w", inputPath).Output(); err == nil {
		if w, _ := strconv.Atoi(strings.TrimSpace(string(out))); w > maxW {
			width := strconv.Itoa(maxW)
			exec.Command(magickCmd, inputPath, "-resize", width, inputPath).Run()
		}
	}

	isAnimated := ext == ".gif"
	if ext != ".gif" {
		identifyCmd := magickCmd
		identifyArgs := []string{"identify", "-format", "%n", inputPath}
		if !strings.HasSuffix(magickCmd, "magick") {
			identifyCmd = toolPath("identify")
			identifyArgs = []string{"-format", "%n", inputPath}
		}
		if out, err := exec.Command(identifyCmd, identifyArgs...).Output(); err == nil {
			frames, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			isAnimated = frames > 1
		}
	}

	var targets []ConvertTarget

	var outURL string
	if isAnimated {
		// Animated: dual formats (JXL + AVIF), no scaling
		var err error
		targets, err = ConvertAnimated(ConvertOptions{
			InputPath: inputPath,
			OutputDir: dir,
			BaseName:  baseName,
		})
		if err != nil || len(targets) == 0 {
			writeJSONError(w, "converter unavailable")
			return
		}
		outURL = URLImagePost(baseName+".avif") + "?anim"
	} else {
		var err error
		targets, err = ConvertImage(ConvertOptions{
			InputPath: inputPath,
			OutputDir: dir,
			BaseName:  baseName,
			Formats:   []string{"jxl", "avif"},
		})
		if err != nil || len(targets) == 0 {
			writeJSONError(w, "converter unavailable")
			return
		}
		// canonical URL is always .avif (the render layer adds the JXL sibling automatically)
		outURL = URLImagePost(baseName + ".avif")
	}

	// Register generated image files with reference tracking (beyond comments, article
	// images are tracked too, so deleting a post doesn't leave orphans)
	for _, t := range targets {
		registerFileRef(t.URL)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": outURL})
}

// Edit post.

func handleAdminEditPost(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id == 0 {
		adminError(w, r, "invalid id", http.StatusBadRequest)
		return
	}
	// Find the file by ID
	ref, ok := findPostByID(id)
	if !ok {
		adminError(w, r, "file not found", http.StatusNotFound)
		return
	}
	path := ref.Path()
	body, err := os.ReadFile(path)
	if err != nil {
		adminError(w, r, "file not found", http.StatusNotFound)
		return
	}
	text := string(body)
	title, tags, modTime, rest, words, _, summaryFromFM := parseFrontmatter(text)
	dateStr := ""
	if !modTime.IsZero() && modTime.Year() > 2000 {
		dateStr = modTime.Format("2006-01-02")
	}
	_ = words
	summary := getPostSummary(id)
	if summary == "" {
		summary = summaryFromFM
	}

	data := map[string]interface{}{
		"Title":      "编辑 - " + title,
		"Action":     "/admin/commit/post",
		"CatKey":     ref.Category.Key,
		"Slug":       ref.Slug,
		"Tags":       strings.Join(tags, ", "),
		"Date":       dateStr,
		"Body":       rest,
		"IsEdit":     true,
		"OldPath":    path,
		"Summary":    summary,
		"SummaryStr": summary,
		"Flash":      "",
		"BackURL":    editorBackURL(r),
	}
	execute(w, r, "editor.html", data)
}

// Save post (create + edit).

// resolvePostDate decides the date written into the frontmatter (the **publish date**).
// Priority: form value -> the old file's **raw date line** -> today (new post). Deliberately
// not parseFrontmatter's modTime: on a missing date it falls back to a placeholder or the
// file mtime, which would treat "the last save time" as the publish date. The editor form
// has no date field, so this fallback chain is what keeps edits from changing the date.
func resolvePostDate(formDate, oldPath string) string {
	if d := strings.TrimSpace(formDate); d != "" {
		return d
	}
	if oldPath != "" {
		if b, err := os.ReadFile(oldPath); err == nil {
			if d := frontmatterRawDate(string(b)); d != "" {
				return d
			}
		}
	}
	return time.Now().Format("2006-01-02")
}

// frontmatterRawDate extracts the **raw** date line from the frontmatter region of the
// original text (no parsing, no fallback).
// Unlike parseFrontmatter, it never invents a value when missing — exactly the semantics
// the date backfill needs.
func frontmatterRawDate(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "---\n") {
		return ""
	}
	endIdx := strings.Index(s[4:], "\n---")
	if endIdx == -1 {
		return ""
	}
	fm := s[4 : endIdx+4]
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "date: ") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "date: ")), "\"'")
		}
	}
	return ""
}

func handleAdminSavePost(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseMultipartForm(10 << 20)
	// The editor submits a registry key. resolveCategoryKey also takes the display name, so
	// a stale form or a pre-registry site still saves into the right directory.
	cat := resolveCategoryKey(r.FormValue("category"))
	slug := strings.TrimSpace(r.FormValue("slug"))
	rawTags := strings.TrimSpace(r.FormValue("tags"))
	dateRaw := strings.TrimSpace(r.FormValue("date"))
	body := r.FormValue("body")
	oldPath := strings.TrimSpace(r.FormValue("old_path"))
	formSummary := strings.TrimSpace(r.FormValue("summary"))

	if cat == "" {
		cat = defaultCategoryKey()
	}
	if !validCategoryKey(cat) {
		adminError(w, r, "invalid category", http.StatusBadRequest)
		return
	}
	if slug == "" {
		slug = "untitled"
	}
	slug = strings.TrimSuffix(slug, ".md")

	// Determine the post ID
	var postID int
	if oldPath != "" {
		// Edit: read the ID from the old file
		if ob, err := os.ReadFile(oldPath); err == nil {
			_, _, _, _, _, oldID, _ := parseFrontmatter(string(ob))
			postID = oldID
		}
	}
	if postID == 0 {
		// New post: allocate a new ID
		maxPostID++
		postID = maxPostID
	}

	// Build the frontmatter
	var fm strings.Builder
	fm.WriteString("---\n")
	fm.WriteString(fmt.Sprintf("id: %d\n", postID))
	fm.WriteString("title: " + slug + "\n")
	// The date line is **mandatory** — see the resolvePostDate doc comment. The old
	// `if dateRaw != ""` was always false when editing (the editor form has no date field)
	// -> the frontmatter lacked date -> the publish date was substituted by parseFrontmatter's
	// placeholder, showing "the current moment" on every read.
	fm.WriteString("date: " + resolvePostDate(dateRaw, oldPath) + "\n")
	if formSummary != "" {
		fm.WriteString("summary: " + formSummary + "\n")
	}
	if rawTags != "" {
		parts := strings.Split(rawTags, ",")
		var clean []string
		for _, t := range parts {
			t = strings.TrimSpace(t)
			if t != "" {
				clean = append(clean, t)
			}
		}
		if len(clean) > 0 {
			fm.WriteString("tags: [" + strings.Join(clean, ", ") + "]\n")
		}
	}
	fm.WriteString(fmt.Sprintf("words: %d\n", len([]rune(body))))
	if oldPath != "" {
		fm.WriteString("updated_at: " + time.Now().Format(time.RFC3339) + "\n")
	}
	fm.WriteString("---\n\n")
	fm.WriteString(body)

	// If editing an old path and the category/slug changed, delete the old file
	var oldBody string
	if oldPath != "" {
		if b, err := os.ReadFile(oldPath); err == nil {
			oldBody = string(b)
		}
		os.Remove(oldPath)
	}

	dir := filepath.Join(RootPosts, postDirFor(cat))
	os.MkdirAll(dir, 0755)
	newPath := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(newPath, []byte(fm.String()), 0644); err != nil {
		execute(w, r, "editor.html", map[string]interface{}{
			"Title": pageTitle("错误"), "Flash": "写入失败: " + err.Error(),
			"Action": "/admin/commit/post", "CatKey": cat, "Slug": slug,
			"Tags": rawTags, "Date": dateRaw, "Body": body, "IsEdit": oldPath != "",
			"BackURL": editorBackURL(r),
		})
		return
	}
	rebuildPostIndex()
	TriggerStaticSync()
	// The cache path is /post/{id}; a slug rename doesn't affect it. Single-page +
	// list-page invalidation
	invalidatePostCache(postID)
	invalidateListCache()
	// Rebuild font subsets automatically after saving (shared + home), ensuring new
	// dynamic chars in titles/categories/mottos are included
	runFontSubset()
	runHomeSubsetAndWBN()
	// File reference tracking diff
	if oldPath != "" && oldBody != "" {
		oldURLs := extractCommentImgPaths(oldBody)
		newURLs := extractCommentImgPaths(body)
		oldSet := map[string]bool{}
		for _, u := range oldURLs {
			oldSet[u] = true
		}
		newSet := map[string]bool{}
		for _, u := range newURLs {
			newSet[u] = true
		}
		for u := range oldSet {
			if !newSet[u] {
				decFileRef(u)
			}
		}
		for u := range newSet {
			if !oldSet[u] {
				incFileRef(u)
			}
		}
	} else {
		// Brand-new post: just +1
		incFileRefs(extractCommentImgPaths(body))
	}
	// SW content generation invalidation: post published/edited -> /sw.js serves a new
	// generation next time -> client SW upgrades and clears caches
	refreshServiceWorker()
	adminRespondRedirect(w, r, "/post/"+strconv.Itoa(postID))
}

// Markdown preview.

func handlePreview(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	md := r.FormValue("body")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(renderMarkdown(md)))
}

// Admin: save config.

func handleAdminConfigSave(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	motto := strings.TrimSpace(r.FormValue("motto"))
	homeMotto := strings.TrimSpace(r.FormValue("home_motto"))
	cfg := loadAppConfig()
	if motto != "" {
		cfg.Motto = motto
	}
	if homeMotto != "" {
		cfg.HomeMotto = homeMotto
	}
	if err := saveAppConfig(cfg); err != nil {
		adminError(w, r, "write error", http.StatusInternalServerError)
		return
	}
	TriggerStaticSync()
	adminRespond(w, r, "")
}

// Font preset CRUD.

// FontOption describes a selectable font (for the admin panel dropdown).
type FontOption struct {
	Family string // CSS font-family value, e.g. 'Nunito'
	Name   string // display name (shown in the UI)
	Weight string // CSS font-weight value, e.g. "400"
	Bevl   string // variable bevel range, e.g. "1 100" (non-empty = bevel adjustment supported)

	order int // dropdown position, from config/font_map.json; never rendered
}

// getAvailableFonts returns the admin panel's selectable faces, read from
// config/font_map.json: a deployment adds one by editing its own data, not this file. An
// entry with no src is not offered; a multi-weight family appears once per weight.
func getAvailableFonts() []FontOption {
	fontMap := loadFontMap()
	opts := make([]FontOption, 0, len(fontMap))
	for key, info := range fontMap {
		if info.Src == "" {
			continue
		}
		name := info.Name
		if name == "" {
			name = key
		}
		opts = append(opts, FontOption{
			Family: "'" + fontCSSFamily(key) + "'",
			Name:   name,
			Weight: info.Weight,
			Bevl:   info.Bevl,
			order:  info.Order,
		})
	}
	sort.Slice(opts, func(i, j int) bool {
		if opts[i].order != opts[j].order {
			return opts[i].order < opts[j].order
		}
		return opts[i].Name < opts[j].Name
	})
	return opts
}

// fontCSSFamily maps a font_map key to the CSS family it supplies a weight of. A
// "-Medium" key is a second weight of the family named before the suffix — the same
// convention runFontSubset relies on when it resolves roles for such a key.
func fontCSSFamily(key string) string {
	return strings.TrimSuffix(key, "-Medium")
}

// saveFontConfig persists the measured font state into state/fonts.json.
// It deliberately does not touch site.json: the hand-written fields and the
// machine-measured ones have different owners and must not share a file.
func saveFontConfig(cfg AppConfig) error {
	if err := saveFontState(cfg); err != nil {
		return err
	}
	// After a font preset change, recompute derived assets (role-level CSS + families
	// table) and the version hash, so subsequently rendered pages reference the new ?v=
	// and get the latest alignment values.
	rebuildInkAssets()
	TriggerStaticSync()
	// A font change affects every page: invalidate the whole site cache — old dist
	// snapshots have the old font references baked in
	invalidateAllCache()
	return nil
}

func handleAdminFontPresetSave(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	presetID := strings.TrimSpace(r.FormValue("preset_id"))
	cfg := loadAppConfig()
	idx := -1
	for i, p := range cfg.FontPresets {
		if p.ID == presetID {
			idx = i
			break
		}
	}
	if idx < 0 {
		adminError(w, r, "preset not found", http.StatusBadRequest)
		return
	}
	// Update each element's font-family (the FONT.md 10-variable system + lalafell roles)
	updated := cfg.FontPresets[idx]
	for _, key := range []string{
		"heading", "subheading", "display_heading", "display_body", "display_mono", "display_jp", "display_i18n", "display_i18n_jp",
		"article", "body", "caption", "footer", "cmt", "data", "data_override_en", "data_override_num", "data_override_sym", "mono", "serif",
		"fun_pill", "fun_title", "fun_title_sym", "fun_desc", "fun_btn", "fun_reroll",
	} {
		val := strings.TrimSpace(r.FormValue(key))
		if val != "" || key == "cmt" || strings.HasPrefix(key, "data_override") {
			updated.Fonts[key] = val
		}
	}
	// Update the name
	if name := strings.TrimSpace(r.FormValue("preset_name")); name != "" {
		updated.Name = name
	}
	// Measured metrics: the admin frontend measures and submits them as a JSON string
	if metricsJSON := strings.TrimSpace(r.FormValue("metrics")); metricsJSON != "" {
		var metrics map[string]FontMetrics
		if err := json.Unmarshal([]byte(metricsJSON), &metrics); err == nil {
			updated.Metrics = metrics
			updated.MetricsVer = 2 // v2 = DOM-measured
		}
	}
	cfg.FontPresets[idx] = updated
	if err := saveFontConfig(cfg); err != nil {
		adminError(w, r, "save error", http.StatusInternalServerError)
		return
	}
	// After a comment font change, pre-generate that font's 3500-char subset
	if cmtVal := strings.TrimSpace(updated.Fonts["cmt"]); cmtVal != "" {
		runCommentFontSubset(cmtVal)
	}
	adminRespond(w, r, "字体预设保存成功")
}

// handleAdminFontRebuild POST /admin/font-rebuild — rebuild the font subsets.
func handleAdminFontRebuild(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// Trigger body/article font subsetting
	runFontSubset()
	// Trigger the admin-page font subset (independent file-level isolation)
	runAdminFontSubset()
	// Trigger the comment subset for the active font
	cfg := loadAppConfig()
	if cfg.ActiveFontPreset != "" {
		for _, p := range cfg.FontPresets {
			if p.ID == cfg.ActiveFontPreset {
				if cmtVal := strings.TrimSpace(p.Fonts["cmt"]); cmtVal != "" {
					runCommentFontSubset(cmtVal)
				}
				break
			}
		}
	}
	adminRespond(w, r, "")
}

func handleAdminFontPresetActivate(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	presetID := strings.TrimSpace(r.FormValue("preset_id"))
	cfg := loadAppConfig()
	found := false
	for _, p := range cfg.FontPresets {
		if p.ID == presetID {
			found = true
			break
		}
	}
	if !found {
		adminError(w, r, "preset not found", http.StatusBadRequest)
		return
	}
	cfg.ActiveFontPreset = presetID
	if err := saveFontConfig(cfg); err != nil {
		adminError(w, r, "save error", http.StatusInternalServerError)
		return
	}
	adminRespond(w, r, "")
}

func handleAdminFontPresetDelete(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	presetID := strings.TrimSpace(r.FormValue("preset_id"))
	cfg := loadAppConfig()
	var remaining []FontPreset
	for _, p := range cfg.FontPresets {
		if p.ID != presetID {
			remaining = append(remaining, p)
		}
	}
	if len(remaining) == len(cfg.FontPresets) {
		adminError(w, r, "preset not found", http.StatusBadRequest)
		return
	}
	// Deleting down to zero presets is not allowed
	if len(remaining) == 0 {
		adminError(w, r, "cannot delete the last preset", http.StatusBadRequest)
		return
	}
	cfg.FontPresets = remaining
	// If the active preset was deleted, switch to the first one
	if cfg.ActiveFontPreset == presetID {
		cfg.ActiveFontPreset = remaining[0].ID
	}
	if err := saveFontConfig(cfg); err != nil {
		adminError(w, r, "save error", http.StatusInternalServerError)
		return
	}
	adminRespond(w, r, "")
}

func handleAdminFontPresetCreate(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("preset_name"))
	if name == "" {
		name = "新预设"
	}
	// Generate a unique ID: name transliteration + last 4 digits of the timestamp
	id := "preset_" + fmt.Sprintf("%d", time.Now().UnixNano()%10000)
	cfg := loadAppConfig()
	// Copy the font config + metrics from the current active preset
	var srcFonts map[string]string
	var srcMetrics map[string]FontMetrics
	for _, p := range cfg.FontPresets {
		if p.ID == cfg.ActiveFontPreset {
			srcFonts = p.Fonts
			srcMetrics = p.Metrics
			break
		}
	}
	if srcFonts == nil {
		// Nothing to copy from: there is no active preset. Reporting beats inventing fonts —
		// a hardcoded fallback would seed the new preset with font names the site may not
		// ship, and their absence would only surface later as misaligned text.
		adminError(w, r, "no active font preset to copy", http.StatusBadRequest)
		return
	}
	// Deep-copy fonts
	fonts := make(map[string]string, len(srcFonts))
	for k, v := range srcFonts {
		fonts[k] = v
	}
	// Deep-copy metrics (if any)
	newPreset := FontPreset{ID: id, Name: name, Fonts: fonts}
	if srcMetrics != nil {
		metrics := make(map[string]FontMetrics, len(srcMetrics))
		for k, v := range srcMetrics {
			metrics[k] = v
		}
		newPreset.Metrics = metrics
	}
	cfg.FontPresets = append(cfg.FontPresets, newPreset)
	cfg.ActiveFontPreset = id
	if err := saveFontConfig(cfg); err != nil {
		adminError(w, r, "save error", http.StatusInternalServerError)
		return
	}
	adminRespond(w, r, "")
}

func generatePhotoID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func randomPhotoColor() string {
	colors := []string{"#FF6B6B", "#4ECDC4", "#45B7D1", "#96CEB4", "#FFEAA7", "#DDA0DD", "#98D8C8", "#F7DC6F", "#BB8FCE", "#85C1E9"}
	return colors[time.Now().UnixMilli()%int64(len(colors))]
}

// generateBlurThumb generates a 230px Gaussian-blur AVIF thumbnail for lazy loading.
// Pipeline: ImageMagick resize+blur+strip -> avifenc 10-bit q65 aq-mode=1 4:4:4
func generateBlurThumb(srcPath, photoID string) {
	thumbDir := DirThumbPhotoBlur
	os.MkdirAll(thumbDir, 0755)

	tmpPNG, err := os.CreateTemp("", "blur-*.png")
	if err != nil {
		log.Printf("photo: blur thumb temp file error for %s: %v", photoID, err)
		return
	}
	pngPath := tmpPNG.Name()
	tmpPNG.Close()
	defer os.Remove(pngPath)

	// ImageMagick: resize to 230px width + Gaussian blur sigma=30, strip metadata
	magickArgs := []string{srcPath, "-resize", "230x", "-blur", "0x30", "-strip", pngPath}
	if strings.HasSuffix(magickCmd, "magick") {
		magickArgs = append([]string{"convert"}, magickArgs...)
	}
	if out, err := exec.Command(magickCmd, magickArgs...).CombinedOutput(); err != nil {
		log.Printf("photo: blur thumb magick error for %s: %s", photoID, strings.TrimSpace(string(out)))
		return
	}

	// avifenc: 10-bit, q65, aq-mode=1, 4:4:4 chroma
	thumbPath := filepath.Join(thumbDir, photoID+".avif")
	if avifencBin == "" {
		log.Printf("photo: avifenc not found, skip blur thumb for %s", photoID)
		return
	}
	if out, err := exec.Command(avifencBin, "-c", "aom", "-s", "6", "-j", "all",
		"-q", "65", "-a", "aq-mode=1", "-d", "10", "-y", "444",
		pngPath, thumbPath).CombinedOutput(); err != nil {
		log.Printf("photo: blur thumb avifenc error for %s: %s", photoID, strings.TrimSpace(string(out)))
		return
	}
}

// generateAllBlurThumbs batch-generates missing blur thumbnails; called asynchronously
// at server startup.
func generateAllBlurThumbs() {
	photos := loadPhotos()
	count := 0
	for _, p := range photos {
		thumbPath := filepath.Join(DirThumbPhotoBlur, p.ID+".avif")
		if _, err := os.Stat(thumbPath); err == nil {
			continue
		}
		var srcPath string
		for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".heic", ".heif", ".avif", ".jxl"} {
			candidate := filepath.Join(DirPhoto, p.ID+ext)
			if _, err := os.Stat(candidate); err == nil {
				srcPath = candidate
				break
			}
		}
		if srcPath == "" {
			log.Printf("photo: no source image for %s, skip blur thumb", p.ID)
			continue
		}
		generateBlurThumb(srcPath, p.ID)
		count++
	}
	if count > 0 {
		log.Printf("photo: generated %d blur thumbnails", count)
	}
}

func handlePhotoAdd(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		adminError(w, r, "Unauthorized", http.StatusUnauthorized)
		return
	}
	r.ParseForm()
	color := strings.TrimSpace(r.FormValue("color"))
	if color == "" {
		color = randomPhotoColor()
	}
	id := generatePhotoID()
	label := randomPhotoLabel()

	os.MkdirAll(DirPhoto, 0755)
	// Random sizes (9:16; varied scaling makes the layout staggered)
	heights := []int{480, 520, 560, 600, 640, 500, 540, 580}
	hh := heights[time.Now().UnixMilli()%int64(len(heights))]
	ww := hh * 9 / 16
	pngPath := filepath.Join(DirPhoto, id+".png")
	magickArgs := []string{"-size", fmt.Sprintf("%dx%d", ww, hh), "xc:" + color, pngPath}
	if strings.HasSuffix(magickCmd, "magick") {
		magickArgs = append([]string{"convert"}, magickArgs...)
	}
	if output, err := exec.Command(magickCmd, magickArgs...).CombinedOutput(); err != nil {
		adminError(w, r, "生成图片失败: "+string(output), http.StatusInternalServerError)
		return
	}
	defer os.Remove(pngPath)

	ConvertImage(ConvertOptions{
		InputPath: pngPath,
		OutputDir: DirPhoto,
		BaseName:  id,
		Formats:   []string{"jxl", "avif"},
	})
	generateBlurThumb(pngPath, id)

	photos := loadPhotos()
	photos = append(photos, Photo{ID: id, Color: color, Label: label, Order: len(photos)})
	savePhotos(photos)
	invalidateCache("/about")
	http.Redirect(w, r, "/about", http.StatusSeeOther)
}

func handlePhotoDelete(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		adminError(w, r, "Unauthorized", http.StatusUnauthorized)
		return
	}
	r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		http.Redirect(w, r, "/about", http.StatusSeeOther)
		return
	}
	for _, ext := range []string{".png", ".jxl", ".avif"} {
		os.Remove(filepath.Join(DirPhoto, id+ext))
	}
	os.Remove(filepath.Join(DirPhoto, ".thumb", id+".avif"))
	photos := loadPhotos()
	var kept []Photo
	for _, p := range photos {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	savePhotos(kept)
	invalidateCache("/about")
	http.Redirect(w, r, "/about", http.StatusSeeOther)
}

func handlePhotoReorder(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		adminError(w, r, "Unauthorized", http.StatusUnauthorized)
		return
	}
	r.ParseForm()
	ids := strings.TrimSpace(r.FormValue("ids"))
	if ids == "" {
		return
	}
	idList := strings.Split(ids, ",")
	photos := loadPhotos()
	ordered := make([]Photo, 0, len(idList))
	for i, id := range idList {
		for _, p := range photos {
			if p.ID == id {
				p.Order = i
				ordered = append(ordered, p)
				break
			}
		}
	}
	savePhotos(ordered)
	invalidateCache("/about")
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
}

func handlePhotoUpload(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		adminError(w, r, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseMultipartForm(50 << 20); err != nil {
		adminError(w, r, "文件太大", http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["images"]
	if len(files) == 0 {
		adminError(w, r, "请选择图片", http.StatusBadRequest)
		return
	}

	allowed := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".heic": true, ".heif": true, ".jxl": true, ".avif": true}
	os.MkdirAll(DirPhoto, 0755)

	type fileResult struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	var results []fileResult

	for _, fh := range files {
		r := fileResult{Name: fh.Filename}
		file, err := fh.Open()
		if err != nil {
			r.Status = "fail"
			r.Error = err.Error()
			results = append(results, r)
			continue
		}

		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if !allowed[ext] {
			file.Close()
			r.Status = "fail"
			r.Error = "不支持的格式: " + ext
			results = append(results, r)
			continue
		}

		id := generatePhotoID()
		label := randomPhotoLabel()

		tmpPath := filepath.Join(DirPhoto, id+ext)
		out, err := os.Create(tmpPath)
		if err != nil {
			file.Close()
			r.Status = "fail"
			r.Error = err.Error()
			results = append(results, r)
			continue
		}
		io.Copy(out, file)
		out.Close()
		file.Close()

		_, err = ConvertImage(ConvertOptions{
			InputPath: tmpPath,
			OutputDir: DirPhoto,
			BaseName:  id,
			Formats:   []string{"jxl", "avif"},
		})
		if err != nil {
			for _, e := range []string{ext, ".jxl", ".avif"} {
				os.Remove(filepath.Join(DirPhoto, id+e))
			}
			r.Status = "fail"
			r.Error = err.Error()
			results = append(results, r)
			continue
		}
		generateBlurThumb(tmpPath, id)

		photos := loadPhotos()
		photos = append(photos, Photo{ID: id, Color: "", Label: label, Order: len(photos)})
		savePhotos(photos)
		r.Status = "ok"
		results = append(results, r)
	}

	invalidateCache("/about")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"results": results})
}

func handlePhotoBatchDelete(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		adminError(w, r, "Unauthorized", http.StatusUnauthorized)
		return
	}
	r.ParseForm()
	ids := r.Form["ids"]
	if len(ids) == 0 {
		http.Redirect(w, r, "/about", http.StatusSeeOther)
		return
	}
	for _, id := range ids {
		for _, ext := range []string{".png", ".jxl", ".avif"} {
			os.Remove(filepath.Join(DirPhoto, id+ext))
		}
		os.Remove(filepath.Join(DirPhoto, ".thumb", id+".avif"))
	}
	photos := loadPhotos()
	var kept []Photo
	for _, p := range photos {
		keep := true
		for _, id := range ids {
			if p.ID == id {
				keep = false
				break
			}
		}
		if keep {
			kept = append(kept, p)
		}
	}
	savePhotos(kept)
	invalidateCache("/about")
	http.Redirect(w, r, "/about", http.StatusSeeOther)
}
