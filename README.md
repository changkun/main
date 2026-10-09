# changkun.de

The main website and the API behind it, in one Go binary.

- `/` serves the embedded site in `static/`.
- `/ideas/*` accepts idea posts, polishes and translates them with an LLM,
  and commits the result as markdown to [changkun/blog](https://github.com/changkun/blog).

## Usage

```
$ docker network create traefik_proxy
$ make build && make up
```

`make build` runs the tests, builds a Linux binary, and produces the image.

The site needs no configuration. The ideas API does, and stays unmounted
without it, so a host with no `.env` still serves the site and answers
`/ideas/*` with 503.

## The ideas API

```
GET  /ideas/ping       Health check, no auth
POST /ideas/post       Submit an idea
POST /ideas/improve    Improve content without posting
```

`/ideas/post` accepts `{"title": "optional", "content": "...", "augmented": "optional"}`
and answers immediately; the work happens in the background. `/ideas/improve`
accepts `{"content": "..."}` and returns `{"ok": true, "content": "..."}`.

Posting an idea fetches any linked URLs, generates a title when one is
missing, detects the language, polishes and translates the text, picks a short
slug, augments it with a sourced deep dive, and commits bilingual markdown to
`content/ideas/`.

Model calls go through [Lux](https://github.com/latere-ai/lux), the model
gateway of the [latere platform](https://platform.latere.ai), using its own
dialect, so one request shape covers every model it routes. Augmentation asks
the provider to search and fetch pages itself, which is what makes the
citations point at sources that exist.

Two things follow from the dialect being the native one. `LLM_BASE_URL` is the
gateway root, `https://api.latere.ai/v1/models` by default: the calls go to
`POST /lux/v1/generate` under it, which does not exist under a
provider-compatible surface such as `/anthropic`. And a model name is resolved
exactly, with no prefix stripped and no alias looked up, so `LLM_MODEL` and
`LLM_TITLE_MODEL` must be names the platform's catalog serves as written.

`LLM_API_KEY` is a Lux Key created on the platform. Lux takes Keys only on its
model routes, never a person's login token, so the key is the service's own
credential. Scope it to the two models and give it a budget.

Every instruction sent to a model lives in `prompts/` as a Go text template,
compiled into the binary with `go:embed`. A deployed server carries its own
prompts and cannot drift from the ones it was built with.

Every route except `/ideas/ping` requires an `Authorization: Bearer <token>`
header carrying an access token from [latere auth](https://auth.latere.ai),
which the compose box on changkun.de obtains through browser PKCE. The token
is verified against the issuer's JWKS and then checked against two lists: the
client it was minted for must be in `AUTH_ALLOWED_CLIENTS`, and the person in
`AUTH_ALLOWED_PRINCIPALS`. A valid token proves identity, not posting rights,
and a login token is addressed to the issuer, so without the client check a
token handed to any other latere app would post here too.

## Configuration

Copy `.env.template` to `.env`. `LLM_API_KEY` and `GIT_TOKEN` are required to
mount the API; everything else has a default.

| Variable | Default | Description |
|---|---|---|
| `MAIN_ADDR` | `0.0.0.0:80` | Listen address |
| `LLM_BASE_URL` | `https://api.latere.ai/v1/models` | Lux gateway root |
| `LLM_API_KEY` | — | Lux Key from the latere platform |
| `LLM_MODEL` | `anthropic/claude-opus-5.5` | Augmentation and translation, as the gateway's catalog names it |
| `LLM_TITLE_MODEL` | `anthropic/claude-haiku-5.5` | Title, slug, and polish, as the gateway's catalog names it |
| `GIT_TOKEN` | — | GitHub personal access token |
| `GIT_REPO` | `changkun/blog` | Target repository, `owner/repo` |
| `GIT_COMMITTER_NAME` | `Changkun Ideas API Server` | Commit author name |
| `GIT_COMMITTER_EMAIL` | `hi+ideas@changkun.de` | Commit author email |
| `AUTH_ALLOWED_PRINCIPALS` | — | Comma-separated emails or principal ids allowed to post. Empty rejects every token |
| `AUTH_ALLOWED_CLIENTS` | `changkun-blog` | Comma-separated OAuth client ids whose tokens are accepted |
| `AUTH_URL` | `https://auth.latere.ai` | latere auth issuer |
| `AUTH_JWKS_URL` | `$AUTH_URL/.well-known/jwks.json` | JWKS used to verify tokens |

## License

MIT | Copyright &copy; 2016 - 2025 [Changkun Ou](https://changkun.de)
