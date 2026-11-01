package model

// secretConfigKeys are the system parameters that hold a credential: the settings list shows a mask for them, a change keeps the stored
// value when the mask is sent back, the value is stored encrypted, and the operation log never keeps what was sent.
var secretConfigKeys = map[string]bool{
	"ldap.bind_password":    true,
	"ldap.ca_cert":          true,
	"notify.email.password": true,
}

// IsSecretConfig reports whether the system parameter holds a credential.
func IsSecretConfig(key string) bool { return secretConfigKeys[key] }
