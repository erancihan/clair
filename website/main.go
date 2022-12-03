package main

import (
	"embed"
	"log"
	"os"

	migrations "mercury/website/migrations"
	routes "mercury/website/routes"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

//go:embed all:web-ui/*
var pagesFS embed.FS

func main() {
	db, err := gorm.Open(sqlite.Open(".opt/dev.db"), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}

	migrations.Run(db)

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
