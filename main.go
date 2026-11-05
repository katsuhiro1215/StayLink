package main

import (
	"context"
	"database/sql"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql 用の PostgreSQL ドライバ

	"github.com/katsuhiro1215/StayLink/internal/reservation"
	"github.com/katsuhiro1215/StayLink/internal/room"
	"github.com/katsuhiro1215/StayLink/internal/session"
	"github.com/katsuhiro1215/StayLink/internal/user"
	"github.com/katsuhiro1215/StayLink/internal/web"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("環境変数 DATABASE_URL が未設定です。.env.example を .env にコピーして、" +
			"`set -a; . ./.env; set +a` で読み込んでください（README 参照）")
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		log.Fatalf("DB の設定が不正です: %v", err)
	}
	defer db.Close()

	// sql.Open は接続しないので、Ping で実際につながるかを起動時に確かめる。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("DB に接続できません（docker compose up -d --wait は実行しましたか）: %v", err)
	}

	// ユーザーがいないと、誰もログインできない。気づけるように、起動時に知らせる。
	var users int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		log.Fatalf("users 表を読めません（scripts/migrate.sh は実行しましたか）: %v", err)
	}
	if users == 0 {
		log.Print("注意：ユーザーがいないため、ログインできません。README の「管理者の作成」を参照して、go run ./cmd/adduser を実行してください")
	}

	// HTTPS で使うとき（前に置いたサーバーが TLS を終端するときを含む）は COOKIE_SECURE=1。
	secureCookie := os.Getenv("COOKIE_SECURE") == "1"
	handler, err := web.NewHandler(room.NewStore(db), reservation.NewStore(db), user.NewStore(db), session.NewStore(db), secureCookie)
	if err != nil {
		log.Fatalf("画面の準備に失敗しました: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	// 認証がないので、既定は自分のPCからだけ接続できる 127.0.0.1。
	// LAN に公開したいときだけ HOST=0.0.0.0 を指定する（認証を付けるまでは非推奨）。
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, port)
	log.Printf("起動します http://%s", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second, // 接続だけ張って送らない相手を切る
	}
	log.Fatal(srv.ListenAndServe())
}
