package main

import (
	"log"
	"net/http"
	"os"

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
	log.Fatal(http.ListenAndServe(addr, handler))
}
