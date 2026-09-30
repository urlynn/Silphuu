package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Comment write path: everything commits through events -> in-memory projection.

// addComment adds a comment (postID=0 is the guestboard) and returns the comment after
// its event has been committed. rid is the parent comment's ULID.
func addComment(nick, email, website, avatar, content string, postID int, ua string, rid string) *Comment {
	c := &Comment{
		ID:      NewULID(),
		Nick:    nick,
		Email:   email,
		Website: website,
		Avatar:  avatar,
		Content: content,
		// RFC3339, so the offset travels with the instant. A zone-less stamp is read back as
		// the browser's own local time and drifts by the offset between node and reader.
		Date:   time.Now().Format(time.RFC3339),
		Rid:    rid,
		PostID: postID,
		UA:     ua,
	}
	if err := CommitEvent(EvCommentAdded, c); err != nil {
		log.Printf("warn: addComment commit failed: %v", err)
		return nil
	}
	return c
}

func getCommentByID(id string) *Comment {
	return cmGet(id)
}

func likeComment(id string, delta int) {
	_ = CommitEvent(EvCommentLiked, map[string]any{"id": id, "delta": delta})
}

func deleteComment(id string) {
	// Fetch post_id for the tombstone payload
	c := cmGet(id)
	if c == nil {
		return
	}
	_ = CommitEvent(EvCommentDeleted, map[string]any{"id": id, "post_id": c.PostID})
}

func getCommentLikes(id string) int {
	return cmLikeCount(id)
}

// Owner-comment signing & registration-free safe delete.

func genCommentOwnerToken(cmtID string) string {
	cfg := loadAppConfig()
	salt := cfg.CommentSalt
	if salt == "" {
		// Weak dev fallback; set comment_salt in config/site.json (or COMMENT_SALT)
		// before deploying, tokens are only as strong as the salt.
		salt = os.Getenv("COMMENT_SALT")
	}
	if salt == "" {
		salt = "silphuu_dev_salt_change_me"
	}
	mac := hmac.New(sha256.New, []byte(salt+"_comment_token_v1"))
	fmt.Fprintf(mac, "cmt:%s", cmtID)
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

func verifyCommentOwnerToken(cmtID, token string) bool {
	if cmtID == "" || token == "" {
		return false
	}
	expected := genCommentOwnerToken(cmtID)
	return hmac.Equal([]byte(expected), []byte(token))
}

// Handlers.

// sanitizeVisitorURL normalises a visitor-supplied link — the commenter's website or
// avatar. A non-web scheme is dropped rather than completed: in an href or an img src it
// is an injection vector, not a link. The length cap keeps one comment from bloating the
// event log.
func sanitizeVisitorURL(raw string) string {
	if raw == "" {
		return ""
	}
	if len(raw) > 255 {
		raw = raw[:255]
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "vbscript:") {
		return ""
	}
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "https://" + raw
	}
	return raw
}

