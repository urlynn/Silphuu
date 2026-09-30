# The site directory

Silphuu is one binary. Everything that differs between two sites lives in a directory outside it,
pointed at with `-dir` (or `SILPHUU_HOME`), so upgrading the engine means replacing the binary and
leaving that directory alone. There is no export step and no second tree to keep in step: the engine
reads that directory, and writes what it renders straight into the served root.

## Two roots

| Root | Named by | Holds | Reachable by URL |
|---|---|---|---|
| **Deployment root** | `-dir` / `SILPHUU_HOME` | configuration, posts, state, the site's own media and sources | no, except through the links below |
| **Served root** | `-static` / `SILPHUU_STATIC` | pages, bundles, thumbnails — everything the engine writes | yes, all of it |

The served root defaults to a **sibling** of the deployment root, but the two ways of naming it do
not agree: the engine falls back to `<name>-static` when neither `-static` nor `SILPHUU_STATIC` is
set, while `make site` scaffolds its suggestion as `<name>-served`. Name it explicitly and the
question never comes up. Sibling rather than child, because everything under the served root answers
at a URL and the deployment root holds `config/site.json` (the comment-signing salt) and `state/`
(the comment log). The engine refuses to start when either root contains the other.

Two links cross that boundary, reconciled by the engine on every start:

- `static/` appears in the served tree as a **directory link** to the deployment root's own. One set
  of bytes, two names, nothing to keep in step.
- Every file in `public/` appears in the served tree **by its own name**, one link per file. Those
  files land at the URL root, where the name itself is the address, so they cannot share one link.

Nothing else is copied between the two roots.

## Creating one

```sh
make site DIR=~/silphuu-site                          # served root defaults to ~/silphuu-site-served
make site DIR=~/silphuu-site SERVED=~/silphuu-served
```

`scripts/init-site.sh` writes the smallest set of files a site needs, plus a `README.md` recording
the same rules. It refuses to overwrite an existing `config/site.json`. To run it:

```sh
make run-site DIR=~/silphuu-site STATIC_DIR=~/silphuu-site-served
```

## The one rule

**A file present in the deployment root is used; a file that is absent falls back to the shipped
copy.** The engine reads through this rule instead of copying anything, so a deployment only needs
to contain the files it actually overrides.

It is a *whole-file* rule, not a merge. Two consequences worth knowing before you hit them:

- Overriding `config/ui_strings.json` replaces the entire string table. A key you leave out renders
  as an **empty string** — there is no per-key fallback and no warning. Copy the shipped file and
  edit it; do not write a short one.
- Overriding `templates/components.html` replaces every component in that file, not the one you
  came to change.

The startup log names what the deployment supplies, so the question "is my override in effect" has
an answer that does not require reading the code:

```
overlay: using the deployment's own config/site.json, config/social.json, config/nav_signatures.json, config/friend.json
```

Nothing listed means the site is running on the shipped defaults.

## What a site can override

| Path in the deployment root | Falls back to |
|---|---|
| `templates/*.html` | the engine's `templates/`, matched by file name, whole file |
| `config/*.json` | the engine's `config/`, for the files it ships: `hero.json`, `nav_signatures.json`, `ui_strings.json`, `ui_strings_admin.json`, `font_map.json`, `font_extra_chars.json`, `preview_fixtures.json` |
| `config/presets/*.bin` | the engine's font-subset presets |
| `src/**` | the engine's `src/` — the whole authored tree (CSS, JS, icons, `sw.js`), the site's copy winning per file |

The engine ships neither `src/css/site.css` nor `static/image/**`. A site without the first gets an
empty customisation layer; without the second, no avatar anywhere. Both come from the example site
under `examples/demo/`, which is why `init-site.sh` copies them — and why editing them there edits
the starting point of every site created afterwards.

## What a site owns outright

These have no shipped copy to fall back to.

