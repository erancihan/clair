package authentication

import (
	"encoding/json"
	"net/http"

	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type RegisterPayload struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func AuthRegister(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// registered as POST only

		var creds RegisterPayload
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			http.Error(w, "Invalid request payload", http.StatusBadRequest)
			return
		}

		hashedPass, err := bcrypt.GenerateFromPassword([]byte(creds.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}

		user := models.User{
			Username: creds.Username,
			Email:    creds.Email,
			Password: string(hashedPass),
		}

		tx := ctx.DBConn.Session(&gorm.Session{Context: r.Context()})

		result := tx.Create(&user)
		if result.Error != nil {
			// Check for duplicate username error
			// Note: Error handling varies slightly by DB driver, generic check here:
			ctx.Logger.Error("db error", zap.Error(result.Error))
			http.Error(w, "Username likely taken or database error", http.StatusConflict)
			return
		}

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("User registered successfully"))
	}
}
