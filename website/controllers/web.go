package controllers

import (
	"embed"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func Web(ctx *gin.Context, files embed.FS) {
	path := ctx.Param("path")

	if path == "/" {
		path = "/index.html"
	}

	// fix file path
	path = "web-ui" + path

	file, err := files.ReadFile(path)
	if err != nil {
		ctx.AbortWithStatusJSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}

	var ContentType string
	switch {
	case strings.HasSuffix(path, ".js"):
		ContentType = "application/json"
	case strings.HasSuffix(path, ".css"):
		ContentType = "text/css"
	default:
		ContentType = "text/html"
	}

	ctx.Data(http.StatusOK, ContentType, file)
}
