package utils

import "testing"

func TestGenerateRefreshTokenUsesStableHashAndUniqueValues(t *testing.T) {
	rawOne, hashOne, err := GenerateRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	rawTwo, hashTwo, err := GenerateRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if rawOne == rawTwo || hashOne == hashTwo {
		t.Fatal("expected independently generated refresh tokens")
	}
	if HashRefreshToken(rawOne) != hashOne {
		t.Fatal("stored refresh token hash does not match the raw token")
	}
	if len(rawOne) < 40 || len(hashOne) != 64 {
		t.Fatalf("unexpected token sizes: raw=%d hash=%d", len(rawOne), len(hashOne))
	}
}
