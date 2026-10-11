package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
	"github.com/davidsugianto/upsera/internal/secret"
)

type adminUserBody struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	IsAdmin   bool      `json:"is_admin"`
	CreatedAt time.Time `json:"created_at"`
}

func toAdminUserBody(u model.User) adminUserBody {
	return adminUserBody{ID: u.ID, Email: u.Email, Name: u.Name, IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt}
}

type listAdminUsersOutput struct {
	Body struct {
		Users []adminUserBody `json:"users"`
	}
}

type createAdminUserInput struct {
	Body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
}

type createAdminUserOutput struct {
	Body adminUserBody
}

// smtpBody is the instance mail server as the API shows it. The password
// is never returned: smtpPasswordMask stands in when one is stored.
type smtpBody struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	TLS      string `json:"tls"`
}

// smtpPasswordMask replaces a stored SMTP password in responses; sending it
// back on update keeps the stored one.
const smtpPasswordMask = notify.Redacted

const smtpDecryptError = "cannot decrypt SMTP settings (APP_SECRET changed?)"

type settingsBody struct {
	BlockPrivateTargets bool      `json:"block_private_targets"`
	RetentionDays       *int      `json:"retention_days"`
	SMTP                *smtpBody `json:"smtp"`
	SMTPError           string    `json:"smtp_error,omitempty"`
}

type getSettingsOutput struct {
	Body settingsBody
}

type updateSettingsInput struct {
	Body struct {
		BlockPrivateTargets bool `json:"block_private_targets"`
		RetentionDays       *int `json:"retention_days,omitempty" minimum:"3"`
		// SMTP: omitted leaves the mail server unchanged, null clears it, an
		// object sets it (see smtpPutBody).
		SMTP json.RawMessage `json:"smtp,omitempty" doc:"Instance SMTP settings; omit to keep, null to clear" nullable:"true"`
	}
}

// smtpPutBody is the object form of the PUT smtp field. A nil Password or
// the mask keeps the stored password; "" means none.
type smtpPutBody struct {
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username string  `json:"username"`
	Password *string `json:"password"`
	From     string  `json:"from"`
	TLS      string  `json:"tls"`
}

type smtpTestInput struct {
	Body struct {
		To string `json:"to"`
	}
}

type smtpTestOutput struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

// smtpAudit is what the audit log records about the mail server: never the
// username or password.
type smtpAudit struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	From string `json:"from"`
}

// settingsAudit is the settings.update audit payload.
type settingsAudit struct {
	model.InstanceSettings
	SMTPChanged bool       `json:"smtp_changed"`
	SMTP        *smtpAudit `json:"smtp,omitempty"`
}

// loadSettingsBody builds the settings response, reading the SMTP settings.
func loadSettingsBody(ctx context.Context, d Deps, st model.InstanceSettings) (settingsBody, error) {
	body := settingsBody{BlockPrivateTargets: st.BlockPrivateTargets, RetentionDays: st.RetentionDays}
	cfg, err := d.Store.GetSMTPSettings(ctx)
	switch {
	case errors.Is(err, secret.ErrCorrupt), errors.Is(err, secret.ErrUnknownVersion):
		body.SMTPError = smtpDecryptError
	case err != nil:
		return body, mapStoreErr(d, err)
	case cfg != nil:
		body.SMTP = &smtpBody{
			Host: cfg.Host, Port: cfg.Port, Username: cfg.Username, From: cfg.From, TLS: cfg.TLS,
		}
		if cfg.Password != "" {
			body.SMTP.Password = smtpPasswordMask
		}
	}
	return body, nil
}

// parseSMTPUpdate resolves the smtp field of a settings update. set is
// false when the field is omitted; cfg is nil when it must be cleared.
func parseSMTPUpdate(ctx context.Context, d Deps, raw json.RawMessage) (cfg *model.SMTPSettings, set bool, err error) {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0:
		return nil, false, nil
	case string(raw) == "null":
		return nil, true, nil
	}
	var in smtpPutBody
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, false, huma.Error422UnprocessableEntity("smtp: must be an object with host, port, username, password, from and tls")
	}
	c := model.SMTPSettings{
		Host: strings.TrimSpace(in.Host), Port: in.Port, Username: in.Username,
		From: strings.TrimSpace(in.From), TLS: in.TLS,
	}
	if err := notify.ValidateSMTPSettings(c); err != nil {
		return nil, false, huma.Error422UnprocessableEntity("smtp: " + err.Error())
	}
	if c.TLS == "none" && c.Username != "" {
		return nil, false, huma.Error422UnprocessableEntity("smtp: authentication requires tls or starttls")
	}
	switch {
	case in.Password == nil || *in.Password == smtpPasswordMask:
		if c.Username != "" {
			old, err := d.Store.GetSMTPSettings(ctx)
			switch {
			case errors.Is(err, secret.ErrCorrupt), errors.Is(err, secret.ErrUnknownVersion):
				return nil, false, huma.Error409Conflict("stored SMTP password cannot be decrypted; enter the password again")
			case err != nil:
				return nil, false, mapStoreErr(d, err)
			case old != nil:
				c.Password = old.Password
			}
		}
	default:
		c.Password = *in.Password
	}
	return &c, true, nil
}

