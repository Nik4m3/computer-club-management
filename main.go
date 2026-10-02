package main

import (
	"context"
	api2 "github.com/Nik4m3/computer-club-management/api"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
	"os"
)

func mustEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	dsn := mustEnv("DB_DSN", "postgres://computer_club:computer_club@localhost:5432/computer_club?sslmode=disable")
	addr := mustEnv("HTTP_ADDR", ":8080")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	api := api2.NewAPI(pool)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	r.Mount("/api", api.Routes())

	r.Handle("/*", http.FileServer(http.Dir("./static")))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./static/index.html")
	})

	log.Printf("listen %s", addr)
	log.Fatal(http.ListenAndServe(addr, r))
}
