package main

// category.go — the category registry (config/topic.json): the one place a category is
// declared. The editor dropdown, the posts/ subdirectory, the /topic/ URL segment and the
// font subset all derive from it, so none of them can drift from the others.
// — see docs/GOTCHAS.md §category-registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Category is one registry entry.
type Category struct {
	// Key is the ASCII slug: the posts/ subdirectory and the /topic/ URL segment. Changing
	// it changes a public URL, so it is the stable half.
	Key string
	// Name is the display name. Free to change; never part of a URL.
	Name string
	Desc string
	// Order places the entry in the editor dropdown and the navbar; ties break on Name.
	Order int
}

// categoryEntry is the on-disk shape of one entry.
type categoryEntry struct {
	Name  string `json:"name"`
	Desc  string `json:"desc"`
	Order int    `json:"order"`
}

// loadCategoryRegistry reads config/topic.json, ordered by Order then Name.
//
// The map key is the slug — {"essay": {"name": "随笔"}} — and "name" is what visitors read.
func loadCategoryRegistry() []Category {
	data, err := os.ReadFile(FileTopicData)
	if err != nil {
		return nil
	}
	// Raw values first, so a non-object entry (a "_comment" string) is skipped instead of
	// failing the whole file.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	out := make([]Category, 0, len(raw))
	for k, v := range raw {
		var e categoryEntry
		if err := json.Unmarshal(v, &e); err != nil {
			continue
		}
		out = append(out, Category{Key: k, Name: e.Name, Desc: e.Desc, Order: e.Order})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// categoryByKey resolves a registry key.
func categoryByKey(key string) (Category, bool) {
	for _, c := range loadCategoryRegistry() {
		if c.Key == key {
			return c, true
		}
	}
	return Category{}, false
}

// categoryByName resolves a display name.
func categoryByName(name string) (Category, bool) {
	for _, c := range loadCategoryRegistry() {
		if c.Name == name {
			return c, true
		}
	}
	return Category{}, false
}

// categoryForDir resolves a posts/ subdirectory name to its category, and reports whether
// the registry declares it. The directory is named after the registry key, or after the
// display name on a site that predates the registry; a directory the registry does not
// declare keeps its own name for both.
func categoryForDir(dir string) (Category, bool) {
	reg := loadCategoryRegistry()
	for _, c := range reg {
		if c.Key == dir {
			return c, true
		}
	}
	for _, c := range reg {
		if c.Name == dir {
			return c, true
		}
	}
	return Category{Key: dir, Name: dir}, false
}

// postDir is one posts/ subdirectory together with the category it belongs to. Carrying
// both is what lets a caller take Path for a file path and Name for a label without
// confusing the two — see docs/GOTCHAS.md §post-category-display-name.
type postDir struct {
	Category
	Path string
}

// postDirs lists every posts/ subdirectory that counts as a category, sorted by display
// name. A directory the registry does not declare only counts once it holds a post: a
// stray or freshly-emptied one would otherwise add a nav entry with no display name and
// nothing behind it.
func postDirs() []postDir {
	entries, err := os.ReadDir(RootPosts)
	if err != nil {
		return nil
	}
	var out []postDir
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		c, declared := categoryForDir(e.Name())
		path := filepath.Join(RootPosts, e.Name())
		if !declared && !holdsPost(path) {
			continue
		}
		out = append(out, postDir{Category: c, Path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// holdsPost reports whether dir contains at least one Markdown file.
func holdsPost(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			return true
		}
	}
	return false
}

// postDirFor returns the posts/ subdirectory a category key is stored in: the key itself
// when it exists, a pre-registry directory named after the display name when that is what
// exists, otherwise the key, to be created.
func postDirFor(key string) string {
	if key == "" {
		return ""
	}
	if isDir(filepath.Join(RootPosts, key)) {
		return key
	}
	if c, ok := categoryByKey(key); ok {
		if isDir(filepath.Join(RootPosts, c.Name)) {
			return c.Name
		}
	}
	return key
}

// categoryOptions returns every category the editor can file a post under: the registry in
// dropdown order, whether or not it holds a post yet, then any undeclared directory.
func categoryOptions() []Category {
	var out []Category
	seen := map[string]bool{}
	for _, c := range loadCategoryRegistry() {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	for _, d := range postDirs() {
		if seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		out = append(out, Category{Key: d.Key, Name: d.Name})
	}
	return out
}

// getCategories returns the display name of every category.
//
// A declared category appears even with no post in it: the registry is the list, and
// dropping a category from the navbar is done by removing its entry.
func getCategories() []string {
	opts := categoryOptions()
	out := make([]string, 0, len(opts))
	for _, c := range opts {
		out = append(out, c.Name)
	}
	return out
}

// getCategoryAliases maps every display name to its registry key. A name with no registry
// entry keeps itself, so a link can never end at a bare "/topic/".
func getCategoryAliases() map[string]string {
	cats := getCategories()
	m := make(map[string]string, len(cats))
	for _, name := range cats {
		m[name] = categoryToAlias(name)
	}
	return m
}

// categoryToAlias returns the registry key for a display name, or the name itself.
func categoryToAlias(cat string) string {
	if c, ok := categoryByName(cat); ok {
		return c.Key
	}
	return cat
}

// aliasToCategory returns the display name for a /topic/ segment: a registry key, a
// pre-registry display name, or the segment itself for an undeclared directory.
func aliasToCategory(alias string) string {
	reg := loadCategoryRegistry()
	for _, c := range reg {
		if c.Key == alias {
			return c.Name
		}
	}
	for _, c := range reg {
		if c.Name == alias {
			return c.Name
		}
	}
	return alias
}

// loadTopicDescs maps a display name to its registry description.
func loadTopicDescs() map[string]string {
	reg := loadCategoryRegistry()
	if len(reg) == 0 {
		return nil
	}
	descs := make(map[string]string, len(reg))
	for _, c := range reg {
		descs[c.Name] = c.Desc
	}
	return descs
}

// defaultCategoryKey is where a post with no submitted category lands: the first declared
// one, else the first directory, else a fresh directory name.
func defaultCategoryKey() string {
	if opts := categoryOptions(); len(opts) > 0 {
		return opts[0].Key
	}
	return "uncategorized"
}

// resolveCategoryKey accepts either half of a category — the registry key or the display
// name — and returns the key, so a form or a link written before the registry existed still
// lands on the right entry. An unknown value is returned unchanged.
func resolveCategoryKey(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	c, _ := categoryForDir(aliasToCategory(v))
	return c.Key
}

// validCategoryKey rejects a submitted category that could address anything but one posts/
// subdirectory. The editor submits a registry key, so a rejection means a forged form or a
// pre-registry site naming its directory directly.
func validCategoryKey(key string) bool {
	if key == "" || key == "." || key == ".." {
		return false
	}
	if strings.ContainsAny(key, `/\`) {
		return false
	}
	return filepath.Base(key) == key
}

// isDir reports whether path is an existing directory.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
