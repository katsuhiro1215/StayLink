// adduser は、ログインする管理者を1人作る。画面に登録の機能は作らないので、ここで作る。
//
// 使い方（パスワードは標準入力から1行。コマンドの引数や履歴に残さないため）：
//
//	set -a; . ./.env; set +a
//	printf '%s\n' 'ここにパスワード' | go run ./cmd/adduser -username admin
//
// 標準ライブラリには、入力を画面に出さずに読む機能がないので、標準入力で渡す。
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/katsuhiro1215/StayLink/internal/user"
)

func main() {
	username := flag.String("username", "", "ユーザー名（英小文字・数字・ . _ - の3〜32文字）")
	flag.Parse()
	if err := run(*username, os.Stdin, os.Getenv("DATABASE_URL")); err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		os.Exit(1)
	}
}

func run(username string, in io.Reader, databaseURL string) error {
	if databaseURL == "" {
		return errors.New("環境変数 DATABASE_URL が未設定です（README 参照）")
	}
	if username == "" {
		return errors.New("-username を指定してください")
	}
	password, err := readPassword(in)
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("DB の設定が不正です: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u, err := user.NewStore(db).Create(ctx, username, password)
	if err != nil {
		return err
	}
	fmt.Printf("ユーザー %q を作りました（ID %d）\n", u.Username, u.ID)
	return nil
}

// readPassword は標準入力の1行目を、パスワードとして返す（末尾の改行だけ除く。空白は、パスワードの一部として残す）。
func readPassword(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", errors.New("標準入力からパスワードを読めませんでした（例：printf '%s\\n' 'パスワード' | go run ./cmd/adduser -username admin）")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
