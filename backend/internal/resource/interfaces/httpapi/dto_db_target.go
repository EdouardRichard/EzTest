package httpapi

import (
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// dbTargetDTO is the public DBTargetView of spec §7.8. It never carries the
// password: credentialSet only reports whether one is stored. Every id crosses
// the API boundary as a decimal string (D29).
type dbTargetDTO struct {
	ID                 string     `json:"id"`
	EnvironmentID      string     `json:"environmentId"`
	Name               string     `json:"name"`
	Host               string     `json:"host"`
	Port               int        `json:"port"`
	Database           string     `json:"database"`
	Username           string     `json:"username"`
	CredentialSet      bool       `json:"credentialSet"`
	SSLMode            string     `json:"sslmode"`
	ConnectTimeoutMS   int        `json:"connectTimeoutMs"`
	StatementTimeoutMS int        `json:"statementTimeoutMs"`
	MaxRows            int        `json:"maxRows"`
	Enabled            bool       `json:"enabled"`
	LastTestedAt       *time.Time `json:"lastTestedAt"`
	LastTestStatus     string     `json:"lastTestStatus"`
	LastTestMessage    string     `json:"lastTestMessage"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// newDBTargetDTO maps a stored target to its public shape. The password field is
// never copied.
func newDBTargetDTO(target domain.DBTarget) dbTargetDTO {
	return dbTargetDTO{
		ID:                 strconv.FormatInt(target.ID, 10),
		EnvironmentID:      strconv.FormatInt(target.EnvironmentID, 10),
		Name:               target.Name,
		Host:               target.Host,
		Port:               target.Port,
		Database:           target.DatabaseName,
		Username:           target.Username,
		CredentialSet:      target.Password != "",
		SSLMode:            target.SSLMode,
		ConnectTimeoutMS:   target.ConnectTimeoutMS,
		StatementTimeoutMS: target.StatementTimeoutMS,
		MaxRows:            target.MaxRows,
		Enabled:            target.Enabled,
		LastTestedAt:       target.LastTestedAt,
		LastTestStatus:     target.LastTestStatus,
		LastTestMessage:    target.LastTestMessage,
		CreatedAt:          target.CreatedAt,
		UpdatedAt:          target.UpdatedAt,
	}
}

// dbTargetTestErrorDTO is the classified connection-test failure.
type dbTargetTestErrorDTO struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// dbTargetTestResultDTO is the POST .../db-target/test response of spec §7.8:
// a failed probe is still a 200 with ok=false and a classified error.
type dbTargetTestResultDTO struct {
	OK                  bool                  `json:"ok"`
	LatencyMS           int64                 `json:"latencyMs"`
	ServerVersion       string                `json:"serverVersion,omitempty"`
	CurrentUser         string                `json:"currentUser,omitempty"`
	TransactionReadOnly bool                  `json:"transactionReadOnly"`
	Warnings            []string              `json:"warnings"`
	Error               *dbTargetTestErrorDTO `json:"error,omitempty"`
}

// newDBTargetTestResultDTO maps a probe result to its public shape.
func newDBTargetTestResultDTO(result domain.DBTargetTestResult) dbTargetTestResultDTO {
	warnings := result.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	dto := dbTargetTestResultDTO{
		OK:                  result.OK,
		LatencyMS:           result.LatencyMS,
		ServerVersion:       result.ServerVersion,
		CurrentUser:         result.CurrentUser,
		TransactionReadOnly: result.TransactionReadOnly,
		Warnings:            warnings,
	}
	if result.Error != nil {
		dto.Error = &dbTargetTestErrorDTO{Reason: result.Error.Reason, Message: result.Error.Message}
	}
	return dto
}

// putDBTargetRequest is the PUT and POST .../test body of spec §7.8. All fields
// are pointers so the test endpoint can distinguish an omitted body from an
// explicit form override; the four required connection fields are enforced by
// toCommand.
type putDBTargetRequest struct {
	Name               *string `json:"name"`
	Host               *string `json:"host"`
	Port               *int    `json:"port"`
	Database           *string `json:"database"`
	Username           *string `json:"username"`
	Password           *string `json:"password"`
	SSLMode            *string `json:"sslmode"`
	ConnectTimeoutMS   *int    `json:"connectTimeoutMs"`
	StatementTimeoutMS *int    `json:"statementTimeoutMs"`
	MaxRows            *int    `json:"maxRows"`
	Enabled            *bool   `json:"enabled"`
}

// isEmpty reports whether every field was omitted (an empty JSON object).
func (req putDBTargetRequest) isEmpty() bool {
	return req.Name == nil && req.Host == nil && req.Port == nil && req.Database == nil &&
		req.Username == nil && req.Password == nil && req.SSLMode == nil &&
		req.ConnectTimeoutMS == nil && req.StatementTimeoutMS == nil &&
		req.MaxRows == nil && req.Enabled == nil
}

// toCommand validates the required connection fields and maps the request onto
// the application command.
func (req putDBTargetRequest) toCommand() (application.UpsertDBTargetCommand, error) {
	required := []struct {
		field string
		set   bool
	}{
		{"host", req.Host != nil},
		{"port", req.Port != nil},
		{"database", req.Database != nil},
		{"username", req.Username != nil},
	}
	for _, field := range required {
		if !field.set {
			return application.UpsertDBTargetCommand{}, domain.NewInvalid(field.field, domain.ReasonDBTargetInvalid,
				field.field+" is required")
		}
	}
	cmd := application.UpsertDBTargetCommand{
		Host:               *req.Host,
		Port:               *req.Port,
		Database:           *req.Database,
		Username:           *req.Username,
		Password:           req.Password,
		SSLMode:            req.SSLMode,
		ConnectTimeoutMS:   req.ConnectTimeoutMS,
		StatementTimeoutMS: req.StatementTimeoutMS,
		MaxRows:            req.MaxRows,
		Enabled:            req.Enabled,
	}
	if req.Name != nil {
		cmd.Name = *req.Name
	}
	return cmd, nil
}
