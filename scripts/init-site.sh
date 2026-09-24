#!/usr/bin/env bash
# init-site.sh — scaffold a deployment directory you own.
#
#   make site DIR=~/silphuu-site
#
# Creates the smallest set of files a site needs, so the engine stays untouched
# and upgrading is a matter of replacing the binary. Refuses to overwrite an
# existing site.json.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [ $# -lt 1 ] || [ -z "${1:-}" ]; then
    echo "usage: $0 <site-directory>" >&2
    echo "   e.g. $0 ~/silphuu-site" >&2
    exit 2
fi

SITE="$1"
case "$SITE" in
    "~"/*) SITE="$HOME/${SITE#\~/}" ;;
esac
if ! SITE_ABS=$(cd "$(dirname "$SITE")" 2>/dev/null && pwd)/"$(basename "$SITE")"; then
    echo "error: cannot resolve $SITE — does its parent directory exist?" >&2
    exit 2
fi

CONFIG="$SITE_ABS/config/site.json"
if [ -e "$CONFIG" ]; then
    echo "error: $CONFIG already exists." >&2
    echo "Refusing to overwrite your configuration. Edit it directly, or pick another directory." >&2
    exit 1
fi

mkdir -p "$SITE_ABS/config" \
         "$SITE_ABS/state" \
         "$SITE_ABS/posts" \
         "$SITE_ABS/templates" \
         "$SITE_ABS/assets/css" \
         "$SITE_ABS/static/image/post" \
         "$SITE_ABS/static/image/comment"

# site.css and image/** are the two things the engine ships neither of. The stylesheet is a
# deployment *source* at src/css/site.css, inlined into every page's <head> by head.html; the
# avatar, default avatar and hero background live under /assets/image/. A site without the
# first one gets an empty customisation layer, without the second one no avatar anywhere.
# Copy both from the example site.
DEMO="$ROOT/examples/demo"
if [ ! -f "$DEMO/src/css/site.css" ] || [ ! -d "$DEMO/assets/image" ]; then
    echo "error: $DEMO sources are incomplete — this checkout cannot scaffold a site." >&2
    exit 2
fi
mkdir -p "$SITE_ABS/src/css"
cp "$DEMO/src/css/site.css" "$SITE_ABS/src/css/site.css"
cp -R "$DEMO/assets/image" "$SITE_ABS/assets/image"

# Copy the footer body so it is obvious where to write your own lines. The engine
# reads its own copy when this one is absent, so deleting it is also fine.
if [ -f "$ROOT/server/templates/footer_content.html" ]; then
    cp "$ROOT/server/templates/footer_content.html" "$SITE_ABS/templates/footer_content.html"
fi

# site.json — the hand-written half of the configuration. Every field a person
# types lives here; the machine-measured font data lives in fonts.json, which this
# script deliberately does not create so the engine's measurements are inherited.

cat > "$CONFIG" <<'JSON'
{
  "_comment": "Hand-written site configuration; font measurements live in fonts.json and must not be mixed in here. Footer: a short credit goes in credit / credit_url (shown inline beside the project's attribution); anything needing layout (copyright, filing number, donation note) goes in templates/footer_content.html.",
  "password": "",
  "author": "Your Name",
  "site_name": "",
  "credit": "",
  "credit_url": "",
  "motto": "Replace this with your own motto",
  "home_motto": "Replace this with your own motto",
  "base_url": "",
  "esa_site_id": 0,
  "pinned_post_id": 0
}
JSON

cat > "$SITE_ABS/README.md" <<'MARKDOWN'
# My site

This is **your own content directory**, kept separate from the Silphuu engine. Upgrading the
engine means replacing the binary — this directory does not change.

## Layout

```
config/                 your configuration. Drop a copy of the engine's own file here to
                        override it.
config/site.json        hand-written configuration; the only file you have to edit
config/topic.json       your categories: the key is the posts/ subdirectory and the
                        /topic/ URL segment, "name" is what visitors read. Absent is
                        normal — the categories are then whatever posts/ directories exist.
config/nav_signatures.json the mood line the navbar shows for each page. Absent is normal —
                        the engine's neutral defaults are then inherited.
state/                  what the engine measures, records or counts — not yours to edit.
state/fonts.json        font measurements, written by the font workshop in the admin UI.
                        Missing is normal — the engine's measurements are then inherited.
posts/                  your posts (Markdown), one directory per category key
src/css/site.css        your stylesheet override, inlined last in every page's <head>
assets/image/avatar.*   your avatar: svg, jxl or avif, in that order of preference.
                        Absent, your slot falls back to the default avatar below.
assets/image/default-avatar.*  the one fallback image: shown for a commenter with no
                        avatar, on the error pages, and in your own slot when avatar.*
                        is absent
assets/image/background/  the hero background
assets/image/sponsor/   sponsor QR codes
static/image/post/      images uploaded from posts
static/image/comment/   images uploaded from comments
templates/              optional: a same-named file here replaces the engine's wholesale
```

## Running

```bash
cd <engine directory> && make run-site DIR=<this directory>
```

Or directly:

```bash
cd <engine directory>/server && SILPHUU_SOCK=/tmp/silphuu.sock ../bin/silphuu -dir <this directory>
```

## The one rule

**A file present here is used; a file that is absent falls back to the engine's copy.**
No field-level merging — what you write is what you get.

| To change | Where |
|---|---|
| Site name, motto, upload host | `config/site.json` |
| Your own credit, beside the project's attribution | `config/site.json` -> `credit` / `credit_url` |
| Categories: display name, URL segment, order | `config/topic.json` |
| Footer body: copyright, filing number, donation note | `templates/footer_content.html` |
| The navbar's mood line for a page | `config/nav_signatures.json` |
| Page copy (buttons, labels and the like) | copy the engine's `config/ui_strings.json` here |
| Layout | copy the engine's `templates/*.html` here |
| Styles, optical nudges | `src/css/site.css` |
| Avatar, default avatar, hero background, sponsor codes | `assets/image/` |
| Fonts | the font workshop in the admin UI — it writes `state/fonts.json` |

Restart after editing: the render cache invalidates itself.
MARKDOWN

cat <<EOF
Created site directory: $SITE_ABS

  config/site.json              <- edit this: site name, credit, motto
  templates/footer_content.html <- your footer body (copyright, filing number)
  src/css/site.css              <- your stylesheet (inlined last in <head>)
  assets/image/                 <- your avatar and default avatar (replace with your own)
  posts/                        <- drop your Markdown posts here
  README.md                     <- the rules

Next:
  1. edit $CONFIG
  2. run: cd $ROOT && make run-site DIR=$SITE_ABS
EOF
