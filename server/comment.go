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
	"io"
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
		Date:    time.Now().Format("2006-01-02 15:04"),
		Rid:     rid,
		PostID:  postID,
		UA:      ua,
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
	salt := cfg.Password
	if salt == "" {
		// Weak dev fallback; set admin password in config/site.json (or
		// COMMENT_SALT) before deploying, tokens are only as strong as the salt.
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
		nick = "匿名"
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
	// than the one they were reading.
	requestedSort := strings.TrimSpace(r.FormValue("sort"))
	if requestedSort != "" {
		redirectURL += "?sort=" + normalizeCommentSort(requestedSort)
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
	invalidateCommentCache(postID)

	if r.Header.Get("HX-Request") == "true" {
		// Re-render in the order the page is showing. Hardcoding an order here would make
		// the morph reorder the whole list under the reader.
		sort := normalizeCommentSort(requestedSort)
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

	if c := getCommentByID(id); c != nil {
		invalidateCommentCache(c.PostID)
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"likes":%d,"liked":%v}`, getCommentLikes(id), liked[id])
}

// handleUnifiedCommentDelete performs unified physical comment deletion
// (DELETE /api/comment/{id} or POST /api/comment/delete).
// Auth policy:
//  1. Admin login (isAdmin) -> direct delete allowed;
//  2. Valid owner HMAC token (X-Comment-Token header or token param) -> owner delete
//     without a password;
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

	c := getCommentByID(id)
	postID := 0
	if c != nil {
		postID = c.PostID
		deleteComment(id)
		decFileRefs(commentImgPaths(c.Content, c.Avatar))
		delOrphanedFiles()
	}
	invalidateCommentCache(postID)

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
		aTag := fmt.Sprintf(`<a href="%s" target="_blank" style="color: #007aff; text-decoration: none;">[图片：点击查看]</a>`, html.EscapeString(linkURL))
		htmlContent = strings.Replace(htmlContent, placeholder, aTag, 1)
	}

	// Convert newlines to HTML breaks
	htmlContent = strings.ReplaceAll(htmlContent, "\n", "<br>")
	return htmlContent
}

var (
	replyEmailTmpl  *template.Template
	adminEmailTmpl  *template.Template
	friendEmailTmpl *template.Template
)

func initEmailTemplates() {
	replyEmailTmpl = template.Must(template.ParseFiles(filepath.Join(RootTemplates, "emails", "reply_notify.html")))
	adminEmailTmpl = template.Must(template.ParseFiles(filepath.Join(RootTemplates, "emails", "admin_notify.html")))
	friendEmailTmpl = template.Must(template.ParseFiles(filepath.Join(RootTemplates, "emails", "friend_notify.html")))
}

type replyEmailData struct {
	ParentNick  string
	ContextName string
	ReplyNick   string
	Content     template.HTML
	PageURL     string
}

type adminEmailData struct {
	Nick     string
	Content  template.HTML
	PageURL  string
	LinkText string
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
}

type resendPayload struct {
	From    string            `json:"from"`
	To      []string          `json:"to"`
	Subject string            `json:"subject"`
	Html    string            `json:"html"`
	Text    string            `json:"text"`
	Headers map[string]string `json:"headers"`
}

// Reply email notification.

func replyNotify(toEmail, parentNick, replyNick, replyContent string, postID int) {
	if toEmail == "" {
		return
	}
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		log.Println("Warning: RESEND_API_KEY environment variable is not set, email notification skipped")
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

	fromHeader := notifyEmailFrom()

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

	payload := resendPayload{
		From:    fromHeader,
		To:      []string{toEmail},
		Subject: subject,
		Html:    htmlBuf.String(),
		Text:    textBody,
		Headers: map[string]string{
			"List-Unsubscribe":         fmt.Sprintf("<mailto:noreply@%s?subject=unsubscribe>, <%s>", strings.TrimPrefix(strings.TrimPrefix(siteBaseURL(), "https://"), "http://"), siteBaseURL()),
			"List-Unsubscribe-Post":    "List-Unsubscribe=One-Click",
			"Auto-Submitted":           "auto-generated",
			"X-Auto-Response-Suppress": "All",
		},
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, _ := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(jsonBytes))
	if req == nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending email via Resend: %v", err)
		return
	}
	if resp != nil {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			log.Printf("Resend API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		} else {
			log.Printf("Email successfully sent via Resend. Response: %s", string(bodyBytes))
		}
	}
}

// Admin notification email.

func sendCommentNotify(nick, content, pageTitle, pageURL string) {
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		log.Println("Warning: RESEND_API_KEY environment variable is not set, email notification skipped")
		return
	}

	adminTo := notifyAdminEmail()
	if adminTo == "" {
		log.Println("Warning: ADMIN_NOTIFY_EMAIL environment variable is not set, admin email notification skipped")
		return
	}

	fromHeader := notifyEmailFrom()

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
	data := adminEmailData{
		Nick:     cleanNick,
		Content:  template.HTML(formattedContent),
		PageURL:  pageURL,
		LinkText: linkText,
	}

	if adminEmailTmpl != nil {
		if err := adminEmailTmpl.Execute(&htmlBuf, data); err != nil {
			log.Printf("Error rendering admin email template: %v", err)
			return
		}
	} else {
		log.Println("Warning: adminEmailTmpl is not initialized")
		return
	}

	payload := resendPayload{
		From:    fromHeader,
		To:      []string{adminTo},
		Subject: subject,
		Html:    htmlBuf.String(),
		Text:    textBody,
		Headers: map[string]string{
			"Auto-Submitted":           "auto-generated",
			"X-Auto-Response-Suppress": "All",
		},
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, _ := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(jsonBytes))
	if req == nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending admin email via Resend: %v", err)
		return
	}
	if resp != nil {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			log.Printf("Resend API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		} else {
			log.Printf("Admin email successfully sent via Resend. Response: %s", string(bodyBytes))
		}
	}
}

// Friend-link application notification email.

func sendFriendApplyNotify(data friendApplyEmailData) {
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		log.Println("Warning: RESEND_API_KEY environment variable is not set, friend email notification skipped")
		return
	}

	adminTo := notifyAdminEmail()
	if adminTo == "" {
		log.Println("Warning: ADMIN_NOTIFY_EMAIL environment variable is not set, friend email notification skipped")
		return
	}

	fromHeader := notifyEmailFrom()
	subject := fmt.Sprintf("收到来自 @%s 的友链申请", data.Name)

	textBody := fmt.Sprintf("站长你好：\n\n博客收到了来自 @%s 的友链申请：\n\n站点名称：%s\n站点链接：%s\n站点描述：%s\n标签分类：%s\n联系邮箱：%s\n临时 SVG 路径：%s\n\n查看友链页面：\n%s\n\n---\n本邮件为「%s」管理通知。",
		data.Name, data.Name, data.URL, data.Desc, data.Tag, data.Email, data.TempSVGPath, data.PageURL, loadAppConfig().Author)

	var htmlBuf bytes.Buffer
	if friendEmailTmpl != nil {
		if err := friendEmailTmpl.Execute(&htmlBuf, data); err != nil {
			log.Printf("Error rendering friend email template: %v", err)
			return
		}
	} else {
		log.Println("Warning: friendEmailTmpl is not initialized")
		return
	}

	payload := resendPayload{
		From:    fromHeader,
		To:      []string{adminTo},
		Subject: subject,
		Html:    htmlBuf.String(),
		Text:    textBody,
		Headers: map[string]string{
			"Auto-Submitted":           "auto-generated",
			"X-Auto-Response-Suppress": "All",
		},
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, _ := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(jsonBytes))
	if req == nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending friend apply email via Resend: %v", err)
		return
	}
	if resp != nil {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			log.Printf("Resend API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		} else {
			log.Printf("Friend apply email successfully sent via Resend. Response: %s", string(bodyBytes))
		}
	}
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
