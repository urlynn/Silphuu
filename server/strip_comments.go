package main

import (
	"strings"
	"unicode"
)

// CDN asset comment stripping: removes CSS and JS comments from what is delivered to the
// CDN/browser — except licence banners, see isPreservedComment.

// isPreservedComment reports whether a /* ... */ comment must survive stripping.
//
// `/*!` is the long-standing convention for a banner a minifier keeps, and
// `@license` / `@preserve` are the JSDoc-style equivalents. Without this, the
// shipped bundle carries no notice at all: neither our own front-end licence nor
// the attribution of any vendored library that ships one. A few hundred bytes,
// once per cached asset, is a cheap price for a notice that actually travels with
// the code someone copies.
func isPreservedComment(body string) bool {
	if strings.HasPrefix(body, "!") {
		return true
	}
	return strings.Contains(body, "@license") || strings.Contains(body, "@preserve")
}

// stripCSSComments removes all /* ... */ comments from CSS and collapses extra blank lines.
func stripCSSComments(src string) string {
	var sb strings.Builder
	sb.Grow(len(src))
	n := len(src)
	inString := byte(0) // '"' or '\''

	for i := 0; i < n; i++ {
		ch := src[i]

		// Inside a string
		if inString != 0 {
			sb.WriteByte(ch)
			if ch == '\\' && i+1 < n {
				i++
				sb.WriteByte(src[i])
			} else if ch == inString {
				inString = 0
			}
			continue
		}

		// String start
		if ch == '"' || ch == '\'' {
			inString = ch
			sb.WriteByte(ch)
			continue
		}

		// Block comment /* ... */
		if ch == '/' && i+1 < n && src[i+1] == '*' {
			start := i
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // skip '/'
			if isPreservedComment(src[start+2 : i-1]) {
				sb.WriteString(src[start : i+1])
			}
			continue
		}

		sb.WriteByte(ch)
	}

	return cleanWhitespace(sb.String())
}

