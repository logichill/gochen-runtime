package main

import (
	"context"
	"log"

	"gochen-runtime/quick"
	"gochen/httpx"
)

func main() {
	app := quick.New()
	app.Use(func(c httpx.IContext, next func() error) error {
		log.Printf("request received")
		return next()
	})
	app.Modules(quick.Module("hello", routes))
	app.Group("/admin").Modules(quick.Module("hello-admin", routes))
	if err := app.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func routes(group httpx.IRouteGroup) error {
	group.GET("/ping", func(c httpx.IContext) error {
		return c.String(200, "pong")
	})
	return nil
}
