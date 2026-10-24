package api

import (
	"embed"
	"net/http"
)

//go:embed web
var web embed.FS

// page serves one embedded file of the browser player.
func page(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := web.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("Cache-Control", "no-cache")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; media-src 'self' blob:; frame-ancestors 'none'")
		w.Write(b)
	}
}