// stripJSComments removes // ... and /* ... */ comments from JS while accurately
// preserving strings, regex literals, and nested template literals.
func stripJSComments(src string) string {
	var sb strings.Builder
	sb.Grow(len(src))
	n := len(src)

	// Stack-based handling of template literal `${...}` nesting
	// Mode: 0 = top-level JS/code, 1 = template literal plain text
	type modeFrame struct {
		isTemplate bool
		braceDepth int
	}
	var stack []modeFrame

	var lastNonSpaceToken string
	var prevNonSpaceChar byte

	isRegexPrefix := func() bool {
		if prevNonSpaceChar == 0 {
			return true
		}
		switch prevNonSpaceChar {
		case '(', '[', '{', ';', ',', '=', '!', '&', '|', '?', ':', '~', '^', '+', '-', '*', '%', '<', '>':
			return true
		}
		switch lastNonSpaceToken {
		case "return", "typeof", "instanceof", "in", "yield", "await", "delete", "throw", "case", "default", "void":
			return true
		}
		return false
	}

	for i := 0; i < n; i++ {
		ch := src[i]
		inTemplateText := len(stack) > 0 && stack[len(stack)-1].isTemplate

		// 1. Inside template literal plain text `...`
		if inTemplateText {
			if ch == '\\' && i+1 < n {
				sb.WriteByte(ch)
				i++
				sb.WriteByte(src[i])
				continue
			}
			if ch == '`' {
				// Exit the current template literal
				sb.WriteByte(ch)
				stack = stack[:len(stack)-1]
				prevNonSpaceChar = '`'
				lastNonSpaceToken = "`"
				continue
			}
			if ch == '$' && i+1 < n && src[i+1] == '{' {
				// Enter the ${...} expression region
				sb.WriteString("${")
				i++
				stack = append(stack, modeFrame{isTemplate: false, braceDepth: 1})
				prevNonSpaceChar = '{'
				lastNonSpaceToken = "{"
				continue
			}
			sb.WriteByte(ch)
			continue
		}

		// 2. In code region (plain JS or inside ${...})

		// Track brace depth of ${...}
		if len(stack) > 0 && !stack[len(stack)-1].isTemplate {
			if ch == '{' {
				stack[len(stack)-1].braceDepth++
			} else if ch == '}' {
				stack[len(stack)-1].braceDepth--
				if stack[len(stack)-1].braceDepth == 0 {
					// ${...} ends, back to the outer template literal text region
					sb.WriteByte(ch)
					stack = stack[:len(stack)-1]
					continue
				}
			}
		}

		// Plain string: '...' or "..."
		if ch == '\'' || ch == '"' {
			quote := ch
			sb.WriteByte(ch)
			prevNonSpaceChar = ch
			lastNonSpaceToken = string(ch)
			i++
			for i < n {
				c := src[i]
				sb.WriteByte(c)
				if c == '\\' && i+1 < n {
					i++
					sb.WriteByte(src[i])
					i++
					continue
				}
				if c == quote {
					break
				}
				i++
			}
			continue
		}

		// New template literal start: `
		if ch == '`' {
			sb.WriteByte(ch)
			stack = append(stack, modeFrame{isTemplate: true, braceDepth: 0})
			continue
		}

		// Single-line comment: // ...
		if ch == '/' && i+1 < n && src[i+1] == '/' {
			i += 2
			for i < n && src[i] != '\n' && src[i] != '\r' {
				i++
			}
			if i < n {
				sb.WriteByte(src[i]) // keep the newline to prevent ASI gluing statements
				prevNonSpaceChar = '\n'
			}
			continue
		}

		// Block comment: /* ... */
		if ch == '/' && i+1 < n && src[i+1] == '*' {
			start := i
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // skip '/'
			if isPreservedComment(src[start+2 : i-1]) {
				sb.WriteString(src[start : i+1])
			}
			continue
		}

		// Regex literal: /.../flags
		if ch == '/' && isRegexPrefix() {
			sb.WriteByte(ch)
			prevNonSpaceChar = ch
			i++
			inClass := false
			for i < n {
				c := src[i]
				sb.WriteByte(c)
				if c == '\\' && i+1 < n {
					i++
					sb.WriteByte(src[i])
					i++
					continue
				}
				if c == '[' {
					inClass = true
				} else if c == ']' {
					inClass = false
				} else if c == '/' && !inClass {
					// Regex ends; read modifiers (g, i, m, s, u, y)
					for i+1 < n && (unicode.IsLetter(rune(src[i+1])) || unicode.IsDigit(rune(src[i+1]))) {
						i++
						sb.WriteByte(src[i])
					}
					lastNonSpaceToken = "/"
					prevNonSpaceChar = '/'
					break
				}
				i++
			}
			continue
		}

		// Plain code character
		sb.WriteByte(ch)
		if !unicode.IsSpace(rune(ch)) {
			prevNonSpaceChar = ch
			if unicode.IsLetter(rune(ch)) || unicode.IsDigit(rune(ch)) || ch == '$' || ch == '_' {
				start := i
				for i+1 < n && (unicode.IsLetter(rune(src[i+1])) || unicode.IsDigit(rune(src[i+1])) || src[i+1] == '$' || src[i+1] == '_') {
					i++
					sb.WriteByte(src[i])
				}
				lastNonSpaceToken = src[start : i+1]
				prevNonSpaceChar = src[i]
			} else {
				lastNonSpaceToken = string(ch)
			}
		}
	}

	return cleanWhitespace(sb.String())
}

// cleanWhitespace normalizes blank lines, removing consecutive empty lines.
func cleanWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	lastWasEmpty := false

	for _, line := range lines {
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
		if strings.TrimSpace(trimmed) == "" {
			if !lastWasEmpty && len(result) > 0 {
				result = append(result, "")
				lastWasEmpty = true
			}
		} else {
			result = append(result, trimmed)
			lastWasEmpty = false
		}
	}

	return strings.Join(result, "\n") + "\n"
}
