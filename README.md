# T1MC00K

A read-only recipe library. Recipes live as
[CookLang](https://cooklang.org) `.cook` files in a directory; on every
start the app syncs that directory into SQLite (new files are imported,
changed files re-parsed, vanished files removed) and serves a searchable,
nicely rendered library.

The frontend follows the Bear Blog way: every page is server-rendered
HTML with real URLs — no JavaScript, no external assets, one inline
stylesheet — and a strict Content-Security-Policy to prove it
(`default-src 'none'`). A read-only JSON API sits alongside for scripts.

## Run

```sh
go run .        # http://localhost:8090
```

Drop `.cook` files into `recipes/` (subdirectories are scanned too) —
they are picked up on start and re-synced daily without a restart.

Configuration via flags or environment:

| Flag       | Env              | Default      |
|------------|------------------|--------------|
| `-addr`    | `T1MC00K_ADDR`   | `:8090`      |
| `-db`      | `T1MC00K_DB`     | `recipes.db` |
| `-dir`     | `T1MC00K_DIR`    | `recipes`    |
| `-resync`  | `T1MC00K_RESYNC` | `24h` (0 = only on start) |
| `-base`    | `T1MC00K_BASE`   | `""` (root)  |

`-base` mounts the app under a URL prefix (e.g. `/recipes`) if you ever put
it behind a reverse proxy; the frontend uses relative URLs so it works
under any mount point.

## HTTP surface

Pages (HTML, no JavaScript required):

| Method | Path                | Description                                  |
|--------|---------------------|----------------------------------------------|
| `GET`  | `/`                 | All recipes; `/?q=terms` for full-text search |
| `GET`  | `/recipes/{id}`     | One recipe, fully rendered                   |

JSON API (read-only; the recipes directory is the source of truth):

| Method | Path                | Description                                       |
|--------|---------------------|---------------------------------------------------|
| `GET`  | `/health`           | Liveness check                                    |
| `GET`  | `/api/recipes`      | List all recipes (id, title, tags, created date)  |
| `GET`  | `/api/recipes/{id}` | Full recipe: ingredients, structured steps, notes, raw source |
| `GET`  | `/api/search?q=`    | Full-text search (title + ingredients + method + notes) |

```sh
curl "localhost:8090/api/search?q=chicken+coconut"
```

## Recipes directory

Each `.cook` file is one recipe:

- `>> title:` → recipe title (falls back to the filename)
- `>> tags:` → cuisine/style tags, shown as `#tag` links
- `>> time required:` / `>> prep time:` / `>> cook time:` → shown as a
  byline; `>> servings:` and `>> source:` round out the subtext
- `@ingredient{amount%unit}(note)` lines at the top → the ingredient list
- `== Section ==` headers, `> text` paragraphs, `~{25%minutes}` timers and
  `@ingredient{2%tbsp}` references render as readable prose
- `--` line comments and `[- -]` block comments become cook's notes,
  shown as blockquotes (and indexed for search)
- the original source text is always one click away

Search uses SQLite FTS5 (`porter unicode61` tokenizer), so `noodle`
matches `noodles`, multi-word queries are ANDed, and results are ranked by
relevance with a snippet showing what matched.

## Layout

```
cook.go                       main: flags, directory sync, shutdown
recipes/                      the .cook source files (source of truth)
internal/parser/              cooklang parsing (cooklang-rs semantics)
internal/store/               SQLite schema, sync, FTS5 search
internal/api/                 HTML pages (html/template) + JSON API
web/layout.html               page shell + Bear Blog CSS (embedded)
web/{home,recipe,error}.html  page templates (embedded)
```
