# getkerf.dev

The Kerf website: one static page, plain HTML and CSS, no JavaScript and no build step. Published to GitHub
Pages by [.github/workflows/pages.yaml](../.github/workflows/pages.yaml) on every push to `main` that touches
`site/` (or by hand: Actions → pages → Run workflow). Brand rules: [docs/branding/brand.md](../docs/branding/brand.md#website).

| Path | What |
|---|---|
| `index.html`, `404.html`, `styles.css` | the page, the not-found page, the stylesheet (light and dark) |
| `assets/shots/` | screenshots, generated (see below) |
| `fonts/` | Geist and Geist Mono, latin, variable weight (OFL, `fonts/OFL.txt`), generated |
| `favicon.svg`, `favicon.ico`, `apple-touch-icon.png`, `icon-*.png`, `site.webmanifest` | icons (`.svg` and the manifest by hand, PNG and ICO generated) |
| `social-preview.png` | Open Graph and Twitter image, a copy of `docs/branding/social-preview.png` |
| `robots.txt`, `sitemap.xml`, `llms.txt`, `CNAME` | search engines, AI assistants, the custom domain |
| `tools/` | `render-assets.mjs` (generated files), `check-links.mjs` (link check); not published |

## Preview

```bash
python3 -m http.server 8000 -d site    # open http://localhost:8000
```

Paths are absolute (`/styles.css`), so serve `site/` as the root; opening the file directly won't load the CSS.

## Check

```bash
node site/tools/check-links.mjs                       # every local file, #fragment and repository link
npx --yes html-validate@11.18.0 site/index.html site/404.html
npx --yes lighthouse@13.6.0 http://localhost:8000/ --view
```

The workflow runs the first two before it deploys.

## Regenerate the screenshots, icons and fonts

After `make web-screenshots`, a logo change or a font update:

```bash
make web-install && node site/tools/render-assets.mjs
```

It uses the web UI's Playwright Chromium and `@fontsource-variable` packages; commit the results.

## Update the sitemap

Set `<lastmod>` in `sitemap.xml` when the page's content changes.
