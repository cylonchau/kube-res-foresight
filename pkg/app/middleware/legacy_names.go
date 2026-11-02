package middleware

import (
	"net/http"
	"strings"
)

// The project was named kubeforecast, then kube-resource-fcst, and is now kube-res-foresight. What is on the wire carries the name: the
// headers of a developer credential, the Authorization scheme, the query parameters, the WebSocket subprotocol and the client headers.
// A platform that is upgraded keeps running next to parts that are not (an operator, a Kyverno policy, a script, a page that is still open in
// a browser), so every earlier name is still understood; the current one is what this program sends. See docs/renaming.md.
var (
	// accessKeyHeaders and accessSecretHeaders are the headers of a developer credential, the current name first.
	accessKeyHeaders    = []string{"X-kube-res-foresight-AccessKey", "X-kube-resource-fcst-AccessKey", "X-KubeForecast-AccessKey"}
	accessSecretHeaders = []string{"X-kube-res-foresight-AccessSecret", "X-kube-resource-fcst-AccessSecret", "X-KubeForecast-AccessSecret"}
	// authorizationSchemes precede "<key>:<secret>" in the Authorization header.
	authorizationSchemes = []string{"kube-res-foresight-AK ", "kube-resource-fcst-AK ", "KubeForecast-AK "}
	// accessKeyParams and accessSecretParams are the query parameters (for the places that cannot set a header).
	accessKeyParams    = []string{"__kube_res_foresight_ak", "__kube_resource_fcst_ak", "__kubeforecast_ak"}
	accessSecretParams = []string{"__kube_res_foresight_sk", "__kube_resource_fcst_sk", "__kubeforecast_sk"}
	// clientHeaders and clientNameHeaders say what kind of client a developer credential call comes from, and which one.
	clientHeaders     = []string{"X-kube-res-foresight-Client", "X-kube-resource-fcst-Client", "X-KubeForecast-Client"}
	clientNameHeaders = []string{"X-kube-res-foresight-Client-Name", "X-kube-resource-fcst-Client-Name", "X-KubeForecast-Client-Name"}
)

// WebSocketAuthSubprotocols are the subprotocols that precede the access token on a WebSocket handshake, the current one first. The
// server offers all of them: it answers with the one the client offered.
var WebSocketAuthSubprotocols = []string{WebSocketAuthSubprotocol, "kube-resource-fcst.jwt", "kubeforecast.jwt"}

// firstHeader is the first of the headers that is set, trimmed.
func firstHeader(r *http.Request, names []string) string {
	for _, name := range names {
		if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// firstParam is the first of the query parameters that is set, trimmed.
func firstParam(r *http.Request, names []string) string {
	query := r.URL.Query()
	for _, name := range names {
		if v := strings.TrimSpace(query.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// cutScheme returns what follows the scheme of an Authorization header, and whether the header has one of the schemes (any case).
func cutScheme(authorization string) (string, bool) {
	for _, scheme := range authorizationSchemes {
		if len(authorization) >= len(scheme) && strings.EqualFold(authorization[:len(scheme)], scheme) {
			return authorization[len(scheme):], true
		}
	}
	return "", false
}

// isWebSocketAuthSubprotocol says whether the entry is one of the subprotocols that precede the token.
func isWebSocketAuthSubprotocol(item string) bool {
	for _, p := range WebSocketAuthSubprotocols {
		if item == p {
			return true
		}
	}
	return false
}
