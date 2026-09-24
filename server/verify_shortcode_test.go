package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestStickerShortcodeURLEncoded(t *testing.T) {
	shortcodeMap = map[string]StickerRef{
		// Fixture: a sticker whose file name contains non-ASCII + spaces (raw, unencoded path)
		"初音未来_初音未来 01": {URL: "/static/sticker/初音未来/avif/初音未来 01.avif"},
	}
	html := renderMarkdown("开头 :初音未来_初音未来 01: 结尾")
	fmt.Println("RENDERED>>>", html)

	if !strings.Contains(string(html), "%20") {
		t.Fatalf("期望输出含百分号编码空格(%%20)，实为: %s", html)
	}
	// A bare space inside quoted src/srcset = the old bug (bluemonday would drop it)
	if strings.Contains(string(html), `src="/static/sticker/初音未来/avif/初音未来 01.avif"`) {
		t.Fatalf("BUG: src 仍是未编码裸路径(含空格): %s", html)
	}
	if strings.Contains(string(html), `srcset="/static/sticker/初音未来/avif/初音未来 01.avif"`) {
		t.Fatalf("BUG: srcset 仍是未编码裸路径(含空格): %s", html)
	}
	// The encoded AVIF src must appear in the final output (passes bluemonday -> loadable)
	if !strings.Contains(string(html), `/static/sticker/%E5%88%9D%E9%9F%B3%E6%9C%AA%E6%9D%A5/avif/%E5%88%9D%E9%9F%B3%E6%9C%AA%E6%9D%A5%2001.avif`) {
		t.Fatalf("期望编码后 AVIF src 出现: %s", html)
	}
	// <picture>/<source> must now survive (bluemonday stripping fixed):
	// animated stickers use anim-picture with an AVIF-only source
	if !strings.Contains(string(html), `picture class="anim-picture"`) {
		t.Fatalf("期望保留 <picture class=\"anim-picture\">: %s", html)
	}
	if !strings.Contains(string(html), `source srcset="`) || !strings.Contains(string(html), `type="image/avif"`) {
		t.Fatalf("期望保留 <source type=\"image/avif\">: %s", html)
	}
	t.Logf("OK encoded url + picture preserved: %s", html)
}
