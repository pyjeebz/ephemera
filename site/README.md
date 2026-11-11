# site

The ephemera.run landing page. Next.js (App Router) with Tailwind CSS and
shadcn/ui; the page builds to static HTML.

```sh
npm install
npm run dev     # http://localhost:3000
npm run build   # static output in .next
```

## Where things are

| Path | What it is |
| --- | --- |
| `app/page.tsx` | The page, section by section |
| `app/globals.css` | shadcn's theme tokens pointed at the site palette (dark, `--radius: 0`), then the site's own styles |
| `components/site/` | The sections: header, hero, features, use cases, quickstart, comparison, FAQ, footer |
| `components/ui/` | shadcn components: Button, Badge, Tooltip, Accordion |
| `lib/engine/sand.js` | The falling-sand simulation: the hero, the four use-case boxes and the footer wordmark |
| `lib/engine/iso.js` | The isometric line drawings in the features grid |

Both engines are plain modules that find their canvases and svgs by selector.
`components/site/effects.tsx` mounts them after the page loads and stops
them on unmount.

## The sand

Each box is a grid of cells. A grain falls if it can, otherwise slides
diagonally down to a random side, otherwise stays put, with rows processed
bottom up in a random direction.

| Where | What it shows |
| --- | --- |
| Hero | The section is a sandbox. Sand rains behind the copy and settles on the headline's letters (drawn to a hidden canvas as a solid mask), the buttons and the install line; then the floor opens, the ledges let go center out, and it drains. Nothing leaves the hero. |
| Use-case boxes | Each acts out its card: five agent lanes that fill and drain on their own, one burst per passing test, untrusted grains that hit the wall and fall back, and CI jobs that always start empty. |
| Footer | The ephemera wordmark is its own sandbox, and a mold: outside the letters is solid, so sand fills each letter from the bottom up and the counters stay open. It's full when you arrive, holds, drains out through the bottom, sits empty, then a light pour fills it again. |

The isometric drawings draw their lines in on first sight (strokes first,
fills after), then loop while on screen; every frame is a pure function of
time. Everything animates only while on screen, and `prefers-reduced-motion`
gets a single settled frame instead.

Brand marks come from Simple Icons (CC0). Satoshi is self-hosted from
Fontshare (ITF Free Font License).
