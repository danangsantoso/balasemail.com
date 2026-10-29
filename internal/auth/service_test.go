package auth

import (
	"testing"

	"github.com/danangsantoso/balasemail.com/internal/platform/apperr"
)

func TestValidateRegistration(t *testing.T) {
	base := RegisterInput{CompanyName: "PT Maju", FullName: "Budi", Email: "budi@maju.co.id", Password: "password-kuat-123"}
	if _, err := ValidateRegistration(base); err != nil {
		t.Fatalf("business email should pass: %v", err)
	}

	cases := map[string]func(*RegisterInput){
		"business_email_required": func(in *RegisterInput) { in.Email = "budi@gmail.com" },
		"disposable_email":        func(in *RegisterInput) { in.Email = "budi@mailinator.com" },
		"invalid_email":           func(in *RegisterInput) { in.Email = "budi" },
		"weak_password":           func(in *RegisterInput) { in.Password = "short" },
		"missing_fields":          func(in *RegisterInput) { in.CompanyName = " " },
	}
	for code, mutate := range cases {
		in := base
		mutate(&in)
		_, err := ValidateRegistration(in)
		if !apperr.Is(err, apperr.KindInvalid) || apperr.As(err).Code != code {
			t.Errorf("%s: got %v", code, err)
		}
	}
}
