package main

import (
	"testing"
)

func TestCommentOwnerToken(t *testing.T) {
	id := "06g75xxxxxxxxxxxxxxxxxxxxxxx"
	token := genCommentOwnerToken(id)
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// Verify valid token
	if !verifyCommentOwnerToken(id, token) {
		t.Errorf("expected token %s to be valid for id %s", token, id)
	}

	// Verify invalid token
	if verifyCommentOwnerToken(id, "invalid_token_123") {
		t.Errorf("expected invalid token to be rejected")
	}

	// Verify different id
	if verifyCommentOwnerToken("06g75xxxxxxxxxxxxxxxxxxxxxxy", token) {
		t.Errorf("token for one id should not be valid for another")
	}

	// Verify empty id
	if verifyCommentOwnerToken("", token) {
		t.Errorf("token should not be valid for empty id")
	}
}
