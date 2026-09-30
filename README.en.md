English | [Chinese](./README.md)

# Silphuu

> A "Fake" Geo-Distributed Edge Stateful Mesh.

## Where the name comes from

**Silphuu = Silph + uu**

- **Silph** — from **Sylph**, the wind spirit of Western myth, which is where the *ling* (spirit)
  comes from. The spelling trades the *y* for an *i*, which looks and reads a little more modern.
- **uu** — "wuwu", which is also the pet name my friends have for me.

Together they make **Silphuu**, or Lingwuwu — a blog system named after me.

The wind spirit also carries what I hope for it: a blog should be like the wind — light, quick,
here and gone.

---

## Why it exists

I have wrestled with all kinds of blog systems and never once assembled the one I had in mind. I
really do not like writing front ends!! React, Vue and their kin in particular — my head is ready to
burst just looking at them. And then there is a pile of npm and Node.js things I had never even heard
of; once installed, the dependencies eat several hundred megabytes of my disk!? What was left, most of
all, was dread.

Then AI grew capable enough to take the front end off my hands. With that weight gone I could finally
follow my own stubbornness and write a blog engine of my own — and Silphuu was born.

---

## Fair warning ⚠

This project grew out of my own obsessions with performance, network traffic and disk size. It is
full of over-engineering and sledgehammers for flies.

JXL is not yet widespread in browsers, and I use it anyway, for an audience of one. I know
WebTransport is immature and htmx 4.0 is still in beta; that does not stop me from trying them.

So this is not a conventional blog system. Its architecture runs on "whatever I feel like" —
essentially, the author writing down whatever he dreamed of, however he pleased:

- **Event-driven.** What happens on the site turns into events, appended line by line to an
  `events.jsonl` log, and compacted into a single `snapshot.json`.
- **No database.** A database's performance is not extreme enough for me; nothing beats memory.
  Comments, likes, view counts — all read straight out of memory. The events *are* the data.
- **Stingy with bandwidth.** I cannot stand sending even one useless byte. WebTransport
  bidirectional streams for the sync, a custom wire format, and anything heartbeat-shaped cut —
  **zero bytes** of idle traffic when nothing changes.
- **Edge-native.** Built for a CDN from the start. Pages go static and the edge serves them straight
  out, with edge cache-busting built in. No centre node between the machines, no master and no
  slave. (Planting a flag: one day, mesh topology!)
- **No build chain.** No front-end framework and no bundling dependencies. Hand-written CSS and JS
  get bundled, thumbnails and font subsets get generated — the engine does all of it itself.

---

## Demo and documentation

- TODO
- Building and running: [BUILD.md](./docs/BUILD.md)

---

## Licence

This project splits its licence between the back end and the front end. Know this before forking or
deploying:

| Scope | Licence |
|---|---|
| **Backend** — Go (`server/*.go`) and Rust (`tools/`) | [**Apache-2.0**](./LICENSE-APACHE) OR [**MIT**](./LICENSE-MIT) |
| **Frontend** — UI, CSS, JS, templates (e.g. `server/src`, `server/templates`) | [**Business Source License 1.1**](./LICENSE-BUSL) |
