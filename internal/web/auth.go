package web

// ログイン・ログアウトと、ログインを求める処理。
//
// 保護の範囲：/login と /static/ 以外のすべて。未ログインなら /login へ送る（元のアドレスは next に入れる）。
// CSRF は、標準の CrossOriginProtection（Origin / Sec-Fetch-Site）と、Cookie の SameSite=Lax で防ぐ。トークンは付けない。
// 設計は rooms/staylink/docs/13-login-design.md。

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/session"
	"github.com/katsuhiro1215/StayLink/internal/user"
)

const (
	sessionCookie = "staylink_session"

	// ログインの入力の上限。これを超える入力は、ハッシュ計算をせずに、違うものとして扱う。
	maxLoginBody     = 4 << 10 // リクエストボディ（4KiB）
	maxUsernameBytes = 64
	maxPasswordBytes = 1024
)

// Authenticator は、ユーザー名とパスワードの照合（user.Store が満たす）。
type Authenticator interface {
	Authenticate(ctx context.Context, username, password string) (user.User, error)
}

// SessionStore は、セッションの保存先（session.Store が満たす）。
type SessionStore interface {
	Create(ctx context.Context, userID int64, now time.Time) (string, error)
	Lookup(ctx context.Context, token string, now time.Time) (user.User, error)
	Delete(ctx context.Context, token string) error
}

// authConfig は、ログインに必要なもの。nil なら、ログインを求めない（テスト用。NewHandler は必ず付ける）。
type authConfig struct {
	users        Authenticator
	sessions     SessionStore
	secureCookie bool // HTTPS のときだけ送る印（Secure）を、常に付ける
	limiter      *limiter
	// slots は、同時に走らせるパスワードの照合の数の上限。照合は重い（CPUを使う）ので、
	// 大量の同時要求で、サーバーが他の処理をできなくなるのを防ぐ。満杯なら、待たせずに 503 を返す。
	slots chan struct{}
}

// maxConcurrentLogins は、同時に照合できる数。
const maxConcurrentLogins = 4

// option は newHandler の設定。
type option func(*server)

// withAuth は、ログインを求めるようにする。secureCookie は、HTTPS で使うときに true（r.TLS があるときは、常に Secure を付ける）。
func withAuth(users Authenticator, sessions SessionStore, secureCookie bool) option {
	return func(s *server) {
		s.auth = &authConfig{
			users: users, sessions: sessions, secureCookie: secureCookie,
			limiter: newLimiter(), slots: make(chan struct{}, maxConcurrentLogins),
		}
	}
}

type loginForm struct {
	Username string
	Next     string
	Error    string
}

// safeNext は、ログイン後に戻る先。自分のサイトのパス（/ で始まる）だけを許す（オープンリダイレクトの防止）。
func safeNext(next string) string {
	if next == "" || next[0] != '/' || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil {
		return "/"
	}
	// ログイン画面・ログアウト（POST専用）・静的ファイルへは戻さない（戻ると、404 や 405 の画面になる）。
	if u.Path == "/login" || strings.HasPrefix(u.Path, "/login/") || u.Path == "/logout" || strings.HasPrefix(u.Path, "/static/") {
		return "/"
	}
	return next
}

// currentUser は、Cookie のセッションから、ログイン中のユーザーを返す。ok=false は、未ログイン。
// DB の障害などは err で返す（未ログインとは区別する）。
func (s *server) currentUser(r *http.Request) (u user.User, ok bool, err error) {
	c, cerr := r.Cookie(sessionCookie)
	if cerr != nil {
		return user.User{}, false, nil
	}
	u, err = s.auth.sessions.Lookup(r.Context(), c.Value, s.now())
	switch {
	case errors.Is(err, session.ErrNoSession):
		return user.User{}, false, nil
	case err != nil:
		return user.User{}, false, err
	}
	return u, true, nil
}

