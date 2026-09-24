package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeSVG(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
		mustNot []string
		mustIn  []string
	}{
		{
			name:    "valid simple svg",
			input:   `<svg viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>`,
			wantErr: false,
			mustIn:  []string{`<svg viewBox="0 0 24 24">`, `</svg>`},
		},
		{
			name:    "strip script and onload",
			input:   `<svg viewBox="0 0 24 24" onload="alert(1)"><script>alert(2)</script><circle cx="12" cy="12" r="10"/></svg>`,
			wantErr: false,
			mustNot: []string{"<script>", "alert(1)", "alert(2)", "onload"},
			mustIn:  []string{`<circle cx="12" cy="12" r="10"/>`},
		},
		{
			name:    "strip foreignObject and javascript link",
			input:   `<svg><foreignObject><body xmlns="http://www.w3.org/1999/xhtml"><script>bad()</script></body></foreignObject><a href="javascript:alert(1)"><path/></a></svg>`,
			wantErr: false,
			mustNot: []string{"foreignObject", "javascript:", "<script>", "bad()"},
		},
		{
			name:    "invalid no svg tag",
			input:   `<div>hello</div>`,
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := sanitizeSVG(tc.input)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, mn := range tc.mustNot {
				if strings.Contains(res, mn) {
					t.Errorf("result should not contain %q, but got: %s", mn, res)
				}
			}
			for _, mi := range tc.mustIn {
				if !strings.Contains(res, mi) {
					t.Errorf("result should contain %q, but got: %s", mi, res)
				}
			}
		})
	}
}

func TestGenerateSafeSlug(t *testing.T) {
	s1 := generateSafeSlug("Google", "https://www.google.com/search")
	if s1 != "google-com" {
		t.Errorf("expected google-com, got %q", s1)
	}

	s2 := generateSafeSlug("Example Blog", "https://blog.example.com/posts")
	if s2 != "blog-example-com" {
		t.Errorf("expected blog-example-com, got %q", s2)
	}

	s3 := generateSafeSlug("测试中文", "invalid-url")
	if s3 == "" {
		t.Errorf("expected non-empty slug, got %q", s3)
	}
}

