// 认证中间件 — JWT 校验 + must_change_password 强制拦截
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// AuthMiddleware 返回 HTTP 中间件，校验 API 请求的 JWT 认证
//
// 规则：
//   - 公开路径（/api/login, /api/health, /, /assets/*）→ 放行
//   - /api/change-password → 需要有效 JWT，不检查 must_change_password
//   - 其他 /api/* → 需要有效 JWT 且 must_change_password == false
func AuthMiddleware(am *AuthManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// 公开路径直接放行
			if isPublicPath(path) {
				next.ServeHTTP(w, r)
				return
			}

			// 非 API 路径也放行（理论上到不了这里，但也别拦截）
			if !strings.HasPrefix(path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			// 提取 Bearer token
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			token := strings.TrimPrefix(authHeader, "Bearer ")

			username, mustChangePwd, err := am.VerifyToken(token)
			if err != nil {
				msg := "unauthorized"
				if strings.Contains(err.Error(), "过期") {
					msg = "token_expired"
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": msg})
				return
			}

			// 检查 token 是否已被吊销
			if am.IsRevoked(token) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}

			// /api/change-password 只需要有效 JWT，不检查 must_change_password
			if path == "/api/change-password" {
				ctx := context.WithValue(r.Context(), authCtxKey, username)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// 其他 /api/* 必须已经改过密码
			if mustChangePwd {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "must_change_password"})
				return
			}

			ctx := context.WithValue(r.Context(), authCtxKey, username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func isPublicPath(path string) bool {
	if path == "/api/login" || path == "/api/health" {
		return true
	}
	if path == "/" || strings.HasPrefix(path, "/assets/") {
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
