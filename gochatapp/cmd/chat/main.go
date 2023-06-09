package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/joho/godotenv"
)

func init() {
	// Load the environment file .env
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Unable to Load the env file.", err)
	}
}

func main() {
	server := flag.String("server", "", "http,websocket")
	flag.Parse()

	if *server == "http" {
		fmt.Println("http server is starting on :8080")
		http.ListenAndServe(":8080", routes())
	} else if *server == "websocket" {
		fmt.Println("websocket server is starting on :8081")
		StartWebsocketServer()
	} else {
		fmt.Println("invalid server. Available server: http or websocket")
	}
}
