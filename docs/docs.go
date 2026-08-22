// Package docs embeds the OpenAPI spec so the API can serve it directly
// without depending on the working directory at runtime. openapi.yaml is
// the source of truth for the machine-readable contract; API.md is the
// prose companion (rationale, "why", things a spec can't express).
package docs

import _ "embed"

//go:embed openapi.yaml
var OpenAPISpec []byte
