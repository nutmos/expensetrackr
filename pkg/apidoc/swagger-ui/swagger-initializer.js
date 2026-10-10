// Swagger UI configuration for expense-service (ours, not part of the
// vendored swagger-ui-dist files). Loads the spec embedded in the binary.
window.addEventListener("load", function () {
  window.ui = SwaggerUIBundle({
    url: "/api/openapi.yaml",
    dom_id: "#swagger-ui",
    deepLinking: true,
    presets: [SwaggerUIBundle.presets.apis],
    layout: "BaseLayout",
    // Offline: never send the spec to the public validator.swagger.io badge.
    validatorUrl: null,
    docExpansion: "list",
    defaultModelsExpandDepth: 0,
    displayOperationId: false,
    displayRequestDuration: true,
    filter: true,
    showExtensions: false,
    tryItOutEnabled: false,
  });
});