func handleCommentAdd(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	nick := strings.TrimSpace(r.FormValue("nick"))
	email := strings.TrimSpace(r.FormValue("email"))
	website := sanitizeVisitorURL(strings.TrimSpace(r.FormValue("website")))
	avatar := sanitizeVisitorURL(strings.TrimSpace(r.FormValue("avatar")))
	content := strings.TrimSpace(r.FormValue("content"))
	postID := 0
	fmt.Sscanf(strings.TrimSpace(r.FormValue("post_id")), "%d", &postID)
	rid := strings.TrimSpace(r.FormValue("rid")) // parent comment ULID; "" = top level

	if nick == "" {
		nick = uiText("comment.anonymous")
	} else if len([]rune(nick)) > 50 {
		nick = string([]rune(nick)[:50])
	}

	pagePath := "/guestbook"
	if postID > 0 {
		pagePath = fmt.Sprintf("/post/%d", postID)
	}
	if content == "" {
		http.Redirect(w, r, pagePath, http.StatusSeeOther)
		return
	}
	if contentLen := len([]rune(content)); contentLen < 2 || contentLen > 300 {
		http.Redirect(w, r, pagePath, http.StatusSeeOther)
		return
	}
	if rid != "" && !validULID(rid) {
		http.Redirect(w, r, pagePath, http.StatusSeeOther)
		return
	}

	ua := strings.TrimSpace(r.Header.Get("User-Agent"))
	if len(ua) > 300 {
		ua = ua[:300]
	}
	comment := addComment(nick, email, website, avatar, content, postID, ua, rid)
	if comment == nil {
		http.Redirect(w, r, pagePath, http.StatusSeeOther)
		return
	}
	newID := comment.ID
	token := genCommentOwnerToken(newID)

	// Set the own-comment cookie (for client JS to read and store in LocalStorage)
	http.SetCookie(w, &http.Cookie{
		Name:     "new_cmt",
		Value:    fmt.Sprintf("%s.%s", newID, token),
		Path:     "/",
		MaxAge:   300,
		SameSite: http.SameSiteLaxMode,
	})

	// If this is a reply, email the original commenter
	if rid != "" {
		if parent := getCommentByID(rid); parent != nil && parent.Email != "" {
			go replyNotify(parent.Email, parent.Nick, nick, content, postID)
		}
	}

	redirectURL := pagePath
	// The form carries the order the page is showing, so a plain post lands back on that
	// same order. Without it a no-JS post would bounce the reader to a different order
	// than the one they were reading. The fragment keeps the order out of the cache key.
	requestedSort := strings.TrimSpace(r.FormValue("sort"))
	if requestedSort != "" {
		redirectURL += "#sort=" + normalizeCommentSort(requestedSort)
	}
	pageTitle := "留言板"
	if postID > 0 {
		if t := getPostTitleFromDB(postID); t != "" {
			pageTitle = t
		} else {
			pageTitle = fmt.Sprintf("文章 #%d", postID)
		}
	}
	go sendCommentNotify(nick, content, pageTitle, siteBaseURL()+redirectURL)
	incFileRefs(commentImgPaths(comment.Content, comment.Avatar))

	if r.Header.Get("HX-Request") == "true" {
		// Re-render in the order the page is showing. Hardcoding an order here would make
		// the morph reorder the whole list under the reader.
		sort := normalizeCommentSort(requestedSort)
		comments := commentsForPost(postID, sort)
		data := struct {
			PostComments []Comment
			CurrentPost  *Post
			Sort         string
		}{
			PostComments: comments,
			CurrentPost:  &Post{ID: postID},
			Sort:         sort,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		execute(w, r, "comment_fragment.html", data)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func handleCommentLike(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" || !validULID(id) {
		return
	}

	// Read the liked list from the cookie (set of ULIDs, '_' separated)
	liked := map[string]bool{}
	if c, err := r.Cookie("liked"); err == nil && c.Value != "" {
		for _, s := range strings.Split(c.Value, "_") {
			if s != "" {
				liked[s] = true
			}
		}
	}

	if liked[id] {
		likeComment(id, -1) // unlike
		delete(liked, id)
	} else {
		likeComment(id, 1)
		liked[id] = true
	}

	var ids []string
	for lid := range liked {
		ids = append(ids, lid)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "liked",
		Value:    strings.Join(ids, "_"),
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"likes":%d,"liked":%v}`, getCommentLikes(id), liked[id])
}

// handleUnifiedCommentDelete performs unified physical comment deletion
// (DELETE /api/comment/{id} or POST /api/comment/delete).
// Auth policy:
//  1. Admin login (isAdmin) -> direct delete allowed;
//  2. Valid owner HMAC token (X-Comment-Token header or token param) -> owner delete
//     without a login;
//  3. Otherwise rejected (403).
func handleUnifiedCommentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" && r.Method != "DELETE" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		id = strings.TrimSpace(r.FormValue("id"))
	}
	if id == "" || !validULID(id) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "invalid id"})
		return
	}

	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		token = r.Header.Get("X-Comment-Token")
	}

	authorized := isAdmin(r) || (token != "" && verifyCommentOwnerToken(id, token))
	if !authorized {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "unauthorized"})
		return
	}

	if c := getCommentByID(id); c != nil {
		deleteComment(id)
		decFileRefs(commentImgPaths(c.Content, c.Avatar))
		delOrphanedFiles()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

var emailImgRe = regexp.MustCompile(`!\[(.*?)\]\((.*?)\)`)

func formatEmailContent(content string) string {
	// Extract and store all image links temporarily to prevent HTML escaping them later
	var imgLinks []string
	replaced := emailImgRe.ReplaceAllStringFunc(content, func(imgStr string) string {
		matches := emailImgRe.FindStringSubmatch(imgStr)
		if len(matches) < 3 {
			return imgStr
		}
		path := strings.TrimSpace(matches[2])
		if idx := strings.IndexAny(path, " \t\""); idx != -1 {
			path = path[:idx]
		}

		linkURL := path
		if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
			if strings.HasPrefix(path, "/") {
				linkURL = siteBaseURL() + path
			} else {
				linkURL = siteBaseURL() + "/" + path
			}
		}

		placeholder := fmt.Sprintf("___IMG_PLACEHOLDER_%d___", len(imgLinks))
		imgLinks = append(imgLinks, linkURL)
		return placeholder
	})

	// HTML escape the text to prevent XSS
	htmlContent := html.EscapeString(replaced)

	// Put the HTML <a> tags back
	for i, linkURL := range imgLinks {
		placeholder := fmt.Sprintf("___IMG_PLACEHOLDER_%d___", i)
		aTag := fmt.Sprintf(`<a href="%s" target="_blank" style="color: #007aff; text-decoration: none;">%s</a>`, html.EscapeString(linkURL), uiText("comment.image_link"))
		htmlContent = strings.Replace(htmlContent, placeholder, aTag, 1)
	}

	// Convert newlines to HTML breaks
	htmlContent = strings.ReplaceAll(htmlContent, "\n", "<br>")
	return htmlContent
}

var (
	replyEmailTmpl   *template.Template
	commentEmailTmpl *template.Template
	friendEmailTmpl  *template.Template
	stickerEmailTmpl *template.Template
)

func initEmailTemplates() {
	emails := filepath.Join(RootTemplates, "emails")
	replyEmailTmpl = template.Must(template.ParseFiles(filepath.Join(emails, "reply_notify.html")))
	commentEmailTmpl = template.Must(template.ParseFiles(filepath.Join(emails, "comment_notify.html")))
	friendEmailTmpl = template.Must(template.ParseFiles(filepath.Join(emails, "friend_notify.html")))
	stickerEmailTmpl = template.Must(template.ParseFiles(filepath.Join(emails, "sticker_notify.html")))
}

type replyEmailData struct {
	ParentNick  string
	ContextName string
	ReplyNick   string
	Content     template.HTML
	PageURL     string
	SiteName    string
	SiteURL     string
}

type commentEmailData struct {
	Nick     string
	Content  template.HTML
	PageURL  string
	LinkText string
	SiteName string
	SiteURL  string
}

type friendApplyEmailData struct {
	Name        string
	URL         string
	Desc        string
	Tag         string
	Color       string
	Email       string
	TempSVGPath string
	Icon        string
	SVGContent  template.HTML
	PageURL     string
	SiteName    string
	SiteURL     string
}

type resendPayload struct {
	From    string            `json:"from"`
	To      []string          `json:"to"`
	Subject string            `json:"subject"`
	Html    string            `json:"html,omitempty"`
	Text    string            `json:"text"`
	Headers map[string]string `json:"headers"`
}

// Reply email notification.

func replyNotify(toEmail, parentNick, replyNick, replyContent string, postID int) {
	if toEmail == "" {
		return
	}
	pageURL := siteBaseURL() + "/guestbook"
	contextName := "留言板"
	if postID > 0 {
		pageURL = fmt.Sprintf("%s/post/%d", siteBaseURL(), postID)
		if title := getPostTitleFromDB(postID); title != "" {
			contextName = fmt.Sprintf("文章「%s」", title)
		} else {
			contextName = fmt.Sprintf("文章 #%d", postID)
		}
	}

	cleanReplyNick := strings.TrimPrefix(replyNick, "@")
	if cleanReplyNick == "" || cleanReplyNick == "匿名" {
		cleanReplyNick = "访客"
	}
	cleanParentNick := strings.TrimPrefix(parentNick, "@")
	if cleanParentNick == "" || cleanParentNick == "匿名" {
		cleanParentNick = "你"
	}

	subject := fmt.Sprintf("@%s 在 %s 回复了你的评论", cleanReplyNick, contextName)

	// Clean leading @parentNick from reply content if present (auto-inserted by frontend reply button)
	displayContent := replyContent
	if strings.HasPrefix(displayContent, "@"+parentNick+" ") {
		displayContent = strings.TrimPrefix(displayContent, "@"+parentNick+" ")
	} else if strings.HasPrefix(displayContent, "@"+cleanParentNick+" ") {
		displayContent = strings.TrimPrefix(displayContent, "@"+cleanParentNick+" ")
	}

	// Plain text version
	textBody := fmt.Sprintf("你好 @%s：\n\n你在「%s」%s 中的评论收到了来自 @%s 的新回复：\n\n--------------------------------------------------\n@%s 说：\n%s\n--------------------------------------------------\n\n查看回复与完整讨论：\n%s\n\n---\n本邮件由系统自动发出，请勿直接回复。\n站点来源：%s (%s)",
		cleanParentNick, loadAppConfig().Author, contextName, cleanReplyNick, cleanReplyNick, displayContent, pageURL, loadAppConfig().Author, siteBaseURL())

	// Rich HTML template
	formattedContent := formatEmailContent(displayContent)
	var htmlBuf bytes.Buffer
	data := replyEmailData{
		ParentNick:  cleanParentNick,
		ContextName: contextName,
		ReplyNick:   cleanReplyNick,
		Content:     template.HTML(formattedContent),
		PageURL:     pageURL,
		SiteName:    siteName(),
		SiteURL:     siteBaseURL(),
	}

	if replyEmailTmpl != nil {
		if err := replyEmailTmpl.Execute(&htmlBuf, data); err != nil {
			log.Printf("Error rendering reply email template: %v", err)
			return
		}
	} else {
		log.Println("Warning: replyEmailTmpl is not initialized")
		return
	}

	notifyVisitor(Message{Event: EventCommentReply, To: toEmail, Subject: subject, Body: textBody, HTML: htmlBuf.String()})
}

// Comment notification email, sent to the site owner.

// buildCommentNewMessage is the notice that a comment arrived: the mail form and the concise form
// a chat channel shows. It is separate from sending so the settings panel can send the same
// message as a test.
//
// A template that cannot be executed costs the HTML part and nothing else: the plain-text mail
// and the chat channels still carry the notice.
func buildCommentNewMessage(nick, content, pageTitle, pageURL string) Message {
	cleanNick := strings.TrimPrefix(nick, "@")
	if cleanNick == "" || cleanNick == "匿名" {
		cleanNick = "访客"
	}

	isGuestbook := strings.Contains(pageTitle, "guestbook") || pageTitle == "留言板"
	var subject string
	var linkText string
	if isGuestbook {
		subject = fmt.Sprintf("@%s 在留言板发表了新留言", cleanNick)
		linkText = "查看留言板"
	} else {
		subject = fmt.Sprintf("@%s 在「%s」发表了新评论", cleanNick, pageTitle)
		linkText = "查看评论详情"
	}

	textBody := fmt.Sprintf("站长你好：\n\n博客收到了来自 @%s 的新互动：\n\n--------------------------------------------------\n内容：\n%s\n--------------------------------------------------\n\n直达链接：\n%s\n\n---\n本邮件为「%s」管理通知。",
		cleanNick, content, pageURL, loadAppConfig().Author)

	formattedContent := formatEmailContent(content)
	var htmlBuf bytes.Buffer
	data := commentEmailData{
		Nick:     cleanNick,
		Content:  template.HTML(formattedContent),
		PageURL:  pageURL,
		LinkText: linkText,
		SiteName: siteName(),
		SiteURL:  siteBaseURL(),
	}

	if commentEmailTmpl != nil {
		if err := commentEmailTmpl.Execute(&htmlBuf, data); err != nil {
			log.Printf("Error rendering comment email template: %v", err)
		}
	} else {
		log.Println("Warning: commentEmailTmpl is not initialized")
	}

	return Message{
		Event:   EventCommentNew,
		Subject: subject,
		Body:    textBody,
		HTML:    htmlBuf.String(),
		// The subject already names the author and the page, and Link is where a chat channel
		// sends a reader who wants the rest of it, so the text is the comment alone.
		Chat: content,
		Link: pageURL,
	}
}

func sendCommentNotify(nick, content, pageTitle, pageURL string) {
	notifyAdmin(buildCommentNewMessage(nick, content, pageTitle, pageURL))
}

// Friend-link application notification email.

// buildFriendApplyMessage is the notice that a friend-link application arrived. The staged SVG
// path is left out of the chat form: it names a file on this server, which a reader on a phone
// cannot open.
func buildFriendApplyMessage(data friendApplyEmailData) Message {
	data.SiteName = siteName()
	data.SiteURL = siteBaseURL()

	subject := fmt.Sprintf("收到来自 @%s 的友链申请", data.Name)

	textBody := fmt.Sprintf("站长你好：\n\n博客收到了来自 @%s 的友链申请：\n\n站点名称：%s\n站点链接：%s\n站点描述：%s\n标签分类：%s\n联系邮箱：%s\n临时 SVG 路径：%s\n\n查看友链页面：\n%s\n\n---\n本邮件为「%s」管理通知。",
		data.Name, data.Name, data.URL, data.Desc, data.Tag, data.Email, data.TempSVGPath, data.PageURL, loadAppConfig().Author)

	var htmlBuf bytes.Buffer
	if friendEmailTmpl != nil {
		if err := friendEmailTmpl.Execute(&htmlBuf, data); err != nil {
			log.Printf("Error rendering friend email template: %v", err)
		}
	} else {
		log.Println("Warning: friendEmailTmpl is not initialized")
	}

	chat := fmt.Sprintf("站点：%s\n链接：%s\n描述：%s\n标签：%s\n邮箱：%s",
		data.Name, data.URL, data.Desc, data.Tag, data.Email)

	return Message{
		Event:   EventFriendApply,
		Subject: subject,
		Body:    textBody,
		HTML:    htmlBuf.String(),
		Chat:    chat,
		Link:    data.PageURL,
	}
}

func sendFriendApplyNotify(data friendApplyEmailData) {
	notifyAdmin(buildFriendApplyMessage(data))
}

// Sticker contribution emails.

// stickerEmailData is what the sticker submission notice renders: which pack, how many
// images it carries, the address the sender left, and where the pack waits. The only
// recipient is the site owner — nothing is mailed back to the sender.
type stickerEmailData struct {
	Group    string
	Email    string
	Count    int
	Staged   string
	PageURL  string
	SiteName string
	SiteURL  string
}

// sendStickerApplyNotify tells the admin a pack is waiting in staging.
func sendStickerApplyNotify(data stickerEmailData) {
	notifyAdmin(buildStickerApplyMessage(data))
}

// buildStickerApplyMessage is the notice that a pack is waiting in staging. The staging directory
// is left out of the chat form: it names a path on this server.
func buildStickerApplyMessage(data stickerEmailData) Message {
	data.SiteName = siteName()
	data.SiteURL = siteBaseURL()
	if data.PageURL == "" {
		data.PageURL = data.SiteURL + PageDashboard
	}

	subject := fmt.Sprintf("新表情包投稿：%s", data.Group)
	textBody := fmt.Sprintf("站长你好：\n\n博客收到了一套表情包投稿，共 %d 张：\n\n表情包名称：%s\n投稿邮箱：%s\n暂存目录：%s\n\n前往后台：\n%s\n\n---\n本邮件为「%s」管理通知。",
		data.Count, data.Group, orUnset(data.Email), data.Staged, data.PageURL, data.SiteName)

	chat := fmt.Sprintf("表情包：%s\n张数：%d\n投稿邮箱：%s",
		data.Group, data.Count, orUnset(data.Email))

	return Message{
		Event:   EventStickerApply,
		Subject: subject,
		Body:    textBody,
		HTML:    renderStickerEmail(stickerEmailTmpl, "sticker apply", data),
		Chat:    chat,
		Link:    data.PageURL,
	}
}

// orUnset returns a readable stand-in for a field the sender left empty.
func orUnset(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未提供"
	}
	return s
}

// renderStickerEmail executes one sticker email template. An unrenderable template yields
// no body rather than a half-written one: a mail with a raw action in it is worse than a
// plain-text mail.
func renderStickerEmail(tmpl *template.Template, what string, data stickerEmailData) string {
	if tmpl == nil {
		log.Printf("Warning: %s template is not initialized", what)
		return ""
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("Error rendering %s email template: %v", what, err)
		return ""
	}
	return buf.String()
}

// File reference tracking (orphan image cleanup, in-memory).

// imgRe matches illustration references in post/comment bodies; it only recognizes the
// current singular directories /static/image/{post,comment}/.
var imgRe = regexp.MustCompile(`(/static/image/(?:post|comment))/([a-f0-9]+)`)

// commentImgPaths lists every uploaded image a comment keeps alive. The avatar counts:
// a visitor may point it at an image they uploaded, and that file has no other reference
// once the comment body stops mentioning it.
func commentImgPaths(content, avatar string) []string {
	return extractCommentImgPaths(content + " " + avatar)
}

func extractCommentImgPaths(s string) []string {
	matches := imgRe.FindAllStringSubmatch(s, -1)
	seen := map[string]bool{}
	var result []string
	for _, m := range matches {
		dir := m[1] // /static/image/post or /static/image/comment
		baseName := m[2]
		for _, ext := range []string{".jxl", ".avif"} {
			path := dir + "/" + baseName + ext
			if !seen[path] {
				result = append(result, path)
				seen[path] = true
			}
		}
	}
	return result
}
