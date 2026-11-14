package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"recycletrace/backend/internal/db"
)

type App struct {
	DB *db.DB
}

func New(d *db.DB) *App { return &App{DB: d} }

type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

var ErrUnauthorized = errors.New("unauthorized")
var ErrForbidden = errors.New("forbidden")

func (a *App) Login(ctx context.Context, username, password string) (string, User, error) {
	var u User
	var hash string
	err := a.DB.Pool.QueryRow(ctx,
		`select id, username, display_name, role, password_hash from rt.users where username=$1`,
		username).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", User{}, ErrUnauthorized
		}
		return "", User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", User{}, ErrUnauthorized
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", User{}, err
	}
	token := hex.EncodeToString(buf)
	_, err = a.DB.Pool.Exec(ctx,
		`insert into rt.sessions(token, user_id, expires_at) values($1,$2,$3)`,
		token, u.ID, time.Now().Add(24*time.Hour))
	if err != nil {
		return "", User{}, err
	}
	return token, u, nil
}

func (a *App) UserByToken(ctx context.Context, token string) (User, error) {
	var u User
	err := a.DB.Pool.QueryRow(ctx, `select u.id, u.username, u.display_name, u.role
		from rt.sessions s join rt.users u on u.id=s.user_id
		where s.token=$1 and s.expires_at > now()`, token).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUnauthorized
		}
		return User{}, err
	}
	return u, nil
}

func (a *App) Logout(ctx context.Context, token string) error {
	_, err := a.DB.Pool.Exec(ctx, `delete from rt.sessions where token=$1`, token)
	return err
}

// RequireRole returns nil when the user holds one of the allowed roles.
func RequireRole(u User, roles ...string) error {
	for _, r := range roles {
		if u.Role == r || u.Role == "admin" {
			return nil
		}
	}
	return ErrForbidden
}

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}
