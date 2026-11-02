package utils

import (
	"bytes"
	"encoding/base64"
	"github.com/pquerna/otp/totp"
	"image/png"
)

// GenerateOTPSecret generates a new TOTP secret and QR Code Data URI
func GenerateOTPSecret(accountName string) (secret string, qrCodeDataURI string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "kube-res-foresight",
		AccountName: accountName,
	})
	if err != nil {
		return "", "", err
	}

	// Generate QR Code Image
	var buf bytes.Buffer
	img, err := key.Image(200, 200)
	if err != nil {
		return key.Secret(), "", err
	}
	err = png.Encode(&buf, img)
	if err != nil {
		return key.Secret(), "", err
	}

	// Convert to Data URI
	qrCodeDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	return key.Secret(), qrCodeDataURI, nil
}

// VerifyOTP verifies a TOTP code against a secret
func VerifyOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}
