package authentication

import "github.com/gorilla/sessions"

const SESSION_NAME = "session-name"

var store = sessions.NewCookieStore([]byte("super-secret-32-byte-key-auth-v1"))