func TestHandleFriendApplyVisitor(t *testing.T) {
	// A guest submission writes friend.json and drops a <unix-ts>_<slug>.svg into
	// staging/friend/. A deployment of its own is what keeps both out of the
	// checkout; the timestamped name rules out cleaning up by path afterwards.
	useExampleSite(t)

	formData := url.Values{
		"name":  {"测试友链"},
		"url":   {"https://example.com"},
		"desc":  {"测试网站描述"},
		"tag":   {"技术"},
		"email": {"visitor@example.com"},
		"svg":   {`<svg viewBox="0 0 24 24"><path d="M1 1h22v22H1z"/></svg>`},
	}

	req := httptest.NewRequest("POST", "/api/friend/apply", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "example.com"
	req.RemoteAddr = "203.0.113.1:54321"
	rec := httptest.NewRecorder()

	handleFriendApply(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect StatusSeeOther (303), got %d; body: %s", rec.Code, rec.Body.String())
	}

	loc := rec.Header().Get("Location")
	if loc != PageFriends+"?applied=1" {
		t.Errorf("expected redirect to %s?applied=1, got %q", PageFriends, loc)
	}

	// Verify friend.json was not modified by the guest submission
	curList := loadFriends()
	for _, f := range curList {
		if f.Name == "测试友链" {
			t.Errorf("visitor should not directly add friend into friend.json")
		}
	}
}

// applyAsGuest posts a friend-link application the way the public form does.
func applyAsGuest(t *testing.T, form url.Values) {
	t.Helper()
	req := httptest.NewRequest("POST", RouteFriendApply, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.9:1234"
	handleFriendApply(httptest.NewRecorder(), req)
}

// approveAsAdmin posts an approval through the same path the friends page uses.
func approveAsAdmin(t *testing.T, staged string, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"staged": {staged}, "redirect": {PageFriends}}
	req := httptest.NewRequest("POST", "/admin/commit/friend/approve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if admin {
		req.Header.Set("X-Is-Admin", "1")
	}
	rec := httptest.NewRecorder()
	handleAdminFriendApprove(rec, req)
	return rec
}

// TestVisitorApplicationIsStagedForReview: a guest's submission is written to disk beside
// its logo. Until it was, the fields lived only in the notification email and nothing on
// disk recorded what had been asked for, so no page could list the queue.
func TestVisitorApplicationIsStagedForReview(t *testing.T) {
	useExampleSite(t)

	applyAsGuest(t, url.Values{
		"name":  {"待审站点"},
		"url":   {"https://pending.example.com"},
		"desc":  {"等待审核的站点"},
		"tag":   {"技术"},
		"email": {"visitor@example.com"},
		"svg":   {`<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>`},
	})

	apps := loadFriendApplications()
	if len(apps) != 1 {
		t.Fatalf("loadFriendApplications() = %d entries, want 1", len(apps))
	}
	got := apps[0]
	if got.Name != "待审站点" || got.URL != "https://pending.example.com" ||
		got.Desc != "等待审核的站点" || got.Tag != "技术" || got.Email != "visitor@example.com" {
		t.Errorf("staged application = %+v, want the submitted fields", got)
	}
	if got.Time == "" {
		t.Error("staged application has no submission time")
	}
	if !got.HasSVG {
		t.Fatal("HasSVG = false, want the submitted logo staged beside the sidecar")
	}
	if _, err := os.Stat(filepath.Join(DirStagingFriend, got.Staged+".svg")); err != nil {
		t.Errorf("staged logo missing: %v", err)
	}
}

// TestAdminFriendApprovePublishesTheApplication: approving moves the staged logo into the
// deployment's src/icon/ and appends the entry, which is the whole of what approval does.
func TestAdminFriendApprovePublishesTheApplication(t *testing.T) {
	useExampleSite(t)

	applyAsGuest(t, url.Values{
		"name": {"待审站点"},
		"url":  {"https://pending.example.com"},
		"desc": {"等待审核的站点"},
		"svg":  {`<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>`},
	})

	apps := loadFriendApplications()
	if len(apps) != 1 {
		t.Fatalf("test precondition broken: %d staged applications, want 1", len(apps))
	}
	staged := apps[0].Staged
	before := len(loadFriends())

	rec := approveAsAdmin(t, staged, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != PageFriends {
		t.Errorf("expected redirect to %s, got %q", PageFriends, loc)
	}

	list := loadFriends()
	if len(list) != before+1 {
		t.Fatalf("friend.json holds %d entries, want %d", len(list), before+1)
	}
	added := list[len(list)-1]
	if added.Name != "待审站点" || added.URL != "https://pending.example.com" || added.Desc != "等待审核的站点" {
		t.Errorf("approved entry = %+v, want the staged fields", added)
	}
	if added.Icon == "" || added.Icon == "link" {
		t.Fatalf("approved entry icon = %q, want the submitted logo's name", added.Icon)
	}

	// The logo has to land where the template reads, or the new card renders blank.
	if _, err := os.Stat(homeSrcPath("icon", added.Icon+".svg")); err != nil {
		t.Errorf("approved logo is not under the deployment's src/icon/: %v", err)
	}
	if _, err := siteIconSVG("icon/" + added.Icon + ".svg"); err != nil {
		t.Errorf("siteIconSVG(%s) = %v, want the approved logo", added.Icon, err)
	}

	// Approving consumes the staged pair, which is what makes a second click a no-op
	// rather than a duplicate entry.
	if _, ok := loadFriendApplication(staged); ok {
		t.Error("the staged application survived approval")
	}
	if _, err := os.Stat(filepath.Join(DirStagingFriend, staged+".svg")); !os.IsNotExist(err) {
		t.Error("the staged logo survived approval")
	}
}

// TestAdminFriendApproveRefusesVisitorsAndUnknownNames: the endpoint is reachable by URL,
// so it has to reject both a visitor and a staged name that is not on disk.
func TestAdminFriendApproveRefusesVisitorsAndUnknownNames(t *testing.T) {
	useExampleSite(t)

	applyAsGuest(t, url.Values{
		"name": {"待审站点"},
		"url":  {"https://pending.example.com"},
		"desc": {"等待审核的站点"},
	})

	apps := loadFriendApplications()
	if len(apps) != 1 {
		t.Fatalf("test precondition broken: %d staged applications, want 1", len(apps))
	}
	staged := apps[0].Staged
	before := len(loadFriends())

	if rec := approveAsAdmin(t, staged, false); rec.Code != http.StatusSeeOther {
		t.Errorf("visitor approve status = %d, want a redirect", rec.Code)
	}
	if got := len(loadFriends()); got != before {
		t.Fatalf("a visitor published a friend link (%d entries, want %d)", got, before)
	}

	// A name that is not on disk, including one that tries to climb out of the staging
	// root or name a path.
	for _, bad := range []string{"", "nope", "../data/config", "/etc/passwd", ".", "sub/nested"} {
		if rec := approveAsAdmin(t, bad, true); rec.Code != http.StatusBadRequest {
			t.Errorf("approve(%q) status = %d, want 400", bad, rec.Code)
		}
	}
	if got := len(loadFriends()); got != before {
		t.Fatalf("an unknown staged name published a friend link (%d entries, want %d)", got, before)
	}
	if _, ok := loadFriendApplication(staged); !ok {
		t.Error("the real application was consumed while unknown names were being refused")
	}
}

// TestAdminFriendApproveWithoutALogoUsesTheFallbackIcon: the sidecar is the application,
// so one that arrived with no logo still publishes — on the same fallback the admin form
// uses for a link added without one.
func TestAdminFriendApproveWithoutALogoUsesTheFallbackIcon(t *testing.T) {
	useExampleSite(t)

	applyAsGuest(t, url.Values{
		"name": {"无图标站点"},
		"url":  {"https://no-logo.example.com"},
		"desc": {"没有提交图标"},
	})

	apps := loadFriendApplications()
	if len(apps) != 1 {
		t.Fatalf("loadFriendApplications() = %d entries, want 1 without a logo", len(apps))
	}
	if apps[0].HasSVG {
		t.Fatal("test precondition broken: HasSVG = true for an application with no logo")
	}

	if rec := approveAsAdmin(t, apps[0].Staged, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303, got %d: %s", rec.Code, rec.Body.String())
	}

	list := loadFriends()
	if len(list) == 0 {
		t.Fatal("approval added no friend link")
	}
	if got := list[len(list)-1].Icon; got != "link" {
		t.Errorf("icon = %q, want the fallback %q", got, "link")
	}
}

// TestFriendsPageShowsPendingApplicationsToAdminsOnly: a staged application carries the
// submitter's address, so it is not part of what a visitor's page contains.
func TestFriendsPageShowsPendingApplicationsToAdminsOnly(t *testing.T) {
	useExampleSite(t)

	applyAsGuest(t, url.Values{
		"name": {"待审站点"},
		"url":  {"https://pending.example.com"},
		"desc": {"等待审核的站点"},
	})

	page := func(admin bool) string {
		t.Helper()
		req := httptest.NewRequest("GET", PageFriends, nil)
		if admin {
			req.Header.Set("X-Is-Admin", "1")
		}
		rec := httptest.NewRecorder()
		handleFriends(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", PageFriends, rec.Code)
		}
		return rec.Body.String()
	}

	adminBody := page(true)
	if !strings.Contains(adminBody, "https://pending.example.com") {
		t.Error("the admin's friends page does not list the pending application")
	}
	if !strings.Contains(adminBody, "name=staged") && !strings.Contains(adminBody, `name="staged"`) {
		t.Error("the admin's friends page carries no approve form")
	}

	visitorBody := page(false)
	if strings.Contains(visitorBody, "https://pending.example.com") {
		t.Error("a visitor's friends page lists a pending application")
	}
	if strings.Contains(visitorBody, "name=staged") || strings.Contains(visitorBody, `name="staged"`) {
		t.Error("a visitor's friends page carries the approve form")
	}
}

func TestHandleFriendApplyAdmin(t *testing.T) {
	// The admin branch writes friend.json and an uploaded icon into the deployment's own
	// src/icon/; a deployment of its own keeps both out of the checkout.
	useExampleSite(t)

	svgIconPath := homeSrcPath("icon", "friend-test-admin-com.svg")

	formData := url.Values{
		"name": {"管理员新增友链"},
		"url":  {"https://test-admin.com"},
		"desc": {"管理员直接添加的站点"},
		"tag":  {"推荐"},
		"svg":  {`<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>`},
	}

	req := httptest.NewRequest("POST", "/api/friend/apply", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Is-Admin", "1")

	rec := httptest.NewRecorder()
	handleFriendApply(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect StatusSeeOther (303), got %d; body: %s", rec.Code, rec.Body.String())
	}

	loc := rec.Header().Get("Location")
	if loc != PageFriends {
		t.Errorf("expected redirect to %s, got %q", PageFriends, loc)
	}

	// Verify friend.json gained the entry
	curList := loadFriends()
	found := false
	for _, f := range curList {
		if f.Name == "管理员新增友链" {
			found = true
			if f.Icon != "friend-test-admin-com" {
				t.Errorf("expected icon friend-test-admin-com, got %s", f.Icon)
			}
			break
		}
	}
	if !found {
		t.Errorf("admin added friend link not found in friend.json")
	}

	// Verify the SVG file was written to disk
	if _, err := os.Stat(svgIconPath); os.IsNotExist(err) {
		t.Errorf("expected svg file %s to be created", svgIconPath)
	}
}

// TestAdminFriendIconResolvesFromTheDeploymentSrc: an uploaded logo is not in the
// embedded set, so the template can only draw it if the disk fallback finds the file the
// admin branch just wrote. Writing it anywhere the template does not read leaves a friend
// link whose icon is silently blank.
func TestAdminFriendIconResolvesFromTheDeploymentSrc(t *testing.T) {
	useExampleSite(t)

	formData := url.Values{
		"name": {"磁盘图标友链"},
		"url":  {"https://disk-icon.example.com"},
		"svg":  {`<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>`},
	}
	req := httptest.NewRequest("POST", "/api/friend/apply", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Is-Admin", "1")
	handleFriendApply(httptest.NewRecorder(), req)

	icon := ""
	for _, f := range loadFriends() {
		if f.Name == "磁盘图标友链" {
			icon = f.Icon
		}
	}
	if icon == "" || icon == "link" {
		t.Fatalf("icon = %q, want the uploaded logo's name", icon)
	}
	if _, embedded := iconSVGs[icon]; embedded {
		t.Fatalf("test precondition broken: %q is in the embedded set", icon)
	}

	got, err := siteIconSVG("icon/" + icon + ".svg")
	if err != nil {
		t.Fatalf("siteIconSVG(%s) = %v, want the logo the admin branch wrote", icon, err)
	}
	if !strings.Contains(string(got), `data-icon="`+icon+`"`) {
		t.Errorf("icon markup = %s, want it to carry data-icon=%q", got, icon)
	}

	// The template function is the real consumer: friends.html draws the card's icon
	// with {{svg .Icon}}, so resolving the file is only half of the fix.
	tmpl, err := loadTemplates().New("icon-probe").Parse(`{{svg .Icon}}`)
	if err != nil {
		t.Fatalf("parse probe template: %v", err)
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "icon-probe", map[string]string{"Icon": icon}); err != nil {
		t.Fatalf("execute probe template: %v", err)
	}
	if !strings.Contains(buf.String(), `data-icon="`+icon+`"`) {
		t.Errorf("{{svg %q}} rendered %q, want the uploaded logo", icon, buf.String())
	}
}

func TestHandleFriendEditAdmin(t *testing.T) {
	useExampleSite(t)

	initial := []FriendLink{
		{Name: "Old Site", URL: "https://old.example.com", Desc: "Old desc", Icon: "link", Tag: "Blog"},
	}
	_ = saveFriends(initial)

	// 1. Visitor cannot edit
	formData := url.Values{
		"index": {"0"},
		"name":  {"Hacked Site"},
		"url":   {"https://hacked.com"},
		"desc":  {"Hacked desc"},
		"tag":   {"Blog"},
	}
	reqVis := httptest.NewRequest("POST", "/admin/commit/friend/edit", strings.NewReader(formData.Encode()))
	reqVis.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recVis := httptest.NewRecorder()
	handleAdminFriendEdit(recVis, reqVis)

	if recVis.Code != http.StatusSeeOther {
		t.Fatalf("expected visitor to be redirected, got %d", recVis.Code)
	}
	listAfterVis := loadFriends()
	if listAfterVis[0].Name != "Old Site" {
		t.Fatalf("visitor modified friend link!")
	}

	// 2. Admin can edit
	editData := url.Values{
		"index":    {"0"},
		"name":     {"New Site Name"},
		"url":      {"https://new-site.example.com"},
		"desc":     {"New site description"},
		"tag":      {"Tech"},
		"color":    {"#3B82F6"},
		"redirect": {PageFriends},
		"svg":      {`<svg viewBox="0 0 24 24"><polygon points="12 2 15 8 22 9 17 14 18 21 12 17 6 21 7 14 2 9 9 8 12 2"/></svg>`},
	}
	reqAdm := httptest.NewRequest("POST", "/admin/commit/friend/edit", strings.NewReader(editData.Encode()))
	reqAdm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqAdm.Header.Set("X-Is-Admin", "1")
	recAdm := httptest.NewRecorder()

	handleAdminFriendEdit(recAdm, reqAdm)

	if recAdm.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303, got %d: %s", recAdm.Code, recAdm.Body.String())
	}
	if loc := recAdm.Header().Get("Location"); loc != PageFriends {
		t.Errorf("expected redirect to %s, got %q", PageFriends, loc)
	}

	listAfterAdm := loadFriends()
	if len(listAfterAdm) != 1 {
		t.Fatalf("expected 1 friend, got %d", len(listAfterAdm))
	}
	updated := listAfterAdm[0]
	if updated.Name != "New Site Name" || updated.URL != "https://new-site.example.com" || updated.Tag != "Tech" || updated.Color != "#3B82F6" {
		t.Errorf("unexpected updated friend link: %+v", updated)
	}
	if !strings.HasPrefix(updated.Icon, "friend-") {
		t.Errorf("expected icon starting with friend-, got %s", updated.Icon)
	}
}

func TestHandleFriendDeleteAdmin(t *testing.T) {
	useExampleSite(t)

	initial := []FriendLink{
		{Name: "Site 1", URL: "https://site1.com", Desc: "Desc 1", Icon: "link", Tag: "Blog"},
		{Name: "Site 2", URL: "https://site2.com", Desc: "Desc 2", Icon: "link", Tag: "Blog"},
	}
	_ = saveFriends(initial)

	// 1. Visitor cannot delete
	delForm := url.Values{"index": {"0"}}
	reqVis := httptest.NewRequest("POST", "/admin/commit/friend/delete", strings.NewReader(delForm.Encode()))
	reqVis.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recVis := httptest.NewRecorder()
	handleAdminFriendDelete(recVis, reqVis)

	if len(loadFriends()) != 2 {
		t.Fatalf("visitor deleted a friend link!")
	}

	// 2. Admin delete via regular POST
	reqAdm := httptest.NewRequest("POST", "/admin/commit/friend/delete", strings.NewReader(delForm.Encode()))
	reqAdm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqAdm.Header.Set("X-Is-Admin", "1")
	recAdm := httptest.NewRecorder()
	handleAdminFriendDelete(recAdm, reqAdm)

	if recAdm.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303, got %d", recAdm.Code)
	}
	listAfterDel := loadFriends()
	if len(listAfterDel) != 1 || listAfterDel[0].Name != "Site 2" {
		t.Fatalf("expected 1 friend remaining (Site 2), got %+v", listAfterDel)
	}

	// 3. Admin delete via fetch (AJAX)
	delFetchForm := url.Values{"index": {"0"}}
	reqFetch := httptest.NewRequest("POST", "/admin/commit/friend/delete", strings.NewReader(delFetchForm.Encode()))
	reqFetch.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqFetch.Header.Set("X-Requested-With", "fetch")
	reqFetch.Header.Set("X-Is-Admin", "1")
	recFetch := httptest.NewRecorder()
	handleAdminFriendDelete(recFetch, reqFetch)

	if recFetch.Code != http.StatusOK {
		t.Fatalf("expected status 200 for fetch delete, got %d", recFetch.Code)
	}
	if len(loadFriends()) != 0 {
		t.Fatalf("expected 0 friends remaining, got %d", len(loadFriends()))
	}
}
