package identityapi

import (
	"strconv"
	"time"

	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// userDTO is the public user representation. PasswordHash is deliberately
// absent; token digests never appear in any DTO either.
type userDTO struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toUserDTO(user identity.User) userDTO {
	return userDTO{
		ID:          strconv.FormatInt(user.ID, 10),
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        string(user.Role),
		Status:      string(user.Status),
		CreatedAt:   user.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   user.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// sessionDTO is the login response: the raw token is returned exactly once.
type sessionDTO struct {
	Token     string  `json:"token"`
	ExpiresAt string  `json:"expires_at"`
	User      userDTO `json:"user"`
}

func toSessionDTO(session identityapp.Session) sessionDTO {
	return sessionDTO{
		Token:     session.Token,
		ExpiresAt: session.ExpiresAt.UTC().Format(time.RFC3339),
		User:      toUserDTO(session.User),
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type createUserRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

type updateUserRequest struct {
	DisplayName *string `json:"display_name"`
	Status      *string `json:"status"`
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}
