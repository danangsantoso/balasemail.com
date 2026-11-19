package auth

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/danangsantoso/balasemail.com/internal/emailaddr"
	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
	"github.com/danangsantoso/balasemail.com/internal/platform/database"
	"github.com/danangsantoso/balasemail.com/internal/platform/httpx"
	"github.com/danangsantoso/balasemail.com/internal/platform/security"
)

// Reset links are valid for an hour and a mailbox gets at most a few per hour.
const (
	ResetTTL          = time.Hour
	resetsPerHour     = 3
	minPasswordLength = 10
)

// Mail is a message the auth service asks the platform to send.
type Mail struct {
	WorkspaceID, To, Subject, HTML, Text string
}

// ResetMailer sends password reset emails. BaseURL is where links point to.
type ResetMailer struct {
	Send    func(ctx context.Context, m Mail) error
	BaseURL string
}

// RequestPasswordReset emails a one-time reset link when the address belongs
// to an active user. It never tells the caller whether the account exists.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	norm, err := emailaddr.Normalize(email)
	if err != nil {
		return apperr.Invalid("invalid_email", "email address is not valid")
	}
	if s.reset.Send == nil {
		return apperr.Internal(fmt.Errorf("password reset mailer is not configured"))
	}
	var userID, wsID, name string
	var recent int
	err = s.db.QueryRow(ctx, `
		SELECT u.id, u.workspace_id, u.full_name,
		       (SELECT count(*) FROM password_resets r WHERE r.user_id=u.id AND r.created_at > now() - interval '1 hour')
		FROM users u WHERE u.email=$1 AND u.disabled_at IS NULL`, norm).Scan(&userID, &wsID, &name, &recent)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return apperr.Internal(err)
	}
	if recent >= resetsPerHour {
		return nil // silently: the earlier links are still valid
	}
	token := security.RandomHex(32)
	if _, err := s.db.Exec(ctx, `INSERT INTO password_resets (user_id, token_hash, expires_at) VALUES ($1,$2,$3)`,
		userID, security.SHA256(token), time.Now().Add(ResetTTL)); err != nil {
		return apperr.Internal(err)
	}
	// The token travels in the URL fragment, so it never reaches server logs or Referer headers.
	link := s.reset.BaseURL + "/app#/reset?token=" + token
	if err := s.reset.Send(ctx, resetMail(wsID, norm, name, link)); err != nil {
		slog.Error("queue password reset email", "err", err)
		return apperr.Internal(err)
	}
	return nil
}

func resetMail(wsID, to, name, link string) Mail {
	greeting := "Halo,"
	if name != "" {
		greeting = "Halo " + name + ","
	}
	minutes := int(ResetTTL.Minutes())
	text := fmt.Sprintf("%s\n\nKami menerima permintaan untuk mengatur ulang password akun BalasEmail Anda.\n"+
		"Buka tautan berikut untuk membuat password baru (berlaku %d menit, hanya bisa dipakai sekali):\n\n%s\n\n"+
		"Jika Anda tidak meminta ini, abaikan email ini. Password Anda tidak berubah.\n\n— BalasEmail", greeting, minutes, link)
	body := fmt.Sprintf(`<div style="font-family:Arial,sans-serif;font-size:15px;line-height:1.6;color:#161a2e;max-width:520px">
<p>%s</p>
<p>Kami menerima permintaan untuk mengatur ulang password akun BalasEmail Anda.</p>
<p><a href="%s" style="display:inline-block;padding:11px 20px;border-radius:10px;background:#4F46E5;color:#ffffff;text-decoration:none;font-weight:bold">Buat password baru</a></p>
<p style="color:#646a85;font-size:13px">Tautan berlaku %d menit dan hanya bisa dipakai sekali. Jika tombol tidak berfungsi, salin alamat ini ke browser:<br>%s</p>
<p style="color:#646a85;font-size:13px">Jika Anda tidak meminta ini, abaikan email ini. Password Anda tidak berubah.</p>
<p>— BalasEmail</p></div>`, html.EscapeString(greeting), html.EscapeString(link), minutes, html.EscapeString(link))
	return Mail{WorkspaceID: wsID, To: to, Subject: "Atur ulang password BalasEmail", HTML: body, Text: text}
}

// ResetPassword sets a new password with a reset token. The token and every
// other outstanding token of the user are used up, and older sessions end.
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	if len(password) < minPasswordLength {
		return apperr.Invalid("weak_password", "password must be at least 10 characters")
	}
	bad := apperr.Invalid("invalid_reset_token", "this reset link is invalid or has expired; request a new one")
	if len(token) != 64 {
		return bad
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return apperr.Internal(err)
	}
	err = database.InTx(ctx, s.db, func(tx pgx.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `SELECT user_id FROM password_resets
			WHERE token_hash=$1 AND used_at IS NULL AND expires_at > now() FOR UPDATE`, security.SHA256(token)).Scan(&userID)
		if database.IsNoRows(err) {
			return bad
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now() WHERE id=$1`, userID, hash); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, userID)
		return err
	})
	if err != nil {
		return apperr.As(err) // the invalid-token error passes through, anything else is internal
	}
	return nil
}

func (s *Service) resetRoutes(mux *http.ServeMux) {
	// Separate budgets: asking for links is cheap to abuse, using one is not.
	mux.Handle("POST /v1/auth/forgot-password", httpx.RateLimitByIP(5, 5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email string `json:"email"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if err := s.RequestPasswordReset(r.Context(), in.Email); err != nil {
			httpx.Error(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusAccepted, map[string]string{
			"message": "if an account exists for this email, a reset link has been sent",
		})
	})))
	mux.Handle("POST /v1/auth/reset-password", httpx.RateLimitByIP(10, 10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if err := s.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
