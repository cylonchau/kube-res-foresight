package migration

import "github.com/cylonchau/kube-res-foresight/pkg/model"

// The buttons of the user, role and operation log pages. A page shows a row or header button only when the menu has a BUTTON permission of that code
// under it (the pages read them from the route of the user), so a database without these rows had a role list with no edit button and a user list
// with no way to edit, reset the one-time password or delete. They are seeded now, under their menus, with the routes each needs, so a role that is
// given the button can use it. The administrator role gets every permission, these included.
func pageButtons() []permissionSeed {
	button := func(menu, code, name, position, btnType string, apis ...string) permissionSeed {
		return permissionSeed{parentCode: menu, permission: model.Permission{Name: name, Code: code, Type: "BUTTON", Position: position, BtnType: btnType,
			Status: true, BeApis: apiList(apis)}}
	}
	return []permissionSeed{
		button("authorization_user", "authorization:user:add", "Add User", "header", "primary", "POST /api/v1/users", "GET /api/v1/roles/all"),
		button("authorization_user", "authorization:user:batchDelete", "Delete Users", "header", "error", "POST /api/v1/users/batch-delete"),
		button("authorization_user", "authorization:user:edit", "Edit User", "row", "primary", "PUT /api/v1/users/:id", "GET /api/v1/roles/all"),
		button("authorization_user", "authorization:user:resetOtp", "Reset One-time Password", "row", "warning", "POST /api/v1/users/otp/reset/:id"),
		button("authorization_user", "authorization:user:delete", "Delete User", "row", "error", "DELETE /api/v1/users/:id"),
		button("authorization_role", "authorization:role:add", "Add Role", "header", "primary", "POST /api/v1/roles"),
		button("authorization_role", "authorization:role:batchDelete", "Delete Roles", "header", "error", "POST /api/v1/roles/batch-delete"),
		button("authorization_role", "authorization:role:edit", "Edit Role and Permissions", "row", "primary", "PUT /api/v1/roles/:id", "GET /api/v1/permissions"),
		button("authorization_role", "authorization:role:delete", "Delete Role", "row", "error", "DELETE /api/v1/roles/:id"),
		button("system_operation-log", "system:operation-log:clear", "Clear Operation Logs", "header", "error", "DELETE /api/v1/operation-logs"),
	}
}
