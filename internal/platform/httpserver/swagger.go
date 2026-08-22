package httpserver

import (
	"net/http"

	"q-wash-api/docs"
)

// openapiSpecHandler serves the embedded OpenAPI document verbatim.
func openapiSpecHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(docs.OpenAPISpec)
}

// swaggerUIHandler serves a minimal Swagger UI page that loads the spec
// from /openapi.yaml. The swagger-ui-dist bundle itself is pulled from a
// CDN (not vendored) to avoid embedding several MB of JS/CSS for a docs
// page; "Try it out" requests still go same-origin to this API.
func swaggerUIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerUIPage))
}

const swaggerUIPage = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Q-Wash API — Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>body { margin: 0; }</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: '/openapi.yaml',
        dom_id: '#swagger-ui',
        presets: [SwaggerUIBundle.presets.apis],
        layout: 'BaseLayout',
      });
    };
  </script>
</body>
</html>
`
