# Vendored Swagger UI

These are static assets from the npm package **`swagger-ui-dist` 5.33.1**,
copied byte for byte (https://www.npmjs.com/package/swagger-ui-dist, upstream
https://github.com/swagger-api/swagger-ui). License: **Apache-2.0**. See
`LICENSE` and `NOTICE` in this directory.

- Tarball: https://registry.npmjs.org/swagger-ui-dist/-/swagger-ui-dist-5.33.1.tgz
- npm integrity: `sha512-H872wWkA53bFIsGgi7OWgmq+CRWw3nFQGdJWRqOB9wNwTm6e5ol34+qPDkV4AJlK+gglM2EsJiOOzsGsEGbluA==`

| File | Origin |
|------|--------|
| `swagger-ui.css` | upstream, unmodified |
| `swagger-ui-bundle.js` | upstream, unmodified (its source-map comment points to a `.map` that is not vendored) |
| `favicon-16x16.png`, `favicon-32x32.png` | upstream, unmodified |
| `LICENSE`, `NOTICE` | upstream, unmodified |
| `index.html`, `swagger-initializer.js` | **ours**: load `/api/openapi.yaml` with `BaseLayout`; no standalone preset, no top bar |

Everything here is embedded into the binary (`pkg/apidoc`) and served under
`/swagger/`. No CDN is involved, and `validatorUrl` is turned off, so the docs
work offline.

To upgrade, download the new tarball, replace the upstream files above, and
update the version in this file, in `pkg/apidoc/apidoc.go` (`SwaggerUIVersion`)
and in the README.
