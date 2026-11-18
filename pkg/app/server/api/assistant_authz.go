package api

import (
	"context"

	"github.com/cylonchau/kube-res-foresight/pkg/app/utils"
	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/gin-gonic/gin"
)

// AssistantAuthorizer answers the assistant whether the user of a question may call a route of the platform: with the same policies the
// middleware of the platform checks a request with (Casbin by the user name and by the role codes, the administrator through everywhere, and for a
// developer credential the permissions it was given). The assistant offers a user only the tools whose routes the user may call, so asking the
// assistant is never a way around a permission.
type AssistantAuthorizer struct {
	Permissions *rdb.PermissionDAO
}

// Allows implements assistant.Authorizer. It fails closed: an unknown state of the permission system denies.
func (a AssistantAuthorizer) Allows(ctx context.Context, p assistant.Principal, method, path string) bool {
	if p.Admin {
		return true
	}
	if p.Developer {
		if a.Permissions == nil {
			return false
		}
		allowed, err := a.Permissions.AllowsDeveloperCredential(ctx, p.PermissionIDs, method, path)
		return err == nil && allowed
	}
	if utils.Enforcer == nil {
		return false
	}
	if ok, err := utils.Enforcer.Enforce(p.Name, path, method); err == nil && ok {
		return true
	}
	for _, role := range p.Roles {
		if ok, err := utils.Enforcer.Enforce(role, path, method); err == nil && ok {
			return true
		}
	}
	return false
}

// principalOfRequest is who asks, as the middleware of the platform left it in the request.
func principalOfRequest(c *gin.Context) (assistant.Principal, bool) {
	rawName, ok := c.Get("username")
	name, isString := rawName.(string)
	if !ok || !isString || name == "" {
		return assistant.Principal{}, false
	}
	p := assistant.Principal{Name: name}
	if roles, ok := c.Get("roles"); ok {
		if list, isList := roles.([]string); isList {
			p.Roles = list
			for _, role := range list {
				if role == "ADMIN" {
					p.Admin = true
				}
			}
		}
	}
	if dev, _ := c.Get("developerCredential"); dev == true {
		p.Developer, p.Admin = true, false
		if ids, ok := c.Get("developerPermissionIDs"); ok {
			p.PermissionIDs, _ = ids.([]uint)
		}
	}
	return p, true
}
