package bootstrap

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"bgin/internal/shared/apidocs"
)

// 文档页面与 OpenAPI 规范路由路径常量。
const (
	// docsPagePath 客户端 Scalar 文档页面路径。
	docsPagePath = "/docs"
	// referenceSpecPath 客户端 OpenAPI 规范路径。
	referenceSpecPath = docsPagePath + "/openapi.json"

	// adminDocsPagePath 是管理端 Scalar 文档页的路径。
	adminDocsPagePath = "/docs/admin"
	// adminReferenceSpecPath 是浏览器拉取管理端规范的路径。
	adminReferenceSpecPath = adminDocsPagePath + "/openapi.json"
)

var (
	clientReferencePage = []byte(buildReferencePage("bgin API", referenceSpecPath))
	adminReferencePage  = []byte(buildReferencePage("bgin Admin API", adminReferenceSpecPath))
)

// buildReferencePage 构建 Scalar API Reference 的宿主 HTML 页。
func buildReferencePage(title, specPath string) string {
	return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>` + title + `</title>
  </head>
  <body>
    <div id="app"></div>
    <script type="module">
      import { createApiReference } from 'https://cdn.jsdelivr.net/npm/@scalar/api-reference/esm.js'

      const spec = await fetch('` + specPath + `').then(r => r.json()).catch(() => ({}))

      const SCALAR_UNIFIED_CONFIG = {
        layout: 'modern',
        theme: 'purple',
        withDefaultFonts: true,
        hideDarkModeToggle: false,
        hideModels: true,

        // Layout & title settings
        operationTitleSource: 'summary',
        showOperationId: false,
        defaultOpenFirstTag: true,
        defaultOpenAllTags: false,
        orderRequiredPropertiesFirst: true,
        orderSchemaPropertiesBy: 'preserve',
        expandAllModelSections: false,
        expandAllResponses: false,
        expandAllSchemaProperties: false,

        // Developer experience
        persistAuth: true,
        hideTestRequestButton: false,
        hideClientButton: false,
        hideSearch: false,
        showDeveloperTools: 'localhost',
        showToolbar: 'localhost',
        documentDownloadType: 'both',

        // Privacy & security
        telemetry: false,
        isEditable: false,
        _integration: 'nestjs',

        // Introduction section full-width layout & custom styles
        customCss: [
          '.introduction-section .section-columns {',
          '  flex-direction: column !important;',
          '  gap: 32px !important;',
          '}',
          '.introduction-section .section-column {',
          '  max-width: 100% !important;',
          '  width: 100% !important;',
          '}',
          '.introduction-section .sticky-cards {',
          '  position: static !important;',
          '  top: auto !important;',
          '  max-width: 100% !important;',
          '  width: 100% !important;',
          '}',
          '.introduction-section .introduction-card {',
          '  display: grid !important;',
          '  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)) !important;',
          '  gap: 16px !important;',
          '}',
          '.introduction-section .introduction-card .scalar-reference-intro-clients {',
          '  grid-column: 1 / -1 !important;',
          '}',
          '',
          '/* Hide sidebar top Ask AI button */',
          'div[class*="gap-1.5"][class*="px-3"] > button:not(:first-of-type),',
          '.flex.gap-1\\.5.px-3.pt-3 > button:not(:first-of-type) {',
          '  display: none !important;',
          '}',
          '',
          '/* Hide sidebar bottom Powered by Scalar attribution link */',
          'a[href="https://www.scalar.com"],',
          'a[href="https://www.scalar.com/"],',
          '.scalar-version-number {',
          '  display: none !important;',
          '}',
        ].join('\n'),
      }

      createApiReference('#app', {
        ...SCALAR_UNIFIED_CONFIG,
        url: '` + specPath + `',
        content: spec,
        tagsSorter: (a, b) => {
          const getTagOrder = (tag) => {
            if (tag?.['x-order']) return Number(tag['x-order'])
            const found = spec?.tags?.find(t => t.name === tag?.name)
            if (found?.['x-order']) return Number(found['x-order'])
            return 999
          }
          const orderA = getTagOrder(a)
          const orderB = getTagOrder(b)
          if (orderA !== orderB) return orderA - orderB
          return (a.name || '').localeCompare(b.name || '')
        },
        operationsSorter: (a, b) => {
          const getOrder = (item) => {
            const p = item.path || ''
            const m = (item.method || item.httpVerb || '').toLowerCase()
            return Number(spec?.paths?.[p]?.[m]?.['x-order'] ?? 999)
          }
          const orderA = getOrder(a)
          const orderB = getOrder(b)
          if (orderA !== orderB) return orderA - orderB
          return (a.path || '').localeCompare(b.path || '')
        },
      })
    </script>
  </body>
</html>`
}

// registerDocs 挂载 API 文档，仅在 ENABLE_DOCS 开启时调用。
func registerDocs(engine *gin.Engine) {
	engine.GET(docsPagePath, serveClientReference)
	engine.GET(referenceSpecPath, serveClientReferenceSpec)

	engine.GET(adminDocsPagePath, serveAdminReference)
	engine.GET(adminReferenceSpecPath, serveAdminReferenceSpec)
}

// serveClientReference 返回客户端 Scalar 的宿主页。
func serveClientReference(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", clientReferencePage)
}

// serveClientReferenceSpec 原样返回编进二进制的客户端规范。
func serveClientReferenceSpec(c *gin.Context) {
	c.Data(http.StatusOK, "application/json; charset=utf-8", apidocs.Spec)
}

// serveAdminReference 返回管理端 Scalar 的宿主页。
func serveAdminReference(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", adminReferencePage)
}

// serveAdminReferenceSpec 原样返回编进二进制的管理端规范。
func serveAdminReferenceSpec(c *gin.Context) {
	c.Data(http.StatusOK, "application/json; charset=utf-8", apidocs.AdminSpec)
}