// sanitizeSendError makes a send error safe to show: one line, bounded, and
// without the password should a server echo it back.
func sanitizeSendError(err error, password string) string {
	msg := err.Error()
	if password != "" {
		msg = strings.ReplaceAll(msg, password, "********")
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return msg
}

func registerAdminRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/admin/users", func(ctx context.Context, _ *noInput) (*listAdminUsersOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		users, err := d.Store.ListUsers(ctx)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listAdminUsersOutput{}
		out.Body.Users = make([]adminUserBody, len(users))
		for i, u := range users {
			out.Body.Users[i] = toAdminUserBody(u)
		}
		return out, nil
	})

	huma.Post(api, "/api/admin/users", func(ctx context.Context, in *createAdminUserInput) (*createAdminUserOutput, error) {
		admin, err := requireAdmin(ctx)
		if err != nil {
			return nil, err
		}
		email := strings.TrimSpace(in.Body.Email)
		if email == "" {
			return nil, huma.Error422UnprocessableEntity("email is required")
		}
		if err := validatePassword(in.Body.Password); err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		hash, err := hashPassword(in.Body.Password)
		if err != nil {
			d.Logger.Error("hash password", "error", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		u, err := d.Store.CreateUser(ctx, email, in.Body.Name, hash, in.Body.IsAdmin)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, nil, actor{userID: &admin.ID}, "admin.user.create", "user", &u.ID,
			map[string]any{"email": u.Email, "is_admin": u.IsAdmin})
		return &createAdminUserOutput{Body: toAdminUserBody(u)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Get(api, "/api/admin/settings", func(ctx context.Context, _ *noInput) (*getSettingsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		st, err := d.Store.GetInstanceSettings(ctx)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		body, err := loadSettingsBody(ctx, d, st)
		if err != nil {
			return nil, err
		}
		return &getSettingsOutput{Body: body}, nil
	})

	huma.Put(api, "/api/admin/settings", func(ctx context.Context, in *updateSettingsInput) (*getSettingsOutput, error) {
		admin, err := requireAdmin(ctx)
		if err != nil {
			return nil, err
		}
		if in.Body.RetentionDays != nil && *in.Body.RetentionDays < 3 {
			return nil, huma.Error422UnprocessableEntity("retention_days must be >= 3")
		}
		// Resolve the smtp field before writing anything, so a bad one
		// leaves all settings untouched.
		smtpCfg, smtpSet, err := parseSMTPUpdate(ctx, d, in.Body.SMTP)
		if err != nil {
			return nil, err
		}
		st, err := d.Store.UpdateInstanceSettings(ctx, model.InstanceSettings{
			BlockPrivateTargets: in.Body.BlockPrivateTargets,
			RetentionDays:       in.Body.RetentionDays,
		})
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Policy.SetBlockPrivate(st.BlockPrivateTargets)
		audit := settingsAudit{InstanceSettings: st, SMTPChanged: smtpSet}
		if smtpSet {
			if err := d.Store.SetSMTPSettings(ctx, smtpCfg); err != nil {
				return nil, mapStoreErr(d, err)
			}
			if smtpCfg != nil {
				audit.SMTP = &smtpAudit{Host: smtpCfg.Host, Port: smtpCfg.Port, From: smtpCfg.From}
			}
		}
		writeAudit(ctx, d, nil, actor{userID: &admin.ID}, "settings.update", "instance_settings", nil, audit)
		body, err := loadSettingsBody(ctx, d, st)
		if err != nil {
			return nil, err
		}
		return &getSettingsOutput{Body: body}, nil
	})

	huma.Post(api, "/api/admin/settings/smtp/test", func(ctx context.Context, in *smtpTestInput) (*smtpTestOutput, error) {
		admin, err := requireAdmin(ctx)
		if err != nil {
			return nil, err
		}
		to := strings.TrimSpace(in.Body.To)
		addr, perr := mail.ParseAddress(to)
		if perr != nil || strings.ContainsAny(to, "\r\n") {
			return nil, huma.Error422UnprocessableEntity("to: invalid email address")
		}
		cfg, err := d.Store.GetSMTPSettings(ctx)
		switch {
		case errors.Is(err, secret.ErrCorrupt), errors.Is(err, secret.ErrUnknownVersion):
			return nil, huma.Error409Conflict(smtpDecryptError)
		case err != nil:
			return nil, mapStoreErr(d, err)
		case cfg == nil:
			return nil, huma.Error409Conflict("SMTP is not configured")
		}
		writeAudit(ctx, d, nil, actor{userID: &admin.ID}, "settings.smtp_test", "instance_settings", nil,
			map[string]any{"to": addr.Address})
		err = notify.SendMail(ctx, d.Policy, *cfg, []string{addr.Address}, "Upsera SMTP test",
			"This is a test message from Upsera.\r\nYour SMTP settings work.")
		if err != nil {
			return nil, huma.NewError(http.StatusBadGateway, "send failed: "+sanitizeSendError(err, cfg.Password))
		}
		out := &smtpTestOutput{}
		out.Body.OK = true
		return out, nil
	})
}