// requireLogin は、/login と /static/ 以外を、ログイン中の人だけに通す。
func (s *server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		_, ok, err := s.currentUser(r)
		if err != nil {
			s.serverError(w, "セッションの確認に失敗しました", err)
			return
		}
		if !ok {
			target := "/login"
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true, // JavaScript から読めない
		Secure:   s.auth.secureCookie || r.TLS != nil,
		SameSite: http.SameSiteLaxMode, // 他のサイトからの送信には付かない
	})
}

func (s *server) renderLogin(w http.ResponseWriter, status int, form loginForm) {
	s.pages.render(w, status, "login", pageData{Title: "ログイン", Active: "login", Data: form})
}

// loginPage は GET /login。ログイン済みなら、戻り先へ送る。
func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	_, ok, err := s.currentUser(r)
	if err != nil {
		s.serverError(w, "セッションの確認に失敗しました", err)
		return
	}
	if ok {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.renderLogin(w, http.StatusOK, loginForm{Next: next})
}

const (
	msgInvalidCredentials = "ユーザー名かパスワードが違います。"
	msgLocked             = "ログインに失敗した回数が多いため、しばらく使えません。時間をおいて、もう一度お試しください。"
)

// login は POST /login。
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "送信された内容が大きすぎます", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "送信された内容を読み取れません", http.StatusBadRequest)
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	next := safeNext(r.PostForm.Get("next"))
	form := loginForm{Username: username, Next: next}
	now := s.now()
	ip := clientKey(r)

	// 試行を、照合の「前」に数える（照合は重いので、後で数えると、同時に届いた要求がすべてすり抜ける）。
	// ロック中は、正しいパスワードでも拒否する。照合（重い計算）もしない。
	if ok, retryAfter := s.auth.limiter.Reserve(ip, now); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		form.Error = msgLocked
		s.renderLogin(w, http.StatusTooManyRequests, form)
		return
	}

	// 長すぎる入力は、ハッシュ計算をせずに、違うものとして扱う（試行には、すでに数えてある）。
	var u user.User
	var err error
	if len(username) > maxUsernameBytes || len(password) > maxPasswordBytes {
		err = user.ErrInvalidCredentials
	} else {
		// 同時に照合できる数には上限がある。満杯なら、待たせずに 503（CPUを使い切られないため）。
		select {
		case s.auth.slots <- struct{}{}:
			u, err = s.auth.users.Authenticate(r.Context(), username, password)
			<-s.auth.slots
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "混み合っています。少し待ってから、もう一度お試しください。", http.StatusServiceUnavailable)
			return
		}
	}
	switch {
	case errors.Is(err, user.ErrInvalidCredentials):
		form.Error = msgInvalidCredentials // 理由は区別しない（ユーザーの有無を教えない）。パスワードは入れ直してもらう
		s.renderLogin(w, http.StatusUnauthorized, form)
		return
	case err != nil:
		s.serverError(w, "ログインの照合に失敗しました", err)
		return
	}

	s.auth.limiter.Reset(ip)
	// 以前のセッションがあれば消し、毎回、新しい値を発行する（セッション固定化攻撃の対策）。
	if c, cerr := r.Cookie(sessionCookie); cerr == nil {
		if derr := s.auth.sessions.Delete(r.Context(), c.Value); derr != nil {
			s.serverError(w, "以前のセッションの削除に失敗しました", derr)
			return
		}
	}
	token, err := s.auth.sessions.Create(r.Context(), u.ID, now)
	if err != nil {
		s.serverError(w, "セッションの作成に失敗しました", err)
		return
	}
	s.setSessionCookie(w, r, token, int(session.MaxAge.Seconds()))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// logout は POST /logout。セッションを消して、Cookie を失効させる。
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if derr := s.auth.sessions.Delete(r.Context(), c.Value); derr != nil {
			s.serverError(w, "セッションの削除に失敗しました", derr)
			return
		}
	}
	s.setSessionCookie(w, r, "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
