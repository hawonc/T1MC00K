# T1MC00K

A small recipe storage backend. Recipes are written in
[CookLang](https://cooklang.org) (`.cook` files), parsed on upload, and stored
in SQLite with a full-text search index over titles, ingredients and method
text. A single static page (`web/index.html`, embedded into the binary) is
enough to drive it.

## Run

```sh
go run .                # http://localhost:8090
```

Configuration via flags or environment:

| Flag    | Env            | Default       |
|---------|----------------|---------------|
| `-addr` | `T1MC00K_ADDR` | `:8090`       |
| `-db`   | `T1MC00K_DB`   | `recipes.db`  |
| `-base` | `T1MC00K_BASE` | `""` (root)   |

`-base` mounts the app under a URL prefix (e.g. `/recipes`) if you ever put
it behind a reverse proxy; the frontend uses relative URLs so it works
under any mount point.

Open http://localhost:8090 to upload `.cook` files, browse and search.

## HTTP API

> **Note:** the upload route (`POST /api/recipes`) is currently commented
> out in `internal/api/server.go` until authentication is added. The curl
> examples below will return `405` until it is re-enabled.

| Method   | Path              | Description                                        |
|----------|-------------------|----------------------------------------------------|
| `GET`    | `/health`         | Liveness check                                     |
| `POST`   | `/api/recipes`    | Upload: multipart (`file` fields, `.cook`) or raw body |
| `GET`    | `/api/recipes`    | List all recipes (id, title, tags)                 |
| `GET`    | `/api/recipes/{id}` | Full recipe incl. ingredients, steps, raw source |
| `DELETE` | `/api/recipes/{id}` | Delete recipe                                   |
| `GET`    | `/api/search?q=`  | Full-text search (title + ingredients + steps)      |

Examples:

```sh
# upload one or more files
curl -X POST localhost:8090/api/recipes \
  -F "file=@samples/pad-thai.cook" -F "file=@samples/carbonara.cook"

# or pipe raw .cook source
curl -X POST --data-binary @my-recipe.cook localhost:8090/api/recipes

# search
curl "localhost:8090/api/search?q=chicken+coconut"
```

## What gets stored

From each `.cook` file:

- `>> title:` → recipe title (falls back to the filename, then the first step)
- `>> tags:` → cuisine/style tags (also normalized into `tags`/`recipe_tags`
  tables for future tag filtering)
- `@ingredient{amount%unit}(note)` lines at the top → structured ingredient list
- everything after → method steps
- the original source text is kept verbatim, so nothing is lost

Search uses SQLite FTS5 (`porter unicode61` tokenizer), so `noodle` matches
`noodles`, multi-word queries are ANDed, and results are ranked by relevance.

## Layout

```
cook.go                       main: flags, embed, graceful shutdown
internal/parser/cooklang.go   .cook parsing (metadata, ingredients, steps)
internal/store/store.go       SQLite schema, CRUD, FTS5 index & search
internal/api/server.go        HTTP handlers
web/index.html                the entire frontend
samples/*.cook                example recipes
```
