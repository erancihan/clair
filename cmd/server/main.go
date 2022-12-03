package main

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	router := gin.Default()
	router.GET("/", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"message": "Hi!"})
	})

	hooks := router.Group("/hooks")
	{
		hooks.GET("/", func(ctx *gin.Context) {})
		hooks.POST("/", func(ctx *gin.Context) {})

		hooks.POST("/:id", func(ctx *gin.Context) {
			hookId := ctx.Param("id")

			fmt.Println(hookId)

			ctx.JSON(http.StatusOK, gin.H{"message": "Ok"})
		})
		hooks.PUT("/:id", func(ctx *gin.Context) {})
		hooks.DELETE("/:id", func(ctx *gin.Context) {})
	}

	router.Run()
}
