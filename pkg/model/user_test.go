package model

import "testing"

// ActorName is the single place that decides how a person is named anywhere
// in a workflow — audit history, bot notifications, the console's ticket
// detail and flowchart. These cases pin the format down: the KubeForecast account
// handle alone (never paired with the Telegram identity), the account's
// nickname is never the primary identity, and the Telegram identity is only
// ever shown as a fallback when there is no linked KubeForecast account at all.
func TestActorName(t *testing.T) {
	tests := []struct {
		name    string
		user    *User
		binding *TelegramBinding
		want    string
	}{
		{
			name:    "account handle alone, even with a linked Telegram username",
			user:    &User{Username: "zhaosi", NickName: "赵四"},
			binding: &TelegramBinding{TelegramUsername: "跑路了"},
			want:    "zhaosi",
		},
		{
			name: "account handle alone when nothing is linked",
			user: &User{Username: "admin", NickName: "Admin Test User"},
			want: "admin",
		},
		{
			name:    "account handle alone even when the account has no @username-derived Telegram identity",
			user:    &User{Username: "test"},
			binding: &TelegramBinding{FirstName: "打杂", LastName: "小弟"},
			want:    "test",
		},
		{
			name:    "binding taken from the user when not passed explicitly, but still username-only",
			user:    &User{Username: "admin", TelegramBinding: &TelegramBinding{TelegramUsername: "sasuke11223"}},
			binding: nil,
			want:    "admin",
		},
		{
			name:    "user recovered from the binding when only the binding is known, still username-only",
			user:    nil,
			binding: &TelegramBinding{TelegramUsername: "sasuke11223", User: &User{Username: "admin"}},
			want:    "admin",
		},
		{
			name: "nickname only as a last resort for an account with no username",
			user: &User{NickName: "内部昵称"},
			want: "内部昵称",
		},
		{
			name:    "Telegram identity alone when there is no KubeForecast account at all",
			user:    nil,
			binding: &TelegramBinding{TelegramUsername: "stranger"},
			want:    "stranger",
		},
		{
			name: "empty when nothing is known",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActorName(tt.user, tt.binding); got != tt.want {
				t.Fatalf("ActorName() = %q, want %q", got, tt.want)
			}
		})
	}
}
