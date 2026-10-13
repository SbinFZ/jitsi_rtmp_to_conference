// NodeMedia-Server PLAY signatures: sign=<expiry>-MD5("/app/stream-<expiry>-<key>").

package input

import (
	"crypto/md5" // required by the NodeMedia protocol, not used for security
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// SignNodeMedia generates a fresh PLAY signature. FLV extension is excluded.
func SignNodeMedia(rawURL, key string, expiry time.Time) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.Path == "" {
		return "", fmt.Errorf("invalid NodeMedia URL")
	}
	path := strings.TrimSuffix(u.Path, ".flv")
	exp := fmt.Sprint(expiry.Unix())
	sum := md5.Sum([]byte(path + "-" + exp + "-" + key))
	q := u.Query()
	q.Set("sign", exp+"-"+hex.EncodeToString(sum[:]))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

var signatureParam = regexp.MustCompile(`(?i)[?&](sign|sig|token|auth_key|auth|expires|signature)=`)

// HasSignature reports a URL that carries a (possibly one-time) credential.
func HasSignature(raw string) bool { return signatureParam.MatchString(raw) }
