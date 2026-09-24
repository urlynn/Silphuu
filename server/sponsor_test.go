package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSponsorManagement(t *testing.T) {
	// The handlers write sponsor.json; a deployment of its own keeps it out of the checkout.
	useExampleSite(t)

	// Initialize with test data
	initial := []Sponsor{
		{Date: "2026-07-07", Nick: "Tester1", Amount: "10.00", Msg: "hello"},
	}
	_ = saveSponsors(initial)

	// Helper for admin requests
	setAdmin := func(r *http.Request) {
		r.Header.Set("X-Is-Admin", "1")
	}

	// 1. Test Add Sponsor with redirect to /sponsor and empty date (should default to today)
	form := url.Values{}
	form.Set("redirect", "/sponsor")
	form.Set("nick", "NewSponsor")
	form.Set("amount", "88.88")
	form.Set("msg", "cheers")
	// date omitted

	req := httptest.NewRequest("POST", "/commit/sponsor/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setAdmin(req)
	w := httptest.NewRecorder()

	handleAdminSponsorAdd(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/sponsor" {
		t.Fatalf("expected redirect to /sponsor, got %q", loc)
	}

	list := loadSponsors()
	if len(list) != 2 {
		t.Fatalf("expected 2 sponsors, got %d", len(list))
	}
	today := time.Now().Format("2006-01-02")
	if list[1].Date != today {
		t.Errorf("expected date %q, got %q", today, list[1].Date)
	}
	if list[1].Nick != "NewSponsor" || list[1].Amount != "88.88" {
		t.Errorf("unexpected sponsor data: %+v", list[1])
	}

	// 2. Test Edit Sponsor
	editForm := url.Values{}
	editForm.Set("redirect", "/sponsor")
	editForm.Set("index", "1")
	editForm.Set("date", "2026-09-01")
	editForm.Set("nick", "UpdatedNick")
	editForm.Set("amount", "99.99")
	editForm.Set("msg", "updated msg")

	reqEdit := httptest.NewRequest("POST", "/commit/sponsor/edit", strings.NewReader(editForm.Encode()))
	reqEdit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setAdmin(reqEdit)
	wEdit := httptest.NewRecorder()

	handleAdminSponsorEdit(wEdit, reqEdit)

	if wEdit.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303 on edit, got %d", wEdit.Code)
	}
	if wEdit.Header().Get("Location") != "/sponsor" {
		t.Fatalf("expected redirect to /sponsor, got %q", wEdit.Header().Get("Location"))
	}

	list = loadSponsors()
	if list[1].Nick != "UpdatedNick" || list[1].Amount != "99.99" || list[1].Date != "2026-09-01" {
		t.Errorf("expected updated sponsor data, got %+v", list[1])
	}

	// 3. Test Delete Sponsor via fetch (AJAX from admin)
	delForm := url.Values{}
	delForm.Set("index", "1")

	reqDelAjax := httptest.NewRequest("POST", "/commit/sponsor/delete", strings.NewReader(delForm.Encode()))
	reqDelAjax.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqDelAjax.Header.Set("X-Requested-With", "fetch")
	setAdmin(reqDelAjax)
	wDelAjax := httptest.NewRecorder()

	handleAdminSponsorDelete(wDelAjax, reqDelAjax)

	if wDelAjax.Code != http.StatusOK {
		t.Fatalf("expected status 200 for AJAX delete, got %d", wDelAjax.Code)
	}
	var res map[string]string
	_ = json.Unmarshal(wDelAjax.Body.Bytes(), &res)
	if res["ok"] != "true" {
		t.Fatalf("expected ok true, got %v", res)
	}

	list = loadSponsors()
	if len(list) != 1 {
		t.Fatalf("expected 1 sponsor after delete, got %d", len(list))
	}

	// 4. Test Delete Sponsor via standard POST (from public /sponsor page)
	delFormPost := url.Values{}
	delFormPost.Set("index", "0")
	delFormPost.Set("redirect", "/sponsor")

	reqDelPost := httptest.NewRequest("POST", "/commit/sponsor/delete", strings.NewReader(delFormPost.Encode()))
	reqDelPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setAdmin(reqDelPost)
	wDelPost := httptest.NewRecorder()

	handleAdminSponsorDelete(wDelPost, reqDelPost)

	if wDelPost.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303 for form delete, got %d", wDelPost.Code)
	}
	if wDelPost.Header().Get("Location") != "/sponsor" {
		t.Fatalf("expected redirect to /sponsor, got %q", wDelPost.Header().Get("Location"))
	}

	list = loadSponsors()
	if len(list) != 0 {
		t.Fatalf("expected 0 sponsors after second delete, got %d", len(list))
	}
}

func TestSponsorRedirectSecurity(t *testing.T) {
	// Should prevent open redirect
	maliciousForm := url.Values{}
	maliciousForm.Set("redirect", "//evil.com")
	req := httptest.NewRequest("POST", "/commit/sponsor/add", strings.NewReader(maliciousForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	target := sponsorRedirectTarget(req)
	if target != "/admin/dashboard" {
		t.Errorf("expected fallback /admin/dashboard for //evil.com, got %q", target)
	}

	// Referer fallback
	reqRef := httptest.NewRequest("POST", "/commit/sponsor/add", nil)
	reqRef.Header.Set("Referer", "http://localhost:8080/sponsor")
	targetRef := sponsorRedirectTarget(reqRef)
	if targetRef != "/sponsor" {
		t.Errorf("expected /sponsor from referer, got %q", targetRef)
	}
}
