package timezone

import (
	"os"
	"testing"
)

func TestForesightTimezoneKeepsThePreviousSetting(t *testing.T) {
	t.Setenv(Env, "")
	if err := os.Unsetenv(Env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBE_RESOURCE_FCST_TIMEZONE", "Asia/Tokyo")
	if got := NameOf(); got != "Asia/Tokyo" {
		t.Fatalf("previous timezone lost: %q", got)
	}
	t.Setenv(Env, "UTC")
	if got := NameOf(); got != "UTC" {
		t.Fatalf("new timezone must win: %q", got)
	}
	t.Setenv(Env, "")
	if got := NameOf(); got != Name {
		t.Fatalf("explicit empty must select the default: %q", got)
	}
}
