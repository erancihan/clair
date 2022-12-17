package main

import (
	"embed"
	"os"

	"clair/website/database"
	routes "clair/website/routes"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

//go:embed all:web-ui/*
var pagesFS embed.FS

func main() {
	database.New()

	router := gin.Default()
	router.RedirectTrailingSlash = true
	router.Use(gin.Recovery())
	router.Use(func() gin.HandlerFunc {
		config := cors.DefaultConfig()

		switch os.Getenv("APP_ENV") {
		case "production":
			config.AllowAllOrigins = false
			config.AllowOrigins = []string{}
		case "development":
			fallthrough
		default:
			config.AllowAllOrigins = true
		}

		return cors.New(config)
	}())

	routes.RegisterTo(router, pagesFS)

	router.Run()
}
