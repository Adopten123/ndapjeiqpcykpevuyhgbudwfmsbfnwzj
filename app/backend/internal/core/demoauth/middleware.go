package demoauth

import (
	"context"
	"net/http"
)

func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		u, ok, cookie := r.authenticate(req)
		if !ok {
			fail(w, 401, "authentication_required", "Войдите с действующим учебным ключом.")
			return
		}
		mutation := req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions
		origin := req.Header.Get("Origin")
		if origin != "" && !allowedOrigin(origin) || cookie && mutation && !allowedOrigin(origin) {
			fail(w, 403, "origin_forbidden", "Источник запроса не разрешён для учебного контура.")
			return
		}
		next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), contextKey{}, cloneUser(u))))
	})
}
