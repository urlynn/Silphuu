package main

import (
	"strings"
	"testing"
)

func TestStripCSSComments(t *testing.T) {
	input := `
/* Layer 1: Variables */
:root {
    --accent: #A7C080; /* Theme green */
    --url: url("/* not a comment */");
}
/* Multiple
   Line
   Comment */
.box {
    content: '/* hello */';
}
`
	output := stripCSSComments(input)
	if strings.Contains(output, "Layer 1") || strings.Contains(output, "Theme green") || strings.Contains(output, "Multiple") {
		t.Errorf("CSS comment was not stripped:\n%s", output)
	}
	if !strings.Contains(output, `"/* not a comment */"`) {
		t.Errorf("String content inside CSS was corrupted:\n%s", output)
	}
	if !strings.Contains(output, `'/* hello */'`) {
		t.Errorf("String content inside CSS was corrupted:\n%s", output)
	}
}

func TestStripJSComments(t *testing.T) {
	input := "\n" +
		"// Top level comment\n" +
		"function test() {\n" +
		"    var str = \"https://example.com/api\"; // URL in string\n" +
		"    var quote = 'He said \"/* hello */\"'; // nested\n" +
		"    var nested = `/* not comment */ ${ (() => `/* inner not comment */ ${ 1 + 1 }`)() } end`; // template with nested template\n" +
		"    /* Block comment */\n" +
		"    var re = /http:\\/\\/example\\.com\\/(a|b)/g; // regex\n" +
		"    var div = 10 / 2; // division\n" +
		"    return str.replace(/\\/\\*.*?\\*\\//g, '');\n" +
		"}\n"

	output := stripJSComments(input)
	if strings.Contains(output, "Top level comment") || strings.Contains(output, "URL in string") || strings.Contains(output, "Block comment") || strings.Contains(output, "division") || strings.Contains(output, "template with nested") {
		t.Errorf("JS comment was not stripped:\n%s", output)
	}
	if !strings.Contains(output, "/* not comment */") || !strings.Contains(output, "/* inner not comment */") {
		t.Errorf("Nested template literal strings corrupted:\n%s", output)
	}
	if !strings.Contains(output, `"https://example.com/api"`) {
		t.Errorf("String with slashes corrupted:\n%s", output)
	}
	if !strings.Contains(output, `10 / 2`) {
		t.Errorf("Division corrupted:\n%s", output)
	}
	if !strings.Contains(output, `/http:\/\/example\.com\/(a|b)/g`) {
		t.Errorf("Regex literal corrupted:\n%s", output)
	}
}
