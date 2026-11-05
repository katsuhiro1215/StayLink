package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/katsuhiro1215/StayLink/internal/web"
)

func main() {
	handler, err := web.NewHandler()
	if err != nil {
		log.Fatalf("画面の準備に失敗しました: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	addr := ":" + port
	log.Printf("起動します http://localhost%s", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second, // 接続だけ張って送らない相手を切る
	}
	log.Fatal(srv.ListenAndServe())
}
