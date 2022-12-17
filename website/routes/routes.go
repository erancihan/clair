package routes

import (
	"clair/website/controllers"
	"clair/website/controllers/auth"
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

func RegisterTo(router *gin.Engine, pagesFS embed.FS) {
	router.GET("/", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"message": "Hi!"})
	})

	router.GET("/ping", controllers.Ping)

	// register:
	// - post email to get QR for 2FA
	// - validate 2FA
	router.POST("/auth/tfa/qr", auth.TFAGetQR)
	router.POST("/auth/tfa/validate", auth.TFAValidate)

	// login:
	// - POST email & token
	router.POST("/auth/login", auth.Login)

	router.GET("/site/*path", func(ctx *gin.Context) {
		controllers.Web(ctx, pagesFS)
	})
}
