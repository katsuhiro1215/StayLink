package web

import "net/http"

// セキュリティ関連のレスポンスヘッダ。すべての応答（404・405・500・静的ファイルを含む）に付ける。
//
// Content-Security-Policy の中身：
//   - default-src 'none'：明示したもの以外は、何も読み込まない
//   - style-src 'self'：CSS は自分のサイトの /static/ だけ。インラインの style 属性・<style> は使えない（テンプレートは使っていない）
//   - img-src 'self'：画像も自分のサイトだけ（いまは画像を使っていない）
//   - form-action 'self'：フォームの送信先は自分のサイトだけ
//   - base-uri 'none'：<base> による相対リンクの書き換えを禁止
//   - frame-ancestors 'none'：他のページに埋め込ませない（クリックジャッキング対策）
//
// JavaScript は使っていないので、script-src は許可しない（default-src 'none' のまま）。
// 将来スクリプトを足すときは、ここに script-src 'self' を足す（インラインは避ける）。
const contentSecurityPolicy = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// securityHeaders は、全応答にセキュリティヘッダを付ける。
//
// 限界：HTTPS を使わない前提なので、Strict-Transport-Security は付けない（HTTP で付けても無視される。
// HTTPS で公開するなら、リバースプロキシ側で付ける）。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff") // Content-Type を勝手に推測させない
		h.Set("X-Frame-Options", "DENY")           // CSP の frame-ancestors を解さない古いブラウザ向け
		h.Set("Referrer-Policy", "no-referrer")    // 外部へのリンクで、画面のアドレスを送らない
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
