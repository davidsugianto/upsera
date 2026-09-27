package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
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

type settingsBody struct {
	BlockPrivateTargets bool `json:"block_private_targets"`
	RetentionDays       *int `json:"retention_days"`
}

type getSettingsOutput struct {
	Body settingsBody
}

type updateSettingsInput struct {
	Body struct {
		BlockPrivateTargets bool `json:"block_private_targets"`
		RetentionDays       *int `json:"retention_days,omitempty" minimum:"3"`
	}
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
		return &getSettingsOutput{Body: settingsBody{BlockPrivateTargets: st.BlockPrivateTargets, RetentionDays: st.RetentionDays}}, nil
	})

	huma.Put(api, "/api/admin/settings", func(ctx context.Context, in *updateSettingsInput) (*getSettingsOutput, error) {
		admin, err := requireAdmin(ctx)
		if err != nil {
			return nil, err
		}
		if in.Body.RetentionDays != nil && *in.Body.RetentionDays < 3 {
			return nil, huma.Error422UnprocessableEntity("retention_days must be >= 3")
		}
		st, err := d.Store.UpdateInstanceSettings(ctx, model.InstanceSettings{
			BlockPrivateTargets: in.Body.BlockPrivateTargets,
			RetentionDays:       in.Body.RetentionDays,
		})
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Policy.SetBlockPrivate(st.BlockPrivateTargets)
		writeAudit(ctx, d, nil, actor{userID: &admin.ID}, "settings.update", "instance_settings", nil, st)
		return &getSettingsOutput{Body: settingsBody{BlockPrivateTargets: st.BlockPrivateTargets, RetentionDays: st.RetentionDays}}, nil
	})
}
