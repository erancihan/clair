package auth

import (
	"net/http"

	"github.com/erancihan/go-otp"
	"github.com/gin-gonic/gin"
)

var (
	Issuer string = "erancihan"
	Window int    = 0
)

func TFAGetQR(ctx *gin.Context) {
	secret, err := otp.NewSecret()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"message": err})
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
	}

	// TODO: store SECRET

	ctx.JSON(http.StatusOK, gin.H{"qr": qr})
}

func TFAValidate(ctx *gin.Context) {
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

	// TODO: mark account's registration complete
	// TODO: generate JWT token

	if ok {
		ctx.JSON(http.StatusOK, gin.H{"token": ""})
	} else {
		ctx.JSON(http.StatusUnauthorized, gin.H{"message": "Bad Token"})
	}
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
