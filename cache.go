package main

import "net/http"

func noCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store") // remove cache ability of assets in client (if client accepts...)
		next.ServeHTTP(w, r)
	})
}