| Path | Who writes it |
|---|---|
| `config/site.json` | you — site name, author, signature, motto, `comment_salt`, `base_url` |
| `config/topic.json` | you — the category registry. The key is the `posts/` subdirectory and the `/topic/` URL segment, `name` is what visitors read. A declared category gets a page even with no post in it, while an undeclared `posts/` directory is listed in the navbar and the editor but has **no page** — `/topic/<key>` answers 404 until you declare it |
| `config/social.json` | you — the social row in the profile card, each entry naming an icon under `src/icon/` |
| `config/sticker.json` | you — how the sticker packs under `static/sticker/` are grouped |
| `config/hero.json` | the hero panel |
| `config/background.json`, `photo.json`, `sponsor.json`, `friend.json` | the admin UI, as you edit them |
| `posts/` | you — Markdown, one directory per category key |
| `static/` | you, and the upload endpoints — the site's media, linked into the served tree |
| `public/` | you — files that must answer at `/`: the favicon, and whatever a crawler or a registrar asks for |
| `Fonts/` | you — the font sources the workshop subsets from |
| `state/` | the engine — font measurements, the event log, counters. Not yours to edit |
| `staging/` | visitors — submissions awaiting review. Nothing serves this root, so an unreviewed file has no URL even though it is already on disk |

`state/` and `config/` are easy to confuse: `config/` is what you decided, `state/` is what the
engine measured or recorded. Font measurements go in `state/fonts.json`, never mixed into
`config/site.json`.

## Fonts

The engine ships no font files and declares no `@font-face` — typefaces are content, so each site
supplies its own. Three files take part:

- **`Fonts/`** — the sources (TTF/OTF). A source named by the map but missing from this directory is
  skipped without a word.
- **`config/font_map.json`** — the family-to-source map the font workshop subsets from, with the CSS
  weight and an optional display name for the admin dropdown. Override it to name your own families
  and weights. A family whose `src` is empty is neither subset nor offered in the dropdown.
- **`src/css/fonts.css`** — the `@font-face` declarations pointing at the subsets the workshop writes
  under `/assets/font/`.

Without any declaration the engine writes the system font stacks itself, so the `--font-*` variables
always resolve and a site that supplies no fonts still renders — on the system fonts, not on blanks.

## Where to change what

| To change | Where |
|---|---|
| Site name, author, signature, motto, base URL | `config/site.json` |
| Your own credit, beside the project's attribution | `config/site.json` → `credit` / `credit_url` |
| The key that signs a visitor's own-comment token | `config/site.json` → `comment_salt` |
| Hero tags, avatar | `config/hero.json`, `static/image/` |
| Hero background | the hero panel, or `config/background.json` + `static/image/background/` |
| Categories: display name, URL segment, order | `config/topic.json` |
| Footer body: copyright, filing number, donation note | `templates/footer_content.html` |
| The navbar's mood line for a page | `config/nav_signatures.json` |
| Page copy: buttons, labels and the like | `config/ui_strings.json` (and `ui_strings_admin.json` for the admin pages) |
| Layout | `templates/*.html` |
| Styles, optical nudges | `src/css/site.css` |
| Sponsor codes | `static/image/sponsor/` |
| Fonts | `Fonts/` + `config/font_map.json` + `src/css/fonts.css` |

**Every one of these is a whole-file replacement.** Copy the engine's file into the deployment root
first, then edit the copy.

## The served root

Point a web server's root at it and nothing else. Everything in it is written by the engine: pages,
bundles, thumbnails, and `robots.txt` / `feed.xml` / `sitemap.xml`. The site's own media answers
there through the two links above, without a second copy existing anywhere.

Editing anything a render depends on — configuration, templates, posts, authored sources —
invalidates the render cache: the file watcher handles edits while the engine runs, and a content
hash of the same inputs covers edits made while it was down. The hash is over file *contents*, so
rewriting a file with identical bytes changes nothing.
