package auth

import (
	"clair/website/database"
	"clair/website/models"
	"net/http"

	"github.com/erancihan/go-otp"
	"github.com/gin-gonic/gin"
)

var (
	Issuer string = "erancihan.com"
	Window int    = 0
)

func TFAGetQR(ctx *gin.Context) {
	secret, err := otp.NewSecret()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"message": err})
		return
	}
	email := ctx.PostForm("email")

	tfa := otp.OTP{
		Issuer:  Issuer,
		Account: email,
		Secret:  secret,
		Window:  Window,
	}

	// create URI
	uri := tfa.CreateURI()
	// create QR
	qr, err := otp.NewQR(uri)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"message": err})
		return
	}

	// store secret
	// TODO: make it temporary
	// TODO: update secret if user with Email exists but not HasRegistered
	tx := database.Conn().Create(&models.User{
		Email:     email,
		TFASecret: secret,
	})
	if tx.Error != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"message": tx.Error})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"qr": qr})
}

func TFAValidate(ctx *gin.Context) {
	email := ctx.PostForm("email")
	token := ctx.PostForm("token")

	// get TFASecret for User with Email
	user := &models.User{
		Email: email,
	}

	tx := database.Conn().First(&user)
	if tx.Error != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"message": tx.Error})
		return
	}

	tfa := otp.OTP{
		Issuer:  Issuer,
		Account: user.Email,
		Secret:  user.TFASecret,
		Window:  Window,
	}

	ok, err := tfa.VerifyCode(token)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"message": err})
		return
	}

	if !ok {
		ctx.JSON(http.StatusUnauthorized, gin.H{"message": "Bad Token"})
		return
	}

	// mark account's registration complete
	user.HasRegistered = true
	tx = database.Conn().Save(user)
	if tx.Error != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"message": tx.Error})
		return
	}

	// TODO: generate JWT token

	ctx.JSON(http.StatusOK, gin.H{"jwt": ""})
}

func Login(ctx *gin.Context) {
	email := ctx.PostForm("email")
	token := ctx.PostForm("token")

	// TODO: get TFASecret for email
	secret := ""

	tfa := otp.OTP{
		Issuer:  Issuer,
		Account: email,
		Secret:  secret,
		Window:  Window,
	}

	ok, err := tfa.VerifyCode(token)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"message": err})
	}

	// TODO: generate JWT token

	if ok {
		ctx.JSON(http.StatusOK, gin.H{"token": ""})
	} else {
		ctx.JSON(http.StatusUnauthorized, gin.H{"message": "Bad Token"})
	}
}
